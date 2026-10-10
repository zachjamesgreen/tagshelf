package store

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
)

// migrationsBefore returns the real migrations up to, and not including, the
// one whose file name ends in suffix.
func migrationsBefore(t *testing.T, suffix string) []migration {
	t.Helper()
	i := slices.IndexFunc(migrations, func(m migration) bool { return strings.HasSuffix(m.name, suffix) })
	if i < 0 {
		t.Fatalf("no migration named *%s", suffix)
	}
	return migrations[:i]
}

// TestFileIDsAreNeverReused is the fault the file IDs migration mends: the
// newest file's ID went to the next file indexed once its row was deleted,
// and a review of the old file then covered the new one.
func TestFileIDsAreNeverReused(t *testing.T) {
	s := openMemory(t)
	lib := setRoot(t, s, RootLibrary, filepath.FromSlash("/m"))
	putFiles(t, s,
		File{RootID: lib, RelPath: "a.flac", Sum: "a", ReleaseTrackID: "t1"},
		File{RootID: lib, RelPath: "b.mp3", Sum: "b", ReleaseTrackID: "t1"},
	)
	groups, err := s.Groups(testKey)
	if err != nil || len(groups) != 1 {
		t.Fatalf("groups = %+v, %v; want one", groups, err)
	}
	reviewed := groups[0].IDs()
	if err := s.RecordReview(Review{Kind: groups[0].Kind, Members: reviewed, Outcome: OutcomeKeepBoth, By: ByUser}); err != nil {
		t.Fatal(err)
	}

	// b, the newest row, goes, and a different copy of the song arrives.
	if err := s.DeleteFiles(reviewed[1:]); err != nil {
		t.Fatal(err)
	}
	putFiles(t, s, File{RootID: lib, RelPath: "c.mp3", Sum: "c", ReleaseTrackID: "t1"})
	files, err := s.Files()
	if err != nil || len(files) != 2 {
		t.Fatalf("files = %+v, %v; want a and c", files, err)
	}
	if c := files[1]; c.ID <= reviewed[1] {
		t.Errorf("the new file got ID %d, want one above the deleted file's %d", c.ID, reviewed[1])
	}
	if n, err := s.GroupsNeedingReview(testKey); err != nil || n != 1 {
		t.Errorf("groups needing review = %d, %v; want the group with the new copy", n, err)
	}
}

// TestFileIDsMigrationKeepsEveryRow upgrades a database with an index from
// before file IDs stopped being reused: every row keeps its ID and values, the
// table keeps its indexes and its uniqueness, and no ID a review names is
// handed out again.
func TestFileIDsMigrationKeepsEveryRow(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tagshelf.db")
	old, err := open(path, migrationsBefore(t, "_file_ids.sql"))
	if err != nil {
		t.Fatal(err)
	}
	// PutFiles and Files are today's, and write and read the has_art column
	// of a later migration; the rebuild leaves it out, as it names its
	// columns, and that migration adds it again.
	if _, err := old.db.Exec(`ALTER TABLE files ADD COLUMN has_art INTEGER`); err != nil {
		t.Fatal(err)
	}
	lib := setRoot(t, old, RootLibrary, filepath.FromSlash("/m"))
	untagged := setRoot(t, old, RootUntagged, filepath.FromSlash("/m/_Untagged"))
	at := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	full := File{RootID: lib, RelPath: filepath.Join("A", "01 - Song.flac"), Format: "FLAC", Artist: "Singer", AlbumArtist: "Band",
		Album: "Album", Title: "Song", Track: 1, TrackTotal: 9, Disc: 2, DiscTotal: 3, Year: 2001,
		ReleaseID: "rel", ReleaseTrackID: "t1", RecordingID: "rec", Manual: "hand 2026-09-27",
		Lossless: true, Bitrate: 900, SampleRate: 44100, BitDepth: 16, Channels: 2, Length: 200 * time.Second,
		Sum: "a", Size: 123, ModTime: time.Unix(1000, 5), IndexedAt: at, TagsJSON: `{"title":["Song"]}`, ReadError: "art unreadable"}
	putFiles(t, old,
		full,
		File{RootID: untagged, RelPath: "gone.mp3", Sum: "gone", Size: 1, ModTime: time.Unix(1, 0), IndexedAt: at},
		File{RootID: untagged, RelPath: "nohash.mp3", Size: 2, ModTime: time.Unix(2, 0), IndexedAt: at},
		File{RootID: lib, RelPath: "b.mp3", Sum: "b", ReleaseTrackID: "t1", Size: 3, ModTime: time.Unix(3, 0), IndexedAt: at},
		File{RootID: lib, RelPath: "newest.mp3", Sum: "n", ReleaseTrackID: "t1", Size: 4, ModTime: time.Unix(4, 0), IndexedAt: at},
	)
	before, err := old.Files()
	if err != nil || len(before) != 5 {
		t.Fatalf("files = %+v, %v", before, err)
	}
	// IDs with a gap, and the newest row deleted after a review named it: the
	// state in which the next file used to take a reviewed ID.
	gone, newest := before[1].ID, before[4].ID
	if err := old.RecordReview(Review{Kind: GroupReleaseTrack, Members: []int64{before[0].ID, before[3].ID, newest}, Keeper: before[0].ID,
		Outcome: OutcomeKeepBoth, By: ByUser}); err != nil {
		t.Fatal(err)
	}
	if err := old.DeleteFiles([]int64{gone, newest}); err != nil {
		t.Fatal(err)
	}
	if before, err = old.Files(); err != nil || len(before) != 3 {
		t.Fatalf("files = %+v, %v", before, err)
	}
	run, err := old.StartRun(KindOrganize, false, "s", "d", at)
	if err != nil {
		t.Fatal(err)
	}
	old.Close()

	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	after, err := s.Files()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(after, before) {
		t.Errorf("files after the upgrade:\n%+v\nwant them as they were:\n%+v", after, before)
	}
	if roots, err := s.Roots(); err != nil || len(roots) != 2 {
		t.Errorf("roots = %+v, %v; want both kept", roots, err)
	}
	if runs, err := s.Runs(); err != nil || len(runs) != 1 || runs[0].ID != run {
		t.Errorf("runs = %+v, %v; want run %d kept", runs, err, run)
	}
	if reviews, err := s.Reviews(); err != nil || len(reviews) != 1 || !slices.Contains(reviews[0].Members, newest) {
		t.Errorf("reviews = %+v, %v; want the one review kept", reviews, err)
	}

	// The table is files again, with its indexes, and nothing of the rebuild.
	var names []string
	rows, err := s.db.Query(`SELECT name FROM sqlite_master WHERE tbl_name LIKE 'files%' AND name NOT LIKE 'sqlite_%' ORDER BY name`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		names = append(names, name)
	}
	rows.Close()
	if want := []string{"files", "files_audio_sum", "files_release_track"}; !reflect.DeepEqual(names, want) {
		t.Errorf("tables and indexes = %v, want %v", names, want)
	}
	var broken int
	if err := s.db.QueryRow(`SELECT count(*) FROM pragma_foreign_key_check`).Scan(&broken); err != nil || broken != 0 {
		t.Errorf("foreign key check found %d broken rows (%v)", broken, err)
	}
	if _, err := s.db.Exec(`INSERT INTO files (root_id, rel_path, path_key, size, mtime, indexed_at) VALUES (99, 'x', 'x', 1, 1, '')`); err == nil {
		t.Error("a file under a root that does not exist was accepted: the foreign key is gone")
	}

	// One row per path still: indexing a file again keeps its row and ID.
	again := full
	again.Title = "Song (remastered)"
	putFiles(t, s, again)
	if files, err := s.Files(); err != nil || len(files) != 3 || files[0].ID != before[0].ID || files[0].Title != again.Title {
		t.Errorf("files after indexing one again = %+v, %v; want the same three rows", files, err)
	}

	// A new file gets an ID no row has had and no review names.
	putFiles(t, s, File{RootID: lib, RelPath: "new.mp3", Sum: "new", ReleaseTrackID: "t1", Size: 5, ModTime: time.Unix(5, 0)})
	added, ok, err := s.FileAt(filepath.FromSlash("/m/new.mp3"))
	if err != nil || !ok {
		t.Fatalf("FileAt(new.mp3) = %t, %v", ok, err)
	}
	if added.ID <= newest {
		t.Errorf("the new file got ID %d, want one above %d, which a review names", added.ID, newest)
	}
	if n, err := s.GroupsNeedingReview(testKey); err != nil || n != 1 {
		t.Errorf("groups needing review = %d, %v; want the group the new copy joined", n, err)
	}
	if err := s.DeleteFiles([]int64{added.ID}); err != nil {
		t.Fatal(err)
	}
	putFiles(t, s, File{RootID: lib, RelPath: "newer.mp3", Sum: "newer", Size: 6, ModTime: time.Unix(6, 0)})
	if next, ok, err := s.FileAt(filepath.FromSlash("/m/newer.mp3")); err != nil || !ok || next.ID <= added.ID {
		t.Errorf("after a delete the next file got ID %d (%t, %v), want one above %d", next.ID, ok, err, added.ID)
	}
}

// TestFileIDsMigrationOnAnEmptyIndex checks that the IDs reviews name are not
// handed out again even when no file row is left to say how high they went.
func TestFileIDsMigrationOnAnEmptyIndex(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tagshelf.db")
	old, err := open(path, migrationsBefore(t, "_file_ids.sql"))
	if err != nil {
		t.Fatal(err)
	}
	if err := old.RecordReview(Review{Kind: GroupExact, Members: []int64{3, 41}, Keeper: 3, Outcome: OutcomeResolved, By: ByAuto}); err != nil {
		t.Fatal(err)
	}
	old.Close()

	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	lib := setRoot(t, s, RootLibrary, filepath.FromSlash("/m"))
	putFiles(t, s, File{RootID: lib, RelPath: "a.mp3", Sum: "a", Size: 1, ModTime: time.Unix(1, 0)})
	if files, err := s.Files(); err != nil || len(files) != 1 || files[0].ID != 42 {
		t.Errorf("files = %+v, %v; want the first file to get ID 42, above every reviewed ID", files, err)
	}
}

// TestUpgradeFromEachRealVersion upgrades a database from every earlier
// version of the real schema, holding what that version could hold: a run
// from the first on, and an index and a review once there were tables for
// them.
func TestUpgradeFromEachRealVersion(t *testing.T) {
	for from := 1; from < len(migrations); from++ {
		t.Run(fmt.Sprintf("from v%d", from), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "tagshelf.db")
			old, err := open(path, migrations[:from])
			if err != nil {
				t.Fatal(err)
			}
			exec := func(query string, args ...any) {
				t.Helper()
				if _, err := old.db.Exec(query, args...); err != nil {
					t.Fatal(err)
				}
			}
			has := func(table string) bool {
				var n int
				if err := old.db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = ?`, table).Scan(&n); err != nil {
					t.Fatal(err)
				}
				return n == 1
			}
			// Written as SQL the first version understood, not through the
			// Store, whose queries are for the current schema.
			exec(`INSERT INTO runs (id, started_at, ended_at, kind, dry_run, source, destination, organized, state)
				VALUES (7, '2026-09-28T10:00:00Z', '2026-09-28T10:01:00Z', 'organize', 0, 's', 'd', 3, 'ok')`)
			exec(`INSERT INTO moves (run_id, from_path, to_path, reason, moved_at) VALUES (7, 'a', 'b', 'organized', '2026-09-28T10:00:30Z')`)
			tagWrites, index, reviews := has("tag_writes"), has("files"), has("dup_reviews")
			if tagWrites {
				exec(`INSERT INTO tag_writes (run_id, path, before, after, written_at) VALUES (7, 'b', '{}', '{"title":["T"]}', '2026-09-28T10:00:40Z')`)
			}
			if index {
				exec(`INSERT INTO roots (id, kind, path) VALUES (2, 'library', ?)`, filepath.FromSlash("/m"))
				exec(`INSERT INTO files (id, root_id, rel_path, path_key, title, audio_sum, size, mtime, indexed_at)
					VALUES (5, 2, 'Song.mp3', 'song.mp3', 'Song', 'sum', 9, 1000, '2026-09-28T10:00:00Z')`)
			}
			if reviews {
				exec(`INSERT INTO dup_reviews (kind, members, keeper, outcome, decided_by, run_id, reviewed_at)
					VALUES ('exact', '[5,6]', 5, 'resolved', 'user', 7, '2026-09-28T10:00:50Z')`)
			}
			old.Close()

			s, err := Open(path)
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			if got := versionOf(t, s.db); got != len(migrations) {
				t.Errorf("user_version = %d, want %d", got, len(migrations))
			}
			if got, want := backupsOf(t, path), []string{fmt.Sprintf("%s.bak-v%d", path, from)}; !reflect.DeepEqual(got, want) {
				t.Errorf("backups = %v, want %v", got, want)
			}
			run, err := s.Run(7)
			if err != nil || run.Kind != KindOrganize || run.State != StateOK || run.Counters.Organized != 3 || run.Ended.IsZero() {
				t.Errorf("run = %+v, %v; want the organize run kept", run, err)
			}
			if moves, err := s.Moves(7); err != nil || len(moves) != 1 || moves[0].From != "a" || moves[0].To != "b" {
				t.Errorf("moves = %+v, %v; want the one move kept", moves, err)
			}
			writes, err := s.TagWrites(7)
			if err != nil || (len(writes) == 1) != tagWrites {
				t.Errorf("tag writes = %+v, %v; want one: %t", writes, err, tagWrites)
			}
			files, err := s.Files()
			switch {
			case err != nil:
				t.Error(err)
			case !index && len(files) != 0:
				t.Errorf("files = %+v, want none", files)
			case index && (len(files) != 1 || files[0].ID != 5 || files[0].Title != "Song" || files[0].Sum != "sum" ||
				files[0].Path != filepath.FromSlash("/m/Song.mp3") || files[0].Size != 9):
				t.Errorf("files = %+v, want the one file kept with its ID", files)
			}
			kept, err := s.Reviews()
			if err != nil || (len(kept) == 1) != reviews {
				t.Errorf("reviews = %+v, %v; want one: %t", kept, err, reviews)
			}
			if owner, err := s.owner(); err != nil || owner != "" {
				t.Errorf("the upgraded database belongs to %q (%v), want to no library until one opens it", owner, err)
			}
		})
	}
}

// TestUpgradeNeedsItsBackup checks that a database is not changed when its
// backup cannot be written, and that a backup cut short earlier is replaced.
func TestUpgradeNeedsItsBackup(t *testing.T) {
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
	backup := path + ".bak-v1"

	// Something that cannot be removed is where the backup is written first.
	if err := os.MkdirAll(filepath.Join(backup+".tmp", "in the way"), 0o755); err != nil {
		t.Fatal(err)
	}
	if s, err := open(path, ms); err == nil || !strings.Contains(err.Error(), "backing up before migrating") {
		if err == nil {
			s.Close()
		}
		t.Fatalf("open with a backup that cannot be written: %v, want it refused for the backup", err)
	}
	if got := versionAt(t, path); got != 1 {
		t.Errorf("user_version = %d after an upgrade without a backup, want 1", got)
	}
	if _, err := os.Stat(backup); !os.IsNotExist(err) {
		t.Errorf("a backup appeared: %v", err)
	}

	// A file left there by a backup that was cut short is only in the way.
	if err := os.RemoveAll(backup + ".tmp"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(backup+".tmp", []byte("half a backup"), 0o644); err != nil {
		t.Fatal(err)
	}
	s, err := open(path, ms)
	if err != nil {
		t.Fatalf("open with a leftover backup file: %v", err)
	}
	s.Close()
	if got := versionAt(t, backup); got != 1 {
		t.Errorf("the backup is at version %d, want the database as it was at 1", got)
	}
	if _, err := os.Stat(backup + ".tmp"); !os.IsNotExist(err) {
		t.Errorf("the leftover backup file is still there: %v", err)
	}
}
