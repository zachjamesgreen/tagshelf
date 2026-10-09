package store

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"testing/fstest"
	"time"
)

func TestOpenCreatesAndMigrates(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "tagshelf.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	if got := versionOf(t, s.db); got != len(migrations) {
		t.Errorf("user_version = %d, want %d", got, len(migrations))
	}
	if s.Path() != path {
		t.Errorf("Path() = %q, want %q", s.Path(), path)
	}
	// A new database has nothing worth backing up.
	if backups := backupsOf(t, path); len(backups) != 0 {
		t.Errorf("new database was backed up: %v", backups)
	}
}

func TestOpenTwiceKeepsData(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tagshelf.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	id, err := s.StartRun(KindOrganize, false, "src", "dst", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	s.Close()

	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	runs, err := s.Runs()
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 1 || runs[0].ID != id {
		t.Errorf("runs after reopening = %+v, want the one run %d", runs, id)
	}
	if backups := backupsOf(t, path); len(backups) != 0 {
		t.Errorf("an up-to-date database was backed up: %v", backups)
	}
}

// testMigrations is a three-step history for exercising upgrades. Each step
// adds a table and a row, so an upgrade can be checked for running exactly
// the steps it was missing.
var testMigrations = fstest.MapFS{
	"0001_one.sql":   {Data: []byte("CREATE TABLE one (x INTEGER); INSERT INTO one VALUES (1);")},
	"0002_two.sql":   {Data: []byte("CREATE TABLE two (x INTEGER); INSERT INTO two VALUES (2);")},
	"0003_three.sql": {Data: []byte("CREATE TABLE three (x INTEGER); INSERT INTO three VALUES (3);")},
}

func TestUpgradeFromEachEarlierVersion(t *testing.T) {
	ms, err := loadMigrations(testMigrations)
	if err != nil {
		t.Fatal(err)
	}
	for from := 0; from < len(ms); from++ {
		t.Run(fmt.Sprintf("from v%d", from), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "tagshelf.db")
			old, err := open(path, ms[:from])
			if err != nil {
				t.Fatal(err)
			}
			if _, err := old.db.Exec("CREATE TABLE marker (x INTEGER); INSERT INTO marker VALUES (42)"); err != nil {
				t.Fatal(err)
			}
			old.Close()

			s, err := open(path, ms)
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			if got := versionOf(t, s.db); got != len(ms) {
				t.Errorf("user_version = %d, want %d", got, len(ms))
			}
			for i, table := range []string{"one", "two", "three"} {
				var n int
				if err := s.db.QueryRow("SELECT count(*) FROM " + table).Scan(&n); err != nil {
					t.Fatalf("table %s: %v", table, err)
				}
				if n != 1 {
					t.Errorf("table %s has %d rows, want 1 (migration %d ran %d times)", table, n, i+1, n)
				}
			}

			backup := fmt.Sprintf("%s.bak-v%d", path, from)
			if got := backupsOf(t, path); !reflect.DeepEqual(got, []string{backup}) {
				t.Fatalf("backups = %v, want %v", got, []string{backup})
			}
			// The backup is the database as it was before the upgrade.
			b, err := sql.Open(driverName, backup)
			if err != nil {
				t.Fatal(err)
			}
			defer b.Close()
			if got := versionOf(t, b); got != from {
				t.Errorf("backup user_version = %d, want %d", got, from)
			}
			var marker int
			if err := b.QueryRow("SELECT x FROM marker").Scan(&marker); err != nil || marker != 42 {
				t.Errorf("backup marker = %d, %v; want 42", marker, err)
			}
		})
	}
}

func TestFailedMigrationChangesNothing(t *testing.T) {
	ms, err := loadMigrations(testMigrations)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "tagshelf.db")
	old, err := open(path, ms[:1])
	if err != nil {
		t.Fatal(err)
	}
	old.Close()

	broken := append(ms[:2:2], migration{version: 3, name: "0003_broken.sql", sql: "CREATE TABLE three (x INTEGER); NOT SQL"})
	if _, err := open(path, broken); err == nil {
		t.Fatal("open with a broken migration succeeded")
	}

	s, err := open(path, ms[:1])
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if got := versionOf(t, s.db); got != 1 {
		t.Errorf("user_version after a failed upgrade = %d, want 1", got)
	}
	var n int
	if err := s.db.QueryRow("SELECT count(*) FROM sqlite_master WHERE name IN ('two', 'three')").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("a failed upgrade left %d of its tables behind", n)
	}
}

func TestOpenRefusesNewerDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tagshelf.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(fmt.Sprintf("PRAGMA user_version = %d", len(migrations)+1)); err != nil {
		t.Fatal(err)
	}
	s.Close()

	s, err = Open(path)
	if !errors.Is(err, ErrNewerDatabase) {
		if s != nil {
			s.Close()
		}
		t.Fatalf("Open of a newer database: err = %v, want ErrNewerDatabase", err)
	}
	if backups := backupsOf(t, path); len(backups) != 0 {
		t.Errorf("a refused database was backed up: %v", backups)
	}
}

func TestOpenFailsWhenFolderCannotBeMade(t *testing.T) {
	blocker := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(blocker, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if s, err := Open(filepath.Join(blocker, "tagshelf.db")); err == nil {
		s.Close()
		t.Fatal("Open under a file succeeded")
	}
}

func TestLoadMigrationsRejectsGaps(t *testing.T) {
	gap := fstest.MapFS{
		"0001_one.sql":   {Data: []byte("SELECT 1")},
		"0003_three.sql": {Data: []byte("SELECT 1")},
	}
	if _, err := loadMigrations(gap); err == nil {
		t.Error("loadMigrations accepted a gap in the numbering")
	}
}

func TestRunRecording(t *testing.T) {
	s, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	start := time.Date(2026, 9, 28, 10, 0, 0, 0, time.UTC)
	id, err := s.StartRun(KindReorganize, true, `C:\src`, `C:\lib`, start)
	if err != nil {
		t.Fatal(err)
	}
	runs, err := s.Runs()
	if err != nil {
		t.Fatal(err)
	}
	want := Run{ID: id, Started: start, Kind: KindReorganize, DryRun: true, Source: `C:\src`, Destination: `C:\lib`, State: StateRunning}
	if len(runs) != 1 || !reflect.DeepEqual(runs[0], want) {
		t.Fatalf("runs after start = %+v, want %+v", runs, want)
	}

	if err := s.RecordMove(id, `C:\src\a.mp3`, `C:\lib\A\a.mp3`, ReasonOrganized); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordMove(id, `C:\lib\x.cue`, `C:\lib\x.cue.orig`, ReasonCueRewrite); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordError(id, `C:\src\b.mp3`, "boom"); err != nil {
		t.Fatal(err)
	}

	end := start.Add(time.Minute)
	c := Counters{TotalFound: 1, Organized: 2, Untagged: 3, Duplicates: 4, InPlace: 5, Errors: 6, OtherFound: 7, OtherMoved: 8, LeftInPlace: 9, Retried: 10, CueRewritten: 11, Indexed: 12}
	if err := s.FinishRun(id, c, StateErrors, end); err != nil {
		t.Fatal(err)
	}
	runs, err = s.Runs()
	if err != nil {
		t.Fatal(err)
	}
	want.Ended, want.Counters, want.State = end, c, StateErrors
	if len(runs) != 1 || !reflect.DeepEqual(runs[0], want) {
		t.Errorf("runs after finish = %+v, want %+v", runs, want)
	}

	moves, err := s.Moves(id)
	if err != nil {
		t.Fatal(err)
	}
	wantMoves := []Move{
		{From: `C:\src\a.mp3`, To: `C:\lib\A\a.mp3`, Reason: ReasonOrganized},
		{From: `C:\lib\x.cue`, To: `C:\lib\x.cue.orig`, Reason: ReasonCueRewrite},
	}
	if len(moves) != len(wantMoves) {
		t.Fatalf("moves = %+v, want %+v", moves, wantMoves)
	}
	for i := range moves {
		if moves[i].At.IsZero() {
			t.Errorf("move %d has no time", i)
		}
		moves[i].At, moves[i].ID = time.Time{}, 0
	}
	if !reflect.DeepEqual(moves, wantMoves) {
		t.Errorf("moves = %+v, want %+v", moves, wantMoves)
	}

	errs, err := s.RunErrors(id)
	if err != nil {
		t.Fatal(err)
	}
	if want := []RunError{{Path: `C:\src\b.mp3`, Message: "boom"}}; !reflect.DeepEqual(errs, want) {
		t.Errorf("errors = %+v, want %+v", errs, want)
	}
}

func TestRecordMoveRejectsUnknownReason(t *testing.T) {
	s, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	id, err := s.StartRun(KindOrganize, false, "s", "d", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := s.RecordMove(id, "a", "b", MoveReason("teleported")); err == nil {
		t.Error("RecordMove accepted an unknown reason")
	}
	if err := s.RecordMove(id+1, "a", "b", ReasonOrganized); err == nil {
		t.Error("RecordMove accepted a run that does not exist")
	}
}

func versionOf(t *testing.T, db *sql.DB) int {
	t.Helper()
	var v int
	if err := db.QueryRow("PRAGMA user_version").Scan(&v); err != nil {
		t.Fatal(err)
	}
	return v
}

// backupsOf lists the migration backups next to the database at path.
func backupsOf(t *testing.T, path string) []string {
	t.Helper()
	backups, err := filepath.Glob(path + ".bak-v*")
	if err != nil {
		t.Fatal(err)
	}
	return backups
}
