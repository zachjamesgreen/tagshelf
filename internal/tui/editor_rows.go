package tui

import (
	"fmt"
	"path/filepath"
	"slices"
	"strings"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"charm.land/huh/v2"

	"github.com/zachjamesgreen/tagshelf/internal/musicbrainz"
	"github.com/zachjamesgreen/tagshelf/internal/store"
	"github.com/zachjamesgreen/tagshelf/internal/tagwrite"
)

// editorRow is one of the tags the editor lists under its fixed fields: a
// tag with no field of its own, an ID, or the manual marker.
type editorRow struct {
	// field is what a save is given the row's values under, and from the
	// field they were read under: the same, but for the marker.
	field, from tagwrite.Field
	label       string
	value       string // what the row's field of the form holds
	// base is what it held when the tags were read, made fit to show. A
	// row that still holds it is left as the file has it.
	base string
	// list is set for a row of several values, shown joined and split at
	// the semicolons again when saved.
	list bool
	// id is set for a row whose values must have the form of an ID.
	id bool
}

// formEntry is a field of the editor's form that takes text, with what it
// edits: one of editorFields, or a row.
type formEntry struct {
	field huh.Field
	id    tagwrite.Field
	row   *editorRow
}

// idRows are the IDs the editor lists, in order, with what it calls each.
var idRows = []struct {
	field tagwrite.Field
	label string
	list  bool
}{
	{tagwrite.RecordingID, "Recording ID", false}, {tagwrite.ReleaseTrackID, "Track ID", false},
	{tagwrite.AlbumID, "Release ID", false}, {tagwrite.ReleaseGroupID, "Release group ID", false},
	{tagwrite.ArtistIDs, "Artist IDs", true}, {tagwrite.AlbumArtistIDs, "Album artist IDs", true},
	{tagwrite.AcoustID, "AcoustID ID", false},
}

// makeRows lists the rows for the tags in e.orig: the tags without a field
// of their own by name, then the IDs, then the marker.
func (e *editor) makeRows() {
	e.rows = nil
	var others []tagwrite.Field
	for field := range e.orig {
		if _, ok := field.OtherName(); ok {
			others = append(others, field)
		}
	}
	slices.Sort(others)
	for _, field := range others {
		name, _ := field.OtherName()
		vs := e.orig[field]
		e.rows = append(e.rows, &editorRow{field: field, from: field, label: clean(name), list: len(vs) > 1})
	}
	e.firstID = len(e.rows)
	for _, id := range idRows {
		e.rows = append(e.rows, &editorRow{field: id.field, from: id.field, label: id.label, list: id.list, id: true})
	}
	e.rows = append(e.rows, &editorRow{field: tagwrite.SetMarker, from: tagwrite.Marker, label: "Marker"})
	for _, r := range e.rows {
		r.base = e.rowText(r)
		r.value = r.base
	}
}

// rowText is what a row's field of the form holds for the file's values.
// Only a tag without a field of its own is given a box of several lines.
func (e *editor) rowText(r *editorRow) string {
	_, other := r.field.OtherName()
	return formText(strings.Join(e.orig[r.from], store.ListSeparator), other)
}

// formText makes a tag's value fit to put in a field of the form: with a
// space in place of every control character, so that none reaches the
// terminal. With lines its line breaks are kept, each as a plain one, for a
// box of several lines; otherwise they are spaces too.
func formText(s string, lines bool) string {
	if !lines {
		return clean(s)
	}
	parts := strings.Split(strings.ReplaceAll(strings.ReplaceAll(s, "\r\n", "\n"), "\r", "\n"), "\n")
	for i, part := range parts {
		parts[i] = clean(part)
	}
	return strings.Join(parts, "\n")
}

// rebase takes the file's tags, as just read, for what the form's fields
// and rows are compared with. A row added by hand has none.
func (e *editor) rebase() {
	e.base = make([]string, len(editorFields))
	for i, field := range editorFields {
		e.base[i] = formText(shown(field, e.orig[field]), field == tagwrite.Comment)
	}
	for _, r := range e.rows {
		r.base = e.rowText(r)
	}
}

// asRead reports whether s, as typed for one of the fixed fields, is what
// the field held when the tags were read.
func (e *editor) asRead(field tagwrite.Field, s string) bool {
	for i, f := range editorFields {
		if f == field {
			return sameText(s, typed(field, e.base[i]))
		}
	}
	return false
}

// sameText reports whether two texts are the same but for the space around
// them and the kind of line break in them.
func sameText(a, b string) bool {
	plain := func(s string) string {
		return strings.TrimSpace(strings.ReplaceAll(strings.ReplaceAll(s, "\r\n", "\n"), "\r", "\n"))
	}
	return plain(a) == plain(b)
}

// values are what the row holds, as a save takes them: none when it was
// emptied, which takes the tag off.
func (r *editorRow) values() []string {
	s := strings.TrimSpace(r.value)
	switch {
	case s == "":
		return []string{}
	case r.list:
		return listed(s)
	}
	return []string{s}
}

// changed reports whether the row holds something else than when the tags
// were read.
func (e *editor) changed(r *editorRow) bool {
	return !sameText(r.value, r.base)
}

// checkRow reports what is wrong with a row as it stands, nil when a save
// would take it. A row left as the file has it is never wrong.
func (e *editor) checkRow(r *editorRow) error {
	if !e.changed(r) {
		return nil
	}
	vs := r.values()
	switch {
	case r.field == tagwrite.SetMarker:
		if len(vs) == 0 && !canLoseMarker(e.file.Path) {
			return tagwrite.ErrMarkerStays
		}
		if len(vs) > 0 {
			return tagwrite.Check(tagwrite.SetMarker, vs[0])
		}
	case r.id:
		for _, v := range vs {
			if !musicbrainz.IsID(v) {
				return fmt.Errorf("%s: %q does not have the form of an ID", r.label, v)
			}
		}
	}
	return nil
}

// canLoseMarker reports whether the marker can be taken off the file at
// path: tagshelf cannot take it off a WMA file.
func canLoseMarker(path string) bool {
	return !strings.EqualFold(filepath.Ext(path), ".wma")
}

// longText reports whether a value takes a box of several lines to edit.
func longText(s string) bool { return strings.ContainsAny(s, "\r\n") }

// buildForm makes the editor's form from what its fields and rows hold now,
// with the cursor on the text field numbered focus. It is made again when a
// tag is added, which the form has no way to take in.
func (m *Model) buildForm(focus int) tea.Cmd {
	e := m.edit
	keys := huh.NewDefaultKeyMap()
	keys.Quit.SetEnabled(false) // ctrl+c is the TUI's
	keys.Input.Prev = key.NewBinding(key.WithKeys("shift+tab", "up"))
	keys.Input.Next = key.NewBinding(key.WithKeys("enter", "tab", "down"))
	keys.Confirm.Prev = key.NewBinding(key.WithKeys("shift+tab", "up"))
	keys.Confirm.Next = key.NewBinding(key.WithKeys("enter", "tab", "down"))
	// A y or n typed on the flag sets nothing: only the arrows and space do.
	keys.Confirm.Toggle = key.NewBinding(key.WithKeys("left", "right", "space"))
	keys.Confirm.Accept.SetEnabled(false)
	keys.Confirm.Reject.SetEnabled(false)
	// In a box of several lines enter is a line break, and the arrows move
	// inside it: tab and shift+tab leave it.
	keys.Text.NewLine = key.NewBinding(key.WithKeys("enter"))
	keys.Text.Next = key.NewBinding(key.WithKeys("tab"))
	keys.Text.Prev = key.NewBinding(key.WithKeys("shift+tab"))
	keys.Text.Submit.SetEnabled(false)
	keys.Text.Editor.SetEnabled(false)

	width := 13
	for _, r := range e.rows {
		width = max(width, min(len([]rune(r.label))+1, 24))
	}
	e.entries = nil
	var fields []huh.Field
	text := func(label string, value *string, check func(string) error) huh.Field {
		if longText(*value) {
			lines := min(max(strings.Count(*value, "\n")+2, 3), 6)
			return huh.NewText().Title(label).Value(value).Lines(lines).ShowLineNumbers(false).Validate(check)
		}
		return huh.NewInput().Title(fmt.Sprintf("%-*s", width, label)).Value(value).Inline(true).Validate(check)
	}
	placeholders := map[tagwrite.Field]string{
		tagwrite.Date: "YYYY or YYYY-MM-DD", tagwrite.Track: "N or N/total", tagwrite.Disc: "N or N/total",
		tagwrite.Genre: "several are written Rock; Jazz",
	}
	labels := []string{"Title", "Artist", "Album artist", "Album", "Year", "Track", "Disc", "Genre", "Composer", "Comment"}
	for i, p := range e.inputs() {
		id := editorFields[i]
		f := text(labels[i], p, func(s string) error { return e.check(id, s) })
		if in, ok := f.(*huh.Input); ok && placeholders[id] != "" {
			in.Placeholder(placeholders[id])
		}
		fields = append(fields, f)
		e.entries = append(e.entries, formEntry{field: f, id: id})
	}
	if tagwrite.HasCompilation(e.file.Path) {
		fields = append(fields, huh.NewConfirm().Title(fmt.Sprintf("%-*s", width, "Compilation")).Value(&e.compilation).Inline(true))
	}
	// The rows need no headings: a tag without a field of its own is listed
	// by its name in capitals, and the IDs and the marker by what they are.
	for _, r := range e.rows {
		f := text(r.label, &r.value, func(string) error { return e.checkRow(r) })
		fields = append(fields, f)
		e.entries = append(e.entries, formEntry{field: f, id: r.field, row: r})
	}
	e.last = fields[len(fields)-1]
	e.form = huh.NewForm(huh.NewGroup(fields...)).WithTheme(closeSet).
		WithKeyMap(keys).WithShowHelp(false).WithWidth(m.editorWidth()).WithHeight(m.editorHeight())
	cmd := e.form.Init()
	for range focus {
		_, next := e.form.Update(huh.NextField())
		cmd = tea.Batch(cmd, next)
	}
	return cmd
}

// focusedEntry is the text field the form's cursor is on, false on the
// compilation flag.
func (e *editor) focusedEntry() (formEntry, int, bool) {
	cur := e.form.GetFocusedField()
	for i, entry := range e.entries {
		if entry.field == cur {
			return entry, i, true
		}
	}
	return formEntry{}, 0, false
}

// formLines is how many lines the form takes when all of it shows.
func (e *editor) formLines() int {
	lines := len(editorFields) + 1 + len(e.rows) // with the flag
	for _, p := range e.inputs() {
		if longText(*p) {
			lines += 6
		}
	}
	for _, r := range e.rows {
		if longText(r.value) {
			lines += 6
		}
	}
	return lines
}

// nameTag starts the naming of a new tag: the name is typed on a line under
// the form.
func (m *Model) nameTag() {
	if e := m.edit; e.form != nil {
		e.naming = newLineInput("")
	}
}

// namingKey handles a key while a new tag is being named.
func (m *Model) namingKey(msg tea.KeyPressMsg) tea.Cmd {
	e := m.edit
	switch msg.String() {
	case "esc":
		e.naming = nil
	case "enter":
		field, err := tagwrite.NewOther(e.naming.String())
		if err != nil {
			m.setToast(false, "%s", capitalize(err.Error()))
			return nil
		}
		e.naming = nil
		return m.addRow(field)
	default:
		e.naming.key(msg)
	}
	return nil
}

// addRow adds a row for the tag field names, after the other tags there are,
// and puts the cursor on it. A tag that has a row already gets the cursor.
func (m *Model) addRow(field tagwrite.Field) tea.Cmd {
	e := m.edit
	at := slices.IndexFunc(e.rows, func(r *editorRow) bool { return r.field == field })
	if at < 0 {
		name, _ := field.OtherName()
		at = e.firstID
		e.rows = slices.Insert(e.rows, at, &editorRow{field: field, from: field, label: name})
		e.firstID++
	}
	return m.buildForm(len(editorFields) + map[bool]int{true: 1}[tagwrite.HasCompilation(e.file.Path)] + at)
}

// rowChanges are the rows whose values differ from the file's, as a save
// takes them.
func (e *editor) rowChanges() tagwrite.Values {
	v := tagwrite.Values{}
	for _, r := range e.rows {
		if e.changed(r) {
			v[r.field] = r.values()
		}
	}
	return v
}
