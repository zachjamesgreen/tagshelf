package tui

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zachjamesgreen/tagshelf/internal/store"
)

// historySource is a library with one run of every kind and state, newest
// first as Load returns them, and the detail of run 5.
func historySource() *fakeSource {
	src := &fakeSource{snap: sampleSnapshot()}
	day := func(d int) time.Time { return started.Add(time.Duration(d-7) * 24 * time.Hour) }
	src.snap.Runs = []store.Run{
		{ID: 7, Started: day(7), Kind: store.KindOrganize, Source: p(`E:\Incoming`), Destination: p(`D:\Music`), State: store.StateRunning},
		{ID: 6, Started: day(6), Kind: store.KindReorganize, Source: p(`D:\Music`), Destination: p(`D:\Music`), State: store.StateCrashed,
			Counters: store.Counters{Organized: 3}},
		{ID: 5, Started: day(5), Kind: store.KindOrganize, Source: p(`E:\Incoming`), Destination: p(`D:\Music`), State: store.StateErrors,
			Counters: store.Counters{Organized: 40, Untagged: 2, OtherMoved: 5, Duplicates: 1, Errors: 2}},
		{ID: 4, Started: day(4), Kind: store.KindOrganize, DryRun: true, Source: p(`E:\Incoming`), Destination: p(`D:\Music`), State: store.StateOK,
			Counters: store.Counters{Organized: 12}},
		{ID: 3, Started: day(3), Kind: store.KindIndex, Destination: p(`D:\Music`), State: store.StateInterrupted,
			Counters: store.Counters{Indexed: 9000}},
		{ID: 2, Started: day(2), Kind: store.KindTUIAction, Destination: p(`D:\Music`), State: store.StateOK},
		{ID: 1, Started: day(1), Kind: store.KindUndo, Destination: p(`D:\Music`), State: store.StateUndone},
	}
	src.details = map[int64]RunDetail{5: {
		Moves: []store.Move{
			{From: p(`E:\Incoming\Rip\01.flac`), To: p(`D:\Music\Singer\Album (2001)\01 - One.flac`), Reason: store.ReasonOrganized},
			{From: p(`E:\Incoming\loose.mp3`), To: p(`D:\Music\_Untagged\loose.mp3`), Reason: store.ReasonUntagged},
		},
		Errors: []store.RunError{
			{Path: p(`E:\Incoming\locked.mp3`), Message: p(`move -> D:\Music\Singer\x.mp3: file in use`)},
			{Path: p(`E:\Incoming\bad.flac`), Message: "open: access denied"},
		},
	}}
	return src
}

func TestRunsTabListsEveryRun(t *testing.T) {
	m := newModel(t, historySource(), 120, 32)
	press(t, m, "5")
	got := screen(m)
	// Newest first, every state named.
	var last int
	for _, want := range []string{"running", "crashed", "errors", "dry", "interrupted", "tui-action", "undone"} {
		i := strings.Index(got, want)
		if i < 0 {
			t.Errorf("the runs list lacks %q:\n%s", want, got)
			continue
		}
		if i < last {
			t.Errorf("%q is out of order:\n%s", want, got)
		}
		last = i
	}
	for _, want := range []string{p(`E:\Incoming -> D:\Music`), "in place", "48"} {
		if !strings.Contains(got, want) {
			t.Errorf("the runs list lacks %q:\n%s", want, got)
		}
	}
}

func TestRunsDetailNavigation(t *testing.T) {
	src := historySource()
	m := newModel(t, src, 120, 32)
	press(t, m, "5")
	press(t, m, "down")
	press(t, m, "j")
	if id := m.runs.selected(m.snap.Runs); id != 5 {
		t.Fatalf("selected run %d, want 5", id)
	}
	press(t, m, "enter")
	got := screen(m)
	for _, want := range []string{"Run 5", "Errors (2)", "locked.mp3", "file in use", "access denied",
		"Moves (2)", filepath.Join("Singer", "Album (2001)", "01 - One.flac"), "untagged"} {
		if !strings.Contains(got, want) {
			t.Errorf("the run detail lacks %q:\n%s", want, got)
		}
	}
	if !strings.Contains(m.keyHelp(), "esc back") {
		t.Errorf("key help in the detail = %q", m.keyHelp())
	}

	press(t, m, "esc")
	if m.runs.detail != nil {
		t.Fatal("esc did not close the detail")
	}
	if id := m.runs.selected(m.snap.Runs); id != 5 {
		t.Errorf("back in the list, run %d is selected, want 5", id)
	}

	// The cursor stays within the list, and on its run when the list reloads.
	for range 20 {
		press(t, m, "down")
	}
	if id := m.runs.selected(m.snap.Runs); id != 1 {
		t.Errorf("at the bottom, run %d is selected, want 1", id)
	}
	press(t, m, "g")
	press(t, m, "down")
	src.snap.Runs = append([]store.Run{{ID: 8, Started: started, Kind: store.KindIndex, State: store.StateOK}}, src.snap.Runs...)
	src.version++
	tickNow(t, m)
	if id := m.runs.selected(m.snap.Runs); id != 6 {
		t.Errorf("after a new run, run %d is selected, want 6", id)
	}

	// Switching tab leaves the detail.
	press(t, m, "enter")
	press(t, m, "1")
	press(t, m, "5")
	if m.runs.detail != nil {
		t.Error("the detail is still open after switching tabs")
	}
}

func TestRunDetailScrolls(t *testing.T) {
	src := historySource()
	var moves []store.Move
	for i := range 100 {
		moves = append(moves, store.Move{From: fmt.Sprintf(p(`E:\in\%03d.mp3`), i), To: fmt.Sprintf(p(`D:\Music\_Untagged\%03d.mp3`), i), Reason: store.ReasonUntagged})
	}
	src.details[5] = RunDetail{Moves: moves}
	m := newModel(t, src, 120, 32)
	press(t, m, "5")
	press(t, m, "G")
	for range len(m.snap.Runs) {
		if m.runs.selected(m.snap.Runs) == 5 {
			break
		}
		press(t, m, "up")
	}
	if id := m.runs.selected(m.snap.Runs); id != 5 {
		t.Fatalf("up never reached run 5: the cursor is on run %d", id)
	}
	press(t, m, "enter")
	if got := screen(m); !strings.Contains(got, "000.mp3") || strings.Contains(got, "099.mp3") {
		t.Fatalf("the detail does not start at the top:\n%s", got)
	}
	press(t, m, "G")
	if got := screen(m); !strings.Contains(got, "099.mp3") || strings.Contains(got, "000.mp3") {
		t.Errorf("G did not scroll to the end:\n%s", got)
	}
	checkFits(t, screen(m), 120, 32)
}

func TestEmptyRunsTab(t *testing.T) {
	m := newModel(t, &fakeSource{snap: sampleSnapshot()}, 120, 32)
	press(t, m, "5")
	press(t, m, "enter")
	if got := screen(m); !strings.Contains(got, "No runs yet") || m.runs.detail != nil {
		t.Errorf("empty runs tab:\n%s", got)
	}
}

func TestGoldenRunsViews(t *testing.T) {
	m := newModel(t, historySource(), 120, 32)
	press(t, m, "5")
	press(t, m, "down")
	got := screen(m)
	checkFits(t, got, 120, 32)
	golden(t, filepath.Join("testdata", "runs.golden"), got)

	press(t, m, "down")
	press(t, m, "enter")
	got = screen(m)
	checkFits(t, got, 120, 32)
	golden(t, filepath.Join("testdata", "run_detail.golden"), got)
}

func TestRunDetailReadFailureReturnsToList(t *testing.T) {
	m := newModel(t, historySource(), 120, 32)
	press(t, m, "5")
	_, cmd := m.Update(keyMsg("enter"))
	m.Update(runDetailMsg{id: m.runs.selected(m.snap.Runs), err: errors.New("disk gone")})
	_ = cmd
	if m.runs.detail != nil {
		t.Error("the detail stayed open after it could not be read")
	}
	if !strings.Contains(m.toast, "disk gone") || m.toastOK {
		t.Errorf("toast = %q, want the read error", m.toast)
	}
}

// Until a person moves it, the cursor is on the newest run, however many
// have been added since the tab's runs were first read.
func TestRunsCursorStartsOnTheNewestRun(t *testing.T) {
	src := historySource()
	all := src.snap.Runs
	src.snap.Runs = all[len(all)-1:]
	m := newModel(t, src, 120, 32)
	src.snap.Runs = all
	src.version++
	tickNow(t, m)
	press(t, m, "5")
	if id := m.runs.selected(m.snap.Runs); id != 7 {
		t.Errorf("the cursor is on run %d, want the newest (7)", id)
	}
	// Once moved, it stays on its run as newer ones arrive.
	press(t, m, "down")
	src.snap.Runs = append([]store.Run{{ID: 8, Kind: store.KindOrganize, State: store.StateOK}}, all...)
	src.version++
	tickNow(t, m)
	if id := m.runs.selected(m.snap.Runs); id != 6 {
		t.Errorf("after a newer run arrived the cursor is on run %d, want it still on 6", id)
	}
}
