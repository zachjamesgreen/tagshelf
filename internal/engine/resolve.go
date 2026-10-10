package engine

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/zachjamesgreen/tagshelf/internal/rules"
	"github.com/zachjamesgreen/tagshelf/internal/store"
)

// Resolution is a person's decision on one duplicate group: Keeper stays where
// it is and every other member moves to the duplicates folder. Members are
// the group's file IDs as the person saw it.
type Resolution struct {
	Kind    store.GroupKind
	Members []int64
	Keeper  int64
}

// SuggestedKeeper is the index in files of the copy the suggested-keeper rule
// would keep.
func SuggestedKeeper(files []store.File) int {
	copies := make([]rules.Copy, len(files))
	for i, f := range files {
		copies[i] = keeperCopy(f)
	}
	return rules.SuggestedKeeper(copies)
}

// ResolveDuplicates carries out resolutions as one run of kind KindTUIAction,
// recorded like any other: each copy not kept moves to the duplicates folder
// under its path relative to its root, non-audio files follow out of a folder
// all of whose audio left, and each group that moved in full gets a review
// decided by the user.
//
// A group is left alone, with an error, when any of its files is missing
// from the index or no longer on disk as it was indexed: the decision was
// made about other files. Cancelling ctx stops before the next group.
func ResolveDuplicates(ctx context.Context, opts Options, resolutions []Resolution) RunFinished {
	o, refused := startAction(opts, "resolving duplicates", KindTUIAction)
	if refused != nil {
		return *refused
	}
	var others int64
	for _, r := range resolutions {
		others += int64(max(len(r.Members)-1, 0))
	}
	o.startPhase(PhaseAudio, others, 0)
	for _, r := range resolutions {
		if ctx.Err() != nil {
			break
		}
		o.resolve(r)
	}
	return o.endAction(ctx)
}

// startAction begins a run of the given kind over the library, which a dry
// run or a run without the library's database cannot be: refused is then how
// it ended. what names the action for that error.
func startAction(opts Options, what string, kind RunKind) (o *organizer, refused *RunFinished) {
	// The library is re-sorted in place: nothing here comes from a source.
	opts.Source = opts.Destination
	o = newOrganizer(opts)
	cfg := o.cfg
	o.emit(RunStarted{Kind: kind, Destination: cfg.Destination})

	notStarted := func(err error) (*organizer, *RunFinished) {
		finish := RunFinished{State: StateNotStarted, Err: err}
		o.emit(finish)
		return nil, &finish
	}
	switch {
	case cfg.Store == nil:
		return notStarted(fmt.Errorf("%s needs the library's database", what))
	case cfg.DryRun:
		return notStarted(fmt.Errorf("%s is not done in a dry run", what))
	}
	id, err := cfg.Store.StartRun(storeKinds[kind], false, "", cfg.Destination, time.Now())
	if err != nil {
		return notStarted(err)
	}
	o.runID = id
	return o, nil
}

// endAction ends a run startAction began once its audio has moved: unless ctx
// was cancelled, non-audio files follow their audio and emptied folders go.
func (o *organizer) endAction(ctx context.Context) RunFinished {
	interrupted := ctx.Err() != nil
	if !interrupted {
		o.startPhase(PhaseCompanions, 0, 0)
		o.followAudio()
		o.startPhase(PhaseCleanup, 0, 0)
		// A copy left by a crash would keep its folder from being removed.
		var emptied []string
		o.touched.Range(func(dir, _ any) bool {
			emptied = append(emptied, dir.(string))
			return true
		})
		o.removeStaleCopies(emptied)
		cleanupEmptyDirectories(o.walkRoots(), nil, func(dir string) {
			o.emit(DirRemoved{Path: dir})
			o.phase.advance(0)
		}, &o.touched)
	}
	o.countNeedReview()
	return o.end(o.finish(interrupted))
}

// resolve carries out one resolution.
func (o *organizer) resolve(r Resolution) {
	st := o.cfg.Store
	members := slices.Clone(r.Members)
	slices.Sort(members)
	members = slices.Compact(members)
	refuse := func(path string, format string, args ...any) {
		if path == "" {
			path = st.Path()
		}
		o.fail(path, "", OpReview, fmt.Errorf(format, args...))
	}
	if len(members) < 2 {
		refuse("", "a duplicate group needs at least two files, not %d", len(members))
		return
	}
	if !slices.Contains(members, r.Keeper) {
		refuse("", "the copy to keep (file %d) is not in its group", r.Keeper)
		return
	}
	files, err := st.FilesByID(members)
	if err != nil {
		o.fail(st.Path(), "", OpRecord, err)
		return
	}
	if len(files) != len(members) {
		refuse("", "%d of the group's %d files are no longer in the index", len(members)-len(files), len(members))
		return
	}
	// Nothing moves unless every copy, the kept one above all, is still the
	// file the index describes.
	for _, f := range files {
		if err := asIndexed(f); err != nil {
			o.fail(f.Path, "", OpReview, err)
			return
		}
	}

	resolved := true
	settled := make(map[string]bool)
	for _, f := range files {
		if f.ID == r.Keeper {
			continue
		}
		if !o.toDuplicates(dupCopy{File: f}, settled) {
			resolved = false
		}
		o.phase.advance(f.Size)
	}
	if !resolved {
		return
	}
	review := store.Review{Kind: r.Kind, Members: members, Keeper: r.Keeper,
		Outcome: store.OutcomeResolved, By: store.ByUser, RunID: o.runID}
	if err := st.RecordReview(review); err != nil {
		o.fail(st.Path(), "", OpRecord, err)
	}
}

// asIndexed checks that f is a file of the library, untagged or other folder
// and still on disk as the index describes it: what a person decided about
// the row then holds for the file.
func asIndexed(f store.File) error {
	switch f.RootKind {
	case store.RootLibrary, store.RootUntagged, store.RootOther:
	default:
		return fmt.Errorf("it is in the %s folder, not in the library", f.RootKind)
	}
	info, err := os.Stat(f.Path)
	if err != nil || !asOnDisk(info, f.Size, f.ModTime) {
		return errors.New("it is gone or has changed since it was indexed")
	}
	return nil
}

// followAudio moves the non-audio files of each folder all of whose audio
// went to one place after it. A folder that still holds audio keeps its
// files.
func (o *organizer) followAudio() {
	o.albumsMu.Lock()
	dirs := make(map[string]string)
	for src, set := range o.albums {
		if len(set) != 1 {
			continue
		}
		for dst := range set {
			if dst != "" {
				dirs[src] = dst
			}
		}
	}
	o.albumsMu.Unlock()

	roots := []string{o.cfg.Destination, o.cfg.Untagged, o.cfg.Other}
	for src, dst := range dirs {
		// A copy lying loose in the library, untagged or other folder has no
		// folder of its own; what lies next to it belongs to other things.
		if containsPath(roots, src) {
			continue
		}
		entries, err := os.ReadDir(src)
		if err != nil {
			continue
		}
		var companions []string
		audioLeft := false
		for _, e := range entries {
			switch {
			case e.IsDir() || strings.HasPrefix(e.Name(), store.OwnFilePrefix):
			case isAudioFile(e.Name()):
				audioLeft = true
			default:
				companions = append(companions, e.Name())
			}
		}
		if audioLeft {
			continue
		}
		for _, name := range companions {
			o.place(filepath.Join(src, name), "", filepath.Join(dst, name), Companion)
			o.phase.advance(0)
		}
	}
}
