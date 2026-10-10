package tag

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"os"
	"slices"
	"strings"
	"testing"
	"time"
)

// vorbisBasics are the comments of a file that qualifies for the library.
var vorbisBasics = []string{"TITLE=Title", "ARTIST=Artist", "ALBUM=Album", "MUSICBRAINZ_ALBUMID=album-id"}

func checkVorbisBasics(t *testing.T, name string, m Metadata) {
	t.Helper()
	if m.Title() != "Title" || m.Artist() != "Artist" || m.Album() != "Album" || m.Raw()["musicbrainz_albumid"] != "album-id" {
		t.Errorf("%s: read %q / %q / %q / %v, want Title / Artist / Album / album-id",
			name, m.Title(), m.Artist(), m.Album(), m.Raw()["musicbrainz_albumid"])
	}
}

// TestVorbisYear is the regression test for a FLAC or OGG date in a form
// other than YYYY, YYYY-MM or YYYY-MM-DD, which read as the year 1 of an
// unparsed time and so filed the album under "Album (1)".
func TestVorbisYear(t *testing.T) {
	for _, tt := range []struct {
		date, year string
		want       int
	}{
		{"2001", "", 2001},
		{"2001-05", "", 2001},
		{"2001-05-12", "", 2001},
		{"2001-05-12T10:30", "", 2001},
		{"2001-05-12T10:30:00Z", "", 2001},
		{"2001 ", "", 2001},
		{"20010512", "", 2001},
		{"2001/05/12", "", 2001},
		{"12/05/2001", "", 0},
		{"May 2001", "", 0},
		{"abcd", "", 0},
		{"", "", 0},
		{"", "1999", 1999},
		{"", "abcd", 0},
		{"12/05/2001", "1999", 1999},
		{"2001", "1999", 2001},
	} {
		m := &metadataVorbis{c: map[string]string{"date": tt.date, "year": tt.year}}
		if got := m.Year(); got != tt.want {
			t.Errorf("Year() for DATE=%q, YEAR=%q = %d, want %d", tt.date, tt.year, got, tt.want)
		}
	}
}

// TestLeadingYear pins the year read from the date of an ID3v2, Vorbis,
// APEv2 or INFO tag.
func TestLeadingYear(t *testing.T) {
	for s, want := range map[string]int{
		"1997": 1997, " 1997 ": 1997, "97": 97, "1997-05": 1997, "1997-05-12T10:30:00": 1997, "19970512": 1997,
		"1997.05.12": 1997, "": 0, "May 1997": 0, "19x7-05": 0, "199705": 199705, "12-05-1997": 0, "-1997": 0,
	} {
		if got := leadingYear(s); got != want {
			t.Errorf("leadingYear(%q) = %d, want %d", s, got, want)
		}
	}
}

// TestPictureLengthIsChecked is the regression test for the data length of
// a picture, which was allocated before any of the picture was read: a FLAC
// file of about 100 bytes that says 300 MiB made the reader allocate 300 MiB,
// and 4 GiB is enough to end the process. The comments are still read.
func TestPictureLengthIsChecked(t *testing.T) {
	for _, dataLen := range []uint32{300 * mib, 0xFFFFFFFF} {
		picture := flacPicture(3, "image/png", "", dataLen, pngData)
		for name, file := range map[string][]byte{
			"FLAC": flacFile(flacBlock(4, false, vorbisComment("v", vorbisBasics...)), flacBlock(6, true, picture)),
			"OGG": oggVorbisFile(vorbisComment("v", append(slices.Clone(vorbisBasics),
				"METADATA_BLOCK_PICTURE="+base64.StdEncoding.EncodeToString(picture))...), []byte("audio")),
		} {
			var m Metadata
			var err error
			n := allocated(func() { m, err = ReadFrom(bytes.NewReader(file)) })
			if n > mib {
				t.Errorf("%s, length %d: reading %d bytes allocated %d MiB", name, dataLen, len(file), n/mib)
			}
			if err != nil {
				t.Errorf("%s, length %d: %v", name, dataLen, err)
				continue
			}
			checkVorbisBasics(t, name, m)
			if p := m.Picture(); p != nil {
				t.Errorf("%s, length %d: Picture() = %v, want none", name, dataLen, p)
			}
		}
	}

	// The other lengths in a picture, the same way.
	for name, picture := range map[string][]byte{
		"MIME type length":   slices.Concat([]byte{0, 0, 0, 3}, []byte{0x40, 0, 0, 0x20}, []byte("image/png")),
		"description length": slices.Concat([]byte{0, 0, 0, 3}, []byte{0, 0, 0, 9}, []byte("image/png"), []byte{0x40, 0, 0, 0x20}, []byte("x")),
		"cut short":          flacPicture(3, "image/png", "", 4, nil),
	} {
		file := flacFile(flacBlock(4, false, vorbisComment("v", vorbisBasics...)), flacBlock(6, true, picture))
		var m Metadata
		var err error
		if n := allocated(func() { m, err = ReadFrom(bytes.NewReader(file)) }); n > mib {
			t.Errorf("%s: reading allocated %d MiB", name, n/mib)
		}
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		checkVorbisBasics(t, name, m)
	}
}

// TestFLACDamagedLength is the regression test for a length inside a FLAC
// Vorbis comment block that runs past the block: the reader used to follow
// it and load the rest of the file, 200 MiB here, into memory.
func TestFLACDamagedLength(t *testing.T) {
	comment := vorbisComment("ffmpeg", vorbisBasics...)
	for name, damage := range map[string]func([]byte){
		"vendor length":  func(c []byte) { c[3] = 0x40 },
		"comment count":  func(c []byte) { c[4+6+3] = 0x40 },
		"comment length": func(c []byte) { c[4+6+4+3] = 0x40 },
	} {
		c := slices.Clone(comment)
		damage(c)
		f := &sparseFile{head: slices.Concat([]byte("fLaC"), flacBlock(0, false, make([]byte, 34)), flacBlock(4, true, c)), size: 200 * mib}
		var err error
		n := allocated(func() { _, err = ReadFrom(f) })
		if err == nil {
			t.Errorf("%s: no error", name)
		}
		if n > mib {
			t.Errorf("%s: reading allocated %d MiB", name, n/mib)
		}
	}
}

// TestVorbisUndecodableElementIsSkipped is the regression test for a comment
// or picture the reader cannot decode, which used to make the whole file
// unreadable.
func TestVorbisUndecodableElementIsSkipped(t *testing.T) {
	with := func(extra ...string) []byte {
		return vorbisComment("v", slices.Insert(slices.Clone(vorbisBasics), 1, extra...)...)
	}
	for name, comment := range map[string][]byte{
		"comment with no =":  with("no equals sign"),
		"empty comment":      with(""),
		"picture not base64": with("METADATA_BLOCK_PICTURE=not base64!"),
		"picture cut short":  with("METADATA_BLOCK_PICTURE=" + base64.StdEncoding.EncodeToString([]byte{0, 0, 0, 3, 0, 0})),
	} {
		for format, file := range map[string][]byte{
			"FLAC": flacFile(flacBlock(4, true, comment)),
			"OGG":  oggVorbisFile(comment, []byte("audio")),
		} {
			m := readFromBytes(t, name, file)
			checkVorbisBasics(t, format+", "+name, m)
			if n := len(m.Raw()); n != len(vorbisBasics)+1+strings.Count(name, "picture") {
				t.Errorf("%s, %s: Raw = %v", format, name, m.Raw())
			}
		}
	}

	// A FLAC PICTURE block of a type outside the list, and one that is cut
	// short, before the comments.
	for name, picture := range map[string][]byte{
		"type 21":   flacPicture(21, "image/png", "", uint32(len(pngData)), pngData),
		"cut short": {0, 0, 0, 3, 0, 0},
		"empty":     {},
	} {
		m := readFromBytes(t, name, flacFile(flacBlock(6, false, picture), flacBlock(4, true, vorbisComment("v", vorbisBasics...))))
		checkVorbisBasics(t, "FLAC picture "+name, m)
		if p := m.Picture(); name == "type 21" && (p == nil || p.Type != "" || !bytes.Equal(p.Data, pngData)) {
			t.Errorf("FLAC picture of type 21: Picture() = %v, want the picture with no type name", p)
		}
	}
}

// TestFLACPictureBlock reads the cover art of a FLAC file from its PICTURE
// block, and of an OGG file from its METADATA_BLOCK_PICTURE comment.
func TestFLACPictureBlock(t *testing.T) {
	picture := flacPicture(3, "image/jpeg", "Front", uint32(len(jpegData)), jpegData)
	for name, file := range map[string][]byte{
		"FLAC": flacFile(flacBlock(4, false, vorbisComment("v", vorbisBasics...)), flacBlock(1, false, make([]byte, 100)), flacBlock(6, true, picture)),
		"OGG": oggVorbisFile(vorbisComment("v", append(slices.Clone(vorbisBasics),
			"metadata_block_picture="+base64.StdEncoding.EncodeToString(picture))...), []byte("audio")),
	} {
		m := readFromBytes(t, name, file)
		checkVorbisBasics(t, name, m)
		p := m.Picture()
		if p == nil || p.MIMEType != "image/jpeg" || p.Ext != "jpg" || p.Type != "Cover (front)" || p.Description != "Front" || !bytes.Equal(p.Data, jpegData) {
			t.Errorf("%s: Picture() = %v", name, p)
		}
	}
}

// TestVorbisManyCommentsOfOneKey is the regression test for a key written
// many times, whose values were joined by building a new string for each:
// 200,000 comments took most of a minute.
func TestVorbisManyCommentsOfOneKey(t *testing.T) {
	const n = 200000
	comments := make([]string, n)
	for i := range comments {
		comments[i] = "ARTIST=an artist name"
	}
	file := flacFile(flacBlock(4, true, vorbisComment("v", comments...)))
	start := time.Now()
	m := readFromBytes(t, "many comments", file)
	if d := time.Since(start); d > 10*time.Second {
		t.Errorf("reading %d comments took %v", n, d)
	}
	if got := m.Artist(); got != "an artist name" {
		t.Errorf("Artist() = %q", got)
	}
	if got, _ := m.Raw()["artist"].(string); strings.Count(got, "\x00") != n-1 || len(got) != n*len("an artist name")+n-1 {
		t.Errorf("Raw artist holds %d values in %d bytes, want %d", strings.Count(got, "\x00")+1, len(got), n)
	}
}

// TestVorbisAlbumArtistWithSpace is the regression test for "ALBUM ARTIST",
// a spelling some taggers use and the APEv2 reader accepts, which the Vorbis
// reader did not.
func TestVorbisAlbumArtistWithSpace(t *testing.T) {
	m := readFromBytes(t, "ALBUM ARTIST", flacFile(flacBlock(4, true, vorbisComment("v", "ALBUM ARTIST=Spaced"))))
	if got := m.AlbumArtist(); got != "Spaced" {
		t.Errorf("AlbumArtist() = %q, want Spaced", got)
	}
	m = readFromBytes(t, "both", flacFile(flacBlock(4, true, vorbisComment("v", "ALBUM ARTIST=Spaced", "ALBUMARTIST=Joined"))))
	if got := m.AlbumArtist(); got != "Joined" {
		t.Errorf("AlbumArtist() = %q, want ALBUMARTIST to win", got)
	}
}

// TestVorbisFoobar2000Form reads a FLAC file tagged the way foobar2000 tags
// one: "ALBUM ARTIST" with a space, the totals as TOTALTRACKS and
// TOTALDISCS, which this reader does not read, and several values as
// repeated comments. It is built here to the format; no file foobar2000
// wrote was at hand.
func TestVorbisFoobar2000Form(t *testing.T) {
	m := readFromBytes(t, "foobar2000", flacFile(
		flacBlock(4, false, vorbisComment("reference libFLAC 1.3.2 20170101",
			"TITLE=Silence Café", "ARTIST=Artist One", "ARTIST=Artist Two", "ALBUM ARTIST=Album Artist", "ALBUM=Album",
			"DATE=2001", "TRACKNUMBER=3", "TOTALTRACKS=10", "DISCNUMBER=1", "TOTALDISCS=2", "GENRE=Jazz")),
		flacBlock(1, true, make([]byte, 2048)),
	))
	track, _ := m.Track()
	disc, _ := m.Disc()
	if m.Format() != VORBIS || m.FileType() != FLAC || m.Title() != "Silence Café" || m.Artist() != "Artist One" ||
		m.AlbumArtist() != "Album Artist" || m.Album() != "Album" || m.Year() != 2001 || track != 3 || disc != 1 {
		t.Errorf("read %s %s %q / %q / %q / %q, year %d, track %d, disc %d", m.Format(), m.FileType(),
			m.Title(), m.Artist(), m.AlbumArtist(), m.Album(), m.Year(), track, disc)
	}
	if got := m.Raw()["artist"]; got != "Artist One\x00Artist Two" {
		t.Errorf("Raw artist = %q, want both", got)
	}
}

// opusFile renders an Ogg Opus stream: the OpusHead packet, the OpusTags
// packet with the comments given, and packets of audio.
func opusFile(comment []byte, audio ...[]byte) []byte {
	b := oggPage(2, 7, 0, []byte("OpusHead\x01\x02\x38\x01\x80\xbb\x00\x00\x00\x00\x00"))
	b = append(b, oggPage(0, 7, 1, append([]byte("OpusTags"), comment...))...)
	return append(b, oggPage(4, 7, 2, audio...)...)
}

// TestOpus reads the tags of an Ogg Opus stream, whose comment packet starts
// with "OpusTags", and checks that its hash covers the audio and not the tags.
func TestOpus(t *testing.T) {
	file := opusFile(vorbisComment("libopus", vorbisBasics...), []byte("opus packet 1"), []byte("opus packet 2"))
	m := readFromBytes(t, "opus", file)
	checkVorbisBasics(t, "opus", m)
	if m.Format() != VORBIS || m.FileType() != OGG {
		t.Errorf("format %q, file type %q, want %q, %q", m.Format(), m.FileType(), VORBIS, OGG)
	}

	sum := sumBytes(t, "opus", file)
	retagged := opusFile(vorbisComment("libopus", "TITLE=Another, much longer title"), []byte("opus packet 1"), []byte("opus packet 2"))
	if got := sumBytes(t, "retagged opus", retagged); got != sum {
		t.Errorf("Sum after a retag = %s, want %s", got, sum)
	}
	other := opusFile(vorbisComment("libopus", vorbisBasics...), []byte("opus packet 1"), []byte("opus packet 3"))
	if got := sumBytes(t, "other opus", other); got == sum {
		t.Errorf("Sum of other audio = %s, want it to differ", got)
	}
}

// TestOGGWithoutComment is the regression test for an Ogg stream with no
// comment packet, which returned io.EOF where "no tags" is meant.
func TestOGGWithoutComment(t *testing.T) {
	file := slices.Concat(oggPage(2, 1, 0, []byte("\x01vorbis identification")), oggPage(4, 1, 1, []byte("audio")))
	if _, err := ReadFrom(bytes.NewReader(file)); err != ErrNoTagsFound {
		t.Errorf("ReadFrom = %v, want ErrNoTagsFound", err)
	}
	// A stream cut short in the middle of a page is still an error.
	if _, err := ReadFrom(bytes.NewReader(file[:len(file)-3])); err == nil || err == ErrNoTagsFound {
		t.Errorf("ReadFrom of a stream cut short = %v, want a read error", err)
	}
}

// TestFLACBehindID3v2 is the regression test for a FLAC stream with an ID3v2
// tag in front, which was read as an MP3: its Vorbis comments were not read,
// and its hash covered its FLAC metadata blocks, so a retag changed it.
func TestFLACBehindID3v2(t *testing.T) {
	flac, err := os.ReadFile("testdata/with_tags/sample.flac")
	if err != nil {
		t.Fatal(err)
	}
	id3 := id3Tag(3, 0, id3Frame(3, "TIT2", latin1("ID3 title")), make([]byte, 100))
	file := slices.Concat(id3, flac)

	m := readFromBytes(t, "ID3v2 + FLAC", file)
	if m.Format() != VORBIS || m.FileType() != FLAC {
		t.Errorf("format %q, file type %q, want %q, %q", m.Format(), m.FileType(), VORBIS, FLAC)
	}
	compareMetadata(t, "ID3v2 + FLAC", m, fullMetadata)

	want := sumTestdata(t, "with_tags/sample.flac")
	if got := sumBytes(t, "ID3v2 + FLAC", file); got != want {
		t.Errorf("Sum = %s, want %s, that of the FLAC stream alone", got, want)
	}
	untagged, err := os.ReadFile("testdata/without_tags/sample.flac")
	if err != nil {
		t.Fatal(err)
	}
	if got := sumBytes(t, "ID3v2 + untagged FLAC", slices.Concat(id3, untagged)); got != want {
		t.Errorf("Sum with other Vorbis comments = %s, want %s", got, want)
	}

	// With a v2.4 footer the stream starts 10 bytes later.
	footer := id3v2TagHeader(4, 0x10, 16)
	copy(footer, "3DI")
	withFooter := slices.Concat(id3v2TagHeader(4, 0x10, 16), id3Frame(4, "TIT2", utf8Text("Title")), footer, flac)
	if m := readFromBytes(t, "ID3v2 with footer + FLAC", withFooter); m.FileType() != FLAC || m.Artist() != fullMetadata.Artist {
		t.Errorf("with a footer: file type %q, artist %q", m.FileType(), m.Artist())
	}
	if got := sumBytes(t, "ID3v2 with footer + FLAC", withFooter); got != want {
		t.Errorf("Sum with a footer = %s, want %s", got, want)
	}
}

// TestShortFileHasNoTags is the regression test for a file too short for an
// ID3v1 tag, which returned a seek error where "no tags" is meant.
func TestShortFileHasNoTags(t *testing.T) {
	for _, n := range []int{11, 64, 127} {
		if _, err := ReadFrom(bytes.NewReader(mp3Audio[:n])); err != ErrNoTagsFound {
			t.Errorf("ReadFrom of %d bytes = %v, want ErrNoTagsFound", n, err)
		}
	}
	b := binary.BigEndian.AppendUint32(nil, 0xFFFB9000)
	if _, err := ReadID3v1Tags(bytes.NewReader(b)); err != ErrNotID3v1 {
		t.Errorf("ReadID3v1Tags of 4 bytes = %v, want ErrNotID3v1", err)
	}
}
