package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// The tests never read the config file of whoever runs them: the folder of
// the user's settings is an empty temp folder unless a test fills it.
func TestMain(m *testing.M) {
	tmp, err := os.MkdirTemp("", "tagshelf-config-test-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	userDir = func() (string, error) { return tmp, nil }
	code := m.Run()
	os.RemoveAll(tmp)
	os.Exit(code)
}

// userConfig puts yaml in the user's own config file for the rest of the
// test, and returns its path.
func userConfig(t *testing.T, yaml string) string {
	t.Helper()
	dir := t.TempDir()
	real := userDir
	userDir = func() (string, error) { return dir, nil }
	t.Cleanup(func() { userDir = real })
	path := filepath.Join(dir, "tagshelf", DefaultConfigName)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestUserPath(t *testing.T) {
	real := userDir
	defer func() { userDir = real }()
	settings := filepath.Join(t.TempDir(), "settings")
	userDir = func() (string, error) { return settings, nil }
	if got, err := UserPath(); err != nil || got != filepath.Join(settings, "tagshelf", "config.yaml") {
		t.Errorf("UserPath = %q, %v", got, err)
	}
	userDir = func() (string, error) { return "", errors.New("%AppData% is not defined") }
	if got, err := UserPath(); err == nil || got != "" {
		t.Errorf("UserPath without a settings folder = %q, %v; want an error", got, err)
	}
	// Without a settings folder there is simply no user file.
	t.Chdir(t.TempDir())
	if f, err := Read(""); err != nil || f.From != FromNowhere || f.Path != "" {
		t.Errorf("Read without a settings folder = %+v, %v; want no file", f, err)
	}
}

// TestReadOrder checks which file is read: the one named, else the working
// directory's config.yaml, else the user's own.
func TestReadOrder(t *testing.T) {
	user := userConfig(t, "workers: 3\n")
	work := t.TempDir()
	t.Chdir(work)

	f, err := Read("")
	if err != nil || f.From != FromUser || f.Path != user || !reflect.DeepEqual(f.Config, Config{Workers: 3}) {
		t.Errorf("Read with only the user's file = %+v, %v", f, err)
	}

	local := filepath.Join(work, DefaultConfigName)
	if err := os.WriteFile(local, []byte("workers: 2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	f, err = Read("")
	if err != nil || f.From != FromWorkingDir || f.Path != local || !reflect.DeepEqual(f.Config, Config{Workers: 2}) {
		t.Errorf("Read with a config.yaml in the working directory = %+v, %v; want that file, by its full path", f, err)
	}

	named := writeConfig(t, "named.yaml", "workers: 1\n")
	f, err = Read(named)
	if err != nil || f.From != FromFlag || f.Path != named || !reflect.DeepEqual(f.Config, Config{Workers: 1}) {
		t.Errorf("Read of a named file = %+v, %v", f, err)
	}

	os.Remove(local)
	os.Remove(user)
	if f, err = Read(""); err != nil || !reflect.DeepEqual(f, File{}) {
		t.Errorf("Read with no file anywhere = %+v, %v; want nothing", f, err)
	}
}

// TestReadFallsThroughAForeignFile checks that a config.yaml of some other
// program in the working directory does not hide the user's own file, and
// that one with a key tagshelf does not know still stops everything.
func TestReadFallsThroughAForeignFile(t *testing.T) {
	user := userConfig(t, "workers: 3\n")
	work := t.TempDir()
	t.Chdir(work)
	local := filepath.Join(work, DefaultConfigName)

	if err := os.WriteFile(local, []byte("workers: [1, 2]\ndry_run: true\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	f, err := Read("")
	if err != nil || f.From != FromUser || f.Path != user || !reflect.DeepEqual(f.Config, Config{Workers: 3}) {
		t.Errorf("Read = %+v, %v; want the user's file alone", f, err)
	}
	if !strings.Contains(f.Skipped, "is not valid YAML") || !strings.Contains(f.Skipped, local) {
		t.Errorf("Skipped = %q, want why %s was passed over", f.Skipped, local)
	}
	logs := captureLog(t)
	if cfg, err := Load("", Overrides{}); err != nil || !reflect.DeepEqual(cfg, Config{Workers: 3}) {
		t.Errorf("Load = %+v, %v", cfg, err)
	}
	for _, want := range []string{"Warning: ignoring the config.yaml in the working directory: ", "Using your config file: " + user} {
		if !strings.Contains(logs.String(), want) {
			t.Errorf("log %q lacks %q", logs, want)
		}
	}

	if err := os.WriteFile(local, []byte("dry-run: true\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	f, err = Read("")
	if err == nil || f.From != FromWorkingDir || !strings.Contains(err.Error(), `has an unknown key "dry-run" on line 1`) {
		t.Errorf("Read beside a file with an unknown key = %+v, %v; want it refused", f, err)
	}
}

// TestReadUserFileIsMandatory checks that the user's own file, once there,
// is not passed over when it cannot be parsed.
func TestReadUserFileIsMandatory(t *testing.T) {
	user := userConfig(t, "workers: [1, 2]\n")
	t.Chdir(t.TempDir())
	f, err := Read("")
	if err == nil || f.Path != user || f.From != FromUser || !strings.Contains(err.Error(), "the config file "+user+" is not valid YAML") {
		t.Errorf("Read = %+v, %v; want the user's file refused by name", f, err)
	}
	if _, err := Load("", Overrides{Destination: "d"}); err == nil {
		t.Error("Load went on with a user file it could not parse")
	}
}

// TestReadMissingNamedFile checks that a named file that is not there can be
// told from one that cannot be read.
func TestReadMissingNamedFile(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "none.yaml")
	f, err := Read(missing)
	if !errors.Is(err, fs.ErrNotExist) || f.Path != missing || f.From != FromFlag {
		t.Errorf("Read = %+v, %v; want fs.ErrNotExist and the file's name", f, err)
	}
	broken := writeConfig(t, "broken.yaml", "workers: [1]\n")
	if _, err := Read(broken); err == nil || errors.Is(err, fs.ErrNotExist) {
		t.Errorf("Read of a broken file = %v; want an error that is not fs.ErrNotExist", err)
	}
}
