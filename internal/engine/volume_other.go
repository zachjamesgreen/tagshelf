//go:build !windows

package engine

import (
	"errors"
	"syscall"
)

// isCrossVolume reports whether err, from a rename, says that the two paths
// are on different file systems, which is the one failure a copy gets around.
func isCrossVolume(err error) bool {
	return errors.Is(err, syscall.EXDEV)
}
