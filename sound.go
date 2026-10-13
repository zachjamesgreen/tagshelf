package main

import (
	"context"
	"encoding/json"
	"runtime"
	"slices"
	"sync"
	"time"

	"github.com/zachjamesgreen/tagshelf/internal/acoustid"
	"github.com/zachjamesgreen/tagshelf/internal/config"
	"github.com/zachjamesgreen/tagshelf/internal/fingerprint"
	"github.com/zachjamesgreen/tagshelf/internal/store"
	"github.com/zachjamesgreen/tagshelf/internal/tui"
)

// answerFor is how long what AcoustID answered for a file is given again
// without asking: its database grows, and an answer of last year may have
// lacked the release.
const answerFor = 30 * 24 * time.Hour

// maxListeners is the most files listened to at once. Each takes a CPU and
// the memory of a decoder; AcoustID is asked one request at a time whatever
// this is.
const maxListeners = 4

// keepEvery is how many files' worth of listening is put on record in one
// write, so that many files cost the library a few writes and not one each.
const keepEvery = 25

// sound identifies files by their sound for the terminal UI: it computes the
// fingerprint, asks AcoustID, and keeps both on record in the library's
// database, by the hash of the audio.
type sound struct {
	cfg config.Config // with absolute paths
	// reader is the library's database as the session has it open, nil when
	// it has none: the record is then opened for each read.
	reader *store.Store

	// print computes a fingerprint and lookup asks AcoustID with a key. The
	// tests put stand-ins in their place: they make no requests.
	print  func(ctx context.Context, path string) (fingerprint.Print, error)
	lookup func(ctx context.Context, key, print string, duration int) ([]acoustid.Result, error)
	now    func() time.Time
}

// newSound is the sound service of a session with the configuration cfg.
func newSound(cfg config.Config) sound {
	return sound{cfg: cfg, print: fingerprint.Of, lookup: askAcoustID, now: time.Now}
}

// askAcoustID asks the real service.
func askAcoustID(ctx context.Context, key, print string, duration int) ([]acoustid.Result, error) {
	client, err := acoustid.New(key)
	if err != nil {
		return nil, err
	}
	return client.Lookup(ctx, print, duration)
}

// Identify implements tui.Sound. Several files are listened to at once, and
// what that gave is put on record a batch of files at a time. The record is
// a help and no more: a library whose database cannot be read or written
// just now is asked afresh, and nothing is kept.
func (s sound) Identify(ctx context.Context, files []store.File, again bool, each func(i int, heard tui.Heard, err error) bool) {
	ctx, stop := context.WithCancel(ctx)
	defer stop()
	var (
		mu      sync.Mutex // guards what follows, and has each called once at a time
		stopped bool
		keep    []store.Fingerprint
	)
	next := make(chan int)
	var listeners sync.WaitGroup
	for range min(len(files), maxListeners, runtime.GOMAXPROCS(0)) {
		listeners.Go(func() {
			for i := range next {
				heard, rec, err := s.identify(ctx, files[i], again)
				mu.Lock()
				if rec != nil {
					if keep = append(keep, *rec); len(keep) >= keepEvery {
						s.record(keep)
						keep = nil
					}
				}
				// What comes of a file after the stop is not said: it was
				// cut short.
				if !stopped && ctx.Err() == nil && !each(i, heard, err) {
					stopped = true
					stop()
				}
				mu.Unlock()
			}
		})
	}
feed:
	for i := range files {
		select {
		case next <- i:
		case <-ctx.Done():
			break feed
		}
	}
	close(next)
	listeners.Wait()
	s.record(keep)
}

// identify listens to f, unless its fingerprint is on record, and asks
// AcoustID, unless its answer is and is not old. Without a key it says how to
// get one, before it listens to anything. keep is what to put on record of
// it, nil when nothing new: after a failure the fingerprint that was made,
// since listening is the slow part and is not to be done twice when AcoustID
// cannot be reached.
func (s sound) identify(ctx context.Context, f store.File, again bool) (heard tui.Heard, keep *store.Fingerprint, err error) {
	key := acoustid.Key(s.cfg.AcoustIDKey)
	if key == "" {
		return tui.Heard{}, nil, acoustid.ErrNoKey
	}
	rec, known := s.onRecord(f.Sum)
	if rec.Duration <= 0 {
		// AcoustID cannot be asked with a fingerprint that has no length:
		// the file is listened to again.
		known = false
	}
	if known && !again && rec.Answer != nil && s.now().Sub(rec.Answered) < answerFor {
		var results []acoustid.Result
		if err := json.Unmarshal(rec.Answer, &results); err == nil {
			return tui.Heard{Results: results, Asked: rec.Answered, FromRecord: true}, nil, nil
		}
	}
	if !known {
		p, err := s.print(ctx, f.Path)
		if err != nil {
			return tui.Heard{}, nil, err
		}
		rec = store.Fingerprint{Sum: f.Sum, Duration: p.Duration, Fingerprint: p.Fingerprint}
		keep = &rec
	}
	results, err := s.lookup(ctx, key, rec.Fingerprint, rec.Duration)
	if err != nil {
		return tui.Heard{}, keep, err
	}
	now := s.now()
	if answer, err := json.Marshal(results); err == nil {
		rec.Answer, rec.Answered = answer, now
		keep = &rec
	}
	return tui.Heard{Results: results, Asked: now}, keep, nil
}

// onRecord reads what the library's database has for the audio that hashes
// to sum.
func (s sound) onRecord(sum string) (store.Fingerprint, bool) {
	if sum == "" || s.cfg.Destination == "" {
		return store.Fingerprint{}, false
	}
	st := s.reader
	if st == nil {
		var err error
		if st, err = store.OpenLibraryReader(s.cfg.Destination, s.cfg.Database); err != nil {
			return store.Fingerprint{}, false
		}
		defer st.Close()
	}
	rec, ok, err := st.Fingerprint(sum)
	return rec, ok && err == nil
}

// record puts recs on record, but for those of audio without a hash. It
// takes the library lock for the moment that takes, as every write to the
// library does; while a run holds it, nothing is kept.
func (s sound) record(recs []store.Fingerprint) {
	recs = slices.DeleteFunc(slices.Clone(recs), func(rec store.Fingerprint) bool { return rec.Sum == "" })
	if len(recs) == 0 || s.cfg.Destination == "" {
		return
	}
	// Only a library that is there has a record to keep: opening one that
	// is not would make it.
	if id, err := store.ReadLibraryID(s.cfg.Destination); err != nil || id == "" {
		return
	}
	st, err := store.OpenLibrary(s.cfg.Destination, s.cfg.Database, false)
	if err != nil {
		return
	}
	defer st.Close()
	_ = st.SetFingerprints(recs, s.now())
}
