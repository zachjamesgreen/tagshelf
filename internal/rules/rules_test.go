package rules

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/zachjamesgreen/tagshelf/internal/tag"
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
		// The backslash is what keeps a tag from naming folders on Windows.
		{`AC\DC`, "AC_DC"},
		{`..\..\up`, ".._.._up"},
		{`Say "Hi"`, "Say _Hi_"},
		{"Tab\there", "Tab_here"},
		{"Bell\x07 and\x1f unit", "Bell_ and_ unit"},
		// Nothing left: a stand-in, never a word a real tag could hold.
		{"", "_"},
		{"...", "_"},
		{"   ", "_"},
		{"..", "_"},
		{"Unknown", "Unknown"},
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

// TestHasMusicBrainzTagsInTXXX covers where Picard puts the album and release
// track IDs of an MP3: user-defined text frames named by their description.
func TestHasMusicBrainzTagsInTXXX(t *testing.T) {
	const id = "c83984e7-47b2-4d76-8098-b80c10a48ad1"
	tests := []struct {
		name string
		raw  map[string]interface{}
		want bool
	}{
		{"album ID", map[string]interface{}{"TXXX": &tag.Comm{Description: "MusicBrainz Album Id", Text: id}}, true},
		{"release track ID, by value", map[string]interface{}{"TXXX_1": tag.Comm{Description: "MusicBrainz Release Track Id", Text: id}}, true},
		{"ID3v2.2 frame", map[string]interface{}{"TXX": &tag.Comm{Description: "MusicBrainz Album Id", Text: id}}, true},
		// The looser rule: any description naming MusicBrainz and a release.
		{"release group ID", map[string]interface{}{"TXXX": &tag.Comm{Description: "MusicBrainz Release Group Id", Text: id}}, true},
		{"empty ID", map[string]interface{}{"TXXX": &tag.Comm{Description: "MusicBrainz Album Id", Text: "  "}}, false},
		{"nil frame", map[string]interface{}{"TXXX": (*tag.Comm)(nil)}, false},
		{"another description", map[string]interface{}{"TXXX": &tag.Comm{Description: "replaygain_track_gain", Text: "-6 dB"}}, false},
		{"MusicBrainz, but no release or track", map[string]interface{}{"TXXX": &tag.Comm{Description: "MusicBrainz Artist Id", Text: id}}, false},
		// The looser rule on a plain key, as in a Vorbis comment.
		{"plain release group key", map[string]interface{}{"MUSICBRAINZ_RELEASEGROUPID": id}, true},
		{"plain artist key", map[string]interface{}{"MUSICBRAINZ_ARTISTID": id}, false},
		{"plain key without a value", map[string]interface{}{"MUSICBRAINZ_ALBUMID": ""}, false},
	}
	for _, tt := range tests {
		if got := HasMusicBrainzTags(&mockMetadata{raw: tt.raw}); got != tt.want {
			t.Errorf("%s: HasMusicBrainzTags = %t, want %t", tt.name, got, tt.want)
		}
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
		{"blank title", mockMetadata{albumArtist: "Singer", album: "Album", title: "  "}, "", false},

		// "Unknown" is a value like any other, and so is one with nothing left
		// once it is made safe: neither is a missing tag.
		{"everything called Unknown",
			mockMetadata{albumArtist: "Unknown", album: "Unknown", title: "Unknown", track: 1},
			filepath.Join(dest, "Unknown", "Unknown", "01 - Unknown.mp3"), true},
		{"track artist called Unknown",
			mockMetadata{artist: "Unknown", album: "Album", title: "Song"},
			filepath.Join(dest, "Unknown", "Album", "Song.mp3"), true},
		{"only dots",
			mockMetadata{albumArtist: "...", album: "...", title: "...", track: 2},
			filepath.Join(dest, "_", "_", "02 - _.mp3"), true},
		{"only dots, with a year",
			mockMetadata{albumArtist: "Singer", album: "...", title: "?", year: 1999},
			filepath.Join(dest, "Singer", "_.. (1999)", "_.mp3"), true},

		// A name that would start with a full stop, and so be hidden off
		// Windows, starts with an underscore: the first one only.
		{"an artist and an album that start with a full stop",
			mockMetadata{albumArtist: ".38 Special", album: "...And Justice for All", title: "One", year: 1988, track: 4},
			filepath.Join(dest, "_38 Special", "_..And Justice for All (1988)", "04 - One.mp3"), true},
		{"a title that starts with one, with a track number before it",
			mockMetadata{albumArtist: "Singer", album: "Album", title: ".hidden", track: 7},
			filepath.Join(dest, "Singer", "Album", "07 - .hidden.mp3"), true},
		{"a title that starts with one, with nothing before it",
			mockMetadata{albumArtist: "Singer", album: "Album", title: ".hidden"},
			filepath.Join(dest, "Singer", "Album", "_hidden.mp3"), true},
		{"a compilation whose track artist starts with one",
			mockMetadata{artist: ".38 Special", albumArtist: "Various Artists", album: "Hits", title: "Song"},
			filepath.Join(dest, "Comps", "Hits", "_38 Special - Song.mp3"), true},
		{"a full stop that is not first",
			mockMetadata{albumArtist: "R.E.M.", album: "Up", title: "Daysleeper", track: 1},
			filepath.Join(dest, "R.E.M", "Up", "01 - Daysleeper.mp3"), true},
		{"compilation whose track artist is only dots",
			mockMetadata{artist: "...", albumArtist: "VA", album: "Hits", title: "Song"},
			filepath.Join(dest, "Comps", "Hits", "_ - Song.mp3"), true},

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

// TestLibraryPathStaysInTheLibrary checks that no tag can name a folder: the
// path is always the destination, an artist folder, an album folder and a
// file, whatever separators and dots the tags hold.
func TestLibraryPathStaysInTheLibrary(t *testing.T) {
	dest := filepath.Join(t.TempDir(), "lib")
	hostile := []string{
		`..`, `.`, `...`, `..\`, `../`, `..\..\up`, `../../up`, `..\..\..\..\escaped`, `\\server\share`,
		`C:\Windows`, `/etc/passwd`, `a\b`, `a/b`, `..\ `, ` ..`, "..\x00", `.. (x)\..`,
	}
	for _, value := range hostile {
		for _, tags := range []Tags{
			{AlbumArtist: value, Album: "Album", Title: "Song"},
			{Artist: value, Album: "Album", Title: "Song"},
			{AlbumArtist: "Singer", Album: value, Title: "Song"},
			{AlbumArtist: "Singer", Album: value, Title: "Song", Year: 2001},
			{AlbumArtist: "Singer", Album: "Album", Title: value},
			{AlbumArtist: "Singer", Album: "Album", Title: value, Track: 3, Disc: 2},
			{AlbumArtist: "VA", Artist: value, Album: "Album", Title: "Song"},
			{AlbumArtist: value, Album: value, Title: value},
		} {
			for _, compilations := range []string{"Comps", value} {
				got, ok := LibraryPathFor(dest, compilations, tags, ".mp3")
				if !ok {
					t.Errorf("LibraryPathFor(%+v) does not qualify", tags)
					continue
				}
				rel, err := filepath.Rel(dest, got)
				if err != nil {
					t.Errorf("LibraryPathFor(%+v) = %q: %v", tags, got, err)
					continue
				}
				parts := strings.Split(rel, string(filepath.Separator))
				if len(parts) != 3 {
					t.Errorf("LibraryPathFor(%+v) = %q: %q is not artist, album and file", tags, got, rel)
				}
				for _, part := range parts {
					if part == "" || part == "." || part == ".." {
						t.Errorf("LibraryPathFor(%+v) = %q leaves the library", tags, got)
					}
				}
			}
		}
	}
}

func TestQualifies(t *testing.T) {
	dest := filepath.Join("C:", "Lib")
	mbid := map[string]interface{}{
		"UFID": &tag.UFID{Provider: "http://musicbrainz.org", Identifier: []byte("c83984e7-47b2-4d76-8098-b80c10a48ad1")},
	}
	marked := map[string]interface{}{
		"TXXX": &tag.Comm{Description: "TAGSHELF_MANUAL", Text: "hand 2026-09-27"},
	}
	oddMarker := map[string]interface{}{
		"TXXX": &tag.Comm{Description: "TAGSHELF_MANUAL", Text: "whatever"},
	}
	emptyMarker := map[string]interface{}{
		"TXXX": &tag.Comm{Description: "TAGSHELF_MANUAL", Text: ""},
	}
	song := filepath.Join(dest, "Singer", "Album", "01 - Song.mp3")
	tests := []struct {
		name string
		m    *mockMetadata
		want string
		ok   bool
	}{
		{"MusicBrainz IDs", &mockMetadata{albumArtist: "Singer", album: "Album", title: "Song", track: 1, raw: mbid}, song, true},
		{"manual marker", &mockMetadata{albumArtist: "Singer", album: "Album", title: "Song", track: 1, raw: marked}, song, true},
		{"marker with an unknown method", &mockMetadata{albumArtist: "Singer", album: "Album", title: "Song", track: 1, raw: oddMarker}, song, true},
		{"marker falls back to the track artist", &mockMetadata{artist: "Singer", album: "Album", title: "Song", track: 1, raw: marked}, song, true},
		{"empty marker", &mockMetadata{albumArtist: "Singer", album: "Album", title: "Song", raw: emptyMarker}, "", false},
		{"ripper tags", &mockMetadata{albumArtist: "Singer", album: "Album", title: "Song", raw: map[string]interface{}{}}, "", false},
		{"marker without album", &mockMetadata{albumArtist: "Singer", title: "Song", raw: marked}, "", false},
		{"marker without title", &mockMetadata{albumArtist: "Singer", album: "Album", raw: marked}, "", false},
		{"marker without either artist", &mockMetadata{album: "Album", title: "Song", raw: marked}, "", false},
		{"MusicBrainz IDs without album", &mockMetadata{albumArtist: "Singer", title: "Song", raw: mbid}, "", false},
	}
	for _, tt := range tests {
		got, ok := Qualifies(dest, "Comps", tt.m, ".mp3")
		if got != tt.want || ok != tt.ok {
			t.Errorf("%s: Qualifies = %q, %t; want %q, %t", tt.name, got, ok, tt.want, tt.ok)
		}
	}
	if _, ok := Qualifies(dest, "Comps", nil, ".mp3"); ok {
		t.Error("nil metadata must not qualify")
	}
}

// vaIDRaw is the raw ID3 tag Picard writes for a Various Artists album artist.
func vaIDRaw() map[string]interface{} {
	return map[string]interface{}{
		"TXXX": &tag.Comm{Description: "MusicBrainz Album Artist Id", Text: VariousArtistsMBID},
	}
}
