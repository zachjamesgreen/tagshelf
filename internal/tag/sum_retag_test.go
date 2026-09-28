package tag

import (
	"bytes"
	"crypto/sha1"
	"encoding/binary"
	"fmt"
	"os"
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
// than "M4A ", which used to fall through to hashing the whole file, so a
// retag changed their Sum.
func TestSumSurvivesRetag(t *testing.T) {
	for _, ext := range []string{".ogg", ".m4a", ".m4b"} {
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

// TestSumUnchangedFormats pins the sums of the formats whose hashing the
// OGG and MP4 brand change must leave alone.
func TestSumUnchangedFormats(t *testing.T) {
	for name, want := range map[string]string{
		"untagged.flac": "5d27aa26206123cbfd8c1c605eebb6e52abc7dcc",
		"tagged.flac":   "5d27aa26206123cbfd8c1c605eebb6e52abc7dcc",
		"untagged.mp3":  "82b9891a4ee4b0f4ade1cfaa09a8997a77761d9e",
		"tagged.mp3":    "8488ce22c145eaecb4448c04f90b4769a873947c",
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
