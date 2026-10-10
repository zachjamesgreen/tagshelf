package tui

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"tagshelf/internal/store"
)

// TestLibrarySourceFollowsARunElsewhere drives the model over a real
// database while a "CLI run" (this process, standing in for another one)
// holds the lock, records progress and moves, and finishes.
func TestLibrarySourceFollowsARunElsewhere(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "tagshelf.db")
	lib := filepath.Join(dir, "Music")
	cli, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer cli.Close()
	libRoot, err := cli.SetRoot(store.RootLibrary, lib)
	if err != nil {
		t.Fatal(err)
	}
	untRoot, err := cli.SetRoot(store.RootUntagged, filepath.Join(lib, "_Untagged"))
	if err != nil {
		t.Fatal(err)
	}
	file := func(root int64, rel string) store.File {
		return store.File{RootID: root, RelPath: rel, Size: 1, ModTime: time.Unix(1, 0), IndexedAt: time.Now()}
	}
	if err := cli.PutFiles([]store.File{file(libRoot, "a.flac"), file(untRoot, "b.mp3")}); err != nil {
		t.Fatal(err)
	}

	reader, err := store.OpenReader(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	// The lock below is this process's own; a source that thinks it is some
	// other process sees it as another run's.
	src := librarySource{st: reader, pid: -1}
	m := New(src, Header{Destination: lib})
	m.Update(windowSize(120, 32))
	do(t, m, m.Init())
	if got := screen(m); !strings.Contains(got, "1 untagged file") || strings.Contains(got, "in progress") {
		t.Fatalf("before the run:\n%s", got)
	}

	lock, err := store.AcquireLock(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Release()
	runID, err := cli.StartRun(store.KindOrganize, false, "src", lib, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := cli.RecordMove(runID, filepath.Join(dir, "src", "c.mp3"), filepath.Join(lib, "_Untagged", "c.mp3"), store.ReasonUntagged); err != nil {
		t.Fatal(err)
	}
	if err := cli.RecordProgress(runID, store.RunProgress{Phase: "audio", Done: 1, Total: 4, At: time.Now()}); err != nil {
		t.Fatal(err)
	}
	tickNow(t, m)
	got := screen(m)
	for _, want := range []string{
		"Organize run in progress (pid " + strconv.Itoa(os.Getpid()),
		"1 / 4 files",
		filepath.Join("_Untagged", "c.mp3"),
	} {
		if !strings.Contains(got, want) {
			t.Errorf("during the run, the view lacks %q:\n%s", want, got)
		}
	}
	if !m.ReadOnly() {
		t.Error("not read-only while the lock is held")
	}

	if err := cli.PutFiles([]store.File{file(untRoot, "c.mp3")}); err != nil {
		t.Fatal(err)
	}
	if err := cli.FinishRun(runID, store.Counters{Untagged: 1}, store.StateOK, time.Now()); err != nil {
		t.Fatal(err)
	}
	lock.Release()
	tickNow(t, m)
	got = screen(m)
	if strings.Contains(got, "in progress") || m.ReadOnly() {
		t.Errorf("after the run, still locked:\n%s", got)
	}
	if !strings.Contains(got, "2 untagged files") {
		t.Errorf("after the run, the view did not reload:\n%s", got)
	}
}

func TestLibrarySourceIgnoresItsOwnLock(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "tagshelf.db")
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	lock, err := store.AcquireLock(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Release()
	if _, held, err := (librarySource{st: st, pid: os.Getpid()}).Lock(); err != nil || held {
		t.Errorf("own lock: held = %t, %v; want not another's", held, err)
	}
}

func TestLibrarySourceRunsAndDetail(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "tagshelf.db")
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	first, err := st.StartRun(store.KindOrganize, false, "src", "lib", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := st.RecordMove(first, filepath.Join("src", "a.mp3"), filepath.Join("lib", "_Untagged", "a.mp3"), store.ReasonUntagged); err != nil {
		t.Fatal(err)
	}
	if err := st.RecordError(first, filepath.Join("src", "b.mp3"), "open: denied"); err != nil {
		t.Fatal(err)
	}
	if err := st.FinishRun(first, store.Counters{Untagged: 1, Errors: 1}, store.StateErrors, time.Now()); err != nil {
		t.Fatal(err)
	}
	second, err := st.StartRun(store.KindIndex, false, "", "lib", time.Now())
	if err != nil {
		t.Fatal(err)
	}

	src := librarySource{st: st, pid: -1}
	snap, err := src.Load(Parts{Runs: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.Runs) != 2 || snap.Runs[0].ID != second || snap.Runs[1].ID != first {
		t.Errorf("runs = %+v, want %d then %d", snap.Runs, second, first)
	}
	d, err := src.RunDetail(first)
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Moves) != 1 || d.Moves[0].Reason != store.ReasonUntagged || len(d.Errors) != 1 || d.Errors[0].Message != "open: denied" {
		t.Errorf("detail = %+v", d)
	}
}
