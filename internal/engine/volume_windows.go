//go:build windows

package engine

import (
	"errors"
	"syscall"
)

// errNotSameDevice is Windows' ERROR_NOT_SAME_DEVICE: "The system cannot
// move the file to a different disk drive."
const errNotSameDevice = syscall.Errno(17)

// isCrossVolume reports whether err, from a rename, says that the two paths
// are on different volumes, which is the one failure a copy gets around.
func isCrossVolume(err error) bool {
	return errors.Is(err, errNotSameDevice)
}
