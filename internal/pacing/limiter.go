// Package pacing spaces the requests tagshelf makes to a web service, and
// tells a request that ran out of time from one that failed otherwise. The
// clients of MusicBrainz and of AcoustID each have a limiter of their own.
package pacing

import (
	"context"
	"errors"
	"net"
	"sync"
	"time"
)

// Clock is the time source for a limiter, swapped for a fake in tests.
type Clock interface {
	Now() time.Time
	// Sleep waits for d or until ctx is done, returning ctx's error then.
	Sleep(ctx context.Context, d time.Duration) error
}

// RealClock is the clock of the machine.
type RealClock struct{}

func (RealClock) Now() time.Time { return time.Now() }

func (RealClock) Sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Limiter spaces requests by an interval that starts at its least, doubles
// each time the service says it is busy, up to its most, and halves again on
// each success, as Picard's rate control does. It is safe for concurrent use.
type Limiter struct {
	clock       Clock
	least, most time.Duration

	mu       sync.Mutex
	interval time.Duration
	next     time.Time // earliest start of the next request
}

// NewLimiter returns a limiter on the clock c whose interval goes from least
// to most.
func NewLimiter(c Clock, least, most time.Duration) *Limiter {
	return &Limiter{clock: c, least: least, most: most, interval: least}
}

// Wait blocks until the caller may send a request. A slot is taken only by a
// caller that goes on to send: one whose ctx is done while it waits leaves
// with ctx's error and pushes nobody back. Concurrent callers sleep to the
// same moment, one of them takes it and the rest sleep on, so requests still
// start an interval apart.
func (l *Limiter) Wait(ctx context.Context) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		l.mu.Lock()
		now := l.clock.Now()
		d := l.next.Sub(now)
		if d <= 0 {
			l.next = now.Add(l.interval)
		}
		l.mu.Unlock()
		if d <= 0 {
			return nil
		}
		if err := l.clock.Sleep(ctx, d); err != nil {
			return err
		}
	}
}

// Backoff records that the service is busy: the interval doubles, and the
// next request waits that long from now. It reports false when the interval
// was already at its most, meaning the caller should give up.
func (l *Limiter) Backoff() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	atMost := l.interval >= l.most
	l.interval = min(l.interval*2, l.most)
	l.next = l.clock.Now().Add(l.interval)
	return !atMost
}

// Recover records a success: the interval halves, down to its least.
func (l *Limiter) Recover() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.interval = max(l.interval/2, l.least)
}

// Interval is the time between two requests as it is now.
func (l *Limiter) Interval() time.Duration {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.interval
}

// TimedOut reports whether err is that of a request that ran out of time.
// One the caller dropped, or gave a deadline of its own that passed, did not:
// its error is the caller's.
func TimedOut(ctx context.Context, err error) bool {
	var ne net.Error
	return ctx.Err() == nil && errors.As(err, &ne) && ne.Timeout()
}
