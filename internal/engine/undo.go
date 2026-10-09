package engine

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"time"

	"tagshelf/internal/rules"
	"tagshelf/internal/store"
)

// TagCheck reports whether the file at path still holds the tags a save left
// it with: after is that save's recorded After. It returns nil when it does,
// and otherwise why not.
type TagCheck func(path, after string) error

// TagRestore undoes a tag save of the file at path, writing its recorded
// before back over its recorded after, and records the write in st under the
// run runID. The engine does not know what is written.
type TagRestore func(st *store.Store, runID int64, path, before, after string) error

// UndoSkip is something of a run that undo leaves as it is, and why.
type UndoSkip struct {
	Path   string
	Reason string
}

// UndoPlan is what undoing a run would do: nothing has been done yet.
type UndoPlan struct {
	Run store.Run
	// Refusal says why the run cannot be undone at all, "" when it can.
	Refusal string
	// How many moves, tag saves and cue sheet rewrites would be undone.
	Moves, TagSaves, Cues int
	// Skipped lists what would be left as it is.
	Skipped []UndoSkip
}

// Nothing reports whether the plan undoes nothing.
func (p UndoPlan) Nothing() bool {
	return p.Refusal != "" || p.Moves+p.TagSaves+p.Cues == 0
}

// undoOp is one thing a run did, to be undone: a move, among them a cue
// sheet's rewrite, or a tag save.
type undoOp struct {
	move  *store.Move
	write *store.TagWrite
	at    time.Time
	seq   int // its place among the run's moves, or among its tag saves
}

// undoer works out, step by step, what of a run can be undone. Planning
// keeps the steps it would take in an overlay over the file system, so that
// a later step sees what an earlier one would have done; undoing for real
// takes each step as it goes.
type undoer struct {
	st    *store.Store
	check TagCheck
	run   store.Run
	ops   []undoOp // latest first
	// later are the moves made since the run's last, by any run, and
	// laterFrom where in later each path was moved away from, by path key.
	later     []store.Move
	laterFrom map[string][]int
	failed    int // steps that could have been undone and were not

	moved map[string]string // where a path's file really is, by path key
	gone  map[string]bool   // paths a planned step has emptied
}

// newUndoer reads what the run did.
func newUndoer(st *store.Store, runID int64, check TagCheck) (*undoer, error) {
	run, err := st.Run(runID)
	if err != nil {
		return nil, err
	}
	u := &undoer{st: st, check: check, run: run, moved: map[string]string{}, gone: map[string]bool{}}
	moves, err := st.Moves(runID)
	if err != nil {
		return nil, err
	}
	writes, err := st.TagWrites(runID)
	if err != nil {
		return nil, err
	}
	var last int64
	for i := range moves {
		u.ops = append(u.ops, undoOp{move: &moves[i], at: moves[i].At, seq: i})
		last = max(last, moves[i].ID)
	}
	if len(moves) > 0 {
		if u.later, err = st.MovesAfter(last); err != nil {
			return nil, err
		}
		u.laterFrom = make(map[string][]int)
		for i, m := range u.later {
			key := rules.PathKey(m.From)
			u.laterFrom[key] = append(u.laterFrom[key], i)
		}
	}
	for i := range writes {
		u.ops = append(u.ops, undoOp{write: &writes[i], at: writes[i].At, seq: i})
	}
	// Undone in the reverse of the order they were done in.
	slices.SortStableFunc(u.ops, func(a, b undoOp) int { return -compareOps(a, b) })
	return u, nil
}

// compareOps orders two things a run did by when it did them. Clocks being
// coarse, a move and a tag save of one file may carry the same time: a save
// came first if the file was then moved away from the path it was saved at,
// and last if it was moved there.
func compareOps(a, b undoOp) int {
	if c := a.at.Compare(b.at); c != 0 {
		return c
	}
	switch {
	case (a.move != nil) == (b.move != nil):
		return cmp.Compare(a.seq, b.seq)
	case a.move != nil:
		return -compareOps(b, a)
	}
	// a is a tag save and b a move.
	if rules.PathKey(b.move.From) == rules.PathKey(a.write.Path) {
		return -1
	}
	return 1
}

// refusal says why the run cannot be undone at all, "" when it can.
func (u *undoer) refusal() string {
	switch {
	case u.run.State == store.StateRunning:
		return "it is still running"
	case u.run.State == store.StateUndone:
		return "it has been undone already"
	case u.run.DryRun:
		return "a dry run changed nothing"
	case len(u.ops) == 0:
		return "it moved no file and saved no tags"
	}
	return ""
}

// stillAt reports whether the file the run left at path is the one there
// now, as far as tagshelf's own later moves tell: it follows the file
// through them, so a file a later run moved away and its undo brought back
// is still the run's.
func (u *undoer) stillAt(path string) bool {
	key, at := rules.PathKey(path), -1
	loc := key
	for {
		next := -1
		for _, i := range u.laterFrom[loc] {
			if i > at {
				next = i
				break
			}
		}
		if next < 0 {
			return loc == key
		}
		at, loc = next, rules.PathKey(u.later[next].To)
	}
}

// where finds the file that is, or by the steps planned so far would be, at
// path.
func (u *undoer) where(path string) (real string, ok bool) {
	key := rules.PathKey(path)
	if real, ok := u.moved[key]; ok {
		return real, true
	}
	if u.gone[key] {
		return "", false
	}
	if _, err := os.Lstat(path); err != nil {
		return "", false
	}
	return path, true
}

// step works out how to undo op. It returns what to do, or why op is left as
// it is; plan is what doing it changes for the steps after it.
func (u *undoer) step(op undoOp) (do func(o *organizer) error, plan func(), skip *UndoSkip) {
	const notThere = "it is no longer where the run left it"
	switch {
	case op.write != nil:
		w := op.write
		real, ok := u.where(w.Path)
		if !ok {
			return nil, nil, &UndoSkip{w.Path, notThere}
		}
		if err := u.check(real, w.After); err != nil {
			return nil, nil, &UndoSkip{w.Path, err.Error()}
		}
		return func(o *organizer) error { return o.restoreTags(real, w) }, func() {}, nil

	case op.move.Reason == store.ReasonCueRewrite:
		// From is the cue sheet and To the original kept beside it.
		cue, orig := op.move.From, op.move.To
		realOrig, ok := u.where(orig)
		if !ok {
			return nil, nil, &UndoSkip{cue, "its original, kept as .orig, is gone"}
		}
		realCue, ok := u.where(cue)
		if !ok {
			return nil, nil, &UndoSkip{cue, notThere}
		}
		// A sheet written to after the run rewrote it was edited by someone:
		// moves keep a file's time, so only an edit makes it later.
		if info, err := os.Lstat(realCue); err != nil || info.ModTime().After(op.at.Add(2*time.Second)) {
			return nil, nil, &UndoSkip{cue, "it has been edited since the run rewrote it"}
		}
		return func(o *organizer) error {
				// The rewritten sheet is tagshelf's own; its original takes
				// its place back.
				if err := os.Rename(realOrig, realCue); err != nil {
					return err
				}
				o.stats.CueRewritten.Add(1)
				return nil
			}, func() {
				u.gone[rules.PathKey(orig)] = true
				delete(u.moved, rules.PathKey(orig))
			}, nil
	}

	m := op.move
	real, ok := u.where(m.To)
	if !ok {
		return nil, nil, &UndoSkip{m.To, notThere}
	}
	info, err := os.Lstat(real)
	if err != nil || !info.Mode().IsRegular() {
		return nil, nil, &UndoSkip{m.To, notThere}
	}
	if !u.stillAt(m.To) {
		return nil, nil, &UndoSkip{m.To, "a later run moved it"}
	}
	if row, indexed, err := u.st.FileAt(real); err != nil {
		return nil, nil, &UndoSkip{m.To, err.Error()}
	} else if indexed && (row.Size != info.Size() || !row.ModTime.Equal(info.ModTime())) {
		return nil, nil, &UndoSkip{m.To, "it has changed since it was indexed"}
	}
	fromKey, toKey := rules.PathKey(m.From), rules.PathKey(m.To)
	if fromKey != toKey {
		if _, taken := u.where(m.From); taken {
			return nil, nil, &UndoSkip{m.To, fmt.Sprintf("something else is at its old path now (%s)", m.From)}
		}
	}
	return func(o *organizer) error {
			if err := o.moveFile(real, m.From); err != nil {
				return err
			}
			o.recordMove(real, m.From, store.ReasonRenamed)
			o.touched.Store(filepath.Dir(real), struct{}{})
			o.stats.OtherMoved.Add(1)
			o.emit(FilePlaced{Path: real, Destination: m.From, Outcome: Renamed})
			return nil
		}, func() {
			delete(u.moved, toKey)
			u.gone[toKey] = true
			delete(u.gone, fromKey)
			u.moved[fromKey] = real
		}, nil
}

// restoreTags undoes the tag save w of the file now at path, and reads the
// file into the index again.
func (o *organizer) restoreTags(path string, w *store.TagWrite) error {
	st := o.cfg.Store
	if err := o.restore(st, o.runID, path, w.Before, w.After); err != nil {
		return err
	}
	// Counted with the files tagged where they are.
	o.stats.InPlace.Add(1)
	if row, indexed, err := st.FileAt(path); err != nil {
		return err
	} else if indexed {
		return o.reindex(row)
	}
	return nil
}

// PlanUndo works out what undoing the run runID would do, changing nothing.
func PlanUndo(st *store.Store, runID int64, check TagCheck) (UndoPlan, error) {
	u, err := newUndoer(st, runID, check)
	if err != nil {
		return UndoPlan{}, err
	}
	plan := UndoPlan{Run: u.run, Refusal: u.refusal()}
	if plan.Refusal != "" {
		return plan, nil
	}
	for _, op := range u.ops {
		_, planned, skip := u.step(op)
		if skip != nil {
			plan.Skipped = append(plan.Skipped, *skip)
			continue
		}
		planned()
		switch {
		case op.write != nil:
			plan.TagSaves++
		case op.move.Reason == store.ReasonCueRewrite:
			plan.Cues++
		default:
			plan.Moves++
		}
	}
	return plan, nil
}

// Undo undoes the run runID as a run of kind KindUndo, recorded like any
// other and so undoable itself: its moves are reversed latest first, cue
// sheets it rewrote get their originals back, and its tag saves are written
// back by restore. Whatever is not as the run left it (moved or changed
// since, its old path taken, its tags edited) is skipped and recorded as an
// error of the undo run; no file of anyone's is overwritten. The run is then
// marked undone, the duplicate decisions it carried out are withdrawn, and
// folders left empty are removed.
//
// A step that fails where it should have worked, such as a move of a file
// another program holds open, leaves the run to be undone again, as does
// cancelling ctx, which stops before the next step.
func Undo(ctx context.Context, opts Options, runID int64, check TagCheck, restore TagRestore) RunFinished {
	refuse := func(o *organizer, err error) RunFinished {
		finish := RunFinished{State: StateNotStarted, Err: err}
		if o == nil {
			o = newOrganizer(opts)
			o.emit(RunStarted{Kind: KindUndo, Destination: o.cfg.Destination})
		}
		o.emit(finish)
		return finish
	}
	if opts.Store == nil {
		return refuse(nil, errors.New("undoing a run needs the library's database"))
	}
	u, err := newUndoer(opts.Store, runID, check)
	if err != nil {
		return refuse(nil, err)
	}
	if why := u.refusal(); why != "" {
		return refuse(nil, fmt.Errorf("run %d cannot be undone: %s", runID, why))
	}
	o, refused := startAction(opts, "undoing a run", KindUndo)
	if refused != nil {
		return *refused
	}
	o.restore = restore

	o.startPhase(PhaseAudio, int64(len(u.ops)), 0)
	for _, op := range u.ops {
		if ctx.Err() != nil {
			break
		}
		do, _, skip := u.step(op)
		switch {
		case skip != nil:
			o.fail(skip.Path, "", OpUndo, errors.New("skipped: "+skip.Reason))
		default:
			if err := do(o); err != nil {
				path := ""
				if op.write != nil {
					path = op.write.Path
				} else {
					path = op.move.To
				}
				o.fail(path, "", OpUndo, err)
				u.failed++
			}
		}
		o.phase.advance(0)
	}
	if ctx.Err() == nil && u.failed == 0 {
		st := o.cfg.Store
		if err := st.MarkUndone(runID, o.runID); err != nil {
			o.fail(st.Path(), "", OpRecord, err)
		}
		// The groups the run settled wait for review again.
		if _, err := st.WithdrawReviews(runID); err != nil {
			o.fail(st.Path(), "", OpRecord, err)
		}
	}
	return o.endAction(ctx)
}
