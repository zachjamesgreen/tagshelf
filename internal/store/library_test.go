package store

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

var uuidPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

// useDataDir points the default database location into a temp folder.
func useDataDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("LOCALAPPDATA", dir)
	t.Setenv("XDG_DATA_HOME", dir)
	return dir
}

func TestOpenLibraryFirstRealRunWritesID(t *testing.T) {
	data := useDataDir(t)
	lib := filepath.Join(t.TempDir(), "lib") // not there yet

	s, err := OpenLibrary(lib, "", false)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	id, err := ReadLibraryID(lib)
	if err != nil {
		t.Fatal(err)
	}
	if !uuidPattern.MatchString(id) {
		t.Fatalf("library ID %q is not a version 4 UUID", id)
	}
	if s.LibraryID() != id {
		t.Errorf("LibraryID() = %q, want %q", s.LibraryID(), id)
	}
	want := filepath.Join(data, "tagshelf", "libraries", id, "tagshelf.db")
	if s.Path() != want {
		t.Errorf("Path() = %q, want %q", s.Path(), want)
	}
	if _, err := os.Stat(want); err != nil {
		t.Errorf("database file not created: %v", err)
	}
}

func TestOpenLibraryReusesID(t *testing.T) {
	useDataDir(t)
	lib := t.TempDir()

	first, err := OpenLibrary(lib, "", false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := first.StartRun(KindOrganize, false, "s", lib, time.Now()); err != nil {
		t.Fatal(err)
	}
	first.Close()

	// A dry run on an identified library uses the real database.
	second, err := OpenLibrary(lib, "", true)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	if second.LibraryID() != first.LibraryID() || second.Path() != first.Path() {
		t.Errorf("reopened library %q at %q, want %q at %q", second.LibraryID(), second.Path(), first.LibraryID(), first.Path())
	}
	runs, err := second.Runs()
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 1 {
		t.Errorf("reopened database has %d runs, want 1", len(runs))
	}
}

func TestOpenLibraryExplicitDatabase(t *testing.T) {
	data := useDataDir(t)
	lib := t.TempDir()
	db := filepath.Join(t.TempDir(), "mine.db")

	s, err := OpenLibrary(lib, db, false)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if s.Path() != db {
		t.Errorf("Path() = %q, want %q", s.Path(), db)
	}
	if _, err := ReadLibraryID(lib); err != nil {
		t.Error(err)
	}
	if entries, _ := os.ReadDir(data); len(entries) != 0 {
		t.Errorf("default location used despite an explicit database: %v", entries)
	}
}

func TestOpenLibraryDryRunUnidentifiedIsInMemory(t *testing.T) {
	data := useDataDir(t)
	lib := t.TempDir()
	explicit := filepath.Join(t.TempDir(), "mine.db")

	for _, database := range []string{"", explicit} {
		s, err := OpenLibrary(lib, database, true)
		if err != nil {
			t.Fatal(err)
		}
		if s.Path() != "" || s.LibraryID() != "" {
			t.Errorf("dry run on an unidentified library opened %q (%q), want in memory", s.Path(), s.LibraryID())
		}
		if _, err := s.StartRun(KindOrganize, true, "s", lib, time.Now()); err != nil {
			t.Errorf("recording in memory: %v", err)
		}
		s.Close()
	}

	if _, err := os.Stat(filepath.Join(lib, LibraryIDFile)); !os.IsNotExist(err) {
		t.Errorf("dry run wrote the ID file: %v", err)
	}
	if _, err := os.Stat(explicit); !os.IsNotExist(err) {
		t.Errorf("dry run created the database file: %v", err)
	}
	if entries, _ := os.ReadDir(data); len(entries) != 0 {
		t.Errorf("dry run wrote under the data folder: %v", entries)
	}
}

func TestOpenLibraryDatabaseFailureWritesNoID(t *testing.T) {
	useDataDir(t)
	lib := t.TempDir()
	blocker := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(blocker, nil, 0o644); err != nil {
		t.Fatal(err)
	}

	if s, err := OpenLibrary(lib, filepath.Join(blocker, "tagshelf.db"), false); err == nil {
		s.Close()
		t.Fatal("OpenLibrary with an unusable database path succeeded")
	}
	if _, err := os.Stat(filepath.Join(lib, LibraryIDFile)); !os.IsNotExist(err) {
		t.Errorf("ID file written although the database could not be opened: %v", err)
	}
}

func TestReadLibraryID(t *testing.T) {
	lib := t.TempDir()
	if id, err := ReadLibraryID(lib); err != nil || id != "" {
		t.Errorf("ReadLibraryID of an unidentified library = %q, %v; want \"\", nil", id, err)
	}
	if id, err := ReadLibraryID(filepath.Join(lib, "missing")); err != nil || id != "" {
		t.Errorf("ReadLibraryID of a missing library = %q, %v; want \"\", nil", id, err)
	}

	id := "0b0f3c5e-1d2a-4c3b-9a8d-7e6f5a4b3c2d"
	if err := os.WriteFile(filepath.Join(lib, LibraryIDFile), []byte(strings.ToUpper(id)+"\r\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got, err := ReadLibraryID(lib); err != nil || got != id {
		t.Errorf("ReadLibraryID = %q, %v; want %q", got, err, id)
	}

	// The ID names a folder, so anything else in the file is refused.
	if err := os.WriteFile(filepath.Join(lib, LibraryIDFile), []byte(`..\..\evil`), 0o644); err != nil {
		t.Fatal(err)
	}
	if got, err := ReadLibraryID(lib); err == nil {
		t.Errorf("ReadLibraryID accepted %q", got)
	}
}
