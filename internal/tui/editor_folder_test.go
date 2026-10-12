package tui

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/zachjamesgreen/tagshelf/internal/engine"
	"github.com/zachjamesgreen/tagshelf/internal/fixtures"
	"github.com/zachjamesgreen/tagshelf/internal/store"
	"github.com/zachjamesgreen/tagshelf/internal/tagwrite"
)

// folderEditor opens the editor on the first of two ripper's files in one
// folder of the library, beside a file tagshelf cannot tag.
func folderEditor(t *testing.T) (m *Model, f *fakeRunner, files []store.File) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "Album")
	src := untaggedSource()
	src.tags = map[string]tagwrite.Values{}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for i, name := range []string{"one.flac", "two.flac"} {
		path := copyFixture(t, dir, fixtures.RipperFLAC, name)
		files = append(files, store.File{ID: int64(i + 1), RootKind: store.RootLibrary, Path: path})
		src.tags[path] = readTags(t, path)
	}
	src.library = append(files, store.File{ID: 3, RootKind: store.RootLibrary, Path: filepath.Join(dir, "three.wv")})
	m, f = newUntaggedModel(t, src)
	pump(t, m, m.openEditor(files[0]))
	if m.edit == nil || m.edit.form == nil {
		t.Fatalf("the editor did not open: toast %q", m.toast)
	}
	return m, f, files
}

// ctrl+f on a field asks before it writes that field to every file of the
// folder that tagshelf can tag, and y then does it as one save of them all.
// A genre written this way marks none of them.
func TestEditorWritesAFieldToTheWholeFolder(t *testing.T) {
	m, f, files := folderEditor(t)
	toField(t, m, tagwrite.Genre)
	typeText(t, m, "Jazz; Blues")
	hit(t, m, "ctrl+f")
	if got := screen(m); !strings.Contains(got, `Write the genre "Jazz; Blues" to all 2 files in this folder? y writes it`) {
		t.Fatalf("the editor does not ask:\n%s", got)
	}
	if len(f.saved) != 0 {
		t.Fatalf("saved %v before the answer", f.saved)
	}

	f.saveFinish = engine.RunFinished{State: engine.StateOK, RunID: 9}
	hit(t, m, "y")
	if !reflect.DeepEqual(f.saved, [][]int64{{1, 2}}) {
		t.Fatalf("saved %v, want the two files that can be tagged, as one save", f.saved)
	}
	if m.edit != nil || !m.toastOK || !strings.Contains(m.toast, "Genre written to 2 files") || !strings.Contains(m.toast, "u undo") {
		t.Errorf("after the write: editor open %t, toast %q (ok %t)", m.edit != nil, m.toast, m.toastOK)
	}

	st, run := realRun(t, filepath.Dir(files[0].Path))
	for _, file := range files {
		if err := f.saves[0](st, run, file); err != nil {
			t.Fatal(err)
		}
		checkTags(t, file.Path, map[tagwrite.Field]string{tagwrite.Genre: "Jazz, Blues", tagwrite.Marker: "", tagwrite.Title: fixtures.Title})
	}
}

// An album written to the folder is a tag the files are filed under: the
// save marks them.
func TestEditorFolderWriteOfTheAlbumMarksTheFiles(t *testing.T) {
	m, f, files := folderEditor(t)
	toField(t, m, tagwrite.Album)
	typeText(t, m, " II")
	hit(t, m, "ctrl+f")
	f.saveFinish = engine.RunFinished{State: engine.StateOK}
	hit(t, m, "y")
	if len(f.saves) != 1 {
		t.Fatalf("saves = %d:\n%s", len(f.saves), screen(m))
	}
	st, run := realRun(t, filepath.Dir(files[0].Path))
	if err := f.saves[0](st, run, files[1]); err != nil {
		t.Fatal(err)
	}
	if got := readTags(t, files[1].Path); got[tagwrite.Album][0] != fixtures.Album+" II" || len(got[tagwrite.Marker]) != 1 {
		t.Errorf("the other file reads album %q, marker %q", got[tagwrite.Album], got[tagwrite.Marker])
	}
}

// Any key but y leaves the folder alone and the form as it was, and the key
// is not typed into the field.
func TestEditorFolderWriteCanBeDeclined(t *testing.T) {
	m, f, _ := folderEditor(t)
	toField(t, m, tagwrite.Composer)
	typeText(t, m, "Ann")
	hit(t, m, "ctrl+f")
	if !strings.Contains(screen(m), "Write the composer") {
		t.Fatalf("the editor does not ask:\n%s", screen(m))
	}
	hit(t, m, "n")
	if len(f.saved) != 0 || m.edit == nil || m.edit.composer != "Ann" || strings.Contains(screen(m), "Write the composer") {
		t.Errorf("after n: saved %v, composer %q:\n%s", f.saved, m.edit.composer, screen(m))
	}
	// An emptied field asks to take the tag off.
	hit(t, m, "ctrl+u", "ctrl+f")
	if got := screen(m); !strings.Contains(got, "Take the composer off all 2 files in this folder?") {
		t.Errorf("the editor does not ask to take the tag off:\n%s", got)
	}
}

// What the key refuses: a field that is one file's own, and a form with
// other changes, which the write would lose when it closes the editor.
func TestEditorFolderWriteRefusals(t *testing.T) {
	m, f, _ := folderEditor(t)
	hit(t, m, "ctrl+f") // on the title
	if !strings.Contains(m.toast, "Only the album artist, album, year, genre, composer and comment") {
		t.Errorf("on the title: toast %q", m.toast)
	}
	typeText(t, m, "!")
	toField(t, m, tagwrite.Genre)
	typeText(t, m, "Jazz")
	hit(t, m, "ctrl+f")
	if !strings.Contains(m.toast, "Save the other changes first") || m.edit.folder != nil || len(f.saved) != 0 {
		t.Errorf("with the title changed too: toast %q, saved %v", m.toast, f.saved)
	}

	m, f, _ = folderEditor(t)
	m.header.DryRun = true
	toField(t, m, tagwrite.Genre)
	hit(t, m, "ctrl+f")
	if !strings.Contains(m.toast, "Dry run") || m.edit.folder != nil || len(f.saved) != 0 {
		t.Errorf("in a dry run: toast %q", m.toast)
	}
}
