//go:build windows

package engine

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"syscall"
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

	var c collector
	finish := Run(context.Background(), Options{Source: source, Destination: dest, Workers: 2, Sink: c.sink})

	var errs []FileError
	for _, e := range c.events {
		if fe, ok := e.(FileError); ok {
			errs = append(errs, fe)
		}
	}
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

	name, err := syscall.UTF16PtrFromString(locked)
	if err != nil {
		t.Fatal(err)
	}
	h, err := syscall.CreateFile(name, syscall.GENERIC_READ, 0, nil, syscall.OPEN_EXISTING, syscall.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer syscall.CloseHandle(h)

	var c collector
	finish := Run(context.Background(), Options{Source: source, Destination: filepath.Join(tmpDir, "lib"), Workers: 1, Sink: c.sink})

	var ops []Operation
	for _, e := range c.events {
		if fe, ok := e.(FileError); ok {
			ops = append(ops, fe.Op)
		}
	}
	if !reflect.DeepEqual(ops, []Operation{OpOpen}) || finish.State != StateErrors {
		t.Errorf("FileError ops = %v, state %v; want one OpOpen and StateErrors", ops, finish.State)
	}
	if _, err := os.Stat(locked); err != nil {
		t.Errorf("locked file is gone: %v", err)
	}
}
