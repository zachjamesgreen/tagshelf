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

	runner      Runner
	run         *liveRun // the run this TUI started, nil when none is going
	last        *lastRun // how the latest run this TUI started ended
	confirmQuit bool     // asking whether to interrupt the run and quit
	quitting    bool     // quit once the run has ended

	runs     runsTab
	dups     dupsTab
	untagged untaggedTab
	edit     *editor // the tag editor, nil when it is not open

	compilations string // the folder name compilations are filed under

	// busy is closed when the action a tab is carrying out on the library has
	// ended, nil when none is.
	busy         chan struct{}
	cancelAction func()
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
		if m.edit != nil && m.edit.form != nil {
			m.edit.form.WithWidth(m.editorWidth()).WithHeight(m.editorHeight())
		}
	case tickMsg:
		return m, m.poll(false, true)
	case refreshMsg:
		return m, m.refreshed(msg)
	case runDoneMsg:
		return m, m.runEnded(msg.run)
	case runDetailMsg:
		m.detailLoaded(msg)
	case dupDoneMsg:
		return m, m.dupsDone(msg)
	case untaggedDoneMsg:
		return m, m.untaggedDone(msg)
	case editorTagsMsg:
		return m, m.editorLoaded(msg)
	case editorDoneMsg:
		return m, m.editorDone(msg)
	case runTickMsg:
		if m.run != nil {
			return m, runTick()
		}
	case tea.KeyPressMsg:
		if m.edit != nil && !m.confirmQuit {
			return m, m.editorKey(msg)
		}
		return m, m.key(msg.String())
	default:
		// What the editor's form sends itself.
		if m.edit != nil && m.edit.form != nil && !m.edit.saving {
			return m, m.formUpdate(msg)
		}
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
		m.dups.follow(m.snap.Groups)
		m.untagged.reload(m.snap.Untagged)
		if e := m.edit; e != nil {
			// The file being edited, as the index has it now.
			for _, f := range m.snap.Untagged {
				if f.ID == e.file.ID {
					e.file = f.File
				}
			}
		}
		return tea.Batch(next, m.runsReloaded())
	}
	return next
}

func (m *Model) key(k string) tea.Cmd {
	m.toast, m.readErr = "", false
	if m.confirmQuit {
		m.confirmQuit = false
		switch k {
		case "y", "Y":
			m.quitting = true
			m.interrupt()
		case "ctrl+c":
			m.interrupt()
		}
		return nil
	}
	if m.tab == TabRuns && !m.help && m.loaded {
		if cmd, ok := m.runsKey(k); ok {
			return cmd
		}
	}
	if m.tab == TabDuplicates && !m.help && m.loaded {
		if cmd, ok := m.dupsKey(k); ok {
			return cmd
		}
	}
	if m.tab == TabUntagged && !m.help && m.loaded {
		if cmd, ok := m.untaggedKey(k); ok {
			return cmd
		}
	}
	if m.tab == TabHome && !m.help {
		switch k {
		case "o":
			return m.startRun(RunOrganize)
		case "r":
			return m.startRun(RunReorganize)
		case "i":
			return m.startRun(RunIndex)
		case "d":
			m.header.DryRun = !m.header.DryRun
			if m.header.DryRun {
				m.setToast(true, "Dry run on: runs show what they would do and move nothing")
			} else {
				m.setToast(true, "Dry run off")
			}
			return nil
		}
	}
	switch k {
	case "q":
		if m.run != nil {
			m.confirmQuit = true
			return nil
		}
		return tea.Quit
	case "ctrl+c":
		if m.run != nil {
			m.interrupt()
			return nil
		}
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
		m.switchTab(Tab(k[0] - '1'))
	case "tab":
		m.switchTab((m.tab + 1) % tabCount)
	case "shift+tab":
		m.switchTab((m.tab + tabCount - 1) % tabCount)
	}
	return nil
}

// switchTab shows tab t, leaving any detail open on the tab it leaves.
func (m *Model) switchTab(t Tab) {
	if t != m.tab {
		m.runs.detail = nil
		m.dups.confirmAll = nil
	}
	m.tab = t
	m.help = false
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
