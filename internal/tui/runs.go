package tui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"tagshelf/internal/store"
)

// RunDetail is what one run did: the files it moved and those it failed on,
// in the order it did them.
type RunDetail struct {
	Moves  []store.Move
	Errors []store.RunError
}

// runsTab is the Runs tab's state: the list of runs, newest first, and the
// run open in detail, if any.
type runsTab struct {
	cursor int   // index into Snapshot.Runs
	selID  int64 // the run under the cursor, followed across reloads
	picked bool  // a person has moved the cursor
	detail *runDetailView
}

// runDetailView is a run open in detail.
type runDetailView struct {
	run    store.Run
	detail RunDetail
	loaded bool
	offset int // the first of the scrolling lines shown
}

// runDetailMsg is a run's detail, read off the UI goroutine.
type runDetailMsg struct {
	id     int64
	detail RunDetail
	err    error
}

// detailHead is how many lines of a run's detail stay put above the lines
// that scroll.
const detailHead = 4

// selected is the ID of the run under the cursor, 0 when there are none.
func (r *runsTab) selected(runs []store.Run) int64 {
	if r.cursor < 0 || r.cursor >= len(runs) {
		return 0
	}
	return runs[r.cursor].ID
}

// follow puts the cursor back on the run it was on after the list reloaded,
// or keeps it within the list when that run is gone.
func (r *runsTab) follow(runs []store.Run) {
	// Until a person has picked a run, the cursor is on the newest.
	if !r.picked {
		r.cursor = 0
		r.selID = r.selected(runs)
		return
	}
	for i, run := range runs {
		if run.ID == r.selID {
			r.cursor = i
			return
		}
	}
	r.cursor = max(min(r.cursor, len(runs)-1), 0)
	r.selID = r.selected(runs)
}

// loadDetail reads a run's detail off the UI goroutine.
func (m *Model) loadDetail(id int64) tea.Cmd {
	src := m.src
	return func() tea.Msg {
		d, err := src.RunDetail(id)
		return runDetailMsg{id: id, detail: d, err: err}
	}
}

// runsReloaded follows the cursor and keeps an open run up to date after the
// library reloaded, reading the detail of a run still going again.
func (m *Model) runsReloaded() tea.Cmd {
	m.runs.follow(m.snap.Runs)
	d := m.runs.detail
	if d == nil {
		return nil
	}
	for _, run := range m.snap.Runs {
		if run.ID == d.run.ID {
			wasRunning := d.run.State == store.StateRunning
			d.run = run
			if wasRunning {
				return m.loadDetail(run.ID)
			}
		}
	}
	return nil
}

func (m *Model) detailLoaded(msg runDetailMsg) {
	d := m.runs.detail
	if d == nil || d.run.ID != msg.id {
		return
	}
	if msg.err != nil {
		// Back to the list, which the toast explains, rather than a detail
		// that would say it is reading for ever.
		m.runs.detail = nil
		m.setToast(false, "Cannot read run %d: %v", msg.id, msg.err)
		return
	}
	d.detail, d.loaded = msg.detail, true
}

// runsKey handles a key on the Runs tab, reporting whether it was one.
func (m *Model) runsKey(k string) (tea.Cmd, bool) {
	runs := m.snap.Runs
	if d := m.runs.detail; d != nil {
		page := max(m.bodyHeight()-detailHead, 1)
		switch k {
		case "u":
			return m.openUndo(d.run.ID), true
		case "esc", "backspace":
			m.runs.detail = nil
		case "up", "k":
			d.offset--
		case "down", "j":
			d.offset++
		case "pgup":
			d.offset -= page
		case "pgdown", "space":
			d.offset += page
		case "g", "home":
			d.offset = 0
		case "G", "end":
			d.offset = m.detailCount(d)
		default:
			return nil, false
		}
		d.offset = max(min(d.offset, m.detailCount(d)-page), 0)
		return nil, true
	}

	page := max(m.bodyHeight()-2, 1)
	switch k {
	case "up", "k":
		m.runs.cursor--
	case "down", "j":
		m.runs.cursor++
	case "pgup":
		m.runs.cursor -= page
	case "pgdown", "space":
		m.runs.cursor += page
	case "g", "home":
		m.runs.cursor = 0
	case "G", "end":
		m.runs.cursor = len(runs) - 1
	case "u":
		if id := m.runs.selected(runs); id != 0 {
			return m.openUndo(id), true
		}
		return nil, true
	case "enter":
		id := m.runs.selected(runs)
		if id == 0 {
			return nil, true
		}
		m.runs.detail = &runDetailView{run: runs[m.runs.cursor]}
		return m.loadDetail(id), true
	default:
		return nil, false
	}
	m.runs.cursor = max(min(m.runs.cursor, len(runs)-1), 0)
	m.runs.selID = m.runs.selected(runs)
	m.runs.picked = true
	return nil, true
}

// runsBody draws the Runs tab in h lines.
func (m *Model) runsBody(h int) string {
	if d := m.runs.detail; d != nil {
		return m.runDetailBody(d, h)
	}
	runs := m.snap.Runs
	var b strings.Builder
	b.WriteString(boldStyle.Render(fmt.Sprintf("Runs (%s)", formatCount(int64(len(runs))))) + "\n")
	if len(runs) == 0 {
		b.WriteString("  " + dimStyle.Render("No runs yet: organize, reorganize or index from Home, or run tagshelf.") + "\n")
		return b.String()
	}
	routeW := max(m.width-runsFixedWidth, 10)
	b.WriteString(dimStyle.Render(fmt.Sprintf("  %-16s  %-10s  %-3s  %-*s  %*s  %6s  %s", "Started", "Kind", "Dry", routeW, "Route", movedWidth, "Moved", "Errors", "State")) + "\n")
	rows := max(h-2, 1)
	first := window(m.runs.cursor, rows)
	for i := first; i < len(runs) && i < first+rows; i++ {
		r := runs[i]
		dry, count := "", formatCount(moved(r.Counters))
		if r.DryRun {
			// It moved nothing.
			dry, count = "dry", "Would move "+count
		}
		line := fmt.Sprintf("%-16s  %-10s  %-3s  %s  %*s  %6s  ", m.formatTime(r.Started), r.Kind, dry,
			cell(clean(route(r)), routeW), movedWidth, count, formatCount(r.Counters.Errors))
		if i == m.runs.cursor {
			b.WriteString("› " + activeTab.Render(line+string(r.State)) + "\n")
			continue
		}
		line = "  " + line
		b.WriteString(line + stateStyle(r.State).Render(string(r.State)) + "\n")
	}
	return b.String()
}

// movedWidth is the width of the runs list's Moved column, which for a dry
// run says the files would have moved.
const movedWidth = 17

// runsFixedWidth is the width of the runs list without its route column.
const runsFixedWidth = 2 + 16 + 2 + 10 + 2 + 3 + 2 + 2 + movedWidth + 2 + 6 + 2 + 11

// route is where a run moved files from and to.
func route(r store.Run) string {
	switch {
	case r.Kind == store.KindReorganize:
		return r.Destination + " in place"
	case r.Source != "":
		return r.Source + " -> " + r.Destination
	}
	return r.Destination
}

// moved counts the files a run moved.
func moved(c store.Counters) int64 {
	return c.Organized + c.Untagged + c.OtherMoved + c.Duplicates + c.Retried
}

func stateStyle(s store.RunState) lipgloss.Style {
	switch s {
	case store.StateErrors, store.StateCrashed, store.StateInterrupted:
		return badStyle
	case store.StateRunning:
		return accentStyle
	}
	return plainStyle
}

// runDetailBody draws a run's detail in h lines: what it was, then its errors
// and moves, scrolled to d.offset.
func (m *Model) runDetailBody(d *runDetailView, h int) string {
	r := d.run
	var b strings.Builder
	title := fmt.Sprintf("Run %d  %s", r.ID, r.Kind)
	if r.DryRun {
		title += " (dry run)"
	}
	when := m.formatTime(r.Started)
	if !r.Ended.IsZero() {
		when += " to " + m.formatTime(r.Ended)
	}
	state := string(r.State)
	if r.UndoneBy != 0 {
		state += fmt.Sprintf(" by run %d", r.UndoneBy)
	}
	b.WriteString(boldStyle.Render(title) + "  " + when + "  " + stateStyle(r.State).Render(state) + "\n")
	b.WriteString("  " + clean(route(r)) + "\n")
	c := r.Counters
	b.WriteString("  " + countersText(r.Kind == store.KindIndex, c.Organized, c.Untagged, c.OtherMoved, c.Duplicates, c.InPlace+c.LeftInPlace, c.Errors, c.Indexed) + "\n\n")
	if !d.loaded {
		b.WriteString(dimStyle.Render("Reading the run…"))
		return b.String()
	}
	n := m.detailCount(d)
	rows := max(h-detailHead, 1)
	offset := max(min(d.offset, n-rows), 0)
	for i := offset; i < min(offset+rows, n); i++ {
		if i > offset {
			b.WriteString("\n")
		}
		b.WriteString(m.detailLine(d, i))
	}
	return b.String()
}

// detailWarnings are the warnings of the run open in detail, which only the
// TUI that did the run has: the first of them, and how many there were.
func (m *Model) detailWarnings(d *runDetailView) ([]string, int64) {
	if l := m.last; l != nil && l.finish.RunID != 0 && l.finish.RunID == d.run.ID {
		return l.warnings, l.warned
	}
	return nil, 0
}

// section is how many lines a list of a run's detail takes: its heading, and
// its items or the line saying there are none.
func section(items int) int {
	return 1 + max(items, 1)
}

// detailCount is how many lines of a run's detail scroll: its errors, its
// warnings if this TUI saw any, then its moves, with a line between.
func (m *Model) detailCount(d *runDetailView) int {
	if !d.loaded {
		return 0
	}
	n := section(len(d.detail.Errors)) + 1 + section(len(d.detail.Moves))
	if warnings, warned := m.detailWarnings(d); warned > 0 {
		n += section(len(warnings)) + 1
	}
	return n
}

// detailLine is line i of the lines of a run's detail that scroll. Only the
// lines shown are made: a run may have moved a whole library.
func (m *Model) detailLine(d *runDetailView, i int) string {
	// list is line i of a section of n items, and whether i is in it.
	list := func(title string, n int, item func(int) string) (string, bool) {
		defer func() { i -= section(n) + 1 }()
		switch {
		case i == 0:
			return boldStyle.Render(fmt.Sprintf("%s (%s)", title, formatCount(int64(n)))), true
		case n == 0 && i == 1:
			return "  " + dimStyle.Render("none"), true
		case i <= n:
			return item(i - 1), true
		case i == section(n):
			return "", true
		}
		return "", false
	}
	errs := d.detail.Errors
	if line, ok := list("Errors", len(errs), func(j int) string {
		return badStyle.Render("  " + clean(errs[j].Path+": "+errs[j].Message))
	}); ok {
		return line
	}
	if warnings, warned := m.detailWarnings(d); warned > 0 {
		title := "Warnings"
		if int64(len(warnings)) < warned {
			title = fmt.Sprintf("Warnings, the first of %s", formatCount(warned))
		}
		if line, ok := list(title, len(warnings), func(j int) string {
			return accentStyle.Render("  " + clean(warnings[j]))
		}); ok {
			return line
		}
	}
	moves := d.detail.Moves
	line, _ := list("Moves", len(moves), func(j int) string {
		mv := moves[j]
		return dimStyle.Render(fmt.Sprintf("  %-11s ", clean(string(mv.Reason)))) + m.shortPath(mv.To) + dimStyle.Render("  from "+clean(mv.From))
	})
	return line
}
