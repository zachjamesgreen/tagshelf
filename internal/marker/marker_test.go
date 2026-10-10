package marker

import (
	"os"
	"testing"
	"time"

	"tagshelf/internal/fixtures"
	"tagshelf/internal/tag"
)

func TestParse(t *testing.T) {
	day := time.Date(2026, 9, 27, 0, 0, 0, 0, time.UTC)
	tests := []struct {
		value      string
		ok         bool
		method     Method
		date       time.Time
		wantString string
	}{
		{"hand 2026-09-27", true, Hand, day, "hand 2026-09-27"},
		{"musicbrainz 2026-09-27", true, MusicBrainz, day, "musicbrainz 2026-09-27"},
		{"  MusicBrainz   2026-09-27 ", true, MusicBrainz, day, "musicbrainz 2026-09-27"},
		{"hand", true, Hand, time.Time{}, "hand"},
		{"hand yesterday", true, Hand, time.Time{}, "hand"},
		{"picard 2026-09-27", true, Unknown, day, "unknown 2026-09-27"},
		{"1", true, Unknown, time.Time{}, "unknown"},
		{"2026-09-27", true, Unknown, time.Time{}, "unknown"},
		{"", false, "", time.Time{}, ""},
		{"  \x00 ", false, "", time.Time{}, ""},
	}
	for _, tt := range tests {
		m, ok := Parse(tt.value)
		if ok != tt.ok || m.Method != tt.method || !m.Date.Equal(tt.date) {
			t.Errorf("Parse(%q) = %+v, %t; want method %q, date %v, %t", tt.value, m, ok, tt.method, tt.date, tt.ok)
		}
		if ok && m.String() != tt.wantString {
			t.Errorf("Parse(%q).String() = %q, want %q", tt.value, m.String(), tt.wantString)
		}
	}
}

func TestNew(t *testing.T) {
	// The day is the one in when's own time zone. Both times are on another
	// day in UTC: the 28th west of it, the 26th east of it.
	for _, zone := range []*time.Location{time.FixedZone("west", -5*3600), time.FixedZone("east", 5*3600)} {
		hour := 23
		if zone.String() == "east" {
			hour = 0
		}
		when := time.Date(2026, 9, 27, hour, 30, 0, 0, zone)
		if when.UTC().Day() == 27 {
			t.Fatalf("%s is the 27th in UTC too, so it tells nothing", when)
		}
		if got := New(MusicBrainz, when).String(); got != "musicbrainz 2026-09-27" {
			t.Errorf("New(MusicBrainz, %s).String() = %q, want musicbrainz 2026-09-27", when, got)
		}
	}
}

// fakeMetadata is a tag.Metadata with only a format and raw tags.
type fakeMetadata struct {
	format tag.Format
	raw    map[string]any
}

func (m fakeMetadata) Format() tag.Format     { return m.format }
func (m fakeMetadata) FileType() tag.FileType { return tag.UnknownFileType }
func (m fakeMetadata) Title() string          { return "" }
func (m fakeMetadata) Album() string          { return "" }
func (m fakeMetadata) Artist() string         { return "" }
func (m fakeMetadata) AlbumArtist() string    { return "" }
func (m fakeMetadata) Composer() string       { return "" }
func (m fakeMetadata) Year() int              { return 0 }
func (m fakeMetadata) Genre() string          { return "" }
func (m fakeMetadata) Track() (int, int)      { return 0, 0 }
func (m fakeMetadata) Disc() (int, int)       { return 0, 0 }
func (m fakeMetadata) Picture() *tag.Picture  { return nil }
func (m fakeMetadata) Lyrics() string         { return "" }
func (m fakeMetadata) Comment() string        { return "" }
func (m fakeMetadata) Raw() map[string]any    { return m.raw }

func TestRead(t *testing.T) {
	tests := []struct {
		name   string
		m      tag.Metadata
		ok     bool
		method Method
	}{
		{"nil", nil, false, ""},
		{"ID3 TXXX", fakeMetadata{tag.ID3v2_4, map[string]any{
			"TXXX":   &tag.Comm{Description: "MusicBrainz Album Id", Text: "x"},
			"TXXX_0": &tag.Comm{Description: "TAGSHELF_MANUAL", Text: "hand 2026-09-27"},
		}}, true, Hand},
		{"ID3 TXXX lower case", fakeMetadata{tag.ID3v2_3, map[string]any{
			"TXXX": &tag.Comm{Description: "tagshelf_manual", Text: "musicbrainz 2026-09-27"},
		}}, true, MusicBrainz},
		{"ID3v2.2 TXX", fakeMetadata{tag.ID3v2_2, map[string]any{
			"TXX": &tag.Comm{Description: "TAGSHELF_MANUAL", Text: "hand"},
		}}, true, Hand},
		{"ID3 empty TXXX", fakeMetadata{tag.ID3v2_4, map[string]any{
			"TXXX": &tag.Comm{Description: "TAGSHELF_MANUAL", Text: " "},
		}}, false, ""},
		{"ID3 plain text frame is not the marker", fakeMetadata{tag.ID3v2_4, map[string]any{
			"TAGSHELF_MANUAL": "hand",
		}}, false, ""},
		{"Vorbis", fakeMetadata{tag.VORBIS, map[string]any{"tagshelf_manual": "hand 2026-09-27"}}, true, Hand},
		{"MP4 freeform", fakeMetadata{tag.MP4, map[string]any{"TAGSHELF_MANUAL": "odd"}}, true, Unknown},
		{"APEv2-style mixed case", fakeMetadata{"APEv2", map[string]any{"Tagshelf_Manual": "hand 2026-09-27"}}, true, Hand},
		{"key only as a prefix", fakeMetadata{tag.VORBIS, map[string]any{"tagshelf_manual_old": "hand"}}, false, ""},
		{"no marker", fakeMetadata{tag.VORBIS, map[string]any{"title": "x"}}, false, ""},
	}
	for _, tt := range tests {
		m, ok := Read(tt.m)
		if ok != tt.ok || m.Method != tt.method {
			t.Errorf("%s: Read = %+v, %t; want method %q, %t", tt.name, m, ok, tt.method, tt.ok)
		}
	}
}

// TestReadFixtures reads the marker the way each format stores it:
// TXXX:TAGSHELF_MANUAL in MP3, a Vorbis comment in FLAC and OGG, and a
// ----:com.apple.iTunes:TAGSHELF_MANUAL atom in M4A.
func TestReadFixtures(t *testing.T) {
	day := time.Date(2026, 9, 27, 0, 0, 0, 0, time.UTC)
	for _, ext := range fixtures.ManualExtensions() {
		m := readFixture(t, fixtures.Manual(ext))
		got, ok := Read(m)
		if !ok || got.Method != Hand || !got.Date.Equal(day) {
			t.Errorf("%s: Read = %+v, %t; want hand 2026-09-27", fixtures.Manual(ext), got, ok)
		}
		// Picard's tags carry no marker.
		if got, ok := Read(readFixture(t, fixtures.Tagged(ext))); ok {
			t.Errorf("%s: Read = %+v, want no marker", fixtures.Tagged(ext), got)
		}
	}
}

func readFixture(t *testing.T, path string) tag.Metadata {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	m, err := tag.ReadFrom(f)
	if err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	return m
}
