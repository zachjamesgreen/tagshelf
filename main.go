package main

import (
	"context"
	"crypto/sha256"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"path/filepath"

	"github.com/zachjamesgreen/tagshelf/internal/config"
	"github.com/zachjamesgreen/tagshelf/internal/engine"
	"github.com/zachjamesgreen/tagshelf/internal/rules"
	"github.com/zachjamesgreen/tagshelf/internal/store"
	"github.com/zachjamesgreen/tagshelf/internal/tagwrite"
	"github.com/zachjamesgreen/tagshelf/internal/version"
)

func main() {
	log.SetFlags(log.LstdFlags)
	ownTempFolder()
	os.Exit(run(interruptContext(), os.Args[1:]))
}

// interruptContext returns a context cancelled by the first Ctrl-C, so the run
// can stop gracefully. A second Ctrl-C exits at once. Off Windows, being told
// to end and losing the terminal stop a run the same way (see stopSignals).
func interruptContext() context.Context {
	ctx, cancel := context.WithCancel(context.Background())
	sigs := make(chan os.Signal, 2)
	signal.Notify(sigs, stopSignals...)
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
	// help is the command's help screen.
	help helpScreen

	config, source, dest, untagged, other, duplicates, compilations, db *string
	workers                                                             *int
	dryRun, noCueRewrite                                                *bool

	// The flags of a run alone, nil for `tagshelf tui`.
	reorganize             *string
	verbose, index, rescan *bool
}

// addLibraryFlags defines the shared flags on set, for the command whose help
// screen is help. The three usage texts say what the flag means for that
// command. In a usage text the word in backquotes names the flag's value.
func addLibraryFlags(set *flag.FlagSet, help helpScreen, sourceUsage, destUsage, dryRunUsage string) *libraryFlags {
	// parse says what is wrong with a command line, in its own words and
	// after the help, so the flag package says nothing.
	set.SetOutput(io.Discard)
	set.Usage = func() {}
	return &libraryFlags{
		set:          set,
		help:         help,
		config:       set.String("config", "", "Take the settings from this config `file`. Flags override what it says."),
		source:       set.String("source", "", sourceUsage),
		dest:         set.String("dest", "", destUsage),
		untagged:     set.String("untagged", "", here("The `folder` for audio files without usable tags (default: <dest>\\_Untagged).")),
		other:        set.String("other", "", here("The `folder` for files that are not audio and do not belong to a tagged album (default: <dest>\\_Other).")),
		duplicates:   set.String("duplicates", "", here("The `folder` for the copies set aside as duplicates (default: <dest>\\_Duplicates).")),
		compilations: set.String("compilations", "", "The artist folder `name` for Various Artists albums (default: \""+rules.DefaultCompilations+"\")."),
		workers:      set.Int("workers", 0, "Work on `n` files at once (default: the number of CPUs)."),
		dryRun:       set.Bool("dry-run", false, dryRunUsage),
		noCueRewrite: set.Bool("no-cue-rewrite", false, "Do not fix the FILE lines of the cue sheets that move."),
		db:           set.String("db", "", "The library's database `file` (default: "+filepath.Join(dataFolderName(), "libraries", "<library-id>", "tagshelf.db")+")."),
	}
}

// misuse reports a command line that was typed wrong: the help, then what is
// wrong, so that it is the last thing on the screen.
func (f *libraryFlags) misuse(what, todo string) int {
	f.help.write(log.Writer(), f.set, *f.config)
	fmt.Fprintln(log.Writer())
	return cannotStart(what, todo)
}

// parse reads args into the flag set. When the command should not go on it
// reports done with the exit code: after -h, which shows the help, and after
// a flag it does not know or an argument that is not a flag, which show the
// help and then the mistake. The flag package stops reading at such an
// argument, so every flag after it would be dropped without a word: a -dry-run
// among them would be lost, and the run a real one.
func (f *libraryFlags) parse(args []string) (code int, done bool) {
	err := f.set.Parse(args)
	switch {
	case errors.Is(err, flag.ErrHelp):
		f.help.write(stdout, f.set, *f.config)
		return exitOK, true
	case err != nil:
		return f.misuse(flagProblem(err), "The flags are listed above."), true
	case f.set.NArg() > 0:
		return f.misuse(fmt.Sprintf("unexpected argument %q: %s takes only flags", f.set.Arg(0), f.set.Name()),
			"A flag that is on or off is written -flag or -flag=false, with no value after it."), true
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

// run parses args, does one run (or, for `tagshelf tui`, shows the TUI, and
// for `tagshelf report` writes the report) and returns the process exit code. With no arguments at all it only shows the
// help, or the TUI when it was started by a double click: a run is never
// started by a config file alone, without a word typed to ask for it.
func run(ctx context.Context, args []string) int {
	if len(args) == 0 {
		// Started by a double click there is nobody to read the help: the
		// window closes with the program. The terminal UI opens instead.
		if ownConsole() {
			code := runTUI(ctx, nil)
			if code != exitOK && code != exitInterrupted {
				waitForEnter()
			}
			return code
		}
		set := flag.NewFlagSet("tagshelf", flag.ContinueOnError)
		organizeFlags(set).help.write(stdout, set, "")
		return exitOK
	}
	if args[0] == "tui" {
		return runTUI(ctx, args[1:])
	}
	if args[0] == "report" {
		return runReport(args[1:])
	}
	if args[0] == "art" {
		return runArt(ctx, args[1:])
	}
	// -version alone, so that it never starts a run beside a config file.
	if len(args) == 1 && (args[0] == "-version" || args[0] == "--version") {
		fmt.Fprintln(stdout, "tagshelf", version.String())
		return exitOK
	}
	set := flag.NewFlagSet("tagshelf", flag.ContinueOnError)
	flags := organizeFlags(set)
	verboseFlag, reorganizeFlag := flags.verbose, flags.reorganize
	indexFlag, rescanFlag := flags.index, flags.rescan

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

	cfg, file, err := config.LoadFile(*flags.config, ov)
	if err != nil {
		return configProblem(err)
	}

	if *indexFlag {
		// An index run moves nothing, so it has nothing to preview and no
		// source to organize; it keeps what it reads.
		cfg.DryRun = false
	} else if cfg.Source == "" {
		return flags.misuse("no source folder is set", here(noSourceTodo))
	}
	if cfg.Destination == "" {
		return flags.misuse("no library folder is set", here(noDestinationTodo))
	}

	cfg, links, problem := usablePaths(cfg, pathOrigin{flags: flags.typedPaths(), file: file.Path})
	if problem != nil {
		return problem.refusal().print()
	}
	for _, link := range links {
		log.Println(link)
	}
	if !*indexFlag {
		if p := sourceProblem(cfg.Source); p != nil {
			return p.print()
		}
	}

	// The library lock is taken and the database opened before anything else
	// is touched: a run that cannot be recorded does not start, and neither
	// does one while another process is changing the library.
	st, err := store.OpenLibrary(cfg.Destination, cfg.Database, cfg.DryRun)
	if err != nil {
		return libraryProblem(err, cfg.Destination)
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

// What to do about a source or a library folder that is not set, written
// with the example folders of Windows (see here).
const (
	noSourceTodo      = `Pass one, as in -source "D:\Incoming", or set source in the config file.`
	noDestinationTodo = `Pass one, as in -dest "D:\Music", or set destination in the config file.`
)

// organizeFlags defines the flags of a run on set.
func organizeFlags(set *flag.FlagSet) *libraryFlags {
	flags := addLibraryFlags(set, organizeHelp,
		"The `folder` of music to organize.",
		"The library: the `folder` the music is organized into.",
		"Preview: say what would be done, and move nothing.")
	flags.verbose = set.Bool("verbose", false, "Print a line for every file.")
	flags.reorganize = set.String("reorganize", "", "Re-sort the library in this `folder` in place. The same as giving it as both -source and -dest.")
	flags.index = set.Bool("index", false, "Only bring the library's index up to date, and move nothing. Needs only -dest.")
	flags.rescan = set.Bool("rescan", false, "Read every file into the index again, even one whose size and date have not changed.")
	return flags
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
		SavePicture: savePicture,
	}
}

// savePicture puts a picture into an audio file, or takes its picture out,
// for the engine: by tagwrite's safe save, put on record in the library's
// history with the picture that gave way and the SHA-256 of the new one.
func savePicture(st *store.Store, runID int64, path string, picture []byte) error {
	_, err := tagwrite.SavePicture(path, picture, func(before []byte, apply func() error) error {
		sum := sha256.Sum256(picture)
		return st.RecordPicture(runID, path, before, sum[:], apply)
	})
	return err
}
