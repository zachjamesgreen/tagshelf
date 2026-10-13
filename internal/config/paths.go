package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
)

// PathKeys are the settings that name a folder or a file. Each call returns
// a new slice.
func PathKeys() []string {
	return []string{"source", "destination", "untagged", "other", "duplicates", "playlists", "database"}
}

// WindowsPath reports whether path is written the Windows way: from a drive
// letter, as D:\Music, D:/Music or D:, or from a share, as \\server\music.
//
// On Windows that is simply a path. Anywhere else it names nothing: it has no
// leading slash, so it would be taken for a folder called "D:\Music" inside
// the working directory, and a run would make that folder and fill it.
func WindowsPath(path string) bool {
	if len(path) >= 2 && path[0] == '\\' && path[1] == '\\' {
		return true
	}
	if len(path) < 2 || path[1] != ':' {
		return false
	}
	letter := path[0] | 0x20 // lower case, for an ASCII letter
	if letter < 'a' || letter > 'z' {
		return false
	}
	return len(path) == 2 || path[2] == '\\' || path[2] == '/'
}

// ForeignPath reports whether path is one this system cannot use because it
// is written for another: a Windows path anywhere but on Windows.
func ForeignPath(path string) bool {
	return runtime.GOOS != "windows" && WindowsPath(path)
}

// ErrOtherHome is a path that starts with ~name, another user's home folder,
// which tagshelf does not look up.
var ErrOtherHome = errors.New("it starts with another user's home folder")

// homeDir is the user's home folder. The tests put their own in its place.
var homeDir = os.UserHomeDir

// ExpandHome gives path with a leading ~ replaced by the user's home folder:
// ~ alone, or ~ and a separator, as in ~/Music. A shell does this for what is
// typed on a command line; nothing does it for a config file or for a field
// of the terminal UI, where ~/Music would otherwise be a folder called "~" in
// the working directory. Any other path is returned as it is.
//
// ~name, another user's home folder, is ErrOtherHome.
func ExpandHome(path string) (string, error) {
	if path == "" || path[0] != '~' {
		return path, nil
	}
	if len(path) > 1 && !os.IsPathSeparator(path[1]) {
		return path, ErrOtherHome
	}
	home, err := homeDir()
	if err != nil {
		return path, fmt.Errorf("the home folder that ~ stands for cannot be found: %w", err)
	}
	if len(path) == 1 {
		return home, nil
	}
	return filepath.Join(home, path[2:]), nil
}

// StartsAtHome reports whether path is written from ~, the home folder.
func StartsAtHome(path string) bool {
	return path == "~" || (len(path) > 1 && path[0] == '~' && os.IsPathSeparator(path[1]))
}
