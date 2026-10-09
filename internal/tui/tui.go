// Package tui is tagshelf's terminal UI: a Bubble Tea app over a library's
// database, which it reads without taking the library lock and reloads
// whenever another process changes it.
package tui

import (
	"context"
	"os"
	"slices"

	tea "charm.land/bubbletea/v2"

	"tagshelf/internal/engine"
	"tagshelf/internal/store"
	"tagshelf/internal/tagwrite"
)

// Options configure the TUI.
type Options struct {
	// Store is the library's database, opened with store.OpenReader.
	Store       *store.Store
	Destination string // the library's root folder
	LibraryID   string
	DryRun      bool
	// Compilations is the folder name compilations are filed under.
	Compilations string
	// Runner does the runs started from Home; nil disables them.
	Runner Runner
}

// Run shows the TUI until the user quits or ctx is cancelled.
func Run(ctx context.Context, opts Options) error {
	src := librarySource{st: opts.Store, pid: os.Getpid()}
	m := New(src, Header{Destination: opts.Destination, LibraryID: opts.LibraryID, DryRun: opts.DryRun})
	m.runner = opts.Runner
	m.compilations = opts.Compilations
	_, err := tea.NewProgram(m, tea.WithContext(ctx)).Run()
	// However the program ended, a run it started stops cleanly before the
	// library's database closes.
	m.stopRun()
	m.stopAction()
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
	// RunDetail reads what one run did.
	RunDetail(id int64) (RunDetail, error)
	// Tags reads the tags of an audio file that the tag editor edits.
	Tags(path string) (tagwrite.Values, error)
}

// Snapshot is what the TUI shows of the library, as read at one moment.
type Snapshot struct {
	Overview store.Overview
	// Active is the run recorded as running, nil when there is none. It is
	// only shown while its process holds the library lock.
	Active *store.ActiveRun
	// Runs is the run history, newest first.
	Runs []store.Run
	// Groups are the duplicate groups waiting for review, and NeedReview how
	// many there are.
	Groups     []store.Group
	NeedReview int64
	// Untagged are the files in the untagged folder, in path order.
	Untagged []store.UntaggedFile
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
	if snap.Active, err = s.st.ActiveRun(recentMoves); err != nil {
		return snap, err
	}
	if snap.Runs, err = s.st.Runs(); err != nil {
		return snap, err
	}
	slices.Reverse(snap.Runs)
	if snap.Groups, err = s.st.GroupsToReview(engine.PossibleKey); err != nil {
		return snap, err
	}
	snap.NeedReview = int64(len(snap.Groups))
	snap.Untagged, err = s.st.UntaggedFiles()
	return snap, err
}

func (s librarySource) RunDetail(id int64) (RunDetail, error) {
	var d RunDetail
	var err error
	if d.Moves, err = s.st.Moves(id); err != nil {
		return d, err
	}
	d.Errors, err = s.st.RunErrors(id)
	return d, err
}

func (s librarySource) Tags(path string) (tagwrite.Values, error) {
	return tagwrite.Read(path)
}
