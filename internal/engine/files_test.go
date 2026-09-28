package engine

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"
)

// exactNames lists the files under dir by their names as stored on disk,
// relative to dir and with forward slashes. On a case-insensitive file system
// os.Stat cannot tell "abba" from "ABBA"; the directory listing can.
func exactNames(t *testing.T, dir string) []string {
	t.Helper()
	var names []string
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		names = append(names, filepath.ToSlash(rel))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(names)
	return names
}

func TestReorganizeCaseOnlyRename(t *testing.T) {
	lib := t.TempDir()
	one := filepath.Join(lib, "abba", "gold", "01 - dancing queen.mp3")
	two := filepath.Join(lib, "abba", "gold", "02 - Waterloo.mp3")
	writeTaggedMP3(t, one, id3Tags{albumArtist: "ABBA", album: "Gold", title: "Dancing Queen", track: "1"}, "one")
	writeTaggedMP3(t, two, id3Tags{albumArtist: "ABBA", album: "Gold", title: "Waterloo", track: "2"}, "two")
	writeTestFile(t, filepath.Join(lib, "abba", "gold", "cover.jpg"), "art")

	var c collector
	finish := Run(context.Background(), Options{Source: lib, Destination: lib, Workers: 2, Sink: c.sink})

	want := []string{"ABBA/Gold/01 - Dancing Queen.mp3", "ABBA/Gold/02 - Waterloo.mp3", "ABBA/Gold/cover.jpg"}
	if got := exactNames(t, lib); !reflect.DeepEqual(got, want) {
		t.Errorf("library = %q, want %q", got, want)
	}
	if finish.State != StateOK {
		t.Errorf("State = %v, want StateOK (%+v)", finish.State, finish.Counters)
	}
	c.checkPhaseOrder(t)

	// A second run finds everything in place.
	again := Run(context.Background(), Options{Source: lib, Destination: lib, Workers: 2}).Counters
	if again.InPlace != 3 || again.Organized != 0 || again.Retried != 0 {
		t.Errorf("second run = %+v, want all 3 in place", again)
	}
}

// Two albums under one artist folder that disagree on its spelling leave the
// folder alone, so that reorganizing never flips it back and forth.
func TestReorganizeCaseConflictLeavesFolder(t *testing.T) {
	lib := t.TempDir()
	if caseSensitive(t, lib) {
		t.Skip("abba and ABBA are separate folders on a case-sensitive file system")
	}
	writeTaggedMP3(t, filepath.Join(lib, "abba", "Gold", "01 - One.mp3"), id3Tags{albumArtist: "ABBA", album: "Gold", title: "One", track: "1"}, "one")
	writeTaggedMP3(t, filepath.Join(lib, "abba", "Arrival", "01 - Two.mp3"), id3Tags{albumArtist: "Abba", album: "Arrival", title: "Two", track: "1"}, "two")

	for range 2 {
		Run(context.Background(), Options{Source: lib, Destination: lib, Workers: 1})
		want := []string{"abba/Arrival/01 - Two.mp3", "abba/Gold/01 - One.mp3"}
		if got := exactNames(t, lib); !reflect.DeepEqual(got, want) {
			t.Errorf("library = %q, want %q", got, want)
		}
	}
}

// caseSensitive reports whether the file system under dir tells names apart
// by case, where "abba" and "ABBA" are simply two folders.
func caseSensitive(t *testing.T, dir string) bool {
	t.Helper()
	probe := filepath.Join(dir, "case-probe")
	writeTestFile(t, probe, "")
	defer os.Remove(probe)
	_, err := os.Stat(filepath.Join(dir, "CASE-PROBE"))
	return err != nil
}

func TestNonASCIINames(t *testing.T) {
	tmpDir := t.TempDir()
	source := filepath.Join(tmpDir, "src")
	dest := filepath.Join(tmpDir, "lib")
	writeTaggedMP3(t, filepath.Join(source, "Björk – Homogénic", "jóga.mp3"),
		id3Tags{albumArtist: "Björk", album: "Homogénic", title: "Jóga", year: "1997", track: "2"}, "jóga")
	writeTaggedMP3(t, filepath.Join(source, "坂本龍一", "戦場のメリークリスマス.mp3"),
		id3Tags{albumArtist: "坂本龍一", album: "戦場のメリークリスマス", title: "Merry Christmas Mr. Lawrence", track: "1"}, "ryuichi")
	writeTestFile(t, filepath.Join(source, "Björk – Homogénic", "Pochette été.jpg"), "art")
	writeTestFile(t, filepath.Join(source, "Ünbekannt ß.mp3"), "no tags")

	finish := Run(context.Background(), Options{Source: source, Destination: dest, Workers: 2})

	want := []string{
		"_Untagged/Ünbekannt ß.mp3",
		"Björk/Homogénic (1997)/02 - Jóga.mp3",
		"Björk/Homogénic (1997)/Pochette été.jpg",
		"坂本龍一/戦場のメリークリスマス/01 - Merry Christmas Mr. Lawrence.mp3",
	}
	sort.Strings(want)
	if got := exactNames(t, dest); !reflect.DeepEqual(got, want) {
		t.Errorf("library = %q, want %q", got, want)
	}
	if finish.State != StateOK {
		t.Errorf("State = %v, want StateOK (%+v)", finish.State, finish.Counters)
	}
}
