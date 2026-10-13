package tui

import (
	"context"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/zachjamesgreen/tagshelf/internal/engine"
	"github.com/zachjamesgreen/tagshelf/internal/store"
)

// dupsTab is the Duplicates tab's state: the groups waiting for review come
// from the snapshot, and this holds what the person has chosen about them.
type dupsTab struct {
	cursor int    // index into Snapshot.Groups
	selKey string // the group under the cursor, followed across reloads

	// keepers holds the copy chosen to keep for the groups where that is not
	// the suggested one, by group key.
	keepers map[string]int64
	// suggested holds the copy the suggested-keeper rule picks for each
	// group it has been worked out for, by group key, until the groups are
	// read again.
	suggested map[string]int64
	// confirmed holds the possible groups a person said are the same song,
	// which may then be resolved like any other.
	confirmed map[string]bool

	// confirmAll holds the exact groups a "resolve all" is asking about, nil
	// when it is not asking.
	confirmAll []store.Group
}

// dupDoneMsg says an action on duplicate groups has ended.
type dupDoneMsg struct {
	keptBoth bool
	groups   int
	finish   engine.RunFinished
	err      error
}

// groupKey names a group by its kind and members.
func groupKey(g store.Group) string {
	var b strings.Builder
	b.WriteString(string(g.Kind))
	for _, id := range g.IDs() {
		b.WriteByte(' ')
		b.WriteString(strconv.FormatInt(id, 10))
	}
	return b.String()
}

// selected is the group under the cursor.
func (d *dupsTab) selected(groups []store.Group) (store.Group, bool) {
	if d.cursor < 0 || d.cursor >= len(groups) {
		return store.Group{}, false
	}
	return groups[d.cursor], true
}

// follow puts the cursor back on its group after the groups reloaded, or
// keeps it within the list when that group is gone.
func (d *dupsTab) follow(groups []store.Group) {
	// A question about the groups as they were is not asked of new ones, and
	// their copies may no longer be what a keeper was suggested from.
	d.confirmAll = nil
	clear(d.suggested)
	for i, g := range groups {
		if groupKey(g) == d.selKey {
			d.cursor = i
			return
		}
	}
	d.moveTo(max(min(d.cursor, len(groups)-1), 0), groups)
}

func (d *dupsTab) moveTo(i int, groups []store.Group) {
	d.cursor = i
	d.selKey = ""
	if g, ok := d.selected(groups); ok {
		d.selKey = groupKey(g)
	}
}

// keeper is the ID of the copy of g to keep: the one chosen, or else the one
// the suggested-keeper rule picks.
func (d *dupsTab) keeper(g store.Group) int64 {
	key := groupKey(g)
	if id, ok := d.keepers[key]; ok {
		return id
	}
	id, ok := d.suggested[key]
	if !ok {
		if d.suggested == nil {
			d.suggested = make(map[string]int64)
		}
		id = g.Files[engine.SuggestedKeeper(g.Files)].ID
		d.suggested[key] = id
	}
	return id
}

// resolvable reports whether g may be resolved: a possible group only once a
// person has confirmed it.
func (d *dupsTab) resolvable(g store.Group) bool {
	return g.Kind != store.GroupPossible || d.confirmed[groupKey(g)]
}

func (d *dupsTab) resolution(g store.Group) engine.Resolution {
	return engine.Resolution{Kind: g.Kind, Members: g.IDs(), Keeper: d.keeper(g)}
}

// stillBusy is why nothing else that changes the library can start while an
// action is being carried out.
const stillBusy = "Still carrying out the last action"

// canChange reports whether the library may be changed from a tab now,
// saying why not in a toast.
func (m *Model) canChange() bool {
	switch {
	case m.locked:
		m.setToast(false, "Read-only: another tagshelf run holds the library lock (pid %d, started %s)",
			m.holder.PID, m.formatTime(m.holder.Started))
	case m.run != nil:
		m.setToast(false, "A run is going; wait for it to end")
	case m.busy != nil:
		m.setToast(false, "%s", stillBusy)
	case m.header.DryRun:
		m.setToast(false, "Dry run is on: turn it off with d on Home before changing the library")
	case m.runner == nil && m.tab == TabFiles:
		m.setToast(false, "Saving tags needs a library, where what is saved is recorded: set one up on %d Settings", int(TabSettings)+1)
	case m.runner == nil:
		m.setToast(false, "The library cannot be changed from here")
	default:
		return true
	}
	return false
}

// dupsKey handles a key on the Duplicates tab, reporting whether it was one.
func (m *Model) dupsKey(k string) (tea.Cmd, bool) {
	d := &m.dups
	groups := m.snap.Groups
	if d.confirmAll != nil {
		all := d.confirmAll
		d.confirmAll = nil
		if (k != "y" && k != "Y") || !m.canChange() {
			return nil, true
		}
		res := make([]engine.Resolution, len(all))
		for i, g := range all {
			res[i] = d.resolution(g)
		}
		return m.resolve(res), true
	}

	g, ok := d.selected(groups)
	switch k {
	case "up":
		d.moveTo(max(d.cursor-1, 0), groups)
	case "down", "j":
		d.moveTo(min(d.cursor+1, max(len(groups)-1, 0)), groups)
	case "n":
		if len(groups) > 0 {
			d.moveTo((d.cursor+1)%len(groups), groups)
		}
	case "k":
		if !ok || !d.resolvable(g) {
			break
		}
		// The next copy along, round to the first.
		now := d.keeper(g)
		for i, f := range g.Files {
			if f.ID == now {
				if d.keepers == nil {
					d.keepers = make(map[string]int64)
				}
				d.keepers[groupKey(g)] = g.Files[(i+1)%len(g.Files)].ID
				break
			}
		}
	case "c":
		if !ok || g.Kind != store.GroupPossible {
			break
		}
		if d.confirmed == nil {
			d.confirmed = make(map[string]bool)
		}
		key := groupKey(g)
		d.confirmed[key] = !d.confirmed[key]
	case "enter":
		if !ok {
			break
		}
		if !d.resolvable(g) {
			m.setToast(false, "These only look alike: press c to confirm they are the same song, or b to keep both")
			break
		}
		if m.canChange() {
			return m.resolve([]engine.Resolution{d.resolution(g)}), true
		}
	case "b":
		if ok && m.canChange() {
			return m.keepBoth(g), true
		}
	case "A":
		if !ok || !m.canChange() {
			break
		}
		var exact []store.Group
		for _, g := range groups {
			if g.Kind == store.GroupExact {
				exact = append(exact, g)
			}
		}
		if len(exact) == 0 {
			m.setToast(false, "No exact groups are waiting")
			break
		}
		d.confirmAll = exact
	default:
		return nil, false
	}
	return nil, true
}

// resolve carries out resolutions off the UI goroutine.
func (m *Model) resolve(res []engine.Resolution) tea.Cmd {
	runner := m.runner
	ctx, done := m.begin()
	return func() tea.Msg {
		defer close(done)
		finish, err := runner.Resolve(ctx, res)
		return dupDoneMsg{groups: len(res), finish: finish, err: err}
	}
}

// keepBoth records off the UI goroutine that every copy of g stays.
func (m *Model) keepBoth(g store.Group) tea.Cmd {
	runner := m.runner
	_, done := m.begin()
	return func() tea.Msg {
		defer close(done)
		return dupDoneMsg{keptBoth: true, groups: 1, err: runner.KeepBoth(g.Kind, g.IDs())}
	}
}

// dupsDone says how an action on duplicate groups went and reloads the
// library it changed.
func (m *Model) dupsDone(msg dupDoneMsg) tea.Cmd {
	m.ended()
	switch {
	case msg.err != nil:
		m.setToast(false, "Cannot change the library: %v", msg.err)
	case msg.keptBoth:
		m.setToast(true, "Kept both: the group stays closed until a new copy appears")
	case msg.finish.State != engine.StateOK:
		m.setToast(false, "%s resolving %s: see %d Runs",
			plural(msg.finish.Counters.Errors, "error", "errors"), plural(int64(msg.groups), "group", "groups"), int(TabRuns)+1)
	default:
		n := msg.finish.Counters.Duplicates + msg.finish.Counters.OtherMoved
		moved := "files moved"
		if n == 1 {
			moved = "file moved"
		}
		m.setToast(true, "Resolved %s: %s %s to _Duplicates", plural(int64(msg.groups), "group", "groups"), formatCount(n), moved)
		m.offerUndo(msg.finish.RunID)
	}
	return m.poll(true, false)
}

// begin notes that an action on the library is being carried out, under the
// context it returns; closing the channel it returns says the action has
// ended.
func (m *Model) begin() (context.Context, chan struct{}) {
	ctx, cancel := context.WithCancel(context.Background())
	m.busy, m.cancelAction = make(chan struct{}), cancel
	return ctx, m.busy
}

// ended notes that the action being carried out is over.
func (m *Model) ended() {
	if m.cancelAction != nil {
		m.cancelAction()
	}
	m.busy, m.cancelAction = nil, nil
}

// stopping asks the action being carried out, if any, to stop: it goes no
// further than the file it is on, and then says how it went.
func (m *Model) stopping() {
	if m.busy != nil && m.cancelAction != nil {
		m.cancelAction()
		if m.run == nil {
			m.setToast(false, "Stopping after the file it is on")
		}
	}
}

// stopAction ends an action being carried out, for when the TUI closes: it
// goes no further than the file it is on, and this waits for that.
func (m *Model) stopAction() {
	if busy := m.busy; busy != nil {
		m.cancelAction()
		<-busy
	}
}

// dupsBody draws the Duplicates tab in h lines: the groups waiting, and the
// copies of the one selected below them.
func (m *Model) dupsBody(h int) string {
	d := &m.dups
	groups := m.snap.Groups
	var b strings.Builder
	b.WriteString(boldStyle.Render(fmt.Sprintf("Duplicates (%s to review)", plural(int64(len(groups)), "group", "groups"))) + "\n")
	if len(groups) == 0 {
		b.WriteString("  " + goodStyle.Render("No duplicate groups to review") + "\n")
		return b.String()
	}
	if d.confirmAll != nil {
		var files int64
		for _, g := range d.confirmAll {
			files += int64(len(g.Files) - 1)
		}
		b.WriteString("\n" + accentStyle.Render(fmt.Sprintf("Resolve %s? %s to _Duplicates; artwork and other files follow out of folders left without audio.",
			plural(int64(len(d.confirmAll)), "exact group", "exact groups"), movesText(files))) + "\n")
		b.WriteString("  Each group keeps its suggested copy, or the one you chose with k.\n")
		b.WriteString("  " + boldStyle.Render("y") + " resolves them   " + boldStyle.Render("n") + " goes back\n")
		return b.String()
	}

	nameW := max(m.width-dupsFixedWidth, 16) / 2
	keepW := max(m.width-dupsFixedWidth-nameW, 10)
	b.WriteString(dimStyle.Render(fmt.Sprintf("  %-*s  %-13s  %6s  %s", nameW, "Song", "Kind", "Copies", "Keeps")) + "\n")
	rows := max(min(len(groups), (h-3)/3), 1)
	first := window(d.cursor, rows)
	for i := first; i < len(groups) && i < first+rows; i++ {
		g := groups[i]
		keeps := dimStyle.Render("to be confirmed")
		plain := "to be confirmed"
		if d.resolvable(g) {
			// The end of the path is what tells two copies in one album apart.
			plain = tailPath(m.shortPath(fileByID(g, d.keeper(g)).Path), keepW)
			keeps = plain
		}
		line := fmt.Sprintf("%s  %-13s  %6d  ", cell(groupName(g), nameW), g.Kind, len(g.Files))
		if i == d.cursor {
			b.WriteString("› " + activeTab.Render(line+plain) + "\n")
			continue
		}
		b.WriteString("  " + line + keeps + "\n")
	}

	g, ok := d.selected(groups)
	if !ok {
		return b.String()
	}
	b.WriteString("\n" + boldStyle.Render(groupName(g)) + dimStyle.Render(fmt.Sprintf("  %s, %s", g.Kind, plural(int64(len(g.Files)), "copy", "copies"))) + "\n")
	keeper := int64(0)
	if d.resolvable(g) {
		keeper = d.keeper(g)
	} else {
		b.WriteString("  " + accentStyle.Render("These only look alike. c confirms they are the same song; b keeps both.") + "\n")
	}
	// Each copy takes two lines: where it is, then what it is.
	for _, f := range g.Files {
		star := " "
		if f.ID == keeper {
			star = goodStyle.Render("★")
		}
		fmt.Fprintf(&b, "  %s %-9s  %s\n", star, where(f.RootKind), m.shortPath(f.Path))
		fmt.Fprintf(&b, "               %s\n", dimStyle.Render(fmt.Sprintf("%-5s %-22s %5s  %s", clean(f.Format), rate(f), length(f.Length), tagLine(f))))
	}
	for i, f := range g.AlsoOn {
		label := "         "
		if i == 0 {
			label = "Also on  "
		}
		b.WriteString(dimStyle.Render("    "+label+"  "+m.shortPath(f.Path)) + "\n")
	}
	return b.String()
}

// The widths of the groups list and of a copy's line without their flexible
// columns.
const (
	dupsFixedWidth = 2 + 2 + 13 + 2 + 6 + 2 + 1
)

func movesText(n int64) string {
	if n == 1 {
		return "1 file moves"
	}
	return formatCount(n) + " files move"
}

// groupName names a group after its first copy with tags to go by.
func groupName(g store.Group) string {
	for _, f := range g.Files {
		if f.Title != "" {
			artist := f.Artist
			if artist == "" {
				artist = f.AlbumArtist
			}
			if artist == "" {
				return clean(f.Title)
			}
			return clean(artist + " - " + f.Title)
		}
	}
	return clean(filepath.Base(g.Files[0].Path))
}

func fileByID(g store.Group, id int64) store.File {
	for _, f := range g.Files {
		if f.ID == id {
			return f
		}
	}
	return g.Files[0]
}

// where names the folder of the library a copy is in.
func where(kind store.RootKind) string {
	switch kind {
	case store.RootLibrary:
		return "library"
	case store.RootUntagged:
		return "_Untagged"
	case store.RootOther:
		return "_Other"
	case store.RootDuplicates:
		return "_Duplicates"
	}
	return string(kind)
}

// rate sums up a copy's audio quality.
func rate(f store.File) string {
	var parts []string
	switch {
	case f.Lossless:
		parts = append(parts, "lossless")
	case f.Bitrate > 0:
		parts = append(parts, fmt.Sprintf("%d kbps", f.Bitrate))
	}
	if f.SampleRate > 0 {
		khz := strconv.FormatFloat(float64(f.SampleRate)/1000, 'f', -1, 64) + " kHz"
		if f.Lossless && f.BitDepth > 0 {
			khz += fmt.Sprintf("/%d", f.BitDepth)
		}
		parts = append(parts, khz)
	}
	return strings.Join(parts, " ")
}

// length writes a duration as minutes and seconds.
func length(d time.Duration) string {
	if d <= 0 {
		return ""
	}
	s := int(d.Round(time.Second).Seconds())
	return fmt.Sprintf("%d:%02d", s/60, s%60)
}

// tagLine is a copy's artist, album and title.
func tagLine(f store.File) string {
	var parts []string
	for _, s := range []string{f.Artist, f.Album, f.Title} {
		if s != "" {
			parts = append(parts, s)
		}
	}
	return clean(strings.Join(parts, " / "))
}
