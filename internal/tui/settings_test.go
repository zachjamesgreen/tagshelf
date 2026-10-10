package tui

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/zachjamesgreen/tagshelf/internal/config"
	"github.com/zachjamesgreen/tagshelf/internal/engine"
)

// fakeSettings is a config file held in memory.
type fakeSettings struct {
	file  SettingsFile
	check SettingsCheck
	note  string
	err   error // what Save fails with

	loads, checks int
	saved         []config.Config
}

func (f *fakeSettings) Load() SettingsFile {
	f.loads++
	return f.file
}

func (f *fakeSettings) Check(c config.Config, read SettingsFile) SettingsCheck {
	f.checks++
	return f.check
}

func (f *fakeSettings) Save(c config.Config, read SettingsFile) (string, error) {
	if f.err != nil {
		return "", f.err
	}
	f.saved = append(f.saved, c)
	f.file.Config, f.file.New = c, false
	return f.note, nil
}

var savedSettings = config.Config{Source: p(`D:\Incoming`), Destination: p(`D:\Music`), Compilations: "Various Artists", DryRun: true}

// settingsModel is a model on the Settings tab of a library, with the
// config file read.
func settingsModel(t *testing.T, f *fakeSettings) *Model {
	t.Helper()
	m := newModel(t, &fakeSource{snap: sampleSnapshot()}, 120, 32)
	m.settingsSrc = f
	press(t, m, "6")
	if !m.settings.loaded {
		t.Fatal("the Settings tab did not read the config file")
	}
	return m
}

// to puts the cursor on the setting with the given key.
func to(t *testing.T, m *Model, key string) {
	t.Helper()
	m.settingsTo(key)
	if m.settings.row().key != key {
		t.Fatalf("there is no setting %q on the tab", key)
	}
}

// TestSettingRowsAreTheConfigKeys checks that the tab lists every key of a
// config file once, so that a new key cannot be left off it.
func TestSettingRowsAreTheConfigKeys(t *testing.T) {
	var rows []string
	for _, r := range settingRows {
		rows = append(rows, r.key)
		if r.label == "" || r.about == "" {
			t.Errorf("the setting %q has no label or nothing said about it", r.key)
		}
		if len(r.label) > 20 {
			t.Errorf("the label %q is wider than its column", r.label)
		}
	}
	keys := config.Keys()
	slices.Sort(rows)
	slices.Sort(keys)
	if !slices.Equal(rows, keys) {
		t.Errorf("the tab lists %v\nthe keys are  %v", rows, keys)
	}
}

func TestGoldenSettings(t *testing.T) {
	f := &fakeSettings{file: SettingsFile{Path: p(`C:\Users\me\AppData\Roaming\tagshelf\config.yaml`), Config: savedSettings,
		Session: map[string]string{"destination": p(`E:\Other`)}, DataFolder: p(`%LOCALAPPDATA%\tagshelf`)}}
	m := settingsModel(t, f)
	to(t, m, "untagged")
	press(t, m, "enter")
	typeText(t, m, p(`D:\Loose`))
	press(t, m, "enter")
	to(t, m, "destination")
	got := screen(m)
	checkFits(t, got, 120, 32)
	golden(t, filepath.Join("testdata", "settings.golden"), got)
}

// TestSettingsSaysWhereTheDatabaseIsKept checks the row of a database that
// is not set: it names the folder it was told, and no folder when told none.
func TestSettingsSaysWhereTheDatabaseIsKept(t *testing.T) {
	told := &fakeSettings{file: SettingsFile{Path: p(`C:\c.yaml`), Config: savedSettings, DataFolder: "~/.local/share/tagshelf"}}
	if got := screen(settingsModel(t, told)); !strings.Contains(got, "Database file        (kept under ~/.local/share/tagshelf, by the library's ID)") {
		t.Errorf("the database row does not name the folder it was told:\n%s", got)
	}
	untold := &fakeSettings{file: SettingsFile{Path: p(`C:\c.yaml`), Config: savedSettings}}
	if got := screen(settingsModel(t, untold)); !strings.Contains(got, "Database file        (kept with tagshelf's own files, by the library's ID)") {
		t.Errorf("the database row with no folder told:\n%s", got)
	}
}

// TestTUIStartsOnSettingsWithNothingSetUp checks where the TUI opens: on
// Settings when there is no library folder and so no run to start, on Home
// otherwise.
func TestTUIStartsOnSettingsWithNothingSetUp(t *testing.T) {
	f := &fakeSettings{file: SettingsFile{Path: p(`C:\cfg\config.yaml`), New: true, Config: config.Config{DryRun: true}}}
	for _, tt := range []struct {
		name string
		opts Options
		want Tab
	}{
		{"no library folder", Options{NoLibrary: noFolder, Settings: f}, TabSettings},
		{"no library folder and no settings to change", Options{NoLibrary: noFolder}, TabHome},
		{"no index yet", Options{NoLibrary: &NoLibrary{What: "no index", CanRun: true}, Settings: f}, TabHome},
		{"a library", Options{source: &fakeSource{snap: sampleSnapshot()}, Settings: f}, TabHome},
	} {
		m := New(noSource{}, Header{})
		m.tick = func() tea.Cmd { return nil }
		m.open(tt.opts, true)
		m.Update(windowSize(100, 24))
		do(t, m, m.Init())
		if m.tab != tt.want {
			t.Errorf("%s: opened on %v, want %v", tt.name, m.tab, tt.want)
		}
		if tt.want != TabSettings {
			continue
		}
		got := screen(m)
		for _, want := range []string{p(`Settings  C:\cfg\config.yaml  not made yet: s makes it`), "Source folder        (not set)", "Dry run              on",
			"The folder of music that o on Home organizes into the library.", "s save"} {
			if !strings.Contains(got, want) {
				t.Errorf("%s: the screen lacks %q:\n%s", tt.name, want, got)
			}
		}
	}
}

// TestSettingsEditing changes settings of each kind and drops the changes.
func TestSettingsEditing(t *testing.T) {
	f := &fakeSettings{file: SettingsFile{Path: p(`C:\cfg\config.yaml`), Config: savedSettings}}
	m := settingsModel(t, f)

	// A folder: typed, with the keys of the tab going into the text.
	to(t, m, "source")
	press(t, m, "enter")
	press(t, m, "ctrl+u")
	typeText(t, m, p(`"E:\New sq1`))
	press(t, m, "backspace")
	press(t, m, "left")
	press(t, m, "left")
	press(t, m, "backspace")
	press(t, m, "end")
	typeText(t, m, p(`\"`))
	if m.tab != TabSettings || m.settings.input == nil {
		t.Fatalf("typing left the field: tab %v, typing %t", m.tab, m.settings.input != nil)
	}
	if got := screen(m); !strings.Contains(got, "type or paste a path  tab complete  enter done  esc cancel") {
		t.Errorf("the keys shown while typing a path:\n%s", got)
	}
	press(t, m, "enter")
	if m.settings.values.Source != p(`E:\Newsq`) || m.settings.input != nil {
		t.Errorf("source = %q, want the path typed, without its quotes and cleaned", m.settings.values.Source)
	}
	// Typing that is cancelled changes nothing.
	press(t, m, "enter")
	typeText(t, m, "xyz")
	press(t, m, "esc")
	if m.settings.values.Source != p(`E:\Newsq`) || m.settings.input != nil {
		t.Errorf("after esc: source = %q, typing %t", m.settings.values.Source, m.settings.input != nil)
	}

	// On or off: enter or space.
	to(t, m, "dry_run")
	press(t, m, "enter")
	to(t, m, "cue_rewrite")
	press(t, m, "space")
	if m.settings.values.DryRun || m.settings.values.RewriteCues() {
		t.Errorf("dry run %t, cue rewrite %t; want both turned off", m.settings.values.DryRun, m.settings.values.RewriteCues())
	}

	// A number: only a whole one is taken, and nothing is 0.
	to(t, m, "workers")
	press(t, m, "enter")
	press(t, m, "backspace")
	typeText(t, m, "4x")
	press(t, m, "enter")
	if m.settings.input == nil || m.toastOK || !strings.Contains(m.toast, "Files at once takes a whole number") {
		t.Errorf("4x was taken for a number: toast %q, typing %t", m.toast, m.settings.input != nil)
	}
	press(t, m, "backspace")
	press(t, m, "enter")
	if m.settings.values.Workers != 4 {
		t.Errorf("workers = %d, want 4", m.settings.values.Workers)
	}
	press(t, m, "enter")
	press(t, m, "backspace")
	press(t, m, "enter")
	if m.settings.values.Workers != 0 {
		t.Errorf("workers = %d, want 0 for nothing typed", m.settings.values.Workers)
	}

	got := screen(m)
	for _, want := range []string{"  unsaved changes", p("  * Source folder        E:\\Newsq"), "  * Dry run              off", "  * Fix cue sheets       off",
		"›   Files at once        0  one for each CPU", p("    Library folder       D:\\Music")} {
		if !strings.Contains(got, want) {
			t.Errorf("the screen lacks %q:\n%s", want, got)
		}
	}

	// Moving: the ends hold.
	for _, k := range []string{"g", "up", "k"} {
		press(t, m, k)
	}
	if m.settings.cursor != 0 {
		t.Errorf("cursor = %d at the top", m.settings.cursor)
	}
	for _, k := range []string{"G", "down", "j"} {
		press(t, m, k)
	}
	if m.settings.cursor != len(settingRows)-1 {
		t.Errorf("cursor = %d at the end", m.settings.cursor)
	}

	press(t, m, "esc")
	if m.settings.dirty() || m.settings.values != savedSettings || !strings.Contains(m.toast, "Changes dropped") {
		t.Errorf("after esc the settings are %+v (toast %q), want them as saved", m.settings.values, m.toast)
	}
	if len(f.saved) != 0 || f.checks != 0 {
		t.Error("the file was checked or saved without s")
	}
}

// TestSettingsPaste pastes a path into the field being typed, and checks a
// paste goes nowhere when none is.
func TestSettingsPaste(t *testing.T) {
	m := settingsModel(t, &fakeSettings{file: SettingsFile{Path: p(`C:\c.yaml`), Config: savedSettings}})
	m.Update(tea.PasteMsg{Content: p(`E:\Nowhere`)})
	if m.settings.dirty() || m.settings.input != nil {
		t.Error("a paste with no field open changed a setting")
	}
	to(t, m, "destination")
	press(t, m, "enter")
	press(t, m, "ctrl+u")
	m.Update(tea.PasteMsg{Content: p("\"E:\\My\n Music\"\n")})
	press(t, m, "enter")
	if got := m.settings.values.Destination; got != p(`E:\My Music`) {
		t.Errorf("destination = %q, want the pasted path on one line, without quotes", got)
	}
}

// TestSettingsSave goes through a save: refused with what is wrong, asked
// about, and written, with the library opened again by the new settings.
func TestSettingsSave(t *testing.T) {
	f := &fakeSettings{file: SettingsFile{Path: p(`C:\cfg\config.yaml`), Config: savedSettings}}
	m := settingsModel(t, f)
	reopens := 0
	m.reopen = func() (Options, error) {
		reopens++
		return Options{Destination: p(`E:\Music`), LibraryID: "aaaa1111-2", Settings: f, DryRun: true, source: &fakeSource{snap: sampleSnapshot()}}, nil
	}

	press(t, m, "s")
	if f.checks != 0 || !m.toastOK || !strings.Contains(m.toast, "Nothing to save") {
		t.Errorf("s with no change: checked %d times, toast %q", f.checks, m.toast)
	}

	to(t, m, "destination")
	press(t, m, "enter")
	press(t, m, "ctrl+u")
	typeText(t, m, p(`E:\Music`))
	press(t, m, "enter")
	to(t, m, "dry_run")
	press(t, m, "enter")

	// Something wrong: nothing is saved, and the cursor goes to it.
	f.check = SettingsCheck{Problems: map[string]string{"untagged": "is the library folder itself", "workers": "must be 0 or more"},
		Confirm: []string{"never asked"}}
	press(t, m, "s")
	if len(f.saved) != 0 || m.toastOK || !strings.Contains(m.toast, "Not saved: 2 settings need changing") || m.settings.row().key != "untagged" {
		t.Fatalf("saved %d times, toast %q, cursor on %q", len(f.saved), m.toast, m.settings.row().key)
	}
	got := screen(m)
	for _, want := range []string{"✗ is the library folder itself", "✗ must be 0 or more", "Untagged folder is the library folder itself."} {
		if !strings.Contains(got, want) {
			t.Errorf("the screen lacks %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "never asked") {
		t.Error("a question was asked about settings that cannot be saved")
	}

	// Questions: anything but y stops the save.
	f.check = SettingsCheck{Confirm: []string{p(`Make the library folder E:\Music?`), p("This opens a different library. Your music in D:\\Music is not moved.")}}
	press(t, m, "s")
	if got := screen(m); !strings.Contains(got, p(`Make the library folder E:\Music?  y yes, n no`)) || len(f.saved) != 0 {
		t.Fatalf("the first question is not asked, or the file was saved (%d):\n%s", len(f.saved), got)
	}
	press(t, m, "y")
	if got := screen(m); !strings.Contains(got, p("This opens a different library. Your music in D:\\Music is not moved.  y yes, n no")) || len(f.saved) != 0 {
		t.Fatalf("the second question is not asked:\n%s", got)
	}
	press(t, m, "n")
	if len(f.saved) != 0 || !strings.Contains(m.toast, "Not saved") || !m.settings.dirty() {
		t.Fatalf("n saved the file (%d) or dropped the changes; toast %q", len(f.saved), m.toast)
	}

	// Yes to both: saved, read again, and the library opened again.
	f.note = "Files already in the library stay where they are: press r on Home to reorganize"
	press(t, m, "s")
	press(t, m, "y")
	press(t, m, "y")
	want := savedSettings
	want.Destination, want.DryRun = p(`E:\Music`), false
	if len(f.saved) != 1 || f.saved[0] != want {
		t.Fatalf("saved %+v, want once, %+v", f.saved, want)
	}
	if reopens != 1 || m.header.Destination != p(`E:\Music`) || m.header.LibraryID != "aaaa1111-2" {
		t.Errorf("opened again %d times, header %+v; want the library of the new settings", reopens, m.header)
	}
	if m.header.DryRun {
		t.Error("dry run was saved off, and the session still has it on")
	}
	if m.tab != TabSettings || m.settings.dirty() || m.settings.problems != nil || m.reopening {
		t.Errorf("after the save: tab %v, dirty %t, problems %v, reopening %t", m.tab, m.settings.dirty(), m.settings.problems, m.reopening)
	}
	if !m.toastOK || !strings.Contains(m.toast, p(`Saved to C:\cfg\config.yaml. Files already in the library stay where they are`)) {
		t.Errorf("toast = %q", m.toast)
	}
	if got := screen(m); !strings.Contains(got, "2 Untagged 12") || strings.Contains(got, "unsaved changes") {
		t.Errorf("the screen after the save:\n%s", got)
	}
}

// TestSettingsSaveKeepsAFlagsDryRun checks that saving dry_run does not
// change the session's when a flag set it.
func TestSettingsSaveKeepsAFlagsDryRun(t *testing.T) {
	f := &fakeSettings{file: SettingsFile{Path: p(`C:\c.yaml`), Config: savedSettings, Session: map[string]string{"dry_run": "on"}}}
	m := settingsModel(t, f)
	m.header.DryRun = true
	to(t, m, "dry_run")
	press(t, m, "enter")
	press(t, m, "s")
	if len(f.saved) != 1 || f.saved[0].DryRun || !m.header.DryRun {
		t.Errorf("saved %+v, session dry run %t; want it saved off and the session's left on", f.saved, m.header.DryRun)
	}
	if got := screen(m); !strings.Contains(got, "Dry run              off  this session: on") {
		t.Errorf("the row does not show what the session goes by:\n%s", got)
	}
}

// TestSettingsSaveFails checks the two ways a save can fail: the file
// changed under the tab, and anything else.
func TestSettingsSaveFails(t *testing.T) {
	for _, tt := range []struct {
		err  error
		want string
	}{
		{ErrSettingsChanged, "Not saved: the config file changed on disk since it was read"},
		{errors.New("access is denied"), "Not saved: access is denied"},
	} {
		f := &fakeSettings{file: SettingsFile{Path: p(`C:\c.yaml`), Config: savedSettings}, err: tt.err}
		m := settingsModel(t, f)
		reopened := false
		m.reopen = func() (Options, error) { reopened = true; return Options{}, nil }
		to(t, m, "verbose")
		press(t, m, "enter")
		press(t, m, "s")
		if m.toastOK || !strings.Contains(m.toast, tt.want) || !m.settings.dirty() || reopened || m.settings.saving {
			t.Errorf("%v: toast %q, dirty %t, reopened %t", tt.err, m.toast, m.settings.dirty(), reopened)
		}
	}
}

// TestSettingsNotSavedDuringARun checks that s is refused while a run is
// going, which the new settings would be opened under.
func TestSettingsNotSavedDuringARun(t *testing.T) {
	f := &fakeSettings{file: SettingsFile{Path: p(`C:\c.yaml`), Config: savedSettings}}
	r := newFakeRunner()
	m := settingsModel(t, f)
	m.runner = r
	press(t, m, "1")
	press(t, m, "o")
	waitStart(t, r)
	press(t, m, "6")
	to(t, m, "verbose")
	press(t, m, "enter")
	press(t, m, "s")
	if f.checks != 0 || len(f.saved) != 0 || !strings.Contains(m.toast, "cannot be saved while a run") {
		t.Errorf("checked %d, saved %d, toast %q", f.checks, len(f.saved), m.toast)
	}
	r.finish <- engine.RunFinished{State: engine.StateOK}
	do(t, m, runEnded(t, m))
	press(t, m, "s")
	if len(f.saved) != 1 {
		t.Errorf("saved %d times once the run had ended, want once", len(f.saved))
	}
}

// TestSettingsAsksBeforeLeavingUnsaved checks that a key that would leave
// the tab, or quit, asks first when there are changes.
func TestSettingsAsksBeforeLeavingUnsaved(t *testing.T) {
	for _, k := range []string{"1", "3", "tab", "shift+tab", "q"} {
		m := settingsModel(t, &fakeSettings{file: SettingsFile{Path: p(`C:\c.yaml`), Config: savedSettings}})
		to(t, m, "verbose")
		press(t, m, "enter")
		press(t, m, k)
		if m.tab != TabSettings || !strings.Contains(screen(m), "Drop the unsaved changes to the settings? y drops them, n stays") {
			t.Fatalf("%s left the tab without asking:\n%s", k, screen(m))
		}
		press(t, m, "n")
		if m.tab != TabSettings || !m.settings.dirty() {
			t.Fatalf("%s then n: tab %v, dirty %t", k, m.tab, m.settings.dirty())
		}
		press(t, m, k)
		msg := press(t, m, "y")
		if m.settings.dirty() {
			t.Errorf("%s then y kept the changes", k)
		}
		if k == "q" {
			if msg != (tea.QuitMsg{}) {
				t.Errorf("q then y did not quit: %#v", msg)
			}
			continue
		}
		if m.tab == TabSettings {
			t.Errorf("%s then y stayed on Settings", k)
		}
	}
	// With nothing changed there is nothing to ask.
	m := settingsModel(t, &fakeSettings{file: SettingsFile{Path: p(`C:\c.yaml`), Config: savedSettings}})
	press(t, m, "2")
	if m.tab != TabUntagged {
		t.Errorf("2 with nothing changed: tab %v", m.tab)
	}
}

// TestSettingsFileThatCannotBeEdited shows a config file that could not be
// read: what is wrong with it, and no setting to change.
func TestSettingsFileThatCannotBeEdited(t *testing.T) {
	f := &fakeSettings{file: SettingsFile{Path: p(`C:\cfg\config.yaml`), Problem: p(`the config file C:\cfg\config.yaml has an unknown key "dry-run" on line 2`)}}
	m := settingsModel(t, f)
	got := screen(m)
	for _, want := range []string{p(`The config file C:\cfg\config.yaml has an unknown key "dry-run" on line 2.`), "Fix the file in an editor, then press R to read it again."} {
		if !strings.Contains(got, want) {
			t.Errorf("the screen lacks %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "Source folder") || strings.Contains(got, "s save") {
		t.Errorf("settings are offered for a file that cannot be edited:\n%s", got)
	}
	for _, k := range []string{"enter", "space", "s", "down"} {
		press(t, m, k)
	}
	if m.settings.input != nil || f.checks != 0 || len(f.saved) != 0 {
		t.Error("a key changed or saved a file that cannot be edited")
	}

	// Put right in an editor, R reads it and opens the library again.
	f.file = SettingsFile{Path: p(`C:\cfg\config.yaml`), Config: savedSettings}
	reopens := 0
	m.reopen = func() (Options, error) {
		reopens++
		return Options{Destination: p(`D:\Music`), Settings: f, source: &fakeSource{snap: sampleSnapshot()}}, nil
	}
	press(t, m, "R")
	if got := screen(m); !strings.Contains(got, p(`Source folder        D:\Incoming`)) || reopens != 1 || m.reopening {
		t.Errorf("after R: opened again %d times\n%s", reopens, got)
	}
	// With changes, R would lose them: it says so instead.
	to(t, m, "verbose")
	press(t, m, "enter")
	press(t, m, "R")
	if reopens != 1 || !m.settings.dirty() || !strings.Contains(m.toast, "unsaved changes") {
		t.Errorf("R with changes: opened again %d times, dirty %t, toast %q", reopens, m.settings.dirty(), m.toast)
	}
}

// TestRefusedRunPointsAtItsSetting checks that a run refused for want of a
// setting says where to change it, and that 6 goes to that setting.
func TestRefusedRunPointsAtItsSetting(t *testing.T) {
	f := &fakeSettings{file: SettingsFile{Path: p(`C:\c.yaml`), Config: savedSettings}}
	r := newFakeRunner()
	r.err = &StartError{What: "no source folder is set", Todo: "Or start tagshelf tui with one.", Setting: "source"}
	m := newRunModel(t, r)
	m.settingsSrc = f
	m.settings.cursor = 5
	press(t, m, "o")
	if got := screen(m); !strings.Contains(got, "Cannot start the organize: no source folder is set.\n  Or start tagshelf tui with one. Press 6 to change it in Settings.") {
		t.Fatalf("the refusal does not point at Settings:\n%s", got)
	}
	press(t, m, "6")
	if m.tab != TabSettings || m.settings.row().key != "source" {
		t.Errorf("6 went to tab %v, setting %q; want Settings, on the source", m.tab, m.settings.row().key)
	}
	// Without a refusal, 6 leaves the cursor where it was.
	m.settings.cursor = 5
	press(t, m, "1")
	press(t, m, "6")
	if m.settings.cursor != 5 {
		t.Errorf("6 moved the cursor to %d with no refusal showing", m.settings.cursor)
	}
}

// TestSettingsWithoutAFile checks the tab when there is no config file to
// edit: it says so, and its keys do nothing.
func TestSettingsWithoutAFile(t *testing.T) {
	m := newModel(t, &fakeSource{snap: sampleSnapshot()}, 100, 24)
	press(t, m, "6")
	if got := screen(m); !strings.Contains(got, "Settings cannot be changed from here.") {
		t.Errorf("the screen:\n%s", got)
	}
	for _, k := range []string{"enter", "s", "down", "esc", "R"} {
		press(t, m, k)
	}
	press(t, m, "1")
	if m.tab != TabHome {
		t.Errorf("tab = %v", m.tab)
	}
}

// TestPathHintAndCompletion checks what is said about a path being typed,
// and what tab completes it to.
func TestPathHintAndCompletion(t *testing.T) {
	dir := t.TempDir()
	for _, d := range []string{"Music", "Music Videos", "Incoming"} {
		if err := os.Mkdir(filepath.Join(dir, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	file := filepath.Join(dir, "music.db")
	if err := os.WriteFile(file, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	in := func(name string) string { return dir + string(filepath.Separator) + name }

	hints := []struct {
		path   string
		folder bool
		want   string
	}{
		{"", true, ""},
		{in("Music"), true, "This folder exists."},
		{in("Nope"), true, "This folder does not exist yet."},
		{file, true, "This is a file, not a folder."},
		{file, false, "This file exists."},
		{in("new.db"), false, "This file does not exist yet."},
		{in("Music"), false, "This is a folder, not a file."},
	}
	for _, h := range hints {
		if got := pathHint(h.path, h.folder); got != h.want {
			t.Errorf("pathHint(%q, folder %t) = %q, want %q", h.path, h.folder, got, h.want)
		}
	}

	completions := []struct {
		typed   string
		folders bool
		want    string
	}{
		{in("inc"), true, in(p(`Incoming\`))},
		{in("Mu"), true, in("Music")}, // as far as Music and Music Videos agree
		{in("Music V"), true, in(p(`Music Videos\`))},
		{in("mus"), false, in("Music")}, // the folders and the file
		{in("music."), false, in("music.db")},
		{in("music."), true, in("music.")}, // a file is not offered for a folder
		{in("zzz"), true, in("zzz")},
		{in(""), true, in("")},
		{"no separator", true, "no separator"},
		{in(p(`Nope\x`)), true, in(p(`Nope\x`))},
		{`"` + in("inc"), true, in(p(`Incoming\`))}, // a quote left from a paste
	}
	for _, c := range completions {
		if got := completePath(c.typed, c.folders); got != c.want {
			t.Errorf("completePath(%q, folders %t) = %q, want %q", c.typed, c.folders, got, c.want)
		}
	}

	// In the tab: tab completes, and the note under the field follows.
	m := settingsModel(t, &fakeSettings{file: SettingsFile{Path: p(`C:\c.yaml`), Config: savedSettings}})
	to(t, m, "source")
	press(t, m, "enter")
	press(t, m, "ctrl+u")
	typeText(t, m, in("inc"))
	press(t, m, "tab")
	if got := m.settings.input.String(); got != in(p(`Incoming\`)) {
		t.Errorf("after tab the field holds %q", got)
	}
	if got := screen(m); !strings.Contains(got, "This folder exists.") {
		t.Errorf("no note under the field:\n%s", got)
	}
	typeText(t, m, "x")
	if got := screen(m); !strings.Contains(got, "This folder does not exist yet.") {
		t.Errorf("the note did not follow the typing:\n%s", got)
	}
	press(t, m, "enter")
	if got := screen(m); strings.Contains(got, "does not exist yet") {
		t.Errorf("the note outlived the field:\n%s", got)
	}
}

func TestLineInput(t *testing.T) {
	l := newLineInput("abc")
	key := func(k string) { l.key(keyMsg(k)) }
	key("left")
	key("X")
	if l.String() != "abXc" || l.pos != 3 {
		t.Errorf("after an insert: %q at %d", l.String(), l.pos)
	}
	key("home")
	key("left")
	l.key(tea.KeyPressMsg{Code: tea.KeyDelete})
	key("backspace") // nothing before the cursor
	if l.String() != "bXc" || l.pos != 0 {
		t.Errorf("after a delete at the start: %q at %d", l.String(), l.pos)
	}
	key("end")
	key("right")
	l.key(tea.KeyPressMsg{Code: tea.KeyDelete}) // nothing after the cursor
	key("backspace")
	if l.String() != "bX" || l.pos != 2 {
		t.Errorf("after a backspace at the end: %q at %d", l.String(), l.pos)
	}
	l.insert("éé")
	key("ctrl+a")
	key("right")
	l.insert("-")
	if l.String() != "b-Xéé" {
		t.Errorf("after inserts: %q", l.String())
	}
	key("ctrl+u")
	if l.String() != "" || l.pos != 0 {
		t.Errorf("after ctrl+u: %q at %d", l.String(), l.pos)
	}

	// Wider than its field, it shows the part the cursor is in.
	l = newLineInput(p(`D:\A very long folder name`))
	if got := ansiStrip(l.view(10)); got != "lder name " {
		t.Errorf("view at the end = %q", got)
	}
	l.key(keyMsg("home"))
	if got := ansiStrip(l.view(10)); got != p(`D:\A very `) {
		t.Errorf("view at the start = %q", got)
	}
}

func ansiStrip(s string) string { return ansi.Strip(s) }
