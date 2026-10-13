package tui

import (
	"fmt"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/zachjamesgreen/tagshelf/internal/store"
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
	TabSettings
	TabFiles
	tabCount
)

var tabNames = [...]string{"Home", "Untagged", "Duplicates", "Library", "Runs", "Settings", "Files"}

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
	helpScroll    int  // the first line of the help shown

	loaded  bool // snap holds a first load
	snap    Snapshot
	version int64 // the data version snap was read at
	locked  bool  // another process holds the library lock
	holder  store.LockHolder
	// have are the lists snap holds as the library was at version, and seen
	// those it has held at all: a list no tab was showing is left as it was
	// when the library changes, and read again when its tab shows.
	have, seen Parts
	lastLoad   time.Time        // when snap was read
	now        func() time.Time // the clock, the tests' own
	tick       func() tea.Cmd   // waits for the next poll

	// Each poll carries the sequence number it was started with; a result
	// older than one already applied is dropped.
	nextSeq, appliedSeq int

	toast   string
	toastOK bool // the toast reports success rather than a problem
	readErr bool // the toast is a failed poll, cleared by the next good one

	loc *time.Location // where times are shown

	// noLibrary says why there is no library to show, nil when there is one.
	noLibrary *NoLibrary
	// reopen opens the library again; nil when there is no way to.
	reopen func() (Options, error)
	// refused is the run that could not start, shown until the next key.
	refused *refusal
	// reopening is set while the library is opened again after a save of the
	// settings: what a poll fails with meanwhile is the old one closing.
	reopening bool

	// settingsSrc is the config file the Settings tab edits, nil when there
	// is none to edit.
	settingsSrc Settings
	settings    settingsTab

	runner      Runner
	run         *liveRun // the run this TUI started, nil when none is going
	last        *lastRun // how the latest run this TUI started ended
	confirmQuit bool     // asking whether to interrupt the run or action and quit
	quitting    bool     // quit once the run and the action have ended

	runs     runsTab
	dups     dupsTab
	untagged untaggedTab
	library  libraryTab
	files    filesTab
	// source is the folder runs organize from, where the Files tab opens.
	source string
	edit   *editor   // the tag editor, nil when it is not open
	look   *lookup   // the MusicBrainz lookup, nil when it is not open
	match  *match    // the release match, nil when it is not open
	undo   *undoFlow // the undo preflight, nil when it is not open
	// undoable is the run the last action made, which u, as the next key,
	// offers to undo; 0 when there is none.
	undoable int64

	// mb is MusicBrainz, nil while no contact is configured.
	mb      MusicBrainz
	lookSeq int // the last sequence number given to a lookup's request

	compilations string // the folder name compilations are filed under
	// genreMode is what a MusicBrainz match does with the genre: config's
	// GenreOff, GenreFill or GenreReplace. Empty is GenreFill.
	genreMode string

	// busy is closed when the action a tab is carrying out on the library has
	// ended, nil when none is.
	busy         chan struct{}
	cancelAction func()
}

// New returns a model reading from src.
func New(src Source, h Header) *Model {
	return &Model{src: src, header: h, loc: time.Local, now: time.Now, tick: tick}
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
	return tea.Batch(m.poll(true, true), m.settingsShown())
}

// parts are the lists the tab showing needs. A flow is open over the tab it
// was opened from.
func (m *Model) parts() Parts {
	return Parts{Untagged: m.tab == TabUntagged, Groups: m.tab == TabDuplicates, Runs: m.tab == TabRuns}
}

// poll checks the library's data version and lock off the UI goroutine and
// reloads when either changed since what the model shows, when the tab
// showing needs a list the model does not have, or when forced. While a run
// is going, here or in another process, a change is only read once runReload
// has passed since the last load.
func (m *Model) poll(force, scheduled bool) tea.Cmd {
	m.nextSeq++
	seq := m.nextSeq
	src := m.src
	known, knownLocked, knownHolder := m.version, m.locked, m.holder
	parts := m.parts()
	if !m.loaded || m.have.lacks(parts) {
		force = true
	}
	wait := (m.run != nil || m.activeRun() != nil) && m.now().Sub(m.lastLoad) < runReload
	return func() tea.Msg {
		r := refreshMsg{seq: seq, scheduled: scheduled}
		if r.version, r.err = src.DataVersion(); r.err != nil {
			return r
		}
		if r.holder, r.locked, r.err = src.Lock(); r.err != nil {
			return r
		}
		if sameLock := r.locked == knownLocked && r.holder == knownHolder; !force && sameLock && (r.version == known || wait) {
			// A change left unread is still one to the next poll.
			r.version = known
			return r
		}
		snap, err := src.Load(parts)
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
	cmd := m.update(msg)
	// Asked to quit while a run or an action was going: now that they have
	// ended.
	if m.quitting && m.run == nil && m.busy == nil {
		return m, tea.Quit
	}
	return m, cmd
}

func (m *Model) update(msg tea.Msg) tea.Cmd {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		if m.edit != nil && m.edit.form != nil {
			m.edit.form.WithWidth(m.editorWidth()).WithHeight(m.editorHeight())
		}
		if m.look != nil && m.look.form != nil {
			m.look.form.WithWidth(m.editorWidth())
		}
		if m.match != nil && m.match.form != nil {
			m.match.form.WithWidth(m.editorWidth())
		}
	case tickMsg:
		return m.poll(false, true)
	case refreshMsg:
		return m.refreshed(msg)
	case reopenedMsg:
		return m.reopened(msg)
	case settingsLoadedMsg:
		m.settingsLoaded(msg)
	case settingsCheckedMsg:
		return m.settingsChecked(msg)
	case settingsSavedMsg:
		return m.settingsSaved(msg)
	case settingsHintMsg:
		if msg.seq == m.settings.hintSeq && m.settings.input != nil {
			m.settings.hint = msg.text
		}
	case filesReadMsg:
		m.filesRead(msg)
	case pickerReadMsg:
		m.pickerRead(msg)
	case pickerMadeMsg:
		return m.pickerMade(msg)
	case runDoneMsg:
		return m.runEnded(msg.run)
	case runDetailMsg:
		m.detailLoaded(msg)
	case dupDoneMsg:
		return m.dupsDone(msg)
	case untaggedDoneMsg:
		return m.untaggedDone(msg)
	case editorTagsMsg:
		return m.editorLoaded(msg)
	case editorDoneMsg:
		return m.editorDone(msg)
	case editorFolderMsg:
		m.editorFolder(msg)
	case editorFolderDoneMsg:
		return m.editorFolderDone(msg)
	case lookupFoundMsg:
		m.lookupFound(msg)
	case lookupPickedMsg:
		m.lookupPicked(msg)
	case lookupDoneMsg:
		return m.lookupDone(msg)
	case matchFoundMsg:
		m.matchFound(msg)
	case matchLoadedMsg:
		m.matchLoaded(msg)
	case matchDoneMsg:
		return m.matchDone(msg)
	case undoPlanMsg:
		m.undoPlanned(msg)
	case undoDoneMsg:
		return m.undoDone(msg)
	case librarySearchMsg:
		m.librarySearched(msg)
	case tea.PasteMsg:
		if m.edit == nil && m.look == nil && m.match == nil && m.undo == nil {
			m.libraryPaste(msg.Content)
			return m.settingsPaste(msg.Content)
		}
		return m.toFlowForm(msg)
	case libraryAlbumMsg:
		return m.libraryAlbum(msg)
	case runTickMsg:
		if m.run != nil {
			return runTick()
		}
	case tea.KeyPressMsg:
		// The offer to undo the last action lasts until the next key.
		undoable := m.undoable
		m.undoable = 0
		if undoable != 0 && msg.String() == "u" && !m.confirmQuit && !m.help && !m.library.typing &&
			m.edit == nil && m.look == nil && m.match == nil && m.undo == nil {
			m.toast, m.readErr = "", false
			return m.openUndo(undoable)
		}
		if m.edit != nil && !m.confirmQuit {
			return m.editorKey(msg)
		}
		if m.look != nil && !m.confirmQuit {
			return m.lookupKey(msg)
		}
		if m.match != nil && !m.confirmQuit {
			return m.matchKey(msg)
		}
		if m.undo != nil && !m.confirmQuit {
			return m.undoKey(msg.String())
		}
		if m.tab == TabSettings && m.settings.picker != nil && !m.confirmQuit && !m.help {
			return m.pickerKey(msg)
		}
		if m.tab == TabSettings && m.settings.input != nil && !m.confirmQuit && !m.help {
			return m.settingsType(msg)
		}
		// Whatever a key types goes into the query: a word an input method
		// commits comes as one key press of several characters.
		if m.library.typing && !m.confirmQuit && msg.Text != "" {
			m.toast, m.readErr = "", false
			m.library.query += msg.Text
			return nil
		}
		return m.key(msg.String())
	default:
		return m.toFlowForm(msg)
	}
	return nil
}

// toFlowForm hands a message that is not a key to the form of the flow that
// is open: what the form sends itself, and pasted text.
func (m *Model) toFlowForm(msg tea.Msg) tea.Cmd {
	if m.edit != nil && m.edit.form != nil && !m.edit.saving {
		return m.formUpdate(msg)
	}
	if l := m.look; l != nil && l.form != nil && l.step == stepQuery {
		_, cmd := l.form.Update(msg)
		return cmd
	}
	if x := m.match; x != nil && x.form != nil && x.step == matchQuery {
		_, cmd := x.form.Update(msg)
		return cmd
	}
	return nil
}

// refreshed applies a poll's result.
func (m *Model) refreshed(r refreshMsg) tea.Cmd {
	var next tea.Cmd
	if r.scheduled {
		next = m.tick()
	}
	if r.seq < m.appliedSeq {
		return next
	}
	m.appliedSeq = r.seq
	if r.err != nil {
		if m.reopening {
			return next
		}
		m.setToast(false, "Cannot read the library: %v", r.err)
		m.readErr = true
		return next
	}
	if m.readErr {
		m.toast, m.readErr = "", false
	}
	changed := r.version != m.version
	m.version = r.version
	m.locked, m.holder = r.locked, r.holder
	if r.snap == nil {
		return next
	}
	old := m.snap
	m.snap = *r.snap
	m.loaded, m.lastLoad = true, m.now()
	// The lists this load did not read stay as they were: as good as they
	// were when the library has not changed, and out of date when it has.
	got := m.snap.Parts
	if changed {
		m.have = Parts{}
	}
	m.have = Parts{m.have.Untagged || got.Untagged, m.have.Groups || got.Groups, m.have.Runs || got.Runs}
	m.seen = Parts{m.seen.Untagged || got.Untagged, m.seen.Groups || got.Groups, m.seen.Runs || got.Runs}
	cmds := []tea.Cmd{next, m.libraryReloaded(), m.filesReloaded()}
	if got.Groups {
		m.dups.follow(m.snap.Groups)
	} else {
		m.snap.Groups = old.Groups
	}
	if got.Runs {
		cmds = append(cmds, m.runsReloaded())
	} else {
		m.snap.Runs = old.Runs
	}
	if got.Untagged {
		m.untagged.reload(m.snap.Untagged)
		// The file a flow is open on, as the index has it now.
		for _, f := range m.snap.Untagged {
			if e := m.edit; e != nil && f.ID == e.file.ID {
				e.file = f.File
			}
			if l := m.look; l != nil && f.ID == l.file.ID {
				l.file = f.File
			}
			if x := m.match; x != nil {
				for i := range x.files {
					if x.files[i].ID == f.ID {
						x.files[i] = f
					}
				}
			}
		}
	} else {
		m.snap.Untagged = old.Untagged
	}
	// The tab changed while this was being read.
	if m.have.lacks(m.parts()) {
		cmds = append(cmds, m.poll(true, false))
	}
	return tea.Batch(cmds...)
}

// shown reads what the tab now showing needs and does not have as the
// library is now.
func (m *Model) shown() tea.Cmd {
	if m.loaded && m.have.lacks(m.parts()) {
		return tea.Batch(m.poll(true, false), m.settingsShown(), m.filesShown())
	}
	return tea.Batch(m.libraryShown(), m.settingsShown(), m.filesShown())
}

func (m *Model) key(k string) tea.Cmd {
	m.toast, m.readErr = "", false
	// The setting a refused run points at, which 6 then goes to.
	if m.refused != nil && m.refused.Setting != "" && k == "6" {
		m.settingsTo(m.refused.Setting)
	}
	m.refused = nil
	if m.confirmQuit {
		m.confirmQuit = false
		switch k {
		case "y", "Y":
			// Update quits once both have ended.
			m.quitting = true
			m.interrupt()
			m.stopping()
		case "ctrl+c":
			if m.run == nil && m.busy != nil {
				return tea.Quit
			}
			m.interrupt()
		}
		return nil
	}
	if m.help {
		page := max(m.bodyHeight()-1, 1)
		switch k {
		case "up", "k":
			m.helpScroll--
			return nil
		case "down", "j":
			m.helpScroll++
			return nil
		case "pgup":
			m.helpScroll -= page
			return nil
		case "pgdown", "space":
			m.helpScroll += page
			return nil
		case "home", "g":
			m.helpScroll = 0
			return nil
		case "end", "G":
			m.helpScroll = len(helpLines())
			return nil
		}
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
	if m.tab == TabLibrary && !m.help && m.loaded {
		if cmd, ok := m.libraryKey(k); ok {
			return cmd
		}
	}
	if m.tab == TabUntagged && !m.help && m.loaded {
		if cmd, ok := m.untaggedKey(k); ok {
			return cmd
		}
	}
	if m.tab == TabSettings && !m.help {
		if cmd, ok := m.settingsKey(k); ok {
			return cmd
		}
	}
	if m.tab == TabFiles && !m.help {
		if cmd, ok := m.filesKey(k); ok {
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
		// An action stopped part way is as much to ask about as a run.
		if m.run != nil || m.busy != nil {
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
		m.help, m.helpScroll = !m.help, 0
		return nil
	case "esc":
		m.help = false
		return nil
	case "R":
		m.setToast(true, "Reloaded")
		return m.poll(true, false)
	case "1", "2", "3", "4", "5", "6", "7":
		m.switchTab(Tab(k[0] - '1'))
	case "tab":
		m.switchTab((m.tab + 1) % tabCount)
	case "shift+tab":
		m.switchTab((m.tab + tabCount - 1) % tabCount)
	}
	return m.shown()
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

// setToast shows a message under the tab. Whatever of a file's tags or name
// it quotes is shown clean.
func (m *Model) setToast(ok bool, format string, args ...any) {
	m.toast = clean(fmt.Sprintf(format, args...))
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
