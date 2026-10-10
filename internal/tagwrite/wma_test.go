package tagwrite

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"slices"
	"strings"
	"testing"

	"go.senan.xyz/taglib"

	"github.com/zachjamesgreen/tagshelf/internal/fixtures"
	"github.com/zachjamesgreen/tagshelf/internal/marker"
	"github.com/zachjamesgreen/tagshelf/internal/tag"
	"github.com/zachjamesgreen/tagshelf/internal/tag/mbz"
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

// TagLib writes the first value of a list to the Extended Content
// Description and the rest to the Metadata Library, and reads the list back
// with the first value last. A save gives it the lists it does not write in
// the order the file has them, so that they stay in it save after save.
func TestWMAListsKeepTheirOrder(t *testing.T) {
	path := copyFixture(t, fixtures.Tagged(".wma"), "song.wma")
	lists := map[string][]string{
		"COMPOSER": {"First", "Second", "Third"},
		"GENRE":    {"Ambient", "Drone"},
		"LYRICIST": {"Someone", "Someone"}, // the same twice: nothing to keep in order
	}
	if err := taglib.WriteTags(path, lists, 0); err != nil {
		t.Fatal(err)
	}
	// The order in the file, as internal/tag reads it, and the order TagLib
	// reads it in.
	inFile := func() map[string]string {
		t.Helper()
		got := map[string]string{}
		for k, v := range read(t, path).Raw() {
			if s, ok := v.(string); ok && strings.Contains(s, "\x00") {
				got[k] = strings.ReplaceAll(s, "\x00", ", ")
			}
		}
		return got
	}
	fileBefore, keysBefore := inFile(), keysOf(t, path)
	if fileBefore["WM/Composer"] != "First, Second, Third" || fileBefore["WM/Genre"] != "Ambient, Drone" {
		t.Fatalf("the lists as written = %v", fileBefore)
	}
	if !slices.Equal(keysBefore["COMPOSER"], []string{"Second", "Third", "First"}) {
		t.Fatalf("TagLib reads the composers as %q: it no longer reads a list rotated, and keepASFOrder may go", keysBefore["COMPOSER"])
	}

	var results []Result
	for i, title := range []string{"One", "Two", "Three", "Four"} {
		res, err := Writer{}.Save(path, Values{Title: {title}, ArtistIDs: {"id-1", "id-2", "id-3"}}, marker.New(marker.Hand, day))
		if err != nil {
			t.Fatal(err)
		}
		results = append(results, res)
		if got := inFile(); got["WM/Composer"] != fileBefore["WM/Composer"] || got["WM/Genre"] != fileBefore["WM/Genre"] {
			t.Fatalf("after save %d the file's lists are %v, want them as they were: %v", i+1, got, fileBefore)
		}
		if got := keysOf(t, path); !slices.Equal(got["COMPOSER"], keysBefore["COMPOSER"]) || !slices.Equal(got["GENRE"], keysBefore["GENRE"]) || !slices.Equal(got["LYRICIST"], keysBefore["LYRICIST"]) {
			t.Fatalf("after save %d TagLib reads the lists as %q, %q and %q", i+1, got["COMPOSER"], got["GENRE"], got["LYRICIST"])
		}
		// The list the save writes is in the order given.
		if got := mbz.Extract(read(t, path)).ArtistIDs; !slices.Equal(got, []string{"id-1", "id-2", "id-3"}) {
			t.Fatalf("after save %d the artist IDs are %q", i+1, got)
		}
	}
	for i := len(results) - 1; i >= 0; i-- {
		if _, err := (Writer{}).Restore(path, results[i].Before, results[i].After); err != nil {
			t.Fatal(err)
		}
	}
	if got := inFile(); got["WM/Composer"] != fileBefore["WM/Composer"] || got["WM/Genre"] != fileBefore["WM/Genre"] {
		t.Errorf("after the restores the file's lists are %v, want %v", got, fileBefore)
	}
	if got := read(t, path).Title(); got != fixtures.Title {
		t.Errorf("after the restores the title is %q", got)
	}
}
