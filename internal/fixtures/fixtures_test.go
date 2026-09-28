package fixtures

import (
	"bytes"
	"encoding/binary"
	"os"
	"strings"
	"testing"
	"unicode/utf16"

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
	return append(names, UntaggedOddWAV, UntaggedSV8MPC, TaggedID3v2AAC, TaggedVAM4A)
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
			b = b[10+int(b[6])<<21|int(b[7])<<14|int(b[8])<<7|int(b[9]):]
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
	check(UntaggedOddWAV, byExt(".wav"))
	check(TaggedID3v2AAC, byExt(".aac"))
	check(TaggedVAM4A, byExt(".m4a"))
	check(UntaggedSV8MPC, func(b []byte) bool { return string(b[:4]) == "MPCK" })
}

// TestReadableTags reads the tagged fixtures of the formats internal/tag
// reads today, and checks the untagged ones carry no tags.
func TestReadableTags(t *testing.T) {
	for _, name := range []string{"tagged.mp3", "tagged.flac", "tagged.ogg", "tagged.m4a", "tagged.m4b",
		"tagged.wav", "tagged.ape", "tagged.mpc", "tagged.aac", TaggedID3v2AAC} {
		t.Run(name, func(t *testing.T) {
			m := readTags(t, name)
			if m == nil {
				t.Fatal("no tags read")
			}
			if m.Title() != Title || m.Artist() != Artist || m.AlbumArtist() != AlbumArtist || m.Album() != Album || m.Year() != Year {
				t.Errorf("got %q / %q / %q / %q / %d", m.Title(), m.Artist(), m.AlbumArtist(), m.Album(), m.Year())
			}
			if n, total := m.Track(); n != Track || total != TrackTotal {
				t.Errorf("track %d/%d, want %d/%d", n, total, Track, TrackTotal)
			}
			if n, total := m.Disc(); n != Disc || total != DiscTotal {
				t.Errorf("disc %d/%d, want %d/%d", n, total, Disc, DiscTotal)
			}
			// Every ID per format is checked in the mbz tests.
			if got := mbz.Extract(m).AlbumID; got != ReleaseID {
				t.Errorf("release ID = %q, want %q", got, ReleaseID)
			}
		})
	}
	for _, name := range []string{"untagged.mp3", "untagged.flac", "untagged.ogg", "untagged.m4a", "untagged.m4b",
		"untagged.wav", "untagged.ape", "untagged.mpc", "untagged.aac", UntaggedOddWAV, UntaggedSV8MPC} {
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

func utf16le(s string) []byte {
	var b []byte
	for _, u := range utf16.Encode([]rune(s)) {
		b = binary.LittleEndian.AppendUint16(b, u)
	}
	return b
}
