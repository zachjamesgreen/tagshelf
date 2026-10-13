package tui

import (
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/zachjamesgreen/tagshelf/internal/acoustid"
	"github.com/zachjamesgreen/tagshelf/internal/musicbrainz"
	"github.com/zachjamesgreen/tagshelf/internal/tagwrite"
)

// heardGeogaddi is a release match on the rip of three files, each of which
// AcoustID knows as a track of the album; the first is on a promo too.
func heardGeogaddi(t *testing.T) (*Model, *fakeMB, *fakeSound) {
	t.Helper()
	mb := geogaddi()
	album, promo := *mb.candidates[0].Release, *mb.candidates[1].Release
	mb.byID = map[string]musicbrainz.Recording{
		"rec-1": {ID: "rec-1", Title: "Ready Lets Go", Releases: []musicbrainz.Release{promo, album}},
		"rec-2": {ID: "rec-2", Title: "Music Is Math", Releases: []musicbrainz.Release{album}},
		"rec-3": {ID: "rec-3", Title: "Beware the Friendly Stranger", Releases: []musicbrainz.Release{album}},
	}
	m, _ := newUntaggedModel(t, matchSource())
	m.mb = mb
	one := func(aid, rec string) Heard {
		return Heard{Results: []acoustid.Result{{ID: aid, Score: 0.9, Recordings: []string{rec}}}}
	}
	sound := &fakeSound{by: map[string]Heard{
		"01 track.flac": one("aid-1", "rec-1"), "02 track.flac": one("aid-2", "rec-2"), "zz.flac": one("aid-3", "rec-3"),
	}}
	m.sound = sound
	cursorTo(t, m, 101)
	hit(t, m, "L")
	if m.match == nil || m.match.step != matchQuery {
		t.Fatalf("the release match did not open:\n%s", screen(m))
	}
	return m, mb, sound
}

// ctrl+s in a release match listens to every file. The releases that hold
// what AcoustID heard are listed first, the one that holds the most files
// first. On the release picked each file goes to the track AcoustID named
// for it, also one whose name and tags said nothing, and is saved with its
// AcoustID.
func TestReleaseMatchBySound(t *testing.T) {
	m, mb, sound := heardGeogaddi(t)
	hit(t, m, "ctrl+s")
	x := m.match
	if x.step != matchPick || len(sound.files) != 3 || len(mb.releaseQueries) != 0 {
		t.Fatalf("after ctrl+s: step %d, listened to %d files, searched %v\n%s", x.step, len(sound.files), mb.releaseQueries, screen(m))
	}
	if len(mb.asked) != 1 || len(mb.asked[0]) != 3 {
		t.Errorf("MusicBrainz was asked for %v, want the three recordings at once", mb.asked)
	}
	if len(x.releases) != 2 || x.releases[0].Release.ID != "rel-album" || x.releases[0].Found != 3 || x.releases[0].Score != 100 ||
		x.releases[1].Release.ID != "rel-other" || x.releases[1].Found != 1 || x.releases[1].Score != 33 || x.cursor != 0 {
		t.Fatalf("releases = %+v", x.releases)
	}
	got := screen(m)
	for _, want := range []string{"3 files › Releases", "by sound first: the score is the share of the files a release holds", "3/3", "1/3", "100", " 33", "ctrl+s by sound"} {
		if !strings.Contains(got, want) {
			t.Errorf("the releases lack %q:\n%s", want, got)
		}
	}

	hit(t, m, "enter")
	if x.step != matchMap {
		t.Fatalf("the release was not loaded:\n%s", screen(m))
	}
	got = screen(m)
	for _, want := range []string{"3 files on a track", "Beware the Friendly Stranger", "← zz.flac"} {
		if !strings.Contains(got, want) {
			t.Errorf("the mapping lacks %q:\n%s", want, got)
		}
	}
	if len(x.mapping.Assignments) != 3 || len(x.mapping.Unassigned) != 0 {
		t.Fatalf("mapping = %+v", x.mapping)
	}
	ids := map[string]string{}
	for _, a := range x.mapping.Assignments {
		ids[filepath.Base(x.files[a.File].Path)] = strings.Join(x.values(a)[tagwrite.AcoustID], ",")
	}
	if want := map[string]string{"01 track.flac": "aid-1", "02 track.flac": "aid-2", "zz.flac": "aid-3"}; !reflect.DeepEqual(ids, want) {
		t.Errorf("the AcoustIDs to be saved = %v, want %v", ids, want)
	}

	// A file moved by hand to a track AcoustID did not name for it gets none.
	a := x.mapping.Assignments[0]
	for _, ref := range x.release.Tracks() {
		if ref != a.Track {
			moved := musicbrainz.Pair(x.release, ref, x.infos[a.File])
			moved.File = a.File
			if got := x.acoustID(moved); got != "" {
				t.Errorf("on another track the file would be saved with the AcoustID %q", got)
			}
			break
		}
	}
}

// What the search finds follows what was heard, without the releases both
// name. One file that cannot be listened to is said and the rest go on;
// without a key nothing does.
func TestReleaseMatchBySoundJoinsTheSearchAndSaysItsProblems(t *testing.T) {
	m, mb, sound := heardGeogaddi(t)
	x := m.match
	extra := musicbrainz.Release{ID: "rel-box", Title: "Box Set", TrackCount: 40}
	mb.candidates = append(mb.candidates, musicbrainz.ReleaseCandidate{Score: 40, Release: &extra})
	hit(t, m, "enter")
	if x.step != matchPick || len(x.releases) != 3 {
		t.Fatalf("the search gave %d releases", len(x.releases))
	}
	hit(t, m, "ctrl+s")
	if len(x.releases) != 3 || x.releases[0].Found != 3 || x.releases[2].Release.ID != "rel-box" {
		t.Fatalf("releases = %+v, want the two by sound and then the box set", x.releases)
	}
	if !reflect.DeepEqual(sound.again, []bool{false, false, false}) {
		t.Errorf("again = %v", sound.again)
	}

	// The second time asks again; one file fails.
	sound.errs = map[string]error{"zz.flac": errors.New("it is not audio")}
	hit(t, m, "ctrl+s")
	if !reflect.DeepEqual(sound.again[3:], []bool{true, true, true}) {
		t.Errorf("the second time: again = %v", sound.again[3:])
	}
	if got := screen(m); x.step != matchPick || x.releases[0].Found != 2 || !strings.Contains(got, "1 file could not be listened to: it is not audio") {
		t.Errorf("with one file failing: step %d, releases %+v\n%s", x.step, x.releases, got)
	}

	// Every file failing is the error, where the match was.
	sound.errs = map[string]error{"zz.flac": errors.New("no"), "01 track.flac": errors.New("it is not audio"), "02 track.flac": errors.New("no")}
	hit(t, m, "/", "ctrl+s")
	if got := screen(m); x.step != matchQuery || !strings.Contains(got, "it is not audio") {
		t.Errorf("with every file failing: step %d\n%s", x.step, got)
	}

	// Without a key the first file ends it.
	sound.errs = map[string]error{"01 track.flac": acoustid.ErrNoKey}
	before := len(sound.files)
	hit(t, m, "ctrl+s")
	if got := screen(m); len(sound.files) != before+1 || !strings.Contains(got, "needs an AcoustID key") {
		t.Errorf("without a key: %d files listened to\n%s", len(sound.files)-before, got)
	}

	// Audio AcoustID does not know.
	sound.errs, sound.by = nil, nil
	hit(t, m, "ctrl+s")
	if got := screen(m); x.step != matchQuery || !strings.Contains(got, "AcoustID does not know the audio of these files") {
		t.Errorf("with nothing known: step %d\n%s", x.step, got)
	}

	// esc while it listens.
	sound.gate = newGate()
	h := start(t, m, "ctrl+s")
	<-sound.gate.entered
	if got := screen(m); x.step != matchListening || !strings.Contains(got, "Listening to the files and asking AcoustID: 1 of 3") || !strings.Contains(got, "esc stop waiting") {
		t.Fatalf("not listening:\n%s", got)
	}
	hit(t, m, "esc")
	h.answer(t, m)
	if x.step != matchQuery {
		t.Errorf("after esc: step %d", x.step)
	}
}

func TestSoundReleases(t *testing.T) {
	rel := func(id string, tracks int, official bool) musicbrainz.Release {
		r := musicbrainz.Release{ID: id, TrackCount: tracks}
		if official {
			r.Status, r.ReleaseGroup = "Official", musicbrainz.ReleaseGroup{PrimaryType: "Album"}
		}
		return r
	}
	recs := []musicbrainz.Recording{
		{ID: "R1", Releases: []musicbrainz.Release{rel("single", 1, false), rel("album", 10, true), rel("deluxe", 14, true), rel("boot", 10, false)}},
		{ID: "r2", Releases: []musicbrainz.Release{rel("album", 10, true), rel("deluxe", 14, true), rel("boot", 10, false), rel("owned", 30, false)}},
		{ID: "r3", Releases: []musicbrainz.Release{rel("elsewhere", 5, true)}},
	}
	heard := map[int]map[string]soundHit{0: {"r1": {}}, 1: {"r2": {}}, 2: {"r9": {}}}
	got := soundReleases(recs, heard, 10, map[string]bool{"owned": true})
	var order []string
	for _, c := range got {
		order = append(order, c.Release.ID)
	}
	// Both files: the official albums, the one of the files' number first,
	// then the bootleg. One file: the one in the library first.
	if want := []string{"album", "deluxe", "boot", "owned", "single"}; !reflect.DeepEqual(order, want) {
		t.Errorf("order = %v, want %v", order, want)
	}
	if got[0].Found != 2 || got[0].Searched != 10 || got[0].Score != 20 || !got[3].InLibrary {
		t.Errorf("candidates = %+v", got)
	}
}
