package store

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// The groups waiting for review are those of Groups that Reviewed does not
// cover, found without a query for each group, and their files come as the
// index has them but for tags_json, which is not read.
func TestGroupsToReviewAgreesWithReviewedAndReadsNoTags(t *testing.T) {
	s := openMemory(t)
	lib := setRoot(t, s, RootLibrary, filepath.FromSlash("/m"))
	untagged := setRoot(t, s, RootUntagged, filepath.FromSlash("/m/_Untagged"))
	tags := `{"APIC":{"picture":{"MIMEType":"image/png"}},"comment":"` + strings.Repeat("x", 2000) + `"}`
	song := File{Format: "MP3", Bitrate: 320, SampleRate: 44100, Channels: 2, Length: 3 * time.Second, Year: 2001,
		Artist: "Singer", AlbumArtist: "Singer", Album: "Album", Size: 9, ModTime: time.Unix(5, 0), IndexedAt: time.Unix(6, 0).UTC(),
		Manual: "hand 2026-01-02", TagsJSON: tags, HasArt: true}
	file := func(root int64, rel, sum string, change func(*File)) File {
		f := song
		f.RootID, f.RelPath, f.Sum, f.Title = root, rel, sum, rel
		if change != nil {
			change(&f)
		}
		return f
	}
	track := func(id string) func(*File) {
		return func(f *File) { f.ReleaseID, f.ReleaseTrackID, f.RecordingID = "rel", id, "rec-"+id }
	}
	putFiles(t, s,
		file(lib, "a1.mp3", "a", nil), file(untagged, "a2.mp3", "a", nil), // reviewed as a pair
		file(lib, "b1.mp3", "b", nil), file(lib, "b2.mp3", "b", nil), file(lib, "b3.mp3", "b", nil), // reviewed before b3 came
		file(lib, "c1.mp3", "c", nil), file(lib, "c2.mp3", "c", nil), // reviewed, but as another kind
		file(lib, "d1.flac", "d1", track("t1")), file(lib, "d2.mp3", "d2", track("t1")), // one member reviewed alone
		file(lib, "also.mp3", "e", func(f *File) { f.ReleaseID, f.ReleaseTrackID, f.RecordingID = "other", "t9", "rec-t1" }),
		file(lib, "e1.mp3", "e1", func(f *File) { f.Title, f.Track = "Same", 2 }), // possible, reviewed with a file since gone
		file(lib, "e2.mp3", "e2", func(f *File) { f.Title, f.Track = "Same", 2 }),
	)
	// A row from before has_art was recorded: its art is read from its tags.
	if _, err := s.db.Exec(`UPDATE files SET has_art = NULL WHERE rel_path = 'd1.flac'`); err != nil {
		t.Fatal(err)
	}
	id := func(rel string) int64 {
		t.Helper()
		for _, f := range filesByPath(t, s) {
			if f.RelPath == rel {
				return f.ID
			}
		}
		t.Fatalf("no file %s", rel)
		return 0
	}
	for _, r := range []Review{
		{Kind: GroupExact, Members: []int64{id("a2.mp3"), id("a1.mp3")}, Outcome: OutcomeKeepBoth, By: ByUser},
		{Kind: GroupExact, Members: []int64{id("b1.mp3"), id("b2.mp3")}, Outcome: OutcomeKeepBoth, By: ByUser},
		{Kind: GroupPossible, Members: []int64{id("c1.mp3"), id("c2.mp3")}, Outcome: OutcomeKeepBoth, By: ByUser},
		{Kind: GroupPossible, Members: []int64{id("e1.mp3"), id("e2.mp3"), 9999}, Outcome: OutcomeKeepBoth, By: ByUser},
		{Kind: GroupReleaseTrack, Members: []int64{id("d1.flac")}, Outcome: OutcomeKeepBoth, By: ByUser},
	} {
		if err := s.RecordReview(r); err != nil {
			t.Fatal(err)
		}
	}

	all, err := s.Groups(testKey)
	if err != nil {
		t.Fatal(err)
	}
	var want []Group
	for _, g := range all {
		reviewed, err := s.Reviewed(g.Kind, g.IDs())
		if err != nil {
			t.Fatal(err)
		}
		if !reviewed {
			want = append(want, g)
		}
	}
	names := []string{"exact: b1.mp3 b2.mp3 b3.mp3", "exact: c1.mp3 c2.mp3", "release-track: d1.flac d2.mp3 / also on: also.mp3"}
	if got := groupNames(want); !reflect.DeepEqual(got, names) {
		t.Fatalf("the groups Reviewed leaves = %q, want %q", got, names)
	}

	got, err := s.GroupsToReview(testKey)
	if err != nil {
		t.Fatal(err)
	}
	if n, err := s.GroupsNeedingReview(testKey); err != nil || n != len(want) {
		t.Errorf("GroupsNeedingReview = %d, %v; want %d", n, err, len(want))
	}
	// As Groups has them, with every column but tags_json.
	for _, g := range want {
		for _, files := range [][]File{g.Files, g.AlsoOn} {
			for i := range files {
				if files[i].TagsJSON != tags || !files[i].HasArt {
					t.Fatalf("Groups gave %s without its tags or art: %+v", files[i].RelPath, files[i])
				}
				files[i].TagsJSON = ""
			}
		}
	}
	// The one row without has_art keeps its tags_json, read to tell its art.
	for _, g := range got {
		for i, f := range g.Files {
			if f.RelPath == "d1.flac" {
				if f.TagsJSON != tags || !f.HasArt {
					t.Errorf("the row without has_art: art %t, %d bytes of tags", f.HasArt, len(f.TagsJSON))
				}
				g.Files[i].TagsJSON = ""
			}
		}
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("GroupsToReview =\n%+v\nwant\n%+v", got, want)
	}

	files, err := s.UntaggedFiles()
	if err != nil || len(files) != 1 || files[0].RelPath != "a2.mp3" || files[0].TagsJSON != "" || !files[0].HasArt || files[0].Manual != song.Manual {
		t.Errorf("UntaggedFiles = %+v, %v; want a2.mp3 as indexed, without its tags_json", files, err)
	}
}

// Possible groups come in the order of their first file, whichever of them
// was the first to gain a second.
func TestPossibleGroupsInOrderOfTheirFirstFile(t *testing.T) {
	s := openMemory(t)
	lib := setRoot(t, s, RootLibrary, filepath.FromSlash("/m"))
	song := func(rel, title string) File {
		return File{RootID: lib, RelPath: rel, Sum: rel, Artist: "Singer", Album: "Album", Title: title}
	}
	putFiles(t, s, song("one a.mp3", "One"), song("two a.mp3", "Two"), song("lone.mp3", "Lone"), song("two b.mp3", "Two"), song("one b.mp3", "One"))
	groups, err := s.Groups(testKey)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"possible: one a.mp3 one b.mp3", "possible: two a.mp3 two b.mp3"}
	if got := groupNames(groups); !reflect.DeepEqual(got, want) {
		t.Errorf("Groups = %q, want %q", got, want)
	}
}
