package musicbrainz

import (
	"context"
	"errors"
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
	// onSleep, when set, runs as a sleep starts: what happens while a
	// caller waits.
	onSleep func()
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
	if f.onSleep != nil {
		f.onSleep()
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

// A lookup dropped while it waits takes no slot: the TUI drops one for every
// new search, and each would push the next request back an interval.
func TestLimiterDroppedWaitsTakeNoSlot(t *testing.T) {
	clk := newFakeClock()
	l := newLimiter(clk)
	if err := l.wait(context.Background()); err != nil {
		t.Fatal(err)
	}
	// Five lookups, each dropped while it waited for its turn.
	for range 5 {
		ctx, cancel := context.WithCancel(context.Background())
		clk.onSleep = cancel
		if err := l.wait(ctx); err != context.Canceled {
			t.Fatalf("a wait dropped while sleeping = %v, want context.Canceled", err)
		}
	}
	clk.onSleep = nil
	if err := l.wait(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got, want := clk.Slept(), []time.Duration{time.Second}; !slices.Equal(got, want) {
		t.Errorf("the request after five dropped ones slept %v, want %v", got, want)
	}
	// The one that was sent took its slot.
	if err := l.wait(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got, want := clk.Slept(), []time.Duration{time.Second, time.Second}; !slices.Equal(got, want) {
		t.Errorf("the request after it slept %v, want %v", got, want)
	}
}

// Callers that wait at once are still let through an interval apart.
func TestLimiterSpacesConcurrentCallers(t *testing.T) {
	clk := newFakeClock()
	l := newLimiter(clk)
	first := clk.Now()
	const callers = 8
	var wg sync.WaitGroup
	for range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := l.wait(context.Background()); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	// Each caller let through moves the next slot on by an interval or more,
	// so two let through together would leave it short of this.
	if got, want := l.next.Sub(first), callers*minInterval; got < want {
		t.Errorf("after %d callers the next slot is %v on, want %v or more", callers, got, want)
	}
}

// The real clock's sleep is what a dropped lookup waits in, up to 32 s for
// each retry while backed off: it has to end with the context.
func TestRealClockSleepEndsWithTheContext(t *testing.T) {
	sleep := func(ctx context.Context) error {
		done := make(chan error, 1)
		go func() { done <- realClock{}.Sleep(ctx, time.Hour) }()
		select {
		case err := <-done:
			return err
		case <-time.After(10 * time.Second):
			return errors.New("still asleep")
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := sleep(ctx); err != context.Canceled {
		t.Errorf("Sleep with a context already done = %v, want context.Canceled", err)
	}
	ctx, cancel = context.WithCancel(context.Background())
	time.AfterFunc(20*time.Millisecond, cancel)
	if err := sleep(ctx); err != context.Canceled {
		t.Errorf("Sleep with a context done part way = %v, want context.Canceled", err)
	}

	// Left alone it sleeps the time through.
	var clk realClock
	before := clk.Now()
	if err := clk.Sleep(context.Background(), 20*time.Millisecond); err != nil {
		t.Errorf("Sleep = %v", err)
	}
	if d := clk.Now().Sub(before); d < 20*time.Millisecond {
		t.Errorf("Sleep of 20ms came back after %v", d)
	}
}
