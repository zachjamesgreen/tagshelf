package tui

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/zachjamesgreen/tagshelf/internal/engine"
	"github.com/zachjamesgreen/tagshelf/internal/store"
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
	// SaveFiles saves tags to files that are not in the index, as one run:
	// they stay where they are, and the run is recorded in the library's
	// history all the same.
	SaveFiles(ctx context.Context, files []store.File, save engine.TagSave, sink engine.Sink) (engine.RunFinished, error)
	// CoverArt gives the album folders named the cover they lack, as one
	// run, by the cover art settings.
	CoverArt(ctx context.Context, folders []string, sink engine.Sink) (engine.RunFinished, error)
	// Undo undoes the run with the given ID as a run of its own; check and
	// restore see to the tag saves the run made.
	Undo(ctx context.Context, runID int64, check engine.TagCheck, restore engine.TagRestore, sink engine.Sink) (engine.RunFinished, error)
}

// recentEvents is how many of a run's latest events Home shows.
const recentEvents = 5

// keptWarnings is how many of a run's warnings are kept to list afterwards:
// they are recorded nowhere else.
const keptWarnings = 200

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
	warned   int64    // how many warnings there were
	warnings []string // the first keptWarnings of them
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
	case engine.Warning:
		text := warningText(e)
		if text == "" {
			// Something put right, not something wrong.
			r.note(recentEvent{label: "removed", path: e.Path, detail: "a temporary copy a crash left"})
			break
		}
		r.warned++
		if len(r.warnings) < keptWarnings {
			r.warnings = append(r.warnings, e.Path+": "+text)
		}
		r.note(recentEvent{label: "warning", path: e.Path, detail: text})
	}
}

// warningText says what a warning is about, without the path it is about;
// "" for one that only reports something put right.
func warningText(e engine.Warning) string {
	switch e.Kind {
	case engine.WarnPermissions:
		return fmt.Sprintf("its permissions could not be kept: %v", e.Err)
	case engine.WarnModTime:
		return fmt.Sprintf("its modification time could not be kept: %v", e.Err)
	case engine.WarnCueNotBeside:
		return fmt.Sprintf("cue sheet not rewritten: %q is not next to it after the move", e.Name)
	case engine.WarnCueEncoding:
		return fmt.Sprintf("cue sheet not rewritten: it is not UTF-8 and %q is not plain ASCII", e.Name)
	case engine.WarnPlaylistEntries:
		return fmt.Sprintf("playlist: %d of its entries were left as they are: not files, or files that are not there", e.Count)
	case engine.WarnPlaylistEncoding:
		return "playlist not rewritten: it is in Windows-1252, which cannot hold a new name"
	case engine.WarnLinkSkipped:
		return "left where it is: a link or junction, which is neither followed nor moved"
	case engine.WarnSpecialSkipped:
		return "left where it is: not a file but a pipe, socket or device"
	case engine.WarnCaseTwins:
		return fmt.Sprintf("%q beside it differs only in letter case: this one is used, and a reorganize moves what is in the other into it", e.Name)
	case engine.WarnDetailsUnread:
		return fmt.Sprintf("its audio details could not be read: %v", e.Err)
	case engine.WarnStaleCopy:
		if e.Err == nil {
			return ""
		}
		return fmt.Sprintf("the temporary copy a crash left could not be removed: %v", e.Err)
	}
	return "warning"
}

// warnings notes the warnings of an action, whose toast says how many there
// were and what the first was: they are recorded nowhere.
type warnings struct {
	mu    sync.Mutex
	n     int64
	first string
}

// sink notes e if it is a warning.
func (w *warnings) sink(e engine.Event) {
	warn, ok := e.(engine.Warning)
	if !ok {
		return
	}
	text := warningText(warn)
	if text == "" {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.n++; w.n == 1 {
		w.first = warn.Path + ": " + text
	}
}

// text is what a toast says of the warnings noted, "" when there were none.
func (w *warnings) text() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	switch w.n {
	case 0:
		return ""
	case 1:
		return "1 warning: " + w.first
	}
	return fmt.Sprintf("%s warnings, the first: %s", formatCount(w.n), w.first)
}

func (r *liveRun) note(e recentEvent) {
	r.recent = append(r.recent, e)
	if len(r.recent) > recentEvents {
		r.recent = r.recent[len(r.recent)-recentEvents:]
	}
}

// state reads what the sink has noted so far.
func (r *liveRun) state() (started bool, p engine.Progress, recent []recentEvent, warned int64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.started, r.progress, append([]recentEvent(nil), r.recent...), r.warned
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
	// warned is how many warnings the run gave, and warnings the first
	// keptWarnings of them.
	warned   int64
	warnings []string
}

// title names the run: its kind, and that it was a dry run.
func (l *lastRun) title() string {
	if l.dryRun {
		return capitalize(l.kind.String()) + " (dry run)"
	}
	return capitalize(l.kind.String())
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
	case m.noLibrary != nil && !m.noLibrary.CanRun:
		// What to do about it is on the screen already, under it.
		m.refuse(kind, &StartError{What: m.noLibrary.What, Setting: m.noLibrary.Setting})
		return nil
	case m.locked:
		m.refuse(kind, &StartError{
			What: fmt.Sprintf("another tagshelf run is working on this library (process %d, started %s)", m.holder.PID, m.formatTime(m.holder.Started)),
			Todo: "Wait for it to finish. Its progress is shown above.",
		})
		return nil
	case m.busy != nil:
		// It holds the library lock, which the run would be refused.
		m.setToast(false, "%s", stillBusy)
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
	last := &lastRun{kind: r.kind, dryRun: r.dryRun, finish: r.finish}
	r.mu.Lock()
	last.warned, last.warnings = r.warned, r.warnings
	r.mu.Unlock()
	switch {
	case r.err != nil:
		m.refuse(r.kind, r.err)
	case r.finish.State == engine.StateInterrupted:
		m.last = last
		m.setToast(false, "%s interrupted", last.title())
	default:
		m.last = last
		m.setToast(r.finish.State == engine.StateOK, "%s finished", last.title())
	}
	if m.noLibrary != nil {
		// The run may have made the index there was none of.
		return tea.Batch(m.poll(true, false), m.openAgain())
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
	started, p, recent, warned := r.state()
	if !started {
		b.WriteString("  " + dimStyle.Render("starting…") + "\n")
		return b.String()
	}
	b.WriteString("  " + progressLine(store.RunProgress{Phase: p.Phase.String(), Done: p.Files, Total: p.FilesTotal}, m.width-2) + "\n")
	b.WriteString("  " + countersLine(r.kind, p.Counters) + warnedText(warned) + "\n")
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
			line += ": " + clean(e.detail)
		}
		switch e.label {
		case "error":
			line = badStyle.Render(line)
		case "warning":
			line = accentStyle.Render(line)
		}
		b.WriteString(line + "\n")
	}
	return b.String()
}

// lastRunView draws how the latest run this TUI started ended, pointing at
// the tabs that deal with what it left.
func (m *Model) lastRunView(l *lastRun) string {
	var b strings.Builder
	title := l.title()
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
	if l.warned > 0 {
		// The run's detail lists them for as long as this TUI is open.
		items = append(items, attention{plural(l.warned, "warning", "warnings"), TabRuns})
	}
	if n := int64(l.finish.NeedReview); n > 0 {
		items = append(items, attention{plural(n, "duplicate group needs review", "duplicate groups need review"), TabDuplicates})
	}
	for _, it := range items {
		fmt.Fprintf(&b, "  %-58s %s\n", it.text, dimStyle.Render(fmt.Sprintf("%d %s", int(it.tab)+1, it.tab)))
	}
	return b.String()
}

// warnedText adds a run's warnings to its counters, when it gave any.
func warnedText(n int64) string {
	if n == 0 {
		return ""
	}
	return "  Warnings " + formatCount(n)
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
