package tui

import (
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"charm.land/bubbles/v2/key"
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
	// longComment is set for a comment of several lines, which the form's
	// one line would run together: it is shown, and left as it is.
	longComment bool

	last    huh.Field // the form's last field, where enter saves
	saving  bool      // a save is being carried out
	leaving bool      // esc was pressed with changes unsaved; again discards them
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
	runner := m.runner
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
		res.finish, res.err = runner.SaveTags(ctx, []int64{id}, save, sink)
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
	return func() tea.Msg {
		v, err := src.Tags(f.Path)
		return editorTagsMsg{id: f.ID, values: v, err: err}
	}
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
	for i, p := range e.inputs() {
		*p = shown(editorFields[i], e.orig[editorFields[i]])
	}
	e.longComment = strings.ContainsAny(e.comment, "\r\n")
	e.compilation = firstValue(e.orig[tagwrite.Compilation]) == "1"

	keys := huh.NewDefaultKeyMap()
	keys.Quit.SetEnabled(false) // ctrl+c is the TUI's
	keys.Input.Prev = key.NewBinding(key.WithKeys("shift+tab", "up"))
	keys.Input.Next = key.NewBinding(key.WithKeys("enter", "tab", "down"))
	keys.Confirm.Prev = key.NewBinding(key.WithKeys("shift+tab", "up"))
	// A y or n typed on the flag sets nothing: only the arrows and space do.
	keys.Confirm.Toggle = key.NewBinding(key.WithKeys("left", "right", "space"))
	keys.Confirm.Accept.SetEnabled(false)
	keys.Confirm.Reject.SetEnabled(false)
	input := func(title string, field tagwrite.Field, value *string) *huh.Input {
		return huh.NewInput().Title(fmt.Sprintf("%-13s", title)).Value(value).Inline(true).
			Validate(func(s string) error { return e.check(field, s) })
	}
	fields := []huh.Field{
		input("Title", tagwrite.Title, &e.title),
		input("Artist", tagwrite.Artist, &e.artist),
		input("Album artist", tagwrite.AlbumArtist, &e.albumArtist),
		input("Album", tagwrite.Album, &e.album),
		input("Year", tagwrite.Date, &e.date).Placeholder("YYYY or YYYY-MM-DD"),
		input("Track", tagwrite.Track, &e.track).Placeholder("N or N/total"),
		input("Disc", tagwrite.Disc, &e.disc).Placeholder("N or N/total"),
		input("Genre", tagwrite.Genre, &e.genre).Placeholder("several are written Rock; Jazz"),
		input("Composer", tagwrite.Composer, &e.composer),
	}
	if !e.longComment {
		fields = append(fields, input("Comment", tagwrite.Comment, &e.comment))
	}
	if tagwrite.HasCompilation(e.file.Path) {
		fields = append(fields, huh.NewConfirm().Title("Compilation").Value(&e.compilation).Inline(true))
	}
	e.last = fields[len(fields)-1]
	e.form = huh.NewForm(huh.NewGroup(fields...)).WithTheme(closeSet).
		WithKeyMap(keys).WithShowHelp(false).WithWidth(m.editorWidth()).WithHeight(m.editorHeight())
	return e.form.Init()
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
	if s = typed(field, s); s == "" || s == shown(field, e.orig[field]) || tagwrite.MultiValued(field) {
		return nil
	}
	return tagwrite.Check(field, s)
}

// editorHeight is how many lines the form may take: all it needs, or what a
// short terminal leaves after the lines around it, within which it scrolls.
func (m *Model) editorHeight() int {
	// Around it: the path and a blank line above; a blank line, the marker,
	// the place, a blank line and what the editor is doing below.
	const fields = 11
	around := 7
	if m.edit != nil && m.edit.longComment {
		around++
	}
	return max(min(m.bodyHeight()-around, fields), 3)
}

func (m *Model) editorWidth() int {
	return max(min(m.width-4, 100), 30)
}

// changes are the fields whose values differ from the file's, as a save
// takes them: a field left as it was is not written at all.
func (e *editor) changes() tagwrite.Values {
	v := tagwrite.Values{}
	for i, p := range e.inputs() {
		field := editorFields[i]
		if field == tagwrite.Comment && e.longComment {
			continue
		}
		now := typed(field, *p)
		if now == shown(field, e.orig[field]) {
			continue
		}
		v[field] = []string{now}
		if tagwrite.MultiValued(field) {
			v[field] = listed(now)
		}
	}
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
	if !e.qualifiesOnceSaved() {
		return "Will stay " + whereItIs(e.file) + ": it is not marked as tagged by hand, and its genre, composer or comment does not make it so", true
	}
	return m.placeLine(e.file, e.tags())
}

// qualifiesOnceSaved reports whether the file would be one the library takes
// once the form is saved, given the tags to file it under: it has MusicBrainz
// IDs or the marker already, or the save would mark it.
func (e *editor) qualifiesOnceSaved() bool {
	for _, field := range []tagwrite.Field{tagwrite.Marker, tagwrite.RecordingID, tagwrite.ReleaseTrackID, tagwrite.AlbumID} {
		if firstValue(e.orig[field]) != "" {
			return true
		}
	}
	return marks(e.changes())
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
	if e.saving {
		return nil
	}
	leaving := e.leaving
	e.leaving = false
	switch k {
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
	if marks(values) {
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

	if e.longComment {
		first, _, _ := strings.Cut(strings.ReplaceAll(e.comment, "\r", "\n"), "\n")
		lines := len(strings.Split(strings.ReplaceAll(strings.ReplaceAll(e.comment, "\r\n", "\n"), "\r", "\n"), "\n"))
		b.WriteString("  " + dimStyle.Render("Comment:       ") + clean(first) + dimStyle.Render(fmt.Sprintf(" … (%d lines; left as it is)", lines)) + "\n")
	}
	const unless = ", but not for genre, composer or comment alone"
	mark := "none; saving marks it as tagged by hand" + unless
	if cur := firstValue(e.orig[tagwrite.Marker]); cur != "" {
		mark = clean(cur)
		if parsed, ok := marker.Parse(cur); ok && parsed.Method != marker.Hand {
			mark += "; saving a change marks it as tagged by hand" + unless
		}
	}
	b.WriteString("  " + dimStyle.Render("Marker:        ") + mark + "\n")
	line, lacking := m.willGoTo(e)
	if lacking {
		line = accentStyle.Render(line)
	}
	b.WriteString("  " + line + "\n")
	switch {
	case e.saving:
		b.WriteString("\n  " + dimStyle.Render("Saving…") + "\n")
	case e.leaving:
		b.WriteString("\n  " + accentStyle.Render("Unsaved changes: esc again discards them, ctrl+s saves") + "\n")
	}
	return b.String()
}
