package engine

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/zachjamesgreen/tagshelf/internal/fixtures"
	"github.com/zachjamesgreen/tagshelf/internal/store"
)

// outsideFiles is an indexed library and, outside it, a folder with a
// ripper's file and a file without tags.
func outsideFiles(t *testing.T) (lib, ripped, plain string, s *store.Store) {
	t.Helper()
	tmp := t.TempDir()
	lib = filepath.Join(tmp, "lib")
	writeTestFile(t, manualLibraryPath(lib, ".flac"), readTestFile(t, fixtures.Manual(".flac")))
	ripped = filepath.Join(tmp, "elsewhere", "ripped.flac")
	writeTestFile(t, ripped, readTestFile(t, fixtures.Path(fixtures.RipperFLAC)))
	plain = filepath.Join(tmp, "elsewhere", "plain.mp3")
	writeTestFile(t, plain, readTestFile(t, fixtures.Untagged(".mp3")))
	s = openStore(t)
	indexLibrary(t, s, lib)
	return lib, ripped, plain, s
}

func listed(t *testing.T, path string) store.File {
	t.Helper()
	f, link, err := ReadListed(path)
	if err != nil || link {
		t.Fatalf("ReadListed(%q) = link %t, %v", path, link, err)
	}
	return f
}

// TestReadListed checks what a list of files outside the index shows of
// each: its tags, size and time, and nothing that takes reading its audio.
func TestReadListed(t *testing.T) {
	_, ripped, plain, _ := outsideFiles(t)
	f := listed(t, ripped)
	info, err := os.Stat(ripped)
	if err != nil {
		t.Fatal(err)
	}
	if f.Path != ripped || f.Title != fixtures.Title || f.Artist != fixtures.Artist || f.Album != fixtures.Album || f.Format != "FLAC" ||
		f.Size != info.Size() || !f.ModTime.Equal(info.ModTime()) || f.Manual != "" || f.Sum != "" || f.ID != 0 {
		t.Errorf("the ripper's file is listed as %+v", f)
	}
	if f := listed(t, plain); f.Title != "" || f.ReadError != "" {
		t.Errorf("the file without tags is listed as %+v", f)
	}
	// A file that is no audio at all is listed, with nothing read.
	junk := filepath.Join(filepath.Dir(plain), "junk.mp3")
	writeTestFile(t, junk, "not audio")
	if f := listed(t, junk); f.Title != "" || f.Size != 9 {
		t.Errorf("the file that is no audio is listed as %+v", f)
	}
	if _, _, err := ReadListed(filepath.Join(filepath.Dir(plain), "gone.mp3")); err == nil {
		t.Error("a file that is not there is listed")
	}

	link := filepath.Join(filepath.Dir(plain), "link.flac")
	if err := os.Symlink(ripped, link); err != nil {
		t.Skipf("this system makes no links here: %v", err)
	}
	if f, isLink, err := ReadListed(link); err != nil || !isLink || f.Title != fixtures.Title {
		t.Errorf("the link is listed as %+v, link %t, %v", f, isLink, err)
	}
}

// TestSaveFilesLeavesTheFilesWhereTheyAre saves tags that make a file
// outside the library qualify: it is saved to, recorded in the library's
// history, and neither moved nor indexed.
func TestSaveFilesLeavesTheFilesWhereTheyAre(t *testing.T) {
	lib, ripped, plain, s := outsideFiles(t)
	before := len(indexedFiles(t, s))
	var c collector
	finish := SaveFiles(context.Background(), Options{Destination: lib, Store: s, Sink: c.sink},
		[]store.File{listed(t, ripped), listed(t, plain)}, overwrite(t, fixtures.Manual(".flac")))
	if finish.State != StateOK || finish.Counters.InPlace != 2 || finish.Counters.Organized != 0 || finish.Counters.Errors != 0 {
		t.Fatalf("finish = %+v, want two files tagged where they are", finish)
	}
	mustExist(t, ripped, plain)
	if now := len(indexedFiles(t, s)); now != before {
		t.Errorf("the index holds %d files after the save, %d before", now, before)
	}
	run := lastRun(t, s)
	if run.Kind != store.KindTUIAction || run.ID != finish.RunID {
		t.Errorf("the save is recorded as %+v", run)
	}
	if moves, err := s.Moves(run.ID); err != nil || len(moves) != 0 {
		t.Errorf("the run recorded %d moves (%v), want none", len(moves), err)
	}
}

// A file that changed since it was listed, one that is gone, and a link are
// not saved to; a save that fails is an error of the run and the rest go on.
func TestSaveFilesRefusals(t *testing.T) {
	lib, ripped, plain, s := outsideFiles(t)
	changed := listed(t, ripped)
	writeTestFile(t, ripped, readTestFile(t, fixtures.Path(fixtures.RipperFLAC))+"more")
	gone := listed(t, plain)
	gone.Path = filepath.Join(filepath.Dir(plain), "gone.mp3")
	files := []store.File{changed, gone}

	link := filepath.Join(filepath.Dir(plain), "link.mp3")
	if err := os.Symlink(plain, link); err == nil {
		f, _, err := ReadListed(link)
		if err != nil {
			t.Fatal(err)
		}
		files = append(files, f)
	}
	saves := 0
	save := func(*store.Store, int64, store.File) error { saves++; return nil }
	finish := SaveFiles(context.Background(), Options{Destination: lib, Store: s}, files, save)
	if finish.State != StateErrors || finish.Counters.Errors != int64(len(files)) || saves != 0 {
		t.Errorf("finish = %+v after %d saves, want %d errors and no save", finish, saves, len(files))
	}

	failing := func(_ *store.Store, _ int64, f store.File) error {
		if f.Path == plain {
			return errors.New("disk full")
		}
		return nil
	}
	finish = SaveFiles(context.Background(), Options{Destination: lib, Store: s}, []store.File{listed(t, plain), listed(t, ripped)}, failing)
	if finish.State != StateErrors || finish.Counters.Errors != 1 || finish.Counters.InPlace != 1 {
		t.Errorf("with one save failing: finish = %+v", finish)
	}

	// Without the library's database there is nowhere to record it.
	if finish := SaveFiles(context.Background(), Options{Destination: lib}, []store.File{listed(t, plain)}, save); finish.State != StateNotStarted {
		t.Errorf("without a database: finish = %+v", finish)
	}
}
