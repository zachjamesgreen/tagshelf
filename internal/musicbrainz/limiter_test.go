package musicbrainz

import (
	"context"
	"slices"
	"sync"
	"testing"
	"time"
)

// fakeClock advances only when something sleeps on it.
type fakeClock struct {
	mu    sync.Mutex
	now   time.Time
	slept []time.Duration
}

func newFakeClock() *fakeClock {
	return &fakeClock{now: time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)}
}

func (f *fakeClock) Now() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.now
}

func (f *fakeClock) Sleep(ctx context.Context, d time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.slept = append(f.slept, d)
	f.now = f.now.Add(d)
	return nil
}

func (f *fakeClock) Advance(d time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.now = f.now.Add(d)
}

func (f *fakeClock) Slept() []time.Duration {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.slept)
}

func TestLimiterSpacesRequestsOneSecondApart(t *testing.T) {
	clk := newFakeClock()
	l := newLimiter(clk)
	ctx := context.Background()

	for range 3 {
		if err := l.wait(ctx); err != nil {
			t.Fatal(err)
		}
	}
	// The first request goes at once, then one per second.
	if got, want := clk.Slept(), []time.Duration{time.Second, time.Second}; !slices.Equal(got, want) {
		t.Errorf("slept %v, want %v", got, want)
	}

	// After a quiet spell there is nothing to wait for.
	clk.Advance(5 * time.Second)
	if err := l.wait(ctx); err != nil {
		t.Fatal(err)
	}
	if got := len(clk.Slept()); got != 2 {
		t.Errorf("waited after an idle period: %v", clk.Slept())
	}

	// A request half a second after the last one waits the other half.
	clk.Advance(500 * time.Millisecond)
	if err := l.wait(ctx); err != nil {
		t.Fatal(err)
	}
	if got := clk.Slept(); got[len(got)-1] != 500*time.Millisecond {
		t.Errorf("slept %v, want a final 500ms", got)
	}
}

func TestLimiterBackoffDoublesToMaxAndRecovers(t *testing.T) {
	clk := newFakeClock()
	l := newLimiter(clk)
	ctx := context.Background()
	if err := l.wait(ctx); err != nil {
		t.Fatal(err)
	}

	var waits []time.Duration
	for {
		ok := l.backoff()
		if !ok {
			break
		}
		before := clk.Now()
		if err := l.wait(ctx); err != nil {
			t.Fatal(err)
		}
		waits = append(waits, clk.Now().Sub(before))
	}
	want := []time.Duration{2 * time.Second, 4 * time.Second, 8 * time.Second, 16 * time.Second, 32 * time.Second}
	if !slices.Equal(waits, want) {
		t.Errorf("backoff waits %v, want %v", waits, want)
	}

	// Successes halve the interval back down to one second.
	for _, want := range []time.Duration{16 * time.Second, 8 * time.Second, 4 * time.Second, 2 * time.Second, time.Second, time.Second} {
		l.recover()
		if l.interval != want {
			t.Errorf("interval after recover = %v, want %v", l.interval, want)
		}
	}
}

func TestLimiterWaitHonoursCancellation(t *testing.T) {
	clk := newFakeClock()
	l := newLimiter(clk)
	ctx, cancel := context.WithCancel(context.Background())
	if err := l.wait(ctx); err != nil {
		t.Fatal(err)
	}
	cancel()
	if err := l.wait(ctx); err != context.Canceled {
		t.Errorf("wait after cancel = %v, want context.Canceled", err)
	}
}
