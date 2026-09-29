package store

import (
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
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

func TestGroupsNeedingReviewScope(t *testing.T) {
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
	n, err := s.GroupsNeedingReview(testKey)
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
	if n, err = s.GroupsNeedingReview(testKey); err != nil || n != 1 {
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

// testKey stands in for rules.PossibleKey, which store cannot import.
func testKey(f File) string {
	if f.Artist == "" || f.Album == "" || f.Title == "" {
		return ""
	}
	return strings.ToLower(f.Artist + "|" + f.Album + "|" + strconv.Itoa(f.Track) + "|" + f.Title)
}

func groupNames(groups []Group) []string {
	var got []string
	for _, g := range groups {
		var names []string
		for _, f := range g.Files {
			names = append(names, f.RelPath)
		}
		var also []string
		for _, f := range g.AlsoOn {
			also = append(also, f.RelPath)
		}
		s := string(g.Kind) + ": " + strings.Join(names, " ")
		if len(also) > 0 {
			s += " / also on: " + strings.Join(also, " ")
		}
		got = append(got, s)
	}
	return got
}

func TestGroups(t *testing.T) {
	s := openMemory(t)
	lib := setRoot(t, s, RootLibrary, filepath.FromSlash("/m"))
	untagged := setRoot(t, s, RootUntagged, filepath.FromSlash("/m/_Untagged"))
	dups := setRoot(t, s, RootDuplicates, filepath.FromSlash("/m/_Duplicates"))
	src := setRoot(t, s, RootSource, filepath.FromSlash("/in"))
	putFiles(t, s,
		// One release track as FLAC and MP3, and an exact copy of the MP3:
		// one confirmed group, joined through the MP3.
		File{RootID: lib, RelPath: "rt.flac", Sum: "f", ReleaseID: "r1", ReleaseTrackID: "t1", RecordingID: "rec1"},
		File{RootID: lib, RelPath: "rt.mp3", Sum: "m", ReleaseID: "r1", ReleaseTrackID: "t1", RecordingID: "rec1"},
		File{RootID: untagged, RelPath: "rt copy.mp3", Sum: "m"},
		// The same recording on a compilation: also on, not a duplicate.
		File{RootID: lib, RelPath: "best of.mp3", Sum: "b", ReleaseID: "r2", ReleaseTrackID: "t9", RecordingID: "rec1"},
		// A plain exact pair.
		File{RootID: lib, RelPath: "x1.mp3", Sum: "x"},
		File{RootID: lib, RelPath: "x2.mp3", Sum: "x"},
		// Hand-tagged copies with no IDs and different audio: possible.
		File{RootID: lib, RelPath: "p1.mp3", Sum: "p1", Artist: "Singer", Album: "Album", Track: 3, Title: "Song"},
		File{RootID: untagged, RelPath: "p2.flac", Sum: "p2", Artist: "SINGER", Album: "album", Track: 3, Title: "song"},
		// Matching tags, but with IDs: not possible, and not a release-track match either.
		File{RootID: lib, RelPath: "q1.mp3", Sum: "q1", Artist: "Singer", Album: "Album", Track: 4, Title: "Other", ReleaseTrackID: "t4"},
		File{RootID: lib, RelPath: "q2.mp3", Sum: "q2", Artist: "Singer", Album: "Album", Track: 4, Title: "Other"},
		// Copies only in the duplicates folder or a source are not the collection's.
		File{RootID: lib, RelPath: "d.mp3", Sum: "d", ReleaseTrackID: "t7"},
		File{RootID: dups, RelPath: "d.mp3", Sum: "d", ReleaseTrackID: "t7"},
		File{RootID: src, RelPath: "d.flac", Sum: "d2", ReleaseTrackID: "t7"},
	)

	groups, err := s.Groups(testKey)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"release-track: rt.flac rt.mp3 rt copy.mp3 / also on: best of.mp3",
		"exact: x1.mp3 x2.mp3",
		"possible: p1.mp3 p2.flac",
	}
	if got := groupNames(groups); !reflect.DeepEqual(got, want) {
		t.Errorf("Groups =\n%q\nwant\n%q", got, want)
	}
}

// A possible group whose files are already one confirmed group adds nothing.
func TestPossibleGroupInsideConfirmedOne(t *testing.T) {
	s := openMemory(t)
	lib := setRoot(t, s, RootLibrary, filepath.FromSlash("/m"))
	putFiles(t, s,
		File{RootID: lib, RelPath: "a.mp3", Sum: "x", Artist: "Singer", Album: "Album", Track: 1, Title: "Song"},
		File{RootID: lib, RelPath: "b.mp3", Sum: "x", Artist: "Singer", Album: "Album", Track: 1, Title: "Song"},
	)
	groups, err := s.Groups(testKey)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := groupNames(groups), []string{"exact: a.mp3 b.mp3"}; !reflect.DeepEqual(got, want) {
		t.Errorf("Groups = %q, want %q", got, want)
	}
}

func TestKeepBothReopensWithNewMember(t *testing.T) {
	s := openMemory(t)
	lib := setRoot(t, s, RootLibrary, filepath.FromSlash("/m"))
	putFiles(t, s,
		File{RootID: lib, RelPath: "a.flac", Sum: "a", ReleaseTrackID: "t1"},
		File{RootID: lib, RelPath: "a.mp3", Sum: "b", ReleaseTrackID: "t1"},
		File{RootID: lib, RelPath: "p1.mp3", Sum: "p1", Artist: "Singer", Album: "Album", Track: 1, Title: "Song"},
		File{RootID: lib, RelPath: "p2.mp3", Sum: "p2", Artist: "Singer", Album: "Album", Track: 1, Title: "Song"},
	)
	needing := func() int {
		t.Helper()
		n, err := s.GroupsNeedingReview(testKey)
		if err != nil {
			t.Fatal(err)
		}
		return n
	}
	if n := needing(); n != 2 {
		t.Fatalf("before review: %d groups need review, want 2", n)
	}

	groups, err := s.Groups(testKey)
	if err != nil {
		t.Fatal(err)
	}
	for _, g := range groups {
		if err := s.RecordReview(Review{Kind: g.Kind, Members: g.IDs(), Outcome: OutcomeKeepBoth, By: ByUser}); err != nil {
			t.Fatal(err)
		}
	}
	if n := needing(); n != 0 {
		t.Fatalf("after keep both: %d groups need review, want 0", n)
	}

	// A third copy of the release track reopens that group only.
	putFiles(t, s, File{RootID: lib, RelPath: "a (1).mp3", Sum: "c", ReleaseTrackID: "t1"})
	if n := needing(); n != 1 {
		t.Errorf("after a new member: %d groups need review, want 1", n)
	}
}

// A possible group may share files with a confirmed one: the exact pair still
// needs resolving, and the third copy still needs a person to look at it.
func TestPossibleGroupOverlappingConfirmedOne(t *testing.T) {
	s := openMemory(t)
	lib := setRoot(t, s, RootLibrary, filepath.FromSlash("/m"))
	putFiles(t, s,
		File{RootID: lib, RelPath: "a.mp3", Sum: "x", Artist: "Singer", Album: "Album", Track: 1, Title: "Song"},
		File{RootID: lib, RelPath: "b.mp3", Sum: "x", Artist: "Singer", Album: "Album", Track: 1, Title: "Song"},
		File{RootID: lib, RelPath: "c.flac", Sum: "c", Artist: "Singer", Album: "Album", Track: 1, Title: "Song"},
	)
	groups, err := s.Groups(testKey)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"exact: a.mp3 b.mp3", "possible: a.mp3 b.mp3 c.flac"}
	if got := groupNames(groups); !reflect.DeepEqual(got, want) {
		t.Errorf("Groups = %q, want %q", got, want)
	}
}
