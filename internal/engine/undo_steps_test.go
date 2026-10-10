package engine

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"tagshelf/internal/store"
)

// loneSong is a source with one song without tags, organized into the
// untagged folder of a library.
func loneSong(t *testing.T) (src, lib, placed string, s *store.Store, run store.Run) {
	t.Helper()
	tmp := t.TempDir()
	src, lib = filepath.Join(tmp, "src"), filepath.Join(tmp, "lib")
	writeTestFile(t, filepath.Join(src, "loose.mp3"), theAudio)
	s = openStore(t)
	if finish := Run(context.Background(), Options{Source: src, Destination: lib, Workers: 1, Store: s}); finish.State != StateOK || finish.Counters.Untagged != 1 {
		t.Fatalf("organize = %+v", finish)
	}
	return src, lib, filepath.Join(lib, "_Untagged", "loose.mp3"), s, lastRun(t, s)
}

// runState reads the state of a run.
func runState(t *testing.T, s *store.Store, id int64) store.RunState {
	t.Helper()
	run, err := s.Run(id)
	if err != nil {
		t.Fatal(err)
	}
	return run.State
}

// A run all of whose steps were skipped is not undone, and says so. While a
// step was skipped for a reason that can pass, the run can be undone again:
// here the file was edited with another tagger, and an index run reads it.
// The run used to be marked undone with nothing put back, for good.
func TestUndoThatSkipsEverythingLeavesTheRunUndoable(t *testing.T) {
	src, lib, placed, s, run := loneSong(t)
	writeTestFile(t, placed, theAudio+"edited")

	check, restore := noTags(t)
	undo := Undo(context.Background(), Options{Destination: lib, Store: s}, run.ID, check, restore)
	if undo.State != StateErrors || undo.Counters.OtherMoved != 0 || undo.Counters.Errors != 1 {
		t.Fatalf("undo = %+v, want nothing moved and the file skipped", undo)
	}
	if got := runState(t, s, run.ID); got == store.StateUndone {
		t.Fatalf("the run is %s after an undo that put nothing back", got)
	}
	if !strings.Contains(undo.NotUndone, "can be undone again") || !strings.Contains(undo.NotUndone, "can pass") {
		t.Errorf("NotUndone = %q, want it to say the run can be undone again once the reason has passed", undo.NotUndone)
	}

	indexLibrary(t, s, lib)
	plan, err := PlanUndo(s, run.ID, check)
	if err != nil || plan.Refusal != "" || plan.Moves != 1 || len(plan.Skipped) != 0 {
		t.Fatalf("plan after an index run = %+v, %v; want the file back", plan, err)
	}
	undo = Undo(context.Background(), Options{Destination: lib, Store: s}, run.ID, check, restore)
	if undo.State != StateOK || undo.Counters.OtherMoved != 1 || undo.NotUndone != "" {
		t.Fatalf("second undo = %+v, want the file moved back", undo)
	}
	if got := runState(t, s, run.ID); got != store.StateUndone {
		t.Errorf("the run is %s after its undo, want it undone", got)
	}
	mustExist(t, filepath.Join(src, "loose.mp3"))
}

// With nothing to put back for a reason that will not pass (the file is
// gone), the run is not marked undone either: nothing of it was.
func TestUndoThatUndoesNothingDoesNotMarkTheRunUndone(t *testing.T) {
	_, lib, placed, s, run := loneSong(t)
	if err := os.Remove(placed); err != nil {
		t.Fatal(err)
	}
	check, restore := noTags(t)
	plan, err := PlanUndo(s, run.ID, check)
	if err != nil || !plan.Nothing() || len(plan.Skipped) != 1 || plan.Skipped[0].Later {
		t.Fatalf("plan = %+v, %v; want nothing to undo and the file skipped for good", plan, err)
	}
	undo := Undo(context.Background(), Options{Destination: lib, Store: s}, run.ID, check, restore)
	if got := runState(t, s, run.ID); got == store.StateUndone {
		t.Errorf("the run is %s after an undo that put nothing back", got)
	}
	if !strings.Contains(undo.NotUndone, "nothing of it could be undone") {
		t.Errorf("NotUndone = %q", undo.NotUndone)
	}
}

// Undoing a reorganize that only changed letter case puts the folders'
// spelling back with the files', and the index rows'. The files went back
// and the folders kept the new spelling.
func TestUndoOfACaseOnlyRename(t *testing.T) {
	lib := filepath.Join(t.TempDir(), "lib")
	if caseSensitive(t, t.TempDir()) {
		t.Skip("abba and ABBA are separate folders on a case-sensitive file system")
	}
	writeTaggedMP3(t, filepath.Join(lib, "abba", "gold", "01 - dancing queen.mp3"), id3Tags{albumArtist: "ABBA", album: "Gold", title: "Dancing Queen", track: "1"}, "one")
	writeTestFile(t, filepath.Join(lib, "abba", "gold", "cover.jpg"), "art")
	s := openStore(t)
	finish := Run(context.Background(), Options{Source: lib, Destination: lib, Workers: 1, Store: s})
	if finish.State != StateOK || finish.Counters.Retried != 2 {
		t.Fatalf("reorganize = %+v", finish)
	}
	if got, want := exactNames(t, lib), []string{"ABBA/Gold/01 - Dancing Queen.mp3", "ABBA/Gold/cover.jpg"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("after the reorganize the library is %q, want %q", got, want)
	}
	// The index followed the new spelling.
	respelled := filepath.Join(lib, "ABBA", "Gold", "01 - Dancing Queen.mp3")
	if _, ok := indexedFiles(t, s)[respelled]; !ok {
		t.Errorf("index after the reorganize = %v, want the row respelled as %s", indexedFiles(t, s), respelled)
	}

	check, restore := noTags(t)
	undo := Undo(context.Background(), Options{Destination: lib, Store: s}, lastRun(t, s).ID, check, restore)
	if undo.State != StateOK || undo.Counters.OtherMoved != 2 {
		t.Fatalf("undo = %+v", undo)
	}
	if got, want := exactNames(t, lib), []string{"abba/gold/01 - dancing queen.mp3", "abba/gold/cover.jpg"}; !reflect.DeepEqual(got, want) {
		t.Errorf("after the undo the library is %q, want %q as before the run", got, want)
	}
	old := filepath.Join(lib, "abba", "gold", "01 - dancing queen.mp3")
	if _, ok := indexedFiles(t, s)[old]; !ok {
		t.Errorf("index after the undo = %v, want the row spelled as %s again", indexedFiles(t, s), old)
	}
	if again := Run(context.Background(), Options{Destination: lib, IndexOnly: true, Store: s}); again.Counters.Indexed != 0 || again.State != StateOK {
		t.Errorf("the index run after the undo = %+v, want nothing to read", again)
	}
}

// Undoing a reorganize in which two files traded names needs each step to
// see what the steps before it did: three moves, and both files back.
func TestUndoOfASwap(t *testing.T) {
	lib, a, b := swappedLibrary(t)
	wasA, wasB := readTestFile(t, a), readTestFile(t, b)
	s := openStore(t)
	if finish := Run(context.Background(), Options{Source: lib, Destination: lib, Workers: 1, Store: s}); finish.State != StateOK || finish.Counters.Retried != 1 {
		t.Fatalf("reorganize = %+v", finish)
	}
	if readTestFile(t, a) != wasB || readTestFile(t, b) != wasA {
		t.Fatal("the reorganize did not swap the files")
	}
	run := lastRun(t, s)
	check, restore := noTags(t)
	plan, err := PlanUndo(s, run.ID, check)
	if err != nil || plan.Moves != 3 || len(plan.Skipped) != 0 {
		t.Fatalf("plan = %+v, %v; want 3 moves and nothing skipped", plan, err)
	}
	if undo := Undo(context.Background(), Options{Destination: lib, Store: s}, run.ID, check, restore); undo.State != StateOK {
		t.Fatalf("undo = %+v", undo)
	}
	if got := exactNames(t, lib); len(got) != 2 {
		t.Errorf("after the undo: %q", got)
	}
	if readTestFile(t, a) != wasA || readTestFile(t, b) != wasB {
		t.Error("the undo did not put the two files back under their old names")
	}
}

// What an undo leaves as it is, step by step.
func TestUndoSkips(t *testing.T) {
	t.Run("a cue sheet that is gone", func(t *testing.T) {
		src, lib, s, run := organizedRip(t)
		cue := filepath.Join(lib, "Singer", "Album", "album.cue")
		if err := os.Remove(cue); err != nil {
			t.Fatal(err)
		}
		check, restore := noTags(t)
		plan, err := PlanUndo(s, run.ID, check)
		if err != nil || plan.Moves != 3 || plan.Cues != 0 || len(plan.Skipped) != 2 {
			t.Fatalf("plan = %+v, %v; want the cue sheet's rewrite and its move skipped", plan, err)
		}
		undo := Undo(context.Background(), Options{Destination: lib, Store: s}, run.ID, check, restore)
		if undo.Counters.OtherMoved != 3 || undo.Counters.Errors != 2 || undo.NotUndone != "" {
			t.Errorf("undo = %+v, want the three other files back and the run undone", undo)
		}
		mustExist(t, filepath.Join(src, "rip", "Song.mp3"))
		// The original the run kept is not the undo's to remove.
		mustExist(t, cue+".orig")
	})

	t.Run("a folder where the run left a file", func(t *testing.T) {
		_, _, placed, s, run := loneSong(t)
		if err := os.Remove(placed); err != nil {
			t.Fatal(err)
		}
		writeTestFile(t, filepath.Join(placed, "inside.txt"), "a folder now")
		check, _ := noTags(t)
		plan, err := PlanUndo(s, run.ID, check)
		if err != nil || !plan.Nothing() || len(plan.Skipped) != 1 || !strings.Contains(plan.Skipped[0].Reason, "no longer where") {
			t.Errorf("plan = %+v, %v; want the path skipped", plan, err)
		}
	})

	t.Run("a tag save whose file is gone", func(t *testing.T) {
		lib, _, dst, s, check, restore := taggedRip(t)
		saved := lastRun(t, s)
		if err := os.Remove(dst); err != nil {
			t.Fatal(err)
		}
		plan, err := PlanUndo(s, saved.ID, check)
		if err != nil || plan.TagSaves != 0 || plan.Moves != 1 || len(plan.Skipped) != 2 {
			t.Fatalf("plan = %+v, %v; want the save and the move of the missing file skipped", plan, err)
		}
		undo := Undo(context.Background(), Options{Destination: lib, Store: s}, saved.ID, check, restore)
		if undo.Counters.InPlace != 0 || undo.Counters.OtherMoved != 1 {
			t.Errorf("undo = %+v, want only the artwork back", undo)
		}
	})

	t.Run("a tag restore that fails", func(t *testing.T) {
		lib, ripped, dst, s, check, _ := taggedRip(t)
		saved := lastRun(t, s)
		failing := func(*store.Store, int64, string, string, string) error { return errors.New("disk full") }
		var c collector
		undo := Undo(context.Background(), Options{Destination: lib, Store: s, Sink: c.sink}, saved.ID, check, failing)
		errs := c.fileErrors()
		if undo.State != StateErrors || len(errs) != 1 || errs[0].Op != OpUndo || errs[0].Path != ripped || !strings.Contains(errs[0].Err.Error(), "disk full") {
			t.Errorf("undo = %+v with errors %+v, want the failed restore reported for the file", undo, errs)
		}
		if got := runState(t, s, saved.ID); got == store.StateUndone {
			t.Error("a run whose tag restore failed is marked undone")
		}
		if !strings.Contains(undo.NotUndone, "1 of its steps failed") {
			t.Errorf("NotUndone = %q", undo.NotUndone)
		}
		// The file went back all the same; its tags are still the saved ones.
		mustExist(t, ripped)
		mustNotExist(t, dst)
	})
}

// Runs that did not end well can be undone like any other: what they moved
// is on record. A dry run and a run still going cannot.
func TestUndoOfRunsThatDidNotEndWell(t *testing.T) {
	t.Run("interrupted", func(t *testing.T) {
		tmp := t.TempDir()
		src, lib := filepath.Join(tmp, "src"), filepath.Join(tmp, "lib")
		for _, name := range []string{"a.mp3", "b.mp3", "c.mp3"} {
			writeTestFile(t, filepath.Join(src, name), theAudio+name)
		}
		s := openStore(t)
		ctx, cancel := context.WithCancel(context.Background())
		sink := func(e Event) {
			if _, ok := e.(FilePlaced); ok {
				cancel()
			}
		}
		finish := Run(ctx, Options{Source: src, Destination: lib, Workers: 1, Store: s, Sink: sink})
		if finish.State != StateInterrupted || finish.Counters.Untagged != 1 {
			t.Fatalf("organize = %+v, want it stopped after one file", finish)
		}
		// What was moved is on record, and the index agrees with the disk.
		moves := movesOf(t, s, finish.RunID)
		if len(moves) != 1 {
			t.Fatalf("moves = %+v, want the one made", moves)
		}
		mustExist(t, moves[0].To)
		mustNotExist(t, moves[0].From)
		for path := range indexedFiles(t, s) {
			mustExist(t, path)
		}
		check, restore := noTags(t)
		undo := Undo(context.Background(), Options{Destination: lib, Store: s}, finish.RunID, check, restore)
		if undo.State != StateOK || undo.Counters.OtherMoved != 1 || undo.NotUndone != "" {
			t.Errorf("undo = %+v", undo)
		}
		if got := exactNames(t, src); len(got) != 3 {
			t.Errorf("source after the undo = %q, want all three files", got)
		}
	})

	t.Run("crashed", func(t *testing.T) {
		src, lib, _, s, run := loneSong(t)
		execSQL(t, s, `UPDATE runs SET state = 'crashed', ended_at = NULL WHERE id = ?`, run.ID)
		check, restore := noTags(t)
		if undo := Undo(context.Background(), Options{Destination: lib, Store: s}, run.ID, check, restore); undo.State != StateOK {
			t.Errorf("undo = %+v", undo)
		}
		mustExist(t, filepath.Join(src, "loose.mp3"))
	})

	t.Run("still running", func(t *testing.T) {
		_, lib, placed, s, run := loneSong(t)
		execSQL(t, s, `UPDATE runs SET state = 'running' WHERE id = ?`, run.ID)
		check, restore := noTags(t)
		if plan, err := PlanUndo(s, run.ID, check); err != nil || !strings.Contains(plan.Refusal, "still running") {
			t.Errorf("plan = %+v, %v", plan, err)
		}
		if undo := Undo(context.Background(), Options{Destination: lib, Store: s}, run.ID, check, restore); undo.State != StateNotStarted {
			t.Errorf("undo = %+v", undo)
		}
		mustExist(t, placed)
	})

	t.Run("a dry run", func(t *testing.T) {
		tmp := t.TempDir()
		src, lib := filepath.Join(tmp, "src"), filepath.Join(tmp, "lib")
		writeTestFile(t, filepath.Join(src, "loose.mp3"), theAudio)
		s := openStore(t)
		finish := Run(context.Background(), Options{Source: src, Destination: lib, Workers: 1, Store: s, DryRun: true})
		check, _ := noTags(t)
		if plan, err := PlanUndo(s, finish.RunID, check); err != nil || !strings.Contains(plan.Refusal, "dry run") {
			t.Errorf("plan = %+v, %v", plan, err)
		}
	})
}
