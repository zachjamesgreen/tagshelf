package tag

import (
	"bytes"
	"crypto/sha1"
	"encoding/binary"
	"fmt"
	"os"
	"slices"
	"testing"

	"tagshelf/internal/fixtures"
)

func sumFile(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(fixtures.Path(name))
	if err != nil {
		t.Fatal(err)
	}
	return sumBytes(t, name, b)
}

func sumBytes(t *testing.T, name string, b []byte) string {
	t.Helper()
	s, err := Sum(bytes.NewReader(b))
	if err != nil {
		t.Fatalf("Sum(%s): %v", name, err)
	}
	return s
}

// TestSumSurvivesRetag is the regression test for OGG and MP4 brands other
// than "M4A ", which used to fall through to hashing the whole file, and for
// MP3, which used to hash its leading ID3v2 tag, so a retag changed their Sum.
func TestSumSurvivesRetag(t *testing.T) {
	for _, ext := range []string{".mp3", ".ogg", ".m4a", ".m4b"} {
		untagged, tagged := "untagged"+ext, "tagged"+ext
		if u, g := sumFile(t, untagged), sumFile(t, tagged); u != g {
			t.Errorf("Sum(%s) = %s, Sum(%s) = %s, want equal", untagged, u, tagged, g)
		}
	}

	// The same Vorbis audio with a comment large enough to span pages, so
	// every audio page has a different sequence number and CRC.
	u, g := sumTestdata(t, "with_tags/sample.ogg"), sumTestdata(t, "with_tags/sample.multipage.ogg")
	if u != g {
		t.Errorf("Sum(sample.ogg) = %s, Sum(sample.multipage.ogg) = %s, want equal", u, g)
	}
}

func sumTestdata(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return sumBytes(t, name, b)
}

// TestSumSurvivesRetagEveryMP4Brand rewrites the major brand of the M4A
// fixtures, since the hash must not depend on it.
func TestSumSurvivesRetagEveryMP4Brand(t *testing.T) {
	for _, brand := range []string{"M4B ", "M4P ", "mp42", "isom", "dash"} {
		var sums [2]string
		for i, name := range []string{"untagged.m4a", "tagged.m4a"} {
			b, err := os.ReadFile(fixtures.Path(name))
			if err != nil {
				t.Fatal(err)
			}
			if string(b[4:12]) != "ftypM4A " {
				t.Fatalf("%s: major brand is not at bytes 8-12", name)
			}
			copy(b[8:12], brand)
			sums[i] = sumBytes(t, brand+" "+name, b)
		}
		if sums[0] != sums[1] {
			t.Errorf("brand %q: untagged Sum %s, tagged Sum %s, want equal", brand, sums[0], sums[1])
		}
	}
}

// TestSumUnchangedFormats pins the sums of FLAC, MP3 and M4A, so a change to
// another format's hashing cannot move them. The MP3 sum is that of the audio
// frames alone.
func TestSumUnchangedFormats(t *testing.T) {
	for name, want := range map[string]string{
		"untagged.flac": "5d27aa26206123cbfd8c1c605eebb6e52abc7dcc",
		"tagged.flac":   "5d27aa26206123cbfd8c1c605eebb6e52abc7dcc",
		"untagged.mp3":  "01d5feccb660677b896110f590ead8e40cab57fc",
		"tagged.mp3":    "01d5feccb660677b896110f590ead8e40cab57fc",
		"untagged.m4a":  "010ad04dd1cbb749db1986bbd9357e4e7323a7f6",
		"tagged.m4a":    "010ad04dd1cbb749db1986bbd9357e4e7323a7f6",
	} {
		if got := sumFile(t, name); got != want {
			t.Errorf("Sum(%s) = %s, want %s", name, got, want)
		}
	}
}

// TestSumAtomsMdatSizes covers an mdat with a 64-bit size (size field 1, as
// in MP4 files over 4 GiB) and one running to the end of the file (size field
// 0), which M4B audiobooks can have now that every brand takes this path.
func TestSumAtomsMdatSizes(t *testing.T) {
	audio := []byte("audio data")
	want := fmt.Sprintf("%x", sha1.Sum(audio))
	ftyp := atom("ftyp", []byte("M4B \x00\x00\x02\x00"))

	large := binary.BigEndian.AppendUint32(nil, 1)
	large = append(large, "mdat"...)
	large = binary.BigEndian.AppendUint64(large, uint64(16+len(audio)))
	large = append(large, audio...)

	toEOF := binary.BigEndian.AppendUint32(nil, 0)
	toEOF = append(toEOF, "mdat"...)
	toEOF = append(toEOF, audio...)

	for name, mdat := range map[string][]byte{"64-bit size": large, "size to EOF": toEOF} {
		b := append(append([]byte{}, ftyp...), mdat...)
		if got := sumBytes(t, name, b); got != want {
			t.Errorf("%s: Sum = %s, want %s", name, got, want)
		}
	}
}

func atom(name string, body []byte) []byte {
	b := binary.BigEndian.AppendUint32(nil, uint32(8+len(body)))
	return append(append(b, name...), body...)
}

// TestSumMP3SkipsID3v1 appends an ID3v1 tag, as tagshelf's MP3 writes do, and
// replaces it, since the old Sum always dropped the last 128 bytes whether or
// not they were a tag.
func TestSumMP3SkipsID3v1(t *testing.T) {
	for _, name := range []string{"untagged.mp3", "tagged.mp3"} {
		b, err := os.ReadFile(fixtures.Path(name))
		if err != nil {
			t.Fatal(err)
		}
		want := sumBytes(t, name, b)

		withV1 := append(append([]byte{}, b...), id3v1("First")...)
		if got := sumBytes(t, name+" + ID3v1", withV1); got != want {
			t.Errorf("%s + ID3v1: Sum = %s, want %s", name, got, want)
		}
		copy(withV1[len(b):], id3v1("Second"))
		if got := sumBytes(t, name+" + edited ID3v1", withV1); got != want {
			t.Errorf("%s + edited ID3v1: Sum = %s, want %s", name, got, want)
		}
	}
}

// TestSumMP3ID3v2Layouts hashes synthetic MP3s whose ID3v2 tag has a v2.4
// footer or an extended header, which the size must cover in full.
func TestSumMP3ID3v2Layouts(t *testing.T) {
	audio := append([]byte{0xff, 0xfb, 0x90, 0x00}, bytes.Repeat([]byte("frame"), 40)...)
	want := fmt.Sprintf("%x", sha1.Sum(audio))
	frame := slices.Concat([]byte("TIT2"), []byte{0, 0, 0, 6, 0, 0, 3}, []byte("Title"))

	// A v2.4 footer is the header with "3DI" in place of "ID3"; it is not
	// counted in the size.
	footer := id3v2TagHeader(4, 0x10, len(frame))
	copy(footer, "3DI")
	withFooter := slices.Concat(id3v2TagHeader(4, 0x10, len(frame)), frame, footer)

	// A v2.3 extended header (flag 0x40) is counted in the size.
	ext := []byte{0, 0, 0, 6, 0, 0, 0, 0, 0, 0}
	withExt := slices.Concat(id3v2TagHeader(3, 0x40, len(ext)+len(frame)), ext, frame)

	for name, tag := range map[string][]byte{"v2.4 footer": withFooter, "v2.3 extended header": withExt} {
		b := slices.Concat(tag, audio)
		if got := sumBytes(t, name, b); got != want {
			t.Errorf("%s: Sum = %s, want %s", name, got, want)
		}
	}

	if got := sumBytes(t, "no tag", audio); got != want {
		t.Errorf("no tag: Sum = %s, want %s", got, want)
	}
}

func id3v2TagHeader(version, flags byte, size int) []byte {
	return []byte{'I', 'D', '3', version, 0, flags,
		byte(size >> 21 & 0x7f), byte(size >> 14 & 0x7f), byte(size >> 7 & 0x7f), byte(size & 0x7f)}
}

func id3v1(title string) []byte {
	b := make([]byte, 128)
	copy(b, "TAG")
	copy(b[3:33], title)
	return b
}

// TestSumSurvivesRetagWAVAndAPEv2 is the regression test for WAV, APE, MPC
// and .aac, whose Sum used to hash their tags.
func TestSumSurvivesRetagWAVAndAPEv2(t *testing.T) {
	for _, pair := range [][2]string{
		{"untagged.wav", "tagged.wav"},
		{"untagged.ape", "tagged.ape"},
		{"untagged.mpc", "tagged.mpc"},
		{"untagged.aac", "tagged.aac"},
		{"untagged.aac", fixtures.TaggedID3v2AAC},
	} {
		if u, g := sumFile(t, pair[0]), sumFile(t, pair[1]); u != g {
			t.Errorf("Sum(%s) = %s, Sum(%s) = %s, want equal", pair[0], u, pair[1], g)
		}
	}
}

// TestSumWAVDataChunk hashes the data chunk payload alone, without the pad
// byte after an odd size, wherever the tag chunks sit: ffmpeg puts LIST/INFO
// before data, go-taglib drops it and appends "ID3 " and LIST/INFO.
func TestSumWAVDataChunk(t *testing.T) {
	frame := slices.Concat([]byte("TIT2"), []byte{0, 0, 0, 6, 0, 0, 3}, []byte("Title"))
	id3 := slices.Concat(id3v2TagHeader(4, 0, len(frame)), frame)
	for _, data := range [][]byte{{1, 2, 3, 4}, {1, 2, 3}} {
		want := fmt.Sprintf("%x", sha1.Sum(data))
		for name, b := range map[string][]byte{
			"plain":            wavFile(wavFmt, wavChunk("data", data)),
			"INFO before data": wavFile(wavFmt, infoList("ISFT", "Lavf"), wavChunk("data", data)),
			"ID3 and INFO after data": wavFile(wavFmt, wavChunk("data", data), wavChunk("ID3 ", id3),
				infoList("INAM", "Title")),
		} {
			if got := sumBytes(t, name, b); got != want {
				t.Errorf("%d-byte data, %s: Sum = %s, want %s", len(data), name, got, want)
			}
		}
	}

	// The odd fixture: the pad byte is not audio.
	b := fixtureBytes(t, fixtures.UntaggedOddWAV)
	i := bytes.Index(b, []byte("data"))
	size := int(binary.LittleEndian.Uint32(b[i+4:]))
	want := fmt.Sprintf("%x", sha1.Sum(b[i+8:i+8+size]))
	if got := sumBytes(t, fixtures.UntaggedOddWAV, b); got != want {
		t.Errorf("Sum(%s) = %s, want %s", fixtures.UntaggedOddWAV, got, want)
	}
	withID3 := wavFile(wavFmt, wavChunk("data", b[i+8:i+8+size]), wavChunk("id3 ", id3))
	if got := sumBytes(t, "odd data + id3", withID3); got != want {
		t.Errorf("odd data + id3: Sum = %s, want %s", got, want)
	}

	// A streamed WAV's data size is unknown: hash to the end of the file.
	streamed := wavFile(wavFmt, wavChunk("data", []byte{1, 2, 3, 4}))
	binary.LittleEndian.PutUint32(streamed[len(streamed)-8:], 0xffffffff)
	if got, want := sumBytes(t, "streamed", streamed), fmt.Sprintf("%x", sha1.Sum([]byte{1, 2, 3, 4})); got != want {
		t.Errorf("streamed: Sum = %s, want %s", got, want)
	}
}

// TestSumAPEv2Layouts hashes APE, MPC and .aac audio between a leading ID3v2
// tag and the trailing tags, in every order taggers leave them: ID3v1 after
// APEv2 (TagLib), before it (mutagen), and both (mutagen, then TagLib).
func TestSumAPEv2Layouts(t *testing.T) {
	frame := slices.Concat([]byte("TIT2"), []byte{0, 0, 0, 6, 0, 0, 3}, []byte("Title"))
	id3 := slices.Concat(id3v2TagHeader(4, 0, len(frame)), frame)
	footer := id3v2TagHeader(4, 0x10, len(frame))
	copy(footer, "3DI")
	id3Footer := slices.Concat(id3v2TagHeader(4, 0x10, len(frame)), frame, footer)
	ape := apeTag(apeItem{key: "Title", value: "T"}, apeItem{key: "Artist", value: "A"})

	for _, name := range []string{"untagged.ape", "untagged.mpc", "untagged.aac", fixtures.UntaggedSV8MPC} {
		audio := fixtureBytes(t, name)
		want := fmt.Sprintf("%x", sha1.Sum(audio))
		for layout, b := range map[string][]byte{
			"none":                  audio,
			"APE":                   slices.Concat(audio, ape),
			"APE TAG":               slices.Concat(audio, ape, id3v1("V1")),
			"TAG APE":               slices.Concat(audio, id3v1("V1"), ape),
			"TAG APE TAG":           slices.Concat(audio, id3v1("V1"), ape, id3v1("V1")),
			"ID3v2 APE TAG":         slices.Concat(id3, audio, ape, id3v1("V1")),
			"ID3v2 with footer APE": slices.Concat(id3Footer, audio, ape),
		} {
			if got := sumBytes(t, name+" "+layout, b); got != want {
				t.Errorf("%s, %s: Sum = %s, want %s", name, layout, got, want)
			}
		}
	}

	// An MP3 carrying an APEv2 tag hashes its frames alone too.
	mp3 := fixtureBytes(t, "untagged.mp3")
	want := sumBytes(t, "untagged.mp3", mp3)
	if got := sumBytes(t, "MP3 + APE + TAG", slices.Concat(mp3, ape, id3v1("V1"))); got != want {
		t.Errorf("MP3 + APE + TAG: Sum = %s, want %s", got, want)
	}
}
