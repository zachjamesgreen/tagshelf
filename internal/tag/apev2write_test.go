package tag

import (
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// writeAPEv2 writes b to a temporary file, runs WriteAPEv2Tags on it with
// set, and returns the file's bytes afterwards.
func writeAPEv2(t *testing.T, b []byte, set map[string][]string) ([]byte, error) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "song")
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	werr := WriteAPEv2Tags(f, set)
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	out, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return out, werr
}

func mustWriteAPEv2(t *testing.T, name string, b []byte, set map[string][]string) []byte {
	t.Helper()
	out, err := writeAPEv2(t, b, set)
	if err != nil {
		t.Fatalf("%s: WriteAPEv2Tags: %v", name, err)
	}
	return out
}

// apeRaw reads the APEv2 tag of b and returns its raw items.
func apeRaw(t *testing.T, name string, b []byte) map[string]interface{} {
	t.Helper()
	m, err := ReadAPEv2Tags(bytes.NewReader(b))
	if err != nil {
		t.Fatalf("%s: ReadAPEv2Tags: %v", name, err)
	}
	return m.Raw()
}

// TestWriteAPEv2Placement writes a tag to every layout of trailing tags and
// checks it lands at the end before any ID3v1 tag, replaces the old APEv2
// tag in place, and leaves every other byte alone.
func TestWriteAPEv2Placement(t *testing.T) {
	frame := slices.Concat([]byte("TIT2"), []byte{0, 0, 0, 6, 0, 0, 3}, []byte("Title"))
	id3 := slices.Concat(id3v2TagHeader(4, 0, len(frame)), frame)
	audio := fixtureBytes(t, "untagged.aac")
	old := apeTag(apeItem{key: "Title", value: "An old title long enough to shrink the tag"})
	set := map[string][]string{"Title": {"New"}}
	want := apeTag(apeItem{key: "Title", value: "New"})

	for _, tt := range []struct {
		name     string
		in, want []byte
	}{
		{"none", audio, slices.Concat(audio, want)},
		{"TAG", slices.Concat(audio, id3v1("V1")), slices.Concat(audio, want, id3v1("V1"))},
		{"APE", slices.Concat(audio, old), slices.Concat(audio, want)},
		{"APE TAG", slices.Concat(audio, old, id3v1("V1")), slices.Concat(audio, want, id3v1("V1"))},
		{"TAG APE", slices.Concat(audio, id3v1("V1"), old), slices.Concat(audio, id3v1("V1"), want)},
		{"TAG APE TAG", slices.Concat(audio, id3v1("V1"), old, id3v1("V2")), slices.Concat(audio, id3v1("V1"), want, id3v1("V2"))},
		{"ID3v2 APE TAG", slices.Concat(id3, audio, old, id3v1("V1")), slices.Concat(id3, audio, want, id3v1("V1"))},
		{"ID3v2 TAG", slices.Concat(id3, audio, id3v1("V1")), slices.Concat(id3, audio, want, id3v1("V1"))},
	} {
		got := mustWriteAPEv2(t, tt.name, tt.in, set)
		if !bytes.Equal(got, tt.want) {
			t.Errorf("%s: wrote\n%q\nwant\n%q", tt.name, got, tt.want)
		}
		if a, b := sumBytes(t, tt.name, tt.in), sumBytes(t, tt.name, got); a != b {
			t.Errorf("%s: Sum changed from %s to %s", tt.name, a, b)
		}
	}
}

// TestWriteAPEv2Items checks which items a write keeps, replaces and
// removes, and the header and footer it writes.
func TestWriteAPEv2Items(t *testing.T) {
	audio := fixtureBytes(t, "untagged.aac")
	cover := "cover.jpg\x00\xff\xd8\xff\xe0"
	in := slices.Concat(audio, apeTag(
		apeItem{key: "TITLE", value: "Old Title"},
		apeItem{key: "Cover Art (Front)", value: cover, binary: true},
		apeItem{key: "Year", value: "1999"},
		apeItem{key: "Date", value: "1999-01-01"},
		apeItem{key: "REPLAYGAIN_TRACK_GAIN", value: "-6.00 dB"},
	))
	got := mustWriteAPEv2(t, "items", in, map[string][]string{
		"Title":                {"New Title"},
		"Year":                 {"2003-04-05"},
		"date":                 {}, // removed
		"Musicbrainz_Artistid": {"a", "b"},
		"Nothing":              nil, // absent: nothing to remove
	})

	raw := apeRaw(t, "items", got)
	wantRaw := map[string]interface{}{
		"Title":                 "New Title",
		"Cover Art (Front)":     []byte(cover),
		"Year":                  "2003-04-05",
		"REPLAYGAIN_TRACK_GAIN": "-6.00 dB",
		"Musicbrainz_Artistid":  "a\x00b",
	}
	if len(raw) != len(wantRaw) {
		t.Errorf("items %q, want %q", raw, wantRaw)
	}
	for k, v := range wantRaw {
		if b, ok := v.([]byte); ok {
			if g, _ := raw[k].([]byte); !bytes.Equal(g, b) {
				t.Errorf("%s = %q, want %q", k, raw[k], b)
			}
		} else if raw[k] != v {
			t.Errorf("%s = %q, want %q", k, raw[k], v)
		}
	}

	// Kept items stay in their order; new ones follow in key order.
	want := apeTag(
		apeItem{key: "Title", value: "New Title"},
		apeItem{key: "Cover Art (Front)", value: cover, binary: true},
		apeItem{key: "Year", value: "2003-04-05"},
		apeItem{key: "REPLAYGAIN_TRACK_GAIN", value: "-6.00 dB"},
		apeItem{key: "Musicbrainz_Artistid", value: "a\x00b"},
	)
	if tag := got[len(audio):]; !bytes.Equal(tag, want) {
		t.Errorf("tag\n%q\nwant\n%q", tag, want)
	}
	header, footer := got[len(audio):len(audio)+32], got[len(got)-32:]
	if v := binary.LittleEndian.Uint32(header[8:]); v != 2000 {
		t.Errorf("version %d, want 2000", v)
	}
	if f := binary.LittleEndian.Uint32(header[20:]); f != 0xA0000000 {
		t.Errorf("header flags %#x, want 0xA0000000", f)
	}
	if f := binary.LittleEndian.Uint32(footer[20:]); f != 0x80000000 {
		t.Errorf("footer flags %#x, want 0x80000000", f)
	}
}

// TestWriteAPEv2DuplicateItems drops the second of two items whose keys
// differ only in case, which the spec forbids.
func TestWriteAPEv2DuplicateItems(t *testing.T) {
	audio := fixtureBytes(t, "untagged.aac")
	in := slices.Concat(audio, apeTag(apeItem{key: "Title", value: "One"}, apeItem{key: "TITLE", value: "Two"}))
	got := mustWriteAPEv2(t, "duplicates", in, map[string][]string{"TITLE": {"Three"}})
	if want := slices.Concat(audio, apeTag(apeItem{key: "TITLE", value: "Three"})); !bytes.Equal(got, want) {
		t.Errorf("wrote %q, want %q", got, want)
	}
}

// TestWriteAPEv2RemoveLast removes the tag when no items are left.
func TestWriteAPEv2RemoveLast(t *testing.T) {
	audio := fixtureBytes(t, "untagged.aac")
	in := slices.Concat(audio, apeTag(apeItem{key: "Title", value: "T"}), id3v1("V1"))
	got := mustWriteAPEv2(t, "remove", in, map[string][]string{"title": {}})
	if want := slices.Concat(audio, id3v1("V1")); !bytes.Equal(got, want) {
		t.Errorf("wrote %q, want %q", got, want)
	}
}

// TestWriteAPEv2Refuses leaves the file alone for bad keys and values and a
// damaged tag.
func TestWriteAPEv2Refuses(t *testing.T) {
	audio := fixtureBytes(t, "untagged.aac")
	good := slices.Concat(audio, apeTag(apeItem{key: "Title", value: "T"}))
	damaged := slices.Clone(good)
	binary.LittleEndian.PutUint32(damaged[len(audio)+32:], 1000) // the item's value size

	for _, tt := range []struct {
		name string
		in   []byte
		set  map[string][]string
	}{
		{"short key", good, map[string][]string{"X": {"v"}}},
		{"non-ASCII key", good, map[string][]string{"Titlé": {"v"}}},
		{"reserved key", good, map[string][]string{"id3": {"v"}}},
		{"same key twice", good, map[string][]string{"Title": {"a"}, "TITLE": {"b"}}},
		{"NUL in value", good, map[string][]string{"Title": {"a\x00b"}}},
		{"invalid UTF-8", good, map[string][]string{"Title": {"\xff"}}},
		{"damaged tag", damaged, map[string][]string{"Title": {"v"}}},
		// The footer says there is a header, which would start in the audio.
		{"missing header", slices.Concat(audio, apeTag(apeItem{key: "Title", value: "T"})[32:]), map[string][]string{"Title": {"v"}}},
	} {
		got, err := writeAPEv2(t, tt.in, tt.set)
		if err == nil {
			t.Errorf("%s: no error", tt.name)
		}
		if !bytes.Equal(got, tt.in) {
			t.Errorf("%s: the file changed", tt.name)
		}
	}
}
