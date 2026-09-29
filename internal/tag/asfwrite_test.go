package tag

import (
	"bytes"
	"encoding/binary"
	"errors"
	"os"
	"slices"
	"strings"
	"testing"

	"tagshelf/internal/fixtures"
)

// asfFileProperties renders a File Properties Object with the given flags
// and a File Size of fileSize.
func asfFileProperties(flags uint32, fileSize uint64) []byte {
	data := make([]byte, 80)
	copy(data, "file id  file id")
	binary.LittleEndian.PutUint64(data[16:], fileSize)
	binary.LittleEndian.PutUint64(data[32:], 3) // data packets
	binary.LittleEndian.PutUint32(data[64:], flags)
	return asfObject(asfFilePropertiesGUID, data)
}

// asfIndexedFile renders an ASF file the way an encoder does: a Header Object
// holding a File Properties Object and children, the Data Object, then a
// Simple Index and an Index Object.
func asfIndexedFile(flags uint32, children ...[]byte) []byte {
	children = append([][]byte{asfFileProperties(flags, 12345)}, children...)
	b := asfFile(nil, children...)
	b = b[:binary.LittleEndian.Uint64(b[16:24])]
	packets := bytes.Repeat([]byte("packet\x00\xff"), 64)
	data := asfObject(asfDataGUID, []byte("file id  file id"), binary.LittleEndian.AppendUint64(nil, 3), []byte{1, 1}, packets)
	// Simple Index: File ID, time interval, max packet count, entries of
	// packet number and packet count.
	simple := asfObject(asfSimpleIndexGUID, []byte("file id  file id"), binary.LittleEndian.AppendUint64(nil, 10000000),
		le32(1), le32(3), le32(0), le16(1), le32(1), le16(1), le32(2), le16(1))
	// Index: entry time interval, specifier count, block count, specifier,
	// then one block of entry count, block position and offsets.
	index := asfObject(asfIndexGUID, le32(1000), le16(1), le32(1), le16(1), le16(1),
		le32(3), binary.LittleEndian.AppendUint64(nil, 0), le32(0), le32(64), le32(128))
	return slices.Concat(b, data, simple, index)
}

// asfPadding renders a Padding Object with n bytes of padding.
func asfPadding(n int) []byte {
	return asfObject(asfPaddingGUID, make([]byte, n))
}

func writeASFAttribute(t *testing.T, in []byte, name, value string) []byte {
	t.Helper()
	var out bytes.Buffer
	if err := WriteASFAttribute(&out, bytes.NewReader(in), name, value); err != nil {
		t.Fatalf("WriteASFAttribute: %v", err)
	}
	return out.Bytes()
}

// asfChildren returns the GUIDs and sizes of the Header Object's children,
// checking its size and count.
func asfChildren(t *testing.T, b []byte) (guids []string, sizes []int) {
	t.Helper()
	size := binary.LittleEndian.Uint64(b[16:24])
	count := binary.LittleEndian.Uint32(b[24:28])
	err := walkASFObjects(b[30:size], -1, func(guid string, data []byte) error {
		guids = append(guids, guid)
		sizes = append(sizes, 24+len(data))
		return nil
	})
	if err != nil {
		t.Fatalf("walking the header: %v", err)
	}
	if len(guids) != int(count) {
		t.Errorf("header count = %d, but it holds %d objects", count, len(guids))
	}
	return guids, sizes
}

// asfFileSize returns the File Properties File Size.
func asfFileSize(t *testing.T, b []byte) uint64 {
	t.Helper()
	size := binary.LittleEndian.Uint64(b[16:24])
	var fileSize uint64
	found := false
	walkASFObjects(b[30:size], -1, func(guid string, data []byte) error {
		if guid == asfFilePropertiesGUID {
			fileSize, found = binary.LittleEndian.Uint64(data[16:24]), true
		}
		return nil
	})
	if !found {
		t.Fatal("no File Properties Object")
	}
	return fileSize
}

// checkASFAudioKept checks that everything after the Header Object (the Data
// Object and the index objects) is byte for byte the same, that Sum is
// unchanged, and that File Size is the real length of the new file.
func checkASFAudioKept(t *testing.T, in, out []byte) {
	t.Helper()
	inSize, outSize := binary.LittleEndian.Uint64(in[16:24]), binary.LittleEndian.Uint64(out[16:24])
	if !bytes.Equal(in[inSize:], out[outSize:]) {
		t.Error("the Data Object or index objects changed")
	}
	if a, b := sumBytes(t, "before", in), sumBytes(t, "after", out); a != b {
		t.Errorf("Sum changed from %s to %s", a, b)
	}
	if got := asfFileSize(t, out); got != uint64(len(out)) {
		t.Errorf("File Size = %d, want the file's length %d", got, len(out))
	}
}

func asfRaw(t *testing.T, b []byte, name string) any {
	t.Helper()
	return readASF(t, b).Raw()[name]
}

// TestWriteASFAttributeCreatesECD adds the marker to a file with no Extended
// Content Description Object: one is added and the header count goes up.
func TestWriteASFAttributeCreatesECD(t *testing.T) {
	in := asfIndexedFile(0, asfCD("Title", "Artist"))
	out := writeASFAttribute(t, in, "TAGSHELF_MANUAL", "hand 2026-09-29")
	guids, _ := asfChildren(t, out)
	if !slices.Equal(guids, []string{asfFilePropertiesGUID, asfContentDescriptionGUID, asfExtendedContentDescriptionGUID}) {
		t.Errorf("header children = %q", guids)
	}
	if got := asfRaw(t, out, "TAGSHELF_MANUAL"); got != "hand 2026-09-29" {
		t.Errorf("Raw()[TAGSHELF_MANUAL] = %q", got)
	}
	if m := readASF(t, out); m.Title() != "Title" || m.Artist() != "Artist" {
		t.Errorf("Title, Artist = %q, %q", m.Title(), m.Artist())
	}
	checkASFAudioKept(t, in, out)
}

// TestWriteASFAttributeReplaces replaces a marker, matching its name
// case-insensitively, in every ECD object, and keeps the other descriptors
// in order.
func TestWriteASFAttributeReplaces(t *testing.T) {
	in := asfIndexedFile(0,
		asfECD(
			asfDescriptor("WM/AlbumTitle", asfUnicode, asfString("Album")),
			asfDescriptor("tagshelf_manual", asfUnicode, asfString("musicbrainz 2026-01-01")),
			asfDescriptor("WM/TrackNumber", asfDWORD, le32(7)),
		),
		asfHeaderExtension(asfObject(asfMetadataGUID, le16(1), asfMetadataRecord("WM/SharedUserRating", asfWORD, le16(50)))),
		asfECD(asfDescriptor("TAGSHELF_MANUAL", asfUnicode, asfString("old"))),
	)
	out := writeASFAttribute(t, in, "TAGSHELF_MANUAL", "hand 2026-09-29")
	guids, _ := asfChildren(t, out)
	if len(guids) != 4 {
		t.Errorf("header holds %d objects, want 4", len(guids))
	}
	m := readASF(t, out)
	raw := m.Raw()
	if raw["TAGSHELF_MANUAL"] != "hand 2026-09-29" {
		t.Errorf("Raw() = %q, want one TAGSHELF_MANUAL", raw)
	}
	if _, ok := raw["tagshelf_manual"]; ok {
		t.Error("the old lower-case marker is still there")
	}
	if n, _ := m.Track(); m.Album() != "Album" || n != 7 || raw["WM/SharedUserRating"] != "50" {
		t.Errorf("Album, Track, rating = %q, %d, %q", m.Album(), n, raw["WM/SharedUserRating"])
	}
	// The kept descriptors stay in order, the marker last.
	ecd := asfECD(
		asfDescriptor("WM/AlbumTitle", asfUnicode, asfString("Album")),
		asfDescriptor("WM/TrackNumber", asfDWORD, le32(7)),
		asfDescriptor("TAGSHELF_MANUAL", asfUnicode, asfString("hand 2026-09-29")),
	)
	if !bytes.Contains(out, ecd) {
		t.Error("the first ECD object is not the kept descriptors then the marker")
	}
	if !bytes.Contains(out, asfECD()) {
		t.Error("the second ECD object is not left empty")
	}
	checkASFAudioKept(t, in, out)

	// Writing the same value again changes nothing.
	if again := writeASFAttribute(t, out, "TAGSHELF_MANUAL", "hand 2026-09-29"); !bytes.Equal(again, out) {
		t.Error("a second identical write changed the file")
	}
}

// TestWriteASFAttributeUsesPadding takes the growth out of a Padding Object
// that has room, so the Data Object does not move, and gives it back when
// the header shrinks.
func TestWriteASFAttributeUsesPadding(t *testing.T) {
	in := asfIndexedFile(0, asfECD(asfDescriptor("WM/AlbumTitle", asfUnicode, asfString("Album"))), asfPadding(1000))
	out := writeASFAttribute(t, in, "TAGSHELF_MANUAL", "musicbrainz 2026-09-29")
	if len(out) != len(in) {
		t.Errorf("file grew from %d to %d bytes; the padding had room", len(in), len(out))
	}
	guids, sizes := asfChildren(t, out)
	if guids[2] != asfPaddingGUID || sizes[2] >= 1024 {
		t.Errorf("padding object %q is %d bytes, want it shrunk from 1024", guids[2], sizes[2])
	}
	checkASFAudioKept(t, in, out)

	shorter := writeASFAttribute(t, out, "TAGSHELF_MANUAL", "hand 2026-09-29")
	if len(shorter) != len(in) {
		t.Errorf("file changed from %d to %d bytes; the padding should take up the difference", len(in), len(shorter))
	}
	if got := asfRaw(t, shorter, "TAGSHELF_MANUAL"); got != "hand 2026-09-29" {
		t.Errorf("Raw()[TAGSHELF_MANUAL] = %q", got)
	}
	checkASFAudioKept(t, in, shorter)
}

// TestWriteASFAttributeSmallPadding grows the header when the padding is too
// small, leaving the padding as it is.
func TestWriteASFAttributeSmallPadding(t *testing.T) {
	in := asfIndexedFile(0, asfPadding(4))
	out := writeASFAttribute(t, in, "TAGSHELF_MANUAL", "hand 2026-09-29")
	if len(out) <= len(in) {
		t.Errorf("file is %d bytes, was %d; want it grown", len(out), len(in))
	}
	if _, sizes := asfChildren(t, out); sizes[1] != 28 {
		t.Errorf("padding object is %d bytes, want 28 as it was", sizes[1])
	}
	checkASFAudioKept(t, in, out)
}

// TestWriteASFAttributeFixtures marks the ffmpeg fixtures: every tag reads
// back as before and the audio is untouched.
func TestWriteASFAttributeFixtures(t *testing.T) {
	for _, path := range []string{fixtures.Untagged(".wma"), fixtures.Tagged(".wma")} {
		in, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		out := writeASFAttribute(t, in, "TAGSHELF_MANUAL", "musicbrainz 2026-09-29")
		rawIn, rawOut := readASF(t, in).Raw(), readASF(t, out).Raw()
		if rawOut["TAGSHELF_MANUAL"] != "musicbrainz 2026-09-29" {
			t.Errorf("%s: Raw()[TAGSHELF_MANUAL] = %q", path, rawOut["TAGSHELF_MANUAL"])
		}
		delete(rawOut, "TAGSHELF_MANUAL")
		if len(rawIn) != len(rawOut) {
			t.Errorf("%s: %d attributes before, %d after", path, len(rawIn), len(rawOut))
		}
		for k, v := range rawIn {
			if s, ok := v.(string); ok && rawOut[k] != s {
				t.Errorf("%s: %s changed from %q to %q", path, k, s, rawOut[k])
			}
		}
		checkASFAudioKept(t, in, out)
	}
}

// TestWriteASFAttributeRefuses covers the files the writer must not change:
// signed ones, broadcasts, and ones it cannot make sense of. It writes
// nothing to them.
func TestWriteASFAttributeRefuses(t *testing.T) {
	signature := asfObject(asfDigitalSignatureGUID, le32(2), le16(4), []byte("sign"))
	ok := asfIndexedFile(0)
	headerSize := binary.LittleEndian.Uint64(ok[16:24])
	zeroData := slices.Clone(ok)
	binary.LittleEndian.PutUint64(zeroData[headerSize+16:], 0)
	noFileProps := asfFile([]byte("packets"), asfCD("t", "a"))
	badCount := slices.Clone(ok)
	binary.LittleEndian.PutUint32(badCount[24:28], 2)
	for _, c := range []struct {
		name    string
		b       []byte
		refused bool   // ErrASFRefused
		msg     string // in the error
	}{
		{"signed", asfIndexedFile(0, asfCD("t", "a"), signature), true, "digitally signed"},
		{"broadcast", asfIndexedFile(1), true, "broadcast"},
		{"data size 0", zeroData, true, "size"},
		{"no file properties", noFileProps, false, "File Properties"},
		{"bad count", badCount, false, "count"},
		{"not ASF", []byte("RIFF....WAVEfmt this is not an ASF file at all"), false, "ASF"},
		{"truncated header", ok[:headerSize-5], false, ""},
		{"truncated data", ok[:len(ok)-200], false, "Data Object"},
		{"empty", nil, false, ""},
	} {
		var out bytes.Buffer
		err := WriteASFAttribute(&out, bytes.NewReader(c.b), "TAGSHELF_MANUAL", "hand 2026-09-29")
		switch {
		case err == nil:
			t.Errorf("%s: no error", c.name)
		case errors.Is(err, ErrASFRefused) != c.refused:
			t.Errorf("%s: %v; errors.Is(ErrASFRefused) = %t, want %t", c.name, err, !c.refused, c.refused)
		case !strings.Contains(err.Error(), c.msg):
			t.Errorf("%s: %v, want it to mention %q", c.name, err, c.msg)
		}
		if out.Len() != 0 {
			t.Errorf("%s: wrote %d bytes", c.name, out.Len())
		}
	}
}

func TestWriteASFAttributeTooLong(t *testing.T) {
	var out bytes.Buffer
	err := WriteASFAttribute(&out, bytes.NewReader(asfIndexedFile(0)), "TAGSHELF_MANUAL", strings.Repeat("x", 40000))
	if err == nil || out.Len() != 0 {
		t.Errorf("a 80,002-byte value: err %v, wrote %d bytes; want an error and nothing written", err, out.Len())
	}
}
