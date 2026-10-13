// Package pacingtest has the clock the tests of a limiter, and of the
// clients that wait on one, go by.
package pacingtest

import (
	"context"
	"slices"
	"sync"
	"time"
)

// Clock advances only when something sleeps on it.
type Clock struct {
	mu    sync.Mutex
	now   time.Time
	slept []time.Duration
	// OnSleep, when set, runs as a sleep starts: what happens while a
	// caller waits.
	OnSleep func()
}

func NewClock() *Clock {
	return &Clock{now: time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)}
}

func (f *Clock) Now() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.now
}

func (f *Clock) Sleep(ctx context.Context, d time.Duration) error {
	if f.OnSleep != nil {
		f.OnSleep()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.slept = append(f.slept, d)
	f.now = f.now.Add(d)
	return nil
}

func (f *Clock) Advance(d time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.now = f.now.Add(d)
}

// Slept is every sleep there was, in order.
func (f *Clock) Slept() []time.Duration {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.slept)
}
