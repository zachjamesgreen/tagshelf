package tui

import (
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"tagshelf/internal/store"
)

// The smallest terminal the TUI draws in.
const (
	minWidth  = 40
	minHeight = 12
)

var (
	boldStyle   = lipgloss.NewStyle().Bold(true)
	dimStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	accentStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("12")).Bold(true)
	activeTab   = lipgloss.NewStyle().Reverse(true).Bold(true)
	bannerStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("0")).Background(lipgloss.Color("11"))
	goodStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("10"))
	badStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("9"))
	plainStyle  = lipgloss.NewStyle()
)

// View draws the screen. Nothing is drawn until the terminal's size is known.
func (m *Model) View() tea.View {
	v := tea.NewView(m.render())
	v.AltScreen = true
	return v
}

func (m *Model) render() string {
	w, h := m.width, m.height
	if w <= 0 || h <= 0 {
		return ""
	}
	if w < minWidth || h < minHeight {
		return fitLines(fmt.Sprintf("tagshelf needs a terminal of at least %dx%d (this one is %dx%d)", minWidth, minHeight, w, h), w, h)
	}

	top, bottom := m.chrome()
	var body string
	switch {
	case m.help:
		body = helpScreen()
	case !m.loaded:
		body = dimStyle.Render("Reading the library…")
	default:
		body = m.tabBody(h - len(top) - len(bottom))
	}
	return fitLines(strings.Join(top, "\n"), w, len(top)) + "\n" +
		fitLines(body, w, h-len(top)-len(bottom)) + "\n" +
		fitLines(strings.Join(bottom, "\n"), w, len(bottom))
}

// chrome is the lines above the tab's body and below it.
func (m *Model) chrome() (top, bottom []string) {
	top = []string{m.headerLine(), m.tabsLine(), dimStyle.Render(strings.Repeat("─", m.width))}
	bottom = []string{m.toastLine(), dimStyle.Render(m.keyHelp())}
	// The banner may use what the body can spare, but no more.
	top = append(top, m.banner(m.height-len(top)-len(bottom)-4)...)
	return top, bottom
}

// bodyHeight is how many lines the tab's body has.
func (m *Model) bodyHeight() int {
	top, bottom := m.chrome()
	return max(m.height-len(top)-len(bottom), 0)
}

// fitLines cuts s to exactly h lines no wider than w.
func fitLines(s string, w, h int) string {
	if h <= 0 {
		return ""
	}
	lines := strings.Split(s, "\n")
	if len(lines) > h {
		lines = lines[:h]
	}
	for len(lines) < h {
		lines = append(lines, "")
	}
	for i, l := range lines {
		lines[i] = ansi.Truncate(l, w, "…")
	}
	return strings.Join(lines, "\n")
}

func (m *Model) headerLine() string {
	line := boldStyle.Render(" tagshelf ") + " " + m.header.Destination
	if id := m.header.LibraryID; id != "" {
		if len(id) > 8 {
			id = id[:8]
		}
		line += dimStyle.Render("  library " + id)
	}
	if m.header.DryRun {
		line += "  " + accentStyle.Render("[dry-run]")
	}
	return line
}

func (m *Model) tabsLine() string {
	var parts []string
	for t := range tabCount {
		label := fmt.Sprintf(" %d %s", int(t)+1, t)
		if m.width < 64 && t != m.tab {
			// Narrow terminals name only the tab showing.
			label = fmt.Sprintf(" %d", int(t)+1)
		}
		if n, ok := m.tabCount(t); ok {
			label += " " + formatCount(n)
		}
		label += " "
		if t == m.tab && !m.help {
			parts = append(parts, activeTab.Render(label))
		} else {
			parts = append(parts, label)
		}
	}
	return strings.Join(parts, " ")
}

// tabCount is the live count a tab shows, if it has one.
func (m *Model) tabCount(t Tab) (int64, bool) {
	if !m.loaded {
		return 0, false
	}
	switch t {
	case TabUntagged:
		return m.snap.Overview.Untagged, true
	case TabDuplicates:
		return m.snap.NeedReview, true
	}
	return 0, false
}

// banner says another process holds the library lock, with that run's
// progress and as many of its latest moves as fit in room lines, and a blank
// line to end it.
func (m *Model) banner(room int) []string {
	if !m.locked {
		return nil
	}
	what := "Library locked"
	a := m.activeRun()
	if a != nil {
		what = capitalize(string(a.Run.Kind)) + " run in progress"
		if a.Run.DryRun {
			what = "Dry " + string(a.Run.Kind) + " run in progress"
		}
	}
	text := fmt.Sprintf(" %s (pid %d, started %s): read-only until it ends ", what, m.holder.PID, m.formatTime(m.holder.Started))
	lines := []string{bannerStyle.Render(text)}
	if a != nil {
		lines = append(lines, " "+progressLine(a.Progress, m.width-2))
		moves := a.Moves
		// One line is kept for the gap below the banner.
		if n := max(room-len(lines)-1, 0); len(moves) > n {
			moves = moves[len(moves)-n:]
		}
		for _, mv := range moves {
			lines = append(lines, dimStyle.Render(fmt.Sprintf("   %-10s ", mv.Reason))+m.shortPath(mv.To))
		}
	}
	return append(lines, "")
}

// progressLine draws a run's recorded progress as a bar, when its phase has a
// known size.
func progressLine(p store.RunProgress, w int) string {
	if p.Phase == "" {
		return dimStyle.Render("starting…")
	}
	phase := fmt.Sprintf("%-10s ", p.Phase)
	if p.Total <= 0 {
		return phase + formatCount(p.Done) + " files"
	}
	counts := fmt.Sprintf(" %s / %s files", formatCount(p.Done), formatCount(p.Total))
	barWidth := min(w-len(phase)-len(counts), 50)
	if barWidth < 4 {
		return phase + counts
	}
	done := int(min(p.Done, p.Total) * int64(barWidth) / p.Total)
	return phase + accentStyle.Render(strings.Repeat("█", done)) + dimStyle.Render(strings.Repeat("░", barWidth-done)) + counts
}

// shortPath shows a path relative to the library when it is inside it.
func (m *Model) shortPath(path string) string {
	if dest := m.header.Destination; dest != "" {
		if rel, err := filepath.Rel(dest, path); err == nil && rel != "." && !strings.HasPrefix(rel, "..") {
			return rel
		}
	}
	return path
}

// tabBody draws the tab showing in h lines.
func (m *Model) tabBody(h int) string {
	switch m.tab {
	case TabHome:
		return m.homeBody()
	case TabUntagged:
		return placeholder("Untagged", fmt.Sprintf("%s files in _Untagged.", formatCount(m.snap.Overview.Untagged)))
	case TabDuplicates:
		return m.dupsBody(h)
	case TabLibrary:
		return placeholder("Library", fmt.Sprintf("%s files in the library.", formatCount(m.snap.Overview.Library)))
	case TabRuns:
		return m.runsBody(h)
	}
	return ""
}

func placeholder(name, summary string) string {
	s := boldStyle.Render(name) + "\n"
	if summary != "" {
		s += "  " + summary + "\n"
	}
	return s + dimStyle.Render("  This tab is not built yet.")
}

func (m *Model) homeBody() string {
	o := m.snap.Overview
	var b strings.Builder
	switch {
	case m.run != nil:
		b.WriteString(m.runView(m.run) + "\n")
	case m.last != nil:
		b.WriteString(m.lastRunView(m.last) + "\n")
	}
	b.WriteString(boldStyle.Render("Library") + "\n")
	counts := []struct {
		name string
		n    int64
	}{{"Library", o.Library}, {"_Untagged", o.Untagged}, {"_Other", o.Other}, {"_Duplicates", o.Duplicates}}
	for _, c := range counts {
		fmt.Fprintf(&b, "  %-12s %9s files\n", c.name, formatCount(c.n))
	}
	b.WriteString("\n" + boldStyle.Render("Needs attention") + "\n")
	items := m.needsAttention()
	if len(items) == 0 {
		b.WriteString("  " + goodStyle.Render("Nothing needs attention") + "\n")
	}
	for _, it := range items {
		fmt.Fprintf(&b, "  %-58s %s\n", it.text, dimStyle.Render(fmt.Sprintf("%d %s", int(it.tab)+1, it.tab)))
	}
	return b.String()
}

// attention is one line of the needs-attention list, pointing at the tab
// where it is dealt with.
type attention struct {
	text string
	tab  Tab
}

func (m *Model) needsAttention() []attention {
	o := m.snap.Overview
	var items []attention
	if o.Untagged > 0 {
		items = append(items, attention{plural(o.Untagged, "untagged file", "untagged files"), TabUntagged})
	}
	if n := m.snap.NeedReview; n > 0 {
		items = append(items, attention{plural(n, "duplicate group to review", "duplicate groups to review"), TabDuplicates})
	}
	if r := o.LastRun; r != nil {
		when := fmt.Sprintf("%s run of %s", r.Kind, m.formatTime(r.Started))
		switch r.State {
		case store.StateErrors:
			items = append(items, attention{fmt.Sprintf("%s in the last %s", plural(r.Counters.Errors, "error", "errors"), when), TabRuns})
		case store.StateInterrupted:
			items = append(items, attention{"The last " + when + " was interrupted", TabRuns})
		case store.StateCrashed:
			items = append(items, attention{"The last " + when + " crashed", TabRuns})
		}
	}
	return items
}

func (m *Model) toastLine() string {
	if m.confirmQuit {
		return accentStyle.Render("Interrupt the run and quit? y interrupts and quits once the files already moving are done, n keeps it going")
	}
	if m.toast == "" {
		return ""
	}
	if m.toastOK {
		return goodStyle.Render(m.toast)
	}
	return badStyle.Render(m.toast)
}

func (m *Model) keyHelp() string {
	help := "1-5 tabs  R reload  ? help  q quit"
	if m.help {
		return "esc back  " + help
	}
	switch {
	case m.run != nil:
		help = "ctrl+c interrupt the run  " + help
	case m.tab == TabDuplicates && m.dups.confirmAll != nil:
		help = "y resolve them  n back"
	case m.tab == TabDuplicates && m.ReadOnly():
		help = "read-only  ↑↓/n select   " + help
	case m.tab == TabDuplicates:
		help = "↑↓/n select  k keeper  enter resolve  b keep both  c confirm  A all exact   " + help
	case m.tab == TabRuns && m.runs.detail != nil:
		help = "esc back  ↑↓ scroll  g/G top/end   " + help
	case m.tab == TabRuns:
		help = "↑↓ select  enter open   " + help
	case m.tab != TabHome:
	case m.ReadOnly():
		if m.width >= 100 {
			help = "read-only while another run holds the library lock   " + help
		} else {
			help = "read-only  " + help
		}
	default:
		help = "o organize  r reorganize  i index  d dry run   " + help
	}
	return help
}

func helpScreen() string {
	keys := [][2]string{
		{"1-5", "switch tab"},
		{"tab", "next tab (shift+tab previous)"},
		{"o", "organize the configured source (Home)"},
		{"r", "reorganize the library in place (Home)"},
		{"i", "bring the index up to date, moving nothing (Home)"},
		{"d", "turn dry run on or off (Home)"},
		{"ctrl+c", "interrupt the run going on"},
		{"k", "choose which copy to keep (Duplicates)"},
		{"enter", "resolve the group (Duplicates), open the run (Runs)"},
		{"b", "keep both copies (Duplicates)"},
		{"c", "confirm a possible group is one song (Duplicates)"},
		{"A", "resolve all exact groups (Duplicates)"},
		{"R", "reload the library now"},
		{"?", "show or hide this help"},
		{"esc", "back"},
		{"q", "quit"},
	}
	var b strings.Builder
	b.WriteString(boldStyle.Render("Keys") + "\n")
	for _, k := range keys {
		fmt.Fprintf(&b, "  %-6s %s\n", k[0], k[1])
	}
	b.WriteString("\n" + dimStyle.Render("The view reloads by itself when another tagshelf changes the library."))
	return b.String()
}

// formatTime shows a date and time of day where the user is.
func (m *Model) formatTime(t time.Time) string {
	if t.IsZero() {
		return "unknown"
	}
	return t.In(m.loc).Format("2006-01-02 15:04")
}

// formatCount writes n with thousands separators.
func formatCount(n int64) string {
	s := strconv.FormatInt(n, 10)
	neg := strings.HasPrefix(s, "-")
	s = strings.TrimPrefix(s, "-")
	var b strings.Builder
	for i, r := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(r)
	}
	if neg {
		return "-" + b.String()
	}
	return b.String()
}

func plural(n int64, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return formatCount(n) + " " + many
}

func capitalize(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}
