package main

import (
	"context"
	"flag"
	"log"
	"path/filepath"

	"tagshelf/internal/config"
	"tagshelf/internal/engine"
	"tagshelf/internal/rules"
)

func main() {
	log.SetFlags(log.LstdFlags | log.Lshortfile)

	configPath := flag.String("config", "", "Path to YAML configuration file")
	sourceFlag := flag.String("source", "", "Source directory containing audio files")
	destFlag := flag.String("dest", "", "Destination directory for organized music")
	untaggedFlag := flag.String("untagged", "", "Directory for untagged / incomplete files")
	otherFlag := flag.String("other", "", "Directory for non-audio files that do not belong to a tagged album (default: <dest>/_Other)")
	compilationsFlag := flag.String("compilations", "", "Artist folder name for Various Artists compilations (default: \""+rules.DefaultCompilations+"\")")
	workersFlag := flag.Int("workers", 0, "Number of concurrent worker goroutines (default: NumCPU)")
	dryRunFlag := flag.Bool("dry-run", false, "Preview actions without moving or deleting files")
	verboseFlag := flag.Bool("verbose", false, "Enable verbose per-file logging")
	reorganizeFlag := flag.String("reorganize", "", "Re-sort an already organized library in place (sets -source and -dest to this directory)")
	noCueRewriteFlag := flag.Bool("no-cue-rewrite", false, "Do not rewrite the FILE lines of moved cue sheets")

	flag.Parse()

	// Only flags the user actually typed may override the config file, so that
	// -dry-run=false and -verbose=false can turn a config setting back off.
	wasSet := make(map[string]bool)
	flag.Visit(func(f *flag.Flag) { wasSet[f.Name] = true })

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
		log.Fatalf("Error: %v", err)
	}

	if cfg.Source == "" {
		log.Fatal("Error: source directory must be specified via -source or config file")
	}
	if cfg.Destination == "" {
		log.Fatal("Error: destination directory must be specified via -dest or config file")
	}

	cfg = cfg.WithDefaults()

	absSource, err := filepath.Abs(cfg.Source)
	if err != nil {
		log.Fatalf("Invalid source path: %v", err)
	}
	absDest, err := filepath.Abs(cfg.Destination)
	if err != nil {
		log.Fatalf("Invalid destination path: %v", err)
	}
	absUntagged, err := filepath.Abs(cfg.Untagged)
	if err != nil {
		log.Fatalf("Invalid untagged path: %v", err)
	}
	absOther, err := filepath.Abs(cfg.Other)
	if err != nil {
		log.Fatalf("Invalid other path: %v", err)
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

	engine.Run(context.Background(), engine.Options{
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
}
