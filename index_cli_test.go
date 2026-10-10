package main

import (
	"bytes"
	"context"
	"errors"
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

func TestCLISinkIndexLines(t *testing.T) {
	var buf bytes.Buffer
	sink := newCLISink(log.New(&buf, "", 0), false)
	start := time.Now()
	sink(engine.RunStarted{Kind: engine.KindIndex})
	sink(engine.PhaseStarted{Phase: engine.PhaseIndex, Files: 1234, Bytes: 40_000_000_000, First: true})
	sink(engine.FileError{Path: "a", Op: engine.OpIndex, Err: errors.New("boom")})
	sink(engine.Progress{Phase: engine.PhaseIndex, Files: 120, FilesTotal: 1234, Bytes: 1_200_000_000, BytesTotal: 40_000_000_000,
		Rate: 12, ByteRate: 85_300_000, ETA: 471 * time.Second, At: start.Add(3 * time.Second)})
	sink(engine.RunFinished{Counters: engine.Counters{TotalFound: 1234, Indexed: 120, Errors: 1}, State: engine.StateInterrupted})

	want := `First index: 1234 files, 40.0 GB. Every file is read once; later runs only read changed files.
Error reading "a" into the index: boom
[PROGRESS] index: 120/1234 files (1.2 GB of 40.0 GB) | 85.3 MB/s | 12.0 files/sec | ETA 7m51s
================= Index Summary ==================
Audio files found:        1234
Files read into index:    120
Errors encountered:       1
==================================================
Index interrupted: the files already read are kept, and the next run reads the rest.
`
	if got := buf.String(); got != want {
		t.Errorf("got\n%s\nwant\n%s", got, want)
	}

	// Only the first index gets the heads-up.
	buf.Reset()
	sink(engine.PhaseStarted{Phase: engine.PhaseIndex, Files: 3, Bytes: 10})
	if buf.Len() != 0 {
		t.Errorf("a later index printed %q", buf.String())
	}
}

func TestFormatBytes(t *testing.T) {
	for n, want := range map[int64]string{
		0: "0 B", 999: "999 B", 1000: "1.0 kB", 3_400_000: "3.4 MB", 40_000_000_000: "40.0 GB",
		2_500_000_000_000: "2.5 TB", 5_000_000_000_000_000: "5000.0 TB",
	} {
		if got := formatBytes(n); got != want {
			t.Errorf("formatBytes(%d) = %q, want %q", n, got, want)
		}
	}
}

// cliEnv sets up a CLI test: log output and standard output captured
// together, the data folder in a temp dir, and an empty config file. It
// returns the temp dir, the config and what was captured.
func cliEnv(t *testing.T) (tmp, config string, logs *bytes.Buffer) {
	t.Helper()
	logs = &bytes.Buffer{}
	log.SetOutput(logs)
	stdout = logs
	t.Cleanup(func() { log.SetOutput(os.Stderr); stdout = os.Stdout })
	isolateDataDir(t)
	tmp = t.TempDir()
	config = filepath.Join(tmp, "empty.yaml")
	writeFile(t, config, "")
	return tmp, config, logs
}

func TestRunIndexFlag(t *testing.T) {
	tmp, config, logs := cliEnv(t)
	lib := filepath.Join(tmp, "lib")
	misplaced := filepath.Join(lib, "loose.mp3")
	writeFile(t, misplaced, "no tags")
	db := filepath.Join(tmp, "tagshelf.db")

	// No source needed, and a dry-run setting does not stop it keeping the index.
	args := []string{"-config", config, "-dest", lib, "-db", db, "-index", "-dry-run"}
	if got := run(context.Background(), args); got != exitOK {
		t.Fatalf("run(%q) = %d, want %d\n%s", args, got, exitOK, logs)
	}
	if _, err := os.Stat(misplaced); err != nil {
		t.Errorf("-index moved a file: %v", err)
	}
	if id, err := store.ReadLibraryID(lib); err != nil || id == "" {
		t.Errorf("library ID = %q, %v; want one written by -index", id, err)
	}
	if !strings.Contains(logs.String(), "First index: 1 files, 7 B. Every file is read once; later runs only read changed files.") {
		t.Errorf("no first-index line in:\n%s", logs)
	}

	logs.Reset()
	if got := run(context.Background(), []string{"-config", config, "-dest", lib, "-db", db, "-index"}); got != exitOK {
		t.Fatalf("second -index = %d", got)
	}
	if strings.Contains(logs.String(), "First index") {
		t.Errorf("second index printed the first-index line:\n%s", logs)
	}
	if got := run(context.Background(), []string{"-config", config, "-dest", lib, "-db", db, "-index", "--rescan"}); got != exitOK {
		t.Fatalf("-index --rescan = %d", got)
	}

	s, err := store.Open(db)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	runs, err := s.Runs()
	if err != nil {
		t.Fatal(err)
	}
	var indexed []int64
	for _, r := range runs {
		if r.Kind != store.KindIndex || r.DryRun || r.State != store.StateOK {
			t.Errorf("run = %+v, want an ok index run", r)
		}
		indexed = append(indexed, r.Counters.Indexed)
	}
	if want := []int64{1, 0, 1}; !reflect.DeepEqual(indexed, want) {
		t.Errorf("files indexed per run = %v, want %v", indexed, want)
	}
}

func TestRunDryRunOnUnidentifiedLibraryWarns(t *testing.T) {
	tmp, config, logs := cliEnv(t)
	writeFile(t, filepath.Join(tmp, "src", "loose.mp3"), "no tags")
	lib := filepath.Join(tmp, "lib")
	const warning = "No library index yet: this dry run indexes in memory and discards it. Run tagshelf -index first to keep it."

	if got := run(context.Background(), []string{"-config", config, "-source", filepath.Join(tmp, "src"), "-dest", lib, "-dry-run"}); got != exitOK {
		t.Fatalf("dry run = %d", got)
	}
	if !strings.Contains(logs.String(), warning) {
		t.Errorf("no warning in:\n%s", logs)
	}

	// Once the library has an ID, a dry run uses its database and says nothing.
	if got := run(context.Background(), []string{"-config", config, "-dest", lib, "-index"}); got != exitOK {
		t.Fatalf("-index = %d", got)
	}
	logs.Reset()
	if got := run(context.Background(), []string{"-config", config, "-source", filepath.Join(tmp, "src"), "-dest", lib, "-dry-run"}); got != exitOK {
		t.Fatalf("dry run = %d", got)
	}
	if strings.Contains(logs.String(), warning) {
		t.Errorf("warned on an identified library:\n%s", logs)
	}
}
