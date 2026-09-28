package musicbrainz

import (
	"context"
	"sync"
	"time"
)

// clock is the time source for the limiter, swapped for a fake in tests.
type clock interface {
	Now() time.Time
	// Sleep waits for d or until ctx is done, returning ctx's error then.
	Sleep(ctx context.Context, d time.Duration) error
}

type realClock struct{}

func (realClock) Now() time.Time { return time.Now() }

func (realClock) Sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

const (
	minInterval = time.Second
	maxInterval = 32 * time.Second
)

// sharedLimiter spaces every request the process makes to MusicBrainz, since
// its limit is per IP address, not per client.
var sharedLimiter = newLimiter(realClock{})

// limiter spaces requests by an interval that starts at one second, doubles on
// each 503 up to maxInterval and halves again on each success, as Picard's
// rate control does.
type limiter struct {
	clock clock

	mu       sync.Mutex
	interval time.Duration
	next     time.Time // earliest start of the next request
}

func newLimiter(c clock) *limiter {
	return &limiter{clock: c, interval: minInterval}
}

// wait blocks until the caller may send a request, reserving its slot first so
// concurrent callers queue one interval apart.
func (l *limiter) wait(ctx context.Context) error {
	l.mu.Lock()
	now := l.clock.Now()
	start := l.next
	if start.Before(now) {
		start = now
	}
	l.next = start.Add(l.interval)
	l.mu.Unlock()

	if d := start.Sub(now); d > 0 {
		return l.clock.Sleep(ctx, d)
	}
	return nil
}

// backoff records a 503: the interval doubles, and the next request waits that
// long from now. It reports false when the interval was already at its
// maximum, meaning the caller should give up.
func (l *limiter) backoff() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	atMax := l.interval >= maxInterval
	l.interval = min(l.interval*2, maxInterval)
	l.next = l.clock.Now().Add(l.interval)
	return !atMax
}

// recover records a success: the interval halves, down to one second.
func (l *limiter) recover() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.interval = max(l.interval/2, minInterval)
}
