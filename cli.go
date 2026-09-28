package main

import (
	"fmt"
	"log"
	"strings"
	"time"

	"tagshelf/internal/engine"
)

// cliSink prints a run's events as log lines. Per-file lines other than
// duplicates, errors and warnings are printed only when verbose.
type cliSink struct {
	lg      *log.Logger
	verbose bool

	// Set by RunStarted, before any other event can arrive.
	dryRun     bool
	reorganize bool

	// Only Progress events touch these, and they come one at a time.
	lastProcessed int64
	lastTime      time.Time
}

func newCLISink(lg *log.Logger, verbose bool) engine.Sink {
	s := &cliSink{lg: lg, verbose: verbose}
	return s.handle
}

func (s *cliSink) handle(e engine.Event) {
	switch e := e.(type) {
	case engine.RunStarted:
		s.dryRun = e.DryRun
		s.reorganize = e.Kind == engine.KindReorganize
		s.lastTime = time.Now()
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
		s.summary(e.Counters)
	}
}

func (s *cliSink) filePlaced(e engine.FilePlaced) {
	switch e.Outcome {
	case engine.Duplicate:
		s.lg.Printf("[SKIPPED] %q -> identical copy already at %q\n", e.Path, e.Destination)
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
	}
}

func (s *cliSink) progress(e engine.Progress) {
	c := e.Counters
	inPlace := c.InPlace + c.LeftInPlace
	totalProcessed := c.Organized + c.Untagged + c.OtherMoved + c.Skipped + inPlace + c.Errors

	elapsed := e.At.Sub(s.lastTime).Seconds()
	rate := 0.0
	if elapsed > 0 {
		rate = float64(totalProcessed-s.lastProcessed) / elapsed
	}
	s.lastProcessed = totalProcessed
	s.lastTime = e.At

	s.lg.Printf("[PROGRESS] Processed: %d (Organized: %d, Untagged: %d, Non-audio: %d, Skipped: %d, In-place: %d, Errors: %d) | %.1f files/sec\n",
		totalProcessed, c.Organized, c.Untagged, c.OtherMoved, c.Skipped, inPlace, c.Errors, rate)
}

func (s *cliSink) summary(c engine.Counters) {
	s.lg.Println("================= Process Summary =================")
	s.lg.Printf("Total audio files found:  %d\n", c.TotalFound)
	s.lg.Printf("Organized files moved:    %d\n", c.Organized)
	s.lg.Printf("Untagged files moved:     %d\n", c.Untagged)
	s.lg.Printf("Non-audio files found:    %d\n", c.OtherFound)
	s.lg.Printf("Non-audio files moved:    %d\n", c.OtherMoved)
	s.lg.Printf("Duplicate files skipped:  %d\n", c.Skipped)
	s.lg.Printf("Already in place:         %d\n", c.InPlace)
	if s.reorganize {
		s.lg.Printf("Not qualifying, left:     %d\n", c.LeftInPlace)
		s.lg.Printf("Moved to clean name:      %d\n", c.Retried)
	}
	s.lg.Printf("Cue sheets rewritten:     %d\n", c.CueRewritten)
	s.lg.Printf("Errors encountered:       %d\n", c.Errors)
	s.lg.Println("==================================================")
}
