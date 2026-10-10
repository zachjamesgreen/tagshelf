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
	"tagshelf/internal/tagwrite"
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

// libraryFlags are the flags the CLI and `tagshelf tui` share: the config
// file, the library's folders and database, and how runs on it are done.
// They are defined once, so that a run started from the TUI goes by the same
// settings as one started from the command line.
type libraryFlags struct {
	set *flag.FlagSet

	config, source, dest, untagged, other, duplicates, compilations, db *string
	workers                                                             *int
	dryRun, noCueRewrite                                                *bool
}

// addLibraryFlags defines the shared flags on set. The three usage texts say
// what the flag means for the command the set belongs to.
func addLibraryFlags(set *flag.FlagSet, sourceUsage, destUsage, dryRunUsage string) *libraryFlags {
	// What the flag package has to say goes where every other message goes.
	set.SetOutput(log.Writer())
	return &libraryFlags{
		set:          set,
		config:       set.String("config", "", "Path to YAML configuration file"),
		source:       set.String("source", "", sourceUsage),
		dest:         set.String("dest", "", destUsage),
		untagged:     set.String("untagged", "", "Directory for untagged / incomplete files (default: <dest>/_Untagged)"),
		other:        set.String("other", "", "Directory for non-audio files that do not belong to a tagged album (default: <dest>/_Other)"),
		duplicates:   set.String("duplicates", "", "Directory the losing copies of duplicates are moved to (default: <dest>/_Duplicates)"),
		compilations: set.String("compilations", "", "Artist folder name for Various Artists compilations (default: \""+rules.DefaultCompilations+"\")"),
		workers:      set.Int("workers", 0, "Number of concurrent worker goroutines (default: NumCPU)"),
		dryRun:       set.Bool("dry-run", false, dryRunUsage),
		noCueRewrite: set.Bool("no-cue-rewrite", false, "Do not rewrite the FILE lines of moved cue sheets"),
		db:           set.String("db", "", "Library database file (default: %LOCALAPPDATA%/tagshelf/libraries/<library-id>/tagshelf.db)"),
	}
}

// parse reads args into the flag set. When the command should not go on it
// reports done with the exit code: after -h, a flag it does not know, or an
// argument that is not a flag. The flag package stops reading at such an
// argument, so every flag after it would be dropped without a word: a -dry-run
// among them would be lost, and the run a real one.
func (f *libraryFlags) parse(args []string) (code int, done bool) {
	if err := f.set.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return exitOK, true
		}
		return exitCannotStart, true
	}
	if f.set.NArg() > 0 {
		log.Printf("Error: unexpected argument %q: %s takes only flags, and a flag that is on or off is given as -flag or -flag=false (see %s -h)",
			f.set.Arg(0), f.set.Name(), f.set.Name())
		return exitCannotStart, true
	}
	return 0, false
}

// overrides are the settings typed on the command line, which override the
// config file's. Only flags the user actually typed may do so, so that
// -dry-run=false can turn a config setting back off and a flag left out
// leaves the file's value alone.
func (f *libraryFlags) overrides() config.Overrides {
	ov := config.Overrides{
		Source:       *f.source,
		Destination:  *f.dest,
		Untagged:     *f.untagged,
		Other:        *f.other,
		Duplicates:   *f.duplicates,
		Compilations: *f.compilations,
		Database:     *f.db,
	}
	f.set.Visit(func(fl *flag.Flag) {
		switch fl.Name {
		case "no-cue-rewrite":
			rewrite := !*f.noCueRewrite
			ov.CueRewrite = &rewrite
		case "workers":
			ov.Workers = *f.workers
		case "dry-run":
			ov.DryRun = f.dryRun
		}
	})
	return ov
}

// run parses args, does one run (or, for `tagshelf tui`, shows the TUI) and
// returns the process exit code.
func run(ctx context.Context, args []string) int {
	if len(args) > 0 && args[0] == "tui" {
		return runTUI(ctx, args[1:])
	}
	set := flag.NewFlagSet("tagshelf", flag.ContinueOnError)
	flags := addLibraryFlags(set,
		"Source directory containing audio files",
		"Destination directory for organized music",
		"Preview actions without moving or deleting files")
	verboseFlag := set.Bool("verbose", false, "Enable verbose per-file logging")
	reorganizeFlag := set.String("reorganize", "", "Re-sort an already organized library in place (sets -source and -dest to this directory)")
	indexFlag := set.Bool("index", false, "Only bring the library index up to date; move nothing (needs only -dest)")
	rescanFlag := set.Bool("rescan", false, "Re-read every file into the index, even if its size and modification time match")
	set.Usage = func() {
		fmt.Fprintf(set.Output(), "Usage: tagshelf [flags]\n       tagshelf tui [flags]   (the terminal UI; see tagshelf tui -h)\n\nFlags:\n")
		set.PrintDefaults()
	}

	if code, done := flags.parse(args); done {
		return code
	}

	ov := flags.overrides()
	if *reorganizeFlag != "" {
		ov.Source = *reorganizeFlag
		ov.Destination = *reorganizeFlag
	}
	set.Visit(func(f *flag.Flag) {
		if f.Name == "verbose" {
			ov.Verbose = verboseFlag
		}
	})

	cfg, err := config.Load(*flags.config, ov)
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
		// The temporary copies of a tag save that crashed, which only
		// tagwrite can tell from a save going on now.
		RemoveStaleCopies: func(dir string) ([]string, error) {
			return tagwrite.RemoveStaleCopies(dir, tagwrite.StaleAfter)
		},
	}
}
