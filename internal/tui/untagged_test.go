package tui

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/zachjamesgreen/tagshelf/internal/engine"
	"github.com/zachjamesgreen/tagshelf/internal/fixtures"
	"github.com/zachjamesgreen/tagshelf/internal/marker"
	"github.com/zachjamesgreen/tagshelf/internal/store"
	"github.com/zachjamesgreen/tagshelf/internal/tag"
)

// untaggedSource is a library whose untagged folder holds a rip of two
// well-tagged tracks, two files from another folder (one without tags, one
// marked but without an album), and one nobody moved there.
func untaggedSource() *fakeSource {
	file := func(id int64, name, from string, f store.File) store.UntaggedFile {
		f.ID, f.RootKind, f.RelPath = id, store.RootUntagged, filepath.FromSlash(name)
		f.Path = filepath.Join(p(`D:\Music\_Untagged`), f.RelPath)
		return store.UntaggedFile{File: f, From: from}
	}
	rip := store.File{Format: "FLAC", Artist: "Boards of Canada", Album: "Geogaddi", Length: 77 * time.Second}
	one, two := rip, rip
	one.Title, two.Title = "Ready Lets Go", "Music Is Math"
	src := &fakeSource{snap: sampleSnapshot()}
	src.snap.Untagged = []store.UntaggedFile{
		file(101, "01 track.flac", p(`E:\Rips\Geogaddi\01 track.flac`), one),
		file(102, "02 track.flac", p(`E:\Rips\Geogaddi\02 track.flac`), two),
		file(103, "demo.mp3", p(`E:\Incoming\demo.mp3`), store.File{Format: "MP3", Length: 95 * time.Second}),
		file(104, "live.mp3", p(`E:\Incoming\live.mp3`), store.File{Format: "MP3", Artist: "Hand Tagged", Title: "Live", Manual: "hand 2026-09-01"}),
		file(105, "loose/x.mp3", "", store.File{Format: "MP3", Artist: "Somebody"}),
	}
	src.snap.Overview.Untagged = 5
	return src
}

func newUntaggedModel(t *testing.T, src *fakeSource) (*Model, *fakeRunner) {
	t.Helper()
	f := newFakeRunner()
	m := newModel(t, src, 120, 32)
	m.runner = f
	press(t, m, "2")
	return m, f
}

// cursorTo moves the cursor down to the file with the given ID.
func cursorTo(t *testing.T, m *Model, id int64) {
	t.Helper()
	for range len(m.untagged.files) {
		if m.untagged.curID == id {
			return
		}
		press(t, m, "down")
	}
	if m.untagged.curID != id {
		t.Fatalf("no file %d to move the cursor to", id)
	}
}

func TestUntaggedTabShowsFilesByOriginalFolder(t *testing.T) {
	m, _ := newUntaggedModel(t, untaggedSource())
	got := screen(m)
	for _, want := range []string{
		"Untagged (5 files)", "grouped by original folder",
		p(`E:\Rips\Geogaddi  2 files`), p(`E:\Incoming  2 files`), filepath.Join("_Untagged", "loose") + "  1 file",
		"01 track.flac", "Boards of Canada", "Geogaddi", "Ready Lets Go", "1:17", "ripper tags",
		"demo.mp3", "no tags", "needs: album (hand)", "needs: album, title",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the untagged tab lacks %q:\n%s", want, got)
		}
	}
	// Groups in name order, their files in path order.
	var order []int64
	for _, f := range m.untagged.files {
		order = append(order, f.ID)
	}
	if want := []int64{105, 103, 104, 101, 102}; !reflect.DeepEqual(order, want) {
		t.Errorf("files shown in order %v, want %v", order, want)
	}
}

func TestAcceptTagsOfTheSelection(t *testing.T) {
	src := untaggedSource()
	m, f := newUntaggedModel(t, src)
	album := p(`D:\Music\Boards of Canada\Geogaddi`)
	f.saveEvents = []engine.Event{
		engine.FilePlaced{Path: p(`D:\Music\_Untagged\01 track.flac`), Destination: album + p(`\01 - Ready Lets Go.flac`), Outcome: engine.Organized},
		engine.FilePlaced{Path: p(`D:\Music\_Untagged\02 track.flac`), Destination: album + p(`\02 - Music Is Math.flac`), Outcome: engine.Organized},
		engine.FilePlaced{Path: p(`D:\Music\_Untagged\cover.jpg`), Destination: album + p(`\cover.jpg`), Outcome: engine.Companion},
	}
	f.saveFinish = engine.RunFinished{State: engine.StateOK, Counters: engine.Counters{Organized: 2}}

	cursorTo(t, m, 101)
	press(t, m, "g")
	if got := screen(m); !strings.Contains(got, "2 selected") || strings.Count(got, "●") != 2 {
		t.Fatalf("g did not select the folder's two files:\n%s", got)
	}
	// The run takes both files out of the untagged folder.
	src.snap.Untagged = src.snap.Untagged[2:]
	loads := src.loads
	press(t, m, "a")
	if want := [][]int64{{101, 102}}; !reflect.DeepEqual(f.saved, want) {
		t.Errorf("saved = %v, want %v", f.saved, want)
	}
	if !m.toastOK || !strings.Contains(m.toast, "2 files moved to "+filepath.Join("Boards of Canada", "Geogaddi")) {
		t.Errorf("toast = %q (ok %t)", m.toast, m.toastOK)
	}
	if len(m.untagged.selected) != 0 || src.loads == loads {
		t.Errorf("after accepting: selection %v, reloaded %t", m.untagged.selected, src.loads != loads)
	}
}

func TestSelectingWithSpaceAndG(t *testing.T) {
	m, f := newUntaggedModel(t, untaggedSource())
	cursorTo(t, m, 103)
	press(t, m, "space") // selects demo.mp3 and steps on to live.mp3
	if !m.untagged.selected[103] || m.untagged.curID != 104 {
		t.Fatalf("after space: selected %v, cursor on %d", m.untagged.selected, m.untagged.curID)
	}
	press(t, m, "g") // the rest of the folder
	press(t, m, "g") // and, all of it being selected, none of it
	if len(m.untagged.selected) != 0 {
		t.Fatalf("g twice left %v selected", m.untagged.selected)
	}
	// With nothing selected, a accepts the file under the cursor.
	press(t, m, "a")
	if want := [][]int64{{104}}; !reflect.DeepEqual(f.saved, want) {
		t.Errorf("saved = %v, want %v", f.saved, want)
	}
	press(t, m, "space")
	press(t, m, "esc")
	if len(m.untagged.selected) != 0 {
		t.Error("esc kept the selection")
	}
}

func TestAcceptToastsSayWhatStays(t *testing.T) {
	for name, tt := range map[string]struct {
		events []engine.Event
		finish engine.RunFinished
		ok     bool
		toast  []string
	}{
		"one moved": {
			[]engine.Event{engine.FilePlaced{Destination: p(`D:\Music\A\B\01 - C.mp3`), Outcome: engine.Organized}},
			engine.RunFinished{State: engine.StateOK, Counters: engine.Counters{Organized: 1}},
			true, []string{"Moved to " + filepath.Join("A", "B", "01 - C.mp3")}},
		"moved to two albums, one stays": {
			[]engine.Event{
				engine.FilePlaced{Destination: p(`D:\Music\A\B\01 - C.mp3`), Outcome: engine.Organized},
				engine.FilePlaced{Destination: p(`D:\Music\A\D\01 - E.mp3`), Outcome: engine.Organized}},
			engine.RunFinished{State: engine.StateOK, Counters: engine.Counters{Organized: 2, LeftInPlace: 1}},
			true, []string{"2 files moved into 2 folders", "1 file stays in _Untagged until it has an artist, album and title"}},
		"an error": {
			nil, engine.RunFinished{State: engine.StateErrors, Counters: engine.Counters{Errors: 1}},
			false, []string{"1 error: see 5 Runs"}},
	} {
		m, f := newUntaggedModel(t, untaggedSource())
		f.saveEvents, f.saveFinish = tt.events, tt.finish
		press(t, m, "a")
		for _, want := range tt.toast {
			if m.toastOK != tt.ok || !strings.Contains(m.toast, want) {
				t.Errorf("%s: toast = %q (ok %t), want %q (ok %t)", name, m.toast, m.toastOK, want, tt.ok)
			}
		}
	}

	m, f := newUntaggedModel(t, untaggedSource())
	f.err = &store.LockedError{Path: p(`D:\db.lock`), Holder: store.LockHolder{PID: 4242, Started: started}}
	press(t, m, "a")
	if m.toastOK || !strings.Contains(m.toast, "4242") {
		t.Errorf("toast = %q (ok %t), want the lock error", m.toast, m.toastOK)
	}
}

func TestUntaggedGroupings(t *testing.T) {
	m, _ := newUntaggedModel(t, untaggedSource())
	cursorTo(t, m, 104)
	press(t, m, "v")
	got := screen(m)
	if !strings.Contains(got, "grouped by album tag") || !strings.Contains(got, "(no album tag)  3 files") || !strings.Contains(got, "Geogaddi  2 files") {
		t.Errorf("by album tag:\n%s", got)
	}
	if m.untagged.curID != 104 {
		t.Errorf("the cursor moved to file %d when regrouping", m.untagged.curID)
	}
	press(t, m, "v")
	got = screen(m)
	if !strings.Contains(got, "grouped by nothing") || strings.Contains(got, "2 files") {
		t.Errorf("ungrouped:\n%s", got)
	}
	press(t, m, "g") // the one group is everything
	if len(m.untagged.selected) != 5 {
		t.Errorf("g selected %d of the 5 ungrouped files", len(m.untagged.selected))
	}
	press(t, m, "v")
	if !strings.Contains(screen(m), "grouped by original folder") {
		t.Error("v did not come back round to folders")
	}
}

func TestAcceptTagsDisabled(t *testing.T) {
	locked := untaggedSource()
	locked.locked, locked.holder = true, store.LockHolder{PID: 8812, Started: started}
	for name, tt := range map[string]struct {
		src   *fakeSource
		dry   bool
		toast string
	}{
		"another process holds the lock": {locked, false, "pid 8812"},
		"dry run":                        {untaggedSource(), true, "Dry run"},
	} {
		m, f := newUntaggedModel(t, tt.src)
		m.header.DryRun = tt.dry
		press(t, m, "a")
		if len(f.saved) != 0 || m.toastOK || !strings.Contains(m.toast, tt.toast) {
			t.Errorf("%s: saved %v, toast %q", name, f.saved, m.toast)
		}
	}
}

// Files that left the untagged folder leave the list and the selection.
func TestUntaggedFollowsReloads(t *testing.T) {
	src := untaggedSource()
	m, _ := newUntaggedModel(t, src)
	cursorTo(t, m, 101)
	press(t, m, "g") // the cursor stays on 01 track.flac, the fourth file shown
	src.snap.Untagged = filesAfter(src.snap.Untagged, 102)
	src.version++
	tickNow(t, m)
	if len(m.untagged.selected) != 0 {
		t.Errorf("selection after the reload = %v, want it empty", m.untagged.selected)
	}
	// Its file gone, the cursor is on the last of those left.
	if f, ok := m.untagged.current(); !ok || f.ID != 104 || m.untagged.cursor != 2 {
		t.Errorf("cursor after the reload at %d on %+v, want it on live.mp3 (104), the last file", m.untagged.cursor, f)
	}
	if line := cursorLine(screen(m)); !strings.Contains(line, "live.mp3") {
		t.Errorf("the line under the cursor is %q, want live.mp3:\n%s", line, screen(m))
	}
	if got := screen(m); strings.Contains(got, "track.flac") || !strings.Contains(got, "Untagged (3 files)") {
		t.Errorf("screen after the reload:\n%s", got)
	}

	src.snap.Untagged = nil
	src.version++
	tickNow(t, m)
	press(t, m, "space")
	press(t, m, "g")
	press(t, m, "a")
	if !strings.Contains(screen(m), "No files in _Untagged") {
		t.Errorf("empty untagged tab:\n%s", screen(m))
	}
}

// filesAfter is files without those up to and including the one with ID last.
func filesAfter(files []store.UntaggedFile, last int64) []store.UntaggedFile {
	for i, f := range files {
		if f.ID == last {
			return files[i+1:]
		}
	}
	return files
}

func TestUntaggedListScrollsWithTheCursor(t *testing.T) {
	src := untaggedSource()
	for i := range 60 {
		f := src.snap.Untagged[0]
		f.ID = int64(200 + i)
		f.Path = filepath.Join(p(`D:\Music\_Untagged`), "many", "track "+string(rune('A'+i%26))+string(rune('a'+i/26))+".flac")
		f.From = ""
		src.snap.Untagged = append(src.snap.Untagged, f)
	}
	m, _ := newUntaggedModel(t, src)
	press(t, m, "pgdown")
	press(t, m, "pgdown")
	got := screen(m)
	checkFits(t, got, 120, 32)
	cur, _ := m.untagged.current()
	if !strings.Contains(got, "› ") || !strings.Contains(got, filepath.Base(cur.Path)) {
		t.Errorf("the cursor's file %q is off screen:\n%s", filepath.Base(cur.Path), got)
	}
}

func TestGoldenUntagged(t *testing.T) {
	m, _ := newUntaggedModel(t, untaggedSource())
	cursorTo(t, m, 101)
	press(t, m, "space")
	got := screen(m)
	checkFits(t, got, 120, 32)
	golden(t, filepath.Join("testdata", "untagged.golden"), got)
}

// Accepting tags writes the manual marker and nothing else, and records the
// save under its run.
func TestAcceptSaveWritesOnlyTheMarker(t *testing.T) {
	dir := t.TempDir()
	path := copyFixture(t, dir, fixtures.RipperFLAC, "ripped.flac")
	st, run := realRun(t, dir)

	day := time.Date(2026, 10, 8, 15, 0, 0, 0, time.UTC)
	if err := acceptSave(day)(st, run, store.File{Path: path}); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	md, err := tag.ReadFrom(f)
	if err != nil {
		t.Fatal(err)
	}
	mark, ok := marker.Read(md)
	if !ok || mark.String() != "hand 2026-10-08" {
		t.Errorf("marker = %q (found %t), want hand 2026-10-08", mark, ok)
	}
	if md.Title() != fixtures.Title || md.Album() != fixtures.Album || md.Artist() != fixtures.Artist {
		t.Errorf("tags after accepting = %q / %q / %q, want them as they were", md.Artist(), md.Album(), md.Title())
	}
	writes, err := st.TagWrites(run)
	if err != nil || len(writes) != 1 || writes[0].Path != path || !strings.Contains(writes[0].After, "hand 2026-10-08") {
		t.Errorf("tag writes = %+v, %v", writes, err)
	}
}

// Files that failed or stayed keep their selection, and wide characters keep
// the columns in line.
func TestUntaggedKeepsWhatStayedSelectedAndColumnsAligned(t *testing.T) {
	src := untaggedSource()
	src.snap.Untagged[0].Title = "東京の夜はとても長いので眠れない歌をうたう"
	m, f := newUntaggedModel(t, src)
	f.saveFinish = engine.RunFinished{State: engine.StateErrors, Counters: engine.Counters{Errors: 2}}
	cursorTo(t, m, 101)
	press(t, m, "g")
	press(t, m, "a")
	if len(m.untagged.selected) != 2 {
		t.Errorf("selection after a failed accept = %v, want both files still selected", m.untagged.selected)
	}
	var cols []int
	for _, line := range strings.Split(screen(m), "\n") {
		if i := strings.Index(line, "ripper tags"); i >= 0 {
			cols = append(cols, ansi.StringWidth(line[:i]))
		}
	}
	if len(cols) != 2 || cols[0] != cols[1] {
		t.Errorf("the state column starts at cells %v, want the two rows in line:\n%s", cols, screen(m))
	}
}

// Closing the TUI stops an action at the file it is on, and waits for it to
// get there: the library's database closes next.
func TestStopActionCancelsTheActionAndWaits(t *testing.T) {
	m, _ := newUntaggedModel(t, untaggedSource())
	ctx, done := m.begin()
	var ended atomic.Bool
	go func() {
		<-ctx.Done()
		time.Sleep(50 * time.Millisecond) // the file it is on
		ended.Store(true)
		close(done)
	}()
	stopped := make(chan struct{})
	go func() {
		m.stopAction()
		close(stopped)
	}()
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("stopAction is still waiting: it did not cancel the action")
	}
	if ctx.Err() == nil {
		t.Error("the action's context was not cancelled")
	}
	if !ended.Load() {
		t.Error("stopAction returned before the action had ended")
	}
}
