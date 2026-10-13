package tui

import (
	"fmt"
	"image"
	"maps"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/huh/v2"
	"charm.land/lipgloss/v2"

	"github.com/zachjamesgreen/tagshelf/internal/engine"
	"github.com/zachjamesgreen/tagshelf/internal/marker"
	"github.com/zachjamesgreen/tagshelf/internal/rules"
	"github.com/zachjamesgreen/tagshelf/internal/store"
	"github.com/zachjamesgreen/tagshelf/internal/tagwrite"
)

// editor is the full-screen tag editor, open on one indexed file.
type editor struct {
	file store.File
	from Tab // the tab it was opened from, for the breadcrumb

	// orig are the file's tags as read when the editor opened, nil until
	// they have been.
	orig tagwrite.Values
	form *huh.Form
	// What the form's fields hold.
	title, artist, albumArtist, album, date, track, disc string
	genre, composer, comment                             string
	compilation                                          bool
	// base is what each of those fields held when the tags were read, made
	// fit to show (see formText). A field that still holds it is left as
	// the file has it, whatever the file has.
	base []string
	// rows are the tags listed under the fixed fields: those without a field
	// of their own, then, from firstID on, the IDs and the marker.
	rows    []*editorRow
	firstID int
	// entries are the form's text fields, in order, with what each edits.
	entries []formEntry
	// naming holds the name being typed for a new tag, nil when none is.
	naming *lineInput

	last    huh.Field // the form's last field, where enter saves
	saving  bool      // a save is being carried out
	leaving bool      // esc was pressed with changes unsaved; again discards them
	// counting is set while the folder's files are read for the question
	// folder then holds: whether to write a field to them all.
	counting bool
	folder   *folderAsk

	// pic is the picture the file holds, nil when it has none to show.
	// picLines are its lines as blocks, picCols cells wide, and beside the
	// text next to it on the lines an image is redrawn for (see picPlace).
	pic      image.Image
	picLines []string
	picCols  int
	beside   string
}

// editorTagsMsg is the tags of the file the editor opened on.
type editorTagsMsg struct {
	id     int64
	values tagwrite.Values
	err    error
	// again is set for a read after a save that went wrong part way: the
	// form keeps what was typed, and only what it is compared with changes.
	again bool
}

// saved is how a save of tags to one file went.
type saved struct {
	id       int64
	moved    string // where the file went, "" when it stayed
	problem  error  // what went wrong with the file, nil when nothing did
	warnings string // what the run warned of, "" when nothing
	finish   engine.RunFinished
	err      error
}

// failed says in a toast why the save did not work, reporting whether it did
// not.
func (m *Model) failed(s saved) bool {
	switch {
	case s.err != nil:
		m.setToast(false, "Cannot change the library: %v", s.err)
	case s.problem != nil:
		m.setToast(false, "Cannot save: %v", s.problem)
	case s.finish.State != engine.StateOK:
		m.setToast(false, "The save did not finish: see %d Runs", int(TabRuns)+1)
	default:
		return false
	}
	return true
}

// saveOne saves tags to the indexed file with the given ID as one run, off
// the UI goroutine; done turns how it went into the message to send.
func (m *Model) saveOne(id int64, save engine.TagSave, done func(saved) tea.Msg) tea.Cmd {
	run := m.tagSaver([]int64{id})
	ctx, ended := m.begin()
	return func() tea.Msg {
		defer close(ended)
		res := saved{id: id}
		var warned warnings
		// The run reports from one goroutine at a time and has ended before
		// res is read.
		sink := func(ev engine.Event) {
			warned.sink(ev)
			switch ev := ev.(type) {
			case engine.FilePlaced:
				if ev.Outcome == engine.Organized {
					res.moved = ev.Destination
				}
			case engine.FileError:
				if res.problem == nil {
					res.problem = ev.Err
				}
			}
		}
		res.finish, res.err = run(ctx, save, sink)
		res.warnings = warned.text()
		return done(res)
	}
}

// editorDoneMsg says a save from the editor has ended.
type editorDoneMsg struct{ saved }

// The fields the editor's text inputs edit, in form order.
var editorFields = []tagwrite.Field{
	tagwrite.Title, tagwrite.Artist, tagwrite.AlbumArtist, tagwrite.Album, tagwrite.Date, tagwrite.Track, tagwrite.Disc,
	tagwrite.Genre, tagwrite.Composer, tagwrite.Comment,
}

// openEditor opens the tag editor on f and reads the file's tags off the UI
// goroutine.
func (m *Model) openEditor(f store.File) tea.Cmd {
	if !tagwrite.Supported(f.Path) {
		m.setToast(false, "tagshelf cannot tag %s files", strings.TrimPrefix(strings.ToLower(filepath.Ext(f.Path)), "."))
		return nil
	}
	m.edit = &editor{file: f, from: m.tab}
	src := m.src
	tags := func() tea.Msg {
		v, err := src.Tags(f.Path)
		return editorTagsMsg{id: f.ID, values: v, err: err}
	}
	// A picture that cannot be read is one the editor does not show.
	picture := func() tea.Msg {
		img, _ := src.Picture(f.Path)
		return editorPicMsg{id: f.ID, img: img}
	}
	return tea.Batch(tags, picture)
}

// editorLoaded puts the tags read from the file into a form.
func (m *Model) editorLoaded(msg editorTagsMsg) tea.Cmd {
	e := m.edit
	if e == nil || e.file.ID != msg.id {
		return nil
	}
	if msg.again {
		if msg.err == nil && msg.values != nil {
			e.orig = msg.values
			e.rebase()
		}
		return nil
	}
	if e.orig != nil {
		return nil
	}
	if msg.err != nil {
		m.edit = nil
		m.setToast(false, "Cannot read the tags: %v", msg.err)
		return nil
	}
	e.orig = msg.values
	if e.orig == nil {
		e.orig = tagwrite.Values{}
	}
	e.rebase()
	for i, p := range e.inputs() {
		*p = e.base[i]
	}
	e.compilation = firstValue(e.orig[tagwrite.Compilation]) == "1"
	e.makeRows()
	return m.buildForm(0)
}

// closeSet is the form's usual look with its fields a line apart, not two:
// the editor has more of them than a short terminal has lines to spare.
var closeSet = huh.ThemeFunc(func(dark bool) *huh.Styles {
	styles := huh.ThemeCharm(dark)
	styles.FieldSeparator = lipgloss.NewStyle().SetString("\n")
	return styles
})

// inputs are the form's text values, in editorFields order.
func (e *editor) inputs() []*string {
	return []*string{&e.title, &e.artist, &e.albumArtist, &e.album, &e.date, &e.track, &e.disc,
		&e.genre, &e.composer, &e.comment}
}

// shown is a field's values as its input holds them: a list as one text,
// its values joined.
func shown(field tagwrite.Field, vs []string) string {
	if tagwrite.MultiValued(field) {
		return strings.Join(vs, store.ListSeparator)
	}
	return firstValue(vs)
}

// listed splits what was typed for a list at its semicolons.
func listed(s string) []string {
	values := []string{}
	for _, v := range strings.Split(s, ";") {
		if v = strings.TrimSpace(v); v != "" {
			values = append(values, v)
		}
	}
	return values
}

func firstValue(vs []string) string {
	if len(vs) == 0 {
		return ""
	}
	return vs[0]
}

// typed is a field's value as a save would take it.
func typed(field tagwrite.Field, s string) string {
	s = strings.TrimSpace(s)
	if field == tagwrite.Track || field == tagwrite.Disc {
		s = strings.ReplaceAll(s, " ", "")
	}
	if tagwrite.MultiValued(field) {
		s = strings.Join(listed(s), store.ListSeparator)
	}
	return s
}

// check reports what is wrong with s as the value of field. A value the file
// already has is never wrong: it is not written.
func (e *editor) check(field tagwrite.Field, s string) error {
	if s = typed(field, s); s == "" || e.asRead(field, s) || tagwrite.MultiValued(field) {
		return nil
	}
	return tagwrite.Check(field, s)
}

// editorHeight is how many lines the form may take: all it needs, or what a
// short terminal leaves after the lines around it, within which it scrolls.
func (m *Model) editorHeight() int {
	// Around it: the path and a blank line above; a blank line, the marker,
	// the place, a blank line and what the editor is doing below.
	around := 7
	fields := len(editorFields) + 1
	if m.edit != nil {
		fields = m.edit.formLines()
		if len(m.edit.orig[tagwrite.NotText]) > 0 {
			around++ // a line more below the form
		}
	}
	return max(min(m.bodyHeight()-around, fields), 3)
}

// editorWidth is how wide a flow's form is: the editor's leaves room for the
// picture beside it.
func (m *Model) editorWidth() int {
	w := m.width - 4
	if cols, _, _, _, ok := m.picBox(); ok {
		w -= cols + picGap
	}
	return max(min(w, 100), 30)
}

// changes are the fields whose values differ from the file's, as a save
// takes them: a field left as it was is not written at all.
func (e *editor) changes() tagwrite.Values {
	v := tagwrite.Values{}
	for i, p := range e.inputs() {
		field := editorFields[i]
		now := typed(field, *p)
		if e.asRead(field, now) {
			continue
		}
		v[field] = []string{now}
		if tagwrite.MultiValued(field) {
			v[field] = listed(now)
		}
	}
	maps.Copy(v, e.rowChanges())
	if tagwrite.HasCompilation(e.file.Path) && e.compilation != (firstValue(e.orig[tagwrite.Compilation]) == "1") {
		v[tagwrite.Compilation] = []string{}
		if e.compilation {
			v[tagwrite.Compilation] = []string{"1"}
		}
	}
	return v
}

// invalid is what is wrong with a field as typed, nil when nothing is.
func (e *editor) invalid() error {
	for i, p := range e.inputs() {
		if err := e.check(editorFields[i], *p); err != nil {
			return err
		}
	}
	for _, r := range e.rows {
		if err := e.checkRow(r); err != nil {
			return err
		}
	}
	return nil
}

// tags are the tags the file would have once saved, as its place in the
// library is worked out from.
func (e *editor) tags() rules.Tags {
	t := rules.Tags{
		Artist: strings.TrimSpace(e.artist), AlbumArtist: strings.TrimSpace(e.albumArtist),
		Album: strings.TrimSpace(e.album), Title: strings.TrimSpace(e.title),
		VariousArtistsID: rules.HasVariousArtistsID(e.orig[tagwrite.AlbumArtistIDs]),
	}
	if d := strings.TrimSpace(e.date); len(d) >= 4 {
		t.Year, _ = strconv.Atoi(d[:4])
	}
	t.Track, _ = numberOf(e.track)
	t.Disc, t.DiscTotal = numberOf(e.disc)
	return t
}

// numberOf reads N and M from "N" or "N/M", zero for what is missing.
func numberOf(s string) (n, total int) {
	a, b, _ := strings.Cut(s, "/")
	n, _ = strconv.Atoi(strings.TrimSpace(a))
	total, _ = strconv.Atoi(strings.TrimSpace(b))
	return n, total
}

// willGoTo says where a save would put the file, or why it would stay;
// lacking is set when it would stay for want of tags.
func (m *Model) willGoTo(e *editor) (line string, lacking bool) {
	if outside(e.file) {
		return staysLine, false
	}
	if !e.qualifiesOnceSaved() {
		return "Will stay " + whereItIs(e.file) + ": it is not marked as tagged by hand, and this save would not mark it", true
	}
	return m.placeLine(e.file, e.tags())
}

// qualifiesOnceSaved reports whether the file would be one the library takes
// once the form is saved, given the tags to file it under: it has MusicBrainz
// IDs or the marker already, or the save would mark it.
func (e *editor) qualifiesOnceSaved() bool {
	changes := e.changes()
	after := func(field tagwrite.Field) []string {
		if vs, ok := changes[field]; ok {
			return vs
		}
		return e.orig[field]
	}
	for _, field := range []tagwrite.Field{tagwrite.RecordingID, tagwrite.ReleaseTrackID, tagwrite.AlbumID} {
		if len(after(field)) > 0 {
			return true
		}
	}
	// A marker set by hand is the marker the file will have.
	if set, ok := changes[tagwrite.SetMarker]; ok {
		return len(set) > 0
	}
	return firstValue(e.orig[tagwrite.Marker]) != "" || marks(changes)
}

// marks reports whether a save of values from the editor marks the file as
// tagged by hand. A change to a tag that decides where the file belongs does,
// and so does a save of the form as it stands, which vouches for the tags as
// they are. A change to the genre, the composer or the comment alone does
// not: it says nothing of whether the other tags are right.
func marks(values tagwrite.Values) bool {
	return len(values) == 0 || tagwrite.Places(values)
}

// whereItIs names the place of a file that stays where it is.
func whereItIs(f store.File) string {
	if f.RootKind == store.RootUntagged {
		return "in _Untagged"
	}
	return "where it is"
}

// placeLine says where the file f would go with the tags t, or why it would
// stay; lacking is set when it would stay for want of tags.
func (m *Model) placeLine(f store.File, t rules.Tags) (line string, lacking bool) {
	dst, ok := rules.LibraryPathFor(m.header.Destination, m.compilations, t, filepath.Ext(f.Path))
	switch {
	case ok && rules.PathKey(dst) == rules.PathKey(f.Path):
		return "Will stay at:  " + m.shortPath(dst), false
	case ok:
		return "Will go to:    " + m.shortPath(dst), false
	}
	needs := missingTags(store.File{Artist: t.Artist, AlbumArtist: t.AlbumArtist, Album: t.Album, Title: t.Title})
	what := "an artist, album and title it can be filed under"
	if len(needs) > 0 {
		what = strings.Join(needs, ", ")
	}
	return fmt.Sprintf("Will stay %s: needs %s", whereItIs(f), what), true
}

// editorKey handles a key while the editor is open.
func (m *Model) editorKey(msg tea.KeyPressMsg) tea.Cmd {
	e := m.edit
	k := msg.String()
	m.toast, m.readErr = "", false
	if k == "ctrl+c" {
		return m.key(k)
	}
	if e.saving || e.counting {
		return nil
	}
	if e.naming != nil {
		return m.namingKey(msg)
	}
	if ask := e.folder; ask != nil {
		e.folder = nil
		if k == "y" {
			return m.writeToFolder(ask)
		}
		return nil
	}
	leaving := e.leaving
	e.leaving = false
	switch k {
	case "ctrl+f":
		return m.askFolder()
	case "ctrl+n":
		m.nameTag()
		return nil
	case "esc":
		if e.form != nil && len(e.changes()) > 0 && !leaving {
			e.leaving = true
			return nil
		}
		m.edit = nil
		return nil
	case "ctrl+s":
		return m.saveEditor()
	}
	if e.form == nil {
		return nil
	}
	// The form never gets the key that would complete it: a completed form
	// cannot be edited again, and a save may be refused.
	if e.form.GetFocusedField() == e.last {
		switch k {
		case "enter":
			return m.saveEditor()
		case "tab", "down":
			return nil
		}
	}
	return m.formUpdate(msg)
}

// formUpdate hands the editor's form a message.
func (m *Model) formUpdate(msg tea.Msg) tea.Cmd {
	_, cmd := m.edit.form.Update(msg)
	return cmd
}

// editSave is the tag save of the editor: the changed fields, with the manual
// marker, saying a person typed the tags, when the save marks the file.
func editSave(values tagwrite.Values, now time.Time) engine.TagSave {
	// A marker set by hand goes in as it was typed: the save adds none.
	if _, set := values[tagwrite.SetMarker]; !set && marks(values) {
		return tagSave(values, marker.Hand, now)
	}
	return func(st *store.Store, runID int64, f store.File) error {
		_, err := tagwrite.Writer{History: st, RunID: runID}.SaveUnmarked(f.Path, values)
		return err
	}
}

// tagSave is a tag save of values with the manual marker of the given
// method.
func tagSave(values tagwrite.Values, method marker.Method, now time.Time) engine.TagSave {
	mark := marker.New(method, now)
	return func(st *store.Store, runID int64, f store.File) error {
		_, err := tagwrite.Writer{History: st, RunID: runID}.Save(f.Path, values, mark)
		return err
	}
}

// saveEditor saves what the form holds off the UI goroutine.
func (m *Model) saveEditor() tea.Cmd {
	e := m.edit
	if e.form == nil {
		return nil
	}
	if err := e.invalid(); err != nil {
		m.setToast(false, "Cannot save: %v", err)
		return nil
	}
	if !m.canChange() {
		return nil
	}
	e.saving = true
	return m.saveOne(e.file.ID, editSave(e.changes(), time.Now()), func(s saved) tea.Msg { return editorDoneMsg{s} })
}

// editorDone says how a save went: the editor closes on a save that worked
// and stays open, with what went wrong, on one that did not.
func (m *Model) editorDone(msg editorDoneMsg) tea.Cmd {
	m.ended()
	e := m.edit
	if e != nil {
		e.saving = false
	}
	reload := m.poll(true, false)
	switch {
	case m.failed(msg.saved):
		// The tags may have been written before it went wrong: what the form
		// is compared with is read again.
		if e != nil && e.file.ID == msg.id && msg.err == nil {
			src, path := m.src, e.file.Path
			if msg.moved != "" {
				path = msg.moved
				e.file.Path = path
			}
			return tea.Batch(reload, func() tea.Msg {
				v, err := src.Tags(path)
				return editorTagsMsg{id: msg.id, values: v, err: err, again: true}
			})
		}
	default:
		var where string
		var lacking bool
		if e != nil && e.file.ID == msg.id {
			where, lacking = m.willGoTo(e)
			m.edit = nil
		}
		m.savedToast(msg.saved, where, lacking)
	}
	return reload
}

// savedToast says a save worked and where it left the file; where and
// lacking are what placeLine said of it.
func (m *Model) savedToast(s saved, where string, lacking bool) {
	switch {
	case s.moved != "":
		m.setToast(true, "Saved and moved to %s", m.shortPath(s.moved))
	case lacking:
		m.setToast(true, "Saved. %s", strings.Replace(where, "Will stay", "It stays", 1))
	default:
		m.setToast(true, "Saved")
	}
	if s.warnings != "" {
		m.setToast(true, "%s; %s", m.toast, s.warnings)
	}
	m.offerUndo(s.finish.RunID)
}

// editorCrumb is the breadcrumb shown in place of the tabs.
func (m *Model) editorCrumb() string {
	e := m.edit
	return " " + dimStyle.Render(e.from.String()+" › ") + boldStyle.Render("Edit tags") + dimStyle.Render(" › ") + clean(filepath.Base(e.file.Path))
}

// editorBody draws the editor.
func (m *Model) editorBody() string {
	e := m.edit
	var b strings.Builder
	b.WriteString(dimStyle.Render("  "+m.shortPath(e.file.Path)) + "\n\n")
	if e.form == nil {
		b.WriteString(dimStyle.Render("Reading the tags…"))
		return b.String()
	}
	b.WriteString(strings.TrimRight(e.form.View(), "\n") + "\n\n")

	if names := e.orig[tagwrite.NotText]; len(names) > 0 {
		b.WriteString("  " + dimStyle.Render("Not text:      "+clean(strings.Join(names, ", "))+", kept as it is") + "\n")
	}
	b.WriteString("  " + dimStyle.Render("Saving marks it tagged by hand; genre, composer, comment and other tags don't") + "\n")
	line, lacking := m.willGoTo(e)
	if lacking {
		line = accentStyle.Render(line)
	}
	b.WriteString("  " + line + "\n")
	switch {
	case e.saving:
		b.WriteString("\n  " + dimStyle.Render("Saving…") + "\n")
	case e.counting:
		b.WriteString("\n  " + dimStyle.Render("Reading the folder…") + "\n")
	case e.naming != nil:
		b.WriteString("\n  " + accentStyle.Render("Name of the new tag: ") + e.naming.view(30) + dimStyle.Render("   enter adds it, esc does not") + "\n")
	case e.folder != nil:
		b.WriteString("\n  " + accentStyle.Render(wrap(e.folder.question(), max(m.width-4, 20))) + "\n")
	case e.leaving:
		b.WriteString("\n  " + accentStyle.Render("Unsaved changes: esc again discards them, ctrl+s saves") + "\n")
	}
	return m.withPicture(b.String())
}
