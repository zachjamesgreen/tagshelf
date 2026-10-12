package tui

import (
	"reflect"
	"strings"
	"testing"

	"github.com/zachjamesgreen/tagshelf/internal/engine"
	"github.com/zachjamesgreen/tagshelf/internal/fixtures"
	"github.com/zachjamesgreen/tagshelf/internal/store"
	"github.com/zachjamesgreen/tagshelf/internal/tagwrite"
)

// toRow moves the editor's cursor from the title to the row for field.
func toRow(t *testing.T, m *Model, field tagwrite.Field) {
	t.Helper()
	for range 80 {
		entry, _, ok := m.edit.focusedEntry()
		if ok && entry.id == field {
			return
		}
		hit(t, m, "tab")
	}
	t.Fatalf("the form has no row for %s:\n%s", field, screen(m))
}

const (
	anID      = "aaaaaaaa-0000-4000-8000-000000000001"
	anotherID = "aaaaaaaa-0000-4000-8000-000000000002"
)

// A comment of several lines is edited in a box of several lines, where
// enter breaks the line. Left alone it is not saved, though the box holds
// its line breaks in its own way.
func TestEditorEditsACommentOfSeveralLines(t *testing.T) {
	m, _, _ := editorOn(t, 104, tagwrite.Values{tagwrite.Title: {"Live"}, tagwrite.Comment: {"first line\r\nsecond\nthird"}})
	got := screen(m)
	for _, want := range []string{"Comment", "first line", "second", "third"} {
		if !strings.Contains(got, want) {
			t.Errorf("the editor lacks %q:\n%s", want, got)
		}
	}
	if got := m.edit.changes(); len(got) != 0 {
		t.Errorf("changes before any typing = %q", got)
	}
	toRow(t, m, tagwrite.Comment)
	hit(t, m, "ctrl+end")
	typeText(t, m, "!")
	hit(t, m, "enter")
	typeText(t, m, "fourth")
	want := tagwrite.Values{tagwrite.Comment: {"first line\nsecond\nthird!\nfourth"}}
	if got := m.edit.changes(); !reflect.DeepEqual(got, want) {
		t.Errorf("changes = %q, want %q", got, want)
	}
	// Tab leaves the box; enter did not.
	hit(t, m, "tab")
	if entry, _, ok := m.edit.focusedEntry(); ok && entry.id == tagwrite.Comment {
		t.Error("tab did not leave the comment's box")
	}
}

// The editor lists the file's other tags by name, its IDs and its marker,
// and what is typed over them is what a save is given: a list split at its
// semicolons, an emptied tag with no values, the marker as a marker set by
// hand.
func TestEditorRowsOfOtherTagsIDsAndTheMarker(t *testing.T) {
	m, _, _ := editorOn(t, 104, tagwrite.Values{
		tagwrite.Title: {"Live"}, tagwrite.Marker: {"hand 2026-09-01"},
		tagwrite.Other("MOOD"): {"calm"}, tagwrite.Other("LABEL"): {"Warp", "Skam"}, tagwrite.Other("URL"): {"https://a.example/x;y"},
		tagwrite.RecordingID: {anID}, tagwrite.ArtistIDs: {anID, anotherID},
		tagwrite.NotText: {"APIC", "PRIV"},
	})
	m.Update(windowSize(120, 60))
	got := screen(m)
	for _, want := range []string{"MOOD", "calm", "LABEL", "Warp; Skam", "URL", "https://a.example/x;y", "Recording ID", anID,
		"Artist IDs", anID + "; " + anotherID, "AcoustID ID", "Marker", "hand 2026-09-01", "Not text:", "APIC, PRIV, kept as it is"} {
		if !strings.Contains(got, want) {
			t.Errorf("the editor lacks %q:\n%s", want, got)
		}
	}
	if got := m.edit.changes(); len(got) != 0 {
		t.Fatalf("changes before any typing = %q", got)
	}

	toRow(t, m, tagwrite.Other("LABEL"))
	hit(t, m, "ctrl+u")
	typeText(t, m, "Warp;Ninja Tune ; ")
	toRow(t, m, tagwrite.Other("MOOD"))
	hit(t, m, "ctrl+u")
	// One value with a semicolon in it stays one value.
	toRow(t, m, tagwrite.Other("URL"))
	typeText(t, m, "z")
	toRow(t, m, tagwrite.ArtistIDs)
	hit(t, m, "ctrl+u")
	typeText(t, m, anotherID)
	toRow(t, m, tagwrite.SetMarker)
	hit(t, m, "ctrl+u")
	typeText(t, m, "musicbrainz 2020-01-02")

	want := tagwrite.Values{
		tagwrite.Other("LABEL"): {"Warp", "Ninja Tune"}, tagwrite.Other("MOOD"): {}, tagwrite.Other("URL"): {"https://a.example/x;yz"},
		tagwrite.ArtistIDs: {anotherID}, tagwrite.SetMarker: {"musicbrainz 2020-01-02"},
	}
	if got := m.edit.changes(); !reflect.DeepEqual(got, want) {
		t.Errorf("changes = %q\nwant      %q", got, want)
	}
	if err := m.edit.invalid(); err != nil {
		t.Errorf("the form is refused: %v", err)
	}
}

// What the rows refuse: an ID that does not have the form of one, and a
// marker that says neither how nor when. What the file already has is never
// refused, however odd.
func TestEditorRowsRefuseWhatIsNoIDAndNoMarker(t *testing.T) {
	m, f, _ := editorOn(t, 104, tagwrite.Values{tagwrite.Title: {"Live"}, tagwrite.RecordingID: {"not-an-id"}, tagwrite.Marker: {"someone 1999"}})
	if err := m.edit.invalid(); err != nil {
		t.Errorf("the file's own odd values are refused: %v", err)
	}
	toRow(t, m, tagwrite.AlbumID)
	typeText(t, m, "rel-1")
	hit(t, m, "ctrl+s")
	if len(f.saved) != 0 || !strings.Contains(m.toast, `Release ID: "rel-1" does not have the form of an ID`) {
		t.Errorf("an ID that is none: saved %v, toast %q", f.saved, m.toast)
	}
	hit(t, m, "ctrl+u")
	toRow(t, m, tagwrite.SetMarker)
	hit(t, m, "ctrl+u")
	typeText(t, m, "tomorrow")
	hit(t, m, "ctrl+s")
	if len(f.saved) != 0 || !strings.Contains(m.toast, "a marker is hand or musicbrainz and a day") {
		t.Errorf("a marker that is none: saved %v, toast %q", f.saved, m.toast)
	}
}

// ctrl+n asks for a name and adds a row for the tag, with the cursor on it.
// A name tagshelf does not take is refused where it is typed.
func TestEditorAddsATag(t *testing.T) {
	m, _, _ := editorOn(t, 104, tagwrite.Values{tagwrite.Title: {"Live"}, tagwrite.Other("LABEL"): {"Warp"}})
	typeText(t, m, "!")
	hit(t, m, "ctrl+n")
	if got := screen(m); !strings.Contains(got, "Name of the new tag:") {
		t.Fatalf("the editor does not ask for the name:\n%s", got)
	}
	typeText(t, m, "title")
	hit(t, m, "enter")
	if !strings.Contains(m.toast, "TITLE has a field of its own") || m.edit.naming == nil {
		t.Errorf("a name with a field of its own: toast %q", m.toast)
	}
	hit(t, m, "ctrl+u")
	typeText(t, m, "my mood")
	hit(t, m, "enter")
	if m.edit.naming != nil || !strings.Contains(screen(m), "MY MOOD") {
		t.Fatalf("the tag was not added:\n%s", screen(m))
	}
	if entry, _, ok := m.edit.focusedEntry(); !ok || entry.id != tagwrite.Other("MY MOOD") {
		t.Errorf("the cursor is on %q, want the new tag", entry.id)
	}
	// What was typed before is still there, and an added tag left empty is
	// nothing to save.
	if got, want := m.edit.changes(), (tagwrite.Values{tagwrite.Title: {"Live!"}}); !reflect.DeepEqual(got, want) {
		t.Errorf("changes with the new tag empty = %q, want %q", got, want)
	}
	typeText(t, m, "calm")
	if got := m.edit.changes()[tagwrite.Other("MY MOOD")]; !reflect.DeepEqual(got, []string{"calm"}) {
		t.Errorf("the new tag would be saved as %q", got)
	}

	// Esc at the name gives it up; the name of a tag that is there goes to it.
	hit(t, m, "ctrl+n")
	typeText(t, m, "x")
	hit(t, m, "esc")
	if m.edit == nil || m.edit.naming != nil || strings.Contains(screen(m), "Name of the new tag") {
		t.Fatalf("esc at the name did not give it up:\n%s", screen(m))
	}
	hit(t, m, "ctrl+n")
	typeText(t, m, "label")
	hit(t, m, "enter")
	if entry, _, ok := m.edit.focusedEntry(); !ok || entry.id != tagwrite.Other("LABEL") {
		t.Errorf("naming a tag that is there put the cursor on %q", entry.id)
	}
}

// A file with a ripper's tags and no marker: a marker typed by hand makes
// it qualify, and is saved as typed, with no other of the save's own. An ID
// changed by hand marks the file.
func TestEditorSavesAMarkerSetByHand(t *testing.T) {
	dir := t.TempDir()
	path := copyFixture(t, dir, fixtures.RipperFLAC, "ripped.flac")
	src := untaggedSource()
	src.snap.Untagged[2].Path = path // file 103
	src.tags = map[string]tagwrite.Values{path: readAllTags(t, path)}
	m, f := newUntaggedModel(t, src)
	cursorTo(t, m, 103)
	hit(t, m, "e")
	toRow(t, m, tagwrite.Genre)
	typeText(t, m, "Jazz")
	if got := screen(m); !strings.Contains(got, "Will stay in _Untagged") {
		t.Fatalf("with a genre alone the file would not stay:\n%s", got)
	}
	toRow(t, m, tagwrite.SetMarker)
	typeText(t, m, "hand 2020-01-02")
	if got := screen(m); !strings.Contains(got, "Will go to:") {
		t.Errorf("with a marker typed the editor does not say where the file would go:\n%s", got)
	}
	f.saveFinish = engine.RunFinished{State: engine.StateOK}
	hit(t, m, "ctrl+s")

	st, run := realRun(t, dir)
	if err := theSave(t, f, 103)(st, run, store.File{ID: 103, Path: path}); err != nil {
		t.Fatal(err)
	}
	checkTags(t, path, map[tagwrite.Field]string{tagwrite.Marker: "hand 2020-01-02", tagwrite.Genre: "Jazz"})

	// An ID typed by hand is a change to where the file belongs.
	if !marks(tagwrite.Values{tagwrite.AlbumID: {anID}}) || marks(tagwrite.Values{tagwrite.AcoustID: {anID}, tagwrite.Other("MOOD"): {"x"}}) {
		t.Error("a MusicBrainz ID changed by hand must mark the file, and an AcoustID ID or another tag must not")
	}
}

// The marker cannot be taken off a WMA file: the editor says so before
// anything is saved.
func TestEditorCannotTakeTheMarkerOffAWMAFile(t *testing.T) {
	src := untaggedSource()
	src.snap.Untagged[3].Path = p(`D:\Music\_Untagged\live.wma`)
	src.tags = map[string]tagwrite.Values{src.snap.Untagged[3].Path: {tagwrite.Title: {"Live"}, tagwrite.Marker: {"hand 2026-09-01"}}}
	m, f := newUntaggedModel(t, src)
	cursorTo(t, m, 104)
	hit(t, m, "e")
	toRow(t, m, tagwrite.SetMarker)
	hit(t, m, "ctrl+u", "ctrl+s")
	if len(f.saved) != 0 || !strings.Contains(m.toast, "cannot take the marker off a WMA file") {
		t.Errorf("saved %v, toast %q", f.saved, m.toast)
	}
}

// ctrl+f writes another tag to the whole folder, as it does a genre.
func TestEditorWritesAnotherTagToTheWholeFolder(t *testing.T) {
	m, f, _ := folderEditor(t)
	hit(t, m, "ctrl+n")
	typeText(t, m, "label")
	hit(t, m, "enter")
	typeText(t, m, "Warp")
	hit(t, m, "ctrl+f")
	if got := screen(m); !strings.Contains(got, `Write the tag LABEL "Warp" to all 2 files in this folder?`) {
		t.Fatalf("the editor does not ask:\n%s", got)
	}
	f.saveFinish = engine.RunFinished{State: engine.StateOK}
	hit(t, m, "y")
	if len(f.saved) != 1 || !strings.Contains(m.toast, "Tag LABEL written to 2 files") {
		t.Errorf("saved %v, toast %q", f.saved, m.toast)
	}
	// Not an ID, and not the marker.
	m, _, _ = folderEditor(t)
	toRow(t, m, tagwrite.AlbumID)
	hit(t, m, "ctrl+f")
	if !strings.Contains(m.toast, "Only the album artist") || m.edit.folder != nil {
		t.Errorf("on an ID: toast %q", m.toast)
	}
}

// readAllTags reads every tag of the file at path, as the editor is given
// them.
func readAllTags(t *testing.T, path string) tagwrite.Values {
	t.Helper()
	v, err := tagwrite.ReadAll(path)
	if err != nil {
		t.Fatal(err)
	}
	return v
}
