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

	"github.com/zachjamesgreen/tagshelf/internal/playlist"
	"github.com/zachjamesgreen/tagshelf/internal/rules"
	"github.com/zachjamesgreen/tagshelf/internal/store"
)

// readPlaylist reads the playlist at path, which is refused when it is too
// large to be one.
func readPlaylist(path string) (*playlist.List, []byte, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, nil, err
	}
	if info.Size() > playlist.MaxSize {
		return nil, nil, fmt.Errorf("it is %d bytes, too large to be read as a playlist", info.Size())
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, err
	}
	return playlist.Parse(data, path), data, nil
}

// playlistTargets are the files the entries of list name, for a playlist in
// the folder dir, each by the key of its path.
func playlistTargets(list *playlist.List, dir string) []string {
	var keys []string
	for _, e := range list.Entries() {
		if target, ok := playlist.Resolve(dir, e.Text); ok {
			keys = append(keys, rules.PathKey(target))
		}
	}
	return keys
}

// refreshPlaylists brings the index's list of the library's playlists up to
// date with the playlists a walk found: new and changed ones are read, and,
// when the walk was complete, those no longer there are dropped.
func (o *organizer) refreshPlaylists(found []walkedFile, complete bool) {
	st := o.cfg.Store
	if st == nil || o.cfg.NoPlaylistRewrite {
		return
	}
	rows, err := st.Playlists()
	if err != nil {
		o.fail(st.Path(), "", OpRecord, err)
		return
	}
	for _, f := range found {
		key := rules.PathKey(f.path)
		row, known := rows[key]
		delete(rows, key)
		if known && !o.cfg.Rescan && sameStat(f.size, f.modTime, row.Size, row.ModTime) {
			continue
		}
		o.indexPlaylist(f.path)
	}
	if !complete {
		return
	}
	var gone []string
	for _, row := range rows {
		gone = append(gone, row.Path)
	}
	if err := st.DeletePlaylists(gone); err != nil {
		o.fail(st.Path(), "", OpRecord, err)
	}
}

// indexPlaylist reads the playlist at path into the index, as it is now. One
// that cannot be read is dropped from it.
func (o *organizer) indexPlaylist(path string) {
	st := o.cfg.Store
	if st == nil {
		return
	}
	info, err := os.Stat(path)
	var list *playlist.List
	if err == nil {
		list, _, err = readPlaylist(path)
	}
	if err != nil {
		if err := st.DeletePlaylists([]string{path}); err != nil {
			o.fail(path, "", OpRecord, err)
		}
		return
	}
	row := store.Playlist{Path: path, Size: info.Size(), ModTime: info.ModTime()}
	if err := st.SetPlaylist(row, playlistTargets(list, filepath.Dir(path))); err != nil {
		o.fail(path, "", OpRecord, err)
	}
}

// playlistHome reports where in the playlists folder the playlist at path
// goes. A playlist stays with its album, as any non-audio file does, when
// every file it names is beside it and follows is set: the audio of its
// folder all went to one album. Any other playlist that names files belongs
// in the playlists folder: at once in an organize run, and in a reorganize
// when it would have moved anyway.
func (o *organizer) playlistHome(path string, follows, reorganizing bool) (string, bool) {
	if o.cfg.NoPlaylistRewrite || o.cfg.Playlists == "" || !playlist.Is(path) {
		return "", false
	}
	list, _, err := readPlaylist(path)
	if err != nil {
		return "", false // said when its entries are rewritten
	}
	entries := list.Entries()
	if len(entries) == 0 {
		return "", false
	}
	dir := filepath.Dir(path)
	beside := true
	for _, e := range entries {
		target, ok := playlist.Resolve(dir, e.Text)
		if !ok || rules.PathKey(filepath.Dir(target)) != rules.PathKey(dir) {
			beside = false
			break
		}
	}
	if (beside && follows) || (reorganizing && !follows) {
		return "", false
	}
	return filepath.Join(o.cfg.Playlists, filepath.Base(path)), true
}

// nowAt reports where the file that was at path when the run began is now,
// and whether the run moved it. A file set aside as a duplicate is where
// the copy that was kept is.
func (o *organizer) nowAt(path string) (string, bool) {
	key := rules.PathKey(path)
	if kept, ok := o.keptFor.Load(key); ok {
		keeper := kept.(string)
		if placed, ok := o.placedAt(keeper); ok {
			keeper = placed
		}
		return keeper, true
	}
	if placed, ok := o.placedAt(path); ok && rules.PathKey(placed) != key {
		return placed, true
	}
	return path, false
}

// rewritePlaylists keeps the playlists pointing at the files this run or
// action moved: the playlists it met itself, and those of the library that
// the index has naming a file it moved.
func (o *organizer) rewritePlaylists(ctx context.Context) {
	if o.cfg.NoPlaylistRewrite {
		return
	}
	type job struct{ was, now string }
	jobs := map[string]job{}
	var moved []string
	o.placed.Range(func(k, v any) bool {
		key, now := k.(string), v.(string)
		if rules.PathKey(now) != key {
			moved = append(moved, key)
		}
		// A playlist the run met is looked at wherever it is now: the
		// index may not know of it.
		if playlist.Is(key) {
			was := key
			if spelled, ok := o.cameFrom.Load(key); ok {
				was = spelled.(string)
			}
			jobs[key] = job{was: was, now: now}
		}
		return true
	})
	o.keptFor.Range(func(k, _ any) bool {
		moved = append(moved, k.(string))
		return true
	})
	if len(moved) == 0 {
		return
	}
	if st := o.cfg.Store; st != nil {
		naming, err := st.PlaylistsNaming(moved)
		if err != nil {
			o.fail(st.Path(), "", OpRecord, err)
		}
		for _, path := range naming {
			key := rules.PathKey(path)
			if _, ok := jobs[key]; ok {
				continue
			}
			now := path
			if placed, ok := o.placedAt(path); ok {
				now = placed
			}
			jobs[key] = job{was: path, now: now}
		}
	}
	if len(jobs) == 0 {
		return
	}
	list := make([]job, 0, len(jobs))
	for _, j := range jobs {
		list = append(list, j)
	}
	slices.SortFunc(list, func(a, b job) int { return cmp.Compare(rules.PathKey(a.now), rules.PathKey(b.now)) })
	o.startPhase(PhasePlaylists, int64(len(list)), 0)
	for _, j := range list {
		if ctx.Err() != nil {
			return
		}
		o.rewritePlaylist(j.was, j.now)
		o.phase.advance(0)
	}
}

// rewritePlaylist points the entries of one playlist at where their files
// are now. The playlist was at was when the run began and is at now; its
// entries are read as written for the folder it was in.
//
// An entry keeps its style: from the playlist's folder or whole, with \ or
// /. Only an entry whose file moved, or whose playlist moved away from it,
// is touched. An entry that is not a file, or names a file that is not
// there, is left as it is. What the playlist held before and after is
// recorded with the run, which is what undoing the run goes by.
func (o *organizer) rewritePlaylist(was, now string) {
	if _, restored := o.restoredPlaylists.Load(rules.PathKey(was)); restored {
		// An undo gave it back the text it had where it is going back to.
		o.indexPlaylist(now)
		return
	}
	readFrom := now
	if o.cfg.DryRun {
		readFrom = was
	}
	list, data, err := readPlaylist(readFrom)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			// The index knew of a playlist that is gone.
			o.indexPlaylist(now)
			return
		}
		o.fail(readFrom, "", OpReadPlaylist, err)
		return
	}
	wasDir, nowDir := filepath.Dir(was), filepath.Dir(now)
	stayed := rules.PathKey(wasDir) == rules.PathKey(nowDir)
	changed, left := 0, list.NotFiles()
	for _, e := range list.Entries() {
		old, ok := playlist.Resolve(wasDir, e.Text)
		if !ok {
			left++
			continue
		}
		style := playlist.StyleOf(e.Text)
		target, moved := o.nowAt(old)
		if !moved {
			whole := style.Absolute && !(style.Rooted && filepath.VolumeName(wasDir) != filepath.VolumeName(nowDir))
			if stayed || whole {
				continue // it reads right as it is
			}
			if _, err := os.Stat(old); err != nil {
				left++
				continue
			}
		}
		if text := playlist.Format(nowDir, target, style); text != e.Text {
			list.Set(e.Line, text)
			changed++
		}
	}
	warn := func() {
		if left > 0 {
			o.emit(Warning{Kind: WarnPlaylistEntries, Path: now, Count: left})
		}
	}
	if changed == 0 {
		if !stayed {
			warn()
			if !o.cfg.DryRun {
				o.forgetPlaylist(was)
				o.indexPlaylist(now)
			}
		}
		return
	}
	rewritten, err := list.Bytes()
	if err != nil {
		o.emit(Warning{Kind: WarnPlaylistEncoding, Path: now})
		return
	}
	if o.cfg.DryRun {
		o.emit(PlaylistRewritten{Path: now, Entries: changed})
		o.stats.Playlists.Add(1)
		warn()
		return
	}
	info, err := os.Stat(now)
	if err != nil {
		o.fail(now, "", OpReadPlaylist, err)
		return
	}
	// The record comes first, so that no playlist is rewritten that undo
	// does not know of.
	if st := o.cfg.Store; st != nil {
		if err := st.RecordPlaylistRewrite(o.runID, now, data, rewritten); err != nil {
			o.fail(now, "", OpRecord, err)
			return
		}
	}
	if err := replaceFile(now, rewritten, info.Mode().Perm()); err != nil {
		o.fail(now, "", OpRewritePlaylist, err)
		return
	}
	if !stayed {
		o.forgetPlaylist(was)
	}
	o.indexPlaylist(now)
	o.emit(PlaylistRewritten{Path: now, Entries: changed})
	o.stats.Playlists.Add(1)
	warn()
}

// forgetPlaylist drops the playlist that was at path from the index.
func (o *organizer) forgetPlaylist(path string) {
	if st := o.cfg.Store; st != nil {
		if err := st.DeletePlaylists([]string{path}); err != nil {
			o.fail(path, "", OpRecord, err)
		}
	}
}

// playlistStep works out how to undo the rewrite of a playlist: it gets back
// what it held before, as the run recorded it, and only while it still
// holds what the rewrite left. A playlist someone edited since keeps its
// entries, which the undo's own moves are then followed in.
func (u *undoer) playlistStep(op undoOp) (do func(o *organizer) error, plan func(), skip *UndoSkip) {
	m := op.move
	real, ok := u.where(m.To)
	if !ok {
		return nil, nil, &UndoSkip{Path: m.To, Reason: "it is no longer where the run left it"}
	}
	held, err := os.ReadFile(real)
	if err != nil {
		return nil, nil, &UndoSkip{Path: m.To, Reason: "it cannot be read: " + err.Error()}
	}
	if !bytes.Equal(held, m.CueAfter) {
		return nil, nil, &UndoSkip{Path: m.To, Reason: "it has been edited since the run rewrote it, so its earlier text is not put back: " +
			"its entries follow the files this undo moves"}
	}
	return func(o *organizer) error {
		info, err := os.Stat(real)
		if err != nil {
			return err
		}
		if err := o.cfg.Store.RecordPlaylistRewrite(o.runID, real, held, m.CueBefore); err != nil {
			return err
		}
		if err := replaceFile(real, m.CueBefore, info.Mode().Perm()); err != nil {
			return err
		}
		o.restoredPlaylists.Store(rules.PathKey(real), struct{}{})
		o.indexPlaylist(real)
		o.stats.Playlists.Add(1)
		return nil
	}, func() {}, nil
}

// movedBack notes, for the playlists, that an undo moved the file at real to
// to. A file the undo moves twice, off the name a run gave it second and
// then off the one it gave first, is known by where it was when the undo
// began.
func (u *undoer) movedBack(o *organizer, real, to string) {
	from := rules.PathKey(real)
	first, again := u.first[from]
	if again {
		delete(u.first, from)
	} else {
		first = from
		o.cameFrom.Store(first, real)
	}
	u.first[rules.PathKey(to)] = first
	o.placed.Store(first, to)
}
