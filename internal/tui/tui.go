// Package tui is tagshelf's terminal UI: a Bubble Tea app over a library's
// database, which it reads without taking the library lock and reloads
// whenever another process changes it.
package tui

import (
	"context"
	"os"

	tea "charm.land/bubbletea/v2"

	"tagshelf/internal/store"
)

// Options configure the TUI.
type Options struct {
	// Store is the library's database, opened with store.OpenReader.
	Store       *store.Store
	Destination string // the library's root folder
	LibraryID   string
	DryRun      bool
}

// Run shows the TUI until the user quits or ctx is cancelled.
func Run(ctx context.Context, opts Options) error {
	src := librarySource{st: opts.Store, pid: os.Getpid()}
	m := New(src, Header{Destination: opts.Destination, LibraryID: opts.LibraryID, DryRun: opts.DryRun})
	_, err := tea.NewProgram(m, tea.WithContext(ctx)).Run()
	return err
}

// Source is where the TUI reads the library from. Its methods are called off
// the UI goroutine, one poll at a time.
type Source interface {
	// DataVersion changes whenever the library's database does.
	DataVersion() (int64, error)
	// Lock reports whether another process holds the library lock, and who.
	Lock() (holder store.LockHolder, held bool, err error)
	// Load reads everything the TUI shows.
	Load() (Snapshot, error)
}

// Snapshot is what the TUI shows of the library, as read at one moment.
type Snapshot struct {
	Overview store.Overview
	// Active is the run recorded as running, nil when there is none. It is
	// only shown while its process holds the library lock.
	Active *store.ActiveRun
}

// recentMoves is how many of a locked run's latest moves the banner shows at
// most.
const recentMoves = 4

// librarySource reads a library's database.
type librarySource struct {
	st  *store.Store
	pid int // this process, whose own lock is not another's
}

func (s librarySource) DataVersion() (int64, error) {
	return s.st.DataVersion()
}

func (s librarySource) Lock() (store.LockHolder, bool, error) {
	h, held, err := store.ReadLock(s.st.Path())
	if err != nil || !held || h.PID == s.pid {
		return store.LockHolder{}, false, err
	}
	return h, true, nil
}

func (s librarySource) Load() (Snapshot, error) {
	var snap Snapshot
	var err error
	if snap.Overview, err = s.st.Overview(); err != nil {
		return snap, err
	}
	snap.Active, err = s.st.ActiveRun(recentMoves)
	return snap, err
}
