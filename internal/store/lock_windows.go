//go:build windows

package store

import (
	"errors"
	"os"

	"golang.org/x/sys/windows"
)

// lockOffset is the byte the lock covers. Windows locks are mandatory, so
// locking the holder's text would stop other processes reading it; a single
// byte far past the end of the file locks nothing anyone reads.
const lockOffset int64 = 1 << 40

func lockRange() *windows.Overlapped {
	return &windows.Overlapped{Offset: uint32(lockOffset & 0xffffffff), OffsetHigh: uint32(lockOffset >> 32)}
}

// tryLock takes an exclusive lock on f without waiting. held reports that
// another open file already has it.
func tryLock(f *os.File) (held bool, err error) {
	err = windows.LockFileEx(windows.Handle(f.Fd()),
		windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, lockRange())
	if errors.Is(err, windows.ERROR_LOCK_VIOLATION) {
		return true, nil
	}
	return false, err
}

func unlock(f *os.File) error {
	return windows.UnlockFileEx(windows.Handle(f.Fd()), 0, 1, 0, lockRange())
}
