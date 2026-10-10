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

// A group of files that were all there before the run is left for review
// however many of them the run reads again: reading a file does not add it.
// Each of these used to hand the old group to auto-resolve, which moved the
// loose copy to _Duplicates.
func TestRereadFilesAreNotAdded(t *testing.T) {
	check := func(t *testing.T, lib, loose string, finish RunFinished) {
		t.Helper()
		if finish.Counters.Duplicates != 0 || finish.NeedReview != 1 {
			t.Errorf("run = %+v, want nothing moved and the old group left for review", finish)
		}
		mustExist(t, songPath(lib), loose)
	}

	t.Run("rescan", func(t *testing.T) {
		lib, loose := libraryWithInternalDuplicate(t)
		s := openStore(t)
		indexLibrary(t, s, lib)
		finish := Run(context.Background(), Options{Source: lib, Destination: lib, Store: s, Rescan: true})
		if finish.Counters.Indexed != 2 {
			t.Fatalf("run = %+v, want both files read again", finish)
		}
		check(t, lib, loose, finish)
		if got := reviews(t, s); len(got) != 0 {
			t.Errorf("reviews = %+v, want none", got)
		}
	})

	t.Run("the run after an interrupted first index", func(t *testing.T) {
		lib, loose := libraryWithInternalDuplicate(t)
		s := openStore(t)
		ctx, cancel := context.WithCancel(context.Background())
		was := afterIndexRead
		afterIndexRead = func(string) { cancel() }
		first := Run(ctx, Options{Destination: lib, IndexOnly: true, Workers: 1, Store: s})
		afterIndexRead = was
		if first.State != StateInterrupted || len(indexedFiles(t, s)) != 1 {
			t.Fatalf("first index = %+v with %d rows, want it stopped after one file", first, len(indexedFiles(t, s)))
		}
		check(t, lib, loose, Run(context.Background(), Options{Source: lib, Destination: lib, Store: s}))
		// The library has now been walked to the end: a copy dropped in
		// after that is new, and is resolved.
		writeTestFile(t, filepath.Join(lib, "_Untagged", "dropped.mp3"), theAudio)
		finish := Run(context.Background(), Options{Source: lib, Destination: lib, Store: s})
		if finish.Counters.Duplicates == 0 {
			t.Errorf("run = %+v, want the group resolved now that a copy was added", finish)
		}
	})

	t.Run("a file whose time alone changed", func(t *testing.T) {
		lib, loose := libraryWithInternalDuplicate(t)
		s := openStore(t)
		indexLibrary(t, s, lib)
		touch(t, loose, 3*modTimeTolerance)
		finish := Run(context.Background(), Options{Source: lib, Destination: lib, Store: s})
		if finish.Counters.Indexed != 1 {
			t.Fatalf("run = %+v, want the touched file read again", finish)
		}
		check(t, lib, loose, finish)
	})

	// A file read again with other audio is added to the group it now joins.
	t.Run("a file whose audio changed", func(t *testing.T) {
		lib := filepath.Join(t.TempDir(), "lib")
		writeTaggedMP3(t, songPath(lib), songTags, theAudio)
		loose := filepath.Join(lib, "_Untagged", "loose.mp3")
		writeTestFile(t, loose, strings.Repeat("other audio ", 40))
		s := openStore(t)
		indexLibrary(t, s, lib)
		writeTestFile(t, loose, theAudio)
		finish := Run(context.Background(), Options{Source: lib, Destination: lib, Store: s})
		if finish.Counters.Duplicates != 1 || finish.NeedReview != 0 {
			t.Errorf("run = %+v, want the file that became a copy moved out", finish)
		}
		mustExist(t, songPath(lib), filepath.Join(lib, "_Duplicates", "loose.mp3"))
	})
}

// touch moves a file's modification time on by d, leaving its content.
func touch(t *testing.T, path string, d time.Duration) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	at := info.ModTime().Add(d)
	if err := os.Chtimes(path, at, at); err != nil {
		t.Fatal(err)
	}
}

// A person chose to keep two copies. When a third arrives the group needs
// review again, and it is a person who reviews it: auto-resolve used to keep
// one copy and move the other two out, one of them a copy kept on purpose.
func TestKeepBothIsNotOverriddenWhenACopyArrives(t *testing.T) {
	lib, loose := libraryWithInternalDuplicate(t)
	s := openStore(t)
	indexLibrary(t, s, lib)
	rows := indexedFiles(t, s)
	members := []int64{rows[songPath(lib)].ID, rows[loose].ID}
	if err := s.RecordReview(store.Review{Kind: store.GroupExact, Members: members, Outcome: store.OutcomeKeepBoth, By: store.ByUser}); err != nil {
		t.Fatal(err)
	}

	source := filepath.Join(t.TempDir(), "src")
	incoming := filepath.Join(source, "third.mp3")
	writeTestFile(t, incoming, theAudio)
	finish := Run(context.Background(), Options{Source: source, Destination: lib, Store: s})
	if finish.Counters.Duplicates != 0 || finish.Counters.Untagged != 1 || finish.NeedReview != 1 {
		t.Errorf("run = %+v, want the third copy placed and the group of three left for review", finish)
	}
	mustExist(t, songPath(lib), loose, filepath.Join(lib, "_Untagged", "third.mp3"))
	if got := reviews(t, s); len(got) != 1 {
		t.Errorf("reviews = %+v, want only the person's", got)
	}
}

// A resolved group does not stand in the way of resolving the next copy of
// the same audio: of its members only the keeper is left in the library.
func TestResolvedGroupResolvesAgainWhenACopyArrives(t *testing.T) {
	lib := filepath.Join(t.TempDir(), "lib")
	writeTaggedMP3(t, songPath(lib), songTags, theAudio)
	s := openStore(t)
	indexLibrary(t, s, lib)
	for i, name := range []string{"second.mp3", "third.mp3"} {
		source := filepath.Join(t.TempDir(), "src")
		writeTestFile(t, filepath.Join(source, name), theAudio)
		finish := Run(context.Background(), Options{Source: source, Destination: lib, Store: s})
		if finish.Counters.Duplicates != 1 || finish.NeedReview != 0 {
			t.Fatalf("copy %d: run = %+v, want it moved out", i+2, finish)
		}
		mustExist(t, songPath(lib), filepath.Join(lib, "_Duplicates", name))
	}
	if got := reviews(t, s); len(got) != 2 {
		t.Errorf("reviews = %+v, want one for each copy that came", got)
	}
}

// A file that an undo put back in its source folder keeps an index row under
// the source root. A later reorganize of the library leaves it alone, even
// when the library has gained a copy of the same audio: the file is outside
// the library, in a folder the run was never pointed at.
func TestReorganizeLeavesAnOldSourceAlone(t *testing.T) {
	tmp := t.TempDir()
	src, lib := filepath.Join(tmp, "src"), filepath.Join(tmp, "lib")
	inSource := filepath.Join(src, "song.mp3")
	writeTestFile(t, inSource, theAudio)
	s := openStore(t)
	if finish := Run(context.Background(), Options{Source: src, Destination: lib, Store: s}); finish.State != StateOK || finish.Counters.Untagged != 1 {
		t.Fatalf("organize = %+v", finish)
	}
	check, restore := noTags(t)
	if finish := Undo(context.Background(), Options{Destination: lib, Store: s}, lastRun(t, s).ID, check, restore); finish.State != StateOK {
		t.Fatalf("undo = %+v", finish)
	}
	if row := indexedFiles(t, s)[inSource]; row.RootKind != store.RootSource {
		t.Fatalf("row of the file put back = %+v, want one under the source root", row)
	}

	// A tagged copy of the same audio is put in the library by hand.
	writeTaggedMP3(t, songPath(lib), songTags, theAudio)
	finish := Run(context.Background(), Options{Source: lib, Destination: lib, Store: s})
	if finish.Counters.Duplicates != 0 || finish.State != StateOK {
		t.Errorf("reorganize = %+v, want nothing put aside", finish)
	}
	mustExist(t, inSource, songPath(lib))
	mustNotExist(t, filepath.Join(lib, "_Duplicates", "song.mp3"))
}

// An untagged folder on another drive that is not there for one run keeps
// its rows, and the run says so: an unplugged drive looks just like an empty
// folder. The rows used to be deleted, and the files got new IDs when the
// drive was back, so that a keep-both review no longer covered them.
func TestMissingRootKeepsItsRows(t *testing.T) {
	tmp := t.TempDir()
	lib, untagged, away := filepath.Join(tmp, "lib"), filepath.Join(tmp, "drive", "Untagged"), filepath.Join(tmp, "drive", "Unplugged")
	writeTaggedMP3(t, songPath(lib), songTags, theAudio)
	loose := filepath.Join(untagged, "loose.mp3")
	writeTestFile(t, loose, theAudio)
	s := openStore(t)
	opts := Options{Destination: lib, Untagged: untagged, IndexOnly: true, Store: s}
	if finish := Run(context.Background(), opts); finish.State != StateOK || finish.Counters.Indexed != 2 {
		t.Fatalf("first index = %+v", finish)
	}
	before := indexedFiles(t, s)
	members := []int64{before[songPath(lib)].ID, before[loose].ID}
	if err := s.RecordReview(store.Review{Kind: store.GroupExact, Members: members, Outcome: store.OutcomeKeepBoth, By: store.ByUser}); err != nil {
		t.Fatal(err)
	}

	if err := os.Rename(untagged, away); err != nil {
		t.Fatal(err)
	}
	var c collector
	opts.Sink = c.sink
	finish := Run(context.Background(), opts)
	if finish.State != StateErrors || finish.Counters.Errors != 1 {
		t.Errorf("index with the folder gone = %+v, want it to end with the one error", finish)
	}
	errs := c.fileErrors()
	if len(errs) != 1 || errs[0].Op != OpWalkRoot || errs[0].Path != untagged || !strings.Contains(errs[0].Err.Error(), "is not there") {
		t.Errorf("errors = %+v, want the missing untagged folder reported", errs)
	}
	if got := indexedFiles(t, s)[loose]; got.ID != before[loose].ID {
		t.Errorf("row of the file in the missing folder = %+v, want it kept as row %d", got, before[loose].ID)
	}

	if err := os.Rename(away, untagged); err != nil {
		t.Fatal(err)
	}
	opts.Sink = nil
	finish = Run(context.Background(), opts)
	if finish.State != StateOK || finish.Counters.Indexed != 0 || finish.NeedReview != 0 {
		t.Errorf("index with the folder back = %+v, want nothing read again and the review still covering the group", finish)
	}
	if got := indexedFiles(t, s)[loose]; got.ID != before[loose].ID {
		t.Errorf("row after the folder came back = %+v, want row %d", got, before[loose].ID)
	}
}

// A root that is missing and holds no rows is simply not there yet, as the
// untagged folder of a new library is: no error.
func TestMissingRootWithoutRowsIsNotThereYet(t *testing.T) {
	lib := filepath.Join(t.TempDir(), "lib")
	writeTaggedMP3(t, songPath(lib), songTags, theAudio)
	s := openStore(t)
	for i := 0; i < 2; i++ {
		if finish := Run(context.Background(), Options{Destination: lib, IndexOnly: true, Store: s}); finish.State != StateOK {
			t.Errorf("index run %d of a library without an untagged folder = %+v", i+1, finish)
		}
	}
}
