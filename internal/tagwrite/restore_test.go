package tagwrite

import (
	"bytes"
	"errors"
	"maps"
	"path/filepath"
	"reflect"
	"slices"
	"testing"

	"go.senan.xyz/taglib"

	"tagshelf/internal/fixtures"
	"tagshelf/internal/marker"
	"tagshelf/internal/tag"
)

// keysOf reads the file at path with go-taglib: every key as it is stored,
// not in the form Read gives the fields.
func keysOf(t *testing.T, path string) map[string][]string {
	t.Helper()
	keys, err := taglib.ReadTags(path)
	if err != nil {
		t.Fatalf("reading %s with go-taglib: %v", path, err)
	}
	return keys
}

// markerOf returns the marker internal/tag reads from the file at path, ""
// for none or for a file without tags.
func markerOf(t *testing.T, path string) string {
	t.Helper()
	md, err := readTags(path)
	if errors.Is(err, tag.ErrNoTagsFound) {
		return ""
	}
	if err != nil {
		t.Fatalf("reading %s back: %v", path, err)
	}
	m, ok := marker.Read(md)
	if !ok {
		return ""
	}
	return m.String()
}

// TestRestoreUndoesASave saves to every taggable format, to a file without
// tags and to a tagged one, one field and every field, and restores: the
// file then has the keys go-taglib read from it before the save, no marker,
// no ID3v1 tag and the same audio, and the restore is recorded. A WMA file
// keeps the marker, which tagshelf cannot remove.
func TestRestoreUndoesASave(t *testing.T) {
	saves := map[string]Values{"one field": {Title: {"X"}}, "every field": musicBrainzSave()}
	for ext, from := range taggableExtensions {
		for _, src := range []string{fixtures.Untagged(from), fixtures.Tagged(from)} {
			for name, fields := range saves {
				t.Run(filepath.Base(src)+"->"+ext+" "+name, func(t *testing.T) {
					path := copyFixture(t, src, "song"+ext)
					keysBefore, sumBefore := keysOf(t, path), audioSum(t, path)
					m := marker.New(marker.MusicBrainz, day)
					res, err := Writer{}.Save(path, fields, m)
					if err != nil || !res.Changed {
						t.Fatalf("save: changed %t, %v", res.Changed, err)
					}
					if ok, err := Matches(path, res.After); err != nil || !ok {
						t.Errorf("the file does not match what the save says it left: %v", err)
					}
					if ok, err := Matches(path, res.Before); err != nil || ok {
						t.Errorf("the file still matches what it had before the save (%v)", err)
					}

					hist := &history{}
					back, err := Writer{History: hist, RunID: 7}.Restore(path, res.Before, res.After)
					if err != nil || !back.Changed {
						t.Fatalf("restore: changed %t, %v", back.Changed, err)
					}
					if got := keysOf(t, path); !reflect.DeepEqual(got, keysBefore) {
						t.Errorf("keys after the restore\n%q\nwant those before the save\n%q", got, keysBefore)
					}
					wantMarker := ""
					if ext == ".wma" {
						wantMarker = m.String()
					}
					if got := markerOf(t, path); got != wantMarker {
						t.Errorf("marker after the restore = %q, want %q", got, wantMarker)
					}
					if got := audioSum(t, path); got != sumBefore {
						t.Errorf("Sum changed from %s to %s", sumBefore, got)
					}
					if has, err := endsWithID3v1(path); err != nil || has {
						t.Errorf("ends with an ID3v1 tag: %t, %v", has, err)
					}
					if len(hist.rows) != 1 || hist.rows[0].runID != 7 {
						t.Errorf("history = %+v, want the restore recorded under run 7", hist.rows)
					}
					if l := leftovers(t, path); l != nil {
						t.Errorf("temporary files left behind: %v", l)
					}

					// Restoring again finds the file changed and leaves it alone.
					if _, err := (Writer{}).Restore(path, res.Before, res.After); !errors.Is(err, ErrChanged) {
						t.Errorf("a second restore: %v, want ErrChanged", err)
					}
				})
			}
		}
	}
}

// A restore that would change nothing writes nothing: the WMA file whose
// only change was the marker, which stays.
func TestRestoreThatChangesNothing(t *testing.T) {
	path := copyFixture(t, fixtures.Tagged(".wma"), "song.wma")
	res, err := Writer{}.Save(path, nil, marker.New(marker.Hand, day))
	if err != nil {
		t.Fatal(err)
	}
	orig := mustRead(t, path)
	hist := &history{}
	back, err := Writer{History: hist}.Restore(path, res.Before, res.After)
	if err != nil || back.Changed {
		t.Errorf("restore: changed %t, %v; want nothing written", back.Changed, err)
	}
	if len(hist.rows) != 0 || !slices.Equal(mustRead(t, path), orig) {
		t.Errorf("the file was written or the restore recorded: %d rows", len(hist.rows))
	}
}

// A marker the save put on an unmarked file is taken away again.
func TestRestoreRemovesTheMarker(t *testing.T) {
	path := copyFixture(t, fixtures.Path(fixtures.RipperFLAC), "ripped.flac")
	res, err := Writer{}.Save(path, Values{Album: {"Another"}}, marker.New(marker.Hand, day))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := (Writer{}).Restore(path, res.Before, res.After); err != nil {
		t.Fatal(err)
	}
	md := read(t, path)
	if m, ok := marker.Read(md); ok {
		t.Errorf("the marker is still there after the restore: %q", m)
	}
	if md.Album() != fixtures.Album {
		t.Errorf("album after the restore = %q, want %q", md.Album(), fixtures.Album)
	}
}

// TestRestoreNumberAsItStood checks that a track or disc number written
// otherwise than a save writes it comes back as it stood: with its leading
// zeros, and in Vorbis comments with the total under the keys it had.
func TestRestoreNumberAsItStood(t *testing.T) {
	for _, tt := range []struct {
		name, ext   string
		set         map[string][]string
		track, disc []string // the stored forms recorded, nil for none
	}{
		{"leading zeros", ".mp3", map[string][]string{taglib.TrackNumber: {"03/012"}, taglib.DiscNumber: {"01"}},
			[]string{"TRACKNUMBER=03/012"}, []string{"DISCNUMBER=01"}},
		{"as a save writes it", ".mp3", map[string][]string{taglib.TrackNumber: {"3/12"}, taglib.DiscNumber: {"1"}}, nil, nil},
		{"one total key", ".flac", map[string][]string{taglib.TrackNumber: {"03"}, "TRACKTOTAL": {"12"}, "TOTALTRACKS": nil,
			taglib.DiscNumber: {"1"}, "DISCTOTAL": nil, "TOTALDISCS": {"2"}},
			[]string{"TRACKNUMBER=03", "TRACKTOTAL=12"}, []string{"DISCNUMBER=1", "TOTALDISCS=2"}},
		{"N of M under the number", ".ogg", map[string][]string{taglib.TrackNumber: {"3/12"}, "TRACKTOTAL": nil, "TOTALTRACKS": nil,
			taglib.DiscNumber: nil, "DISCTOTAL": nil, "TOTALDISCS": nil},
			[]string{"TRACKNUMBER=3/12"}, nil},
		{"both total keys, as a save writes it", ".flac", map[string][]string{taglib.TrackNumber: {"3"}, "TRACKTOTAL": {"12"}, "TOTALTRACKS": {"12"},
			taglib.DiscNumber: {"1"}, "DISCTOTAL": nil, "TOTALDISCS": nil}, nil, nil},
		{"leading zeros", ".ape", map[string][]string{taglib.TrackNumber: {"03"}, taglib.DiscNumber: {"1/02"}},
			[]string{"TRACKNUMBER=03"}, []string{"DISCNUMBER=1/02"}},
		{"leading zeros", ".wma", map[string][]string{taglib.TrackNumber: {"03"}, taglib.DiscNumber: {"01/02"}},
			[]string{"TRACKNUMBER=03"}, []string{"DISCNUMBER=01/02"}},
		{"leading zeros", ".wav", map[string][]string{taglib.TrackNumber: {"03"}, taglib.DiscNumber: {"1/2"}},
			[]string{"TRACKNUMBER=03"}, nil},
		{"leading zeros", ".aac", map[string][]string{taglib.TrackNumber: {"03/10"}, taglib.DiscNumber: {"1/2"}},
			[]string{"TRACKNUMBER=03/10"}, nil},
	} {
		t.Run(tt.name+tt.ext, func(t *testing.T) {
			path := copyFixture(t, fixtures.Tagged(tt.ext), "song"+tt.ext)
			if err := taglib.WriteTags(path, tt.set, 0); err != nil {
				t.Fatal(err)
			}
			keysBefore := keysOf(t, path)
			res, err := Writer{}.Save(path, Values{Track: {"4/9"}, Disc: {"2/3"}}, marker.New(marker.Hand, day))
			if err != nil {
				t.Fatal(err)
			}
			if got, ok := res.Before[TrackStored]; !slices.Equal(got, tt.track) || ok != (tt.track != nil) {
				t.Errorf("Before[%s] = %q, want %q", TrackStored, got, tt.track)
			}
			if got, ok := res.Before[DiscStored]; !slices.Equal(got, tt.disc) || ok != (tt.disc != nil) {
				t.Errorf("Before[%s] = %q, want %q", DiscStored, got, tt.disc)
			}
			if _, ok := res.After[TrackStored]; ok {
				t.Errorf("After has %s: %v", TrackStored, res.After)
			}

			back, err := Writer{}.Restore(path, res.Before, res.After)
			if err != nil {
				t.Fatal(err)
			}
			if got := keysOf(t, path); !reflect.DeepEqual(got, keysBefore) {
				t.Errorf("keys after the restore\n%q\nwant those before the save\n%q", got, keysBefore)
			}

			// The restore undone: its After, stored forms and all, is what
			// the file holds, and the file then has what the save left.
			if ok, err := Matches(path, back.After); err != nil || !ok {
				t.Errorf("the file does not match what the restore says it left: %v", err)
			}
			if _, err := (Writer{}).Restore(path, back.Before, back.After); err != nil {
				t.Fatalf("undoing the restore: %v", err)
			}
			want := maps.Clone(res.After)
			if tt.ext == ".wma" {
				delete(want, Marker) // the restore kept it, so undoing the restore had none to put back
			}
			if ok, err := Matches(path, want); err != nil || !ok {
				t.Errorf("the file does not match what the save left, after the restore was undone: %v", err)
			}
		})
	}
}

// A format that does not keep a number as it stood still gets its value
// back: MP4 holds the track as integers.
func TestRestoreNumberByValue(t *testing.T) {
	path := copyFixture(t, fixtures.Tagged(".m4a"), "song.m4a")
	res, err := Writer{}.Save(path, Values{Track: {"4/9"}}, marker.New(marker.Hand, day))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := res.Before[TrackStored]; ok {
		t.Fatalf("Before has %s: MP4 has one way to hold a number", TrackStored)
	}
	to := Values{Track: {"3/10"}, TrackStored: {"TRACKNUMBER=03/010"}, Marker: {}}
	back, err := Writer{}.Restore(path, to, res.After)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := back.After[TrackStored]; ok || !back.Changed {
		t.Errorf("Restore = %+v, want the number put back by its value", back)
	}
	if got := keysOf(t, path)[taglib.TrackNumber]; !slices.Equal(got, []string{"3/10"}) {
		t.Errorf("TRACKNUMBER = %q, want 3/10", got)
	}
	if l := leftovers(t, path); l != nil {
		t.Errorf("temporary files left behind: %v", l)
	}
}

// A stored form writes only the number's own keys, whatever else a record
// names in it.
func TestRestoreStoredFormKeepsToItsKeys(t *testing.T) {
	path := copyFixture(t, fixtures.Tagged(".flac"), "song.flac")
	res, err := Writer{}.Save(path, Values{Track: {"4/9"}}, marker.New(marker.Hand, day))
	if err != nil {
		t.Fatal(err)
	}
	to := Values{Track: {"3/10"}, TrackStored: {"TRACKNUMBER=3", "TRACKTOTAL=10", "TITLE=Other", "DISCNUMBER=9", "junk"}, Marker: {}}
	if _, err := (Writer{}).Restore(path, to, res.After); err != nil {
		t.Fatal(err)
	}
	keys := keysOf(t, path)
	if !slices.Equal(keys[taglib.Title], []string{fixtures.Title}) || !slices.Equal(keys[taglib.DiscNumber], []string{"1"}) {
		t.Errorf("title %q, disc %q; want them left alone", keys[taglib.Title], keys[taglib.DiscNumber])
	}
	if got, err := Read(path); err != nil || !slices.Equal(got[Track], []string{"3/10"}) {
		t.Errorf("track = %q, %v; want 3/10", got[Track], err)
	}
}

// An .aac file that had only tags tagshelf does not write, in its ID3v2
// tag, has no APEv2 tag again after the restore: there is nothing to read
// back from that side.
func TestRestoreAACWithoutAnAPETag(t *testing.T) {
	path := copyFixture(t, fixtures.Untagged(".aac"), "song.aac")
	if err := taglib.WriteTags(path, map[string][]string{"GENRE": {"Ambient"}}, 0); err != nil {
		t.Fatal(err)
	}
	keysBefore := keysOf(t, path)
	res, err := Writer{}.Save(path, Values{Title: {"X"}}, marker.New(marker.Hand, day))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(mustRead(t, path), []byte("APETAGEX")) {
		t.Fatal("the save wrote no APEv2 tag")
	}
	if _, err := (Writer{}).Restore(path, res.Before, res.After); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(mustRead(t, path), []byte("APETAGEX")) {
		t.Error("an APEv2 tag is left after the restore")
	}
	if got := keysOf(t, path); !reflect.DeepEqual(got, keysBefore) {
		t.Errorf("keys after the restore %q, want those before the save %q", got, keysBefore)
	}
}

// A file that had a manual marker gets that marker back.
func TestRestoreBringsBackTheMarker(t *testing.T) {
	for _, ext := range fixtures.ManualExtensions() {
		t.Run(ext, func(t *testing.T) {
			path := copyFixture(t, fixtures.Manual(ext), "song"+ext)
			keysBefore := keysOf(t, path)
			res, err := Writer{}.Save(path, musicBrainzSave(), marker.New(marker.MusicBrainz, day))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := (Writer{}).Restore(path, res.Before, res.After); err != nil {
				t.Fatal(err)
			}
			if got := markerOf(t, path); got != fixtures.ManualMarker {
				t.Errorf("marker after the restore = %q, want %q", got, fixtures.ManualMarker)
			}
			if got := keysOf(t, path); !reflect.DeepEqual(got, keysBefore) {
				t.Errorf("keys after the restore\n%q\nwant those before the save\n%q", got, keysBefore)
			}
		})
	}
}
