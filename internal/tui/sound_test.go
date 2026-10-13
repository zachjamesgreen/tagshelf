package tui

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/zachjamesgreen/tagshelf/internal/acoustid"
	"github.com/zachjamesgreen/tagshelf/internal/engine"
	"github.com/zachjamesgreen/tagshelf/internal/musicbrainz"
	"github.com/zachjamesgreen/tagshelf/internal/store"
	"github.com/zachjamesgreen/tagshelf/internal/tagwrite"
)

// fakeSound is an AcoustID that answers every file with heard, or with err.
// A file whose name is in by or errs is answered with that.
type fakeSound struct {
	heard Heard
	err   error
	by    map[string]Heard
	errs  map[string]error
	gate  *gate

	files []string
	again []bool
}

// Identify answers the files one after another, in their order.
func (f *fakeSound) Identify(ctx context.Context, files []store.File, again bool, each func(int, Heard, error) bool) {
	for i, file := range files {
		heard, err := f.identify(ctx, file, again)
		if ctx.Err() != nil || !each(i, heard, err) {
			return
		}
	}
}

func (f *fakeSound) identify(ctx context.Context, file store.File, again bool) (Heard, error) {
	f.files, f.again = append(f.files, file.Path), append(f.again, again)
	if err := f.gate.wait(ctx); err != nil {
		return Heard{}, err
	}
	name := filepath.Base(file.Path)
	if err := f.errs[name]; err != nil {
		return Heard{}, err
	}
	if heard, ok := f.by[name]; ok {
		return heard, nil
	}
	return f.heard, f.err
}

// heardJulie is a lookup on a file whose sound AcoustID knows: the song of
// julie, and with a lower score a recording the search does not find.
func heardJulie(t *testing.T) (*Model, *fakeRunner, *fakeMB, *fakeSound) {
	t.Helper()
	mb := julie()
	song := mb.pages[0].Recordings[0]
	other := musicbrainz.Recording{ID: "rec-9", Title: "Julie and Candy (demo)", Length: 300000, ArtistCredit: boards,
		Releases: []musicbrainz.Release{{ID: "rel-demo", Title: "Old Tunes", Status: "Bootleg"}}}
	mb.byID = map[string]musicbrainz.Recording{"rec-1": song, "rec-9": other}
	m, f, _ := lookupOn(t, 104, mb)
	sound := &fakeSound{heard: Heard{Asked: time.Now(), Results: []acoustid.Result{
		{ID: "aid-low", Score: 0.52, Recordings: []string{"rec-9", "REC-1"}},
		{ID: "aid-best", Score: 0.97, Recordings: []string{"rec-1"}},
		{ID: "aid-none", Score: 0.4},
	}}}
	m.sound = sound
	return m, f, mb, sound
}

// ctrl+s on the query listens to the file. What AcoustID heard is listed
// first, best first, with its score as a percentage, and nothing is picked.
// A match picked from it is saved with the AcoustID of the audio.
func TestLookupBySound(t *testing.T) {
	m, f, mb, sound := heardJulie(t)
	hit(t, m, "ctrl+s")
	l := m.look
	if l.step != stepPick || len(sound.files) != 1 || sound.files[0] != l.file.Path || sound.again[0] {
		t.Fatalf("after ctrl+s: step %d, listened to %v (again %v)\n%s", l.step, sound.files, sound.again, screen(m))
	}
	if !reflect.DeepEqual(mb.asked, [][]string{{"rec-1", "rec-9"}}) {
		t.Errorf("MusicBrainz was asked for %v, want the two recordings, the best first", mb.asked)
	}
	if len(mb.queries) != 0 {
		t.Errorf("a search was made: %v", mb.queries)
	}
	// The song on its two releases, then the demo.
	if len(l.matches) != 3 || l.bySound != 3 || l.matches[0].Recording.ID != "rec-1" || l.matches[2].Recording.ID != "rec-9" || l.cursor != 0 {
		t.Fatalf("matches = %+v, bySound %d, cursor %d", l.matches, l.bySound, l.cursor)
	}
	got := screen(m)
	for _, want := range []string{"97%", "52%", "by sound first, with AcoustID's score as %", "Julie and Candy (demo)", "ctrl+s by sound"} {
		if !strings.Contains(got, want) {
			t.Errorf("the matches lack %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "asks again") {
		t.Errorf("an answer just given is said to be old:\n%s", got)
	}

	// Picked and confirmed: the AcoustID of the best result goes in.
	for i, c := range l.matches {
		if c.Release != nil && c.Release.ID == "rel-album" {
			for range i {
				hit(t, m, "down")
			}
		}
	}
	hit(t, m, "enter")
	if l.step != stepConfirm {
		t.Fatalf("the match was not picked:\n%s", screen(m))
	}
	if got := m.lookupWrites(l)[tagwrite.AcoustID]; !reflect.DeepEqual(got, []string{"aid-best"}) {
		t.Errorf("the save would write the AcoustID %v, want that of the best result", got)
	}
	if got := screen(m); !strings.Contains(got, "AcoustID") || !strings.Contains(got, "aid-best") {
		t.Errorf("the diff does not show the AcoustID:\n%s", got)
	}
	f.saveFinish = engine.RunFinished{State: engine.StateOK, RunID: 3, Counters: engine.Counters{Organized: 1}}
	hit(t, m, "enter")
	if m.look != nil || len(f.saved) != 1 {
		t.Errorf("the match was not saved:\n%s", screen(m))
	}
}

// After a search, what AcoustID heard goes above what the search found, and
// a recording both name is listed once. A match of the search alone is
// saved without an AcoustID.
func TestLookupBySoundJoinsTheSearch(t *testing.T) {
	m, _, _, sound := heardJulie(t)
	hit(t, m, "enter")
	l := m.look
	if l.step != stepPick || len(l.matches) != 3 || l.bySound != 0 {
		t.Fatalf("the search gave %d matches, %d by sound", len(l.matches), l.bySound)
	}
	hit(t, m, "ctrl+s")
	// rec-1 twice and rec-9 by sound, then the search's rec-2.
	if len(l.matches) != 4 || l.bySound != 3 || l.matches[3].Recording.ID != "rec-2" {
		t.Fatalf("matches = %d, by sound %d, last %+v", len(l.matches), l.bySound, l.matches[len(l.matches)-1].Recording)
	}
	if _, ok := l.hit(3); ok {
		t.Error("the search's own match is taken for one AcoustID heard")
	}
	l.picked = l.matches[3]
	if _, ok := m.lookupWrites(l)[tagwrite.AcoustID]; ok {
		t.Error("a match AcoustID did not name would be saved with an AcoustID")
	}

	// A second time asks AcoustID again, whatever is on record.
	sound.heard.FromRecord, sound.heard.Asked = true, time.Now().Add(-72*time.Hour)
	hit(t, m, "ctrl+s")
	if !reflect.DeepEqual(sound.again, []bool{false, true}) {
		t.Errorf("again = %v, want the second time to ask again", sound.again)
	}
	if got := screen(m); !strings.Contains(got, "its answer is 3 days old, ctrl+s asks again") {
		t.Errorf("an answer from the record does not say its age:\n%s", got)
	}

	// A new search keeps what was heard.
	hit(t, m, "/", "enter")
	if l.step != stepPick || l.bySound != 3 || len(l.matches) != 4 {
		t.Errorf("after a new search: %d matches, %d by sound", len(l.matches), l.bySound)
	}
}

// What stands in the way is said where the lookup was, and esc stops the
// listening.
func TestLookupBySoundProblems(t *testing.T) {
	m, _, mb, sound := heardJulie(t)
	l := m.look

	sound.err = acoustid.ErrNoKey
	hit(t, m, "ctrl+s")
	if got := screen(m); l.step != stepQuery || !strings.Contains(got, "needs an AcoustID key") {
		t.Errorf("without a key: step %d\n%s", l.step, got)
	}
	if l.sound != nil {
		t.Error("a lookup that failed counts as asked")
	}

	sound.err, sound.heard.Results = nil, nil
	hit(t, m, "ctrl+s")
	if got := screen(m); l.step != stepQuery || !strings.Contains(got, "AcoustID does not know this audio") {
		t.Errorf("with no result: step %d\n%s", l.step, got)
	}

	sound.heard.Results = []acoustid.Result{{ID: "aid", Score: 0.9, Recordings: []string{"rec-gone"}}}
	hit(t, m, "ctrl+s")
	if got := screen(m); l.step != stepQuery || !strings.Contains(got, "no MusicBrainz recording is linked to it") {
		t.Errorf("with a recording MusicBrainz lacks: step %d\n%s", l.step, got)
	}

	sound.heard.Results = []acoustid.Result{{ID: "aid", Score: 0.9, Recordings: []string{"rec-1"}}}
	mb.err = errors.New("MusicBrainz is down")
	hit(t, m, "ctrl+s")
	if got := screen(m); l.step != stepQuery || !strings.Contains(got, "MusicBrainz is down") {
		t.Errorf("with MusicBrainz down: step %d\n%s", l.step, got)
	}
	mb.err = nil

	// esc while it listens: the answer that comes later is not taken.
	sound.gate = newGate()
	h := start(t, m, "ctrl+s")
	<-sound.gate.entered
	if l.step != stepListening || !strings.Contains(screen(m), "Listening to the file and asking AcoustID") || !strings.Contains(screen(m), "esc stop waiting") {
		t.Fatalf("not listening:\n%s", screen(m))
	}
	hit(t, m, "esc")
	h.answer(t, m)
	if l.step != stepQuery || l.bySound != 0 {
		t.Errorf("after esc: step %d, %d by sound", l.step, l.bySound)
	}

	// Without a way to listen, the key says so.
	sound.gate = nil
	m.sound = nil
	hit(t, m, "ctrl+s")
	if !strings.Contains(screen(m), "Identifying by sound is not available here") {
		t.Errorf("without a sound service:\n%s", screen(m))
	}
}

// A second ctrl+s that hears fewer recordings, or none, leaves the list no
// longer than its matches, and the cursor on one of them.
func TestLookupBySoundAgainWithFewerMatches(t *testing.T) {
	m, _, _, sound := heardJulie(t)
	hit(t, m, "ctrl+s", "down", "down")
	l := m.look
	if l.shown != 3 || l.cursor != 2 {
		t.Fatalf("after ctrl+s: %d shown, cursor %d", l.shown, l.cursor)
	}

	sound.heard.Results = []acoustid.Result{{ID: "aid-low", Score: 0.52, Recordings: []string{"rec-9"}}}
	hit(t, m, "ctrl+s")
	if l.step != stepPick || len(l.matches) != 1 || l.shown != 1 || l.cursor != 0 {
		t.Fatalf("with one recording heard: step %d, %d matches, %d shown, cursor %d", l.step, len(l.matches), l.shown, l.cursor)
	}
	if got := screen(m); !strings.Contains(got, "Julie and Candy (demo)") {
		t.Errorf("the match is not listed:\n%s", got)
	}

	// None at all, and nothing searched: there is no list to go back to.
	sound.heard.Results = nil
	hit(t, m, "ctrl+s")
	if l.step != stepQuery || len(l.matches) != 0 || l.shown != 0 || l.cursor != 0 {
		t.Errorf("with nothing heard: step %d, %d matches, %d shown, cursor %d", l.step, len(l.matches), l.shown, l.cursor)
	}
	if got := screen(m); !strings.Contains(got, "AcoustID does not know this audio") {
		t.Errorf("nothing heard is not said:\n%s", got)
	}
}

// A recording AcoustID named that MusicBrainz was not asked for, or did not
// give, is not among those heard: the search's match of it stays listed.
func TestLookupBySoundKeepsWhatTheSearchFoundOfTheRest(t *testing.T) {
	m, _, mb, sound := heardJulie(t)
	// AcoustID names the search's rec-2 too; MusicBrainz gives it no
	// recording for that ID.
	sound.heard.Results[0].Recordings = append(sound.heard.Results[0].Recordings, "rec-2")
	if _, known := mb.byID["rec-2"]; known {
		t.Fatal("the lookup by ID knows rec-2")
	}
	hit(t, m, "enter", "ctrl+s")
	l := m.look
	if len(l.matches) != 4 || l.bySound != 3 || l.matches[3].Recording.ID != "rec-2" {
		t.Fatalf("matches = %d, by sound %d", len(l.matches), l.bySound)
	}
	if _, ok := l.hit(3); ok {
		t.Error("the search's match is taken for one listed by sound")
	}
}

// A new search that fails, or is stopped, leaves what was heard listed.
func TestLookupBySoundOutlastsASearchThatFails(t *testing.T) {
	m, _, mb, sound := heardJulie(t)
	hit(t, m, "ctrl+s")
	l := m.look
	asked := len(sound.files)

	mb.err = errors.New("MusicBrainz is down")
	hit(t, m, "/", "enter")
	if got := screen(m); l.step != stepPick || l.bySound != 3 || len(l.matches) != 3 || l.shown != 3 || !strings.Contains(got, "MusicBrainz is down") || !strings.Contains(got, "97%") {
		t.Errorf("after a search that failed: step %d, %d matches, %d by sound, %d shown\n%s", l.step, len(l.matches), l.bySound, l.shown, got)
	}
	mb.err = nil

	mb.gate = newGate()
	hit(t, m, "/")
	h := start(t, m, "enter")
	<-mb.gate.entered
	hit(t, m, "esc")
	h.answer(t, m)
	mb.gate = nil
	if l.step != stepPick || l.bySound != 3 || l.shown != 3 {
		t.Errorf("after a search that was stopped: step %d, %d by sound, %d shown", l.step, l.bySound, l.shown)
	}
	if len(sound.files) != asked {
		t.Errorf("AcoustID was asked again: %d times", len(sound.files)-asked)
	}

	// And one that goes through puts its matches after them.
	hit(t, m, "/", "enter")
	if l.step != stepPick || l.bySound != 3 || len(l.matches) != 4 || l.shown != 4 {
		t.Errorf("after a search: step %d, %d matches, %d by sound, %d shown", l.step, len(l.matches), l.bySound, l.shown)
	}
}

// An answer from the record that names nothing says how old it is too.
func TestLookupBySoundSaysTheAgeOfAnEmptyAnswer(t *testing.T) {
	m, _, _, sound := heardJulie(t)
	sound.heard = Heard{FromRecord: true, Asked: time.Now().Add(-72 * time.Hour)}
	hit(t, m, "ctrl+s")
	if got := screen(m); !strings.Contains(got, "AcoustID does not know this audio; its answer is 3 days old, ctrl+s asks again") {
		t.Errorf("an empty answer from the record does not say its age:\n%s", got)
	}
	sound.heard.FromRecord = false
	hit(t, m, "ctrl+s")
	if got := screen(m); !sound.again[1] || !strings.Contains(got, "AcoustID does not know this audio") || strings.Contains(got, "asks again") {
		t.Errorf("an answer just given (again %v):\n%s", sound.again, got)
	}
}

func TestSoundHits(t *testing.T) {
	hits, ids := soundHits([]acoustid.Result{
		{ID: "a", Score: 0.5, Recordings: []string{"R1", "r2"}},
		{ID: "b", Score: 0.9, Recordings: []string{"r1"}},
		{ID: "c", Score: 0.9},
	})
	if !reflect.DeepEqual(ids, []string{"r1", "r2"}) || hits["r1"] != (soundHit{0.9, "b"}) || hits["r2"] != (soundHit{0.5, "a"}) {
		t.Errorf("soundHits = %v, %v", hits, ids)
	}
	for score, want := range map[float64]string{0.974: "97%", 1: "100", 0.05: "5%", 0.999: "100", -1: "0%"} {
		if got := percent(score); got != want {
			t.Errorf("percent(%v) = %q, want %q", score, got, want)
		}
	}
	for d, want := range map[time.Duration]string{time.Hour: "under two hours", 5 * time.Hour: "5 hours", 30 * time.Hour: "a day", 72 * time.Hour: "3 days"} {
		if got := age(d); got != want {
			t.Errorf("age(%v) = %q, want %q", d, got, want)
		}
	}
}
