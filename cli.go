package main

import (
	"fmt"
	"log"
	"strings"
	"time"

	"tagshelf/internal/engine"
)

// Process exit codes.
const (
	exitOK          = 0   // the run finished without file errors
	exitErrors      = 1   // the run finished, but some files failed
	exitCannotStart = 2   // bad flags, config or database, nothing was touched
	exitInterrupted = 130 // Ctrl-C stopped the run
)

// exitCode maps how a run ended to the process exit code.
func exitCode(s engine.RunState) int {
	switch s {
	case engine.StateErrors:
		return exitErrors
	case engine.StateInterrupted:
		return exitInterrupted
	case engine.StateNotStarted:
		return exitCannotStart
	default:
		return exitOK
	}
}

// cliSink prints a run's events as log lines. Per-file lines other than
// duplicates, errors and warnings are printed only when verbose.
type cliSink struct {
	lg      *log.Logger
	verbose bool

	// Set by RunStarted, before any other event can arrive.
	dryRun     bool
	reorganize bool
	index      bool // an index-only run

	// Only Progress events touch this, and they come one at a time.
	lastPrinted time.Time
}

// progressEvery is how often the CLI prints a progress line.
const progressEvery = 2 * time.Second

func newCLISink(lg *log.Logger, verbose bool) engine.Sink {
	s := &cliSink{lg: lg, verbose: verbose}
	return s.handle
}

func (s *cliSink) handle(e engine.Event) {
	switch e := e.(type) {
	case engine.RunStarted:
		s.dryRun = e.DryRun
		s.reorganize = e.Kind == engine.KindReorganize
		s.index = e.Kind == engine.KindIndex
		s.lastPrinted = time.Now()
	case engine.PhaseStarted:
		if e.Phase == engine.PhaseIndex && e.First {
			s.lg.Printf("First index: %d files, %s. Every file is read once; later runs only read changed files.\n", e.Files, formatBytes(e.Bytes))
		}
	case engine.FilePlaced:
		s.filePlaced(e)
	case engine.FileError:
		s.fileError(e)
	case engine.Warning:
		s.warning(e)
	case engine.CueRewritten:
		if s.verbose {
			renames := make([]string, len(e.Renames))
			for i, r := range e.Renames {
				renames[i] = fmt.Sprintf("%q -> %q", r.From, r.To)
			}
			prefix := "[CUE]"
			if s.dryRun {
				prefix = "[DRY-RUN] [CUE]"
			}
			s.lg.Printf("%s %q: %s\n", prefix, e.Path, strings.Join(renames, ", "))
		}
	case engine.DirRemoved:
		s.lg.Printf("[CLEANUP] Removed empty directory %q\n", e.Path)
	case engine.Progress:
		s.progress(e)
	case engine.RunFinished:
		if e.State == engine.StateNotStarted {
			s.lg.Printf("Error: could not record the run, nothing was touched: %v\n", e.Err)
			return
		}
		if s.index {
			s.indexSummary(e.Counters)
			s.needReview(e.NeedReview)
			if e.State == engine.StateInterrupted {
				s.lg.Println("Index interrupted: the files already read are kept, and the next run reads the rest.")
			}
			return
		}
		s.summary(e.Counters)
		s.needReview(e.NeedReview)
		if e.State == engine.StateInterrupted {
			s.lg.Println("Run interrupted: the remaining files were left where they are and empty folders were not cleaned up.")
		}
	}
}

func (s *cliSink) filePlaced(e engine.FilePlaced) {
	switch e.Outcome {
	case engine.Duplicate:
		if s.dryRun {
			s.lg.Printf("[DRY-RUN] [DUPLICATE] %q -> %q\n", e.Path, e.Destination)
			return
		}
		s.lg.Printf("[DUPLICATE] %q -> %q\n", e.Path, e.Destination)
		return
	case engine.InPlace:
		if s.verbose {
			s.lg.Printf("[IN-PLACE] %q is already at its destination\n", e.Path)
		}
		return
	case engine.Left:
		if s.verbose {
			s.lg.Printf("[LEFT] %q does not qualify for the library, leaving it in place\n", e.Path)
		}
		return
	case engine.Renamed:
		if s.verbose {
			s.lg.Printf("[RENAMED] %q -> %q\n", e.Path, e.Destination)
		}
		return
	}

	if !s.verbose {
		return
	}
	var label string
	switch e.Outcome {
	case engine.Organized:
		label = "MOVED"
		if s.dryRun {
			label = "ORGANIZE"
		}
	case engine.Untagged:
		label = "UNTAGGED"
	case engine.Companion:
		label = "COMPANION"
	default:
		label = "OTHER"
	}
	if s.dryRun {
		s.lg.Printf("[DRY-RUN] [%s] %q -> %q\n", label, e.Path, e.Destination)
		return
	}
	s.lg.Printf("[%s] %q -> %q\n", label, e.Path, e.Destination)
}

func (s *cliSink) fileError(e engine.FileError) {
	switch e.Op {
	case engine.OpWalk:
		s.lg.Printf("Walk error at %q: %v\n", e.Path, e.Err)
	case engine.OpWalkRoot:
		s.lg.Printf("Walk directory failed: %v\n", e.Err)
	case engine.OpOpen:
		s.lg.Printf("Error opening file %q: %v\n", e.Path, e.Err)
	case engine.OpRelPath:
		s.lg.Printf("Error computing relative path for %q: %v\n", e.Path, e.Err)
	case engine.OpResolve:
		s.lg.Printf("Error resolving destination for %q -> %q: %v\n", e.Path, e.Destination, e.Err)
	case engine.OpMove:
		s.lg.Printf("Error moving file %q -> %q: %v\n", e.Path, e.Destination, e.Err)
	case engine.OpRename:
		s.lg.Printf("Error moving %q to its clean name %q: %v\n", e.Path, e.Destination, e.Err)
	case engine.OpReadCue:
		s.lg.Printf("Error reading cue sheet %q: %v\n", e.Path, e.Err)
	case engine.OpSaveCueOriginal:
		s.lg.Printf("Error saving original of cue sheet %q: %v\n", e.Path, e.Err)
	case engine.OpRewriteCue:
		s.lg.Printf("Error rewriting cue sheet %q: %v\n", e.Path, e.Err)
	case engine.OpRecord:
		s.lg.Printf("Error recording %q in the database: %v\n", e.Path, e.Err)
	case engine.OpIndex:
		s.lg.Printf("Error reading %q into the index: %v\n", e.Path, e.Err)
	default:
		s.lg.Printf("Error with %q: %v\n", e.Path, e.Err)
	}
}

func (s *cliSink) warning(e engine.Warning) {
	switch e.Kind {
	case engine.WarnPermissions:
		s.lg.Printf("Warning: could not preserve permissions on %q: %v\n", e.Path, e.Err)
	case engine.WarnModTime:
		s.lg.Printf("Warning: could not preserve modification time on %q: %v\n", e.Path, e.Err)
	case engine.WarnCueNotBeside:
		s.lg.Printf("Warning: not rewriting cue sheet %q: %q is not next to it after the move\n", e.Path, e.Name)
	case engine.WarnCueEncoding:
		s.lg.Printf("Warning: not rewriting cue sheet %q: it is not UTF-8 and %q is not plain ASCII\n", e.Path, e.Name)
	case engine.WarnLinkSkipped:
		s.lg.Printf("Warning: leaving %q where it is: it is a link or junction to a folder, or to something that cannot be read\n", e.Path)
	case engine.WarnStaleCopy:
		if e.Err != nil {
			s.lg.Printf("Warning: could not remove the temporary copy a crash left at %q: %v\n", e.Path, e.Err)
		} else {
			s.lg.Printf("Removed the temporary copy a crash left at %q\n", e.Path)
		}
	}
}

func (s *cliSink) progress(e engine.Progress) {
	if e.At.Sub(s.lastPrinted) < progressEvery {
		return
	}
	s.lastPrinted = e.At

	c := e.Counters
	files := fmt.Sprintf("%d", e.Files)
	if e.FilesTotal > 0 {
		files = fmt.Sprintf("%d/%d", e.Files, e.FilesTotal)
	}
	eta := ""
	if e.ETA > 0 {
		eta = fmt.Sprintf(" | ETA %s", max(e.ETA.Round(time.Second), time.Second))
	}
	if e.Phase == engine.PhaseIndex {
		// Indexing reads every byte, so its speed is in bytes.
		s.lg.Printf("[PROGRESS] index: %s files (%s of %s) | %s/s | %.1f files/sec%s\n",
			files, formatBytes(e.Bytes), formatBytes(e.BytesTotal), formatBytes(int64(e.ByteRate)), e.Rate, eta)
		return
	}
	s.lg.Printf("[PROGRESS] %s: %s files (Organized: %d, Untagged: %d, Non-audio: %d, Duplicates: %d, In-place: %d, Errors: %d) | %.1f files/sec%s\n",
		e.Phase, files, c.Organized, c.Untagged, c.OtherMoved, c.Duplicates, c.InPlace+c.LeftInPlace, c.Errors, e.Rate, eta)
}

func (s *cliSink) summary(c engine.Counters) {
	s.lg.Println("================= Process Summary =================")
	s.lg.Printf("Total audio files found:  %d\n", c.TotalFound)
	s.lg.Printf("Files read into index:    %d\n", c.Indexed)
	s.lg.Printf("Organized files moved:    %d\n", c.Organized)
	s.lg.Printf("Untagged files moved:     %d\n", c.Untagged)
	s.lg.Printf("Non-audio files found:    %d\n", c.OtherFound)
	s.lg.Printf("Non-audio files moved:    %d\n", c.OtherMoved)
	s.lg.Printf("Duplicates moved:         %d\n", c.Duplicates)
	s.lg.Printf("Already in place:         %d\n", c.InPlace)
	if s.reorganize {
		s.lg.Printf("Not qualifying, left:     %d\n", c.LeftInPlace)
		s.lg.Printf("Moved to clean name:      %d\n", c.Retried)
	}
	s.lg.Printf("Cue sheets rewritten:     %d\n", c.CueRewritten)
	s.lg.Printf("Errors encountered:       %d\n", c.Errors)
	s.lg.Println("==================================================")
}

// needReview points at the TUI when duplicate groups wait for a person.
func (s *cliSink) needReview(n int) {
	switch {
	case n == 1:
		s.lg.Println("1 duplicate group needs review: run tagshelf tui")
	case n > 1:
		s.lg.Printf("%d duplicate groups need review: run tagshelf tui\n", n)
	}
}

func (s *cliSink) indexSummary(c engine.Counters) {
	s.lg.Println("================= Index Summary ==================")
	s.lg.Printf("Audio files found:        %d\n", c.TotalFound)
	s.lg.Printf("Files read into index:    %d\n", c.Indexed)
	s.lg.Printf("Errors encountered:       %d\n", c.Errors)
	s.lg.Println("==================================================")
}

// formatBytes writes a byte count in decimal units, as disks are sold.
func formatBytes(n int64) string {
	const unit = 1000
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	value, exp := float64(n)/unit, 0
	for value >= unit && exp < 3 {
		value /= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", value, "kMGT"[exp])
}
