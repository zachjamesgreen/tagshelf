package store

import (
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"os"
)

// readerParams configure a reader's connection: it waits out the other
// process's writes, and can never write itself.
const readerParams = "_pragma=busy_timeout(5000)&_pragma=query_only(1)"

// ErrNoDatabase is returned by OpenReader when there is no database file yet.
var ErrNoDatabase = errors.New("no library database yet")

// ErrOutdatedDatabase is returned by OpenReader when the database has not been
// migrated to this tagshelf's schema. Opening it for a run (OpenLibrary)
// migrates it.
var ErrOutdatedDatabase = errors.New("database is older than this tagshelf")

// OpenReader opens an existing database for reading while other processes may
// be running on the library. It takes no library lock, never migrates and
// never writes: a database that is missing, older or newer than this
// tagshelf is refused with ErrNoDatabase, ErrOutdatedDatabase or
// ErrNewerDatabase.
func OpenReader(path string) (*Store, error) {
	if _, err := os.Stat(path); errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("%w at %q", ErrNoDatabase, path)
	} else if err != nil {
		return nil, fmt.Errorf("opening database %q: %w", path, err)
	}
	db, err := sql.Open(driverName, path+"?"+readerParams)
	if err != nil {
		return nil, fmt.Errorf("opening database %q: %w", path, err)
	}
	// One connection that stays open: PRAGMA data_version only means something
	// when asked of the same connection every time.
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	db.SetConnMaxIdleTime(0)
	db.SetConnMaxLifetime(0)
	version, err := userVersion(db)
	if err != nil {
		db.Close()
		return nil, fmt.Errorf("opening database %q: %w", path, err)
	}
	switch latest := len(migrations); {
	case version > latest:
		db.Close()
		return nil, fmt.Errorf("database %q: %w: it is at schema version %d, this tagshelf knows up to %d", path, ErrNewerDatabase, version, latest)
	case version < latest:
		db.Close()
		return nil, fmt.Errorf("database %q: %w: it is at schema version %d, this tagshelf needs %d", path, ErrOutdatedDatabase, version, latest)
	}
	return &Store{db: db, path: path}, nil
}

// OpenLibraryReader opens the database of the library at destination for
// reading, as OpenReader does; database is the database file, or "" for the
// default location for the library's ID. A library without an ID file has no
// database yet: the error is ErrNoDatabase.
//
// A database older than this tagshelf is first brought up to date the way a
// run would, which needs the library lock: while another process holds it the
// error is a *LockedError. A database that belongs to another library is
// refused with ErrOtherLibrary.
func OpenLibraryReader(destination, database string) (*Store, error) {
	id, err := ReadLibraryID(destination)
	if err != nil {
		return nil, err
	}
	if id == "" {
		return nil, fmt.Errorf("%w: %q has no %s file", ErrNoDatabase, destination, LibraryIDFile)
	}
	path := database
	if path == "" {
		if path, err = DefaultPath(id); err != nil {
			return nil, err
		}
	}
	s, err := OpenReader(path)
	if errors.Is(err, ErrOutdatedDatabase) {
		var up *Store
		if up, err = OpenLibrary(destination, database, false); err != nil {
			return nil, fmt.Errorf("upgrading the library database: %w", err)
		}
		if err = up.Close(); err != nil {
			return nil, err
		}
		s, err = OpenReader(path)
	}
	if err != nil {
		return nil, err
	}
	if _, err := s.checkOwner(id, destination); err != nil {
		s.Close()
		return nil, err
	}
	s.libraryID = id
	return s, nil
}

// DataVersion changes whenever another connection commits a change to the
// database, so a reader can poll it to learn when to read again. Only
// comparisons between values from the same Store mean anything.
func (s *Store) DataVersion() (int64, error) {
	var v int64
	err := s.db.QueryRow("PRAGMA data_version").Scan(&v)
	return v, err
}
