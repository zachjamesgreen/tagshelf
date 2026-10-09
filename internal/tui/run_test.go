package tui

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"tagshelf/internal/engine"
	"tagshelf/internal/store"
)

// fakeRunner is a run the test drives: it tells the test it started, feeds
// the sink whatever the test sends, and ends when the test says so or its
// context is cancelled.
type fakeRunner struct {
	started chan fakeStart
	events  chan engine.Event
	sunk    chan struct{} // the sink has had the event sent
	finish  chan engine.RunFinished
	err     error // returned at once instead of running

	// What the Duplicates tab asked for, and what Resolve answers.
	resolved      [][]engine.Resolution
	keptBoth      []engine.Resolution // Keeper 0
	resolveFinish engine.RunFinished
}

func (f *fakeRunner) Resolve(ctx context.Context, resolutions []engine.Resolution) (engine.RunFinished, error) {
	if f.err != nil {
		return engine.RunFinished{}, f.err
	}
	f.resolved = append(f.resolved, resolutions)
	return f.resolveFinish, nil
}

func (f *fakeRunner) KeepBoth(kind store.GroupKind, members []int64) error {
	if f.err != nil {
		return f.err
	}
	f.keptBoth = append(f.keptBoth, engine.Resolution{Kind: kind, Members: members})
	return nil
}

type fakeStart struct {
	kind   RunKind
	dryRun bool
	ctx    context.Context
}

func newFakeRunner() *fakeRunner {
	return &fakeRunner{
		started: make(chan fakeStart, 1),
		events:  make(chan engine.Event),
		sunk:    make(chan struct{}),
		finish:  make(chan engine.RunFinished),
	}
}

func (f *fakeRunner) Run(ctx context.Context, kind RunKind, dryRun bool, sink engine.Sink) (engine.RunFinished, error) {
	if f.err != nil {
		return engine.RunFinished{}, f.err
	}
	f.started <- fakeStart{kind, dryRun, ctx}
	for {
		select {
		case e := <-f.events:
			sink(e)
			f.sunk <- struct{}{}
		case fin := <-f.finish:
			return fin, nil
		case <-ctx.Done():
			return engine.RunFinished{State: engine.StateInterrupted}, nil
		}
	}
}

// send hands the running fake an event and returns once the sink has it.
func (f *fakeRunner) send(t *testing.T, e engine.Event) {
	t.Helper()
	select {
	case f.events <- e:
	case <-time.After(5 * time.Second):
		t.Fatal("the run is not taking events")
	}
	<-f.sunk
}

func newRunModel(t *testing.T, r Runner) *Model {
	t.Helper()
	m := newModel(t, &fakeSource{snap: sampleSnapshot()}, 120, 32)
	m.runner = r
	return m
}

// waitStart waits for the fake to be asked to run.
func waitStart(t *testing.T, f *fakeRunner) fakeStart {
	t.Helper()
	select {
	case s := <-f.started:
		return s
	case <-time.After(5 * time.Second):
		t.Fatal("no run started")
	}
	return fakeStart{}
}

// runEnded waits for the model's run to return and delivers its end, as the
// waiting command would, returning what the model does next.
func runEnded(t *testing.T, m *Model) tea.Cmd {
	t.Helper()
	r := m.run
	if r == nil {
		t.Fatal("no run going")
	}
	select {
	case <-r.done:
	case <-time.After(5 * time.Second):
		t.Fatal("the run did not end")
	}
	_, cmd := m.Update(runDoneMsg{run: r})
	return cmd
}

func TestOrganizeFromHome(t *testing.T) {
	f := newFakeRunner()
	m := newRunModel(t, f)

	press(t, m, "o")
	if s := waitStart(t, f); s.kind != RunOrganize || s.dryRun {
		t.Fatalf("started %+v, want a real organize", s)
	}
	f.send(t, engine.RunStarted{Kind: engine.KindOrganize, Source: `E:\Incoming`, Destination: `D:\Music`})
	f.send(t, engine.PhaseStarted{Phase: engine.PhaseAudio, Files: 40})
	f.send(t, engine.FilePlaced{Path: `E:\Incoming\a.flac`, Destination: `D:\Music\Singer\Album\01 - Song.flac`, Outcome: engine.Organized})
	f.send(t, engine.FileError{Path: `E:\Incoming\bad.mp3`, Op: engine.OpOpen, Err: errors.New("access denied")})
	f.send(t, engine.Progress{Phase: engine.PhaseAudio, Files: 17, FilesTotal: 40, Rate: 7.5, ETA: 3 * time.Second,
		Counters: engine.Counters{Organized: 16, Errors: 1}})

	got := screen(m)
	for _, want := range []string{"Organize running", "audio", "17 / 40 files", "Organized 16", "Errors 1", "7.5 files/s", "ETA 3s",
		filepath.Join("Singer", "Album", "01 - Song.flac"), "bad.mp3", "ctrl+c interrupt"} {
		if !strings.Contains(got, want) {
			t.Errorf("live run lacks %q:\n%s", want, got)
		}
	}
	if msg := press(t, m, "o"); msg != nil || !strings.Contains(m.toast, "already") {
		t.Errorf("a second o: toast %q", m.toast)
	}

	f.finish <- engine.RunFinished{State: engine.StateErrors, NeedReview: 3,
		Counters: engine.Counters{Organized: 38, Untagged: 1, Errors: 1}}
	cmd := runEnded(t, m)
	if m.run != nil {
		t.Fatal("the run is still shown as going")
	}
	loads := m.src.(*fakeSource).loads
	do(t, m, cmd)
	if m.src.(*fakeSource).loads == loads {
		t.Error("the library was not reloaded after the run")
	}
	got = screen(m)
	for _, want := range []string{"Organize finished with errors", "Organized 38", "1 error", "5 Runs", "3 duplicate groups need review", "3 Duplicates"} {
		if !strings.Contains(got, want) {
			t.Errorf("finished run lacks %q:\n%s", want, got)
		}
	}
}

func TestRunKeys(t *testing.T) {
	for _, tt := range []struct {
		keys   []string
		kind   RunKind
		dryRun bool
	}{
		{[]string{"r"}, RunReorganize, false},
		{[]string{"i"}, RunIndex, false},
		{[]string{"d", "o"}, RunOrganize, true},
		{[]string{"d", "d", "r"}, RunReorganize, false},
	} {
		f := newFakeRunner()
		m := newRunModel(t, f)
		for _, k := range tt.keys {
			press(t, m, k)
		}
		s := waitStart(t, f)
		if s.kind != tt.kind || s.dryRun != tt.dryRun {
			t.Errorf("%v: started %v dry %t, want %v dry %t", tt.keys, s.kind, s.dryRun, tt.kind, tt.dryRun)
		}
		f.finish <- engine.RunFinished{}
		runEnded(t, m)
	}
}

func TestDryRunToggleShowsInHeader(t *testing.T) {
	m := newRunModel(t, newFakeRunner())
	press(t, m, "d")
	if !strings.Contains(screen(m), "[dry-run]") {
		t.Error("dry run on, but the header does not say so")
	}
	press(t, m, "d")
	if strings.Contains(screen(m), "[dry-run]") {
		t.Error("dry run off, but the header still says so")
	}
}

func TestCtrlCInterruptsTheRunNotTheTUI(t *testing.T) {
	f := newFakeRunner()
	m := newRunModel(t, f)
	press(t, m, "o")
	s := waitStart(t, f)

	if msg := press(t, m, "ctrl+c"); msg == (tea.QuitMsg{}) {
		t.Fatal("ctrl+c quit the TUI during a run")
	}
	select {
	case <-s.ctx.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("ctrl+c did not cancel the run")
	}
	if !strings.Contains(screen(m), "Interrupting") {
		t.Errorf("no interrupting notice:\n%s", screen(m))
	}
	if cmd := runEnded(t, m); cmd == nil {
		t.Fatal("no reload after the run")
	} else if msg := cmd(); msg == (tea.QuitMsg{}) {
		t.Fatal("the TUI quit after an interrupted run")
	}
	if !strings.Contains(screen(m), "Organize interrupted") {
		t.Errorf("finished view does not say interrupted:\n%s", screen(m))
	}
}

func TestQuitDuringRunAsksFirst(t *testing.T) {
	f := newFakeRunner()
	m := newRunModel(t, f)
	press(t, m, "o")
	s := waitStart(t, f)

	if msg := press(t, m, "q"); msg == (tea.QuitMsg{}) {
		t.Fatal("q quit without asking")
	}
	if !strings.Contains(screen(m), "Interrupt the run and quit?") {
		t.Fatalf("no question:\n%s", screen(m))
	}
	press(t, m, "n")
	if s.ctx.Err() != nil || strings.Contains(screen(m), "Interrupt the run and quit?") {
		t.Fatal("n did not keep the run going")
	}

	press(t, m, "q")
	if msg := press(t, m, "y"); msg == (tea.QuitMsg{}) {
		t.Fatal("quit before the run ended")
	}
	select {
	case <-s.ctx.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("y did not cancel the run")
	}
	cmd := runEnded(t, m)
	if cmd == nil || cmd() != (tea.QuitMsg{}) {
		t.Error("the TUI did not quit once the run ended")
	}
}

func TestRunRefusedWhileAnotherProcessHoldsTheLock(t *testing.T) {
	f := newFakeRunner()
	src := lockedSource()
	m := newModel(t, src, 120, 32)
	m.runner = f
	press(t, m, "o")
	if m.run != nil {
		t.Fatal("a run started while the library is locked")
	}
	if !strings.Contains(m.toast, "pid 8812") || m.toastOK {
		t.Errorf("toast = %q (ok %t), want an error naming the holder", m.toast, m.toastOK)
	}
}

// The lock can be taken between polls: then the runner reports it.
func TestRunnerLockedErrorShowsToast(t *testing.T) {
	f := newFakeRunner()
	f.err = &store.LockedError{Path: `D:\db.lock`, Holder: store.LockHolder{PID: 4242, Started: started}}
	m := newRunModel(t, f)
	press(t, m, "o")
	runEnded(t, m)
	if !strings.Contains(m.toast, "4242") || m.toastOK {
		t.Errorf("toast = %q (ok %t), want the lock error", m.toast, m.toastOK)
	}
	if strings.Contains(screen(m), "Organize finished") {
		t.Error("a run that never started is shown as finished")
	}
}

func TestNoRunnerShowsToast(t *testing.T) {
	m := newModel(t, &fakeSource{snap: sampleSnapshot()}, 120, 32)
	press(t, m, "o")
	if m.run != nil || m.toast == "" || m.toastOK {
		t.Errorf("run %v, toast %q: want an error toast and no run", m.run, m.toast)
	}
}

// Leaving the TUI however it ends cancels a run still going and waits for it.
func TestStopRunCancelsAndWaits(t *testing.T) {
	f := newFakeRunner()
	m := newRunModel(t, f)
	press(t, m, "o")
	s := waitStart(t, f)
	r := m.run
	m.stopRun()
	if s.ctx.Err() == nil {
		t.Error("stopRun did not cancel the run")
	}
	select {
	case <-r.done:
	default:
		t.Error("stopRun returned before the run ended")
	}
}

// TestGoldenRunViews snapshots Home during a run and after it, at 120x32.
func TestGoldenRunViews(t *testing.T) {
	f := newFakeRunner()
	m := newRunModel(t, f)
	press(t, m, "d")
	press(t, m, "o")
	waitStart(t, f)
	f.send(t, engine.PhaseStarted{Phase: engine.PhaseAudio, Files: 2050})
	f.send(t, engine.FilePlaced{Path: `E:\Incoming\a.flac`, Destination: `D:\Music\Boards of Canada\Geogaddi (2002)\01 - Ready Lets Go.flac`, Outcome: engine.Organized})
	f.send(t, engine.FilePlaced{Path: `E:\Incoming\b.mp3`, Destination: `D:\Music\_Duplicates\Incoming\b.mp3`, Outcome: engine.Duplicate})
	f.send(t, engine.Progress{Phase: engine.PhaseAudio, Files: 1234, FilesTotal: 2050, Rate: 41.3, ETA: 20 * time.Second,
		Counters: engine.Counters{Organized: 1200, Untagged: 20, Duplicates: 14}})
	got := screen(m)
	checkFits(t, got, 120, 32)
	golden(t, filepath.Join("testdata", "home_running.golden"), got)

	f.finish <- engine.RunFinished{State: engine.StateErrors, NeedReview: 3,
		Counters: engine.Counters{Organized: 2010, Untagged: 20, Duplicates: 18, Errors: 2}}
	runEnded(t, m)
	got = screen(m)
	checkFits(t, got, 120, 32)
	golden(t, filepath.Join("testdata", "home_finished.golden"), got)
}
