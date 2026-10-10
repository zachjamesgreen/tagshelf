package tui

import (
	"cmp"
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/zachjamesgreen/tagshelf/internal/engine"
	"github.com/zachjamesgreen/tagshelf/internal/store"
	"github.com/zachjamesgreen/tagshelf/internal/tagwrite"
)

// fakeSource is a library the tests change by hand.
type fakeSource struct {
	version int64
	holder  store.LockHolder
	locked  bool
	snap    Snapshot
	// err fails every method but Lock; the others fail one method alone.
	err                         error
	lockErr, loadErr, searchErr error
	loads                       int
	asked                       []Parts // what each load was asked for
	details                     map[int64]RunDetail
	detailed                    []int64                    // the runs whose detail was read
	tags                        map[string]tagwrite.Values // by path, for the tag editor
	// plans are what undoing each run would do, and planned which were asked.
	plans   map[int64]engine.UndoPlan
	planned []int64
	// library are the library's files, and searches what was searched for.
	library  []store.File
	searches []string
	// releases are the MusicBrainz releases the library holds.
	releases map[string]bool
}

func (f *fakeSource) UndoPlan(runID int64) (engine.UndoPlan, error) {
	f.planned = append(f.planned, runID)
	return f.plans[runID], f.err
}

// Search finds the library files with query somewhere in their tags, in the
// order the store lists them: by artist, album, disc and track.
func (f *fakeSource) Search(query string, limit int) ([]store.File, error) {
	f.searches = append(f.searches, query)
	var found []store.File
	for _, file := range f.library {
		if strings.Contains(strings.ToLower(file.Artist+" "+file.Album+" "+file.Title), strings.ToLower(query)) {
			found = append(found, file)
		}
	}
	artist := func(file store.File) string {
		return strings.ToLower(cmp.Or(file.AlbumArtist, file.Artist))
	}
	slices.SortStableFunc(found, func(a, b store.File) int {
		return cmp.Or(cmp.Compare(artist(a), artist(b)), cmp.Compare(strings.ToLower(a.Album), strings.ToLower(b.Album)),
			cmp.Compare(a.Disc, b.Disc), cmp.Compare(a.Track, b.Track), cmp.Compare(store.PathKey(a.RelPath), store.PathKey(b.RelPath)))
	})
	return found[:min(len(found), limit)], cmp.Or(f.searchErr, f.err)
}

func (f *fakeSource) Album(of store.File) ([]store.File, error) {
	var album []store.File
	for _, file := range f.library {
		if filepath.Dir(file.Path) == filepath.Dir(of.Path) {
			album = append(album, file)
		}
	}
	return album, f.err
}

func (f *fakeSource) LibraryReleases() (map[string]bool, error) {
	return f.releases, f.err
}

func (f *fakeSource) Tags(path string) (tagwrite.Values, error) {
	return f.tags[path], f.err
}

func (f *fakeSource) RunDetail(id int64) (RunDetail, error) {
	f.detailed = append(f.detailed, id)
	return f.details[id], f.err
}

func (f *fakeSource) DataVersion() (int64, error) { return f.version, f.err }

func (f *fakeSource) Lock() (store.LockHolder, bool, error) {
	return f.holder, f.locked, f.lockErr
}

// Load returns the snapshot with the lists asked for and no others, as the
// real source reads them.
func (f *fakeSource) Load(parts Parts) (Snapshot, error) {
	f.loads++
	f.asked = append(f.asked, parts)
	snap := f.snap
	snap.Parts = parts
	if !parts.Untagged {
		snap.Untagged = nil
	}
	if !parts.Groups {
		snap.Groups = nil
	}
	if !parts.Runs {
		snap.Runs = nil
	}
	return snap, cmp.Or(f.loadErr, f.err)
}

var (
	started = time.Date(2026, 9, 29, 14, 5, 0, 0, time.UTC)
	header  = Header{Destination: `D:\Music`, LibraryID: "3f2a1b2c-0d4e-4f60-8a7b-9c0d1e2f3a4b"}
)

// newModel returns a model over src that has had its first load, sized w by
// h, showing times in UTC. It does not wait for its next poll: the tests
// send the poll timer themselves, with tickNow.
func newModel(t *testing.T, src Source, w, h int) *Model {
	t.Helper()
	m := New(src, header)
	m.loc = time.UTC
	m.tick = func() tea.Cmd { return nil }
	m.Update(tea.WindowSizeMsg{Width: w, Height: h})
	do(t, m, m.Init())
	return m
}

// do runs a command and whatever it leads to, as pump does, and returns the
// first message it gave.
func do(t *testing.T, m *Model, cmd tea.Cmd) tea.Msg {
	t.Helper()
	return pump(t, m, cmd)
}

// press sends a key, runs what comes of it and returns the first message
// that gave.
func press(t *testing.T, m *Model, k string) tea.Msg {
	t.Helper()
	_, cmd := m.Update(keyMsg(k))
	return do(t, m, cmd)
}

// tickNow is the poll timer going off.
func tickNow(t *testing.T, m *Model) {
	t.Helper()
	_, cmd := m.Update(tickMsg{})
	do(t, m, cmd)
}

// textKey is a key press that types text, which may be more than one
// character: a word an input method commits.
func textKey(text string) tea.KeyPressMsg {
	return tea.KeyPressMsg{Code: []rune(text)[0], Text: text}
}

// keyMsg is the key press named k: a named key, ctrl+ a letter, or a single
// character.
func keyMsg(k string) tea.KeyPressMsg {
	switch k {
	case "left":
		return tea.KeyPressMsg{Code: tea.KeyLeft}
	case "right":
		return tea.KeyPressMsg{Code: tea.KeyRight}
	case "home":
		return tea.KeyPressMsg{Code: tea.KeyHome}
	case "end":
		return tea.KeyPressMsg{Code: tea.KeyEnd}
	case "tab":
		return tea.KeyPressMsg{Code: tea.KeyTab}
	case "shift+tab":
		return tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift}
	case "esc":
		return tea.KeyPressMsg{Code: tea.KeyEscape}
	case "ctrl+c":
		return tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl}
	case "up":
		return tea.KeyPressMsg{Code: tea.KeyUp}
	case "down":
		return tea.KeyPressMsg{Code: tea.KeyDown}
	case "enter":
		return tea.KeyPressMsg{Code: tea.KeyEnter}
	case "pgup":
		return tea.KeyPressMsg{Code: tea.KeyPgUp}
	case "pgdown":
		return tea.KeyPressMsg{Code: tea.KeyPgDown}
	}
	if rest, ok := strings.CutPrefix(k, "ctrl+"); ok {
		return tea.KeyPressMsg{Code: []rune(rest)[0], Mod: tea.ModCtrl}
	}
	if k == "backspace" {
		return tea.KeyPressMsg{Code: tea.KeyBackspace}
	}
	if k == "space" {
		return tea.KeyPressMsg{Code: tea.KeySpace, Text: " "}
	}
	r := []rune(k)
	if len(r) != 1 {
		// A key made up from a name would be a letter carrying the name.
		panic("keyMsg: no key is named " + k)
	}
	return tea.KeyPressMsg{Code: r[0], Text: k}
}

func screen(m *Model) string {
	return ansi.Strip(m.View().Content)
}

func sampleSnapshot() Snapshot {
	return Snapshot{NeedReview: 3, Overview: store.Overview{
		Library: 18204, Untagged: 12, Other: 96, Duplicates: 58,
		LastRun: &store.Run{ID: 7, Kind: store.KindOrganize, Started: started.Add(-24 * time.Hour), State: store.StateErrors, Counters: store.Counters{Errors: 2}},
	}}
}

func lockedSource() *fakeSource {
	snap := sampleSnapshot()
	snap.Active = &store.ActiveRun{
		Run:      store.Run{ID: 8, Kind: store.KindOrganize, Started: started, State: store.StateRunning},
		Progress: store.RunProgress{Phase: "audio", Done: 1234, Total: 2050, At: started.Add(time.Minute)},
		Moves: []store.Move{
			{From: `E:\Incoming\a.flac`, To: `D:\Music\Boards of Canada\Geogaddi (2002)\01 - Ready Lets Go.flac`, Reason: store.ReasonOrganized},
			{From: `E:\Incoming\b.mp3`, To: `D:\Music\_Untagged\Incoming\b.mp3`, Reason: store.ReasonUntagged},
			{From: `E:\Incoming\cover.jpg`, To: `D:\Music\Boards of Canada\Geogaddi (2002)\cover.jpg`, Reason: store.ReasonCompanion},
		},
	}
	return &fakeSource{version: 5, locked: true, holder: store.LockHolder{PID: 8812, Started: started}, snap: snap}
}

func TestNothingRenderedBeforeTheSizeIsKnown(t *testing.T) {
	src := &fakeSource{snap: sampleSnapshot()}
	m := New(src, header)
	if got := m.View().Content; got != "" {
		t.Errorf("before any message: view = %q, want nothing", got)
	}
	do(t, m, m.Init())
	if got := m.View().Content; got != "" {
		t.Errorf("loaded but unsized: view = %q, want nothing", got)
	}
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	if got := screen(m); !strings.Contains(got, "Needs attention") {
		t.Errorf("sized view lacks the Home tab:\n%s", got)
	}
}

func TestZeroAndTinySizesDoNotPanic(t *testing.T) {
	for _, size := range [][2]int{{0, 0}, {1, 1}, {0, 24}, {80, 0}, {39, 11}, {40, 11}, {39, 12}} {
		for _, src := range []*fakeSource{{snap: sampleSnapshot()}, lockedSource()} {
			m := New(src, header)
			m.Update(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
			do(t, m, m.Init())
			got := screen(m)
			if size[0] > 0 && size[1] > 0 {
				if size[0] >= 5 && !strings.HasPrefix(got, "40x12") {
					t.Errorf("%dx%d: view = %q, want the size it needs", size[0], size[1], got)
				}
				checkFits(t, got, size[0], size[1])
			} else if got != "" {
				t.Errorf("%dx%d: view = %q, want nothing", size[0], size[1], got)
			}
		}
	}
}

// checkFits fails when the screen is not exactly h lines of at most w cells.
func checkFits(t *testing.T, s string, w, h int) {
	t.Helper()
	lines := strings.Split(s, "\n")
	if len(lines) != h {
		t.Errorf("screen has %d lines, want %d", len(lines), h)
	}
	for i, l := range lines {
		if n := ansi.StringWidth(l); n > w {
			t.Errorf("line %d is %d cells wide, more than %d: %q", i, n, w, l)
		}
	}
}

func TestMinimumSizeFits(t *testing.T) {
	locked := everySource()
	locked.locked, locked.holder, locked.snap.Active = true, lockedSource().holder, lockedSource().snap.Active
	for name, src := range map[string]*fakeSource{
		"nothing in the lists": {snap: sampleSnapshot()}, "locked, nothing in the lists": lockedSource(),
		"full lists": everySource(), "locked, full lists": locked,
	} {
		m := newModel(t, src, minWidth, minHeight)
		// Every tab, a run's detail, the undo of a run, and the help.
		for _, k := range []string{"1", "2", "down", "3", "down", "4", "5", "down", "enter", "down", "u", "esc", "?", "down"} {
			press(t, m, k)
			got := screen(m)
			checkFits(t, got, minWidth, minHeight)
			if strings.Contains(got, "Reading the library") {
				t.Errorf("%s: after %s the tab's list is not drawn:\n%s", name, k, got)
			}
		}
	}
	// The lists have something in them at this size.
	m := newModel(t, everySource(), minWidth, minHeight)
	for k, want := range map[string]string{"2": "x.mp3", "3": "Julie and Candy", "5": "reorganize"} {
		press(t, m, k)
		if got := screen(m); !strings.Contains(got, want) {
			t.Errorf("at the smallest size tab %s lacks %q:\n%s", k, want, got)
		}
	}
}

func TestQuit(t *testing.T) {
	for _, k := range []string{"q", "ctrl+c"} {
		m := newModel(t, &fakeSource{}, 80, 24)
		if msg := press(t, m, k); msg != (tea.QuitMsg{}) {
			t.Errorf("%s: message = %#v, want tea.QuitMsg", k, msg)
		}
	}
}

func TestTabKeys(t *testing.T) {
	m := newModel(t, &fakeSource{snap: sampleSnapshot()}, 120, 32)
	steps := []struct {
		key  string
		want Tab
	}{
		{"3", TabDuplicates}, {"5", TabRuns}, {"tab", TabSettings}, {"tab", TabHome}, {"shift+tab", TabSettings}, {"shift+tab", TabRuns},
		{"6", TabSettings}, {"2", TabUntagged}, {"1", TabHome}, {"x", TabHome}, {"7", TabHome},
	}
	for _, s := range steps {
		press(t, m, s.key)
		if m.tab != s.want {
			t.Errorf("after %s: tab = %v, want %v", s.key, m.tab, s.want)
		}
	}
	press(t, m, "4")
	if got := screen(m); !strings.Contains(got, "Library  18,204 files") {
		t.Errorf("Library tab:\n%s", got)
	}
}

func TestTabsShowLiveCounts(t *testing.T) {
	src := &fakeSource{snap: sampleSnapshot()}
	m := newModel(t, src, 120, 32)
	tabs := strings.Split(screen(m), "\n")[1]
	if !strings.Contains(tabs, "2 Untagged 12") || !strings.Contains(tabs, "3 Duplicates 3") {
		t.Errorf("tabs = %q, want the Untagged and Duplicates counts", tabs)
	}

	src.version++
	src.snap.Overview.Untagged = 1500
	tickNow(t, m)
	if tabs := strings.Split(screen(m), "\n")[1]; !strings.Contains(tabs, "2 Untagged 1,500") {
		t.Errorf("tabs after a change = %q, want the new Untagged count", tabs)
	}
}

func TestHomeNeedsAttention(t *testing.T) {
	m := newModel(t, &fakeSource{snap: sampleSnapshot()}, 120, 32)
	got := screen(m)
	for _, want := range []string{
		"12 untagged files", "2 Untagged",
		"3 duplicate groups to review", "3 Duplicates",
		"2 errors in the last organize run of 2026-09-28 14:05", "5 Runs",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("Home lacks %q:\n%s", want, got)
		}
	}

	quiet := sampleSnapshot()
	quiet.Overview.Untagged, quiet.NeedReview = 0, 0
	quiet.Overview.LastRun.State = store.StateOK
	m = newModel(t, &fakeSource{snap: quiet}, 120, 32)
	if got := screen(m); !strings.Contains(got, "Nothing needs attention") {
		t.Errorf("Home with nothing to do:\n%s", got)
	}

	for state, want := range map[store.RunState]string{
		store.StateInterrupted: "The last organize run of 2026-09-28 14:05 was interrupted",
		store.StateCrashed:     "The last organize run of 2026-09-28 14:05 crashed",
	} {
		quiet.Overview.LastRun.State = state
		m = newModel(t, &fakeSource{snap: quiet}, 120, 32)
		if got := screen(m); !strings.Contains(got, want) {
			t.Errorf("Home after a %s run lacks %q:\n%s", state, want, got)
		}
	}
}

func TestPollReloadsOnlyWhenTheLibraryChanges(t *testing.T) {
	src := &fakeSource{version: 1, snap: sampleSnapshot()}
	m := newModel(t, src, 120, 32)
	if src.loads != 1 {
		t.Fatalf("loads after start = %d, want 1", src.loads)
	}
	tickNow(t, m)
	tickNow(t, m)
	if src.loads != 1 {
		t.Errorf("loads with nothing changed = %d, want 1", src.loads)
	}

	src.version = 2
	tickNow(t, m)
	if src.loads != 2 {
		t.Errorf("loads after the data version changed = %d, want 2", src.loads)
	}

	src.locked, src.holder = true, store.LockHolder{PID: 1, Started: started}
	tickNow(t, m)
	if src.loads != 3 {
		t.Errorf("loads after the lock was taken = %d, want 3", src.loads)
	}

	press(t, m, "R")
	if src.loads != 4 {
		t.Errorf("loads after R = %d, want 4", src.loads)
	}
	if got := screen(m); !strings.Contains(got, "Reloaded") {
		t.Errorf("no toast after R:\n%s", got)
	}
}

func TestPollGoesOnAfterEachResult(t *testing.T) {
	m := New(&fakeSource{}, header)
	msg := m.Init()()
	if _, cmd := m.Update(msg); cmd == nil {
		t.Error("the first load did not schedule the next poll")
	}
	_, cmd := m.Update(tickMsg{})
	if _, next := m.Update(cmd()); next == nil {
		t.Error("a poll did not schedule the next one")
	}
	// A reload by hand is not part of the loop, which already has a poll due.
	_, cmd = m.Update(keyMsg("R"))
	if _, next := m.Update(cmd()); next != nil {
		t.Error("a reload by hand started a second poll loop")
	}
}

func TestStalePollResultIsDropped(t *testing.T) {
	src := &fakeSource{version: 1, snap: sampleSnapshot()}
	m := newModel(t, src, 120, 32)

	_, older := m.Update(tickMsg{})
	src.version, src.snap.Overview.Untagged = 2, 40
	_, newer := m.Update(keyMsg("R"))
	m.Update(newer())
	// The older poll sees a version it does not know either, so it reads a
	// snapshot of its own: only its age keeps that from being shown.
	src.version, src.snap.Overview.Untagged = 3, 12
	msg := older() // started before the reload, finished after it
	if r, ok := msg.(refreshMsg); !ok || r.snap == nil || r.snap.Overview.Untagged != 12 {
		t.Fatalf("the older poll came back with %+v, want a snapshot of its own", msg)
	}
	m.Update(msg)

	if m.snap.Overview.Untagged != 40 {
		t.Errorf("untagged = %d, want 40 from the newer poll", m.snap.Overview.Untagged)
	}
}

func TestReadErrorShowsAToastAndKeepsTheView(t *testing.T) {
	src := &fakeSource{version: 1, snap: sampleSnapshot()}
	m := newModel(t, src, 120, 32)
	src.err = errors.New("disk gone")
	tickNow(t, m)
	got := screen(m)
	if !strings.Contains(got, "Cannot read the library: disk gone") {
		t.Errorf("no error toast:\n%s", got)
	}
	if !strings.Contains(got, "12 untagged files") {
		t.Errorf("the last good view is gone:\n%s", got)
	}

	src.err = nil
	src.version++
	tickNow(t, m)
	if got := screen(m); strings.Contains(got, "Cannot read") {
		t.Errorf("error toast outlived the problem:\n%s", got)
	}
}

func TestLockBanner(t *testing.T) {
	src := lockedSource()
	m := newModel(t, src, 120, 32)
	got := screen(m)
	for _, want := range []string{
		"Organize run in progress (pid 8812, started 2026-09-29 14:05): read-only until it ends",
		"audio", "1,234 / 2,050 files",
		`Boards of Canada\Geogaddi (2002)\01 - Ready Lets Go.flac`, `_Untagged\Incoming\b.mp3`,
		"read-only while another run holds the library lock",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("locked view lacks %q:\n%s", want, got)
		}
	}
	if !m.ReadOnly() {
		t.Error("ReadOnly() = false while another process holds the lock")
	}

	// The run finishes: its process lets the lock go and the view reloads.
	src.locked, src.holder = false, store.LockHolder{}
	src.version++
	src.snap.Active = nil
	src.snap.Overview.Untagged = 13
	tickNow(t, m)
	got = screen(m)
	if strings.Contains(got, "in progress") || strings.Contains(got, "read-only") {
		t.Errorf("banner still shown after the run ended:\n%s", got)
	}
	if !strings.Contains(got, "13 untagged files") {
		t.Errorf("view not reloaded after the run ended:\n%s", got)
	}
	if m.ReadOnly() {
		t.Error("ReadOnly() = true after the lock was released")
	}
}

func TestLockBannerWithoutARunningRun(t *testing.T) {
	src := lockedSource()
	src.snap.Active = nil
	m := newModel(t, src, 120, 32)
	if got := screen(m); !strings.Contains(got, "Library locked (pid 8812, started 2026-09-29 14:05): read-only until it ends") {
		t.Errorf("locked view:\n%s", got)
	}
}

func TestRunningRowIgnoredWithoutTheLock(t *testing.T) {
	// A run left running by a process that died shows no banner.
	src := lockedSource()
	src.locked, src.holder = false, store.LockHolder{}
	m := newModel(t, src, 120, 32)
	if got := screen(m); strings.Contains(got, "in progress") {
		t.Errorf("banner for a run nobody is doing:\n%s", got)
	}
}

func TestProgressLine(t *testing.T) {
	tests := []struct {
		p    store.RunProgress
		w    int
		want string
	}{
		{store.RunProgress{}, 80, "starting…"},
		{store.RunProgress{Phase: "walk", Done: 1500}, 80, "walk       1,500 files"},
		{store.RunProgress{Phase: "audio", Done: 5, Total: 10}, 34, "audio      █████░░░░░ 5 / 10 files"},
		{store.RunProgress{Phase: "audio", Done: 12, Total: 10}, 35, "audio      ██████████ 12 / 10 files"},
		{store.RunProgress{Phase: "audio", Done: 5, Total: 10}, 20, "audio       5 / 10 files"},
	}
	for _, tt := range tests {
		if got := ansi.Strip(progressLine(tt.p, tt.w)); got != tt.want {
			t.Errorf("progressLine(%+v, %d) = %q, want %q", tt.p, tt.w, got, tt.want)
		}
	}
}

func TestHelpScreen(t *testing.T) {
	m := newModel(t, &fakeSource{snap: sampleSnapshot()}, 120, 32)
	press(t, m, "?")
	if got := screen(m); !strings.Contains(got, "switch tab") {
		t.Errorf("help screen:\n%s", got)
	}
	press(t, m, "esc")
	if got := screen(m); strings.Contains(got, "switch tab") || !strings.Contains(got, "Needs attention") {
		t.Errorf("after esc:\n%s", got)
	}
}

func TestFormatCount(t *testing.T) {
	for n, want := range map[int64]string{0: "0", 999: "999", 1000: "1,000", 18204: "18,204", 1234567: "1,234,567", -1500: "-1,500"} {
		if got := formatCount(n); got != want {
			t.Errorf("formatCount(%d) = %q, want %q", n, got, want)
		}
	}
}

func windowSize(w, h int) tea.WindowSizeMsg {
	return tea.WindowSizeMsg{Width: w, Height: h}
}
