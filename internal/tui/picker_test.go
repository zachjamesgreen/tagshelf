package tui

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/zachjamesgreen/tagshelf/internal/config"
)

// pickerTree makes a folder with three folders, a hidden one and a file in
// it, one of them with a folder of its own, and returns it.
func pickerTree(t *testing.T) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), "top")
	for _, dir := range []string{"beta", filepath.Join("Alpha", "inner"), "gamma", ".hidden"} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "a file.txt"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}

// pickerOn opens the Settings tab's folder picker on the source folder,
// which the config file sets to dir.
func pickerOn(t *testing.T, dir string) (*Model, *fakeSettings) {
	t.Helper()
	// The tests have no drives of their own: the real ones are not theirs
	// to list.
	old := listDrives
	listDrives = func() []string { return nil }
	t.Cleanup(func() { listDrives = old })
	f := &fakeSettings{file: SettingsFile{Path: p(`C:\cfg\config.yaml`), Config: config.Config{Source: dir}}}
	m := settingsModel(t, f)
	to(t, m, "source")
	hit(t, m, "b")
	if m.settings.picker == nil || m.settings.picker.reading {
		t.Fatalf("the picker did not open:\n%s", screen(m))
	}
	return m, f
}

func TestListFolders(t *testing.T) {
	root := pickerTree(t)
	got, err := listFolders(root, false)
	if want := []string{"Alpha", "beta", "gamma"}; err != nil || !slices.Equal(got, want) {
		t.Errorf("listFolders = %q, %v; want %q: folders only, by name whatever their case, none hidden", got, err, want)
	}
	got, err = listFolders(root, true)
	if want := []string{".hidden", "Alpha", "beta", "gamma"}; err != nil || !slices.Equal(got, want) {
		t.Errorf("with the hidden ones = %q, %v; want %q", got, err, want)
	}
	if _, err := listFolders(filepath.Join(root, "none"), false); err == nil {
		t.Error("a folder that is not there lists without an error")
	}

	// A link to a folder is a folder to go into; one to a file is not.
	if err := os.Symlink(filepath.Join(root, "beta"), filepath.Join(root, "link")); err != nil {
		t.Skipf("this system makes no links here: %v", err)
	}
	if err := os.Symlink(filepath.Join(root, "a file.txt"), filepath.Join(root, "link to a file")); err != nil {
		t.Fatal(err)
	}
	if got, _ := listFolders(root, false); !slices.Equal(got, []string{"Alpha", "beta", "gamma", "link"}) {
		t.Errorf("with links = %q, want the link to a folder among them and not the one to a file", got)
	}
}

// The system's own folders at the top of a drive are never listed.
func TestListFoldersLeavesOutTheSystemsOwn(t *testing.T) {
	root := t.TempDir()
	for _, dir := range []string{"$RECYCLE.BIN", "System Volume Information", "Music"} {
		if err := os.Mkdir(filepath.Join(root, dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if got, err := listFolders(root, true); err != nil || !slices.Equal(got, []string{"Music"}) {
		t.Errorf("listFolders = %q, %v; want Music alone", got, err)
	}
}

func TestPickerStart(t *testing.T) {
	root := pickerTree(t)
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip(err)
	}
	for value, want := range map[string]string{
		root:                                    root,
		filepath.Join(root, "beta"):             filepath.Join(root, "beta"),
		filepath.Join(root, "not", "there"):     root, // the nearest folder above that is there
		filepath.Join(root, "a file.txt", "in"): root,
		"":                                      home,
		"~":                                     home,
	} {
		if got := pickerStart(value); got != want {
			t.Errorf("pickerStart(%q) = %q, want %q", value, got, want)
		}
	}
}

// b on a folder setting opens the picker at the folder the setting holds.
// Enter goes into the folder under the cursor, backspace back out with the
// cursor on the folder left, and s takes the folder shown as the setting's
// value, which is not saved until s saves the settings.
func TestPickerWalksAndTakesAFolder(t *testing.T) {
	root := pickerTree(t)
	m, f := pickerOn(t, root)
	got := screen(m)
	for _, want := range []string{"pick the source folder", root, "› Alpha", "beta", "gamma", "s take this folder"} {
		if !strings.Contains(got, want) {
			t.Errorf("the picker lacks %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, ".hidden") || strings.Contains(got, "a file.txt") {
		t.Errorf("the picker lists a hidden folder or a file:\n%s", got)
	}

	hit(t, m, "enter")
	if pk := m.settings.picker; pk.dir != filepath.Join(root, "Alpha") || !slices.Equal(pk.names, []string{"inner"}) {
		t.Fatalf("after enter the picker shows %q with %q", pk.dir, pk.names)
	}
	hit(t, m, "backspace")
	if pk := m.settings.picker; pk.dir != root || pk.names[pk.cursor] != "Alpha" {
		t.Fatalf("after backspace the picker shows %q with the cursor on %q", pk.dir, pk.names[pk.cursor])
	}
	hit(t, m, "down", "down", "enter")
	if got := screen(m); !strings.Contains(got, "No folders in it") {
		t.Errorf("an empty folder does not say so:\n%s", got)
	}
	hit(t, m, "s")
	if m.settings.picker != nil || m.settings.values.Source != filepath.Join(root, "gamma") {
		t.Errorf("after s: picker open %t, source %q", m.settings.picker != nil, m.settings.values.Source)
	}
	if len(f.saved) != 0 || !m.settings.dirty() {
		t.Errorf("taking a folder saved the settings (%d saves), or left them unchanged", len(f.saved))
	}
	if got := screen(m); !strings.Contains(got, filepath.Join(root, "gamma")) || !strings.Contains(got, "unsaved changes") {
		t.Errorf("the settings do not show the folder taken:\n%s", got)
	}
}

// Esc leaves the picker without changing the setting, and the other keys of
// the Settings tab mean nothing while it is open.
func TestPickerEscLeavesTheSettingAlone(t *testing.T) {
	root := pickerTree(t)
	m, _ := pickerOn(t, root)
	hit(t, m, "enter", "1", "q", "R")
	if m.tab != TabSettings || m.settings.picker == nil {
		t.Fatalf("a key of the tab left the picker: tab %v", m.tab)
	}
	hit(t, m, "esc")
	if m.settings.picker != nil || m.settings.values.Source != root || m.settings.dirty() {
		t.Errorf("after esc: picker open %t, source %q", m.settings.picker != nil, m.settings.values.Source)
	}
	// Only a folder can be picked.
	to(t, m, "workers")
	hit(t, m, "b")
	if m.settings.picker != nil || !strings.Contains(m.toast, "Only a folder can be picked") {
		t.Errorf("b on a number: picker open %t, toast %q", m.settings.picker != nil, m.toast)
	}
}

// The dot shows the hidden folders, with the cursor kept on its folder, and
// n makes a folder in the one shown and goes into it.
func TestPickerShowsHiddenFoldersAndMakesOne(t *testing.T) {
	root := pickerTree(t)
	m, _ := pickerOn(t, root)
	hit(t, m, "down", ".")
	if pk := m.settings.picker; !slices.Contains(pk.names, ".hidden") || pk.names[pk.cursor] != "beta" {
		t.Fatalf("after the dot: %q, cursor on %q", pk.names, pk.names[pk.cursor])
	}

	hit(t, m, "n")
	if got := screen(m); !strings.Contains(got, "Name of the new folder:") {
		t.Fatalf("the picker does not ask for the name:\n%s", got)
	}
	typeText(t, m, "a/b")
	hit(t, m, "enter")
	if !strings.Contains(m.toast, "cannot hold a slash") || m.settings.picker.naming == nil {
		t.Errorf("a name with a slash: toast %q", m.toast)
	}
	hit(t, m, "ctrl+u")
	typeText(t, m, "New Music")
	hit(t, m, "enter")
	made := filepath.Join(root, "New Music")
	if info, err := os.Stat(made); err != nil || !info.IsDir() {
		t.Fatalf("the folder was not made: %v", err)
	}
	if pk := m.settings.picker; pk.dir != made || pk.naming != nil {
		t.Errorf("after making it the picker shows %q", pk.dir)
	}
	// One that is there already is gone into, not an error.
	hit(t, m, "backspace", "n")
	typeText(t, m, "beta")
	hit(t, m, "enter")
	if pk := m.settings.picker; pk.dir != filepath.Join(root, "beta") || pk.problem != "" {
		t.Errorf("naming a folder that is there: the picker shows %q, problem %q", pk.dir, pk.problem)
	}
}

// A folder that cannot be opened is not gone into: the picker stays where
// it was and says why.
func TestPickerSaysWhenAFolderCannotBeOpened(t *testing.T) {
	root := pickerTree(t)
	m, _ := pickerOn(t, root)
	if err := os.Remove(filepath.Join(root, "Alpha", "inner")); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(root, "Alpha")); err != nil {
		t.Fatal(err)
	}
	hit(t, m, "enter") // Alpha is still listed, and gone
	if pk := m.settings.picker; pk.dir != root || !strings.Contains(pk.problem, "Cannot open") {
		t.Errorf("the picker shows %q, problem %q", pk.dir, pk.problem)
	}
	if got := screen(m); !strings.Contains(got, "Cannot open") {
		t.Errorf("the picker does not say why:\n%s", got)
	}
}

// Above the top of a drive is the list of drives, where there are drives:
// one is opened like a folder, and none can be taken or made there. Where
// there are none, nothing is above the top.
func TestPickerListsTheDrivesAboveTheTopOfOne(t *testing.T) {
	root := pickerTree(t)
	top := filepath.VolumeName(root) + string(filepath.Separator)
	m, _ := pickerOn(t, root)
	// The picker is put at the top of the drive, which is not read, and the
	// folder of the test stands in for a drive to open.
	m.settings.picker.dir = top
	hit(t, m, "backspace")
	if pk := m.settings.picker; pk.dir != top {
		t.Fatalf("with no drives, backspace at %s went to %q", top, pk.dir)
	}

	listDrives = func() []string { return []string{root, "Q:"} }
	hit(t, m, "backspace")
	pk := m.settings.picker
	if pk.dir != "" || !slices.Equal(pk.names, []string{root, "Q:"}) {
		t.Fatalf("above the top of the drive: %q with %q", pk.dir, pk.names)
	}
	if got := screen(m); !strings.Contains(got, "The drives of this computer") {
		t.Errorf("the list of drives is not named:\n%s", got)
	}
	hit(t, m, "s")
	if m.settings.picker == nil || !strings.Contains(m.toast, "Open a drive first") {
		t.Errorf("s on the list of drives: toast %q", m.toast)
	}
	hit(t, m, "n")
	if m.settings.picker.naming != nil {
		t.Error("n on the list of drives asks for a name")
	}
	hit(t, m, "backspace")
	if m.settings.picker.dir != "" {
		t.Error("backspace on the list of drives went somewhere")
	}
	hit(t, m, "enter")
	if m.settings.picker.dir != root {
		t.Errorf("enter on a drive shows %q, want %q", m.settings.picker.dir, root)
	}
}

// ctrl+o while a path is being typed opens the picker at what was typed.
func TestPickerOpensFromThePathBeingTyped(t *testing.T) {
	root := pickerTree(t)
	old := listDrives
	listDrives = func() []string { return nil }
	t.Cleanup(func() { listDrives = old })
	m := settingsModel(t, &fakeSettings{file: SettingsFile{Path: p(`C:\cfg\config.yaml`)}})
	to(t, m, "destination")
	hit(t, m, "enter")
	typeText(t, m, filepath.Join(root, "beta"))
	hit(t, m, "ctrl+o")
	if pk := m.settings.picker; pk == nil || pk.dir != filepath.Join(root, "beta") || m.settings.input != nil || pk.key != "destination" {
		t.Fatalf("ctrl+o did not open the picker at the path typed:\n%s", screen(m))
	}
	hit(t, m, "s")
	if m.settings.values.Destination != filepath.Join(root, "beta") {
		t.Errorf("the library folder is %q", m.settings.values.Destination)
	}
}

// A long list scrolls with the cursor and says where in it the cursor is.
func TestPickerScrollsALongList(t *testing.T) {
	root := filepath.Join(t.TempDir(), "many")
	for i := range 60 {
		if err := os.MkdirAll(filepath.Join(root, "folder "+string(rune('A'+i/26))+string(rune('a'+i%26))), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	m, _ := pickerOn(t, root)
	got := screen(m)
	checkFits(t, got, m.width, m.height)
	if !strings.Contains(got, "of 60") || !strings.Contains(got, "› folder Aa") {
		t.Errorf("the top of a long list:\n%s", got)
	}
	hit(t, m, "end")
	got = screen(m)
	checkFits(t, got, m.width, m.height)
	if !strings.Contains(got, "› folder Ch") || !strings.Contains(got, "to 60 of 60") {
		t.Errorf("the end of a long list:\n%s", got)
	}
	hit(t, m, "pgup", "home")
	if got := screen(m); !strings.Contains(got, "› folder Aa") {
		t.Errorf("home did not go to the top:\n%s", got)
	}
}
