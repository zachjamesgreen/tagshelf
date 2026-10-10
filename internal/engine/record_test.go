package engine

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/zachjamesgreen/tagshelf/internal/store"
)

// openStore opens a real database in a temp folder, closed when the test ends.
func openStore(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.Open(filepath.Join(t.TempDir(), "tagshelf.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// onlyRun returns the single run recorded in s.
func onlyRun(t *testing.T, s *store.Store) store.Run {
	t.Helper()
	runs, err := s.Runs()
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 1 {
		t.Fatalf("recorded %d runs, want 1: %+v", len(runs), runs)
	}
	return runs[0]
}

// movesOf returns a run's moves without their times, sequence numbers and
// cue sheet texts, sorted by source path, since workers record them in no
// particular order.
func movesOf(t *testing.T, s *store.Store, runID int64) []store.Move {
	t.Helper()
	moves, err := s.Moves(runID)
	if err != nil {
		t.Fatal(err)
	}
	for i := range moves {
		if moves[i].At.IsZero() {
			t.Errorf("move %+v has no time", moves[i])
		}
		if moves[i].Seq == 0 {
			t.Errorf("move %+v has no sequence number", moves[i])
		}
		moves[i] = store.Move{From: moves[i].From, To: moves[i].To, Reason: moves[i].Reason}
	}
	sort.Slice(moves, func(i, j int) bool { return moves[i].From < moves[j].From })
	return moves
}

func TestRunRecordsRunAndMoves(t *testing.T) {
	tmpDir := t.TempDir()
	source := filepath.Join(tmpDir, "src")
	dest := filepath.Join(tmpDir, "lib")
	song := filepath.Join(source, "Rip", "song.mp3")
	cue := filepath.Join(source, "Rip", "album.cue")
	loose := filepath.Join(source, "loose.mp3")
	notes := filepath.Join(source, "Notes", "readme.txt")
	writeTaggedMP3(t, song, id3Tags{albumArtist: "Singer", album: "Album", title: "Song", track: "1"}, "audio")
	writeTestFile(t, cue, "FILE \"song.mp3\" MP3\n")
	writeTestFile(t, loose, "no tags")
	writeTestFile(t, notes, "notes")

	s := openStore(t)
	finish := Run(context.Background(), Options{Source: source, Destination: dest, Workers: 2, Store: s})
	if finish.State != StateOK {
		t.Fatalf("run ended %v: %+v", finish.State, finish)
	}

	run := onlyRun(t, s)
	if run.Kind != store.KindOrganize || run.DryRun || run.Source != source || run.Destination != dest {
		t.Errorf("run = %+v, want a real organize of %q into %q", run, source, dest)
	}
	if run.State != store.StateOK || run.Ended.IsZero() || run.Ended.Before(run.Started) {
		t.Errorf("run = %+v, want finished ok", run)
	}
	if run.Counters != store.Counters(finish.Counters) {
		t.Errorf("recorded counters %+v, want %+v", run.Counters, finish.Counters)
	}

	album := filepath.Join(dest, "Singer", "Album")
	want := []store.Move{
		{From: filepath.Join(album, "album.cue"), To: filepath.Join(album, "album.cue.orig"), Reason: store.ReasonCueRewrite},
		{From: notes, To: filepath.Join(dest, "_Other", "Notes", "readme.txt"), Reason: store.ReasonOther},
		{From: cue, To: filepath.Join(album, "album.cue"), Reason: store.ReasonCompanion},
		{From: song, To: filepath.Join(album, "01 - Song.mp3"), Reason: store.ReasonOrganized},
		{From: loose, To: filepath.Join(dest, "_Untagged", "loose.mp3"), Reason: store.ReasonUntagged},
	}
	sort.Slice(want, func(i, j int) bool { return want[i].From < want[j].From })
	if got := movesOf(t, s, run.ID); !reflect.DeepEqual(got, want) {
		t.Errorf("moves:\ngot  %+v\nwant %+v", got, want)
	}
	if errs, err := s.RunErrors(run.ID); err != nil || len(errs) != 0 {
		t.Errorf("errors = %+v, %v; want none", errs, err)
	}
}

func TestRunRecordsErrors(t *testing.T) {
	tmpDir := t.TempDir()
	source := filepath.Join(tmpDir, "src")
	song := filepath.Join(source, "loose.mp3")
	writeTestFile(t, song, "no tags")
	untagged := filepath.Join(tmpDir, "untagged")
	writeTestFile(t, untagged, "in the way")

	s := openStore(t)
	Run(context.Background(), Options{Source: source, Destination: filepath.Join(tmpDir, "lib"), Untagged: untagged, Workers: 1, Store: s})

	run := onlyRun(t, s)
	if run.State != store.StateErrors || run.Counters.Errors != 1 {
		t.Errorf("run = %+v, want state errors with 1 error", run)
	}
	errs, err := s.RunErrors(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(errs) != 1 || errs[0].Path != song || !strings.Contains(errs[0].Message, "untagged") {
		t.Errorf("errors = %+v, want one for %q naming the destination", errs, song)
	}
	if moves := movesOf(t, s, run.ID); len(moves) != 0 {
		t.Errorf("moves = %+v, want none", moves)
	}
}

func TestRunRecordsDryRun(t *testing.T) {
	tmpDir := t.TempDir()
	source := filepath.Join(tmpDir, "src")
	writeTestFile(t, filepath.Join(source, "loose.mp3"), "no tags")

	s := openStore(t)
	Run(context.Background(), Options{Source: source, Destination: filepath.Join(tmpDir, "lib"), Workers: 1, DryRun: true, Store: s})

	run := onlyRun(t, s)
	if !run.DryRun || run.State != store.StateOK || run.Counters.Untagged != 1 {
		t.Errorf("run = %+v, want a dry run, ok, with 1 untagged", run)
	}
	if moves := movesOf(t, s, run.ID); len(moves) != 0 {
		t.Errorf("a dry run recorded moves: %+v", moves)
	}
}

func TestRunRecordsRenamed(t *testing.T) {
	lib, _, _ := swappedLibrary(t)

	s := openStore(t)
	Run(context.Background(), Options{Source: lib, Destination: lib, Workers: 1, Store: s})

	run := onlyRun(t, s)
	if run.Kind != store.KindReorganize || run.State != store.StateOK {
		t.Errorf("run = %+v, want an ok reorganize", run)
	}
	moves := movesOf(t, s, run.ID)
	var organized, renamed int
	for _, m := range moves {
		switch m.Reason {
		case store.ReasonOrganized:
			organized++
		case store.ReasonRenamed:
			renamed++
			if !strings.Contains(filepath.Base(m.From), "(1)") {
				t.Errorf("renamed move %+v does not come from a suffixed name", m)
			}
		}
	}
	if organized != 2 || renamed != 1 || len(moves) != 3 {
		t.Errorf("moves = %+v, want 2 organized and 1 renamed", moves)
	}
}

func TestRunRecordsInterrupted(t *testing.T) {
	tmpDir := t.TempDir()
	source := filepath.Join(tmpDir, "src")
	writeTestFile(t, filepath.Join(source, "loose.mp3"), "no tags")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	s := openStore(t)
	Run(ctx, Options{Source: source, Destination: filepath.Join(tmpDir, "lib"), Workers: 1, Store: s})

	if run := onlyRun(t, s); run.State != store.StateInterrupted {
		t.Errorf("run = %+v, want interrupted", run)
	}
}

func TestRunNotStartedWhenStoreFails(t *testing.T) {
	tmpDir := t.TempDir()
	source := filepath.Join(tmpDir, "src")
	song := filepath.Join(source, "loose.mp3")
	writeTestFile(t, song, "no tags")
	s := openStore(t)
	s.Close() // every write now fails

	var c collector
	finish := Run(context.Background(), Options{Source: source, Destination: filepath.Join(tmpDir, "lib"), Workers: 1, Store: s, Sink: c.sink})

	if finish.State != StateNotStarted || finish.Err == nil {
		t.Errorf("RunFinished = %+v, want StateNotStarted with the error", finish)
	}
	if _, err := os.Stat(song); err != nil {
		t.Errorf("the song moved although the run could not be recorded: %v", err)
	}
	if _, ok := c.events[0].(RunStarted); !ok {
		t.Errorf("first event = %T, want RunStarted", c.events[0])
	}
	if last := c.lastFinished(t); last.State != StateNotStarted {
		t.Errorf("last event = %+v, want StateNotStarted", last)
	}
}

func TestWalkSkipsLibraryIDFile(t *testing.T) {
	t.Run("organize", func(t *testing.T) {
		tmpDir := t.TempDir()
		source := filepath.Join(tmpDir, "src")
		dest := filepath.Join(tmpDir, "lib")
		// The source is itself some other library, with its ID file at the
		// root and in a folder of its own.
		rootID := filepath.Join(source, store.LibraryIDFile)
		nestedID := filepath.Join(source, "Old", store.LibraryIDFile)
		writeTestFile(t, rootID, "id")
		writeTestFile(t, nestedID, "id")
		writeTestFile(t, filepath.Join(source, "Old", "loose.mp3"), "no tags")

		counters := Run(context.Background(), Options{Source: source, Destination: dest, Workers: 1}).Counters

		if counters.OtherFound != 0 || counters.Untagged != 1 {
			t.Errorf("counters = %+v, want the ID files ignored and 1 untagged", counters)
		}
		readTestFile(t, rootID)
		readTestFile(t, nestedID)
	})

	t.Run("reorganize", func(t *testing.T) {
		lib := t.TempDir()
		id := filepath.Join(lib, store.LibraryIDFile)
		writeTestFile(t, id, "id")
		old := filepath.Join(lib, "Old Place")
		writeTaggedMP3(t, filepath.Join(old, "song.mp3"), id3Tags{albumArtist: "Singer", album: "Album", title: "Song", track: "1"}, "audio")

		counters := Run(context.Background(), Options{Source: lib, Destination: lib, Workers: 1}).Counters

		if counters.OtherFound != 0 || counters.Organized != 1 {
			t.Errorf("counters = %+v, want the ID file ignored and 1 organized", counters)
		}
		readTestFile(t, id)
		if _, err := os.Stat(old); !os.IsNotExist(err) {
			t.Errorf("the emptied folder was not cleaned up: %v", err)
		}
	})
}

func TestRunRecordsProgressForOtherProcesses(t *testing.T) {
	old := progressRecordInterval
	progressRecordInterval = time.Millisecond
	t.Cleanup(func() { progressRecordInterval = old })

	tmpDir := t.TempDir()
	source := filepath.Join(tmpDir, "src")
	dest := filepath.Join(tmpDir, "lib")
	for _, name := range []string{"a.mp3", "b.mp3", "c.mp3"} {
		writeTestFile(t, filepath.Join(source, name), "no tags "+name)
	}
	st := openStore(t)

	// While files are placed, read the run's row the way the TUI would.
	var seen []store.RunProgress
	sink := func(e Event) {
		if _, ok := e.(FilePlaced); !ok {
			return
		}
		time.Sleep(20 * time.Millisecond)
		a, err := st.ActiveRun(0)
		if err != nil {
			t.Error(err)
			return
		}
		if a == nil {
			t.Error("no running run while files are being placed")
			return
		}
		seen = append(seen, a.Progress)
	}
	Run(context.Background(), Options{Source: source, Destination: dest, Workers: 1, Store: st, Sink: sink})

	var sawAudio bool
	for _, p := range seen {
		if p.Phase == "audio" {
			sawAudio = true
			if p.Total != 3 || p.Done > 3 || p.At.IsZero() {
				t.Errorf("audio progress = %+v, want up to 3 of 3 files, with a time", p)
			}
		}
	}
	if !sawAudio {
		t.Errorf("progress seen while running = %+v, want some from the audio phase", seen)
	}
}
