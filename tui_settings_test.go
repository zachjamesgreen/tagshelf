package main

import (
	"context"
	"errors"
	"flag"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/zachjamesgreen/tagshelf/internal/config"
	"github.com/zachjamesgreen/tagshelf/internal/engine"
	"github.com/zachjamesgreen/tagshelf/internal/tui"
)

// sessionFor is the session `tagshelf tui args` would have, before it has
// opened anything.
func sessionFor(t *testing.T, args ...string) *tuiSession {
	t.Helper()
	flags := tuiFlags(flag.NewFlagSet("tagshelf tui", flag.ContinueOnError))
	if code, done := flags.parse(args); done {
		t.Fatalf("tagshelf tui %q was refused with exit code %d", args, code)
	}
	s := &tuiSession{flags: flags}
	t.Cleanup(s.close)
	return s
}

// TestNewConfigFileStartsFromTheTemplate checks what the Settings tab shows
// before there is a config file: the example's settings, with dry run on and
// no folders, and that the template holds every key so that a new file
// explains each of them.
func TestNewConfigFileStartsFromTheTemplate(t *testing.T) {
	on := true
	want := config.Config{AutoResolveExact: &on, Compilations: "Various Artists", DryRun: true, CueRewrite: &on,
		GenreFromMusicBrainz: "fill"}
	if got := newFileConfig(); !reflect.DeepEqual(got, want) {
		t.Errorf("a new file starts as %+v\nwant              %+v", got, want)
	}
	for _, key := range config.Keys() {
		if !strings.Contains(string(configTemplate), "\n"+key+":") && !strings.Contains(string(configTemplate), "\n#"+key+":") {
			t.Errorf("the template has no line for %s", key)
		}
		if optionalKeys[key] != strings.Contains(string(configTemplate), "\n#"+key+":") {
			t.Errorf("%s: optional %t, but the template comments it out: %t", key, optionalKeys[key], !optionalKeys[key])
		}
	}
}

// TestSettingsLoad checks which file the Settings tab edits and what it
// reads of it.
func TestSettingsLoad(t *testing.T) {
	tmp, empty, _ := cliEnv(t)
	t.Chdir(t.TempDir())
	user, err := config.UserPath()
	if err != nil {
		t.Fatal(err)
	}

	// Nothing anywhere: the user's own file, which saving would make.
	f := sessionFor(t).Load()
	if f.Path != user || !f.New || f.Problem != "" || !f.Config.DryRun || f.Config.Destination != "" || f.Text != nil || len(f.Session) != 0 {
		t.Errorf("with no config file: %+v", f)
	}

	// A file named that is not there yet: that file.
	named := filepath.Join(tmp, "new.yaml")
	if f = sessionFor(t, "-config", named).Load(); f.Path != named || !f.New || f.Problem != "" {
		t.Errorf("with a named file that is not there: %+v", f)
	}

	// A file that is there: what it says, and its text.
	const yaml = "destination: 'D:\\Music'\nworkers: 3\n"
	writeFile(t, empty, yaml)
	f = sessionFor(t, "-config", empty).Load()
	if f.Path != empty || f.New || f.Problem != "" || string(f.Text) != yaml || !reflect.DeepEqual(f.Config, config.Config{Destination: `D:\Music`, Workers: 3}) {
		t.Errorf("with a file: %+v", f)
	}

	// One that cannot be read as settings: why, and nothing to edit.
	writeFile(t, empty, "dry-run: true\n")
	f = sessionFor(t, "-config", empty).Load()
	if f.Path != empty || f.New || !strings.Contains(f.Problem, `has an unknown key "dry-run" on line 1`) {
		t.Errorf("with a file that has an unknown key: %+v", f)
	}

	// The folder the databases are kept under is named for the row of a
	// database that is not set.
	if f.DataFolder != dataFolderName() || f.DataFolder == "" {
		t.Errorf("the data folder is named %q, want %q", f.DataFolder, dataFolderName())
	}

	// The flags typed are what the session goes by.
	writeFile(t, empty, yaml)
	f = sessionFor(t, "-config", empty, "-dest", filepath.FromSlash(tmp+"/lib/./x/.."), "-dry-run", "-no-cue-rewrite", "-workers", "2", "-compilations", "Mixed", "-db", filepath.Join(tmp, "my.db")).Load()
	want := map[string]string{"destination": filepath.Join(tmp, "lib"), "dry_run": "on", "cue_rewrite": "off", "workers": "2", "compilations": "Mixed", "database": filepath.Join(tmp, "my.db")}
	if !reflect.DeepEqual(f.Session, want) {
		t.Errorf("the session's settings = %v\nwant                    %v", f.Session, want)
	}
	if f = sessionFor(t, "-config", empty, "-dry-run=false", "-no-cue-rewrite=false").Load(); !reflect.DeepEqual(f.Session, map[string]string{"dry_run": "off", "cue_rewrite": "on"}) {
		t.Errorf("flags typed as off: %v", f.Session)
	}
}

// TestSettingsCheck checks what is refused before a save and what is asked.
func TestSettingsCheck(t *testing.T) {
	tmp, empty, _ := cliEnv(t)
	in := func(name string) string { return filepath.Join(tmp, name) }
	if err := os.Mkdir(in("src"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(in("lib"), 0o755); err != nil {
		t.Fatal(err)
	}
	s := sessionFor(t, "-config", empty)
	read := s.Load()
	tests := []struct {
		name     string
		cfg      config.Config
		problems map[string]string
		confirm  []string
	}{
		{"folders that are there", config.Config{Source: in("src"), Destination: in("lib")}, nil, nil},
		{"nothing set", config.Config{}, nil, nil},
		{"a library folder to make", config.Config{Destination: in("new")}, nil, []string{"Make the library folder " + in("new") + "?"}},
		{"a source that is not there", config.Config{Source: in("nope"), Destination: in("new")}, map[string]string{"source": "does not exist"}, nil},
		{"a source that is a file", config.Config{Source: empty}, map[string]string{"source": "is a file, not a folder"}, nil},
		{"a library folder that is a file", config.Config{Destination: empty}, map[string]string{"destination": "is a file, not a folder"}, nil},
		{"paths that are not whole", config.Config{Source: "src", Destination: `lib\x`, Untagged: "u", Database: "my.db"},
			map[string]string{"source": "must be a whole path", "destination": "must be a whole path", "untagged": "must be a whole path", "database": "must be a whole path"}, nil},
		{"what the settings themselves rule out", config.Config{Source: in("src"), Destination: in("lib"), Untagged: in("lib"), Workers: -1},
			map[string]string{"untagged": "library folder itself", "workers": "0 or more"}, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := s.Check(tt.cfg, read)
			if len(got.Problems) != len(tt.problems) || !reflect.DeepEqual(got.Confirm, tt.confirm) {
				t.Errorf("Check = %+v\nwant problems with %v and to ask %q", got, tt.problems, tt.confirm)
			}
			for key, part := range tt.problems {
				if !strings.Contains(got.Problems[key], part) {
					t.Errorf("the problem with %s is %q, want it to say %q", key, got.Problems[key], part)
				}
			}
		})
	}
	if _, err := os.Stat(in("new")); !os.IsNotExist(err) {
		t.Errorf("checking made the library folder: %v", err)
	}
}

// TestSettingsCheckAsksBeforeLeavingALibrary checks the questions about the
// library the TUI has open: changing its folder or its database is asked
// about, unless a flag keeps the session on it.
func TestSettingsCheckAsksBeforeLeavingALibrary(t *testing.T) {
	tmp, cfgFile, logs := cliEnv(t)
	lib, other := filepath.Join(tmp, "lib"), filepath.Join(tmp, "other")
	if err := os.Mkdir(other, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, cfgFile, "destination: '"+lib+"'\n")
	if got := run(context.Background(), []string{"-config", cfgFile, "-index"}); got != exitOK {
		t.Fatalf("-index = %d\n%s", got, logs)
	}
	asked := func(s *tuiSession, change func(*config.Config)) []string {
		t.Helper()
		if opts, err := s.open(); err != nil || opts.Store == nil {
			t.Fatalf("the library did not open: %+v, %v", opts, err)
		}
		read := s.Load()
		c := read.Config
		change(&c)
		check := s.Check(c, read)
		if len(check.Problems) != 0 {
			t.Fatalf("problems: %v", check.Problems)
		}
		return check.Confirm
	}

	s := sessionFor(t, "-config", cfgFile)
	if got := asked(s, func(c *config.Config) { c.Workers = 2 }); got != nil {
		t.Errorf("a change that keeps the library asks %q", got)
	}
	// The same folder written another way: with a separator at its end, and
	// on Windows, where case does not matter, in capitals as well.
	another := lib + string(filepath.Separator)
	if runtime.GOOS == "windows" {
		another = strings.ToUpper(lib) + `\`
	}
	if got := asked(s, func(c *config.Config) { c.Destination = another }); got != nil {
		t.Errorf("the same folder written another way asks %q", got)
	}
	if got, want := asked(s, func(c *config.Config) { c.Destination = other }), []string{"This opens a different library. Your music in " + lib + " is not moved."}; !reflect.DeepEqual(got, want) {
		t.Errorf("another library folder asks %q, want %q", got, want)
	}
	if got := asked(s, func(c *config.Config) { c.Destination = "" }); len(got) != 1 || !strings.Contains(got[0], "without a library folder") {
		t.Errorf("no library folder asks %q", got)
	}
	if got := asked(s, func(c *config.Config) { c.Database = filepath.Join(tmp, "my.db") }); len(got) != 1 || !strings.Contains(got[0], "different database") {
		t.Errorf("another database asks %q", got)
	}
	// Both at once is one question: the library's.
	if got := asked(s, func(c *config.Config) { c.Destination, c.Database = other, filepath.Join(tmp, "my.db") }); len(got) != 1 || !strings.Contains(got[0], "different library") {
		t.Errorf("another library and database ask %q", got)
	}

	// With -dest the session stays on its library whatever the file says.
	flagged := sessionFor(t, "-config", cfgFile, "-dest", lib)
	if got := asked(flagged, func(c *config.Config) { c.Destination = other }); got != nil {
		t.Errorf("with -dest, another library folder in the file asks %q", got)
	}
}

// TestSettingsSaveMakesTheFile saves settings where there is no config file:
// the file is made from the template, with the settings in it, and the
// library folder is made.
func TestSettingsSaveMakesTheFile(t *testing.T) {
	tmp, _, _ := cliEnv(t)
	t.Chdir(t.TempDir())
	s := sessionFor(t)
	read := s.Load()
	lib := filepath.Join(tmp, "new", "Music")
	c := read.Config
	c.Source, c.Destination, c.MusicBrainzContact, c.Workers = tmp, lib, "me@example.org", 2
	note, err := s.Save(c, read)
	if err != nil || note != "" {
		t.Fatalf("Save = %q, %v", note, err)
	}
	if info, err := os.Stat(lib); err != nil || !info.IsDir() {
		t.Errorf("the library folder was not made: %v", err)
	}
	text, err := os.ReadFile(read.Path)
	if err != nil {
		t.Fatal(err)
	}
	quoted := func(p string) string { return `"` + strings.ReplaceAll(p, `\`, `\\`) + `"` }
	for _, want := range []string{
		"# tagshelf configuration\n",
		"\nsource: " + quoted(tmp) + " ",
		"\ndestination: " + quoted(lib) + " ",
		"\n#untagged: ", "\n#other: ", "\n#duplicates: ", "\n#database: ",
		"\nmusicbrainz_contact: \"me@example.org\" # email or URL",
		"\nworkers: 2 ", "\ndry_run: true ", "\ncue_rewrite: true ", "\nauto_resolve_exact: true ", "\nverbose: false\n",
	} {
		if !strings.Contains(string(text), want) {
			t.Errorf("the file lacks %q:\n%s", want, text)
		}
	}
	if len(strings.Split(string(text), "\n")) != len(strings.Split(string(configTemplate), "\n")) {
		t.Errorf("the file does not have the template's lines:\n%s", text)
	}

	// Read again it is the settings saved, and no longer new.
	again := s.Load()
	if again.New || again.Problem != "" || len(again.Config.Changed(c)) != 0 || string(again.Text) != string(text) {
		t.Errorf("read again: %+v\nwant the settings saved: %+v", again, c)
	}

	// Saved from what was read before, when a file has appeared since: no.
	if _, err := s.Save(c, read); !errors.Is(err, tui.ErrSettingsChanged) {
		t.Errorf("a save over a file that appeared = %v, want ErrSettingsChanged", err)
	}
}

// TestSettingsSaveChangesOnlyItsLines saves a change to a config file with
// comments of its own, and checks the rest of the file is left alone.
func TestSettingsSaveChangesOnlyItsLines(t *testing.T) {
	tmp, cfgFile, _ := cliEnv(t)
	lib := filepath.Join(tmp, "lib")
	if err := os.Mkdir(lib, 0o755); err != nil {
		t.Fatal(err)
	}
	before := "# my settings\r\n\r\ndestination: '" + lib + "'   # the big drive\r\ndry_run:   true\r\n# compilations: Mixed\r\n"
	writeFile(t, cfgFile, before)
	s := sessionFor(t, "-config", cfgFile)
	read := s.Load()
	c := read.Config
	c.DryRun, c.Compilations, c.Verbose = false, "Sampler", true
	if note, err := s.Save(c, read); err != nil || note != "" {
		t.Fatalf("Save = %q, %v", note, err)
	}
	want := "# my settings\r\n\r\ndestination: '" + lib + "'   # the big drive\r\ndry_run: false\r\ncompilations: \"Sampler\"\r\nverbose: true\r\n"
	if got, _ := os.ReadFile(cfgFile); string(got) != want {
		t.Errorf("the file is\n%q\nwant\n%q", got, want)
	}
	if entries, _ := os.ReadDir(tmp); len(entries) != 2 {
		t.Errorf("the folder holds %v, want the config file and the library alone", entries)
	}

	// Changed in an editor since it was read: not written over.
	edited := want + "workers: 7\r\n"
	writeFile(t, cfgFile, edited)
	read2 := read
	read2.Text = []byte(want)
	c.Workers = 1
	if _, err := s.Save(c, read2); !errors.Is(err, tui.ErrSettingsChanged) {
		t.Errorf("a save over a file changed since = %v, want ErrSettingsChanged", err)
	}
	// Or gone.
	os.Remove(cfgFile)
	if _, err := s.Save(c, read2); !errors.Is(err, tui.ErrSettingsChanged) {
		t.Errorf("a save over a file that is gone = %v, want ErrSettingsChanged", err)
	}
	if _, err := os.Stat(cfgFile); !os.IsNotExist(err) {
		t.Errorf("the refused save made the file: %v", err)
	}
}

// TestSettingsSaveThatCannotBeWritten checks a value the file cannot hold is
// an error, with the file and the folders left as they were.
func TestSettingsSaveThatCannotBeWritten(t *testing.T) {
	tmp, cfgFile, _ := cliEnv(t)
	writeFile(t, cfgFile, "workers: 1\n")
	s := sessionFor(t, "-config", cfgFile)
	read := s.Load()
	c := read.Config
	c.Compilations, c.Destination = "two\nlines", filepath.Join(tmp, "lib")
	if _, err := s.Save(c, read); err == nil || !strings.Contains(err.Error(), "cannot be typed") {
		t.Errorf("Save = %v, want the value refused", err)
	}
	if got, _ := os.ReadFile(cfgFile); string(got) != "workers: 1\n" {
		t.Errorf("the file was changed: %q", got)
	}
	if _, err := os.Stat(filepath.Join(tmp, "lib")); !os.IsNotExist(err) {
		t.Errorf("the library folder was made for a save that failed: %v", err)
	}
}

// TestSettingsFromNothingToALibrary goes the way a first start does: the TUI
// opens with nothing set up, the folders are saved on the Settings tab, the
// library is opened again and indexed, and is there.
func TestSettingsFromNothingToALibrary(t *testing.T) {
	tmp, _, logs := cliEnv(t)
	t.Chdir(t.TempDir())
	src, lib := filepath.Join(tmp, "in"), filepath.Join(tmp, "Music")
	writeFile(t, filepath.Join(src, "loose.mp3"), "no tags")
	shown, code := openTUI(t, []string{"tui"}, func(opts tui.Options) {
		if opts.NoLibrary == nil || opts.NoLibrary.Setting != "destination" || opts.Runner != nil {
			t.Fatalf("started on %+v (no library %+v)", opts, opts.NoLibrary)
		}
		read := opts.Settings.Load()
		c := read.Config
		c.Source, c.Destination, c.DryRun = src, lib, false
		check := opts.Settings.Check(c, read)
		if len(check.Problems) != 0 || len(check.Confirm) != 1 {
			t.Fatalf("Check = %+v, want only the question about making the folder", check)
		}
		if _, err := opts.Settings.Save(c, read); err != nil {
			t.Fatal(err)
		}

		opts, err := opts.Reopen()
		if err != nil || opts.NoLibrary == nil || !opts.NoLibrary.CanRun || opts.Destination != lib || opts.Runner == nil || opts.DryRun {
			t.Fatalf("after the save: %+v (no library %+v), %v; want the new folder, not yet indexed", opts, opts.NoLibrary, err)
		}
		if !strings.Contains(strings.Join(opts.NoLibrary.Todo, "\n"), "Or press o to organize "+src+" into it.") {
			t.Errorf("what to do does not offer the source just saved: %q", opts.NoLibrary.Todo)
		}
		finish, err := opts.Runner.Run(context.Background(), tui.RunOrganize, false, func(engine.Event) {})
		if err != nil || finish.State != engine.StateOK {
			t.Fatalf("organize = %+v, %v", finish, err)
		}
		if opts, err = opts.Reopen(); err != nil || opts.Store == nil || opts.NoLibrary != nil {
			t.Fatalf("after the run: %+v, %v; want the library", opts, err)
		}
		if o, err := opts.Store.Overview(); err != nil || o.Untagged != 1 {
			t.Errorf("the library's overview = %+v, %v; want the one untagged file", o, err)
		}
		// The settings now shown are the file's, with no flag over them.
		if again := opts.Settings.Load(); again.New || again.Config.Destination != lib || len(again.Session) != 0 {
			t.Errorf("the settings read again: %+v", again)
		}
	})
	if code != exitOK || shown.Settings == nil {
		t.Errorf("exit code %d\n%s", code, logs)
	}
	// From another folder, with nothing typed but the command, it is found.
	t.Chdir(t.TempDir())
	if again, code := openTUI(t, []string{"tui"}, nil); code != exitOK || again.Store == nil || again.Destination != lib {
		t.Errorf("started again elsewhere: exit code %d, %+v; want the library saved", code, again)
	}
}
