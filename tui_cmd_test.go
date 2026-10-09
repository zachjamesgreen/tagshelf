package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"tagshelf/internal/config"
	"tagshelf/internal/engine"
	"tagshelf/internal/fixtures"
	"tagshelf/internal/marker"
	"tagshelf/internal/store"
	"tagshelf/internal/tagwrite"
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

// TestTUIRunnerDuplicates resolves one duplicate group and keeps both copies
// of another, through the runner tagshelf tui uses.
func TestTUIRunnerDuplicates(t *testing.T) {
	tmp, _, _ := cliEnv(t)
	r, lib := tuiRunnerFor(t, tmp, "")
	// Two pairs of files with the same bytes: no tags, so the same audio.
	writeFile(t, filepath.Join(lib, "_Untagged", "a.mp3"), "the first song's audio")
	writeFile(t, filepath.Join(lib, "_Untagged", "copies", "a.mp3"), "the first song's audio")
	writeFile(t, filepath.Join(lib, "_Untagged", "b.mp3"), "the second song's audio")
	writeFile(t, filepath.Join(lib, "_Untagged", "copies", "b.mp3"), "the second song's audio")
	if finish, err := r.Run(context.Background(), tui.RunIndex, false, func(engine.Event) {}); err != nil || finish.State != engine.StateOK {
		t.Fatalf("index = %+v, %v", finish, err)
	}

	groups := func() []store.Group {
		t.Helper()
		st, err := store.Open(r.cfg.Database)
		if err != nil {
			t.Fatal(err)
		}
		defer st.Close()
		g, err := st.GroupsToReview(engine.PossibleKey)
		if err != nil {
			t.Fatal(err)
		}
		return g
	}
	waiting := groups()
	if len(waiting) != 2 {
		t.Fatalf("groups to review = %+v, want 2", waiting)
	}

	g := waiting[0]
	finish, err := r.Resolve(context.Background(), []engine.Resolution{{Kind: g.Kind, Members: g.IDs(), Keeper: g.Files[0].ID}})
	if err != nil || finish.State != engine.StateOK || finish.Counters.Duplicates != 1 {
		t.Fatalf("resolve = %+v, %v", finish, err)
	}
	if _, err := os.Stat(g.Files[0].Path); err != nil {
		t.Errorf("the kept copy is gone: %v", err)
	}
	if _, err := os.Stat(g.Files[1].Path); !os.IsNotExist(err) {
		t.Errorf("the other copy is still there (%v)", err)
	}
	if _, err := os.Stat(filepath.Join(lib, "_Duplicates", g.Files[1].RelPath)); err != nil {
		t.Errorf("the other copy is not in _Duplicates: %v", err)
	}

	if err := r.KeepBoth(waiting[1].Kind, waiting[1].IDs()); err != nil {
		t.Fatal(err)
	}
	if left := groups(); len(left) != 0 {
		t.Errorf("groups still to review = %+v, want none", left)
	}
	for _, f := range waiting[1].Files {
		if _, err := os.Stat(f.Path); err != nil {
			t.Errorf("keep both moved %s: %v", f.Path, err)
		}
	}
}

// TestTUIRunnerSaveTags accepts the tags of a ripped file and of one without
// tags, through the runner tagshelf tui uses and a real tag save: the rip
// moves into the library, and the save and the move are recorded in one run.
func TestTUIRunnerSaveTags(t *testing.T) {
	tmp, _, _ := cliEnv(t)
	r, lib := tuiRunnerFor(t, tmp, "")
	ripped := filepath.Join(lib, "_Untagged", "ripped.flac")
	bare := filepath.Join(lib, "_Untagged", "bare.flac")
	data, err := os.ReadFile(fixtures.Path(fixtures.RipperFLAC))
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, ripped, string(data))
	if data, err = os.ReadFile(fixtures.Path(fixtures.ManualNoAlbumFLAC)); err != nil {
		t.Fatal(err)
	}
	writeFile(t, bare, string(data))
	if finish, err := r.Run(context.Background(), tui.RunIndex, false, func(engine.Event) {}); err != nil || finish.State != engine.StateOK {
		t.Fatalf("index = %+v, %v", finish, err)
	}
	open := func() *store.Store {
		t.Helper()
		st, err := store.Open(r.cfg.Database)
		if err != nil {
			t.Fatal(err)
		}
		return st
	}
	st := open()
	untagged, err := st.UntaggedFiles()
	st.Close()
	if err != nil || len(untagged) != 2 {
		t.Fatalf("untagged files = %+v, %v; want 2", untagged, err)
	}

	save := func(st *store.Store, runID int64, f store.File) error {
		_, err := tagwrite.Writer{History: st, RunID: runID}.Save(f.Path, nil, marker.New(marker.Hand, time.Now()))
		return err
	}
	var placed []engine.FilePlaced
	sink := func(e engine.Event) {
		if p, ok := e.(engine.FilePlaced); ok {
			placed = append(placed, p)
		}
	}
	finish, err := r.SaveTags(context.Background(), []int64{untagged[0].ID, untagged[1].ID}, save, sink)
	if err != nil || finish.State != engine.StateOK || finish.Counters.Organized != 1 || finish.Counters.LeftInPlace != 1 {
		t.Fatalf("save tags = %+v, %v; want one organized and one left", finish, err)
	}
	if _, err := os.Stat(ripped); !os.IsNotExist(err) {
		t.Errorf("the rip is still in _Untagged (%v)", err)
	}
	if _, err := os.Stat(bare); err != nil {
		t.Errorf("the file without an album left _Untagged: %v", err)
	}
	var dst string
	for _, p := range placed {
		if p.Outcome == engine.Organized {
			dst = p.Destination
		}
	}
	if _, err := os.Stat(dst); err != nil || !strings.HasPrefix(dst, filepath.Join(lib, fixtures.AlbumArtist)) {
		t.Errorf("the rip went to %q (%v), want it under its album artist in the library", dst, err)
	}

	st = open()
	defer st.Close()
	if left, err := st.UntaggedFiles(); err != nil || len(left) != 1 || left[0].Path != bare {
		t.Errorf("untagged files afterwards = %+v, %v; want only the one without an album", left, err)
	}
	runs, err := st.Runs()
	if err != nil {
		t.Fatal(err)
	}
	run := runs[len(runs)-1]
	if len(runs) != 2 || run.Kind != store.KindTUIAction || run.State != store.StateOK {
		t.Errorf("runs = %+v, want the index and one finished tui-action", runs)
	}
	moves, err := st.Moves(run.ID)
	if err != nil || len(moves) != 1 || moves[0].From != ripped || moves[0].To != dst || moves[0].Reason != store.ReasonOrganized {
		t.Errorf("moves = %+v, %v", moves, err)
	}
	// Only the rip was written: the other file had its marker already.
	writes, err := st.TagWrites(run.ID)
	if err != nil || len(writes) != 1 || writes[0].Path != ripped || !strings.Contains(writes[0].After, "hand ") {
		t.Errorf("tag writes = %+v, %v", writes, err)
	}
}

// TestTUIRunnerUndo organizes a source through the runner tagshelf tui uses
// and undoes the run: the file is back in the source and the run is undone.
func TestTUIRunnerUndo(t *testing.T) {
	tmp, _, _ := cliEnv(t)
	source := filepath.Join(tmp, "src")
	song := filepath.Join(source, "song.mp3")
	writeFile(t, song, "no tags")
	r, lib := tuiRunnerFor(t, tmp, source)
	organized, err := r.Run(context.Background(), tui.RunOrganize, false, func(engine.Event) {})
	if err != nil || organized.State != engine.StateOK || organized.RunID == 0 {
		t.Fatalf("organize = %+v, %v", organized, err)
	}
	if _, err := os.Stat(song); !os.IsNotExist(err) {
		t.Fatalf("the song is still in the source (%v)", err)
	}
	noCheck := func(path, after string) error { return nil }
	noRestore := func(*store.Store, int64, string, string, string) error { return nil }
	undone, err := r.Undo(context.Background(), organized.RunID, noCheck, noRestore, func(engine.Event) {})
	if err != nil || undone.State != engine.StateOK || undone.Counters.OtherMoved != 1 {
		t.Fatalf("undo = %+v, %v", undone, err)
	}
	if _, err := os.Stat(song); err != nil {
		t.Errorf("the song is not back in the source: %v", err)
	}
	if _, err := os.Stat(filepath.Join(lib, "_Untagged", "song.mp3")); !os.IsNotExist(err) {
		t.Errorf("the song is still in _Untagged (%v)", err)
	}
	// A run cannot be undone twice.
	if _, err := r.Undo(context.Background(), organized.RunID, noCheck, noRestore, func(engine.Event) {}); err == nil || !strings.Contains(err.Error(), "undone already") {
		t.Errorf("a second undo: %v", err)
	}
}
