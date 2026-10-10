package tui

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/zachjamesgreen/tagshelf/internal/engine"
	"github.com/zachjamesgreen/tagshelf/internal/store"
)

// noLibraryModel is a model opened on no library, as tagshelf tui opens it
// when there is none to show.
func noLibraryModel(t *testing.T, opts Options) *Model {
	t.Helper()
	m := newModel(t, noSource{}, 100, 24)
	m.open(opts, true)
	do(t, m, m.poll(true, false))
	return m
}

var noFolder = &NoLibrary{What: "no library folder is set", Todo: []string{
	`Start again with one, as in: tagshelf tui -dest "D:\Music"`,
	`Or set destination in the config file C:\Users\me\AppData\Roaming\tagshelf\config.yaml, which does not exist yet.`,
}}

// TestNoLibraryScreens checks what the TUI shows with no library: Home says
// what is missing and what to do, and the other tabs point there instead of
// showing empty lists.
func TestNoLibraryScreens(t *testing.T) {
	m := noLibraryModel(t, Options{NoLibrary: noFolder})
	got := screen(m)
	for _, want := range []string{
		" tagshelf  no library yet",
		" 1 Home   2 Untagged   3 Duplicates   4 Library   5 Runs",
		"No library yet\n  No library folder is set.\n  Start again with one, as in: tagshelf tui -dest \"D:\\Music\"\n",
		"  Or set destination in the config file C:\\Users\\me\\AppData\\Roaming\\tagshelf\\config.yaml, which\n  does not exist yet.",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("Home lacks %q:\n%s", want, got)
		}
	}
	for _, not := range []string{"Needs attention", "_Untagged", "Reading the library"} {
		if strings.Contains(got, not) {
			t.Errorf("Home shows %q with no library:\n%s", not, got)
		}
	}
	for _, k := range []string{"2", "3", "4", "5"} {
		press(t, m, k)
		if got := screen(m); !strings.Contains(got, "No library yet: see 1 Home.") {
			t.Errorf("tab %s does not say there is no library:\n%s", k, got)
		}
	}
	// Keys of the tabs do nothing that needs a library.
	for _, k := range []string{"enter", "a", "e", "l", "u", "/", "esc", "1"} {
		press(t, m, k)
	}
	if m.tab != TabHome {
		t.Errorf("tab = %v, want Home", m.tab)
	}
}

// TestNoLibraryRefusesRuns checks that a run started with no library folder
// is refused on Home in two parts, what is wrong and what to do, and that the
// next key clears it.
func TestNoLibraryRefusesRuns(t *testing.T) {
	f := newFakeRunner()
	m := noLibraryModel(t, Options{NoLibrary: noFolder, Runner: f})
	for key, kind := range map[string]string{"o": "organize", "r": "reorganize", "i": "index"} {
		press(t, m, key)
		if m.run != nil {
			t.Fatalf("%s started a run with no library folder", key)
		}
		got := screen(m)
		if want := "Cannot start the " + kind + ": no library folder is set.\n\nNo library yet\n  No library folder is set.\n  Start again with one"; !strings.Contains(got, want) {
			t.Errorf("after %s the screen lacks %q:\n%s", key, want, got)
		}
		press(t, m, "?")
		press(t, m, "?")
		if strings.Contains(screen(m), "Cannot start") {
			t.Errorf("the refusal of %s outlived the next key", key)
		}
	}
	select {
	case <-f.started:
		t.Error("the runner was asked to run")
	default:
	}
}

// TestRunThatMakesTheIndexOpensTheLibrary starts the TUI on a library with
// no index, runs an index from it, and checks that the library is opened
// and shown once the run has ended.
func TestRunThatMakesTheIndexOpensTheLibrary(t *testing.T) {
	f := newFakeRunner()
	src := &fakeSource{version: 3, snap: sampleSnapshot()}
	none := &NoLibrary{What: `the library at D:\Music has no index yet`, Todo: []string{"Press i to index the music that is in it. Nothing is moved."}, CanRun: true}
	reopens := 0
	var reopened Options
	opts := Options{Destination: `D:\Music`, NoLibrary: none, Runner: f, DryRun: true}
	opts.Reopen = func() (Options, error) {
		reopens++
		return reopened, nil
	}
	m := noLibraryModel(t, opts)
	if got := screen(m); !strings.Contains(got, `The library at D:\Music has no index yet.`) || !strings.Contains(got, "Press i to index") ||
		!strings.Contains(got, ` tagshelf  D:\Music`) || strings.Contains(got, "no library yet") {
		t.Fatalf("the screen does not say the index is missing, under the library's folder:\n%s", got)
	}
	// The user turns dry run off, which opening again must not turn back on.
	press(t, m, "d")

	// A run that leaves the library without an index: the screen stands.
	reopened = Options{Destination: `D:\Music`, NoLibrary: none, Runner: f, DryRun: true, Reopen: opts.Reopen}
	press(t, m, "i")
	waitStart(t, f)
	f.finish <- engine.RunFinished{State: engine.StateOK}
	do(t, m, runEnded(t, m))
	if reopens != 1 || m.noLibrary == nil || !strings.Contains(screen(m), "Index finished") {
		t.Fatalf("after a run that made no index: opened again %d times, no library %v\n%s", reopens, m.noLibrary, screen(m))
	}

	// A run that makes it.
	reopened = Options{Destination: `D:\Music`, LibraryID: "df56ddd4-0000", Runner: f, DryRun: true, Reopen: opts.Reopen, source: src}
	press(t, m, "i")
	waitStart(t, f)
	f.finish <- engine.RunFinished{State: engine.StateOK}
	do(t, m, runEnded(t, m))
	if m.noLibrary != nil || m.header.LibraryID != "df56ddd4-0000" || m.header.DryRun {
		t.Fatalf("after the index was made: no library %v, header %+v; want the library, with dry run still off", m.noLibrary, m.header)
	}
	got := screen(m)
	for _, want := range []string{"library df56ddd4", "Needs attention", "18,204 files", "2 Untagged 12"} {
		if !strings.Contains(got, want) {
			t.Errorf("the library's Home lacks %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "No library yet") {
		t.Errorf("Home still says there is no library:\n%s", got)
	}
}

// TestOpenDropsTheOldLibrarysPolls checks that what a poll of the library
// shown before finds is not taken for the one opened since.
func TestOpenDropsTheOldLibrarysPolls(t *testing.T) {
	old := &fakeSource{version: 9, snap: sampleSnapshot()}
	m := newModel(t, old, 100, 24)
	stale := m.poll(true, false)
	m.open(Options{NoLibrary: noFolder}, false)
	do(t, m, stale)
	if m.loaded || m.snap.Overview.Library != 0 || m.version != 0 {
		t.Errorf("a poll of the old library was applied: loaded %t, version %d, %+v", m.loaded, m.version, m.snap.Overview)
	}
	do(t, m, m.poll(true, false))
	if !m.loaded || !strings.Contains(screen(m), "No library yet") {
		t.Errorf("the new state was not read:\n%s", screen(m))
	}
}

// TestReopenThatFails checks that a library that cannot be opened after a
// run leaves the TUI as it was, with the reason shown.
func TestReopenThatFails(t *testing.T) {
	none := &NoLibrary{What: "the library has no index yet", CanRun: true}
	m := noLibraryModel(t, Options{NoLibrary: none})
	m.Update(reopenedMsg{err: errors.New("the database belongs to another library")})
	if m.noLibrary != none || m.toastOK || !strings.Contains(m.toast, "Cannot open the library: the database belongs to another library") {
		t.Errorf("toast = %q (ok %t), no library %v", m.toast, m.toastOK, m.noLibrary)
	}
}

// TestRefusalText checks how each reason a run could not start is put: the
// runner's own two parts, a lock in tagshelf's words, anything else as it is.
func TestRefusalText(t *testing.T) {
	held := time.Date(2026, 3, 1, 14, 5, 0, 0, time.UTC)
	tests := []struct {
		err  error
		want string
	}{
		{&StartError{What: "no source folder is set", Todo: "Start tagshelf tui with one."},
			"Cannot start the organize: no source folder is set.\n  Start tagshelf tui with one.\n"},
		{&store.LockedError{Path: `D:\Music\.tagshelf-lock`, Holder: store.LockHolder{PID: 4242, Started: held}},
			"Cannot start the organize: another tagshelf run is working on this library (process 4242, started 2026-03-01 14:05).\n  Wait for it to finish, then try again.\n"},
		{&store.LockedError{Path: `D:\Music\.tagshelf-lock`},
			"Cannot start the organize: another tagshelf run is working on this library.\n  Wait for it to finish, then try again.\n"},
		{errors.New("the disk is full."), "Cannot start the organize: the disk is full.\n"},
	}
	for _, tt := range tests {
		f := newFakeRunner()
		f.err = tt.err
		m := newRunModel(t, f)
		press(t, m, "o")
		if m.run != nil || m.last != nil {
			t.Errorf("%v: the refused run is shown as going or finished", tt.err)
		}
		if got := screen(m); !strings.Contains(got, tt.want) {
			t.Errorf("%v: the screen lacks %q:\n%s", tt.err, tt.want, got)
		}
	}
}

func TestSentence(t *testing.T) {
	for in, want := range map[string]string{
		"no library folder is set": "No library folder is set.",
		"the file is gone.":        "The file is gone.",
		"émigré":                   "Émigré.",
	} {
		if got := sentence(in); got != want {
			t.Errorf("sentence(%q) = %q, want %q", in, got, want)
		}
	}
}
