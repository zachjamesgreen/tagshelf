//go:build !windows

package engine

import (
	"context"
	"errors"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"
)

// What is not a file is left where it is: a named pipe, a socket. A pipe
// named like a song used to be opened to read its tags, which waits for ever
// on whoever writes to it.
func TestWalkLeavesWhatIsNotAFileAlone(t *testing.T) {
	// A short folder: the path of a socket has room for about a hundred
	// characters.
	tmp, err := os.MkdirTemp("", "ts")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(tmp) })
	source, lib := filepath.Join(tmp, "src"), filepath.Join(tmp, "lib")
	writeTaggedMP3(t, filepath.Join(source, "Rip", "song.mp3"), songTags, theAudio)
	pipe := filepath.Join(source, "Rip", "pipe.mp3")
	if err := syscall.Mkfifo(pipe, 0o644); err != nil {
		t.Skipf("cannot make a named pipe: %v", err)
	}
	special := map[string]fs.FileMode{pipe: fs.ModeNamedPipe}
	socket := filepath.Join(source, "Rip", "s.sock")
	if l, err := net.Listen("unix", socket); err == nil {
		// The socket stays in the folder for as long as it is listened on.
		t.Cleanup(func() { l.Close() })
		special[socket] = fs.ModeSocket
	} else {
		t.Logf("no socket in the test: %v", err)
	}

	var c collector
	done := make(chan RunFinished, 1)
	go func() {
		done <- Run(context.Background(), Options{Source: source, Destination: lib, Workers: 1, Sink: c.sink})
	}()
	var finish RunFinished
	select {
	case finish = <-done:
	case <-time.After(20 * time.Second):
		t.Fatal("the run did not end: it is waiting on the named pipe")
	}
	if finish.State != StateOK || finish.Counters.TotalFound != 1 || finish.Counters.OtherFound != 0 || finish.Counters.Errors != 0 {
		t.Errorf("run = %+v, want only the song found", finish)
	}
	mustExist(t, songPath(lib))
	warned := map[string]bool{}
	for _, w := range c.warnings(WarnSpecialSkipped) {
		warned[w.Path] = true
	}
	for path, kind := range special {
		if info, err := os.Lstat(path); err != nil || info.Mode()&kind == 0 {
			t.Errorf("%s after the run: %v, %v; want it where it was", path, info, err)
		}
		if !warned[path] {
			t.Errorf("no warning says %s was left alone: %+v", path, c.warnings(WarnSpecialSkipped))
		}
	}
	mustNotExist(t, filepath.Join(lib, "_Other"), filepath.Join(lib, "_Untagged"))
}

// The folders a Linux system keeps at the top of a volume are left out of a
// walk: lost+found, which only root can read and would be an error on every
// run, and the trash, whose deleted songs are whole files with their tags. A
// folder of the same name anywhere else is like any other.
func TestWalkSkipsLinuxSystemFoldersAtAVolumeRoot(t *testing.T) {
	lib := filepath.Join(t.TempDir(), "mnt")
	volumeRootAt(t, lib)
	deleted := id3Tags{albumArtist: "Singer", album: "Album", title: "Deleted", track: "9"}
	trashed := filepath.Join(lib, ".Trash-1000", "files", "deleted.mp3")
	writeTaggedMP3(t, trashed, deleted, "deleted audio")
	shared := filepath.Join(lib, ".Trash", "1000", "files", "too.mp3")
	writeTaggedMP3(t, shared, deleted, "more deleted audio")
	found := filepath.Join(lib, "lost+found", "#12345")
	writeTestFile(t, found, "a piece of a file")
	// A folder only root can read, as lost+found is: listing it fails.
	if err := os.Chmod(filepath.Dir(found), 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(filepath.Dir(found), 0o755) })
	writeTaggedMP3(t, filepath.Join(lib, "dropped.mp3"), songTags, theAudio)
	// Not at the top of the volume: an album that happens to have the name.
	writeTaggedMP3(t, filepath.Join(lib, "Old", "lost+found", "b.mp3"),
		id3Tags{albumArtist: "Singer", album: "Album", title: "Other", track: "2"}, "other audio")

	s := openStore(t)
	var c collector
	finish := Run(context.Background(), Options{Source: lib, Destination: lib, Workers: 1, Store: s, Sink: c.sink})
	if finish.State != StateOK || finish.Counters.TotalFound != 2 || finish.Counters.Organized != 2 || finish.Counters.OtherFound != 0 {
		t.Errorf("reorganize = %+v with errors %+v, want the two songs outside the system folders organized and no word of the rest", finish, c.fileErrors())
	}
	mustExist(t, trashed, shared, songPath(lib), filepath.Join(lib, "Singer", "Album", "02 - Other.mp3"))
	mustNotExist(t, filepath.Join(lib, "Singer", "Album", "09 - Deleted.mp3"))
	if rows := indexedFiles(t, s); len(rows) != 2 {
		t.Errorf("index = %v, want the two songs and nothing from the trash", rows)
	}
}

// The top of a volume is where a file system is mounted: the root folder, and
// any folder on another device than the one above it.
func TestVolumeRoot(t *testing.T) {
	if !volumeRoot("/") {
		t.Error(`volumeRoot("/") = false`)
	}
	dir := t.TempDir()
	if volumeRoot(dir) || volumeRoot(filepath.Join(dir, "not there")) {
		t.Errorf("volumeRoot(%q) = true for a folder made a moment ago, or for one that is not there", dir)
	}
	if runtime.GOOS == "linux" {
		// /proc is a file system of its own on every Linux.
		if _, err := os.Stat("/proc/self"); err == nil && !volumeRoot("/proc") {
			t.Error(`volumeRoot("/proc") = false`)
		}
	}
}

// A name longer than the file system takes is an error on that one file,
// which stays where it is: tagshelf does not shorten names. Linux counts 255
// bytes, so a title of a hundred Japanese characters, which Windows takes,
// is too long there.
func TestNameTooLongLeavesTheFileInPlace(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("the limit of 255 bytes is Linux's")
	}
	tmp := t.TempDir()
	source, lib := filepath.Join(tmp, "src"), filepath.Join(tmp, "lib")
	long := filepath.Join(source, "long.mp3")
	title := strings.Repeat("あ", 100)
	writeTaggedMP3(t, long, id3Tags{albumArtist: "Singer", album: "Album", title: title, track: "2"}, "long audio")
	writeTaggedMP3(t, filepath.Join(source, "song.mp3"), songTags, theAudio)

	var c collector
	finish := Run(context.Background(), Options{Source: source, Destination: lib, Workers: 1, Sink: c.sink})
	if finish.Counters.Organized != 1 || finish.Counters.Errors != 1 {
		t.Errorf("run = %+v, want the one song organized and the other an error", finish)
	}
	mustExist(t, long, songPath(lib))
	errs := c.fileErrors()
	if len(errs) != 1 || errs[0].Path != long || !errors.Is(errs[0].Err, syscall.ENAMETOOLONG) {
		t.Errorf("errors = %+v, want one for %s that says its name is too long", errs, long)
	}
	entries, err := os.ReadDir(filepath.Join(lib, "Singer", "Album"))
	if err != nil || len(entries) != 1 {
		t.Errorf("the album folder holds %v, %v; want the one song, and nothing left of the other", entries, err)
	}
}
