package tui

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/zachjamesgreen/tagshelf/internal/engine"
	"github.com/zachjamesgreen/tagshelf/internal/store"
	"github.com/zachjamesgreen/tagshelf/internal/tagwrite"
)

// On a terminal too short for it the help scrolls: every key can be read.
func TestHelpScrollsOnAShortTerminal(t *testing.T) {
	m := newModel(t, &fakeSource{snap: sampleSnapshot()}, 80, 24)
	press(t, m, "?")
	got := screen(m)
	checkFits(t, got, 80, 24)
	const first, last, end = "switch tab", "  q      quit", "The view reloads by itself"
	if !strings.Contains(got, first) || strings.Contains(got, last) || !strings.Contains(got, "↑↓ scroll") {
		t.Fatalf("the top of the help at 80x24:\n%s", got)
	}
	// A line at a time, every line of it passes.
	seen := map[string]bool{}
	for range len(helpLines()) {
		for _, line := range strings.Split(screen(m), "\n") {
			seen[line] = true
		}
		press(t, m, "down")
	}
	for _, line := range helpLines() {
		if !seen[ansi.Strip(line)] {
			t.Errorf("scrolling the help never showed %q", ansi.Strip(line))
		}
	}
	got = screen(m)
	checkFits(t, got, 80, 24)
	if strings.Contains(got, first) || !strings.Contains(got, last) || !strings.Contains(got, end) {
		t.Errorf("the end of the help at 80x24:\n%s", got)
	}
	press(t, m, "home")
	if got := screen(m); !strings.Contains(got, first) || strings.Contains(got, last) {
		t.Errorf("home did not go back to the top:\n%s", got)
	}
	press(t, m, "end")
	if got := screen(m); !strings.Contains(got, end) {
		t.Errorf("end did not go to the end:\n%s", got)
	}
	press(t, m, "pgup")
	press(t, m, "pgdown")
	if got := screen(m); !strings.Contains(got, end) {
		t.Errorf("a page up and a page down did not come back to the end:\n%s", got)
	}
	// It opens at the top each time.
	press(t, m, "?")
	press(t, m, "?")
	if got := screen(m); !strings.Contains(got, first) {
		t.Errorf("the help opened again scrolled:\n%s", got)
	}

	// Where it fits there is nothing to scroll.
	m = newModel(t, &fakeSource{snap: sampleSnapshot()}, 120, 32)
	press(t, m, "?")
	press(t, m, "down")
	if got := screen(m); !strings.Contains(got, first) || !strings.Contains(got, end) || strings.Contains(got, "scroll") {
		t.Errorf("the help at 120x32:\n%s", got)
	}
}

// However small the terminal, the message says first what size it needs.
func TestTooSmallMessageShowsTheSizeNeeded(t *testing.T) {
	for _, size := range [][2]int{{39, 11}, {39, 30}, {30, 30}, {80, 11}, {20, 3}, {12, 5}, {5, 2}, {5, 1}} {
		w, h := size[0], size[1]
		m := New(&fakeSource{snap: sampleSnapshot()}, header)
		m.Update(windowSize(w, h))
		got := screen(m)
		checkFits(t, got, w, h)
		lines := strings.Split(got, "\n")
		if !strings.HasPrefix(lines[0], "40x12") {
			t.Errorf("%dx%d: the first line is %q, want it to start with the size needed", w, h, lines[0])
		}
		// With the lines to wrap on, all of it shows.
		if words := strings.Join(strings.Fields(got), " "); h >= 5 && w >= 9 && words != fmt.Sprintf("40x12 needed: this terminal is %dx%d", w, h) {
			t.Errorf("%dx%d: the message reads %q", w, h, words)
		}
	}
}

func TestShortPath(t *testing.T) {
	m := New(&fakeSource{}, header)
	for path, want := range map[string]string{
		p(`D:\Music\A\b.mp3`): p(`A\b.mp3`),
		// A folder of the library whose name starts with dots is inside it.
		p(`D:\Music\...And You Will Know Us by the Trail of Dead\01.mp3`): p(`...And You Will Know Us by the Trail of Dead\01.mp3`),
		p(`D:\Music\..hidden.mp3`):   `..hidden.mp3`,
		p(`D:\Music\..\Other\b.mp3`): p(`D:\Music\..\Other\b.mp3`),
		p(`D:\Other\b.mp3`):          p(`D:\Other\b.mp3`),
		p(`D:\Music`):                p(`D:\Music`),
		p(`D:\`):                     p(`D:\`),
		p(`E:\x.mp3`):                p(`E:\x.mp3`),
	} {
		if got := m.shortPath(path); got != want {
			t.Errorf("shortPath(%q) = %q, want %q", path, got, want)
		}
	}
}

// A key press may type more than one character: a word an input method
// commits. All of it goes into the query.
func TestLibraryQueryTakesKeysOfSeveralCharacters(t *testing.T) {
	m, _, src := libraryModel(t, "")
	hit(t, m, "/")
	m.Update(textKey("日本"))
	typeText(t, m, " x")
	m.Update(textKey("geo gadi"))
	if m.library.query != "日本 xgeo gadi" {
		t.Fatalf("the query = %q, want all that was typed", m.library.query)
	}
	hit(t, m, "ctrl+u")
	m.Update(textKey("geogadi"))
	hit(t, m, "enter")
	if len(src.searches) != 1 || src.searches[0] != "geogadi" || len(m.library.results) != 2 {
		t.Errorf("searched for %q and found %d files", src.searches, len(m.library.results))
	}
	// Not typing, text is not the query's.
	m.Update(textKey("more"))
	if m.library.query != "geogadi" {
		t.Errorf("a key outside the query field changed it to %q", m.library.query)
	}
}

// dirty is a tag with a line break, an escape sequence and other control
// characters in it, and cleaned how it is shown.
const (
	dirty   = "Li\nve\x1b[31m\a\t\r\u2028!"
	cleaned = "Li ve [31m    !"
)

// checkClean fails when the screen lets through any of what dirty holds, or
// is no longer as many lines as the terminal has.
func checkClean(t *testing.T, m *Model, where string) {
	t.Helper()
	raw := m.View().Content
	for _, bad := range []string{"\x1b[31m", "\a", "\t", "\r", "\u2028"} {
		if strings.Contains(raw, bad) {
			t.Errorf("%s: the screen holds %q from a tag:\n%q", where, bad, raw)
		}
	}
	got := screen(m)
	checkFits(t, got, m.width, m.height)
	if !strings.Contains(got, "Li ve [31m") {
		t.Errorf("%s: the tag is not shown, or not as %q:\n%s", where, cleaned, got)
	}
}

// Tags, file names and error messages are drawn with nothing in them that
// would break a line or reach the terminal as an escape sequence.
func TestWhatFilesHoldIsShownClean(t *testing.T) {
	if got := clean(dirty); got != cleaned {
		t.Fatalf("clean(%q) = %q, want %q", dirty, got, cleaned)
	}
	if got := clean("Björk – Jóga 東京"); got != "Björk – Jóga 東京" {
		t.Errorf("clean changed plain text to %q", got)
	}

	// The Untagged list, grouped by the album tag, and a toast.
	src := untaggedSource()
	u := src.snap.Untagged
	u[0].Title, u[0].Album, u[1].Album = dirty, dirty, dirty
	m, f := newUntaggedModel(t, src)
	checkClean(t, m, "the Untagged list")
	press(t, m, "v")
	checkClean(t, m, "the Untagged list's headings")
	f.err = errors.New(dirty)
	press(t, m, "a")
	checkClean(t, m, "a toast")
	f.err = nil

	// The editor's marker line, and the lookup's and the release match's diffs.
	cursorTo(t, m, 101)
	src.tags = map[string]tagwrite.Values{}
	for _, file := range u {
		src.tags[file.Path] = tagwrite.Values{tagwrite.Title: {"Plain"}, tagwrite.Marker: {dirty}}
	}
	hit(t, m, "e")
	checkClean(t, m, "the editor")
	hit(t, m, "esc")
	for _, file := range u {
		src.tags[file.Path] = tagwrite.Values{tagwrite.Title: {dirty}}
	}
	mb := julie()
	mb.release.Media[0].Tracks[1].Title = "Julie" + dirty
	mb.pages[0].Recordings[0].Title = "Julie" + dirty
	m.mb = mb
	hit(t, m, "l")
	if m.look == nil {
		t.Fatalf("the lookup did not open:\n%s", screen(m))
	}
	toAlbum(t, m)
	checkClean(t, m, "the lookup's matches")
	hit(t, m, "enter")
	checkClean(t, m, "the lookup's diff")
	hit(t, m, "esc", "esc", "esc")
	geo := geogaddi()
	geo.release.Media[0].Tracks[0].Title = "Ready" + dirty
	geo.release.Title = "Geo" + dirty
	geo.candidates[0].Release.Title = "Geo" + dirty
	m.mb = geo
	hit(t, m, "L", "enter")
	checkClean(t, m, "the releases")
	hit(t, m, "enter")
	checkClean(t, m, "the release match's tracks")
	hit(t, m, "enter")
	checkClean(t, m, "the release match's diff")

	// The Duplicates tab, the Library tab's results and a run's detail.
	dsrc := dupSource()
	dsrc.snap.Groups[0].Files[0].Title, dsrc.snap.Groups[0].Files[1].Album = dirty, dirty
	dm, _ := newDupModel(t, dsrc)
	checkClean(t, dm, "the Duplicates tab")
	lsrc := librarySrc()
	lsrc.library[0].Album, lsrc.library[1].Title = "Geogadi "+dirty, "Geogadi "+dirty
	lm := newModel(t, lsrc, 120, 32)
	hit(t, lm, "4", "/")
	typeText(t, lm, "geogadi")
	hit(t, lm, "enter")
	checkClean(t, lm, "the Library tab")
	hsrc := historySource()
	hsrc.details[5] = RunDetail{Errors: []store.RunError{{Path: p(`E:\Incoming\bad.flac`), Message: dirty}}}
	hm := newModel(t, hsrc, 120, 32)
	hit(t, hm, "5", "down", "down", "enter")
	checkClean(t, hm, "a run's detail")
	// And the events of a run going on.
	fr := newFakeRunner()
	rm := newRunModel(t, fr)
	press(t, rm, "o")
	waitStart(t, fr)
	fr.send(t, engine.PhaseStarted{Phase: engine.PhaseAudio, Files: 4})
	fr.send(t, engine.FileError{Path: p(`E:\Incoming\bad.mp3`), Op: engine.OpOpen, Err: errors.New(dirty)})
	checkClean(t, rm, "a run going on")
	fr.end(t, engine.RunFinished{})
	runEnded(t, rm)
}

// A list as long as a library draws no more than the rows in view.
func TestListsFormatOnlyTheRowsShown(t *testing.T) {
	const many = 20000
	// However long the list, drawing it takes about what a screen of rows
	// takes: far fewer allocations than there are rows.
	few := func(what string, draw func()) {
		t.Helper()
		if n := testing.AllocsPerRun(3, draw); n > many/4 {
			t.Errorf("%s of %d rows allocates %.0f times: every row is still made", what, many, n)
		}
	}

	src := untaggedSource()
	for i := range many {
		f := src.snap.Untagged[0]
		f.ID = int64(1000 + i)
		f.Path = filepath.Join(p(`D:\Music\_Untagged`), fmt.Sprintf("rip %03d", i/40), fmt.Sprintf("track %05d.flac", i))
		f.From = filepath.Join(p(`E:\Rips`), fmt.Sprintf("rip %03d", i/40), fmt.Sprintf("track %05d.flac", i))
		src.snap.Untagged = append(src.snap.Untagged, f)
	}
	m, _ := newUntaggedModel(t, src)
	for range 300 {
		press(t, m, "pgdown")
	}
	few("the Untagged tab", func() { m.untaggedBody(27) })
	few("a key on the Untagged tab", func() { m.untaggedKey("down") })

	hsrc := historySource()
	var moves []store.Move
	for i := range many {
		moves = append(moves, store.Move{From: fmt.Sprintf(p(`E:\in\%05d.mp3`), i), To: fmt.Sprintf(p(`D:\Music\_Untagged\%05d.mp3`), i), Reason: store.ReasonUntagged})
	}
	hsrc.details[5] = RunDetail{Moves: moves}
	hm := newModel(t, hsrc, 120, 32)
	hit(t, hm, "5", "down", "down", "enter")
	for range 50 {
		press(t, hm, "pgdown")
	}
	few("a run's detail", func() { hm.runsBody(27) })
	few("a key in a run's detail", func() { hm.runsKey("down") })
	few("G in a run's detail", func() { hm.runsKey("G") })
	if got := screen(hm); !strings.Contains(got, fmt.Sprintf("%05d.mp3", many-1)) || !strings.Contains(got, "Moves (20,000)") && !strings.Contains(got, "Run 5") {
		t.Errorf("the end of a long run's detail:\n%s", got)
	}
}

// Whichever way the list is grouped and wherever the cursor is, the Untagged
// tab shows the cursor's file on the cursor's line, the first heading at
// the top and the last file at the bottom.
func TestUntaggedWindowFollowsTheCursor(t *testing.T) {
	src := untaggedSource()
	for i := range 70 {
		f := src.snap.Untagged[i%5]
		f.ID = int64(1000 + i)
		f.Path = filepath.Join(p(`D:\Music\_Untagged`), fmt.Sprintf("more %02d.flac", i))
		if i%3 == 0 {
			f.From = fmt.Sprintf(p(`E:\Folder %d\more %02d.flac`), i/9, i)
		}
		src.snap.Untagged = append(src.snap.Untagged, f)
	}
	m, _ := newUntaggedModel(t, src)
	for range groupingCount {
		u := &m.untagged
		u.moveTo(0)
		top := strings.Split(screen(m), "\n")[5]
		for i := range u.files {
			u.moveTo(i)
			got := screen(m)
			checkFits(t, got, 120, 32)
			want := filepath.Base(u.files[i].Path)
			if line := cursorLine(got); !strings.Contains(line, want) {
				t.Fatalf("grouped by %s, cursor on file %d: the line under the cursor is %q, want %s:\n%s", groupingNames[u.by], i, line, want, got)
			}
			if strings.Count(got, "› ") != 1 {
				t.Fatalf("grouped by %s, cursor on file %d: not one cursor line:\n%s", groupingNames[u.by], i, got)
			}
		}
		// The body ends two lines above the end of the screen.
		lines := strings.Split(screen(m), "\n")
		if last := lines[len(lines)-3]; !strings.HasPrefix(last, "› ") {
			t.Errorf("grouped by %s: with the cursor on the last file the list ends with %q", groupingNames[u.by], last)
		}
		u.moveTo(0)
		if got := strings.Split(screen(m), "\n")[5]; got != top {
			t.Errorf("grouped by %s: back at the top the list starts with %q, want %q", groupingNames[u.by], got, top)
		}
		press(t, m, "v")
	}
}

// A group's suggested keeper is worked out once, and again when the groups
// are read again: their copies may have changed.
func TestSuggestedKeeperIsKeptUntilTheGroupsReload(t *testing.T) {
	src := dupSource()
	m, _ := newDupModel(t, src)
	screen(m)
	g := m.snap.Groups[0]
	if id, ok := m.dups.suggested[groupKey(g)]; !ok || id != 12 {
		t.Fatalf("after drawing, the suggested keeper kept for the first group is %d (kept %t), want the FLAC (12)", id, ok)
	}
	// What was worked out is what is used.
	m.dups.suggested[groupKey(g)] = 11
	if id := m.dups.keeper(g); id != 11 {
		t.Errorf("keeper = %d, want the one already worked out (11)", id)
	}
	// The FLAC is read again as a poor MP3: the suggestion follows.
	flac := &src.snap.Groups[0].Files[1]
	flac.Lossless, flac.Bitrate, flac.Format = false, 64, "MP3"
	src.version++
	tickNow(t, m)
	if id := m.dups.keeper(m.snap.Groups[0]); id != 11 {
		t.Errorf("after the reload the keeper is %d, want the better MP3 (11)", id)
	}
	flac.Lossless, flac.Bitrate = true, 900
	src.version++
	tickNow(t, m)
	if id := m.dups.keeper(m.snap.Groups[0]); id != 12 {
		t.Errorf("after the second reload the keeper is %d, want the lossless copy again (12)", id)
	}
	// A copy chosen with k outlives a reload.
	press(t, m, "k")
	src.version++
	tickNow(t, m)
	if id := m.dups.keeper(m.snap.Groups[0]); id != 11 {
		t.Errorf("the copy chosen with k is no longer the keeper after a reload: %d", id)
	}
}

// Git must check the golden screens out with the line endings they were
// written with, whatever core.autocrlf says.
func TestGoldenFilesKeepTheirLineEndings(t *testing.T) {
	attrs, err := os.ReadFile(filepath.Join("..", "..", ".gitattributes"))
	if err != nil {
		t.Fatal(err)
	}
	const want = "internal/tui/testdata/*.golden text eol=lf"
	found := false
	for _, line := range strings.Split(string(attrs), "\n") {
		found = found || strings.TrimSpace(line) == want
	}
	if !found {
		t.Errorf(".gitattributes lacks the line %q", want)
	}
	files, err := filepath.Glob(filepath.Join("testdata", "*.golden"))
	if err != nil || len(files) == 0 {
		t.Fatalf("golden files: %v, %v", files, err)
	}
	for _, file := range files {
		data, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(data, []byte("\r")) {
			t.Errorf("%s has CRLF line endings", file)
		}
	}
}

// What the golden screens showed that was wrong: a Keeps column cut where
// the copies differ, a dry run told of as a real one, and results in an
// order the program would not give.
func TestWhatTheGoldenScreensShowedWrong(t *testing.T) {
	m, _ := newDupModel(t, dupSource())
	if line := cursorLine(screen(m)); !strings.HasSuffix(strings.TrimRight(line, " "), "05 - Julie and Candy.flac") || !strings.Contains(line, "…") {
		t.Errorf("the Keeps column does not end with the copy's file name: %q", line)
	}

	f := newFakeRunner()
	m = newRunModel(t, f)
	press(t, m, "d")
	press(t, m, "o")
	waitStart(t, f)
	f.end(t, engine.RunFinished{State: engine.StateOK, Counters: engine.Counters{Organized: 3}})
	runEnded(t, m)
	if m.toast != "Organize (dry run) finished" {
		t.Errorf("the toast after a dry run = %q", m.toast)
	}
	press(t, m, "d")
	press(t, m, "o")
	waitStart(t, f)
	f.end(t, engine.RunFinished{State: engine.StateInterrupted})
	runEnded(t, m)
	if m.toast != "Organize interrupted" {
		t.Errorf("the toast after a real run interrupted = %q", m.toast)
	}

	m = newModel(t, historySource(), 120, 32)
	press(t, m, "5")
	for _, line := range strings.Split(screen(m), "\n") {
		dry := strings.Contains(line, " dry ")
		switch {
		case dry && !strings.Contains(line, "Would move 12"):
			t.Errorf("the dry run's line does not say its files would have moved: %q", line)
		case !dry && strings.Contains(line, "Would move"):
			t.Errorf("a real run's line says its files would have moved: %q", line)
		}
	}
	if got := screen(m); !strings.Contains(got, "Would move 12") || !strings.Contains(got, "              48  ") {
		t.Errorf("the runs list:\n%s", got)
	}

	lm, _, _ := libraryModel(t, "a")
	var artists []string
	for _, file := range lm.library.results {
		artists = append(artists, file.Artist)
	}
	if strings.Join(artists, ", ") != "Autechre, Boards of Canada, Boards of Canada" {
		t.Errorf("the fake lists results by %v, want the store's order, by artist", artists)
	}
}

// Warnings a run gives are counted and listed where its errors are: on Home
// while it goes on and after it, and in the run's detail for as long as the
// TUI that did it is open. An error of a whole root shows like any other.
func TestRunShowsWarningsAndTheErrorOfAWholeRoot(t *testing.T) {
	f := newFakeRunner()
	m := newRunModel(t, f)
	src := m.src.(*fakeSource)
	press(t, m, "o")
	waitStart(t, f)
	f.send(t, engine.PhaseStarted{Phase: engine.PhaseWalk})
	f.send(t, engine.Warning{Kind: engine.WarnStaleCopy, Path: p(`D:\Music\A\.song.mp3.tagshelf-1.tmp`)})
	f.send(t, engine.Warning{Kind: engine.WarnLinkSkipped, Path: p(`E:\Incoming\Linked`)})
	f.send(t, engine.Warning{Kind: engine.WarnStaleCopy, Path: p(`D:\Music\A\.other.mp3.tagshelf-2.tmp`), Err: errors.New("access denied")})
	f.send(t, engine.FileError{Path: p(`E:\Incoming`), Op: engine.OpWalkRoot, Err: errors.New("the folder cannot be read")})
	f.send(t, engine.Progress{Phase: engine.PhaseWalk, Files: 3, Counters: engine.Counters{Errors: 1}})
	got := screen(m)
	for _, want := range []string{
		p("removed    A\\.song.mp3.tagshelf-1.tmp: a temporary copy a crash left"),
		p(`warning    E:\Incoming\Linked: left where it is: a link or junction to a folder`),
		p("warning    A\\.other.mp3.tagshelf-2.tmp: the temporary copy a crash left could not be removed: access denied"),
		p(`error      E:\Incoming: the folder cannot be read`),
		"Errors 1  Warnings 2",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the run going on lacks %q:\n%s", want, got)
		}
	}

	f.end(t, engine.RunFinished{RunID: 9, State: engine.StateErrors, Counters: engine.Counters{Errors: 1}})
	run := store.Run{ID: 9, Started: started, Kind: store.KindOrganize, Source: p(`E:\Incoming`), Destination: p(`D:\Music`),
		State: store.StateErrors, Counters: store.Counters{Errors: 1}}
	src.snap.Runs = []store.Run{run, {ID: 8, Started: started, Kind: store.KindIndex, State: store.StateOK}}
	src.details = map[int64]RunDetail{9: {Errors: []store.RunError{{Path: p(`E:\Incoming`), Message: "walk-root: the folder cannot be read"}}}}
	do(t, m, runEnded(t, m))
	got = screen(m)
	for _, want := range []string{"Organize finished with errors", "1 error ", "2 warnings", "5 Runs"} {
		if !strings.Contains(got, want) {
			t.Errorf("Home after the run lacks %q:\n%s", want, got)
		}
	}
	hit(t, m, "5", "enter")
	got = screen(m)
	for _, want := range []string{
		"Errors (1)", p(`E:\Incoming: walk-root: the folder cannot be read`),
		"Warnings (2)", p(`E:\Incoming\Linked: left where it is`), "could not be removed: access denied", "Moves (0)",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the run's detail lacks %q:\n%s", want, got)
		}
	}
	// Another run's detail has no warnings to list: none are recorded.
	hit(t, m, "esc", "down", "enter")
	if got := screen(m); strings.Contains(got, "Warnings") || !strings.Contains(got, "Run 8") {
		t.Errorf("the detail of a run this TUI did not do:\n%s", got)
	}

	// More warnings than are kept are still counted.
	live := &liveRun{}
	for i := range keptWarnings + 5 {
		live.sink(engine.Warning{Kind: engine.WarnLinkSkipped, Path: fmt.Sprintf(p(`E:\in\%d`), i)})
	}
	if live.warned != keptWarnings+5 || len(live.warnings) != keptWarnings {
		t.Errorf("%d warnings counted and %d kept, want %d and %d", live.warned, len(live.warnings), keptWarnings+5, keptWarnings)
	}
}

// An action's toast says what its run warned of: nothing else will.
func TestActionToastsTellOfWarnings(t *testing.T) {
	cue := engine.Warning{Kind: engine.WarnCueNotBeside, Path: p(`D:\Music\A\B\album.cue`), Name: "01.flac"}
	link := engine.Warning{Kind: engine.WarnLinkSkipped, Path: p(`D:\Music\A\Link`)}
	cleared := engine.Warning{Kind: engine.WarnStaleCopy, Path: p(`D:\Music\A\x.tmp`)}
	placed := engine.FilePlaced{Destination: p(`D:\Music\A\B\01 - C.mp3`), Outcome: engine.Organized}

	m, f := newUntaggedModel(t, untaggedSource())
	f.saveEvents = []engine.Event{placed, cue, cleared, link}
	f.saveFinish = engine.RunFinished{State: engine.StateOK, Counters: engine.Counters{Organized: 1}}
	press(t, m, "a")
	want := p(`2 warnings, the first: D:\Music\A\B\album.cue: cue sheet not rewritten: "01.flac" is not next to it after the move`)
	if !m.toastOK || !strings.Contains(m.toast, "Moved to") || !strings.Contains(m.toast, want) {
		t.Errorf("the toast of an accept that warned = %q (ok %t), want %q in it", m.toast, m.toastOK, want)
	}

	m, f, _ = editorOn(t, 104, tagwrite.Values{tagwrite.Title: {"Live"}})
	typeText(t, m, "!")
	f.saveEvents = []engine.Event{link}
	hit(t, m, "ctrl+s")
	if want := p(`1 warning: D:\Music\A\Link: left where it is`); !strings.HasPrefix(m.toast, "Saved") || !strings.Contains(m.toast, want) {
		t.Errorf("the toast of a save that warned = %q, want %q in it", m.toast, want)
	}

	m, f, _ = editorOn(t, 104, tagwrite.Values{tagwrite.Title: {"Live"}})
	typeText(t, m, "!")
	f.saveEvents = []engine.Event{cleared}
	hit(t, m, "ctrl+s")
	if strings.Contains(m.toast, "warning") {
		t.Errorf("a temporary copy cleared away is told of as a warning: %q", m.toast)
	}
}

// The texts of the warnings a run can give.
func TestWarningTexts(t *testing.T) {
	err := errors.New("denied")
	for kind, want := range map[engine.WarningKind]string{
		engine.WarnPermissions:  "its permissions could not be kept: denied",
		engine.WarnModTime:      "its modification time could not be kept: denied",
		engine.WarnCueNotBeside: `cue sheet not rewritten: "a.flac" is not next to it after the move`,
		engine.WarnCueEncoding:  `cue sheet not rewritten: it is not UTF-8 and "a.flac" is not plain ASCII`,
		engine.WarnLinkSkipped:  "left where it is: a link or junction to a folder, or to something that cannot be read",
		engine.WarnStaleCopy:    "the temporary copy a crash left could not be removed: denied",
	} {
		if got := warningText(engine.Warning{Kind: kind, Name: "a.flac", Err: err}); got != want {
			t.Errorf("warningText(%d) = %q, want %q", kind, got, want)
		}
	}
}

// An undo that leaves the run to be undone again does not say it is undone.
func TestUndoNotMarkedUndoneSaysSo(t *testing.T) {
	m, f, _ := undoOn(t, 2)
	f.undoFinish = engine.RunFinished{RunID: 8, State: engine.StateErrors, Counters: engine.Counters{OtherMoved: 3, Errors: 1},
		NotUndone: "run 5 is not marked undone and can be undone again: 1 of its steps failed"}
	hit(t, m, "enter")
	if m.undo != nil || len(f.undone) != 1 {
		t.Fatalf("undone %v, preflight open %t", f.undone, m.undo != nil)
	}
	if strings.Contains(m.toast, "Run 5 undone") || m.toastOK || strings.Contains(m.toast, "(u undo)") {
		t.Errorf("the toast says run 5 was undone, or offers to undo the undo: %q (ok %t)", m.toast, m.toastOK)
	}
	for _, want := range []string{"Run 5 is not undone yet: 3 files moved back", "1 skipped or failed: see 5 Runs", "undo it again for the rest"} {
		if !strings.Contains(m.toast, want) {
			t.Errorf("toast %q lacks %q", m.toast, want)
		}
	}

	// Nothing of it done at all.
	m, f, _ = undoOn(t, 2)
	f.undoFinish = engine.RunFinished{RunID: 8, State: engine.StateErrors, Counters: engine.Counters{Errors: 2},
		NotUndone: "run 5 is not marked undone and can be undone again: nothing of it could be undone"}
	hit(t, m, "enter")
	if want := "Run 5 is not undone yet: nothing could be put back; 2 skipped or failed: see 5 Runs"; !strings.Contains(m.toast, want) || m.toastOK {
		t.Errorf("toast = %q (ok %t), want %q in it", m.toast, m.toastOK, want)
	}

	// Skips that cannot pass leave the run undone: that much is said, and
	// the toast is not a success.
	m, f, _ = undoOn(t, 2)
	f.undoFinish = engine.RunFinished{RunID: 8, State: engine.StateErrors, Counters: engine.Counters{OtherMoved: 3, Errors: 1}}
	hit(t, m, "enter")
	if want := "Run 5 undone: 3 files moved back; 1 skipped or failed: see 5 Runs"; m.toast != want || m.toastOK {
		t.Errorf("toast = %q (ok %t), want %q", m.toast, m.toastOK, want)
	}
	// An interrupted undo.
	m, f, _ = undoOn(t, 2)
	f.undoFinish = engine.RunFinished{RunID: 8, State: engine.StateInterrupted, Counters: engine.Counters{OtherMoved: 3},
		NotUndone: "run 5 is not marked undone and can be undone again: the undo was stopped before its end"}
	hit(t, m, "enter")
	if want := "The undo was interrupted: 3 files moved back so far; undo the run again for the rest"; m.toast != want || m.toastOK {
		t.Errorf("toast = %q (ok %t), want %q", m.toast, m.toastOK, want)
	}
}

// The preflight marks what is skipped for a reason that can pass, and says
// what becomes of an edited cue sheet's original.
func TestUndoPreflightMarksWhatCanBeUndoneLater(t *testing.T) {
	src := undoSource()
	plan := src.plans[5]
	plan.Skipped = append(plan.Skipped, engine.UndoSkip{Path: p(`D:\Music\Singer\Album (2001)\album.cue`), Reason: "it has been edited since"})
	src.plans[5] = plan
	m := newModel(t, src, 120, 32)
	m.runner = newFakeRunner()
	hit(t, m, "5", "down", "down", "u")
	got := screen(m)
	for _, want := range []string{
		"Left as they are (3):",
		p("  Singer\\Album (2001)\\01 - One.flac  it has changed since it was indexed"),
		p("* _Untagged\\loose.mp3  something else is at its old path now"),
		"What is marked * can be undone later, once what is in the way is gone; the run stays to be undone again.",
		"A cue sheet left as it is keeps its text; the original kept beside it (.orig) still moves back with it.",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the preflight lacks %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "* Singer") {
		t.Errorf("a skip that cannot pass is marked as one that can:\n%s", got)
	}
	// With nothing that can pass and no cue sheet, neither is said.
	m, _, _ = undoOn(t, 3)
	plan = src.plans[5]
	plan.Skipped = plan.Skipped[:1]
	m.undo.plan = &plan
	if got := screen(m); strings.Contains(got, "marked *") || strings.Contains(got, ".orig") {
		t.Errorf("the preflight of a plan with one plain skip:\n%s", got)
	}
}
