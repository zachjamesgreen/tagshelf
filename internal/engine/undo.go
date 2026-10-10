package engine

import (
	"bytes"
	"cmp"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
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
	// Later is set when the reason can pass: the file's index row is out of
	// date, a later run moved it, or something lies at its old path. Such a
	// step can still be undone another time, so the run stays undoable.
	Later bool
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
	// order is its place among the run's moves and tag saves together, from
	// 1; 0 for a run recorded before they were numbered.
	order int
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

	// origOf holds, by the path key of a cue sheet whose rewrite is not
	// undone because the sheet was edited, the original the run kept beside
	// it: it goes back with the sheet.
	origOf map[string]string
}

// newUndoer reads what the run did.
func newUndoer(st *store.Store, runID int64, check TagCheck) (*undoer, error) {
	run, err := st.Run(runID)
	if err != nil {
		return nil, err
	}
	u := &undoer{st: st, check: check, run: run, moved: map[string]string{}, gone: map[string]bool{}, origOf: map[string]string{}}
	moves, err := st.Moves(runID)
	if err != nil {
		return nil, err
	}
	writes, err := st.TagWrites(runID)
	if err != nil {
		return nil, err
	}
	var last int64
	var moveOps, writeOps []undoOp
	for i := range moves {
		moveOps = append(moveOps, undoOp{move: &moves[i], at: moves[i].At, order: moves[i].Seq})
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
		writeOps = append(writeOps, undoOp{write: &writes[i], at: writes[i].At, order: writes[i].Seq})
	}
	// Undone in the reverse of the order they were done in.
	u.ops = inOrder(moveOps, writeOps)
	slices.Reverse(u.ops)
	return u, nil
}

// inOrder puts a run's moves and tag saves, each in the order they were
// recorded, into the one order the run did them in: that of the sequence
// numbers they share. A run recorded before they were numbered is ordered by
// mergeByTime.
func inOrder(moves, writes []undoOp) []undoOp {
	ops := slices.Concat(moves, writes)
	if slices.ContainsFunc(ops, func(op undoOp) bool { return op.order == 0 }) {
		return mergeByTime(moves, writes)
	}
	slices.SortStableFunc(ops, func(a, b undoOp) int { return cmp.Compare(a.order, b.order) })
	return ops
}

// mergeByTime orders the moves and tag saves of a run that did not number
// them by the time recorded for each. Clocks being coarse, a move and a tag
// save may carry the same time: the move came first if it brought the file to
// the path the save was made at, and the save otherwise, as it did if the
// file was then moved away or the two are different files. Merging the two
// lists keeps each in its own order whatever the times say, which a sort by
// comparing pairs did not: steps of several files at one time have no
// consistent order by that rule.
func mergeByTime(moves, writes []undoOp) []undoOp {
	ops := make([]undoOp, 0, len(moves)+len(writes))
	for len(moves) > 0 && len(writes) > 0 {
		m, w := moves[0], writes[0]
		c := m.at.Compare(w.at)
		if c == 0 && rules.PathKey(m.move.To) == rules.PathKey(w.write.Path) {
			c = -1
		}
		if c < 0 {
			ops, moves = append(ops, m), moves[1:]
		} else {
			ops, writes = append(ops, w), writes[1:]
		}
	}
	return append(append(ops, moves...), writes...)
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
			return nil, nil, &UndoSkip{Path: w.Path, Reason: notThere}
		}
		if err := u.check(real, w.After); err != nil {
			return nil, nil, &UndoSkip{Path: w.Path, Reason: err.Error()}
		}
		return func(o *organizer) error { return o.restoreTags(real, w) }, func() {}, nil

	case op.move.Reason == store.ReasonCueRewrite:
		return u.cueStep(op)
	}

	m := op.move
	real, ok := u.where(m.To)
	if !ok {
		return nil, nil, &UndoSkip{Path: m.To, Reason: notThere}
	}
	info, err := os.Lstat(real)
	if err != nil || !info.Mode().IsRegular() {
		return nil, nil, &UndoSkip{Path: m.To, Reason: notThere}
	}
	// The reasons from here on can pass: undoing the later run, an index
	// run, or moving what is in the way cures them.
	if !u.stillAt(m.To) {
		return nil, nil, &UndoSkip{Path: m.To, Reason: "a later run moved it", Later: true}
	}
	if row, indexed, err := u.st.FileAt(real); err != nil {
		return nil, nil, &UndoSkip{Path: m.To, Reason: err.Error(), Later: true}
	} else if indexed && !sameStat(info.Size(), info.ModTime(), row.Size, row.ModTime) {
		return nil, nil, &UndoSkip{Path: m.To, Reason: "it has changed since it was indexed (an index run reads it again)", Later: true}
	}
	fromKey, toKey := rules.PathKey(m.From), rules.PathKey(m.To)
	if fromKey != toKey {
		if _, taken := u.where(m.From); taken {
			return nil, nil, &UndoSkip{Path: m.To, Reason: fmt.Sprintf("something else is at its old path now (%s)", m.From), Later: true}
		}
	}
	// The original of a cue sheet that keeps its edited text goes back with
	// the sheet, wherever the sheet's moves take it.
	orig, withOrig := u.origOf[toKey]
	origBack := m.From + ".orig"
	carry := func() {
		delete(u.origOf, toKey)
		u.origOf[fromKey] = origBack
	}
	return func(o *organizer) error {
			if err := o.moveFile(real, m.From); err != nil {
				return err
			}
			o.recordMove(real, m.From, store.ReasonRenamed)
			o.touched.Store(filepath.Dir(real), struct{}{})
			o.stats.OtherMoved.Add(1)
			o.emit(FilePlaced{Path: real, Destination: m.From, Outcome: Renamed})
			if fromKey == toKey {
				// A move that only changed letter case renamed the folders
				// above the file too, without a record of their own.
				o.respellFolders(m.From)
			}
			if withOrig && o.moveOrigBack(orig, origBack) {
				carry()
			}
			return nil
		}, func() {
			delete(u.moved, toKey)
			u.gone[toKey] = true
			delete(u.gone, fromKey)
			u.moved[fromKey] = real
			if withOrig {
				if _, taken := u.where(origBack); !taken {
					origKey := rules.PathKey(orig)
					u.moved[rules.PathKey(origBack)] = orig
					if origKey != rules.PathKey(origBack) {
						u.gone[origKey] = true
					}
					carry()
				}
			}
		}, nil
}

// cueStep works out how to undo the rewrite of a cue sheet. From is the cue
// sheet and To the original kept beside it.
//
// The sheet gets back what it held before this run's rewrite, as the run
// recorded it, and only while it still holds what the rewrite left: a sheet
// someone edited since keeps its text. The .orig is the first original, which
// may be an earlier run's; it is removed only when it is this run's, that is,
// when it holds what the sheet is given back. A rewrite recorded before runs
// kept what they changed is undone from the .orig, as it was then.
func (u *undoer) cueStep(op undoOp) (do func(o *organizer) error, plan func(), skip *UndoSkip) {
	const notThere = "it is no longer where the run left it"
	const edited = "it has been edited since the run rewrote it, so its text is left as it is: " +
		"it still names files as the run named them, which this undo renames back"
	m := op.move
	cue, orig := m.From, m.To
	origKey := rules.PathKey(orig)
	realOrig, hasOrig := u.where(orig)
	follow := func() {
		if hasOrig {
			u.origOf[rules.PathKey(cue)] = realOrig
		}
	}

	if len(m.CueAfter) == 0 {
		if !hasOrig {
			return nil, nil, &UndoSkip{Path: cue, Reason: "its original, kept as .orig, is gone"}
		}
		realCue, ok := u.where(cue)
		if !ok {
			return nil, nil, &UndoSkip{Path: cue, Reason: notThere}
		}
		// A sheet written to after the run rewrote it was edited by someone:
		// moves keep a file's time, so only an edit makes it later.
		if info, err := os.Lstat(realCue); err != nil || info.ModTime().After(op.at.Add(2*time.Second)) {
			follow()
			return nil, nil, &UndoSkip{Path: cue, Reason: edited}
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
				u.gone[origKey] = true
				delete(u.moved, origKey)
			}, nil
	}

	realCue, ok := u.where(cue)
	if !ok {
		return nil, nil, &UndoSkip{Path: cue, Reason: notThere}
	}
	now, err := os.ReadFile(realCue)
	if err != nil {
		return nil, nil, &UndoSkip{Path: cue, Reason: notThere}
	}
	// Only this run's own original follows or goes: one an earlier run kept
	// is that run's to deal with.
	if hasOrig {
		kept, err := os.ReadFile(realOrig)
		hasOrig = err == nil && bytes.Equal(kept, m.CueBefore)
	}
	if !bytes.Equal(now, m.CueAfter) {
		follow()
		return nil, nil, &UndoSkip{Path: cue, Reason: edited}
	}
	return func(o *organizer) error {
			info, err := os.Stat(realCue)
			if err != nil {
				return err
			}
			if err := replaceFile(realCue, m.CueBefore, info.Mode().Perm()); err != nil {
				return err
			}
			if hasOrig {
				// The sheet is its original again; a copy beside it would
				// only keep the folder from ever being removed.
				if err := os.Remove(realOrig); err != nil {
					return err
				}
			}
			o.stats.CueRewritten.Add(1)
			return nil
		}, func() {
			if hasOrig {
				u.gone[origKey] = true
				delete(u.moved, origKey)
			}
		}, nil
}

// replaceFile writes data over the file at path, through a temporary file
// beside it, so that a failure part way leaves the file as it was.
func replaceFile(path string, data []byte, perm os.FileMode) error {
	f, err := os.CreateTemp(filepath.Dir(path), store.OwnFilePrefix+"cue-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	_, err = f.Write(data)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Chmod(tmp, perm)
	}
	if err == nil {
		err = os.Rename(tmp, path)
	}
	if err != nil {
		os.Remove(tmp)
	}
	return err
}

// moveOrigBack moves the original of a cue sheet that undo left as edited to
// dst, beside the sheet, and reports whether it did. An original that cannot
// go there stays, with an error.
func (o *organizer) moveOrigBack(orig, dst string) bool {
	if _, err := os.Lstat(dst); err == nil {
		o.fail(orig, dst, OpUndo, errors.New("skipped: something else is at the place beside its cue sheet"))
		return false
	}
	if err := o.moveFile(orig, dst); err != nil {
		o.fail(orig, dst, OpUndo, err)
		return false
	}
	o.recordMove(orig, dst, store.ReasonRenamed)
	o.touched.Store(filepath.Dir(orig), struct{}{})
	o.stats.OtherMoved.Add(1)
	o.emit(FilePlaced{Path: orig, Destination: dst, Outcome: Renamed})
	return true
}

// respellFolders gives the folders above path inside the library the exact
// spelling path has them in.
func (o *organizer) respellFolders(path string) {
	for _, dir := range o.libraryDirs(path) {
		if _, err := renameCase(dir); err != nil {
			o.fail(dir, dir, OpRename, err)
		}
	}
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
// sheets it rewrote get back what they held before, and its tag saves are
// written back by restore. Whatever is not as the run left it (moved or
// changed since, its old path taken, its tags edited) is skipped and recorded
// as an error of the undo run; no file of anyone's is overwritten. Folders
// left empty are removed.
//
// The run is marked undone, and the duplicate decisions it carried out are
// withdrawn, only when at least one step was undone and none is left that
// could be another time. It stays as it was, to be undone again, when a step
// failed where it should have worked (a move of a file another program holds
// open), when one was skipped for a reason that can pass (UndoSkip.Later),
// when nothing at all was undone, and when ctx was cancelled, which stops
// before the next step. RunFinished.NotUndone then says which.
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
	done, later := 0, 0
	for _, op := range u.ops {
		if ctx.Err() != nil {
			break
		}
		do, _, skip := u.step(op)
		switch {
		case skip != nil:
			o.fail(skip.Path, "", OpUndo, errors.New("skipped: "+skip.Reason))
			if skip.Later {
				later++
			}
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
			} else {
				done++
			}
		}
		o.phase.advance(0)
	}
	var why []string
	if ctx.Err() != nil {
		why = append(why, "the undo was stopped before its end")
	}
	if u.failed > 0 {
		why = append(why, fmt.Sprintf("%d of its steps failed", u.failed))
	}
	if later > 0 {
		why = append(why, fmt.Sprintf("%d of its steps were skipped for a reason that can pass", later))
	}
	if len(why) == 0 && done == 0 {
		why = append(why, "nothing of it could be undone")
	}
	if len(why) > 0 {
		o.notUndone = fmt.Sprintf("run %d is not marked undone and can be undone again: %s", runID, strings.Join(why, ", "))
	} else {
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
