package main

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/zachjamesgreen/tagshelf/internal/acoustid"
	"github.com/zachjamesgreen/tagshelf/internal/config"
	"github.com/zachjamesgreen/tagshelf/internal/fingerprint"
	"github.com/zachjamesgreen/tagshelf/internal/store"
)

// soundOn is a sound service on an indexed library, with stand-ins for the
// listening and for AcoustID that count how often they were asked.
func soundOn(t *testing.T, key string) (s *sound, lib string, listened, asked *int, clock *time.Time) {
	t.Helper()
	_, args, logs, _ := artLibrary(t)
	lib = args[3]
	_ = logs
	listened, asked = new(int), new(int)
	at := time.Date(2026, 10, 12, 8, 0, 0, 0, time.UTC)
	clock = &at
	svc := newSound(config.Config{Destination: lib, AcoustIDKey: key})
	svc.print = func(_ context.Context, path string) (fingerprint.Print, error) {
		*listened++
		return fingerprint.Print{Fingerprint: "AQAA-" + filepath.Base(path), Duration: 187}, nil
	}
	svc.lookup = func(_ context.Context, key, print string, duration int) ([]acoustid.Result, error) {
		*asked++
		if key != "my-key" || print == "" || duration != 187 {
			t.Errorf("AcoustID was asked with key %q, fingerprint %q and length %d", key, print, duration)
		}
		return []acoustid.Result{{ID: "aid-1", Score: 0.97, Recordings: []string{"rec-1"}}}, nil
	}
	svc.now = func() time.Time { return *clock }
	return &svc, lib, listened, asked, clock
}

// The fingerprint is kept for good and AcoustID's answer for thirty days:
// a file identified again is neither listened to nor asked about, until the
// answer is old or it is asked for again.
func TestSoundKeepsARecord(t *testing.T) {
	s, lib, listened, asked, clock := soundOn(t, "my-key")
	f := store.File{Path: filepath.Join(lib, "Artist", "Album", "01.flac"), Sum: "sum-1"}
	want := []acoustid.Result{{ID: "aid-1", Score: 0.97, Recordings: []string{"rec-1"}}}

	heard, err := s.Identify(context.Background(), f, false)
	if err != nil || !reflect.DeepEqual(heard.Results, want) || heard.FromRecord || !heard.Asked.Equal(*clock) {
		t.Fatalf("Identify = %+v, %v", heard, err)
	}
	if *listened != 1 || *asked != 1 {
		t.Fatalf("listened %d times, asked %d times", *listened, *asked)
	}

	first := *clock
	*clock = clock.Add(29 * 24 * time.Hour)
	heard, err = s.Identify(context.Background(), f, false)
	if err != nil || !reflect.DeepEqual(heard.Results, want) || !heard.FromRecord || !heard.Asked.Equal(first) {
		t.Errorf("29 days on: %+v, %v; want the answer on record, with when it was given", heard, err)
	}
	if *listened != 1 || *asked != 1 {
		t.Errorf("29 days on: listened %d times, asked %d times, want neither again", *listened, *asked)
	}

	// Asked for again: AcoustID is asked, the file is not listened to.
	if heard, err := s.Identify(context.Background(), f, true); err != nil || heard.FromRecord || *listened != 1 || *asked != 2 {
		t.Errorf("again: %+v, %v; listened %d, asked %d", heard, err, *listened, *asked)
	}
	// And once the answer is old.
	*clock = clock.Add(31 * 24 * time.Hour)
	if heard, err := s.Identify(context.Background(), f, false); err != nil || heard.FromRecord || *listened != 1 || *asked != 3 {
		t.Errorf("31 days after the last answer: %+v, %v; listened %d, asked %d", heard, err, *listened, *asked)
	}

	// The same audio under another name is the same record.
	moved := store.File{Path: filepath.Join(lib, "Artist", "Album", "renamed.flac"), Sum: "sum-1"}
	if heard, err := s.Identify(context.Background(), moved, false); err != nil || !heard.FromRecord || *listened != 1 {
		t.Errorf("under another name: %+v, %v; listened %d", heard, err, *listened)
	}
}

// Without a key nothing is listened to; a file without a hash, or outside
// any library, is identified and nothing is kept; and a failure keeps the
// fingerprint, so that the file is not listened to twice.
func TestSoundWithoutAKeyOrARecord(t *testing.T) {
	old := acoustid.Key("")
	if old != "" {
		t.Skip("this build carries a key")
	}
	s, lib, listened, asked, _ := soundOn(t, "")
	f := store.File{Path: filepath.Join(lib, "Artist", "Album", "01.flac"), Sum: "sum-1"}
	if _, err := s.Identify(context.Background(), f, false); !errors.Is(err, acoustid.ErrNoKey) || *listened != 0 || *asked != 0 {
		t.Errorf("without a key: %v; listened %d, asked %d", err, *listened, *asked)
	}

	s.cfg.AcoustIDKey = "my-key"
	noSum := store.File{Path: f.Path}
	for range 2 {
		if _, err := s.Identify(context.Background(), noSum, false); err != nil {
			t.Fatal(err)
		}
	}
	if *listened != 2 || *asked != 2 {
		t.Errorf("a file without a hash: listened %d, asked %d, want both each time", *listened, *asked)
	}

	// AcoustID down: the fingerprint is kept, and the next try only asks.
	down := errors.New("no route")
	ask := s.lookup
	s.lookup = func(context.Context, string, string, int) ([]acoustid.Result, error) { return nil, down }
	if _, err := s.Identify(context.Background(), f, false); !errors.Is(err, down) || *listened != 3 {
		t.Fatalf("with AcoustID down: %v, listened %d", err, *listened)
	}
	s.lookup = ask
	if heard, err := s.Identify(context.Background(), f, false); err != nil || heard.FromRecord || *listened != 3 || *asked != 3 {
		t.Errorf("after it came back: %+v, %v; listened %d, asked %d", heard, err, *listened, *asked)
	}

	// A folder that is no library: identified, and no library is made of it.
	dir := t.TempDir()
	s.cfg.Destination = dir
	if _, err := s.Identify(context.Background(), store.File{Path: filepath.Join(dir, "a.mp3"), Sum: "sum-9"}, false); err != nil {
		t.Fatal(err)
	}
	if id, err := store.ReadLibraryID(dir); err != nil || id != "" {
		t.Errorf("identifying a file made a library of its folder: %q, %v", id, err)
	}
	if entries := listDir(t, dir); len(entries) != 0 {
		t.Errorf("identifying a file left %v in the folder", entries)
	}
}

func listDir(t *testing.T, dir string) []string {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(dir, "*"))
	if err != nil {
		t.Fatal(err)
	}
	hidden, _ := filepath.Glob(filepath.Join(dir, ".*"))
	return slices.Concat(matches, hidden)
}
