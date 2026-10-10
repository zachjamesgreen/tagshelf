package engine

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"tagshelf/internal/fixtures"
	"tagshelf/internal/store"
)

// recordedSaves are a tag save, check and restore that stand in for real
// tags and are recorded as tag writes: a file holds the tags "manual" or
// "ripper" when it has that FLAC fixture's bytes.
func recordedSaves(t *testing.T) (save TagSave, check TagCheck, restore TagRestore) {
	t.Helper()
	bytesOf := func(name string) []byte {
		if name == "manual" {
			return []byte(readTestFile(t, fixtures.Manual(".flac")))
		}
		return []byte(readTestFile(t, fixtures.Path(fixtures.RipperFLAC)))
	}
	write := func(st *store.Store, runID int64, path, before, after string) error {
		return st.RecordTagWrite(runID, path, `"`+before+`"`, `"`+after+`"`, func() error {
			return os.WriteFile(path, bytesOf(after), 0644)
		})
	}
	save = func(st *store.Store, runID int64, f store.File) error {
		return write(st, runID, f.Path, "ripper", "manual")
	}
	check = func(path, after string) error {
		if got, err := os.ReadFile(path); err != nil || !bytes.Equal(got, bytesOf(strings.Trim(after, `"`))) {
			return errors.New("its tags have changed since they were saved")
		}
		return nil
	}
	restore = func(st *store.Store, runID int64, path, before, after string) error {
		return write(st, runID, path, strings.Trim(after, `"`), strings.Trim(before, `"`))
	}
	return save, check, restore
}

// execSQL changes the database of s behind its back, as only a test may.
func execSQL(t *testing.T, s *store.Store, query string, args ...any) {
	t.Helper()
	db, err := sql.Open("sqlite", s.Path()+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(query, args...); err != nil {
		t.Fatal(err)
	}
}

// A tag save of many files, each saved to and then moved into the library,
// is undone step by step in the reverse of the order it was done in, also
// when the clock gave every step the same time: the steps are numbered. The
// order used to go by the recorded times, and from eleven files on an undo
// restored a file's tags before it moved the file back, found the file "no
// longer where the run left it", and left the saved tags in it.
//
// A run recorded before the steps were numbered is still ordered by time,
// by a merge that keeps each file's save before its move.
func TestUndoOfATagSaveWhoseStepsShareOneTime(t *testing.T) {
	for _, numbered := range []bool{true, false} {
		const n = 30
		lib := filepath.Join(t.TempDir(), "lib")
		var paths []string
		for i := 0; i < n; i++ {
			p := filepath.Join(lib, "_Untagged", fmt.Sprintf("rip%02d", i), "ripped.flac")
			writeTestFile(t, p, readTestFile(t, fixtures.Path(fixtures.RipperFLAC)))
			paths = append(paths, p)
		}
		s := openStore(t)
		indexLibrary(t, s, lib)
		save, check, restore := recordedSaves(t)
		var ids []int64
		rows := indexedFiles(t, s)
		for _, p := range paths {
			ids = append(ids, rows[p].ID)
		}
		if finish := SaveTags(context.Background(), Options{Destination: lib, Store: s}, ids, save); finish.State != StateOK || finish.Counters.Organized != n {
			t.Fatalf("save tags = %+v", finish)
		}
		saved := lastRun(t, s)
		const instant = "2026-10-08T12:00:00Z"
		execSQL(t, s, `UPDATE moves SET moved_at = ? WHERE run_id = ?`, instant, saved.ID)
		execSQL(t, s, `UPDATE tag_writes SET written_at = ? WHERE run_id = ?`, instant, saved.ID)
		if !numbered {
			execSQL(t, s, `UPDATE moves SET seq = NULL WHERE run_id = ?`, saved.ID)
			execSQL(t, s, `UPDATE tag_writes SET seq = NULL WHERE run_id = ?`, saved.ID)
		}

		plan, err := PlanUndo(s, saved.ID, check)
		if err != nil || plan.Moves != n || plan.TagSaves != n || len(plan.Skipped) != 0 {
			t.Fatalf("numbered %t: plan = %d moves, %d tag saves, skipped %+v, %v; want all %d of each", numbered, plan.Moves, plan.TagSaves, plan.Skipped, err, n)
		}
		undo := Undo(context.Background(), Options{Destination: lib, Store: s}, saved.ID, check, restore)
		if undo.State != StateOK || undo.NotUndone != "" {
			t.Errorf("numbered %t: undo = %+v", numbered, undo)
		}
		for _, p := range paths {
			if got := readTestFile(t, p); got != readTestFile(t, fixtures.Path(fixtures.RipperFLAC)) {
				t.Fatalf("numbered %t: %s is back without the tags it had before the save", numbered, p)
			}
		}
	}
}

// tagSaveRun builds the record of a run that saved and then moved n files,
// with times from next, and numbered or not.
func tagSaveRun(n int, numbered bool, next func() time.Time) (moves, writes []undoOp) {
	seq := 0
	number := func() int {
		if !numbered {
			return 0
		}
		seq++
		return seq
	}
	for i := 0; i < n; i++ {
		p, q := fmt.Sprintf(`C:\lib\_Untagged\%d.flac`, i), fmt.Sprintf(`C:\lib\Artist\Album\%d.flac`, i)
		writes = append(writes, undoOp{write: &store.TagWrite{Path: p}, at: next(), order: number()})
		moves = append(moves, undoOp{move: &store.Move{From: p, To: q}, at: next(), order: number()})
	}
	return moves, writes
}

// undoOfTagSaveRun is the record of the undo of such a run: each file moved
// back and then given its tags back, the last file first.
func undoOfTagSaveRun(n int, next func() time.Time) (moves, writes []undoOp) {
	for i := n - 1; i >= 0; i-- {
		p, q := fmt.Sprintf(`C:\lib\_Untagged\%d.flac`, i), fmt.Sprintf(`C:\lib\Artist\Album\%d.flac`, i)
		moves = append(moves, undoOp{move: &store.Move{From: q, To: p}, at: next()})
		writes = append(writes, undoOp{write: &store.TagWrite{Path: p}, at: next()})
	}
	return moves, writes
}

// outOfOrder returns the first file whose save and move ops has in another
// order than saveFirst says, or -1.
func outOfOrder(n int, ops []undoOp, saveFirst bool) int {
	saved, moved := map[string]int{}, map[string]int{}
	for i, op := range ops {
		if op.move != nil {
			moved[op.move.From], moved[op.move.To] = i, i
		} else {
			saved[op.write.Path] = i
		}
	}
	for i := 0; i < n; i++ {
		p := fmt.Sprintf(`C:\lib\_Untagged\%d.flac`, i)
		if (saved[p] < moved[p]) != saveFirst {
			return i
		}
	}
	return -1
}

// The order of a run's steps does not depend on the clock: not when every
// step carries one time, not when the clock was set back during the run.
func TestInOrder(t *testing.T) {
	at := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	clocks := map[string]func() func() time.Time{
		"one instant": func() func() time.Time { return func() time.Time { return at } },
		"two steps to a tick": func() func() time.Time {
			step := 0
			return func() time.Time { step++; return at.Add(time.Duration(step/2) * 16 * time.Millisecond) }
		},
		"three steps to a tick": func() func() time.Time {
			step := 0
			return func() time.Time { step++; return at.Add(time.Duration(step/3) * 16 * time.Millisecond) }
		},
		"every step its own time": func() func() time.Time {
			step := 0
			return func() time.Time { step++; return at.Add(time.Duration(step) * time.Millisecond) }
		},
	}
	for name, clock := range clocks {
		for n := 1; n <= 40; n++ {
			// Numbered: exact, whatever the times.
			moves, writes := tagSaveRun(n, true, clock())
			ops := inOrder(moves, writes)
			for i, op := range ops {
				if op.order != i+1 {
					t.Fatalf("%s, %d files, numbered: step %d of the order has number %d", name, n, i+1, op.order)
				}
			}
			// Not numbered: each file's save before its move, and in the
			// undo of that run each move before its save.
			moves, writes = tagSaveRun(n, false, clock())
			if i := outOfOrder(n, inOrder(moves, writes), true); i >= 0 {
				t.Fatalf("%s, %d files, not numbered: file %d is moved before its tags are saved", name, n, i)
			}
			moves, writes = undoOfTagSaveRun(n, clock())
			if i := outOfOrder(n, inOrder(moves, writes), false); i >= 0 {
				t.Fatalf("%s, %d files, not numbered: file %d has its tags restored before it is moved back", name, n, i)
			}
		}
	}

	// A clock set back in the middle of a numbered run changes nothing.
	back := 0
	moves, writes := tagSaveRun(12, true, func() time.Time { back++; return at.Add(-time.Duration(back) * time.Second) })
	for i, op := range inOrder(moves, writes) {
		if op.order != i+1 {
			t.Fatalf("with the clock running backwards, step %d of the order has number %d", i+1, op.order)
		}
	}
}
