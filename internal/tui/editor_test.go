package tui

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"tagshelf/internal/engine"
	"tagshelf/internal/fixtures"
	"tagshelf/internal/store"
	"tagshelf/internal/tagwrite"
)

// pump runs a command and whatever it leads to, as Bubble Tea would, feeding
// every message back to the model. Commands that wait, such as a cursor's
// blink, are left behind.
func pump(t *testing.T, m *Model, cmd tea.Cmd) {
	t.Helper()
	if cmd == nil {
		return
	}
	got := make(chan tea.Msg, 1)
	go func() { got <- cmd() }()
	var msg tea.Msg
	select {
	case msg = <-got:
	case <-time.After(50 * time.Millisecond):
		return
	}
	if msg == nil {
		return
	}
	// A batch or a sequence is a list of commands.
	if v := reflect.ValueOf(msg); v.Kind() == reflect.Slice && v.Type().Elem() == reflect.TypeOf(tea.Cmd(nil)) {
		for i := range v.Len() {
			pump(t, m, v.Index(i).Interface().(tea.Cmd))
		}
		return
	}
	if _, quit := msg.(tea.QuitMsg); quit {
		return
	}
	_, next := m.Update(msg)
	pump(t, m, next)
}

// hit sends a key to the model and pumps what comes of it.
func hit(t *testing.T, m *Model, keys ...string) {
	t.Helper()
	for _, k := range keys {
		_, cmd := m.Update(keyMsg(k))
		pump(t, m, cmd)
	}
}

// typeText types s a key at a time.
func typeText(t *testing.T, m *Model, s string) {
	t.Helper()
	for _, r := range s {
		k := string(r)
		if r == ' ' {
			k = "space"
		}
		hit(t, m, k)
	}
}

// editorOn opens the tag editor from the Untagged tab on the file with the
// given ID, whose tags the source says are tags.
func editorOn(t *testing.T, id int64, tags tagwrite.Values) (*Model, *fakeRunner, *fakeSource) {
	t.Helper()
	src := untaggedSource()
	m, f := newUntaggedModel(t, src)
	cursorTo(t, m, id)
	cur, _ := m.untagged.current()
	src.tags = map[string]tagwrite.Values{cur.Path: tags}
	hit(t, m, "e")
	if m.edit == nil || m.edit.form == nil {
		t.Fatalf("the editor did not open:\n%s", screen(m))
	}
	return m, f, src
}

func TestEditorSavesAnUntaggedFileIntoTheLibrary(t *testing.T) {
	m, f, src := editorOn(t, 103, nil)
	got := screen(m)
	for _, want := range []string{
		"Untagged › Edit tags › demo.mp3", filepath.Join("_Untagged", "demo.mp3"),
		"Title", "Artist", "Album artist", "Album", "Year", "Track", "Disc", "Compilation",
		"Marker:", "none", "Will stay in _Untagged: needs artist, album, title", "ctrl+s save",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the editor lacks %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "2 Untagged") {
		t.Errorf("the tabs still show under the editor:\n%s", got)
	}

	typeText(t, m, "Demo Song")
	hit(t, m, "down")
	typeText(t, m, "Hand Tagged")
	hit(t, m, "down", "down")
	typeText(t, m, "Tape")
	hit(t, m, "tab")
	typeText(t, m, "1999")
	hit(t, m, "enter")
	typeText(t, m, "3/12")
	want := "Will go to:    " + filepath.Join("Hand Tagged", "Tape (1999)", "03 - Demo Song.mp3")
	if got := screen(m); !strings.Contains(got, want) {
		t.Fatalf("the editor lacks %q:\n%s", want, got)
	}
	changes := tagwrite.Values{
		tagwrite.Title: {"Demo Song"}, tagwrite.Artist: {"Hand Tagged"}, tagwrite.Album: {"Tape"},
		tagwrite.Date: {"1999"}, tagwrite.Track: {"3/12"},
	}
	if got := m.edit.changes(); !reflect.DeepEqual(got, changes) {
		t.Errorf("changes = %v, want %v", got, changes)
	}

	dst := `D:\Music\Hand Tagged\Tape (1999)\03 - Demo Song.mp3`
	f.saveEvents = []engine.Event{engine.FilePlaced{Path: m.edit.file.Path, Destination: dst, Outcome: engine.Organized}}
	f.saveFinish = engine.RunFinished{State: engine.StateOK, Counters: engine.Counters{Organized: 1}}
	loads := src.loads
	hit(t, m, "ctrl+s")
	if !reflect.DeepEqual(f.saved, [][]int64{{103}}) {
		t.Errorf("saved = %v, want file 103", f.saved)
	}
	if m.edit != nil || !m.toastOK || !strings.Contains(m.toast, "Saved and moved to "+filepath.Join("Hand Tagged", "Tape (1999)")) {
		t.Errorf("after saving: editor open %t, toast %q (ok %t)", m.edit != nil, m.toast, m.toastOK)
	}
	if src.loads == loads || !strings.Contains(screen(m), "2 Untagged") {
		t.Errorf("after saving the library was not reloaded or the tabs are not back:\n%s", screen(m))
	}
}

// A save that leaves the file short of an artist, album or title is still a
// save: the file stays with its marker and what it needs.
func TestEditorPartialSaveStays(t *testing.T) {
	m, f, _ := editorOn(t, 103, nil)
	hit(t, m, "down")
	typeText(t, m, "Hand Tagged")
	f.saveFinish = engine.RunFinished{State: engine.StateOK, Counters: engine.Counters{LeftInPlace: 1}}
	// Enter walks the rest of the form and, on its last field, saves.
	hit(t, m, "enter", "enter", "enter", "enter", "enter", "enter", "enter")
	if len(f.saved) != 1 || m.edit != nil {
		t.Fatalf("saved %v, editor open %t", f.saved, m.edit != nil)
	}
	if !m.toastOK || !strings.Contains(m.toast, "It stays in _Untagged: needs album, title") {
		t.Errorf("toast = %q (ok %t)", m.toast, m.toastOK)
	}
}

func TestEditorOnALibraryFile(t *testing.T) {
	src := untaggedSource()
	m, f := newUntaggedModel(t, src)
	file := store.File{ID: 7, RootKind: store.RootLibrary, Path: `D:\Music\Singer\Albun (2001)\02 - Song.flac`}
	src.tags = map[string]tagwrite.Values{file.Path: {
		tagwrite.Title: {"Song"}, tagwrite.Artist: {"Singer"}, tagwrite.Album: {"Albun"}, tagwrite.Date: {"2001-05-06"},
		tagwrite.Track: {"2"}, tagwrite.Compilation: {"1"}, tagwrite.Marker: {"musicbrainz 2026-01-02"},
	}}
	pump(t, m, m.openEditor(file))
	got := screen(m)
	for _, want := range []string{"Will stay at:  " + filepath.Join("Singer", "Albun (2001)", "02 - Song.flac"), "musicbrainz 2026-01-02", "2001-05-06"} {
		if !strings.Contains(got, want) {
			t.Errorf("the editor lacks %q:\n%s", want, got)
		}
	}
	if len(m.edit.changes()) != 0 {
		t.Errorf("changes before any typing = %v", m.edit.changes())
	}

	// Fix the album's spelling and clear the compilation flag.
	hit(t, m, "down", "down", "down", "backspace")
	typeText(t, m, "m")
	hit(t, m, "down", "down", "down", "down", "n", "tab", "space")
	if got := screen(m); !strings.Contains(got, "Will go to:    "+filepath.Join("Singer", "Album (2001)", "02 - Song.flac")) {
		t.Errorf("the editor does not show the new place:\n%s", got)
	}
	want := tagwrite.Values{tagwrite.Album: {"Album"}, tagwrite.Compilation: {}}
	if got := m.edit.changes(); !reflect.DeepEqual(got, want) {
		t.Errorf("changes = %v, want %v", got, want)
	}
	f.saveFinish = engine.RunFinished{State: engine.StateOK}
	hit(t, m, "ctrl+s")
	if !reflect.DeepEqual(f.saved, [][]int64{{7}}) || m.edit != nil {
		t.Errorf("saved = %v, editor open %t", f.saved, m.edit != nil)
	}
}

func TestEditorEscAsksBeforeDiscardingChanges(t *testing.T) {
	m, f, _ := editorOn(t, 104, tagwrite.Values{tagwrite.Title: {"Live"}})
	// Keys that mean something on the tabs are text here.
	typeText(t, m, "q1?")
	if m.edit == nil || m.edit.title != "Liveq1?" || m.help {
		t.Fatalf("typing did not go to the field: title %q, help %t", m.edit.title, m.help)
	}
	hit(t, m, "esc")
	if m.edit == nil || !strings.Contains(screen(m), "Unsaved changes") {
		t.Fatalf("esc with changes did not ask:\n%s", screen(m))
	}
	hit(t, m, "x") // any other key goes on editing
	if strings.Contains(screen(m), "Unsaved changes") {
		t.Error("the question outlived the next key")
	}
	hit(t, m, "esc", "esc")
	if m.edit != nil || len(f.saved) != 0 {
		t.Errorf("esc twice: editor open %t, saved %v", m.edit != nil, f.saved)
	}

	// With nothing changed esc leaves at once.
	hit(t, m, "e", "esc")
	if m.edit != nil {
		t.Error("esc with nothing changed did not close the editor")
	}
}

func TestEditorRefusesWhatCannotBeSaved(t *testing.T) {
	m, f, _ := editorOn(t, 104, tagwrite.Values{tagwrite.Title: {"Live"}})
	hit(t, m, "down", "down", "down", "down")
	typeText(t, m, "199")
	hit(t, m, "ctrl+s")
	if len(f.saved) != 0 || m.edit == nil || m.toastOK || !strings.Contains(m.toast, "YYYY") {
		t.Errorf("a bad year: saved %v, toast %q", f.saved, m.toast)
	}
	typeText(t, m, "9")

	// A save the file refused keeps the editor open on what was typed.
	f.saveEvents = []engine.Event{engine.FileError{Path: m.edit.file.Path, Op: engine.OpSaveTags, Err: errors.New("it is gone or has changed since it was indexed")}}
	f.saveFinish = engine.RunFinished{State: engine.StateErrors, Counters: engine.Counters{Errors: 1}}
	hit(t, m, "ctrl+s")
	if len(f.saved) != 1 || m.edit == nil || m.edit.saving || m.toastOK || !strings.Contains(m.toast, "has changed since it was indexed") {
		t.Errorf("a refused save: saved %v, editor open %t, toast %q", f.saved, m.edit != nil, m.toast)
	}
	if m.edit.date != "1999" {
		t.Errorf("the year typed was lost: %q", m.edit.date)
	}

	m.header.DryRun = true
	hit(t, m, "ctrl+s")
	if len(f.saved) != 1 || !strings.Contains(m.toast, "Dry run") {
		t.Errorf("a save in a dry run: saved %v, toast %q", f.saved, m.toast)
	}
}

func TestEditorDoesNotOpenOnWhatItCannotTag(t *testing.T) {
	src := untaggedSource()
	src.snap.Untagged[2].Path = `D:\Music\_Untagged\demo.xyz`
	m, _ := newUntaggedModel(t, src)
	cursorTo(t, m, 103)
	hit(t, m, "e")
	if m.edit != nil || m.toastOK || !strings.Contains(m.toast, "xyz") {
		t.Errorf("editor open %t, toast %q", m.edit != nil, m.toast)
	}

	// Tags that cannot be read close it again.
	src.err = errors.New("the disk is gone")
	cursorTo(t, m, 104)
	hit(t, m, "e")
	if m.edit != nil || !strings.Contains(m.toast, "the disk is gone") {
		t.Errorf("after a failed read: editor open %t, toast %q", m.edit != nil, m.toast)
	}
}

func TestGoldenEditor(t *testing.T) {
	m, _, _ := editorOn(t, 104, tagwrite.Values{
		tagwrite.Title: {"Live"}, tagwrite.Artist: {"Hand Tagged"}, tagwrite.Marker: {"hand 2026-09-01"},
	})
	hit(t, m, "down", "down", "down")
	typeText(t, m, "Tape")
	got := screen(m)
	checkFits(t, got, 120, 32)
	golden(t, filepath.Join("testdata", "editor.golden"), got)
}

// The editor's save writes the fields that changed and the marker, and
// leaves the rest of the file's tags alone.
func TestEditSaveWritesChangesAndTheMarker(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "ripped.flac")
	data, err := os.ReadFile(fixtures.Path(fixtures.RipperFLAC))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(filepath.Join(dir, "tagshelf.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	run, err := st.StartRun(store.KindTUIAction, false, "", dir, time.Now())
	if err != nil {
		t.Fatal(err)
	}

	before, err := librarySource{}.Tags(path)
	if err != nil || firstValue(before[tagwrite.Title]) != fixtures.Title || firstValue(before[tagwrite.Marker]) != "" {
		t.Fatalf("tags before = %v, %v", before, err)
	}
	day := time.Date(2026, 10, 8, 15, 0, 0, 0, time.UTC)
	values := tagwrite.Values{tagwrite.Album: {"Another Album"}, tagwrite.Track: {"7/9"}}
	if err := editSave(values, day)(st, run, store.File{Path: path}); err != nil {
		t.Fatal(err)
	}
	after, err := librarySource{}.Tags(path)
	if err != nil {
		t.Fatal(err)
	}
	want := map[tagwrite.Field]string{
		tagwrite.Album: "Another Album", tagwrite.Track: "7/9", tagwrite.Marker: "hand 2026-10-08",
		tagwrite.Title: fixtures.Title, tagwrite.Artist: fixtures.Artist,
	}
	for field, value := range want {
		if got := firstValue(after[field]); got != value {
			t.Errorf("%s after the save = %q, want %q", field, got, value)
		}
	}
	if writes, err := st.TagWrites(run); err != nil || len(writes) != 1 || !strings.Contains(writes[0].After, "Another Album") {
		t.Errorf("tag writes = %+v, %v", writes, err)
	}
}

// On a short terminal the form scrolls, and the lines under it still show.
func TestEditorFitsAShortTerminal(t *testing.T) {
	m, _, _ := editorOn(t, 104, tagwrite.Values{tagwrite.Title: {"Live"}})
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 16})
	hit(t, m, "down", "down", "down", "down", "down", "down")
	typeText(t, m, "2")
	got := screen(m)
	checkFits(t, got, 80, 16)
	for _, want := range []string{"Disc", "> 2", "Marker:", "Will stay in _Untagged: needs artist, album", "ctrl+s save"} {
		if !strings.Contains(got, want) {
			t.Errorf("the short editor lacks %q:\n%s", want, got)
		}
	}
}

// Enter on the last field saves; when the save is refused the form is still
// there to go on with.
func TestEditorEnterSaveRefusedKeepsTheForm(t *testing.T) {
	m, f, _ := editorOn(t, 104, tagwrite.Values{tagwrite.Title: {"Live"}})
	m.header.DryRun = true
	hit(t, m, "enter", "enter", "enter", "enter", "enter", "enter", "enter", "tab", "down", "enter")
	if len(f.saved) != 0 || !strings.Contains(m.toast, "Dry run") {
		t.Fatalf("saved %v, toast %q", f.saved, m.toast)
	}
	if got := screen(m); !strings.Contains(got, "Album artist") || !strings.Contains(got, "Compilation") {
		t.Fatalf("the form is gone after a refused save:\n%s", got)
	}
	m.header.DryRun = false
	hit(t, m, "up")
	typeText(t, m, "2")
	hit(t, m, "ctrl+s")
	if len(f.saved) != 1 {
		t.Errorf("the form could not be saved afterwards: toast %q", m.toast)
	}
}

// Values the file already has are not the editor's to refuse, however odd;
// and words in a path are not taken for missing tags.
func TestEditorLeavesOddValuesItDoesNotWrite(t *testing.T) {
	m, f, _ := editorOn(t, 104, tagwrite.Values{
		tagwrite.Title: {"Live"}, tagwrite.Artist: {"Hand Tagged"}, tagwrite.Album: {"All She Needs"},
		tagwrite.Date: {"2004-05"}, tagwrite.Track: {"A1"},
	})
	// Past the odd year and track, to the disc.
	hit(t, m, "down", "down", "down", "down", "down", "down")
	typeText(t, m, "1")
	if got := m.edit.changes(); !reflect.DeepEqual(got, tagwrite.Values{tagwrite.Disc: {"1"}}) {
		t.Fatalf("changes = %v, want only the disc", got)
	}
	if _, lacking := m.willGoTo(m.edit); lacking {
		t.Error("an album called All She Needs reads as lacking tags")
	}
	f.saveFinish = engine.RunFinished{State: engine.StateOK, Counters: engine.Counters{LeftInPlace: 1}}
	hit(t, m, "ctrl+s")
	if len(f.saved) != 1 || m.toast != "Saved" {
		t.Errorf("saved %v, toast %q", f.saved, m.toast)
	}
}

// A WMA file has no compilation flag to edit; enter on its last field saves.
func TestEditorHidesCompilationForWMA(t *testing.T) {
	src := untaggedSource()
	src.snap.Untagged[2].Path = `D:\Music\_Untagged\demo.wma`
	m, f := newUntaggedModel(t, src)
	cursorTo(t, m, 103)
	hit(t, m, "e")
	if got := screen(m); strings.Contains(got, "Compilation") || !strings.Contains(got, "Disc") {
		t.Fatalf("the WMA editor:\n%s", got)
	}
	hit(t, m, "enter", "enter", "enter", "enter", "enter", "enter", "enter")
	if len(f.saved) != 1 {
		t.Errorf("enter on the last field saved %v", f.saved)
	}
}

// After a save that went wrong part way, the form is compared with the tags
// the file has now.
func TestEditorRereadsTagsAfterAFailedSave(t *testing.T) {
	m, f, src := editorOn(t, 104, tagwrite.Values{tagwrite.Artist: {"A"}})
	hit(t, m, "down", "backspace")
	typeText(t, m, "B")
	path := m.edit.file.Path
	// The save writes B, then the run fails on the file.
	src.tags[path] = tagwrite.Values{tagwrite.Artist: {"B"}}
	f.saveEvents = []engine.Event{engine.FileError{Path: path, Op: engine.OpMove, Err: errors.New("access denied")}}
	f.saveFinish = engine.RunFinished{State: engine.StateErrors, Counters: engine.Counters{Errors: 1}}
	hit(t, m, "ctrl+s")
	if m.edit == nil || len(m.edit.changes()) != 0 {
		t.Fatalf("after the failed save: editor open %t, changes %v", m.edit != nil, m.edit.changes())
	}
	hit(t, m, "backspace")
	typeText(t, m, "A")
	if got := m.edit.changes(); !reflect.DeepEqual(got, tagwrite.Values{tagwrite.Artist: {"A"}}) {
		t.Errorf("changes = %v, want the artist put back to A", got)
	}
}
