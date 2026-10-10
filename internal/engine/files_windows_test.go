//go:build windows

package engine

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// A file another program holds open cannot be moved on Windows. Its move
// fails with a FileError, it stays whole in the source with no copy left in
// the library, and the rest of the run goes on.
func TestMoveOfOpenFileFails(t *testing.T) {
	tmpDir := t.TempDir()
	source := filepath.Join(tmpDir, "src")
	dest := filepath.Join(tmpDir, "lib")
	held := filepath.Join(source, "Rip", "held.mp3")
	free := filepath.Join(source, "Rip", "free.mp3")
	writeTaggedMP3(t, held, id3Tags{albumArtist: "Singer", album: "Album", title: "Held", track: "1"}, "held")
	writeTaggedMP3(t, free, id3Tags{albumArtist: "Singer", album: "Album", title: "Free", track: "2"}, "free")

	// Like most programs, os.Open shares reading and writing but not deleting,
	// which is what a rename needs.
	f, err := os.Open(held)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	// The rename's failure is the answer: the file is not copied to find out
	// that its source cannot be removed either.
	watchCopies(t, func(dst io.Writer, src io.Reader) (int64, error) {
		t.Error("the held file was copied")
		return io.Copy(dst, src)
	})

	var c collector
	finish := Run(context.Background(), Options{Source: source, Destination: dest, Workers: 2, Sink: c.sink})

	errs := c.fileErrors()
	if len(errs) != 1 || errs[0].Path != held || errs[0].Op != OpMove {
		t.Fatalf("FileErrors = %+v, want one OpMove for %q", errs, held)
	}
	if finish.State != StateErrors || finish.Counters.Organized != 1 {
		t.Errorf("RunFinished = %+v, want StateErrors with the free file organized", finish)
	}
	if _, err := os.Stat(held); err != nil {
		t.Errorf("held file is gone from the source: %v", err)
	}
	want := []string{"Singer/Album/02 - Free.mp3"}
	if got := exactNames(t, dest); !reflect.DeepEqual(got, want) {
		t.Errorf("library = %q, want %q", got, want)
	}
}

// A file opened without any sharing cannot even be read; it fails with a
// FileError at open.
func TestReadOfLockedFileFails(t *testing.T) {
	tmpDir := t.TempDir()
	source := filepath.Join(tmpDir, "src")
	locked := filepath.Join(source, "locked.mp3")
	writeTaggedMP3(t, locked, id3Tags{albumArtist: "Singer", album: "Album", title: "Locked", track: "1"}, "locked")

	lockPath(t, locked)

	var c collector
	finish := Run(context.Background(), Options{Source: source, Destination: filepath.Join(tmpDir, "lib"), Workers: 1, Sink: c.sink})

	var ops []Operation
	for _, fe := range c.fileErrors() {
		ops = append(ops, fe.Op)
	}
	if !reflect.DeepEqual(ops, []Operation{OpOpen}) || finish.State != StateErrors {
		t.Errorf("FileError ops = %v, state %v; want one OpOpen and StateErrors", ops, finish.State)
	}
	if _, err := os.Stat(locked); err != nil {
		t.Errorf("locked file is gone: %v", err)
	}
}

// isSubdirectory with the paths Windows has: another drive, another case, a
// folder whose name starts like the parent's, and a share.
func TestIsSubdirectoryAcrossDrivesAndCase(t *testing.T) {
	for _, tt := range []struct {
		parent, sub string
		want        bool
	}{
		{`C:\Music`, `D:\Music\Artist`, false},
		{`C:\Music`, `c:\music\Artist`, true},
		{`C:\Music`, `C:\Music2\Artist`, false},
		{`C:\Music\`, `C:\Music\Artist`, true},
		{`\\nas\share\Music`, `\\nas\share\Music\Artist`, true},
		{`\\nas\share\Music`, `\\nas\other\Music\Artist`, false},
	} {
		if got := isSubdirectory(tt.parent, tt.sub); got != tt.want {
			t.Errorf("isSubdirectory(%q, %q) = %t, want %t", tt.parent, tt.sub, got, tt.want)
		}
	}
}
