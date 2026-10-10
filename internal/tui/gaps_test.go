package tui

import (
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/zachjamesgreen/tagshelf/internal/engine"
	"github.com/zachjamesgreen/tagshelf/internal/store"
	"github.com/zachjamesgreen/tagshelf/internal/tagwrite"
)

// renamed gives the untagged file with the given ID another name, as a
// reorganize in another process would, and reloads.
func renamed(t *testing.T, m *Model, src *fakeSource, id int64, name string) string {
	t.Helper()
	for i := range src.snap.Untagged {
		if f := &src.snap.Untagged[i]; f.ID == id {
			f.RelPath, f.Path = name, filepath.Join(`D:\Music\_Untagged`, name)
			src.version++
			tickNow(t, m)
			return f.Path
		}
	}
	t.Fatalf("no untagged file %d", id)
	return ""
}

// A reload while a flow is open brings the file it is open on up to date.
func TestReloadUpdatesTheFileAFlowIsOpenOn(t *testing.T) {
	m, _, src := editorOn(t, 104, tagwrite.Values{tagwrite.Title: {"Live"}})
	path := renamed(t, m, src, 104, "live take 2.mp3")
	if m.edit == nil || m.edit.file.Path != path || !strings.Contains(screen(m), "Edit tags › live take 2.mp3") {
		t.Errorf("the editor after its file was renamed: %+v\n%s", m.edit, screen(m))
	}

	m, _, src = lookupOn(t, 104, julie())
	path = renamed(t, m, src, 104, "live take 2.mp3")
	if m.look == nil || m.look.file.Path != path || !strings.Contains(screen(m), "Look up › live take 2.mp3") {
		t.Errorf("the lookup after its file was renamed: %+v\n%s", m.look, screen(m))
	}

	m, _ = matchOn(t, geogaddi())
	src = m.src.(*fakeSource)
	path = renamed(t, m, src, 102, "02 renamed.flac")
	if m.match == nil || m.match.files[1].Path != path || !strings.Contains(screen(m), "← 02 renamed.flac") {
		t.Errorf("the release match after a file of it was renamed:\n%s", screen(m))
	}
}

// A run still going that is open in detail is read again with each reload;
// one that has ended is not.
func TestDetailOfARunStillGoingFollowsIt(t *testing.T) {
	src := historySource()
	move := func(n int) store.Move {
		return store.Move{From: `E:\Incoming\a.flac`, To: filepath.Join(`D:\Music\Singer\Album`, string(rune('0'+n))+".flac"), Reason: store.ReasonOrganized}
	}
	src.details[7] = RunDetail{Moves: []store.Move{move(1)}}
	m := newModel(t, src, 120, 32)
	hit(t, m, "5", "enter")
	if got := screen(m); !strings.Contains(got, "Run 7") || !strings.Contains(got, "Moves (1)") {
		t.Fatalf("the detail of the run going on:\n%s", got)
	}
	src.details[7] = RunDetail{Moves: []store.Move{move(1), move(2)}}
	src.snap.Runs[0].Counters.Organized = 2
	src.version++
	tickNow(t, m)
	if got := screen(m); !strings.Contains(got, "Moves (2)") || !strings.Contains(got, "2.flac") || !strings.Contains(got, "Organized 2") {
		t.Errorf("the detail after the run moved another file:\n%s", got)
	}
	// It ends: its last moves are read, and after that nothing more.
	src.details[7] = RunDetail{Moves: []store.Move{move(1), move(2), move(3)}}
	src.snap.Runs[0].State = store.StateOK
	src.version++
	tickNow(t, m)
	if got := screen(m); !strings.Contains(got, "Moves (3)") {
		t.Errorf("the detail after the run ended:\n%s", got)
	}
	read := len(src.detailed)
	src.version++
	tickNow(t, m)
	if len(src.detailed) != read {
		t.Errorf("the detail of a run that has ended was read again: %v", src.detailed[read:])
	}
}

// A save the Runner cannot start, and one that was interrupted, leave the
// editor and the lookup open with what went wrong.
func TestSavesThatDoNotStartOrFinish(t *testing.T) {
	locked := &store.LockedError{Path: `D:\db.lock`, Holder: store.LockHolder{PID: 4242, Started: started}}

	m, f, _ := editorOn(t, 104, tagwrite.Values{tagwrite.Title: {"Live"}})
	typeText(t, m, "!")
	f.err = locked
	hit(t, m, "ctrl+s")
	if m.edit == nil {
		t.Fatalf("the editor closed on a save that could not start: toast %q", m.toast)
	}
	if m.edit.saving || m.toastOK || !strings.Contains(m.toast, "Cannot change the library") || !strings.Contains(m.toast, "4242") || m.edit.title != "Live!" {
		t.Errorf("a save that could not start: saving %t, toast %q, title %q", m.edit.saving, m.toast, m.edit.title)
	}
	f.err = nil
	f.saveFinish = engine.RunFinished{State: engine.StateInterrupted}
	hit(t, m, "ctrl+s")
	if m.edit == nil {
		t.Fatalf("the editor closed on an interrupted save: toast %q", m.toast)
	}
	if m.toastOK || m.toast != "The save did not finish: see 5 Runs" {
		t.Errorf("an interrupted save: toast %q (ok %t)", m.toast, m.toastOK)
	}

	m, f, _ = lookupOn(t, 104, julie())
	toAlbum(t, m)
	hit(t, m, "enter")
	f.err = locked
	hit(t, m, "enter")
	if m.look == nil {
		t.Fatalf("the lookup closed on a save that could not start: toast %q", m.toast)
	}
	if m.look.step != stepConfirm || m.toastOK || !strings.Contains(m.toast, "4242") {
		t.Errorf("a save that could not start: step %d, toast %q", m.look.step, m.toast)
	}
	f.err = nil
	f.saveFinish = engine.RunFinished{State: engine.StateInterrupted}
	hit(t, m, "enter")
	if m.look == nil {
		t.Fatalf("the lookup closed on an interrupted save: toast %q", m.toast)
	}
	if m.look.step != stepPick || m.toast != "The save did not finish: see 5 Runs" {
		t.Errorf("an interrupted save: step %d, toast %q", m.look.step, m.toast)
	}
}

// ctrl+c quits from inside every flow, and with a run going interrupts the
// run instead.
func TestCtrlCInsideAFlow(t *testing.T) {
	flows := map[string]func(t *testing.T) *Model{
		"the editor": func(t *testing.T) *Model {
			m, _, _ := editorOn(t, 104, tagwrite.Values{tagwrite.Title: {"Live"}})
			return m
		},
		"the lookup": func(t *testing.T) *Model {
			m, _, _ := lookupOn(t, 104, julie())
			return m
		},
		"the release match": func(t *testing.T) *Model {
			m, _ := matchOn(t, geogaddi())
			return m
		},
		"the undo preflight": func(t *testing.T) *Model {
			m, _, _ := undoOn(t, 2)
			return m
		},
	}
	open := func(m *Model) bool { return m.edit != nil || m.look != nil || m.match != nil || m.undo != nil }
	for name, flow := range flows {
		m := flow(t)
		if msg := press(t, m, "ctrl+c"); msg != (tea.QuitMsg{}) {
			t.Errorf("ctrl+c in %s gave %#v, want it to quit", name, msg)
		}

		// A run going on is interrupted, and the flow stays.
		m = flow(t)
		f := m.runner.(*fakeRunner)
		done := make(chan struct{})
		m.run = &liveRun{kind: RunOrganize, cancel: func() { close(done) }, done: done}
		if msg := press(t, m, "ctrl+c"); msg == (tea.QuitMsg{}) {
			t.Errorf("ctrl+c in %s quit during a run", name)
		}
		select {
		case <-done:
		default:
			t.Errorf("ctrl+c in %s did not interrupt the run", name)
		}
		if !open(m) || !strings.Contains(m.toast, "Interrupting the organize") || len(f.saved)+len(f.undone) != 0 {
			t.Errorf("after ctrl+c in %s during a run: flow open %t, toast %q", name, open(m), m.toast)
		}
	}
}

// ctrl+c at the question "Interrupt the run and quit?" interrupts the run
// and does not quit.
func TestCtrlCAtTheQuitQuestion(t *testing.T) {
	f := newFakeRunner()
	m := newRunModel(t, f)
	press(t, m, "o")
	s := waitStart(t, f)
	press(t, m, "q")
	if msg := press(t, m, "ctrl+c"); msg == (tea.QuitMsg{}) {
		t.Fatal("ctrl+c at the question quit")
	}
	select {
	case <-s.ctx.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("ctrl+c at the question did not interrupt the run")
	}
	if m.confirmQuit || m.quitting {
		t.Errorf("after ctrl+c at the question: still asking %t, quitting %t", m.confirmQuit, m.quitting)
	}
	if cmd := runEnded(t, m); cmd == nil || cmd() == (tea.QuitMsg{}) {
		t.Error("the TUI quit once the run ended")
	}
}

// The forms of the lookup and the release match follow the terminal's size,
// and all three forms take pasted text.
func TestFlowFormsResizeAndTakePastedText(t *testing.T) {
	paste := func(m *Model, text string) {
		t.Helper()
		_, cmd := m.Update(tea.PasteMsg{Content: text})
		pump(t, m, cmd)
	}

	m, _, _ := editorOn(t, 104, tagwrite.Values{tagwrite.Title: {"Live"}})
	paste(m, " at Home")
	if m.edit.title != "Live at Home" {
		t.Errorf("pasted into the editor: title %q", m.edit.title)
	}

	m, _, _ = lookupOn(t, 104, julie())
	m.Update(windowSize(50, 16))
	got := screen(m)
	checkFits(t, got, 50, 16)
	if !strings.Contains(got, "> Live") || !strings.Contains(got, "Length") {
		t.Errorf("the lookup's form at 50x16:\n%s", got)
	}
	paste(m, " at Home")
	typeText(t, m, "!")
	if m.look.title != "Live at Home!" {
		t.Errorf("pasted and typed into the lookup after a resize: title %q", m.look.title)
	}

	m, _ = newUntaggedModel(t, matchSource())
	m.mb = geogaddi()
	cursorTo(t, m, 101)
	hit(t, m, "L")
	m.Update(windowSize(50, 16))
	got = screen(m)
	checkFits(t, got, 50, 16)
	if !strings.Contains(got, "> Geogaddi") || !strings.Contains(got, "Year") {
		t.Errorf("the release match's form at 50x16:\n%s", got)
	}
	paste(m, " (Deluxe)")
	typeText(t, m, "!")
	if m.match.album != "Geogaddi (Deluxe)!" {
		t.Errorf("pasted and typed into the release match after a resize: album %q", m.match.album)
	}
	// Past the query nothing takes pasted text.
	hit(t, m, "enter")
	paste(m, "ignored")
	if m.match.step != matchPick || m.match.album != "Geogaddi (Deluxe)!" {
		t.Errorf("pasted on the releases: step %d, album %q", m.match.step, m.match.album)
	}
}

// Home draws a run going on again every runRefresh, for as long as it goes.
func TestRunTickGoesOnWhileTheRunDoes(t *testing.T) {
	f := newFakeRunner()
	m := newRunModel(t, f)
	if _, cmd := m.Update(runTickMsg{}); cmd != nil {
		t.Error("a run tick with no run going asked for another")
	}
	press(t, m, "o")
	waitStart(t, f)
	if _, cmd := m.Update(runTickMsg{}); cmd == nil {
		t.Error("a run tick during a run did not ask for the next")
	}
	f.end(t, engine.RunFinished{})
	runEnded(t, m)
	if _, cmd := m.Update(runTickMsg{}); cmd != nil {
		t.Error("a run tick after the run asked for another")
	}
}

// The real source's search, album and release IDs go to the library's index.
func TestLibrarySourceSearchAlbumAndReleases(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "tagshelf.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	lib := filepath.Join(dir, "Music")
	libRoot, err := st.SetRoot(store.RootLibrary, lib)
	if err != nil {
		t.Fatal(err)
	}
	untRoot, err := st.SetRoot(store.RootUntagged, filepath.Join(lib, "_Untagged"))
	if err != nil {
		t.Fatal(err)
	}
	file := func(root int64, rel, artist, album, title, release string, track int) store.File {
		return store.File{RootID: root, RelPath: filepath.FromSlash(rel), Artist: artist, Album: album, Title: title, Track: track,
			ReleaseID: release, Size: 1, ModTime: time.Unix(1, 0)}
	}
	if err := st.PutFiles([]store.File{
		file(libRoot, "Boards of Canada/Geogaddi/02 - Music Is Math.flac", "Boards of Canada", "Geogaddi", "Music Is Math", "rel-1", 2),
		file(libRoot, "Boards of Canada/Geogaddi/01 - Ready Lets Go.flac", "Boards of Canada", "Geogaddi", "Ready Lets Go", "rel-1", 1),
		file(libRoot, "Autechre/Amber/01 - Foil.mp3", "Autechre", "Amber", "Foil", "rel-2", 1),
		file(untRoot, "loose.mp3", "Boards of Canada", "Geogaddi", "Loose", "rel-3", 9),
	}); err != nil {
		t.Fatal(err)
	}
	src := librarySource{st: st, destination: lib, pid: -1}

	titles := func(files []store.File) []string {
		var got []string
		for _, f := range files {
			got = append(got, f.Title)
		}
		return got
	}
	found, err := src.Search("geogaddi boards", 10)
	if err != nil || !reflect.DeepEqual(titles(found), []string{"Ready Lets Go", "Music Is Math"}) {
		t.Fatalf("Search = %v, %v; want the album's two tracks of the library, in track order", titles(found), err)
	}
	if found, err := src.Search("a", 1); err != nil || len(found) != 1 || found[0].Title != "Foil" {
		t.Errorf("Search with a limit of 1 = %v, %v; want the first by artist", titles(found), err)
	}
	album, err := src.Album(found[1])
	if err != nil || !reflect.DeepEqual(titles(album), []string{"Ready Lets Go", "Music Is Math"}) {
		t.Errorf("Album = %v, %v; want the folder's two files", titles(album), err)
	}
	if album[0].Path != filepath.Join(lib, "Boards of Canada", "Geogaddi", "01 - Ready Lets Go.flac") {
		t.Errorf("Album gave the path %q", album[0].Path)
	}
	releases, err := src.LibraryReleases()
	if err != nil || !reflect.DeepEqual(releases, map[string]bool{"rel-1": true, "rel-2": true}) {
		t.Errorf("LibraryReleases = %v, %v; want those of the library alone", releases, err)
	}
}

// The corners of a release match: a file that cannot be tagged, a track
// count that is no number, no release found, a file whose tags cannot be
// read, and no file left on a track.
func TestReleaseMatchCorners(t *testing.T) {
	// A file tagshelf cannot tag keeps the selection from being matched.
	src := matchSource()
	src.snap.Untagged[1].Path = `D:\Music\_Untagged\02 track.xyz`
	m, _ := newUntaggedModel(t, src)
	m.mb = geogaddi()
	cursorTo(t, m, 101)
	hit(t, m, "L")
	if m.match != nil || m.toastOK || m.toast != "tagshelf cannot tag xyz files: leave 02 track.xyz out" {
		t.Errorf("a selection with a file that cannot be tagged: match open %t, toast %q", m.match != nil, m.toast)
	}

	mb := geogaddi()
	m, _ = newUntaggedModel(t, matchSource())
	m.mb = mb
	cursorTo(t, m, 101)
	hit(t, m, "L", "down", "down")
	typeText(t, m, "x")
	hit(t, m, "enter")
	if got := screen(m); m.match.step != matchQuery || len(mb.releaseQueries) != 0 || !strings.Contains(got, `The number of tracks is a number, not "3x"`) {
		t.Fatalf("a track count that is no number: step %d, %d searches\n%s", m.match.step, len(mb.releaseQueries), got)
	}
	hit(t, m, "backspace", "down")
	typeText(t, m, "-5")
	hit(t, m, "enter")
	if got := screen(m); len(mb.releaseQueries) != 0 || !strings.Contains(got, `The year is a number, not "-5"`) {
		t.Fatalf("a year below nought: %d searches\n%s", len(mb.releaseQueries), got)
	}
	hit(t, m, "ctrl+u")

	// Nothing typed to search for.
	hit(t, m, "up", "up", "ctrl+u", "up", "ctrl+u", "enter")
	if got := screen(m); len(mb.releaseQueries) != 0 || !strings.Contains(got, "Nothing to search for: type an album or an artist") {
		t.Fatalf("an empty query: %d searches\n%s", len(mb.releaseQueries), got)
	}
	typeText(t, m, "Geogaddi")

	// No release like it.
	found := mb.candidates
	mb.candidates = nil
	hit(t, m, "enter")
	if got := screen(m); m.match.step != matchQuery || len(mb.releaseQueries) != 1 || !strings.Contains(got, "MusicBrainz has no release like that") {
		t.Fatalf("a search that found nothing: step %d\n%s", m.match.step, got)
	}
	mb.candidates = found
	hit(t, m, "enter")
	if m.match.step != matchPick {
		t.Fatalf("the search: step %d\n%s", m.match.step, screen(m))
	}

	// A file whose tags cannot be read leaves the releases to pick from.
	msrc := m.src.(*fakeSource)
	msrc.err = errors.New("the disk is gone")
	hit(t, m, "enter")
	if got := screen(m); m.match.step != matchPick || !strings.Contains(got, "reading the tags of 01 track.flac: the disk is gone") {
		t.Fatalf("tags that cannot be read: step %d\n%s", m.match.step, got)
	}
	msrc.err = nil
	hit(t, m, "enter")
	if m.match.step != matchMap {
		t.Fatalf("the release: step %d\n%s", m.match.step, screen(m))
	}

	// Every file taken off its track: there is nothing to write.
	hit(t, m, "x", "down", "x", "up")
	if n := len(m.match.mapping.Assignments); n != 0 || m.match.grabbed != -1 {
		t.Fatalf("%d files are still on a track, grabbed %d", n, m.match.grabbed)
	}
	hit(t, m, "r")
	if got := screen(m); !strings.Contains(got, "There is no file on this line to move") {
		t.Errorf("r on a track without a file:\n%s", got)
	}
	hit(t, m, "enter")
	if got := screen(m); m.match.step != matchMap || !strings.Contains(got, "No file is on a track, so there is nothing to write") {
		t.Errorf("enter with no file on a track: step %d\n%s", m.match.step, got)
	}
}

// Every format a run indexes can be tagged: the editor, the lookup and the
// release match open on a file of any of them.
func TestFlowsOpenOnEveryIndexedFormat(t *testing.T) {
	for _, ext := range []string{".mp3", ".flac", ".m4a", ".ogg", ".oga", ".opus", ".wma", ".aac", ".ape", ".mpc", ".m4p", ".m4b", ".m4r", ".wav", ".dsf"} {
		src := untaggedSource()
		src.snap.Untagged[2].Path = `D:\Music\_Untagged\demo` + ext // file 103
		m, _ := newUntaggedModel(t, src)
		m.mb = julie()
		cursorTo(t, m, 103)
		hit(t, m, "e")
		if m.edit == nil || m.edit.form == nil {
			t.Errorf("the editor did not open on a %s file: toast %q", ext, m.toast)
		}
		hit(t, m, "esc", "l")
		if m.look == nil {
			t.Errorf("the lookup did not open on a %s file: toast %q", ext, m.toast)
		}
		hit(t, m, "esc", "L")
		if m.match == nil {
			t.Errorf("the release match did not open on a folder with a %s file: toast %q", ext, m.toast)
		}
	}
}
