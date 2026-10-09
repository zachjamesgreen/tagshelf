package engine

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"tagshelf/internal/fixtures"
	"tagshelf/internal/store"
)

// overwrite is a tag save for the tests: it replaces the file with a fixture
// that has the tags a real save would have left.
func overwrite(t *testing.T, fixture string) TagSave {
	return func(_ *store.Store, _ int64, f store.File) error {
		return os.WriteFile(f.Path, []byte(readTestFile(t, fixture)), 0644)
	}
}

// untaggedLibrary is an indexed library whose untagged folder holds a rip
// with ripper tags, artwork next to it, and a marked file without an album.
func untaggedLibrary(t *testing.T) (lib, ripped, art, noAlbum string, s *store.Store) {
	t.Helper()
	lib = filepath.Join(t.TempDir(), "lib")
	ripped = filepath.Join(lib, "_Untagged", "rip", "ripped.flac")
	writeTestFile(t, ripped, readTestFile(t, fixtures.Path(fixtures.RipperFLAC)))
	art = filepath.Join(lib, "_Untagged", "rip", "cover.jpg")
	writeTestFile(t, art, "art")
	noAlbum = filepath.Join(lib, "_Untagged", "noalbum.flac")
	writeTestFile(t, noAlbum, readTestFile(t, fixtures.Path(fixtures.ManualNoAlbumFLAC)))
	s = openStore(t)
	indexLibrary(t, s, lib)
	return lib, ripped, art, noAlbum, s
}

func TestSaveTagsMovesFilesThatQualify(t *testing.T) {
	lib, ripped, art, noAlbum, s := untaggedLibrary(t)
	files := indexedFiles(t, s)
	ids := []int64{files[ripped].ID, files[noAlbum].ID}

	var savedIn []int64
	save := func(st *store.Store, runID int64, f store.File) error {
		savedIn = append(savedIn, runID)
		if f.Path == ripped {
			return overwrite(t, fixtures.Manual(".flac"))(st, runID, f)
		}
		return nil // the marked file is left as it is
	}
	var c collector
	finish := SaveTags(context.Background(), Options{Destination: lib, Store: s, Sink: c.sink}, ids, save)
	if finish.State != StateOK || finish.Counters.Organized != 1 || finish.Counters.LeftInPlace != 1 {
		t.Fatalf("finish = %+v, want one organized and one left", finish)
	}

	dst := manualLibraryPath(lib, ".flac")
	movedArt := filepath.Join(filepath.Dir(dst), "cover.jpg")
	mustExist(t, dst, movedArt, noAlbum)
	mustNotExist(t, ripped, art, filepath.Dir(ripped))

	runs, err := s.Runs()
	if err != nil {
		t.Fatal(err)
	}
	run := runs[len(runs)-1]
	if run.Kind != store.KindTUIAction || run.State != store.StateOK {
		t.Errorf("run = %+v, want a finished tui-action", run)
	}
	if len(savedIn) != 2 || savedIn[0] != run.ID || savedIn[1] != run.ID {
		t.Errorf("saves were made under runs %v, want both under run %d", savedIn, run.ID)
	}
	moves, err := s.Moves(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(moves) != 2 || moves[0].From != ripped || moves[0].To != dst || moves[0].Reason != store.ReasonOrganized ||
		moves[1].From != art || moves[1].To != movedArt || moves[1].Reason != store.ReasonCompanion {
		t.Errorf("moves = %+v", moves)
	}

	after := indexedFiles(t, s)
	row := after[dst]
	info, err := os.Stat(dst)
	if err != nil {
		t.Fatal(err)
	}
	if row.ID != files[ripped].ID || row.RootKind != store.RootLibrary || row.Manual == "" ||
		row.Size != info.Size() || !row.ModTime.Equal(info.ModTime()) {
		t.Errorf("the moved file's index row = %+v, want the saved file in the library", row)
	}
	if row := after[noAlbum]; row.RootKind != store.RootUntagged {
		t.Errorf("the file without an album: index row = %+v, want it still untagged", row)
	}
	if c.placed()[ripped] != Organized || c.placed()[noAlbum] != Left {
		t.Errorf("placed = %+v", c.placed())
	}
}

// A save that fails leaves its file where it was and the run with an error;
// the other files still go.
func TestSaveTagsRecordsFailedSaves(t *testing.T) {
	lib, ripped, _, noAlbum, s := untaggedLibrary(t)
	files := indexedFiles(t, s)
	save := func(st *store.Store, runID int64, f store.File) error {
		if f.Path == noAlbum {
			return errors.New("disk full")
		}
		return overwrite(t, fixtures.Manual(".flac"))(st, runID, f)
	}
	finish := SaveTags(context.Background(), Options{Destination: lib, Store: s},
		[]int64{files[noAlbum].ID, files[ripped].ID}, save)
	if finish.State != StateErrors || finish.Counters.Errors != 1 || finish.Counters.Organized != 1 {
		t.Fatalf("finish = %+v, want one error and one organized", finish)
	}
	mustExist(t, noAlbum, manualLibraryPath(lib, ".flac"))
	runs, _ := s.Runs()
	errs, err := s.RunErrors(runs[len(runs)-1].ID)
	if err != nil || len(errs) != 1 || errs[0].Path != noAlbum {
		t.Errorf("run errors = %+v, %v", errs, err)
	}
}

// A file that changed since it was indexed, or is not in the index, is not
// saved to: the person chose about another file.
func TestSaveTagsRefusesChangedFiles(t *testing.T) {
	lib, ripped, _, _, s := untaggedLibrary(t)
	id := indexedFiles(t, s)[ripped].ID
	writeTestFile(t, ripped, readTestFile(t, fixtures.Path(fixtures.ManualNoAlbumFLAC))+"more")
	saves := 0
	save := func(*store.Store, int64, store.File) error { saves++; return nil }
	finish := SaveTags(context.Background(), Options{Destination: lib, Store: s}, []int64{id, 9999}, save)
	if finish.State != StateErrors || finish.Counters.Errors != 2 || saves != 0 {
		t.Errorf("finish = %+v after %d saves, want two errors and no save", finish, saves)
	}
	mustExist(t, ripped)
}

func TestSaveTagsRefusals(t *testing.T) {
	lib, ripped, _, _, s := untaggedLibrary(t)
	id := indexedFiles(t, s)[ripped].ID
	save := func(*store.Store, int64, store.File) error {
		t.Error("a save was made")
		return nil
	}
	for name, opts := range map[string]Options{
		"no database": {Destination: lib},
		"dry run":     {Destination: lib, Store: s, DryRun: true},
	} {
		if finish := SaveTags(context.Background(), opts, []int64{id}, save); finish.State != StateNotStarted || finish.Err == nil {
			t.Errorf("%s: finish = %+v, want it not started", name, finish)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if finish := SaveTags(ctx, Options{Destination: lib, Store: s}, []int64{id}, save); finish.State != StateInterrupted {
		t.Errorf("cancelled: finish = %+v, want it interrupted", finish)
	}
}
