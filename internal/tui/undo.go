package tui

import (
	"encoding/json"
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"

	"tagshelf/internal/engine"
	"tagshelf/internal/store"
	"tagshelf/internal/tagwrite"
)

// undoFlow is the full-screen undo preflight of one run: what undoing it
// would do, to confirm before anything is done.
type undoFlow struct {
	run  store.Run
	from Tab // the tab it was opened from, for the breadcrumb

	plan    *engine.UndoPlan // nil until it has been worked out
	working bool             // the undo is being carried out
	scroll  int              // the first of the skipped files shown
}

// undoPlanMsg is what undoing a run would do, worked out off the UI
// goroutine.
type undoPlanMsg struct {
	id   int64
	plan engine.UndoPlan
	err  error
}

// undoDoneMsg says an undo has ended.
type undoDoneMsg struct {
	id     int64
	finish engine.RunFinished
	err    error
}

// undoCheck reports whether the file at path still holds the tags a save
// recorded as its After.
func undoCheck(path, after string) error {
	var want tagwrite.Values
	if err := json.Unmarshal([]byte(after), &want); err != nil {
		return fmt.Errorf("the record of its tag save cannot be read: %w", err)
	}
	ok, err := tagwrite.Matches(path, want)
	if err != nil {
		return err
	}
	if !ok {
		return tagwrite.ErrChanged
	}
	return nil
}

// undoRestore writes a tag save's recorded Before back over its After.
func undoRestore(st *store.Store, runID int64, path, before, after string) error {
	var to, from tagwrite.Values
	if err := json.Unmarshal([]byte(before), &to); err != nil {
		return fmt.Errorf("the record of its tag save cannot be read: %w", err)
	}
	if err := json.Unmarshal([]byte(after), &from); err != nil {
		return fmt.Errorf("the record of its tag save cannot be read: %w", err)
	}
	_, err := tagwrite.Writer{History: st, RunID: runID}.Restore(path, to, from)
	return err
}

// offerUndo lets the next key, if it is u, open the undo of the run that
// just changed the library, and says so in the toast.
func (m *Model) offerUndo(runID int64) {
	// Not while keys go elsewhere: u would be taken for the offer.
	if runID == 0 || m.toast == "" || m.edit != nil || m.look != nil || m.match != nil || m.undo != nil || m.library.typing {
		return
	}
	m.undoable = runID
	m.toast += "  (u undo)"
}

// openUndo opens the undo preflight of the run with the given ID and works
// out off the UI goroutine what undoing it would do.
func (m *Model) openUndo(id int64) tea.Cmd {
	u := &undoFlow{run: store.Run{ID: id}, from: m.tab}
	for _, r := range m.snap.Runs {
		if r.ID == id {
			u.run = r
		}
	}
	m.undo = u
	src := m.src
	return func() tea.Msg {
		plan, err := src.UndoPlan(id)
		return undoPlanMsg{id: id, plan: plan, err: err}
	}
}

// undoPlanned shows what undoing the run would do.
func (m *Model) undoPlanned(msg undoPlanMsg) {
	u := m.undo
	if u == nil || u.run.ID != msg.id || u.working {
		return
	}
	if msg.err != nil {
		m.undo = nil
		m.setToast(false, "Cannot work out the undo: %v", msg.err)
		return
	}
	u.plan, u.run = &msg.plan, msg.plan.Run
}

// undoKey handles a key while the undo preflight is open.
func (m *Model) undoKey(k string) tea.Cmd {
	u := m.undo
	m.toast, m.readErr = "", false
	if k == "ctrl+c" {
		return m.key(k)
	}
	if u.working {
		return nil
	}
	page := max(m.bodyHeight()-9, 1)
	switch k {
	case "esc", "n":
		m.undo = nil
	case "up", "k":
		u.scroll--
	case "down", "j":
		u.scroll++
	case "pgup":
		u.scroll -= page
	case "pgdown", "space":
		u.scroll += page
	case "enter", "y":
		if u.plan == nil || u.plan.Nothing() || !m.canChange() {
			return nil
		}
		u.working = true
		runner, id := m.runner, u.run.ID
		ctx, done := m.begin()
		return func() tea.Msg {
			defer close(done)
			finish, err := runner.Undo(ctx, id, undoCheck, undoRestore, func(engine.Event) {})
			return undoDoneMsg{id: id, finish: finish, err: err}
		}
	}
	return nil
}

// undoDone says how an undo went and reloads the library it changed.
func (m *Model) undoDone(msg undoDoneMsg) tea.Cmd {
	m.ended()
	if u := m.undo; u != nil && u.run.ID == msg.id {
		u.working = false
		if msg.err == nil {
			m.undo = nil
		}
	}
	if msg.err != nil {
		m.setToast(false, "Cannot undo the run: %v", msg.err)
		return m.poll(true, false)
	}
	c := msg.finish.Counters
	var did []string
	if c.OtherMoved > 0 {
		did = append(did, plural(c.OtherMoved, "file", "files")+" moved back")
	}
	if c.InPlace > 0 {
		did = append(did, plural(c.InPlace, "file has", "files have")+" their earlier tags back")
	}
	if c.CueRewritten > 0 {
		did = append(did, plural(c.CueRewritten, "cue sheet", "cue sheets")+" restored")
	}
	if len(did) == 0 {
		did = []string{"nothing could be put back"}
	}
	text := fmt.Sprintf("Run %d undone: %s", msg.id, strings.Join(did, ", "))
	switch {
	case msg.finish.State == engine.StateInterrupted:
		m.setToast(false, "The undo was interrupted: %s so far; undo the run again for the rest", strings.TrimPrefix(text, fmt.Sprintf("Run %d undone: ", msg.id)))
	case c.Errors > 0:
		m.setToast(false, "%s; %s skipped or failed: see %d Runs", text, formatCount(c.Errors), int(TabRuns)+1)
	default:
		m.setToast(true, "%s", text)
		// An undo is a run like any other.
		m.offerUndo(msg.finish.RunID)
	}
	return m.poll(true, false)
}

// undoCrumb is the breadcrumb shown in place of the tabs.
func (m *Model) undoCrumb() string {
	return " " + dimStyle.Render(m.undo.from.String()+" › ") + boldStyle.Render("Undo") + dimStyle.Render(" › ") + fmt.Sprintf("run %d", m.undo.run.ID)
}

// undoHelp is the key help of the undo preflight.
func (m *Model) undoHelp() string {
	u := m.undo
	switch {
	case u.working:
		return "undoing…"
	case u.plan == nil || u.plan.Nothing():
		return "esc back"
	}
	return "enter undo it  ↑↓ scroll the skipped files  esc back"
}

// undoBody draws the undo preflight in h lines.
func (m *Model) undoBody(h int) string {
	u := m.undo
	r := u.run
	var b strings.Builder
	title := fmt.Sprintf("Undo run %d", r.ID)
	if r.Kind != "" {
		title += fmt.Sprintf(": %s of %s", r.Kind, m.formatTime(r.Started))
	}
	b.WriteString("\n  " + boldStyle.Render(title) + "\n")
	if r.Kind != "" {
		b.WriteString("  " + dimStyle.Render(route(r)) + "\n")
	}
	b.WriteString("\n")
	p := u.plan
	switch {
	case p == nil:
		b.WriteString("  " + dimStyle.Render("Working out what can be undone…") + "\n")
		return b.String()
	case p.Refusal != "":
		b.WriteString("  " + badStyle.Render("This run cannot be undone: "+p.Refusal) + "\n")
		return b.String()
	}

	var can []string
	if p.Moves > 0 {
		can = append(can, plural(int64(p.Moves), "file moves back", "files move back"))
	}
	if p.TagSaves > 0 {
		can = append(can, plural(int64(p.TagSaves), "file gets its earlier tags back", "files get their earlier tags back"))
	}
	if p.Cues > 0 {
		can = append(can, plural(int64(p.Cues), "cue sheet gets its original back", "cue sheets get their originals back"))
	}
	if len(can) == 0 {
		b.WriteString("  " + badStyle.Render("Nothing of this run can be undone any more") + "\n")
	} else {
		b.WriteString("  " + goodStyle.Render("Can be undone: ") + strings.Join(can, ", ") + "\n")
		b.WriteString("  " + dimStyle.Render("No file is overwritten. The undo is a run of its own, which can be undone in turn.") + "\n")
	}
	if n := len(p.Skipped); n > 0 {
		b.WriteString("\n  " + accentStyle.Render(fmt.Sprintf("Left as %s (%s):", map[bool]string{true: "it is", false: "they are"}[n == 1], formatCount(int64(n)))) + "\n")
		room := max(h-10, 1)
		u.scroll = max(min(u.scroll, n-room), 0)
		for _, s := range p.Skipped[u.scroll:min(u.scroll+room, n)] {
			b.WriteString("    " + m.shortPath(s.Path) + dimStyle.Render("  "+s.Reason) + "\n")
		}
	}
	if u.working {
		b.WriteString("\n  " + dimStyle.Render("Undoing…") + "\n")
	}
	return b.String()
}
