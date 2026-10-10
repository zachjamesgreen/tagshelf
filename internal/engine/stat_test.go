package engine

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"tagshelf/internal/store"
)

// changes are the ways a file can differ from its index row, one sign at a
// time: a tagger may keep a file's time when it saves, which leaves the size
// as the only sign, and may keep its size by using padding, which leaves the
// time. within is a time that differs by less than modTimeTolerance, as the
// same file's does on a file system that keeps times less finely.
var changes = []struct {
	name    string
	change  func(t *testing.T, path string)
	changed bool
}{
	{"only the time", func(t *testing.T, path string) { touch(t, path, time.Hour) }, true},
	{"only the time, backwards", func(t *testing.T, path string) { touch(t, path, -time.Hour) }, true},
	{"only the size", func(t *testing.T, path string) {
		t.Helper()
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.WriteString("more"); err != nil {
			t.Fatal(err)
		}
		if err := f.Close(); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(path, info.ModTime(), info.ModTime()); err != nil {
			t.Fatal(err)
		}
	}, true},
	{"a time within the tolerance", func(t *testing.T, path string) { touch(t, path, modTimeTolerance/2) }, false},
	{"a time within the tolerance, backwards", func(t *testing.T, path string) { touch(t, path, -modTimeTolerance) }, false},
	{"nothing", func(*testing.T, string) {}, false},
}

func TestSameStat(t *testing.T) {
	at := time.Date(2026, 10, 8, 12, 0, 1, 500, time.UTC)
	for _, tt := range []struct {
		size int64
		d    time.Duration
		want bool
	}{
		{10, 0, true},
		{10, modTimeTolerance, true},
		{10, -modTimeTolerance, true},
		{10, modTimeTolerance + 1, false},
		{10, -modTimeTolerance - 1, false},
		{11, 0, false},
		// A FAT volume an hour off after a change of daylight saving is
		// outside it: such a library is read again.
		{10, time.Hour, false},
	} {
		if got := sameStat(tt.size, at.Add(tt.d), 10, at); got != tt.want {
			t.Errorf("sameStat(size %d, %v off) = %t, want %t", tt.size, tt.d, got, tt.want)
		}
	}
}

// The index reads a file again when its size or its time changed.
func TestIndexRereadsOnEitherSign(t *testing.T) {
	for _, c := range changes {
		lib, loose := libraryWithInternalDuplicate(t)
		s := openStore(t)
		indexLibrary(t, s, lib)
		c.change(t, loose)
		finish := Run(context.Background(), Options{Destination: lib, IndexOnly: true, Store: s})
		want := int64(0)
		if c.changed {
			want = 1
		}
		if finish.Counters.Indexed != want {
			t.Errorf("%s changed: the index read %d files, want %d", c.name, finish.Counters.Indexed, want)
		}
	}
}

// A tag save is refused for a file that changed in either way since it was
// indexed, and goes ahead for one that did not.
func TestSaveTagsChecksEitherSign(t *testing.T) {
	for _, c := range changes {
		lib, ripped, _, _, s := untaggedLibrary(t)
		id := indexedFiles(t, s)[ripped].ID
		c.change(t, ripped)
		saves := 0
		save := func(*store.Store, int64, store.File) error { saves++; return nil }
		finish := SaveTags(context.Background(), Options{Destination: lib, Store: s}, []int64{id}, save)
		if c.changed && (finish.Counters.Errors != 1 || saves != 0) {
			t.Errorf("%s changed: save = %+v after %d saves, want it refused", c.name, finish, saves)
		}
		if !c.changed && (finish.Counters.Errors != 0 || saves != 1) {
			t.Errorf("%s changed: save = %+v after %d saves, want it made", c.name, finish, saves)
		}
	}
}

// A resolution is refused when a copy changed in either way.
func TestResolveDuplicatesChecksEitherSign(t *testing.T) {
	for _, c := range changes {
		lib, kept, copyPath, _, s := reviewLibrary(t)
		g := onlyGroup(t, s)
		c.change(t, copyPath)
		finish := ResolveDuplicates(context.Background(), Options{Destination: lib, Store: s},
			[]Resolution{{Kind: g.Kind, Members: g.IDs(), Keeper: indexedFiles(t, s)[kept].ID}})
		if c.changed {
			if finish.State != StateErrors || len(reviews(t, s)) != 0 {
				t.Errorf("%s changed: resolve = %+v, want it refused", c.name, finish)
			}
			mustExist(t, copyPath)
			continue
		}
		if finish.State != StateOK || finish.Counters.Duplicates != 1 {
			t.Errorf("%s changed: resolve = %+v, want the copy moved out", c.name, finish)
		}
	}
}

// Auto-resolve leaves out of a group a copy that changed in either way: the
// index no longer describes it, so the group has one copy and is not a group.
func TestAutoResolveChecksEitherSign(t *testing.T) {
	for _, c := range changes {
		lib := filepath.Join(t.TempDir(), "lib")
		writeTaggedMP3(t, songPath(lib), songTags, theAudio)
		s := openStore(t)
		indexLibrary(t, s, lib)
		c.change(t, songPath(lib))
		o := newOrganizer(Options{Source: lib, Destination: lib, Store: s})
		rows := indexedFiles(t, s)
		members := o.groupMembers([]store.File{rows[songPath(lib)]}, map[string]bool{})
		if got := len(members) == 0; got != c.changed {
			t.Errorf("%s changed: the group has %d members, want the copy left out: %t", c.name, len(members), c.changed)
		}
	}
}

// Undo skips a file that changed in either way since it was indexed, as one
// that can be undone once an index run has read it, and moves back one that
// did not.
func TestUndoChecksEitherSign(t *testing.T) {
	for _, c := range changes {
		_, lib, s, run := organizedRip(t)
		song := filepath.Join(lib, "Singer", "Album", "01 - Song.mp3")
		c.change(t, song)
		check, _ := noTags(t)
		plan, err := PlanUndo(s, run.ID, check)
		if err != nil {
			t.Fatal(err)
		}
		if !c.changed {
			if plan.Moves != 4 || len(plan.Skipped) != 0 {
				t.Errorf("%s changed: plan = %+v, want every file back", c.name, plan)
			}
			continue
		}
		if plan.Moves != 3 || len(plan.Skipped) != 1 || plan.Skipped[0].Path != song ||
			!strings.Contains(plan.Skipped[0].Reason, "changed since") || !plan.Skipped[0].Later {
			t.Errorf("%s changed: plan = %+v, want the song skipped as changed, for now", c.name, plan)
		}
	}
}
