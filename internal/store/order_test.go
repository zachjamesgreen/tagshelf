package store

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// A run's moves and tag saves share one numbering, in the order they were
// made, each run from 1: undo goes by it, since their recorded times may be
// the same for several of them.
func TestMovesAndTagWritesShareOneSequence(t *testing.T) {
	s := openMemory(t)
	at := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	first, err := s.StartRun(KindTUIAction, false, "", `C:\lib`, at)
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.StartRun(KindTUIAction, false, "", `C:\lib`, at)
	if err != nil {
		t.Fatal(err)
	}
	save := func(run int64, path string) {
		t.Helper()
		if err := s.RecordTagWrite(run, path, `{}`, `{}`, func() error { return nil }); err != nil {
			t.Fatal(err)
		}
	}
	move := func(run int64, from, to string) {
		t.Helper()
		if err := s.RecordMove(run, from, to, ReasonOrganized); err != nil {
			t.Fatal(err)
		}
	}
	save(first, `C:\lib\a`)                                                                                     // 1
	move(first, `a`, `b`)                                                                                       // 2
	save(second, `C:\lib\x`)                                                                                    // 1 of the other run
	save(first, `C:\lib\c`)                                                                                     // 3
	move(first, `c`, `d`)                                                                                       // 4
	move(second, `x`, `y`)                                                                                      // 2 of the other run
	if err := s.RecordCueRewrite(first, `e.cue`, `e.cue.orig`, []byte("before"), []byte("after")); err != nil { // 5
		t.Fatal(err)
	}
	// A save that fails takes its row back; the number it had is not missed.
	if err := s.RecordTagWrite(first, `C:\lib\f`, `{}`, `{}`, func() error { return os.ErrPermission }); err == nil {
		t.Fatal("a failed save was recorded without an error")
	}
	save(first, `C:\lib\g`) // 6

	moves, err := s.Moves(first)
	if err != nil {
		t.Fatal(err)
	}
	writes, err := s.TagWrites(first)
	if err != nil {
		t.Fatal(err)
	}
	var moveSeqs, writeSeqs []int
	for _, m := range moves {
		moveSeqs = append(moveSeqs, m.Seq)
	}
	for _, w := range writes {
		writeSeqs = append(writeSeqs, w.Seq)
	}
	if want := []int{2, 4, 5}; !equalInts(moveSeqs, want) {
		t.Errorf("the first run's moves are numbered %v, want %v", moveSeqs, want)
	}
	if want := []int{1, 3, 6}; !equalInts(writeSeqs, want) {
		t.Errorf("the first run's tag saves are numbered %v, want %v", writeSeqs, want)
	}
	if string(moves[2].CueBefore) != "before" || string(moves[2].CueAfter) != "after" || moves[0].CueBefore != nil || moves[0].CueAfter != nil {
		t.Errorf("moves = %+v, want the cue rewrite alone to hold what the sheet held before and after", moves)
	}
	if other, err := s.Moves(second); err != nil || len(other) != 1 || other[0].Seq != 2 {
		t.Errorf("the second run's moves = %+v, %v; want its own numbering", other, err)
	}
}

func equalInts(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// Moves and tag saves recorded before they were numbered read as number 0
// after the upgrade, which undo takes for "order by time, as then".
func TestMovesFromBeforeTheSequenceHaveNone(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tagshelf.db")
	old, err := open(path, migrationsBefore(t, "_undo_order.sql"))
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	run, err := old.StartRun(KindReorganize, false, `C:\lib`, `C:\lib`, at)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := old.db.Exec(`INSERT INTO moves (run_id, from_path, to_path, reason, moved_at) VALUES (?, 'a.cue', 'a.cue.orig', 'cue-rewrite', ?)`, run, formatTime(at)); err != nil {
		t.Fatal(err)
	}
	if _, err := old.db.Exec(`INSERT INTO tag_writes (run_id, path, before, after, written_at) VALUES (?, 'a', '{}', '{}', ?)`, run, formatTime(at)); err != nil {
		t.Fatal(err)
	}
	old.Close()

	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	moves, err := s.Moves(run)
	if err != nil || len(moves) != 1 || moves[0].Seq != 0 || moves[0].CueBefore != nil || moves[0].CueAfter != nil {
		t.Errorf("moves after the upgrade = %+v, %v; want the old move without a number or cue text", moves, err)
	}
	writes, err := s.TagWrites(run)
	if err != nil || len(writes) != 1 || writes[0].Seq != 0 {
		t.Errorf("tag saves after the upgrade = %+v, %v; want the old save without a number", writes, err)
	}
	// What the run does from now on is numbered from 1.
	if err := s.RecordMove(run, `b`, `c`, ReasonRenamed); err != nil {
		t.Fatal(err)
	}
	if moves, err := s.Moves(run); err != nil || len(moves) != 2 || moves[1].Seq != 1 {
		t.Errorf("moves = %+v, %v", moves, err)
	}
}

// RecordMove gives the moved row the modification time the file has at its
// new path: a copy to a file system that keeps times less finely does not
// have the source's, and the row would no longer describe its file.
func TestRecordMoveTakesTheTimeOfTheMovedFile(t *testing.T) {
	s := openMemory(t)
	lib := t.TempDir()
	root := setRoot(t, s, RootLibrary, lib)
	from, to := filepath.Join(lib, "in", "a.mp3"), filepath.Join(lib, "A", "a.mp3")
	for _, dir := range []string{filepath.Dir(from), filepath.Dir(to)} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(to, []byte("12345"), 0o644); err != nil {
		t.Fatal(err)
	}
	// What the source had, and what the destination's file system kept.
	fine := time.Date(2019, 3, 14, 9, 26, 53, 123456700, time.UTC)
	coarse := fine.Truncate(2 * time.Second)
	if err := os.Chtimes(to, coarse, coarse); err != nil {
		t.Fatal(err)
	}
	putFiles(t, s, File{RootID: root, RelPath: filepath.Join("in", "a.mp3"), Sum: "a", Size: 5, ModTime: fine})
	run, err := s.StartRun(KindReorganize, false, lib, lib, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := s.RecordMove(run, from, to, ReasonOrganized); err != nil {
		t.Fatal(err)
	}
	row, ok, err := s.FileAt(to)
	if err != nil || !ok || !row.ModTime.Equal(coarse) || row.Sum != "a" {
		t.Errorf("row of the moved file = %+v, %t, %v; want it with the time %v its file has", row, ok, err, coarse)
	}

	// A file of another size is not the one the row describes: the row keeps
	// its time, and the next walk reads the file again.
	if err := os.WriteFile(from, []byte("another size"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordMove(run, to, from, ReasonRenamed); err != nil {
		t.Fatal(err)
	}
	if row, ok, err := s.FileAt(from); err != nil || !ok || !row.ModTime.Equal(coarse) {
		t.Errorf("row moved to a file of another size = %+v, %t, %v; want its time left alone", row, ok, err)
	}
	// And a destination that is not there, as in a test of the store alone,
	// leaves the row's time as it was.
	gone := filepath.Join(lib, "nowhere.mp3")
	if err := s.RecordMove(run, from, gone, ReasonRenamed); err != nil {
		t.Fatal(err)
	}
	if row, ok, err := s.FileAt(gone); err != nil || !ok || !row.ModTime.Equal(coarse) {
		t.Errorf("row moved to a path with no file = %+v, %t, %v", row, ok, err)
	}
}

// ReviewedTogether tells a decision about copies that are still side by side
// from a resolution whose other copies have gone to the duplicates folder.
func TestReviewedTogether(t *testing.T) {
	s := openMemory(t)
	record := func(kind GroupKind, outcome ReviewOutcome, members ...int64) {
		t.Helper()
		if err := s.RecordReview(Review{Kind: kind, Members: members, Outcome: outcome, By: ByUser}); err != nil {
			t.Fatal(err)
		}
	}
	record(GroupExact, OutcomeKeepBoth, 1, 2)
	record(GroupExact, OutcomeResolved, 5, 6)
	record(GroupReleaseTrack, OutcomeKeepBoth, 8, 9)
	for _, tt := range []struct {
		name    string
		members []int64
		want    bool
	}{
		{"the two copies kept", []int64{1, 2}, true},
		{"the two copies kept and a third", []int64{1, 2, 3}, true},
		{"one of them and a newcomer", []int64{1, 3}, false},
		{"the keeper of a resolved group and a newcomer", []int64{5, 7}, false},
		{"files of a review of another kind", []int64{8, 9}, false},
		{"files no review has seen", []int64{3, 4}, false},
	} {
		if got, err := s.ReviewedTogether(GroupExact, tt.members); err != nil || got != tt.want {
			t.Errorf("%s: ReviewedTogether = %t, %v; want %t", tt.name, got, err, tt.want)
		}
	}
}

// IndexedBefore counts only runs that walked the library to the end.
func TestIndexedBefore(t *testing.T) {
	s := openMemory(t)
	at := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	start := func(kind RunKind) int64 {
		t.Helper()
		id, err := s.StartRun(kind, false, "", `C:\lib`, at)
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	finish := func(id int64, state RunState) {
		t.Helper()
		if err := s.FinishRun(id, Counters{}, state, at); err != nil {
			t.Fatal(err)
		}
	}
	before := func(id int64) bool {
		t.Helper()
		got, err := s.IndexedBefore(id)
		if err != nil {
			t.Fatal(err)
		}
		return got
	}

	first := start(KindIndex)
	if before(first) {
		t.Error("the first run has a run before it")
	}
	finish(first, StateInterrupted)
	second := start(KindReorganize)
	if before(second) {
		t.Error("an interrupted index counts as one that walked the library to the end")
	}
	if err := s.markCrashed(); err != nil {
		t.Fatal(err)
	}
	action := start(KindTUIAction)
	finish(action, StateOK)
	third := start(KindOrganize)
	if before(third) {
		t.Error("a crashed reorganize or a TUI action counts as a walk of the library")
	}
	finish(third, StateErrors)
	fourth := start(KindIndex)
	if !before(fourth) {
		t.Error("an organize that ended, with errors, does not count")
	}
	if before(third) {
		t.Error("a run counts as being before itself")
	}
}

// has_art is what the index recorded; for a row from before it did, the
// row's tags are searched for a picture as they used to be.
func TestHasArt(t *testing.T) {
	s := openMemory(t)
	root := setRoot(t, s, RootLibrary, filepath.FromSlash("/m"))
	picture := `{"APIC":{"picture":"Cover (front)","mime":"image/png","bytes":10},"title":"x"}`
	putFiles(t, s,
		File{RootID: root, RelPath: "art.flac", HasArt: true},
		File{RootID: root, RelPath: "plain.flac"},
		// Tags that describe a picture do not overrule what was recorded.
		File{RootID: root, RelPath: "recorded.mp3", TagsJSON: picture},
		File{RootID: root, RelPath: "old-art.mp3", TagsJSON: picture},
		File{RootID: root, RelPath: "old-plain.mp3", TagsJSON: `{"title":"a \"picture\" of you","comment":{"bytes":5}}`},
	)
	if _, err := s.db.Exec(`UPDATE files SET has_art = NULL WHERE rel_path LIKE 'old-%'`); err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{"art.flac": true, "plain.flac": false, "recorded.mp3": false, "old-art.mp3": true, "old-plain.mp3": false}
	files, err := s.Files()
	if err != nil || len(files) != len(want) {
		t.Fatalf("files = %+v, %v", files, err)
	}
	for _, f := range files {
		if f.HasArt != want[f.RelPath] {
			t.Errorf("%s: HasArt = %t, want %t", f.RelPath, f.HasArt, want[f.RelPath])
		}
	}
	stats, err := s.RootFiles(root)
	if err != nil {
		t.Fatal(err)
	}
	if got := stats[PathKey("art.flac")]; got.Sum != "" {
		t.Errorf("RootFiles of a file without a hash = %+v, want an empty Sum", got)
	}
}
