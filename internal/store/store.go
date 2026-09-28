// Package store keeps a library's database: its run history and, later, its
// index. The database is SQLite, one file per library, migrated forward on
// open.
package store

import (
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	_ "modernc.org/sqlite" // registers the "sqlite" driver
)

const driverName = "sqlite"

// fileParams configure every connection to a database file. WAL lets the TUI
// read while a CLI run writes; busy_timeout waits out the other process's
// writes instead of failing; _txlock=immediate takes the write lock when a
// transaction begins, so two writers queue rather than deadlock.
const fileParams = "_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)&_txlock=immediate"

// ErrNewerDatabase is returned when a database was written by a newer tagshelf
// than this one.
var ErrNewerDatabase = errors.New("database is newer than this tagshelf")

// Store is an open library database. It is safe for concurrent use.
type Store struct {
	db        *sql.DB
	path      string // "" in memory
	libraryID string // "" in memory
}

// Open opens the database file at path, creating it and its folder if needed,
// and migrates it to the current schema.
func Open(path string) (*Store, error) {
	return open(path, migrations)
}

// OpenMemory opens an empty database that lives only as long as the Store.
func OpenMemory() (*Store, error) {
	db, err := sql.Open(driverName, ":memory:?_pragma=foreign_keys(1)")
	if err != nil {
		return nil, err
	}
	// Every connection to :memory: is a database of its own, so there must be
	// exactly one, kept open for the life of the Store.
	db.SetMaxOpenConns(1)
	db.SetConnMaxIdleTime(0)
	db.SetConnMaxLifetime(0)
	if err := migrate(db, "", migrations); err != nil {
		db.Close()
		return nil, err
	}
	return &Store{db: db}, nil
}

func open(path string, ms []migration) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("creating the database folder: %w", err)
	}
	_, statErr := os.Stat(path)
	existed := statErr == nil

	db, err := sql.Open(driverName, path+"?"+fileParams)
	if err != nil {
		return nil, fmt.Errorf("opening database %q: %w", path, err)
	}
	// One connection serializes this process's writes, which SQLite would do
	// anyway, without them waiting on each other's busy timeouts.
	db.SetMaxOpenConns(1)
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("opening database %q: %w", path, err)
	}
	backupPath := ""
	if existed {
		backupPath = path
	}
	if err := migrate(db, backupPath, ms); err != nil {
		db.Close()
		return nil, fmt.Errorf("database %q: %w", path, err)
	}
	return &Store{db: db, path: path}, nil
}

// Close closes the database.
func (s *Store) Close() error {
	return s.db.Close()
}

// Path is the database file, or "" for a database in memory.
func (s *Store) Path() string {
	return s.path
}

// LibraryID is the ID of the library the database belongs to, or "" for a
// database in memory.
func (s *Store) LibraryID() string {
	return s.libraryID
}

//go:embed migrations/*.sql
var migrationFiles embed.FS

// migrations is the schema's history. Version N of the database has had the
// first N applied; PRAGMA user_version records N.
var migrations = mustLoadMigrations()

func mustLoadMigrations() []migration {
	sub, err := fs.Sub(migrationFiles, "migrations")
	if err != nil {
		panic(err)
	}
	ms, err := loadMigrations(sub)
	if err != nil {
		panic(err)
	}
	return ms
}

// migration is one numbered SQL file, NNNN_name.sql.
type migration struct {
	version int
	name    string
	sql     string
}

// loadMigrations reads the .sql files at the top of fsys, which must be
// numbered 1, 2, 3, ... without gaps.
func loadMigrations(fsys fs.FS) ([]migration, error) {
	names, err := fs.Glob(fsys, "*.sql")
	if err != nil {
		return nil, err
	}
	var ms []migration
	for _, name := range names {
		num, _, ok := strings.Cut(name, "_")
		version, err := strconv.Atoi(num)
		if !ok || err != nil {
			return nil, fmt.Errorf("migration %q is not named NNNN_name.sql", name)
		}
		data, err := fs.ReadFile(fsys, name)
		if err != nil {
			return nil, err
		}
		ms = append(ms, migration{version: version, name: name, sql: string(data)})
	}
	sort.Slice(ms, func(i, j int) bool { return ms[i].version < ms[j].version })
	for i, m := range ms {
		if m.version != i+1 {
			return nil, fmt.Errorf("migration %q is out of sequence: want version %d", m.name, i+1)
		}
	}
	return ms, nil
}

// migrate brings db up to the last of ms. Before changing a database that
// already existed it copies it to <backupPath>.bak-v<N>; backupPath is "" when
// there is nothing worth keeping. The migrations run in one transaction, so a
// failure leaves the database as it was.
func migrate(db *sql.DB, backupPath string, ms []migration) error {
	latest := len(ms)
	version, err := userVersion(db)
	if err != nil {
		return err
	}
	if version > latest {
		return fmt.Errorf("%w: it is at schema version %d, this tagshelf knows up to %d", ErrNewerDatabase, version, latest)
	}
	if version == latest {
		return nil
	}
	if backupPath != "" {
		if err := backup(db, fmt.Sprintf("%s.bak-v%d", backupPath, version)); err != nil {
			return fmt.Errorf("backing up before migrating: %w", err)
		}
	}

	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// Another process may have migrated since the version was read; the
	// transaction now holds the write lock, so this read is the one that counts.
	if err := tx.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		return err
	}
	if version > latest {
		return fmt.Errorf("%w: it is at schema version %d, this tagshelf knows up to %d", ErrNewerDatabase, version, latest)
	}
	for _, m := range ms[version:] {
		if _, err := tx.Exec(m.sql); err != nil {
			return fmt.Errorf("migration %s: %w", m.name, err)
		}
	}
	if _, err := tx.Exec(fmt.Sprintf("PRAGMA user_version = %d", latest)); err != nil {
		return err
	}
	return tx.Commit()
}

func userVersion(db *sql.DB) (int, error) {
	var v int
	err := db.QueryRow("PRAGMA user_version").Scan(&v)
	return v, err
}

// backup writes a consistent copy of db to dst, replacing any earlier copy
// there. VACUUM INTO includes changes still in the WAL, which copying the file
// would miss.
func backup(db *sql.DB, dst string) error {
	tmp := dst + ".tmp"
	if err := os.Remove(tmp); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if _, err := db.Exec("VACUUM INTO ?", tmp); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, dst); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}
