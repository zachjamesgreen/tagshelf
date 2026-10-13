package tui

import (
	"cmp"
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/zachjamesgreen/tagshelf/internal/engine"
	"github.com/zachjamesgreen/tagshelf/internal/store"
	"github.com/zachjamesgreen/tagshelf/internal/tagwrite"
)

// FolderList is what the Files tab shows of one folder: the folders in it
// and its audio files, each by name.
type FolderList struct {
	// Dir is the folder, empty for the list of drives.
	Dir     string
	Folders []string
	Files   []ListedFile
}

// ListedFile is an audio file of a folder the Files tab shows.
type ListedFile struct {
	// File is the file's index row when it has one, and otherwise what was
	// read from the file itself, without an ID.
	File store.File
	// Indexed is set for a file the index holds, which is then one of the
	// library's, and InLibrary for any file inside the library's folders.
	Indexed, InLibrary bool
	// Link is set for a symbolic link, which tagshelf does not write through.
	Link bool
	// Problem is why the file could not be read, empty when it could.
	Problem string
}

// readFolder lists dir for the Files tab. st is the library's database, nil
// when there is no library: with one, a file of the library is listed by its
// index row.
func readFolder(st *store.Store, dir string, hidden bool) (FolderList, error) {
	list := FolderList{Dir: dir}
	folders, err := listFolders(dir, hidden)
	if err != nil {
		return list, err
	}
	list.Folders = folders
	if dir == "" {
		return list, nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return list, err
	}
	inLibrary := false
	if st != nil {
		if kind, ok, err := st.RootOf(dir); err == nil && ok && kind != store.RootSource {
			inLibrary = true
		}
	}
	for _, e := range entries {
		name := e.Name()
		// tagshelf's own files are never music, and a dot hides a file as it
		// does a folder.
		if strings.HasPrefix(name, store.OwnFilePrefix) || (!hidden && strings.HasPrefix(name, ".")) || !tagwrite.Supported(name) {
			continue
		}
		if e.IsDir() || e.Type()&^fs.ModeSymlink != 0 {
			continue // a folder, or something that is no file
		}
		path := filepath.Join(dir, name)
		if inLibrary {
			if row, ok, err := st.FileAt(path); err == nil && ok && row.RootKind != store.RootSource {
				list.Files = append(list.Files, ListedFile{File: row, Indexed: true, InLibrary: true})
				continue
			}
		}
		f, link, err := engine.ReadListed(path)
		switch {
		case err != nil && link:
			continue // a link that leads nowhere, or to a folder
		case err != nil:
			list.Files = append(list.Files, ListedFile{File: store.File{Path: path, RelPath: name}, InLibrary: inLibrary, Problem: err.Error()})
			continue
		}
		list.Files = append(list.Files, ListedFile{File: f, InLibrary: inLibrary, Link: link})
	}
	slices.SortFunc(list.Files, func(a, b ListedFile) int {
		x, y := filepath.Base(a.File.Path), filepath.Base(b.File.Path)
		return cmp.Or(cmp.Compare(strings.ToLower(x), strings.ToLower(y)), cmp.Compare(x, y))
	})
	return list, nil
}

// filesTab is the state of the Files tab: one folder of the computer, its
// folders and its audio files.
type filesTab struct {
	loaded, reading bool
	// seq numbers the reads, so that the answer to an earlier one is dropped,
	// and going is the read on its way: the folder, and where the cursor is
	// to be put in it.
	seq   int
	going filesReadMsg
	list  FolderList
	// cursor counts through the folders and then the files.
	cursor   int
	selected map[int64]bool
	hidden   bool
	problem  string
	// stale is set when the library changed while another tab showed: the
	// folder is read again when this one shows.
	stale bool

	// ids gives every file that is not in the index a number of its own for
	// as long as tagshelf runs, by the key of its path. The numbers are below
	// zero, where no row of the index has its ID.
	ids  map[string]int64
	next int64
}

// filesReadMsg is a folder the Files tab asked for.
type filesReadMsg struct {
	seq  int
	list FolderList
	err  error
	// on is the folder to put the cursor on, and onID the file, when the
	// folder is one read again.
	on   string
	onID int64
}

// outside reports whether f is a file the Files tab listed that is not in
// the index: one tagshelf tags where it is and never moves.
func outside(f store.File) bool { return f.ID < 0 }

// staysLine says, where a file of the index is told where a save would put
// it, that a file outside it stays.
const staysLine = "Stays where it is: a file outside the library is not moved"

// filesStart is the folder the Files tab opens on: the source folder, or
// else the home folder.
func (m *Model) filesStart() string { return pickerStart(m.source) }

// filesGo reads dir off the UI goroutine, which a drive that is slow to
// answer would otherwise hold up, to show it with the cursor on the folder
// named on, or on the file with the ID onID.
func (m *Model) filesGo(dir, on string, onID int64) tea.Cmd {
	t := &m.files
	t.seq++
	t.reading, t.stale = true, false
	t.going = filesReadMsg{list: FolderList{Dir: dir}, on: on, onID: onID}
	seq, src, hidden := t.seq, m.src, t.hidden
	return func() tea.Msg {
		list, err := src.Folder(dir, hidden)
		return filesReadMsg{seq: seq, list: list, err: err, on: on, onID: onID}
	}
}

// filesRead shows the folder that was read. One that cannot be read is not
// gone into: the tab stays where it was and says why.
func (m *Model) filesRead(msg filesReadMsg) {
	t := &m.files
	if msg.seq != t.seq {
		return
	}
	t.reading = false
	if msg.err != nil {
		t.problem = "Cannot open " + msg.list.Dir + ": " + msg.err.Error()
		// Not read at all yet: the list stays empty, and esc or ~ goes on.
		t.loaded = true
		return
	}
	t.loaded, t.problem = true, ""
	t.list = msg.list
	if t.ids == nil {
		t.ids = make(map[string]int64)
	}
	here := make(map[int64]bool, len(t.list.Files))
	t.cursor = max(slices.Index(t.list.Folders, msg.on), 0)
	for i := range t.list.Files {
		f := &t.list.Files[i].File
		if f.ID == 0 {
			key := store.PathKey(f.Path)
			if t.ids[key] == 0 {
				t.next--
				t.ids[key] = t.next
			}
			f.ID = t.ids[key]
		}
		here[f.ID] = true
		if f.ID == msg.onID && msg.onID != 0 {
			t.cursor = len(t.list.Folders) + i
		}
	}
	for id := range t.selected {
		if !here[id] {
			delete(t.selected, id)
		}
	}
}

// filesShown reads the folder when the Files tab shows and has none, or one
// that the library has changed under.
func (m *Model) filesShown() tea.Cmd {
	t := &m.files
	if m.tab != TabFiles || t.reading || (t.loaded && !t.stale) {
		return nil
	}
	if !t.loaded {
		return m.filesGo(m.filesStart(), "", 0)
	}
	return m.filesAgain()
}

// filesAgain reads the folder shown again, keeping the cursor on its file.
func (m *Model) filesAgain() tea.Cmd {
	t := &m.files
	var onID int64
	on := ""
	if folder, file, ok := t.current(); ok && file != nil {
		onID = file.File.ID
	} else if ok {
		on = folder
	}
	return m.filesGo(t.list.Dir, on, onID)
}

// filesReloaded reads the folder again after the library changed: a save
// may have changed a file's tags, or moved it away.
func (m *Model) filesReloaded() tea.Cmd {
	t := &m.files
	switch {
	case !t.loaded || t.reading:
		return nil
	case m.tab != TabFiles:
		t.stale = true
		return nil
	}
	return m.filesAgain()
}

// current is what the cursor is on: a folder by name, or a file. ok is false
// for an empty folder.
func (t *filesTab) current() (folder string, file *ListedFile, ok bool) {
	n := len(t.list.Folders)
	switch {
	case t.cursor < n:
		return t.list.Folders[t.cursor], nil, true
	case t.cursor-n < len(t.list.Files):
		return "", &t.list.Files[t.cursor-n], true
	}
	return "", nil, false
}

func (t *filesTab) count() int { return len(t.list.Folders) + len(t.list.Files) }

// listed is the file with the ID id, if the folder shown holds it.
func (t *filesTab) listed(id int64) (store.File, bool) {
	for _, f := range t.list.Files {
		if f.File.ID == id {
			return f.File, true
		}
	}
	return store.File{}, false
}

// whyNot says why tagshelf cannot change f, empty when it can.
func whyNot(f *ListedFile) string {
	switch {
	case f.Problem != "":
		return "This file cannot be read: " + f.Problem
	case f.Link:
		return "This file is a link: tagshelf does not change a file through one"
	case f.InLibrary && !f.Indexed:
		return fmt.Sprintf("This file is in the library's folders and not in the index yet: press i on %d Home first", int(TabHome)+1)
	case f.Indexed && f.File.RootKind == store.RootDuplicates:
		return "This file was set aside as a duplicate: tagshelf leaves the copies in _Duplicates as they are"
	}
	return ""
}

// targets are the files an action is for: those selected, or else the one
// under the cursor. Files tagshelf cannot change are left out of a
// selection, and for the one under the cursor why says why not.
func (t *filesTab) targets() (files []store.File, why string) {
	for i := range t.list.Files {
		f := &t.list.Files[i]
		if t.selected[f.File.ID] && whyNot(f) == "" {
			files = append(files, f.File)
		}
	}
	if len(files) > 0 {
		return files, ""
	}
	if _, f, ok := t.current(); ok && f != nil {
		if why := whyNot(f); why != "" {
			return nil, why
		}
		return []store.File{f.File}, ""
	}
	return nil, ""
}

// filesKey handles a key on the Files tab, reporting whether it was one.
func (m *Model) filesKey(k string) (tea.Cmd, bool) {
	t := &m.files
	if !t.loaded || t.reading {
		return nil, false
	}
	page := max(m.filesRows()-1, 1)
	last := max(t.count()-1, 0)
	folder, file, ok := t.current()
	switch k {
	case "up", "k":
		t.cursor = max(t.cursor-1, 0)
	case "down", "j":
		t.cursor = min(t.cursor+1, last)
	case "pgup":
		t.cursor = max(t.cursor-page, 0)
	case "pgdown":
		t.cursor = min(t.cursor+page, last)
	case "home":
		t.cursor = 0
	case "end":
		t.cursor = last
	case "enter", "right":
		switch {
		case ok && file == nil:
			into := folder
			if t.list.Dir != "" {
				into = filepath.Join(t.list.Dir, folder)
			}
			return m.filesGo(into, "", 0), true
		case ok:
			return m.filesEdit(file), true
		}
	case "backspace", "left", "h":
		if above, up := folderAbove(t.list.Dir); up {
			on := filepath.Base(t.list.Dir)
			if above == "" {
				on = t.list.Dir // a drive is listed by its top folder
			}
			clear(t.selected)
			return m.filesGo(above, on, 0), true
		}
	case "~":
		if home, err := os.UserHomeDir(); err == nil && home != "" {
			clear(t.selected)
			return m.filesGo(home, "", 0), true
		}
	case ".":
		t.hidden = !t.hidden
		return m.filesAgain(), true
	case "space", " ":
		if ok && file != nil {
			if why := whyNot(file); why != "" {
				m.setToast(false, "%s", why)
				return nil, true
			}
			if t.selected == nil {
				t.selected = make(map[int64]bool)
			}
			if id := file.File.ID; t.selected[id] {
				delete(t.selected, id)
			} else {
				t.selected[id] = true
			}
			t.cursor = min(t.cursor+1, last)
		}
	case "esc":
		if len(t.selected) == 0 {
			return nil, false
		}
		clear(t.selected)
	case "e":
		if ok && file != nil {
			return m.filesEdit(file), true
		}
	case "l":
		if ok && file != nil {
			if why := whyNot(file); why != "" {
				m.setToast(false, "%s", why)
				return nil, true
			}
			return m.openLookup(file.File, ""), true
		}
	case "L":
		// The selection, or else every file of the folder that can be changed.
		var files []store.UntaggedFile
		for i := range t.list.Files {
			f := &t.list.Files[i]
			if (len(t.selected) == 0 || t.selected[f.File.ID]) && whyNot(f) == "" {
				files = append(files, store.UntaggedFile{File: f.File})
			}
		}
		if len(files) > 0 {
			return m.openMatch(files), true
		}
	case "a":
		files, why := t.targets()
		if why != "" {
			m.setToast(false, "%s", why)
			return nil, true
		}
		if len(files) > 0 && m.canChange() {
			ids := make([]int64, len(files))
			for i, f := range files {
				ids[i] = f.ID
			}
			return m.acceptTags(ids), true
		}
	default:
		return nil, false
	}
	return nil, true
}

// filesEdit opens the tag editor on a file, or says why it cannot be
// changed.
func (m *Model) filesEdit(f *ListedFile) tea.Cmd {
	if why := whyNot(f); why != "" {
		m.setToast(false, "%s", why)
		return nil
	}
	return m.openEditor(f.File)
}

// folderOf lists the files of the folder f is in that a write to the whole
// folder would go to: for a file of the index its album, as the library has
// it, and for one outside it the files the Files tab lists beside it.
func (m *Model) folderOf(f store.File) ([]store.File, error) {
	if !outside(f) {
		return m.src.Album(f)
	}
	var files []store.File
	for i := range m.files.list.Files {
		if l := &m.files.list.Files[i]; whyNot(l) == "" && outside(l.File) {
			files = append(files, l.File)
		}
	}
	return files, nil
}

// tagSaver is what saves tags to the files with the given IDs as one run:
// the Runner's SaveTags for files of the index, and for the files the Files
// tab listed outside it SaveFiles, which leaves them where they are. It is
// worked out on the UI goroutine and called off it.
func (m *Model) tagSaver(ids []int64) func(ctx context.Context, save engine.TagSave, sink engine.Sink) (engine.RunFinished, error) {
	runner := m.runner
	var listed []store.File
	for _, id := range ids {
		if id < 0 {
			if f, ok := m.files.listed(id); ok {
				listed = append(listed, f)
			}
		}
	}
	if len(listed) > 0 {
		return func(ctx context.Context, save engine.TagSave, sink engine.Sink) (engine.RunFinished, error) {
			return runner.SaveFiles(ctx, listed, save, sink)
		}
	}
	return func(ctx context.Context, save engine.TagSave, sink engine.Sink) (engine.RunFinished, error) {
		return runner.SaveTags(ctx, ids, save, sink)
	}
}

// filesRows is how many folders and files the tab lists at once.
func (m *Model) filesRows() int {
	// Above the list: the title, the folder and the column names. Below it:
	// a line for where in a long list the cursor is.
	return max(m.bodyHeight()-4, 1)
}

// filesState is the badge of a file: why it cannot be changed, or how far
// its tags are from what the library takes.
func filesState(f *ListedFile) string {
	switch {
	case f.Problem != "":
		return "cannot be read"
	case f.Link:
		return "a link, not changed"
	case f.InLibrary && !f.Indexed:
		return "not in the index yet"
	case f.Indexed && f.File.RootKind == store.RootLibrary:
		return "in the library"
	case f.Indexed && f.File.RootKind == store.RootDuplicates:
		return "set aside"
	}
	return untaggedState(f.File)
}

// filesBody draws the Files tab in h lines.
func (m *Model) filesBody(h int) string {
	t := &m.files
	var b strings.Builder
	title := boldStyle.Render("Files")
	switch {
	case !t.loaded:
		return title + "\n" + dimStyle.Render("  Reading the folder…")
	case len(t.selected) > 0:
		title += "  " + accentStyle.Render(formatCount(int64(len(t.selected)))+" selected")
	case t.reading:
		title += dimStyle.Render("  reading…")
	}
	b.WriteString(title + "\n")
	shown := t.list.Dir
	if shown == "" {
		shown = "The drives of this computer"
	}
	b.WriteString("  " + boldStyle.Render(tailPath(clean(shown), max(m.width-4, 10))) + "\n")
	if t.problem != "" {
		b.WriteString(badStyle.Render(wrapAll(clean(t.problem), max(m.width-2, 20))) + "\n")
	}
	if t.count() == 0 {
		if t.problem == "" {
			b.WriteString(dimStyle.Render("  No folders and no audio files in it") + "\n")
		}
		return b.String()
	}

	flex := max(m.width-untaggedFixedWidth, 32)
	fileW, artistW, albumW := flex*28/100, flex*22/100, flex*24/100
	titleW := flex - fileW - artistW - albumW
	row := func(file, artist, album, title, length, state string) string {
		return fmt.Sprintf("%s  %s  %s  %s  %5s  %s",
			cell(file, fileW), cell(artist, artistW), cell(album, albumW), cell(title, titleW), length, state)
	}
	b.WriteString(dimStyle.Render("    "+row("Name", "Artist", "Album", "Title", "Len", "State")) + "\n")

	rows := max(h-4, 1)
	first := window(t.cursor, rows)
	folders := len(t.list.Folders)
	for i := first; i < min(first+rows, t.count()); i++ {
		var line string
		if i < folders {
			line = "  " + cell(clean(t.list.Folders[i])+string(filepath.Separator), max(m.width-6, 10))
		} else {
			f := &t.list.Files[i-folders]
			mark := "  "
			if t.selected[f.File.ID] {
				mark = "● "
			}
			artist := f.File.Artist
			if artist == "" {
				artist = f.File.AlbumArtist
			}
			line = mark + row(filepath.Base(f.File.Path), artist, f.File.Album, f.File.Title, length(f.File.Length), filesState(f))
		}
		if i == t.cursor {
			line = "› " + activeTab.Render(line)
		} else {
			line = "  " + line
		}
		b.WriteString(line + "\n")
	}
	if n := t.count(); n > rows {
		b.WriteString(dimStyle.Render(fmt.Sprintf("  %d to %d of %d", first+1, min(first+rows, n), n)) + "\n")
	}
	return b.String()
}
