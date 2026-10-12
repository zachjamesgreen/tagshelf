package tui

import (
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/zachjamesgreen/tagshelf/internal/engine"
	"github.com/zachjamesgreen/tagshelf/internal/fixtures"
	"github.com/zachjamesgreen/tagshelf/internal/store"
	"github.com/zachjamesgreen/tagshelf/internal/tagwrite"
)

// These tests run the save each flow hands the Runner, which the other tests
// only count, on copies of real files with a real database: what the flow
// showed it would write is what the file then holds.

// copyFixture copies the named audio fixture into dir as name and returns
// its path.
func copyFixture(t *testing.T, dir, fixture, name string) string {
	t.Helper()
	data, err := os.ReadFile(fixtures.Path(fixture))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// realRun opens a database in dir and starts the run a save would be
// recorded under.
func realRun(t *testing.T, dir string) (*store.Store, int64) {
	t.Helper()
	st, err := store.Open(filepath.Join(dir, "tagshelf.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	run, err := st.StartRun(store.KindTUIAction, false, "", dir, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	return st, run
}

// readTags reads the tags of a real file as the editor would.
func readTags(t *testing.T, path string) tagwrite.Values {
	t.Helper()
	v, err := tagwrite.Read(path)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

// theSave is the one save the flow handed the fake Runner, for the files
// with the given IDs in any order.
func theSave(t *testing.T, f *fakeRunner, ids ...int64) engine.TagSave {
	t.Helper()
	if len(f.saves) != 1 {
		t.Fatalf("the flow handed the Runner %d saves, want 1", len(f.saves))
	}
	got := slices.Clone(f.saved[0])
	slices.Sort(got)
	if !slices.Equal(got, ids) {
		t.Fatalf("the save is for files %v, want %v", f.saved[0], ids)
	}
	return f.saves[0]
}

// checkTags fails for each field of want the file at path does not hold.
func checkTags(t *testing.T, path string, want map[tagwrite.Field]string) {
	t.Helper()
	got := readTags(t, path)
	for field, value := range want {
		if v := strings.Join(got[field], ", "); v != value {
			t.Errorf("%s: %s = %q, want %q", filepath.Base(path), field, v, value)
		}
	}
}

func TestEditorSaveWritesWhatWasTyped(t *testing.T) {
	dir := t.TempDir()
	path := copyFixture(t, dir, fixtures.RipperFLAC, "ripped.flac")
	src := untaggedSource()
	src.snap.Untagged[2].Path = path // file 103
	src.tags = map[string]tagwrite.Values{path: readTags(t, path)}
	m, f := newUntaggedModel(t, src)
	cursorTo(t, m, 103)
	hit(t, m, "e")
	if m.edit == nil || m.edit.form == nil || m.edit.title != fixtures.Title {
		t.Fatalf("the editor did not open on the file's tags:\n%s", screen(m))
	}
	typeText(t, m, "!")
	hit(t, m, "down", "down", "down", "ctrl+u")
	typeText(t, m, "Another Album")
	hit(t, m, "down", "down", "ctrl+u")
	typeText(t, m, "7/9")
	hit(t, m, "ctrl+s")

	st, run := realRun(t, dir)
	if err := theSave(t, f, 103)(st, run, store.File{ID: 103, Path: path}); err != nil {
		t.Fatal(err)
	}
	checkTags(t, path, map[tagwrite.Field]string{
		tagwrite.Title: fixtures.Title + "!", tagwrite.Album: "Another Album", tagwrite.Track: "7/9",
		// What was not typed over is as it was, and the file is marked.
		tagwrite.Artist: fixtures.Artist, tagwrite.AlbumArtist: fixtures.AlbumArtist,
		tagwrite.Marker: "hand " + time.Now().Format(time.DateOnly),
	})
	if writes, err := st.TagWrites(run); err != nil || len(writes) != 1 || writes[0].Path != path {
		t.Errorf("tag writes = %+v, %v; want the one save recorded", writes, err)
	}
}

func TestLookupSaveWritesTheMatchPicked(t *testing.T) {
	dir := t.TempDir()
	path := copyFixture(t, dir, "untagged.mp3", "live.mp3")
	src := untaggedSource()
	src.releases = map[string]bool{"rel-comp": true}
	src.snap.Untagged[3].Path = path // file 104
	src.tags = map[string]tagwrite.Values{path: readTags(t, path)}
	m, f := newUntaggedModel(t, src)
	m.mb = julie()
	cursorTo(t, m, 104)
	hit(t, m, "l")
	if m.look == nil {
		t.Fatalf("the lookup did not open:\n%s", screen(m))
	}
	toAlbum(t, m)
	hit(t, m, "enter")
	if m.look.step != stepConfirm {
		t.Fatalf("the lookup did not get to the diff: step %d\n%s", m.look.step, screen(m))
	}
	hit(t, m, "enter")

	st, run := realRun(t, dir)
	if err := theSave(t, f, 104)(st, run, store.File{ID: 104, Path: path}); err != nil {
		t.Fatal(err)
	}
	want := tagwrite.Values{
		tagwrite.Title: {"Julie and Candy"}, tagwrite.Artist: {"Boards of Canada"}, tagwrite.AlbumArtist: {"Boards of Canada"},
		tagwrite.Album: {"Geogaddi"}, tagwrite.Date: {"2002-02-18"}, tagwrite.Track: {"5/23"}, tagwrite.Disc: {"1/1"},
		tagwrite.Compilation: {}, tagwrite.Genre: {}, tagwrite.Composer: {}, tagwrite.Comment: {},
		tagwrite.RecordingID: {"rec-1"}, tagwrite.ReleaseTrackID: {"track-5"},
		tagwrite.AlbumID: {"rel-album"}, tagwrite.ReleaseGroupID: {"rg-1"},
		tagwrite.ArtistIDs: {"artist-1"}, tagwrite.AlbumArtistIDs: {"artist-1"},
		tagwrite.Marker: {"musicbrainz " + time.Now().Format(time.DateOnly)},
	}
	if got := readTags(t, path); !reflect.DeepEqual(got, want) {
		t.Errorf("tags after the save:\n got %v\nwant %v", got, want)
	}
}

// Two files of different formats, each moved by hand onto the other's
// track: each is written with the tags of the track it was put on.
func TestReleaseMatchSaveWritesEachFileItsOwnTrack(t *testing.T) {
	dir := t.TempDir()
	flac := copyFixture(t, dir, "untagged.flac", "01 track.flac")
	mp3 := copyFixture(t, dir, "untagged.mp3", "02 track.mp3")
	src := matchSource()
	src.snap.Untagged[0].Path, src.snap.Untagged[1].Path = flac, mp3 // files 101 and 102
	// The third file of the rip is beside them, so that the three come in the
	// order of their names wherever the temp folder is.
	src.snap.Untagged[5].Path = filepath.Join(dir, "zz.flac")
	src.tags = map[string]tagwrite.Values{flac: readTags(t, flac), mp3: readTags(t, mp3), src.snap.Untagged[5].Path: {}}
	m, f := newUntaggedModel(t, src)
	m.mb = geogaddi()
	cursorTo(t, m, 101)
	hit(t, m, "L", "enter", "enter")
	x := m.match
	if x == nil || x.step != matchMap {
		t.Fatalf("the release match did not get to the tracks:\n%s", screen(m))
	}
	// The first file onto the second track, which puts the second file off
	// it; then that one onto the first track.
	hit(t, m, "r", "down", "enter", "down", "down", "r", "up", "up", "up", "enter")
	on := map[int]int{} // file -> track index
	for _, a := range x.mapping.Assignments {
		on[a.File] = a.Track.Index
	}
	if !reflect.DeepEqual(on, map[int]int{0: 1, 1: 0}) {
		t.Fatalf("the files are on tracks %v, want the first on the second track and the second on the first:\n%s", on, screen(m))
	}
	hit(t, m, "enter", "enter")

	st, run := realRun(t, dir)
	save := theSave(t, f, 101, 102)
	for id, path := range map[int64]string{101: flac, 102: mp3} {
		if err := save(st, run, store.File{ID: id, Path: path}); err != nil {
			t.Fatal(err)
		}
	}
	marker := "musicbrainz " + time.Now().Format(time.DateOnly)
	checkTags(t, flac, map[tagwrite.Field]string{
		tagwrite.Title: "Music Is Math", tagwrite.Track: "2/3", tagwrite.RecordingID: "rec-2", tagwrite.ReleaseTrackID: "track-2",
		tagwrite.Album: "Geogaddi", tagwrite.Artist: "Boards of Canada", tagwrite.AlbumID: "rel-album", tagwrite.Marker: marker,
	})
	checkTags(t, mp3, map[tagwrite.Field]string{
		tagwrite.Title: "Ready Lets Go", tagwrite.Track: "1/3", tagwrite.RecordingID: "rec-1", tagwrite.ReleaseTrackID: "track-1",
		tagwrite.Album: "Geogaddi", tagwrite.Artist: "Boards of Canada", tagwrite.AlbumID: "rel-album", tagwrite.Marker: marker,
	})
}

func TestLibraryEditSaveWritesWhatWasTyped(t *testing.T) {
	dir := t.TempDir()
	path := copyFixture(t, dir, fixtures.RipperFLAC, "02 - Music Is Math.flac")
	src := librarySrc()
	src.library[1].Path = path // file 302
	src.tags[path] = readTags(t, path)
	f := newFakeRunner()
	m := newModel(t, src, 120, 32)
	m.runner = f
	hit(t, m, "4", "/")
	typeText(t, m, "geogadi")
	hit(t, m, "enter", "down", "e")
	if m.edit == nil || m.edit.form == nil || m.edit.file.ID != 302 || m.edit.album != fixtures.Album {
		t.Fatalf("the editor did not open on the result's tags:\n%s", screen(m))
	}
	hit(t, m, "down", "down", "down")
	typeText(t, m, " (Deluxe)")
	hit(t, m, "ctrl+s")

	st, run := realRun(t, dir)
	if err := theSave(t, f, 302)(st, run, store.File{ID: 302, Path: path}); err != nil {
		t.Fatal(err)
	}
	checkTags(t, path, map[tagwrite.Field]string{
		tagwrite.Album: fixtures.Album + " (Deluxe)", tagwrite.Title: fixtures.Title, tagwrite.Artist: fixtures.Artist,
		tagwrite.Marker: "hand " + time.Now().Format(time.DateOnly),
	})
}
