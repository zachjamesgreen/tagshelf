package tag

import (
	"bytes"
	"encoding/binary"
	"testing"
)

// vorbisComment builds a Vorbis comment block: vendor, count, then each
// length-prefixed comment.
func vorbisComment(vendor string, comments ...string) []byte {
	var b bytes.Buffer
	put := func(s string) {
		binary.Write(&b, binary.LittleEndian, uint32(len(s)))
		b.WriteString(s)
	}
	put(vendor)
	binary.Write(&b, binary.LittleEndian, uint32(len(comments)))
	for _, c := range comments {
		put(c)
	}
	return b.Bytes()
}

// TestVorbisRepeatedKeys checks that a key written more than once keeps every
// value, joined with NUL in Raw as the ASF reader does, while the getters
// return the first.
func TestVorbisRepeatedKeys(t *testing.T) {
	m := newMetadataVorbis()
	err := m.readVorbisComment(bytes.NewReader(vorbisComment("v",
		"MUSICBRAINZ_ARTISTID=a1",
		"ARTIST=First",
		"musicbrainz_artistid=a2",
		"ARTIST=Second",
		"DATE=2003",
		"date=2004",
		"TITLE=Only",
	)))
	if err != nil {
		t.Fatal(err)
	}
	raw := m.Raw()
	if got := raw["musicbrainz_artistid"]; got != "a1\x00a2" {
		t.Errorf("Raw musicbrainz_artistid = %q, want a1 NUL a2", got)
	}
	if got := raw["artist"]; got != "First\x00Second" {
		t.Errorf("Raw artist = %q, want First NUL Second", got)
	}
	if got := raw["title"]; got != "Only" {
		t.Errorf("Raw title = %q, want Only", got)
	}
	if got := m.Artist(); got != "First" {
		t.Errorf("Artist() = %q, want First", got)
	}
	if got := m.Year(); got != 2003 {
		t.Errorf("Year() = %d, want 2003", got)
	}
}

func TestVorbisTrackAndDisc(t *testing.T) {
	table := []struct {
		c                          map[string]string
		track, tracks, disc, discs int
	}{
		{map[string]string{"tracknumber": "3", "tracktotal": "12"}, 3, 12, 0, 0},
		{map[string]string{"tracknumber": "3/12", "discnumber": "1/2"}, 3, 12, 1, 2},
		{map[string]string{"tracknumber": "3/12", "tracktotal": "13"}, 3, 13, 0, 0},
		{map[string]string{"tracknumber": " 3 / 12 ", "discnumber": "2", "disctotal": "3"}, 3, 12, 2, 3},
		{map[string]string{"discnumber": "1/2", "disctotal": "4"}, 0, 0, 1, 4},
	}

	for ii, tt := range table {
		m := &metadataVorbis{c: tt.c}
		if x, n := m.Track(); x != tt.track || n != tt.tracks {
			t.Errorf("[%d] Track() = %d, %d, expected: %d, %d", ii, x, n, tt.track, tt.tracks)
		}
		if x, n := m.Disc(); x != tt.disc || n != tt.discs {
			t.Errorf("[%d] Disc() = %d, %d, expected: %d, %d", ii, x, n, tt.disc, tt.discs)
		}
	}
}
