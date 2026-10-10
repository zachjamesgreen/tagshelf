package engine

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"tagshelf/internal/store"
)

// reviewLibrary is a library holding one exact duplicate group for a person
// to review: the song in its album, and a copy of it with artwork in a folder
// of its own, indexed before either was a duplicate to any run.
func reviewLibrary(t *testing.T) (lib, kept, copyPath, art string, s *store.Store) {
	t.Helper()
	lib = filepath.Join(t.TempDir(), "lib")
	kept = songPath(lib)
	writeTaggedMP3(t, kept, songTags, theAudio)
	copyPath = filepath.Join(lib, "Singer", "Album (bonus)", "01 - Song.mp3")
	writeTaggedMP3(t, copyPath, songTags, theAudio)
	art = filepath.Join(lib, "Singer", "Album (bonus)", "cover.jpg")
	writeTestFile(t, art, "art")
	s = openStore(t)
	indexLibrary(t, s, lib)
	return lib, kept, copyPath, art, s
}

// onlyGroup returns the one duplicate group the library has.
func onlyGroup(t *testing.T, s *store.Store) store.Group {
	t.Helper()
	groups, err := s.Groups(PossibleKey)
	if err != nil {
		t.Fatal(err)
	}
	if len(groups) != 1 {
		t.Fatalf("groups = %+v, want one", groups)
	}
	return groups[0]
}

func TestResolveDuplicatesMovesTheOthers(t *testing.T) {
	lib, kept, copyPath, art, s := reviewLibrary(t)
	g := onlyGroup(t, s)
	files := indexedFiles(t, s)
	keeper := files[kept]

	var c collector
	finish := ResolveDuplicates(context.Background(), Options{Destination: lib, Store: s, Sink: c.sink},
		[]Resolution{{Kind: g.Kind, Members: g.IDs(), Keeper: keeper.ID}})
	if finish.State != StateOK || finish.Counters.Duplicates != 1 {
		t.Fatalf("finish = %+v, want one duplicate moved", finish)
	}

	moved := filepath.Join(lib, "_Duplicates", "Singer", "Album (bonus)", "01 - Song.mp3")
	movedArt := filepath.Join(lib, "_Duplicates", "Singer", "Album (bonus)", "cover.jpg")
	mustExist(t, kept, moved, movedArt)
	mustNotExist(t, copyPath, art, filepath.Dir(copyPath))

	run := lastRun(t, s)
	if run.Kind != store.KindTUIAction || run.State != store.StateOK {
		t.Errorf("run = %+v, want a finished tui-action", run)
	}
	moves, err := s.Moves(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(moves) != 2 || moves[0].From != copyPath || moves[0].To != moved || moves[0].Reason != store.ReasonDuplicate ||
		moves[1].From != art || moves[1].To != movedArt || moves[1].Reason != store.ReasonCompanion {
		t.Errorf("moves = %+v", moves)
	}
	rs := reviews(t, s)
	if len(rs) != 1 || rs[0].By != store.ByUser || rs[0].Outcome != store.OutcomeResolved || rs[0].Keeper != keeper.ID || rs[0].RunID != run.ID {
		t.Errorf("reviews = %+v", rs)
	}
	if n, err := s.GroupsNeedingReview(PossibleKey); err != nil || n != 0 {
		t.Errorf("groups needing review = %d, %v; want 0", n, err)
	}
	if row := indexedFiles(t, s)[moved]; row.ID == 0 || row.RootKind != store.RootDuplicates {
		t.Errorf("the moved copy's index row = %+v, want it in the duplicates folder", row)
	}
	if first, ok := c.events[0].(RunStarted); !ok || first.Kind != KindTUIAction {
		t.Errorf("first event = %+v, want a tui-action RunStarted", c.events[0])
	}
}

// The copy a person chose to keep can be the one outside the album.
func TestResolveDuplicatesKeepsTheChosenCopy(t *testing.T) {
	lib, kept, copyPath, art, s := reviewLibrary(t)
	g := onlyGroup(t, s)
	finish := ResolveDuplicates(context.Background(), Options{Destination: lib, Store: s},
		[]Resolution{{Kind: g.Kind, Members: g.IDs(), Keeper: indexedFiles(t, s)[copyPath].ID}})
	if finish.State != StateOK {
		t.Fatalf("finish = %+v", finish)
	}
	mustExist(t, copyPath, art, filepath.Join(lib, "_Duplicates", "Singer", "Album", "01 - Song.mp3"))
	mustNotExist(t, kept)
}

// A copy that is gone or changed since it was indexed stops its group: the
// decision was made about other files.
func TestResolveDuplicatesRefusesChangedFiles(t *testing.T) {
	for _, change := range []string{"keeper gone", "other changed"} {
		lib, kept, copyPath, _, s := reviewLibrary(t)
		g := onlyGroup(t, s)
		keeper := indexedFiles(t, s)[kept]
		switch change {
		case "keeper gone":
			if err := os.Remove(kept); err != nil {
				t.Fatal(err)
			}
		case "other changed":
			writeTaggedMP3(t, copyPath, songTags, "other audio altogether, and longer")
		}
		finish := ResolveDuplicates(context.Background(), Options{Destination: lib, Store: s},
			[]Resolution{{Kind: g.Kind, Members: g.IDs(), Keeper: keeper.ID}})
		if finish.State != StateErrors {
			t.Errorf("%s: finish = %+v, want errors", change, finish)
		}
		mustExist(t, copyPath)
		if len(reviews(t, s)) != 0 {
			t.Errorf("%s: a review was recorded", change)
		}
	}
}

// Companions stay with audio that stays: a folder is followed only when all
// its audio left.
func TestResolveDuplicatesLeavesCompanionsOfRemainingAudio(t *testing.T) {
	lib, kept, copyPath, art, s := reviewLibrary(t)
	other := filepath.Join(filepath.Dir(copyPath), "02 - Other.mp3")
	writeTaggedMP3(t, other, id3Tags{albumArtist: "Singer", album: "Album (bonus)", title: "Other", track: "2"}, "another song entirely")
	indexLibrary(t, s, lib)
	g := onlyGroup(t, s)
	finish := ResolveDuplicates(context.Background(), Options{Destination: lib, Store: s},
		[]Resolution{{Kind: g.Kind, Members: g.IDs(), Keeper: indexedFiles(t, s)[kept].ID}})
	if finish.State != StateOK {
		t.Fatalf("finish = %+v", finish)
	}
	mustExist(t, art, other)
	mustNotExist(t, copyPath)
}

func TestResolveDuplicatesRefusals(t *testing.T) {
	lib, kept, _, _, s := reviewLibrary(t)
	g := onlyGroup(t, s)
	keeper := indexedFiles(t, s)[kept].ID
	for name, tt := range map[string]struct {
		opts Options
		res  Resolution
	}{
		"no store":         {Options{Destination: lib}, Resolution{Kind: g.Kind, Members: g.IDs(), Keeper: keeper}},
		"dry run":          {Options{Destination: lib, Store: s, DryRun: true}, Resolution{Kind: g.Kind, Members: g.IDs(), Keeper: keeper}},
		"keeper not in it": {Options{Destination: lib, Store: s}, Resolution{Kind: g.Kind, Members: g.IDs(), Keeper: 9999}},
		"one member":       {Options{Destination: lib, Store: s}, Resolution{Kind: g.Kind, Members: []int64{keeper}, Keeper: keeper}},
		"unknown member":   {Options{Destination: lib, Store: s}, Resolution{Kind: g.Kind, Members: []int64{keeper, 9999}, Keeper: keeper}},
	} {
		finish := ResolveDuplicates(context.Background(), tt.opts, []Resolution{tt.res})
		if finish.State == StateOK {
			t.Errorf("%s: finish = %+v, want it refused", name, finish)
		}
		if n := len(reviews(t, s)); n != 0 {
			t.Errorf("%s: %d reviews recorded", name, n)
		}
	}
	mustExist(t, kept)
}

// A copy lying loose in the untagged folder has no folder of its own: the
// other loose files there are not its companions.
func TestResolveDuplicatesLeavesLooseFilesOfARootAlone(t *testing.T) {
	lib := filepath.Join(t.TempDir(), "lib")
	kept := songPath(lib)
	writeTaggedMP3(t, kept, songTags, theAudio)
	loose := filepath.Join(lib, "_Untagged", "loose.mp3")
	writeTestFile(t, loose, theAudio)
	playlist := filepath.Join(lib, "_Untagged", "mix.m3u")
	writeTestFile(t, playlist, "a playlist of other songs")
	s := openStore(t)
	indexLibrary(t, s, lib)
	g := onlyGroup(t, s)

	finish := ResolveDuplicates(context.Background(), Options{Destination: lib, Store: s},
		[]Resolution{{Kind: g.Kind, Members: g.IDs(), Keeper: indexedFiles(t, s)[kept].ID}})
	if finish.State != StateOK || finish.Counters.Duplicates != 1 {
		t.Fatalf("finish = %+v", finish)
	}
	mustExist(t, playlist, filepath.Join(lib, "_Duplicates", "loose.mp3"))
	mustNotExist(t, loose)
}
