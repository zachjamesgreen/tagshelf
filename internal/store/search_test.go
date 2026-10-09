package store

import (
	"path/filepath"
	"testing"
	"time"
)

func TestSearchLibraryAndFolderFiles(t *testing.T) {
	s := openMemory(t)
	lib := filepath.Join(t.TempDir(), "lib")
	libID := setRoot(t, s, RootLibrary, lib)
	untaggedID := setRoot(t, s, RootUntagged, filepath.Join(lib, "_Untagged"))
	mtime := time.Unix(100, 0)
	file := func(root int64, rel, artist, album, title string, track int) File {
		return File{RootID: root, RelPath: filepath.FromSlash(rel), Artist: artist, Album: album, Title: title, Track: track, Size: 1, ModTime: mtime}
	}
	va := file(libID, "Various Artists/Warp 20/11 - Julie and Candy.mp3", "Boards of Canada", "Warp 20", "Julie and Candy", 11)
	va.AlbumArtist = "Various Artists"
	putFiles(t, s,
		file(libID, "Boards of Canada/Geogaddi (2002)/02 - Music Is Math.flac", "Boards of Canada", "Geogaddi", "Music Is Math", 2),
		file(libID, "Boards of Canada/Geogaddi (2002)/01 - Ready Lets Go.flac", "Boards of Canada", "Geogaddi", "Ready Lets Go", 1),
		file(libID, "Boards of Canada/Geogaddi (2002)/bonus/99 - Extra.flac", "Boards of Canada", "Geogaddi", "Extra", 99),
		file(libID, "Autechre/Amber/01 - Foil.flac", "Autechre", "Amber", "Foil", 1),
		file(libID, "100%/Rate_s/01 - Song.flac", "100%", "Rate_s", "Song", 1),
		va,
		file(untaggedID, "geogaddi rip.flac", "Boards of Canada", "Geogaddi", "Untagged One", 3),
		file(libID, "Sigur Rós/Ágætis byrjun/01 - Intro.flac", "Sigur Rós", "Ágætis byrjun", "Intro", 1),
	)

	titles := func(query string, limit int) []string {
		t.Helper()
		files, err := s.SearchLibrary(query, limit)
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, f := range files {
			out = append(out, f.Title)
		}
		return out
	}
	for query, want := range map[string][]string{
		"geogaddi":     {"Ready Lets Go", "Music Is Math", "Extra"}, // album, any case; library only
		"boards math":  {"Music Is Math"},                           // every word, in any field
		"JULIE":        {"Julie and Candy"},
		"various":      {"Julie and Candy"}, // album artist
		"100%":         {"Song"},            // % is a character here
		"rate_":        {"Song"},
		"ratex":        nil,
		"ÁGÆTIS rós":   {"Intro"}, // case beyond ASCII
		"canada warp":  {"Julie and Candy"},
		"songrate":     nil, // not across fields
		"   ":          nil,
		"no such song": nil,
	} {
		if got := titles(query, 10); len(got) != len(want) || (len(got) > 0 && !equalStrings(got, want)) {
			t.Errorf("search %q = %v, want %v", query, got, want)
		}
	}
	if got := titles("a", 2); len(got) != 2 {
		t.Errorf("a limit of 2 gave %v", got)
	}

	found, err := s.SearchLibrary("ready", 1)
	if err != nil || len(found) != 1 {
		t.Fatalf("search = %+v, %v", found, err)
	}
	album, err := s.FolderFiles(found[0])
	if err != nil || len(album) != 2 || album[0].Title != "Ready Lets Go" || album[1].Title != "Music Is Math" {
		t.Errorf("folder files = %+v, %v; want the album's two files and not the folder below", album, err)
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
