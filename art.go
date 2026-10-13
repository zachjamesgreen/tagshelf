package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log"

	"github.com/zachjamesgreen/tagshelf/internal/config"
	"github.com/zachjamesgreen/tagshelf/internal/coverart"
	"github.com/zachjamesgreen/tagshelf/internal/engine"
	"github.com/zachjamesgreen/tagshelf/internal/store"
)

var artHelp = helpScreen{
	intro: `tagshelf art gives the albums of the library the cover they lack. An album
is one folder. A folder that has a cover (an image file named cover, folder
or front) keeps it. One without gets a file named cover: the picture its
audio files hold, or else the front cover the Cover Art Archive has for the
MusicBrainz release in their tags.

It is one run, which the terminal UI lists and can undo. Stopped with
Ctrl-C, it goes on where it stopped the next time: the folders that have a
cover by then are passed over.

Usage:
  tagshelf art [flags]

Examples:
  tagshelf art -dest D:\Music -file -dry-run
      Say which folders would get a cover, and from where. Nothing is asked
      of the Cover Art Archive and nothing is written.
  tagshelf art -dest D:\Music -file
      Give every album folder without a cover a cover file.
`,
	groups: []flagGroup{
		{"What it does", []string{"file", "replace", "dry-run"}},
		{"The library", []string{"dest", "db"}},
		{"Other", []string{"config"}},
	},
	extra: [][2]string{{"-h", "Show this help."}},
}

// artFlags are the flags of `tagshelf art`.
type artFlags struct {
	*libraryFlags
	file    *bool
	replace *string
}

// newArtFlags defines the flags of `tagshelf art` on set.
func newArtFlags(set *flag.FlagSet) artFlags {
	set.SetOutput(io.Discard)
	set.Usage = func() {}
	return artFlags{
		libraryFlags: &libraryFlags{
			set:    set,
			help:   artHelp,
			config: set.String("config", "", "Take the library and the cover art settings from this config `file`. Flags override what it says."),
			dest:   set.String("dest", "", "The library: the `folder` whose albums are seen to."),
			db:     set.String("db", "", "The library's database `file`, when it is not in its usual place."),
			dryRun: set.Bool("dry-run", false, "Preview: say what would be done, ask the Cover Art Archive nothing and write nothing."),
		},
		file:    set.Bool("file", false, "Write a cover file into each album folder that has no cover. The same as cover_art_file in the config file."),
		replace: set.String("replace", "", "`When` a cover a folder has gives way to the Cover Art Archive's: never, smaller or always. The same as cover_art_replace."),
	}
}

// coverFetch asks the Cover Art Archive for a front cover. The tests put a
// stand-in in its place: they make no requests.
var coverFetch engine.CoverFetch = coverart.New().Front

// runArt runs `tagshelf art [flags]` and returns the process exit code.
func runArt(ctx context.Context, args []string) int {
	set := flag.NewFlagSet("tagshelf art", flag.ContinueOnError)
	flags := newArtFlags(set)
	if code, done := flags.parse(args); done {
		return code
	}
	cfg, file, err := config.LoadFile(*flags.config, config.Overrides{Destination: *flags.dest, Database: *flags.db})
	if err != nil {
		return configProblem(err)
	}
	set.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "file":
			cfg.CoverArtFile = *flags.file
		case "replace":
			cfg.CoverArtReplace = *flags.replace
		case "dry-run":
			cfg.DryRun = *flags.dryRun
		}
	})
	if cfg.Destination == "" {
		return flags.misuse("no library folder is set", here(noDestinationTodo))
	}
	if problem := config.Check(config.Config{CoverArtReplace: cfg.CoverArtReplace})["cover_art_replace"]; problem != "" {
		return flags.misuse("when a cover is replaced "+problem+", not "+cfg.CoverArtReplace, "Give -replace, or cover_art_replace in the config file, one of the three.")
	}
	if !cfg.CoverArtFile {
		return cannotStart("no cover art setting is on, so there is nothing for tagshelf art to do",
			"Pass -file, or set cover_art_file: true in the config file, to have cover files written.")
	}
	// The other folders of the config file are not this command's concern.
	paths, _, problem := usablePaths(config.Config{Destination: cfg.Destination, Database: cfg.Database},
		pathOrigin{flags: flags.typedPaths(), file: file.Path})
	if problem != nil {
		return problem.refusal().print()
	}

	// Only a library that has been indexed has albums to see to: opening one
	// that is not there would make it.
	if id, err := store.ReadLibraryID(paths.Destination); err != nil || id == "" {
		return libraryRefusal(store.ErrNoDatabase, paths.Destination).print()
	}
	st, err := store.OpenLibrary(paths.Destination, paths.Database, cfg.DryRun)
	if err != nil {
		return libraryProblem(err, paths.Destination)
	}
	defer st.Close()

	lg := log.Default()
	lg.Println("================ Cover art ================")
	lg.Printf("Library:      %s\n", paths.Destination)
	lg.Printf("Cover file:   %t\n", cfg.CoverArtFile)
	lg.Printf("Replace:      %s\n", cfg.CoverReplace())
	lg.Printf("Dry-Run:      %t\n", cfg.DryRun)
	lg.Println("===========================================")

	opts := engineOptions(paths.WithDefaults())
	opts.DryRun = cfg.DryRun
	opts.Store = st
	opts.Sink = artSink(lg, cfg.DryRun)
	finish := engine.CoverArt(ctx, opts, engine.CoverOptions{File: cfg.CoverArtFile, Replace: cfg.CoverReplace(), Fetch: coverFetch})
	return exitCode(finish.State)
}

// artSink prints a cover art run's events as log lines.
func artSink(lg *log.Logger, dryRun bool) engine.Sink {
	prefix := ""
	if dryRun {
		prefix = "[DRY-RUN] "
	}
	return func(e engine.Event) {
		switch e := e.(type) {
		case engine.CoverWritten:
			what := fmt.Sprintf("%q from %s", e.Path, e.From)
			if e.Asked {
				what = fmt.Sprintf("%q from %s, if it has one", e.Path, e.From)
			}
			if e.Replaced != "" {
				what += fmt.Sprintf(", in place of %q", e.Replaced)
			}
			lg.Printf("%s[COVER] %s\n", prefix, what)
		case engine.CoverSkipped:
			lg.Printf("%s[NO COVER] %q: %s\n", prefix, e.Dir, e.Why)
		case engine.FileError:
			lg.Printf("[ERROR] %q: %v\n", e.Path, e.Err)
		case engine.RunFinished:
			if e.State == engine.StateNotStarted {
				fmt.Fprintf(lg.Writer(), "tagshelf cannot start: the run could not be recorded: %v.\nNothing was touched.\n", e.Err)
				return
			}
			lg.Println("==================== Summary ====================")
			if dryRun {
				lg.Printf("Covers that would be written: %d\n", e.Counters.Covers)
			} else {
				lg.Printf("Covers written:           %d\n", e.Counters.Covers)
			}
			lg.Printf("Errors encountered:       %d\n", e.Counters.Errors)
			lg.Println("==================================================")
			if e.State == engine.StateInterrupted {
				lg.Println("Stopped: the next tagshelf art goes on with the folders that still have no cover.")
			}
		}
	}
}
