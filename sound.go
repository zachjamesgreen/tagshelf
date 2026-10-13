package main

import (
	"context"
	"encoding/json"
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

// sound identifies files by their sound for the terminal UI: it computes the
// fingerprint, asks AcoustID, and keeps both on record in the library's
// database, by the hash of the audio.
type sound struct {
	cfg config.Config // with absolute paths

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

// Identify implements tui.Sound. Without a key it says how to get one,
// before it listens to anything. The record is a help and no more: a library
// whose database cannot be read or written just now is asked afresh, and
// nothing is kept.
func (s sound) Identify(ctx context.Context, f store.File, again bool) (tui.Heard, error) {
	key := acoustid.Key(s.cfg.AcoustIDKey)
	if key == "" {
		return tui.Heard{}, acoustid.ErrNoKey
	}
	rec, known := s.onRecord(f.Sum)
	if known && !again && rec.Answer != nil && s.now().Sub(rec.Answered) < answerFor {
		var results []acoustid.Result
		if err := json.Unmarshal(rec.Answer, &results); err == nil {
			return tui.Heard{Results: results, Asked: rec.Answered, FromRecord: true}, nil
		}
	}
	if !known {
		p, err := s.print(ctx, f.Path)
		if err != nil {
			return tui.Heard{}, err
		}
		rec = store.Fingerprint{Sum: f.Sum, Duration: p.Duration, Fingerprint: p.Fingerprint}
		// Kept at once: listening is the slow part, and is not done twice
		// when AcoustID cannot be reached.
		s.record(rec)
	}
	results, err := s.lookup(ctx, key, rec.Fingerprint, rec.Duration)
	if err != nil {
		return tui.Heard{}, err
	}
	now := s.now()
	if answer, err := json.Marshal(results); err == nil {
		rec.Answer, rec.Answered = answer, now
		s.record(rec)
	}
	return tui.Heard{Results: results, Asked: now}, nil
}

// onRecord reads what the library's database has for the audio that hashes
// to sum.
func (s sound) onRecord(sum string) (store.Fingerprint, bool) {
	if sum == "" || s.cfg.Destination == "" {
		return store.Fingerprint{}, false
	}
	st, err := store.OpenLibraryReader(s.cfg.Destination, s.cfg.Database)
	if err != nil {
		return store.Fingerprint{}, false
	}
	defer st.Close()
	rec, ok, err := st.Fingerprint(sum)
	return rec, ok && err == nil
}

// record puts rec on record. It takes the library lock for the moment that
// takes, as every write to the library does; while a run holds it, nothing
// is kept.
func (s sound) record(rec store.Fingerprint) {
	if rec.Sum == "" || s.cfg.Destination == "" {
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
	_ = st.SetFingerprint(rec, s.now())
}
