package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/zachjamesgreen/tagshelf/internal/config"
	"github.com/zachjamesgreen/tagshelf/internal/engine"
	"github.com/zachjamesgreen/tagshelf/internal/store"
	"github.com/zachjamesgreen/tagshelf/internal/tagwrite"
	"github.com/zachjamesgreen/tagshelf/internal/tui"
)

// TestMain keeps every test of the package away from the user's own files.
// go test starts in the repository root, where a config.yaml would be the
// owner's real one, and tagshelf picks that file up when -config is left out;
// its databases go under the user's application data unless told otherwise.
// When the working directory has no config.yaml it reads the user's own
// config file, under the user's settings. So the tests run in an empty folder
// with the data and settings folders in a temp dir, and no test has to
// remember to pass -config or to move a folder. Nor is any test taken for a
// double click, whatever console go test was started from.
func TestMain(m *testing.M) {
	os.Exit(testMain(m))
}

func testMain(m *testing.M) int {
	start, err := os.Getwd()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	tmp, err := os.MkdirTemp("", "tagshelf-test-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer os.RemoveAll(tmp)
	defer os.Chdir(start) // a folder cannot be removed from inside it
	work, data, settings := filepath.Join(tmp, "work"), filepath.Join(tmp, "data"), filepath.Join(tmp, "settings")
	ownConsole = func() bool { return false }
	waitForEnter = func() {}
	// The TUI needs a terminal, and would wait on one for good: a test that
	// means to open it puts its own stand-in here.
	showTUI = func(context.Context, tui.Options) error { return errors.New("the tests have no terminal") }
	for _, step := range []func() error{
		func() error { return os.Mkdir(work, 0o755) },
		func() error { return os.Mkdir(data, 0o755) },
		func() error { return os.Mkdir(settings, 0o755) },
		func() error { return os.Chdir(work) },
		func() error { return os.Setenv("LOCALAPPDATA", data) },
		func() error { return os.Setenv("XDG_DATA_HOME", data) },
		func() error { return os.Setenv("APPDATA", settings) },
		func() error { return os.Setenv("XDG_CONFIG_HOME", settings) },
	} {
		if err := step(); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
	}
	return m.Run()
}

// TestTestsRunAwayFromTheUsersFiles checks what TestMain set up, and that a
// run with no -config then finds no config file to take a library from.
func TestTestsRunAwayFromTheUsersFiles(t *testing.T) {
	_, _, logs := cliEnv(t)
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if entries, err := os.ReadDir(wd); err != nil || len(entries) != 0 {
		t.Fatalf("the tests run in %q, which holds %v (%v); want an empty folder", wd, entries, err)
	}
	if !strings.Contains(wd, "tagshelf-test-") {
		t.Errorf("the tests run in %q, want a temp folder", wd)
	}
	if user, err := config.UserPath(); err != nil || !strings.HasPrefix(user, os.TempDir()) {
		t.Errorf("the user's config file is %q, %v; want one in a temp folder", user, err)
	} else if _, err := os.Stat(user); !os.IsNotExist(err) {
		t.Errorf("the user's config file is there before any test made it: %v", err)
	}

	// With no -config and no -dest there is nothing to say where the library
	// is, unless a config.yaml was found.
	song := filepath.Join(t.TempDir(), "src", "loose.mp3")
	writeFile(t, song, "no tags")
	if got := run(context.Background(), []string{"-source", filepath.Dir(song)}); got != exitCannotStart {
		t.Errorf("run without -config or -dest = %d, want %d\n%s", got, exitCannotStart, logs)
	}
	if !strings.Contains(logs.String(), "tagshelf cannot start: no library folder is set.") || strings.Contains(logs.String(), "Using config file") {
		t.Errorf("log %q: want the run refused for lack of a destination, with no config file found", logs)
	}
	if _, err := os.Stat(song); err != nil {
		t.Errorf("the song moved: %v", err)
	}
}

// TestTestMainMovesTheDataFolder checks the data folder TestMain set for the
// whole package, before any test's own.
func TestTestMainMovesTheDataFolder(t *testing.T) {
	for _, name := range []string{"LOCALAPPDATA", "XDG_DATA_HOME"} {
		if dir := os.Getenv(name); !strings.Contains(dir, "tagshelf-test-") {
			t.Errorf("%s = %q, want the temp folder TestMain made", name, dir)
		}
	}
}

// TestRunRefusesLeftoverArguments checks that an argument that is not a flag
// stops the run. The flag package reads no further than such an argument, so
// without the refusal every flag after it would be dropped in silence.
func TestRunRefusesLeftoverArguments(t *testing.T) {
	tests := []struct {
		name  string
		extra []string
		first string // the argument the error must name
	}{
		{"a value after a flag that takes none hides -dry-run", []string{"-verbose", "true", "-dry-run"}, "true"},
		{"tui after the flags is not the subcommand", []string{"tui"}, "tui"},
		{"help is not a command", []string{"help"}, "help"},
		{"nor is index", []string{"index", "-dry-run"}, "index"},
		{"nor is version", []string{"version"}, "version"},
		{"only the first is named", []string{"one", "two"}, "one"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmp, config, logs := cliEnv(t)
			song := filepath.Join(tmp, "src", "loose.mp3")
			writeFile(t, song, "no tags")
			lib := filepath.Join(tmp, "lib")
			args := append([]string{"-config", config, "-source", filepath.Join(tmp, "src"), "-dest", lib}, tt.extra...)

			if got := run(context.Background(), args); got != exitCannotStart {
				t.Errorf("run(%q) = %d, want %d", args, got, exitCannotStart)
			}
			if want := fmt.Sprintf("tagshelf cannot start: unexpected argument %q: tagshelf takes only flags.", tt.first); !strings.Contains(logs.String(), want) {
				t.Errorf("log %q lacks %q", logs, want)
			}
			if _, err := os.Stat(song); err != nil {
				t.Errorf("the refused run moved the song: %v", err)
			}
			if _, err := os.Stat(lib); !os.IsNotExist(err) {
				t.Errorf("the refused run made the library folder: %v", err)
			}
		})
	}
}

// TestRunGoesByTheConfigFilesDryRun checks that dry_run in the config file
// holds unless -dry-run is typed, either way: the file's true may be all that
// stands between a person and a real run.
func TestRunGoesByTheConfigFilesDryRun(t *testing.T) {
	tests := []struct {
		name  string
		yaml  string
		extra []string
		moved bool
	}{
		{"the file says dry", "dry_run: true\n", nil, false},
		{"the file says dry, the flag says not", "dry_run: true\n", []string{"-dry-run=false"}, true},
		{"the file says real", "dry_run: false\n", nil, true},
		{"the file says real, the flag says dry", "dry_run: false\n", []string{"-dry-run"}, false},
		{"the file says nothing", "", nil, true},
		{"the file says nothing, the flag says dry", "", []string{"-dry-run=true"}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmp, config, logs := cliEnv(t)
			writeFile(t, config, tt.yaml)
			song := filepath.Join(tmp, "src", "loose.mp3")
			writeFile(t, song, "no tags")
			args := append([]string{"-config", config, "-source", filepath.Join(tmp, "src"), "-dest", filepath.Join(tmp, "lib")}, tt.extra...)
			if got := run(context.Background(), args); got != exitOK {
				t.Fatalf("run(%q) = %d, want %d\n%s", args, got, exitOK, logs)
			}
			_, err := os.Stat(song)
			if moved := os.IsNotExist(err); moved != tt.moved {
				t.Errorf("song moved = %t, want %t (%v)\n%s", moved, tt.moved, err, logs)
			}
			if want := fmt.Sprintf("Dry-Run:      %t", !tt.moved); !strings.Contains(logs.String(), want) {
				t.Errorf("log lacks %q:\n%s", want, logs)
			}
		})
	}
}

// TestRunRefusesAConfigFileWithAnUnknownKey checks a misspelt key stops the
// run, in a file named with -config and in a config.yaml found in the
// working directory alike.
func TestRunRefusesAConfigFileWithAnUnknownKey(t *testing.T) {
	for _, command := range [][]string{nil, {"tui"}} {
		for _, discovered := range []bool{false, true} {
			t.Run(fmt.Sprintf("%v discovered=%t", command, discovered), func(t *testing.T) {
				tmp, config, logs := cliEnv(t)
				song := filepath.Join(tmp, "src", "loose.mp3")
				writeFile(t, song, "no tags")
				args := append(command, "-source", filepath.Join(tmp, "src"), "-dest", filepath.Join(tmp, "lib"))
				const yaml = "verbose: true\ndry-run: true\n"
				if discovered {
					dir := t.TempDir()
					writeFile(t, filepath.Join(dir, "config.yaml"), yaml)
					t.Chdir(dir)
				} else {
					writeFile(t, config, yaml)
					args = append(args, "-config", config)
				}

				ctx, cancel := context.WithTimeout(context.Background(), refusalTimeout)
				defer cancel()
				const want = `unknown key "dry-run" on line 2`
				if command != nil {
					// The TUI opens all the same, on what is wrong with the
					// file: with no library, and no way to start a run.
					var shown tui.Options
					real := showTUI
					defer func() { showTUI = real }()
					showTUI = func(_ context.Context, opts tui.Options) error {
						shown = opts
						return nil
					}
					if got := run(ctx, args); got != exitOK {
						t.Errorf("run(%q) = %d, want %d\n%s", args, got, exitOK, logs)
					}
					if n := shown.NoLibrary; n == nil || !strings.Contains(n.What, want) || n.CanRun || shown.Runner != nil || shown.Store != nil {
						t.Errorf("the TUI was opened with %+v (no library %+v), want only what is wrong with the config file", shown, n)
					}
					return
				}
				if got := run(ctx, args); got != exitCannotStart {
					t.Errorf("run(%q) = %d, want %d", args, got, exitCannotStart)
				}
				if !strings.Contains(logs.String(), want) {
					t.Errorf("log %q lacks %q", logs, want)
				}
				if _, err := os.Stat(song); err != nil {
					t.Errorf("the refused run moved the song: %v", err)
				}
			})
		}
	}
}

// TestRunFlagModes covers the modes of the command line the other tests do
// not start: -reorganize, a run with nothing to organize from, and -h.
func TestRunFlagModes(t *testing.T) {
	t.Run("reorganize sets the source and the destination", func(t *testing.T) {
		tmp, config, logs := cliEnv(t)
		lib := filepath.Join(tmp, "lib")
		inLib := filepath.Join(lib, "loose.mp3")
		writeFile(t, inLib, "no tags")
		// The config file names another source and another destination; a slip
		// that set only one of the two would use the other from here.
		elsewhere := filepath.Join(tmp, "incoming", "song.mp3")
		writeFile(t, elsewhere, "no tags either")
		otherLib := filepath.Join(tmp, "other-lib")
		writeFile(t, config, fmt.Sprintf("source: %q\ndestination: %q\n", filepath.Dir(elsewhere), otherLib))

		if got := run(context.Background(), []string{"-config", config, "-reorganize", lib}); got != exitOK {
			t.Fatalf("run = %d, want %d\n%s", got, exitOK, logs)
		}
		for _, want := range []string{"Mode:         reorganize library in place", "Source:       " + lib, "Destination:  " + lib, "Not qualifying, left:     1"} {
			if !strings.Contains(logs.String(), want) {
				t.Errorf("log lacks %q:\n%s", want, logs)
			}
		}
		for _, stays := range []string{inLib, elsewhere} {
			if _, err := os.Stat(stays); err != nil {
				t.Errorf("%q did not stay where it was: %v", stays, err)
			}
		}
		if _, err := os.Stat(otherLib); !os.IsNotExist(err) {
			t.Errorf("the config file's destination was used: %v", err)
		}
		if id, err := store.ReadLibraryID(lib); err != nil || id == "" {
			t.Errorf("library ID of the reorganized folder = %q, %v; want one", id, err)
		}
	})

	t.Run("no source", func(t *testing.T) {
		tmp, config, logs := cliEnv(t)
		lib := filepath.Join(tmp, "lib")
		if got := run(context.Background(), []string{"-config", config, "-dest", lib}); got != exitCannotStart {
			t.Errorf("run = %d, want %d", got, exitCannotStart)
		}
		if !strings.Contains(logs.String(), "tagshelf cannot start: no source folder is set.") {
			t.Errorf("log %q does not ask for a source", logs)
		}
		if _, err := os.Stat(lib); !os.IsNotExist(err) {
			t.Errorf("the refused run made the library folder: %v", err)
		}
	})

	for _, command := range [][]string{nil, {"tui"}} {
		t.Run(fmt.Sprintf("%v -h", command), func(t *testing.T) {
			tmp, config, logs := cliEnv(t)
			song := filepath.Join(tmp, "src", "loose.mp3")
			writeFile(t, song, "no tags")
			lib := filepath.Join(tmp, "lib")
			args := append(command, "-config", config, "-source", filepath.Join(tmp, "src"), "-dest", lib, "-h")
			ctx, cancel := context.WithTimeout(context.Background(), refusalTimeout)
			defer cancel()
			if got := run(ctx, args); got != exitOK {
				t.Errorf("run(%q) = %d, want %d", args, got, exitOK)
			}
			// Both commands take the folder flags, and say so.
			for _, want := range []string{"tagshelf tui", "-untagged", "-other", "-duplicates", "-compilations", "-workers", "-no-cue-rewrite", "-dry-run", "-db"} {
				if !strings.Contains(logs.String(), want) {
					t.Errorf("usage lacks %q:\n%s", want, logs)
				}
			}
			if _, err := os.Stat(song); err != nil {
				t.Errorf("-h moved the song: %v", err)
			}
			if _, err := os.Stat(lib); !os.IsNotExist(err) {
				t.Errorf("-h made the library folder: %v", err)
			}
		})
	}
}

func TestExitCode(t *testing.T) {
	for state, want := range map[engine.RunState]int{
		engine.StateOK:          exitOK,
		engine.StateErrors:      exitErrors,
		engine.StateInterrupted: exitInterrupted,
		// A run that could not be recorded touched nothing.
		engine.StateNotStarted: exitCannotStart,
	} {
		if got := exitCode(state); got != want {
			t.Errorf("exitCode(%v) = %d, want %d", state, got, want)
		}
	}
}

// TestEngineOptions checks every setting reaches the engine: each field of
// the configuration is given a value that is not its default, and the whole
// of the engine's options compared.
func TestEngineOptions(t *testing.T) {
	on, off := true, false
	cfg := config.Config{
		Source: "src", Destination: "dest", Untagged: "untagged", Other: "other", Duplicates: "duplicates",
		AutoResolveExact: &off, Compilations: "comps", Workers: 7, DryRun: true, Verbose: true, CueRewrite: &off,
		MusicBrainzContact: "me@example.com", GenreFromMusicBrainz: "off", CoverArtFile: true, CoverArtReplace: "always", Database: "db",
	}
	v := reflect.ValueOf(cfg)
	for i := range v.NumField() {
		if v.Field(i).IsZero() {
			t.Fatalf("the test leaves config field %s at its zero value", v.Type().Field(i).Name)
		}
	}
	want := engine.Options{
		Source: "src", Destination: "dest", Untagged: "untagged", Other: "other", Duplicates: "duplicates",
		Compilations: "comps", Workers: 7, DryRun: true, NoCueRewrite: true, NoAutoResolve: true,
	}
	got := engineOptions(cfg)
	// A function compares equal to nothing: that the run is given the one
	// that clears a crashed tag save's copies is TestRunRemovesStaleCopies.
	if got.RemoveStaleCopies == nil {
		t.Error("engineOptions leaves RemoveStaleCopies nil: a crashed tag save's copies would never be removed")
	}
	got.RemoveStaleCopies = nil
	if !reflect.DeepEqual(got, want) {
		t.Errorf("engineOptions = %+v\nwant           %+v", got, want)
	}

	// The two settings that are on unless turned off, left alone and turned on.
	for _, setting := range []*bool{nil, &on} {
		cfg.AutoResolveExact, cfg.CueRewrite = setting, setting
		if got := engineOptions(cfg); got.NoAutoResolve || got.NoCueRewrite {
			t.Errorf("with the settings at %v: NoAutoResolve %t, NoCueRewrite %t; want both false", setting, got.NoAutoResolve, got.NoCueRewrite)
		}
	}
	// Each on its own, so that one cannot stand in for the other.
	cfg.AutoResolveExact, cfg.CueRewrite = &off, &on
	if got := engineOptions(cfg); !got.NoAutoResolve || got.NoCueRewrite {
		t.Errorf("auto-resolve off alone: NoAutoResolve %t, NoCueRewrite %t", got.NoAutoResolve, got.NoCueRewrite)
	}
	cfg.AutoResolveExact, cfg.CueRewrite = &on, &off
	if got := engineOptions(cfg); got.NoAutoResolve || !got.NoCueRewrite {
		t.Errorf("cue rewriting off alone: NoAutoResolve %t, NoCueRewrite %t", got.NoAutoResolve, got.NoCueRewrite)
	}
}

// TestRunHandsItsSettingsToTheEngine runs the CLI with -other and
// -compilations and with auto-resolve turned off in the config file, and
// looks at where the files went.
func TestRunHandsItsSettingsToTheEngine(t *testing.T) {
	tmp, config, logs := cliEnv(t)
	writeFile(t, config, "auto_resolve_exact: false\n")
	src, lib, other := filepath.Join(tmp, "src"), filepath.Join(tmp, "lib"), filepath.Join(tmp, "elsewhere")
	audio := strings.Repeat("some audio ", 40)
	writeFile(t, filepath.Join(lib, "_Untagged", "song.mp3"), audio)
	args := []string{"-config", config, "-dest", lib, "-other", other, "-compilations", "Mixed"}
	if got := run(context.Background(), append(args, "-index")); got != exitOK {
		t.Fatalf("index run = %d\n%s", got, logs)
	}
	writeFile(t, filepath.Join(src, "Folder", "song.mp3"), audio)
	writeFile(t, filepath.Join(src, "notes.txt"), "not audio")

	logs.Reset()
	if got := run(context.Background(), append(args, "-source", src)); got != exitOK {
		t.Fatalf("run = %d\n%s", got, logs)
	}
	for _, want := range []string{"Other:        " + other, "Compilations: Mixed", "Auto-resolve: false", "1 duplicate group needs review"} {
		if !strings.Contains(logs.String(), want) {
			t.Errorf("log lacks %q:\n%s", want, logs)
		}
	}
	if _, err := os.Stat(filepath.Join(other, "notes.txt")); err != nil {
		t.Errorf("the non-audio file did not go to the folder -other names: %v", err)
	}
	if _, err := os.Stat(filepath.Join(lib, "_Other")); !os.IsNotExist(err) {
		t.Errorf("the default _Other folder was used: %v", err)
	}
	// With auto-resolve off, the incoming copy is not set aside by the run.
	if _, err := os.Stat(filepath.Join(lib, "_Duplicates")); !os.IsNotExist(err) {
		t.Errorf("a copy was moved to _Duplicates although auto_resolve_exact is off: %v", err)
	}
	if _, err := os.Stat(filepath.Join(src, "Folder", "song.mp3")); !os.IsNotExist(err) {
		t.Errorf("the incoming copy is still in the source: %v", err)
	}
	if !strings.Contains(logs.String(), "Untagged files moved:     1") || !strings.Contains(logs.String(), "Duplicates moved:         0") {
		t.Errorf("want the incoming copy moved as untagged, not as a duplicate:\n%s", logs)
	}
}

// TestRunRemovesStaleCopies checks that a real run clears out the copy a tag
// save that crashed left beside a file, once it is old enough not to belong
// to a save going on now, and that a dry run does not. The library's ID and
// lock files, which carry the same prefix, stay.
func TestRunRemovesStaleCopies(t *testing.T) {
	for _, dry := range []bool{true, false} {
		tmp, config, logs := cliEnv(t)
		writeFile(t, config, "")
		src, lib := filepath.Join(tmp, "src"), filepath.Join(tmp, "lib")
		writeFile(t, filepath.Join(src, "Rip", "loose.mp3"), "no tags")
		stale := filepath.Join(src, "Rip", ".tagshelf-123456.mp3")
		writeFile(t, stale, "half a save")
		fresh := filepath.Join(src, "Rip", ".tagshelf-654321.mp3")
		writeFile(t, fresh, "a save going on")
		old := time.Now().Add(-2 * tagwrite.StaleAfter)
		if err := os.Chtimes(stale, old, old); err != nil {
			t.Fatal(err)
		}
		args := []string{"-config", config, "-source", src, "-dest", lib, fmt.Sprintf("-dry-run=%t", dry)}
		if got := run(context.Background(), args); got != exitOK {
			t.Fatalf("dry %t: run = %d\n%s", dry, got, logs)
		}
		_, err := os.Stat(stale)
		if removed := os.IsNotExist(err); removed == dry {
			t.Errorf("dry %t: the stale copy removed: %t (%v)\n%s", dry, removed, err, logs)
		}
		if _, err := os.Stat(fresh); err != nil {
			t.Errorf("dry %t: the copy of a save going on now was removed: %v", dry, err)
		}
		if dry {
			continue
		}
		if !strings.Contains(logs.String(), "Removed the temporary copy a crash left at") {
			t.Errorf("the log does not say what was removed:\n%s", logs)
		}
		for _, own := range []string{store.LibraryIDFile, store.LibraryLockFile} {
			if _, err := os.Stat(filepath.Join(lib, own)); err != nil {
				t.Errorf("the library's %s after the run: %v", own, err)
			}
		}
	}
}

// TestRunRefusesADatabaseOfAnotherLibrary checks that one database named for
// two libraries stops the second run before it rewrites the first's index.
func TestRunRefusesADatabaseOfAnotherLibrary(t *testing.T) {
	tmp, config, logs := cliEnv(t)
	db := filepath.Join(tmp, "shared.db")
	first, second := filepath.Join(tmp, "first"), filepath.Join(tmp, "second")
	writeFile(t, filepath.Join(first, "_Untagged", "a.mp3"), "no tags")
	song := filepath.Join(tmp, "src", "loose.mp3")
	writeFile(t, song, "no tags either")
	if got := run(context.Background(), []string{"-config", config, "-dest", first, "-db", db, "-index"}); got != exitOK {
		t.Fatalf("-index of the first library = %d\n%s", got, logs)
	}

	logs.Reset()
	args := []string{"-config", config, "-source", filepath.Join(tmp, "src"), "-dest", second, "-db", db}
	if got := run(context.Background(), args); got != exitCannotStart {
		t.Errorf("run(%q) = %d, want %d", args, got, exitCannotStart)
	}
	if !strings.Contains(logs.String(), "the database belongs to another library") {
		t.Errorf("log %q does not say whose the database is", logs)
	}
	if _, err := os.Stat(song); err != nil {
		t.Errorf("the refused run moved the song: %v", err)
	}
	s, err := store.Open(db)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if n, err := s.CountFiles(); err != nil || n != 1 {
		t.Errorf("the first library's index holds %d files (%v), want its 1", n, err)
	}
	if roots, err := s.Roots(); err != nil || len(roots) == 0 || roots[0].Path != first {
		t.Errorf("roots = %+v, %v; want the first library's", roots, err)
	}
}
