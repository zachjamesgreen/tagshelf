package fixtures

import (
	"bytes"
	"encoding/binary"
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode/utf16"

	"go.senan.xyz/taglib"

	"tagshelf/internal/marker"
	"tagshelf/internal/rules"
	"tagshelf/internal/tag"
	"tagshelf/internal/tag/mbz"
)

// maxSize keeps fixtures small; the TagLib APE file is the largest.
const maxSize = 100 << 10

func allFixtures() []string {
	var names []string
	for _, ext := range Extensions() {
		names = append(names, "untagged"+ext, "tagged"+ext)
	}
	for _, ext := range ManualExtensions() {
		names = append(names, "manual"+ext)
	}
	return append(names, UntaggedOddWAV, UntaggedSV8MPC, TaggedID3v2AAC, TaggedVAM4A, ManualNoAlbumFLAC, RipperFLAC)
}

// TestEveryFixtureIsListed checks that testdata/audio holds the fixtures
// this package names and no others.
func TestEveryFixtureIsListed(t *testing.T) {
	entries, err := os.ReadDir(Dir())
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, e := range entries {
		if !e.IsDir() && e.Name() != "README.md" {
			got = append(got, e.Name())
		}
	}
	want := allFixtures()
	slices.Sort(got)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Errorf("testdata/audio holds\n%q\nwant\n%q", got, want)
	}
}

// id3v2Size returns the length of the ID3v2 tag that starts b, header
// included: its size is four bytes of seven bits each.
func id3v2Size(b []byte) int {
	return 10 + (int(b[6])<<21 | int(b[7])<<14 | int(b[8])<<7 | int(b[9]))
}

// TestID3v2Size checks the size against tags whose last size byte has the
// bits of the header's ten set, where adding the ten and or-ing it in
// differ.
func TestID3v2Size(t *testing.T) {
	for _, tt := range []struct {
		size [4]byte
		want int
	}{
		{[4]byte{0, 0, 4, 0x45}, 591},
		{[4]byte{0, 0, 1, 0x3e}, 200},
		{[4]byte{0, 0, 0, 0x0a}, 20},
		{[4]byte{0, 0, 0, 0x7f}, 137},
		{[4]byte{1, 2, 3, 4}, 10 + 1<<21 + 2<<14 + 3<<7 + 4},
	} {
		b := append([]byte{'I', 'D', '3', 4, 0, 0}, tt.size[:]...)
		if got := id3v2Size(b); got != tt.want {
			t.Errorf("id3v2Size with size bytes % x = %d, want %d", tt.size, got, tt.want)
		}
	}
	// The manual MP3 is such a tag: the audio starts right after it.
	b := readFixture(t, "manual.mp3")
	if n := id3v2Size(b); n != len(b)-len(readFixture(t, "untagged.mp3")) {
		t.Errorf("manual.mp3: ID3v2 tag of %d bytes, want %d", n, len(b)-len(readFixture(t, "untagged.mp3")))
	}
}

func readFixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(Path(name))
	if err != nil {
		t.Fatalf("fixture %s: %v", name, err)
	}
	return data
}

func TestFixturesExistAndAreSmall(t *testing.T) {
	for _, name := range allFixtures() {
		data := readFixture(t, name)
		if len(data) == 0 || len(data) > maxSize {
			t.Errorf("%s is %d bytes, want 1..%d", name, len(data), maxSize)
		}
	}
}

// TestFormatSignatures checks each fixture is the container it claims to be.
// A tagged fixture with a leading ID3v2 tag is checked after the tag.
func TestFormatSignatures(t *testing.T) {
	asfHeader := []byte{0x30, 0x26, 0xB2, 0x75, 0x8E, 0x66, 0xCF, 0x11}
	tests := []struct {
		name string
		ok   func(b []byte) bool
	}{
		{".mp3", func(b []byte) bool { return b[0] == 0xFF && b[1]&0xE0 == 0xE0 }},
		{".flac", func(b []byte) bool { return string(b[:4]) == "fLaC" }},
		{".ogg", func(b []byte) bool { return string(b[:4]) == "OggS" }},
		{".m4a", func(b []byte) bool { return string(b[4:12]) == "ftypM4A " }},
		{".m4b", func(b []byte) bool { return string(b[4:12]) == "ftypM4B " }},
		{".wav", func(b []byte) bool { return string(b[:4]) == "RIFF" && string(b[8:12]) == "WAVE" }},
		{".wma", func(b []byte) bool { return bytes.HasPrefix(b, asfHeader) }},
		{".aac", func(b []byte) bool { return b[0] == 0xFF && b[1]&0xF6 == 0xF0 }}, // ADTS, not MP3
		{".ape", func(b []byte) bool { return string(b[:4]) == "MAC " }},
		{".mpc", func(b []byte) bool { return string(b[:3]) == "MP+" }},
	}
	check := func(name string, ok func([]byte) bool) {
		b := readFixture(t, name)
		if bytes.HasPrefix(b, []byte("ID3")) && len(b) >= 10 {
			b = b[id3v2Size(b):]
		}
		if len(b) < 12 {
			t.Errorf("%s is too short to hold a signature", name)
			return
		}
		if !ok(b) {
			t.Errorf("%s does not start with its format's signature: % x", name, b[:12])
		}
	}
	for _, tt := range tests {
		check("untagged"+tt.name, tt.ok)
		check("tagged"+tt.name, tt.ok)
	}
	byExt := func(ext string) func([]byte) bool {
		for _, tt := range tests {
			if tt.name == ext {
				return tt.ok
			}
		}
		panic(ext)
	}
	for _, ext := range ManualExtensions() {
		check("manual"+ext, byExt(ext))
	}
	check(ManualNoAlbumFLAC, byExt(".flac"))
	check(RipperFLAC, byExt(".flac"))
	check(UntaggedOddWAV, byExt(".wav"))
	check(TaggedID3v2AAC, byExt(".aac"))
	check(TaggedVAM4A, byExt(".m4a"))
	check(UntaggedSV8MPC, func(b []byte) bool { return string(b[:4]) == "MPCK" })
}

// TestReadableTags reads every tagged fixture with internal/tag, and checks
// the untagged ones carry no tags.
func TestReadableTags(t *testing.T) {
	tagged := []string{TaggedID3v2AAC, TaggedVAM4A}
	untagged := []string{UntaggedOddWAV, UntaggedSV8MPC}
	for _, ext := range Extensions() {
		tagged = append(tagged, "tagged"+ext)
		untagged = append(untagged, "untagged"+ext)
	}
	for _, name := range tagged {
		t.Run(name, func(t *testing.T) {
			m := readTags(t, name)
			if m == nil {
				t.Fatal("no tags read")
			}
			albumArtist, albumArtistID, trackTotal := AlbumArtist, AlbumArtistID, TrackTotal
			switch name {
			case TaggedVAM4A: // known only by its album artist ID
				albumArtist, albumArtistID = "", rules.VariousArtistsMBID
			case "tagged.wma": // Picard writes no total for WMA
				trackTotal = 0
			}
			if m.Title() != Title || m.Artist() != Artist || m.AlbumArtist() != albumArtist || m.Album() != Album || m.Year() != Year {
				t.Errorf("got %q / %q / %q / %q / %d", m.Title(), m.Artist(), m.AlbumArtist(), m.Album(), m.Year())
			}
			if n, total := m.Track(); n != Track || total != trackTotal {
				t.Errorf("track %d/%d, want %d/%d", n, total, Track, trackTotal)
			}
			if n, total := m.Disc(); n != Disc || total != DiscTotal {
				t.Errorf("disc %d/%d, want %d/%d", n, total, Disc, DiscTotal)
			}
			want := mbz.IDs{
				RecordingID: RecordingID, ReleaseTrackID: ReleaseTrackID, AlbumID: ReleaseID, ReleaseGroupID: ReleaseGroupID,
				ArtistIDs: []string{ArtistID}, AlbumArtistIDs: []string{albumArtistID},
			}
			if got := mbz.Extract(m); !reflect.DeepEqual(got, want) {
				t.Errorf("MusicBrainz IDs = %+v, want %+v", got, want)
			}
			if got, ok := marker.Read(m); ok {
				t.Errorf("has the manual marker %q", got)
			}
		})
	}
	for _, name := range untagged {
		t.Run(name, func(t *testing.T) {
			if m := readTags(t, name); m != nil && (m.Title() != "" || m.Artist() != "" || m.Album() != "") {
				t.Errorf("untagged fixture has tags: %q / %q / %q", m.Title(), m.Artist(), m.Album())
			}
		})
	}
}

// readTags returns nil when the file has no tags at all.
func readTags(t *testing.T, name string) tag.Metadata {
	t.Helper()
	f, err := os.Open(Path(name))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	m, err := tag.ReadFrom(f)
	if err == tag.ErrNoTagsFound {
		return nil
	}
	if err != nil {
		t.Fatalf("reading %s: %v", name, err)
	}
	return m
}

// TestTagLayouts checks where the tags sit in the WAV, WMA, APE, MPC and .aac
// fixtures, and that the untagged fixtures have none.
func TestTagLayouts(t *testing.T) {
	for _, name := range []string{"tagged.aac", "tagged.ape", "tagged.mpc"} {
		b := readFixture(t, name)
		if len(b) < 32 || string(b[len(b)-32:len(b)-24]) != "APETAGEX" {
			t.Errorf("%s does not end with an APEv2 footer", name)
		}
		if !bytes.Contains(b, []byte("musicbrainz_trackid\x00"+RecordingID)) {
			t.Errorf("%s has no recording ID under Picard's APEv2 key", name)
		}
	}
	for _, name := range []string{"tagged.mp3", TaggedID3v2AAC} {
		if b := readFixture(t, name); string(b[:3]) != "ID3" || b[3] != 4 {
			t.Errorf("%s does not start with an ID3v2.4 tag", name)
		}
	}

	wav := readFixture(t, "tagged.wav")
	if got := int(binary.LittleEndian.Uint32(wav[4:8])); got != len(wav)-8 {
		t.Errorf("tagged.wav RIFF size %d, want %d", got, len(wav)-8)
	}
	if !hasChunk(wav, "id3 ") {
		t.Error("tagged.wav has no id3 chunk")
	}
	odd := readFixture(t, UntaggedOddWAV)
	if i := bytes.Index(odd, []byte("data")); i < 0 || binary.LittleEndian.Uint32(odd[i+4:])%2 != 1 {
		t.Errorf("%s does not have an odd-sized data chunk", UntaggedOddWAV)
	}

	if !bytes.Contains(readFixture(t, "tagged.wma"), utf16le("MusicBrainz/Track Id")) {
		t.Error("tagged.wma has no MusicBrainz/Track Id descriptor")
	}

	untagged := []string{UntaggedOddWAV, UntaggedSV8MPC}
	for _, ext := range Extensions() {
		untagged = append(untagged, "untagged"+ext)
	}
	for _, name := range untagged {
		b := readFixture(t, name)
		switch {
		case bytes.Contains(b, []byte("APETAGEX")),
			bytes.HasPrefix(b, []byte("ID3")),
			bytes.Contains(b, []byte(ReleaseID)),
			bytes.Contains(b, utf16le(ReleaseID)),
			bytes.Contains(b, []byte(Title)),
			bytes.Contains(b, utf16le(Title)),
			strings.HasSuffix(name, ".wav") && hasChunk(b, "id3 "):
			t.Errorf("%s carries tags", name)
		}
	}
}

// hasChunk reports whether a RIFF file has a top-level chunk with this id.
func hasChunk(riff []byte, id string) bool {
	for off := 12; off+8 <= len(riff); {
		if string(riff[off:off+4]) == id {
			return true
		}
		size := int64(binary.LittleEndian.Uint32(riff[off+4:]))
		next := int64(off) + 8 + size + size%2
		if next > int64(len(riff)) {
			return false
		}
		off = int(next)
	}
	return false
}

// TestManualFixtures checks the hand-tagged fixtures carry the fixture tags
// and the manual marker, but no MusicBrainz IDs.
func TestManualFixtures(t *testing.T) {
	var names []string
	for _, ext := range ManualExtensions() {
		names = append(names, "manual"+ext)
	}
	for _, name := range append(names, ManualNoAlbumFLAC, RipperFLAC) {
		t.Run(name, func(t *testing.T) {
			if b := readFixture(t, name); len(b) > maxSize {
				t.Errorf("%d bytes, want at most %d", len(b), maxSize)
			}
			m := readTags(t, name)
			if m == nil {
				t.Fatal("no tags read")
			}
			album := Album
			if name == ManualNoAlbumFLAC {
				album = ""
			}
			if m.Title() != Title || m.Artist() != Artist || m.AlbumArtist() != AlbumArtist || m.Album() != album || m.Year() != Year {
				t.Errorf("got %q / %q / %q / %q / %d", m.Title(), m.Artist(), m.AlbumArtist(), m.Album(), m.Year())
			}
			if n, total := m.Track(); n != Track || total != TrackTotal {
				t.Errorf("track %d/%d, want %d/%d", n, total, Track, TrackTotal)
			}
			if n, total := m.Disc(); n != Disc || total != DiscTotal {
				t.Errorf("disc %d/%d, want %d/%d", n, total, Disc, DiscTotal)
			}
			if ids := mbz.Extract(m); !reflect.DeepEqual(ids, mbz.IDs{}) {
				t.Errorf("carries MusicBrainz IDs: %+v", ids)
			}
			got, ok := marker.Read(m)
			if name == RipperFLAC {
				if ok {
					t.Errorf("has the manual marker %q, want none", got)
				}
				return
			}
			if want := marker.New(marker.Hand, time.Date(2026, 9, 27, 0, 0, 0, 0, time.UTC)); !ok || got != want || got.String() != ManualMarker {
				t.Errorf("manual marker = %q, %t; want %q", got, ok, ManualMarker)
			}
		})
	}
}

// TestWhatTheFixturesLack pins what the README says the fixtures do not
// have, as go-taglib reads them: tests in other packages rely on it, or
// make up for it in their own copies.
func TestWhatTheFixturesLack(t *testing.T) {
	// Every key any fixture holds: the tags tagshelf writes and no others.
	known := []string{
		taglib.Title, taglib.Artist, taglib.AlbumArtist, taglib.Album, taglib.Date,
		taglib.TrackNumber, "TRACKTOTAL", taglib.DiscNumber, "DISCTOTAL",
		taglib.MusicBrainzArtistID, taglib.MusicBrainzAlbumArtistID, taglib.MusicBrainzAlbumID,
		taglib.MusicBrainzReleaseGroupID, taglib.MusicBrainzTrackID, taglib.MusicBrainzReleaseTrackID,
		marker.Key,
	}
	for _, name := range allFixtures() {
		if name == UntaggedSV8MPC {
			continue // a header only: TagLib does not open it
		}
		t.Run(name, func(t *testing.T) {
			keys, err := taglib.ReadTags(Path(name))
			if err != nil {
				t.Fatal(err)
			}
			for k, v := range keys {
				if k == "ENCODER" && strings.HasSuffix(name, ".ogg") {
					continue // ffmpeg's
				}
				if !slices.Contains(known, k) {
					t.Errorf("has %s = %q, a tag tagshelf does not write", k, v)
				}
			}
			props, err := taglib.ReadProperties(Path(name))
			if err != nil {
				t.Fatal(err)
			}
			if len(props.Images) != 0 {
				t.Errorf("has %d pictures", len(props.Images))
			}

			// What the README says of the lengths.
			switch {
			case strings.HasSuffix(name, ".aac"):
				if props.Length != 0 || props.BitRate != 0 {
					t.Errorf("go-taglib reads %v at %d kb/s: the README says it reads no length", props.Length, props.BitRate)
				}
			case strings.HasSuffix(name, ".mpc"):
				if props.Length != 40*time.Millisecond {
					t.Errorf("go-taglib reads %v, the README says 40 ms", props.Length)
				}
			case props.Length <= 0:
				t.Errorf("go-taglib reads no length")
			}

			// The Vorbis fixtures have each total under one key.
			if ext := strings.TrimPrefix(name[strings.LastIndex(name, "."):], "."); (ext == "flac" || ext == "ogg") && !strings.HasPrefix(name, "untagged") {
				if !slices.Equal(keys["TRACKTOTAL"], []string{"10"}) || !slices.Equal(keys["DISCTOTAL"], []string{"2"}) {
					t.Errorf("TRACKTOTAL %q, DISCTOTAL %q; want 10 and 2", keys["TRACKTOTAL"], keys["DISCTOTAL"])
				}
			}
		})
	}
}

// The .aac fixtures hold nine ADTS frames of 1024 samples at 8 kHz, 1.152 s,
// whatever go-taglib makes of them.
func TestAACFrames(t *testing.T) {
	b := readFixture(t, "untagged.aac")
	frames := 0
	for off := 0; off+7 <= len(b); frames++ {
		if b[off] != 0xFF || b[off+1]&0xF6 != 0xF0 {
			t.Fatalf("no ADTS frame at %d", off)
		}
		off += int(b[off+3]&3)<<11 | int(b[off+4])<<3 | int(b[off+5])>>5
	}
	if frames != 9 {
		t.Errorf("%d ADTS frames, want 9", frames)
	}
}

func utf16le(s string) []byte {
	var b []byte
	for _, u := range utf16.Encode([]rune(s)) {
		b = binary.LittleEndian.AppendUint16(b, u)
	}
	return b
}

// The fixtures are found from any working directory: a test that changes
// its own need not ask for them first.
func TestDirDoesNotGoByTheWorkingDirectory(t *testing.T) {
	want := Dir()
	t.Chdir(t.TempDir())
	if got := Dir(); got != want {
		t.Errorf("in another working directory Dir() = %q, want %q", got, want)
	}
	if _, err := os.Stat(Tagged(".flac")); err != nil {
		t.Errorf("a fixture is not where Dir says: %v", err)
	}
}
