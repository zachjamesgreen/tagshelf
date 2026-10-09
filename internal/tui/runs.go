package tui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

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
			d.offset = len(detailLines(m, d))
		default:
			return nil, false
		}
		d.offset = max(min(d.offset, len(detailLines(m, d))-page), 0)
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
	b.WriteString(dimStyle.Render(fmt.Sprintf("  %-16s  %-10s  %-3s  %-*s  %7s  %6s  %s", "Started", "Kind", "Dry", routeW, "Route", "Moved", "Errors", "State")) + "\n")
	rows := max(h-2, 1)
	first := 0
	if m.runs.cursor >= rows {
		first = m.runs.cursor - rows + 1
	}
	for i := first; i < len(runs) && i < first+rows; i++ {
		r := runs[i]
		dry := ""
		if r.DryRun {
			dry = "dry"
		}
		line := fmt.Sprintf("%-16s  %-10s  %-3s  %-*s  %7s  %6s  ", m.formatTime(r.Started), r.Kind, dry,
			routeW, ansi.Truncate(route(r), routeW, "…"), formatCount(moved(r.Counters)), formatCount(r.Counters.Errors))
		if i == m.runs.cursor {
			b.WriteString("› " + activeTab.Render(line+string(r.State)) + "\n")
			continue
		}
		line = "  " + line
		b.WriteString(line + stateStyle(r.State).Render(string(r.State)) + "\n")
	}
	return b.String()
}

// runsFixedWidth is the width of the runs list without its route column.
const runsFixedWidth = 2 + 16 + 2 + 10 + 2 + 3 + 2 + 2 + 7 + 2 + 6 + 2 + 11

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
	b.WriteString("  " + route(r) + "\n")
	c := r.Counters
	b.WriteString("  " + countersText(r.Kind == store.KindIndex, c.Organized, c.Untagged, c.OtherMoved, c.Duplicates, c.InPlace+c.LeftInPlace, c.Errors, c.Indexed) + "\n\n")
	if !d.loaded {
		b.WriteString(dimStyle.Render("Reading the run…"))
		return b.String()
	}
	lines := detailLines(m, d)
	rows := max(h-detailHead, 1)
	offset := max(min(d.offset, len(lines)-rows), 0)
	end := min(offset+rows, len(lines))
	b.WriteString(strings.Join(lines[offset:end], "\n"))
	return b.String()
}

// detailLines are the lines of a run's detail that scroll: its errors, then
// its moves.
func detailLines(m *Model, d *runDetailView) []string {
	if !d.loaded {
		return nil
	}
	var lines []string
	lines = append(lines, boldStyle.Render(fmt.Sprintf("Errors (%s)", formatCount(int64(len(d.detail.Errors))))))
	if len(d.detail.Errors) == 0 {
		lines = append(lines, "  "+dimStyle.Render("none"))
	}
	for _, e := range d.detail.Errors {
		lines = append(lines, badStyle.Render("  "+e.Path+": "+e.Message))
	}
	lines = append(lines, "", boldStyle.Render(fmt.Sprintf("Moves (%s)", formatCount(int64(len(d.detail.Moves))))))
	if len(d.detail.Moves) == 0 {
		lines = append(lines, "  "+dimStyle.Render("none"))
	}
	for _, mv := range d.detail.Moves {
		lines = append(lines, dimStyle.Render(fmt.Sprintf("  %-11s ", mv.Reason))+m.shortPath(mv.To)+dimStyle.Render("  from "+mv.From))
	}
	return lines
}
