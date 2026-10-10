package tag

import (
	"bytes"
	"encoding/binary"
	"slices"
	"testing"

	"tagshelf/internal/fixtures"
)

// wavChunk renders a RIFF chunk with its pad byte.
func wavChunk(id string, data []byte) []byte {
	b := slices.Concat([]byte(id), binary.LittleEndian.AppendUint32(nil, uint32(len(data))), data)
	if len(data)%2 == 1 {
		b = append(b, 0)
	}
	return b
}

// wavFile renders a WAVE file from its chunks.
func wavFile(chunks ...[]byte) []byte {
	body := slices.Concat(append([][]byte{[]byte("WAVE")}, chunks...)...)
	return slices.Concat([]byte("RIFF"), binary.LittleEndian.AppendUint32(nil, uint32(len(body))), body)
}

// infoList renders a LIST/INFO chunk from ID, value pairs.
func infoList(kv ...string) []byte {
	b := []byte("INFO")
	for i := 0; i+1 < len(kv); i += 2 {
		b = append(b, wavChunk(kv[i], append([]byte(kv[i+1]), 0))...)
	}
	return wavChunk("LIST", b)
}

var wavFmt = wavChunk("fmt ", []byte{1, 0, 1, 0, 0x40, 0x1f, 0, 0, 0x80, 0x3e, 0, 0, 2, 0, 16, 0})

func TestReadWAVFixture(t *testing.T) {
	m := readFromBytes(t, "tagged.wav", fixtureBytes(t, "tagged.wav"))
	checkFixtureValues(t, "tagged.wav", m)
	if m.Format() != ID3v2_4 || m.FileType() != WAV {
		t.Errorf("format %q, file type %q, want %q, %q", m.Format(), m.FileType(), ID3v2_4, WAV)
	}

	// The untagged fixtures have neither an ID3 chunk nor a LIST/INFO one.
	for _, name := range []string{"untagged.wav", fixtures.UntaggedOddWAV} {
		if _, err := ReadFrom(bytes.NewReader(fixtureBytes(t, name))); err != ErrNoTagsFound {
			t.Errorf("ReadFrom(%s) = %v, want ErrNoTagsFound", name, err)
		}
	}
}

// TestReadWAVUnreadableID3Chunk is the regression test for an ID3 chunk that
// cannot be read, which used to make the whole file unreadable even when its
// LIST/INFO chunk had every field. Without that chunk it is still an error.
func TestReadWAVUnreadableID3Chunk(t *testing.T) {
	bad := wavChunk("id3 ", []byte("ID3\x09\x00\x00\x00\x00\x00\x00"))
	info := infoList("INAM", "Info Title", "IART", "Info Artist")
	for name, b := range map[string][]byte{
		"ID3 before INFO": wavFile(wavFmt, wavChunk("data", []byte{0, 0}), bad, info),
		"INFO before ID3": wavFile(wavFmt, info, wavChunk("data", []byte{0, 0}), bad),
	} {
		m := readFromBytes(t, name, b)
		if m.Title() != "Info Title" || m.Artist() != "Info Artist" || m.Format() != INFO {
			t.Errorf("%s: read %q / %q as %s, want the INFO values", name, m.Title(), m.Artist(), m.Format())
		}
	}

	_, err := ReadFrom(bytes.NewReader(wavFile(wavFmt, wavChunk("data", []byte{0, 0}), bad)))
	if err == nil || err == ErrNoTagsFound {
		t.Errorf("unreadable ID3 chunk alone: error = %v, want the chunk's error", err)
	}
}

// TestReadWAVChunkNames reads the "ID3 " chunk TagLib writes as well as the
// "id3 " one mutagen writes, after an odd-sized data chunk and its pad byte.
func TestReadWAVChunkNames(t *testing.T) {
	frame := slices.Concat([]byte("TIT2"), []byte{0, 0, 0, 6, 0, 0, 3}, []byte("Title"))
	id3 := slices.Concat(id3v2TagHeader(4, 0, len(frame)), frame)
	for _, id := range []string{"ID3 ", "id3 "} {
		b := wavFile(wavFmt, wavChunk("data", []byte{1, 2, 3}), wavChunk(id, id3))
		if m := readFromBytes(t, id, b); m.Title() != "Title" {
			t.Errorf("%q chunk: title %q, want Title", id, m.Title())
		}
	}
}

// TestReadWAVInfo reads LIST/INFO field by field where the ID3 chunk has no
// value, and on its own when there is no ID3 chunk.
func TestReadWAVInfo(t *testing.T) {
	info := infoList("INAM", "Info Title", "IART", "Info Artist", "IPRD", "Info Album", "ICRD", "1999-01-02",
		"ITRK", "4/9", "IGNR", "Info Genre")

	m := readFromBytes(t, "INFO only", wavFile(wavFmt, info, wavChunk("data", []byte{0, 0})))
	if m.Title() != "Info Title" || m.Artist() != "Info Artist" || m.Album() != "Info Album" || m.Year() != 1999 ||
		m.Genre() != "Info Genre" {
		t.Errorf("INFO only: got %q / %q / %q / %d / %q", m.Title(), m.Artist(), m.Album(), m.Year(), m.Genre())
	}
	if n, total := m.Track(); n != 4 || total != 9 {
		t.Errorf("INFO only: track %d/%d, want 4/9", n, total)
	}
	if m.Format() != INFO || m.FileType() != WAV {
		t.Errorf("INFO only: format %q, file type %q, want %q, %q", m.Format(), m.FileType(), INFO, WAV)
	}
	if m.Raw()["INAM"] != "Info Title" {
		t.Errorf("INFO only: Raw()[INAM] = %v", m.Raw()["INAM"])
	}

	frame := slices.Concat([]byte("TIT2"), []byte{0, 0, 0, 10, 0, 0, 3}, []byte("ID3 Title"))
	id3 := slices.Concat(id3v2TagHeader(4, 0, len(frame)), frame)
	m = readFromBytes(t, "ID3 and INFO", wavFile(wavFmt, info, wavChunk("data", []byte{0, 0}), wavChunk("id3 ", id3)))
	if m.Title() != "ID3 Title" || m.Artist() != "Info Artist" || m.Year() != 1999 {
		t.Errorf("ID3 and INFO: got %q / %q / %d, want ID3 Title / Info Artist / 1999", m.Title(), m.Artist(), m.Year())
	}
	if m.Format() != ID3v2_4 {
		t.Errorf("ID3 and INFO: format %q, want %q", m.Format(), ID3v2_4)
	}

	// Not UTF-8: Windows-1252, as Picard writes it.
	m = readFromBytes(t, "cp1252", wavFile(wavFmt, infoList("INAM", "Caf\xe9 \x93Q\x94"), wavChunk("data", nil)))
	if want := "Café “Q”"; m.Title() != want {
		t.Errorf("cp1252: title %q, want %q", m.Title(), want)
	}
}

func TestReadWAVNoTags(t *testing.T) {
	b := wavFile(wavFmt, wavChunk("data", []byte{0, 0}))
	if _, err := ReadFrom(bytes.NewReader(b)); err != ErrNoTagsFound {
		t.Errorf("error = %v, want ErrNoTagsFound", err)
	}
	// A chunk size running past the end of the file stops the walk.
	binary.LittleEndian.PutUint32(b[len(b)-6:], 0xffffffff)
	if _, err := ReadFrom(bytes.NewReader(b)); err != ErrNoTagsFound {
		t.Errorf("oversized chunk: error = %v, want ErrNoTagsFound", err)
	}
}
