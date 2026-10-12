package tui

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/zachjamesgreen/tagshelf/internal/engine"
	"github.com/zachjamesgreen/tagshelf/internal/rules"
	"github.com/zachjamesgreen/tagshelf/internal/store"
	"github.com/zachjamesgreen/tagshelf/internal/tagwrite"
)

// folderFields are the fields the editor can write to every file of the
// folder at once, by what the question calls each: those an album's files
// share. A title, a track or a disc number is one file's own.
var folderFields = map[tagwrite.Field]string{
	tagwrite.AlbumArtist: "album artist", tagwrite.Album: "album", tagwrite.Date: "year",
	tagwrite.Genre: "genre", tagwrite.Composer: "composer", tagwrite.Comment: "comment",
}

// folderAsk is the editor's question before it writes one field to every
// file of the folder.
type folderAsk struct {
	field  tagwrite.Field
	values []string // what is written; none takes the tag off
	ids    []int64  // the files of the folder tagshelf can tag
}

// editorFolderMsg is the files of the folder the editor's file is in, for a
// field to be written to them all.
type editorFolderMsg struct {
	id     int64
	field  tagwrite.Field
	values []string
	files  []store.File
	err    error
}

// editorFolderDoneMsg says a write to the whole folder has ended.
type editorFolderDoneMsg struct {
	field    tagwrite.Field
	files    int
	moved    []string
	warnings string
	finish   engine.RunFinished
	err      error
}

// folderName is what the question calls a field that can be written to the
// whole folder, false for one that cannot: one file's own, an ID or the
// marker.
func folderName(field tagwrite.Field) (string, bool) {
	if name, ok := field.OtherName(); ok {
		return "tag " + name, true
	}
	name, ok := folderFields[field]
	return name, ok
}

// value is what the form holds for field, as a save takes it.
func (e *editor) value(field tagwrite.Field) []string {
	for _, r := range e.rows {
		if r.field == field {
			return r.values()
		}
	}
	for i, p := range e.inputs() {
		if editorFields[i] != field {
			continue
		}
		s := typed(field, *p)
		switch {
		case s == "":
			return []string{}
		case tagwrite.MultiValued(field):
			return listed(s)
		}
		return []string{s}
	}
	return []string{}
}

// askFolder starts the write of the field under the cursor to every file of
// the folder: it reads which files those are, off the UI goroutine, for the
// question to count them.
func (m *Model) askFolder() tea.Cmd {
	e := m.edit
	if e.form == nil {
		return nil
	}
	entry, _, ok := e.focusedEntry()
	field := entry.id
	if _, shared := folderName(field); !ok || !shared {
		m.setToast(false, "Only the album artist, album, year, genre, composer, comment and the other tags can be written to the whole folder")
		return nil
	}
	if err := e.invalid(); err != nil {
		m.setToast(false, "Cannot write that: %v", err)
		return nil
	}
	for changed := range e.changes() {
		if changed != field {
			// The write closes the editor, as a save does.
			m.setToast(false, "Save the other changes first, or take them back: writing to the folder closes the editor")
			return nil
		}
	}
	if !m.canChange() {
		return nil
	}
	e.counting = true
	src, file, values := m.src, e.file, e.value(field)
	return func() tea.Msg {
		files, err := src.Album(file)
		return editorFolderMsg{id: file.ID, field: field, values: values, files: files, err: err}
	}
}

// editorFolder puts the question, now that the folder's files are known.
func (m *Model) editorFolder(msg editorFolderMsg) {
	e := m.edit
	if e == nil || e.file.ID != msg.id {
		return
	}
	e.counting = false
	if msg.err != nil {
		m.setToast(false, "Cannot read the folder's files: %v", msg.err)
		return
	}
	ask := &folderAsk{field: msg.field, values: msg.values}
	for _, f := range msg.files {
		if tagwrite.Supported(f.Path) {
			ask.ids = append(ask.ids, f.ID)
		}
	}
	if len(ask.ids) == 0 {
		m.setToast(false, "The folder has no files tagshelf can tag")
		return
	}
	e.folder = ask
}

// question is what the editor asks before the write.
func (a *folderAsk) question() string {
	files := "all " + plural(int64(len(a.ids)), "file", "files") + " in this folder"
	if len(a.ids) == 1 {
		files = "the one file in this folder"
	}
	name, _ := folderName(a.field)
	if len(a.values) == 0 {
		return fmt.Sprintf("Take the %s off %s? y takes it off, any other key does not", name, files)
	}
	return fmt.Sprintf("Write the %s %q to %s? y writes it, any other key does not", name, clean(strings.Join(a.values, store.ListSeparator)), files)
}

// writeToFolder carries the answered question out, as one run that undo
// takes back whole.
func (m *Model) writeToFolder(ask *folderAsk) tea.Cmd {
	if !m.canChange() {
		return nil
	}
	m.edit.saving = true
	save := editSave(tagwrite.Values{ask.field: ask.values}, time.Now())
	return m.saveMany(ask.ids, save, func(moved []string, warnings string, finish engine.RunFinished, err error) tea.Msg {
		return editorFolderDoneMsg{field: ask.field, files: len(ask.ids), moved: moved, warnings: warnings, finish: finish, err: err}
	})
}

// editorFolderDone says how the write went. The editor closes on one that
// worked: the files may have moved, the one it was open on among them.
func (m *Model) editorFolderDone(msg editorFolderDoneMsg) tea.Cmd {
	m.ended()
	if e := m.edit; e != nil {
		e.saving = false
	}
	reload := m.poll(true, false)
	errs := msg.finish.Counters.Errors
	switch {
	case msg.err != nil:
		m.setToast(false, "Cannot change the library: %v", msg.err)
		return reload
	case errs > 0 || msg.finish.State != engine.StateOK:
		m.setToast(false, "The write to the folder did not finish: %s, see %d Runs", plural(errs, "error", "errors"), int(TabRuns)+1)
		return reload
	}
	m.edit = nil
	name, _ := folderName(msg.field)
	parts := []string{strings.ToUpper(name[:1]) + name[1:] + " written to " + plural(int64(msg.files), "file", "files")}
	if n := len(msg.moved); n > 0 {
		dirs := map[string]bool{}
		for _, p := range msg.moved {
			dirs[rules.PathKey(filepath.Dir(p))] = true
		}
		moved := plural(int64(n), "file", "files") + " moved"
		if len(dirs) == 1 {
			moved += " to " + m.shortPath(filepath.Dir(msg.moved[0]))
		}
		parts = append(parts, moved)
	}
	if msg.warnings != "" {
		parts = append(parts, msg.warnings)
	}
	m.setToast(true, "%s", strings.Join(parts, "; "))
	m.offerUndo(msg.finish.RunID)
	return reload
}
