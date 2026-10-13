package tui

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/zachjamesgreen/tagshelf/internal/engine"
	"github.com/zachjamesgreen/tagshelf/internal/fixtures"
	"github.com/zachjamesgreen/tagshelf/internal/store"
	"github.com/zachjamesgreen/tagshelf/internal/tagwrite"
)

// filesFolder makes a folder outside any library: two folders in it, a
// ripper's file, a manually tagged one and one without tags, and beside them
// a text file, a hidden audio file and a copy a crashed save left.
func filesFolder(t *testing.T) (dir string) {
	t.Helper()
	dir = filepath.Join(t.TempDir(), "Incoming")
	for _, sub := range []string{"beta", "Alpha"} {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	copyFixture(t, dir, fixtures.RipperFLAC, "one.flac")
	copyFixture(t, dir, "manual.mp3", "Two.mp3")
	copyFixture(t, dir, "untagged.ogg", "three.ogg")
	copyFixture(t, dir, "untagged.mp3", ".hidden.mp3")
	copyFixture(t, dir, "untagged.flac", store.OwnFilePrefix+"123.flac")
	copyFixture(t, filepath.Join(dir, "Alpha"), "untagged.mp3", "inner.mp3")
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

// filesOn opens the Files tab of a TUI whose source folder is dir, read from
// the disk.
func filesOn(t *testing.T, dir string) (*Model, *fakeRunner, *fakeSource) {
	t.Helper()
	old := listDrives
	listDrives = func() []string { return nil }
	t.Cleanup(func() { listDrives = old })
	src := untaggedSource()
	src.disk = true
	m, f := newUntaggedModel(t, src)
	m.source = dir
	keys(t, m, "7")
	if m.tab != TabFiles || !m.files.loaded || m.files.reading {
		t.Fatalf("the Files tab did not open:\n%s", screen(m))
	}
	return m, f, src
}

// keys sends keys to the model as hit does, and after each waits out a read
// of the folder that hit gave up on: the disk may take longer than a pump
// waits, and the tab takes no key while it reads.
func keys(t *testing.T, m *Model, ks ...string) {
	t.Helper()
	for _, k := range ks {
		hit(t, m, k)
		settle(t, m)
	}
}

// settle reads the folder the Files tab is waiting for, if it is waiting,
// however long that takes.
func settle(t *testing.T, m *Model) {
	t.Helper()
	if !m.files.reading {
		return
	}
	going := m.files.going
	m.Update(m.filesGo(going.list.Dir, going.on, going.onID)())
	if m.files.reading {
		t.Fatal("the Files tab is still reading")
	}
}

// names lists what the Files tab shows, folders first.
func (t *filesTab) names() []string {
	out := slices.Clone(t.list.Folders)
	for _, f := range t.list.Files {
		out = append(out, filepath.Base(f.File.Path))
	}
	return out
}

// toFile puts the Files tab's cursor on the file named name.
func toFile(t *testing.T, m *Model, name string) {
	t.Helper()
	for i, n := range m.files.names() {
		if n == name {
			m.files.cursor = i
			return
		}
	}
	t.Fatalf("the Files tab does not list %q: %q", name, m.files.names())
}

// The Files tab opens on the source folder: its folders first, then its
// audio files with what their tags say, each with how far it is from what
// the library takes. What is no audio is not listed, nor what tagshelf made.
func TestFilesTabListsAFolder(t *testing.T) {
	dir := filesFolder(t)
	m, _, _ := filesOn(t, dir)
	if got, want := m.files.names(), []string{"Alpha", "beta", "one.flac", "three.ogg", "Two.mp3"}; !slices.Equal(got, want) {
		t.Fatalf("the tab lists %q, want %q", got, want)
	}
	got := screen(m)
	for _, want := range []string{"Files", dir, "Alpha" + string(filepath.Separator), "one.flac", fixtures.Artist, fixtures.Album,
		"ripper tags", "ready (hand)", "no tags", "1-7 tabs", "space select"} {
		if !strings.Contains(got, want) {
			t.Errorf("the tab lacks %q:\n%s", want, got)
		}
	}
	checkFits(t, got, m.width, m.height)
	for _, f := range m.files.list.Files {
		if f.File.ID >= 0 || f.Indexed || f.InLibrary {
			t.Errorf("%s is listed as %+v, want a file outside the index with a number below zero", filepath.Base(f.File.Path), f)
		}
	}

	// The dot shows the hidden file, and never tagshelf's own.
	ids := map[string]int64{}
	for _, f := range m.files.list.Files {
		ids[f.File.Path] = f.File.ID
	}
	toFile(t, m, "three.ogg")
	keys(t, m, ".")
	if got, want := m.files.names(), []string{"Alpha", "beta", ".hidden.mp3", "one.flac", "three.ogg", "Two.mp3"}; !slices.Equal(got, want) {
		t.Errorf("with the hidden ones the tab lists %q, want %q", got, want)
	}
	// Read again, a file keeps its number and the cursor its file.
	for _, f := range m.files.list.Files {
		if id, ok := ids[f.File.Path]; ok && id != f.File.ID {
			t.Errorf("%s is now file %d, was %d", filepath.Base(f.File.Path), f.File.ID, id)
		}
	}
	if _, f, ok := m.files.current(); !ok || f == nil || filepath.Base(f.File.Path) != "three.ogg" {
		t.Error("the cursor did not stay on its file")
	}
}

// Enter goes into the folder under the cursor and backspace back out, with
// the cursor on the folder left. A folder that cannot be opened is not gone
// into.
func TestFilesTabWalksFolders(t *testing.T) {
	dir := filesFolder(t)
	m, _, _ := filesOn(t, dir)
	keys(t, m, "enter")
	if got := m.files.names(); m.files.list.Dir != filepath.Join(dir, "Alpha") || !slices.Equal(got, []string{"inner.mp3"}) {
		t.Fatalf("after enter the tab shows %q with %q", m.files.list.Dir, got)
	}
	keys(t, m, "backspace")
	if folder, _, _ := m.files.current(); m.files.list.Dir != dir || folder != "Alpha" {
		t.Fatalf("after backspace the tab shows %q with the cursor on %q", m.files.list.Dir, folder)
	}
	keys(t, m, "down", "enter")
	if got := screen(m); !strings.Contains(got, "No folders and no audio files in it") {
		t.Errorf("an empty folder does not say so:\n%s", got)
	}
	keys(t, m, "backspace")

	if err := os.RemoveAll(filepath.Join(dir, "Alpha")); err != nil {
		t.Fatal(err)
	}
	m.files.cursor = 0
	keys(t, m, "enter")
	if m.files.list.Dir != dir || !strings.Contains(screen(m), "Cannot open") {
		t.Errorf("a folder that is gone: the tab shows %q:\n%s", m.files.list.Dir, screen(m))
	}
}

// A file outside the library is edited like any other, and saved where it
// is: the editor says it stays, and the save goes to the run that moves
// nothing, as the file the tab listed.
func TestFilesTabEditsAFileWhereItIs(t *testing.T) {
	dir := filesFolder(t)
	m, f, src := filesOn(t, dir)
	path := filepath.Join(dir, "one.flac")
	src.tags = map[string]tagwrite.Values{path: readAllTags(t, path)}
	toFile(t, m, "one.flac")
	keys(t, m, "e")
	if m.edit == nil || m.edit.form == nil || m.edit.title != fixtures.Title {
		t.Fatalf("the editor did not open on the file's tags:\n%s", screen(m))
	}
	if got := screen(m); !strings.Contains(got, staysLine) || strings.Contains(got, "Will go to") {
		t.Errorf("the editor does not say the file stays:\n%s", got)
	}
	typeText(t, m, "!")
	f.saveFinish = engine.RunFinished{State: engine.StateOK, RunID: 3, Counters: engine.Counters{InPlace: 1}}
	listed := len(src.listed)
	keys(t, m, "ctrl+s")

	if len(f.savedFiles) != 1 || len(f.savedFiles[0]) != 1 || f.savedFiles[0][0].Path != path || f.savedFiles[0][0].Size == 0 {
		t.Fatalf("the save went to %+v, want the one file as it was listed", f.savedFiles)
	}
	if m.edit != nil || !m.toastOK || !strings.HasPrefix(m.toast, "Saved") || !strings.Contains(m.toast, "u undo") {
		t.Errorf("after the save: editor open %t, toast %q", m.edit != nil, m.toast)
	}
	if len(src.listed) == listed {
		t.Error("the folder was not read again after the save")
	}

	// The save itself, on the real file: written, marked, and not moved.
	st, run := realRun(t, t.TempDir())
	if err := f.saves[0](st, run, f.savedFiles[0][0]); err != nil {
		t.Fatal(err)
	}
	checkTags(t, path, map[tagwrite.Field]string{tagwrite.Title: fixtures.Title + "!", tagwrite.Marker: "hand " + time.Now().Format(time.DateOnly)})
}

// Space selects files, and a accepts the tags of the selection, or of the
// file under the cursor, as one save of files outside the index.
func TestFilesTabAcceptsTheSelection(t *testing.T) {
	dir := filesFolder(t)
	m, f, _ := filesOn(t, dir)
	toFile(t, m, "one.flac")
	keys(t, m, "space", "space")
	if len(m.files.selected) != 2 || !strings.Contains(screen(m), "2 selected") {
		t.Fatalf("the selection is %v:\n%s", m.files.selected, screen(m))
	}
	// On a folder the space key selects nothing.
	m.files.cursor = 0
	keys(t, m, "space")
	if len(m.files.selected) != 2 {
		t.Errorf("space on a folder changed the selection to %v", m.files.selected)
	}
	f.saveFinish = engine.RunFinished{State: engine.StateOK, Counters: engine.Counters{InPlace: 2}}
	keys(t, m, "a")
	if len(f.savedFiles) != 1 || len(f.savedFiles[0]) != 2 {
		t.Fatalf("accepting the selection saved %+v", f.savedFiles)
	}
	var names []string
	for _, file := range f.savedFiles[0] {
		names = append(names, filepath.Base(file.Path))
	}
	if !slices.Equal(names, []string{"one.flac", "three.ogg"}) {
		t.Errorf("the files accepted are %q", names)
	}
	if !strings.Contains(m.toast, "2 files tagged where they are") {
		t.Errorf("toast %q", m.toast)
	}

	// Esc drops a selection before it leaves the tab to the other keys.
	keys(t, m, "space")
	keys(t, m, "esc")
	if len(m.files.selected) != 0 || m.tab != TabFiles {
		t.Errorf("esc: selection %v, tab %v", m.files.selected, m.tab)
	}
}

// l looks the file up and L matches every file of the folder, or the
// selection, to a release; both say the files stay.
func TestFilesTabLooksUpAndMatches(t *testing.T) {
	dir := filesFolder(t)
	m, _, _ := filesOn(t, dir)
	m.mb = julie()
	toFile(t, m, "three.ogg")
	keys(t, m, "l")
	if m.look == nil || m.look.file.Path != filepath.Join(dir, "three.ogg") {
		t.Fatalf("the lookup did not open on the file:\n%s", screen(m))
	}
	toAlbum(t, m)
	keys(t, m, "enter")
	if got := screen(m); m.look.step != stepConfirm || !strings.Contains(got, staysLine) {
		t.Errorf("the lookup's diff does not say the file stays:\n%s", got)
	}
	keys(t, m, "esc", "esc", "esc")
	if m.look != nil {
		t.Fatalf("the lookup did not close:\n%s", screen(m))
	}

	keys(t, m, "L")
	if m.match == nil || len(m.match.files) != 3 {
		t.Fatalf("L without a selection did not take the folder's three files:\n%s", screen(m))
	}
	keys(t, m, "esc")
	toFile(t, m, "one.flac")
	keys(t, m, "space", "L")
	if m.match == nil || len(m.match.files) != 1 || filepath.Base(m.match.files[0].Path) != "one.flac" {
		t.Fatalf("L with a selection did not take it:\n%s", screen(m))
	}
}

// What the tab does not change, and says why: a link, a file that cannot be
// read, a file of the library's folders the index does not hold yet, and a
// copy set aside as a duplicate.
func TestFilesTabSaysWhyAFileCannotBeChanged(t *testing.T) {
	dir := p(`D:\Music\_Untagged\rip`)
	at := func(name string) store.File { return store.File{Path: filepath.Join(dir, name), RelPath: name} }
	src := untaggedSource()
	src.folders = map[string]FolderList{dir: {Dir: dir, Files: []ListedFile{
		{File: at("a link.flac"), Link: true},
		{File: at("broken.flac"), Problem: "access denied"},
		{File: at("new.flac"), InLibrary: true},
		{File: store.File{ID: 9, RootKind: store.RootDuplicates, Path: filepath.Join(dir, "set aside.flac")}, Indexed: true, InLibrary: true},
	}}}
	m, f := newUntaggedModel(t, src)
	m.source = dir
	m.files = filesTab{}
	// The folder is not on the disk: the tab is told where to open.
	do(t, m, m.filesGo(dir, "", 0))
	keys(t, m, "7")
	got := screen(m)
	for _, want := range []string{"a link, not changed", "cannot be read", "not in the index yet", "set aside"} {
		if !strings.Contains(got, want) {
			t.Errorf("the tab lacks the state %q:\n%s", want, got)
		}
	}
	for i, want := range []string{"is a link", "cannot be read: access denied", "not in the index yet: press i on 1 Home first", "set aside as a duplicate"} {
		for _, k := range []string{"e", "l", "a", "space", "enter"} {
			m.files.cursor = i
			m.toast = ""
			keys(t, m, k)
			if m.edit != nil || m.look != nil || len(f.saved) != 0 || len(m.files.selected) != 0 || !strings.Contains(m.toast, want) {
				t.Errorf("%s on file %d: toast %q, want it to say the file %s", k, i, m.toast, want)
			}
			m.edit, m.look = nil, nil
		}
	}
	// L leaves them all out, and so has nothing to match.
	keys(t, m, "L")
	if m.match != nil {
		t.Error("L opened a release match of files that cannot be changed")
	}
}

// A file of the library that the tab lists is the index's own: editing it is
// the save that files it, as on the Library tab.
func TestFilesTabLeavesALibraryFileToTheIndex(t *testing.T) {
	dir := p(`D:\Music\Singer\Album (2001)`)
	file := store.File{ID: 7, RootKind: store.RootLibrary, Path: filepath.Join(dir, "02 - Song.flac"), Artist: "Singer", Album: "Album", Title: "Song"}
	src := untaggedSource()
	src.folders = map[string]FolderList{dir: {Dir: dir, Files: []ListedFile{{File: file, Indexed: true, InLibrary: true}}}}
	src.tags = map[string]tagwrite.Values{file.Path: {tagwrite.Title: {"Song"}, tagwrite.Artist: {"Singer"}, tagwrite.Album: {"Album"},
		tagwrite.Date: {"2001"}, tagwrite.Track: {"2"}, tagwrite.Marker: {"hand 2026-01-02"}}}
	m, f := newUntaggedModel(t, src)
	do(t, m, m.filesGo(dir, "", 0))
	keys(t, m, "7")
	if got := screen(m); !strings.Contains(got, "in the library") {
		t.Errorf("the file is not shown as one of the library:\n%s", got)
	}
	keys(t, m, "e")
	if m.edit == nil || m.edit.form == nil {
		t.Fatalf("the editor did not open: toast %q", m.toast)
	}
	if got := screen(m); strings.Contains(got, staysLine) || !strings.Contains(got, "Will stay at:") {
		t.Errorf("the editor does not say where the library's file goes:\n%s", got)
	}
	typeText(t, m, "!")
	f.saveFinish = engine.RunFinished{State: engine.StateOK}
	keys(t, m, "ctrl+s")
	if !reflect.DeepEqual(f.saved, [][]int64{{7}}) || len(f.savedFiles) != 0 {
		t.Errorf("the save went to files %v and to %d saves of files outside the index", f.saved, len(f.savedFiles))
	}
}

// Before a library is set up the tab lists and reads, and says what saving
// needs.
func TestFilesTabReadsWithoutALibrary(t *testing.T) {
	dir := filesFolder(t)
	m, f, _ := filesOn(t, dir)
	m.runner = nil
	m.noLibrary = &NoLibrary{What: "no library folder is set"}
	if got := screen(m); !strings.Contains(got, "one.flac") || strings.Contains(got, "No library yet") {
		t.Fatalf("without a library the tab does not list the folder:\n%s", got)
	}
	toFile(t, m, "one.flac")
	keys(t, m, "a")
	if len(f.saved) != 0 || !strings.Contains(m.toast, "Saving tags needs a library") || !strings.Contains(m.toast, "6 Settings") {
		t.Errorf("a without a library: saved %v, toast %q", f.saved, m.toast)
	}
	keys(t, m, "e")
	if m.edit == nil || m.edit.form == nil {
		t.Fatalf("the editor did not open to read the tags: toast %q", m.toast)
	}
	typeText(t, m, "!")
	keys(t, m, "ctrl+s")
	if len(f.saved) != 0 || m.edit == nil || !strings.Contains(m.toast, "Saving tags needs a library") {
		t.Errorf("ctrl+s without a library: saved %v, toast %q", f.saved, m.toast)
	}
}

// The folder is read again when the library changes, and only when the tab
// shows: a save may have moved a file away or changed what it holds.
func TestFilesTabReadsTheFolderAgainWhenTheLibraryChanges(t *testing.T) {
	dir := filesFolder(t)
	m, _, src := filesOn(t, dir)
	listed := len(src.listed)
	keys(t, m, "1")
	src.version++
	tickNow(t, m)
	if len(src.listed) != listed || !m.files.stale {
		t.Errorf("with another tab showing the folder was read %d times more, stale %t", len(src.listed)-listed, m.files.stale)
	}
	keys(t, m, "7")
	if len(src.listed) != listed+1 || m.files.stale {
		t.Errorf("back on the tab the folder was read %d times more, stale %t", len(src.listed)-listed, m.files.stale)
	}
	src.version++
	tickNow(t, m)
	settle(t, m)
	if len(src.listed) < listed+2 {
		t.Errorf("with the tab showing the folder was read %d times more, want 2", len(src.listed)-listed)
	}

	src.folderErr = errors.New("the drive is gone")
	m.files.cursor = 0 // a folder
	keys(t, m, "enter")
	// The folder's path is long: the message may run over the line.
	if got := screen(m); !strings.Contains(got, "Cannot open") || !strings.Contains(m.files.problem, "the drive is gone") {
		t.Errorf("a folder that cannot be read:\n%s", got)
	}
}

// readFolder lists a file of the library by its index row, and a file the
// index does not hold as one of the library's folders that is not indexed.
func TestReadFolderKnowsTheLibrarysFiles(t *testing.T) {
	tmp := t.TempDir()
	lib := filepath.Join(tmp, "lib")
	album := filepath.Join(lib, "Artist", "Album")
	indexed := copyFixture(t, mkdir(t, album), "manual.flac", "01 - Song.flac")
	added := copyFixture(t, album, "untagged.mp3", "new.mp3")
	outsidePath := copyFixture(t, mkdir(t, filepath.Join(tmp, "elsewhere")), "untagged.mp3", "x.mp3")

	st, err := store.Open(filepath.Join(tmp, "tagshelf.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	root, err := st.SetRoot(store.RootLibrary, lib)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(indexed)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.PutFiles([]store.File{{RootID: root, RelPath: filepath.Join("Artist", "Album", "01 - Song.flac"), Title: "Indexed Title",
		Size: info.Size(), ModTime: info.ModTime()}}); err != nil {
		t.Fatal(err)
	}

	list, err := readFolder(st, album, false)
	if err != nil || len(list.Files) != 2 {
		t.Fatalf("readFolder = %+v, %v", list, err)
	}
	if f := list.Files[0]; !f.Indexed || !f.InLibrary || f.File.ID <= 0 || f.File.Title != "Indexed Title" || f.File.Path != indexed {
		t.Errorf("the indexed file is listed as %+v, want its index row", f)
	}
	if f := list.Files[1]; f.Indexed || !f.InLibrary || f.File.ID != 0 || f.File.Path != added {
		t.Errorf("the file the index does not hold is listed as %+v", f)
	}
	list, err = readFolder(st, filepath.Dir(outsidePath), false)
	if err != nil || len(list.Files) != 1 || list.Files[0].Indexed || list.Files[0].InLibrary {
		t.Errorf("a folder outside the library = %+v, %v", list, err)
	}
	// The library's own top folder is one of its folders.
	if list, err := readFolder(st, lib, false); err != nil || !slices.Equal(list.Folders, []string{"Artist"}) {
		t.Errorf("the library folder = %+v, %v", list, err)
	}
}

func mkdir(t *testing.T, dir string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestGoldenFiles(t *testing.T) {
	dir := p(`D:\Incoming\Rips`)
	at := func(name string, f store.File) store.File {
		f.Path, f.RelPath = filepath.Join(dir, name), name
		return f
	}
	src := untaggedSource()
	src.folders = map[string]FolderList{dir: {Dir: dir, Folders: []string{"Geogaddi", "scans"}, Files: []ListedFile{
		{File: at("01 track.flac", store.File{Artist: "Boards of Canada", Album: "Geogaddi", Title: "Ready Lets Go"})},
		{File: at("02 track.flac", store.File{Artist: "Boards of Canada", Album: "Geogaddi", Title: "Music Is Math", Manual: "hand 2026-09-01"})},
		{File: at("demo.mp3", store.File{})},
		{File: at("link.mp3", store.File{Title: "Linked"}), Link: true},
	}}}
	m, _ := newUntaggedModel(t, src)
	do(t, m, m.filesGo(dir, "", 0))
	keys(t, m, "7", "down", "down", "space")
	got := screen(m)
	checkFits(t, got, 120, 32)
	golden(t, filepath.Join("testdata", "files.golden"), got)
}
