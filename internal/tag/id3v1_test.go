// This file is subject to the CC0 1.0 Universal (CC0 1.0) Public Domain Dedication
// license.  Its contents can be found at:
// http://creativecommons.org/publicdomain/zero/1.0

package tag

import (
	"bytes"
	"slices"
	"testing"
	"unicode/utf8"

	"tagshelf/internal/tag/internal/id3v1_test"
)

//go:generate go-bindata -o internal/id3v1_test/id3v1_testdata.go -pkg id3v1_test -ignore .txt -ignore .go internal/id3v1_test

// TestReadID3v1Tags compares every field of the six samples. An ID3v1 tag
// does not say how its text is encoded: the reader takes it as UTF-8 when it
// is valid UTF-8, and as Windows-1252 otherwise. So the UTF-8 samples read as
// written (the artist is cut at the field's 30 bytes), and the Shift-JIS
// (MS932) ones read as the Windows-1252 text of the same bytes, which is
// wrong but valid; U+FFFD stands for the byte 0x81, which Windows-1252 does
// not define.
func TestReadID3v1Tags(t *testing.T) {
	ms932 := testMetadata{
		Title:  "ƒTƒ“ƒvƒ‹‚Ìƒ^ƒCƒgƒ‹",
		Artist: "ƒTƒ“ƒvƒ‹‚ÌƒA�[ƒeƒBƒXƒg",
		Album:  "ƒTƒ“ƒvƒ‹‚ÌƒAƒ‹ƒoƒ€",
	}
	utf8Sample := testMetadata{Title: "サンプルのタイトル", Artist: "サンプルのアーティス", Album: "サンプルのアルバム"}
	ascii := testMetadata{Title: "Sample Title", Artist: "Sample Artist", Album: "Sample Album"}

	for _, tt := range []struct {
		name    string
		want    testMetadata
		comment string
		track   int
	}{
		{"sample_usascii_v1.mp3", ascii, "Sample Data with US-ASCII 6789", 0},
		{"sample_usascii_v1.1.mp3", ascii, "Sample Data with US-ASCII 67", 1},
		{"sample_ms932_v1.mp3", ms932, "Sample Data with MS932 3456789", 0},
		{"sample_ms932_v1.1.mp3", ms932, "Sample Data with MS932 34567", 1},
		{"sample_utf8_v1.mp3", utf8Sample, "Sample Data with UTF8 23456789", 0},
		{"sample_utf8_v1.1.mp3", utf8Sample, "Sample Data with UTF8 234567", 1},
	} {
		mp3 := id3v1_test.MustAsset("internal/id3v1_test/" + tt.name)
		m, err := ReadID3v1Tags(bytes.NewReader(mp3))
		if err != nil {
			t.Errorf("%s: %v", tt.name, err)
			continue
		}
		if m.Format() != ID3v1 || m.FileType() != MP3 {
			t.Errorf("%s: format %q, file type %q, want %q, %q", tt.name, m.Format(), m.FileType(), ID3v1, MP3)
		}
		want := tt.want
		want.Year, want.Genre, want.Comment, want.Track = 1990, "Vocal", tt.comment, tt.track
		compareMetadata(t, tt.name, m, want)
		if got := m.Raw()["comment"]; got != tt.comment {
			t.Errorf("%s: Raw comment = %q, want %q", tt.name, got, tt.comment)
		}
	}
}

// id3v1Tag renders an ID3v1.1 tag. Each field is cut or padded with NULs to
// its size.
func id3v1Tag(title, artist, album, year, comment string, track, genre byte) []byte {
	b := []byte("TAG")
	for _, f := range []struct {
		s string
		n int
	}{{title, 30}, {artist, 30}, {album, 30}, {year, 4}, {comment, 28}} {
		field := make([]byte, f.n)
		copy(field, f.s)
		b = append(b, field...)
	}
	return append(b, 0, track, genre)
}

// TestID3v1Text is the regression test for the text of an ID3v1 tag, which
// was returned as its raw bytes: not valid UTF-8 for any byte from 0x80 up,
// and with whatever followed the first NUL of a field still in it.
func TestID3v1Text(t *testing.T) {
	tag := id3v1Tag("J\xf3ga", "Bj\xf6rk", "Homogenic\x00junk!", "1997", "Don\x92t \x96 stop\x00\x00more", 2, 8)
	m := readFromBytes(t, "ID3v1", slices.Concat(mp3Audio, tag))
	want := testMetadata{Title: "Jóga", Artist: "Björk", Album: "Homogenic", Year: 1997, Comment: "Don’t – stop", Track: 2, Genre: "Jazz"}
	compareMetadata(t, "ISO-8859-1 tag", m, want)
	for _, s := range []string{m.Title(), m.Artist(), m.Album(), m.Comment()} {
		if !utf8.ValidString(s) {
			t.Errorf("%q is not valid UTF-8", s)
		}
	}

	// UTF-8 text stays as it is.
	tag = id3v1Tag("Jóga", "Björk", "Homogenic", "1997", "", 0, 255)
	m = readFromBytes(t, "ID3v1", slices.Concat(mp3Audio, tag))
	compareMetadata(t, "UTF-8 tag", m, testMetadata{Title: "Jóga", Artist: "Björk", Album: "Homogenic", Year: 1997})
}
