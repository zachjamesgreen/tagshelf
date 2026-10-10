package tag

import (
	"bytes"
	"encoding/binary"
	"os"
	"slices"
	"testing"

	"github.com/zachjamesgreen/tagshelf/internal/fixtures"
)

// apeItem is one APEv2 item for apeTag; binary marks a binary value.
type apeItem struct {
	key, value string
	binary     bool
}

// apeTag renders an APEv2 tag with header and footer, as Picard, TagLib and
// mutagen write one.
func apeTag(items ...apeItem) []byte {
	var body []byte
	for _, it := range items {
		body = binary.LittleEndian.AppendUint32(body, uint32(len(it.value)))
		var flags uint32
		if it.binary {
			flags = 1 << 1
		}
		body = binary.LittleEndian.AppendUint32(body, flags)
		body = append(append(append(body, it.key...), 0), it.value...)
	}
	block := func(flags uint32) []byte {
		b := []byte("APETAGEX")
		b = binary.LittleEndian.AppendUint32(b, 2000)
		b = binary.LittleEndian.AppendUint32(b, uint32(len(body)+32))
		b = binary.LittleEndian.AppendUint32(b, uint32(len(items)))
		b = binary.LittleEndian.AppendUint32(b, flags)
		return append(b, make([]byte, 8)...)
	}
	return slices.Concat(block(1<<31|1<<29), body, block(1<<31))
}

func fixtureBytes(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(fixtures.Path(name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func readFromBytes(t *testing.T, name string, b []byte) Metadata {
	t.Helper()
	m, err := ReadFrom(bytes.NewReader(b))
	if err != nil {
		t.Fatalf("ReadFrom(%s): %v", name, err)
	}
	return m
}

// checkFixtureValues checks the values every tagged fixture carries.
func checkFixtureValues(t *testing.T, name string, m Metadata) {
	t.Helper()
	if m.Title() != fixtures.Title || m.Artist() != fixtures.Artist || m.AlbumArtist() != fixtures.AlbumArtist ||
		m.Album() != fixtures.Album || m.Year() != fixtures.Year {
		t.Errorf("%s: got %q / %q / %q / %q / %d", name, m.Title(), m.Artist(), m.AlbumArtist(), m.Album(), m.Year())
	}
	if n, total := m.Track(); n != fixtures.Track || total != fixtures.TrackTotal {
		t.Errorf("%s: track %d/%d, want %d/%d", name, n, total, fixtures.Track, fixtures.TrackTotal)
	}
	if n, total := m.Disc(); n != fixtures.Disc || total != fixtures.DiscTotal {
		t.Errorf("%s: disc %d/%d, want %d/%d", name, n, total, fixtures.Disc, fixtures.DiscTotal)
	}
}

func TestReadAPEv2Fixtures(t *testing.T) {
	for name, fileType := range map[string]FileType{"tagged.ape": APE, "tagged.mpc": MPC, "tagged.aac": AAC} {
		m := readFromBytes(t, name, fixtureBytes(t, name))
		checkFixtureValues(t, name, m)
		if m.Format() != APEv2 || m.FileType() != fileType {
			t.Errorf("%s: format %q, file type %q, want %q, %q", name, m.Format(), m.FileType(), APEv2, fileType)
		}
		// Raw keeps each key's own case, for mbz and for writers.
		if got := m.Raw()["musicbrainz_trackid"]; got != fixtures.RecordingID {
			t.Errorf("%s: Raw()[musicbrainz_trackid] = %v, want %s", name, got, fixtures.RecordingID)
		}
		if got := m.Raw()["Musicbrainz_Albumid"]; got != fixtures.ReleaseID {
			t.Errorf("%s: Raw()[Musicbrainz_Albumid] = %v, want %s", name, got, fixtures.ReleaseID)
		}
	}
}

func TestReadAPEv2UntaggedFixtures(t *testing.T) {
	for _, name := range []string{"untagged.ape", "untagged.mpc", "untagged.aac", fixtures.UntaggedSV8MPC} {
		_, err := ReadFrom(bytes.NewReader(fixtureBytes(t, name)))
		if err != ErrNoTagsFound {
			t.Errorf("ReadFrom(%s) error = %v, want ErrNoTagsFound", name, err)
		}
	}
}

// TestReadAPEStaleID3v2 is the regression test for APE files with a leading
// ID3v2 tag, which TagLib skips and never updates: the APEv2 tag is current.
func TestReadAPEStaleID3v2(t *testing.T) {
	frame := slices.Concat([]byte("TIT2"), []byte{0, 0, 0, 6, 0, 0, 3}, []byte("Stale"))
	stale := slices.Concat(id3v2TagHeader(4, 0, len(frame)), frame)
	for _, name := range []string{"tagged.ape", "tagged.mpc"} {
		b := slices.Concat(stale, fixtureBytes(t, name))
		m := readFromBytes(t, "ID3v2 + "+name, b)
		checkFixtureValues(t, "ID3v2 + "+name, m)
		if m.Format() != APEv2 {
			t.Errorf("ID3v2 + %s: format %q, want %q", name, m.Format(), APEv2)
		}
	}

	// Without an APEv2 tag the ID3v2 tag is all there is.
	b := slices.Concat(stale, fixtureBytes(t, "untagged.ape"))
	if m := readFromBytes(t, "ID3v2 + untagged.ape", b); m.Title() != "Stale" {
		t.Errorf("ID3v2 + untagged.ape: title %q, want Stale", m.Title())
	}
}

// TestReadAACID3v2 keeps reading the ID3v2 tag go-taglib writes to .aac.
func TestReadAACID3v2(t *testing.T) {
	m := readFromBytes(t, fixtures.TaggedID3v2AAC, fixtureBytes(t, fixtures.TaggedID3v2AAC))
	checkFixtureValues(t, fixtures.TaggedID3v2AAC, m)
	if m.Format() != ID3v2_4 || m.FileType() != AAC {
		t.Errorf("format %q, file type %q, want %q, %q", m.Format(), m.FileType(), ID3v2_4, AAC)
	}
}

// TestReadAPEv2WithID3v1 covers an ID3v1 tag after the APEv2 tag (TagLib)
// and before it (mutagen appends APEv2 after an existing ID3v1).
func TestReadAPEv2WithID3v1(t *testing.T) {
	audio := fixtureBytes(t, "untagged.ape")
	ape := apeTag(apeItem{key: "Title", value: "APE Title"})
	for name, b := range map[string][]byte{
		"APE TAG":     slices.Concat(audio, ape, id3v1("V1")),
		"TAG APE":     slices.Concat(audio, id3v1("V1"), ape),
		"TAG APE TAG": slices.Concat(audio, id3v1("V1"), ape, id3v1("V1 again")),
	} {
		if m := readFromBytes(t, name, b); m.Title() != "APE Title" {
			t.Errorf("%s: title %q, want APE Title", name, m.Title())
		}
	}

	// ID3v1 alone still reads.
	if m := readFromBytes(t, "ID3v1 only", slices.Concat(audio, id3v1("V1"))); m.Title() != "V1" {
		t.Errorf("ID3v1 only: title %q, want V1", m.Title())
	}
}

func TestReadAPEv2Items(t *testing.T) {
	cover := []byte{0xff, 0xd8, 0xff, 0xe0, 1, 2, 3}
	b := slices.Concat(fixtureBytes(t, "untagged.mpc"), apeTag(
		apeItem{key: "TITLE", value: "Upper"},
		apeItem{key: "artist", value: "First\x00Second"},
		apeItem{key: "ALBUMARTIST", value: "AA"},
		apeItem{key: "Year", value: "1997-05-12"},
		apeItem{key: "Track", value: "7"},
		apeItem{key: "Genre", value: "Jazz"},
		apeItem{key: "Comment", value: "c"},
		apeItem{key: "Cover Art (Front)", value: "cover.jpg\x00" + string(cover), binary: true},
	))
	m := readFromBytes(t, "items", b)
	if m.Title() != "Upper" || m.Artist() != "First" || m.AlbumArtist() != "AA" || m.Year() != 1997 ||
		m.Genre() != "Jazz" || m.Comment() != "c" {
		t.Errorf("got %q / %q / %q / %d / %q / %q", m.Title(), m.Artist(), m.AlbumArtist(), m.Year(), m.Genre(), m.Comment())
	}
	if n, total := m.Track(); n != 7 || total != 0 {
		t.Errorf("track %d/%d, want 7/0", n, total)
	}
	p := m.Picture()
	if p == nil || !bytes.Equal(p.Data, cover) || p.Ext != "jpg" || p.MIMEType != "image/jpeg" {
		t.Errorf("picture = %v, want the JPEG cover", p)
	}
	if got, ok := m.Raw()["Cover Art (Front)"].([]byte); !ok || !bytes.HasSuffix(got, cover) {
		t.Errorf("Raw cover = %v, want the binary value", m.Raw()["Cover Art (Front)"])
	}
	if got := m.Raw()["artist"]; got != "First\x00Second" {
		t.Errorf("Raw artist = %q, want both values", got)
	}
}

// TestReadAPEv2Malformed checks a damaged tag is an error, not a panic.
func TestReadAPEv2Malformed(t *testing.T) {
	audio := fixtureBytes(t, "untagged.ape")
	tag := apeTag(apeItem{key: "Title", value: "T"})

	truncated := slices.Clone(tag)
	binary.LittleEndian.PutUint32(truncated[32:], 1000) // the item's value size
	if _, err := ReadFrom(bytes.NewReader(slices.Concat(audio, truncated))); err == nil {
		t.Error("oversized item: no error")
	}

	// A tag size running past the start of the file is not a tag.
	huge := slices.Clone(tag)
	binary.LittleEndian.PutUint32(huge[len(huge)-32+12:], 1<<30)
	if _, err := ReadFrom(bytes.NewReader(slices.Concat(audio, huge))); err != ErrNoTagsFound {
		t.Errorf("tag larger than the file: error = %v, want ErrNoTagsFound", err)
	}
}
