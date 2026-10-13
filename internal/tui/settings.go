package tui

import (
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/zachjamesgreen/tagshelf/internal/config"
)

// Settings is the config file the Settings tab edits. Its methods are called
// off the UI goroutine, one at a time.
type Settings interface {
	// Load reads the config file as it is now.
	Load() SettingsFile
	// Check looks at settings about to be saved, changing nothing.
	Check(c config.Config, read SettingsFile) SettingsCheck
	// Save writes the settings to the file that was read. It returns
	// ErrSettingsChanged, and writes nothing, when the file is no longer as
	// read. note is something to tell the user about what was saved.
	Save(c config.Config, read SettingsFile) (note string, err error)
}

// ErrSettingsChanged is the config file having changed on disk since the
// Settings tab read it.
var ErrSettingsChanged = errors.New("the config file changed on disk since it was read")

// SettingsFile is the config file as read for the Settings tab.
type SettingsFile struct {
	// Path is the file. New reports that it does not exist yet: saving
	// makes it.
	Path string
	New  bool
	// Config is what the file sets, or what a new file would start with.
	Config config.Config
	// Problem is why the file cannot be edited here, empty when it can.
	Problem string
	// Session holds, by key, the settings a flag gives this session in place
	// of the file's, as they are shown.
	Session map[string]string
	// Text is the file as read, which Save compares with the file as it is.
	Text []byte
	// DataFolder is the folder a library's database is kept under when the
	// database setting is empty, as it is shown.
	DataFolder string
	// Problems are the settings of the file that cannot be used as they are
	// written, by key, with why. They are shown before anything is changed.
	Problems map[string]string
}

// SettingsCheck is what Settings.Check found.
type SettingsCheck struct {
	// Problems are the settings that cannot be saved, by key, with why.
	Problems map[string]string
	// Confirm are the questions to put before saving, each answered yes.
	Confirm []string
}

type settingKind int

const (
	kindFolder settingKind = iota
	kindFile
	kindText
	kindNumber
	kindToggle
)

// settingRow is one setting on the Settings tab.
type settingRow struct {
	key, label string
	kind       settingKind
	unset      string // what an empty value means
	about      string
}

// settingRows are the settings in the order the tab lists them.
var settingRows = []settingRow{
	{"source", "Source folder", kindFolder, "not set", "The folder of music that o on Home organizes into the library."},
	{"destination", "Library folder", kindFolder, "not set", "The library: the folder the music is organized into."},
	{"untagged", "Untagged folder", kindFolder, "_Untagged in the library folder", "Where audio files without usable tags go."},
	{"other", "Other folder", kindFolder, "_Other in the library folder", "Where files that are not audio, and belong to no tagged album, go."},
	{"duplicates", "Duplicates folder", kindFolder, "_Duplicates in the library folder", "Where the copies set aside as duplicates go. tagshelf never deletes them."},
	{"compilations", "Compilations name", kindText, "Various Artists", "The artist folder that Various Artists albums are filed under."},
	{"auto_resolve_exact", "Settle exact copies", kindToggle, "", "On: a run keeps the best of identical copies it brings in and sets the rest aside. Off: they wait on the Duplicates tab."},
	{"dry_run", "Dry run", kindToggle, "", "On: runs show what they would do and move nothing. d on Home changes it for this session alone."},
	{"cue_rewrite", "Fix cue sheets", kindToggle, "", "On: the FILE lines of cue sheets that move are fixed, and the original kept beside them."},
	{"workers", "Files at once", kindNumber, "", "How many files a run works on at once. 0 means one for each CPU."},
	{"verbose", "Verbose", kindToggle, "", "On: a run from the command line prints a line for every file."},
	{"musicbrainz_contact", "MusicBrainz contact", kindText, "not set: lookups are off", "An email address or URL MusicBrainz can reach you at. It is sent with every lookup."},
	{"genre_from_musicbrainz", "MusicBrainz genre", kindText, "fill", "What a match does with the genre: fill writes it to a file that has none, replace writes it always, off never."},
	{"cover_art_file", "Cover file", kindToggle, "", "On: an album folder without a cover gets a file named cover, from the pictures in its files or else from the Cover Art Archive."},
	{"cover_art_replace", "Replace a cover", kindText, "never", "When a cover an album has gives way to the Cover Art Archive's: never, smaller (when the archive's is larger) or always."},
	{"database", "Database file", kindFile, "kept with tagshelf's own files, by the library's ID", "The file the library's index and run history are kept in."},
}

// settingsTab is the state of the Settings tab.
type settingsTab struct {
	loaded  bool
	loading bool
	file    SettingsFile
	values  config.Config // the settings as edited
	cursor  int

	input *lineInput // the value being typed, nil when none is
	// picker is the folder picker, open for the setting under the cursor,
	// nil when it is not.
	picker *folderPicker
	// hint says what the folder being typed is on the disk; hintSeq is the
	// last request for one.
	hint    string
	hintSeq int

	// problems are the settings that cannot be used or saved, by key: the
	// file's own as it was read, then those of the last save refused.
	problems map[string]string
	// confirm are the questions still to answer before the save asked for
	// goes ahead, and checked the settings they were asked about.
	confirm []string
	checked config.Config
	saving  bool
	// leave is the key that would leave the tab with unsaved changes, kept
	// while the user is asked about them.
	leave string
}

type settingsLoadedMsg struct{ file SettingsFile }

type settingsCheckedMsg struct {
	values config.Config
	check  SettingsCheck
}

type settingsSavedMsg struct {
	values config.Config
	note   string
	err    error
}

type settingsHintMsg struct {
	seq  int
	text string
}

// dirty reports whether the tab holds changes that are not saved. A new
// file's settings are unsaved until it is made, but only what was typed here
// is a change to ask about.
func (s *settingsTab) dirty() bool {
	return s.loaded && len(s.values.Changed(s.file.Config)) > 0
}

func (s *settingsTab) row() settingRow { return settingRows[s.cursor] }

// settingsShown reads the config file when the Settings tab shows and has
// not read it.
func (m *Model) settingsShown() tea.Cmd {
	if m.tab != TabSettings || m.settings.loaded || m.settings.loading {
		return nil
	}
	return m.settingsLoad()
}

// settingsLoad reads the config file off the UI goroutine.
func (m *Model) settingsLoad() tea.Cmd {
	src := m.settingsSrc
	if src == nil {
		return nil
	}
	m.settings.loading = true
	return func() tea.Msg { return settingsLoadedMsg{file: src.Load()} }
}

func (m *Model) settingsLoaded(msg settingsLoadedMsg) {
	s := &m.settings
	s.loaded, s.loading = true, false
	s.file, s.values = msg.file, msg.file.Config
	s.input, s.hint, s.problems, s.confirm, s.leave = nil, "", maps.Clone(msg.file.Problems), nil, ""
	s.picker = nil
}

// settingsTo puts the cursor on the setting with the given key.
func (m *Model) settingsTo(key string) {
	for i, r := range settingRows {
		if r.key == key {
			m.settings.cursor = i
		}
	}
}

// settingsKey handles a key on the Settings tab while no value is being
// typed. ok is false for a key the tab leaves to the rest of the TUI.
func (m *Model) settingsKey(k string) (cmd tea.Cmd, ok bool) {
	s := &m.settings
	if s.leave != "" {
		leave := s.leave
		s.leave = ""
		if k == "y" || k == "Y" {
			s.values, s.problems = s.file.Config, maps.Clone(s.file.Problems)
			return m.key(leave), true
		}
		return nil, true
	}
	if len(s.confirm) > 0 {
		if k != "y" && k != "Y" {
			s.confirm = nil
			m.setToast(false, "Not saved")
			return nil, true
		}
		s.confirm = s.confirm[1:]
		if len(s.confirm) > 0 {
			return nil, true
		}
		return m.settingsSave(s.checked), true
	}
	if s.saving {
		return nil, true
	}
	// While the file is being read again there is nothing sound to change.
	editable := s.loaded && !s.loading && s.file.Problem == ""
	switch k {
	case "1", "2", "3", "4", "5", "tab", "shift+tab", "q":
		if s.dirty() {
			s.leave = k
			return nil, true
		}
		return nil, false
	case "R":
		if s.dirty() {
			m.setToast(false, "There are unsaved changes: s saves them, esc drops them")
			return nil, true
		}
		// The library is opened again too: the file may have been put
		// right, or changed, in an editor.
		if m.run != nil || m.busy != nil {
			return m.settingsLoad(), true
		}
		m.reopening = m.reopen != nil
		return tea.Sequence(m.settingsLoad(), m.openAgain()), true
	}
	if !editable {
		return nil, false
	}
	switch k {
	case "up", "k":
		s.cursor = max(s.cursor-1, 0)
	case "down", "j":
		s.cursor = min(s.cursor+1, len(settingRows)-1)
	case "home", "g":
		s.cursor = 0
	case "end", "G":
		s.cursor = len(settingRows) - 1
	case "enter", "space":
		return m.settingsChange(), true
	case "b":
		if s.row().kind != kindFolder {
			m.setToast(false, "Only a folder can be picked: enter changes this setting")
			return nil, true
		}
		value, _ := s.values.Values()[s.row().key].(string)
		return m.openPicker(value), true
	case "esc":
		if s.dirty() {
			s.values, s.problems = s.file.Config, maps.Clone(s.file.Problems)
			m.setToast(true, "Changes dropped")
		}
	case "s":
		return m.settingsAskSave(), true
	default:
		return nil, false
	}
	return nil, true
}

// settingsChange turns the setting under the cursor on or off, or starts
// typing its value.
func (m *Model) settingsChange() tea.Cmd {
	s := &m.settings
	row := s.row()
	value := s.values.Values()[row.key]
	delete(s.problems, row.key)
	switch v := value.(type) {
	case bool:
		s.values, _ = s.values.Set(row.key, !v)
		return nil
	case int:
		s.input = newLineInput(strconv.Itoa(v))
	case string:
		s.input = newLineInput(v)
	}
	return m.settingsHint()
}

// settingsType handles a key while a value is being typed.
func (m *Model) settingsType(msg tea.KeyPressMsg) tea.Cmd {
	s := &m.settings
	m.toast, m.readErr = "", false
	row := s.row()
	switch msg.String() {
	case "esc":
		s.input, s.hint = nil, ""
		return nil
	case "enter":
		text := strings.TrimSpace(s.input.String())
		var value any = text
		if row.kind == kindNumber {
			n, err := strconv.Atoi(text)
			if text == "" {
				n, err = 0, nil
			}
			if err != nil {
				m.setToast(false, "%s takes a whole number", row.label)
				return nil
			}
			value = n
		}
		if row.kind == kindFolder || row.kind == kindFile {
			// A path pasted from Explorer comes in quotes.
			text = strings.Trim(text, `"`)
			if text != "" {
				text = filepath.Clean(text)
			}
			value = text
		}
		s.values, _ = s.values.Set(row.key, value)
		s.input, s.hint = nil, ""
		return nil
	case "tab":
		if row.kind == kindFolder || row.kind == kindFile {
			s.input.set(completePath(s.input.String(), row.kind == kindFolder))
		}
	case "ctrl+o":
		// From typing to picking, at the folder typed so far.
		if row.kind == kindFolder {
			return m.openPicker(strings.Trim(strings.TrimSpace(s.input.String()), `"`))
		}
	default:
		s.input.key(msg)
	}
	return m.settingsHint()
}

// settingsPaste types pasted text into the value being typed.
func (m *Model) settingsPaste(text string) tea.Cmd {
	s := &m.settings
	if m.tab != TabSettings || s.input == nil {
		return nil
	}
	s.input.insert(strings.Join(strings.Fields(text), " "))
	return m.settingsHint()
}

// settingsHint looks at the folder being typed off the UI goroutine, which a
// drive that is slow to answer would otherwise hold up.
func (m *Model) settingsHint() tea.Cmd {
	s := &m.settings
	if s.input == nil || (s.row().kind != kindFolder && s.row().kind != kindFile) {
		s.hint = ""
		return nil
	}
	s.hintSeq++
	seq, path, folder := s.hintSeq, strings.Trim(strings.TrimSpace(s.input.String()), `"`), s.row().kind == kindFolder
	return func() tea.Msg { return settingsHintMsg{seq: seq, text: pathHint(path, folder)} }
}

// pathHint says what path is on the disk, for a setting that names a folder
// or else a file.
func pathHint(path string, folder bool) string {
	if path == "" {
		return ""
	}
	if config.ForeignPath(path) {
		return "This is a Windows path, which this system cannot use."
	}
	path, err := config.ExpandHome(path)
	if errors.Is(err, config.ErrOtherHome) {
		return "Only ~ alone, your own home folder, is known."
	}
	info, err := os.Stat(path)
	switch {
	case err != nil && !errors.Is(err, os.ErrNotExist):
		return "Cannot be read: " + err.Error()
	case err != nil && folder:
		return "This folder does not exist yet."
	case err != nil:
		return "This file does not exist yet."
	case folder && !info.IsDir():
		return "This is a file, not a folder."
	case !folder && info.IsDir():
		return "This is a folder, not a file."
	case folder:
		return "This folder exists."
	}
	return "This file exists."
}

// completePath completes the last part of a path being typed to the folder
// it begins, or as far as the folders it could be have in common. Files are
// offered too unless only folders will do.
func completePath(typed string, foldersOnly bool) string {
	typed = strings.TrimLeft(typed, `"`)
	cut := lastSeparator(typed)
	if cut < 0 {
		return typed
	}
	separator := string(filepath.Separator)
	dir, prefix := typed[:cut+1], typed[cut+1:]
	// What is typed stays as typed: only the look-up puts ~ in full.
	lookIn, err := config.ExpandHome(dir)
	if err != nil {
		return typed
	}
	entries, err := os.ReadDir(lookIn)
	if err != nil {
		return typed
	}
	var matches []string
	for _, e := range entries {
		if foldersOnly && !e.IsDir() {
			continue
		}
		if len(e.Name()) >= len(prefix) && strings.EqualFold(e.Name()[:len(prefix)], prefix) {
			name := e.Name()
			if e.IsDir() {
				name += separator
			}
			matches = append(matches, name)
		}
	}
	switch len(matches) {
	case 0:
		return typed
	case 1:
		return dir + matches[0]
	}
	common := []rune(strings.TrimSuffix(matches[0], separator))
	for _, m := range matches[1:] {
		other := []rune(m)
		n := 0
		for n < len(common) && n < len(other) && strings.EqualFold(string(common[n]), string(other[n])) {
			n++
		}
		common = common[:n]
	}
	if len(common) <= len([]rune(prefix)) {
		return typed
	}
	return dir + string(common)
}

// lastSeparator is where the last path separator of path is, or -1. A
// backslash is one on Windows only: elsewhere it is a character of a name.
func lastSeparator(path string) int {
	for i := len(path) - 1; i >= 0; i-- {
		if os.IsPathSeparator(path[i]) {
			return i
		}
	}
	return -1
}

// settingsAskSave starts saving: the settings are checked first, off the UI
// goroutine.
func (m *Model) settingsAskSave() tea.Cmd {
	s := &m.settings
	switch {
	case m.run != nil || m.busy != nil:
		m.setToast(false, "Settings cannot be saved while a run or an action is going")
		return nil
	case !s.dirty() && !s.file.New:
		m.setToast(true, "Nothing to save")
		return nil
	}
	src, values, read := m.settingsSrc, s.values, s.file
	s.saving = true
	return func() tea.Msg { return settingsCheckedMsg{values: values, check: src.Check(values, read)} }
}

func (m *Model) settingsChecked(msg settingsCheckedMsg) tea.Cmd {
	s := &m.settings
	s.saving = false
	s.problems = msg.check.Problems
	if n := len(s.problems); n > 0 {
		for i, r := range settingRows {
			if _, bad := s.problems[r.key]; bad {
				s.cursor = i
				break
			}
		}
		what := "1 setting needs"
		if n > 1 {
			what = fmt.Sprintf("%d settings need", n)
		}
		m.setToast(false, "Not saved: %s changing", what)
		return nil
	}
	if len(msg.check.Confirm) > 0 {
		s.confirm, s.checked = msg.check.Confirm, msg.values
		return nil
	}
	return m.settingsSave(msg.values)
}

// settingsSave writes the settings off the UI goroutine.
func (m *Model) settingsSave(values config.Config) tea.Cmd {
	s := &m.settings
	src, read := m.settingsSrc, s.file
	s.saving = true
	return func() tea.Msg {
		note, err := src.Save(values, read)
		return settingsSavedMsg{values: values, note: note, err: err}
	}
}

func (m *Model) settingsSaved(msg settingsSavedMsg) tea.Cmd {
	s := &m.settings
	s.saving = false
	switch {
	case errors.Is(msg.err, ErrSettingsChanged):
		m.setToast(false, "Not saved: the config file changed on disk since it was read. esc drops your changes, then R reads it again")
		return nil
	case msg.err != nil:
		m.setToast(false, "Not saved: %v", msg.err)
		return nil
	}
	// Dry run as saved is how the session goes on, unless a flag set it.
	if _, flagged := s.file.Session["dry_run"]; !flagged && (s.file.New || msg.values.DryRun != s.file.Config.DryRun) {
		m.header.DryRun = msg.values.DryRun
	}
	text := "Saved to " + s.file.Path
	if msg.note != "" {
		text += ". " + msg.note
	}
	m.setToast(true, "%s", text)
	// What was saved is what the tab shows until the file is read again.
	s.file.Config, s.file.New = msg.values, false
	m.reopening = m.reopen != nil
	return tea.Sequence(m.settingsLoad(), m.openAgain())
}

// settingsBody draws the Settings tab in h lines.
func (m *Model) settingsBody(h int) string {
	s := &m.settings
	switch {
	case m.settingsSrc == nil:
		return dimStyle.Render("Settings cannot be changed from here.")
	case !s.loaded:
		return dimStyle.Render("Reading the config file…")
	case s.picker != nil:
		return m.pickerBody()
	}
	w := max(m.width, 40)
	var b strings.Builder
	title := boldStyle.Render("Settings") + "  " + clean(s.file.Path)
	switch {
	case s.file.Problem != "":
	case s.file.New:
		title += dimStyle.Render("  not made yet: s makes it")
	case s.dirty():
		title += accentStyle.Render("  unsaved changes")
	}
	b.WriteString(title + "\n")
	if s.file.Problem != "" {
		b.WriteString("\n" + badStyle.Render(wrapAll(sentence(clean(s.file.Problem)), w-2)) + "\n")
		b.WriteString(wrapAll("It cannot be changed here until that is put right. Fix the file in an editor, then press R to read it again.", w-2) + "\n")
		return b.String()
	}
	b.WriteString("\n")

	values, saved := s.values.Values(), s.file.Config.Values()
	const labelWidth = 20
	valueWidth := max(w-4-labelWidth-1, 10)
	for i, r := range settingRows {
		mark := "  "
		if i == s.cursor {
			mark = accentStyle.Render("› ")
		}
		changed := " "
		if values[r.key] != saved[r.key] {
			changed = accentStyle.Render("*")
		}
		var value string
		switch {
		case i == s.cursor && s.input != nil:
			value = s.input.view(valueWidth)
		default:
			value = m.settingValue(r, values[r.key])
			if problem, bad := s.problems[r.key]; bad {
				value += "  " + badStyle.Render("✗ "+clean(problem))
			} else if session, ok := s.file.Session[r.key]; ok {
				value += dimStyle.Render("  this session: " + clean(session))
			}
		}
		fmt.Fprintf(&b, "%s%s %-*s %s\n", mark, changed, labelWidth, r.label, value)
	}

	b.WriteString("\n")
	row := s.row()
	b.WriteString(dimStyle.Render(wrapAll(row.about, w-2)) + "\n")
	switch {
	case s.input != nil && s.hint != "":
		b.WriteString(clean(s.hint) + "\n")
	case s.problems[row.key] != "":
		b.WriteString(badStyle.Render(wrapAll(row.label+" "+clean(s.problems[row.key])+".", w-2)) + "\n")
	case s.file.Session[row.key] != "":
		b.WriteString(dimStyle.Render(wrapAll("A flag sets this to "+clean(s.file.Session[row.key])+" for this session. What you change here is the file, for the next time.", w-2)) + "\n")
	}
	return b.String()
}

// settingValue shows a setting's value on its row.
func (m *Model) settingValue(r settingRow, value any) string {
	switch v := value.(type) {
	case bool:
		if v {
			return "on"
		}
		return "off"
	case int:
		if v == 0 && r.key == "workers" {
			return "0" + dimStyle.Render("  one for each CPU")
		}
		return strconv.Itoa(v)
	case string:
		if v == "" && r.key == "database" && m.settings.file.DataFolder != "" {
			return dimStyle.Render("(kept under " + clean(m.settings.file.DataFolder) + ", by the library's ID)")
		}
		if v == "" {
			return dimStyle.Render("(" + r.unset + ")")
		}
		return clean(v)
	}
	return ""
}

// settingsQuestion is what the Settings tab asks on the line above the keys,
// empty when it asks nothing.
func (m *Model) settingsQuestion() string {
	s := &m.settings
	switch {
	case m.tab != TabSettings:
		return ""
	case s.leave != "":
		return "Drop the unsaved changes to the settings? y drops them, n stays"
	case len(s.confirm) > 0:
		return clean(s.confirm[0]) + "  y yes, n no"
	}
	return ""
}

func (m *Model) settingsHelp(rest string) string {
	s := &m.settings
	switch {
	case s.picker != nil:
		return m.pickerHelp()
	case s.input != nil && s.row().kind == kindFolder:
		return "type or paste a path  tab complete  ctrl+o pick  enter done  esc cancel"
	case s.input != nil && s.row().kind == kindFile:
		return "type or paste a path  tab complete  enter done  esc cancel"
	case s.input != nil:
		return "type or paste  enter done  esc cancel"
	case !s.loaded || s.file.Problem != "":
		return rest
	}
	if s.row().kind == kindFolder {
		return "↑↓ move  enter type  b pick a folder  s save  esc drop changes   " + rest
	}
	return "↑↓ move  enter change  s save  esc drop changes   " + rest
}

// lineInput is one line of text being typed, with a cursor.
type lineInput struct {
	text []rune
	pos  int
}

func newLineInput(text string) *lineInput {
	r := []rune(text)
	return &lineInput{text: r, pos: len(r)}
}

func (l *lineInput) String() string { return string(l.text) }

func (l *lineInput) set(text string) {
	l.text = []rune(text)
	l.pos = len(l.text)
}

func (l *lineInput) insert(text string) {
	add := []rune(text)
	l.text = append(l.text[:l.pos], append(add, l.text[l.pos:]...)...)
	l.pos += len(add)
}

func (l *lineInput) key(msg tea.KeyPressMsg) {
	switch msg.String() {
	case "left":
		l.pos = max(l.pos-1, 0)
	case "right":
		l.pos = min(l.pos+1, len(l.text))
	case "home", "ctrl+a":
		l.pos = 0
	case "end", "ctrl+e":
		l.pos = len(l.text)
	case "backspace":
		if l.pos > 0 {
			l.text = append(l.text[:l.pos-1], l.text[l.pos:]...)
			l.pos--
		}
	case "delete":
		if l.pos < len(l.text) {
			l.text = append(l.text[:l.pos], l.text[l.pos+1:]...)
		}
	case "ctrl+u":
		l.text, l.pos = nil, 0
	default:
		if msg.Text != "" {
			l.insert(msg.Text)
		}
	}
}

// view draws the text in w cells with the cursor shown, scrolled so that the
// cursor is in view.
func (l *lineInput) view(w int) string {
	w = max(w, 4)
	text := append(append([]rune{}, l.text...), ' ')
	start := 0
	if l.pos >= w {
		start = l.pos - w + 1
	}
	end := min(start+w, len(text))
	var b strings.Builder
	for i := start; i < end; i++ {
		c := clean(string(text[i]))
		if i == l.pos {
			c = activeTab.Render(c)
		}
		b.WriteString(c)
	}
	return ansi.Truncate(b.String(), w, "")
}
