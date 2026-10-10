package tag

import (
	"bytes"
	"encoding/binary"
	"os"
	"slices"
	"testing"
	"unicode/utf16"

	"tagshelf/internal/fixtures"
)

// asfObject renders an ASF object: GUID, 64-bit size, data.
func asfObject(guid string, data ...[]byte) []byte {
	body := slices.Concat(data...)
	b := []byte(guid)
	b = binary.LittleEndian.AppendUint64(b, uint64(24+len(body)))
	return append(b, body...)
}

// asfString renders NUL-terminated UTF-16LE text.
func asfString(s string) []byte {
	var b []byte
	for _, u := range utf16.Encode([]rune(s)) {
		b = binary.LittleEndian.AppendUint16(b, u)
	}
	return append(b, 0, 0)
}

func le16(n int) []byte { return binary.LittleEndian.AppendUint16(nil, uint16(n)) }
func le32(n int) []byte { return binary.LittleEndian.AppendUint32(nil, uint32(n)) }

// asfDescriptor renders one Extended Content Description descriptor.
func asfDescriptor(name string, typ int, value []byte) []byte {
	n := asfString(name)
	return slices.Concat(le16(len(n)), n, le16(typ), le16(len(value)), value)
}

func asfECD(descriptors ...[]byte) []byte {
	return asfObject(asfExtendedContentDescriptionGUID, le16(len(descriptors)), slices.Concat(descriptors...))
}

func asfCD(title, author string) []byte {
	t, a := asfString(title), asfString(author)
	return asfObject(asfContentDescriptionGUID, le16(len(t)), le16(len(a)), le16(0), le16(0), le16(0), t, a)
}

// asfMetadataRecord renders one Metadata / Metadata Library record.
func asfMetadataRecord(name string, typ int, value []byte) []byte {
	n := asfString(name)
	return slices.Concat(le16(0), le16(1), le16(len(n)), le16(typ), le32(len(value)), n, value)
}

func asfHeaderExtension(objects ...[]byte) []byte {
	nested := slices.Concat(objects...)
	return asfObject(asfHeaderExtensionGUID, make([]byte, 16), le16(6), le32(len(nested)), nested)
}

// asfFile renders a Header Object holding children, then a Data Object
// holding packets, then an index-like trailer.
func asfFile(packets []byte, children ...[]byte) []byte {
	body := slices.Concat(children...)
	header := []byte(asfHeaderGUID)
	header = binary.LittleEndian.AppendUint64(header, uint64(30+len(body)))
	header = binary.LittleEndian.AppendUint32(header, uint32(len(children)))
	header = append(header, 1, 2)
	data := asfObject(asfDataGUID, make([]byte, 16), binary.LittleEndian.AppendUint64(nil, 1), []byte{1, 1}, packets)
	index := asfObject(asfGUID("33000890-E5B1-11CF-89F4-00A0C90349CB"), []byte("simple index"))
	return slices.Concat(header, body, data, index)
}

// addASFChild appends child to the Header Object of an ASF file, fixing the
// header size and count, as a tag writer that adds an object does.
func addASFChild(t *testing.T, file, child []byte) []byte {
	t.Helper()
	if string(file[:16]) != asfHeaderGUID {
		t.Fatal("not an ASF file")
	}
	size := binary.LittleEndian.Uint64(file[16:24])
	out := slices.Concat(file[:size], child, file[size:])
	binary.LittleEndian.PutUint64(out[16:24], size+uint64(len(child)))
	binary.LittleEndian.PutUint32(out[24:28], binary.LittleEndian.Uint32(file[24:28])+1)
	return out
}

func readASF(t *testing.T, b []byte) Metadata {
	t.Helper()
	m, err := ReadFrom(bytes.NewReader(b))
	if err != nil {
		t.Fatalf("ReadFrom: %v", err)
	}
	if m.Format() != ASF || m.FileType() != WMA {
		t.Fatalf("Format, FileType = %v, %v, want ASF, WMA", m.Format(), m.FileType())
	}
	return m
}

func TestReadASFFixture(t *testing.T) {
	b, err := os.ReadFile(fixtures.Tagged(".wma"))
	if err != nil {
		t.Fatal(err)
	}
	m := readASF(t, b)
	for _, c := range []struct{ field, got, want string }{
		{"Title", m.Title(), fixtures.Title},
		{"Artist", m.Artist(), fixtures.Artist},
		{"AlbumArtist", m.AlbumArtist(), fixtures.AlbumArtist},
		{"Album", m.Album(), fixtures.Album},
	} {
		if c.got != c.want {
			t.Errorf("%s = %q, want %q", c.field, c.got, c.want)
		}
	}
	if y := m.Year(); y != fixtures.Year {
		t.Errorf("Year = %d, want %d", y, fixtures.Year)
	}
	if n, total := m.Track(); n != fixtures.Track || total != 0 {
		t.Errorf("Track = %d, %d, want %d, 0", n, total, fixtures.Track)
	}
	if n, total := m.Disc(); n != fixtures.Disc || total != fixtures.DiscTotal {
		t.Errorf("Disc = %d, %d, want %d, %d", n, total, fixtures.Disc, fixtures.DiscTotal)
	}
	// ffmpeg writes the title into both the Content Description and the
	// ECD; the same value is kept once.
	if got := m.Raw()["Title"]; got != fixtures.Title {
		t.Errorf("Raw()[Title] = %q, want %q", got, fixtures.Title)
	}
	if got := m.Raw()["MusicBrainz/Album Id"]; got != fixtures.ReleaseID {
		t.Errorf("Raw()[MusicBrainz/Album Id] = %q, want %q", got, fixtures.ReleaseID)
	}

	f, err := os.Open(fixtures.Tagged(".wma"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if format, fileType, err := Identify(f); err != nil || format != ASF || fileType != WMA {
		t.Errorf("Identify = %v, %v, %v, want ASF, WMA, nil", format, fileType, err)
	}
}

func TestReadASFUntaggedFixture(t *testing.T) {
	b, err := os.ReadFile(fixtures.Untagged(".wma"))
	if err != nil {
		t.Fatal(err)
	}
	m := readASF(t, b)
	if m.Title() != "" || m.Artist() != "" || m.Album() != "" {
		t.Errorf("untagged.wma reads %q / %q / %q", m.Title(), m.Artist(), m.Album())
	}
}

// TestReadASFMarker adds the manually-tagged marker to the fixture's ECD the
// way the marker writer will: a second ECD object here, since ReadASFTags
// must read every one.
func TestReadASFMarker(t *testing.T) {
	b, err := os.ReadFile(fixtures.Tagged(".wma"))
	if err != nil {
		t.Fatal(err)
	}
	marked := addASFChild(t, b, asfECD(asfDescriptor("TAGSHELF_MANUAL", asfUnicode, asfString("hand 2026-09-27"))))
	m := readASF(t, marked)
	if got := m.Raw()["TAGSHELF_MANUAL"]; got != "hand 2026-09-27" {
		t.Errorf("Raw()[TAGSHELF_MANUAL] = %q, want %q", got, "hand 2026-09-27")
	}
	if m.Title() != fixtures.Title {
		t.Errorf("Title = %q after marking", m.Title())
	}
}

func TestReadASFValueTypes(t *testing.T) {
	jpeg := []byte{0xff, 0xd8, 0xff, 0xe0}
	pic := slices.Concat([]byte{3}, le32(len(jpeg)), asfString("image/jpeg"), asfString("front"), jpeg)
	b := asfFile([]byte("packets"),
		asfCD("CD Title", "CD Author"),
		asfECD(
			asfDescriptor("WM/TrackNumber", asfDWORD, le32(7)),
			asfDescriptor("WM/Year", asfUnicode, asfString("1997-05-12")),
			asfDescriptor("WM/IsCompilation", asfBool, le32(1)),
			asfDescriptor("MusicBrainz/Artist Id", asfUnicode, asfString("a1")),
			asfDescriptor("MusicBrainz/Artist Id", asfUnicode, asfString("a2")),
			asfDescriptor("wm/albumtitle", asfUnicode, asfString("Lower Case")),
		),
		asfHeaderExtension(
			asfObject(asfMetadataGUID, le16(1), asfMetadataRecord("WM/SharedUserRating", asfWORD, le16(50))),
			asfObject(asfMetadataLibraryGUID, le16(2),
				asfMetadataRecord("WM/Picture", asfBytes, pic),
				asfMetadataRecord("WM/MediaClassPrimaryID", asfGUIDVal, []byte(asfGUID("D1607DBC-E323-4BE2-86A1-48A42A28441E")))),
		),
	)
	m := readASF(t, b)
	if m.Title() != "CD Title" || m.Artist() != "CD Author" || m.Album() != "Lower Case" {
		t.Errorf("Title, Artist, Album = %q, %q, %q", m.Title(), m.Artist(), m.Album())
	}
	if n, _ := m.Track(); n != 7 {
		t.Errorf("Track = %d, want 7 (DWORD)", n)
	}
	if y := m.Year(); y != 1997 {
		t.Errorf("Year = %d, want 1997", y)
	}
	raw := m.Raw()
	for k, want := range map[string]string{
		"WM/IsCompilation":       "true",
		"MusicBrainz/Artist Id":  "a1\x00a2",
		"WM/SharedUserRating":    "50",
		"WM/MediaClassPrimaryID": "D1607DBC-E323-4BE2-86A1-48A42A28441E",
	} {
		if raw[k] != want {
			t.Errorf("Raw()[%s] = %q, want %q", k, raw[k], want)
		}
	}
	p := m.Picture()
	if p == nil || p.MIMEType != "image/jpeg" || p.Description != "front" || !bytes.Equal(p.Data, jpeg) || p.Ext != "jpg" {
		t.Errorf("Picture = %+v", p)
	}
}

func TestReadASFLegacyTrack(t *testing.T) {
	m := readASF(t, asfFile(nil, asfECD(asfDescriptor("WM/Track", asfDWORD, le32(0)))))
	if n, _ := m.Track(); n != 1 {
		t.Errorf("Track = %d, want 1 from zero-based WM/Track", n)
	}
}

// TestReadASFTruncated cuts the fixture's header at every length: the reader
// must fail cleanly, never panic.
func TestReadASFTruncated(t *testing.T) {
	b, err := os.ReadFile(fixtures.Tagged(".wma"))
	if err != nil {
		t.Fatal(err)
	}
	size := int(binary.LittleEndian.Uint64(b[16:24]))
	for n := 11; n < size; n++ {
		if _, err := ReadFrom(bytes.NewReader(b[:n])); err == nil {
			t.Fatalf("ReadFrom of %d header bytes: no error", n)
		}
	}
	// A child whose size runs past the header.
	bad := slices.Clone(b)
	binary.LittleEndian.PutUint64(bad[30+16:30+24], uint64(size))
	if _, err := ReadFrom(bytes.NewReader(bad)); err == nil {
		t.Error("ReadFrom with an oversized child: no error")
	}
}

// TestSumASFSurvivesRetag is the regression test for WMA, whose Sum used to
// fall through to hashing the whole file.
func TestSumASFSurvivesRetag(t *testing.T) {
	u, g := sumFile(t, "untagged.wma"), sumFile(t, "tagged.wma")
	if u != g {
		t.Errorf("Sum(untagged.wma) = %s, Sum(tagged.wma) = %s, want equal", u, g)
	}
	b, err := os.ReadFile(fixtures.Tagged(".wma"))
	if err != nil {
		t.Fatal(err)
	}
	marked := addASFChild(t, b, asfECD(asfDescriptor("TAGSHELF_MANUAL", asfUnicode, asfString("musicbrainz 2026-09-27"))))
	if m := sumBytes(t, "marked tagged.wma", marked); m != g {
		t.Errorf("Sum after adding the marker = %s, want %s", m, g)
	}
}

// TestSumASFRange pins the bytes SumASF hashes: the Data Object's packets,
// without its 50-byte header or the index that follows it.
func TestSumASFRange(t *testing.T) {
	packets := []byte("the data packets")
	want := sha1Hex(packets)
	for name, b := range map[string][]byte{
		"bare":   asfFile(packets),
		"tagged": asfFile(packets, asfCD("t", "a"), asfECD(asfDescriptor("WM/AlbumTitle", asfUnicode, asfString("x")))),
	} {
		if got := sumBytes(t, name, b); got != want {
			t.Errorf("Sum(%s) = %s, want %s", name, got, want)
		}
	}
}
