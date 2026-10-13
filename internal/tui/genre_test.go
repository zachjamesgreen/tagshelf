package tui

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"go.senan.xyz/taglib"

	"github.com/zachjamesgreen/tagshelf/internal/config"
	"github.com/zachjamesgreen/tagshelf/internal/musicbrainz"
	"github.com/zachjamesgreen/tagshelf/internal/store"
	"github.com/zachjamesgreen/tagshelf/internal/tagwrite"
)

// TestWithGenres checks when a match writes MusicBrainz's genres: as the
// setting says, and never anything when MusicBrainz has none.
func TestWithGenres(t *testing.T) {
	theirs, mine := []string{"Electronic", "Idm"}, []string{"Ambient"}
	tests := []struct {
		name   string
		genres []string
		mode   string
		has    []string
		want   []string // nil: the genre is not written
	}{
		{"fill, and the file has none", theirs, config.GenreFill, nil, theirs},
		{"fill, and the file has one", theirs, config.GenreFill, mine, nil},
		{"the setting left out is fill", theirs, "", nil, theirs},
		{"replace, and the file has one", theirs, config.GenreReplace, mine, theirs},
		{"replace, and the file has none", theirs, config.GenreReplace, nil, theirs},
		{"off", theirs, config.GenreOff, nil, nil},
		{"MusicBrainz has none, replace", nil, config.GenreReplace, mine, nil},
		{"MusicBrainz has none, fill", nil, config.GenreFill, nil, nil},
	}
	for _, tt := range tests {
		base := tagwrite.Values{tagwrite.Title: {"Song"}}
		got := withGenres(base, tt.genres, tt.mode, tt.has)
		if written, ok := got[tagwrite.Genre]; ok != (tt.want != nil) || !reflect.DeepEqual(written, tt.want) {
			t.Errorf("%s: the genre written is %q (written %t), want %q", tt.name, written, ok, tt.want)
		}
		if _, ok := base[tagwrite.Genre]; ok || got[tagwrite.Title][0] != "Song" {
			t.Errorf("%s: the values given were changed, or the title lost", tt.name)
		}
	}
}

// A lookup shows the genre MusicBrainz has among what it would write, and
// writes it to a file that has none.
func TestLookupFillsInTheGenre(t *testing.T) {
	dir := t.TempDir()
	path := copyFixture(t, dir, "untagged.mp3", "live.mp3")
	src := untaggedSource()
	src.releases = map[string]bool{"rel-comp": true}
	src.snap.Untagged[3].Path = path // file 104
	src.tags = map[string]tagwrite.Values{path: readTags(t, path)}
	m, f := newUntaggedModel(t, src)
	mb := julie()
	mb.release.ReleaseGroup.Genres = []musicbrainz.Genre{{Name: "electronic", Count: 5}, {Name: "idm", Count: 5}, {Name: "rock", Count: 1}}
	m.mb = mb
	cursorTo(t, m, 104)
	hit(t, m, "l")
	toAlbum(t, m)
	hit(t, m, "enter")
	if m.look == nil || m.look.step != stepConfirm {
		t.Fatalf("the lookup did not get to the diff:\n%s", screen(m))
	}
	if got := screen(m); !strings.Contains(got, "Genre") || !strings.Contains(got, "Electronic, Idm") {
		t.Errorf("the diff does not show the genre:\n%s", got)
	}
	hit(t, m, "enter")

	st, run := realRun(t, dir)
	if err := theSave(t, f, 104)(st, run, store.File{ID: 104, Path: path}); err != nil {
		t.Fatal(err)
	}
	checkTags(t, path, map[tagwrite.Field]string{tagwrite.Genre: "Electronic, Idm", tagwrite.Title: "Julie and Candy"})
}

// What a lookup would write goes by the setting and by the genre the file
// has: the diff shows no genre row for one it leaves alone.
func TestLookupLeavesAGenreAloneUnlessToldToReplaceIt(t *testing.T) {
	file := store.File{Path: filepath.Join("x", "song.flac")}
	l := &lookup{file: file, tags: musicbrainz.Tags{Title: "Song", Genres: []string{"Electronic"}},
		orig: tagwrite.Values{tagwrite.Genre: {"Ambient"}}}
	m := &Model{}
	if _, ok := m.lookupWrites(l)[tagwrite.Genre]; ok {
		t.Error("with the setting left out, a file that has a genre gets MusicBrainz's")
	}
	m.genreMode = config.GenreReplace
	if got := m.lookupWrites(l)[tagwrite.Genre]; !reflect.DeepEqual(got, []string{"Electronic"}) {
		t.Errorf("with replace the genre written is %q", got)
	}
	m.genreMode = config.GenreOff
	l.orig = tagwrite.Values{}
	if _, ok := m.lookupWrites(l)[tagwrite.Genre]; ok {
		t.Error("with the setting off a genre is written")
	}
}

// A release match looks at each file as it saves to it: of two files matched
// to one release, the one that has a genre keeps it and the other gets
// MusicBrainz's. With replace both get it.
func TestReleaseMatchFillsInTheGenreFileByFile(t *testing.T) {
	for _, mode := range []string{config.GenreFill, config.GenreReplace, config.GenreOff} {
		t.Run(mode, func(t *testing.T) {
			dir := t.TempDir()
			flac := copyFixture(t, dir, "untagged.flac", "01 track.flac")
			mp3 := copyFixture(t, dir, "untagged.mp3", "02 track.mp3")
			if err := taglib.WriteTags(mp3, map[string][]string{taglib.Genre: {"Ambient"}}, 0); err != nil {
				t.Fatal(err)
			}
			src := matchSource()
			src.snap.Untagged[0].Path, src.snap.Untagged[1].Path = flac, mp3 // files 101 and 102
			src.snap.Untagged[5].Path = filepath.Join(dir, "zz.flac")
			src.tags = map[string]tagwrite.Values{flac: readTags(t, flac), mp3: readTags(t, mp3), src.snap.Untagged[5].Path: {}}
			m, f := newUntaggedModel(t, src)
			m.genreMode = mode
			mb := geogaddi()
			mb.release.Genres = []musicbrainz.Genre{{Name: "electronic", Count: 2}}
			m.mb = mb
			cursorTo(t, m, 101)
			hit(t, m, "L", "enter", "enter")
			if m.match == nil || m.match.step != matchMap {
				t.Fatalf("the release match did not get to the tracks:\n%s", screen(m))
			}
			hit(t, m, "enter", "enter")

			st, run := realRun(t, dir)
			save := theSave(t, f, 101, 102)
			for id, path := range map[int64]string{101: flac, 102: mp3} {
				if err := save(st, run, store.File{ID: id, Path: path}); err != nil {
					t.Fatal(err)
				}
			}
			want := map[string][2]string{
				config.GenreFill:    {"Electronic", "Ambient"},
				config.GenreReplace: {"Electronic", "Electronic"},
				config.GenreOff:     {"", "Ambient"},
			}[mode]
			checkTags(t, flac, map[tagwrite.Field]string{tagwrite.Genre: want[0], tagwrite.Album: "Geogaddi"})
			checkTags(t, mp3, map[tagwrite.Field]string{tagwrite.Genre: want[1], tagwrite.Album: "Geogaddi"})
		})
	}
}
