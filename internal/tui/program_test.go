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
	p := tea.NewProgram(m, tea.WithContext(ctx),
		tea.WithInput(&slowReader{r: strings.NewReader("q"), delay: 300 * time.Millisecond}),
		tea.WithOutput(&out), tea.WithWindowSize(120, 32))
	if _, err := p.Run(); err != nil {
		t.Fatalf("program ended with %v", err)
	}
	if got := ansi.Strip(out.String()); !strings.Contains(got, "Needs attention") {
		t.Errorf("the program never drew Home:\n%q", got)
	}
}
