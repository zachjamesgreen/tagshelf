package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"tagshelf/internal/engine"
)

// TestCLISinkLines pins the exact log line printed for every event, so that
// moving the organizer behind a sink cannot change what the CLI prints.
func TestCLISinkLines(t *testing.T) {
	boom := errors.New("boom")
	organize := engine.RunStarted{Kind: engine.KindOrganize}
	dryRun := engine.RunStarted{Kind: engine.KindOrganize, DryRun: true}
	placed := func(o engine.Outcome) engine.FilePlaced {
		return engine.FilePlaced{Path: "a", Destination: "b", Outcome: o}
	}
	cue := engine.CueRewritten{Path: "c.cue", Renames: []engine.Rename{{From: "x", To: "y"}, {From: "p", To: "q"}}}

	tests := []struct {
		name    string
		start   engine.RunStarted
		verbose bool
		event   engine.Event
		want    string // "" means nothing is printed
	}{
		{"moved", organize, true, placed(engine.Organized), `[MOVED] "a" -> "b"`},
		{"untagged", organize, true, placed(engine.Untagged), `[UNTAGGED] "a" -> "b"`},
		{"companion", organize, true, placed(engine.Companion), `[COMPANION] "a" -> "b"`},
		{"other", organize, true, placed(engine.Other), `[OTHER] "a" -> "b"`},
		{"dry organize", dryRun, true, placed(engine.Organized), `[DRY-RUN] [ORGANIZE] "a" -> "b"`},
		{"dry untagged", dryRun, true, placed(engine.Untagged), `[DRY-RUN] [UNTAGGED] "a" -> "b"`},
		{"dry companion", dryRun, true, placed(engine.Companion), `[DRY-RUN] [COMPANION] "a" -> "b"`},
		{"dry other", dryRun, true, placed(engine.Other), `[DRY-RUN] [OTHER] "a" -> "b"`},
		{"moved quiet", organize, false, placed(engine.Organized), ""},
		{"duplicate", organize, false, placed(engine.Duplicate), `[SKIPPED] "a" -> identical copy already at "b"`},
		{"in place", organize, true, placed(engine.InPlace), `[IN-PLACE] "a" is already at its destination`},
		{"in place quiet", organize, false, placed(engine.InPlace), ""},
		{"left", organize, true, placed(engine.Left), `[LEFT] "a" does not qualify for the library, leaving it in place`},
		{"left quiet", organize, false, placed(engine.Left), ""},
		{"renamed", organize, true, placed(engine.Renamed), `[RENAMED] "a" -> "b"`},
		{"renamed quiet", organize, false, placed(engine.Renamed), ""},
		{"cue", organize, true, cue, `[CUE] "c.cue": "x" -> "y", "p" -> "q"`},
		{"dry cue", dryRun, true, cue, `[DRY-RUN] [CUE] "c.cue": "x" -> "y", "p" -> "q"`},
		{"cue quiet", organize, false, cue, ""},
		{"cleanup", organize, false, engine.DirRemoved{Path: "d"}, `[CLEANUP] Removed empty directory "d"`},
		{"walk", organize, false, engine.FileError{Path: "a", Op: engine.OpWalk, Err: boom}, `Walk error at "a": boom`},
		{"walk root", organize, false, engine.FileError{Path: "a", Op: engine.OpWalkRoot, Err: boom}, `Walk directory failed: boom`},
		{"open", organize, false, engine.FileError{Path: "a", Op: engine.OpOpen, Err: boom}, `Error opening file "a": boom`},
		{"relpath", organize, false, engine.FileError{Path: "a", Op: engine.OpRelPath, Err: boom}, `Error computing relative path for "a": boom`},
		{"resolve", organize, false, engine.FileError{Path: "a", Destination: "b", Op: engine.OpResolve, Err: boom}, `Error resolving destination for "a" -> "b": boom`},
		{"move", organize, false, engine.FileError{Path: "a", Destination: "b", Op: engine.OpMove, Err: boom}, `Error moving file "a" -> "b": boom`},
		{"rename", organize, false, engine.FileError{Path: "a", Destination: "b", Op: engine.OpRename, Err: boom}, `Error moving "a" to its clean name "b": boom`},
		{"read cue", organize, false, engine.FileError{Path: "a", Op: engine.OpReadCue, Err: boom}, `Error reading cue sheet "a": boom`},
		{"save cue", organize, false, engine.FileError{Path: "a", Destination: "a.orig", Op: engine.OpSaveCueOriginal, Err: boom}, `Error saving original of cue sheet "a": boom`},
		{"rewrite cue", organize, false, engine.FileError{Path: "a", Op: engine.OpRewriteCue, Err: boom}, `Error rewriting cue sheet "a": boom`},
		{"perm", organize, false, engine.Warning{Kind: engine.WarnPermissions, Path: "a", Err: boom}, `Warning: could not preserve permissions on "a": boom`},
		{"mtime", organize, false, engine.Warning{Kind: engine.WarnModTime, Path: "a", Err: boom}, `Warning: could not preserve modification time on "a": boom`},
		{"cue beside", organize, false, engine.Warning{Kind: engine.WarnCueNotBeside, Path: "a", Name: "n"}, `Warning: not rewriting cue sheet "a": "n" is not next to it after the move`},
		{"cue encoding", organize, false, engine.Warning{Kind: engine.WarnCueEncoding, Path: "a", Name: "n"}, `Warning: not rewriting cue sheet "a": it is not UTF-8 and "n" is not plain ASCII`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			sink := newCLISink(log.New(&buf, "", 0), tt.verbose)
			sink(tt.start)
			sink(tt.event)
			want := ""
			if tt.want != "" {
				want = tt.want + "\n"
			}
			if got := buf.String(); got != want {
				t.Errorf("got %q, want %q", got, want)
			}
		})
	}
}

func TestCLISinkProgressAndSummary(t *testing.T) {
	var buf bytes.Buffer
	sink := newCLISink(log.New(&buf, "", 0), false)
	start := time.Now()
	sink(engine.RunStarted{Kind: engine.KindReorganize})

	c := engine.Counters{TotalFound: 20, Organized: 4, Untagged: 3, OtherFound: 9, OtherMoved: 2, Skipped: 1, InPlace: 5, LeftInPlace: 1, Retried: 2, CueRewritten: 1, Errors: 1}
	progress := func(after time.Duration) engine.Progress {
		return engine.Progress{Counters: c, Phase: engine.PhaseAudio, Files: 17, FilesTotal: 40, Rate: 7.5, ETA: 3067 * time.Millisecond, At: start.Add(after)}
	}
	sink(engine.Progress{Counters: c, Phase: engine.PhaseWalk, Files: 29, At: start.Add(time.Second)}) // too soon
	sink(engine.Progress{Counters: c, Phase: engine.PhaseWalk, Files: 29, Rate: 12.25, At: start.Add(2500 * time.Millisecond)})
	sink(progress(3 * time.Second)) // too soon after the last line
	sink(progress(4600 * time.Millisecond))
	sink(engine.RunFinished{Counters: c})

	lines := strings.Split(buf.String(), "\n")
	wantProgress := []string{
		"[PROGRESS] walk: 29 files (Organized: 4, Untagged: 3, Non-audio: 2, Skipped: 1, In-place: 6, Errors: 1) | 12.2 files/sec",
		"[PROGRESS] audio: 17/40 files (Organized: 4, Untagged: 3, Non-audio: 2, Skipped: 1, In-place: 6, Errors: 1) | 7.5 files/sec | ETA 3s",
	}
	if got := lines[:2]; !reflect.DeepEqual(got, wantProgress) {
		t.Errorf("progress lines:\ngot  %q\nwant %q", got, wantProgress)
	}
	want := `================= Process Summary =================
Total audio files found:  20
Organized files moved:    4
Untagged files moved:     3
Non-audio files found:    9
Non-audio files moved:    2
Duplicate files skipped:  1
Already in place:         5
Not qualifying, left:     1
Moved to clean name:      2
Cue sheets rewritten:     1
Errors encountered:       1
==================================================
`
	if got := strings.Join(lines[2:], "\n"); got != want {
		t.Errorf("summary:\ngot\n%s\nwant\n%s", got, want)
	}
}

func TestCLISinkInterruptedSummary(t *testing.T) {
	var buf bytes.Buffer
	sink := newCLISink(log.New(&buf, "", 0), false)
	sink(engine.RunStarted{Kind: engine.KindOrganize})
	sink(engine.RunFinished{State: engine.StateInterrupted})

	want := "==================================================\nRun interrupted: the remaining files were left where they are and empty folders were not cleaned up.\n"
	if got := buf.String(); !strings.HasSuffix(got, want) {
		t.Errorf("summary does not end with the interrupted line:\n%s", got)
	}
}

// TestRunExitCodes runs the CLI end to end. Every case passes an explicit empty
// -config, so a config.yaml in the working directory is never picked up.
func TestRunExitCodes(t *testing.T) {
	log.SetOutput(io.Discard)
	defer log.SetOutput(os.Stderr)

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()

	tests := []struct {
		name string
		ctx  context.Context
		args func(tmp string) []string
		want int
	}{
		{"ok", context.Background(), func(tmp string) []string {
			return []string{"-source", filepath.Join(tmp, "src"), "-dest", filepath.Join(tmp, "lib")}
		}, exitOK},
		{"file errors", context.Background(), func(tmp string) []string {
			// The untagged folder is a file, so the untagged song cannot move.
			writeFile(t, filepath.Join(tmp, "untagged"), "in the way")
			return []string{"-source", filepath.Join(tmp, "src"), "-dest", filepath.Join(tmp, "lib"), "-untagged", filepath.Join(tmp, "untagged")}
		}, exitErrors},
		{"interrupted", cancelled, func(tmp string) []string {
			return []string{"-source", filepath.Join(tmp, "src"), "-dest", filepath.Join(tmp, "lib")}
		}, exitInterrupted},
		{"no dest", context.Background(), func(tmp string) []string {
			return []string{"-source", filepath.Join(tmp, "src")}
		}, exitCannotStart},
		{"missing config", context.Background(), func(tmp string) []string {
			return []string{"-config", filepath.Join(tmp, "missing.yaml"), "-source", filepath.Join(tmp, "src"), "-dest", filepath.Join(tmp, "lib")}
		}, exitCannotStart},
		{"bad flag", context.Background(), func(tmp string) []string {
			return []string{"-no-such-flag"}
		}, exitCannotStart},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmp := t.TempDir()
			song := filepath.Join(tmp, "src", "loose.mp3")
			writeFile(t, song, "no tags")
			config := filepath.Join(tmp, "empty.yaml")
			writeFile(t, config, "")
			args := append([]string{"-config", config}, tt.args(tmp)...)

			if got := run(tt.ctx, args); got != tt.want {
				t.Errorf("run(%q) = %d, want %d", args, got, tt.want)
			}
			_, err := os.Stat(song)
			if moved := err != nil; moved != (tt.want == exitOK) {
				t.Errorf("song moved = %t, want %t", moved, tt.want == exitOK)
			}
		})
	}
}

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0644); err != nil {
		t.Fatal(err)
	}
}
