package engine

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/zachjamesgreen/tagshelf/internal/fixtures"
)

// errOtherVolume is the rename error otherVolume stands in with.
var errOtherVolume = errors.New("the paths are on different volumes (test)")

// otherVolume makes every rename from one folder to another fail for the
// length of the test as a rename to another volume does, so that moves take
// the copy path; a rename within a folder, as of the finished copy onto its
// name, works. landed, if not nil, is told each path a rename put a file at.
func otherVolume(t *testing.T, landed func(path string)) {
	t.Helper()
	wasRename, wasCross := renameFile, crossVolume
	t.Cleanup(func() { renameFile, crossVolume = wasRename, wasCross })
	crossVolume = func(err error) bool { return errors.Is(err, errOtherVolume) }
	renameFile = func(from, to string) error {
		if filepath.Dir(from) != filepath.Dir(to) {
			return &os.LinkError{Op: "rename", Old: from, New: to, Err: errOtherVolume}
		}
		if err := os.Rename(from, to); err != nil {
			return err
		}
		if landed != nil {
			landed(to)
		}
		return nil
	}
}

// watchCopies replaces copyData for the length of the test.
func watchCopies(t *testing.T, copy func(dst io.Writer, src io.Reader) (int64, error)) {
	t.Helper()
	was := copyData
	t.Cleanup(func() { copyData = was })
	copyData = copy
}

// ripToOrganize is a source with one tagged song, dated well in the past,
// and the library it goes to.
func ripToOrganize(t *testing.T) (source, lib, song string, modTime time.Time) {
	t.Helper()
	tmp := t.TempDir()
	source, lib = filepath.Join(tmp, "src"), filepath.Join(tmp, "lib")
	song = filepath.Join(source, "rip", "song.mp3")
	writeTaggedMP3(t, song, songTags, theAudio)
	modTime = time.Date(2019, 3, 14, 9, 26, 53, 0, time.UTC)
	if err := os.Chtimes(song, modTime, modTime); err != nil {
		t.Fatal(err)
	}
	return source, lib, song, modTime
}

// A move to another volume, the common case for this tool, copies the file
// and then removes the source: the library's file has the bytes and the
// time, the source is gone, and nothing else is left in the library.
func TestMoveToAnotherVolume(t *testing.T) {
	source, lib, song, modTime := ripToOrganize(t)
	want := readTestFile(t, song)
	otherVolume(t, nil)

	var c collector
	finish := Run(context.Background(), Options{Source: source, Destination: lib, Workers: 1, Sink: c.sink})
	if finish.State != StateOK || finish.Counters.Organized != 1 {
		t.Fatalf("run = %+v (errors %+v), want the song organized", finish, c.fileErrors())
	}
	if got := readTestFile(t, songPath(lib)); got != want {
		t.Errorf("the copy holds %d bytes, want the song's %d", len(got), len(want))
	}
	if info, err := os.Stat(songPath(lib)); err != nil || !info.ModTime().Equal(modTime) {
		t.Errorf("the copy's time = %v, %v; want the song's %v", info.ModTime(), err, modTime)
	}
	mustNotExist(t, song)
	if got, want := exactNames(t, lib), []string{"Singer/Album/01 - Song.mp3"}; !reflect.DeepEqual(got, want) {
		t.Errorf("library = %q, want %q", got, want)
	}
}

// While the copy is being written, the name the file will have holds only
// the empty placeholder of the claim, and the data goes to a file tagshelf
// names as its own: a process that ends here leaves no part of a song under
// a name of the library.
func TestCopyToAnotherVolumeIsWrittenUnderATemporaryName(t *testing.T) {
	source, lib, _, _ := ripToOrganize(t)
	otherVolume(t, nil)
	var during []string
	var placeholder int64 = -1
	watchCopies(t, func(dst io.Writer, src io.Reader) (int64, error) {
		during = exactNames(t, lib)
		if info, err := os.Stat(songPath(lib)); err == nil {
			placeholder = info.Size()
		}
		return io.Copy(dst, src)
	})

	if finish := Run(context.Background(), Options{Source: source, Destination: lib, Workers: 1}); finish.State != StateOK {
		t.Fatalf("run = %+v", finish)
	}
	if len(during) != 2 || during[1] != "Singer/Album/01 - Song.mp3" ||
		!strings.HasPrefix(during[0], "Singer/Album/.tagshelf-move-") || !strings.HasSuffix(during[0], ".mp3") {
		t.Errorf("library during the copy = %q, want the claimed name and a .tagshelf-move- copy", during)
	}
	if placeholder != 0 {
		t.Errorf("the claimed name held %d bytes during the copy, want the empty placeholder", placeholder)
	}
}

// A copy that fails (a full disk, say) leaves the source whole and no part of
// a file in the library; so does a finished copy that cannot be put in place.
func TestFailedCopyToAnotherVolumeLeavesTheSource(t *testing.T) {
	for _, failing := range []string{"the copy", "the rename of the copy"} {
		source, lib, song, _ := ripToOrganize(t)
		want := readTestFile(t, song)
		otherVolume(t, nil)
		switch failing {
		case "the copy":
			watchCopies(t, func(dst io.Writer, src io.Reader) (int64, error) {
				io.CopyN(dst, src, 10)
				return 10, errors.New("disk full")
			})
		case "the rename of the copy":
			renameFile = func(from, to string) error {
				return &os.LinkError{Op: "rename", Old: from, New: to, Err: errOtherVolume}
			}
		}

		var c collector
		finish := Run(context.Background(), Options{Source: source, Destination: lib, Workers: 1, Sink: c.sink})
		errs := c.fileErrors()
		if finish.State != StateErrors || len(errs) != 1 || errs[0].Op != OpMove || errs[0].Path != song {
			t.Errorf("%s fails: run = %+v with errors %+v, want one OpMove for the song", failing, finish, errs)
		}
		if got := readTestFile(t, song); got != want {
			t.Errorf("%s fails: the source holds %d bytes, want its %d", failing, len(got), len(want))
		}
		if got := exactNames(t, lib); len(got) != 0 {
			t.Errorf("%s fails: library = %q, want nothing left in it", failing, got)
		}
	}
}

// A rename that fails for any other reason than another volume is an error
// and nothing is copied: a file another program holds open would be copied
// in full and the copy deleted again, on every run.
func TestFailedRenameIsNotCopied(t *testing.T) {
	source, lib, song, _ := ripToOrganize(t)
	wasRename := renameFile
	t.Cleanup(func() { renameFile = wasRename })
	renameFile = func(from, to string) error {
		return &os.LinkError{Op: "rename", Old: from, New: to, Err: fs.ErrPermission}
	}
	copies := 0
	watchCopies(t, func(dst io.Writer, src io.Reader) (int64, error) {
		copies++
		return io.Copy(dst, src)
	})

	var c collector
	finish := Run(context.Background(), Options{Source: source, Destination: lib, Workers: 1, Sink: c.sink})
	errs := c.fileErrors()
	if finish.State != StateErrors || len(errs) != 1 || errs[0].Op != OpMove || !errors.Is(errs[0].Err, fs.ErrPermission) {
		t.Errorf("run = %+v with errors %+v, want the rename's error as one OpMove", finish, errs)
	}
	if copies != 0 {
		t.Errorf("the file was copied %d times, want no copy", copies)
	}
	mustExist(t, song)
	if got := exactNames(t, lib); len(got) != 0 {
		t.Errorf("library = %q, want nothing left in it", got)
	}
}

// A read-only source keeps its attribute and its time through a copy, with
// no warning. (With a writable source the permission check cannot fail on
// Windows: every new file has those bits.)
func TestCopyFileKeepsReadOnly(t *testing.T) {
	dir := t.TempDir()
	src, dst := filepath.Join(dir, "src.flac"), filepath.Join(dir, "dst.flac")
	writeTestFile(t, src, readTestFile(t, fixtures.Path(fixtures.RipperFLAC)))
	want := time.Date(2019, 3, 14, 9, 26, 53, 0, time.UTC)
	if err := os.Chtimes(src, want, want); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(src, 0444); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(src, 0644); os.Chmod(dst, 0644) })

	if err := copyFile(src, dst, func(w Warning) { t.Errorf("unexpected warning: %+v", w) }); err != nil {
		t.Fatal(err)
	}
	srcInfo, err := os.Stat(src)
	if err != nil {
		t.Fatal(err)
	}
	dstInfo, err := os.Stat(dst)
	if err != nil {
		t.Fatal(err)
	}
	if dstInfo.Mode().Perm() != srcInfo.Mode().Perm() || dstInfo.Mode().Perm()&0200 != 0 {
		t.Errorf("the copy's mode = %v, want the read-only source's %v", dstInfo.Mode().Perm(), srcInfo.Mode().Perm())
	}
	if !dstInfo.ModTime().Equal(want) {
		t.Errorf("the copy's time = %v, want %v", dstInfo.ModTime(), want)
	}
}

// A copy whose data cannot be written leaves nothing at its destination.
func TestCopyFileThatFailsRemovesItsDestination(t *testing.T) {
	dir := t.TempDir()
	src, dst := filepath.Join(dir, "src.mp3"), filepath.Join(dir, "dst.mp3")
	writeTestFile(t, src, theAudio)
	watchCopies(t, func(io.Writer, io.Reader) (int64, error) { return 0, errors.New("disk full") })
	if err := copyFile(src, dst, func(Warning) {}); err == nil || !strings.Contains(err.Error(), "disk full") {
		t.Errorf("copyFile = %v, want the write's error", err)
	}
	mustNotExist(t, dst)
	if err := copyFile(filepath.Join(dir, "missing.mp3"), dst, func(Warning) {}); err == nil {
		t.Error("copyFile of a missing source succeeded")
	}
	mustNotExist(t, dst)
}

// A move to another volume gives the library's file the time its file
// system keeps, which may be coarser than the source's: FAT32 keeps 2
// seconds, exFAT 10 ms, many shares 1 second. The index row takes the time
// the file really has, so that the file is still the one the index describes:
// it used to keep the source's, and every tag save, resolve and undo then
// refused the file as changed.
func TestIndexRowTakesTheTimeTheMovedFileHas(t *testing.T) {
	source, lib, song, _ := ripToOrganize(t)
	// A time a FAT volume cannot hold: an odd second, and a fraction.
	fine := time.Date(2019, 3, 14, 9, 26, 53, 123456700, time.UTC)
	if err := os.Chtimes(song, fine, fine); err != nil {
		t.Fatal(err)
	}
	coarse := fine.Truncate(2 * time.Second)
	otherVolume(t, func(path string) {
		if err := os.Chtimes(path, coarse, coarse); err != nil {
			t.Error(err)
		}
	})

	s := openStore(t)
	if finish := Run(context.Background(), Options{Source: source, Destination: lib, Workers: 1, Store: s}); finish.State != StateOK {
		t.Fatalf("run = %+v", finish)
	}
	info, err := os.Stat(songPath(lib))
	if err != nil || !info.ModTime().Equal(coarse) {
		t.Fatalf("the library's file has time %v, %v; want the coarse %v", info.ModTime(), err, coarse)
	}
	row, ok := indexedFiles(t, s)[songPath(lib)]
	if !ok || !row.ModTime.Equal(coarse) {
		t.Errorf("the row's time = %v, want the %v its file has, not the source's %v", row.ModTime, coarse, fine)
	}
	// Nothing reads the file again, and the run can be undone.
	if again := Run(context.Background(), Options{Destination: lib, IndexOnly: true, Store: s}); again.Counters.Indexed != 0 {
		t.Errorf("the next index run read %d files again, want none", again.Counters.Indexed)
	}
	check, _ := noTags(t)
	runs, err := s.Runs()
	if err != nil {
		t.Fatal(err)
	}
	if plan, err := PlanUndo(s, runs[0].ID, check); err != nil || plan.Moves != 1 || len(plan.Skipped) != 0 {
		t.Errorf("plan to undo the organize = %+v, %v; want the one move back", plan, err)
	}
}
