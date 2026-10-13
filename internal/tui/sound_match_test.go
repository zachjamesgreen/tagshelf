package tui

import (
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

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

// A file AcoustID names two of the release's recordings for goes on the
// track it is surest of, and leaves the other to the file that is that
// track.
func TestReleaseMatchBySoundGoesByTheScore(t *testing.T) {
	m, _, sound := heardGeogaddi(t)
	sound.by["01 track.flac"] = Heard{Results: []acoustid.Result{
		{ID: "aid-reprise", Score: 0.95, Recordings: []string{"rec-2"}},
		{ID: "aid-low", Score: 0.52, Recordings: []string{"rec-1"}},
	}}
	sound.by["02 track.flac"] = Heard{Results: []acoustid.Result{{ID: "aid-song", Score: 0.9, Recordings: []string{"rec-1"}}}}
	hit(t, m, "ctrl+s", "enter")
	x := m.match
	if x.step != matchMap || len(x.mapping.Assignments) != 3 {
		t.Fatalf("the release was not loaded, or not every file is on a track:\n%s", screen(m))
	}
	got := map[string]string{}
	for _, a := range x.mapping.Assignments {
		got[filepath.Base(x.files[a.File].Path)] = x.release.Track(a.Track).Recording.ID + " " + x.acoustID(a)
	}
	want := map[string]string{"01 track.flac": "rec-2 aid-reprise", "02 track.flac": "rec-1 aid-song", "zz.flac": "rec-3 aid-3"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("the files are on %v, want %v", got, want)
	}
}

// Only the best few recordings of a file are asked of MusicBrainz, but the
// file goes on the track of any recording AcoustID named for it.
func TestReleaseMatchBySoundPlacesByEveryRecordingNamed(t *testing.T) {
	m, mb, sound := heardGeogaddi(t)
	named := []string{"x-1", "x-2", "x-3", "x-4", "x-5", "x-6", "x-7", "rec-3"}
	sound.by["zz.flac"] = Heard{Results: []acoustid.Result{{ID: "aid-3", Score: 0.98, Recordings: named}}}
	hit(t, m, "ctrl+s")
	x := m.match
	if len(mb.asked) != 1 || len(mb.asked[0]) != 2+maxHeardEach || slices.Contains(mb.asked[0], "rec-3") {
		t.Fatalf("MusicBrainz was asked for %v, want the best %d of the file with many", mb.asked, maxHeardEach)
	}
	if x.step != matchPick || x.releases[0].Release.ID != "rel-album" || x.releases[0].Found != 2 {
		t.Fatalf("releases = %+v", x.releases)
	}
	hit(t, m, "enter")
	for _, a := range x.mapping.Assignments {
		if filepath.Base(x.files[a.File].Path) == "zz.flac" {
			if rec := x.release.Track(a.Track).Recording.ID; rec != "rec-3" || x.acoustID(a) != "aid-3" {
				t.Errorf("zz.flac is on the track of %s with the AcoustID %q", rec, x.acoustID(a))
			}
			return
		}
	}
	t.Errorf("zz.flac is on no track: %+v", x.mapping)
}

// AcoustID busy, slow or out of reach ends the listening at that file: the
// ones after it would fare the same.
func TestReleaseMatchBySoundStopsWhenAcoustIDIsDown(t *testing.T) {
	for _, down := range []error{acoustid.ErrBusy, acoustid.ErrTimeout, fmt.Errorf("%w: no route to host", acoustid.ErrUnreachable)} {
		m, mb, sound := heardGeogaddi(t)
		sound.errs = map[string]error{"01 track.flac": down}
		hit(t, m, "ctrl+s")
		x := m.match
		if got := screen(m); len(sound.files) != 1 || x.step != matchQuery || x.heard != nil || len(mb.asked) != 0 || !strings.Contains(got, down.Error()) {
			t.Errorf("with %v: %d files listened to, step %d\n%s", down, len(sound.files), x.step, got)
		}
	}
}

// After files that failed, ctrl+s tries again without asking AcoustID anew
// for the ones it answered; once all were answered, it asks anew.
func TestReleaseMatchBySoundTriesTheFailedAgain(t *testing.T) {
	m, _, sound := heardGeogaddi(t)
	sound.errs = map[string]error{"zz.flac": errors.New("it is not audio")}
	hit(t, m, "ctrl+s")
	x := m.match
	if got := screen(m); x.failed != 1 || !strings.Contains(got, "1 file could not be listened to: it is not audio") {
		t.Fatalf("with one file failing: failed %d\n%s", x.failed, got)
	}
	sound.errs = nil
	hit(t, m, "ctrl+s")
	if !reflect.DeepEqual(sound.again, []bool{false, false, false, false, false, false}) {
		t.Errorf("after a file failed: again = %v, want none asked anew", sound.again)
	}
	if x.failed != 0 || x.releases[0].Found != 3 {
		t.Errorf("after the retry: failed %d, releases %+v", x.failed, x.releases)
	}
	hit(t, m, "ctrl+s")
	if !reflect.DeepEqual(sound.again[6:], []bool{true, true, true}) {
		t.Errorf("after all were answered: again = %v, want all asked anew", sound.again[6:])
	}

	// A file that failed is said even when AcoustID knows none of the rest.
	sound.by, sound.errs = nil, map[string]error{"zz.flac": errors.New("it is not audio")}
	hit(t, m, "ctrl+s")
	if got := screen(m); !strings.Contains(got, "1 file could not be listened to: it is not audio") {
		t.Errorf("a failure beside files AcoustID does not know:\n%s", got)
	}
}

// A second ctrl+s that finds no release by sound leaves the cursor on a
// release there is, or the screen for the query when there is none.
func TestReleaseMatchBySoundAgainWithNothing(t *testing.T) {
	m, mb, sound := heardGeogaddi(t)
	hit(t, m, "ctrl+s", "down")
	x := m.match
	if x.step != matchPick || x.cursor != 1 {
		t.Fatalf("after ctrl+s: step %d, cursor %d", x.step, x.cursor)
	}
	sound.by = nil
	hit(t, m, "ctrl+s")
	if got := screen(m); x.step != matchQuery || len(x.releases) != 0 || x.cursor != 0 || !strings.Contains(got, "AcoustID does not know the audio of these files") {
		t.Errorf("with nothing known and nothing searched: step %d, %d releases, cursor %d\n%s", x.step, len(x.releases), x.cursor, got)
	}

	// With one release from the search, the cursor is on it.
	mb.candidates = mb.candidates[:1]
	sound.by = map[string]Heard{"01 track.flac": {Results: []acoustid.Result{{ID: "aid-1", Score: 0.9, Recordings: []string{"rec-1"}}}}}
	hit(t, m, "enter", "ctrl+s", "down")
	if len(x.releases) != 2 || x.cursor != 1 {
		t.Fatalf("by sound after a search: %d releases, cursor %d", len(x.releases), x.cursor)
	}
	sound.by = nil
	hit(t, m, "ctrl+s")
	if x.step != matchPick || len(x.releases) != 1 || x.cursor != 0 {
		t.Errorf("with nothing known after a search: step %d, %d releases, cursor %d", x.step, len(x.releases), x.cursor)
	}
	hit(t, m, "enter")
	if x.step != matchMap {
		t.Errorf("the release under the cursor could not be picked: step %d", x.step)
	}
}

// Answers taken from the record say how old the oldest is.
func TestReleaseMatchBySoundSaysTheAgeOfAnswersOnRecord(t *testing.T) {
	m, _, sound := heardGeogaddi(t)
	hit(t, m, "ctrl+s")
	if got := screen(m); strings.Contains(got, "asks again") {
		t.Errorf("answers just given are said to be old:\n%s", got)
	}
	for name, ago := range map[string]time.Duration{"01 track.flac": 30 * time.Hour, "zz.flac": 72 * time.Hour} {
		heard := sound.by[name]
		heard.FromRecord, heard.Asked = true, time.Now().Add(-ago)
		sound.by[name] = heard
	}
	m.match.heard = nil // as when the match is opened anew
	hit(t, m, "/", "ctrl+s")
	if got := screen(m); !strings.Contains(got, "by sound first, by the share of the files held; answers up to 3 days old, ctrl+s asks again") {
		t.Errorf("answers from the record do not say their age:\n%s", got)
	}

	// Nothing known, by the record: that says its age too.
	for name := range sound.by {
		sound.by[name] = Heard{FromRecord: true, Asked: time.Now().Add(-5 * time.Hour)}
	}
	m.match.heard = nil
	hit(t, m, "/", "ctrl+s")
	if got := screen(m); !strings.Contains(got, "AcoustID does not know the audio of these files; answers up to 5 hours old, ctrl+s asks again") {
		t.Errorf("nothing known by the record does not say its age:\n%s", got)
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
