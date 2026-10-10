package store

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// lockFree reports whether nobody holds the lock file at path, and fails the
// test if a free lock file still names a holder.
func lockFree(t *testing.T, path string) bool {
	t.Helper()
	l, err := acquireLock(path)
	if errors.Is(err, ErrLocked) {
		return false
	}
	if err != nil {
		t.Fatal(err)
	}
	l.Release()
	if data, err := os.ReadFile(path); err != nil || len(data) != 0 {
		t.Errorf("free lock file %q holds %q (%v), want it empty", path, data, err)
	}
	return true
}

// TestLibraryLockIsTheLibrarys checks that two runs on one library exclude
// each other although each names a database of its own.
func TestLibraryLockIsTheLibrarys(t *testing.T) {
	useDataDir(t)
	lib := t.TempDir()
	first, err := OpenLibrary(lib, filepath.Join(t.TempDir(), "one.db"), false)
	if err != nil {
		t.Fatal(err)
	}

	other := filepath.Join(t.TempDir(), "two.db")
	for _, database := range []string{other, ""} {
		for _, dryRun := range []bool{false, true} {
			s, err := OpenLibrary(lib, database, dryRun)
			var locked *LockedError
			if !errors.As(err, &locked) {
				if err == nil {
					s.Close()
				}
				t.Fatalf("OpenLibrary with database %q, dry run %t: %v; want a *LockedError", database, dryRun, err)
			}
			if locked.Path != LibraryLockPath(lib) || locked.Holder.PID != os.Getpid() {
				t.Errorf("locked on %q by pid %d, want the library's own lock file %q held by this process", locked.Path, locked.Holder.PID, LibraryLockPath(lib))
			}
		}
	}
	if _, err := os.Stat(other); !os.IsNotExist(err) {
		t.Errorf("a refused run created its database: %v", err)
	}
	if h, held, err := ReadLibraryLock(lib); err != nil || !held || h.PID != os.Getpid() {
		t.Errorf("ReadLibraryLock = %+v, %t, %v; want held by this process", h, held, err)
	}

	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	if _, held, err := ReadLibraryLock(lib); err != nil || held {
		t.Errorf("ReadLibraryLock after Close: held = %t, %v; want free", held, err)
	}
	if !lockFree(t, LibraryLockPath(lib)) || !lockFree(t, LockPath(first.Path())) {
		t.Error("Close left a lock held")
	}
}

// TestLibraryLockFailureReleasesIt checks a run refused at the database's lock
// does not go on holding the library's.
func TestLibraryLockFailureReleasesIt(t *testing.T) {
	useDataDir(t)
	lib := t.TempDir()
	db := filepath.Join(t.TempDir(), "tagshelf.db")
	held, err := AcquireLock(db)
	if err != nil {
		t.Fatal(err)
	}
	defer held.Release()
	if s, err := OpenLibrary(lib, db, false); !errors.Is(err, ErrLocked) {
		if err == nil {
			s.Close()
		}
		t.Fatalf("OpenLibrary on a locked database: %v, want ErrLocked", err)
	}
	if !lockFree(t, LibraryLockPath(lib)) {
		t.Error("the refused open still holds the library's lock")
	}
	if id, err := ReadLibraryID(lib); err != nil || id != "" {
		t.Errorf("the refused open gave the library the ID %q (%v)", id, err)
	}
}

// TestFirstRunsOnOneLibraryDoNotRace starts several first runs on a library
// that has no ID yet: one gets to give it one, the rest are refused as locked,
// and those that come later use the same ID.
func TestFirstRunsOnOneLibraryDoNotRace(t *testing.T) {
	data := useDataDir(t)
	lib := filepath.Join(t.TempDir(), "lib")

	const runs = 8
	var wg sync.WaitGroup
	ids := make([]string, runs)
	errs := make([]error, runs)
	start := make(chan struct{})
	for i := range runs {
		wg.Go(func() {
			<-start
			s, err := OpenLibrary(lib, "", false)
			if err != nil {
				errs[i] = err
				return
			}
			ids[i] = s.LibraryID()
			time.Sleep(20 * time.Millisecond) // a run takes a while
			errs[i] = s.Close()
		})
	}
	close(start)
	wg.Wait()

	want, err := ReadLibraryID(lib)
	if err != nil || want == "" {
		t.Fatalf("library ID = %q, %v; want one", want, err)
	}
	opened := 0
	for i := range runs {
		switch {
		case errs[i] == nil:
			opened++
			if ids[i] != want {
				t.Errorf("run %d opened the library as %q, want %q", i, ids[i], want)
			}
		case !errors.Is(errs[i], ErrLocked):
			t.Errorf("run %d: %v; want it to open or to be refused as locked", i, errs[i])
		}
	}
	if opened == 0 {
		t.Error("no run opened the library")
	}
	libraries, err := os.ReadDir(filepath.Join(data, "tagshelf", "libraries"))
	if err != nil || len(libraries) != 1 || libraries[0].Name() != want {
		t.Errorf("databases were made for %v (%v), want only library %s", libraries, err, want)
	}
}

// TestDatabaseBelongsToOneLibrary checks that a database opened for one
// library is refused for another, whose run would rewrite its roots and drop
// the first library's index.
func TestDatabaseBelongsToOneLibrary(t *testing.T) {
	useDataDir(t)
	first, second := t.TempDir(), filepath.Join(t.TempDir(), "second")
	db := filepath.Join(t.TempDir(), "shared.db")

	s, err := OpenLibrary(first, db, false)
	if err != nil {
		t.Fatal(err)
	}
	firstID := s.LibraryID()
	root := setRoot(t, s, RootLibrary, first)
	putFiles(t, s, File{RootID: root, RelPath: "a.mp3", Sum: "a", Size: 1, ModTime: time.Unix(1, 0)})
	s.Close()

	refused := func(what string, s *Store, err error) {
		t.Helper()
		if !errors.Is(err, ErrOtherLibrary) {
			if err == nil {
				s.Close()
			}
			t.Fatalf("%s: %v; want ErrOtherLibrary", what, err)
		}
		if !strings.Contains(err.Error(), firstID) || !strings.Contains(err.Error(), strconv.Quote(db)) {
			t.Errorf("%s: error %q does not name the database and the library it belongs to", what, err)
		}
	}

	// A folder that is not a library yet stays one that is not.
	s, err = OpenLibrary(second, db, false)
	refused("a new library", s, err)
	if id, err := ReadLibraryID(second); err != nil || id != "" {
		t.Errorf("the refused library was given the ID %q (%v)", id, err)
	}
	if !lockFree(t, LibraryLockPath(second)) || !lockFree(t, LockPath(db)) {
		t.Error("the refused open left a lock held")
	}

	// Nor may a library with an ID of its own have it, to run or to read.
	const secondID = "3f2a1b2c-0d4e-4f60-8a7b-9c0d1e2f3a4b"
	if err := writeLibraryID(second, secondID); err != nil {
		t.Fatal(err)
	}
	for _, dryRun := range []bool{false, true} {
		s, err = OpenLibrary(second, db, dryRun)
		refused("another library", s, err)
	}
	s, err = OpenLibraryReader(second, db)
	refused("another library's reader", s, err)

	// The first library still has its database and index.
	s, err = OpenLibrary(first, db, false)
	if err != nil {
		t.Fatalf("the library the database belongs to: %v", err)
	}
	if roots, err := s.Roots(); err != nil || len(roots) != 1 || roots[0].Path != filepath.Clean(first) {
		t.Errorf("roots = %+v, %v; want the first library's", roots, err)
	}
	if n, err := s.CountFiles(); err != nil || n != 1 {
		t.Errorf("CountFiles = %d, %v; want the first library's file", n, err)
	}
	s.Close()
	r, err := OpenLibraryReader(first, db)
	if err != nil {
		t.Fatalf("the reader of the library the database belongs to: %v", err)
	}
	r.Close()
}

// TestDatabaseWithoutALibraryIsTakenByTheFirst checks a database from before
// databases recorded their library goes to the first library that opens it.
func TestDatabaseWithoutALibraryIsTakenByTheFirst(t *testing.T) {
	useDataDir(t)
	first, second := t.TempDir(), t.TempDir()
	db := filepath.Join(t.TempDir(), "tagshelf.db")
	plain, err := Open(db)
	if err != nil {
		t.Fatal(err)
	}
	if owner, err := plain.owner(); err != nil || owner != "" {
		t.Fatalf("a new database belongs to %q (%v), want to no library", owner, err)
	}
	plain.Close()

	// A reader does not claim it: readers never write.
	const id = "3f2a1b2c-0d4e-4f60-8a7b-9c0d1e2f3a4b"
	if err := writeLibraryID(second, id); err != nil {
		t.Fatal(err)
	}
	r, err := OpenLibraryReader(second, db)
	if err != nil {
		t.Fatal(err)
	}
	if owner, err := r.owner(); err != nil || owner != "" {
		t.Errorf("after a reader, the database belongs to %q (%v), want to no library", owner, err)
	}
	r.Close()

	s, err := OpenLibrary(first, db, false)
	if err != nil {
		t.Fatal(err)
	}
	if owner, err := s.owner(); err != nil || owner != s.LibraryID() || owner == "" {
		t.Errorf("the database belongs to %q (%v), want to the library %q that opened it", owner, err, s.LibraryID())
	}
	s.Close()
	if s, err := OpenLibrary(second, db, false); !errors.Is(err, ErrOtherLibrary) {
		if err == nil {
			s.Close()
		}
		t.Errorf("a second library: %v, want ErrOtherLibrary", err)
	}
}

// TestReadLockForgetsADeadHolder checks that a lock file left naming a
// process that died is emptied by the first read, so that later reads do not
// take the lock again to find out.
func TestReadLockForgetsADeadHolder(t *testing.T) {
	db := filepath.Join(t.TempDir(), "tagshelf.db")
	stale := "pid=999999\nstarted=" + formatTime(time.Now().Add(-time.Hour)) + "\n"
	for _, read := range []struct {
		path string
		read func() (LockHolder, bool, error)
	}{
		{LockPath(db), func() (LockHolder, bool, error) { return ReadLock(db) }},
		{LibraryLockPath(filepath.Dir(db)), func() (LockHolder, bool, error) { return ReadLibraryLock(filepath.Dir(db)) }},
	} {
		if err := os.WriteFile(read.path, []byte(stale), 0o644); err != nil {
			t.Fatal(err)
		}
		if h, held, err := read.read(); err != nil || held {
			t.Fatalf("a dead holder's lock reads as held by %+v (%v)", h, err)
		}
		if data, err := os.ReadFile(read.path); err != nil || len(data) != 0 {
			t.Errorf("%q still names the dead holder: %q, %v", read.path, data, err)
		}
		if _, held, err := read.read(); err != nil || held {
			t.Errorf("the emptied lock file reads as held (%v)", err)
		}
	}
}

// TestAcquireLockOutlastsAReadersCheck checks that a lock held only for an
// instant, as ReadLock holds it to check a dead holder, does not refuse a run.
func TestAcquireLockOutlastsAReadersCheck(t *testing.T) {
	db := filepath.Join(t.TempDir(), "tagshelf.db")
	reader, err := os.OpenFile(LockPath(db), os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	if held, err := tryLock(reader); err != nil || held {
		t.Fatalf("tryLock = %t, %v", held, err)
	}
	released := make(chan struct{})
	go func() {
		defer close(released)
		time.Sleep(5 * time.Millisecond)
		unlock(reader)
	}()

	l, err := AcquireLock(db)
	if err != nil {
		t.Fatalf("AcquireLock while a reader checked the lock: %v", err)
	}
	l.Release()
	<-released

	// A lock that stays held is still refused, and soon.
	holder, err := AcquireLock(db)
	if err != nil {
		t.Fatal(err)
	}
	defer holder.Release()
	start := time.Now()
	if _, err := AcquireLock(db); !errors.Is(err, ErrLocked) {
		t.Fatalf("AcquireLock of a held lock: %v, want ErrLocked", err)
	}
	if waited := time.Since(start); waited > time.Second {
		t.Errorf("AcquireLock waited %s for a held lock", waited)
	}
}
