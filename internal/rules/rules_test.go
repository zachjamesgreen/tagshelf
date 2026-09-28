package rules

import (
	"path/filepath"
	"testing"

	"tagshelf/internal/tag"
)

func TestSanitizeName(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"Normal Name", "Normal Name"},
		{"AC/DC", "AC_DC"},
		{"What?*<>:|", "What______"},
		{"Trailing Dot.", "Trailing Dot"},
		{"  Spaces  ", "Spaces"},
		{"", "Unknown"},
		{"...", "Unknown"},
		{"   ", "Unknown"},
	}

	for _, tt := range tests {
		got := SanitizeName(tt.input)
		if got != tt.expected {
			t.Errorf("SanitizeName(%q) = %q, expected %q", tt.input, got, tt.expected)
		}
	}
}

// Mock metadata for testing tag evaluation
type mockMetadata struct {
	title       string
	album       string
	artist      string
	albumArtist string
	year        int
	track       int
	disc, discs int
	raw         map[string]interface{}
}

func (m *mockMetadata) Format() tag.Format          { return tag.ID3v2_3 }
func (m *mockMetadata) FileType() tag.FileType      { return tag.MP3 }
func (m *mockMetadata) Title() string               { return m.title }
func (m *mockMetadata) Album() string               { return m.album }
func (m *mockMetadata) Artist() string              { return m.artist }
func (m *mockMetadata) AlbumArtist() string         { return m.albumArtist }
func (m *mockMetadata) Composer() string            { return "" }
func (m *mockMetadata) Year() int                   { return m.year }
func (m *mockMetadata) Genre() string               { return "Rock" }
func (m *mockMetadata) Track() (int, int)           { return m.track, 0 }
func (m *mockMetadata) Disc() (int, int)            { return m.disc, m.discs }
func (m *mockMetadata) Picture() *tag.Picture       { return nil }
func (m *mockMetadata) Lyrics() string              { return "" }
func (m *mockMetadata) Comment() string             { return "" }
func (m *mockMetadata) Raw() map[string]interface{} { return m.raw }

func TestHasMusicBrainzTags(t *testing.T) {
	// Case 1: No tags
	m1 := &mockMetadata{
		title:  "Song",
		album:  "Album",
		artist: "Artist",
		raw:    map[string]interface{}{},
	}
	if HasMusicBrainzTags(m1) {
		t.Errorf("Expected false for mock without MB tags")
	}

	// Case 2: Raw tag with MusicBrainz track ID
	m2 := &mockMetadata{
		title:  "Song",
		album:  "Album",
		artist: "Artist",
		raw: map[string]interface{}{
			"MusicBrainz Track Id": "c83984e7-47b2-4d76-8098-b80c10a48ad1",
		},
	}
	if !HasMusicBrainzTags(m2) {
		t.Errorf("Expected true for mock with MusicBrainz Track Id in raw tags")
	}

	// Case 3: UFID tag with musicbrainz provider
	m3 := &mockMetadata{
		title:  "Song",
		album:  "Album",
		artist: "Artist",
		raw: map[string]interface{}{
			"UFID": tag.UFID{
				Provider:   "http://musicbrainz.org",
				Identifier: []byte("c83984e7-47b2-4d76-8098-b80c10a48ad1"),
			},
		},
	}
	if !HasMusicBrainzTags(m3) {
		t.Errorf("Expected true for mock with UFID musicbrainz tag")
	}
}

func TestLibraryPath(t *testing.T) {
	dest := filepath.Join("C:", "Lib")
	tests := []struct {
		name string
		m    mockMetadata
		want string
		ok   bool
	}{
		{"album artist wins over featured artist",
			mockMetadata{artist: "Singer feat. Guest", albumArtist: "Singer", album: "Album", title: "Song", year: 2001, track: 3, disc: 1, discs: 1},
			filepath.Join(dest, "Singer", "Album (2001)", "03 - Song.mp3"), true},
		{"falls back to track artist",
			mockMetadata{artist: "Singer", album: "Album", title: "Song", year: 2001, track: 3},
			filepath.Join(dest, "Singer", "Album (2001)", "03 - Song.mp3"), true},
		{"no year",
			mockMetadata{albumArtist: "Singer", album: "Album", title: "Song", track: 12},
			filepath.Join(dest, "Singer", "Album", "12 - Song.mp3"), true},
		{"no track number",
			mockMetadata{albumArtist: "Singer", album: "Album", title: "Song"},
			filepath.Join(dest, "Singer", "Album", "Song.mp3"), true},
		{"multi-disc by total",
			mockMetadata{albumArtist: "Singer", album: "Album", title: "Song", track: 4, disc: 1, discs: 2},
			filepath.Join(dest, "Singer", "Album", "1-04 - Song.mp3"), true},
		{"multi-disc by disc number without total",
			mockMetadata{albumArtist: "Singer", album: "Album", title: "Song", track: 4, disc: 2},
			filepath.Join(dest, "Singer", "Album", "2-04 - Song.mp3"), true},
		{"sanitized album with year",
			mockMetadata{albumArtist: "AC/DC", album: "Who?", title: "T.N.T.", year: 1975, track: 1},
			filepath.Join(dest, "AC_DC", "Who_ (1975)", "01 - T.N.T.mp3"), true},
		{"missing title", mockMetadata{albumArtist: "Singer", album: "Album"}, "", false},
		{"missing album", mockMetadata{albumArtist: "Singer", title: "Song"}, "", false},
		{"missing both artists", mockMetadata{album: "Album", title: "Song"}, "", false},

		// Compilations
		{"compilation by MusicBrainz ID",
			mockMetadata{artist: "Singer feat. Guest", album: "Hits", title: "Song", year: 2012, track: 3, raw: vaIDRaw()},
			filepath.Join(dest, "Comps", "Hits (2012)", "03 - Singer feat. Guest - Song.mp3"), true},
		{"compilation by album artist name",
			mockMetadata{artist: "Singer", albumArtist: "V.A.", album: "Hits", title: "Song", track: 3},
			filepath.Join(dest, "Comps", "Hits", "03 - Singer - Song.mp3"), true},
		{"compilation multi-disc",
			mockMetadata{artist: "Singer", albumArtist: "various artists", album: "Hits", title: "Song", track: 3, disc: 2, discs: 2},
			filepath.Join(dest, "Comps", "Hits", "2-03 - Singer - Song.mp3"), true},
		{"compilation without track artist",
			mockMetadata{albumArtist: "VA", album: "Hits", title: "Song", track: 3},
			filepath.Join(dest, "Comps", "Hits", "03 - Song.mp3"), true},
		{"real album artist wins over MusicBrainz ID",
			mockMetadata{artist: "Singer", albumArtist: "Singer", album: "Hits", title: "Song", track: 3, raw: vaIDRaw()},
			filepath.Join(dest, "Singer", "Hits", "03 - Song.mp3"), true},
		{"a different MusicBrainz ID is not a compilation",
			mockMetadata{artist: "Singer", album: "Hits", title: "Song", track: 3, raw: map[string]interface{}{
				"TXXX": &tag.Comm{Description: "MusicBrainz Album Artist Id", Text: "c83984e7-47b2-4d76-8098-b80c10a48ad1"},
			}},
			filepath.Join(dest, "Singer", "Hits", "03 - Song.mp3"), true},
	}
	for _, tt := range tests {
		m := tt.m
		got, ok := LibraryPath(dest, "Comps", &m, ".mp3")
		if got != tt.want || ok != tt.ok {
			t.Errorf("%s: LibraryPath = %q, %t; want %q, %t", tt.name, got, ok, tt.want, tt.ok)
		}
	}
}

// vaIDRaw is the raw ID3 tag Picard writes for a Various Artists album artist.
func vaIDRaw() map[string]interface{} {
	return map[string]interface{}{
		"TXXX": &tag.Comm{Description: "MusicBrainz Album Artist Id", Text: VariousArtistsMBID},
	}
}
