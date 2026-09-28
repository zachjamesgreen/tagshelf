package main

import (
	"context"
	"errors"
	"flag"
	"log"
	"os"
	"os/signal"
	"path/filepath"

	"tagshelf/internal/config"
	"tagshelf/internal/engine"
	"tagshelf/internal/rules"
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

// run parses args, does one run and returns the process exit code.
func run(ctx context.Context, args []string) int {
	flags := flag.NewFlagSet("tagshelf", flag.ContinueOnError)
	configPath := flags.String("config", "", "Path to YAML configuration file")
	sourceFlag := flags.String("source", "", "Source directory containing audio files")
	destFlag := flags.String("dest", "", "Destination directory for organized music")
	untaggedFlag := flags.String("untagged", "", "Directory for untagged / incomplete files")
	otherFlag := flags.String("other", "", "Directory for non-audio files that do not belong to a tagged album (default: <dest>/_Other)")
	compilationsFlag := flags.String("compilations", "", "Artist folder name for Various Artists compilations (default: \""+rules.DefaultCompilations+"\")")
	workersFlag := flags.Int("workers", 0, "Number of concurrent worker goroutines (default: NumCPU)")
	dryRunFlag := flags.Bool("dry-run", false, "Preview actions without moving or deleting files")
	verboseFlag := flags.Bool("verbose", false, "Enable verbose per-file logging")
	reorganizeFlag := flags.String("reorganize", "", "Re-sort an already organized library in place (sets -source and -dest to this directory)")
	noCueRewriteFlag := flags.Bool("no-cue-rewrite", false, "Do not rewrite the FILE lines of moved cue sheets")

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
		Compilations: *compilationsFlag,
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

	if cfg.Source == "" {
		log.Println("Error: source directory must be specified via -source or config file")
		return exitCannotStart
	}
	if cfg.Destination == "" {
		log.Println("Error: destination directory must be specified via -dest or config file")
		return exitCannotStart
	}

	cfg = cfg.WithDefaults()

	absSource, err := filepath.Abs(cfg.Source)
	if err != nil {
		log.Printf("Invalid source path: %v", err)
		return exitCannotStart
	}
	absDest, err := filepath.Abs(cfg.Destination)
	if err != nil {
		log.Printf("Invalid destination path: %v", err)
		return exitCannotStart
	}
	absUntagged, err := filepath.Abs(cfg.Untagged)
	if err != nil {
		log.Printf("Invalid untagged path: %v", err)
		return exitCannotStart
	}
	absOther, err := filepath.Abs(cfg.Other)
	if err != nil {
		log.Printf("Invalid other path: %v", err)
		return exitCannotStart
	}

	cfg.Source = absSource
	cfg.Destination = absDest
	cfg.Untagged = absUntagged
	cfg.Other = absOther

	log.Println("================ Music Organizer ================")
	if cfg.Reorganizing() {
		log.Println("Mode:         reorganize library in place")
	}
	log.Printf("Source:       %s\n", cfg.Source)
	log.Printf("Destination:  %s\n", cfg.Destination)
	log.Printf("Untagged:     %s\n", cfg.Untagged)
	log.Printf("Other:        %s\n", cfg.Other)
	log.Printf("Compilations: %s\n", cfg.Compilations)
	log.Printf("Workers:      %d\n", cfg.Workers)
	log.Printf("Dry-Run:      %t\n", cfg.DryRun)
	log.Printf("Verbose:      %t\n", cfg.Verbose)
	log.Printf("Cue rewrite:  %t\n", cfg.RewriteCues())
	log.Println("==================================================")

	finish := engine.Run(ctx, engine.Options{
		Source:       cfg.Source,
		Destination:  cfg.Destination,
		Untagged:     cfg.Untagged,
		Other:        cfg.Other,
		Compilations: cfg.Compilations,
		Workers:      cfg.Workers,
		DryRun:       cfg.DryRun,
		NoCueRewrite: !cfg.RewriteCues(),
		Sink:         newCLISink(log.Default(), cfg.Verbose),
	})
	return exitCode(finish.State)
}
