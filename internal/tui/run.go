package tui

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	tea "charm.land/bubbletea/v2"

	"tagshelf/internal/engine"
	"tagshelf/internal/store"
)

// RunKind is a run the TUI can start.
type RunKind int

const (
	RunOrganize   RunKind = iota // the configured source into the library
	RunReorganize                // the library re-sorted in place
	RunIndex                     // only the index brought up to date
)

var runKindNames = [...]string{"organize", "reorganize", "index"}

func (k RunKind) String() string {
	if k < 0 || int(k) >= len(runKindNames) {
		return fmt.Sprintf("RunKind(%d)", int(k))
	}
	return runKindNames[k]
}

// Runner does for the TUI whatever changes the library. Each method takes the
// library lock for as long as it lasts and returns an error when it could
// not start, a *store.LockedError when another process holds the lock. They
// are called off the UI goroutine, and a run's workers call sink.
type Runner interface {
	// Run does an organize, reorganize or index run and returns how it ended.
	Run(ctx context.Context, kind RunKind, dryRun bool, sink engine.Sink) (engine.RunFinished, error)
	// Resolve carries out decisions on duplicate groups as one run: each
	// group's keeper stays and its other copies move to the duplicates folder.
	Resolve(ctx context.Context, resolutions []engine.Resolution) (engine.RunFinished, error)
	// KeepBoth records that every copy of a duplicate group stays.
	KeepBoth(kind store.GroupKind, members []int64) error
	// SaveTags saves tags to the indexed files with the given IDs as one run
	// and moves those that then qualify into the library.
	SaveTags(ctx context.Context, ids []int64, save engine.TagSave, sink engine.Sink) (engine.RunFinished, error)
}

// recentEvents is how many of a run's latest events Home shows.
const recentEvents = 5

// runRefresh is how often Home redraws a run going on.
const runRefresh = 250 * time.Millisecond

// liveRun is a run this TUI started. Its sink only notes what it is told,
// under mu, so the run's workers never wait for the screen.
type liveRun struct {
	kind   RunKind
	dryRun bool
	cancel context.CancelFunc
	done   chan struct{} // closed once Run has returned

	// Set before done closes.
	finish engine.RunFinished
	err    error

	interrupting bool // the UI goroutine's alone

	mu       sync.Mutex
	started  bool // a phase has started
	progress engine.Progress
	recent   []recentEvent
}

// recentEvent is one line of a run's latest events.
type recentEvent struct {
	label, path, detail string
}

func (r *liveRun) sink(e engine.Event) {
	r.mu.Lock()
	defer r.mu.Unlock()
	switch e := e.(type) {
	case engine.PhaseStarted:
		r.started = true
		r.progress = engine.Progress{Counters: r.progress.Counters, Phase: e.Phase, FilesTotal: e.Files, BytesTotal: e.Bytes}
	case engine.Progress:
		r.started = true
		r.progress = e
	case engine.FilePlaced:
		if label := outcomeLabel(e.Outcome); label != "" {
			r.note(recentEvent{label: label, path: e.Destination})
		}
	case engine.FileError:
		r.note(recentEvent{label: "error", path: e.Path, detail: e.Err.Error()})
	}
}

func (r *liveRun) note(e recentEvent) {
	r.recent = append(r.recent, e)
	if len(r.recent) > recentEvents {
		r.recent = r.recent[len(r.recent)-recentEvents:]
	}
}

// state reads what the sink has noted so far.
func (r *liveRun) state() (started bool, p engine.Progress, recent []recentEvent) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.started, r.progress, append([]recentEvent(nil), r.recent...)
}

// outcomeLabel names a file's outcome for the recent events, "" for the ones
// that change nothing.
func outcomeLabel(o engine.Outcome) string {
	switch o {
	case engine.Organized:
		return "organized"
	case engine.Untagged:
		return "untagged"
	case engine.Other:
		return "other"
	case engine.Companion:
		return "companion"
	case engine.Duplicate:
		return "duplicate"
	case engine.Renamed:
		return "renamed"
	}
	return ""
}

// lastRun is how the latest run this TUI started ended.
type lastRun struct {
	kind   RunKind
	dryRun bool
	finish engine.RunFinished
}

// runDoneMsg says a run this TUI started has returned.
type runDoneMsg struct{ run *liveRun }

// runTickMsg says it is time to redraw a run going on.
type runTickMsg struct{}

func runTick() tea.Cmd {
	return tea.Tick(runRefresh, func(time.Time) tea.Msg { return runTickMsg{} })
}

// startRun starts a run of kind, unless one is going or the library is
// locked by another process.
func (m *Model) startRun(kind RunKind) tea.Cmd {
	switch {
	case m.run != nil:
		m.setToast(false, "A run is already going; ctrl+c interrupts it")
		return nil
	case m.locked:
		m.setToast(false, "Cannot start the %s: another tagshelf run holds the library lock (pid %d, started %s)",
			kind, m.holder.PID, m.formatTime(m.holder.Started))
		return nil
	case m.runner == nil:
		m.setToast(false, "Runs cannot be started from here")
		return nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	r := &liveRun{kind: kind, dryRun: m.header.DryRun && kind != RunIndex, cancel: cancel, done: make(chan struct{})}
	m.run = r
	runner := m.runner
	go func() {
		defer close(r.done)
		defer cancel()
		r.finish, r.err = runner.Run(ctx, kind, r.dryRun, r.sink)
	}()
	return tea.Batch(func() tea.Msg {
		<-r.done
		return runDoneMsg{run: r}
	}, runTick())
}

// runEnded shows how a run ended and reloads the library it changed.
func (m *Model) runEnded(r *liveRun) tea.Cmd {
	if r != m.run {
		return nil
	}
	m.run = nil
	switch {
	case r.err != nil:
		m.setToast(false, "Cannot start the %s: %v", r.kind, r.err)
	case r.finish.State == engine.StateInterrupted:
		m.last = &lastRun{kind: r.kind, dryRun: r.dryRun, finish: r.finish}
		m.setToast(false, "%s interrupted", capitalize(r.kind.String()))
	default:
		m.last = &lastRun{kind: r.kind, dryRun: r.dryRun, finish: r.finish}
		m.setToast(r.finish.State == engine.StateOK, "%s finished", capitalize(r.kind.String()))
	}
	if m.quitting {
		return tea.Quit
	}
	return m.poll(true, false)
}

// interrupt asks the run going on to stop: moves already under way finish.
func (m *Model) interrupt() {
	r := m.run
	if r == nil {
		return
	}
	if !r.interrupting {
		r.interrupting = true
		r.cancel()
	}
	m.setToast(false, "Interrupting the %s: finishing the files already moving", r.kind)
}

// stopRun interrupts a run still going and waits for it to end, for when the
// TUI closes.
func (m *Model) stopRun() {
	if r := m.run; r != nil {
		r.cancel()
		<-r.done
	}
}

// runView draws the run going on.
func (m *Model) runView(r *liveRun) string {
	var b strings.Builder
	title := capitalize(r.kind.String()) + " running"
	if r.dryRun {
		title += " (dry run)"
	}
	b.WriteString(boldStyle.Render(title))
	if r.interrupting {
		b.WriteString("  " + badStyle.Render("Interrupting: finishing the files already moving"))
	}
	b.WriteString("\n")
	started, p, recent := r.state()
	if !started {
		b.WriteString("  " + dimStyle.Render("starting…") + "\n")
		return b.String()
	}
	b.WriteString("  " + progressLine(store.RunProgress{Phase: p.Phase.String(), Done: p.Files, Total: p.FilesTotal}, m.width-2) + "\n")
	b.WriteString("  " + countersLine(r.kind, p.Counters) + "\n")
	rate := fmt.Sprintf("%.1f files/s", p.Rate)
	if p.Phase == engine.PhaseIndex && p.ByteRate > 0 {
		rate += fmt.Sprintf("  %.1f MB/s", p.ByteRate/1e6)
	}
	if p.ETA > 0 {
		rate += "  ETA " + max(p.ETA.Round(time.Second), time.Second).String()
	}
	b.WriteString("  " + dimStyle.Render(rate) + "\n")
	for _, e := range recent {
		line := dimStyle.Render(fmt.Sprintf("  %-10s ", e.label)) + m.shortPath(e.path)
		if e.detail != "" {
			line += ": " + e.detail
		}
		if e.label == "error" {
			line = badStyle.Render(line)
		}
		b.WriteString(line + "\n")
	}
	return b.String()
}

// lastRunView draws how the latest run this TUI started ended, pointing at
// the tabs that deal with what it left.
func (m *Model) lastRunView(l *lastRun) string {
	var b strings.Builder
	title := capitalize(l.kind.String())
	if l.dryRun {
		title += " (dry run)"
	}
	switch l.finish.State {
	case engine.StateErrors:
		title += " finished with errors"
	case engine.StateInterrupted:
		title += " interrupted"
	default:
		title += " finished"
	}
	b.WriteString(boldStyle.Render(title) + "\n")
	b.WriteString("  " + countersLine(l.kind, l.finish.Counters) + "\n")
	var items []attention
	if n := l.finish.Counters.Errors; n > 0 {
		items = append(items, attention{plural(n, "error", "errors"), TabRuns})
	}
	if n := int64(l.finish.NeedReview); n > 0 {
		items = append(items, attention{plural(n, "duplicate group needs review", "duplicate groups need review"), TabDuplicates})
	}
	for _, it := range items {
		fmt.Fprintf(&b, "  %-58s %s\n", it.text, dimStyle.Render(fmt.Sprintf("%d %s", int(it.tab)+1, it.tab)))
	}
	return b.String()
}

// countersLine sums up a run's counters.
func countersLine(kind RunKind, c engine.Counters) string {
	return countersText(kind == RunIndex, c.Organized, c.Untagged, c.OtherMoved, c.Duplicates, c.InPlace+c.LeftInPlace, c.Errors, c.Indexed)
}

// countersText sums up a run's counters, however they are held.
func countersText(index bool, organized, untagged, other, duplicates, inPlace, errs, indexed int64) string {
	if index {
		return fmt.Sprintf("Indexed %s  Errors %s", formatCount(indexed), formatCount(errs))
	}
	return fmt.Sprintf("Organized %s  Untagged %s  Non-audio %s  Duplicates %s  In place %s  Errors %s",
		formatCount(organized), formatCount(untagged), formatCount(other),
		formatCount(duplicates), formatCount(inPlace), formatCount(errs))
}
