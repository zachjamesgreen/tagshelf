package store

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
)

// ReadLock reports whether a process holds the lock of the database at dbPath
// and, when it does, who. It never waits and never takes the lock for itself,
// except for an instant when the lock file still names a holder that is gone
// (a process that died holding it): then it checks the lock is free by taking
// it, forgets the dead holder so that the next read need not check again, and
// at once releases it. AcquireLock outlasts that instant. A holder that has
// taken the lock but not yet written its details reads as not held.
//
// The lock is per open file, so a lock this process holds reads as held too,
// with this process's pid.
func ReadLock(dbPath string) (holder LockHolder, held bool, err error) {
	return readLock(LockPath(dbPath))
}

// ReadLibraryLock is ReadLock for the lock in the destination root of a
// library, which every run on the library holds whatever its database.
func ReadLibraryLock(destination string) (holder LockHolder, held bool, err error) {
	return readLock(LibraryLockPath(destination))
}

func readLock(path string) (holder LockHolder, held bool, err error) {
	// Opened for writing when that is allowed, only to forget a dead holder.
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		f, err = os.Open(path)
	}
	if errors.Is(err, fs.ErrNotExist) {
		return LockHolder{}, false, nil
	}
	if err != nil {
		return LockHolder{}, false, fmt.Errorf("reading the library lock: %w", err)
	}
	defer f.Close()
	// A holder gives up the lock only after emptying the file, so an empty
	// file means nobody holds it, without touching the lock.
	h, ok := parseHolder(f)
	if !ok {
		return LockHolder{}, false, nil
	}
	busy, err := tryLock(f)
	if err != nil {
		return LockHolder{}, false, fmt.Errorf("reading the library lock %q: %w", path, err)
	}
	if !busy {
		// The named holder died holding the lock; the OS released it. While
		// the lock is ours nobody else can be writing their name.
		f.Truncate(0)
		unlock(f)
		return LockHolder{}, false, nil
	}
	return h, true, nil
}
