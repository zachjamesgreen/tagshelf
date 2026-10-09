package store

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func openReader(t *testing.T, path string) *Store {
	t.Helper()
	r, err := OpenReader(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { r.Close() })
	return r
}

func TestOpenReaderRefusesMissingOldAndNewDatabases(t *testing.T) {
	dir := t.TempDir()
	if _, err := OpenReader(filepath.Join(dir, "missing.db")); !errors.Is(err, ErrNoDatabase) {
		t.Errorf("missing database: error = %v, want ErrNoDatabase", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "missing.db")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("OpenReader created the missing database: %v", err)
	}

	old := filepath.Join(dir, "old.db")
	s, err := open(old, migrations[:1])
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
	if _, err := OpenReader(old); !errors.Is(err, ErrOutdatedDatabase) {
		t.Errorf("old database: error = %v, want ErrOutdatedDatabase", err)
	}
	if got := versionAt(t, old); got != 1 {
		t.Errorf("OpenReader migrated the old database to version %d", got)
	}

	newer := filepath.Join(dir, "newer.db")
	s, err = Open(newer)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(fmt.Sprintf("PRAGMA user_version = %d", len(migrations)+1)); err != nil {
		t.Fatal(err)
	}
	s.Close()
	if _, err := OpenReader(newer); !errors.Is(err, ErrNewerDatabase) {
		t.Errorf("newer database: error = %v, want ErrNewerDatabase", err)
	}
}

// versionAt reads the schema version of the database at path without
// migrating it.
func versionAt(t *testing.T, path string) int {
	t.Helper()
	db, err := sql.Open(driverName, path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	return versionOf(t, db)
}

func TestOpenReaderNeverWrites(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tagshelf.db")
	w, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	r := openReader(t, path)
	if _, err := r.StartRun(KindOrganize, false, "s", "d", time.Now()); err == nil {
		t.Error("a reader recorded a run")
	}
}

func TestOpenReaderWhileLibraryLocked(t *testing.T) {
	useDataDir(t)
	lib := t.TempDir()
	db := filepath.Join(t.TempDir(), "tagshelf.db")
	h := startLockHelper(t, lib, db)

	r := openReader(t, db)
	a, err := r.ActiveRun(5)
	if err != nil {
		t.Fatal(err)
	}
	if a == nil || a.Run.ID != h.runID {
		t.Fatalf("ActiveRun = %+v, want the helper's run %d", a, h.runID)
	}
}

func TestDataVersionChangesOnOtherConnectionsCommits(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tagshelf.db")
	w, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	r := openReader(t, path)

	first, err := r.DataVersion()
	if err != nil {
		t.Fatal(err)
	}
	if again, err := r.DataVersion(); err != nil || again != first {
		t.Fatalf("data_version with no change = %d, %v; want %d", again, err, first)
	}
	if _, err := w.StartRun(KindOrganize, false, "s", "d", time.Now()); err != nil {
		t.Fatal(err)
	}
	if after, err := r.DataVersion(); err != nil || after == first {
		t.Fatalf("data_version after a commit = %d, %v; want a change from %d", after, err, first)
	}
}

func TestReadLock(t *testing.T) {
	useDataDir(t)
	dir := t.TempDir()
	db := filepath.Join(dir, "tagshelf.db")

	if _, held, err := ReadLock(db); err != nil || held {
		t.Fatalf("no lock file: held = %t, %v; want free", held, err)
	}

	// This process's own lock reads as held, by this process.
	l, err := AcquireLock(db)
	if err != nil {
		t.Fatal(err)
	}
	h, held, err := ReadLock(db)
	if err != nil || !held || h.PID != os.Getpid() {
		t.Fatalf("own lock: holder %+v, held %t, %v; want held by pid %d", h, held, err, os.Getpid())
	}
	l.Release()
	if _, held, err := ReadLock(db); err != nil || held {
		t.Fatalf("after release: held = %t, %v; want free", held, err)
	}

	// Another process's lock names it, and ReadLock does not disturb it.
	lib := t.TempDir()
	helper := startLockHelper(t, lib, db)
	for range 3 {
		h, held, err = ReadLock(db)
		if err != nil || !held || h.PID != helper.cmd.Process.Pid {
			t.Fatalf("helper's lock: holder %+v, held %t, %v; want held by pid %d", h, held, err, helper.cmd.Process.Pid)
		}
	}
	if s, err := OpenLibrary(lib, db, false); !errors.Is(err, ErrLocked) {
		if err == nil {
			s.Close()
		}
		t.Fatalf("OpenLibrary after ReadLock: %v, want ErrLocked", err)
	}

	// A holder killed with the lock leaves its details behind, but the lock
	// is free.
	helper.cmd.Process.Kill()
	helper.cmd.Wait()
	if data, _ := os.ReadFile(LockPath(db)); len(data) == 0 {
		t.Fatal("the killed helper's details are gone from the lock file; the test needs them")
	}
	if _, held, err := ReadLock(db); err != nil || held {
		t.Fatalf("after the holder died: held = %t, %v; want free", held, err)
	}
	s, err := OpenLibrary(lib, db, false)
	if err != nil {
		t.Fatalf("OpenLibrary after ReadLock of a stale lock: %v", err)
	}
	s.Close()
}

func TestRecordProgressAndActiveRun(t *testing.T) {
	s := openMemory(t)
	if a, err := s.ActiveRun(3); err != nil || a != nil {
		t.Fatalf("ActiveRun with no runs = %+v, %v; want nil", a, err)
	}

	done, err := s.StartRun(KindOrganize, false, "s", "d", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := s.FinishRun(done, Counters{}, StateOK, time.Now()); err != nil {
		t.Fatal(err)
	}
	started := time.Date(2026, 9, 29, 10, 0, 0, 0, time.UTC)
	id, err := s.StartRun(KindReorganize, true, "lib", "lib", started)
	if err != nil {
		t.Fatal(err)
	}

	a, err := s.ActiveRun(3)
	if err != nil || a == nil {
		t.Fatalf("ActiveRun = %+v, %v", a, err)
	}
	if a.Run.ID != id || a.Run.Kind != KindReorganize || !a.Run.DryRun || !a.Run.Started.Equal(started) {
		t.Errorf("run = %+v, want run %d, a dry reorganize started %s", a.Run, id, started)
	}
	if a.Progress != (RunProgress{}) || len(a.Moves) != 0 {
		t.Errorf("before any progress: %+v, %v; want none", a.Progress, a.Moves)
	}

	for i := range 5 {
		if err := s.RecordMove(id, fmt.Sprintf("from%d", i), fmt.Sprintf("to%d", i), ReasonOrganized); err != nil {
			t.Fatal(err)
		}
	}
	at := started.Add(2 * time.Second)
	if err := s.RecordProgress(id, RunProgress{Phase: "audio", Done: 5, Total: 12, At: at}); err != nil {
		t.Fatal(err)
	}
	a, err = s.ActiveRun(3)
	if err != nil || a == nil {
		t.Fatalf("ActiveRun = %+v, %v", a, err)
	}
	want := RunProgress{Phase: "audio", Done: 5, Total: 12, At: at}
	if a.Progress.Phase != want.Phase || a.Progress.Done != want.Done || a.Progress.Total != want.Total || !a.Progress.At.Equal(at) {
		t.Errorf("progress = %+v, want %+v", a.Progress, want)
	}
	var froms []string
	for _, m := range a.Moves {
		froms = append(froms, m.From)
	}
	if fmt.Sprint(froms) != "[from2 from3 from4]" {
		t.Errorf("last moves = %v, want the latest three, oldest first", froms)
	}

	if err := s.FinishRun(id, Counters{}, StateOK, time.Now()); err != nil {
		t.Fatal(err)
	}
	if a, err := s.ActiveRun(3); err != nil || a != nil {
		t.Fatalf("ActiveRun after the run finished = %+v, %v; want nil", a, err)
	}
}

func TestOverview(t *testing.T) {
	s := openMemory(t)
	o, err := s.Overview()
	if err != nil {
		t.Fatal(err)
	}
	if o != (Overview{}) {
		t.Errorf("empty overview = %+v", o)
	}

	lib := setRoot(t, s, RootLibrary, `C:\Music`)
	unt := setRoot(t, s, RootUntagged, `C:\Music\_Untagged`)
	dup := setRoot(t, s, RootDuplicates, `C:\Music\_Duplicates`)
	file := func(root int64, rel, sum string) File {
		return File{RootID: root, RelPath: rel, Sum: sum, Size: 1, ModTime: time.Unix(1, 0), IndexedAt: time.Now()}
	}
	putFiles(t, s,
		file(lib, "a.mp3", "x"), file(lib, "b.mp3", "y"), file(lib, "c.mp3", ""),
		file(unt, "a.mp3", "x"), file(unt, "d.mp3", "z"),
		file(dup, "d.mp3", "z"), file(dup, "e.mp3", "y"),
	)

	first, err := s.StartRun(KindOrganize, false, "s", "d", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := s.FinishRun(first, Counters{Errors: 2}, StateErrors, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.StartRun(KindOrganize, false, "s", "d", time.Now()); err != nil {
		t.Fatal(err)
	}

	o, err = s.Overview()
	if err != nil {
		t.Fatal(err)
	}
	if o.Library != 3 || o.Untagged != 2 || o.Other != 0 || o.Duplicates != 2 {
		t.Errorf("counts = %+v, want library 3, untagged 2, other 0, duplicates 2", o)
	}
	// x is in the library and _Untagged; z and y each have their second copy
	// in _Duplicates only, which does not make a group.
	if n, err := s.GroupsNeedingReview(testKey); err != nil || n != 1 {
		t.Errorf("groups needing review = %d, %v; want 1", n, err)
	}
	if o.LastRun == nil || o.LastRun.ID != first || o.LastRun.State != StateErrors || o.LastRun.Counters.Errors != 2 {
		t.Errorf("last run = %+v, want run %d with 2 errors, not the one still running", o.LastRun, first)
	}
}

func TestOpenLibraryReader(t *testing.T) {
	useDataDir(t)
	lib := t.TempDir()
	db := filepath.Join(t.TempDir(), "tagshelf.db")

	if _, err := OpenLibraryReader(lib, db); !errors.Is(err, ErrNoDatabase) {
		t.Errorf("library without an ID: error = %v, want ErrNoDatabase", err)
	}
	if id, _ := ReadLibraryID(lib); id != "" {
		t.Errorf("OpenLibraryReader wrote a library ID %q", id)
	}

	const id = "3f2a1b2c-0d4e-4f60-8a7b-9c0d1e2f3a4b"
	if err := writeLibraryID(lib, id); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenLibraryReader(lib, db); !errors.Is(err, ErrNoDatabase) {
		t.Errorf("library without a database: error = %v, want ErrNoDatabase", err)
	}

	// An old database is upgraded under the lock, but not while another
	// holds it.
	old, err := open(db, migrations[:1])
	if err != nil {
		t.Fatal(err)
	}
	old.Close()
	lock, err := AcquireLock(db)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := OpenLibraryReader(lib, db); !errors.Is(err, ErrLocked) {
		t.Errorf("old database while locked: error = %v, want ErrLocked", err)
	}
	lock.Release()

	r, err := OpenLibraryReader(lib, db)
	if err != nil {
		t.Fatalf("old database: %v", err)
	}
	defer r.Close()
	if r.LibraryID() != id || r.Path() != db {
		t.Errorf("reader library %q at %q, want %q at %q", r.LibraryID(), r.Path(), id, db)
	}
	if got := versionAt(t, db); got != len(migrations) {
		t.Errorf("schema version = %d, want %d after the upgrade", got, len(migrations))
	}
	if _, held, err := ReadLock(db); err != nil || held {
		t.Errorf("after opening, the lock is held = %t, %v; want free", held, err)
	}

	// With no database named, the default location for the ID is used.
	def, err := DefaultPath(id)
	if err != nil {
		t.Fatal(err)
	}
	w, err := Open(def)
	if err != nil {
		t.Fatal(err)
	}
	w.Close()
	r2, err := OpenLibraryReader(lib, "")
	if err != nil {
		t.Fatal(err)
	}
	defer r2.Close()
	if r2.Path() != def {
		t.Errorf("default reader at %q, want %q", r2.Path(), def)
	}
}
