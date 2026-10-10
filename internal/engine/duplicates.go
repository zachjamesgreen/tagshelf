package engine

import (
	"context"
	"os"
	"path/filepath"
	"slices"

	"github.com/zachjamesgreen/tagshelf/internal/rules"
	"github.com/zachjamesgreen/tagshelf/internal/store"
)

// dupCopy is a member of an exact duplicate group as a run sees it.
type dupCopy struct {
	store.File
	// incoming is set for a file this organize run walked in its source.
	incoming bool
	// added is set for a file the run adds to the group: an incoming file, or
	// one the index did not hold before this run, or held with other audio.
	// A file that was only read again is not added.
	added bool
}

// resolveDuplicates deals with the exact duplicates, files with the same
// audio hash, among the files the run is about to place, and returns those
// still to be placed.
//
// A group the run added a file to is resolved when auto-resolve is on and no
// review holds two of its copies: the suggested keeper stays, and every other
// copy moves to the duplicates folder under its path relative to its root.
// Groups made only of files that were already there are left for review, and
// so is a group a person decided about, such as two copies kept on purpose
// that a third now joins.
//
// Copies already in the duplicates folder do not count. A file whose only
// other copies are there is placed like any other: the copy that was kept
// when they were put aside is gone, and the library would otherwise have
// none.
func (o *organizer) resolveDuplicates(ctx context.Context, audio []walkedFile) []walkedFile {
	st := o.cfg.Store
	if st == nil {
		return audio
	}
	groups, err := st.ExactGroups()
	if err != nil {
		o.fail(st.Path(), "", OpRecord, err)
		return audio
	}
	walked := make(map[string]bool, len(audio))
	for _, f := range audio {
		walked[rules.PathKey(f.path)] = true
	}
	settled := make(map[string]bool)
	for _, g := range groups {
		if ctx.Err() != nil {
			// Files not yet settled stay where they are, as the workers
			// would leave them.
			break
		}
		o.resolveGroup(g, walked, settled)
	}
	if len(settled) == 0 {
		return audio
	}
	rest := make([]walkedFile, 0, len(audio))
	for _, f := range audio {
		if settled[rules.PathKey(f.path)] {
			o.phase.advance(f.size)
			continue
		}
		rest = append(rest, f)
	}
	return rest
}

// resolveGroup resolves one exact group, if it is the run's to resolve,
// adding the path keys of the walked files it moved (or failed to) to settled.
func (o *organizer) resolveGroup(g []store.File, walked, settled map[string]bool) {
	members := o.groupMembers(g, walked)
	if !slices.ContainsFunc(members, func(m dupCopy) bool { return m.added }) {
		return
	}

	if len(members) < 2 || o.cfg.NoAutoResolve {
		return
	}
	ids := make([]int64, len(members))
	copies := make([]rules.Copy, len(members))
	for i, m := range members {
		ids[i] = m.ID
		copies[i] = keeperCopy(m.File)
	}
	st := o.cfg.Store
	// A review that holds two of these copies is a person's decision about
	// them, or a run's that a person can see: the whole group is left for a
	// person, whichever copies the review did not cover.
	reviewed, err := st.ReviewedTogether(store.GroupExact, ids)
	if err != nil {
		o.fail(members[0].Path, "", OpRecord, err)
		return
	}
	if reviewed {
		return
	}

	keeper := rules.SuggestedKeeper(copies)
	kept := members[keeper]
	if kept.incoming {
		// The kept copy is placed before any other copy moves out: if it
		// cannot be, or the run stops first, the group stays as it was
		// rather than leaving this audio only in the duplicates folder.
		if o.cfg.DryRun {
			for i, m := range members {
				if i != keeper {
					o.vacated.Store(rules.PathKey(m.Path), struct{}{})
				}
			}
		}
		settled[rules.PathKey(kept.Path)] = true
		o.processFile(kept.Path)
		if _, ok := o.placedAt(kept.Path); !ok {
			return
		}
	}
	resolved := true
	for i, m := range members {
		if i != keeper && !o.toDuplicates(m, settled) {
			resolved = false
		}
	}
	if kept.incoming {
		// A copy that moved out may have held the kept copy's name.
		o.retryKept(kept.Path)
	}
	if !resolved || o.cfg.DryRun {
		return
	}
	review := store.Review{Kind: store.GroupExact, Members: ids, Keeper: members[keeper].ID,
		Outcome: store.OutcomeResolved, By: store.ByAuto, RunID: o.runID}
	if err := st.RecordReview(review); err != nil {
		o.fail(members[keeper].Path, "", OpRecord, err)
	}
}

// groupMembers lists the copies in an exact group that are where the index
// says, with the size and modification time it recorded, leaving out those
// in the duplicates folder. A source other than this run's is no concern of
// the run, and neither is a file it did not walk there.
func (o *organizer) groupMembers(g []store.File, walked map[string]bool) (members []dupCopy) {
	reorganizing := o.cfg.Reorganizing()
	var seen []os.FileInfo
	for _, f := range g {
		key := rules.PathKey(f.Path)
		isIncoming := walked[key] && !reorganizing
		if f.RootKind == store.RootSource && !isIncoming {
			continue
		}
		info, err := os.Stat(f.Path)
		if err != nil || !asOnDisk(info, f.Size, f.ModTime) {
			continue
		}
		// One file indexed under two roots is still one copy.
		if slices.ContainsFunc(seen, func(s os.FileInfo) bool { return os.SameFile(s, info) }) {
			continue
		}
		seen = append(seen, info)
		if f.RootKind == store.RootDuplicates {
			continue
		}
		_, added := o.added.Load(key)
		members = append(members, dupCopy{File: f, incoming: isIncoming, added: isIncoming || (added && !o.firstIndex)})
	}
	return members
}

// toDuplicates moves a copy to the duplicates folder, under its path relative
// to its root, and reports whether it got there (in a dry run, whether it
// would). Either way a walked file is settled: it is not placed again.
func (o *organizer) toDuplicates(m dupCopy, settled map[string]bool) bool {
	key := rules.PathKey(m.Path)
	settled[key] = true
	if !o.place(m.Path, "", filepath.Join(o.cfg.Duplicates, m.RelPath), Duplicate) {
		// Its neighbours cannot follow a file that stayed behind.
		o.recordAlbum(filepath.Dir(m.Path), "")
		return false
	}
	if final, ok := o.placedAt(m.Path); ok {
		// Companion files follow their audio into the duplicates folder.
		o.recordAlbum(filepath.Dir(m.Path), filepath.Dir(final))
	}
	if o.cfg.DryRun {
		o.vacated.Store(key, struct{}{})
	}
	return true
}

// retryKept moves a kept copy that landed on a " (n)" name onto its clean
// name, if that is free now.
func (o *organizer) retryKept(path string) {
	o.suffixedMu.Lock()
	i := slices.IndexFunc(o.suffixed, func(s suffixedMove) bool { return s.src == path })
	var s suffixedMove
	if i >= 0 {
		s = o.suffixed[i]
		o.suffixed = slices.Delete(o.suffixed, i, i+1)
	}
	o.suffixedMu.Unlock()
	if i >= 0 {
		o.retryOne(s)
	}
}

// keeperCopy is what the suggested-keeper rule compares of an indexed file.
func keeperCopy(f store.File) rules.Copy {
	ids := 0
	for _, id := range []string{f.ReleaseID, f.ReleaseTrackID, f.RecordingID} {
		if id != "" {
			ids++
		}
	}
	return rules.Copy{
		Lossless:       f.Lossless,
		Bitrate:        f.Bitrate,
		SampleRate:     f.SampleRate,
		BitDepth:       f.BitDepth,
		Length:         f.Length,
		InLibrary:      f.RootKind == store.RootLibrary,
		MusicBrainzIDs: ids,
		Manual:         f.Manual != "",
		Art:            f.HasArt,
		IndexOrder:     f.ID,
	}
}

// PossibleKey is rules.PossibleKey for an indexed file, comparing its track
// artist, or its album artist when it has none.
func PossibleKey(f store.File) string {
	artist := f.Artist
	if artist == "" {
		artist = f.AlbumArtist
	}
	return rules.PossibleKey(artist, f.Album, f.Track, f.Title)
}

// countNeedReview counts the duplicate groups left waiting for review, for
// RunFinished. A dry run changed nothing, so it has nothing new to count.
func (o *organizer) countNeedReview() {
	st := o.cfg.Store
	if st == nil || o.cfg.DryRun {
		return
	}
	n, err := st.GroupsNeedingReview(PossibleKey)
	if err != nil {
		o.fail(st.Path(), "", OpRecord, err)
		return
	}
	o.needReview = n
}
