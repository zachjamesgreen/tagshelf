package tui

import (
	"errors"
	"fmt"
	"github.com/charmbracelet/x/ansi"
	"image"
	"os"
	"strings"
	"unicode"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"

	"github.com/zachjamesgreen/tagshelf/internal/engine"
	"github.com/zachjamesgreen/tagshelf/internal/musicbrainz"
	"github.com/zachjamesgreen/tagshelf/internal/store"
	"github.com/zachjamesgreen/tagshelf/internal/tagwrite"
)

// NoLibrary is why the TUI has no library to show: tagshelf is not set up
// yet, or the library has never been indexed.
type NoLibrary struct {
	// What is missing, as a phrase without a full stop.
	What string
	// Todo is what to do about it, a line each.
	Todo []string
	// CanRun reports whether a run can be started all the same: the library
	// folder is known, and a run would make its index.
	CanRun bool
	// Setting is the key of the setting that would put it right, empty when
	// it is none.
	Setting string
}

// StartError is why a run could not start, and what to do about it. A Runner
// returns one where it knows what to do.
type StartError struct {
	What string // a phrase without a full stop
	Todo string // whole sentences, empty when there is nothing to do
	// Setting is the key of the setting to change, empty when it is none.
	Setting string
}

func (e *StartError) Error() string { return e.What }

// errNoLibrary is what reading a library that is not there fails with.
var errNoLibrary = errors.New("there is no library yet")

// noSource stands in for the library while there is none: it reads as an
// empty one that never changes.
type noSource struct{}

func (noSource) DataVersion() (int64, error)               { return 0, nil }
func (noSource) Lock() (store.LockHolder, bool, error)     { return store.LockHolder{}, false, nil }
func (noSource) Load(parts Parts) (Snapshot, error)        { return Snapshot{Parts: parts}, nil }
func (noSource) RunDetail(int64) (RunDetail, error)        { return RunDetail{}, errNoLibrary }
func (noSource) Tags(path string) (tagwrite.Values, error) { return tagwrite.ReadAll(path) }
func (noSource) Picture(path string) (image.Image, error)  { return readPicture(path) }
func (noSource) UndoPlan(int64) (engine.UndoPlan, error)   { return engine.UndoPlan{}, errNoLibrary }
func (noSource) Search(string, int) ([]store.File, error)  { return nil, nil }
func (noSource) Album(store.File) ([]store.File, error)    { return nil, nil }
func (noSource) LibraryReleases() (map[string]bool, error) { return nil, nil }

// Folder lists a folder without a library to look its files up in: the Files
// tab shows the computer's folders before set-up is finished.
func (noSource) Folder(dir string, hidden bool) (FolderList, error) {
	return readFolder(nil, dir, hidden)
}

// open points the model at the library opts describe, or at none. What it
// held of another library is dropped. first is the TUI starting: opened
// again later, the model keeps the dry run setting its user may have changed.
func (m *Model) open(opts Options, first bool) {
	m.src = noSource{}
	switch {
	case opts.source != nil:
		m.src = opts.source
	case opts.Store != nil:
		m.src = librarySource{st: opts.Store, destination: opts.Destination, pid: os.Getpid()}
	}
	dryRun := m.header.DryRun
	if first {
		dryRun = opts.DryRun
	}
	m.header = Header{Destination: opts.Destination, LibraryID: opts.LibraryID, DryRun: dryRun}
	m.noLibrary = opts.NoLibrary
	m.reopen = opts.Reopen
	m.reopening = false
	m.settingsSrc = opts.Settings
	// With nothing to show and no run to start, the settings are where to go.
	if first && opts.NoLibrary != nil && !opts.NoLibrary.CanRun && opts.Settings != nil {
		m.tab = TabSettings
	}
	m.runner = opts.Runner
	m.compilations = opts.Compilations
	m.source = opts.Source
	// The folder the Files tab shows is read again: what it holds may now
	// be, or no longer be, a file of the library.
	m.files.stale = m.files.loaded
	m.genreMode = opts.GenreFromMusicBrainz
	m.coverFile = opts.CoverFile
	m.sound = opts.Sound
	m.mb = nil
	if client, err := musicbrainz.New(opts.MusicBrainzContact); err == nil {
		m.mb = client
	}

	m.loaded, m.snap, m.version = false, Snapshot{}, 0
	m.locked, m.holder = false, store.LockHolder{}
	m.have, m.seen = Parts{}, Parts{}
	// What a poll of the library left behind finds is not this one's.
	m.appliedSeq = m.nextSeq + 1
	m.nextSeq++
}

// reopenedMsg is the result of opening the library again.
type reopenedMsg struct {
	opts Options
	err  error
}

// openAgain opens the library again off the UI goroutine, when there is a
// way to: after a run that may have made the index the TUI was started
// without.
func (m *Model) openAgain() tea.Cmd {
	reopen := m.reopen
	if reopen == nil {
		return nil
	}
	return func() tea.Msg {
		opts, err := reopen()
		return reopenedMsg{opts: opts, err: err}
	}
}

// reopened shows the library that was opened again.
func (m *Model) reopened(msg reopenedMsg) tea.Cmd {
	m.reopening = false
	if msg.err != nil {
		m.setToast(false, "Cannot open the library: %v", msg.err)
		return nil
	}
	m.open(msg.opts, false)
	return m.poll(true, false)
}

// refuse shows on Home why a run of the given kind could not start.
func (m *Model) refuse(kind RunKind, err error) {
	var start *StartError
	var locked *store.LockedError
	switch {
	case errors.As(err, &start):
	case errors.As(err, &locked):
		start = &StartError{What: "another tagshelf run is working on this library", Todo: "Wait for it to finish, then try again."}
		if h := locked.Holder; h.PID != 0 {
			start.What += fmt.Sprintf(" (process %d, started %s)", h.PID, m.formatTime(h.Started))
		}
	default:
		start = &StartError{What: err.Error()}
	}
	m.refused = &refusal{kind: kind, StartError: *start}
}

// refusal is a run that could not start, shown on Home until the next key.
type refusal struct {
	kind RunKind
	StartError
}

// refusalView draws why a run could not start, and what to do about it.
func (m *Model) refusalView(r *refusal) string {
	w := max(m.width-2, 20)
	what := "Cannot start the " + r.kind.String() + ": " + strings.TrimSuffix(clean(r.What), ".") + "."
	out := badStyle.Render(wrapAll(what, w))
	todo := r.Todo
	if r.Setting != "" && m.settingsSrc != nil {
		todo = strings.TrimSpace(todo + " Press 6 to change it in Settings.")
	}
	if todo != "" {
		out += "\n" + indent(wrapAll(clean(todo), w-2))
	}
	return out + "\n"
}

// noLibraryView draws why there is no library to show, and what to do.
func (m *Model) noLibraryView() string {
	n := m.noLibrary
	w := max(m.width-4, 20)
	var b strings.Builder
	b.WriteString(boldStyle.Render("No library yet") + "\n")
	b.WriteString(indent(wrapAll(sentence(clean(n.What)), w)) + "\n")
	for _, line := range n.Todo {
		b.WriteString(indent(wrapAll(clean(line), w)) + "\n")
	}
	return b.String()
}

// sentence makes a phrase a sentence: a capital first and a full stop last.
func sentence(s string) string {
	s = strings.TrimSuffix(s, ".") + "."
	r, size := utf8.DecodeRuneInString(s)
	return string(unicode.ToUpper(r)) + s[size:]
}

// indent puts two spaces before every line of s.
func indent(s string) string {
	return "  " + strings.ReplaceAll(s, "\n", "\n  ")
}

// wrapAll breaks s into lines of at most w cells: between words, and inside
// a word wider than that, such as a long path, so that none of it is cut off.
func wrapAll(s string, w int) string {
	return ansi.Hardwrap(wrap(s, w), w, false)
}
