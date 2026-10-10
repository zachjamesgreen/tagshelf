package tag

import (
	"bytes"
	"encoding/binary"
	"io"
	"runtime"
	"slices"
	"testing"
	"unicode/utf16"
)

// The helpers in this file build tags and files byte by byte to the formats'
// specifications, for the tests of inputs no fixture holds: damaged files,
// files made on purpose, and the forms particular taggers write. No file
// from those taggers was at hand, so the forms are built from what the
// specifications and the taggers' documentation say they write.

// mp3Audio stands in for the MPEG frames after a tag.
var mp3Audio = append([]byte{0xff, 0xfb, 0x90, 0x00}, bytes.Repeat([]byte("frame"), 40)...)

// jpegData and pngData start as the real formats do. The JPEG holds the
// 0xFF bytes that unsynchronisation has to stuff.
var (
	jpegData = slices.Concat([]byte{0xFF, 0xD8, 0xFF, 0xE0, 0, 16}, []byte("JFIF\x00"), bytes.Repeat([]byte{0xFF, 0x00, 0xFF, 0xE1, 0x41}, 10), []byte{0xFF, 0xD9})
	pngData  = slices.Concat(pngHeader, []byte("\x00\x00\x00\rIHDR"), bytes.Repeat([]byte{7}, 40))
)

// Text in the encodings an ID3v2 frame names in its first byte.
func latin1(s string) []byte { return append([]byte{0}, s...) }
func utf8Text(s string) []byte {
	return append([]byte{3}, s...)
}

// utf16Units encodes s as UTF-16 in the given byte order, with no mark.
func utf16Units(s string, bo binary.AppendByteOrder) []byte {
	var b []byte
	for _, u := range utf16.Encode([]rune(s)) {
		b = bo.AppendUint16(b, u)
	}
	return b
}

// utf16LE encodes s as little-endian UTF-16 behind a byte order mark, the
// form Windows taggers and Picard write.
func utf16LE(s string) []byte {
	return append([]byte{0xFF, 0xFE}, utf16Units(s, binary.LittleEndian)...)
}

// utf16Text is a text frame's body in encoding 1.
func utf16Text(s string) []byte { return append([]byte{1}, utf16LE(s)...) }

// id3Frame renders a frame of an ID3v2 tag of the given version (2, 3 or 4)
// with no flags set.
func id3Frame(version byte, name string, body []byte) []byte {
	return id3FrameFlags(version, name, 0, body)
}

// id3FrameFlags renders a frame with the given format flags (the second
// flag byte). In 2.2 there are none.
func id3FrameFlags(version byte, name string, flags byte, body []byte) []byte {
	n := len(body)
	switch version {
	case 2:
		return slices.Concat([]byte(name), []byte{byte(n >> 16), byte(n >> 8), byte(n)}, body)
	case 3:
		return slices.Concat([]byte(name), binary.BigEndian.AppendUint32(nil, uint32(n)), []byte{0, flags}, body)
	}
	return slices.Concat([]byte(name), synchsafe(n), []byte{0, flags}, body)
}

func synchsafe(n int) []byte {
	return []byte{byte(n >> 21 & 0x7f), byte(n >> 14 & 0x7f), byte(n >> 7 & 0x7f), byte(n & 0x7f)}
}

// id3Tag renders an ID3v2 tag from its frames (and anything else that goes
// in its body, such as an extended header or padding).
func id3Tag(version, flags byte, body ...[]byte) []byte {
	b := slices.Concat(body...)
	return append(id3v2TagHeader(version, flags, len(b)), b...)
}

// unsynchronise stuffs a zero byte after each 0xFF that would otherwise look
// like a sync word or a stuffed byte, as the ID3v2 specification asks.
func unsynchronise(b []byte) []byte {
	var out []byte
	for i, x := range b {
		out = append(out, x)
		if x == 0xFF && (i+1 == len(b) || b[i+1] == 0 || b[i+1] >= 0xE0) {
			out = append(out, 0)
		}
	}
	return out
}

// apicBody is the body of an APIC frame with a description in ISO-8859-1.
func apicBody(mime string, picType byte, desc string, data []byte) []byte {
	return slices.Concat([]byte{0}, []byte(mime), []byte{0, picType}, []byte(desc), []byte{0}, data)
}

// txxxBody is the body of a TXXX frame in UTF-8.
func txxxBody(desc, value string) []byte {
	return slices.Concat([]byte{3}, []byte(desc), []byte{0}, []byte(value))
}

// readID3 reads a crafted ID3v2 tag followed by audio.
func readID3(t *testing.T, name string, tag []byte) Metadata {
	t.Helper()
	return readFromBytes(t, name, slices.Concat(tag, mp3Audio))
}

// dataAtom renders the "data" atom of an MP4 item: the class, a locale of
// zero and the value.
func dataAtom(class int, value []byte) []byte {
	return atom("data", slices.Concat([]byte{0, byte(class >> 16), byte(class >> 8), byte(class)}, []byte{0, 0, 0, 0}, value))
}

// textItem renders an MP4 text item with one value for each of values.
func textItem(name string, values ...string) []byte {
	var body []byte
	for _, v := range values {
		body = append(body, dataAtom(atomClassText, []byte(v))...)
	}
	return atom(name, body)
}

// freeformItem renders a "----" item as iTunes and Picard write it.
func freeformItem(name, value string) []byte {
	return atom("----", slices.Concat(
		atom("mean", append([]byte{0, 0, 0, 0}, "com.apple.iTunes"...)),
		atom("name", append([]byte{0, 0, 0, 0}, name...)),
		dataAtom(atomClassText, []byte(value)),
	))
}

// mp4Moov renders the moov atom that holds items.
func mp4Moov(items ...[]byte) []byte {
	ilst := atom("ilst", slices.Concat(items...))
	hdlr := atom("hdlr", make([]byte, 25))
	meta := atom("meta", slices.Concat([]byte{0, 0, 0, 0}, hdlr, ilst))
	return atom("moov", slices.Concat(atom("mvhd", make([]byte, 100)), atom("udta", meta)))
}

var mp4Ftyp = atom("ftyp", []byte("M4A \x00\x00\x02\x00M4A mp42isom"))

// mp4File renders an M4A file with items as its tags, laid out as iTunes
// does: ftyp, moov, mdat.
func mp4File(items ...[]byte) []byte {
	return slices.Concat(mp4Ftyp, mp4Moov(items...), atom("mdat", []byte("audio data")))
}

// mp4Basics are the tags of a file that qualifies for the library.
func mp4Basics() [][]byte {
	return [][]byte{
		textItem("\xa9nam", "Title"),
		textItem("\xa9ART", "Artist"),
		textItem("\xa9alb", "Album"),
		freeformItem("MusicBrainz Album Id", "album-id"),
	}
}

// checkMP4Basics checks that the tags of mp4Basics were read.
func checkMP4Basics(t *testing.T, name string, m Metadata) {
	t.Helper()
	if m.Title() != "Title" || m.Artist() != "Artist" || m.Album() != "Album" || m.Raw()["MusicBrainz Album Id"] != "album-id" {
		t.Errorf("%s: read %q / %q / %q / %v, want Title / Artist / Album / album-id",
			name, m.Title(), m.Artist(), m.Album(), m.Raw()["MusicBrainz Album Id"])
	}
}

// flacBlock renders a FLAC metadata block.
func flacBlock(typ byte, last bool, body []byte) []byte {
	if last {
		typ |= 0x80
	}
	n := len(body)
	return slices.Concat([]byte{typ, byte(n >> 16), byte(n >> 8), byte(n)}, body)
}

// flacFile renders a FLAC file: the marker, a STREAMINFO block, blocks (the
// last of which must be flagged as last) and something for audio.
func flacFile(blocks ...[]byte) []byte {
	return slices.Concat([]byte("fLaC"), flacBlock(0, false, make([]byte, 34)), slices.Concat(blocks...), []byte("flac audio frames"))
}

// flacPicture renders the body of a FLAC PICTURE block, which is also what
// a METADATA_BLOCK_PICTURE comment holds in base64. dataLen is the length
// written for data, which the damaged cases set wrong.
func flacPicture(typ uint32, mime, desc string, dataLen uint32, data []byte) []byte {
	b := binary.BigEndian.AppendUint32(nil, typ)
	b = binary.BigEndian.AppendUint32(b, uint32(len(mime)))
	b = append(b, mime...)
	b = binary.BigEndian.AppendUint32(b, uint32(len(desc)))
	b = append(b, desc...)
	b = append(b, make([]byte, 16)...) // width, height, depth, colours
	b = binary.BigEndian.AppendUint32(b, dataLen)
	return append(b, data...)
}

// oggPage renders one Ogg page holding whole packets, with its checksum.
func oggPage(flags byte, serial, sequence uint32, packets ...[]byte) []byte {
	var table, data []byte
	for _, p := range packets {
		for n := len(p); ; n -= 255 {
			if n < 255 {
				table = append(table, byte(n))
				break
			}
			table = append(table, 255)
		}
		data = append(data, p...)
	}
	b := []byte("OggS")
	b = append(b, 0, flags)
	b = binary.LittleEndian.AppendUint64(b, 0) // granule position
	b = binary.LittleEndian.AppendUint32(b, serial)
	b = binary.LittleEndian.AppendUint32(b, sequence)
	b = append(b, 0, 0, 0, 0) // the checksum, over the page with this zero
	b = append(b, byte(len(table)))
	b = slices.Concat(b, table, data)
	binary.LittleEndian.PutUint32(b[22:], oggCRCUpdate(0, oggCRC32Poly04c11db7, b))
	return b
}

// oggVorbisFile renders an Ogg Vorbis stream with the comment packet given
// and packets of audio.
func oggVorbisFile(comment []byte, audio ...[]byte) []byte {
	b := oggPage(2, 1, 0, []byte("\x01vorbis identification"))
	b = append(b, oggPage(0, 1, 1, append([]byte("\x03vorbis"), comment...), []byte("\x05vorbis setup"))...)
	return append(b, oggPage(4, 1, 2, audio...)...)
}

// sparseFile is a file of size bytes that starts with head and is zeros
// after it, without holding the zeros: for the tests of what a damaged
// length makes the reader load.
type sparseFile struct {
	head []byte
	size int64
	pos  int64
}

func (f *sparseFile) Read(p []byte) (int, error) {
	if f.pos >= f.size {
		return 0, io.EOF
	}
	n := int(min(int64(len(p)), f.size-f.pos))
	clear(p[:n])
	if f.pos < int64(len(f.head)) {
		copy(p[:n], f.head[f.pos:])
	}
	f.pos += int64(n)
	return n, nil
}

func (f *sparseFile) Seek(offset int64, whence int) (int64, error) {
	switch whence {
	case io.SeekCurrent:
		offset += f.pos
	case io.SeekEnd:
		offset += f.size
	}
	if offset < 0 {
		return 0, io.ErrUnexpectedEOF
	}
	f.pos = offset
	return offset, nil
}

// allocated runs f and returns the bytes it allocated.
func allocated(f func()) uint64 {
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	f()
	runtime.ReadMemStats(&after)
	return after.TotalAlloc - before.TotalAlloc
}

const mib = 1 << 20
