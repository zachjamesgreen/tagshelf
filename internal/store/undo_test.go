package store

import (
	"errors"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"
)

// These are the store's side of undo: the functions internal/engine's undo is
// built on, each with the rule it is relied on for.

func TestRunReadsOneRun(t *testing.T) {
	s := openMemory(t)
	start := time.Date(2026, 9, 28, 10, 0, 0, 0, time.UTC)
	first, err := s.StartRun(KindOrganize, false, `C:\src`, `C:\lib`, start)
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.StartRun(KindReorganize, true, `C:\lib`, `C:\lib`, start.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	c := Counters{TotalFound: 1, Organized: 2, Untagged: 3, Duplicates: 4, InPlace: 5, Errors: 6, OtherFound: 7, OtherMoved: 8, LeftInPlace: 9, Retried: 10, CueRewritten: 11, Indexed: 12}
	if err := s.FinishRun(first, c, StateErrors, start.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}

	got, err := s.Run(first)
	want := Run{ID: first, Started: start, Ended: start.Add(time.Minute), Kind: KindOrganize, Source: `C:\src`, Destination: `C:\lib`, Counters: c, State: StateErrors}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Errorf("Run(%d) = %+v, %v; want %+v", first, got, err, want)
	}
	if got, err := s.Run(second); err != nil || got.ID != second || got.Kind != KindReorganize || !got.DryRun || got.State != StateRunning || !got.Ended.IsZero() {
		t.Errorf("Run(%d) = %+v, %v; want the dry reorganize, still running", second, got, err)
	}
	if got, err := s.Run(second + 1); err == nil {
		t.Errorf("Run of a run that does not exist = %+v, want an error", got)
	}
}

func TestMarkUndone(t *testing.T) {
	s := openMemory(t)
	now := time.Now()
	var ids []int64
	for _, kind := range []RunKind{KindOrganize, KindOrganize, KindUndo} {
		id, err := s.StartRun(kind, false, "s", "d", now)
		if err != nil {
			t.Fatal(err)
		}
		if err := s.FinishRun(id, Counters{Organized: 2}, StateOK, now); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	undone, other, undo := ids[0], ids[1], ids[2]

	if err := s.MarkUndone(undone, undo); err != nil {
		t.Fatal(err)
	}
	if r, err := s.Run(undone); err != nil || r.State != StateUndone || r.UndoneBy != undo || r.Counters.Organized != 2 || r.Ended.IsZero() {
		t.Errorf("the undone run = %+v, %v; want it undone by run %d and otherwise as it was", r, err, undo)
	}
	for _, id := range []int64{other, undo} {
		if r, err := s.Run(id); err != nil || r.State != StateOK || r.UndoneBy != 0 {
			t.Errorf("run %d = %+v, %v; want it left ok and not undone", id, r, err)
		}
	}

	if err := s.MarkUndone(undo+1, undo); err == nil || !strings.Contains(err.Error(), "no such run") {
		t.Errorf("MarkUndone of a run that does not exist: %v, want it refused", err)
	}
	if err := s.MarkUndone(other, undo+1); err == nil {
		t.Error("MarkUndone by an undo run that does not exist was accepted")
	}
	if r, err := s.Run(other); err != nil || r.State != StateOK {
		t.Errorf("run %d = %+v, %v; want it left ok by the refused call", other, r, err)
	}
}

func TestMovesAfter(t *testing.T) {
	s := openMemory(t)
	first, err := s.StartRun(KindOrganize, false, "s", "d", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.StartRun(KindReorganize, false, "d", "d", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range []struct {
		run      int64
		from, to string
		reason   MoveReason
	}{
		{first, "a", "b", ReasonOrganized},
		{first, "x.cue", "x.cue.orig", ReasonCueRewrite},
		{first, "c", "d", ReasonUntagged},
		{second, "b", "e", ReasonRenamed},
		{second, "y.cue", "y.cue.orig", ReasonCueRewrite},
		{second, "d", "f", ReasonDuplicate},
	} {
		if err := s.RecordMove(m.run, m.from, m.to, m.reason); err != nil {
			t.Fatal(err)
		}
	}
	firstMoves, err := s.Moves(first)
	if err != nil || len(firstMoves) != 3 {
		t.Fatalf("moves of the first run = %+v, %v", firstMoves, err)
	}

	froms := func(after int64) []string {
		t.Helper()
		moves, err := s.MovesAfter(after)
		if err != nil {
			t.Fatal(err)
		}
		var got []string
		last := after
		for _, m := range moves {
			if m.ID <= last {
				t.Errorf("MovesAfter(%d) lists move %d after move %d: not later moves in order", after, m.ID, last)
			}
			last = m.ID
			got = append(got, m.From+">"+m.To+" "+string(m.Reason))
		}
		return got
	}
	// By any run, in order, without the given move itself and without the cue
	// sheets' originals, which are not files that moved.
	if got, want := froms(firstMoves[0].ID), []string{"c>d untagged", "b>e renamed", "d>f duplicate"}; !reflect.DeepEqual(got, want) {
		t.Errorf("MovesAfter(the first move) = %v, want %v", got, want)
	}
	if got, want := froms(firstMoves[2].ID), []string{"b>e renamed", "d>f duplicate"}; !reflect.DeepEqual(got, want) {
		t.Errorf("MovesAfter(the first run's last move) = %v, want %v", got, want)
	}
	if got, want := froms(0), []string{"a>b organized", "c>d untagged", "b>e renamed", "d>f duplicate"}; !reflect.DeepEqual(got, want) {
		t.Errorf("MovesAfter(0) = %v, want %v", got, want)
	}
	secondMoves, err := s.Moves(second)
	if err != nil || len(secondMoves) != 3 {
		t.Fatalf("moves of the second run = %+v, %v", secondMoves, err)
	}
	if got := froms(secondMoves[2].ID); len(got) != 0 {
		t.Errorf("MovesAfter(the last move) = %v, want none", got)
	}
}

func TestWithdrawReviews(t *testing.T) {
	s := openMemory(t)
	lib := setRoot(t, s, RootLibrary, filepath.FromSlash("/m"))
	putFiles(t, s,
		File{RootID: lib, RelPath: "a1.mp3", Sum: "a"}, File{RootID: lib, RelPath: "a2.mp3", Sum: "a"},
		File{RootID: lib, RelPath: "b1.mp3", Sum: "b"}, File{RootID: lib, RelPath: "b2.mp3", Sum: "b"},
		File{RootID: lib, RelPath: "c1.mp3", Sum: "c"}, File{RootID: lib, RelPath: "c2.mp3", Sum: "c"},
		File{RootID: lib, RelPath: "d1.mp3", Sum: "d"}, File{RootID: lib, RelPath: "d2.mp3", Sum: "d"},
	)
	groups, err := s.Groups(testKey)
	if err != nil || len(groups) != 4 {
		t.Fatalf("groups = %+v, %v; want four", groups, err)
	}
	run, err := s.StartRun(KindTUIAction, false, "", "d", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	other, err := s.StartRun(KindTUIAction, false, "", "d", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	// Two decisions the run carried out, one another run did, one no run did.
	for i, by := range []int64{run, run, other, 0} {
		if err := s.RecordReview(Review{Kind: groups[i].Kind, Members: groups[i].IDs(), Outcome: OutcomeKeepBoth, By: ByUser, RunID: by}); err != nil {
			t.Fatal(err)
		}
	}
	if n, err := s.GroupsNeedingReview(testKey); err != nil || n != 0 {
		t.Fatalf("groups needing review = %d, %v; want none", n, err)
	}

	if n, err := s.WithdrawReviews(run); err != nil || n != 2 {
		t.Errorf("WithdrawReviews = %d, %v; want the run's 2", n, err)
	}
	waiting, err := s.GroupsToReview(testKey)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := groupNames(waiting), []string{"exact: a1.mp3 a2.mp3", "exact: b1.mp3 b2.mp3"}; !reflect.DeepEqual(got, want) {
		t.Errorf("groups to review = %q, want the run's two: %q", got, want)
	}
	reviews, err := s.Reviews()
	if err != nil || len(reviews) != 2 || reviews[0].RunID != other || reviews[1].RunID != 0 {
		t.Errorf("reviews left = %+v, %v; want the other run's and the one no run carried out", reviews, err)
	}
	if n, err := s.WithdrawReviews(run); err != nil || n != 0 {
		t.Errorf("a second WithdrawReviews = %d, %v; want nothing left to withdraw", n, err)
	}
}

func TestFileAt(t *testing.T) {
	s := openMemory(t)
	lib := filepath.Join(t.TempDir(), "lib")
	libID := setRoot(t, s, RootLibrary, lib)
	untaggedID := setRoot(t, s, RootUntagged, filepath.Join(lib, "_Untagged"))
	putFiles(t, s,
		File{RootID: libID, RelPath: filepath.Join("Singer", "Album", "01 - Song.mp3"), Title: "Song", Sum: "s", Size: 7, ModTime: time.Unix(5, 0)},
		File{RootID: untaggedID, RelPath: "loose.mp3", Title: "Loose"},
		// A row left under the outer root for a path the inner root covers.
		File{RootID: libID, RelPath: filepath.Join("_Untagged", "loose.mp3"), Title: "Stale"},
	)

	song := filepath.Join(lib, "Singer", "Album", "01 - Song.mp3")
	f, ok, err := s.FileAt(song)
	if err != nil || !ok || f.Title != "Song" || f.Path != song || f.RootKind != RootLibrary || f.Sum != "s" || f.Size != 7 {
		t.Errorf("FileAt(song) = %+v, %t, %v; want the song's whole row", f, ok, err)
	}
	// The innermost root holding the path is the one asked.
	if f, ok, err := s.FileAt(filepath.Join(lib, "_Untagged", "loose.mp3")); err != nil || !ok || f.Title != "Loose" || f.RootKind != RootUntagged {
		t.Errorf("FileAt(loose) = %+v, %t, %v; want the untagged folder's row", f, ok, err)
	}
	// Not cleaned yet, as paths from a move log may be.
	if f, ok, err := s.FileAt(filepath.Join(lib, "Singer", ".", "Album", "..", "Album", "01 - Song.mp3")); err != nil || !ok || f.Title != "Song" {
		t.Errorf("FileAt(an unclean path) = %+v, %t, %v; want the song", f, ok, err)
	}
	for name, path := range map[string]string{
		"a file with no row":   filepath.Join(lib, "Singer", "Album", "02 - Other.mp3"),
		"a folder":             filepath.Join(lib, "Singer", "Album"),
		"the root itself":      lib,
		"a path outside roots": filepath.Join(filepath.Dir(lib), "elsewhere", "01 - Song.mp3"),
	} {
		if f, ok, err := s.FileAt(path); err != nil || ok {
			t.Errorf("FileAt(%s) = %+v, %t, %v; want no row", name, f, ok, err)
		}
	}

	upper := strings.ToUpper(song)
	f, ok, err = s.FileAt(upper)
	if err != nil {
		t.Fatal(err)
	}
	if windows := runtime.GOOS == "windows"; ok != windows {
		t.Errorf("FileAt(%q) found = %t, want %t: letter case is ignored on Windows only", upper, ok, windows)
	} else if ok && f.Path != song {
		t.Errorf("FileAt(%q) = %q, want the row with its own spelling %q", upper, f.Path, song)
	}
}

// TestRecordTagWriteIsOnRecordBeforeTheSave checks the row is committed when
// the save itself is made, so that no failure after the file was replaced can
// leave a save undo does not know of.
func TestRecordTagWriteIsOnRecordBeforeTheSave(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tagshelf.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	// What another connection sees is what is committed.
	other, err := OpenReader(path)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	id, err := s.StartRun(KindTUIAction, false, "", `C:\lib`, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	recorded := func() int {
		t.Helper()
		writes, err := other.TagWrites(id)
		if err != nil {
			t.Fatal(err)
		}
		return len(writes)
	}

	during := -1
	if err := s.RecordTagWrite(id, `C:\lib\a.flac`, `{}`, `{"title":["New"]}`, func() error {
		during = recorded()
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if during != 1 {
		t.Errorf("%d tag saves were on record while the file was being replaced, want 1", during)
	}
	if got := recorded(); got != 1 {
		t.Errorf("%d tag saves on record after the save, want 1", got)
	}

	// A save that fails is taken back off the record.
	if err := s.RecordTagWrite(id, `C:\lib\b.flac`, `{}`, `{}`, func() error { return errSaveFailed }); err != errSaveFailed {
		t.Errorf("RecordTagWrite with a failing save = %v, want the save's own error", err)
	}
	if got := recorded(); got != 1 {
		t.Errorf("%d tag saves on record after a failed save, want still 1", got)
	}
}

var errSaveFailed = errors.New("the save failed")

// TestGroupsWithoutAHashAreNotExact checks that files joined by their release
// track are an exact group only when a hash says their audio is the same:
// not when none could be hashed, and not when none has any audio.
func TestGroupsWithoutAHashAreNotExact(t *testing.T) {
	s := openMemory(t)
	lib := setRoot(t, s, RootLibrary, filepath.FromSlash("/m"))
	putFiles(t, s,
		File{RootID: lib, RelPath: "damaged.mp3", ReleaseTrackID: "t1", ReadError: "cannot hash"},
		File{RootID: lib, RelPath: "damaged.flac", ReleaseTrackID: "t1", ReadError: "cannot hash"},
		File{RootID: lib, RelPath: "silent.mp3", Sum: emptyAudioSum, ReleaseTrackID: "t2"},
		File{RootID: lib, RelPath: "silent.flac", Sum: emptyAudioSum, ReleaseTrackID: "t2"},
		File{RootID: lib, RelPath: "same.mp3", Sum: "x", ReleaseTrackID: "t3"},
		File{RootID: lib, RelPath: "same (1).mp3", Sum: "x", ReleaseTrackID: "t3"},
		File{RootID: lib, RelPath: "one hashed.mp3", Sum: "y", ReleaseTrackID: "t4"},
		File{RootID: lib, RelPath: "one hashed.flac", ReleaseTrackID: "t4"},
	)
	groups, err := s.Groups(testKey)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"release-track: damaged.mp3 damaged.flac",
		"release-track: silent.mp3 silent.flac",
		"exact: same.mp3 same (1).mp3",
		"release-track: one hashed.mp3 one hashed.flac",
	}
	if got := groupNames(groups); !reflect.DeepEqual(got, want) {
		t.Errorf("Groups = %q, want %q", got, want)
	}
	// What a run resolves on its own agrees: only the hashed pair.
	exact, err := s.ExactGroups()
	if err != nil || len(exact) != 1 || exact[0][0].RelPath != "same.mp3" {
		t.Errorf("ExactGroups = %+v, %v; want only the pair with the same hash", exact, err)
	}
}
