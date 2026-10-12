package config

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

// homeIs makes dir the home folder for the rest of the test.
func homeIs(t *testing.T, dir string) {
	t.Helper()
	was := homeDir
	homeDir = func() (string, error) { return dir, nil }
	t.Cleanup(func() { homeDir = was })
}

func TestWindowsPath(t *testing.T) {
	for path, want := range map[string]bool{
		`D:\Music`: true, `d:\music`: true, `D:/Music`: true, `D:`: true, `D:\`: true,
		`\\nas\music`: true, `\\?\D:\Music`: true,
		"": false, "/home/me/Music": false, "~/Music": false, "Music": false, `Music\Rips`: false,
		"D": false, "D:Music": false, "1:\\x": false, "AB:\\x": false, `\Music`: false, "a:b": false,
	} {
		if got := WindowsPath(path); got != want {
			t.Errorf("WindowsPath(%q) = %t, want %t", path, got, want)
		}
		if got, want := ForeignPath(path), want && runtime.GOOS != "windows"; got != want {
			t.Errorf("ForeignPath(%q) = %t, want %t on %s", path, got, want, runtime.GOOS)
		}
	}
}

func TestExpandHome(t *testing.T) {
	home := filepath.Join(t.TempDir(), "me")
	homeIs(t, home)
	sep := string(filepath.Separator)
	for path, want := range map[string]string{
		"":                  "",
		"~":                 home,
		"~/":                home,
		"~/Music":           filepath.Join(home, "Music"),
		"~/Music/Rips/":     filepath.Join(home, "Music", "Rips"),
		"~" + sep + "Music": filepath.Join(home, "Music"),
		"/srv/music":        "/srv/music",
		"Music/~":           "Music/~",
		"a~":                "a~",
		"./~":               "./~",
	} {
		got, err := ExpandHome(path)
		if err != nil || got != want {
			t.Errorf("ExpandHome(%q) = %q, %v; want %q", path, got, err, want)
		}
		if want := path == "~" || strings.HasPrefix(path, "~/") || strings.HasPrefix(path, "~"+sep); StartsAtHome(path) != want {
			t.Errorf("StartsAtHome(%q) = %t, want %t", path, !want, want)
		}
	}

	// Another user's home is not looked up, and the path comes back as it was.
	for _, path := range []string{"~bob", "~bob/Music", "~~", "~.config"} {
		if got, err := ExpandHome(path); !errors.Is(err, ErrOtherHome) || got != path {
			t.Errorf("ExpandHome(%q) = %q, %v; want ErrOtherHome", path, got, err)
		}
		if StartsAtHome(path) {
			t.Errorf("StartsAtHome(%q) = true", path)
		}
	}

	// A backslash is a separator on Windows alone.
	got, err := ExpandHome(`~\Music`)
	if runtime.GOOS == "windows" {
		if err != nil || got != filepath.Join(home, "Music") {
			t.Errorf(`ExpandHome("~\Music") = %q, %v`, got, err)
		}
	} else if !errors.Is(err, ErrOtherHome) {
		t.Errorf(`ExpandHome("~\Music") = %q, %v; want it taken for a name, not a path`, got, err)
	}

	// Without a home folder ~ cannot be told, and a path without it does not ask.
	homeDir = func() (string, error) { return "", errors.New("$HOME is not defined") }
	if got, err := ExpandHome("~/Music"); err == nil || !strings.Contains(err.Error(), "the home folder that ~ stands for cannot be found") || got != "~/Music" {
		t.Errorf("ExpandHome without a home folder = %q, %v", got, err)
	}
	if got, err := ExpandHome("/srv/music"); err != nil || got != "/srv/music" {
		t.Errorf("ExpandHome of a path without ~, with no home folder = %q, %v", got, err)
	}
}

// TestPathProblems checks which paths are refused as written: a Windows one
// anywhere but on Windows, and one from another user's home.
func TestPathProblems(t *testing.T) {
	homeIs(t, t.TempDir())
	if got := PathProblems(Config{Source: "~/In", Destination: "/srv/music", Untagged: "rel"}); got != nil {
		t.Errorf("PathProblems of paths that can be read = %v", got)
	}
	got := PathProblems(Config{Source: "~bob/In", Destination: `D:\Music`, Database: `\\nas\db\music.db`, Other: "~/Other"})
	want := map[string]string{"source": "starts with another user's home folder; only ~ alone, your own, is known"}
	if runtime.GOOS != "windows" {
		want["destination"] = "is a Windows path, which this system cannot use"
		want["database"] = "is a Windows path, which this system cannot use"
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("PathProblems = %v\nwant           %v", got, want)
	}
}

// TestCheckKnowsHome checks that the folders are compared with ~ in full:
// ~/Music and the home folder's Music are one folder.
func TestCheckKnowsHome(t *testing.T) {
	home := t.TempDir()
	homeIs(t, home)
	tests := []struct {
		name string
		cfg  Config
		want map[string]string
	}{
		{"folders from home", Config{Source: "~/In", Destination: "~/Music"}, nil},
		{"the library inside the source", Config{Source: "~", Destination: "~/Music"}, map[string]string{"destination": "inside the source folder"}},
		{"the same folder written two ways", Config{Source: filepath.Join(home, "Music"), Destination: "~/Music"}, nil},
		{"untagged is the library", Config{Destination: "~/Music", Untagged: filepath.Join(home, "Music")}, map[string]string{"untagged": "library folder itself"}},
		{"another user's home", Config{Source: "~bob/In", Destination: "~/Music", Untagged: "~bob"},
			map[string]string{"source": "another user's home folder", "untagged": "another user's home folder"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Check(tt.cfg)
			if len(got) != len(tt.want) {
				t.Errorf("Check = %v, want problems with %v", got, tt.want)
			}
			for key, part := range tt.want {
				if !strings.Contains(got[key], part) {
					t.Errorf("Check[%q] = %q, want it to say %q", key, got[key], part)
				}
			}
		})
	}
	if runtime.GOOS != "windows" {
		got := Check(Config{Source: `D:\In`, Destination: `D:\In\Music`, Workers: -1})
		if !strings.Contains(got["source"], "Windows path") || !strings.Contains(got["destination"], "Windows path") || got["workers"] == "" || len(got) != 3 {
			t.Errorf("Check of Windows paths = %v, want each named as one, and nothing said of where they lie", got)
		}
	}
}

// TestWriteFileKeepsWhatTheFileIs checks a config file that is there: it
// keeps its permissions, and one that is a symbolic link stays a link, with
// the file it leads to written.
func TestWriteFileKeepsWhatTheFileIs(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte("a: 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o640); err != nil {
		t.Fatal(err)
	}
	if err := WriteFile(path, []byte("a: 2\n")); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		// Windows keeps no such permissions.
		if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o640 {
			t.Errorf("the file's permissions after the save: %v, %v; want them as they were, 0640", info.Mode().Perm(), err)
		}
	}

	// A new file's folder is the user's alone.
	fresh := filepath.Join(dir, "tagshelf", "config.yaml")
	if err := WriteFile(fresh, []byte("a: 1\n")); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		if info, err := os.Stat(filepath.Dir(fresh)); err != nil || info.Mode().Perm() != 0o700 {
			t.Errorf("the folder made for the file: %v, %v; want 0700", info.Mode().Perm(), err)
		}
	}

	// A link, as a config file kept in a repository of dotfiles is.
	dotfiles := filepath.Join(dir, "dotfiles", "tagshelf.yaml")
	if err := os.MkdirAll(filepath.Dir(dotfiles), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dotfiles, []byte("a: 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "linked.yaml")
	if err := os.Symlink(dotfiles, link); err != nil {
		t.Skipf("cannot make a symbolic link: %v", err)
	}
	if err := WriteFile(link, []byte("a: 3\n")); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Lstat(link); err != nil || info.Mode()&fs.ModeSymlink == 0 {
		t.Errorf("the link after the save: %v, %v; want it a link still", info, err)
	}
	if data, err := os.ReadFile(dotfiles); err != nil || string(data) != "a: 3\n" {
		t.Errorf("the file the link leads to holds %q, %v; want what was saved", data, err)
	}
	if entries, err := os.ReadDir(filepath.Dir(dotfiles)); err != nil || len(entries) != 1 {
		t.Errorf("the folder of the file holds %v, %v; want the file alone, and no temporary file", entries, err)
	}

	// A link that leads nowhere is not written over.
	dangling := filepath.Join(dir, "dangling.yaml")
	if err := os.Symlink(filepath.Join(dir, "nowhere", "x.yaml"), dangling); err != nil {
		t.Fatal(err)
	}
	if err := WriteFile(dangling, []byte("a: 4\n")); err == nil || !strings.Contains(err.Error(), "is a link that leads nowhere") {
		t.Errorf("WriteFile to a link that leads nowhere = %v", err)
	}
	if info, err := os.Lstat(dangling); err != nil || info.Mode()&fs.ModeSymlink == 0 {
		t.Errorf("the link that leads nowhere after the save: %v, %v; want it left", info, err)
	}
}
