package tag

import (
	"bytes"
	"crypto/sha1"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"slices"
	"testing"

	"tagshelf/internal/fixtures"
)

func sha1Hex(b ...[]byte) string {
	return fmt.Sprintf("%x", sha1.Sum(slices.Concat(b...)))
}

// TestSumAtomsEveryMdat is the regression test for the MP4 hash, which
// covered the first mdat atom only. A fragmented MP4 has one mdat for each
// fragment, so two files that start alike, such as two tracks that open with
// the same encoded silence, got the same hash and formed an exact duplicate
// group.
func TestSumAtomsEveryMdat(t *testing.T) {
	ftyp := atom("ftyp", []byte("iso5\x00\x00\x02\x00iso5iso6mp41"))
	moov := mp4Moov(mp4Basics()...)
	moof := atom("moof", make([]byte, 40))
	fragmented := func(moov []byte, fragments ...string) []byte {
		b := slices.Concat(ftyp, moov)
		for _, f := range fragments {
			b = slices.Concat(b, moof, atom("mdat", []byte(f)))
		}
		return append(b, atom("mfra", make([]byte, 24))...)
	}

	one := fragmented(moov, "silence", "first song")
	if got, want := sumBytes(t, "fragmented", one), sha1Hex([]byte("silence"), []byte("first song")); got != want {
		t.Errorf("Sum of a fragmented file = %s, want %s, the SHA-1 of every mdat", got, want)
	}
	two := fragmented(moov, "silence", "second song")
	if sumBytes(t, "fragmented", one) == sumBytes(t, "other fragmented", two) {
		t.Error("two files whose first fragment is the same have the same Sum")
	}
	retagged := fragmented(mp4Moov(textItem("\xa9nam", "Another title")), "silence", "first song")
	if got, want := sumBytes(t, "retagged", retagged), sumBytes(t, "fragmented", one); got != want {
		t.Errorf("Sum after a retag = %s, want %s", got, want)
	}

	// A file with one mdat hashes as it always did, whatever follows the
	// mdat: the tags, nothing, or bytes that are not atoms.
	want := sha1Hex([]byte("audio data"))
	mdat := atom("mdat", []byte("audio data"))
	for name, file := range map[string][]byte{
		"moov after mdat": slices.Concat(ftyp, mdat, moov),
		"mdat last":       slices.Concat(ftyp, moov, mdat),
		"junk after mdat": slices.Concat(ftyp, moov, mdat, []byte("\x00\x00\x00\x03junk")),
		"cut short after": slices.Concat(ftyp, mdat, moov[:len(moov)-10]),
		"ID3v1 after it":  slices.Concat(ftyp, moov, mdat, id3v1("Title")),
	} {
		if got := sumBytes(t, name, file); got != want {
			t.Errorf("%s: Sum = %s, want %s", name, got, want)
		}
	}

	// An mdat cut short, and a file with none, are still errors.
	for name, file := range map[string][]byte{
		"mdat cut short": slices.Concat(ftyp, moov, mdat[:len(mdat)-3]),
		"no mdat":        slices.Concat(ftyp, moov),
	} {
		if s, err := Sum(bytes.NewReader(file)); err == nil {
			t.Errorf("%s: Sum = %s, want an error", name, s)
		}
	}
}

// failingReader fails every read with err.
type failingReader struct{ err error }

func (r failingReader) Read([]byte) (int, error)       { return 0, r.err }
func (r failingReader) Seek(int64, int) (int64, error) { return 0, nil }

// TestSumAllReturnsItsError is the regression test for SumAll, which
// returned "" and no error when reading failed.
func TestSumAllReturnsItsError(t *testing.T) {
	failed := errors.New("read failed")
	if s, err := SumAll(failingReader{failed}); err != failed || s != "" {
		t.Errorf("SumAll = %q, %v, want the read error", s, err)
	}
	if got, want := sumAllBytes(t, []byte("abc")), sha1Hex([]byte("abc")); got != want {
		t.Errorf("SumAll = %s, want %s", got, want)
	}
	// Sum of a file of no known format hashes all of it.
	if got, want := sumBytes(t, "unknown format", []byte("no audio format at all")), sha1Hex([]byte("no audio format at all")); got != want {
		t.Errorf("Sum of an unknown format = %s, want %s", got, want)
	}
}

func sumAllBytes(t *testing.T, b []byte) string {
	t.Helper()
	s, err := SumAll(bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// TestSumOGGPinned pins the Sum of the OGG fixture, as TestSumUnchangedFormats
// does for FLAC, MP3 and M4A: every other check of an OGG hash compares two
// results of Sum with each other, and would pass if both left the audio out.
func TestSumOGGPinned(t *testing.T) {
	const want = "70d4baff23d0dc67d1b67af4e7089e33bf19ae51"
	for _, name := range []string{"untagged.ogg", "tagged.ogg"} {
		if got := sumFile(t, name); got != want {
			t.Errorf("Sum(%s) = %s, want %s", name, got, want)
		}
	}

	// The hash is that of every packet but the comment.
	audio := [][]byte{[]byte("audio packet 1"), []byte("audio packet 2")}
	file := oggVorbisFile(vorbisComment("v", vorbisBasics...), audio...)
	want2 := sha1Hex([]byte("\x01vorbis identification"), []byte("\x05vorbis setup"), audio[0], audio[1])
	if got := sumBytes(t, "crafted OGG", file); got != want2 {
		t.Errorf("Sum of a crafted OGG = %s, want %s", got, want2)
	}
}

// TestSumDiffersWithTheAudio changes one byte of the audio of a fixture of
// each format: the Sum must change. A hash that left the audio out, or
// covered only a header every file of one encoder shares, would make every
// such pair of files an exact duplicate group.
func TestSumDiffersWithTheAudio(t *testing.T) {
	for _, name := range []string{"untagged.flac", "tagged.flac", "untagged.mp3", "tagged.mp3", "untagged.m4a", "tagged.m4a",
		"tagged.m4b", "untagged.wav", "tagged.wav", "untagged.ape", "tagged.ape", "untagged.mpc", "tagged.mpc",
		"untagged.aac", "tagged.aac", fixtures.TaggedID3v2AAC, "untagged.wma", "tagged.wma", "untagged.ogg", "tagged.ogg"} {
		b := fixtureBytes(t, name)
		sum := sumBytes(t, name, b)
		for what, at := range audioOffsets(t, name, b) {
			changed := slices.Clone(b)
			changed[at] ^= 0x01
			if bytes.HasPrefix(b, []byte("OggS")) {
				fixOggChecksums(changed)
			}
			if got := sumBytes(t, name, changed); got == sum {
				t.Errorf("%s: Sum is the same with the %s byte of the audio changed", name, what)
			}
		}
	}
}

// audioOffsets finds the first, middle and last byte of the audio of a
// fixture: the mdat of an MP4 file, the data chunk of a WAV file, the last
// page of an OGG file, the packets of a WMA file, and otherwise what lies
// between a leading ID3v2 tag (or the FLAC metadata) and the trailing tags.
func audioOffsets(t *testing.T, name string, b []byte) map[string]int {
	t.Helper()
	start, end := 0, len(b)
	switch {
	case bytes.HasPrefix(b, []byte("fLaC")):
		start = 4
		for last := false; !last; {
			last = b[start]&0x80 != 0
			start += 4 + int(b[start+1])<<16 + int(b[start+2])<<8 + int(b[start+3])
		}
	case bytes.HasPrefix(b, []byte("OggS")):
		// The body of the last page.
		start = bytes.LastIndex(b, []byte("OggS"))
		start += 27 + int(b[start+26])
	case string(b[4:8]) == "ftyp":
		start = bytes.Index(b, []byte("mdat")) + 4
		end = start - 8 + int(binary.BigEndian.Uint32(b[start-8:]))
	case isWAV(b):
		start = bytes.Index(b, []byte("data")) + 8
		end = start + int(binary.LittleEndian.Uint32(b[start-4:]))
	case isASF(b):
		start = bytes.Index(b, []byte(asfDataGUID)) + 50
		end = start - 50 + int(binary.LittleEndian.Uint64(b[start-50+16:]))
	default:
		if bytes.HasPrefix(b, []byte("ID3")) {
			start = int(id3v2TagSize(b[:10]))
		}
		tags, err := findTrailingTags(bytes.NewReader(b), int64(start), int64(len(b)))
		if err != nil {
			t.Fatal(err)
		}
		end = int(tags.audioEnd)
	}
	if start >= end || end > len(b) {
		t.Fatalf("%s: audio from %d to %d of %d bytes", name, start, end, len(b))
	}
	return map[string]int{"first": start, "middle": (start + end) / 2, "last": end - 1}
}

// fixOggChecksums rewrites the checksum of every page of an Ogg stream.
func fixOggChecksums(b []byte) {
	for i := 0; i+27 <= len(b) && string(b[i:i+4]) == "OggS"; {
		n := 27 + int(b[i+26])
		for _, s := range b[i+27 : i+n] {
			n += int(s)
		}
		page := b[i : i+n]
		clear(page[22:26])
		binary.LittleEndian.PutUint32(page[22:], oggCRCUpdate(0, oggCRC32Poly04c11db7, page))
		i += n
	}
}

// TestSumDSF is the regression test for the hash of a DSF file, which
// covered the whole file, its ID3v2 tag included, so a retag changed it. It
// is the payload of the data chunk.
func TestSumDSF(t *testing.T) {
	b, err := os.ReadFile("testdata/with_tags/sample.dsf")
	if err != nil {
		t.Fatal(err)
	}
	// The DSD chunk is 28 bytes and holds the offset of the tag; the fmt
	// chunk is 52; the data chunk's size counts its 12-byte header.
	tagAt := int(binary.LittleEndian.Uint64(b[20:28]))
	if string(b[28:32]) != "fmt " || string(b[80:84]) != "data" || string(b[tagAt:tagAt+3]) != "ID3" {
		t.Fatal("sample.dsf is not laid out as expected")
	}
	dataEnd := 80 + int(binary.LittleEndian.Uint64(b[84:92]))
	want := sha1Hex(b[92:dataEnd])
	if got := sumBytes(t, "sample.dsf", b); got != want {
		t.Errorf("Sum = %s, want %s, the SHA-1 of the data chunk", got, want)
	}

	// The same audio with its tag removed, as a tagger leaves it: the
	// pointer is 0 and the file size smaller.
	untagged := slices.Clone(b[:tagAt])
	binary.LittleEndian.PutUint64(untagged[12:20], uint64(tagAt))
	binary.LittleEndian.PutUint64(untagged[20:28], 0)
	if got := sumBytes(t, "untagged DSF", untagged); got != want {
		t.Errorf("Sum without the tag = %s, want %s", got, want)
	}
	if _, err := ReadFrom(bytes.NewReader(untagged)); err != ErrNoTagsFound {
		t.Errorf("ReadFrom of a DSF file with no ID3v2 chunk = %v, want ErrNoTagsFound", err)
	}

	changed := slices.Clone(b)
	changed[dataEnd-1] ^= 0x01
	if got := sumBytes(t, "changed DSF", changed); got == want {
		t.Error("Sum is the same with the last byte of the audio changed")
	}

	for name, bad := range map[string][]byte{
		"no data chunk":  b[:80],
		"data cut short": b[:dataEnd-1],
		"chunk size 0":   slices.Concat(b[:32], make([]byte, 8), b[40:]),
	} {
		if s, err := Sum(bytes.NewReader(bad)); err == nil {
			t.Errorf("%s: Sum = %s, want an error", name, s)
		}
	}
}

// TestSumASFDataToEndOfFile covers a Data Object of size 0, which is allowed
// while broadcasting: the packets run to the end of the file.
func TestSumASFDataToEndOfFile(t *testing.T) {
	packets := []byte("the data packets")
	b := asfFile(packets)
	i := bytes.Index(b, []byte(asfDataGUID))
	b = b[:i+50+len(packets)] // without the index after the Data Object
	binary.LittleEndian.PutUint64(b[i+16:], 0)
	if got, want := sumBytes(t, "size 0", b), sha1Hex(packets); got != want {
		t.Errorf("Sum = %s, want %s", got, want)
	}
}
