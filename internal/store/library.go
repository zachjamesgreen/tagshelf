package store

import (
	"crypto/rand"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
)

// LibraryIDFile holds a library's ID in its destination root. Walks and
// cleanup leave it alone.
const LibraryIDFile = ".tagshelf-library"

var libraryIDPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// OpenLibrary opens the database of the library at destination. database is
// the database file to use, or "" for the default location for the library's
// ID.
//
// A library without an ID file gets one, but only once its database has
// opened, so a database that cannot be opened leaves the library untouched.
// A dry run on such a library writes nothing at all: it gets a database in
// memory, even when database names a file.
func OpenLibrary(destination, database string, dryRun bool) (*Store, error) {
	id, err := ReadLibraryID(destination)
	if err != nil {
		return nil, err
	}
	if id == "" && dryRun {
		return OpenMemory()
	}
	fresh := id == ""
	if fresh {
		if id, err = newLibraryID(); err != nil {
			return nil, err
		}
	}
	path := database
	if path == "" {
		if path, err = DefaultPath(id); err != nil {
			return nil, err
		}
	}
	s, err := Open(path)
	if err != nil {
		return nil, err
	}
	if fresh {
		if err := writeLibraryID(destination, id); err != nil {
			s.Close()
			return nil, err
		}
	}
	s.libraryID = id
	return s, nil
}

// ReadLibraryID returns the ID in the library's ID file, or "" when the
// library has none yet.
func ReadLibraryID(destination string) (string, error) {
	path := filepath.Join(destination, LibraryIDFile)
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("reading library ID: %w", err)
	}
	id := strings.ToLower(strings.TrimSpace(string(data)))
	if !libraryIDPattern.MatchString(id) {
		return "", fmt.Errorf("library ID file %q does not hold a UUID", path)
	}
	return id, nil
}

// writeLibraryID creates the library's ID file, creating the destination
// folder if needed. It never replaces an existing ID file.
func writeLibraryID(destination, id string) error {
	if err := os.MkdirAll(destination, 0o755); err != nil {
		return fmt.Errorf("creating the destination: %w", err)
	}
	path := filepath.Join(destination, LibraryIDFile)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("writing library ID: %w", err)
	}
	if _, err := f.WriteString(id + "\n"); err != nil {
		f.Close()
		os.Remove(path)
		return fmt.Errorf("writing library ID: %w", err)
	}
	if err := f.Close(); err != nil {
		os.Remove(path)
		return fmt.Errorf("writing library ID: %w", err)
	}
	return nil
}

// newLibraryID returns a random (version 4) UUID.
func newLibraryID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16]), nil
}

// DefaultPath is where the database of the library with the given ID lives
// unless configured otherwise: always on a local disk, never among the music.
func DefaultPath(libraryID string) (string, error) {
	dir, err := dataDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "tagshelf", "libraries", libraryID, "tagshelf.db"), nil
}

// dataDir is the per-user folder for application data that is not roaming:
// %LOCALAPPDATA% on Windows, $XDG_DATA_HOME or ~/.local/share elsewhere.
func dataDir() (string, error) {
	if runtime.GOOS == "windows" {
		if dir := os.Getenv("LOCALAPPDATA"); dir != "" {
			return dir, nil
		}
		return "", errors.New("%LOCALAPPDATA% is not set: set database: in the config or pass -db")
	}
	if dir := os.Getenv("XDG_DATA_HOME"); filepath.IsAbs(dir) {
		return dir, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".local", "share"), nil
}
