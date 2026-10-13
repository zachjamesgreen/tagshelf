package tui

import (
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	tea "charm.land/bubbletea/v2"

	"github.com/zachjamesgreen/tagshelf/internal/engine"
	"github.com/zachjamesgreen/tagshelf/internal/rules"
)

// coverDoneMsg says a cover art run has ended.
type coverDoneMsg struct {
	written []engine.CoverWritten
	put     []engine.CoverEmbedded
	skipped []engine.CoverSkipped
	finish  engine.RunFinished
	err     error
	// after is set for the run that follows a match of its own accord: it
	// adds to what the match said, and says nothing when it wrote nothing.
	after bool
}

// albumFolders are the folders of the given files, each once.
func albumFolders(paths []string) []string {
	var dirs []string
	seen := map[string]bool{}
	for _, p := range paths {
		dir := filepath.Dir(p)
		if key := rules.PathKey(dir); !seen[key] {
			seen[key] = true
			dirs = append(dirs, dir)
		}
	}
	return dirs
}

// coverArt gives the album folders dirs, and their files, the cover they lack, as one run off
// the UI goroutine. It is what c asks for on an album, with why not when the
// setting is off, and what follows a match of its own accord, which says
// nothing then.
func (m *Model) coverArt(dirs []string, after bool) tea.Cmd {
	if len(dirs) == 0 {
		return nil
	}
	if !m.coverFile {
		if !after {
			m.setToast(false, "Cover art is off: turn on Cover file or Cover in the files on %d Settings", int(TabSettings)+1)
		}
		return nil
	}
	if after {
		// Quietly: the match is done, and what stands in the way of a cover
		// is said when c asks for one.
		if m.locked || m.run != nil || m.busy != nil || m.header.DryRun || m.runner == nil {
			return nil
		}
	} else if !m.canChange() {
		return nil
	}
	runner := m.runner
	ctx, ended := m.begin()
	return func() tea.Msg {
		defer close(ended)
		msg := coverDoneMsg{after: after}
		var mu sync.Mutex
		sink := func(e engine.Event) {
			mu.Lock()
			defer mu.Unlock()
			switch e := e.(type) {
			case engine.CoverWritten:
				msg.written = append(msg.written, e)
			case engine.CoverEmbedded:
				msg.put = append(msg.put, e)
			case engine.CoverSkipped:
				msg.skipped = append(msg.skipped, e)
			}
		}
		msg.finish, msg.err = runner.CoverArt(ctx, slices.Clone(dirs), sink)
		return msg
	}
}

// coverDone says how a cover art run went.
func (m *Model) coverDone(msg coverDoneMsg) tea.Cmd {
	m.ended()
	errs := msg.finish.Counters.Errors
	var said string
	ok := true
	switch {
	case msg.err != nil:
		said, ok = fmt.Sprintf("Cover art: %v", msg.err), false
	case errs > 0 || msg.finish.State != engine.StateOK:
		said, ok = fmt.Sprintf("The cover could not be got: see %d Runs", int(TabRuns)+1), false
	case len(msg.written) == 1:
		w := msg.written[0]
		said = "Cover written to " + m.shortPath(w.Path) + " from " + w.From
		if w.Replaced != "" {
			said += ", in place of " + filepath.Base(w.Replaced)
		}
	case len(msg.written) > 1:
		said = plural(int64(len(msg.written)), "cover", "covers") + " written"
	case len(msg.put) > 0:
		said = "Cover put into " + plural(int64(len(msg.put)), "file", "files")
	case msg.after:
		// Nothing to add to what the match said.
		return m.poll(true, false)
	case len(msg.skipped) == 1:
		said, ok = "No cover written: "+msg.skipped[0].Why, false
	default:
		said = "The album has a cover already"
	}
	if len(msg.written) > 0 && len(msg.put) > 0 {
		said += ", and put into " + plural(int64(len(msg.put)), "file", "files")
	}
	if msg.after && m.toast != "" {
		// The match's own message stays, without its offer to undo: u now
		// takes back what was done last, which is the cover.
		said = strings.TrimSuffix(m.toast, "  (u undo)") + "; " + strings.ToLower(said[:1]) + said[1:]
		ok = ok && m.toastOK
	}
	m.setToast(ok, "%s", said)
	if len(msg.written)+len(msg.put) > 0 {
		m.offerUndo(msg.finish.RunID)
	}
	return m.poll(true, false)
}
