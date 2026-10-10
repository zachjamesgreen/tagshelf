package engine

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/zachjamesgreen/tagshelf/internal/fixtures"
	"github.com/zachjamesgreen/tagshelf/internal/store"
	"github.com/zachjamesgreen/tagshelf/internal/tag"
)

// indexedFiles returns every file in the index keyed by its path.
func indexedFiles(t *testing.T, s *store.Store) map[string]store.File {
	t.Helper()
	files, err := s.Files()
	if err != nil {
		t.Fatal(err)
	}
	m := make(map[string]store.File)
	for _, f := range files {
		m[f.Path] = f
	}
	return m
}

// indexPhase returns the run's index PhaseStarted, if it had one.
func (c *collector) indexPhase() (PhaseStarted, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, e := range c.events {
		if p, ok := e.(PhaseStarted); ok && p.Phase == PhaseIndex {
			return p, true
		}
	}
	return PhaseStarted{}, false
}

// libraryWithFixtures builds a library holding the tagged FLAC fixture in its
// place, an untagged MP3 in _Untagged and an unindexed cover image.
func libraryWithFixtures(t *testing.T) (lib, flac, mp3 string) {
	t.Helper()
	lib = filepath.Join(t.TempDir(), "lib")
	flac = manualLibraryPath(lib, ".flac")
	writeTestFile(t, flac, readTestFile(t, fixtures.Tagged(".flac")))
	mp3 = filepath.Join(lib, "_Untagged", "loose.mp3")
	writeTestFile(t, mp3, readTestFile(t, fixtures.Untagged(".mp3")))
	writeTestFile(t, filepath.Join(filepath.Dir(flac), "cover.jpg"), "art")
	return lib, flac, mp3
}

func TestIndexOnlyIndexesLibraryAndMovesNothing(t *testing.T) {
	lib, flac, mp3 := libraryWithFixtures(t)
	// Misplaced, but an index run moves nothing.
	misplaced := filepath.Join(lib, "misplaced.mp3")
	writeTaggedMP3(t, misplaced, id3Tags{albumArtist: "Singer", album: "Album", title: "Song", track: "1"}, "audio")
	source := filepath.Join(t.TempDir(), "src")
	unindexed := filepath.Join(source, "song.mp3")
	writeTestFile(t, unindexed, "no tags")

	s := openStore(t)
	var c collector
	finish := Run(context.Background(), Options{Source: source, Destination: lib, IndexOnly: true, Workers: 2, Store: s, Sink: c.sink})
	if finish.State != StateOK || finish.Counters.Indexed != 3 || finish.Counters.TotalFound != 3 {
		t.Fatalf("finish = %+v, want ok with 3 found and indexed", finish)
	}
	if got, want := c.phases(), []Phase{PhaseWalk, PhaseIndex}; fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("phases = %v, want %v", got, want)
	}
	if p, _ := c.indexPhase(); !p.First || p.Files != 3 || p.Bytes == 0 {
		t.Errorf("index phase = %+v, want the first index of 3 files with their bytes", p)
	}
	for _, path := range []string{misplaced, unindexed, flac, mp3} {
		if _, err := os.Stat(path); err != nil {
			t.Errorf("index run moved %q: %v", path, err)
		}
	}

	run := onlyRun(t, s)
	if run.Kind != store.KindIndex || run.Source != "" || run.State != store.StateOK || run.Counters.Indexed != 3 {
		t.Errorf("run = %+v, want an ok index run with 3 indexed", run)
	}
	if moves := movesOf(t, s, run.ID); len(moves) != 0 {
		t.Errorf("index run recorded moves: %+v", moves)
	}

	files := indexedFiles(t, s)
	if len(files) != 3 {
		t.Fatalf("index = %v, want the library's three audio files", files)
	}
	if _, ok := files[unindexed]; ok {
		t.Error("an index run indexed the source")
	}
	f := files[flac]
	info, err := os.Stat(flac)
	if err != nil {
		t.Fatal(err)
	}
	want := wantSum(t, flac)
	if f.Format != "FLAC" || f.Title != fixtures.Title || f.Artist != fixtures.Artist || f.AlbumArtist != fixtures.AlbumArtist ||
		f.Album != fixtures.Album || f.Track != fixtures.Track || f.TrackTotal != fixtures.TrackTotal ||
		f.Disc != fixtures.Disc || f.DiscTotal != fixtures.DiscTotal || f.Year != fixtures.Year ||
		f.ReleaseID != fixtures.ReleaseID || f.ReleaseTrackID != fixtures.ReleaseTrackID || f.RecordingID != fixtures.RecordingID ||
		f.Manual != "" || f.Sum != want || f.Size != info.Size() || !f.ModTime.Equal(info.ModTime()) || f.ReadError != "" {
		t.Errorf("FLAC row = %+v", f)
	}
	if !f.Lossless || f.SampleRate != 8000 || f.BitDepth != 16 || f.Channels != 1 || f.Length != time.Second {
		t.Errorf("FLAC properties = lossless %t, %d Hz, %d bit, %d ch, %v", f.Lossless, f.SampleRate, f.BitDepth, f.Channels, f.Length)
	}
	if f.TagsJSON == "" || f.TagsJSON == "{}" {
		t.Errorf("FLAC tags_json = %q, want the raw tags", f.TagsJSON)
	}
	if m := files[mp3]; m.Lossless || m.Bitrate == 0 || m.Sum == "" {
		t.Errorf("MP3 row = %+v, want lossy with a bitrate and a hash", m)
	}

	// Nothing changed: the next index run reads nothing.
	var again collector
	finish = Run(context.Background(), Options{Destination: lib, IndexOnly: true, Store: s, Sink: again.sink})
	if finish.Counters.Indexed != 0 {
		t.Errorf("second index run read %d files, want 0", finish.Counters.Indexed)
	}
	if p, ok := again.indexPhase(); !ok || p.First || p.Files != 0 {
		t.Errorf("second index phase = %+v, %t; want an empty one", p, ok)
	}
}

func wantSum(t *testing.T, path string) string {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	sum, err := tag.Sum(f)
	if err != nil {
		t.Fatal(err)
	}
	return sum
}

func TestIndexFreshness(t *testing.T) {
	lib, flac, mp3 := libraryWithFixtures(t)
	other := filepath.Join(lib, "_Untagged", "other.mp3")
	writeTestFile(t, other, "no tags")
	s := openStore(t)
	index := func(rescan bool) RunFinished {
		t.Helper()
		finish := Run(context.Background(), Options{Destination: lib, IndexOnly: true, Rescan: rescan, Store: s})
		if finish.State != StateOK {
			t.Fatalf("index run = %+v", finish)
		}
		return finish
	}
	index(false)
	before := indexedFiles(t, s)

	// One file changes, one goes, one arrives.
	writeTestFile(t, mp3, readTestFile(t, fixtures.Tagged(".mp3")))
	later := time.Now().Add(time.Hour)
	if err := os.Chtimes(mp3, later, later); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(other); err != nil {
		t.Fatal(err)
	}
	added := filepath.Join(lib, "_Untagged", "new.ogg")
	writeTestFile(t, added, readTestFile(t, fixtures.Untagged(".ogg")))

	if got := index(false).Counters.Indexed; got != 2 {
		t.Errorf("after one change and one addition, read %d files, want 2", got)
	}
	after := indexedFiles(t, s)
	if len(after) != 3 {
		t.Errorf("index = %v, want flac, mp3 and the new ogg", after)
	}
	if _, ok := after[other]; ok {
		t.Error("vanished file kept its row")
	}
	if after[flac] != before[flac] {
		t.Errorf("unchanged FLAC row changed:\n%+v\n%+v", before[flac], after[flac])
	}
	if m := after[mp3]; m.ID != before[mp3].ID || m.Title != fixtures.Title || !m.ModTime.Equal(later) {
		t.Errorf("changed MP3 row = %+v, want re-read in place", m)
	}

	if got := index(true).Counters.Indexed; got != 3 {
		t.Errorf("rescan read %d files, want all 3", got)
	}
}

func TestOrganizeIndexesSourceAndMovesRowsInPlace(t *testing.T) {
	lib, flac, _ := libraryWithFixtures(t)
	source := filepath.Join(t.TempDir(), "src")
	song := filepath.Join(source, "Rip", "song.m4a")
	writeTestFile(t, song, readTestFile(t, fixtures.Tagged(".m4a")))
	loose := filepath.Join(source, "loose.wav")
	writeTestFile(t, loose, readTestFile(t, fixtures.Untagged(".wav")))

	s := openStore(t)
	// A dry run indexes the source and the library but moves nothing.
	var dry collector
	if finish := Run(context.Background(), Options{Source: source, Destination: lib, DryRun: true, Store: s, Sink: dry.sink}); finish.Counters.Indexed != 4 {
		t.Fatalf("dry run = %+v, want 4 files indexed", finish)
	}
	if got, want := dry.phases(), []Phase{PhaseWalk, PhaseIndex, PhaseAudio, PhaseCompanions, PhaseCue}; fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("phases = %v, want %v", got, want)
	}
	before := indexedFiles(t, s)

	var c collector
	finish := Run(context.Background(), Options{Source: source, Destination: lib, Store: s, Sink: c.sink})
	if finish.State != StateOK || finish.Counters.Organized != 1 || finish.Counters.Untagged != 1 {
		t.Fatalf("run = %+v, want one organized and one untagged", finish)
	}
	if finish.Counters.Indexed != 0 {
		t.Errorf("real run re-read %d files the dry run indexed", finish.Counters.Indexed)
	}
	if _, ok := c.indexPhase(); ok {
		t.Error("a run with nothing to index had an index phase")
	}

	after := indexedFiles(t, s)
	if len(after) != 4 {
		t.Fatalf("index = %v, want 4 files", after)
	}
	moved := map[string]string{
		song:  manualLibraryPath(lib, ".m4a"),
		loose: filepath.Join(lib, "_Untagged", "loose.wav"),
	}
	for from, to := range moved {
		if _, ok := after[from]; ok {
			t.Errorf("row left at %q after the move", from)
		}
		if got := after[to]; got.ID != before[from].ID || got.Sum != before[from].Sum || got.Sum == "" {
			t.Errorf("row at %q = %+v, want the row of %q moved in place", to, got, from)
		}
	}
	if after[flac] != before[flac] {
		t.Error("library file's row changed")
	}

	// The next run reads nothing.
	if finish := Run(context.Background(), Options{Source: source, Destination: lib, Store: s}); finish.Counters.Indexed != 0 {
		t.Errorf("third run read %d files, want 0", finish.Counters.Indexed)
	}
}

func TestReorganizeMovesRowsInPlace(t *testing.T) {
	lib := filepath.Join(t.TempDir(), "lib")
	misplaced := filepath.Join(lib, "misplaced.flac")
	writeTestFile(t, misplaced, readTestFile(t, fixtures.Tagged(".flac")))
	s := openStore(t)
	if finish := Run(context.Background(), Options{Destination: lib, IndexOnly: true, Store: s}); finish.Counters.Indexed != 1 {
		t.Fatalf("index run = %+v", finish)
	}
	before := indexedFiles(t, s)[misplaced]

	finish := Run(context.Background(), Options{Source: lib, Destination: lib, Store: s})
	if finish.Counters.Organized != 1 || finish.Counters.Indexed != 0 {
		t.Fatalf("reorganize = %+v, want the file moved and nothing re-read", finish)
	}
	after := indexedFiles(t, s)
	if got := after[manualLibraryPath(lib, ".flac")]; len(after) != 1 || got.ID != before.ID {
		t.Errorf("index after reorganize = %+v, want row %d moved", after, before.ID)
	}
}

func TestIndexInterruptedKeepsCommittedAndResumes(t *testing.T) {
	lib := filepath.Join(t.TempDir(), "lib")
	const total = 12
	for i := range total {
		writeTestFile(t, filepath.Join(lib, "_Untagged", fmt.Sprintf("%02d.mp3", i)), fmt.Sprintf("no tags %d", i))
	}
	s := openStore(t)

	oldFiles, oldHook := indexBatchFiles, afterIndexRead
	defer func() { indexBatchFiles, afterIndexRead = oldFiles, oldHook }()
	indexBatchFiles = 2
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var read atomic.Int64
	afterIndexRead = func(string) {
		if read.Add(1) == 5 {
			cancel()
		}
	}

	finish := Run(ctx, Options{Destination: lib, IndexOnly: true, Workers: 1, Store: s})
	if finish.State != StateInterrupted {
		t.Fatalf("interrupted index = %+v", finish)
	}
	kept, err := s.CountFiles()
	if err != nil {
		t.Fatal(err)
	}
	if kept < 5 || kept >= total || kept != finish.Counters.Indexed {
		t.Fatalf("interrupted index kept %d rows and counted %d, want the 5 or 6 read", kept, finish.Counters.Indexed)
	}
	if run := onlyRun(t, s); run.State != store.StateInterrupted || run.Counters.Indexed != kept {
		t.Errorf("run = %+v", run)
	}

	afterIndexRead = oldHook
	var c collector
	finish = Run(context.Background(), Options{Destination: lib, IndexOnly: true, Store: s, Sink: c.sink})
	if finish.State != StateOK || finish.Counters.Indexed != total-kept {
		t.Errorf("resumed index = %+v, want the remaining %d read", finish, total-kept)
	}
	if p, _ := c.indexPhase(); p.First {
		t.Error("a resumed index claims to be the first")
	}
	if n, _ := s.CountFiles(); n != total {
		t.Errorf("index holds %d files after resuming, want %d", n, total)
	}
}

func TestIndexBatchesByTime(t *testing.T) {
	lib := filepath.Join(t.TempDir(), "lib")
	for i := range 3 {
		writeTestFile(t, filepath.Join(lib, fmt.Sprintf("%d.mp3", i)), "no tags")
	}
	s := openStore(t)
	oldInterval, oldHook := indexBatchInterval, afterIndexRead
	defer func() { indexBatchInterval, afterIndexRead = oldInterval, oldHook }()
	indexBatchInterval = time.Millisecond
	var committed []int64
	afterIndexRead = func(string) {
		// Give the batch timer a chance before the next file.
		deadline := time.Now().Add(time.Second)
		for {
			n, _ := s.CountFiles()
			if n > int64(len(committed)) || time.Now().After(deadline) {
				committed = append(committed, n)
				return
			}
			time.Sleep(time.Millisecond)
		}
	}
	Run(context.Background(), Options{Destination: lib, IndexOnly: true, Workers: 1, Store: s})
	if fmt.Sprint(committed) != "[1 2 3]" {
		t.Errorf("rows committed after each file = %v, want [1 2 3]", committed)
	}
}

func TestIndexReadsManualMarker(t *testing.T) {
	lib := filepath.Join(t.TempDir(), "lib")
	manual := filepath.Join(lib, "_Untagged", "manual.flac")
	writeTestFile(t, manual, readTestFile(t, fixtures.Manual(".flac")))
	s := openStore(t)
	Run(context.Background(), Options{Destination: lib, IndexOnly: true, Store: s})
	if got := indexedFiles(t, s)[manual]; got.Manual != fixtures.ManualMarker || got.ReleaseTrackID != "" {
		t.Errorf("manual row = %+v, want marker %q and no MusicBrainz IDs", got, fixtures.ManualMarker)
	}
}

func TestRunWithoutStoreKeepsNoIndex(t *testing.T) {
	tmp := t.TempDir()
	source := filepath.Join(tmp, "src")
	writeTestFile(t, filepath.Join(source, "loose.mp3"), "no tags")
	var c collector
	Run(context.Background(), Options{Source: source, Destination: filepath.Join(tmp, "lib"), Sink: c.sink})
	if _, ok := c.indexPhase(); ok {
		t.Error("a run without a store indexed")
	}
}
