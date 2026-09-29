package tui

import (
	"errors"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"tagshelf/internal/store"
)

// fakeSource is a library the tests change by hand.
type fakeSource struct {
	version int64
	holder  store.LockHolder
	locked  bool
	snap    Snapshot
	err     error
	loads   int
}

func (f *fakeSource) DataVersion() (int64, error) { return f.version, f.err }

func (f *fakeSource) Lock() (store.LockHolder, bool, error) {
	return f.holder, f.locked, nil
}

func (f *fakeSource) Load() (Snapshot, error) {
	f.loads++
	return f.snap, f.err
}

var (
	started = time.Date(2026, 9, 29, 14, 5, 0, 0, time.UTC)
	header  = Header{Destination: `D:\Music`, LibraryID: "3f2a1b2c-0d4e-4f60-8a7b-9c0d1e2f3a4b"}
)

// newModel returns a model over src that has had its first load, sized w by
// h, showing times in UTC.
func newModel(t *testing.T, src Source, w, h int) *Model {
	t.Helper()
	m := New(src, header)
	m.loc = time.UTC
	m.Update(tea.WindowSizeMsg{Width: w, Height: h})
	do(t, m, m.Init())
	return m
}

// do runs a command the way Bubble Tea would and feeds its message back,
// except for ticks, which would sleep; those the tests send themselves.
func do(t *testing.T, m *Model, cmd tea.Cmd) tea.Msg {
	t.Helper()
	if cmd == nil {
		return nil
	}
	msg := cmd()
	if _, ok := msg.(refreshMsg); ok {
		m.Update(msg)
	}
	return msg
}

// press sends a key and runs what it returns.
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

func keyMsg(k string) tea.KeyPressMsg {
	switch k {
	case "tab":
		return tea.KeyPressMsg{Code: tea.KeyTab}
	case "shift+tab":
		return tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift}
	case "esc":
		return tea.KeyPressMsg{Code: tea.KeyEscape}
	case "ctrl+c":
		return tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl}
	}
	r := []rune(k)[0]
	return tea.KeyPressMsg{Code: r, Text: k}
}

func screen(m *Model) string {
	return ansi.Strip(m.View().Content)
}

func sampleSnapshot() Snapshot {
	return Snapshot{Overview: store.Overview{
		Library: 18204, Untagged: 12, Other: 96, Duplicates: 58, ExactGroups: 3,
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
				if size[0] > 20 && !strings.Contains(strings.ReplaceAll(got, "\n", ""), "at least") {
					t.Errorf("%dx%d: view = %q, want the minimum size message", size[0], size[1], got)
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
	for _, src := range []*fakeSource{{snap: sampleSnapshot()}, lockedSource()} {
		m := newModel(t, src, minWidth, minHeight)
		for _, k := range []string{"1", "2", "3", "4", "5", "?"} {
			press(t, m, k)
			checkFits(t, screen(m), minWidth, minHeight)
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
		{"3", TabDuplicates}, {"5", TabRuns}, {"tab", TabHome}, {"shift+tab", TabRuns}, {"2", TabUntagged}, {"1", TabHome}, {"x", TabHome},
	}
	for _, s := range steps {
		press(t, m, s.key)
		if m.tab != s.want {
			t.Errorf("after %s: tab = %v, want %v", s.key, m.tab, s.want)
		}
	}
	press(t, m, "4")
	if got := screen(m); !strings.Contains(got, "18,204 files in the library") {
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
	quiet.Overview.Untagged, quiet.Overview.ExactGroups = 0, 0
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
	src.version, src.snap.Overview.Untagged = 1, 12
	m.Update(older()) // started before the reload, finished after it

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
