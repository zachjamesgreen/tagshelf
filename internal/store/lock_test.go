package store

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The lock tests run a second process: this test binary again, as
// TestLockHelperProcess, with lockHelperEnv naming the library to hold.
const (
	lockHelperEnv   = "TAGSHELF_LOCK_HELPER_LIBRARY"
	lockHelperDBEnv = "TAGSHELF_LOCK_HELPER_DB"
)

// TestLockHelperProcess is not a test: run by startLockHelper, it opens the
// library, starts a run, prints "locked <run id>" and then holds the lock
// until its stdin closes or it is killed.
func TestLockHelperProcess(t *testing.T) {
	lib := os.Getenv(lockHelperEnv)
	if lib == "" {
		t.Skip("helper process for the library lock tests")
	}
	s, err := OpenLibrary(lib, os.Getenv(lockHelperDBEnv), false)
	if err != nil {
		fmt.Println("error", err)
		os.Exit(1)
	}
	id, err := s.StartRun(KindOrganize, false, "src", lib, time.Now())
	if err != nil {
		fmt.Println("error", err)
		os.Exit(1)
	}
	fmt.Println("locked", id)
	bufio.NewReader(os.Stdin).ReadString('\n') // until the parent closes stdin
	s.Close()
	os.Exit(0)
}

type lockHelper struct {
	cmd   *exec.Cmd
	stdin interface{ Close() error }
	runID int64
}

// startLockHelper starts a process that holds the lock of the library at lib
// (with its database at db, or "" for the default) and waits until it does.
func startLockHelper(t *testing.T, lib, db string) *lockHelper {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestLockHelperProcess$")
	cmd.Env = append(os.Environ(), lockHelperEnv+"="+lib, lockHelperDBEnv+"="+db)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	h := &lockHelper{cmd: cmd, stdin: stdin}
	t.Cleanup(func() {
		cmd.Process.Kill()
		cmd.Wait()
	})

	line, err := bufio.NewReader(stdout).ReadString('\n')
	if _, scanErr := fmt.Sscanf(line, "locked %d", &h.runID); err != nil || scanErr != nil {
		t.Fatalf("helper did not take the lock: %q, %v", line, err)
	}
	return h
}

func TestLockSecondProcessFailsWithHolder(t *testing.T) {
	useDataDir(t)
	lib := t.TempDir()
	db := filepath.Join(t.TempDir(), "tagshelf.db")
	before := time.Now().Add(-time.Second)
	h := startLockHelper(t, lib, db)

	start := time.Now()
	s, err := OpenLibrary(lib, db, false)
	if err == nil {
		s.Close()
		t.Fatal("OpenLibrary succeeded while another process holds the lock")
	}
	if waited := time.Since(start); waited > 2*time.Second {
		t.Errorf("OpenLibrary waited %s for the lock, want an immediate failure", waited)
	}
	var locked *LockedError
	if !errors.As(err, &locked) || !errors.Is(err, ErrLocked) {
		t.Fatalf("OpenLibrary error = %v, want a *LockedError", err)
	}
	if locked.Holder.PID != h.cmd.Process.Pid {
		t.Errorf("holder pid = %d, want the helper's %d", locked.Holder.PID, h.cmd.Process.Pid)
	}
	if locked.Holder.Started.Before(before) || locked.Holder.Started.After(time.Now()) {
		t.Errorf("holder started %s, want between %s and now", locked.Holder.Started, before)
	}
	if !strings.Contains(err.Error(), fmt.Sprint(h.cmd.Process.Pid)) {
		t.Errorf("error %q does not name the holder pid", err)
	}

	// A dry run is refused the same way.
	if s, err := OpenLibrary(lib, db, true); !errors.Is(err, ErrLocked) {
		if err == nil {
			s.Close()
		}
		t.Errorf("dry run OpenLibrary error = %v, want ErrLocked", err)
	}

	// The refused opens must not have marked the holder's run crashed.
	plain, err := Open(db)
	if err != nil {
		t.Fatal(err)
	}
	runs, err := plain.Runs()
	plain.Close()
	if err != nil || len(runs) != 1 || runs[0].State != StateRunning {
		t.Fatalf("runs = %+v, %v; want the helper's run still running", runs, err)
	}

	h.stdin.Close()
	if err := h.cmd.Wait(); err != nil {
		t.Fatalf("helper: %v", err)
	}
	s, err = OpenLibrary(lib, db, false)
	if err != nil {
		t.Fatalf("OpenLibrary after the holder closed: %v", err)
	}
	s.Close()
}

func TestLockReleasedWhenHolderIsKilled(t *testing.T) {
	useDataDir(t)
	lib := t.TempDir()
	h := startLockHelper(t, lib, "")

	if s, err := OpenLibrary(lib, "", false); !errors.Is(err, ErrLocked) {
		if err == nil {
			s.Close()
		}
		t.Fatalf("OpenLibrary error = %v, want ErrLocked while the helper runs", err)
	}

	if err := h.cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	h.cmd.Wait()

	s, err := OpenLibrary(lib, "", false)
	if err != nil {
		t.Fatalf("OpenLibrary after killing the holder: %v", err)
	}
	defer s.Close()
	runs, err := s.Runs()
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 1 || runs[0].ID != h.runID || runs[0].State != StateCrashed {
		t.Fatalf("runs = %+v, want the killed helper's run %d crashed", runs, h.runID)
	}
	if !runs[0].Ended.IsZero() {
		t.Errorf("crashed run has end time %s, want unknown", runs[0].Ended)
	}
}

func TestLockReleasedOnClose(t *testing.T) {
	useDataDir(t)
	lib := t.TempDir()

	first, err := OpenLibrary(lib, "", false)
	if err != nil {
		t.Fatal(err)
	}
	// The lock is per open file, so this process cannot take it twice.
	if s, err := OpenLibrary(lib, "", false); !errors.Is(err, ErrLocked) {
		if err == nil {
			s.Close()
		}
		t.Fatalf("second OpenLibrary error = %v, want ErrLocked", err)
	}
	var locked *LockedError
	_, err = AcquireLock(first.Path())
	if !errors.As(err, &locked) || locked.Holder.PID != os.Getpid() {
		t.Fatalf("AcquireLock error = %v, want this process as holder", err)
	}
	id, err := first.StartRun(KindOrganize, false, "s", lib, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := first.FinishRun(id, Counters{}, StateOK, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}

	second, err := OpenLibrary(lib, "", false)
	if err != nil {
		t.Fatalf("OpenLibrary after Close: %v", err)
	}
	defer second.Close()
	if runs, _ := second.Runs(); len(runs) != 1 || runs[0].State != StateOK {
		t.Errorf("runs = %+v, want the finished run left ok", runs)
	}
}

func TestLockCrashedOnlyWhenNobodyHoldsIt(t *testing.T) {
	useDataDir(t)
	lib := t.TempDir()
	db := filepath.Join(t.TempDir(), "tagshelf.db")

	// A run left running by a process that died: recorded without the lock.
	plain, err := Open(db)
	if err != nil {
		t.Fatal(err)
	}
	dead, err := plain.StartRun(KindOrganize, false, "s", lib, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	plain.Close()

	s, err := OpenLibrary(lib, db, false)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	live, err := s.StartRun(KindReorganize, false, lib, lib, time.Now())
	if err != nil {
		t.Fatal(err)
	}

	// The live run's process holds the lock, so nobody may crash it.
	if other, err := OpenLibrary(lib, db, false); !errors.Is(err, ErrLocked) {
		if err == nil {
			other.Close()
		}
		t.Fatalf("OpenLibrary error = %v, want ErrLocked", err)
	}
	runs, err := s.Runs()
	if err != nil {
		t.Fatal(err)
	}
	states := map[int64]RunState{}
	for _, r := range runs {
		states[r.ID] = r.State
	}
	if states[dead] != StateCrashed || states[live] != StateRunning {
		t.Errorf("states = %v, want run %d crashed and run %d running", states, dead, live)
	}
}

func TestLockDryRunInMemoryTakesNoLock(t *testing.T) {
	data := useDataDir(t)
	lib := t.TempDir()
	db := filepath.Join(t.TempDir(), "tagshelf.db")

	for _, database := range []string{"", db} {
		first, err := OpenLibrary(lib, database, true)
		if err != nil {
			t.Fatal(err)
		}
		// Two dry runs on an unidentified library do not exclude each other.
		second, err := OpenLibrary(lib, database, true)
		if err != nil {
			t.Fatalf("second in-memory dry run: %v", err)
		}
		second.Close()
		first.Close()
	}
	if _, err := os.Stat(LockPath(db)); !os.IsNotExist(err) {
		t.Errorf("in-memory dry run created the lock file: %v", err)
	}
	if entries, _ := os.ReadDir(data); len(entries) != 0 {
		t.Errorf("in-memory dry run wrote under the data folder: %v", entries)
	}
}

func TestLockUnreadableHolder(t *testing.T) {
	db := filepath.Join(t.TempDir(), "tagshelf.db")
	l, err := AcquireLock(db)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Release()
	if err := os.WriteFile(LockPath(db), []byte("garbage"), 0o644); err != nil {
		t.Skipf("cannot overwrite a held lock file here: %v", err)
	}
	_, err = AcquireLock(db)
	var locked *LockedError
	if !errors.As(err, &locked) || locked.Holder.PID != 0 {
		t.Fatalf("AcquireLock error = %v, want a *LockedError with no holder", err)
	}
	if !strings.Contains(err.Error(), LockPath(db)) {
		t.Errorf("error %q does not name the lock file", err)
	}
}
