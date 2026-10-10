//go:build !windows

package engine

import (
	"errors"
	"path/filepath"
	"syscall"
)

// isCrossVolume reports whether err, from a rename, says that the two paths
// are on different file systems, which is the one failure a copy gets around.
func isCrossVolume(err error) bool {
	return errors.Is(err, syscall.EXDEV)
}

// volumeRoot reports whether dir is where a file system is mounted: the root
// folder, or a folder on another device than the one above it.
func volumeRoot(dir string) bool {
	parent := filepath.Dir(dir)
	if parent == dir {
		return true
	}
	var here, above syscall.Stat_t
	if syscall.Stat(dir, &here) != nil || syscall.Stat(parent, &above) != nil {
		return false
	}
	return here.Dev != above.Dev
}
