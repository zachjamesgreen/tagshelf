package main

import (
	"context"
	"errors"
	"flag"
	"log"

	"tagshelf/internal/config"
	"tagshelf/internal/engine"
	"tagshelf/internal/store"
	"tagshelf/internal/tui"
)

// showTUI shows the TUI until the user quits or the context is cancelled.
// The tests put a stand-in in its place: the real one needs a terminal.
var showTUI = tui.Run

// runTUI runs `tagshelf tui [flags]` and returns the process exit code.
func runTUI(ctx context.Context, args []string) int {
	cfg, code, ok := tuiConfig(args)
	if !ok {
		return code
	}
	dest, database := cfg.Destination, cfg.Database

	// The TUI only reads the library, so it takes no lock and can open while a
	// run is going.
	st, err := store.OpenLibraryReader(dest, database)
	var locked *store.LockedError
	switch {
	case errors.Is(err, store.ErrNoDatabase):
		log.Printf("Error: %v; run tagshelf -index -dest %q first", err, dest)
		return exitCannotStart
	case errors.As(err, &locked):
		log.Printf("Error: %v; try again when it has finished", err)
		return exitCannotStart
	case err != nil:
		log.Printf("Error: cannot open the library database: %v", err)
		return exitCannotStart
	}
	// The database closes once the TUI has returned, which it does only
	// after stopping whatever run or action it had going.
	defer st.Close()

	err = showTUI(ctx, tui.Options{Store: st, Destination: dest, LibraryID: st.LibraryID(), DryRun: cfg.DryRun, Compilations: cfg.Compilations,
		MusicBrainzContact: cfg.MusicBrainzContact, Runner: tuiRunner{cfg: cfg}})
	if ctx.Err() != nil {
		return exitInterrupted
	}
	if err != nil {
		log.Printf("Error: %v", err)
		return exitErrors
	}
	return exitOK
}

// tuiConfig works out the configuration `tagshelf tui` was started with, with
// defaults filled in and paths made absolute. ok is false when the TUI should
// not open, and code is then the exit code. It takes the CLI's folder flags,
// so that the runs the TUI starts use the folders a CLI run with the same
// flags would.
func tuiConfig(args []string) (cfg config.Config, code int, ok bool) {
	set := flag.NewFlagSet("tagshelf tui", flag.ContinueOnError)
	flags := addLibraryFlags(set,
		"Source directory that o organizes into the library",
		"The library: the destination directory of organized music",
		"Start with dry run on")
	if code, done := flags.parse(args); done {
		return config.Config{}, code, false
	}
	cfg, err := config.Load(*flags.config, flags.overrides())
	if err != nil {
		log.Printf("Error: %v", err)
		return config.Config{}, exitCannotStart, false
	}
	if cfg.Destination == "" {
		log.Println("Error: destination directory must be specified via -dest or config file")
		return config.Config{}, exitCannotStart, false
	}
	if cfg, err = absolutePaths(cfg.WithDefaults()); err != nil {
		log.Printf("Error: %v", err)
		return config.Config{}, exitCannotStart, false
	}
	return cfg, exitOK, true
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
			return engine.RunFinished{}, errors.New("no source directory is configured: set source in the config file or pass -source to tagshelf tui")
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
