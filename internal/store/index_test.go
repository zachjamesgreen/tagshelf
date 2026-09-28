package store

import (
	"path/filepath"
	"reflect"
	"runtime"
	"testing"
	"time"
)

func openMemory(t *testing.T) *Store {
	t.Helper()
	s, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func setRoot(t *testing.T, s *Store, kind RootKind, path string) int64 {
	t.Helper()
	id, err := s.SetRoot(kind, path)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func putFiles(t *testing.T, s *Store, files ...File) {
	t.Helper()
	if err := s.PutFiles(files); err != nil {
		t.Fatal(err)
	}
}

// filesByPath returns every indexed file keyed by its absolute path.
func filesByPath(t *testing.T, s *Store) map[string]File {
	t.Helper()
	files, err := s.Files()
	if err != nil {
		t.Fatal(err)
	}
	m := make(map[string]File)
	for _, f := range files {
		m[f.Path] = f
	}
	return m
}

func TestIndexMigrationKeepsRunHistory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tagshelf.db")
	old, err := open(path, migrations[:1]) // run history only, before the index
	if err != nil {
		t.Fatal(err)
	}
	id, err := old.StartRun(KindOrganize, false, "s", "d", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	old.Close()

	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	runs, err := s.Runs()
	if err != nil || len(runs) != 1 || runs[0].ID != id || runs[0].Counters.Indexed != 0 {
		t.Fatalf("runs after the upgrade = %+v, %v; want run %d with nothing indexed", runs, err, id)
	}
	if n, err := s.CountFiles(); err != nil || n != 0 {
		t.Errorf("CountFiles after the upgrade = %d, %v; want an empty index", n, err)
	}
}

func TestSetRootRewritesPath(t *testing.T) {
	s := openMemory(t)
	lib := setRoot(t, s, RootLibrary, `D:\Music`)
	untagged := setRoot(t, s, RootUntagged, `D:\Music\_Untagged`)
	if lib == untagged {
		t.Fatal("two kinds of root share an ID")
	}
	putFiles(t, s, File{RootID: lib, RelPath: filepath.Join("A", "a.mp3"), Size: 1, ModTime: time.Unix(1, 0)})

	// The library turned up under another drive letter: same root, same rows.
	if again := setRoot(t, s, RootLibrary, `E:\Music`); again != lib {
		t.Errorf("moving the library root gave ID %d, want %d", again, lib)
	}
	roots, err := s.Roots()
	if err != nil {
		t.Fatal(err)
	}
	want := []Root{{ID: lib, Kind: RootLibrary, Path: `E:\Music`}, {ID: untagged, Kind: RootUntagged, Path: `D:\Music\_Untagged`}}
	if !reflect.DeepEqual(roots, want) {
		t.Errorf("roots = %+v, want %+v", roots, want)
	}
	if _, ok := filesByPath(t, s)[filepath.Join(`E:\Music`, "A", "a.mp3")]; !ok {
		t.Errorf("file not found under the moved root: %+v", filesByPath(t, s))
	}
}

func TestPutFilesRoundTripAndReplace(t *testing.T) {
	s := openMemory(t)
	root := t.TempDir()
	id := setRoot(t, s, RootLibrary, root)
	mtime := time.Date(2026, 9, 28, 10, 0, 0, 123456700, time.UTC)
	indexed := time.Date(2026, 9, 28, 11, 0, 0, 0, time.UTC)
	f := File{
		RootID: id, RelPath: filepath.Join("Artist", "Album", "01 - Song.flac"),
		Format: "FLAC", Artist: "Artist", AlbumArtist: "Album Artist", Album: "Album", Title: "Song",
		Track: 1, TrackTotal: 10, Disc: 1, DiscTotal: 2, Year: 2001,
		ReleaseID: "rel", ReleaseTrackID: "reltrack", RecordingID: "rec", Manual: "hand 2026-09-27",
		Lossless: true, Bitrate: 900, SampleRate: 44100, BitDepth: 16, Channels: 2, Length: 3500 * time.Millisecond,
		Sum: "abc", Size: 1234, ModTime: mtime, IndexedAt: indexed, TagsJSON: `{"x":"y"}`,
	}
	putFiles(t, s, f)

	files, err := s.Files()
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 {
		t.Fatalf("files = %+v, want one", files)
	}
	want := f
	want.ID = files[0].ID
	want.Path = filepath.Join(root, f.RelPath)
	if !files[0].ModTime.Equal(mtime) || !files[0].IndexedAt.Equal(indexed) {
		t.Errorf("times = %v, %v; want %v, %v", files[0].ModTime, files[0].IndexedAt, mtime, indexed)
	}
	files[0].ModTime, files[0].IndexedAt = want.ModTime, want.IndexedAt
	if !reflect.DeepEqual(files[0], want) {
		t.Errorf("file:\ngot  %+v\nwant %+v", files[0], want)
	}

	// Re-reading a changed file replaces its row and keeps its ID.
	changed := File{RootID: id, RelPath: f.RelPath, Title: "New", Size: 99, ModTime: mtime.Add(time.Second), ReadError: "no tags"}
	putFiles(t, s, changed)
	files, err = s.Files()
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 || files[0].ID != want.ID || files[0].Title != "New" || files[0].Size != 99 || files[0].Sum != "" || files[0].ReadError != "no tags" {
		t.Errorf("after re-read = %+v, want the one row %d replaced", files, want.ID)
	}
}

func TestPutFilesFoldsCaseOnWindows(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("case folding is Windows only")
	}
	s := openMemory(t)
	id := setRoot(t, s, RootLibrary, `C:\lib`)
	putFiles(t, s, File{RootID: id, RelPath: `A\song.mp3`, Size: 1, ModTime: time.Unix(1, 0)})
	putFiles(t, s, File{RootID: id, RelPath: `a\SONG.mp3`, Size: 2, ModTime: time.Unix(2, 0)})
	stats, err := s.RootFiles(id)
	if err != nil {
		t.Fatal(err)
	}
	if len(stats) != 1 || stats[`a\song.mp3`].RelPath != `a\SONG.mp3` || stats[`a\song.mp3`].Size != 2 {
		t.Errorf("stats = %+v, want one row respelled a\\SONG.mp3", stats)
	}
}

func TestRootFilesDeleteAndRespell(t *testing.T) {
	s := openMemory(t)
	lib := setRoot(t, s, RootLibrary, `C:\lib`)
	other := setRoot(t, s, RootUntagged, `C:\untagged`)
	mtime := time.Unix(100, 5)
	putFiles(t, s,
		File{RootID: lib, RelPath: "a.mp3", Size: 1, ModTime: mtime},
		File{RootID: lib, RelPath: "b.mp3", Size: 2, ModTime: mtime},
		File{RootID: other, RelPath: "c.mp3", Size: 3, ModTime: mtime},
	)
	stats, err := s.RootFiles(lib)
	if err != nil {
		t.Fatal(err)
	}
	if len(stats) != 2 || stats["a.mp3"].Size != 1 || !stats["a.mp3"].ModTime.Equal(mtime) || stats["b.mp3"].Size != 2 {
		t.Fatalf("library stats = %+v", stats)
	}
	if err := s.DeleteFiles([]int64{stats["a.mp3"].ID}); err != nil {
		t.Fatal(err)
	}
	if err := s.RespellFile(stats["b.mp3"].ID, "B.mp3"); err != nil {
		t.Fatal(err)
	}
	got := filesByPath(t, s)
	if len(got) != 2 {
		t.Errorf("files = %v, want b and c", got)
	}
	if _, ok := got[filepath.Join(`C:\lib`, "B.mp3")]; !ok {
		t.Errorf("b.mp3 not respelled: %v", got)
	}
	if n, err := s.CountFiles(); err != nil || n != 2 {
		t.Errorf("CountFiles = %d, %v; want 2", n, err)
	}
}

func TestRecordMoveMovesRowInPlace(t *testing.T) {
	s := openMemory(t)
	src := filepath.Join(t.TempDir(), "src")
	lib := filepath.Join(t.TempDir(), "lib")
	srcID := setRoot(t, s, RootSource, src)
	libID := setRoot(t, s, RootLibrary, lib)
	untaggedID := setRoot(t, s, RootUntagged, filepath.Join(lib, "_Untagged"))
	mtime := time.Unix(100, 0)
	putFiles(t, s,
		File{RootID: srcID, RelPath: filepath.Join("Rip", "song.mp3"), Title: "Song", Sum: "s1", Size: 1, ModTime: mtime},
		File{RootID: srcID, RelPath: "loose.mp3", Sum: "s2", Size: 2, ModTime: mtime},
		File{RootID: srcID, RelPath: "gone.mp3", Sum: "s3", Size: 3, ModTime: mtime},
		// A stale row where song.mp3 is going.
		File{RootID: libID, RelPath: filepath.Join("A", "01 - Song.mp3"), Sum: "stale", Size: 4, ModTime: mtime},
	)
	before := filesByPath(t, s)
	run, err := s.StartRun(KindOrganize, false, src, lib, time.Now())
	if err != nil {
		t.Fatal(err)
	}

	song := filepath.Join(lib, "A", "01 - Song.mp3")
	loose := filepath.Join(lib, "_Untagged", "loose.mp3")
	for _, m := range []struct {
		from, to string
		reason   MoveReason
	}{
		{filepath.Join(src, "Rip", "song.mp3"), song, ReasonOrganized},
		{filepath.Join(src, "loose.mp3"), loose, ReasonUntagged},
		{filepath.Join(src, "gone.mp3"), filepath.Join(t.TempDir(), "elsewhere.mp3"), ReasonOther},
		{song, song, ReasonCueRewrite},
	} {
		if err := s.RecordMove(run, m.from, m.to, m.reason); err != nil {
			t.Fatal(err)
		}
	}

	after := filesByPath(t, s)
	if len(after) != 2 {
		t.Fatalf("files after moves = %+v, want song and loose", after)
	}
	if got := after[song]; got.ID != before[filepath.Join(src, "Rip", "song.mp3")].ID || got.RootID != libID || got.Sum != "s1" || got.Title != "Song" {
		t.Errorf("song row = %+v, want the source row moved into the library", got)
	}
	if got := after[loose]; got.ID != before[filepath.Join(src, "loose.mp3")].ID || got.RootID != untaggedID || got.RelPath != "loose.mp3" {
		t.Errorf("loose row = %+v, want the source row moved into the innermost root", got)
	}
	if sum, err := s.FilesWithSum("s1"); err != nil || len(sum) != 1 || sum[0].Path != song {
		t.Errorf("FilesWithSum(s1) = %+v, %v; want the song", sum, err)
	}
}

func TestRootOf(t *testing.T) {
	sep := string(filepath.Separator)
	lib := filepath.Join(sep+"m", "lib")
	roots := []Root{{ID: 1, Path: lib}, {ID: 2, Path: filepath.Join(lib, "_Untagged")}, {ID: 3, Path: sep}}
	tests := []struct {
		path string
		id   int64
		rel  string
	}{
		{filepath.Join(lib, "A", "a.mp3"), 1, filepath.Join("A", "a.mp3")},
		{filepath.Join(lib, "_Untagged", "a.mp3"), 2, "a.mp3"},
		{filepath.Join(lib+"x", "a.mp3"), 3, filepath.Join("m", "libx", "a.mp3")},
		{filepath.Join(sep + "a.mp3"), 3, "a.mp3"},
	}
	for _, tt := range tests {
		id, rel, ok := rootOf(roots, tt.path)
		if !ok || id != tt.id || rel != tt.rel {
			t.Errorf("rootOf(%q) = %d, %q, %t; want %d, %q", tt.path, id, rel, ok, tt.id, tt.rel)
		}
	}
	if _, _, ok := rootOf(roots[:2], lib); ok {
		t.Error("a root is not below itself")
	}
	if runtime.GOOS == "windows" {
		if id, rel, ok := rootOf(roots[:2], `\M\LIB\A\a.mp3`); !ok || id != 1 || rel != `A\a.mp3` {
			t.Errorf("rootOf ignoring case = %d, %q, %t", id, rel, ok)
		}
	}
}
