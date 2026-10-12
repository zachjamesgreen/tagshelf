package main

import (
	"bufio"
	"context"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"runtime"
	"strings"
	"testing"

	"github.com/zachjamesgreen/tagshelf/internal/config"
)

// homeAt makes dir the home folder for the rest of the test.
func homeAt(t *testing.T, dir string) {
	t.Helper()
	t.Setenv("HOME", dir)
	t.Setenv("USERPROFILE", dir)
}

// TestHere checks the texts tagshelf makes up with example folders: as they
// are on Windows, and with ~ and slashes anywhere else.
func TestHere(t *testing.T) {
	in := `tagshelf -source D:\Incoming -dest "D:\Music" puts untagged files in <dest>\_Untagged`
	want := in
	if runtime.GOOS != "windows" {
		want = `tagshelf -source ~/Incoming -dest "~/Music" puts untagged files in <dest>/_Untagged`
	}
	if got := here(in); got != want {
		t.Errorf("here = %q, want %q", got, want)
	}
}

// TestUsablePaths checks the folders a run goes by: ~ in full, relative
// paths from the working directory, and the defaults that follow from them.
func TestUsablePaths(t *testing.T) {
	home, work := t.TempDir(), t.TempDir()
	homeAt(t, home)
	t.Chdir(work)
	// The working directory as the system names it, which on some is not
	// what a temp folder is called.
	work, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}

	cfg, links, problem := usablePaths(config.Config{
		Source: "~/In", Destination: "~", Other: filepath.Join("rel", "other"), Database: filepath.Join("~", "db", "music.db"),
	}, pathOrigin{})
	if problem != nil || len(links) != 0 {
		t.Fatalf("usablePaths = %v, links %v", problem, links)
	}
	want := config.Config{
		Source: filepath.Join(home, "In"), Destination: home, Untagged: filepath.Join(home, "_Untagged"),
		Other: filepath.Join(work, "rel", "other"), Duplicates: filepath.Join(home, "_Duplicates"),
		Database: filepath.Join(home, "db", "music.db"), Compilations: cfg.Compilations, Workers: cfg.Workers,
	}
	if cfg != want {
		t.Errorf("usablePaths =\n%+v\nwant\n%+v", cfg, want)
	}
	if cfg.Compilations == "" || cfg.Workers == 0 {
		t.Errorf("the defaults are not filled in: %+v", cfg)
	}

	// Another user's home is not looked up, and the message says where the
	// path was given.
	_, _, problem = usablePaths(config.Config{Source: "~bob/In", Destination: home}, pathOrigin{flags: map[string]string{"source": "-source"}})
	if problem == nil || problem.key != "source" ||
		problem.what() != "the source folder ~bob/In, given with -source, starts with another user's home folder" ||
		!strings.Contains(problem.todo, "Write the folder out in full") {
		t.Errorf("for ~bob/In: %+v", problem)
	}
	_, _, problem = usablePaths(config.Config{Destination: "~bob"}, pathOrigin{file: "/etc/t.yaml"})
	if problem == nil || problem.what() != "the library folder ~bob, set as destination in the config file /etc/t.yaml, starts with another user's home folder" {
		t.Errorf("for ~bob from a config file: %+v", problem)
	}
}

// TestUsablePathsGoThroughALink checks a folder that is itself a symbolic
// link, as ~/Music is on many a Linux system: the run goes by the folder it
// leads to, since a walk does not go through a link, and so do the folders
// that default to being inside it. The database is a file, and stays.
func TestUsablePathsGoThroughALink(t *testing.T) {
	tmp := t.TempDir()
	// The temp folder as the system names it, which a link is resolved to.
	tmp, err := filepath.EvalSymlinks(tmp)
	if err != nil {
		t.Fatal(err)
	}
	real, link := filepath.Join(tmp, "disk", "Music"), filepath.Join(tmp, "Music")
	if err := os.MkdirAll(real, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("cannot make a symbolic link: %v", err)
	}
	gone := filepath.Join(tmp, "gone")
	if err := os.Symlink(filepath.Join(tmp, "nowhere"), gone); err != nil {
		t.Fatal(err)
	}

	cfg, links, problem := usablePaths(config.Config{Source: gone, Destination: link, Database: filepath.Join(link, "db")}, pathOrigin{})
	if problem != nil {
		t.Fatal(problem)
	}
	if cfg.Destination != real || cfg.Untagged != filepath.Join(real, "_Untagged") {
		t.Errorf("the library is %q with untagged files in %q, want the folder the link leads to", cfg.Destination, cfg.Untagged)
	}
	if cfg.Source != gone {
		t.Errorf("a link that leads nowhere became %q, want it left for the run to report", cfg.Source)
	}
	if cfg.Database != filepath.Join(link, "db") {
		t.Errorf("the database is %q, want it as it was given", cfg.Database)
	}
	if want := []string{"The library folder " + link + " is a link to " + real + "."}; len(links) != 1 || links[0] != want[0] {
		t.Errorf("links = %q, want %q", links, want)
	}
	if got := usablePath(link); got != real {
		t.Errorf("usablePath(%q) = %q, want %q", link, got, real)
	}
}

// TestRunFromHome runs with folders written from ~, which no shell put in
// full: as they are in a config file, and as a flag in quotes. The library
// is made in the home folder, and no folder called "~" where the run started.
func TestRunFromHome(t *testing.T) {
	tmp, cfgFile, logs := cliEnv(t)
	home := filepath.Join(tmp, "home")
	song := filepath.Join(home, "In", "loose.mp3")
	writeFile(t, song, "no tags")
	homeAt(t, home)
	work := t.TempDir()
	t.Chdir(work)
	writeFile(t, cfgFile, "source: \"~/In\"\n")

	if got := run(context.Background(), []string{"-config", cfgFile, "-dest", "~/Music"}); got != exitOK {
		t.Fatalf("run = %d, want %d\n%s", got, exitOK, logs)
	}
	if _, err := os.Stat(filepath.Join(home, "Music", "_Untagged", "loose.mp3")); err != nil {
		t.Errorf("the song is not in the library under the home folder: %v\n%s", err, logs)
	}
	if entries, err := os.ReadDir(work); err != nil || len(entries) != 0 {
		t.Errorf("the folder the run started in holds %v, %v; want nothing made there", entries, err)
	}
	if want := "Destination:  " + filepath.Join(home, "Music"); !strings.Contains(logs.String(), want) {
		t.Errorf("the log lacks %q:\n%s", want, logs)
	}
}

// TestRunRefusesWindowsPaths runs, off Windows, with folders written the
// Windows way. Such a path has no leading slash, so it used to be taken for a
// folder called "D:\Music" where the run started, which the run then made
// and filled. A config.yaml found in that folder is enough to bring one in.
func TestRunRefusesWindowsPaths(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("on Windows these are paths")
	}
	tmp, cfgFile, logs := cliEnv(t)
	song := filepath.Join(tmp, "src", "loose.mp3")
	writeFile(t, song, "no tags")
	work := t.TempDir()
	t.Chdir(work)
	found := filepath.Join(work, "config.yaml")
	const todo = "\nGive the folder as this system writes it, as in ~/Music.\n"

	tests := []struct {
		name   string
		yaml   string // the config.yaml of the folder, "" for none
		args   []string
		refuse string
	}{
		{"a library folder in the config file found in the folder", "source: '" + filepath.Dir(song) + "'\ndestination: 'D:\\Music'\ndry_run: false\n", []string{"-verbose"},
			`tagshelf cannot start: the library folder D:\Music, set as destination in the config file ` + found + `, is a Windows path.` + todo},
		{"an untagged folder in it, with the library given as a flag", "untagged: 'I:\\Untagged'\n", []string{"-source", filepath.Dir(song), "-dest", filepath.Join(tmp, "lib")},
			`tagshelf cannot start: the untagged folder I:\Untagged, set as untagged in the config file ` + found + `, is a Windows path.` + todo},
		{"a flag", "", []string{"-config", cfgFile, "-source", filepath.Dir(song), "-dest", `D:/Music`},
			`tagshelf cannot start: the library folder D:/Music, given with -dest, is a Windows path.` + todo},
		{"a share", "", []string{"-config", cfgFile, "-reorganize", `\\nas\music`},
			`tagshelf cannot start: the source folder \\nas\music, given with -reorganize, is a Windows path.` + todo},
		{"a database", "", []string{"-config", cfgFile, "-index", "-dest", filepath.Join(tmp, "lib"), "-db", `C:\tagshelf\music.db`},
			`tagshelf cannot start: the database file C:\tagshelf\music.db, given with -db, is a Windows path.` + todo},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			os.Remove(found)
			if tt.yaml != "" {
				writeFile(t, found, tt.yaml)
			}
			logs.Reset()
			if got := run(context.Background(), tt.args); got != exitCannotStart {
				t.Errorf("run(%q) = %d, want %d", tt.args, got, exitCannotStart)
			}
			if !strings.HasSuffix(logs.String(), tt.refuse) {
				t.Errorf("printed\n%s\nwant it to end with\n%s", logs, tt.refuse)
			}
			if _, err := os.Stat(song); err != nil {
				t.Errorf("the song moved: %v", err)
			}
			entries, err := os.ReadDir(work)
			if err != nil {
				t.Fatal(err)
			}
			for _, e := range entries {
				if e.Name() != "config.yaml" {
					t.Errorf("the refused run made %q where it was started", e.Name())
				}
			}
			if _, err := os.Stat(filepath.Join(tmp, "lib")); !os.IsNotExist(err) {
				t.Errorf("the refused run made the library folder: %v", err)
			}
		})
	}

	// A Windows path that a flag replaces is not gone by.
	os.Remove(found)
	writeFile(t, found, "destination: 'D:\\Music'\n")
	logs.Reset()
	lib := filepath.Join(tmp, "lib")
	if got := run(context.Background(), []string{"-source", filepath.Dir(song), "-dest", lib}); got != exitOK {
		t.Errorf("with -dest in place of the config file's Windows path: %d, want %d\n%s", got, exitOK, logs)
	}
	if _, err := os.Stat(filepath.Join(lib, "_Untagged", "loose.mp3")); err != nil {
		t.Errorf("the song is not in the library: %v", err)
	}
}

// TestRunGoesThroughALinkedSource organizes a source folder that is a
// symbolic link. The walk does not go through a link, so the run used to
// find nothing in it and end well, having done nothing.
func TestRunGoesThroughALinkedSource(t *testing.T) {
	tmp, cfgFile, logs := cliEnv(t)
	tmp, err := filepath.EvalSymlinks(tmp)
	if err != nil {
		t.Fatal(err)
	}
	real, link, lib := filepath.Join(tmp, "disk", "In"), filepath.Join(tmp, "In"), filepath.Join(tmp, "lib")
	writeFile(t, filepath.Join(real, "loose.mp3"), "no tags")
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("cannot make a symbolic link: %v", err)
	}
	if got := run(context.Background(), []string{"-config", cfgFile, "-source", link, "-dest", lib}); got != exitOK {
		t.Fatalf("run = %d, want %d\n%s", got, exitOK, logs)
	}
	if _, err := os.Stat(filepath.Join(lib, "_Untagged", "loose.mp3")); err != nil {
		t.Errorf("the song behind the link was not organized: %v\n%s", err, logs)
	}
	if want := "The source folder " + link + " is a link to " + real + "."; !strings.Contains(logs.String(), want) {
		t.Errorf("the log lacks %q:\n%s", want, logs)
	}
	if info, err := os.Lstat(link); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Errorf("the link after the run: %v, %v; want it where it was", info, err)
	}
}

// TestConfigExamplesAreInStep checks the two example files, which are the
// templates of a new config file: the one for Windows and the one for
// everything else say the same, line for line, apart from how a path is
// written.
func TestConfigExamplesAreInStep(t *testing.T) {
	read := func(name string) ([]string, config.Config) {
		t.Helper()
		data, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		cfg, err := config.Parse(data)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		var lines []string
		for s := bufio.NewScanner(strings.NewReader(string(data))); s.Scan(); {
			lines = append(lines, s.Text())
		}
		return lines, cfg
	}
	// go test runs in the package's folder, but the tests of this package
	// move to an empty one: the files are found from the source file.
	_, file, _, _ := runtime.Caller(0)
	dir := filepath.Dir(file)
	win, winCfg := read(filepath.Join(dir, "config.example.yaml"))
	lin, linCfg := read(filepath.Join(dir, "config.example.linux.yaml"))
	if len(win) != len(lin) {
		t.Fatalf("the Windows example has %d lines, the other %d", len(win), len(lin))
	}

	key := regexp.MustCompile(`^#?([a-z_]+):`)
	comment := regexp.MustCompile(`# .*$`)
	paths := map[string]bool{}
	for _, k := range config.PathKeys() {
		paths[k] = true
	}
	for i := range win {
		w, l := key.FindStringSubmatch(win[i]), key.FindStringSubmatch(lin[i])
		switch {
		case w == nil || l == nil:
			// A line of the header. One names the user's own config file,
			// which is somewhere else on each system.
			if win[i] != lin[i] && !(strings.Contains(win[i], `%APPDATA%\tagshelf\config.yaml`) && strings.Contains(lin[i], "~/.config/tagshelf/config.yaml")) {
				t.Errorf("line %d differs:\n%s\n%s", i+1, win[i], lin[i])
			}
		case w[0] != l[0]:
			t.Errorf("line %d sets %q in one and %q in the other", i+1, w[0], l[0])
		case !paths[w[1]] && win[i] != lin[i]:
			t.Errorf("line %d, which is no path, differs:\n%s\n%s", i+1, win[i], lin[i])
		case paths[w[1]] && w[1] != "database" && comment.FindString(win[i]) != comment.FindString(lin[i]):
			t.Errorf("line %d explains %s differently:\n%s\n%s", i+1, w[1], win[i], lin[i])
		}
	}

	// What they set is the same, the made-up folders aside.
	winCfg.Source, winCfg.Destination, linCfg.Source, linCfg.Destination = "", "", "", ""
	if !reflect.DeepEqual(winCfg, linCfg) {
		t.Errorf("the examples set different things:\n%+v\n%+v", winCfg, linCfg)
	}
	if strings.Contains(strings.Join(lin, "\n"), `\`) || strings.Contains(strings.Join(lin, "\n"), "%") {
		t.Error("the example for Linux holds a backslash or a Windows variable")
	}
}
