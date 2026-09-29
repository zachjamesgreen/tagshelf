package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"tagshelf/internal/config"
	"tagshelf/internal/engine"
	"tagshelf/internal/store"
	"tagshelf/internal/tui"
)

func TestRunTUIRefusesToStart(t *testing.T) {
	tests := []struct {
		name string
		args func(tmp, config string) []string
		log  string // part of the error logged, if any
	}{
		{"no destination", func(tmp, config string) []string {
			return []string{"tui", "-config", config}
		}, "destination directory must be specified"},
		{"unknown flag", func(tmp, config string) []string {
			return []string{"tui", "-config", config, "-no-such-flag", tmp}
		}, ""}, // the flag package reports it
		{"stray argument", func(tmp, config string) []string {
			return []string{"tui", "-config", config, "-dest", tmp, "extra"}
		}, "unexpected arguments"},
		{"library without an index", func(tmp, config string) []string {
			return []string{"tui", "-config", config, "-dest", filepath.Join(tmp, "lib")}
		}, "run tagshelf -index -dest"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmp, config, logs := cliEnv(t)
			if got := run(context.Background(), tt.args(tmp, config)); got != exitCannotStart {
				t.Errorf("exit code = %d, want %d", got, exitCannotStart)
			}
			if !strings.Contains(logs.String(), tt.log) {
				t.Errorf("log %q lacks %q", logs.String(), tt.log)
			}
		})
	}
}

func TestRunTUIOpensAnIndexedLibraryWithoutTheLock(t *testing.T) {
	tmp, config, logs := cliEnv(t)
	lib := filepath.Join(tmp, "lib")
	db := filepath.Join(tmp, "tagshelf.db")
	if got := run(context.Background(), []string{"-config", config, "-dest", lib, "-db", db, "-index"}); got != exitOK {
		t.Fatalf("-index exit code = %d\n%s", got, logs)
	}

	// A cancelled context ends the TUI as soon as it starts; getting that far
	// means the library opened.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if got := run(ctx, []string{"tui", "-config", config, "-dest", lib, "-db", db}); got != exitInterrupted {
		t.Errorf("exit code = %d, want %d\n%s", got, exitInterrupted, logs)
	}
}

// tuiRunnerFor returns the runner tagshelf tui would use for a library at
// lib, with a source folder holding one untagged song.
func tuiRunnerFor(t *testing.T, tmp, source string) (tuiRunner, string) {
	t.Helper()
	lib := filepath.Join(tmp, "lib")
	cfg, err := absolutePaths(config.Config{Source: source, Destination: lib, Database: filepath.Join(tmp, "tagshelf.db")}.WithDefaults())
	if err != nil {
		t.Fatal(err)
	}
	return tuiRunner{cfg: cfg}, lib
}

func TestTUIRunnerRuns(t *testing.T) {
	tmp, _, _ := cliEnv(t)
	source := filepath.Join(tmp, "src")
	song := filepath.Join(source, "loose.mp3")
	writeFile(t, song, "no tags")
	r, lib := tuiRunnerFor(t, tmp, source)
	var events int
	sink := func(engine.Event) { events++ }

	// A dry run moves nothing.
	finish, err := r.Run(context.Background(), tui.RunOrganize, true, sink)
	if err != nil || finish.State != engine.StateOK || finish.Counters.Untagged != 1 {
		t.Fatalf("dry organize = %+v, %v", finish, err)
	}
	if _, err := os.Stat(song); err != nil {
		t.Fatalf("a dry run moved the song: %v", err)
	}
	if events == 0 {
		t.Error("the sink saw no events")
	}

	finish, err = r.Run(context.Background(), tui.RunOrganize, false, sink)
	if err != nil || finish.State != engine.StateOK || finish.Counters.Untagged != 1 {
		t.Fatalf("organize = %+v, %v", finish, err)
	}
	if _, err := os.Stat(filepath.Join(lib, "_Untagged", "loose.mp3")); err != nil {
		t.Fatalf("the song was not organized: %v", err)
	}

	finish, err = r.Run(context.Background(), tui.RunReorganize, false, sink)
	if err != nil || finish.State != engine.StateOK || finish.Counters.LeftInPlace != 1 {
		t.Errorf("reorganize = %+v, %v; want the untagged song left where it is", finish, err)
	}
	// Dry run does not apply to an index run.
	finish, err = r.Run(context.Background(), tui.RunIndex, true, sink)
	if err != nil || finish.State != engine.StateOK || finish.Counters.Organized+finish.Counters.Untagged != 0 {
		t.Errorf("index = %+v, %v", finish, err)
	}

	st, err := store.Open(r.cfg.Database)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	runs, err := st.Runs()
	if err != nil {
		t.Fatal(err)
	}
	var kinds []string
	for _, run := range runs {
		kinds = append(kinds, fmt.Sprintf("%s dry=%t", run.Kind, run.DryRun))
	}
	// The dry run came before the library had an ID, so it ran in memory and
	// left no record.
	if want := "organize dry=false,reorganize dry=false,index dry=false"; strings.Join(kinds, ",") != want {
		t.Errorf("recorded runs = %v, want %s", kinds, want)
	}
}

func TestTUIRunnerRefuses(t *testing.T) {
	tmp, _, _ := cliEnv(t)
	r, _ := tuiRunnerFor(t, tmp, "")
	if _, err := r.Run(context.Background(), tui.RunOrganize, false, func(engine.Event) {}); err == nil || !strings.Contains(err.Error(), "no source directory") {
		t.Errorf("organize without a source: %v", err)
	}

	// Another holder of the library lock: here, this process's own open.
	r, lib := tuiRunnerFor(t, tmp, filepath.Join(tmp, "src"))
	held, err := store.OpenLibrary(lib, r.cfg.Database, false)
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()
	_, err = r.Run(context.Background(), tui.RunReorganize, false, func(engine.Event) {})
	var locked *store.LockedError
	if !errors.As(err, &locked) {
		t.Errorf("run while locked: %v, want a *store.LockedError", err)
	}
}
