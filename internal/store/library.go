package store

import (
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
)

// OwnFilePrefix starts the name of every file tagshelf keeps among the music:
// the library ID file and the temp copies a tag save writes. Walks skip them,
// so a copy left behind by a crashed save is never taken for a song.
const OwnFilePrefix = ".tagshelf-"

// LibraryIDFile holds a library's ID in its destination root. Walks and
// cleanup leave it alone.
const LibraryIDFile = OwnFilePrefix + "library"

var libraryIDPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// ErrOtherLibrary is returned when a database already belongs to a library
// other than the one it is being opened for.
var ErrOtherLibrary = errors.New("the database belongs to another library")

// OpenLibrary opens the database of the library at destination for a run.
// database is the database file to use, or "" for the default location for the
// library's ID.
//
// It first takes the library lock, without waiting: the lock file in the
// destination, then the database's. If another process holds either, the
// error is a *LockedError (errors.Is ErrLocked) and nothing else is touched.
// The Store holds the lock until Close. Holding it, OpenLibrary marks runs
// left running as crashed: their process died without finishing them.
//
// A database belongs to the first library it is opened for, whose ID it then
// holds: opening it for another library is refused with ErrOtherLibrary, as
// its index would otherwise be rewritten for that library's files.
//
// A library without an ID file gets one, but only once its database has
// opened, so a database that cannot be opened leaves the library without an
// ID (though with its folder and lock file made).
// A dry run on such a library writes nothing at all: it gets a database in
// memory, even when database names a file, and takes no lock, as there is no
// library database yet to guard.
func OpenLibrary(destination, database string, dryRun bool) (*Store, error) {
	id, err := ReadLibraryID(destination)
	if err != nil {
		return nil, err
	}
	if id == "" && dryRun {
		return OpenMemory()
	}
	libraryLock, err := AcquireLibraryLock(destination)
	if err != nil {
		return nil, err
	}
	s, err := openLocked(destination, database, dryRun)
	if err != nil {
		libraryLock.Release()
		return nil, err
	}
	if s.path == "" {
		// In memory after all: nothing to guard.
		libraryLock.Release()
		return s, nil
	}
	s.libraryLock = libraryLock
	return s, nil
}

// openLocked is OpenLibrary once the lock in the destination is held.
func openLocked(destination, database string, dryRun bool) (*Store, error) {
	// Read again now that no other run can be giving the library its ID: two
	// first runs must not each make one.
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
	lock, err := AcquireLock(path)
	if err != nil {
		return nil, err
	}
	s, err := Open(path)
	if err != nil {
		lock.Release()
		return nil, err
	}
	s.lock = lock
	if err := s.claim(id, destination); err != nil {
		s.Close()
		return nil, err
	}
	if fresh {
		if err := writeLibraryID(destination, id); err != nil {
			s.Close()
			return nil, err
		}
	}
	if err := s.markCrashed(); err != nil {
		s.Close()
		return nil, err
	}
	s.libraryID = id
	return s, nil
}

// owner returns the ID of the library the database belongs to, or "" when it
// has not been opened for a library yet.
func (s *Store) owner() (string, error) {
	var id string
	err := s.db.QueryRow(`SELECT id FROM library`).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("reading the database's library ID: %w", err)
	}
	return id, nil
}

// checkOwner refuses a database that belongs to a library other than id, the
// one at destination.
func (s *Store) checkOwner(id, destination string) (owner string, err error) {
	if owner, err = s.owner(); err != nil {
		return "", err
	}
	if owner != "" && owner != id {
		return owner, fmt.Errorf("%w: %q holds the index of library %s, and the library at %q is %s; name another database with database: or -db",
			ErrOtherLibrary, s.path, owner, destination, id)
	}
	return owner, nil
}

// claim makes the database the library id's, if it is no library's yet.
func (s *Store) claim(id, destination string) error {
	owner, err := s.checkOwner(id, destination)
	if err != nil || owner != "" {
		return err
	}
	if _, err := s.db.Exec(`INSERT INTO library (id) VALUES (?)`, id); err != nil {
		return fmt.Errorf("recording the database's library ID: %w", err)
	}
	return nil
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
	dir, err := DataFolder()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "libraries", libraryID, "tagshelf.db"), nil
}

// DataFolder is the folder tagshelf keeps the libraries' databases under.
func DataFolder() (string, error) {
	dir, err := dataDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "tagshelf"), nil
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
