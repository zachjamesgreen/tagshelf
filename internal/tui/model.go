package tui

import (
	"fmt"
	"time"

	tea "charm.land/bubbletea/v2"

	"tagshelf/internal/store"
)

// pollInterval is how often the TUI checks whether the library changed.
const pollInterval = time.Second

// Tab is one of the numbered tabs.
type Tab int

const (
	TabHome Tab = iota
	TabUntagged
	TabDuplicates
	TabLibrary
	TabRuns
	tabCount
)

var tabNames = [...]string{"Home", "Untagged", "Duplicates", "Library", "Runs"}

func (t Tab) String() string {
	if t < 0 || t >= tabCount {
		return fmt.Sprintf("Tab(%d)", int(t))
	}
	return tabNames[t]
}

// Header is what the header line says about the library.
type Header struct {
	Destination string
	LibraryID   string
	DryRun      bool
}

// Model is the TUI's state. Use New to make one.
type Model struct {
	src    Source
	header Header

	width, height int // 0 until the first WindowSizeMsg
	tab           Tab
	help          bool // the key help screen is showing

	loaded  bool // snap holds a first load
	snap    Snapshot
	version int64 // the data version snap was read at
	locked  bool  // another process holds the library lock
	holder  store.LockHolder

	// Each poll carries the sequence number it was started with; a result
	// older than one already applied is dropped.
	nextSeq, appliedSeq int

	toast   string
	toastOK bool // the toast reports success rather than a problem
	readErr bool // the toast is a failed poll, cleared by the next good one

	loc *time.Location // where times are shown
}

// New returns a model reading from src.
func New(src Source, h Header) *Model {
	return &Model{src: src, header: h, loc: time.Local}
}

// tickMsg says it is time to poll the library again.
type tickMsg struct{}

// refreshMsg is the result of a poll.
type refreshMsg struct {
	seq       int
	scheduled bool // from the poll loop, which goes on after it
	version   int64
	holder    store.LockHolder
	locked    bool
	snap      *Snapshot // nil when nothing changed and nothing was forced
	err       error
}

// Init starts the first load and, after it, the poll loop.
func (m *Model) Init() tea.Cmd {
	return m.poll(true, true)
}

// poll checks the library's data version and lock off the UI goroutine and
// reloads when either changed since what the model shows, or when forced.
func (m *Model) poll(force, scheduled bool) tea.Cmd {
	m.nextSeq++
	seq := m.nextSeq
	src := m.src
	known, knownLocked, knownHolder := m.version, m.locked, m.holder
	if !m.loaded {
		force = true
	}
	return func() tea.Msg {
		r := refreshMsg{seq: seq, scheduled: scheduled}
		if r.version, r.err = src.DataVersion(); r.err != nil {
			return r
		}
		if r.holder, r.locked, r.err = src.Lock(); r.err != nil {
			return r
		}
		if !force && r.version == known && r.locked == knownLocked && r.holder == knownHolder {
			return r
		}
		snap, err := src.Load()
		if err != nil {
			r.err = err
			return r
		}
		r.snap = &snap
		return r
	}
}

func tick() tea.Cmd {
	return tea.Tick(pollInterval, func(time.Time) tea.Msg { return tickMsg{} })
}

// Update handles a message.
func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
	case tickMsg:
		return m, m.poll(false, true)
	case refreshMsg:
		return m, m.refreshed(msg)
	case tea.KeyPressMsg:
		return m, m.key(msg.String())
	}
	return m, nil
}

// refreshed applies a poll's result.
func (m *Model) refreshed(r refreshMsg) tea.Cmd {
	var next tea.Cmd
	if r.scheduled {
		next = tick()
	}
	if r.seq < m.appliedSeq {
		return next
	}
	m.appliedSeq = r.seq
	if r.err != nil {
		m.setToast(false, "Cannot read the library: %v", r.err)
		m.readErr = true
		return next
	}
	if m.readErr {
		m.toast, m.readErr = "", false
	}
	m.version = r.version
	m.locked, m.holder = r.locked, r.holder
	if r.snap != nil {
		m.snap = *r.snap
		m.loaded = true
	}
	return next
}

func (m *Model) key(k string) tea.Cmd {
	m.toast, m.readErr = "", false
	switch k {
	case "q", "ctrl+c":
		return tea.Quit
	case "?":
		m.help = !m.help
		return nil
	case "esc":
		m.help = false
		return nil
	case "R":
		m.setToast(true, "Reloaded")
		return m.poll(true, false)
	case "1", "2", "3", "4", "5":
		m.tab = Tab(k[0] - '1')
		m.help = false
	case "tab":
		m.tab = (m.tab + 1) % tabCount
		m.help = false
	case "shift+tab":
		m.tab = (m.tab + tabCount - 1) % tabCount
		m.help = false
	}
	return nil
}

func (m *Model) setToast(ok bool, format string, args ...any) {
	m.toast = fmt.Sprintf(format, args...)
	m.toastOK = ok
	m.readErr = false
}

// ReadOnly reports whether actions that change files are disabled: another
// process holds the library lock.
func (m *Model) ReadOnly() bool {
	return m.locked
}

// activeRun is the run the lock holder is doing, nil when there is no lock
// holder or it has no run recorded as running.
func (m *Model) activeRun() *store.ActiveRun {
	if !m.locked {
		return nil
	}
	return m.snap.Active
}
