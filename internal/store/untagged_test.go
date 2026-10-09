package store

import (
	"path/filepath"
	"testing"
	"time"
)

func TestUntaggedFilesWithWhereTheyCameFrom(t *testing.T) {
	s := openMemory(t)
	src := filepath.Join(t.TempDir(), "src")
	lib := filepath.Join(t.TempDir(), "lib")
	srcID := setRoot(t, s, RootSource, src)
	libID := setRoot(t, s, RootLibrary, lib)
	untaggedID := setRoot(t, s, RootUntagged, filepath.Join(lib, "_Untagged"))
	mtime := time.Unix(100, 0)
	putFiles(t, s,
		File{RootID: srcID, RelPath: filepath.Join("Rip", "b.mp3"), Title: "B", Size: 1, ModTime: mtime},
		File{RootID: untaggedID, RelPath: "a.mp3", Size: 2, ModTime: mtime}, // put there by hand
		File{RootID: libID, RelPath: filepath.Join("A", "song.mp3"), Size: 3, ModTime: mtime},
	)
	run, err := s.StartRun(KindOrganize, false, src, lib, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	b := filepath.Join(lib, "_Untagged", "b.mp3")
	// An older move to the same name, from elsewhere, and then the one that
	// counts.
	for _, from := range []string{filepath.Join(src, "Old", "b.mp3"), filepath.Join(src, "Rip", "b.mp3")} {
		if err := s.RecordMove(run, from, b, ReasonUntagged); err != nil {
			t.Fatal(err)
		}
	}

	got, err := s.UntaggedFiles()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].RelPath != "a.mp3" || got[0].From != "" ||
		got[1].Path != b || got[1].Title != "B" || got[1].From != filepath.Join(src, "Rip", "b.mp3") {
		t.Errorf("untagged files = %+v", got)
	}
}
