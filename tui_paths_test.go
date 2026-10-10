package main

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

// TestTUIOpensOnAFolderItCannotUse checks tagshelf tui on a config file whose
// folder cannot be used as it is written: the TUI opens, on Settings, with
// the setting named and marked, and no library is opened. Nothing is made
// where it was started.
func TestTUIOpensOnAFolderItCannotUse(t *testing.T) {
	tests := []struct {
		name          string
		yaml          string
		setting, what string
		todo          string
		problems      map[string]string
		offWindows    bool
	}{
		{"another user's home", "destination: '~bob/Music'\n", "destination",
			"the library folder ~bob/Music, set as destination in the config file CONFIG, starts with another user's home folder",
			"Press 6 to change it in Settings. tagshelf knows only ~ alone, your own home folder. Write the folder out in full.",
			map[string]string{"destination": "starts with another user's home folder; only ~ alone, your own, is known"}, false},
		{"Windows paths off Windows", "source: 'E:\\In'\ndestination: 'D:\\Music'\n", "source",
			`the source folder E:\In, set as source in the config file CONFIG, is a Windows path`,
			"Press 6 to change it in Settings. Give the folder as this system writes it, as in ~/Music.",
			map[string]string{"source": "is a Windows path, which this system cannot use", "destination": "is a Windows path, which this system cannot use"}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.offWindows && runtime.GOOS == "windows" {
				t.Skip("on Windows these are paths")
			}
			_, cfgFile, logs := cliEnv(t)
			writeFile(t, cfgFile, tt.yaml)
			work := t.TempDir()
			t.Chdir(work)
			logs.Reset()
			shown, code := openTUI(t, []string{"tui", "-config", cfgFile}, nil)
			if code != exitOK || logs.Len() != 0 {
				t.Errorf("exit code %d, printed %q; want the TUI opened without a word", code, logs)
			}
			n := shown.NoLibrary
			if n == nil || shown.Store != nil || shown.Destination != "" {
				t.Fatalf("the TUI was opened on a library: %+v", shown)
			}
			if want := strings.ReplaceAll(tt.what, "CONFIG", cfgFile); n.What != want || n.Setting != tt.setting || n.CanRun {
				t.Errorf("no library because %q, setting %q, can run %t\nwant               %q, setting %q", n.What, n.Setting, n.CanRun, want, tt.setting)
			}
			if len(n.Todo) != 1 || n.Todo[0] != tt.todo {
				t.Errorf("what to do = %q, want %q", n.Todo, tt.todo)
			}
			if got := shown.Settings.Load().Problems; !reflect.DeepEqual(got, tt.problems) {
				t.Errorf("the settings marked = %v, want %v", got, tt.problems)
			}
			if entries, err := os.ReadDir(work); err != nil || len(entries) != 0 {
				t.Errorf("the folder tagshelf tui was started in holds %v, %v; want nothing made there", entries, err)
			}
		})
	}
}

// TestTUIRefusesAFlagItCannotUse checks a folder typed as a flag that cannot
// be used: the Settings tab changes the config file, not the flags, so the
// TUI does not open, and says which flag it was.
func TestTUIRefusesAFlagItCannotUse(t *testing.T) {
	_, cfgFile, logs := cliEnv(t)
	logs.Reset()
	if got := run(context.Background(), []string{"tui", "-config", cfgFile, "-dest", "~bob/Music"}); got != exitCannotStart {
		t.Errorf("tagshelf tui -dest ~bob/Music = %d, want %d", got, exitCannotStart)
	}
	want := "tagshelf cannot start: the library folder ~bob/Music, given with -dest, starts with another user's home folder.\n" +
		"tagshelf knows only ~ alone, your own home folder. Write the folder out in full.\n"
	if logs.String() != want {
		t.Errorf("printed %q\nwant    %q", logs, want)
	}
	if runtime.GOOS == "windows" {
		return
	}
	logs.Reset()
	if got := run(context.Background(), []string{"tui", "-config", cfgFile, "-dest", `D:\Music`}); got != exitCannotStart {
		t.Errorf(`tagshelf tui -dest D:\Music = %d, want %d`, got, exitCannotStart)
	}
	want = `tagshelf cannot start: the library folder D:\Music, given with -dest, is a Windows path.` + "\n" +
		"Give the folder as this system writes it, as in ~/Music.\n"
	if logs.String() != want {
		t.Errorf("printed %q\nwant    %q", logs, want)
	}
}

// TestSettingsKnowHome checks folders written from ~ on the Settings tab:
// they are whole paths, they are looked for and made in the home folder, and
// the config file keeps them as they were typed.
func TestSettingsKnowHome(t *testing.T) {
	tmp, cfgFile, _ := cliEnv(t)
	home := filepath.Join(tmp, "home")
	if err := os.MkdirAll(filepath.Join(home, "In"), 0o755); err != nil {
		t.Fatal(err)
	}
	homeAt(t, home)
	work := t.TempDir()
	t.Chdir(work)

	s := sessionFor(t, "-config", cfgFile)
	read := s.Load()
	c := read.Config
	c.Source, c.Destination = "~/In", "~/Music"
	check := s.Check(c, read)
	if want := []string{"Make the library folder " + filepath.Join(home, "Music") + "?"}; len(check.Problems) != 0 || !reflect.DeepEqual(check.Confirm, want) {
		t.Fatalf("Check = %+v\nwant no problems, and to ask %q", check, want)
	}

	// Still refused: a path that is not whole, and a source that is not there.
	bad := c
	bad.Source, bad.Untagged = "~/Nope", filepath.Join("rel", "untagged")
	check = s.Check(bad, read)
	if check.Problems["source"] != "does not exist" || check.Problems["untagged"] != here(`must be a whole path, such as D:\Music`) || len(check.Problems) != 2 {
		t.Errorf("Check of %q and %q = %v", bad.Source, bad.Untagged, check.Problems)
	}

	if note, err := s.Save(c, read); err != nil || note != "" {
		t.Fatalf("Save = %q, %v", note, err)
	}
	if info, err := os.Stat(filepath.Join(home, "Music")); err != nil || !info.IsDir() {
		t.Errorf("the library folder was not made in the home folder: %v", err)
	}
	if entries, err := os.ReadDir(work); err != nil || len(entries) != 0 {
		t.Errorf("the folder tagshelf was started in holds %v, %v; want no folder called ~", entries, err)
	}
	text, err := os.ReadFile(cfgFile)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`source: "~/In"`, `destination: "~/Music"`} {
		if !strings.Contains(string(text), want) {
			t.Errorf("the config file lacks %q, as it was typed:\n%s", want, text)
		}
	}

	// The library it opens is the one in the home folder.
	opts, err := s.open()
	if err != nil || opts.Destination != filepath.Join(home, "Music") {
		t.Errorf("open = %+v, %v; want the library at %s", opts, err, filepath.Join(home, "Music"))
	}
	if got := s.Load().Problems; got != nil {
		t.Errorf("the settings marked after the save = %v", got)
	}
}
