package engine

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"tagshelf/internal/fixtures"
	"tagshelf/internal/rules"
	"tagshelf/internal/store"
)

// theAudio is the audio the duplicate tests copy: long enough to hash, and
// hashing the same with or without an ID3v2 tag in front.
var theAudio = strings.Repeat("the audio ", 40)

// songTags are the tags of the library song the duplicate tests copy.
var songTags = id3Tags{albumArtist: "Singer", album: "Album", title: "Song", track: "1"}

// songPath is where songTags put a song in the library at lib.
func songPath(lib string) string {
	return filepath.Join(lib, "Singer", "Album", "01 - Song.mp3")
}

// indexLibrary brings the index of lib up to date, as tagshelf -index does.
func indexLibrary(t *testing.T, s *store.Store, lib string) {
	t.Helper()
	if finish := Run(context.Background(), Options{Destination: lib, IndexOnly: true, Store: s}); finish.State != StateOK {
		t.Fatalf("index run = %+v", finish)
	}
}

func reviews(t *testing.T, s *store.Store) []store.Review {
	t.Helper()
	r, err := s.Reviews()
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func mustExist(t *testing.T, paths ...string) {
	t.Helper()
	for _, p := range paths {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("expected %q to exist: %v", p, err)
		}
	}
}

func mustNotExist(t *testing.T, paths ...string) {
	t.Helper()
	for _, p := range paths {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("expected %q to be gone (%v)", p, err)
		}
	}
}

func TestIdenticalIncomingMovesToDuplicates(t *testing.T) {
	lib := filepath.Join(t.TempDir(), "lib")
	kept := songPath(lib)
	writeTaggedMP3(t, kept, songTags, theAudio)
	s := openStore(t)
	indexLibrary(t, s, lib)
	keptRow := indexedFiles(t, s)[kept]

	source := filepath.Join(t.TempDir(), "src")
	// The same audio under other tags, with its artwork.
	incoming := filepath.Join(source, "Rip", "copy.mp3")
	writeTaggedMP3(t, incoming, id3Tags{albumArtist: "Singer", album: "Album", title: "Other Title", track: "2"}, theAudio)
	writeTestFile(t, filepath.Join(source, "Rip", "cover.jpg"), "art")

	var c collector
	finish := Run(context.Background(), Options{Source: source, Destination: lib, Store: s, Sink: c.sink})
	if finish.State != StateOK || finish.Counters.Duplicates != 1 || finish.Counters.Organized != 0 {
		t.Fatalf("run = %+v, want one duplicate moved and nothing organized", finish)
	}
	dup := filepath.Join(lib, "_Duplicates", "Rip", "copy.mp3")
	mustExist(t, kept, dup, filepath.Join(lib, "_Duplicates", "Rip", "cover.jpg"))
	mustNotExist(t, incoming, filepath.Join(lib, "Singer", "Album", "02 - Other Title.mp3"))
	if got := readTestFile(t, kept); got == "" {
		t.Error("kept copy emptied")
	}

	var placed *FilePlaced
	for _, e := range c.events {
		if p, ok := e.(FilePlaced); ok && p.Path == incoming {
			placed = &p
		}
	}
	if placed == nil || placed.Outcome != Duplicate || placed.Destination != dup {
		t.Errorf("FilePlaced for the copy = %+v, want Duplicate to %q", placed, dup)
	}

	runs, err := s.Runs()
	if err != nil {
		t.Fatal(err)
	}
	run := runs[len(runs)-1]
	if run.Counters.Duplicates != 1 {
		t.Errorf("recorded duplicates = %d, want 1", run.Counters.Duplicates)
	}
	moves := movesOf(t, s, run.ID)
	if len(moves) != 2 || moves[0].From != incoming || moves[0].To != dup || moves[0].Reason != store.ReasonDuplicate {
		t.Errorf("moves = %+v, want the copy moved as a duplicate", moves)
	}

	rows := indexedFiles(t, s)
	dupRow := rows[dup]
	if dupRow.RootKind != store.RootDuplicates {
		t.Errorf("row of the moved copy = %+v, want it under the duplicates root", dupRow)
	}
	got := reviews(t, s)
	if len(got) != 1 {
		t.Fatalf("reviews = %+v, want 1", got)
	}
	r := got[0]
	if r.Kind != store.GroupExact || r.By != store.ByAuto || r.Outcome != store.OutcomeResolved ||
		r.Keeper != keptRow.ID || r.RunID != run.ID || len(r.Members) != 2 {
		t.Errorf("review = %+v, want an auto resolution keeping row %d", r, keptRow.ID)
	}
	if finish.NeedReview != 0 {
		t.Errorf("NeedReview = %d, want 0", finish.NeedReview)
	}
}

func TestHitOnlyInDuplicatesSendsFileThere(t *testing.T) {
	lib := filepath.Join(t.TempDir(), "lib")
	writeTestFile(t, filepath.Join(lib, "_Duplicates", "old", "song.mp3"), theAudio)
	s := openStore(t)
	indexLibrary(t, s, lib)

	source := filepath.Join(t.TempDir(), "src")
	incoming := filepath.Join(source, "song.mp3")
	writeTaggedMP3(t, incoming, songTags, theAudio)

	finish := Run(context.Background(), Options{Source: source, Destination: lib, Store: s})
	if finish.Counters.Duplicates != 1 || finish.Counters.Organized != 0 {
		t.Fatalf("run = %+v, want the file sent to the duplicates folder", finish)
	}
	mustExist(t, filepath.Join(lib, "_Duplicates", "song.mp3"))
	mustNotExist(t, incoming, songPath(lib))
	if got := reviews(t, s); len(got) != 0 {
		t.Errorf("reviews = %+v, want none: there was no group", got)
	}
}

func TestBetterCopyReplacesWorseInUntagged(t *testing.T) {
	lib := filepath.Join(t.TempDir(), "lib")
	worse := filepath.Join(lib, "_Untagged", "song.mp3")
	writeTestFile(t, worse, theAudio) // no tags at all
	s := openStore(t)
	indexLibrary(t, s, lib)

	source := filepath.Join(t.TempDir(), "src")
	incoming := filepath.Join(source, "song.mp3")
	writeTaggedMP3(t, incoming, songTags, theAudio) // MusicBrainz-tagged
	if a, b := wantSum(t, worse), wantSum(t, incoming); a != b {
		t.Fatalf("test files do not share their audio hash: %s, %s", a, b)
	}

	finish := Run(context.Background(), Options{Source: source, Destination: lib, Store: s})
	if finish.State != StateOK || finish.Counters.Organized != 1 || finish.Counters.Duplicates != 1 {
		t.Fatalf("run = %+v, want the better copy organized and the worse one moved out", finish)
	}
	mustExist(t, songPath(lib), filepath.Join(lib, "_Duplicates", "song.mp3"))
	mustNotExist(t, worse, incoming)

	rows := indexedFiles(t, s)
	got := reviews(t, s)
	if len(got) != 1 || got[0].Keeper != rows[songPath(lib)].ID {
		t.Errorf("reviews = %+v, want the organized copy (row %d) kept", got, rows[songPath(lib)].ID)
	}
}

func TestKeptCopyTakesTheNameItsDuplicateHeld(t *testing.T) {
	lib := filepath.Join(t.TempDir(), "lib")
	worse := filepath.Join(lib, "_Untagged", "song.mp3")
	writeTestFile(t, worse, theAudio)
	s := openStore(t)
	indexLibrary(t, s, lib)
	source := filepath.Join(t.TempDir(), "src")
	// Better tagged, but with no title it still belongs in _Untagged, under
	// the very name the worse copy has.
	writeTaggedMP3(t, filepath.Join(source, "song.mp3"), id3Tags{albumArtist: "Singer", album: "Album"}, theAudio)

	for _, dry := range []bool{true, false} {
		var c collector
		finish := Run(context.Background(), Options{Source: source, Destination: lib, Store: s, DryRun: dry, Sink: c.sink})
		if finish.State != StateOK || finish.Counters.Untagged != 1 || finish.Counters.Duplicates != 1 {
			t.Fatalf("dry %t: run = %+v", dry, finish)
		}
		if dry {
			for _, e := range c.events {
				if p, ok := e.(FilePlaced); ok && p.Outcome == Untagged && p.Destination != worse {
					t.Errorf("dry run planned the kept copy for %q, want %q", p.Destination, worse)
				}
			}
		}
	}
	if got := readTestFile(t, worse); got == theAudio {
		t.Error("the untagged copy was not replaced by the better one")
	}
	if got := readTestFile(t, filepath.Join(lib, "_Duplicates", "song.mp3")); got != theAudio {
		t.Errorf("duplicates folder holds %q, want the worse copy", got)
	}
	mustNotExist(t, filepath.Join(lib, "_Untagged", "song (1).mp3"))
}

func TestDuplicatesStayWhenTheKeptCopyCannotMove(t *testing.T) {
	lib := filepath.Join(t.TempDir(), "lib")
	worse := filepath.Join(lib, "_Untagged", "song.mp3")
	writeTestFile(t, worse, theAudio)
	s := openStore(t)
	indexLibrary(t, s, lib)
	// The kept copy's album folder cannot be made.
	writeTestFile(t, filepath.Join(lib, "Singer"), "in the way")
	source := filepath.Join(t.TempDir(), "src")
	incoming := filepath.Join(source, "song.mp3")
	writeTaggedMP3(t, incoming, songTags, theAudio)

	finish := Run(context.Background(), Options{Source: source, Destination: lib, Store: s})
	if finish.Counters.Errors == 0 || finish.Counters.Duplicates != 0 {
		t.Fatalf("run = %+v, want the kept copy's move to fail and nothing put aside", finish)
	}
	mustExist(t, worse, incoming)
	mustNotExist(t, filepath.Join(lib, "_Duplicates"))
	if got := reviews(t, s); len(got) != 0 {
		t.Errorf("reviews = %+v, want none for an unresolved group", got)
	}
}

// libraryWithInternalDuplicate builds a library whose song also sits, as the
// same audio, in _Untagged.
func libraryWithInternalDuplicate(t *testing.T) (lib, loose string) {
	t.Helper()
	lib = filepath.Join(t.TempDir(), "lib")
	writeTaggedMP3(t, songPath(lib), songTags, theAudio)
	loose = filepath.Join(lib, "_Untagged", "loose.mp3")
	writeTestFile(t, loose, theAudio)
	return lib, loose
}

func TestExistingDuplicatesAreLeftAlone(t *testing.T) {
	t.Run("first index", func(t *testing.T) {
		lib, loose := libraryWithInternalDuplicate(t)
		s := openStore(t)
		source := t.TempDir()
		finish := Run(context.Background(), Options{Source: source, Destination: lib, Store: s})
		if finish.Counters.Duplicates != 0 || finish.NeedReview != 1 {
			t.Errorf("run = %+v, want nothing moved and one group to review", finish)
		}
		mustExist(t, songPath(lib), loose)
	})
	t.Run("organize", func(t *testing.T) {
		lib, loose := libraryWithInternalDuplicate(t)
		s := openStore(t)
		indexLibrary(t, s, lib)
		finish := Run(context.Background(), Options{Source: t.TempDir(), Destination: lib, Store: s})
		if finish.Counters.Duplicates != 0 || finish.NeedReview != 1 {
			t.Errorf("run = %+v, want nothing moved and one group to review", finish)
		}
		mustExist(t, songPath(lib), loose)
	})
	t.Run("reorganize", func(t *testing.T) {
		lib, loose := libraryWithInternalDuplicate(t)
		s := openStore(t)
		indexLibrary(t, s, lib)
		finish := Run(context.Background(), Options{Source: lib, Destination: lib, Store: s})
		if finish.Counters.Duplicates != 0 || finish.NeedReview != 1 {
			t.Errorf("run = %+v, want nothing moved and one group to review", finish)
		}
		mustExist(t, songPath(lib), loose)
		if got := reviews(t, s); len(got) != 0 {
			t.Errorf("reviews = %+v, want none", got)
		}
	})
}

func TestReorganizeResolvesFileNewSinceLastIndex(t *testing.T) {
	lib := filepath.Join(t.TempDir(), "lib")
	writeTaggedMP3(t, songPath(lib), songTags, theAudio)
	s := openStore(t)
	indexLibrary(t, s, lib)
	// Dropped in by hand after the last index.
	loose := filepath.Join(lib, "_Untagged", "loose.mp3")
	writeTestFile(t, loose, theAudio)

	finish := Run(context.Background(), Options{Source: lib, Destination: lib, Store: s})
	if finish.Counters.Duplicates != 1 || finish.NeedReview != 0 {
		t.Fatalf("run = %+v, want the new copy moved out", finish)
	}
	mustExist(t, songPath(lib), filepath.Join(lib, "_Duplicates", "loose.mp3"))
	mustNotExist(t, loose)
}

func TestReviewedGroupStaysWhenAMemberChanges(t *testing.T) {
	lib, loose := libraryWithInternalDuplicate(t)
	s := openStore(t)
	indexLibrary(t, s, lib)
	rows := indexedFiles(t, s)
	members := []int64{rows[songPath(lib)].ID, rows[loose].ID}
	if err := s.RecordReview(store.Review{Kind: store.GroupExact, Members: members, Outcome: store.OutcomeKeepBoth, By: store.ByUser}); err != nil {
		t.Fatal(err)
	}
	// Touched since the last index: re-read, but still a reviewed member.
	later := time.Now().Add(time.Hour)
	if err := os.Chtimes(loose, later, later); err != nil {
		t.Fatal(err)
	}

	finish := Run(context.Background(), Options{Source: lib, Destination: lib, Store: s})
	if finish.Counters.Indexed != 1 || finish.Counters.Duplicates != 0 || finish.NeedReview != 0 {
		t.Errorf("run = %+v, want the changed file re-read and nothing moved", finish)
	}
	mustExist(t, songPath(lib), loose)
}

func TestNoAutoResolveLeavesGroupForReview(t *testing.T) {
	lib := filepath.Join(t.TempDir(), "lib")
	writeTaggedMP3(t, songPath(lib), songTags, theAudio)
	s := openStore(t)
	indexLibrary(t, s, lib)
	source := filepath.Join(t.TempDir(), "src")
	writeTaggedMP3(t, filepath.Join(source, "song.mp3"), songTags, theAudio)

	finish := Run(context.Background(), Options{Source: source, Destination: lib, Store: s, NoAutoResolve: true})
	if finish.Counters.Duplicates != 0 || finish.Counters.Organized != 1 || finish.NeedReview != 1 {
		t.Fatalf("run = %+v, want the copy organized beside the other and one group to review", finish)
	}
	// Same name, so it takes the next free one.
	mustExist(t, songPath(lib), filepath.Join(lib, "Singer", "Album", "01 - Song (1).mp3"))
}

func TestIncomingCopiesResolveAmongThemselves(t *testing.T) {
	lib := filepath.Join(t.TempDir(), "lib")
	s := openStore(t)
	source := filepath.Join(t.TempDir(), "src")
	tagged := filepath.Join(source, "a", "song.mp3")
	writeTaggedMP3(t, tagged, songTags, theAudio)
	bare := filepath.Join(source, "b", "song.mp3")
	writeTestFile(t, bare, theAudio)

	finish := Run(context.Background(), Options{Source: source, Destination: lib, Store: s, Workers: 4})
	if finish.Counters.Organized != 1 || finish.Counters.Duplicates != 1 || finish.Counters.Untagged != 0 {
		t.Fatalf("run = %+v, want the tagged copy organized and the bare one put aside", finish)
	}
	mustExist(t, songPath(lib), filepath.Join(lib, "_Duplicates", "b", "song.mp3"))
}

func TestDryRunPlansDuplicatesAndMovesNothing(t *testing.T) {
	lib := filepath.Join(t.TempDir(), "lib")
	worse := filepath.Join(lib, "_Untagged", "song.mp3")
	writeTestFile(t, worse, theAudio)
	s := openStore(t)
	indexLibrary(t, s, lib)
	source := filepath.Join(t.TempDir(), "src")
	incoming := filepath.Join(source, "song.mp3")
	writeTaggedMP3(t, incoming, songTags, theAudio)

	var c collector
	finish := Run(context.Background(), Options{Source: source, Destination: lib, Store: s, DryRun: true, Sink: c.sink})
	if finish.Counters.Organized != 1 || finish.Counters.Duplicates != 1 {
		t.Fatalf("dry run = %+v, want one organized and one duplicate planned", finish)
	}
	if got := c.placed(); got[worse] != Duplicate || got[incoming] != Organized {
		t.Errorf("planned outcomes = %v", got)
	}
	mustExist(t, worse, incoming)
	mustNotExist(t, filepath.Join(lib, "_Duplicates"))
	if got := reviews(t, s); len(got) != 0 {
		t.Errorf("a dry run recorded reviews: %+v", got)
	}
}

func TestDryRunTreatsVacatedNamesAsFree(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src.mp3")
	taken := filepath.Join(dir, "taken.mp3")
	writeTestFile(t, src, "a")
	writeTestFile(t, taken, "b")
	o := newOrganizer(Options{Source: dir, Destination: dir, DryRun: true})
	o.vacated.Store(rules.PathKey(taken), struct{}{})
	got, outcome, err := o.resolveDestination(src, taken, false)
	if err != nil || outcome != outcomeMove || got != taken {
		t.Errorf("resolveDestination = %q, %v, %v; want the vacated name", got, outcome, err)
	}
}

func TestIdenticalCompanionMovesToDuplicates(t *testing.T) {
	tmp := t.TempDir()
	lib := filepath.Join(tmp, "lib")
	source := filepath.Join(tmp, "src")
	writeTestFile(t, filepath.Join(lib, "_Other", "Rip", "notes.txt"), "same notes")
	writeTestFile(t, filepath.Join(source, "Rip", "notes.txt"), "same notes")
	writeTestFile(t, filepath.Join(source, "Rip", "other.txt"), "different")
	writeTestFile(t, filepath.Join(lib, "_Other", "Rip", "other.txt"), "not the same")

	finish := Run(context.Background(), Options{Source: source, Destination: lib})
	if finish.Counters.Duplicates != 1 || finish.Counters.OtherMoved != 1 {
		t.Fatalf("run = %+v, want one duplicate and one non-audio file moved", finish)
	}
	mustExist(t, filepath.Join(lib, "_Duplicates", "Rip", "notes.txt"), filepath.Join(lib, "_Other", "Rip", "other (1).txt"))
}

// A FLAC and an MP3 rip of the same release track are duplicates, but not
// exact ones: a run leaves both in the library and counts the group for
// review.
func TestReleaseTrackGroupNeedsReview(t *testing.T) {
	source := filepath.Join(t.TempDir(), "src")
	for _, ext := range []string{".flac", ".mp3"} {
		data, err := os.ReadFile(fixtures.Tagged(ext))
		if err != nil {
			t.Fatal(err)
		}
		writeTestFile(t, filepath.Join(source, "Rip", "song"+ext), string(data))
	}
	lib := filepath.Join(t.TempDir(), "lib")
	s := openStore(t)

	finish := Run(context.Background(), Options{Source: source, Destination: lib, Store: s})
	if finish.State != StateOK || finish.Counters.Organized != 2 || finish.Counters.Duplicates != 0 {
		t.Fatalf("run = %+v, want both organized and nothing moved to duplicates", finish)
	}
	if finish.NeedReview != 1 {
		t.Errorf("NeedReview = %d, want 1", finish.NeedReview)
	}
	groups, err := s.Groups(PossibleKey)
	if err != nil {
		t.Fatal(err)
	}
	if len(groups) != 1 || groups[0].Kind != store.GroupReleaseTrack || len(groups[0].Files) != 2 {
		t.Errorf("groups = %+v, want one release-track group of 2", groups)
	}
	if len(reviews(t, s)) != 0 {
		t.Errorf("reviews = %+v, want none: release-track groups wait for a person", reviews(t, s))
	}
}
