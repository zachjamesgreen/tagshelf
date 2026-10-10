package main

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/zachjamesgreen/tagshelf/internal/engine"
	"github.com/zachjamesgreen/tagshelf/internal/store"
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
		{"duplicate", organize, false, placed(engine.Duplicate), `[DUPLICATE] "a" -> "b"`},
		{"dry duplicate", dryRun, false, placed(engine.Duplicate), `[DRY-RUN] [DUPLICATE] "a" -> "b"`},
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
		{"record", organize, false, engine.FileError{Path: "a", Op: engine.OpRecord, Err: boom}, `Error recording "a" in the database: boom`},
		{"not started", organize, false, engine.RunFinished{State: engine.StateNotStarted, Err: boom}, `Error: could not record the run, nothing was touched: boom`},
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

	c := engine.Counters{TotalFound: 20, Indexed: 7, Organized: 4, Untagged: 3, OtherFound: 9, OtherMoved: 2, Duplicates: 1, InPlace: 5, LeftInPlace: 1, Retried: 2, CueRewritten: 1, Errors: 1}
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
		"[PROGRESS] walk: 29 files (Organized: 4, Untagged: 3, Non-audio: 2, Duplicates: 1, In-place: 6, Errors: 1) | 12.2 files/sec",
		"[PROGRESS] audio: 17/40 files (Organized: 4, Untagged: 3, Non-audio: 2, Duplicates: 1, In-place: 6, Errors: 1) | 7.5 files/sec | ETA 3s",
	}
	if got := lines[:2]; !reflect.DeepEqual(got, wantProgress) {
		t.Errorf("progress lines:\ngot  %q\nwant %q", got, wantProgress)
	}
	want := `================= Process Summary =================
Total audio files found:  20
Files read into index:    7
Organized files moved:    4
Untagged files moved:     3
Non-audio files found:    9
Non-audio files moved:    2
Duplicates moved:         1
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

func TestCLISinkNeedReview(t *testing.T) {
	for n, want := range map[int]string{
		0: "",
		1: "1 duplicate group needs review: run tagshelf tui\n",
		3: "3 duplicate groups need review: run tagshelf tui\n",
	} {
		for _, kind := range []engine.RunKind{engine.KindOrganize, engine.KindIndex} {
			var buf bytes.Buffer
			sink := newCLISink(log.New(&buf, "", 0), false)
			sink(engine.RunStarted{Kind: kind})
			sink(engine.RunFinished{NeedReview: n})
			got := strings.TrimPrefix(buf.String()[strings.LastIndex(buf.String(), "=====\n"):], "=====\n")
			if got != want {
				t.Errorf("kind %v, %d groups: after the summary got %q, want %q", kind, n, got, want)
			}
		}
	}
}

// TestRunMovesDuplicates runs the CLI end to end over an incoming copy of a
// song already in the library, with the duplicates folder moved by
// -duplicates.
func TestRunMovesDuplicates(t *testing.T) {
	tmp, config, out := cliEnv(t)
	lib := filepath.Join(tmp, "lib")
	audio := strings.Repeat("some audio ", 40)
	writeFile(t, filepath.Join(lib, "_Untagged", "song.mp3"), audio)
	dups := filepath.Join(tmp, "dups")
	args := []string{"-config", config, "-dest", lib, "-duplicates", dups}
	if got := run(context.Background(), append(args, "-index")); got != exitOK {
		t.Fatalf("index run = %d", got)
	}
	copied := filepath.Join(tmp, "src", "Folder", "song.mp3")
	writeFile(t, copied, audio)

	out.Reset()
	if got := run(context.Background(), append(args, "-source", filepath.Join(tmp, "src"))); got != exitOK {
		t.Fatalf("run = %d, want %d:\n%s", got, exitOK, out.String())
	}
	moved := filepath.Join(dups, "Folder", "song.mp3")
	if _, err := os.Stat(moved); err != nil {
		t.Errorf("copy not moved to %q: %v", moved, err)
	}
	for _, want := range []string{fmt.Sprintf("[DUPLICATE] %q -> %q", copied, moved), "Duplicates moved:         1"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output lacks %q:\n%s", want, out.String())
		}
	}
	if strings.Contains(out.String(), "need review") || strings.Contains(out.String(), "needs review") {
		t.Errorf("output asks for a review:\n%s", out.String())
	}
}

// TestRunExitCodes runs the CLI end to end. Every case passes an explicit empty
// -config, though TestMain has seen to it that there is no config.yaml in the
// working directory to pick up.
func TestRunExitCodes(t *testing.T) {
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
		{"database cannot open", context.Background(), func(tmp string) []string {
			// The database's folder would have to be where a file is.
			writeFile(t, filepath.Join(tmp, "blocker"), "in the way")
			return []string{"-source", filepath.Join(tmp, "src"), "-dest", filepath.Join(tmp, "lib"), "-db", filepath.Join(tmp, "blocker", "tagshelf.db")}
		}, exitCannotStart},
		{"database newer", context.Background(), func(tmp string) []string {
			db := filepath.Join(tmp, "newer.db")
			s, err := store.Open(db)
			if err != nil {
				t.Fatal(err)
			}
			s.Close()
			setUserVersion(t, db, 1000)
			return []string{"-source", filepath.Join(tmp, "src"), "-dest", filepath.Join(tmp, "lib"), "-db", db}
		}, exitCannotStart},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmp, config, logs := cliEnv(t)
			song := filepath.Join(tmp, "src", "loose.mp3")
			writeFile(t, song, "no tags")
			args := append([]string{"-config", config}, tt.args(tmp)...)

			if got := run(tt.ctx, args); got != tt.want {
				t.Errorf("run(%q) = %d, want %d\n%s", args, got, tt.want, logs)
			}
			_, err := os.Stat(song)
			if moved := err != nil; moved != (tt.want == exitOK) {
				t.Errorf("song moved = %t, want %t", moved, tt.want == exitOK)
			}
		})
	}
}

// isolateDataDir points the default database location into a temp folder, so
// that CLI tests never write the user's real application data.
func isolateDataDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("LOCALAPPDATA", dir)
	t.Setenv("XDG_DATA_HOME", dir)
	return dir
}

// setUserVersion stamps the database at path with a schema version.
func setUserVersion(t *testing.T, path string, v int) {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(fmt.Sprintf("PRAGMA user_version = %d", v)); err != nil {
		t.Fatal(err)
	}
}

func TestRunRecordsHistory(t *testing.T) {
	tmp, config, _ := cliEnv(t)
	data := os.Getenv("LOCALAPPDATA")
	song := filepath.Join(tmp, "src", "loose.mp3")
	writeFile(t, song, "no tags")
	lib := filepath.Join(tmp, "lib")

	if got := run(context.Background(), []string{"-config", config, "-source", filepath.Join(tmp, "src"), "-dest", lib}); got != exitOK {
		t.Fatalf("run = %d, want %d", got, exitOK)
	}

	id, err := store.ReadLibraryID(lib)
	if err != nil || id == "" {
		t.Fatalf("library ID = %q, %v; want one written by the first real run", id, err)
	}
	s, err := store.Open(filepath.Join(data, "tagshelf", "libraries", id, "tagshelf.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	runs, err := s.Runs()
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 1 || runs[0].State != store.StateOK || runs[0].Counters.Untagged != 1 {
		t.Fatalf("runs = %+v, want one ok run with 1 untagged", runs)
	}
	moves, err := s.Moves(runs[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(moves) != 1 || moves[0].From != song || moves[0].Reason != store.ReasonUntagged {
		t.Errorf("moves = %+v, want the untagged move of %q", moves, song)
	}
}

func TestRunRefusedWhileLibraryLocked(t *testing.T) {
	tmp, config, out := cliEnv(t)
	song := filepath.Join(tmp, "src", "loose.mp3")
	writeFile(t, song, "no tags")
	lib := filepath.Join(tmp, "lib")

	// Another run holds the library. The lock is per open file, so holding it
	// in this process stands in for another process (internal/store tests the
	// real thing).
	holder, err := store.OpenLibrary(lib, "", false)
	if err != nil {
		t.Fatal(err)
	}
	defer holder.Close()

	// Naming another database does not get a run past the lock: it is the
	// library's, not the database's.
	elsewhere := filepath.Join(tmp, "another.db")
	for _, extra := range [][]string{nil, {"-dry-run"}, {"-db", elsewhere}, {"-db", elsewhere, "-index"}} {
		out.Reset()
		args := append([]string{"-config", config, "-source", filepath.Join(tmp, "src"), "-dest", lib}, extra...)
		if got := run(context.Background(), args); got != exitCannotStart {
			t.Fatalf("run(%q) = %d, want %d", args, got, exitCannotStart)
		}
		if !strings.Contains(out.String(), "try again when it has finished") {
			t.Errorf("run(%q) printed %q, want it to say the library is busy", args, out.String())
		}
		if want := fmt.Sprintf("pid %d, started ", os.Getpid()); !strings.Contains(out.String(), want) {
			t.Errorf("run(%q) printed %q, want the holder's %q", args, out.String(), want)
		}
	}
	if _, err := os.Stat(song); err != nil {
		t.Errorf("a refused run moved the song: %v", err)
	}
	if runs, err := holder.Runs(); err != nil || len(runs) != 0 {
		t.Errorf("runs = %+v, %v; want none recorded by refused runs", runs, err)
	}
	if _, err := os.Stat(elsewhere); !os.IsNotExist(err) {
		t.Errorf("a refused run made its database: %v", err)
	}
}

func TestRunDryRunOnUnidentifiedLibraryWritesNothing(t *testing.T) {
	tmp, config, _ := cliEnv(t)
	data := os.Getenv("LOCALAPPDATA")
	song := filepath.Join(tmp, "src", "loose.mp3")
	writeFile(t, song, "no tags")
	lib := filepath.Join(tmp, "lib")
	db := filepath.Join(tmp, "explicit.db")

	for _, extra := range [][]string{nil, {"-db", db}} {
		args := append([]string{"-config", config, "-source", filepath.Join(tmp, "src"), "-dest", lib, "-dry-run"}, extra...)
		if got := run(context.Background(), args); got != exitOK {
			t.Fatalf("run(%q) = %d, want %d", args, got, exitOK)
		}
	}

	if _, err := os.Stat(song); err != nil {
		t.Errorf("a dry run moved the song: %v", err)
	}
	for _, path := range []string{lib, db} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Errorf("a dry run created %q: %v", path, err)
		}
	}
	if entries, _ := os.ReadDir(data); len(entries) != 0 {
		t.Errorf("a dry run wrote under the data folder: %v", entries)
	}

	// Nor is anything written in a folder that is there but is not a library
	// yet: no ID file, and no lock file.
	if err := os.Mkdir(lib, 0o755); err != nil {
		t.Fatal(err)
	}
	args := []string{"-config", config, "-source", filepath.Join(tmp, "src"), "-dest", lib, "-dry-run"}
	if got := run(context.Background(), args); got != exitOK {
		t.Fatalf("run(%q) = %d, want %d", args, got, exitOK)
	}
	if entries, err := os.ReadDir(lib); err != nil || len(entries) != 0 {
		t.Errorf("a dry run wrote %v in the folder (%v)", entries, err)
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
