package store

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// ErrLocked is returned when another process holds the library lock. The
// error is a *LockedError, which says who holds it.
var ErrLocked = errors.New("the library is locked by another tagshelf run")

// LockedError is the library lock being held by another process.
type LockedError struct {
	Path   string     // the lock file
	Holder LockHolder // zero when the holder could not be read
}

func (e *LockedError) Error() string {
	if e.Holder.PID == 0 {
		return fmt.Sprintf("%v (%s)", ErrLocked, e.Path)
	}
	return fmt.Sprintf("%v: pid %d, started %s (%s)", ErrLocked, e.Holder.PID,
		e.Holder.Started.Local().Format("2006-01-02 15:04:05"), e.Path)
}

func (e *LockedError) Is(target error) bool { return target == ErrLocked }

// LockHolder is the process holding the library lock.
type LockHolder struct {
	PID     int
	Started time.Time
}

// The library lock is two lock files, taken in this order and held together:
// one in the library's destination root, so that two processes working on one
// library exclude each other whatever database each was told to use, and one
// beside the database, so that two libraries never write one database.

// LibraryLockFile is the lock file in a library's destination root. Like the
// library ID file beside it, walks and cleanup leave it alone.
const LibraryLockFile = OwnFilePrefix + "lock"

// LibraryLockPath is the lock file in the destination root of a library.
func LibraryLockPath(destination string) string {
	return filepath.Join(destination, LibraryLockFile)
}

// LockPath is the lock file of the database at dbPath.
func LockPath(dbPath string) string {
	return dbPath + ".lock"
}

// Lock is a held lock file: an exclusive OS lock on it, which the OS releases
// if the process dies. The file itself stays; it names the holder while the
// lock is held.
type Lock struct {
	f *os.File
}

// AcquireLibraryLock takes the lock in the destination root of a library
// without waiting, creating the destination if needed. If another process
// holds it, the error is a *LockedError.
func AcquireLibraryLock(destination string) (*Lock, error) {
	if err := os.MkdirAll(destination, 0o755); err != nil {
		return nil, fmt.Errorf("creating the destination: %w", err)
	}
	return acquireLock(LibraryLockPath(destination))
}

// AcquireLock takes the lock of the database at dbPath without waiting. If
// another process holds it, the error is a *LockedError.
//
// The lock is per open file, not per process: a second AcquireLock in the same
// process fails too.
func AcquireLock(dbPath string) (*Lock, error) {
	path := LockPath(dbPath)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("creating the database folder: %w", err)
	}
	return acquireLock(path)
}

// lockTries and lockRetryEvery are how AcquireLock outlasts a ReadLock that
// is checking a stale holder: that check holds the lock for an instant, and
// must not be taken for a run.
const (
	lockTries      = 5
	lockRetryEvery = 10 * time.Millisecond
)

func acquireLock(path string) (*Lock, error) {
	// Never truncated on open: until the lock is ours, the file names the holder.
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		return nil, fmt.Errorf("opening the library lock: %w", err)
	}
	var held bool
	for try := range lockTries {
		if try > 0 {
			time.Sleep(lockRetryEvery)
		}
		if held, err = tryLock(f); err != nil || !held {
			break
		}
	}
	if err != nil {
		f.Close()
		return nil, fmt.Errorf("taking the library lock %q: %w", path, err)
	}
	if held {
		holder := readHolder(f)
		f.Close()
		return nil, &LockedError{Path: path, Holder: holder}
	}
	l := &Lock{f: f}
	if err := l.writeHolder(LockHolder{PID: os.Getpid(), Started: time.Now()}); err != nil {
		l.Release()
		return nil, fmt.Errorf("writing the library lock %q: %w", path, err)
	}
	return l, nil
}

// Release gives the lock up. It is safe to call more than once.
func (l *Lock) Release() error {
	if l == nil || l.f == nil {
		return nil
	}
	f := l.f
	l.f = nil
	// Forget the holder first, so the file never names a process that is gone
	// unless that process died holding the lock.
	f.Truncate(0)
	err := unlock(f)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}

// writeHolder records the holder in the lock file, where other processes can
// read it: the OS lock never covers the text.
func (l *Lock) writeHolder(h LockHolder) error {
	text := fmt.Sprintf("pid=%d\nstarted=%s\n", h.PID, formatTime(h.Started))
	if _, err := l.f.WriteAt([]byte(text), 0); err != nil {
		return err
	}
	if err := l.f.Truncate(int64(len(text))); err != nil {
		return err
	}
	return l.f.Sync()
}

// readHolder reads who holds the lock in f. The holder writes its details just
// after taking the lock, so an empty or partial file is retried briefly; the
// zero LockHolder means they could not be read.
func readHolder(f *os.File) LockHolder {
	for range 10 {
		if h, ok := parseHolder(f); ok {
			return h
		}
		time.Sleep(20 * time.Millisecond)
	}
	return LockHolder{}
}

func parseHolder(f *os.File) (LockHolder, bool) {
	data, err := io.ReadAll(io.NewSectionReader(f, 0, 4096))
	if err != nil {
		return LockHolder{}, false
	}
	var h LockHolder
	for line := range strings.SplitSeq(string(data), "\n") {
		key, value, _ := strings.Cut(strings.TrimSpace(line), "=")
		switch key {
		case "pid":
			h.PID, _ = strconv.Atoi(value)
		case "started":
			h.Started, _ = time.Parse(timeFormat, value)
		}
	}
	return h, h.PID > 0 && !h.Started.IsZero()
}
