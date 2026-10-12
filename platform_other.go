//go:build !windows

package main

import (
	"os"
	"path/filepath"
	"syscall"
)

// stopSignals are what stops a run the gentle way: Ctrl-C, the signal a
// system sends to end a program, and the one it sends when the terminal the
// program runs in goes away.
var stopSignals = []os.Signal{os.Interrupt, syscall.SIGTERM, syscall.SIGHUP}

// ownTempFolder gives this process a temp folder of the user's own: tagshelf
// in the cache folder, which is ~/.cache/tagshelf unless XDG_CACHE_HOME says
// otherwise.
//
// The tag library compiles its code once and keeps the result in the temp
// folder. That is /tmp here, which all users share: whoever runs tagshelf
// second cannot write to the folder the first one made, and then no file's
// audio details can be read. tagshelf's own temporary files are made beside
// what they replace, so the tag library and SQLite are all that follow.
//
// Without a cache folder to use the temp folder stays as it is.
func ownTempFolder() {
	cache, err := os.UserCacheDir()
	if err != nil {
		return
	}
	dir := filepath.Join(cache, "tagshelf")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return
	}
	os.Setenv("TMPDIR", dir)
}
