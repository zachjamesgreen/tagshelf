package tagwrite

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"slices"
	"strings"
	"testing"

	"tagshelf/internal/fixtures"
	"tagshelf/internal/marker"
	"tagshelf/internal/tag"
)

// asfGUID returns the on-disk bytes of an ASF GUID written as
// XXXXXXXX-XXXX-XXXX-XXXX-XXXXXXXXXXXX.
func asfGUID(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(strings.ReplaceAll(s, "-", ""))
	if err != nil || len(b) != 16 {
		t.Fatalf("bad GUID %s", s)
	}
	slices.Reverse(b[0:4])
	slices.Reverse(b[4:6])
	slices.Reverse(b[6:8])
	return b
}

func asfObject(guid []byte, data ...[]byte) []byte {
	body := slices.Concat(data...)
	return slices.Concat(guid, binary.LittleEndian.AppendUint64(nil, uint64(24+len(body))), body)
}

func asfHeaderSize(b []byte) uint64 { return binary.LittleEndian.Uint64(b[16:24]) }

// asfFileProperties returns the offset of the File Properties Object's data.
func asfFileProperties(t *testing.T, b []byte) int {
	t.Helper()
	fp := asfGUID(t, "8CABDCA1-A947-11CF-8EE4-00C00C205365")
	for p := 30; p < int(asfHeaderSize(b)); p += int(binary.LittleEndian.Uint64(b[p+16:])) {
		if bytes.Equal(b[p:p+16], fp) {
			return p + 24
		}
	}
	t.Fatal("no File Properties Object")
	return 0
}

// indexedWMA is the tagged WMA fixture with a Simple Index and an Index
// Object after its Data Object, as Windows Media encoders write them. ffmpeg
// writes neither for short files.
func indexedWMA(t *testing.T) []byte {
	t.Helper()
	b := mustRead(t, fixtures.Tagged(".wma"))
	le16 := func(n int) []byte { return binary.LittleEndian.AppendUint16(nil, uint16(n)) }
	le32 := func(n int) []byte { return binary.LittleEndian.AppendUint32(nil, uint32(n)) }
	le64 := func(n int) []byte { return binary.LittleEndian.AppendUint64(nil, uint64(n)) }
	fileID := b[asfHeaderSize(b)+24 : asfHeaderSize(b)+40]
	index := asfObject(asfGUID(t, "D6E229D3-35DA-11D1-9034-00A0C90349BE"),
		le32(1000), le16(1), le32(1), le16(1), le16(1), le32(1), le64(0), le32(0))
	simple := asfObject(asfGUID(t, "33000890-E5B1-11CF-89F4-00A0C90349CB"),
		fileID, le64(10000000), le32(1), le32(1), le32(0), le16(1))
	return slices.Concat(b, index, simple)
}

// TestWMAAudioKept saves to a WMA with index objects: everything after the
// Header Object is byte for byte the same, Sum is unchanged, and File Size is
// the new length.
func TestWMAAudioKept(t *testing.T) {
	orig := indexedWMA(t)
	path := writeTemp(t, orig, "song.wma")
	if _, err := (Writer{}).Save(path, musicBrainzSave(), marker.New(marker.MusicBrainz, day)); err != nil {
		t.Fatal(err)
	}
	now := mustRead(t, path)
	if !bytes.Equal(orig[asfHeaderSize(orig):], now[asfHeaderSize(now):]) {
		t.Error("the Data Object or the index objects changed")
	}
	a, err := tag.Sum(bytes.NewReader(orig))
	if err != nil {
		t.Fatal(err)
	}
	if b := audioSum(t, path); a == "" || a != b {
		t.Errorf("Sum changed from %q to %q", a, b)
	}
	if got := binary.LittleEndian.Uint64(now[asfFileProperties(t, now)+16:]); got != uint64(len(now)) {
		t.Errorf("File Size = %d, want %d", got, len(now))
	}
	if got, _ := marker.Read(read(t, path)); got.String() != "musicbrainz 2026-09-28" {
		t.Errorf("marker = %q", got.String())
	}
}

// TestWMAMarker covers accept tags and last write wins for WMA, whose marker
// go-taglib never reads: the file keeps exactly one marker.
func TestWMAMarker(t *testing.T) {
	path := copyFixture(t, fixtures.Tagged(".wma"), "song.wma")
	res, err := Writer{}.Save(path, nil, marker.New(marker.Hand, day))
	if err != nil {
		t.Fatal(err)
	}
	if !res.Changed || !slices.Equal(res.Before[Marker], []string{}) || !slices.Equal(res.After[Marker], []string{"hand 2026-09-28"}) {
		t.Errorf("accept tags = %+v, want the marker written", res)
	}
	if res, err := (Writer{}).Save(path, nil, marker.New(marker.MusicBrainz, day)); err != nil || res.Changed {
		t.Errorf("accepting a marked WMA again: %+v, %v; want nothing written", res, err)
	}
	res, err = Writer{}.Save(path, Values{Title: {"Other"}}, marker.New(marker.MusicBrainz, day))
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(res.Before[Marker], []string{"hand 2026-09-28"}) {
		t.Errorf("Before[marker] = %q, want the marker read from the file", res.Before[Marker])
	}
	md := read(t, path)
	if got := md.Raw()[marker.Key]; got != "musicbrainz 2026-09-28" {
		t.Errorf("Raw()[%s] = %q, want only the new marker", marker.Key, got)
	}
	if md.Title() != "Other" || md.Album() != fixtures.Album {
		t.Errorf("Title, Album = %q, %q", md.Title(), md.Album())
	}
}

// TestWMARefused covers signed and broadcast WMA files: refused, the original
// untouched, no temporary files left.
func TestWMARefused(t *testing.T) {
	signed := func() []byte {
		b := indexedWMA(t)
		sig := asfObject(asfGUID(t, "2211B3FC-BD23-11D2-B4B7-00A0C955FC6E"), []byte{2, 0, 0, 0, 4, 0}, []byte("sign"))
		size := asfHeaderSize(b)
		out := slices.Concat(b[:size], sig, b[size:])
		binary.LittleEndian.PutUint64(out[16:24], size+uint64(len(sig)))
		binary.LittleEndian.PutUint32(out[24:28], binary.LittleEndian.Uint32(b[24:28])+1)
		return out
	}()
	broadcast := indexedWMA(t)
	broadcast[asfFileProperties(t, broadcast)+64] |= 1
	for name, b := range map[string][]byte{"signed": signed, "broadcast": broadcast} {
		path := writeTemp(t, b, "song.wma")
		_, err := Writer{}.Save(path, Values{Title: {"X"}}, marker.New(marker.Hand, day))
		if !errors.Is(err, tag.ErrASFRefused) {
			t.Errorf("%s: Save = %v, want tag.ErrASFRefused", name, err)
		} else {
			t.Logf("%s: %v", name, err)
		}
		if now := mustRead(t, path); !bytes.Equal(now, b) {
			t.Errorf("%s: the original changed", name)
		}
		if l := leftovers(t, path); l != nil {
			t.Errorf("%s: temporary files left behind: %v", name, l)
		}
	}
}
