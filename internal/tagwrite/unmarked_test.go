package tagwrite

import (
	"bytes"
	"reflect"
	"slices"
	"testing"

	"github.com/zachjamesgreen/tagshelf/internal/fixtures"
	"github.com/zachjamesgreen/tagshelf/internal/marker"
	"github.com/zachjamesgreen/tagshelf/internal/tag"
)

// described are a genre list, a composer list and a comment: the tags that
// say nothing of where a file belongs.
func described() Values {
	return Values{Genre: {"Rock", "Hip Hop"}, Composer: {"Ann Lee", "Bob"}, Comment: {"a note, with a comma"}}
}

// TestGenreComposerComment saves the three to a file of every format without
// the marker, and takes them away again: each list reads back whole and in
// order by both readers, the audio is the same, the file gets no marker, and
// a second save of the same values writes nothing.
func TestGenreComposerComment(t *testing.T) {
	for ext, from := range taggableExtensions {
		t.Run(ext, func(t *testing.T) {
			path := copyFixture(t, tagged(t, from), "song"+ext)
			audio := audioSum(t, path)
			want := described()

			res, err := Writer{}.SaveUnmarked(path, want)
			if err != nil {
				t.Fatal(err)
			}
			wantBefore := Values{Genre: {}, Composer: {}, Comment: {}}
			if !res.Changed || !reflect.DeepEqual(res.Before, wantBefore) || !reflect.DeepEqual(res.After, want) {
				t.Errorf("SaveUnmarked = %+v, want changed from %v to %v: the marker is no part of it", res, wantBefore, want)
			}
			got, err := Read(path)
			if err != nil {
				t.Fatal(err)
			}
			for field, vs := range want {
				if !slices.Equal(got[field], vs) {
					t.Errorf("%s reads %q, want %q", field, got[field], vs)
				}
			}
			if len(got[Marker]) != 0 {
				t.Errorf("the file got the marker %q", got[Marker])
			}
			if got[Title][0] != fixtures.Title || got[RecordingID][0] != fixtures.RecordingID {
				t.Errorf("the other tags changed: %v", got)
			}
			md := read(t, path)
			if genres, composers := tag.Genres(md.Raw()), tag.Composers(md.Raw()); !slices.Equal(genres, want[Genre]) || !slices.Equal(composers, want[Composer]) {
				t.Errorf("the tag reader lists genres %q and composers %q", genres, composers)
			}
			if md.Comment() != want[Comment][0] {
				t.Errorf("the tag reader's comment is %q", md.Comment())
			}
			if now := audioSum(t, path); now != audio {
				t.Error("the audio changed")
			}

			written := mustRead(t, path)
			if res, err := (Writer{}).SaveUnmarked(path, want); err != nil || res.Changed {
				t.Errorf("a second save of the same values: changed %t, %v", res.Changed, err)
			}
			if !bytes.Equal(mustRead(t, path), written) {
				t.Error("the second save wrote the file")
			}

			// One genre in place of two, and the other two taken away.
			less := Values{Genre: {"Jazz"}, Composer: {}, Comment: {}}
			if _, err := (Writer{}).SaveUnmarked(path, less); err != nil {
				t.Fatal(err)
			}
			if got, err := Read(path); err != nil || !slices.Equal(got[Genre], less[Genre]) || len(got[Composer]) != 0 || len(got[Comment]) != 0 {
				t.Errorf("after taking them away: %v, %v", got, err)
			}
			if md := read(t, path); len(tag.Composers(md.Raw())) != 0 || md.Comment() != "" {
				t.Errorf("the tag reader still finds composers %q, comment %q", tag.Composers(md.Raw()), md.Comment())
			}
		})
	}
}

// TestSaveUnmarkedKeepsTheMarker checks that a file which has a marker keeps
// it as it was through a save without one, and that such a save can be
// undone without the marker coming into it.
func TestSaveUnmarkedKeepsTheMarker(t *testing.T) {
	for _, ext := range fixtures.ManualExtensions() {
		t.Run(ext, func(t *testing.T) {
			path := copyFixture(t, fixtures.Manual(ext), "song"+ext)
			orig, err := Read(path)
			if err != nil {
				t.Fatal(err)
			}

			res, err := Writer{}.SaveUnmarked(path, described())
			if err != nil {
				t.Fatal(err)
			}
			if _, ok := res.Before[Marker]; ok {
				t.Errorf("the record of the save names the marker: %v", res.Before)
			}
			if got := markerOf(t, path); got != fixtures.ManualMarker {
				t.Errorf("the marker is %q after the save, want %q as before", got, fixtures.ManualMarker)
			}

			if _, err := (Writer{}).Restore(path, res.Before, res.After); err != nil {
				t.Fatal(err)
			}
			back, err := Read(path)
			if err != nil || !reflect.DeepEqual(back, orig) {
				t.Errorf("after the undo the file reads\n%v, want\n%v (%v)", back, orig, err)
			}
		})
	}
}

// TestSaveWithTheMarkerTakesTheThreeToo checks that a save of a title can
// carry a genre: one save, one record, and the marker with it.
func TestSaveWithTheMarkerTakesTheThreeToo(t *testing.T) {
	path := copyFixture(t, fixtures.Tagged(".mp3"), "song.mp3")
	values := described()
	values[Title] = []string{"New Title"}
	if _, err := (Writer{}).Save(path, values, marker.New(marker.Hand, day)); err != nil {
		t.Fatal(err)
	}
	got, err := Read(path)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got[Genre], values[Genre]) || got[Title][0] != "New Title" || len(got[Marker]) != 1 {
		t.Errorf("the file reads %v", got)
	}
}

// TestPlaces checks which changes mark a file as manually tagged: those to a
// tag its place in the library goes by, or to a MusicBrainz ID.
func TestPlaces(t *testing.T) {
	for _, field := range []Field{Title, Artist, AlbumArtist, Album, Date, Track, Disc, Compilation,
		RecordingID, ReleaseTrackID, AlbumID, ReleaseGroupID, ArtistIDs, AlbumArtistIDs} {
		if !Places(Values{Genre: {"Rock"}, field: {}}) {
			t.Errorf("a change to %s does not mark the file", field)
		}
	}
	if Places(described()) {
		t.Error("a change to genre, composer and comment alone marks the file")
	}
	if Places(nil) {
		t.Error("no change at all marks the file")
	}
}
