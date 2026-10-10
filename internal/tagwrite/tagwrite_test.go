package tagwrite

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"go.senan.xyz/taglib"

	"github.com/zachjamesgreen/tagshelf/internal/fixtures"
	"github.com/zachjamesgreen/tagshelf/internal/marker"
	"github.com/zachjamesgreen/tagshelf/internal/store"
	"github.com/zachjamesgreen/tagshelf/internal/tag"
	"github.com/zachjamesgreen/tagshelf/internal/tag/mbz"
)

var day = time.Date(2026, 9, 28, 15, 4, 5, 0, time.UTC)

// copyFixture copies a fixture into a new temporary folder under name and
// returns its path.
func copyFixture(t *testing.T, src, name string) string {
	t.Helper()
	return writeTemp(t, mustRead(t, src), name)
}

// writeTemp writes b to a file called name in a new temporary folder and
// returns its path.
func writeTemp(t *testing.T, b []byte, name string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// audioSum is tag.Sum of the file at path.
func audioSum(t *testing.T, path string) string {
	t.Helper()
	s, err := sum(path)
	if err != nil {
		t.Fatal(err)
	}
	return s
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

// taggableExtensions are the extensions Save writes, each with the extension
// of the files it is copied from: see untagged and tagged.
var taggableExtensions = map[string]string{
	".mp3": ".mp3", ".flac": ".flac", ".ogg": ".ogg", ".m4a": ".m4a", ".m4b": ".m4b",
	".m4r": ".m4a", ".m4p": ".m4a", ".wav": ".wav", ".ape": ".ape", ".mpc": ".mpc",
	".aac": ".aac",
	".wma": ".wma",
	".oga": ".ogg", ".opus": ".opus", ".dsf": ".dsf",
}

// untagged returns the path of a file of the format of ext without tags:
// the shared fixture, or for the two formats that have none a file of this
// package's own. The Opus file is a second of silence from ffmpeg's libopus;
// the DSF file is built here, since ffmpeg writes none.
func untagged(t *testing.T, ext string) string {
	t.Helper()
	switch ext {
	case ".opus":
		return filepath.Join("testdata", "untagged.opus")
	case ".dsf":
		return writeTemp(t, untaggedDSF(), "untagged.dsf")
	}
	return fixtures.Untagged(ext)
}

// tagged is untagged with the tags every tagged fixture carries. For Opus
// and DSF they are written here by TagLib, under the key names Picard uses.
func tagged(t *testing.T, ext string) string {
	t.Helper()
	if ext != ".opus" && ext != ".dsf" {
		return fixtures.Tagged(ext)
	}
	keys := map[string][]string{
		taglib.Title: {fixtures.Title}, taglib.Artist: {fixtures.Artist}, taglib.AlbumArtist: {fixtures.AlbumArtist},
		taglib.Album: {fixtures.Album}, taglib.Date: {strconv.Itoa(fixtures.Year)},
		taglib.MusicBrainzArtistID: {fixtures.ArtistID}, taglib.MusicBrainzAlbumArtistID: {fixtures.AlbumArtistID},
		taglib.MusicBrainzAlbumID: {fixtures.ReleaseID}, taglib.MusicBrainzReleaseGroupID: {fixtures.ReleaseGroupID},
		taglib.MusicBrainzTrackID: {fixtures.RecordingID}, taglib.MusicBrainzReleaseTrackID: {fixtures.ReleaseTrackID},
	}
	if ext == ".opus" {
		// Vorbis comments: the totals are keys of their own, here once.
		keys[taglib.TrackNumber], keys["TRACKTOTAL"] = []string{strconv.Itoa(fixtures.Track)}, []string{strconv.Itoa(fixtures.TrackTotal)}
		keys[taglib.DiscNumber], keys["DISCTOTAL"] = []string{strconv.Itoa(fixtures.Disc)}, []string{strconv.Itoa(fixtures.DiscTotal)}
	} else {
		keys[taglib.TrackNumber] = []string{fmt.Sprintf("%d/%d", fixtures.Track, fixtures.TrackTotal)}
		keys[taglib.DiscNumber] = []string{fmt.Sprintf("%d/%d", fixtures.Disc, fixtures.DiscTotal)}
	}
	path := copyFixture(t, untagged(t, ext), "tagged"+ext)
	if err := taglib.WriteTags(path, keys, 0); err != nil {
		t.Fatal(err)
	}
	return path
}

// untaggedDSF builds a DSF file of two channels of DSD silence, one block
// each: the DSD chunk, which says how long the file is and that it has no
// tag, the fmt chunk and the data chunk.
func untaggedDSF() []byte {
	const block = 4096
	audio := bytes.Repeat([]byte{0x69}, 2*block)
	le := binary.LittleEndian
	b := append([]byte("DSD "), le.AppendUint64(nil, 28)...)
	b = le.AppendUint64(b, uint64(28+52+12+len(audio)))
	b = le.AppendUint64(b, 0) // no ID3v2 tag
	b = append(b, "fmt "...)
	b = le.AppendUint64(b, 52)
	// Version 1, raw DSD, stereo, two channels, 2.8224 MHz, one bit a sample.
	for _, v := range []uint32{1, 0, 2, 2, 2822400, 1} {
		b = le.AppendUint32(b, v)
	}
	b = le.AppendUint64(b, block*8) // samples in each channel
	b = le.AppendUint32(b, block)
	b = le.AppendUint32(b, 0)
	b = append(b, "data"...)
	b = le.AppendUint64(b, uint64(12+len(audio)))
	return append(b, audio...)
}

// TestRoundTrip saves every field to each taggable format, from untagged and
// from tagged files, and reads them back with internal/tag, mbz, the marker
// reader and go-taglib. The audio must be the same afterwards: by tag.Sum,
// and where it is one block of the file, byte for byte.
func TestRoundTrip(t *testing.T) {
	for ext, from := range taggableExtensions {
		for _, src := range []string{untagged(t, from), tagged(t, from)} {
			t.Run(filepath.Base(src)+"->"+ext, func(t *testing.T) {
				path := copyFixture(t, src, "song"+ext)
				keysBefore, err := taglib.ReadTags(path)
				if err != nil {
					t.Fatal(err)
				}
				sumBefore := audioSum(t, path)
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

				if got := audioSum(t, path); got != sumBefore {
					t.Errorf("Sum changed from %s to %s", sumBefore, got)
				}
				if block := audioBlock(t, from); block != nil && !bytes.Contains(mustRead(t, path), block) {
					t.Error("the audio's bytes are no longer in the file")
				}
			})
		}
	}
}

// audioBlock returns the audio of the fixtures for ext where it is one run
// of bytes that every tagged copy must still hold: the whole untagged file,
// or a WAV file's data chunk. It is nil for the other formats.
func audioBlock(t *testing.T, ext string) []byte {
	t.Helper()
	switch ext {
	case ".mp3", ".aac", ".ape", ".mpc":
		return mustRead(t, fixtures.Untagged(ext))
	case ".wav":
		b := mustRead(t, fixtures.Untagged(ext))
		i := bytes.Index(b, []byte("data"))
		if i < 0 {
			t.Fatal("untagged.wav has no data chunk")
		}
		return b[i : i+8+int(binary.LittleEndian.Uint32(b[i+4:]))]
	}
	return nil
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
		t.Run(ext, func(t *testing.T) {
			path := copyFixture(t, fixtures.Tagged(ext), "song"+ext)
			if _, err := (Writer{}).Save(path, Values{ArtistIDs: ids, AlbumArtistIDs: ids[:2]}, marker.New(marker.Hand, day)); err != nil {
				t.Fatal(err)
			}
			got := mbz.Extract(read(t, path))
			if !slices.Equal(got.ArtistIDs, ids) || !slices.Equal(got.AlbumArtistIDs, ids[:2]) {
				t.Errorf("artist IDs %q, album artist IDs %q; want %q and %q", got.ArtistIDs, got.AlbumArtistIDs, ids, ids[:2])
			}
		})
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
		{fixtures.Tagged(".mp3"), "song.aac"},
		{fixtures.Untagged(".mp3"), "song.aac"},
		{fixtures.Tagged(".mp3"), "song.dsf"},
		{fixtures.Tagged(".flac"), "song.opus"},
		{fixtures.Untagged(".mp3"), "song.opus"},
		// FLAC in an Ogg stream: TagLib writes its tags, but internal/tag
		// does not read them back, so the file is left as it was.
		{filepath.Join("testdata", "oggflac.oga"), "song.oga"},
	} {
		path := copyFixture(t, tt.src, tt.name)
		orig := mustRead(t, path)
		_, err := Writer{}.Save(path, Values{Title: {"X"}}, marker.New(marker.Hand, day))
		if !errors.Is(err, ErrNotVerified) {
			t.Errorf("%s as %s: Save = %v, want ErrNotVerified", filepath.Base(tt.src), tt.name, err)
		}
		if now := mustRead(t, path); !bytes.Equal(now, orig) {
			t.Errorf("%s as %s: the original changed", filepath.Base(tt.src), tt.name)
		}
		if l := leftovers(t, path); l != nil {
			t.Errorf("%s as %s: temporary files left behind: %v", filepath.Base(tt.src), tt.name, l)
		}
	}
}

// An Ogg stream is tagged for what it holds, Vorbis or Opus, under any of
// the three extensions a run takes for one.
func TestOggStreamUnderAnyOggExtension(t *testing.T) {
	for _, tt := range []struct{ from, name string }{
		{".opus", "song.ogg"}, {".opus", "song.oga"}, {".ogg", "song.opus"},
	} {
		path := copyFixture(t, tagged(t, tt.from), tt.name)
		before := audioSum(t, path)
		m := marker.New(marker.Hand, day)
		if _, err := (Writer{}).Save(path, Values{Title: {"Another"}, Track: {"2/5"}}, m); err != nil {
			t.Errorf("%s audio as %s: %v", tt.from, tt.name, err)
			continue
		}
		md := read(t, path)
		if got, ok := marker.Read(md); md.Title() != "Another" || md.Album() != fixtures.Album || !ok || got != m {
			t.Errorf("%s audio as %s reads back title %q, album %q, marker %v", tt.from, tt.name, md.Title(), md.Album(), got)
		}
		if got := audioSum(t, path); got != before {
			t.Errorf("%s audio as %s: Sum changed from %s to %s", tt.from, tt.name, before, got)
		}
	}
}

// The files this package makes for the formats without a fixture are what
// they are meant to be: TagLib and internal/tag read them as Opus and DSF,
// tagged or not, and hash the same audio in both.
func TestOwnFilesAreWhatTheyClaim(t *testing.T) {
	for ext, want := range map[string]struct {
		codec    string
		fileType tag.FileType
	}{".opus": {"opus", tag.OGG}, ".dsf": {"dsd", tag.DSF}} {
		plain, with := untagged(t, ext), tagged(t, ext)
		props, err := taglib.ReadProperties(plain)
		if err != nil || props.InnerCodec != want.codec || props.Channels == 0 {
			t.Errorf("%s: TagLib reads it as %+v, %v; want %s audio", ext, props, err, want.codec)
		}
		if keys, err := taglib.ReadTags(plain); err != nil || len(keys[taglib.Title]) != 0 {
			t.Errorf("untagged%s has tags: %v, %v", ext, keys, err)
		}
		md := read(t, with)
		if x, n := md.Track(); md.FileType() != want.fileType || md.Title() != fixtures.Title || md.Year() != fixtures.Year || x != fixtures.Track || n != fixtures.TrackTotal {
			t.Errorf("tagged%s reads as %s with title %q, year %d, track %d/%d", ext, md.FileType(), md.Title(), md.Year(), x, n)
		}
		if a, b := audioSum(t, plain), audioSum(t, with); a != b {
			t.Errorf("%s: tagging the file changed its Sum from %s to %s", ext, a, b)
		}
	}
}

func TestUnsupportedFormats(t *testing.T) {
	// Every format a run indexes is taggable; the extension decides. A run
	// leaves these two with the files that are not audio.
	for _, ext := range []string{".aiff", ".wv"} {
		path := copyFixture(t, fixtures.Tagged(".mp3"), "song"+ext)
		orig := mustRead(t, path)
		_, err := Writer{}.Save(path, Values{Title: {"X"}}, marker.New(marker.Hand, day))
		if !errors.Is(err, ErrUnsupported) || err.Error() != "tagging not supported for this format" {
			t.Errorf("%s: Save = %v, want %q", ext, err, ErrUnsupported)
		}
		if now := mustRead(t, path); !bytes.Equal(now, orig) {
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
	// The fixture has its total under one of the two Vorbis keys, which is
	// not how a save writes it, so the record keeps the keys as they stood.
	wantBefore := Values{Title: {fixtures.Title}, Album: {fixtures.Album}, Track: {"3/10"}, Marker: {},
		TrackStored: {"TRACKNUMBER=3", "TRACKTOTAL=10"}}
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
	orig := mustRead(t, path)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	res, err = w.Save(path, Values{Title: {"New Title"}, Album: {}, Track: {"4/12"}}, marker.New(marker.Hand, day.AddDate(0, 0, 1)))
	if err != nil {
		t.Fatal(err)
	}
	if res.Changed {
		t.Error("Changed = true for a save that changes nothing")
	}
	if writes, err := s.TagWrites(run); err != nil || len(writes) != 1 {
		t.Errorf("a no-change save was recorded: %d tag writes, %v", len(writes), err)
	}
	info2, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if now := mustRead(t, path); !bytes.Equal(now, orig) || !info2.ModTime().Equal(info.ModTime()) {
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
	orig := mustRead(t, path)
	_, err = Writer{History: s, RunID: 999}.Save(path, Values{Title: {"X"}}, marker.New(marker.Hand, day))
	if err == nil {
		t.Fatal("Save succeeded with a run that does not exist")
	}
	if now := mustRead(t, path); !bytes.Equal(now, orig) {
		t.Error("the original changed")
	}
	if l := leftovers(t, path); l != nil {
		t.Errorf("temporary files left behind: %v", l)
	}
}

func TestSaveRejectsBadValues(t *testing.T) {
	path := copyFixture(t, fixtures.Tagged(".flac"), "song.flac")
	orig := mustRead(t, path)
	for _, v := range []Values{
		{Title: {"a", "b"}},
		{Track: {"x"}},
		{Disc: {"1/y"}},
		{Date: {"2003-04"}},
		{Compilation: {"yes"}},
		{Marker: {"hand"}},
		{TrackStored: {"TRACKNUMBER=03"}},
		{Field("genre"): {"Rock"}},
	} {
		if _, err := (Writer{}).Save(path, v, marker.New(marker.Hand, day)); err == nil || errors.Is(err, ErrNotVerified) {
			t.Errorf("Save(%v) = %v, want a validation error", v, err)
		}
	}
	if now := mustRead(t, path); !bytes.Equal(now, orig) {
		t.Error("the file changed")
	}
}

// picardTotals gives the copy of a Vorbis fixture at path the second pair of
// total keys Picard writes: the fixtures have TRACKTOTAL and DISCTOTAL only.
func picardTotals(t *testing.T, path string) {
	t.Helper()
	keys := keysOf(t, path)
	err := taglib.WriteTags(path, map[string][]string{"TOTALTRACKS": keys["TRACKTOTAL"], "TOTALDISCS": keys["DISCTOTAL"]}, 0)
	if err != nil {
		t.Fatal(err)
	}
}

// TestClearFields checks in every format that fields given no values are
// removed and the others left alone. The Vorbis files have the totals under
// both of the keys Picard writes, and lose both.
func TestClearFields(t *testing.T) {
	for ext, from := range taggableExtensions {
		t.Run(ext, func(t *testing.T) {
			f := formats[ext]
			path := copyFixture(t, tagged(t, from), "song"+ext)
			if f.vorbis {
				picardTotals(t, path)
			}
			if _, err := (Writer{}).Save(path, Values{Album: nil, Track: {}, ReleaseGroupID: {""}}, marker.New(marker.Hand, day)); err != nil {
				t.Fatal(err)
			}
			md := read(t, path)
			if x, n := md.Track(); md.Album() != "" || x != 0 || n != 0 || mbz.Extract(md).ReleaseGroupID != "" {
				t.Errorf("album %q, track %d/%d, release group %q; want all cleared", md.Album(), x, n, mbz.Extract(md).ReleaseGroupID)
			}
			if md.Title() != fixtures.Title {
				t.Errorf("title %q, want it left alone", md.Title())
			}
			keys := keysOf(t, path)
			for _, k := range []string{taglib.Album, taglib.TrackNumber, "TRACKTOTAL", "TOTALTRACKS", taglib.MusicBrainzReleaseGroupID} {
				if v, ok := keys[k]; ok {
					t.Errorf("%s = %q is still there", k, v)
				}
			}
			wantDisc := map[string][]string{taglib.DiscNumber: {"1/2"}}
			if f.vorbis {
				wantDisc = map[string][]string{taglib.DiscNumber: {"1"}, "DISCTOTAL": {"2"}, "TOTALDISCS": {"2"}}
			}
			for k, want := range wantDisc {
				if !slices.Equal(keys[k], want) {
					t.Errorf("%s = %q, want %q left alone", k, keys[k], want)
				}
			}
		})
	}
}

// TestVorbisTotals checks that a number goes under both total keys, and
// that a number saved without a total takes both away.
func TestVorbisTotals(t *testing.T) {
	for _, ext := range []string{".flac", ".ogg"} {
		t.Run(ext, func(t *testing.T) {
			path := copyFixture(t, fixtures.Tagged(ext), "song"+ext)
			picardTotals(t, path)
			if _, err := (Writer{}).Save(path, Values{Track: {"4/9"}, Disc: {"2"}}, marker.New(marker.Hand, day)); err != nil {
				t.Fatal(err)
			}
			keys := keysOf(t, path)
			want := map[string][]string{
				taglib.TrackNumber: {"4"}, "TRACKTOTAL": {"9"}, "TOTALTRACKS": {"9"},
				taglib.DiscNumber: {"2"}, "DISCTOTAL": nil, "TOTALDISCS": nil,
			}
			for k, v := range want {
				if !slices.Equal(keys[k], v) {
					t.Errorf("%s = %q, want %q", k, keys[k], v)
				}
			}
			if x, n := read(t, path).Disc(); x != 2 || n != 0 {
				t.Errorf("Disc() = %d/%d, want 2/0", x, n)
			}
		})
	}
}

// TestOtherTagsAndCoverKept gives a file of every format tags tagshelf does
// not write and a cover, which no fixture has: a save of every field and its
// restore leave them as they were, a list in the order it had.
func TestOtherTagsAndCoverKept(t *testing.T) {
	others := map[string][]string{
		"GENRE": {"Ambient"}, "COMMENT": {"A comment"}, "LYRICS": {"La la\nla"},
		"REPLAYGAIN_TRACK_GAIN": {"-6.50 dB"}, "COMPOSER": {"Someone", "Someone Else"},
	}
	cover := append([]byte{0xff, 0xd8, 0xff, 0xe0, 0, 16, 'J', 'F', 'I', 'F', 0}, bytes.Repeat([]byte{0x5a}, 300)...)
	for ext, from := range taggableExtensions {
		t.Run(ext, func(t *testing.T) {
			path := copyFixture(t, tagged(t, from), "song"+ext)
			others := maps.Clone(others)
			if ext == ".wma" {
				delete(others, "REPLAYGAIN_TRACK_GAIN") // TagLib has no ASF key for it
			}
			if err := taglib.WriteTags(path, others, 0); err != nil {
				t.Fatal(err)
			}
			if err := taglib.WriteImage(path, cover); err != nil {
				t.Fatal(err)
			}
			keysBefore := keysOf(t, path)
			for k, v := range others {
				if !slices.Equal(keysBefore[k], v) {
					t.Fatalf("%s = %q before the save, want %q", k, keysBefore[k], v)
				}
			}
			check := func(when string, written map[string][]string) {
				t.Helper()
				keys := keysOf(t, path)
				for k, v := range keysBefore {
					if _, ok := written[k]; !ok && !slices.Equal(keys[k], v) {
						t.Errorf("%s: %s changed from %q to %q", when, k, v, keys[k])
					}
				}
				for k, v := range keys {
					if _, ok := keysBefore[k]; !ok && written[k] == nil {
						t.Errorf("%s: %s = %q was added", when, k, v)
					}
				}
				if got, err := taglib.ReadImage(path); err != nil || !bytes.Equal(got, cover) {
					t.Errorf("%s: the cover is %d bytes, %v; want the %d put there", when, len(got), err, len(cover))
				}
			}

			m := marker.New(marker.MusicBrainz, day)
			res, err := Writer{}.Save(path, musicBrainzSave(), m)
			if err != nil {
				t.Fatal(err)
			}
			written := toKeys(formats[ext], res.After)
			check("after the save", written)
			if got := read(t, path).Title(); got != "New Title" {
				t.Errorf("Title() = %q, want the save made", got)
			}

			if _, err := (Writer{}).Restore(path, res.Before, res.After); err != nil {
				t.Fatal(err)
			}
			check("after the restore", nil)
		})
	}
}

// id3v1 builds an ID3v1 tag with the title given.
func id3v1(title string) []byte {
	b := make([]byte, id3v1Size)
	copy(b, "TAG"+title)
	b[127] = 255 // no genre
	return b
}

// TestNoID3v1TagAdded checks that a save leaves an MP3 or .aac file without
// the ID3v1 tag TagLib writes with every save, which would cut a long title
// short and cannot hold one that is not Latin-1. The APEv2 tag of an .aac
// file is the last thing in it, as Picard leaves it.
func TestNoID3v1TagAdded(t *testing.T) {
	title := "Ein sehr langer Titel, der nicht in dreißig Zeichen paßt 日本語"
	for _, name := range []string{"untagged.mp3", "tagged.mp3", "manual.mp3", "untagged.aac", "tagged.aac", fixtures.TaggedID3v2AAC} {
		t.Run(name, func(t *testing.T) {
			path := copyFixture(t, fixtures.Path(name), "song"+filepath.Ext(name))
			res, err := Writer{}.Save(path, Values{Title: {title}}, marker.New(marker.Hand, day))
			if err != nil {
				t.Fatal(err)
			}
			b := mustRead(t, path)
			if has, err := endsWithID3v1(path); err != nil || has || string(b[len(b)-id3v1Size:][:3]) == "TAG" {
				t.Errorf("the file ends with an ID3v1 tag: %t, %v", has, err)
			}
			if filepath.Ext(name) == ".aac" && string(b[len(b)-32:len(b)-24]) != "APETAGEX" {
				t.Error("the file does not end with its APEv2 tag")
			}
			if got := read(t, path).Title(); got != title {
				t.Errorf("Title() = %q, want %q", got, title)
			}
			if _, err := (Writer{}).Restore(path, res.Before, res.After); err != nil {
				t.Fatal(err)
			}
			if has, err := endsWithID3v1(path); err != nil || has {
				t.Errorf("after the restore the file ends with an ID3v1 tag: %t, %v", has, err)
			}
		})
	}
}

// TestID3v1TagKept checks that a file which has an ID3v1 tag keeps it: only
// a tag the save itself would add is taken off. TagLib brings it up to date.
func TestID3v1TagKept(t *testing.T) {
	for _, ext := range []string{".mp3", ".aac"} {
		t.Run(ext, func(t *testing.T) {
			audio := mustRead(t, fixtures.Untagged(ext))
			path := writeTemp(t, slices.Concat(mustRead(t, fixtures.Tagged(ext)), id3v1("Old Title")), "song"+ext)
			sumBefore := audioSum(t, path)
			if _, err := (Writer{}).Save(path, Values{Title: {"New Title"}}, marker.New(marker.Hand, day)); err != nil {
				t.Fatal(err)
			}
			b := mustRead(t, path)
			last := b[len(b)-id3v1Size:]
			if has, err := endsWithID3v1(path); err != nil || !has {
				t.Fatalf("the ID3v1 tag is gone: %v", err)
			}
			if got := string(bytes.TrimRight(last[3:33], "\x00")); got != "New Title" {
				t.Errorf("ID3v1 title = %q, want New Title", got)
			}
			if n := bytes.Count(b, []byte("TAGNew Title")); n != 1 {
				t.Errorf("%d ID3v1 tags, want one", n)
			}
			if got := read(t, path).Title(); got != "New Title" {
				t.Errorf("Title() = %q, want New Title", got)
			}
			if !bytes.Contains(b, audio) || audioSum(t, path) != sumBefore {
				t.Error("the audio changed")
			}
		})
	}
}

// TestEndsWithID3v1 checks what counts as an ID3v1 tag at the end of a file.
func TestEndsWithID3v1(t *testing.T) {
	ape := mustRead(t, fixtures.Tagged(".ape"))
	footer := slices.Clone(ape[len(ape)-32:])
	// An APEv2 tag whose last 128 bytes start with TAG, by chance.
	chance := slices.Concat(bytes.Repeat([]byte{1}, 200), []byte("TAG"), bytes.Repeat([]byte{1}, 93), footer)
	for name, tt := range map[string]struct {
		b    []byte
		want bool
	}{
		"an ID3v1 tag":                {slices.Concat(ape, id3v1("T")), true},
		"an APEv2 tag":                {ape, false},
		"an APEv2 footer after TAG":   {chance, false},
		"shorter than an ID3v1 tag":   {[]byte("TAG"), false},
		"only an ID3v1 tag":           {id3v1("T"), true},
		"TAG elsewhere in the ending": {slices.Concat(ape, []byte("xTAG"), make([]byte, 126)), false},
	} {
		if got, err := endsWithID3v1(writeTemp(t, tt.b, "song.mp3")); err != nil || got != tt.want {
			t.Errorf("%s: endsWithID3v1 = %t, %v; want %t", name, got, err, tt.want)
		}
	}
	if _, err := endsWithID3v1(filepath.Join(t.TempDir(), "missing.mp3")); err == nil {
		t.Error("endsWithID3v1 of a missing file = nil")
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

// Read returns what a person edits in the form Save takes it, so that
// saving what was read changes nothing.
func TestReadReturnsWhatSaveTakes(t *testing.T) {
	for _, ext := range fixtures.ManualExtensions() {
		t.Run("manual"+ext, func(t *testing.T) {
			path := copyFixture(t, fixtures.Manual(ext), "song"+ext)
			got, err := Read(path)
			if err != nil {
				t.Fatal(err)
			}
			if first(got[Title]) != fixtures.Title || first(got[Album]) != fixtures.Album || first(got[Marker]) != fixtures.ManualMarker {
				t.Errorf("read %v", got)
			}
			again := Values{}
			for _, f := range []Field{Title, Artist, AlbumArtist, Album, Date, Track, Disc} {
				again[f] = got[f]
			}
			res, err := Writer{}.Save(path, again, marker.New(marker.Hand, day))
			if err != nil || res.Changed {
				t.Errorf("saving what was read: changed %t, %v", res.Changed, err)
			}
		})
	}
	for ext, from := range taggableExtensions {
		t.Run("tagged"+ext, func(t *testing.T) {
			got, err := Read(copyFixture(t, tagged(t, from), "song"+ext))
			if err != nil {
				t.Fatal(err)
			}
			want := Values{
				Title: {fixtures.Title}, Artist: {fixtures.Artist}, AlbumArtist: {fixtures.AlbumArtist}, Album: {fixtures.Album},
				Date: {"2001"}, Track: {"3/10"}, Disc: {"1/2"}, Compilation: {},
				RecordingID: {fixtures.RecordingID}, ReleaseTrackID: {fixtures.ReleaseTrackID}, AlbumID: {fixtures.ReleaseID},
				ReleaseGroupID: {fixtures.ReleaseGroupID}, ArtistIDs: {fixtures.ArtistID}, AlbumArtistIDs: {fixtures.AlbumArtistID},
				Marker: {},
			}
			if ext == ".wma" {
				want[Track] = []string{"3"} // Picard writes no total for WMA
			}
			if !reflect.DeepEqual(got, want) {
				t.Errorf("Read = %v\nwant   %v", got, want)
			}
		})
	}
	if _, err := Read("song.xyz"); !errors.Is(err, ErrUnsupported) {
		t.Errorf("Read of an unknown format: %v", err)
	}
}

// history is a tag history that remembers what it was told.
type history struct {
	rows []struct {
		runID               int64
		path, before, after string
	}
}

func (h *history) RecordTagWrite(runID int64, path, before, after string, apply func() error) error {
	if err := apply(); err != nil {
		return err
	}
	h.rows = append(h.rows, struct {
		runID               int64
		path, before, after string
	}{runID, path, before, after})
	return nil
}
