// Copyright 2015, David Howden
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package tag

import (
	"bytes"
	"image/png"
	"os"
	"slices"
	"testing"
)

type testMetadata struct {
	Album       string
	AlbumArtist string
	Artist      string
	Comment     string
	Composer    string
	Disc        int
	DiscTotal   int
	Genre       string
	Lyrics      string
	Title       string
	Track       int
	TrackTotal  int
	Year        int
}

var emptyMetadata = testMetadata{}
var fullMetadata = testMetadata{
	Album:       "Test Album",
	AlbumArtist: "Test AlbumArtist",
	Artist:      "Test Artist",
	Composer:    "Test Composer",
	Disc:        2,
	DiscTotal:   0,
	Genre:       "Jazz",
	Lyrics:      "",
	Title:       "Test Title",
	Track:       3,
	TrackTotal:  6,
	Year:        2000,
	Comment:     "Test Comment",
}
var mp3id3v11Metadata = testMetadata{
	Album:   "Test Album",
	Artist:  "Test Artist",
	Genre:   "Jazz",
	Lyrics:  "",
	Title:   "Test Title",
	Track:   3,
	Year:    2000,
	Comment: "Test Comment",
}

func TestReadFrom(t *testing.T) {
	testdata := map[string]struct {
		metadata testMetadata
		format   Format
		fileType FileType
	}{
		"with_tags/sample.flac":          {fullMetadata, VORBIS, FLAC},
		"with_tags/sample.id3v11.mp3":    {mp3id3v11Metadata, ID3v1, MP3},
		"with_tags/sample.id3v22.mp3":    {fullMetadata, ID3v2_2, MP3},
		"with_tags/sample.id3v23.mp3":    {fullMetadata, ID3v2_3, MP3},
		"with_tags/sample.id3v24.mp3":    {fullMetadata, ID3v2_4, MP3},
		"with_tags/sample.m4a":           {fullMetadata, MP4, M4A},
		"with_tags/sample.mp4":           {fullMetadata, MP4, M4A},
		"with_tags/sample.ogg":           {fullMetadata, VORBIS, OGG},
		"with_tags/sample.multipage.ogg": {fullMetadata, VORBIS, OGG},
		"with_tags/sample.dsf":           {fullMetadata, ID3v2_4, DSF},
		"without_tags/sample.flac":       {emptyMetadata, VORBIS, FLAC},
		"without_tags/sample.m4a":        {emptyMetadata, MP4, M4A},
		"without_tags/sample.mp4":        {emptyMetadata, MP4, UnknownFileType}, // its brand is mp42
		"without_tags/sample.ogg":        {emptyMetadata, VORBIS, OGG},
	}

	for path, tt := range testdata {
		m, err := readTestdata(path)
		if err != nil {
			t.Errorf("%s: %v", path, err)
			continue
		}
		if m.Format() != tt.format || m.FileType() != tt.fileType {
			t.Errorf("%s: format %q, file type %q, want %q, %q", path, m.Format(), m.FileType(), tt.format, tt.fileType)
		}
		compareMetadata(t, path, m, tt.metadata)
	}

	// An MP3 with no tag of any kind is the one sample with nothing to
	// read: the other formats carry an empty tag.
	if _, err := readTestdata("without_tags/sample.mp3"); err != ErrNoTagsFound {
		t.Errorf("without_tags/sample.mp3: ReadFrom = %v, want ErrNoTagsFound", err)
	}
}

func readTestdata(path string) (Metadata, error) {
	f, err := os.Open("testdata/" + path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return ReadFrom(f)
}

func compareMetadata(t *testing.T, name string, m Metadata, tt testMetadata) {
	t.Helper()
	testValue(t, name, "Album", tt.Album, m.Album())
	testValue(t, name, "AlbumArtist", tt.AlbumArtist, m.AlbumArtist())
	testValue(t, name, "Artist", tt.Artist, m.Artist())
	testValue(t, name, "Composer", tt.Composer, m.Composer())
	testValue(t, name, "Genre", tt.Genre, m.Genre())
	testValue(t, name, "Lyrics", tt.Lyrics, m.Lyrics())
	testValue(t, name, "Title", tt.Title, m.Title())
	testValue(t, name, "Year", tt.Year, m.Year())
	testValue(t, name, "Comment", tt.Comment, m.Comment())

	disc, discTotal := m.Disc()
	testValue(t, name, "Disc", tt.Disc, disc)
	testValue(t, name, "DiscTotal", tt.DiscTotal, discTotal)

	track, trackTotal := m.Track()
	testValue(t, name, "Track", tt.Track, track)
	testValue(t, name, "TrackTotal", tt.TrackTotal, trackTotal)
}

func testValue(t *testing.T, name, field string, expected interface{}, found interface{}) {
	t.Helper()
	if expected != found {
		t.Errorf("%s: %s: expected '%v', found '%v'", name, field, expected, found)
	}
}

// onePixelPNG is a whole PNG file: one transparent pixel.
var onePixelPNG = []byte{
	0x89, 'P', 'N', 'G', '\r', '\n', 0x1A, '\n',
	0, 0, 0, 13, 'I', 'H', 'D', 'R', 0, 0, 0, 1, 0, 0, 0, 1, 8, 6, 0, 0, 0, 0x1F, 0x15, 0xC4, 0x89,
	0, 0, 0, 10, 'I', 'D', 'A', 'T', 0x78, 0x9C, 0x63, 0x00, 0x01, 0x00, 0x00, 0x05, 0x00, 0x01, 0x0D, 0x0A, 0x2D, 0xB4,
	0, 0, 0, 0, 'I', 'E', 'N', 'D', 0xAE, 0x42, 0x60, 0x82,
}

// id3v22SampleTag is the tag of testdata/with_tags/sample.id3v22.mp3, built
// by hand to the ID3v2.2 specification in the form old iTunes versions
// wrote: three-letter frames, text in ISO-8859-1, a PIC frame, and the
// MusicBrainz IDs under the names Picard gives them in 2.2. Upstream's file
// of that name was a copy of the ID3v2.3 sample.
func id3v22SampleTag() []byte {
	return id3Tag(2, 0,
		id3Frame(2, "TT2", latin1("Test Title")),
		id3Frame(2, "TP1", latin1("Test Artist")),
		id3Frame(2, "TP2", latin1("Test AlbumArtist")),
		id3Frame(2, "TAL", latin1("Test Album")),
		id3Frame(2, "TCM", latin1("Test Composer")),
		id3Frame(2, "TCO", latin1("(8)")),
		id3Frame(2, "TYE", latin1("2000")),
		id3Frame(2, "TRK", latin1("03/06")),
		id3Frame(2, "TPA", latin1("02")),
		id3Frame(2, "COM", slices.Concat([]byte{0}, []byte("eng"), []byte{0}, []byte("Test Comment"))),
		id3Frame(2, "UFI", []byte("http://musicbrainz.org\x0055555555-5555-4555-8555-555555555555")),
		id3Frame(2, "TXX", slices.Concat([]byte{0}, []byte("MusicBrainz Album Id"), []byte{0}, []byte("33333333-3333-4333-8333-333333333333"))),
		id3Frame(2, "TXX", slices.Concat([]byte{0}, []byte("MusicBrainz Album Artist Id"), []byte{0}, []byte("22222222-2222-4222-8222-222222222222"))),
		id3Frame(2, "PIC", slices.Concat([]byte{0}, []byte("PNG"), []byte{3}, []byte("Cover"), []byte{0}, onePixelPNG)),
		id3Frame(2, "TEN", latin1("iTunes v4.9")),
		make([]byte, 512),
	)
}

// TestID3v22Sample reads everything in the ID3v2.2 sample, the only test
// input with three-letter frames, and checks that the file is the tag built
// above in front of the audio of the ID3v2.3 sample.
func TestID3v22Sample(t *testing.T) {
	file, err := os.ReadFile("testdata/with_tags/sample.id3v22.mp3")
	if err != nil {
		t.Fatal(err)
	}
	v23, err := os.ReadFile("testdata/with_tags/sample.id3v23.mp3")
	if err != nil {
		t.Fatal(err)
	}
	audio := v23[id3v2TagSize(v23[:10]):]
	if !bytes.Equal(file, slices.Concat(id3v22SampleTag(), audio)) {
		t.Error("sample.id3v22.mp3 is not id3v22SampleTag followed by the audio of sample.id3v23.mp3")
	}
	if got, want := sumBytes(t, "sample.id3v22.mp3", file), sumBytes(t, "sample.id3v23.mp3", v23); got != want {
		t.Errorf("Sum = %s, want %s, that of the same audio behind an ID3v2.3 tag", got, want)
	}

	m := readFromBytes(t, "sample.id3v22.mp3", file)
	if m.Format() != ID3v2_2 || m.FileType() != MP3 {
		t.Errorf("format %q, file type %q, want %q, %q", m.Format(), m.FileType(), ID3v2_2, MP3)
	}
	compareMetadata(t, "sample.id3v22.mp3", m, fullMetadata)

	p := m.Picture()
	if p == nil || p.Ext != "PNG" || p.MIMEType != "image/png" || p.Type != "Cover (front)" || p.Description != "Cover" || !bytes.Equal(p.Data, onePixelPNG) {
		t.Errorf("Picture() = %v, want the PNG of the PIC frame", p)
	} else if _, err := png.Decode(bytes.NewReader(p.Data)); err != nil {
		t.Errorf("the picture does not decode: %v", err)
	}

	raw := m.Raw()
	if u, _ := raw["UFI"].(*UFID); u == nil || u.Provider != "http://musicbrainz.org" || string(u.Identifier) != "55555555-5555-4555-8555-555555555555" {
		t.Errorf("UFI = %v", raw["UFI"])
	}
	if c, _ := raw["TXX"].(*Comm); c == nil || c.Description != "MusicBrainz Album Id" || c.Text != "33333333-3333-4333-8333-333333333333" {
		t.Errorf("TXX = %v", raw["TXX"])
	}
	if c, _ := raw["TXX_0"].(*Comm); c == nil || c.Description != "MusicBrainz Album Artist Id" || c.Text != "22222222-2222-4222-8222-222222222222" {
		t.Errorf("TXX_0 = %v", raw["TXX_0"])
	}
	if got := raw["TEN"]; got != "iTunes v4.9" {
		t.Errorf("TEN = %q", got)
	}
}
