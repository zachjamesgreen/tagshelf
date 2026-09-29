package main

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
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
			return []string{"tui", "-config", config, "-source", tmp}
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
