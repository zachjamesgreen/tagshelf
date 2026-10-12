package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zachjamesgreen/tagshelf/internal/config"
	"github.com/zachjamesgreen/tagshelf/internal/engine"
	"github.com/zachjamesgreen/tagshelf/internal/tui"
)

// openTUI runs tagshelf with args and returns the options the TUI was shown
// with, and the exit code. during, when not nil, is called while it shows.
func openTUI(t *testing.T, args []string, during func(opts tui.Options)) (shown tui.Options, code int) {
	t.Helper()
	real := showTUI
	defer func() { showTUI = real }()
	calls := 0
	showTUI = func(_ context.Context, opts tui.Options) error {
		calls++
		shown = opts
		if during != nil {
			during(opts)
		}
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), refusalTimeout)
	defer cancel()
	code = run(ctx, args)
	if calls != 1 {
		t.Fatalf("run(%q) showed the TUI %d times, want once (exit code %d)", args, calls, code)
	}
	return shown, code
}

// userConfigFile writes yaml as the user's own config file for the rest of
// the test, and returns its path.
func userConfigFile(t *testing.T, yaml string) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("APPDATA", dir)
	t.Setenv("XDG_CONFIG_HOME", dir)
	path := filepath.Join(dir, "tagshelf", "config.yaml")
	writeFile(t, path, yaml)
	return path
}

// TestTUIOpensWithoutALibrary checks that tagshelf tui opens on a set-up
// that is not finished, saying what is missing, where it used to refuse.
func TestTUIOpensWithoutALibrary(t *testing.T) {
	tests := []struct {
		name   string
		args   func(t *testing.T, tmp string) []string
		what   string   // NoLibrary.What, with TMP for the temp folder
		todo   []string // parts of NoLibrary.Todo, in order
		canRun bool
	}{
		{"nothing set up at all", func(t *testing.T, tmp string) []string { return []string{"tui"} },
			"no library folder is set",
			[]string{`Press 6 to set it in Settings, which keeps it in the config file APPDATA\tagshelf\config.yaml, which does not exist yet.`, `tagshelf tui -dest "D:\Music"`}, false},
		{"a config file that is named and not there yet", func(t *testing.T, tmp string) []string {
			return []string{"tui", "-config", filepath.Join(tmp, "new.yaml")}
		}, "no library folder is set", []string{`Press 6 to set it in Settings, which keeps it in the config file TMP\new.yaml, which does not exist yet.`, "tagshelf tui -dest"}, false},
		{"a config file without a destination", func(t *testing.T, tmp string) []string {
			return []string{"tui", "-config", filepath.Join(tmp, "empty.yaml")}
		}, "no library folder is set", []string{`Press 6 to set it in Settings, which keeps it in the config file TMP\empty.yaml.`, "tagshelf tui -dest"}, false},
		{"a config file that is not YAML", func(t *testing.T, tmp string) []string {
			writeFile(t, filepath.Join(tmp, "bad.yaml"), "destination: [\n")
			return []string{"tui", "-config", filepath.Join(tmp, "bad.yaml"), "-dest", filepath.Join(tmp, "lib")}
		}, `the config file TMP\bad.yaml is not valid YAML: `, []string{"Fix the file in an editor, then press R on 6 Settings to read it again."}, false},
		{"a library folder that is not there", func(t *testing.T, tmp string) []string {
			return []string{"tui", "-config", filepath.Join(tmp, "empty.yaml"), "-dest", filepath.Join(tmp, "lib")}
		}, `the library folder TMP\lib does not exist yet`, []string{"Press i to make it, as an empty library."}, true},
		{"a library folder with no index", func(t *testing.T, tmp string) []string {
			writeFile(t, filepath.Join(tmp, "lib", "song.mp3"), "no tags")
			return []string{"tui", "-config", filepath.Join(tmp, "empty.yaml"), "-dest", filepath.Join(tmp, "lib"), "-source", filepath.Join(tmp, "in")}
		}, `the library at TMP\lib has no index yet`, []string{"Press i to index the music that is in it. Nothing is moved.", `Or press o to organize TMP\in into it.`}, true},
		{"a library to reorganize with no index", func(t *testing.T, tmp string) []string {
			writeFile(t, filepath.Join(tmp, "lib", "song.mp3"), "no tags")
			return []string{"tui", "-config", filepath.Join(tmp, "empty.yaml"), "-dest", filepath.Join(tmp, "lib"), "-source", filepath.Join(tmp, "lib")}
		}, `the library at TMP\lib has no index yet`, []string{"Press i to index"}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmp, _, logs := cliEnv(t)
			user, err := config.UserPath()
			if err != nil {
				t.Fatal(err)
			}
			fill := strings.NewReplacer(`APPDATA\tagshelf\config.yaml`, user, `TMP\`, tmp+string(filepath.Separator), "TMP", tmp).Replace
			args := tt.args(t, tmp)
			logs.Reset()
			shown, code := openTUI(t, args, nil)
			if code != exitOK || logs.Len() != 0 {
				t.Errorf("exit code %d, printed %q; want the TUI opened without a word", code, logs)
			}
			n := shown.NoLibrary
			if n == nil || shown.Store != nil {
				t.Fatalf("the TUI was opened on a library: %+v", shown)
			}
			if !strings.HasPrefix(n.What, fill(tt.what)) || n.CanRun != tt.canRun {
				t.Errorf("no library because %q (can run %t), want %q (%t)", n.What, n.CanRun, fill(tt.what), tt.canRun)
			}
			todo := strings.Join(n.Todo, "\n")
			at := 0
			for _, want := range tt.todo {
				i := strings.Index(todo[at:], fill(want))
				if i < 0 {
					t.Errorf("what to do is\n%s\nwhich lacks, in order, %q", todo, fill(want))
					break
				}
				at += i
			}
			if tt.name == "a library to reorganize with no index" && strings.Contains(todo, "press o") {
				t.Errorf("what to do offers to organize the library into itself:\n%s", todo)
			}
			// A run can be started exactly when there is a library folder.
			if (shown.Runner != nil) != tt.canRun || shown.Reopen == nil || shown.Settings == nil {
				t.Errorf("runner %v, reopen set %t; want a runner only with a library folder, and a way to open again", shown.Runner, shown.Reopen != nil)
			}
			if !tt.canRun && shown.Destination != "" {
				t.Errorf("the TUI was given the library folder %q without one to go by", shown.Destination)
			}
			if _, err := os.Stat(filepath.Join(tmp, "new.yaml")); !os.IsNotExist(err) {
				t.Errorf("opening the TUI made the config file it was named: %v", err)
			}
		})
	}
}

// TestTUIOpensTheLibraryARunMade opens the TUI on a library with no index,
// indexes it as i would, and opens the library again as the TUI then does:
// it is there, and its database closes when the TUI has returned.
func TestTUIOpensTheLibraryARunMade(t *testing.T) {
	tmp, config, logs := cliEnv(t)
	lib := filepath.Join(tmp, "lib")
	writeFile(t, filepath.Join(lib, "song.mp3"), "no tags")
	var again tui.Options
	shown, code := openTUI(t, []string{"tui", "-config", config, "-dest", lib, "-dry-run"}, func(opts tui.Options) {
		// A dry index is an index all the same: it moves nothing either way.
		finish, err := opts.Runner.Run(context.Background(), tui.RunIndex, false, func(engine.Event) {})
		if err != nil || finish.State != engine.StateOK {
			t.Fatalf("index = %+v, %v", finish, err)
		}
		if again, err = opts.Reopen(); err != nil {
			t.Fatalf("opening again: %v", err)
		}
		if again.Store == nil || again.NoLibrary != nil || again.LibraryID == "" || again.Destination != lib || !again.DryRun {
			t.Fatalf("opened again with %+v, want the library that was indexed", again)
		}
		if _, err := again.Store.DataVersion(); err != nil {
			t.Errorf("the database cannot be read while the TUI shows: %v", err)
		}
		// Opened once more, the database before it is closed.
		once := again.Store
		if again, err = opts.Reopen(); err != nil || again.Store == nil {
			t.Fatalf("opening a third time: %+v, %v", again, err)
		}
		if _, err := once.DataVersion(); err == nil {
			t.Error("the database opened before is still open")
		}
	})
	if code != exitOK || shown.NoLibrary == nil {
		t.Errorf("exit code %d, started on %+v\n%s", code, shown, logs)
	}
	if _, err := again.Store.DataVersion(); err == nil {
		t.Error("the database is still open after the TUI returned")
	}
}

// TestUsersConfigFile checks that the user's own config file is what
// tagshelf goes by when none is named and the folder has none, for a run and
// for the TUI, and that it alone starts no run.
func TestUsersConfigFile(t *testing.T) {
	tmp, _, logs := cliEnv(t)
	var out bytes.Buffer
	stdout = &out
	song := filepath.Join(tmp, "in", "loose.mp3")
	writeFile(t, song, "no tags")
	lib := filepath.Join(tmp, "lib")
	user := userConfigFile(t, "source: '"+filepath.Join(tmp, "in")+"'\ndestination: '"+lib+"'\ndry_run: false\n")
	t.Chdir(t.TempDir())

	if got := run(context.Background(), nil); got != exitOK {
		t.Errorf("run() = %d", got)
	}
	if !strings.Contains(out.String(), "\nConfig file:\n  "+user+" (your own config file)\n") {
		t.Errorf("the help does not name the user's config file:\n%s", out.String())
	}
	if _, err := os.Stat(song); err != nil {
		t.Fatalf("tagshelf with nothing typed moved the song: %v", err)
	}

	// A config.yaml of some other program in the folder does not hide it.
	writeFile(t, "config.yaml", "- not\n- ours\n")
	logs.Reset()
	shown, code := openTUI(t, []string{"tui"}, nil)
	if code != exitOK || shown.Destination != lib || shown.NoLibrary == nil || !shown.NoLibrary.CanRun {
		t.Errorf("tagshelf tui: exit code %d, opened with %+v; want the library of the user's config file, not yet indexed", code, shown)
	}
	if !strings.Contains(logs.String(), "Warning: ignoring the config.yaml in the working directory: ") {
		t.Errorf("log %q does not say the folder's config.yaml was passed over", logs)
	}
	os.Remove("config.yaml")

	logs.Reset()
	if got := run(context.Background(), []string{"-verbose"}); got != exitOK {
		t.Fatalf("run(-verbose) = %d\n%s", got, logs)
	}
	if !strings.Contains(logs.String(), "Using your config file: "+user) {
		t.Errorf("log does not say which config file was used:\n%s", logs)
	}
	if _, err := os.Stat(song); !os.IsNotExist(err) {
		t.Errorf("the run left the song in the source of the user's config file: %v", err)
	}
	if _, err := os.Stat(filepath.Join(lib, "_Untagged")); err != nil {
		t.Errorf("the run did not organize into the library of the user's config file: %v", err)
	}

	// Once it is there it is not passed over: broken, it stops a run.
	writeFile(t, user, "dry-run: false\n")
	logs.Reset()
	if got := run(context.Background(), []string{"-verbose"}); got != exitCannotStart {
		t.Errorf("run with a broken user config file = %d, want %d", got, exitCannotStart)
	}
	if want := "tagshelf cannot start: the config file " + user + ` has an unknown key "dry-run" on line 1`; !strings.Contains(logs.String(), want) {
		t.Errorf("log %q lacks %q", logs, want)
	}
}

// TestDoubleClickOpensTheTUI checks tagshelf with nothing typed: started from
// a terminal it shows the help, and started by a double click, where the help
// would vanish with the window, the TUI. If that ends badly the window waits
// for Enter.
func TestDoubleClickOpensTheTUI(t *testing.T) {
	_, _, logs := cliEnv(t)
	var out bytes.Buffer
	stdout = &out
	realConsole, realWait := ownConsole, waitForEnter
	defer func() { ownConsole, waitForEnter = realConsole, realWait }()
	waited := 0
	waitForEnter = func() { waited++ }

	ownConsole = func() bool { return true }
	shown, code := openTUI(t, nil, nil)
	if code != exitOK || shown.NoLibrary == nil || out.Len() != 0 || logs.Len() != 0 || waited != 0 {
		t.Errorf("double click: exit code %d, opened with %+v, printed %q %q, waited %d times; want the TUI and nothing else",
			code, shown, out.String(), logs, waited)
	}
	// With an argument it is not a double click, whatever the console.
	if got := run(context.Background(), []string{"-version"}); got != exitOK || !strings.Contains(out.String(), "tagshelf ") && waited != 0 {
		t.Errorf("-version in a console of its own = %d, waited %d times", got, waited)
	}

	real := showTUI
	defer func() { showTUI = real }()
	showTUI = func(context.Context, tui.Options) error { return errors.New("the terminal went away") }
	if got := run(context.Background(), nil); got != exitErrors || waited != 1 {
		t.Errorf("double click with a TUI that fails: exit code %d, waited %d times; want %d and once", got, waited, exitErrors)
	}
	if !strings.Contains(logs.String(), "The terminal UI stopped: the terminal went away.") {
		t.Errorf("log %q lacks why the TUI stopped", logs)
	}

	ownConsole = func() bool { return false }
	showTUI = func(context.Context, tui.Options) error {
		t.Error("the TUI was shown from a terminal")
		return nil
	}
	out.Reset()
	if got := run(context.Background(), nil); got != exitOK || !strings.Contains(out.String(), "Usage:\n  tagshelf [flags]") || waited != 1 {
		t.Errorf("from a terminal: exit code %d, waited %d times, printed\n%s", got, waited, out.String())
	}
}

// TestTUIRunnerRefusesASourceThatIsNotAFolder checks that o says what is
// wrong with the source, and what to do, before anything is opened.
func TestTUIRunnerRefusesASourceThatIsNotAFolder(t *testing.T) {
	tmp, _, _ := cliEnv(t)
	noSink := func(engine.Event) {}
	for source, want := range map[string]string{
		"":                           "no source folder is set",
		filepath.Join(tmp, "nope"):   "the source folder " + filepath.Join(tmp, "nope") + " does not exist",
		filepath.Join(tmp, "f.yaml"): "the source " + filepath.Join(tmp, "f.yaml") + " is a file, not a folder",
	} {
		writeFile(t, filepath.Join(tmp, "f.yaml"), "")
		r, lib := tuiRunnerFor(t, tmp, source)
		_, err := r.Run(context.Background(), tui.RunOrganize, false, noSink)
		var start *tui.StartError
		if !errors.As(err, &start) || start.What != want || start.Todo == "" {
			t.Errorf("organize from %q: %v (%+v), want a StartError saying %q and what to do", source, err, start, want)
		}
		if _, err := os.Stat(lib); !os.IsNotExist(err) {
			t.Errorf("the refused run made the library folder: %v", err)
		}
	}
	// An index reads only the library, and does not look at the source.
	r, _ := tuiRunnerFor(t, tmp, filepath.Join(tmp, "nope"))
	if finish, err := r.Run(context.Background(), tui.RunIndex, false, noSink); err != nil || finish.State != engine.StateOK {
		t.Errorf("index with a source that is not there: %+v, %v", finish, err)
	}
}
