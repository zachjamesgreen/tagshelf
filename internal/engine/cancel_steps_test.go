package engine

import (
	"context"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/zachjamesgreen/tagshelf/internal/store"
)

// cancelAt returns a sink that keeps every event in c and cancels the run at
// the first event when says so.
func cancelAt(c *collector, cancel context.CancelFunc, when func(Event) bool) Sink {
	return func(e Event) {
		c.sink(e)
		if when(e) {
			cancel()
		}
	}
}

// A run cancelled while it places non-audio files still rewrites the cue
// sheets that did move, since no later run comes back to them, and skips the
// cleanup. With a database, the moves it made are on record and the index
// agrees with the disk.
func TestCancelledRunStillRewritesMovedCueSheets(t *testing.T) {
	tmp := t.TempDir()
	source, lib := filepath.Join(tmp, "src"), filepath.Join(tmp, "lib")
	writeTaggedMP3(t, filepath.Join(source, "rip", "Song.mp3"), songTags, theAudio)
	writeTestFile(t, filepath.Join(source, "rip", "album.cue"), cueOf("Song.mp3"))
	// Later in the walk than the cue sheet, so not yet placed when it stops.
	art := filepath.Join(source, "rip", "cover.jpg")
	writeTestFile(t, art, "art")

	s := openStore(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var c collector
	finish := Run(ctx, Options{Source: source, Destination: lib, Workers: 1, Store: s, Sink: cancelAt(&c, cancel, func(e Event) bool {
		p, ok := e.(FilePlaced)
		return ok && p.Outcome == Companion
	})})
	if finish.State != StateInterrupted || finish.Counters.Organized != 1 || finish.Counters.OtherMoved != 1 || finish.Counters.CueRewritten != 1 {
		t.Fatalf("run = %+v, want it stopped after the song and the cue sheet, the sheet rewritten", finish)
	}
	album := filepath.Join(lib, "Singer", "Album")
	if got := readTestFile(t, filepath.Join(album, "album.cue")); got != cueOf("01 - Song.mp3") {
		t.Errorf("the moved cue sheet = %q, want it rewritten although the run was cancelled", got)
	}
	mustExist(t, art, filepath.Join(source, "rip"))
	if n := c.count(func(e Event) bool { _, ok := e.(DirRemoved); return ok }); n != 0 {
		t.Errorf("cleanup ran after cancellation: %d directories removed", n)
	}

	// What it did is on record, and the index agrees with the disk.
	var reasons []store.MoveReason
	for _, m := range movesOf(t, s, finish.RunID) {
		reasons = append(reasons, m.Reason)
		if m.Reason != store.ReasonCueRewrite {
			mustExist(t, m.To)
			mustNotExist(t, m.From)
		}
	}
	if want := []store.MoveReason{store.ReasonCueRewrite, store.ReasonOrganized, store.ReasonCompanion}; !sameReasons(reasons, want) {
		t.Errorf("recorded moves = %v, want %v", reasons, want)
	}
	rows := indexedFiles(t, s)
	if _, ok := rows[songPath(lib)]; !ok || len(rows) != 1 {
		t.Errorf("index = %v, want the one song at its place in the library", rows)
	}
	if run, err := s.Run(finish.RunID); err != nil || run.State != store.StateInterrupted {
		t.Errorf("the run's row = %+v, %v; want it interrupted", run, err)
	}
}

func sameReasons(got, want []store.MoveReason) bool {
	count := func(rs []store.MoveReason) map[store.MoveReason]int {
		m := map[store.MoveReason]int{}
		for _, r := range rs {
			m[r]++
		}
		return m
	}
	return reflect.DeepEqual(count(got), count(want))
}

// A reorganize cancelled after its first move does not go on to retry the
// file that landed on a " (1)" name, nor to rename for letter case: both
// would be moves made after the run was told to stop.
func TestCancelledReorganizeDoesNotRetry(t *testing.T) {
	lib, a, _ := swappedLibrary(t)
	// A third song, in place apart from letter case.
	lower := filepath.Join(filepath.Dir(a), "03 - c.mp3")
	writeTaggedMP3(t, lower, id3Tags{albumArtist: "Singer", album: "Album", title: "C", track: "3"}, "really C")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var c collector
	moved := func(e Event) bool {
		p, ok := e.(FilePlaced)
		return ok && p.Outcome == Organized
	}
	finish := Run(ctx, Options{Source: lib, Destination: lib, Workers: 1, Sink: cancelAt(&c, cancel, moved)})
	if finish.State != StateInterrupted || finish.Counters.Organized != 1 || finish.Counters.Retried != 0 {
		t.Fatalf("reorganize = %+v, want it stopped after one move, with nothing retried", finish)
	}
	names := exactNames(t, lib)
	if len(names) != 3 || !strings.Contains(strings.Join(names, "|"), " (1).mp3") || !strings.Contains(strings.Join(names, "|"), "03 - c.mp3") {
		t.Errorf("library = %q, want the first file still on its (1) name and the third as it was spelled", names)
	}
	if n := c.count(func(e Event) bool { p, ok := e.(PhaseStarted); return ok && p.Phase == PhaseRetrySuffixed }); n != 0 {
		t.Errorf("the retry phase started %d times after cancellation", n)
	}
}

// Cancelling while exact duplicates are being settled stops before the next
// group: its copies stay where they are, as the workers would leave them.
func TestCancelledWhileResolvingDuplicates(t *testing.T) {
	lib := filepath.Join(t.TempDir(), "lib")
	other := id3Tags{albumArtist: "Singer", album: "Album", title: "Other", track: "2"}
	otherAudio := strings.Repeat("other audio ", 40)
	writeTaggedMP3(t, songPath(lib), songTags, theAudio)
	writeTaggedMP3(t, filepath.Join(lib, "Singer", "Album", "02 - Other.mp3"), other, otherAudio)
	s := openStore(t)
	indexLibrary(t, s, lib)
	source := filepath.Join(t.TempDir(), "src")
	writeTestFile(t, filepath.Join(source, "one.mp3"), theAudio)
	writeTestFile(t, filepath.Join(source, "two.mp3"), otherAudio)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var c collector
	finish := Run(ctx, Options{Source: source, Destination: lib, Workers: 1, Store: s, Sink: cancelAt(&c, cancel, func(e Event) bool {
		p, ok := e.(FilePlaced)
		return ok && p.Outcome == Duplicate
	})})
	if finish.State != StateInterrupted || finish.Counters.Duplicates != 1 || finish.Counters.Untagged != 0 {
		t.Fatalf("run = %+v, want it stopped after the first group", finish)
	}
	if left := exactNames(t, source); len(left) != 1 {
		t.Errorf("source = %q, want the copy of the second group still there", left)
	}
	if got := reviews(t, s); len(got) != 1 {
		t.Errorf("reviews = %+v, want the one group that was resolved", got)
	}
}

// Cancelling between two resolutions a person made carries out the first and
// leaves the second group as it was.
func TestCancelledBetweenResolutions(t *testing.T) {
	lib, kept, copyPath, _, s := reviewLibrary(t)
	// A second group, added and indexed.
	otherAudio := strings.Repeat("other audio ", 40)
	second := filepath.Join(lib, "Singer", "Album", "02 - Other.mp3")
	secondCopy := filepath.Join(lib, "_Untagged", "other.mp3")
	writeTaggedMP3(t, second, id3Tags{albumArtist: "Singer", album: "Album", title: "Other", track: "2"}, otherAudio)
	writeTestFile(t, secondCopy, otherAudio)
	indexLibrary(t, s, lib)
	rows := indexedFiles(t, s)
	resolutions := []Resolution{
		{Kind: store.GroupExact, Members: []int64{rows[kept].ID, rows[copyPath].ID}, Keeper: rows[kept].ID},
		{Kind: store.GroupExact, Members: []int64{rows[second].ID, rows[secondCopy].ID}, Keeper: rows[second].ID},
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var c collector
	finish := ResolveDuplicates(ctx, Options{Destination: lib, Store: s, Sink: cancelAt(&c, cancel, func(e Event) bool {
		_, ok := e.(FilePlaced)
		return ok
	})}, resolutions)
	if finish.State != StateInterrupted || finish.Counters.Duplicates != 1 {
		t.Fatalf("resolve = %+v, want it stopped after the first group", finish)
	}
	mustNotExist(t, copyPath)
	mustExist(t, kept, second, secondCopy)
	if got := reviews(t, s); len(got) != 1 || got[0].Keeper != rows[kept].ID {
		t.Errorf("reviews = %+v, want only the first group's", got)
	}
}
