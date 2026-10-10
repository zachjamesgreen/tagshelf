package tui

import (
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/zachjamesgreen/tagshelf/internal/engine"
	"github.com/zachjamesgreen/tagshelf/internal/musicbrainz"
	"github.com/zachjamesgreen/tagshelf/internal/store"
	"github.com/zachjamesgreen/tagshelf/internal/tagwrite"
)

// geogaddi is a MusicBrainz that knows one release of three tracks.
func geogaddi() *fakeMB {
	track := func(n int, title string, length time.Duration) musicbrainz.Track {
		id := string(rune('0' + n))
		return musicbrainz.Track{ID: "track-" + id, Position: n, Title: title, Length: int(length.Milliseconds()),
			ArtistCredit: boards, Recording: &musicbrainz.TrackRecording{ID: "rec-" + id}}
	}
	rel := musicbrainz.Release{ID: "rel-album", Title: "Geogaddi", Status: "Official", Date: "2002-02-18", Country: "GB", TrackCount: 3,
		ArtistCredit: boards, ReleaseGroup: musicbrainz.ReleaseGroup{ID: "rg-1", PrimaryType: "Album"},
		Media: []musicbrainz.Medium{{Position: 1, Format: "CD", TrackCount: 3, Tracks: []musicbrainz.Track{
			track(1, "Ready Lets Go", 59*time.Second),
			track(2, "Music Is Math", 321*time.Second),
			track(3, "Beware the Friendly Stranger", 37*time.Second),
		}}}}
	other := musicbrainz.Release{ID: "rel-other", Title: "Geogaddi (promo)", Date: "2002", TrackCount: 1, ArtistCredit: boards,
		Media: []musicbrainz.Medium{{Position: 1, Format: "CD", TrackCount: 1}}}
	promo := other
	promo.Media = []musicbrainz.Medium{{Position: 1, Format: "CD", TrackCount: 1, Tracks: []musicbrainz.Track{track(1, "Ready Lets Go (edit)", 50*time.Second)}}}
	found := rel
	return &fakeMB{
		release:  &rel,
		releases: map[string]*musicbrainz.Release{rel.ID: &rel, promo.ID: &promo},
		candidates: []musicbrainz.ReleaseCandidate{
			{Score: 96, Release: &found, Found: 2, Searched: 3},
			{Score: 61, Release: &other, InLibrary: true, Searched: 3},
		},
	}
}

// matchSource is the untagged library with a rip of three files: one that
// fits its track, one 21 s shorter than its track, and one without a title.
func matchSource() *fakeSource {
	src := untaggedSource()
	u := src.snap.Untagged
	u[0].Length, u[1].Length = 59*time.Second, 300*time.Second
	third := u[1]
	third.ID, third.Title, third.Length = 106, "", 37*time.Second
	third.Path = filepath.Join(p(`D:\Music\_Untagged`), "zz.flac")
	third.From = p(`E:\Rips\Geogaddi\zz.flac`)
	src.snap.Untagged = append(u, third)
	src.tags = map[string]tagwrite.Values{}
	for _, f := range src.snap.Untagged {
		src.tags[f.Path] = tagwrite.Values{tagwrite.Title: {f.Title}, tagwrite.Artist: {f.Artist}, tagwrite.Album: {f.Album}}
	}
	return src
}

// matchOn opens a release match from the Untagged tab on the rip's folder
// and takes it as far as the mapping screen.
func matchOn(t *testing.T, mb *fakeMB) (*Model, *fakeRunner) {
	t.Helper()
	m, f := newUntaggedModel(t, matchSource())
	m.mb = mb
	cursorTo(t, m, 101)
	hit(t, m, "L", "enter", "enter")
	if m.match == nil || m.match.step != matchMap {
		t.Fatalf("the release match did not get to the tracks:\n%s", screen(m))
	}
	return m, f
}

func TestReleaseMatchTagsAFolderAsOneRelease(t *testing.T) {
	mb := geogaddi()
	m, f := newUntaggedModel(t, matchSource())
	m.mb = mb
	cursorTo(t, m, 101)
	hit(t, m, "L")
	got := screen(m)
	for _, want := range []string{"Untagged › Release match › 3 files", "Search MusicBrainz for a release", "> Geogaddi", "> Boards of Canada", "> 3"} {
		if !strings.Contains(got, want) {
			t.Errorf("the query step lacks %q:\n%s", want, got)
		}
	}

	// The year only ranks; it can be typed.
	hit(t, m, "down", "down", "down", "down", "tab")
	typeText(t, m, "2002")
	hit(t, m, "enter")
	if want := []musicbrainz.ReleaseQuery{{Album: "Geogaddi", Artist: "Boards of Canada", Tracks: 3, Year: 2002}}; !reflect.DeepEqual(mb.releaseQueries, want) {
		t.Fatalf("searched for %+v, want %+v", mb.releaseQueries, want)
	}
	if len(mb.boosts) != 1 || len(mb.boosts[0]) == 0 || mb.boosts[0][0].Title != "Ready Lets Go" {
		t.Errorf("the files searched for along with it: %+v", mb.boosts)
	}
	got = screen(m)
	for _, want := range []string{"3 files › Releases", "2 releases", " 96", "Geogaddi", "Boards of Canada", "2002", "GB", "CD", "2/3", "Geogaddi (promo) (in library)"} {
		if !strings.Contains(got, want) {
			t.Errorf("the releases lack %q:\n%s", want, got)
		}
	}

	hit(t, m, "enter")
	if !reflect.DeepEqual(mb.looked, []string{"rel-album"}) {
		t.Fatalf("looked up %v", mb.looked)
	}
	got = screen(m)
	for _, want := range []string{
		"Releases › Tracks", "Geogaddi by Boards of Canada (2002, GB, CD)",
		"2 files on a track, 1 file on none (not written), 1 track without a file",
		"01", "Ready Lets Go", "0:59", "← 01 track.flac",
		"Music Is Math", "5:21", "← 02 track.flac", "! length differs by 0:21",
		"Beware the Friendly Stranger", "(no file)",
		"(on no track: not written)", "← zz.flac",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the mapping lacks %q:\n%s", want, got)
		}
	}

	hit(t, m, "enter")
	got = screen(m)
	for _, want := range []string{
		"Tracks › Confirm", "Tag 2 files as Geogaddi by Boards of Canada",
		"Will go to:    " + filepath.Join("Boards of Canada", "Geogaddi (2002)"),
		"Warning: 1 file is on no track and will not be written",
		"Warning: 1 file differs in length from the track by more than 10 s",
		"01 track.flac -> 01 Ready Lets Go", "-> Boards of Canada", "-> 2002-02-18", "-> 1/3", "and its MusicBrainz IDs",
		"02 track.flac -> 02 Music Is Math", "-> 2/3",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the diff lacks %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "zz.flac") {
		t.Errorf("the diff lists the file on no track:\n%s", got)
	}

	album := p(`D:\Music\Boards of Canada\Geogaddi (2002)`)
	f.saveEvents = []engine.Event{
		engine.FilePlaced{Destination: album + p(`\01 - Ready Lets Go.flac`), Outcome: engine.Organized},
		engine.FilePlaced{Destination: album + p(`\02 - Music Is Math.flac`), Outcome: engine.Organized},
	}
	f.saveFinish = engine.RunFinished{State: engine.StateOK, Counters: engine.Counters{Organized: 2}}
	hit(t, m, "enter")
	// One run for both; the file on no track is not written.
	if !reflect.DeepEqual(f.saved, [][]int64{{101, 102}}) {
		t.Errorf("saved = %v, want files 101 and 102 in one run", f.saved)
	}
	if m.match != nil || !m.toastOK || !strings.Contains(m.toast, "2 files moved to "+filepath.Join("Boards of Canada", "Geogaddi (2002)")) {
		t.Errorf("after confirming: match open %t, toast %q (ok %t)", m.match != nil, m.toast, m.toastOK)
	}
}

func TestReleaseMatchReassignUpdatesTheDiff(t *testing.T) {
	m, f := matchOn(t, geogaddi())
	x := m.match

	// Take the file without a title, on the last line, to the third track.
	hit(t, m, "down", "down", "down", "r")
	if x.grabbed != 2 || !strings.Contains(screen(m), "Moving zz.flac") {
		t.Fatalf("r did not pick up the file: grabbed %d\n%s", x.grabbed, screen(m))
	}
	hit(t, m, "enter")
	if x.grabbed != 2 || !strings.Contains(screen(m), "Move to a track") {
		t.Fatalf("enter off a track put the file somewhere:\n%s", screen(m))
	}
	hit(t, m, "up", "enter")
	if x.grabbed != -1 || len(x.mapping.Assignments) != 3 || len(x.mapping.Unassigned) != 0 {
		t.Fatalf("after reassigning: %+v", x.mapping)
	}
	hit(t, m, "enter")
	if got := screen(m); !strings.Contains(got, "Tag 3 files") {
		t.Fatalf("the diff after reassigning:\n%s", got)
	}
	hit(t, m, "pgdown")
	if got := screen(m); !strings.Contains(got, "zz.flac -> 03 Beware the Friendly Stranger") || !strings.Contains(got, "-> 3/3") {
		t.Errorf("the diff lacks the reassigned file:\n%s", got)
	}

	// Back on the tracks: x takes a file off, and a file moved onto a taken
	// track takes its place.
	hit(t, m, "esc", "x")
	if len(x.mapping.Assignments) != 2 || !reflect.DeepEqual(x.mapping.Unassigned, []int{2}) {
		t.Fatalf("after x on the third track: %+v", x.mapping)
	}
	hit(t, m, "up", "up", "r", "esc")
	if x.grabbed != -1 || x.step != matchMap {
		t.Fatalf("esc while moving a file: grabbed %d, step %d", x.grabbed, x.step)
	}
	hit(t, m, "r", "down", "enter")
	if a := x.mapping.Assignments; len(a) != 1 || a[0].File != 0 || a[0].Track.Index != 1 || !reflect.DeepEqual(x.mapping.Unassigned, []int{1, 2}) {
		t.Fatalf("after moving the first file onto the second track: %+v", x.mapping)
	}
	f.saveFinish = engine.RunFinished{State: engine.StateOK}
	hit(t, m, "enter", "enter")
	if !reflect.DeepEqual(f.saved, [][]int64{{101}}) {
		t.Errorf("saved = %v, want only the file left on a track", f.saved)
	}
}

// A selection is matched instead of the cursor's group, and may be part of
// a release.
func TestReleaseMatchOfAPartialSelection(t *testing.T) {
	mb := geogaddi()
	m, f := newUntaggedModel(t, matchSource())
	m.mb = mb
	cursorTo(t, m, 102)
	hit(t, m, "space", "up", "up", "L")
	if m.match == nil || len(m.match.files) != 1 || m.match.files[0].ID != 102 {
		t.Fatalf("the match is not of the selection: %+v", m.match)
	}
	hit(t, m, "enter", "enter")
	if got := screen(m); !strings.Contains(got, "1 file on a track, 2 tracks without a file") {
		t.Fatalf("the mapping of a partial selection:\n%s", got)
	}
	f.saveFinish = engine.RunFinished{State: engine.StateOK}
	hit(t, m, "enter", "enter")
	if !reflect.DeepEqual(f.saved, [][]int64{{102}}) {
		t.Errorf("saved = %v, want file 102", f.saved)
	}
}

func TestReleaseMatchStepsBackAndShowsErrors(t *testing.T) {
	mb := geogaddi()
	m, f := matchOn(t, mb)
	hit(t, m, "enter", "esc")
	if m.match.step != matchMap {
		t.Fatalf("esc on the diff: step %d", m.match.step)
	}
	hit(t, m, "esc")
	if m.match.step != matchPick {
		t.Fatalf("esc on the tracks: step %d", m.match.step)
	}
	// A release that cannot be read leaves the releases to pick from.
	mb.err = musicbrainz.ErrUnavailable
	hit(t, m, "enter")
	if got := screen(m); m.match.step != matchPick || !strings.Contains(got, "not accepting requests") {
		t.Fatalf("a failed release lookup: step %d\n%s", m.match.step, got)
	}
	hit(t, m, "esc")
	if m.match.step != matchQuery {
		t.Fatalf("esc on the releases: step %d", m.match.step)
	}
	// The form stays editable past its last field, and a failed search shows.
	hit(t, m, "down", "down")
	typeText(t, m, "0")
	mb.err = errors.New("no route to host")
	hit(t, m, "enter")
	if got := screen(m); m.match.step != matchQuery || !strings.Contains(got, "no route to host") || !strings.Contains(got, "> 30") {
		t.Fatalf("a failed search: step %d\n%s", m.match.step, got)
	}
	mb.err = nil
	hit(t, m, "enter")
	if n := len(mb.releaseQueries); m.match.step != matchPick || mb.releaseQueries[n-1].Tracks != 30 {
		t.Fatalf("the second search: step %d, %+v", m.match.step, mb.releaseQueries)
	}
	hit(t, m, "esc", "esc")
	if m.match != nil || len(f.saved) != 0 || !strings.Contains(screen(m), "2 Untagged") {
		t.Errorf("esc on the query: match open %t, saved %v", m.match != nil, f.saved)
	}

	// A save that cannot start leaves the diff to confirm again.
	m, f = matchOn(t, geogaddi())
	hit(t, m, "enter")
	// With dry run on nothing is written: the runner would not refuse it.
	m.header.DryRun = true
	hit(t, m, "enter")
	if m.match == nil {
		t.Fatalf("the release match closed on a save in a dry run: toast %q", m.toast)
	}
	if len(f.saved) != 0 || m.match.step != matchConfirm || !strings.Contains(m.toast, "Dry run") {
		t.Errorf("a save in a dry run: saved %v, step %d, toast %q", f.saved, m.match.step, m.toast)
	}
	m.header.DryRun = false
	f.err = &store.LockedError{Path: p(`D:\db.lock`), Holder: store.LockHolder{PID: 4242, Started: started}}
	hit(t, m, "enter")
	if m.match == nil {
		t.Fatalf("the release match closed on a save that could not start: toast %q", m.toast)
	}
	if m.match.step != matchConfirm || !strings.Contains(m.toast, "4242") {
		t.Errorf("a save that could not start: step %d, toast %q", m.match.step, m.toast)
	}
}

// The release looked up is the one under the cursor, not the first found.
func TestReleaseMatchPicksTheReleaseUnderTheCursor(t *testing.T) {
	mb := geogaddi()
	m, _ := newUntaggedModel(t, matchSource())
	m.mb = mb
	cursorTo(t, m, 101)
	hit(t, m, "L", "enter", "down", "down", "up", "pgdown", "pgup", "down", "enter")
	if !reflect.DeepEqual(mb.looked, []string{"rel-other"}) {
		t.Fatalf("looked up %v, want the second release", mb.looked)
	}
	if m.match == nil || m.match.step != matchMap {
		t.Fatalf("the release match did not get to the tracks:\n%s", screen(m))
	}
	if got := screen(m); !strings.Contains(got, "Geogaddi (promo) by Boards of Canada") || !strings.Contains(got, "Ready Lets Go (edit)") {
		t.Errorf("the tracks are not those of the release picked:\n%s", got)
	}

	// However far down the list, the release under the cursor is on screen
	// and is the one picked.
	for i := range 60 {
		rel := *mb.candidates[1].Release
		rel.ID, rel.Title = fmt.Sprintf("rel-%02d", i), fmt.Sprintf("Pressing %02d", i)
		mb.candidates = append(mb.candidates, musicbrainz.ReleaseCandidate{Score: 50, Release: &rel})
	}
	hit(t, m, "esc", "esc", "enter", "pgdown", "pgdown", "pgdown", "pgdown")
	x := m.match
	if x.step != matchPick || x.cursor != len(x.releases)-1 {
		t.Fatalf("step %d, cursor %d of %d releases", x.step, x.cursor, len(x.releases))
	}
	if line := cursorLine(screen(m)); !strings.Contains(line, "Pressing 59") {
		t.Errorf("the line under the cursor is %q, want the last release:\n%s", line, screen(m))
	}
	hit(t, m, "enter")
	if last := mb.looked[len(mb.looked)-1]; last != "rel-59" {
		t.Errorf("looked up %q, want the last release", last)
	}
}

func TestReleaseMatchWithoutAContactExplains(t *testing.T) {
	m, _ := newUntaggedModel(t, matchSource())
	cursorTo(t, m, 101)
	hit(t, m, "L")
	if got := screen(m); m.match == nil || !strings.Contains(got, "MusicBrainz lookups are off") || !strings.Contains(got, "musicbrainz_contact") {
		t.Fatalf("the explanation:\n%s", got)
	}
	hit(t, m, "enter", "esc")
	if m.match != nil {
		t.Error("esc did not close the explanation")
	}
}

func TestGoldenReleaseMatch(t *testing.T) {
	m, _ := matchOn(t, geogaddi())
	got := screen(m)
	checkFits(t, got, 120, 32)
	golden(t, filepath.Join("testdata", "match_tracks.golden"), got)
	hit(t, m, "enter")
	got = screen(m)
	checkFits(t, got, 120, 32)
	golden(t, filepath.Join("testdata", "match_confirm.golden"), got)
}

// With the list not grouped there is no group to take: L wants a selection.
func TestReleaseMatchOfAnUngroupedListWantsASelection(t *testing.T) {
	m, _ := newUntaggedModel(t, matchSource())
	m.mb = geogaddi()
	hit(t, m, "v", "v", "L")
	if m.match != nil || m.toastOK || !strings.Contains(m.toast, "Select the files") {
		t.Fatalf("L on an ungrouped list: match open %t, toast %q", m.match != nil, m.toast)
	}
	hit(t, m, "space", "L")
	if m.match == nil || len(m.match.files) != 1 {
		t.Errorf("L with a selection on an ungrouped list did not open a match of it")
	}
}

// The searches made for the files themselves take the album and artist typed
// into account, as musicbrainz.BoostQueries lays down and its own tests
// check: here, that what the form holds is what it is given. The two files
// are an untagged one and one with a title and an artist tag, from a folder
// whose name is not an album's.
func TestReleaseMatchBoostsWithTheQueryTyped(t *testing.T) {
	tests := []struct {
		desc   string
		artist string // typed into the form's empty artist field
		want   []musicbrainz.Query
	}{
		{"album and artist typed", "Boards of Canada", []musicbrainz.Query{
			{Title: "demo", Artist: "Boards of Canada", Album: "Geogaddi", Length: 95 * time.Second},
			{Title: "Live", Artist: "Boards of Canada", Album: "Geogaddi"},
		}},
		{"the artist left empty", "", []musicbrainz.Query{
			{Title: "demo", Album: "Geogaddi", Length: 95 * time.Second},
			{Title: "Live", Artist: "Hand Tagged", Album: "Geogaddi"},
		}},
	}
	for _, tt := range tests {
		mb := geogaddi()
		m, _ := newUntaggedModel(t, matchSource())
		m.mb = mb
		cursorTo(t, m, 103)
		hit(t, m, "L")
		if m.match == nil || m.match.step != matchQuery {
			t.Fatalf("%s: the release match did not open:\n%s", tt.desc, screen(m))
		}
		if m.match.album != "Incoming" || m.match.artist != "" {
			t.Fatalf("%s: suggested the album %q and the artist %q, want the folder's name and no artist", tt.desc, m.match.album, m.match.artist)
		}
		hit(t, m, "ctrl+u")
		typeText(t, m, "Geogaddi")
		hit(t, m, "down")
		typeText(t, m, tt.artist)
		hit(t, m, "enter")
		if want := []musicbrainz.ReleaseQuery{{Album: "Geogaddi", Artist: tt.artist, Tracks: 2}}; !reflect.DeepEqual(mb.releaseQueries, want) {
			t.Errorf("%s: searched for %+v, want %+v", tt.desc, mb.releaseQueries, want)
		}
		if len(mb.boosts) != 1 || !reflect.DeepEqual(mb.boosts[0], tt.want) {
			t.Errorf("%s: the files were searched for as\n %+v\nwant\n %+v", tt.desc, mb.boosts, tt.want)
		}
	}
}

// A folder of loose files taken for a release by a slip is refused before
// anything is asked of MusicBrainz or read from the files.
func TestReleaseMatchRefusesTooManyFiles(t *testing.T) {
	tests := []struct {
		files   int
		refused bool
	}{{musicbrainz.MaxMatchFiles + 1, true}, {musicbrainz.MaxMatchFiles, false}}
	for _, tt := range tests {
		src := untaggedSource()
		src.snap.Untagged = nil
		for i := range tt.files {
			name := fmt.Sprintf("%04d.mp3", i+1)
			f := store.File{ID: int64(1000 + i), RootKind: store.RootUntagged, RelPath: name, Format: "MP3"}
			f.Path = filepath.Join(p(`D:\Music\_Untagged`), name)
			src.snap.Untagged = append(src.snap.Untagged, store.UntaggedFile{File: f, From: p(`E:\Loose\`) + name})
		}
		mb := geogaddi()
		m, _ := newUntaggedModel(t, src)
		m.mb = mb
		cursorTo(t, m, 1000)
		hit(t, m, "L")
		if m.match == nil || len(m.match.files) != tt.files {
			t.Fatalf("%d files: the release match did not open on the folder:\n%s", tt.files, screen(m))
		}
		hit(t, m, "enter")
		got := screen(m)
		if !tt.refused {
			if len(mb.releaseQueries) != 1 || m.match.step != matchPick {
				t.Errorf("%d files: searched %d times, step %d; want the search made", tt.files, len(mb.releaseQueries), m.match.step)
			}
			continue
		}
		if want := "501 files are too many for one release (at most 500): select fewer"; !strings.Contains(got, want) {
			t.Errorf("%d files: the form lacks %q:\n%s", tt.files, want, got)
		}
		if len(mb.releaseQueries) != 0 || len(mb.looked) != 0 || m.match.step != matchQuery {
			t.Errorf("%d files: searched %d times, looked up %d releases, step %d; want nothing asked",
				tt.files, len(mb.releaseQueries), len(mb.looked), m.match.step)
		}
	}
}
