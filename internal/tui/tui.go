// Package tui is tagshelf's terminal UI: a Bubble Tea app over a library's
// database, which it reads without taking the library lock and reloads
// whenever another process changes it.
package tui

import (
	"context"
	"slices"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/zachjamesgreen/tagshelf/internal/engine"
	"github.com/zachjamesgreen/tagshelf/internal/store"
	"github.com/zachjamesgreen/tagshelf/internal/tagwrite"
)

// Options configure the TUI.
type Options struct {
	// Store is the library's database, opened with store.OpenReader. It is
	// nil when there is no library to show, and NoLibrary then says why.
	Store       *store.Store
	Destination string // the library's root folder
	LibraryID   string
	DryRun      bool
	// Compilations is the folder name compilations are filed under.
	Compilations string
	// MusicBrainzContact is the email address or URL MusicBrainz lookups
	// identify themselves with; lookups are off while it is empty.
	MusicBrainzContact string
	// GenreFromMusicBrainz is what a match does with the genre: config's
	// GenreOff, GenreFill or GenreReplace.
	GenreFromMusicBrainz string
	// Runner does the runs started from Home; nil disables them.
	Runner Runner
	// NoLibrary says why Store is nil.
	NoLibrary *NoLibrary
	// Reopen opens the library again, and returns the options to go on
	// with. The TUI calls it once a run it started without a library has
	// ended, which may have made one. It is called off the UI goroutine; nil
	// leaves the TUI with what it was started on.
	Reopen func() (Options, error)
	// Settings is the config file the Settings tab edits; nil leaves the
	// tab with nothing to change.
	Settings Settings

	// source is read in place of Store when set: the tests' own.
	source Source
}

// Run shows the TUI until the user quits or ctx is cancelled.
func Run(ctx context.Context, opts Options) error {
	m := New(noSource{}, Header{})
	m.open(opts, true)
	return m.show(ctx)
}

// show runs the model as a program until the user quits or ctx is
// cancelled; options are for the tests, which have no terminal.
func (m *Model) show(ctx context.Context, options ...tea.ProgramOption) error {
	_, err := tea.NewProgram(m, append([]tea.ProgramOption{tea.WithContext(ctx)}, options...)...).Run()
	// However the program ended, a run or an action it started stops cleanly
	// before the library's database closes.
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
	// Load reads what every tab shows of the library, and of its lists
	// those parts names.
	Load(parts Parts) (Snapshot, error)
	// RunDetail reads what one run did.
	RunDetail(id int64) (RunDetail, error)
	// Tags reads the tags of an audio file that the tag editor edits.
	Tags(path string) (tagwrite.Values, error)
	// UndoPlan works out what undoing a run would do, changing nothing.
	UndoPlan(runID int64) (engine.UndoPlan, error)
	// Search lists up to limit files of the library whose artist, album or
	// title holds every word of query.
	Search(query string, limit int) ([]store.File, error)
	// Album lists the files of the album f is on.
	Album(f store.File) ([]store.File, error)
	// LibraryReleases lists the MusicBrainz release IDs of the files in the
	// library.
	LibraryReleases() (map[string]bool, error)
}

// Parts names the lists of a Snapshot. Each is as long as the library is
// large and is shown by one tab, so a load reads only those asked for.
type Parts struct {
	Untagged bool // the Untagged tab's files
	Groups   bool // the Duplicates tab's groups
	Runs     bool // the Runs tab's history
}

// lacks reports whether p is without a part want names.
func (p Parts) lacks(want Parts) bool {
	return want.Untagged && !p.Untagged || want.Groups && !p.Groups || want.Runs && !p.Runs
}

// Snapshot is what the TUI shows of the library, as read at one moment.
type Snapshot struct {
	Overview store.Overview
	// Active is the run recorded as running, nil when there is none. It is
	// only shown while its process holds the library lock.
	Active *store.ActiveRun
	// NeedReview is how many duplicate groups wait for review.
	NeedReview int64

	// Parts says which of the lists below were read.
	Parts Parts
	// Runs is the run history, newest first.
	Runs []store.Run
	// Groups are the duplicate groups waiting for review.
	Groups []store.Group
	// Untagged are the files in the untagged folder, in path order.
	Untagged []store.UntaggedFile
}

// recentMoves is how many of a locked run's latest moves the banner shows at
// most.
const recentMoves = 4

// runReload is how often at most the library is read again while a run is
// going: a run changes it with every file it moves.
const runReload = 3 * time.Second

// librarySource reads a library's database.
type librarySource struct {
	st          *store.Store
	destination string // the library's root folder, "" when it is not known
	pid         int    // this process, whose own lock is not another's
}

func (s librarySource) DataVersion() (int64, error) {
	return s.st.DataVersion()
}

// Lock reads the lock of the database and the lock of the library: a run on
// the library that uses another database holds only the second.
func (s librarySource) Lock() (store.LockHolder, bool, error) {
	h, held, err := store.ReadLock(s.st.Path())
	if err != nil {
		return store.LockHolder{}, false, err
	}
	if held && h.PID != s.pid {
		return h, true, nil
	}
	if s.destination == "" {
		return store.LockHolder{}, false, nil
	}
	h, held, err = store.ReadLibraryLock(s.destination)
	if err != nil || !held || h.PID == s.pid {
		return store.LockHolder{}, false, err
	}
	return h, true, nil
}

func (s librarySource) Load(parts Parts) (Snapshot, error) {
	snap := Snapshot{Parts: parts}
	var err error
	if snap.Overview, err = s.st.Overview(); err != nil {
		return snap, err
	}
	if snap.Active, err = s.st.ActiveRun(recentMoves); err != nil {
		return snap, err
	}
	if parts.Groups {
		if snap.Groups, err = s.st.GroupsToReview(engine.PossibleKey); err != nil {
			return snap, err
		}
		snap.NeedReview = int64(len(snap.Groups))
	} else {
		// Counting them reads no file's row in full.
		n, err := s.st.GroupsNeedingReview(engine.PossibleKey)
		if err != nil {
			return snap, err
		}
		snap.NeedReview = int64(n)
	}
	if parts.Runs {
		if snap.Runs, err = s.st.Runs(); err != nil {
			return snap, err
		}
		slices.Reverse(snap.Runs)
	}
	if parts.Untagged {
		if snap.Untagged, err = s.st.UntaggedFiles(); err != nil {
			return snap, err
		}
	}
	return snap, nil
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

func (s librarySource) LibraryReleases() (map[string]bool, error) {
	return s.st.ReleaseIDs(store.RootLibrary)
}

func (s librarySource) Search(query string, limit int) ([]store.File, error) {
	return s.st.SearchLibrary(query, limit)
}

func (s librarySource) Album(f store.File) ([]store.File, error) {
	return s.st.FolderFiles(f)
}

func (s librarySource) UndoPlan(runID int64) (engine.UndoPlan, error) {
	return engine.PlanUndo(s.st, runID, undoCheck)
}
