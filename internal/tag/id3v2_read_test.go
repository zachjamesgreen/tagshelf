package tag

import (
	"bytes"
	"encoding/binary"
	"io"
	"slices"
	"strconv"
	"testing"
	"time"
)

// id3Basics are the frames of a tag that qualifies for the library, in
// ID3v2.3 and 2.4.
func id3Basics(version byte) [][]byte {
	return [][]byte{
		id3Frame(version, "TIT2", utf8Text("Title")),
		id3Frame(version, "TPE1", utf8Text("Artist")),
		id3Frame(version, "TALB", utf8Text("Album")),
		id3Frame(version, "TXXX", txxxBody("MusicBrainz Album Id", "album-id")),
	}
}

func checkID3Basics(t *testing.T, name string, m Metadata) {
	t.Helper()
	if m.Title() != "Title" || m.Artist() != "Artist" || m.Album() != "Album" {
		t.Errorf("%s: read %q / %q / %q, want Title / Artist / Album", name, m.Title(), m.Artist(), m.Album())
	}
	c, _ := m.Raw()["TXXX"].(*Comm)
	if c == nil || c.Description != "MusicBrainz Album Id" || c.Text != "album-id" {
		t.Errorf("%s: TXXX = %v, want the MusicBrainz album ID", name, m.Raw()["TXXX"])
	}
}

// TestID3v2UndecodableFrameIsSkipped is the regression test for a frame the
// reader cannot decode, which used to make the whole file unreadable. The
// frame sits between the good ones, so reading has to go on past it.
func TestID3v2UndecodableFrameIsSkipped(t *testing.T) {
	for _, version := range []byte{3, 4} {
		for name, bad := range map[string][]byte{
			"COMM of 1 byte":             id3Frame(version, "COMM", []byte{0}),
			"COMM of 3 bytes":            id3Frame(version, "COMM", []byte{0, 'e', 'n'}),
			"empty TXXX":                 id3Frame(version, "TXXX", nil),
			"UFID with no zero byte":     id3Frame(version, "UFID", []byte("http://musicbrainz.org")),
			"APIC with no terminator":    id3Frame(version, "APIC", slices.Concat([]byte{0}, []byte("image/png"), []byte{0, 3}, []byte("description"))),
			"APIC with no MIME type end": id3Frame(version, "APIC", []byte{0, 'i', 'm', 'a', 'g', 'e'}),
		} {
			frames := id3Basics(version)
			frames = slices.Insert(frames, 1, bad)
			m := readID3(t, name, id3Tag(version, 0, frames...))
			checkID3Basics(t, "v2."+strconv.Itoa(int(version))+" "+name, m)
		}
	}

	// A 2.4 frame flagged as compressed with no data length indicator.
	frames := slices.Insert(id3Basics(4), 1, id3FrameFlags(4, "TCOM", 0x08, utf8Text("Composer")))
	m := readID3(t, "compressed", id3Tag(4, 0, frames...))
	checkID3Basics(t, "compressed without a data length indicator", m)
	if got := m.Composer(); got != "" {
		t.Errorf("Composer() of a compressed frame = %q, want it left out", got)
	}
}

// TestID3v2UTF16 reads UTF-16 text in every form: with either byte order
// mark, with none under encoding 1 (little-endian, as Windows taggers left
// it), as encoding 2 (big-endian), and with an odd byte at the end. In
// frames with a description, the description ends at a zero code unit, which
// the reader used to look for at odd offsets too.
func TestID3v2UTF16(t *testing.T) {
	be := func(s string) []byte { return utf16Units(s, binary.BigEndian) }
	le := func(s string) []byte { return utf16Units(s, binary.LittleEndian) }
	m := readID3(t, "UTF-16", id3Tag(3, 0,
		id3Frame(3, "TIT2", utf16Text("Títle")),
		id3Frame(3, "TPE1", slices.Concat([]byte{1, 0xFE, 0xFF}, be("Ártist"))),
		id3Frame(3, "TALB", slices.Concat([]byte{1}, le("Álbum"))),
		id3Frame(3, "TPE2", slices.Concat([]byte{2}, be("Álbum Artist"))),
		// Ended by a single zero byte, where two are due.
		id3Frame(3, "TCOM", slices.Concat([]byte{1}, utf16LE("Composer"), []byte{0})),
		// Picard's form of a MusicBrainz ID in a 2.3 tag.
		id3Frame(3, "TXXX", slices.Concat([]byte{1}, utf16LE("MusicBrainz Album Id"), []byte{0, 0}, utf16LE("album-id"))),
		// Encoding 2 with ASCII text: every other byte is zero.
		id3Frame(3, "TXXX", slices.Concat([]byte{2}, be("MusicBrainz Artist Id"), []byte{0, 0}, be("artist-id"))),
		// U+0100 after "a" puts two zero bytes at an odd offset.
		id3Frame(3, "TXXX", slices.Concat([]byte{1}, utf16LE("aĀ"), []byte{0, 0}, utf16LE("value"))),
		id3Frame(3, "COMM", slices.Concat([]byte{1}, []byte("eng"), utf16LE(""), []byte{0, 0}, utf16LE("Comment"))),
		id3Frame(3, "APIC", slices.Concat([]byte{1}, []byte("image/jpeg\x00\x03"), utf16LE("Cover"), []byte{0, 0}, jpegData)),
	))
	if m.Title() != "Títle" || m.Artist() != "Ártist" || m.Album() != "Álbum" || m.AlbumArtist() != "Álbum Artist" || m.Composer() != "Composer" {
		t.Errorf("read %q / %q / %q / %q / %q", m.Title(), m.Artist(), m.Album(), m.AlbumArtist(), m.Composer())
	}
	for key, want := range map[string]Comm{
		"TXXX":   {Description: "MusicBrainz Album Id", Text: "album-id"},
		"TXXX_0": {Description: "MusicBrainz Artist Id", Text: "artist-id"},
		"TXXX_1": {Description: "aĀ", Text: "value"},
		"COMM":   {Language: "eng", Text: "Comment"},
	} {
		if c, _ := m.Raw()[key].(*Comm); c == nil || *c != want {
			t.Errorf("%s = %v, want %v", key, m.Raw()[key], want)
		}
	}
	if m.Comment() != "Comment" {
		t.Errorf("Comment() = %q, want Comment", m.Comment())
	}
	p := m.Picture()
	if p == nil || p.Description != "Cover" || p.MIMEType != "image/jpeg" || p.Ext != "jpg" || p.Type != "Cover (front)" || !bytes.Equal(p.Data, jpegData) {
		t.Errorf("Picture() = %v, want the JPEG described as Cover", p)
	}
}

// TestID3v24Unsynchronisation is the regression test for ID3v2.4 tags, which
// unsynchronise each frame on its own: the frame's size counts the stuffed
// bytes, and a data length indicator gives the length without them. The
// reader used to treat 2.4 like 2.3 and lose every frame after the first
// that held a stuffed byte, and it took the indicator for the size.
func TestID3v24Unsynchronisation(t *testing.T) {
	apic := apicBody("image/jpeg", 3, "", jpegData)
	stuffed := unsynchronise(apic)
	if len(stuffed) == len(apic) {
		t.Fatal("the picture has nothing to unsynchronise")
	}
	withIndicator := id3FrameFlags(4, "APIC", 0x03, slices.Concat(synchsafe(len(apic)), stuffed))

	for name, tag := range map[string][]byte{
		"header flag":               id3Tag(4, 0x80, slices.Insert(id3Basics(4), 1, id3Frame(4, "APIC", stuffed))...),
		"frame flags":               id3Tag(4, 0, slices.Insert(id3Basics(4), 1, withIndicator)...),
		"header flag, picture last": id3Tag(4, 0x80, append(id3Basics(4), id3Frame(4, "APIC", stuffed))...),
		"frame flags, picture last": id3Tag(4, 0, append(id3Basics(4), withIndicator)...),
		// A data length indicator on a frame that is not unsynchronised.
		"indicator alone": id3Tag(4, 0, slices.Insert(id3Basics(4), 1, id3FrameFlags(4, "APIC", 0x01, slices.Concat(synchsafe(len(apic)), apic)))...),
	} {
		m := readID3(t, name, tag)
		checkID3Basics(t, name, m)
		if p := m.Picture(); p == nil || !bytes.Equal(p.Data, jpegData) {
			t.Errorf("%s: Picture() = %v, want the %d bytes of the JPEG", name, p, len(jpegData))
		}
	}
}

// TestID3v23Unsynchronisation reads a 2.3 tag, and a 2.2 one, unsynchronised
// as a whole, sizes counted before the stuffing.
func TestID3v23Unsynchronisation(t *testing.T) {
	frames := slices.Insert(id3Basics(3), 1, id3Frame(3, "APIC", apicBody("image/jpeg", 3, "", jpegData)))
	body := unsynchronise(slices.Concat(frames...))
	m := readID3(t, "v2.3", id3Tag(3, 0x80, body))
	checkID3Basics(t, "v2.3 unsynchronised", m)
	if p := m.Picture(); p == nil || !bytes.Equal(p.Data, jpegData) {
		t.Errorf("v2.3: Picture() = %v, want the %d bytes of the JPEG", p, len(jpegData))
	}

	pic := slices.Concat([]byte{0}, []byte("JPG\x03"), []byte{0}, jpegData)
	body = unsynchronise(slices.Concat(id3Frame(2, "TT2", latin1("Title")), id3Frame(2, "PIC", pic), id3Frame(2, "TP1", latin1("Artist"))))
	m = readID3(t, "v2.2", id3Tag(2, 0x80, body))
	if p := m.Picture(); m.Title() != "Title" || m.Artist() != "Artist" || p == nil || !bytes.Equal(p.Data, jpegData) {
		t.Errorf("v2.2 unsynchronised: read %q / %q / %v", m.Title(), m.Artist(), p)
	}
}

// countingReader counts the calls to Read.
type countingReader struct {
	io.ReadSeeker
	reads int
}

func (r *countingReader) Read(p []byte) (int, error) {
	r.reads++
	return r.ReadSeeker.Read(p)
}

// TestID3v2UnsynchronisedReadsInBlocks is the regression test for the
// unsynchronisation filter, which asked the file for one byte at a time: a
// system call for every byte of the tag.
func TestID3v2UnsynchronisedReadsInBlocks(t *testing.T) {
	picture := slices.Concat(jpegData, bytes.Repeat([]byte{0x41, 0xFF, 0xE2}, mib/3))
	frames := append(id3Basics(3), id3Frame(3, "APIC", apicBody("image/jpeg", 3, "", picture)))
	file := slices.Concat(id3Tag(3, 0x80, unsynchronise(slices.Concat(frames...))), mp3Audio)

	r := &countingReader{ReadSeeker: bytes.NewReader(file)}
	m, err := ReadFrom(r)
	if err != nil {
		t.Fatal(err)
	}
	if p := m.Picture(); p == nil || !bytes.Equal(p.Data, picture) {
		t.Errorf("Picture() = %v, want the %d bytes written", p, len(picture))
	}
	if r.reads > 100 {
		t.Errorf("%d calls to Read for a tag of %d bytes, want a few", r.reads, len(file))
	}
}

// TestID3v2MultipleValues is the regression test for a text frame with
// several values, which ID3v2.4 separates with NUL: the reader used to drop
// the NULs and run the values together.
func TestID3v2MultipleValues(t *testing.T) {
	m := readID3(t, "multiple values", id3Tag(4, 0,
		id3Frame(4, "TPE1", utf8Text("Artist One\x00Artist Two")),
		id3Frame(4, "TPE2", utf16Text("Album Artist One\x00Album Artist Two")),
		// Each value with its own byte order mark, and a terminator.
		id3Frame(4, "TCOM", slices.Concat([]byte{1}, utf16LE("One"), []byte{0, 0}, utf16LE("Two"), []byte{0, 0})),
		id3Frame(4, "TIT2", utf8Text("Title\x00")),
	))
	if got := m.Artist(); got != "Artist One" {
		t.Errorf("Artist() = %q, want the first value", got)
	}
	if got := m.AlbumArtist(); got != "Album Artist One" {
		t.Errorf("AlbumArtist() = %q, want the first value", got)
	}
	if got := m.Title(); got != "Title" {
		t.Errorf("Title() = %q, want Title", got)
	}
	for key, want := range map[string]string{
		"TPE1": "Artist One\x00Artist Two",
		"TPE2": "Album Artist One\x00Album Artist Two",
		"TCOM": "One\x00Two",
		"TIT2": "Title",
	} {
		if got := m.Raw()[key]; got != want {
			t.Errorf("Raw %s = %q, want %q", key, got, want)
		}
	}
}

// TestID3v2Windows1252 is the regression test for frames labelled
// ISO-8859-1 that hold Windows-1252 punctuation, which used to read as the
// control characters U+0080 to U+009F.
func TestID3v2Windows1252(t *testing.T) {
	m := readID3(t, "Windows-1252", id3Tag(3, 0,
		id3Frame(3, "TIT2", latin1("Don\x92t Stop \x96 Live")),
		id3Frame(3, "TPE1", latin1("Bj\xf6rk")),
	))
	if got, want := m.Title(), "Don’t Stop – Live"; got != want {
		t.Errorf("Title() = %q, want %q", got, want)
	}
	if got, want := m.Artist(), "Björk"; got != want {
		t.Errorf("Artist() = %q, want %q", got, want)
	}
}

// TestID3v24PlainFrameSizes is the regression test for ID3v2.4 tags whose
// frame sizes are plain 32-bit numbers, as old iTunes versions wrote them,
// where synchsafe ones are due. The two differ from 128 bytes up; the frames
// after the first such frame used to be lost.
func TestID3v24PlainFrameSizes(t *testing.T) {
	plain := func(name string, body []byte) []byte {
		f := id3Frame(4, name, body)
		binary.BigEndian.PutUint32(f[4:8], uint32(len(body)))
		return f
	}
	for _, n := range []int{414, 127, 128, 255, 256, 70000} {
		apic := apicBody("image/png", 3, "", slices.Concat(pngData, bytes.Repeat([]byte{0x41, 0x00}, n))[:n])
		for name, frame := range map[string][]byte{"plain": plain("APIC", apic), "synchsafe": id3Frame(4, "APIC", apic)} {
			name = name + " size, picture of " + strconv.Itoa(n) + " bytes"
			for layout, tag := range map[string][]byte{
				"in the middle":  id3Tag(4, 0, slices.Insert(id3Basics(4), 1, frame)...),
				"last":           id3Tag(4, 0, append(id3Basics(4), frame)...),
				"before padding": id3Tag(4, 0, append(append(id3Basics(4), frame), make([]byte, 300))...),
			} {
				m := readID3(t, name, tag)
				checkID3Basics(t, name+", "+layout, m)
				if p := m.Picture(); p == nil || len(p.Data) != len(apic)-len("image/png")-4 {
					t.Errorf("%s, %s: Picture() = %v, want %d bytes", name, layout, p, len(apic)-len("image/png")-4)
				}
			}
		}
	}
}

// TestID3v2EmptyFrameIsSkipped is the regression test for a frame of size 0,
// which used to end the read: only a name of zero bytes is padding.
func TestID3v2EmptyFrameIsSkipped(t *testing.T) {
	for _, version := range []byte{3, 4} {
		frames := slices.Insert(id3Basics(version), 1, id3Frame(version, "TCOP", nil))
		frames = append(frames, make([]byte, 64)) // padding
		m := readID3(t, "empty frame", id3Tag(version, 0, frames...))
		checkID3Basics(t, "v2."+strconv.Itoa(int(version))+" with an empty TCOP", m)
		if _, ok := m.Raw()["TCOP"]; ok {
			t.Errorf("v2.%d: the empty frame was stored", version)
		}
		if n := len(m.Raw()); n != 4 {
			t.Errorf("v2.%d: %d frames read, want 4: %v", version, n, m.Raw())
		}
	}
}

// TestID3v22LastFrame is the regression test for the frame loop, which
// counted the tag's 10-byte header against its size and so stopped 10 bytes
// early: a last ID3v2.2 frame of up to 10 bytes in a tag with no padding was
// not read.
func TestID3v22LastFrame(t *testing.T) {
	m := readID3(t, "v2.2", id3Tag(2, 0,
		id3Frame(2, "TT2", latin1("Title")),
		id3Frame(2, "TRK", latin1("7")),
	))
	if x, _ := m.Track(); x != 7 {
		t.Errorf("Track() = %d, want 7 from the last frame", x)
	}
}

// TestID3v2ManyFramesOfOneName is the regression test for the numbering of
// frames with a name already taken, which tried every number from 0 for each
// frame: 40,000 frames took over a minute.
func TestID3v2ManyFramesOfOneName(t *testing.T) {
	const n = 40000
	frame := id3Frame(2, "XXX", []byte{1})
	start := time.Now()
	m := readID3(t, "many frames", id3Tag(2, 0, bytes.Repeat(frame, n)))
	if d := time.Since(start); d > 10*time.Second {
		t.Errorf("reading %d frames took %v", n, d)
	}
	raw := m.Raw()
	if len(raw) != n {
		t.Fatalf("%d frames read, want %d", len(raw), n)
	}
	for _, key := range []string{"XXX", "XXX_0", "XXX_1", "XXX_" + strconv.Itoa(n-2)} {
		if _, ok := raw[key]; !ok {
			t.Errorf("no frame stored as %s", key)
		}
	}
}

// TestID3v2DamagedFrameSize is the regression test for a frame whose size
// runs past its tag: the reader used to follow it and load the rest of the
// file, 200 MiB here, into memory. The frames before it are kept.
func TestID3v2DamagedFrameSize(t *testing.T) {
	for _, version := range []byte{2, 3, 4} {
		title, damaged := id3Frame(version, "TIT2", utf8Text("Title")), id3Frame(version, "TALB", utf8Text("Album"))
		if version == 2 {
			title, damaged = id3Frame(2, "TT2", latin1("Title")), id3Frame(2, "TAL", latin1("Album"))
			damaged[3] = 0x7F
		} else {
			damaged[4] = 0x07
		}
		f := &sparseFile{head: id3Tag(version, 0, title, damaged, make([]byte, 100)), size: 200 * mib}
		var m Metadata
		var err error
		n := allocated(func() { m, err = ReadFrom(f) })
		if err != nil {
			t.Fatalf("v2.%d: %v", version, err)
		}
		if n > mib {
			t.Errorf("v2.%d: reading allocated %d MiB", version, n/mib)
		}
		if m.Title() != "Title" || m.Album() != "" {
			t.Errorf("v2.%d: read title %q and album %q, want the title alone", version, m.Title(), m.Album())
		}
	}
}

// TestID3v2TagLongerThanFile reads a tag whose size runs past the end of
// the file, as far as the file goes.
func TestID3v2TagLongerThanFile(t *testing.T) {
	tag := id3Tag(4, 0, id3Basics(4)...)
	copy(tag[6:10], synchsafe(50*mib))
	checkID3Basics(t, "tag longer than the file", readFromBytes(t, "long tag", tag))
}

// TestID3v2ExtendedHeader reads tags with an extended header, which sits
// between the tag header and the frames: 4 bytes of size that do not count
// themselves in 2.3, and a synchsafe size that does in 2.4.
func TestID3v2ExtendedHeader(t *testing.T) {
	v23 := []byte{0, 0, 0, 6, 0, 0, 0, 0, 0, 0}
	v23CRC := []byte{0, 0, 0, 10, 0x80, 0, 0, 0, 0, 0, 1, 2, 3, 4}
	v24 := []byte{0, 0, 0, 6, 1, 0}
	for name, tag := range map[string][]byte{
		"v2.3":          id3Tag(3, 0x40, append([][]byte{v23}, id3Basics(3)...)...),
		"v2.3 with CRC": id3Tag(3, 0x40, append([][]byte{v23CRC}, id3Basics(3)...)...),
		"v2.4":          id3Tag(4, 0x40, append([][]byte{v24}, id3Basics(4)...)...),
	} {
		checkID3Basics(t, name+" extended header", readID3(t, name, tag))
	}

	// An extended header that runs past the tag is an error, as before.
	for name, tag := range map[string][]byte{
		"v2.3":           id3Tag(3, 0x40, []byte{0x0E, 0xDB, 0xDB, 0xDB}, id3Frame(3, "TIT2", utf8Text("Title"))),
		"v2.4":           id3Tag(4, 0x40, []byte{0x7F, 0x7F, 0x7F, 0x7F}, id3Frame(4, "TIT2", utf8Text("Title"))),
		"v2.4 of size 0": id3Tag(4, 0x40, []byte{0, 0, 0, 0}, id3Frame(4, "TIT2", utf8Text("Title"))),
	} {
		if _, err := ReadFrom(bytes.NewReader(slices.Concat(tag, mp3Audio))); err == nil {
			t.Errorf("%s: no error for an extended header that cannot be right", name)
		}
	}
}

// TestID3v2FrameFlags covers the frame flags the reader acts on. Compressed
// and encrypted frames are left out; the frames around them are read.
func TestID3v2FrameFlags(t *testing.T) {
	composer := utf8Text("Composer")
	for name, tag := range map[string][]byte{
		"v2.3 compressed": id3Tag(3, 0, slices.Insert(id3Basics(3), 1,
			id3FrameFlags(3, "TCOM", 0x80, slices.Concat([]byte{0, 0, 0, 9}, composer)))...),
		"v2.3 encrypted": id3Tag(3, 0, slices.Insert(id3Basics(3), 1,
			id3FrameFlags(3, "TCOM", 0x40, slices.Concat([]byte{1}, composer)))...),
		"v2.4 compressed": id3Tag(4, 0, slices.Insert(id3Basics(4), 1,
			id3FrameFlags(4, "TCOM", 0x09, slices.Concat(synchsafe(9), composer)))...),
		"v2.4 encrypted": id3Tag(4, 0, slices.Insert(id3Basics(4), 1,
			id3FrameFlags(4, "TCOM", 0x04, slices.Concat([]byte{1}, composer)))...),
	} {
		m := readID3(t, name, tag)
		checkID3Basics(t, name, m)
		if got := m.Composer(); got != "" {
			t.Errorf("%s: Composer() = %q, want the frame left out", name, got)
		}
	}

	// Flags that do not change how the frame is stored: the status flags,
	// and "read only".
	f := id3Frame(4, "TCOM", composer)
	f[8] = 0x70
	m := readID3(t, "status flags", id3Tag(4, 0, append(id3Basics(4), f)...))
	if got := m.Composer(); got != "Composer" {
		t.Errorf("status flags: Composer() = %q, want Composer", got)
	}
}

// TestID3v2URLAndBinaryFrames covers the frames that are not text: URLs,
// which are always ISO-8859-1, the user-defined URL with its description,
// and frames the reader keeps as their bytes.
func TestID3v2URLAndBinaryFrames(t *testing.T) {
	m := readID3(t, "URL frames", id3Tag(3, 0,
		id3Frame(3, "WOAR", []byte("http://example.com/artist")),
		id3Frame(3, "WXXX", slices.Concat([]byte{3}, []byte("Página"), []byte{0}, []byte("http://example.com/page"))),
		id3Frame(3, "PRIV", []byte("owner\x00\x01\x02\x03")),
		id3Frame(3, "UFID", []byte("http://musicbrainz.org\x00recording-id")),
		id3Frame(3, "USLT", slices.Concat([]byte{3}, []byte("eng"), []byte{0}, []byte("Line one\nLine two"))),
	))
	raw := m.Raw()
	if got := raw["WOAR"]; got != "http://example.com/artist" {
		t.Errorf("WOAR = %q", got)
	}
	if c, _ := raw["WXXX"].(*Comm); c == nil || c.Description != "Página" || c.Text != "http://example.com/page" {
		t.Errorf("WXXX = %v", raw["WXXX"])
	}
	if b, _ := raw["PRIV"].([]byte); string(b) != "owner\x00\x01\x02\x03" {
		t.Errorf("PRIV = %q, want its bytes", raw["PRIV"])
	}
	if u, _ := raw["UFID"].(*UFID); u == nil || u.Provider != "http://musicbrainz.org" || string(u.Identifier) != "recording-id" {
		t.Errorf("UFID = %v", raw["UFID"])
	}
	if got := m.Lyrics(); got != "Line one\nLine two" {
		t.Errorf("Lyrics() = %q", got)
	}
}

// TestID3v2Pictures reads the picture frames: APIC with PNG and JPEG data,
// and the ID3v2.2 PIC frame, whose format is three letters that files write
// in upper case.
func TestID3v2Pictures(t *testing.T) {
	for _, version := range []byte{3, 4} {
		m := readID3(t, "APIC", id3Tag(version, 0, append(id3Basics(version),
			id3Frame(version, "APIC", apicBody("image/png", 3, "Front", pngData)))...))
		p := m.Picture()
		if p == nil || p.MIMEType != "image/png" || p.Ext != "png" || p.Type != "Cover (front)" || p.Description != "Front" || !bytes.Equal(p.Data, pngData) {
			t.Errorf("v2.%d APIC: Picture() = %v", version, p)
		}
	}

	for format, mime := range map[string]string{"JPG": "image/jpeg", "jpg": "image/jpeg", "PNG": "image/png"} {
		m := readID3(t, "PIC", id3Tag(2, 0,
			id3Frame(2, "TT2", latin1("Title")),
			id3Frame(2, "PIC", slices.Concat([]byte{0}, []byte(format), []byte{4}, []byte("Back"), []byte{0}, jpegData)),
		))
		p := m.Picture()
		if p == nil || p.MIMEType != mime || p.Ext != format || p.Type != "Cover (back)" || p.Description != "Back" || !bytes.Equal(p.Data, jpegData) {
			t.Errorf("PIC %s: Picture() = %v, want MIME type %s", format, p, mime)
		}
	}
}

// TestID3v2PicardForms reads tags in the two forms Picard writes: ID3v2.3
// with every text in UTF-16, and ID3v2.4 in UTF-8 with several artists in
// one frame. They are built here to the specification; no file Picard wrote
// was at hand.
func TestID3v2PicardForms(t *testing.T) {
	u := func(desc, value string) []byte {
		return slices.Concat([]byte{1}, utf16LE(desc), []byte{0, 0}, utf16LE(value))
	}
	v23 := id3Tag(3, 0,
		id3Frame(3, "TIT2", utf16Text("Silence Café")),
		id3Frame(3, "TPE1", utf16Text("Artist One/Artist Two")),
		id3Frame(3, "TPE2", utf16Text("Album Artist")),
		id3Frame(3, "TALB", utf16Text("Album")),
		id3Frame(3, "TRCK", utf16Text("3/10")),
		id3Frame(3, "TPOS", utf16Text("1/2")),
		id3Frame(3, "TYER", utf16Text("2001")),
		id3Frame(3, "TDAT", utf16Text("1205")),
		id3Frame(3, "TXXX", u("MusicBrainz Album Id", "album-id")),
		id3Frame(3, "TXXX", u("MusicBrainz Artist Id", "artist-1/artist-2")),
		id3Frame(3, "UFID", []byte("http://musicbrainz.org\x00recording-id")),
		id3Frame(3, "COMM", slices.Concat([]byte{1}, []byte("eng"), utf16LE(""), []byte{0, 0}, utf16LE("A comment"))),
		id3Frame(3, "APIC", slices.Concat([]byte{0}, []byte("image/jpeg\x00\x03\x00"), jpegData)),
		make([]byte, 1024),
	)
	v24 := id3Tag(4, 0,
		id3Frame(4, "TIT2", utf8Text("Silence Café")),
		id3Frame(4, "TPE1", utf8Text("Artist One\x00Artist Two")),
		id3Frame(4, "TPE2", utf8Text("Album Artist")),
		id3Frame(4, "TALB", utf8Text("Album")),
		id3Frame(4, "TRCK", utf8Text("3/10")),
		id3Frame(4, "TPOS", utf8Text("1/2")),
		id3Frame(4, "TDRC", utf8Text("2001-12-05")),
		id3Frame(4, "TXXX", txxxBody("MusicBrainz Album Id", "album-id")),
		id3Frame(4, "TXXX", txxxBody("MusicBrainz Artist Id", "artist-1\x00artist-2")),
		id3Frame(4, "UFID", []byte("http://musicbrainz.org\x00recording-id")),
		id3Frame(4, "COMM", slices.Concat([]byte{3}, []byte("eng"), []byte{0}, []byte("A comment"))),
		id3Frame(4, "APIC", slices.Concat([]byte{3}, []byte("image/jpeg\x00\x03\x00"), jpegData)),
		make([]byte, 1024),
	)
	for name, tt := range map[string]struct {
		tag               []byte
		format            Format
		artist, artistIDs string
	}{
		"v2.3": {v23, ID3v2_3, "Artist One/Artist Two", "artist-1/artist-2"},
		"v2.4": {v24, ID3v2_4, "Artist One", "artist-1\x00artist-2"},
	} {
		m := readID3(t, name, tt.tag)
		track, tracks := m.Track()
		disc, discs := m.Disc()
		if m.Format() != tt.format || m.FileType() != MP3 || m.Title() != "Silence Café" || m.Artist() != tt.artist ||
			m.AlbumArtist() != "Album Artist" || m.Album() != "Album" || m.Year() != 2001 ||
			track != 3 || tracks != 10 || disc != 1 || discs != 2 || m.Comment() != "A comment" {
			t.Errorf("%s: read %s %s %q / %q / %q / %q, year %d, track %d/%d, disc %d/%d, comment %q", name, m.Format(), m.FileType(),
				m.Title(), m.Artist(), m.AlbumArtist(), m.Album(), m.Year(), track, tracks, disc, discs, m.Comment())
		}
		raw := m.Raw()
		if c, _ := raw["TXXX"].(*Comm); c == nil || c.Description != "MusicBrainz Album Id" || c.Text != "album-id" {
			t.Errorf("%s: TXXX = %v", name, raw["TXXX"])
		}
		if c, _ := raw["TXXX_0"].(*Comm); c == nil || c.Description != "MusicBrainz Artist Id" || c.Text != tt.artistIDs {
			t.Errorf("%s: TXXX_0 = %v", name, raw["TXXX_0"])
		}
		if u, _ := raw["UFID"].(*UFID); u == nil || u.Provider != "http://musicbrainz.org" || string(u.Identifier) != "recording-id" {
			t.Errorf("%s: UFID = %v", name, raw["UFID"])
		}
		if p := m.Picture(); p == nil || p.MIMEType != "image/jpeg" || p.Type != "Cover (front)" || p.Description != "" || !bytes.Equal(p.Data, jpegData) {
			t.Errorf("%s: Picture() = %v", name, p)
		}
	}
}
