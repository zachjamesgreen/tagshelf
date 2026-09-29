package tagwrite

import (
	"bytes"
	"encoding/binary"
	"os"
	"reflect"
	"slices"
	"testing"

	"tagshelf/internal/fixtures"
	"tagshelf/internal/marker"
	"tagshelf/internal/tag"
	"tagshelf/internal/tag/mbz"
)

// readAPESide reads the APEv2 side of an .aac file.
func readAPESide(t *testing.T, path string) tag.Metadata {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	md, err := tag.ReadAPEv2Tags(f)
	if err != nil {
		t.Fatalf("reading the APEv2 tag of %s: %v", path, err)
	}
	return md
}

func audioSum(t *testing.T, path string) string {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	s, err := tag.Sum(f)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// sides are the values both tags of an .aac file must agree on.
type sides struct {
	Title, Artist, AlbumArtist, Album string
	Year, Track, TrackTotal           int
	Disc, DiscTotal                   int
	IDs                               mbz.IDs
	Marker                            string
}

func sidesOf(md tag.Metadata) sides {
	s := sides{Title: md.Title(), Artist: md.Artist(), AlbumArtist: md.AlbumArtist(), Album: md.Album(), Year: md.Year(), IDs: mbz.Extract(md)}
	s.Track, s.TrackTotal = md.Track()
	s.Disc, s.DiscTotal = md.Disc()
	if m, ok := marker.Read(md); ok {
		s.Marker = m.String()
	}
	return s
}

// checkBothSides checks that the ID3v2 and APEv2 tags of an .aac file read
// the same, that there is one APEv2 tag, and returns what they read.
func checkBothSides(t *testing.T, name, path string) sides {
	t.Helper()
	id3 := read(t, path)
	if id3.Format() != tag.ID3v2_4 || id3.FileType() != tag.AAC {
		t.Errorf("%s: reads as %s %s, want ID3v2.4 AAC", name, id3.Format(), id3.FileType())
	}
	a, b := sidesOf(id3), sidesOf(readAPESide(t, path))
	if !reflect.DeepEqual(a, b) {
		t.Errorf("%s: the sides differ\nID3v2: %+v\nAPEv2: %+v", name, a, b)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if n := bytes.Count(data, []byte("APETAGEX")); n != 2 {
		t.Errorf("%s: %d APEv2 headers and footers, want one tag with both", name, n)
	}
	return a
}

// TestAACBothSides saves every field to .aac files without tags, with
// Picard's APEv2 tag only and with ID3v2 only, and checks both sides, the
// Picard key names and that the audio hash is kept.
func TestAACBothSides(t *testing.T) {
	for _, src := range []string{fixtures.Untagged(".aac"), fixtures.Tagged(".aac"), fixtures.Path(fixtures.TaggedID3v2AAC)} {
		path := copyFixture(t, src, "song.aac")
		sumBefore := audioSum(t, path)
		m := marker.New(marker.MusicBrainz, day)
		fields := musicBrainzSave()
		if _, err := (Writer{}).Save(path, fields, m); err != nil {
			t.Fatalf("%s: %v", src, err)
		}
		got := checkBothSides(t, src, path)
		if got.Title != "New Title" || got.Marker != m.String() || got.IDs.RecordingID != fields[RecordingID][0] {
			t.Errorf("%s: read %+v", src, got)
		}

		want := map[string]interface{}{
			"Title":                      "New Title",
			"Artist":                     "New Artist feat. Other",
			"Album Artist":               "New Album Artist",
			"Album":                      "New Album",
			"Year":                       "2003-04-05",
			"Track":                      "4/9",
			"Disc":                       "2/3",
			"Compilation":                "1",
			"musicbrainz_trackid":        fields[RecordingID][0],
			"musicbrainz_releasetrackid": fields[ReleaseTrackID][0],
			"Musicbrainz_Albumid":        fields[AlbumID][0],
			"Musicbrainz_Releasegroupid": fields[ReleaseGroupID][0],
			"Musicbrainz_Artistid":       fields[ArtistIDs][0] + "\x00" + fields[ArtistIDs][1],
			"Musicbrainz_Albumartistid":  fields[AlbumArtistIDs][0],
			"TAGSHELF_MANUAL":            m.String(),
		}
		if raw := readAPESide(t, path).Raw(); !reflect.DeepEqual(raw, want) {
			t.Errorf("%s: APEv2 items\n%q\nwant\n%q", src, raw, want)
		}
		if s := audioSum(t, path); s != sumBefore {
			t.Errorf("%s: Sum changed from %s to %s", src, sumBefore, s)
		}
	}
}

// TestAACPicardUpdate saves some fields to a Picard-tagged .aac (APEv2 only)
// that also carries cover art and a second date key: both sides take the
// new values, the fields not saved keep Picard's values on both sides, and no
// stale APEv2 value is left.
func TestAACPicardUpdate(t *testing.T) {
	path := copyFixture(t, fixtures.Tagged(".aac"), "song.aac")
	cover := "cover.jpg\x00\xff\xd8\xff\xe0"
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := tag.WriteAPEv2Tags(f, map[string][]string{"DATE": {"1999"}}); err != nil {
		t.Fatal(err)
	}
	f.Close()
	// A cover is a binary item, which WriteAPEv2Tags cannot add: append it by
	// hand as the last item.
	addBinaryAPEItem(t, path, "Cover Art (Front)", cover)
	sumBefore := audioSum(t, path)

	m := marker.New(marker.Hand, day)
	if _, err := (Writer{}).Save(path, Values{Title: {"Hand Title"}, Album: {}, Date: {"2002"}}, m); err != nil {
		t.Fatal(err)
	}
	got := checkBothSides(t, "Picard", path)
	want := sides{
		Title: "Hand Title", Artist: fixtures.Artist, AlbumArtist: fixtures.AlbumArtist, Album: "",
		Year: 2002, Track: fixtures.Track, TrackTotal: fixtures.TrackTotal, Disc: fixtures.Disc, DiscTotal: fixtures.DiscTotal,
		IDs: mbz.IDs{
			RecordingID: fixtures.RecordingID, ReleaseTrackID: fixtures.ReleaseTrackID, AlbumID: fixtures.ReleaseID,
			ReleaseGroupID: fixtures.ReleaseGroupID, ArtistIDs: []string{fixtures.ArtistID}, AlbumArtistIDs: []string{fixtures.AlbumArtistID},
		},
		Marker: m.String(),
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("read %+v\nwant %+v", got, want)
	}

	raw := readAPESide(t, path).Raw()
	for _, k := range []string{"Album", "DATE"} {
		if v, ok := raw[k]; ok {
			t.Errorf("stale APEv2 item %s = %q left", k, v)
		}
	}
	if raw["Year"] != "2002" || raw["Title"] != "Hand Title" {
		t.Errorf("Year %q, Title %q; want 2002 and Hand Title", raw["Year"], raw["Title"])
	}
	if b, _ := raw["Cover Art (Front)"].([]byte); string(b) != cover {
		t.Errorf("cover art = %q, want it kept", raw["Cover Art (Front)"])
	}
	if s := audioSum(t, path); s != sumBefore {
		t.Errorf("Sum changed from %s to %s", sumBefore, s)
	}
}

// addBinaryAPEItem appends a binary item to the APEv2 tag, with header,
// that ends the file at path.
func addBinaryAPEItem(t *testing.T, path, key, value string) {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	le := binary.LittleEndian
	footer := b[len(b)-32:]
	if string(footer[:8]) != "APETAGEX" {
		t.Fatal("the file does not end with an APEv2 footer")
	}
	start := len(b) - 32 - int(le.Uint32(footer[12:])) // the header
	item := le.AppendUint32(nil, uint32(len(value)))
	item = le.AppendUint32(item, 1<<1) // binary
	item = append(append(append(item, key...), 0), value...)
	body := slices.Concat(b[start+32:len(b)-32], item)
	block := func(from []byte) []byte {
		out := slices.Clone(from)
		le.PutUint32(out[12:], uint32(len(body)+32))
		le.PutUint32(out[16:], le.Uint32(from[16:])+1)
		return out
	}
	out := slices.Concat(b[:start], block(b[start:start+32]), body, block(footer))
	if err := os.WriteFile(path, out, 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestAACNoChange skips a save only when both sides already have the
// values and the marker.
func TestAACNoChange(t *testing.T) {
	path := copyFixture(t, fixtures.Tagged(".aac"), "song.aac")
	fields := Values{Title: {"T"}, Track: {"5/7"}}
	if _, err := (Writer{}).Save(path, fields, marker.New(marker.Hand, day)); err != nil {
		t.Fatal(err)
	}
	res, err := Writer{}.Save(path, fields, marker.New(marker.Hand, day))
	if err != nil {
		t.Fatal(err)
	}
	if res.Changed {
		t.Error("a save that changes nothing wrote the file")
	}

	// The APEv2 side goes stale behind tagshelf's back: the same save
	// writes again and brings it back in line.
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := tag.WriteAPEv2Tags(f, map[string][]string{"Title": {"Stale"}}); err != nil {
		t.Fatal(err)
	}
	f.Close()
	res, err = Writer{}.Save(path, fields, marker.New(marker.Hand, day))
	if err != nil {
		t.Fatal(err)
	}
	if !res.Changed {
		t.Error("a stale APEv2 side was not written")
	}
	if got := checkBothSides(t, "restored", path); got.Title != "T" || got.Track != 5 || got.TrackTotal != 7 {
		t.Errorf("read %+v", got)
	}
}
