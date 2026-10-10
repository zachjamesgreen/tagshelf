package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"log"
	"os"
	"sync"

	"github.com/zachjamesgreen/tagshelf/internal/config"
	"github.com/zachjamesgreen/tagshelf/internal/engine"
	"github.com/zachjamesgreen/tagshelf/internal/store"
	"github.com/zachjamesgreen/tagshelf/internal/tui"
)

// showTUI shows the TUI until the user quits or the context is cancelled.
// The tests put a stand-in in its place: the real one needs a terminal.
var showTUI = tui.Run

// runTUI runs `tagshelf tui [flags]` and returns the process exit code. The
// TUI opens whenever it can: without a library folder, or on a library with
// no index yet, it says what is missing where a run would be refused.
func runTUI(ctx context.Context, args []string) int {
	set := flag.NewFlagSet("tagshelf tui", flag.ContinueOnError)
	flags := tuiFlags(set)
	if code, done := flags.parse(args); done {
		return code
	}
	s := &tuiSession{flags: flags}
	// The database closes once the TUI has returned, which it does only
	// after stopping whatever run or action it had going.
	defer s.close()
	opts, err := s.open()
	var path *pathProblem
	switch {
	case errors.As(err, &path):
		return path.refusal().print()
	case err != nil:
		return libraryProblem(err, opts.Destination)
	}
	if s.file.Skipped != "" {
		log.Printf("Warning: ignoring the %s in the working directory: %s\n", config.DefaultConfigName, s.file.Skipped)
	}

	err = showTUI(ctx, opts)
	if ctx.Err() != nil {
		return exitInterrupted
	}
	if err != nil {
		fmt.Fprintf(log.Writer(), "The terminal UI stopped: %v.\n", err)
		return exitErrors
	}
	return exitOK
}

// tuiFlags defines the flags of `tagshelf tui` on set: the CLI's folder
// flags, so that the runs the TUI starts use the folders a CLI run with the
// same flags would.
func tuiFlags(set *flag.FlagSet) *libraryFlags {
	return addLibraryFlags(set, tuiHelp,
		"The `folder` of music that o organizes into the library.",
		"The library: the `folder` of organized music to open.",
		"Start with dry run on.")
}

// tuiSession is the library `tagshelf tui` shows. It opens it when the TUI
// starts, and again when the TUI asks: after a run that may have made the
// index it was started without.
type tuiSession struct {
	flags *libraryFlags

	// mu guards what follows: the TUI opens the library again, and reads
	// and saves the settings, off its UI goroutine.
	mu sync.Mutex
	// file is the config file as last read.
	file config.File
	// st is the library's database, nil while there is no library, and cfg
	// the configuration it was opened by.
	st  *store.Store
	cfg config.Config
}

func (s *tuiSession) close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closeStore()
}

func (s *tuiSession) closeStore() {
	if s.st != nil {
		s.st.Close()
		s.st = nil
	}
}

// settings reads the config file and the flags again. With defaults filled
// in and paths made absolute they are the configuration the TUI's runs go
// by. When there is no library folder to go by, none says why, and cfg is
// not to be used. A folder typed as a flag that this system cannot use is an
// error, a *pathProblem: the Settings tab changes the file, not the flags.
func (s *tuiSession) settings() (cfg config.Config, none *tui.NoLibrary, err error) {
	f, err := config.Read(*s.flags.config)
	s.file = f
	switch {
	case err != nil && f.From == config.FromFlag && errors.Is(err, fs.ErrNotExist):
		// A file named for the TUI need not be there yet: the settings are
		// then the flags alone.
	case err != nil:
		return config.Config{}, &tui.NoLibrary{What: err.Error(), Todo: []string{"Fix the file in an editor, then press R on 6 Settings to read it again."}}, nil
	}
	cfg = f.Config.With(s.flags.overrides())
	if cfg.Destination == "" {
		return cfg, &tui.NoLibrary{What: "no library folder is set", Setting: "destination", Todo: []string{
			"Press 6 to set it in Settings, which keeps it in " + s.configFileToEdit() + ".",
			here(`Or start again with one, as in: tagshelf tui -dest "D:\Music"`),
		}}, nil
	}
	cfg, _, problem := usablePaths(cfg, pathOrigin{flags: s.flags.typedPaths(), file: f.Path})
	switch {
	case problem != nil && problem.flag != "":
		return cfg, nil, problem
	case problem != nil:
		return cfg, &tui.NoLibrary{What: problem.what(), Setting: problem.key, Todo: []string{
			"Press 6 to change it in Settings. " + problem.todo,
		}}, nil
	}
	return cfg, nil, nil
}

// configFileToEdit names the config file a setting would be put in: the one
// read, the one named, or else the user's own.
func (s *tuiSession) configFileToEdit() string {
	path := s.file.Path
	if path == "" {
		var err error
		if path, err = config.UserPath(); err != nil {
			return "a config file named with -config"
		}
	}
	if _, err := os.Stat(path); err != nil {
		return "the config file " + path + ", which does not exist yet"
	}
	return "the config file " + path
}

// open opens the library the settings name, closing the one it had open.
// A library that is not there to open is no error: the options then say what
// is missing, and the TUI opens on that. With an error, the options still
// name the library's folder.
func (s *tuiSession) open() (tui.Options, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closeStore()
	cfg, none, err := s.settings()
	opts := tui.Options{Destination: cfg.Destination, DryRun: cfg.DryRun, Compilations: cfg.Compilations,
		MusicBrainzContact: cfg.MusicBrainzContact, NoLibrary: none, Reopen: s.open, Settings: s}
	if err != nil || none != nil {
		opts.Destination = ""
		return opts, err
	}
	opts.Runner = tuiRunner{cfg: cfg}

	// The TUI only reads the library, so it takes no lock and can open while a
	// run is going.
	st, err := store.OpenLibraryReader(cfg.Destination, cfg.Database)
	switch {
	case errors.Is(err, store.ErrNoDatabase):
		opts.NoLibrary = noIndexYet(cfg)
		return opts, nil
	case err != nil:
		return opts, err
	}
	s.st, s.cfg = st, cfg
	opts.Store, opts.LibraryID = st, st.LibraryID()
	return opts, nil
}

// noIndexYet says that the library the settings name has no index, or is
// not there at all, and which run would make it.
func noIndexYet(cfg config.Config) *tui.NoLibrary {
	none := &tui.NoLibrary{CanRun: true}
	if _, err := os.Stat(cfg.Destination); errors.Is(err, fs.ErrNotExist) {
		none.What = "the library folder " + cfg.Destination + " does not exist yet"
		none.Todo = []string{"Press i to make it, as an empty library."}
	} else {
		none.What = "the library at " + cfg.Destination + " has no index yet"
		none.Todo = []string{"Press i to index the music that is in it. Nothing is moved."}
	}
	if cfg.Source != "" && !cfg.Reorganizing() {
		none.Todo = append(none.Todo, "Or press o to organize "+cfg.Source+" into it.")
	}
	return none
}

// tuiRunner does the runs started from the TUI with the configuration it was
// opened with. Each run takes the library lock for as long as it lasts, as a
// CLI run does.
type tuiRunner struct {
	cfg config.Config // with absolute paths
}

func (r tuiRunner) Run(ctx context.Context, kind tui.RunKind, dryRun bool, sink engine.Sink) (engine.RunFinished, error) {
	cfg := r.cfg
	cfg.DryRun = dryRun
	switch kind {
	case tui.RunOrganize:
		if cfg.Source == "" {
			return engine.RunFinished{}, &tui.StartError{What: "no source folder is set", Setting: "source",
				Todo: here(`Or start tagshelf tui with one, as in -source "D:\Incoming".`)}
		}
		if p := sourceProblem(cfg.Source); p != nil {
			return engine.RunFinished{}, &tui.StartError{What: p.what, Todo: p.todo, Setting: "source"}
		}
	case tui.RunReorganize:
		cfg.Source = cfg.Destination
	case tui.RunIndex:
		cfg.Source, cfg.DryRun = "", false
	}
	st, err := store.OpenLibrary(cfg.Destination, cfg.Database, cfg.DryRun)
	if err != nil {
		return engine.RunFinished{}, err
	}
	defer st.Close()
	opts := engineOptions(cfg)
	opts.Sink = sink
	opts.Store = st
	opts.IndexOnly = kind == tui.RunIndex
	finish := engine.Run(ctx, opts)
	if finish.State == engine.StateNotStarted {
		return finish, finish.Err
	}
	return finish, nil
}

func (r tuiRunner) Resolve(ctx context.Context, resolutions []engine.Resolution) (engine.RunFinished, error) {
	st, err := store.OpenLibrary(r.cfg.Destination, r.cfg.Database, false)
	if err != nil {
		return engine.RunFinished{}, err
	}
	defer st.Close()
	opts := engineOptions(r.cfg)
	opts.DryRun = false
	opts.Store = st
	finish := engine.ResolveDuplicates(ctx, opts, resolutions)
	if finish.State == engine.StateNotStarted {
		return finish, finish.Err
	}
	return finish, nil
}

func (r tuiRunner) KeepBoth(kind store.GroupKind, members []int64) error {
	st, err := store.OpenLibrary(r.cfg.Destination, r.cfg.Database, false)
	if err != nil {
		return err
	}
	defer st.Close()
	return st.RecordReview(store.Review{Kind: kind, Members: members, Outcome: store.OutcomeKeepBoth, By: store.ByUser})
}

func (r tuiRunner) SaveTags(ctx context.Context, ids []int64, save engine.TagSave, sink engine.Sink) (engine.RunFinished, error) {
	st, err := store.OpenLibrary(r.cfg.Destination, r.cfg.Database, false)
	if err != nil {
		return engine.RunFinished{}, err
	}
	defer st.Close()
	opts := engineOptions(r.cfg)
	opts.DryRun = false
	opts.Store = st
	opts.Sink = sink
	finish := engine.SaveTags(ctx, opts, ids, save)
	if finish.State == engine.StateNotStarted {
		return finish, finish.Err
	}
	return finish, nil
}

func (r tuiRunner) Undo(ctx context.Context, runID int64, check engine.TagCheck, restore engine.TagRestore, sink engine.Sink) (engine.RunFinished, error) {
	st, err := store.OpenLibrary(r.cfg.Destination, r.cfg.Database, false)
	if err != nil {
		return engine.RunFinished{}, err
	}
	defer st.Close()
	opts := engineOptions(r.cfg)
	opts.DryRun = false
	opts.Store = st
	opts.Sink = sink
	finish := engine.Undo(ctx, opts, runID, check, restore)
	if finish.State == engine.StateNotStarted {
		return finish, finish.Err
	}
	return finish, nil
}
