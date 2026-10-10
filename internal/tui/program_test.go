package tui

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

// slowReader hands out its text only after a delay, as a person would type
// it once the screen is up.
type slowReader struct {
	r     io.Reader
	delay time.Duration
	begun bool
}

func (s *slowReader) Read(p []byte) (int, error) {
	if !s.begun {
		time.Sleep(s.delay)
		s.begun = true
	}
	return s.r.Read(p)
}

// TestProgramOpensAndQuits runs the real Bubble Tea program loop over the
// model: it draws Home and ends on q.
func TestProgramOpensAndQuits(t *testing.T) {
	var out bytes.Buffer
	m := New(&fakeSource{snap: sampleSnapshot()}, header)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	err := m.show(ctx,
		tea.WithInput(&slowReader{r: strings.NewReader("q"), delay: 300 * time.Millisecond}),
		tea.WithOutput(&out), tea.WithWindowSize(120, 32))
	if err != nil {
		t.Fatalf("program ended with %v", err)
	}
	if got := ansi.Strip(out.String()); !strings.Contains(got, "Needs attention") {
		t.Errorf("the program never drew Home:\n%q", got)
	}
}

// typist is a keyboard the test types on, a key at a time.
type typist struct{ keys chan string }

func (ty typist) Read(p []byte) (int, error) {
	k, ok := <-ty.keys
	if !ok {
		return 0, io.EOF
	}
	return copy(p, k), nil
}

// However the program ends, a run or an action it started has been stopped
// and has ended by the time show returns: the caller closes the library's
// database next.
func TestShowStopsWhatIsGoingOnBeforeItReturns(t *testing.T) {
	for name, begin := range map[string]func(t *testing.T, f *fakeRunner, ty typist){
		"a run": func(t *testing.T, f *fakeRunner, ty typist) {
			ty.keys <- "o"
			waitStart(t, f)
		},
		"an action": func(t *testing.T, f *fakeRunner, ty typist) {
			f.gate = newGate()
			ty.keys <- "2"
			time.Sleep(300 * time.Millisecond) // the tab's files are read
			ty.keys <- "a"
			f.gate.reached(t)
		},
	} {
		t.Run(name, func(t *testing.T) {
			f := newFakeRunner()
			f.lag = 150 * time.Millisecond // the file it is on
			m := New(untaggedSource(), header)
			m.runner = f
			ty := typist{keys: make(chan string)}
			defer close(ty.keys)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			type end struct {
				err     error
				stopped bool
			}
			ended := make(chan end, 1)
			go func() {
				err := m.show(ctx, tea.WithInput(ty), tea.WithOutput(io.Discard), tea.WithWindowSize(120, 32))
				ended <- end{err, f.stopped.Load()}
			}()
			time.Sleep(300 * time.Millisecond) // the screen is up
			begin(t, f, ty)
			if f.stopped.Load() {
				t.Fatal("it stopped before the program ended")
			}

			cancel()
			select {
			case e := <-ended:
				if e.err == nil {
					t.Error("the program ended by its context returned no error")
				}
				if !e.stopped {
					t.Error("show returned before what was going on had ended")
				}
			case <-time.After(10 * time.Second):
				t.Fatal("show did not return: what was going on was not stopped")
			}
		})
	}
}
