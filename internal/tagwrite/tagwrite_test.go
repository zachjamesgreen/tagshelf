package tagwrite

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"go.senan.xyz/taglib"

	"tagshelf/internal/fixtures"
	"tagshelf/internal/marker"
	"tagshelf/internal/store"
	"tagshelf/internal/tag"
	"tagshelf/internal/tag/mbz"
)

var day = time.Date(2026, 9, 28, 15, 4, 5, 0, time.UTC)

// copyFixture copies a fixture into a new temporary folder under name and
// returns its path.
func copyFixture(t *testing.T, src, name string) string {
	t.Helper()
	data, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(dst, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return dst
}

func read(t *testing.T, path string) tag.Metadata {
	t.Helper()
	md, err := readTags(path)
	if err != nil {
		t.Fatalf("reading %s back: %v", path, err)
	}
	return md
}

// musicBrainzSave is every field a MusicBrainz pick writes, with two artist
// IDs.
func musicBrainzSave() Values {
	return Values{
		Title:          {"New Title"},
		Artist:         {"New Artist feat. Other"},
		AlbumArtist:    {"New Album Artist"},
		Album:          {"New Album"},
		Date:           {"2003-04-05"},
		Track:          {"4/9"},
		Disc:           {"2/3"},
		Compilation:    {"1"},
		RecordingID:    {"aaaaaaaa-0000-4000-8000-000000000001"},
		ReleaseTrackID: {"aaaaaaaa-0000-4000-8000-000000000002"},
		AlbumID:        {"aaaaaaaa-0000-4000-8000-000000000003"},
		ReleaseGroupID: {"aaaaaaaa-0000-4000-8000-000000000004"},
		ArtistIDs:      {"aaaaaaaa-0000-4000-8000-000000000005", "aaaaaaaa-0000-4000-8000-000000000006"},
		AlbumArtistIDs: {"aaaaaaaa-0000-4000-8000-000000000007"},
	}
}

// taggableExtensions are the extensions Save writes, each with the fixture
// extension it is copied from.
var taggableExtensions = map[string]string{
	".mp3": ".mp3", ".flac": ".flac", ".ogg": ".ogg", ".m4a": ".m4a", ".m4b": ".m4b",
	".m4r": ".m4a", ".m4p": ".m4a", ".wav": ".wav", ".ape": ".ape", ".mpc": ".mpc",
	".wma": ".wma",
}

// TestRoundTrip saves every field to each taggable format, from untagged and
// from tagged files, and reads them back with internal/tag, mbz, the marker
// reader and go-taglib.
func TestRoundTrip(t *testing.T) {
	for ext, from := range taggableExtensions {
		for _, src := range []string{fixtures.Untagged(from), fixtures.Tagged(from)} {
			t.Run(filepath.Base(src)+"->"+ext, func(t *testing.T) {
				path := copyFixture(t, src, "song"+ext)
				keysBefore, err := taglib.ReadTags(path)
				if err != nil {
					t.Fatal(err)
				}
				m := marker.New(marker.MusicBrainz, day)
				fields := musicBrainzSave()
				res, err := Writer{}.Save(path, fields, m)
				if err != nil {
					t.Fatal(err)
				}
				if !res.Changed {
					t.Error("Changed = false for a save that changes every field")
				}

				md := read(t, path)
				if got, ok := marker.Read(md); !ok || got != m {
					t.Errorf("marker = %+v, %t; want %+v", got, ok, m)
				}
				if md.Title() != "New Title" || md.Artist() != "New Artist feat. Other" || md.AlbumArtist() != "New Album Artist" || md.Album() != "New Album" {
					t.Errorf("read back title %q, artist %q, album artist %q, album %q", md.Title(), md.Artist(), md.AlbumArtist(), md.Album())
				}
				if md.Year() != 2003 {
					t.Errorf("Year() = %d, want 2003", md.Year())
				}
				if x, n := md.Track(); x != 4 || n != 9 {
					t.Errorf("Track() = %d/%d, want 4/9", x, n)
				}
				if x, n := md.Disc(); x != 2 || n != 3 {
					t.Errorf("Disc() = %d/%d, want 2/3", x, n)
				}
				want := mbz.IDs{
					RecordingID:    fields[RecordingID][0],
					ReleaseTrackID: fields[ReleaseTrackID][0],
					AlbumID:        fields[AlbumID][0],
					ReleaseGroupID: fields[ReleaseGroupID][0],
					ArtistIDs:      fields[ArtistIDs],
					AlbumArtistIDs: fields[AlbumArtistIDs],
				}
				if got := mbz.Extract(md); !reflect.DeepEqual(got, want) {
					t.Errorf("MusicBrainz IDs = %+v, want %+v", got, want)
				}

				keys, err := taglib.ReadTags(path)
				if err != nil {
					t.Fatal(err)
				}
				if got := keys[taglib.Compilation]; ext != ".wma" && !slices.Equal(got, []string{"1"}) { // TagLib has no ASF key for it
					t.Errorf("COMPILATION = %q, want 1", got)
				}
				// Everything else is left alone.
				written := toKeys(formats[ext], Values(fields))
				for k, v := range keysBefore {
					if _, ok := written[k]; !ok && !slices.Equal(keys[k], v) {
						t.Errorf("%s changed from %q to %q", k, v, keys[k])
					}
				}
				if leftovers(t, path) != nil {
					t.Errorf("temporary files left behind: %v", leftovers(t, path))
				}
			})
		}
	}
}

// leftovers lists the files beside path other than path itself.
func leftovers(t *testing.T, path string) []string {
	t.Helper()
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		if e.Name() != filepath.Base(path) {
			names = append(names, e.Name())
		}
	}
	return names
}

// TestMultiValuedArtistIDsVorbis pins the Vorbis reader fix: every value of a
// repeated key reads back.
func TestMultiValuedArtistIDsVorbis(t *testing.T) {
	ids := []string{"aaaaaaaa-0000-4000-8000-00000000000a", "aaaaaaaa-0000-4000-8000-00000000000b", "aaaaaaaa-0000-4000-8000-00000000000c"}
	for _, ext := range []string{".flac", ".ogg"} {
		path := copyFixture(t, fixtures.Tagged(ext), "song"+ext)
		if _, err := (Writer{}).Save(path, Values{ArtistIDs: ids, AlbumArtistIDs: ids[:2]}, marker.New(marker.Hand, day)); err != nil {
			t.Fatalf("%s: %v", ext, err)
		}
		got := mbz.Extract(read(t, path))
		if !slices.Equal(got.ArtistIDs, ids) || !slices.Equal(got.AlbumArtistIDs, ids[:2]) {
			t.Errorf("%s: artist IDs %q, album artist IDs %q; want %q and %q", ext, got.ArtistIDs, got.AlbumArtistIDs, ids, ids[:2])
		}
	}
}

// TestWrongExtensionFails covers files whose content is not what their
// extension says: go-taglib would write the wrong tag, or nothing at all.
func TestWrongExtensionFails(t *testing.T) {
	for _, tt := range []struct{ src, name string }{
		{fixtures.Tagged(".flac"), "song.mp3"},  // caught before writing
		{fixtures.Tagged(".mp3"), "song.wav"},   // caught before writing
		{fixtures.Untagged(".mp3"), "song.wav"}, // no tags to tell: caught on reading back
		{fixtures.Untagged(".mp3"), "song.ogg"},
	} {
		path := copyFixture(t, tt.src, tt.name)
		orig, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		_, err = Writer{}.Save(path, Values{Title: {"X"}}, marker.New(marker.Hand, day))
		if !errors.Is(err, ErrNotVerified) {
			t.Errorf("%s as %s: Save = %v, want ErrNotVerified", filepath.Base(tt.src), tt.name, err)
		}
		now, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(now, orig) {
			t.Errorf("%s as %s: the original changed", filepath.Base(tt.src), tt.name)
		}
		if l := leftovers(t, path); l != nil {
			t.Errorf("%s as %s: temporary files left behind: %v", filepath.Base(tt.src), tt.name, l)
		}
	}
}

func TestUnsupportedFormats(t *testing.T) {
	for _, ext := range []string{".aac"} {
		path := copyFixture(t, fixtures.Tagged(ext), "song"+ext)
		orig, _ := os.ReadFile(path)
		_, err := Writer{}.Save(path, Values{Title: {"X"}}, marker.New(marker.Hand, day))
		if !errors.Is(err, ErrUnsupported) || err.Error() != "tagging not supported for this format" {
			t.Errorf("%s: Save = %v, want %q", ext, err, ErrUnsupported)
		}
		if now, _ := os.ReadFile(path); !bytes.Equal(now, orig) {
			t.Errorf("%s: the file changed", ext)
		}
		if Supported(path) {
			t.Errorf("Supported(%q) = true", path)
		}
	}
	if !Supported(`C:\x\Song.M4P`) {
		t.Error("Supported(.M4P) = false")
	}
}

// TestHistory checks that each save records the written fields before and
// after, marker included, and that a save changing nothing records nothing
// and leaves the file alone.
func TestHistory(t *testing.T) {
	s, err := store.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	run, err := s.StartRun(store.KindTUIAction, false, "", "lib", day)
	if err != nil {
		t.Fatal(err)
	}
	w := Writer{History: s, RunID: run}

	path := copyFixture(t, fixtures.Tagged(".flac"), "song.flac")
	m := marker.New(marker.Hand, day)
	res, err := w.Save(path, Values{Title: {"New Title"}, Album: {""}, Track: {"04/12"}}, m)
	if err != nil {
		t.Fatal(err)
	}
	wantBefore := Values{Title: {fixtures.Title}, Album: {fixtures.Album}, Track: {"3/10"}, Marker: {}}
	wantAfter := Values{Title: {"New Title"}, Album: {}, Track: {"4/12"}, Marker: {"hand 2026-09-28"}}
	if !res.Changed || !reflect.DeepEqual(res.Before, wantBefore) || !reflect.DeepEqual(res.After, wantAfter) {
		t.Errorf("Save = %+v, want changed from %v to %v", res, wantBefore, wantAfter)
	}

	writes, err := s.TagWrites(run)
	if err != nil {
		t.Fatal(err)
	}
	if len(writes) != 1 || writes[0].Path != path {
		t.Fatalf("tag writes = %+v, want one for %s", writes, path)
	}
	var before, after Values
	if err := json.Unmarshal([]byte(writes[0].Before), &before); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(writes[0].After), &after); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, wantBefore) || !reflect.DeepEqual(after, wantAfter) {
		t.Errorf("recorded before %v, after %v; want %v and %v", before, after, wantBefore, wantAfter)
	}

	// The same save again, even on a later day, changes nothing.
	orig, _ := os.ReadFile(path)
	info, _ := os.Stat(path)
	res, err = w.Save(path, Values{Title: {"New Title"}, Album: {}, Track: {"4/12"}}, marker.New(marker.Hand, day.AddDate(0, 0, 1)))
	if err != nil {
		t.Fatal(err)
	}
	if res.Changed {
		t.Error("Changed = true for a save that changes nothing")
	}
	if writes, _ := s.TagWrites(run); len(writes) != 1 {
		t.Errorf("a no-change save was recorded: %d tag writes", len(writes))
	}
	now, _ := os.ReadFile(path)
	info2, _ := os.Stat(path)
	if !bytes.Equal(now, orig) || !info2.ModTime().Equal(info.ModTime()) {
		t.Error("a no-change save wrote the file")
	}
}

// TestAcceptTagsWritesOnlyTheMarker covers accept tags: no fields, so only
// the marker is written, and only when the file has none.
func TestAcceptTagsWritesOnlyTheMarker(t *testing.T) {
	path := copyFixture(t, fixtures.Tagged(".mp3"), "song.mp3")
	res, err := Writer{}.Save(path, nil, marker.New(marker.Hand, day))
	if err != nil {
		t.Fatal(err)
	}
	if !res.Changed || !reflect.DeepEqual(res.After, Values{Marker: {"hand 2026-09-28"}}) {
		t.Errorf("Save = %+v, want the marker written", res)
	}
	md := read(t, path)
	if md.Title() != fixtures.Title || mbz.Extract(md).AlbumID != fixtures.ReleaseID {
		t.Errorf("accept tags changed the tags: title %q", md.Title())
	}

	res, err = Writer{}.Save(path, nil, marker.New(marker.MusicBrainz, day))
	if err != nil {
		t.Fatal(err)
	}
	if res.Changed {
		t.Error("accepting a marked file again wrote it")
	}
}

// TestSaveOverwritesTheMarker checks last write wins.
func TestSaveOverwritesTheMarker(t *testing.T) {
	path := copyFixture(t, fixtures.Manual(".m4a"), "song.m4a")
	if _, err := (Writer{}).Save(path, Values{Title: {"Other"}}, marker.New(marker.MusicBrainz, day)); err != nil {
		t.Fatal(err)
	}
	if got, _ := marker.Read(read(t, path)); got.String() != "musicbrainz 2026-09-28" {
		t.Errorf("marker = %q, want musicbrainz 2026-09-28", got.String())
	}
}

// TestFailedRecordLeavesOriginal checks that a save the history cannot record
// is not made.
func TestFailedRecordLeavesOriginal(t *testing.T) {
	s, err := store.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	path := copyFixture(t, fixtures.Tagged(".ogg"), "song.ogg")
	orig, _ := os.ReadFile(path)
	_, err = Writer{History: s, RunID: 999}.Save(path, Values{Title: {"X"}}, marker.New(marker.Hand, day))
	if err == nil {
		t.Fatal("Save succeeded with a run that does not exist")
	}
	if now, _ := os.ReadFile(path); !bytes.Equal(now, orig) {
		t.Error("the original changed")
	}
	if l := leftovers(t, path); l != nil {
		t.Errorf("temporary files left behind: %v", l)
	}
}

func TestSaveRejectsBadValues(t *testing.T) {
	path := copyFixture(t, fixtures.Tagged(".flac"), "song.flac")
	orig, _ := os.ReadFile(path)
	for _, v := range []Values{
		{Title: {"a", "b"}},
		{Track: {"x"}},
		{Disc: {"1/y"}},
		{Date: {"2003-04"}},
		{Compilation: {"yes"}},
		{Marker: {"hand"}},
		{Field("genre"): {"Rock"}},
	} {
		if _, err := (Writer{}).Save(path, v, marker.New(marker.Hand, day)); err == nil || errors.Is(err, ErrNotVerified) {
			t.Errorf("Save(%v) = %v, want a validation error", v, err)
		}
	}
	if now, _ := os.ReadFile(path); !bytes.Equal(now, orig) {
		t.Error("the file changed")
	}
}

// TestClearFields checks that fields given no values are removed, including
// the Vorbis totals.
func TestClearFields(t *testing.T) {
	for _, ext := range []string{".flac", ".mp3", ".m4a", ".ape"} {
		path := copyFixture(t, fixtures.Tagged(ext), "song"+ext)
		if _, err := (Writer{}).Save(path, Values{Album: nil, Track: {}, ReleaseGroupID: {""}}, marker.New(marker.Hand, day)); err != nil {
			t.Fatalf("%s: %v", ext, err)
		}
		md := read(t, path)
		if x, n := md.Track(); md.Album() != "" || x != 0 || n != 0 || mbz.Extract(md).ReleaseGroupID != "" {
			t.Errorf("%s: album %q, track %d/%d, release group %q; want all cleared", ext, md.Album(), x, n, mbz.Extract(md).ReleaseGroupID)
		}
		if md.Title() != fixtures.Title {
			t.Errorf("%s: title %q, want it left alone", ext, md.Title())
		}
	}
}

// TestNonASCIIPath saves to a file whose folder and name are not ASCII.
func TestNonASCIIPath(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "Björk ☕")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "Jóga 日本.flac")
	data, err := os.ReadFile(fixtures.Tagged(".flac"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := (Writer{}).Save(path, Values{Title: {"Jóga"}}, marker.New(marker.Hand, day)); err != nil {
		t.Fatal(err)
	}
	if got := read(t, path).Title(); got != "Jóga" {
		t.Errorf("Title() = %q, want Jóga", got)
	}
}

func TestErrorNamesTheFile(t *testing.T) {
	_, err := Writer{}.Save(filepath.Join(t.TempDir(), "missing.flac"), nil, marker.New(marker.Hand, day))
	if err == nil || !strings.Contains(err.Error(), "missing.flac") {
		t.Errorf("Save of a missing file = %v, want an error naming it", err)
	}
}
