package tui

import (
	"reflect"
	"strings"
	"testing"

	"github.com/zachjamesgreen/tagshelf/internal/fixtures"
	"github.com/zachjamesgreen/tagshelf/internal/store"
	"github.com/zachjamesgreen/tagshelf/internal/tagwrite"
)

// toField moves the editor's cursor from the title to the field with the
// given place in the form.
func toField(t *testing.T, m *Model, field tagwrite.Field) {
	t.Helper()
	for _, f := range editorFields {
		if f == field {
			return
		}
		hit(t, m, "down")
	}
	t.Fatalf("the form has no field %s", field)
}

// A genre and a composer typed for a ripper's file are saved to it, each
// list a value to a tag, and the file is not marked: it stays in _Untagged,
// and the editor says so before the save.
func TestEditorSavesAGenreWithoutMarkingTheFile(t *testing.T) {
	dir := t.TempDir()
	path := copyFixture(t, dir, fixtures.RipperFLAC, "ripped.flac")
	src := untaggedSource()
	src.snap.Untagged[2].Path = path // file 103
	src.tags = map[string]tagwrite.Values{path: readTags(t, path)}
	m, f := newUntaggedModel(t, src)
	cursorTo(t, m, 103)
	hit(t, m, "e")
	if m.edit == nil || m.edit.form == nil {
		t.Fatalf("the editor did not open:\n%s", screen(m))
	}
	if got := screen(m); !strings.Contains(got, "Will go to:") {
		t.Fatalf("before any typing a save would mark the file, and the editor should say where it would go:\n%s", got)
	}

	toField(t, m, tagwrite.Genre)
	typeText(t, m, " Rock ;Hip Hop;; ")
	hit(t, m, "down")
	typeText(t, m, "Ann Lee")
	hit(t, m, "down")
	typeText(t, m, "a note; with a semicolon")

	want := tagwrite.Values{tagwrite.Genre: {"Rock", "Hip Hop"}, tagwrite.Composer: {"Ann Lee"}, tagwrite.Comment: {"a note; with a semicolon"}}
	if got := m.edit.changes(); !reflect.DeepEqual(got, want) {
		t.Errorf("changes = %v, want %v", got, want)
	}
	if got := screen(m); !strings.Contains(got, "Will stay in _Untagged: it is not marked as tagged by hand") {
		t.Errorf("the editor does not say the file would stay:\n%s", got)
	}
	hit(t, m, "ctrl+s")

	st, run := realRun(t, dir)
	if err := theSave(t, f, 103)(st, run, store.File{ID: 103, Path: path}); err != nil {
		t.Fatal(err)
	}
	checkTags(t, path, map[tagwrite.Field]string{
		tagwrite.Genre: "Rock, Hip Hop", tagwrite.Composer: "Ann Lee", tagwrite.Comment: "a note; with a semicolon",
		tagwrite.Title: fixtures.Title, tagwrite.Marker: "",
	})
	if writes, err := st.TagWrites(run); err != nil || len(writes) != 1 || strings.Contains(writes[0].After, "marker") {
		t.Errorf("tag writes = %+v, %v; want the one save recorded, without the marker", writes, err)
	}
}

// A change to a tag the file is filed under marks it, whatever else the same
// save changes.
func TestEditorSaveOfATitleAndAGenreMarksTheFile(t *testing.T) {
	dir := t.TempDir()
	path := copyFixture(t, dir, fixtures.RipperFLAC, "ripped.flac")
	src := untaggedSource()
	src.snap.Untagged[2].Path = path
	src.tags = map[string]tagwrite.Values{path: readTags(t, path)}
	m, f := newUntaggedModel(t, src)
	cursorTo(t, m, 103)
	hit(t, m, "e")
	typeText(t, m, "!")
	toField(t, m, tagwrite.Genre)
	typeText(t, m, "Jazz")
	if got := screen(m); !strings.Contains(got, "Will go to:") {
		t.Errorf("the editor does not say where the file would go:\n%s", got)
	}
	hit(t, m, "ctrl+s")

	st, run := realRun(t, dir)
	if err := theSave(t, f, 103)(st, run, store.File{ID: 103, Path: path}); err != nil {
		t.Fatal(err)
	}
	if got := readTags(t, path); len(got[tagwrite.Marker]) != 1 || !strings.HasPrefix(got[tagwrite.Marker][0], "hand ") ||
		!reflect.DeepEqual(got[tagwrite.Genre], []string{"Jazz"}) {
		t.Errorf("after the save: marker %q, genre %q", got[tagwrite.Marker], got[tagwrite.Genre])
	}
}

// The lists a file holds are shown joined, and showing them changes nothing:
// only what is typed is saved. One value with a semicolon in it stays one
// value for as long as it is left alone.
func TestEditorShowsListsJoined(t *testing.T) {
	m, _, _ := editorOn(t, 104, tagwrite.Values{
		tagwrite.Title: {"Live"}, tagwrite.Genre: {"Rock", "Hip Hop"}, tagwrite.Composer: {"Ann; Bob"},
	})
	if got := screen(m); !strings.Contains(got, "Rock; Hip Hop") || !strings.Contains(got, "Ann; Bob") {
		t.Fatalf("the editor does not show the lists:\n%s", got)
	}
	if got := m.edit.changes(); len(got) != 0 {
		t.Errorf("changes before any typing = %v", got)
	}
	toField(t, m, tagwrite.Genre)
	hit(t, m, "backspace", "backspace", "backspace", "backspace", "backspace", "backspace", "backspace", "backspace", "backspace")
	want := tagwrite.Values{tagwrite.Genre: {"Rock"}}
	if got := m.edit.changes(); !reflect.DeepEqual(got, want) {
		t.Errorf("changes = %v, want %v", got, want)
	}
	hit(t, m, "ctrl+u")
	if got, want := m.edit.changes(), (tagwrite.Values{tagwrite.Genre: {}}); !reflect.DeepEqual(got, want) {
		t.Errorf("with the genre emptied, changes = %v, want %v", got, want)
	}
}

// A comment of several lines has no field in the form, which would run its
// lines together: it is shown by its first line and never saved over.
func TestEditorLeavesACommentOfSeveralLinesAlone(t *testing.T) {
	m, _, _ := editorOn(t, 104, tagwrite.Values{tagwrite.Title: {"Live"}, tagwrite.Comment: {"first line\r\nsecond\nthird"}})
	got := screen(m)
	if !strings.Contains(got, "first line") || !strings.Contains(got, "(3 lines; left as it is)") || strings.Contains(got, "second") {
		t.Errorf("the editor does not show the comment by its first line:\n%s", got)
	}
	typeText(t, m, "!")
	if got, want := m.edit.changes(), (tagwrite.Values{tagwrite.Title: {"Live!"}}); !reflect.DeepEqual(got, want) {
		t.Errorf("changes = %v, want %v", got, want)
	}
	// The form ends a field earlier: enter on the composer saves.
	if m.edit.form.GetFocusedField() == m.edit.last {
		t.Fatal("the title is the form's last field")
	}
}

// TestMarks checks which saves of the editor mark a file.
func TestMarks(t *testing.T) {
	tests := []struct {
		name   string
		values tagwrite.Values
		want   bool
	}{
		{"the form as it stands, which vouches for the tags", tagwrite.Values{}, true},
		{"a title", tagwrite.Values{tagwrite.Title: {"x"}}, true},
		{"the compilation flag", tagwrite.Values{tagwrite.Compilation: {}}, true},
		{"a genre alone", tagwrite.Values{tagwrite.Genre: {"Rock"}}, false},
		{"a composer and a comment", tagwrite.Values{tagwrite.Composer: {}, tagwrite.Comment: {"x"}}, false},
		{"a genre and a year", tagwrite.Values{tagwrite.Genre: {"Rock"}, tagwrite.Date: {"2001"}}, true},
	}
	for _, tt := range tests {
		if got := marks(tt.values); got != tt.want {
			t.Errorf("%s: marks = %t, want %t", tt.name, got, tt.want)
		}
	}
}
