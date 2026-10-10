package tag

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"unicode/utf8"

	"github.com/zachjamesgreen/tagshelf/internal/fixtures"
)

// dataIssue73 is the input of upstream's issue 73: an ID3v2.3 tag whose
// extended header runs far past the file.
var dataIssue73 = []byte{0x49, 0x44, 0x33, 0x03, 0x00, 0x40, 0x00, 0x00, 0x00, 0x0E, 0xDB, 0xDB, 0xDB, 0xDB, 0xDB, 0xDB,
	0xDB, 0x06, 0xFF, 0x54, 0x58, 0x58, 0x00}

// fuzzSeeds are the inputs FuzzReadFrom starts from, and the ones a plain
// "go test" runs it on: every fixture and sample, and damaged and crafted
// files of each format.
func fuzzSeeds(t testing.TB) map[string][]byte {
	seeds := map[string][]byte{"issue 73": dataIssue73, "empty": nil, "short": []byte("ID3"), "eleven bytes": []byte("ID3\x04\x00\x00\x00\x00\x00\x00\x00")}

	files, err := filepath.Glob(filepath.Join(filepath.Dir(fixtures.Path("tagged.mp3")), "*.*"))
	if err != nil {
		t.Fatal(err)
	}
	samples, err := filepath.Glob("testdata/*/*.*")
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range append(files, samples...) {
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		// The start of a large sample is enough to seed from.
		seeds[path] = b[:min(len(b), 128<<10)]
		// Cut short in the tags, and with a byte of them changed.
		seeds[path+" cut short"] = b[:min(len(b), 200)]
		if len(b) > 64 {
			damaged := slices.Clone(seeds[path])
			damaged[40] ^= 0x80
			seeds[path+" damaged"] = damaged
		}
	}

	picture := flacPicture(3, "image/png", "", 0xFFFFFFFF, pngData)
	longAtom := binary.BigEndian.AppendUint32(nil, 1)
	longAtom = append(longAtom, "mdat"...)
	longAtom = binary.BigEndian.AppendUint64(longAtom, 1<<40)
	crafted := map[string][]byte{
		"ID3v2.2":                id3Tag(2, 0, id3Frame(2, "TT2", latin1("Title")), id3Frame(2, "PIC", []byte{0, 'P', 'N', 'G', 3, 0, 1, 2})),
		"ID3v2.3 UTF-16":         id3Tag(3, 0, id3Frame(3, "TIT2", utf16Text("Title")), id3Frame(3, "COMM", []byte{1, 'e', 'n'})),
		"ID3v2.3 unsynchronised": id3Tag(3, 0x80, unsynchronise(id3Frame(3, "APIC", apicBody("image/jpeg", 3, "", jpegData)))),
		"ID3v2.4 flags":          id3Tag(4, 0x80, id3FrameFlags(4, "TIT2", 0x0F, []byte{0, 0, 0, 5, 3, 'T'}), id3FrameFlags(4, "APIC", 0x03, []byte{0, 0})),
		"ID3v2.4 extended":       id3Tag(4, 0x40, []byte{0, 0, 0, 6, 1, 0}, id3Frame(4, "TXXX", txxxBody("a", "b"))),
		"ID3v2 before FLAC":      slices.Concat(id3Tag(3, 0), flacFile(flacBlock(4, true, vorbisComment("v", "TITLE=T")))),
		"ID3v1":                  slices.Concat(mp3Audio, id3v1Tag("T\xf3", "A", "B", "1999", "C", 1, 2)),
		"FLAC picture":           flacFile(flacBlock(4, false, vorbisComment("v", "DATE=12/05/2001", "no equals")), flacBlock(6, true, picture)),
		"OGG picture":            oggVorbisFile(vorbisComment("v", "METADATA_BLOCK_PICTURE="+base64.StdEncoding.EncodeToString(picture)), []byte("audio")),
		"Opus":                   opusFile(vorbisComment("v", vorbisBasics...), []byte("audio")),
		"MP4":                    mp4File(append(mp4Basics(), atom("\xa9nam", dataAtom(atomClassInteger, []byte{1})), freeformItem("trkn_count", "x"), atom("covr", dataAtom(0, jpegData)))...),
		"MP4 containers":         slices.Concat(mp4Ftyp, bytes.Repeat(atom("moov", nil), 1000), longAtom),
		"WAV":                    wavFile(wavFmt, wavChunk("data", []byte{1, 2, 3}), wavChunk("id3 ", []byte("ID3\x09")), infoList("INAM", "T")),
		"APEv2":                  slices.Concat(mp3Audio, apeTag(apeItem{key: "Title", value: "T"}), id3v1("T")),
		"ASF":                    asfFile([]byte("packets"), asfCD("t", "a")),
	}
	for name, b := range crafted {
		seeds[name] = b
	}
	return seeds
}

// FuzzReadFrom feeds ReadFrom and Sum damaged and crafted files. Whatever
// the input, neither may panic, the getters of what ReadFrom returns may not
// panic, the text of an ID3v1 tag is valid UTF-8, and a Sum is 40 hex digits.
// A plain "go test" runs it on the seeds; "go test -fuzz FuzzReadFrom" goes
// on from them.
func FuzzReadFrom(f *testing.F) {
	for _, b := range fuzzSeeds(f) {
		f.Add(b)
	}
	f.Fuzz(func(t *testing.T, in []byte) {
		r := bytes.NewReader(in)
		Identify(r)

		r.Seek(0, 0)
		m, err := ReadFrom(r)
		if err == nil {
			if m == nil {
				t.Fatal("ReadFrom returned neither metadata nor an error")
			}
			allGetters(m)
			if m.Format() == ID3v1 {
				for _, s := range []string{m.Title(), m.Artist(), m.Album(), m.Comment()} {
					if !utf8.ValidString(s) {
						t.Errorf("ID3v1 text %q is not valid UTF-8", s)
					}
				}
			}
		}

		r.Seek(0, 0)
		if s, err := Sum(r); err == nil && len(s) != 40 {
			t.Errorf("Sum = %q, want 40 hex digits", s)
		}
	})
}

// TestIssue73 is upstream's test of its issue 73, which only checked that
// nothing panics: the extended header that runs past the file is an error.
func TestIssue73(t *testing.T) {
	if _, err := ReadFrom(bytes.NewReader(dataIssue73)); err == nil {
		t.Error("ReadFrom: no error for an extended header that runs past the file")
	}
}
