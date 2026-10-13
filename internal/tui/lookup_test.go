package tui

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/zachjamesgreen/tagshelf/internal/engine"
	"github.com/zachjamesgreen/tagshelf/internal/fixtures"
	"github.com/zachjamesgreen/tagshelf/internal/musicbrainz"
	"github.com/zachjamesgreen/tagshelf/internal/store"
	"github.com/zachjamesgreen/tagshelf/internal/tagwrite"
)

// fakeMB is a MusicBrainz that answers what the test gave it.
type fakeMB struct {
	pages map[int]*musicbrainz.RecordingSearch // by offset
	// release is what a lookup of any release answers, unless releases has
	// one for the ID asked.
	release  *musicbrainz.Release
	releases map[string]*musicbrainz.Release
	err      error
	// gate, when set, holds every request back.
	gate *gate

	// What a release match is answered with, and what it asked.
	candidates     []musicbrainz.ReleaseCandidate
	releaseQueries []musicbrainz.ReleaseQuery
	boosts         [][]musicbrainz.Query

	queries []musicbrainz.Query
	offsets []int
	looked  []string
}

func (f *fakeMB) SearchRecordings(ctx context.Context, q musicbrainz.Query, offset int) (*musicbrainz.RecordingSearch, error) {
	f.queries, f.offsets = append(f.queries, q), append(f.offsets, offset)
	if err := cmp.Or(f.gate.wait(ctx), f.err); err != nil {
		return nil, err
	}
	if p := f.pages[offset]; p != nil {
		return p, nil
	}
	return &musicbrainz.RecordingSearch{}, nil
}

func (f *fakeMB) SearchReleaseCandidates(ctx context.Context, q musicbrainz.ReleaseQuery, boost []musicbrainz.Query, inLibrary func(string) bool) ([]musicbrainz.ReleaseCandidate, error) {
	f.releaseQueries, f.boosts = append(f.releaseQueries, q), append(f.boosts, boost)
	if err := cmp.Or(f.gate.wait(ctx), f.err); err != nil {
		return nil, err
	}
	return f.candidates, nil
}

func (f *fakeMB) LookupRelease(ctx context.Context, id string) (*musicbrainz.Release, error) {
	f.looked = append(f.looked, id)
	if err := cmp.Or(f.gate.wait(ctx), f.err); err != nil {
		return nil, err
	}
	if rel := f.releases[id]; rel != nil {
		return rel, nil
	}
	return f.release, nil
}

var boards = []musicbrainz.ArtistCredit{{Name: "Boards of Canada", Artist: musicbrainz.Artist{ID: "artist-1", Name: "Boards of Canada"}}}

// julie is a MusicBrainz that knows one song: on its album, on a compilation
// the library already has, and as a recording on no release.
func julie() *fakeMB {
	onAlbum := musicbrainz.Release{ID: "rel-album", Title: "Geogaddi", Status: "Official", Date: "2002-02-18", Country: "GB",
		ReleaseGroup: musicbrainz.ReleaseGroup{ID: "rg-1", PrimaryType: "Album"},
		Media:        []musicbrainz.Medium{{Position: 1, Format: "CD", TrackCount: 23, TrackOffset: 4, Track: []musicbrainz.Track{{Length: 330000}}}}}
	onComp := musicbrainz.Release{ID: "rel-comp", Title: "Warp 20", Status: "Official", Date: "2009", Country: "XW",
		ReleaseGroup: musicbrainz.ReleaseGroup{PrimaryType: "Album", SecondaryTypes: []string{"Compilation"}},
		Media:        []musicbrainz.Medium{{Position: 2, Format: "Digital Media", TrackCount: 12, TrackOffset: 10, Track: []musicbrainz.Track{{Length: 333000}}}}}
	full := onAlbum
	full.ArtistCredit = boards
	full.Media = []musicbrainz.Medium{{Position: 1, Format: "CD", TrackCount: 23, Tracks: []musicbrainz.Track{
		{ID: "track-4", Position: 4, Title: "Sunshine Recorder", ArtistCredit: boards, Recording: &musicbrainz.TrackRecording{ID: "rec-other"}},
		{ID: "track-5", Position: 5, Title: "Julie and Candy", Length: 330000, ArtistCredit: boards, Recording: &musicbrainz.TrackRecording{ID: "rec-1"}},
	}}}
	return &fakeMB{
		pages: map[int]*musicbrainz.RecordingSearch{0: {Count: 2, Recordings: []musicbrainz.Recording{
			{ID: "rec-1", Title: "Julie and Candy", Length: 330000, ArtistCredit: boards, Releases: []musicbrainz.Release{onComp, onAlbum}},
			{ID: "rec-2", Title: "Julie and Candy (live)", Length: 250000, ArtistCredit: boards},
		}}},
		release: &full,
	}
}

// lookupOn opens the lookup from the Untagged tab on the file with the given
// ID.
func lookupOn(t *testing.T, id int64, mb MusicBrainz) (*Model, *fakeRunner, *fakeSource) {
	t.Helper()
	src := untaggedSource()
	src.releases = map[string]bool{"rel-comp": true}
	m, f := newUntaggedModel(t, src)
	if mb != nil {
		m.mb = mb
	}
	cursorTo(t, m, id)
	cur, _ := m.untagged.current()
	src.tags = map[string]tagwrite.Values{cur.Path: {tagwrite.Title: {cur.Title}, tagwrite.Artist: {cur.Artist}}}
	hit(t, m, "l")
	if m.look == nil {
		t.Fatalf("the lookup did not open:\n%s", screen(m))
	}
	return m, f, src
}

// toAlbum searches with the query as it stands and moves the cursor to the
// match on the album.
func toAlbum(t *testing.T, m *Model) {
	t.Helper()
	hit(t, m, "enter")
	for i, c := range m.look.matches {
		if c.Release != nil && c.Release.ID == "rel-album" {
			for range i {
				hit(t, m, "down")
			}
			return
		}
	}
	t.Fatalf("no match on the album:\n%s", screen(m))
}

func TestLookupTagsAndMovesAFile(t *testing.T) {
	mb := julie()
	m, f, _ := lookupOn(t, 104, mb)
	got := screen(m)
	for _, want := range []string{"Untagged › Look up › live.mp3", "Search MusicBrainz for", "> Live", "> Hand Tagged", "enter search"} {
		if !strings.Contains(got, want) {
			t.Errorf("the query step lacks %q:\n%s", want, got)
		}
	}

	// Fix the query: another title, and a length.
	hit(t, m, "backspace", "backspace", "backspace", "backspace")
	typeText(t, m, "Julie and Candy")
	hit(t, m, "down", "down", "down")
	typeText(t, m, "5:30")
	hit(t, m, "enter")
	if len(mb.queries) != 1 || mb.queries[0].Title != "Julie and Candy" || mb.queries[0].Artist != "Hand Tagged" || mb.queries[0].Length != 330*time.Second {
		t.Fatalf("searched for %+v", mb.queries)
	}
	got = screen(m)
	for _, want := range []string{
		"Look up › live.mp3 › Matches", "3 of 3 matches", "Julie and Candy", "Boards of Canada",
		"Geogaddi", "2002", "GB", "CD", "5/23", "+0:00",
		"Warp 20 (in library)", "2009", "Digital Media", "2-11/12", "+0:03", "-1:20",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the matches lack %q:\n%s", want, got)
		}
	}
	// The official album outranks the compilation the library has.
	if len(m.look.matches) == 0 {
		t.Fatal("no matches to pick from")
	}
	if c := m.look.matches[0]; c.Release == nil || c.Release.ID != "rel-album" {
		t.Fatalf("first match = %+v, want the album", c.Release)
	}

	hit(t, m, "enter")
	if !reflect.DeepEqual(mb.looked, []string{"rel-album"}) {
		t.Fatalf("looked up %v", mb.looked)
	}
	got = screen(m)
	for _, want := range []string{
		"Matches › Confirm", "Tag it as Julie and Candy by Boards of Canada, on Geogaddi",
		"Live", "-> Julie and Candy", "Hand Tagged", "-> Boards of Canada", "-> Geogaddi", "-> 2002-02-18", "-> 5/23", "-> 1/1",
		"-> rec-1", "-> track-5", "-> rel-album", "-> rg-1", "-> artist-1",
		"musicbrainz, today",
		"Will go to:    " + filepath.Join("Boards of Canada", "Geogaddi (2002)", "05 - Julie and Candy.mp3"),
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the diff lacks %q:\n%s", want, got)
		}
	}

	dst := p(`D:\Music\Boards of Canada\Geogaddi (2002)\05 - Julie and Candy.mp3`)
	f.saveEvents = []engine.Event{engine.FilePlaced{Path: m.look.file.Path, Destination: dst, Outcome: engine.Organized}}
	f.saveFinish = engine.RunFinished{State: engine.StateOK, Counters: engine.Counters{Organized: 1}}
	hit(t, m, "enter")
	if !reflect.DeepEqual(f.saved, [][]int64{{104}}) {
		t.Errorf("saved = %v, want file 104", f.saved)
	}
	if m.look != nil || !m.toastOK || !strings.Contains(m.toast, "Saved and moved to "+filepath.Join("Boards of Canada", "Geogaddi (2002)")) {
		t.Errorf("after confirming: lookup open %t, toast %q (ok %t)", m.look != nil, m.toast, m.toastOK)
	}
}

func TestLookupEscStepsBackAndTheQueryRunsAgain(t *testing.T) {
	mb := julie()
	m, f, _ := lookupOn(t, 104, mb)
	toAlbum(t, m)
	hit(t, m, "enter")
	if m.look.step != stepConfirm {
		t.Fatalf("step = %d, want the diff:\n%s", m.look.step, screen(m))
	}
	hit(t, m, "esc")
	if m.look.step != stepPick {
		t.Fatalf("esc on the diff: step = %d, want the matches", m.look.step)
	}
	hit(t, m, "esc")
	if m.look.step != stepQuery || !strings.Contains(screen(m), "> Live") {
		t.Fatalf("esc on the matches: step = %d, want the query as it was:\n%s", m.look.step, screen(m))
	}
	typeText(t, m, " at Home")
	hit(t, m, "enter")
	if len(mb.queries) != 2 || mb.queries[1].Title != "Live at Home" || m.look.step != stepPick {
		t.Fatalf("the second search: %+v, step %d", mb.queries, m.look.step)
	}
	hit(t, m, "esc", "esc")
	if m.look != nil || len(f.saved) != 0 {
		t.Errorf("esc on the query: lookup open %t, saved %v", m.look != nil, f.saved)
	}
	if !strings.Contains(screen(m), "2 Untagged") {
		t.Errorf("the tabs are not back:\n%s", screen(m))
	}
}

func TestLookupWithoutAContactExplains(t *testing.T) {
	m, _, _ := lookupOn(t, 104, nil)
	got := screen(m)
	for _, want := range []string{"MusicBrainz lookups are off", "musicbrainz_contact: you@example.com", "config file", "ctrl+e edit the tags by hand"} {
		if !strings.Contains(got, want) {
			t.Errorf("the explanation lacks %q:\n%s", want, got)
		}
	}
	checkFits(t, got, 120, 32)
	hit(t, m, "enter", "x", "ctrl+e")
	if m.look != nil || m.edit == nil || m.edit.form == nil {
		t.Fatalf("ctrl+e did not open the editor: lookup open %t, editor open %t", m.look != nil, m.edit != nil)
	}
	hit(t, m, "esc", "l", "esc")
	if m.look != nil || m.edit != nil {
		t.Error("esc did not close the explanation")
	}
}

func TestLookupShowsNetworkErrors(t *testing.T) {
	mb := julie()
	mb.err = errors.New("MusicBrainz recording search: dial tcp: no route to host")
	m, _, _ := lookupOn(t, 104, mb)
	hit(t, m, "enter")
	if got := screen(m); m.look.step != stepQuery || !strings.Contains(got, "no route to host") || !strings.Contains(got, "> Live") {
		t.Fatalf("a failed search: step %d\n%s", m.look.step, got)
	}
	// Searching again works once the network does.
	mb.err = nil
	hit(t, m, "enter")
	if m.look.step != stepPick || strings.Contains(screen(m), "no route to host") {
		t.Fatalf("the second search: step %d\n%s", m.look.step, screen(m))
	}
	// A release that cannot be read leaves the matches to pick from again.
	mb.err = musicbrainz.ErrUnavailable
	hit(t, m, "down", "enter")
	if got := screen(m); m.look.step != stepPick || !strings.Contains(got, "not accepting requests") {
		t.Fatalf("a failed release lookup: step %d\n%s", m.look.step, got)
	}
	// A recording on no release cannot be picked.
	hit(t, m, "up", "enter")
	if got := screen(m); m.look.step != stepPick || !strings.Contains(got, "on no release") {
		t.Errorf("picking a recording on no release: step %d\n%s", m.look.step, got)
	}

	// Nothing typed is nothing to search for, and a bad length is said.
	m, _, _ = lookupOn(t, 103, julie())
	m.look.title, m.look.artist, m.look.album = "", " ", ""
	hit(t, m, "enter")
	if !strings.Contains(screen(m), "Nothing to search for") {
		t.Errorf("an empty query:\n%s", screen(m))
	}
	typeText(t, m, "x")
	hit(t, m, "down", "down", "down")
	typeText(t, m, "abc")
	hit(t, m, "enter")
	if !strings.Contains(screen(m), "a length is m:ss") {
		t.Errorf("a bad length:\n%s", screen(m))
	}
}

func TestLookupMoreShowsAndFetchesMatches(t *testing.T) {
	page := func(first, n, total int) *musicbrainz.RecordingSearch {
		s := &musicbrainz.RecordingSearch{Count: total, Offset: first}
		for i := first; i < first+n; i++ {
			rel := func(k int) musicbrainz.Release {
				return musicbrainz.Release{ID: fmt.Sprintf("rel-%d-%d", i, k), Title: fmt.Sprintf("Album %d", k)}
			}
			s.Recordings = append(s.Recordings, musicbrainz.Recording{ID: fmt.Sprintf("rec-%d", i), Title: fmt.Sprintf("Live %02d", i),
				Releases: []musicbrainz.Release{rel(1), rel(2)}})
		}
		return s
	}
	mb := &fakeMB{pages: map[int]*musicbrainz.RecordingSearch{0: page(0, 25, 30), 25: page(25, 5, 30)}}
	m, _, _ := lookupOn(t, 104, mb)
	hit(t, m, "enter")
	if got := screen(m); m.look.shown != 25 || !strings.Contains(got, "25 of 50 matches; m shows more") {
		t.Fatalf("the first page: %d shown\n%s", m.look.shown, got)
	}
	checkFits(t, screen(m), 120, 32)
	hit(t, m, "m")
	if m.look.shown != 50 || len(mb.offsets) != 1 {
		t.Fatalf("m with matches in hand: %d shown, fetched %v", m.look.shown, mb.offsets)
	}
	hit(t, m, "m")
	if !reflect.DeepEqual(mb.offsets, []int{0, 25}) || len(m.look.matches) != 60 || m.look.shown != 60 || m.look.step != stepPick {
		t.Fatalf("m past the matches in hand: fetched %v, %d of %d shown", mb.offsets, m.look.shown, len(m.look.matches))
	}
	hit(t, m, "m")
	if len(mb.offsets) != 2 || !strings.Contains(screen(m), "no more matches") {
		t.Errorf("m at the end: fetched %v\n%s", mb.offsets, screen(m))
	}
	// The cursor's match stays on screen however far down it is.
	hit(t, m, "pgdown", "pgdown", "pgdown")
	cur := m.look.matches[m.look.cursor]
	if m.look.cursor < 30 || cur.Release == nil {
		t.Fatalf("the cursor is on match %d: %+v", m.look.cursor, cur)
	}
	// Each title is on the list once for each of its two releases, and the
	// breadcrumb has an arrow of its own: the line is what counts.
	if line := cursorLine(screen(m)); !strings.Contains(line, cur.Title()) || !strings.Contains(line, cur.Release.Title) {
		t.Errorf("the line under the cursor is %q, want %q on %q:\n%s", line, cur.Title(), cur.Release.Title, screen(m))
	}
}

// cursorLine is the line of the screen the cursor is on: the one that
// starts with its arrow.
func cursorLine(screen string) string {
	for _, line := range strings.Split(screen, "\n") {
		if strings.HasPrefix(line, "› ") {
			return line
		}
	}
	return ""
}

func TestLookupSaveRefusedKeepsTheLookupOpen(t *testing.T) {
	m, f, _ := lookupOn(t, 104, julie())
	toAlbum(t, m)
	hit(t, m, "enter")
	m.header.DryRun = true
	hit(t, m, "enter")
	if len(f.saved) != 0 || m.look == nil || m.look.step != stepConfirm || !strings.Contains(m.toast, "Dry run") {
		t.Fatalf("a save in a dry run: saved %v, toast %q", f.saved, m.toast)
	}
	m.header.DryRun = false
	f.saveEvents = []engine.Event{engine.FileError{Path: m.look.file.Path, Op: engine.OpSaveTags, Err: errors.New("it is gone or has changed since it was indexed")}}
	f.saveFinish = engine.RunFinished{State: engine.StateErrors, Counters: engine.Counters{Errors: 1}}
	hit(t, m, "enter")
	// The file may not be as the diff said any more: back to the matches.
	if len(f.saved) != 1 || m.look == nil || m.look.step != stepPick || m.toastOK || !strings.Contains(m.toast, "has changed since it was indexed") {
		t.Errorf("a refused save: saved %v, lookup open %t, toast %q", f.saved, m.look != nil, m.toast)
	}
}

func TestGoldenLookup(t *testing.T) {
	m, _, _ := lookupOn(t, 104, julie())
	toAlbum(t, m)
	got := screen(m)
	checkFits(t, got, 120, 32)
	golden(t, filepath.Join("testdata", "lookup_matches.golden"), got)
	hit(t, m, "enter")
	got = screen(m)
	checkFits(t, got, 120, 32)
	golden(t, filepath.Join("testdata", "lookup_confirm.golden"), got)
}

// A confirmed match writes its tags, MusicBrainz IDs included, clears what
// MusicBrainz has no value for, and marks the file as picked from there.
func TestLookupSaveWritesTheMatch(t *testing.T) {
	dir := t.TempDir()
	path := copyFixture(t, dir, fixtures.RipperFLAC, "ripped.flac")
	st, run := realRun(t, dir)

	mb := julie()
	tags := musicbrainz.TagsFor(mb.release, musicbrainz.TrackRef{Medium: 0, Index: 1})
	day := time.Date(2026, 10, 8, 15, 0, 0, 0, time.UTC)
	if err := lookupSave(lookupValues(tags, path), day)(st, run, store.File{Path: path}); err != nil {
		t.Fatal(err)
	}
	after, err := librarySource{}.Tags(path)
	if err != nil {
		t.Fatal(err)
	}
	want := tagwrite.Values{
		tagwrite.Title: {"Julie and Candy"}, tagwrite.Artist: {"Boards of Canada"}, tagwrite.AlbumArtist: {"Boards of Canada"},
		tagwrite.Album: {"Geogaddi"}, tagwrite.Date: {"2002-02-18"}, tagwrite.Track: {"5/23"}, tagwrite.Disc: {"1/1"},
		tagwrite.Compilation: {}, tagwrite.Genre: {}, tagwrite.Composer: {}, tagwrite.Comment: {},
		tagwrite.RecordingID: {"rec-1"}, tagwrite.ReleaseTrackID: {"track-5"},
		tagwrite.AlbumID: {"rel-album"}, tagwrite.ReleaseGroupID: {"rg-1"},
		tagwrite.ArtistIDs: {"artist-1"}, tagwrite.AlbumArtistIDs: {"artist-1"}, tagwrite.Marker: {"musicbrainz 2026-10-08"},
	}
	if !reflect.DeepEqual(after, want) {
		t.Errorf("tags after the save:\n got %v\nwant %v", after, want)
	}
}

// The query form stays editable whatever is pressed on its last field, and
// the cursor stays on its match when more are ranked in above it.
func TestLookupFormSurvivesItsLastFieldAndTheCursorFollowsItsMatch(t *testing.T) {
	mb := julie()
	// A second page holding a better match than anything on the first.
	mb.pages[0].Count = 3
	mb.pages[2] = &musicbrainz.RecordingSearch{Count: 3, Recordings: []musicbrainz.Recording{
		{ID: "rec-3", Title: "Live", ArtistCredit: []musicbrainz.ArtistCredit{{Name: "Hand Tagged"}},
			Releases: []musicbrainz.Release{{ID: "rel-3", Title: "Tape"}}},
	}}
	m, _, _ := lookupOn(t, 104, mb)
	hit(t, m, "down", "down", "down", "down", "tab", "down")
	if got := screen(m); !strings.Contains(got, "> Live") || !strings.Contains(got, "Length") {
		t.Fatalf("the query form is gone:\n%s", got)
	}
	typeText(t, m, "9")
	if m.look.length != "9" {
		t.Fatalf("the form no longer takes keys: length %q", m.look.length)
	}
	hit(t, m, "backspace")

	toAlbum(t, m)
	hit(t, m, "m")
	if len(mb.offsets) != 2 || len(m.look.matches) != 4 {
		t.Fatalf("m fetched %v and left %d matches", mb.offsets, len(m.look.matches))
	}
	if first := m.look.matches[0]; first.Recording.ID != "rec-3" {
		t.Fatalf("the better match was not ranked first: %+v", first.Recording)
	}
	if c := m.look.matches[m.look.cursor]; c.Release == nil || c.Release.ID != "rel-album" || m.look.cursor >= m.look.shown {
		t.Errorf("the cursor is on %+v (index %d of %d shown), want it still on the album", c.Recording, m.look.cursor, m.look.shown)
	}
}
