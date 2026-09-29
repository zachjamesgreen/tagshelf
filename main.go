package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"

	"tagshelf/internal/config"
	"tagshelf/internal/engine"
	"tagshelf/internal/rules"
	"tagshelf/internal/store"
)

func main() {
	log.SetFlags(log.LstdFlags | log.Lshortfile)
	os.Exit(run(interruptContext(), os.Args[1:]))
}

// interruptContext returns a context cancelled by the first Ctrl-C, so the run
// can stop gracefully. A second Ctrl-C exits at once.
func interruptContext() context.Context {
	ctx, cancel := context.WithCancel(context.Background())
	sigs := make(chan os.Signal, 2)
	signal.Notify(sigs, os.Interrupt)
	go func() {
		<-sigs
		log.Println("Interrupted: finishing the files already moving. Press Ctrl-C again to exit at once.")
		cancel()
		<-sigs
		log.Println("Interrupted again: exiting at once.")
		os.Exit(exitInterrupted)
	}()
	return ctx
}

// run parses args, does one run (or, for `tagshelf tui`, shows the TUI) and
// returns the process exit code.
func run(ctx context.Context, args []string) int {
	if len(args) > 0 && args[0] == "tui" {
		return runTUI(ctx, args[1:])
	}
	flags := flag.NewFlagSet("tagshelf", flag.ContinueOnError)
	configPath := flags.String("config", "", "Path to YAML configuration file")
	sourceFlag := flags.String("source", "", "Source directory containing audio files")
	destFlag := flags.String("dest", "", "Destination directory for organized music")
	untaggedFlag := flags.String("untagged", "", "Directory for untagged / incomplete files")
	otherFlag := flags.String("other", "", "Directory for non-audio files that do not belong to a tagged album (default: <dest>/_Other)")
	duplicatesFlag := flags.String("duplicates", "", "Directory the losing copies of duplicates are moved to (default: <dest>/_Duplicates)")
	compilationsFlag := flags.String("compilations", "", "Artist folder name for Various Artists compilations (default: \""+rules.DefaultCompilations+"\")")
	workersFlag := flags.Int("workers", 0, "Number of concurrent worker goroutines (default: NumCPU)")
	dryRunFlag := flags.Bool("dry-run", false, "Preview actions without moving or deleting files")
	verboseFlag := flags.Bool("verbose", false, "Enable verbose per-file logging")
	reorganizeFlag := flags.String("reorganize", "", "Re-sort an already organized library in place (sets -source and -dest to this directory)")
	noCueRewriteFlag := flags.Bool("no-cue-rewrite", false, "Do not rewrite the FILE lines of moved cue sheets")
	dbFlag := flags.String("db", "", "Library database file (default: %LOCALAPPDATA%/tagshelf/libraries/<library-id>/tagshelf.db)")
	indexFlag := flags.Bool("index", false, "Only bring the library index up to date; move nothing (needs only -dest)")
	rescanFlag := flags.Bool("rescan", false, "Re-read every file into the index, even if its size and modification time match")

	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return exitOK
		}
		return exitCannotStart
	}

	// Only flags the user actually typed may override the config file, so that
	// -dry-run=false and -verbose=false can turn a config setting back off.
	wasSet := make(map[string]bool)
	flags.Visit(func(f *flag.Flag) { wasSet[f.Name] = true })

	ov := config.Overrides{
		Source:       *sourceFlag,
		Destination:  *destFlag,
		Untagged:     *untaggedFlag,
		Other:        *otherFlag,
		Duplicates:   *duplicatesFlag,
		Compilations: *compilationsFlag,
		Database:     *dbFlag,
	}
	if *reorganizeFlag != "" {
		ov.Source = *reorganizeFlag
		ov.Destination = *reorganizeFlag
	}
	if wasSet["no-cue-rewrite"] {
		rewrite := !*noCueRewriteFlag
		ov.CueRewrite = &rewrite
	}
	if wasSet["workers"] {
		ov.Workers = *workersFlag
	}
	if wasSet["dry-run"] {
		ov.DryRun = dryRunFlag
	}
	if wasSet["verbose"] {
		ov.Verbose = verboseFlag
	}

	cfg, err := config.Load(*configPath, ov)
	if err != nil {
		log.Printf("Error: %v", err)
		return exitCannotStart
	}

	if *indexFlag {
		// An index run moves nothing, so it has nothing to preview and no
		// source to organize; it keeps what it reads.
		cfg.DryRun = false
	} else if cfg.Source == "" {
		log.Println("Error: source directory must be specified via -source or config file")
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

	// The library lock is taken and the database opened before anything else
	// is touched: a run that cannot be recorded does not start, and neither
	// does one while another process is changing the library.
	st, err := store.OpenLibrary(cfg.Destination, cfg.Database, cfg.DryRun)
	var locked *store.LockedError
	if errors.As(err, &locked) {
		log.Printf("Error: %v; try again when it has finished", err)
		return exitCannotStart
	}
	if err != nil {
		log.Printf("Error: cannot open the library database: %v", err)
		return exitCannotStart
	}
	defer st.Close()
	database := st.Path()
	if database == "" {
		database = "in memory (dry run on a library without an ID yet)"
		log.Println("No library index yet: this dry run indexes in memory and discards it. Run tagshelf -index first to keep it.")
	}

	log.Println("================ Music Organizer ================")
	switch {
	case *indexFlag:
		log.Println("Mode:         index only, nothing moves")
	case cfg.Reorganizing():
		log.Println("Mode:         reorganize library in place")
	}
	if !*indexFlag {
		log.Printf("Source:       %s\n", cfg.Source)
	}
	log.Printf("Destination:  %s\n", cfg.Destination)
	log.Printf("Untagged:     %s\n", cfg.Untagged)
	log.Printf("Other:        %s\n", cfg.Other)
	log.Printf("Duplicates:   %s\n", cfg.Duplicates)
	log.Printf("Compilations: %s\n", cfg.Compilations)
	log.Printf("Workers:      %d\n", cfg.Workers)
	log.Printf("Dry-Run:      %t\n", cfg.DryRun)
	log.Printf("Verbose:      %t\n", cfg.Verbose)
	log.Printf("Cue rewrite:  %t\n", cfg.RewriteCues())
	log.Printf("Auto-resolve: %t\n", cfg.AutoResolvesExact())
	log.Printf("Database:     %s\n", database)
	log.Println("==================================================")

	opts := engineOptions(cfg)
	opts.Sink = newCLISink(log.Default(), cfg.Verbose)
	opts.Store = st
	opts.Rescan = *rescanFlag
	opts.IndexOnly = *indexFlag
	finish := engine.Run(ctx, opts)
	return exitCode(finish.State)
}

// absolutePaths makes the folders and database a run uses absolute, so that
// they mean the same wherever the run is started from.
func absolutePaths(cfg config.Config) (config.Config, error) {
	for _, p := range []struct {
		name string
		path *string
	}{
		{"source", &cfg.Source}, {"destination", &cfg.Destination}, {"untagged", &cfg.Untagged},
		{"other", &cfg.Other}, {"duplicates", &cfg.Duplicates}, {"database", &cfg.Database},
	} {
		if *p.path == "" {
			continue
		}
		abs, err := filepath.Abs(*p.path)
		if err != nil {
			return cfg, fmt.Errorf("invalid %s path: %w", p.name, err)
		}
		*p.path = abs
	}
	return cfg, nil
}

// engineOptions are the engine's options for a run with cfg, without its
// sink, store or index-only choices.
func engineOptions(cfg config.Config) engine.Options {
	return engine.Options{
		Source:        cfg.Source,
		Destination:   cfg.Destination,
		Untagged:      cfg.Untagged,
		Other:         cfg.Other,
		Duplicates:    cfg.Duplicates,
		Compilations:  cfg.Compilations,
		Workers:       cfg.Workers,
		DryRun:        cfg.DryRun,
		NoCueRewrite:  !cfg.RewriteCues(),
		NoAutoResolve: !cfg.AutoResolvesExact(),
	}
}
