package tui

import (
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"tagshelf/internal/engine"
	"tagshelf/internal/store"
	"tagshelf/internal/tagwrite"
)

// librarySrc is a library holding one album of two tracks filed under a
// misspelt name, and a song by someone else.
func librarySrc() *fakeSource {
	file := func(id int64, rel string, f store.File) store.File {
		f.ID, f.RootKind, f.RelPath = id, store.RootLibrary, filepath.FromSlash(rel)
		f.Path = filepath.Join(`D:\Music`, f.RelPath)
		return f
	}
	album := store.File{Format: "FLAC", Artist: "Boards of Canada", Album: "Geogadi", Year: 2002, Length: 59 * time.Second}
	one, two := album, album
	one.Title, one.Track = "Ready Lets Go", 1
	two.Title, two.Track, two.Length = "Music Is Math", 2, 321*time.Second
	src := &fakeSource{snap: sampleSnapshot()}
	src.library = []store.File{
		file(301, "Boards of Canada/Geogadi (2002)/01 - Ready Lets Go.flac", one),
		file(302, "Boards of Canada/Geogadi (2002)/02 - Music Is Math.flac", two),
		file(303, "Autechre/Amber (1994)/01 - Foil.mp3", store.File{Format: "MP3", Artist: "Autechre", Album: "Amber", Title: "Foil", Track: 1, Year: 1994}),
	}
	src.tags = map[string]tagwrite.Values{}
	for _, f := range src.library {
		src.tags[f.Path] = tagwrite.Values{tagwrite.Title: {f.Title}, tagwrite.Artist: {f.Artist}, tagwrite.Album: {f.Album},
			tagwrite.Date: {fmt.Sprint(f.Year)}, tagwrite.Track: {fmt.Sprint(f.Track)}}
	}
	return src
}

// libraryModel opens the Library tab and searches it for query.
func libraryModel(t *testing.T, query string) (*Model, *fakeRunner, *fakeSource) {
	t.Helper()
	src := librarySrc()
	f := newFakeRunner()
	m := newModel(t, src, 120, 32)
	m.runner = f
	hit(t, m, "4")
	if query != "" {
		hit(t, m, "/")
		typeText(t, m, query)
		hit(t, m, "enter")
	}
	return m, f, src
}

func TestLibrarySearchFindsFiles(t *testing.T) {
	m, _, src := libraryModel(t, "")
	if got := screen(m); !strings.Contains(got, "press / and type an artist, album or title") || !strings.Contains(got, "18,204 files") {
		t.Fatalf("the tab before a search:\n%s", got)
	}
	// Typing is typing: keys that mean something elsewhere are characters.
	hit(t, m, "/")
	typeText(t, m, "q5 geox")
	hit(t, m, "backspace")
	if m.tab != TabLibrary || m.library.query != "q5 geo" || !strings.Contains(screen(m), "Search: q5 geo") {
		t.Fatalf("typed %q on tab %s:\n%s", m.library.query, m.tab, screen(m))
	}
	hit(t, m, "ctrl+u")
	typeText(t, m, "GEOGADI")
	hit(t, m, "enter")
	if !reflect.DeepEqual(src.searches, []string{"GEOGADI"}) {
		t.Fatalf("searched for %v", src.searches)
	}
	got := screen(m)
	for _, want := range []string{`2 files for "GEOGADI"`, "Boards of Canada", "Geogadi", "2002", "01", "Ready Lets Go", "0:59", "FLAC", "Music Is Math", "5:21"} {
		if !strings.Contains(got, want) {
			t.Errorf("the results lack %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "Foil") {
		t.Errorf("the results list a file that does not match:\n%s", got)
	}

	hit(t, m, "/", "ctrl+u")
	typeText(t, m, "zzz")
	hit(t, m, "enter")
	if !strings.Contains(screen(m), `Nothing in the library matches "zzz"`) {
		t.Errorf("a search without results:\n%s", screen(m))
	}
	// esc leaves the query alone; the tab keys work again.
	hit(t, m, "/", "esc", "1")
	if m.tab != TabHome {
		t.Errorf("after esc the tab keys do not work: on %s", m.tab)
	}
}

func TestLibraryResultOpensEditLookupAndReleaseMatch(t *testing.T) {
	m, f, src := libraryModel(t, "geogadi")
	hit(t, m, "down", "e")
	if m.edit == nil || m.edit.file.ID != 302 || !strings.Contains(screen(m), "Library › Edit tags › 02 - Music Is Math.flac") {
		t.Fatalf("e did not open the editor on the result:\n%s", screen(m))
	}
	// Fixing the album's spelling moves the file within the library.
	hit(t, m, "down", "down", "down", "backspace", "backspace")
	typeText(t, m, "ddi")
	want := "Will go to:    " + filepath.Join("Boards of Canada", "Geogaddi (2002)", "02 - Music Is Math.flac")
	if got := screen(m); !strings.Contains(got, want) {
		t.Fatalf("the editor lacks %q:\n%s", want, got)
	}
	dst := `D:\Music\Boards of Canada\Geogaddi (2002)\02 - Music Is Math.flac`
	f.saveEvents = []engine.Event{engine.FilePlaced{Path: m.edit.file.Path, Destination: dst, Outcome: engine.Organized}}
	f.saveFinish = engine.RunFinished{State: engine.StateOK, Counters: engine.Counters{Organized: 1}}
	searches := len(src.searches)
	hit(t, m, "ctrl+s")
	if !reflect.DeepEqual(f.saved, [][]int64{{302}}) || m.edit != nil || !strings.Contains(m.toast, "Saved and moved to") {
		t.Fatalf("saved %v, editor open %t, toast %q", f.saved, m.edit != nil, m.toast)
	}
	// The results are read again once the library has changed.
	if len(src.searches) != searches+1 || src.searches[searches] != "geogadi" {
		t.Errorf("the search was not run again after the save: %v", src.searches)
	}
	if cur, _ := m.library.current(); cur.ID != 302 {
		t.Errorf("the cursor left its file: on %d", cur.ID)
	}

	m.mb = geogaddi()
	hit(t, m, "l")
	if m.look == nil || m.look.file.ID != 302 || !strings.Contains(screen(m), "Library › Look up") {
		t.Fatalf("l did not open the lookup on the result:\n%s", screen(m))
	}
	hit(t, m, "esc")

	// L takes the result's whole album.
	hit(t, m, "L")
	if m.match == nil || len(m.match.files) != 2 || m.match.files[0].ID != 301 || m.match.files[1].ID != 302 {
		t.Fatalf("L did not open a release match of the album: %+v", m.match)
	}
	if got := screen(m); !strings.Contains(got, "Library › Release match › 2 files") || !strings.Contains(got, "> Geogadi") {
		t.Fatalf("the release match of the album:\n%s", got)
	}
	hit(t, m, "enter", "enter", "enter")
	if got := screen(m); !strings.Contains(got, "Tag 2 files as Geogaddi") || !strings.Contains(got, "Geogadi") || !strings.Contains(got, "-> Geogaddi") {
		t.Fatalf("the album's diff:\n%s", got)
	}
	f.saveEvents, f.saveFinish = nil, engine.RunFinished{State: engine.StateOK, Counters: engine.Counters{Organized: 2}}
	hit(t, m, "enter")
	if len(f.saved) != 2 || !reflect.DeepEqual(f.saved[1], []int64{301, 302}) || m.match != nil {
		t.Errorf("saved %v, match open %t", f.saved, m.match != nil)
	}
}

func TestLibrarySearchProblemsAndLimit(t *testing.T) {
	m, _, src := libraryModel(t, "")
	src.err = errors.New("database is locked")
	hit(t, m, "/")
	typeText(t, m, "amber")
	hit(t, m, "enter")
	if got := screen(m); !strings.Contains(got, "Cannot search the library: database is locked") {
		t.Fatalf("a failed search:\n%s", got)
	}
	// Nothing to act on: the keys do nothing.
	hit(t, m, "e", "l", "L", "down")
	if m.edit != nil || m.look != nil || m.match != nil {
		t.Error("an action opened without a result")
	}

	src.err = nil
	for i := range searchLimit + 20 {
		src.library = append(src.library, store.File{ID: int64(1000 + i), RootKind: store.RootLibrary, Artist: "Many", Album: "Songs",
			Title: fmt.Sprintf("Song %03d", i), Path: fmt.Sprintf(`D:\Music\Many\Songs\%03d.mp3`, i)})
	}
	hit(t, m, "/", "ctrl+u")
	typeText(t, m, "many")
	hit(t, m, "enter")
	got := screen(m)
	if !strings.Contains(got, fmt.Sprintf("the first %d files; add a word to narrow it down", searchLimit)) {
		t.Errorf("a search past the limit:\n%s", got)
	}
	hit(t, m, "pgdown", "pgdown", "pgdown", "pgdown", "pgdown", "pgdown", "pgdown", "pgdown", "pgdown", "pgdown")
	checkFits(t, screen(m), 120, 32)
	cur, _ := m.library.current()
	if m.library.cursor >= searchLimit || !strings.Contains(screen(m), cur.Title) {
		t.Errorf("the cursor (%d, %q) is past the limit or off screen:\n%s", m.library.cursor, cur.Title, screen(m))
	}
}

func TestGoldenLibrary(t *testing.T) {
	m, _, _ := libraryModel(t, "a")
	hit(t, m, "down")
	got := screen(m)
	checkFits(t, got, 120, 32)
	golden(t, filepath.Join("testdata", "library.golden"), got)
}

// A search a person just asked for is not overtaken by one repeated because
// the library changed, and results go stale rather than being read again
// while another tab shows.
func TestLibraryReloadsLeaveANewSearchAlone(t *testing.T) {
	m, _, src := libraryModel(t, "geogadi")
	hit(t, m, "/", "ctrl+u")
	typeText(t, m, "amber")
	// The search starts, and before its answer the library reloads.
	_, search := m.Update(keyMsg("enter"))
	src.version++
	tickNow(t, m)
	pump(t, m, search)
	if cur, _ := m.library.current(); m.library.searched != "amber" || cur.Title != "Foil" {
		t.Fatalf("after a reload during the search: results for %q, cursor on %q", m.library.searched, cur.Title)
	}

	// On another tab a change only marks the results stale.
	hit(t, m, "1")
	searches := len(src.searches)
	src.version++
	tickNow(t, m)
	if len(src.searches) != searches || !m.library.stale {
		t.Fatalf("a reload on another tab searched %v (stale %t)", src.searches[searches:], m.library.stale)
	}
	hit(t, m, "4")
	if len(src.searches) != searches+1 || m.library.stale {
		t.Errorf("coming back did not read the results again: %v", src.searches[searches:])
	}

	// A repeat that fails keeps what was found.
	src.err = errors.New("database is locked")
	src.version++
	_, cmd := m.Update(tickMsg{})
	pump(t, m, cmd)
	src.err = nil
	if got := screen(m); len(m.library.results) != 1 || !strings.Contains(got, "Foil") {
		t.Errorf("a failed repeat lost the results:\n%s", got)
	}

	// Clearing the query drops a search on its way.
	hit(t, m, "/")
	_, search = m.Update(keyMsg("enter"))
	hit(t, m, "/", "ctrl+u", "enter")
	pump(t, m, search)
	if m.library.searched != "" || len(m.library.results) != 0 || m.library.waiting {
		t.Errorf("after clearing: searched %q, %d results, waiting %t", m.library.searched, len(m.library.results), m.library.waiting)
	}
	// Pasted text is typed.
	hit(t, m, "/")
	m.Update(tea.PasteMsg{Content: "boards of\ncanada"})
	if m.library.query != "boards of canada" {
		t.Errorf("pasted query = %q", m.library.query)
	}
}

// A retag that moves nothing still says the files were tagged.
func TestReleaseMatchOfAnAlbumInPlaceSaysSo(t *testing.T) {
	m, f, _ := libraryModel(t, "geogadi")
	m.mb = geogaddi()
	hit(t, m, "L", "enter", "enter", "enter")
	f.saveFinish = engine.RunFinished{State: engine.StateOK, Counters: engine.Counters{InPlace: 2}}
	hit(t, m, "enter")
	if !m.toastOK || !strings.Contains(m.toast, "2 files tagged where they are") {
		t.Errorf("toast = %q (ok %t)", m.toast, m.toastOK)
	}
}
