package main

import (
	"context"
	"errors"
	"flag"
	"log"
	"path/filepath"

	"tagshelf/internal/config"
	"tagshelf/internal/store"
	"tagshelf/internal/tui"
)

// runTUI runs `tagshelf tui [flags]` and returns the process exit code.
func runTUI(ctx context.Context, args []string) int {
	flags := flag.NewFlagSet("tagshelf tui", flag.ContinueOnError)
	configPath := flags.String("config", "", "Path to YAML configuration file")
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
	ov := config.Overrides{Destination: *destFlag, Database: *dbFlag}
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
	dest, err := filepath.Abs(cfg.Destination)
	if err != nil {
		log.Printf("Invalid destination path: %v", err)
		return exitCannotStart
	}
	database := cfg.Database
	if database != "" {
		if database, err = filepath.Abs(database); err != nil {
			log.Printf("Invalid database path: %v", err)
			return exitCannotStart
		}
	}

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

	err = tui.Run(ctx, tui.Options{Store: st, Destination: dest, LibraryID: st.LibraryID(), DryRun: cfg.DryRun})
	if ctx.Err() != nil {
		return exitInterrupted
	}
	if err != nil {
		log.Printf("Error: %v", err)
		return exitErrors
	}
	return exitOK
}
