package tui

import (
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/zachjamesgreen/tagshelf/internal/engine"
	"github.com/zachjamesgreen/tagshelf/internal/musicbrainz"
	"github.com/zachjamesgreen/tagshelf/internal/tagwrite"
)

// c on a file of the Library tab asks for the cover of its album: the folder
// the file is in. What the run wrote, and from where, is said, with the
// offer to undo it.
func TestLibraryCoverKey(t *testing.T) {
	m, f, _ := libraryModel(t, "a")
	file, ok := m.library.current()
	if !ok {
		t.Fatalf("the search found nothing:\n%s", screen(m))
	}
	dir := filepath.Dir(file.Path)

	// With the setting off the key says where to turn it on.
	hit(t, m, "c")
	if len(f.covered) != 0 || !strings.Contains(m.toast, "Cover art is off: turn on Cover file or Cover in the files on 6 Settings") {
		t.Fatalf("with the setting off: covered %v, toast %q", f.covered, m.toast)
	}

	m.coverFile = true
	cover := filepath.Join(dir, "cover.jpg")
	f.coverEvents = []engine.Event{engine.CoverWritten{Dir: dir, Path: cover, From: engine.CoverFromRelease}}
	f.coverFinish = engine.RunFinished{State: engine.StateOK, RunID: 12, Counters: engine.Counters{Covers: 1}}
	hit(t, m, "c")
	if !reflect.DeepEqual(f.covered, [][]string{{dir}}) {
		t.Fatalf("the run was asked for %v, want the file's folder", f.covered)
	}
	if !m.toastOK || !strings.Contains(m.toast, "Cover written to") || !strings.Contains(m.toast, "from the Cover Art Archive") ||
		!strings.Contains(m.toast, "u undo") || m.undoable != 12 {
		t.Errorf("toast %q (ok %t), undoable %d", m.toast, m.toastOK, m.undoable)
	}

	// Pictures put into the album's files are counted, with the cover file
	// or alone, and can be undone.
	one, two := filepath.Join(dir, "01.flac"), filepath.Join(dir, "02.flac")
	f.coverEvents = []engine.Event{
		engine.CoverWritten{Dir: dir, Path: cover, From: engine.CoverFromRelease},
		engine.CoverEmbedded{Path: one, From: engine.CoverFromFolder}, engine.CoverEmbedded{Path: two, From: engine.CoverFromFolder},
	}
	f.coverFinish = engine.RunFinished{State: engine.StateOK, RunID: 13, Counters: engine.Counters{Covers: 1, Pictures: 2}}
	hit(t, m, "c")
	if !m.toastOK || !strings.Contains(m.toast, "Cover written to") || !strings.Contains(m.toast, ", and put into 2 files") || m.undoable != 13 {
		t.Errorf("with a cover file and pictures: toast %q (ok %t), undoable %d", m.toast, m.toastOK, m.undoable)
	}
	f.coverEvents = []engine.Event{engine.CoverEmbedded{Path: one, From: engine.CoverFromFolder}}
	f.coverFinish = engine.RunFinished{State: engine.StateOK, RunID: 14, Counters: engine.Counters{Pictures: 1}}
	hit(t, m, "c")
	if !m.toastOK || !strings.Contains(m.toast, "Cover put into 1 file") || !strings.Contains(m.toast, "u undo") || m.undoable != 14 {
		t.Errorf("with a picture alone: toast %q (ok %t), undoable %d", m.toast, m.toastOK, m.undoable)
	}

	// A folder left without one says why; one that has a cover says so.
	f.coverEvents = []engine.Event{engine.CoverSkipped{Dir: dir, Why: "the Cover Art Archive has no front cover for its release"}}
	f.coverFinish = engine.RunFinished{State: engine.StateOK}
	hit(t, m, "c")
	if m.toastOK || !strings.Contains(m.toast, "No cover written: the Cover Art Archive has no front cover") || m.undoable != 0 {
		t.Errorf("with none at the archive: toast %q (ok %t)", m.toast, m.toastOK)
	}
	f.coverEvents = nil
	hit(t, m, "c")
	if !strings.Contains(m.toast, "The album has a cover already") {
		t.Errorf("with a cover there already: toast %q", m.toast)
	}

	// What goes wrong is said, and nothing is offered to undo.
	f.coverFinish = engine.RunFinished{State: engine.StateErrors, Counters: engine.Counters{Errors: 1}}
	hit(t, m, "c")
	if m.toastOK || !strings.Contains(m.toast, "The cover could not be got: see 5 Runs") {
		t.Errorf("with an error: toast %q", m.toast)
	}
	f.err = errors.New("the library is locked")
	hit(t, m, "c")
	if m.toastOK || !strings.Contains(m.toast, "the library is locked") {
		t.Errorf("with the run refused: toast %q", m.toast)
	}

	// In a dry run nothing is changed, a cover included.
	f.err = nil
	m.header.DryRun = true
	covered := len(f.covered)
	hit(t, m, "c")
	if len(f.covered) != covered || !strings.Contains(m.toast, "Dry run") {
		t.Errorf("in a dry run: covered %d more times, toast %q", len(f.covered)-covered, m.toast)
	}
}

// A match that is saved is followed by the cover of the album the file went
// to, when the setting is on: its message is added to the match's. With the
// setting off, or nothing written, the match's message stands alone.
func TestCoverFollowsAMatch(t *testing.T) {
	dst := p(`D:\Music\Boards of Canada\Geogaddi (2002)\05 - Julie and Candy.mp3`)
	save := func(t *testing.T, on bool, events []engine.Event) (*Model, *fakeRunner) {
		t.Helper()
		src := untaggedSource()
		src.releases = map[string]bool{"rel-comp": true}
		path := src.snap.Untagged[3].Path // file 104
		src.tags = map[string]tagwrite.Values{path: {}}
		m, f := newUntaggedModel(t, src)
		m.coverFile = on
		m.mb = julie()
		f.saveEvents = []engine.Event{engine.FilePlaced{Path: path, Destination: dst, Outcome: engine.Organized}}
		f.saveFinish = engine.RunFinished{State: engine.StateOK, RunID: 3, Counters: engine.Counters{Organized: 1}}
		f.coverEvents = events
		f.coverFinish = engine.RunFinished{State: engine.StateOK, RunID: 4}
		cursorTo(t, m, 104)
		hit(t, m, "l")
		toAlbum(t, m)
		hit(t, m, "enter", "enter")
		if m.look != nil || len(f.saved) != 1 {
			t.Fatalf("the match was not saved:\n%s", screen(m))
		}
		return m, f
	}

	written := []engine.Event{engine.CoverWritten{Dir: filepath.Dir(dst), Path: filepath.Join(filepath.Dir(dst), "cover.jpg"), From: engine.CoverFromRelease}}
	m, f := save(t, true, written)
	if !reflect.DeepEqual(f.covered, [][]string{{filepath.Dir(dst)}}) {
		t.Fatalf("the cover run was asked for %v, want the folder the file went to", f.covered)
	}
	if !strings.Contains(m.toast, "Saved and moved to") || !strings.Contains(m.toast, "; cover written to") || strings.Count(m.toast, "u undo") != 1 || m.undoable != 4 {
		t.Errorf("toast %q, undoable %d: want the match's message with the cover's, and u for the cover", m.toast, m.undoable)
	}

	m, f = save(t, true, nil)
	if len(f.covered) != 1 || !strings.HasPrefix(m.toast, "Saved and moved to") || strings.Contains(m.toast, "cover") || m.undoable != 3 {
		t.Errorf("with nothing written: toast %q, undoable %d", m.toast, m.undoable)
	}
	m, f = save(t, false, written)
	if len(f.covered) != 0 || strings.Contains(m.toast, "cover") || strings.Contains(m.toast, "Cover") {
		t.Errorf("with the setting off: covered %v, toast %q", f.covered, m.toast)
	}
}

// A release match is followed by the covers of the folders its files went
// to, each folder once.
func TestCoverFollowsAReleaseMatch(t *testing.T) {
	src := matchSource()
	m, f := newUntaggedModel(t, src)
	m.coverFile = true
	mb := geogaddi()
	mb.release.Genres = []musicbrainz.Genre{{Name: "electronic", Count: 1}}
	m.mb = mb
	album := p(`D:\Music\Boards of Canada\Geogaddi (2002)`)
	f.saveEvents = []engine.Event{
		engine.FilePlaced{Destination: filepath.Join(album, "01 - Ready Lets Go.flac"), Outcome: engine.Organized},
		engine.FilePlaced{Destination: filepath.Join(album, "02 - Music Is Math.flac"), Outcome: engine.Organized},
	}
	f.saveFinish = engine.RunFinished{State: engine.StateOK, Counters: engine.Counters{Organized: 2}}
	f.coverFinish = engine.RunFinished{State: engine.StateOK}
	cursorTo(t, m, 101)
	hit(t, m, "L", "enter", "enter")
	if m.match == nil || m.match.step != matchMap {
		t.Fatalf("the release match did not get to the tracks:\n%s", screen(m))
	}
	hit(t, m, "enter", "enter")
	if !reflect.DeepEqual(f.covered, [][]string{{album}}) {
		t.Errorf("the cover run was asked for %v, want the one album folder once", f.covered)
	}
}

func TestAlbumFolders(t *testing.T) {
	a, b := p(`D:\Music\A\One`), p(`D:\Music\B\Two`)
	got := albumFolders([]string{filepath.Join(a, "1.flac"), filepath.Join(b, "1.flac"), filepath.Join(a, "2.flac")})
	if !reflect.DeepEqual(got, []string{a, b}) {
		t.Errorf("albumFolders = %q, want each folder once, in the order met", got)
	}
	if albumFolders(nil) != nil {
		t.Error("no files have folders")
	}
}
