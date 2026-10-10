//go:build windows

package engine

import (
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
)

// lockPath opens a file or folder with no sharing, so that nothing else can
// read or list it until the test ends or the returned function is called. It
// skips the test on a machine where a file held that way can still be opened.
func lockPath(t *testing.T, path string) func() {
	t.Helper()
	name, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		t.Fatal(err)
	}
	h, err := syscall.CreateFile(name, syscall.GENERIC_READ, 0, nil, syscall.OPEN_EXISTING, syscall.FILE_FLAG_BACKUP_SEMANTICS, 0)
	if err != nil {
		t.Fatal(err)
	}
	closed := false
	unlock := func() {
		if !closed {
			closed = true
			syscall.CloseHandle(h)
		}
	}
	t.Cleanup(unlock)
	// Some machines let a file held this way be opened all the same (GitHub's
	// Windows runners do). A test of what tagshelf does with a file it cannot
	// read has nothing to test there.
	if info, err := os.Stat(path); err == nil && !info.IsDir() {
		if f, err := os.Open(path); err == nil {
			f.Close()
			t.Skipf("this machine lets %s be opened while it is held open without sharing", filepath.Base(path))
		}
	}
	return unlock
}

// A folder that cannot be listed during one run keeps the index rows of the
// files in it: a walk cut short says nothing about what it did not reach.
// Deleting the rows would give the files new IDs when they are read again,
// and reviews would stop covering them.
func TestUnreadableFolderKeepsItsRows(t *testing.T) {
	lib := filepath.Join(t.TempDir(), "lib")
	writeTaggedMP3(t, songPath(lib), songTags, theAudio)
	gone := filepath.Join(lib, "_Untagged", "gone.mp3")
	writeTestFile(t, gone, "no tags")
	s := openStore(t)
	indexLibrary(t, s, lib)
	before := indexedFiles(t, s)
	if len(before) != 2 {
		t.Fatalf("index = %v", before)
	}
	// A file that really went is still forgotten, in a root that was read.
	if err := os.Remove(gone); err != nil {
		t.Fatal(err)
	}

	unlock := lockPath(t, filepath.Join(lib, "Singer"))
	var c collector
	finish := Run(context.Background(), Options{Destination: lib, IndexOnly: true, Store: s, Sink: c.sink})
	unlock()
	errs := c.fileErrors()
	if finish.State != StateErrors || len(errs) != 1 || errs[0].Op != OpWalk || errs[0].Path != filepath.Join(lib, "Singer") {
		t.Errorf("index run = %+v with errors %+v, want the locked folder reported", finish, errs)
	}
	after := indexedFiles(t, s)
	if after[songPath(lib)].ID != before[songPath(lib)].ID {
		t.Errorf("the row of the song in the locked folder is gone or new: %+v", after)
	}
	if _, ok := after[gone]; ok {
		t.Errorf("the row of a file that is gone from a folder that was read is still there")
	}
}

// System Volume Information cannot be listed. At the top of a volume it is
// left out before the walk tries, so that a library at M:\ does not end
// every run with an error, and its index can still forget files that went.
func TestUnreadableSystemFolderIsNotAnError(t *testing.T) {
	lib := filepath.Join(t.TempDir(), "M")
	volumeRootAt(t, lib)
	writeTaggedMP3(t, songPath(lib), songTags, theAudio)
	gone := filepath.Join(lib, "_Untagged", "gone.mp3")
	writeTestFile(t, gone, "no tags")
	system := filepath.Join(lib, "System Volume Information")
	writeTestFile(t, filepath.Join(system, "tracking.log"), "log")
	s := openStore(t)
	lockPath(t, system)

	var c collector
	if finish := Run(context.Background(), Options{Destination: lib, IndexOnly: true, Store: s, Sink: c.sink}); finish.State != StateOK {
		t.Fatalf("index run = %+v with errors %+v, want the system folder left out without a word", finish, c.fileErrors())
	}
	if err := os.Remove(gone); err != nil {
		t.Fatal(err)
	}
	if finish := Run(context.Background(), Options{Source: lib, Destination: lib, Store: s}); finish.State != StateOK {
		t.Errorf("reorganize = %+v", finish)
	}
	if rows := indexedFiles(t, s); len(rows) != 1 {
		t.Errorf("index = %v, want the row of the file that went deleted: the library was walked to the end", rows)
	}
}

// A junction to a folder is neither followed nor moved: the files behind it
// live somewhere else, and moving the junction as a file would break it. It
// used to be handed on as a non-audio file and moved to _Other.
func TestWalkLeavesJunctionsAlone(t *testing.T) {
	tmp := t.TempDir()
	source, lib, elsewhere := filepath.Join(tmp, "src"), filepath.Join(tmp, "lib"), filepath.Join(tmp, "elsewhere")
	writeTaggedMP3(t, filepath.Join(source, "Rip", "song.mp3"), songTags, theAudio)
	behind := filepath.Join(elsewhere, "other.mp3")
	writeTaggedMP3(t, behind, id3Tags{albumArtist: "Singer", album: "Album", title: "Other", track: "2"}, "other audio")
	link := filepath.Join(source, "Rip", "Link")
	if out, err := exec.Command("cmd", "/c", "mklink", "/J", link, elsewhere).CombinedOutput(); err != nil {
		t.Skipf("cannot make a junction: %v: %s", err, out)
	}

	var c collector
	finish := Run(context.Background(), Options{Source: source, Destination: lib, Workers: 1, Sink: c.sink})
	if finish.State != StateOK || finish.Counters.TotalFound != 1 || finish.Counters.OtherFound != 0 {
		t.Errorf("run = %+v, want only the song in the source found", finish)
	}
	mustExist(t, behind, songPath(lib))
	if info, err := os.Lstat(link); err != nil || info.IsDir() {
		t.Errorf("the junction after the run: %v, %v; want it where it was", info, err)
	}
	if entries, err := os.ReadDir(link); err != nil || len(entries) != 1 {
		t.Errorf("the junction lists %v, %v; want it to lead to its folder still", entries, err)
	}
	if w := c.warnings(WarnLinkSkipped); len(w) != 1 || w[0].Path != link {
		t.Errorf("warnings = %+v, want the junction reported as left alone", w)
	}
	mustNotExist(t, filepath.Join(lib, "_Other"), filepath.Join(lib, "Singer", "Album", "02 - Other.mp3"))
}

// A copy that cannot be moved to the duplicates folder, because another
// program holds it open, leaves its group without a review, so that it stays
// on the Duplicates tab: for a run that resolves on its own and for a person's
// resolution alike. Nothing is copied to find that out.
func TestGroupWhoseCopyCannotMoveIsNotReviewed(t *testing.T) {
	hold := func(t *testing.T, path string) {
		t.Helper()
		// Like most programs, os.Open shares reading and writing but not
		// deleting, which is what a rename needs.
		f, err := os.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { f.Close() })
	}
	noCopies := func(t *testing.T) {
		t.Helper()
		watchCopies(t, func(dst io.Writer, src io.Reader) (int64, error) {
			t.Error("the held file was copied")
			return io.Copy(dst, src)
		})
	}

	t.Run("auto-resolve", func(t *testing.T) {
		lib := filepath.Join(t.TempDir(), "lib")
		writeTaggedMP3(t, songPath(lib), songTags, theAudio)
		s := openStore(t)
		indexLibrary(t, s, lib)
		// Dropped in since, so the run adds it to the group.
		loose := filepath.Join(lib, "_Untagged", "loose.mp3")
		writeTestFile(t, loose, theAudio)
		hold(t, loose)
		noCopies(t)

		finish := Run(context.Background(), Options{Source: lib, Destination: lib, Workers: 1, Store: s})
		if finish.State != StateErrors || finish.Counters.Duplicates != 0 || finish.NeedReview != 1 {
			t.Errorf("reorganize = %+v, want the move failed and the group left to review", finish)
		}
		mustExist(t, loose, songPath(lib))
		if got := reviews(t, s); len(got) != 0 {
			t.Errorf("reviews = %+v, want none for a group that was not resolved", got)
		}
	})

	t.Run("a person's resolution", func(t *testing.T) {
		lib, kept, copyPath, _, s := reviewLibrary(t)
		g := onlyGroup(t, s)
		hold(t, copyPath)
		noCopies(t)

		finish := ResolveDuplicates(context.Background(), Options{Destination: lib, Store: s},
			[]Resolution{{Kind: g.Kind, Members: g.IDs(), Keeper: indexedFiles(t, s)[kept].ID}})
		if finish.State != StateErrors || finish.Counters.Duplicates != 0 || finish.NeedReview != 1 {
			t.Errorf("resolve = %+v, want the move failed and the group left to review", finish)
		}
		mustExist(t, copyPath, kept)
		if got := reviews(t, s); len(got) != 0 {
			t.Errorf("reviews = %+v, want none", got)
		}
	})
}

// An audio file the index cannot open is an error of the run, and gets no
// row: there is nothing to say about it.
func TestIndexOfAFileThatCannotBeOpened(t *testing.T) {
	lib := filepath.Join(t.TempDir(), "lib")
	writeTaggedMP3(t, songPath(lib), songTags, theAudio)
	lockPath(t, songPath(lib))
	s := openStore(t)
	var c collector
	finish := Run(context.Background(), Options{Destination: lib, IndexOnly: true, Store: s, Sink: c.sink})
	errs := c.fileErrors()
	if finish.State != StateErrors || len(errs) != 1 || errs[0].Op != OpIndex || errs[0].Path != songPath(lib) {
		t.Errorf("index run = %+v with errors %+v, want the file reported", finish, errs)
	}
	if rows := indexedFiles(t, s); len(rows) != 0 {
		t.Errorf("index = %v, want no row for a file that could not be opened", rows)
	}
}
