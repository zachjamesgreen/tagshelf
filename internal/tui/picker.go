package tui

import (
	"cmp"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/zachjamesgreen/tagshelf/internal/config"
)

// folderPicker is the Settings tab's folder picker: it shows one folder and
// the folders in it, and gives the setting it was opened for the folder
// shown when that is taken.
type folderPicker struct {
	key, label string // the setting it picks for
	// dir is the folder shown. It is empty for the list of drives, which
	// Windows has above the top of each.
	dir   string
	names []string // the folders in it, by name
	// cursor is the folder under the cursor, and top the first one drawn.
	cursor, top int
	hidden      bool // folders whose names start with a dot are listed too
	problem     string
	// seq numbers the reads, so that the answer to an earlier one is dropped.
	seq     int
	reading bool
	// naming holds the name being typed for a new folder, nil when none is.
	naming *lineInput
}

// pickerReadMsg is the folders of a folder the picker asked for.
type pickerReadMsg struct {
	seq   int
	dir   string
	names []string
	err   error
	// on is the folder to put the cursor on, empty for the first.
	on string
}

// pickerMadeMsg says a new folder was made, or why not.
type pickerMadeMsg struct {
	seq int
	dir string
	err error
}

// listDrives lists the drives of a system that has them, each as the top
// folder of one. Tests put their own in its place.
var listDrives = drives

// systemFolders are the system's own folders at the top of a drive, which
// are never a place for music.
var systemFolders = map[string]bool{"$recycle.bin": true, "system volume information": true}

// listFolders lists the folders in dir by name, or the drives when dir is
// empty. A link to a folder is one. Those named with a leading dot are left
// out unless hidden is set.
func listFolders(dir string, hidden bool) ([]string, error) {
	if dir == "" {
		return listDrives(), nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var names []string
	for _, e := range entries {
		name := e.Name()
		if systemFolders[strings.ToLower(name)] || (!hidden && strings.HasPrefix(name, ".")) {
			continue
		}
		isDir := e.IsDir()
		if e.Type()&fs.ModeSymlink != 0 {
			info, err := os.Stat(filepath.Join(dir, name))
			isDir = err == nil && info.IsDir()
		}
		if isDir {
			names = append(names, name)
		}
	}
	slices.SortFunc(names, func(a, b string) int {
		return cmp.Or(cmp.Compare(strings.ToLower(a), strings.ToLower(b)), cmp.Compare(a, b))
	})
	return names, nil
}

// folderAbove is the folder dir is in, and whether there is one: above the
// top of a drive is the list of drives, and above / is nothing.
func folderAbove(dir string) (string, bool) {
	switch parent := filepath.Dir(dir); {
	case dir == "":
		return "", false
	case parent != dir:
		return parent, true
	case len(listDrives()) > 0:
		return "", true
	}
	return "", false
}

// pickerStart is where the picker opens for a setting that holds value: that
// folder if it is there, or else the nearest folder above it that is, or
// else the home folder.
func pickerStart(value string) string {
	if path, err := config.ExpandHome(strings.TrimSpace(value)); err == nil && path != "" && !config.ForeignPath(path) {
		if abs, err := filepath.Abs(path); err == nil {
			for dir := abs; ; {
				if info, err := os.Stat(dir); err == nil && info.IsDir() {
					return dir
				}
				parent := filepath.Dir(dir)
				if parent == dir {
					break
				}
				dir = parent
			}
		}
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		return home
	}
	if len(listDrives()) > 0 {
		return ""
	}
	return string(filepath.Separator)
}

// openPicker opens the folder picker for the setting under the cursor, at
// the folder from holds.
func (m *Model) openPicker(from string) tea.Cmd {
	s := &m.settings
	row := s.row()
	if row.kind != kindFolder {
		return nil
	}
	s.input, s.hint = nil, ""
	delete(s.problems, row.key)
	s.picker = &folderPicker{key: row.key, label: row.label}
	return m.pickerGo(pickerStart(from), "")
}

// pickerGo reads the folders of dir off the UI goroutine, which a drive that
// is slow to answer would otherwise hold up, to show it with the cursor on
// the folder named on.
func (m *Model) pickerGo(dir, on string) tea.Cmd {
	p := m.settings.picker
	p.seq++
	p.reading = true
	seq, hidden := p.seq, p.hidden
	return func() tea.Msg {
		names, err := listFolders(dir, hidden)
		return pickerReadMsg{seq: seq, dir: dir, names: names, err: err, on: on}
	}
}

// pickerRead shows the folder that was read. One that cannot be read is not
// gone into: the picker stays where it was and says why.
func (m *Model) pickerRead(msg pickerReadMsg) {
	p := m.settings.picker
	if p == nil || msg.seq != p.seq {
		return
	}
	p.reading = false
	if msg.err != nil {
		p.problem = "Cannot open " + msg.dir + ": " + msg.err.Error()
		return
	}
	p.dir, p.names, p.problem = msg.dir, msg.names, ""
	p.cursor, p.top = max(slices.Index(p.names, msg.on), 0), 0
}

// pickerKey handles a key while the folder picker is open.
func (m *Model) pickerKey(msg tea.KeyPressMsg) tea.Cmd {
	s := &m.settings
	p := s.picker
	k := msg.String()
	m.toast, m.readErr = "", false
	if k == "ctrl+c" {
		return m.key(k)
	}
	if p.naming != nil {
		return m.pickerNaming(msg)
	}
	if p.reading {
		if k == "esc" {
			s.picker = nil
		}
		return nil
	}
	page := max(m.pickerRows()-1, 1)
	switch k {
	case "esc":
		s.picker = nil
	case "up", "k":
		p.cursor = max(p.cursor-1, 0)
	case "down", "j":
		p.cursor = min(p.cursor+1, max(len(p.names)-1, 0))
	case "pgup":
		p.cursor = max(p.cursor-page, 0)
	case "pgdown":
		p.cursor = min(p.cursor+page, max(len(p.names)-1, 0))
	case "home", "g":
		p.cursor = 0
	case "end", "G":
		p.cursor = max(len(p.names)-1, 0)
	case "enter", "right", "l":
		if p.cursor < len(p.names) {
			into := p.names[p.cursor]
			if p.dir != "" {
				into = filepath.Join(p.dir, into)
			}
			return m.pickerGo(into, "")
		}
	case "backspace", "left", "h":
		if above, ok := folderAbove(p.dir); ok {
			on := filepath.Base(p.dir)
			if above == "" {
				on = p.dir // a drive is listed by its top folder
			}
			return m.pickerGo(above, on)
		}
	case "~":
		if home, err := os.UserHomeDir(); err == nil && home != "" {
			return m.pickerGo(home, "")
		}
	case ".":
		p.hidden = !p.hidden
		on := ""
		if p.cursor < len(p.names) {
			on = p.names[p.cursor]
		}
		return m.pickerGo(p.dir, on)
	case "n":
		if p.dir == "" {
			m.setToast(false, "Open a drive first: a folder is made inside one")
			return nil
		}
		p.naming = newLineInput("")
	case "s":
		if p.dir == "" {
			m.setToast(false, "Open a drive first, then take a folder on it")
			return nil
		}
		s.values, _ = s.values.Set(p.key, p.dir)
		s.picker = nil
	}
	return nil
}

// pickerNaming handles a key while a new folder is being named: enter makes
// the folder in the one shown, and goes into it.
func (m *Model) pickerNaming(msg tea.KeyPressMsg) tea.Cmd {
	p := m.settings.picker
	switch msg.String() {
	case "esc":
		p.naming = nil
	case "enter":
		name := strings.TrimSpace(p.naming.String())
		switch {
		case name == "":
			p.naming = nil
			return nil
		case name == "." || name == ".." || strings.ContainsAny(name, `/\`) || filepath.Base(name) != name:
			m.setToast(false, "A folder's name cannot hold a slash")
			return nil
		}
		p.naming = nil
		p.seq++
		p.reading = true
		seq, dir := p.seq, filepath.Join(p.dir, name)
		return func() tea.Msg {
			err := os.Mkdir(dir, 0o755)
			// One that is there already is simply gone into.
			if info, serr := os.Stat(dir); errors.Is(err, fs.ErrExist) && serr == nil && info.IsDir() {
				err = nil
			}
			return pickerMadeMsg{seq: seq, dir: dir, err: err}
		}
	default:
		p.naming.key(msg)
	}
	return nil
}

// pickerMade goes into the folder that was made, or says why it was not.
func (m *Model) pickerMade(msg pickerMadeMsg) tea.Cmd {
	p := m.settings.picker
	if p == nil || msg.seq != p.seq {
		return nil
	}
	p.reading = false
	if msg.err != nil {
		p.problem = "Cannot make the folder: " + msg.err.Error()
		return nil
	}
	return m.pickerGo(msg.dir, "")
}

// pickerRows is how many folders the picker lists at once.
func (m *Model) pickerRows() int {
	// Above the list: the title, a blank line, the folder shown and a blank
	// line. Below it: a blank line and a line for what went wrong or the name
	// being typed.
	return max(m.bodyHeight()-6, 1)
}

// pickerBody draws the folder picker in place of the settings.
func (m *Model) pickerBody() string {
	p := m.settings.picker
	w := max(m.width, 40)
	var b strings.Builder
	b.WriteString(boldStyle.Render("Settings") + "  " + accentStyle.Render("pick the "+strings.ToLower(p.label)) + "\n\n")
	shown := p.dir
	if shown == "" {
		shown = "The drives of this computer"
	}
	b.WriteString("  " + boldStyle.Render(clean(shown)) + "\n\n")

	rows := m.pickerRows()
	if p.cursor < p.top {
		p.top = p.cursor
	}
	if p.cursor >= p.top+rows {
		p.top = p.cursor - rows + 1
	}
	switch {
	case p.reading && len(p.names) == 0:
		b.WriteString(dimStyle.Render("  Reading…") + "\n")
	case len(p.names) == 0:
		b.WriteString(dimStyle.Render("  No folders in it") + "\n")
	}
	for i := p.top; i < min(p.top+rows, len(p.names)); i++ {
		mark := "    "
		if i == p.cursor {
			mark = "  " + accentStyle.Render("› ")
		}
		b.WriteString(mark + cell(clean(p.names[i]), w-6) + "\n")
	}
	if n := len(p.names); n > rows {
		b.WriteString(dimStyle.Render(fmt.Sprintf("  %d to %d of %d", p.top+1, min(p.top+rows, n), n)) + "\n")
	}
	switch {
	case p.naming != nil:
		b.WriteString("\n  " + accentStyle.Render("Name of the new folder: ") + p.naming.view(40) + dimStyle.Render("   enter makes it, esc does not") + "\n")
	case p.problem != "":
		b.WriteString("\n" + badStyle.Render(wrapAll(clean(p.problem), w-2)) + "\n")
	}
	return b.String()
}

// pickerHelp is the picker's line of keys.
func (m *Model) pickerHelp() string {
	if m.settings.picker.naming != nil {
		return "type the name  enter make the folder  esc cancel"
	}
	return "↑↓ move  enter open  backspace up  s take this folder  n new folder  . hidden  ~ home  esc cancel"
}
