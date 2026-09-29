package store

import (
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestExactGroups(t *testing.T) {
	s := openMemory(t)
	lib := setRoot(t, s, RootLibrary, filepath.FromSlash("/m"))
	src := setRoot(t, s, RootSource, filepath.FromSlash("/in"))
	putFiles(t, s,
		File{RootID: lib, RelPath: "a.mp3", Sum: "x"},
		File{RootID: lib, RelPath: "b.mp3", Sum: "y"},
		File{RootID: src, RelPath: "c.mp3", Sum: "x"},
		File{RootID: lib, RelPath: "d.mp3"}, // not hashed
		File{RootID: src, RelPath: "e.mp3"},
		File{RootID: src, RelPath: "f.mp3", Sum: "z"},
		File{RootID: lib, RelPath: "g.mp3", Sum: "z"},
		File{RootID: lib, RelPath: "h.mp3", Sum: "z"},
		File{RootID: lib, RelPath: "i.mp3", Sum: emptyAudioSum}, // no audio
		File{RootID: src, RelPath: "j.mp3", Sum: emptyAudioSum},
	)
	groups, err := s.ExactGroups()
	if err != nil {
		t.Fatal(err)
	}
	var got [][]string
	for _, g := range groups {
		var names []string
		for _, f := range g {
			names = append(names, f.RelPath)
		}
		got = append(got, names)
	}
	want := [][]string{{"a.mp3", "c.mp3"}, {"f.mp3", "g.mp3", "h.mp3"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ExactGroups = %v, want %v", got, want)
	}
	if groups[0][1].Path != filepath.Join(filepath.FromSlash("/in"), "c.mp3") {
		t.Errorf("member path = %q", groups[0][1].Path)
	}
}

func TestReviewsCoverSubsets(t *testing.T) {
	s := openMemory(t)
	run, err := s.StartRun(KindOrganize, false, "s", "d", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	r := Review{Kind: GroupExact, Members: []int64{7, 3, 5}, Keeper: 5, Outcome: OutcomeResolved, By: ByAuto, RunID: run}
	if err := s.RecordReview(r); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordReview(Review{Kind: GroupPossible, Members: []int64{1, 2}, Outcome: OutcomeKeepBoth, By: ByUser}); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		kind    GroupKind
		members []int64
		want    bool
	}{
		{GroupExact, []int64{3, 5, 7}, true},
		{GroupExact, []int64{7, 3}, true},
		{GroupExact, []int64{3, 5, 8}, false}, // a new member reopens it
		{GroupReleaseTrack, []int64{3, 5}, false},
		{GroupPossible, []int64{2, 1}, true},
	}
	for _, tt := range tests {
		got, err := s.Reviewed(tt.kind, tt.members)
		if err != nil {
			t.Fatal(err)
		}
		if got != tt.want {
			t.Errorf("Reviewed(%s, %v) = %v, want %v", tt.kind, tt.members, got, tt.want)
		}
	}

	reviews, err := s.Reviews()
	if err != nil {
		t.Fatal(err)
	}
	if len(reviews) != 2 {
		t.Fatalf("Reviews = %+v, want 2", reviews)
	}
	got := reviews[0]
	if got.At.IsZero() {
		t.Error("review has no time")
	}
	got.At, got.ID = time.Time{}, 0
	r.Members = []int64{3, 5, 7}
	if !reflect.DeepEqual(got, r) {
		t.Errorf("review = %+v, want %+v", got, r)
	}
	if reviews[1].Keeper != 0 || reviews[1].RunID != 0 {
		t.Errorf("keep-both review = %+v, want no keeper and no run", reviews[1])
	}
}

func TestExactGroupsNeedingReview(t *testing.T) {
	s := openMemory(t)
	lib := setRoot(t, s, RootLibrary, filepath.FromSlash("/m"))
	untagged := setRoot(t, s, RootUntagged, filepath.FromSlash("/m/_Untagged"))
	dups := setRoot(t, s, RootDuplicates, filepath.FromSlash("/m/_Duplicates"))
	src := setRoot(t, s, RootSource, filepath.FromSlash("/in"))
	putFiles(t, s,
		File{RootID: lib, RelPath: "a.mp3", Sum: "x"}, // needs review with b
		File{RootID: untagged, RelPath: "b.mp3", Sum: "x"},
		File{RootID: lib, RelPath: "c.mp3", Sum: "y"}, // only duplicates and source copies
		File{RootID: dups, RelPath: "c.mp3", Sum: "y"},
		File{RootID: src, RelPath: "c.mp3", Sum: "y"},
		File{RootID: lib, RelPath: "d.mp3", Sum: "z"}, // reviewed
		File{RootID: lib, RelPath: "e.mp3", Sum: "z"},
	)
	n, err := s.ExactGroupsNeedingReview()
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("before review: %d groups, want 2", n)
	}
	byPath := filesByPath(t, s)
	members := []int64{byPath[filepath.FromSlash("/m/d.mp3")].ID, byPath[filepath.FromSlash("/m/e.mp3")].ID}
	if err := s.RecordReview(Review{Kind: GroupExact, Members: members, Outcome: OutcomeKeepBoth, By: ByUser}); err != nil {
		t.Fatal(err)
	}
	if n, err = s.ExactGroupsNeedingReview(); err != nil || n != 1 {
		t.Errorf("after review: %d groups (%v), want 1", n, err)
	}
}

func TestDuplicatesMigrationKeepsSkippedCounts(t *testing.T) {
	i := slices.IndexFunc(migrations, func(m migration) bool { return strings.HasSuffix(m.name, "_duplicates.sql") })
	if i < 0 {
		t.Fatal("no duplicates migration")
	}
	path := filepath.Join(t.TempDir(), "tagshelf.db")
	old, err := open(path, migrations[:i])
	if err != nil {
		t.Fatal(err)
	}
	id, err := old.StartRun(KindOrganize, false, "s", "d", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := old.db.Exec(`UPDATE runs SET skipped = 4 WHERE id = ?`, id); err != nil {
		t.Fatal(err)
	}
	old.Close()

	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	runs, err := s.Runs()
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 1 || runs[0].Counters.Duplicates != 4 {
		t.Errorf("runs = %+v, want the old skipped count as duplicates", runs)
	}
}
