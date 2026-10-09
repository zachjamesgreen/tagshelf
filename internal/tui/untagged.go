package tui

import (
	"cmp"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"tagshelf/internal/engine"
	"tagshelf/internal/marker"
	"tagshelf/internal/rules"
	"tagshelf/internal/store"
	"tagshelf/internal/tagwrite"
)

// grouping is how the Untagged tab groups its files.
type grouping int

const (
	byFolder grouping = iota // the folder a run took each file from
	byAlbum                  // the album tag
	ungrouped
	groupingCount
)

var groupingNames = [...]string{"original folder", "album tag", "nothing"}

// untaggedTab is the Untagged tab's state: the untagged folder's files come
// from the snapshot, and this holds how they are grouped and which are
// selected.
type untaggedTab struct {
	by grouping
	// files are the snapshot's files in the order shown, group after group,
	// and groups where each group starts.
	files  []store.UntaggedFile
	groups []untaggedGroup

	cursor int   // index into files
	curID  int64 // the file under the cursor, followed across reloads

	selected map[int64]bool
}

// untaggedGroup is a run of files shown under one heading.
type untaggedGroup struct {
	name     string
	first, n int // files[first : first+n]
}

// untaggedDoneMsg says an accept of tags has ended.
type untaggedDoneMsg struct {
	files  int
	moved  []string // where the files that qualified went
	finish engine.RunFinished
	err    error
}

// reload takes the snapshot's files, keeping the cursor on its file and the
// selection to files still there.
func (u *untaggedTab) reload(files []store.UntaggedFile) {
	u.files = slices.Clone(files)
	name := u.groupName
	slices.SortStableFunc(u.files, func(a, b store.UntaggedFile) int {
		return cmp.Or(
			cmp.Compare(strings.ToLower(name(a)), strings.ToLower(name(b))),
			cmp.Compare(name(a), name(b)),
			cmp.Compare(store.PathKey(a.Path), store.PathKey(b.Path)))
	})
	u.groups = u.groups[:0]
	here := make(map[int64]bool, len(u.files))
	cursor := -1
	for i, f := range u.files {
		here[f.ID] = true
		if f.ID == u.curID {
			cursor = i
		}
		if n := len(u.groups); n == 0 || u.groups[n-1].name != name(f) {
			u.groups = append(u.groups, untaggedGroup{name: name(f), first: i})
		}
		u.groups[len(u.groups)-1].n++
	}
	for id := range u.selected {
		if !here[id] {
			delete(u.selected, id)
		}
	}
	if cursor < 0 {
		cursor = max(min(u.cursor, len(u.files)-1), 0)
	}
	u.moveTo(cursor)
}

// groupName is the heading f is shown under.
func (u *untaggedTab) groupName(f store.UntaggedFile) string {
	switch u.by {
	case byFolder:
		if f.From != "" {
			return filepath.Dir(f.From)
		}
		return filepath.Dir(f.Path)
	case byAlbum:
		if f.Album == "" {
			return "(no album tag)"
		}
		return f.Album
	}
	return ""
}

func (u *untaggedTab) moveTo(i int) {
	u.cursor = max(min(i, len(u.files)-1), 0)
	u.curID = 0
	if f, ok := u.current(); ok {
		u.curID = f.ID
	}
}

// current is the file under the cursor.
func (u *untaggedTab) current() (store.UntaggedFile, bool) {
	if u.cursor < 0 || u.cursor >= len(u.files) {
		return store.UntaggedFile{}, false
	}
	return u.files[u.cursor], true
}

// groupAt is the group holding the file at index i.
func (u *untaggedTab) groupAt(i int) untaggedGroup {
	for _, g := range u.groups {
		if i < g.first+g.n {
			return g
		}
	}
	return untaggedGroup{}
}

// targets are the files an action applies to: those selected, in the order
// shown, or else the one under the cursor.
func (u *untaggedTab) targets() []int64 {
	var ids []int64
	for _, f := range u.files {
		if u.selected[f.ID] {
			ids = append(ids, f.ID)
		}
	}
	if f, ok := u.current(); ok && len(ids) == 0 {
		ids = []int64{f.ID}
	}
	return ids
}

// untaggedKey handles a key on the Untagged tab, reporting whether it was
// one.
func (m *Model) untaggedKey(k string) (tea.Cmd, bool) {
	u := &m.untagged
	page := max(m.bodyHeight()-3, 1)
	switch k {
	case "up", "k":
		u.moveTo(u.cursor - 1)
	case "down", "j":
		u.moveTo(u.cursor + 1)
	case "pgup":
		u.moveTo(u.cursor - page)
	case "pgdown":
		u.moveTo(u.cursor + page)
	case "space", " ":
		if f, ok := u.current(); ok {
			u.setSelected(!u.selected[f.ID], f.ID)
			u.moveTo(u.cursor + 1)
		}
	case "g":
		if _, ok := u.current(); !ok {
			break
		}
		// The whole group, or none of it when all of it already is.
		g := u.groupAt(u.cursor)
		ids := make([]int64, g.n)
		all := true
		for i, f := range u.files[g.first : g.first+g.n] {
			ids[i] = f.ID
			all = all && u.selected[f.ID]
		}
		u.setSelected(!all, ids...)
	case "v":
		u.by = (u.by + 1) % groupingCount
		u.reload(m.snap.Untagged)
	case "esc":
		if len(u.selected) == 0 {
			return nil, false
		}
		clear(u.selected)
	case "e":
		if f, ok := u.current(); ok {
			return m.openEditor(f.File), true
		}
	case "l":
		if f, ok := u.current(); ok {
			return m.openLookup(f.File, f.From), true
		}
	case "L":
		// The selection, or else the whole group the cursor is in.
		var files []store.UntaggedFile
		for _, f := range u.files {
			if u.selected[f.ID] {
				files = append(files, f)
			}
		}
		if _, ok := u.current(); ok && len(files) == 0 {
			if u.by == ungrouped {
				m.setToast(false, "Select the files of one release first (space), or group the list (v) to take a whole group")
				return nil, true
			}
			g := u.groupAt(u.cursor)
			files = append(files, u.files[g.first:g.first+g.n]...)
		}
		if len(files) > 0 {
			return m.openMatch(files), true
		}
	case "a":
		ids := u.targets()
		if len(ids) > 0 && m.canChange() {
			return m.acceptTags(ids), true
		}
	default:
		return nil, false
	}
	return nil, true
}

func (u *untaggedTab) setSelected(on bool, ids ...int64) {
	if u.selected == nil {
		u.selected = make(map[int64]bool)
	}
	for _, id := range ids {
		if on {
			u.selected[id] = true
		} else {
			delete(u.selected, id)
		}
	}
}

// acceptSave is the tag save of "accept tags": it writes only the manual
// marker, saying a person looked at the tags the file has and took them.
func acceptSave(now time.Time) engine.TagSave {
	mark := marker.New(marker.Hand, now)
	return func(st *store.Store, runID int64, f store.File) error {
		_, err := tagwrite.Writer{History: st, RunID: runID}.Save(f.Path, nil, mark)
		return err
	}
}

// acceptTags accepts the tags of the files with the given IDs off the UI
// goroutine.
func (m *Model) acceptTags(ids []int64) tea.Cmd {
	return m.saveMany(ids, acceptSave(time.Now()), func(moved []string, finish engine.RunFinished, err error) tea.Msg {
		return untaggedDoneMsg{files: len(ids), moved: moved, finish: finish, err: err}
	})
}

// saveMany saves tags to the indexed files with the given IDs as one run,
// off the UI goroutine; done turns where the files that qualified went and
// how the run ended into the message to send.
func (m *Model) saveMany(ids []int64, save engine.TagSave, done func(moved []string, finish engine.RunFinished, err error) tea.Msg) tea.Cmd {
	runner := m.runner
	ctx, ended := m.begin()
	return func() tea.Msg {
		defer close(ended)
		var mu sync.Mutex
		var moved []string
		sink := func(e engine.Event) {
			if p, ok := e.(engine.FilePlaced); ok && p.Outcome == engine.Organized {
				mu.Lock()
				moved = append(moved, p.Destination)
				mu.Unlock()
			}
		}
		finish, err := runner.SaveTags(ctx, ids, save, sink)
		return done(moved, finish, err)
	}
}

// untaggedDone says how an accept of tags went and reloads the library it
// changed.
func (m *Model) untaggedDone(msg untaggedDoneMsg) tea.Cmd {
	m.ended()
	if msg.err != nil {
		m.setToast(false, "Cannot change the library: %v", msg.err)
		return m.poll(true, false)
	}
	// The files that moved leave the selection with the reload; those that
	// stayed or failed are still there to see and act on.
	m.movedToast(msg.moved, msg.finish)
	return m.poll(true, false)
}

// movedToast says how a run that saved tags to files went: where those that
// moved went, how many stayed, and how many failed.
func (m *Model) movedToast(moved []string, finish engine.RunFinished) {
	var parts []string
	switch n := len(moved); {
	case n == 1:
		parts = append(parts, "Moved to "+m.shortPath(moved[0]))
	case n > 1:
		dirs := make(map[string]bool)
		for _, p := range moved {
			dirs[rules.PathKey(filepath.Dir(p))] = true
		}
		if len(dirs) == 1 {
			parts = append(parts, fmt.Sprintf("%s files moved to %s", formatCount(int64(n)), m.shortPath(filepath.Dir(moved[0]))))
		} else {
			parts = append(parts, fmt.Sprintf("%s files moved into %s folders of the library", formatCount(int64(n)), formatCount(int64(len(dirs)))))
		}
	}
	if n := finish.Counters.InPlace; n > 0 {
		parts = append(parts, plural(n, "file", "files")+" tagged where "+map[bool]string{true: "it is", false: "they are"}[n == 1])
	}
	if left := finish.Counters.LeftInPlace; left > 0 {
		where := "where it is"
		if m.tab == TabUntagged {
			where = "in _Untagged"
		}
		stay := "stays " + where + " until it has"
		if left > 1 {
			stay = "stay " + strings.Replace(where, "it is", "they are", 1) + " until they have"
		}
		parts = append(parts, fmt.Sprintf("%s %s an artist, album and title", plural(left, "file", "files"), stay))
	}
	errs := finish.Counters.Errors
	if errs > 0 || finish.State != engine.StateOK {
		parts = append(parts, fmt.Sprintf("%s: see %d Runs", plural(errs, "error", "errors"), int(TabRuns)+1))
	}
	if len(parts) == 0 {
		parts = append(parts, "Nothing changed")
	}
	m.setToast(errs == 0 && finish.State == engine.StateOK, "%s", strings.Join(parts, "; "))
}

// missingTags lists what f still needs before it can go into the library: an
// artist (album artist or track artist), an album and a title.
func missingTags(f store.File) []string {
	var needs []string
	if f.AlbumArtist == "" && f.Artist == "" {
		needs = append(needs, "artist")
	}
	if f.Album == "" {
		needs = append(needs, "album")
	}
	if f.Title == "" {
		needs = append(needs, "title")
	}
	return needs
}

// untaggedState is the badge saying why f is in the untagged folder.
func untaggedState(f store.File) string {
	needs := missingTags(f)
	method := ""
	if mark, ok := marker.Parse(f.Manual); ok {
		method = " (" + string(mark.Method) + ")"
	}
	chosen := method != "" || f.ReleaseID != "" || f.ReleaseTrackID != "" || f.RecordingID != ""
	switch {
	case len(needs) > 0 && (chosen || len(needs) < 3):
		return "needs: " + strings.Join(needs, ", ") + method
	case len(needs) > 0:
		return "no tags"
	case chosen:
		// It moves into the library with the next run.
		return "ready" + method
	}
	return "ripper tags"
}

// cell fits s to exactly w terminal cells, which wide characters fill two at
// a time.
func cell(s string, w int) string {
	s = ansi.Truncate(s, w, "…")
	return s + strings.Repeat(" ", max(w-ansi.StringWidth(s), 0))
}

// The width of a file's line without its flexible columns: the cursor and
// selection marks, the length, the state and the gaps.
const untaggedFixedWidth = 4 + 5 + 22 + 5*2

// untaggedBody draws the Untagged tab in h lines.
func (m *Model) untaggedBody(h int) string {
	u := &m.untagged
	var b strings.Builder
	title := fmt.Sprintf("Untagged (%s)", plural(int64(len(u.files)), "file", "files"))
	b.WriteString(boldStyle.Render(title) + dimStyle.Render("  grouped by "+groupingNames[u.by]))
	if n := len(u.selected); n > 0 {
		b.WriteString("  " + accentStyle.Render(formatCount(int64(n))+" selected"))
	}
	b.WriteString("\n")
	if len(u.files) == 0 {
		b.WriteString("  " + goodStyle.Render("No files in _Untagged") + "\n")
		return b.String()
	}

	flex := max(m.width-untaggedFixedWidth, 32)
	fileW, artistW, albumW := flex*28/100, flex*22/100, flex*24/100
	titleW := flex - fileW - artistW - albumW
	row := func(file, artist, album, title, length, state string) string {
		return fmt.Sprintf("%s  %s  %s  %s  %5s  %s",
			cell(file, fileW), cell(artist, artistW), cell(album, albumW), cell(title, titleW), length, state)
	}
	b.WriteString(dimStyle.Render("    "+row("File", "Artist", "Album", "Title", "Len", "State")) + "\n")

	// Every line there is to show, and the one the cursor is on.
	var lines []string
	cursorLine := 0
	for _, g := range u.groups {
		if u.by != ungrouped {
			lines = append(lines, boldStyle.Render(m.shortPath(g.name))+dimStyle.Render(fmt.Sprintf("  %s", plural(int64(g.n), "file", "files"))))
		}
		for i := g.first; i < g.first+g.n; i++ {
			f := u.files[i]
			mark := "  "
			if u.selected[f.ID] {
				mark = "● "
			}
			artist := f.Artist
			if artist == "" {
				artist = f.AlbumArtist
			}
			line := mark + row(filepath.Base(f.Path), artist, f.Album, f.Title, length(f.Length), untaggedState(f.File))
			if i == u.cursor {
				cursorLine = len(lines)
				line = "› " + activeTab.Render(line)
			} else {
				line = "  " + line
			}
			lines = append(lines, line)
		}
	}
	room := max(h-2, 1)
	first := 0
	if cursorLine >= room {
		first = cursorLine - room + 1
	}
	for _, l := range lines[first:min(first+room, len(lines))] {
		b.WriteString(l + "\n")
	}
	return b.String()
}
