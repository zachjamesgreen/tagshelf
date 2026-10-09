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

// runTUI runs `tagshelf tui [flags]` and returns the process exit code.
func runTUI(ctx context.Context, args []string) int {
	flags := flag.NewFlagSet("tagshelf tui", flag.ContinueOnError)
	configPath := flags.String("config", "", "Path to YAML configuration file")
	sourceFlag := flags.String("source", "", "Source directory that o organizes into the library")
	destFlag := flags.String("dest", "", "The library: the destination directory of organized music")
	dbFlag := flags.String("db", "", "Library database file (default: %LOCALAPPDATA%/tagshelf/libraries/<library-id>/tagshelf.db)")
	dryRunFlag := flags.Bool("dry-run", false, "Start with dry run on")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return exitOK
		}
		return exitCannotStart
	}
	if flags.NArg() > 0 {
		log.Printf("Error: unexpected arguments %q", flags.Args())
		return exitCannotStart
	}
	ov := config.Overrides{Source: *sourceFlag, Destination: *destFlag, Database: *dbFlag}
	flags.Visit(func(f *flag.Flag) {
		if f.Name == "dry-run" {
			ov.DryRun = dryRunFlag
		}
	})
	cfg, err := config.Load(*configPath, ov)
	if err != nil {
		log.Printf("Error: %v", err)
		return exitCannotStart
	}
	if cfg.Destination == "" {
		log.Println("Error: destination directory must be specified via -dest or config file")
		return exitCannotStart
	}
	if cfg, err = absolutePaths(cfg.WithDefaults()); err != nil {
		log.Printf("Error: %v", err)
		return exitCannotStart
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
	defer st.Close()

	err = tui.Run(ctx, tui.Options{Store: st, Destination: dest, LibraryID: st.LibraryID(), DryRun: cfg.DryRun, Compilations: cfg.Compilations, Runner: tuiRunner{cfg: cfg}})
	if ctx.Err() != nil {
		return exitInterrupted
	}
	if err != nil {
		log.Printf("Error: %v", err)
		return exitErrors
	}
	return exitOK
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
