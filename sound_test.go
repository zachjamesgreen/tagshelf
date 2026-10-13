package main

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"github.com/zachjamesgreen/tagshelf/internal/acoustid"
	"github.com/zachjamesgreen/tagshelf/internal/config"
	"github.com/zachjamesgreen/tagshelf/internal/fingerprint"
	"github.com/zachjamesgreen/tagshelf/internal/store"
	"github.com/zachjamesgreen/tagshelf/internal/tui"
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

	heard, err := identify(s, f, false)
	if err != nil || !reflect.DeepEqual(heard.Results, want) || heard.FromRecord || !heard.Asked.Equal(*clock) {
		t.Fatalf("Identify = %+v, %v", heard, err)
	}
	if *listened != 1 || *asked != 1 {
		t.Fatalf("listened %d times, asked %d times", *listened, *asked)
	}

	first := *clock
	*clock = clock.Add(29 * 24 * time.Hour)
	heard, err = identify(s, f, false)
	if err != nil || !reflect.DeepEqual(heard.Results, want) || !heard.FromRecord || !heard.Asked.Equal(first) {
		t.Errorf("29 days on: %+v, %v; want the answer on record, with when it was given", heard, err)
	}
	if *listened != 1 || *asked != 1 {
		t.Errorf("29 days on: listened %d times, asked %d times, want neither again", *listened, *asked)
	}

	// Asked for again: AcoustID is asked, the file is not listened to.
	if heard, err := identify(s, f, true); err != nil || heard.FromRecord || *listened != 1 || *asked != 2 {
		t.Errorf("again: %+v, %v; listened %d, asked %d", heard, err, *listened, *asked)
	}
	// And once the answer is old.
	*clock = clock.Add(31 * 24 * time.Hour)
	if heard, err := identify(s, f, false); err != nil || heard.FromRecord || *listened != 1 || *asked != 3 {
		t.Errorf("31 days after the last answer: %+v, %v; listened %d, asked %d", heard, err, *listened, *asked)
	}

	// The same audio under another name is the same record.
	moved := store.File{Path: filepath.Join(lib, "Artist", "Album", "renamed.flac"), Sum: "sum-1"}
	if heard, err := identify(s, moved, false); err != nil || !heard.FromRecord || *listened != 1 {
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
	if _, err := identify(s, f, false); !errors.Is(err, acoustid.ErrNoKey) || *listened != 0 || *asked != 0 {
		t.Errorf("without a key: %v; listened %d, asked %d", err, *listened, *asked)
	}

	s.cfg.AcoustIDKey = "my-key"
	noSum := store.File{Path: f.Path}
	for range 2 {
		if _, err := identify(s, noSum, false); err != nil {
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
	if _, err := identify(s, f, false); !errors.Is(err, down) || *listened != 3 {
		t.Fatalf("with AcoustID down: %v, listened %d", err, *listened)
	}
	s.lookup = ask
	if heard, err := identify(s, f, false); err != nil || heard.FromRecord || *listened != 3 || *asked != 3 {
		t.Errorf("after it came back: %+v, %v; listened %d, asked %d", heard, err, *listened, *asked)
	}

	// A folder that is no library: identified, and no library is made of it.
	dir := t.TempDir()
	s.cfg.Destination = dir
	if _, err := identify(s, store.File{Path: filepath.Join(dir, "a.mp3"), Sum: "sum-9"}, false); err != nil {
		t.Fatal(err)
	}
	if id, err := store.ReadLibraryID(dir); err != nil || id != "" {
		t.Errorf("identifying a file made a library of its folder: %q, %v", id, err)
	}
	if entries := listDir(t, dir); len(entries) != 0 {
		t.Errorf("identifying a file left %v in the folder", entries)
	}
}

// identify is what the sound service makes of the one file f.
func identify(s *sound, f store.File, again bool) (heard tui.Heard, err error) {
	s.Identify(context.Background(), []store.File{f}, again, func(_ int, h tui.Heard, e error) bool {
		heard, err = h, e
		return true
	})
	return heard, err
}

// A fingerprint on record without a length is one AcoustID cannot be asked
// with: the file is listened to again, and what that gives takes its place.
func TestSoundListensAgainToARecordWithoutALength(t *testing.T) {
	s, lib, listened, asked, clock := soundOn(t, "my-key")
	st, err := store.OpenLibrary(lib, "", false)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetFingerprint(store.Fingerprint{Sum: "sum-1", Duration: 0, Fingerprint: "AQAA-old"}, *clock); err != nil {
		t.Fatal(err)
	}
	st.Close()

	f := store.File{Path: filepath.Join(lib, "Artist", "Album", "01.flac"), Sum: "sum-1"}
	if heard, err := identify(s, f, false); err != nil || len(heard.Results) != 1 || *listened != 1 || *asked != 1 {
		t.Fatalf("Identify = %+v, %v; listened %d, asked %d", heard, err, *listened, *asked)
	}
	if rec, ok := s.onRecord("sum-1"); !ok || rec.Duration != 187 || rec.Fingerprint != "AQAA-01.flac" || rec.Answer == nil {
		t.Errorf("on record afterwards: %+v, %t", rec, ok)
	}
}

// Many files are listened to several at once, each is answered once, and all
// of it is on record afterwards, read through the database the session has
// open. Once the caller has heard enough, nothing more is said.
func TestSoundIdentifiesManyFiles(t *testing.T) {
	s, lib, _, _, _ := soundOn(t, "my-key")
	reader, err := store.OpenLibraryReader(lib, "")
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	s.reader = reader

	var listening, most, listened, asked atomic.Int32
	s.print = func(_ context.Context, path string) (fingerprint.Print, error) {
		n := listening.Add(1)
		defer listening.Add(-1)
		for old := most.Load(); n > old && !most.CompareAndSwap(old, n); old = most.Load() {
		}
		listened.Add(1)
		time.Sleep(2 * time.Millisecond)
		if filepath.Base(path) == "07.flac" {
			return fingerprint.Print{}, errors.New("it is not audio")
		}
		return fingerprint.Print{Fingerprint: "AQAA-" + filepath.Base(path), Duration: 187}, nil
	}
	s.lookup = func(_ context.Context, _, print string, _ int) ([]acoustid.Result, error) {
		asked.Add(1)
		return []acoustid.Result{{ID: "aid-" + print, Score: 0.9}}, nil
	}
	const n = 2*keepEvery + 10
	files := make([]store.File, n)
	for i := range files {
		files[i] = store.File{Path: filepath.Join(lib, fmt.Sprintf("%02d.flac", i)), Sum: fmt.Sprintf("sum-%02d", i)}
	}

	got := map[int]string{}
	s.Identify(context.Background(), files, false, func(i int, heard tui.Heard, err error) bool {
		// Never twice at once: the map is written without a lock.
		if _, twice := got[i]; twice {
			t.Errorf("file %d was answered twice", i)
		}
		got[i] = "failed"
		if err == nil && len(heard.Results) == 1 {
			got[i] = heard.Results[0].ID
		}
		return true
	})
	if len(got) != n || got[7] != "failed" || got[8] != "aid-AQAA-08.flac" || got[n-1] != fmt.Sprintf("aid-AQAA-%02d.flac", n-1) {
		t.Errorf("answers for %d of %d files; file 7 %q, file 8 %q, the last %q", len(got), n, got[7], got[8], got[n-1])
	}
	if listened.Load() != n || asked.Load() != n-1 {
		t.Errorf("listened %d times and asked %d times for %d files, one of them not audio", listened.Load(), asked.Load(), n)
	}
	if want := int32(min(maxListeners, runtime.GOMAXPROCS(0))); most.Load() > want || (want > 1 && most.Load() < 2) {
		t.Errorf("%d files were listened to at once, want up to %d", most.Load(), want)
	}
	for i, f := range files {
		rec, ok, err := reader.Fingerprint(f.Sum)
		if err != nil || ok != (i != 7) || (ok && rec.Answer == nil) {
			t.Errorf("on record for file %d: %+v, %t, %v", i, rec, ok, err)
		}
	}

	// All of it is on record now: nothing is listened to or asked again.
	fromRecord := 0
	s.Identify(context.Background(), files[:7], false, func(_ int, heard tui.Heard, err error) bool {
		if err == nil && heard.FromRecord {
			fromRecord++
		}
		return true
	})
	if fromRecord != 7 || listened.Load() != n || asked.Load() != n-1 {
		t.Errorf("a second time: %d of 7 answers from the record; listened %d, asked %d", fromRecord, listened.Load(), asked.Load())
	}

	// The caller stops it: no answer follows the one it stopped at.
	said := 0
	s.Identify(context.Background(), files, true, func(int, tui.Heard, error) bool {
		said++
		return false
	})
	if said != 1 {
		t.Errorf("after the caller stopped it, %d answers were given", said)
	}
	// And one that is cancelled says nothing at all.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	s.Identify(ctx, files, true, func(int, tui.Heard, error) bool {
		t.Error("an answer was given for a cancelled request")
		return true
	})
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
