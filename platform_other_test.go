//go:build !windows

package main

import (
	"os"
	"path/filepath"
	"slices"
	"syscall"
	"testing"
	"time"
)

// TestOwnTempFolder checks where the temp folder of the process is put: in
// the user's cache folder, as a folder of the user's alone, and nowhere when
// there is no cache folder.
func TestOwnTempFolder(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, "cache"))
	t.Setenv("TMPDIR", "/tmp")
	cache, err := os.UserCacheDir()
	if err != nil {
		t.Fatal(err)
	}
	ownTempFolder()
	want := filepath.Join(cache, "tagshelf")
	if got := os.TempDir(); got != want {
		t.Errorf("the temp folder is %q, want %q", got, want)
	}
	if info, err := os.Stat(want); err != nil || !info.IsDir() || info.Mode().Perm() != 0o700 {
		t.Errorf("the temp folder: %v, %v; want a folder of the user's alone", info, err)
	}

	// With nowhere to keep a cache the temp folder stays as it is.
	t.Setenv("XDG_CACHE_HOME", "")
	t.Setenv("HOME", "")
	t.Setenv("TMPDIR", "/var/tmp")
	ownTempFolder()
	if got := os.TempDir(); got != "/var/tmp" {
		t.Errorf("without a cache folder the temp folder is %q, want it left at /var/tmp", got)
	}
}

// TestStopSignals checks that a run stops the gentle way when the system
// tells tagshelf to end, or its terminal goes away, as it does on Ctrl-C.
func TestStopSignals(t *testing.T) {
	for _, sig := range []os.Signal{os.Interrupt, syscall.SIGTERM, syscall.SIGHUP} {
		if !slices.Contains(stopSignals, sig) {
			t.Errorf("%v does not stop a run", sig)
		}
	}
	ctx := interruptContext()
	if err := syscall.Kill(os.Getpid(), syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	select {
	case <-ctx.Done():
	case <-time.After(5 * time.Second):
		t.Error("SIGTERM did not cancel the run's context")
	}
}
