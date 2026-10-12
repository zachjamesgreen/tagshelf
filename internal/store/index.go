package store

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// RootKind names one of the folders the index covers.
type RootKind string

const (
	RootLibrary    RootKind = "library"
	RootUntagged   RootKind = "untagged"
	RootOther      RootKind = "other"
	RootDuplicates RootKind = "duplicates"
	RootSource     RootKind = "source" // the folder a run organizes from
)

// Root is an indexed folder and where it is now.
type Root struct {
	ID   int64
	Kind RootKind
	Path string
}

// File is an audio file's index row. Path and RootKind are filled in by
// queries from the root's current path and kind; writes go by RootID and
// RelPath.
type File struct {
	ID       int64
	RootID   int64
	RootKind RootKind
	RelPath  string
	Path     string

	Format      string // the tag reader's file type, e.g. MP3, FLAC
	Artist      string
	AlbumArtist string
	Album       string
	Title       string
	Track       int
	TrackTotal  int
	Disc        int
	DiscTotal   int
	Year        int
	// Genre and Composer hold every value of the tag, joined by ListSeparator.
	Genre    string
	Composer string

	ReleaseID      string
	ReleaseTrackID string
	RecordingID    string
	Manual         string // the manual marker as written, "" when there is none

	Lossless   bool
	Bitrate    int // kbit/s
	SampleRate int // Hz
	BitDepth   int
	Channels   int
	Length     time.Duration

	Sum       string // tag.Sum; "" when the audio could not be hashed
	Size      int64
	ModTime   time.Time
	IndexedAt time.Time
	TagsJSON  string // everything the tag reader returned
	// HasArt is set when the file has embedded art. For a row read before
	// the index recorded it, it is whether TagsJSON describes a picture.
	HasArt    bool
	ReadError string // what could not be read, "" when everything was
}

// ListSeparator joins the values of a tag that holds several, such as two
// genres, where they are kept or shown as one text.
const ListSeparator = "; "

// FileStat is what freshness checks compare: a row's identity, size and
// modification time, and the audio hash it was indexed with ("" when the
// audio could not be hashed).
type FileStat struct {
	ID      int64
	RelPath string
	Size    int64
	ModTime time.Time
	Sum     string
}

// PathKey is the form a path is compared in: cleaned, and case-folded on
// Windows, whose file systems ignore case. It matches rules.PathKey.
func PathKey(path string) string {
	key := filepath.Clean(path)
	if runtime.GOOS == "windows" {
		key = strings.ToLower(key)
	}
	return key
}

// SetRoot records where the root of the given kind is now and returns its ID.
// Moving a root keeps its files' rows, which are relative to it.
func (s *Store) SetRoot(kind RootKind, path string) (int64, error) {
	var id int64
	err := s.db.QueryRow(`INSERT INTO roots (kind, path) VALUES (?, ?)
		ON CONFLICT (kind) DO UPDATE SET path = excluded.path
		RETURNING id`, string(kind), filepath.Clean(path)).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("recording the %s root: %w", kind, err)
	}
	return id, nil
}

// Roots lists the indexed roots.
func (s *Store) Roots() ([]Root, error) {
	return queryRoots(s.db)
}

type querier interface {
	Query(query string, args ...any) (*sql.Rows, error)
}

func queryRoots(q querier) ([]Root, error) {
	rows, err := q.Query(`SELECT id, kind, path FROM roots ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var roots []Root
	for rows.Next() {
		var r Root
		var kind string
		if err := rows.Scan(&r.ID, &kind, &r.Path); err != nil {
			return nil, err
		}
		r.Kind = RootKind(kind)
		roots = append(roots, r)
	}
	return roots, rows.Err()
}

// CountFiles returns how many files the index holds.
func (s *Store) CountFiles() (int64, error) {
	var n int64
	err := s.db.QueryRow(`SELECT count(*) FROM files`).Scan(&n)
	return n, err
}

// RootFiles returns the size and modification time of every file indexed
// under a root, keyed by the PathKey of its relative path.
func (s *Store) RootFiles(rootID int64) (map[string]FileStat, error) {
	rows, err := s.db.Query(`SELECT id, rel_path, path_key, size, mtime, COALESCE(audio_sum, '') FROM files WHERE root_id = ?`, rootID)
	if err != nil {
		return nil, fmt.Errorf("reading the index: %w", err)
	}
	defer rows.Close()
	stats := make(map[string]FileStat)
	for rows.Next() {
		var st FileStat
		var key string
		var mtime int64
		if err := rows.Scan(&st.ID, &st.RelPath, &key, &st.Size, &mtime, &st.Sum); err != nil {
			return nil, fmt.Errorf("reading the index: %w", err)
		}
		st.ModTime = time.Unix(0, mtime)
		stats[key] = st
	}
	return stats, rows.Err()
}

// PutFiles adds or replaces the rows of files, in one transaction.
func (s *Store) PutFiles(files []File) error {
	if len(files) == 0 {
		return nil
	}
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("writing the index: %w", err)
	}
	defer tx.Rollback()
	stmt, err := tx.Prepare(`INSERT INTO files (root_id, rel_path, path_key, format,
		artist, album_artist, album, title, track, track_total, disc, disc_total, year,
		mb_release_id, mb_release_track_id, mb_recording_id, manual,
		lossless, bitrate, sample_rate, bit_depth, channels, length_ms,
		audio_sum, size, mtime, indexed_at, tags_json, read_error, has_art, genre, composer)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (root_id, path_key) DO UPDATE SET rel_path = excluded.rel_path, format = excluded.format,
		artist = excluded.artist, album_artist = excluded.album_artist, album = excluded.album, title = excluded.title,
		track = excluded.track, track_total = excluded.track_total, disc = excluded.disc, disc_total = excluded.disc_total,
		year = excluded.year, mb_release_id = excluded.mb_release_id, mb_release_track_id = excluded.mb_release_track_id,
		mb_recording_id = excluded.mb_recording_id, manual = excluded.manual, lossless = excluded.lossless,
		bitrate = excluded.bitrate, sample_rate = excluded.sample_rate, bit_depth = excluded.bit_depth,
		channels = excluded.channels, length_ms = excluded.length_ms, audio_sum = excluded.audio_sum,
		size = excluded.size, mtime = excluded.mtime, indexed_at = excluded.indexed_at,
		tags_json = excluded.tags_json, read_error = excluded.read_error, has_art = excluded.has_art,
		genre = excluded.genre, composer = excluded.composer`)
	if err != nil {
		return fmt.Errorf("writing the index: %w", err)
	}
	defer stmt.Close()
	for _, f := range files {
		var sum sql.NullString
		if f.Sum != "" {
			sum = sql.NullString{String: f.Sum, Valid: true}
		}
		tags := f.TagsJSON
		if tags == "" {
			tags = "{}"
		}
		indexedAt := f.IndexedAt
		if indexedAt.IsZero() {
			indexedAt = time.Now()
		}
		if _, err := stmt.Exec(f.RootID, f.RelPath, PathKey(f.RelPath), f.Format,
			f.Artist, f.AlbumArtist, f.Album, f.Title, f.Track, f.TrackTotal, f.Disc, f.DiscTotal, f.Year,
			f.ReleaseID, f.ReleaseTrackID, f.RecordingID, f.Manual,
			f.Lossless, f.Bitrate, f.SampleRate, f.BitDepth, f.Channels, f.Length.Milliseconds(),
			sum, f.Size, f.ModTime.UnixNano(), formatTime(indexedAt), tags, f.ReadError, f.HasArt, f.Genre, f.Composer); err != nil {
			return fmt.Errorf("indexing %q: %w", f.RelPath, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("writing the index: %w", err)
	}
	return nil
}

// DeleteFiles removes the rows of files that are gone.
func (s *Store) DeleteFiles(ids []int64) error {
	if len(ids) == 0 {
		return nil
	}
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("removing vanished files from the index: %w", err)
	}
	defer tx.Rollback()
	for _, id := range ids {
		if _, err := tx.Exec(`DELETE FROM files WHERE id = ?`, id); err != nil {
			return fmt.Errorf("removing vanished files from the index: %w", err)
		}
	}
	return tx.Commit()
}

// RespellFile records a new spelling of a file's relative path that differs
// only in letter case, as when a folder above it was renamed.
func (s *Store) RespellFile(id int64, relPath string) error {
	if _, err := s.db.Exec(`UPDATE files SET rel_path = ? WHERE id = ?`, relPath, id); err != nil {
		return fmt.Errorf("respelling %q in the index: %w", relPath, err)
	}
	return nil
}

// fileColumns are the columns scanFile reads, in order.
const fileColumns = `f.id, f.root_id, r.kind, f.rel_path, r.path, f.format,
	f.artist, f.album_artist, f.album, f.title, f.track, f.track_total, f.disc, f.disc_total, f.year,
	f.mb_release_id, f.mb_release_track_id, f.mb_recording_id, f.manual,
	f.lossless, f.bitrate, f.sample_rate, f.bit_depth, f.channels, f.length_ms,
	f.audio_sum, f.size, f.mtime, f.indexed_at, f.tags_json, f.read_error, f.has_art,
	COALESCE(f.genre, ''), COALESCE(f.composer, '')`

// Files lists every indexed file, in the order they were first indexed.
func (s *Store) Files() ([]File, error) {
	return s.queryFiles(`SELECT ` + fileColumns + ` FROM files f JOIN roots r ON r.id = f.root_id ORDER BY f.id`)
}

// FilesWithSum lists the indexed files whose audio hashes to sum.
func (s *Store) FilesWithSum(sum string) ([]File, error) {
	return s.queryFiles(`SELECT `+fileColumns+` FROM files f JOIN roots r ON r.id = f.root_id WHERE f.audio_sum = ? ORDER BY f.id`, sum)
}

// FilesByID lists the indexed files with the given IDs, in ID order. IDs
// without a row are simply not in the result.
func (s *Store) FilesByID(ids []int64) ([]File, error) {
	data, err := json.Marshal(ids)
	if err != nil {
		return nil, err
	}
	return s.queryFiles(`SELECT `+fileColumns+` FROM files f JOIN roots r ON r.id = f.root_id
		WHERE f.id IN (SELECT value FROM json_each(?)) ORDER BY f.id`, string(data))
}

func (s *Store) queryFiles(query string, args ...any) ([]File, error) {
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var files []File
	for rows.Next() {
		var f File
		var root, kind string
		var lengthMS, mtime int64
		var sum, indexedAt sql.NullString
		var hasArt sql.NullBool
		if err := rows.Scan(&f.ID, &f.RootID, &kind, &f.RelPath, &root, &f.Format,
			&f.Artist, &f.AlbumArtist, &f.Album, &f.Title, &f.Track, &f.TrackTotal, &f.Disc, &f.DiscTotal, &f.Year,
			&f.ReleaseID, &f.ReleaseTrackID, &f.RecordingID, &f.Manual,
			&f.Lossless, &f.Bitrate, &f.SampleRate, &f.BitDepth, &f.Channels, &lengthMS,
			&sum, &f.Size, &mtime, &indexedAt, &f.TagsJSON, &f.ReadError, &hasArt, &f.Genre, &f.Composer); err != nil {
			return nil, err
		}
		f.RootKind = RootKind(kind)
		f.Path = filepath.Join(root, f.RelPath)
		f.Length = time.Duration(lengthMS) * time.Millisecond
		f.Sum = sum.String
		f.HasArt = hasArt.Bool
		if !hasArt.Valid {
			f.HasArt = describesPicture(f.TagsJSON)
		}
		f.ModTime = time.Unix(0, mtime)
		if f.IndexedAt, err = parseTime(indexedAt); err != nil {
			return nil, err
		}
		files = append(files, f)
	}
	return files, rows.Err()
}

// describesPicture reports whether a row's tags_json holds a picture as the
// index used to describe one: an object with a "picture" key. Only the MP3
// and MP4 readers put one there, which is why has_art replaced it.
func describesPicture(tagsJSON string) bool {
	if !strings.Contains(tagsJSON, `"picture"`) {
		return false
	}
	var tags map[string]any
	if json.Unmarshal([]byte(tagsJSON), &tags) != nil {
		return false
	}
	for _, v := range tags {
		if m, ok := v.(map[string]any); ok {
			if _, ok := m["picture"]; ok {
				return true
			}
		}
	}
	return false
}

// moveFileRow follows a move made by tagshelf in the index, inside the
// transaction that logs it: the row of the file at from now describes the
// file at to. A file moved out of every root loses its row; one moved in from
// outside gets its row from the next walk.
//
// The row takes the modification time the file has at to. A rename keeps it,
// but a copy to a file system with coarser times (exFAT, FAT32, many network
// shares) does not, and a row still holding the source's time would no longer
// match its file. A file whose size is not the row's is left for the next
// walk to read again.
func moveFileRow(tx *sql.Tx, from, to string) error {
	roots, err := queryRoots(tx)
	if err != nil {
		return err
	}
	fromRoot, fromRel, fromOK := rootOf(roots, from)
	if !fromOK {
		return nil
	}
	fromKey := PathKey(fromRel)
	toRoot, toRel, toOK := rootOf(roots, to)
	if !toOK {
		_, err := tx.Exec(`DELETE FROM files WHERE root_id = ? AND path_key = ?`, fromRoot, fromKey)
		return err
	}
	toKey := PathKey(toRel)
	if toRoot != fromRoot || toKey != fromKey {
		// A stale row for whatever was at the destination before.
		if _, err := tx.Exec(`DELETE FROM files WHERE root_id = ? AND path_key = ?`, toRoot, toKey); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(`UPDATE files SET root_id = ?, rel_path = ?, path_key = ? WHERE root_id = ? AND path_key = ?`,
		toRoot, toRel, toKey, fromRoot, fromKey); err != nil {
		return err
	}
	info, err := os.Stat(to)
	if err != nil {
		// Nothing there to take a time from: the row keeps the one it has.
		return nil
	}
	_, err = tx.Exec(`UPDATE files SET mtime = ? WHERE root_id = ? AND path_key = ? AND size = ?`,
		info.ModTime().UnixNano(), toRoot, toKey, info.Size())
	return err
}

// rootOf finds the innermost root holding path and path's place under it.
func rootOf(roots []Root, path string) (id int64, rel string, ok bool) {
	path = filepath.Clean(path)
	best := -1
	for _, r := range roots {
		if rest, in := under(r.Path, path); in && len(r.Path) > best {
			id, rel, ok, best = r.ID, rest, true, len(r.Path)
		}
	}
	return id, rel, ok
}

// under reports whether path lies below dir, and path relative to it.
func under(dir, path string) (string, bool) {
	prefix := dir
	if !strings.HasSuffix(prefix, string(filepath.Separator)) {
		prefix += string(filepath.Separator)
	}
	if len(path) <= len(prefix) {
		return "", false
	}
	head := path[:len(prefix)]
	if head != prefix && !(runtime.GOOS == "windows" && strings.EqualFold(head, prefix)) {
		return "", false
	}
	return path[len(prefix):], true
}

// FileAt returns the index row of the file at path, if it has one.
func (s *Store) FileAt(path string) (File, bool, error) {
	roots, err := queryRoots(s.db)
	if err != nil {
		return File{}, false, err
	}
	root, rel, ok := rootOf(roots, path)
	if !ok {
		return File{}, false, nil
	}
	files, err := s.queryFiles(`SELECT `+fileColumns+` FROM files f JOIN roots r ON r.id = f.root_id
		WHERE f.root_id = ? AND f.path_key = ?`, root, PathKey(rel))
	if err != nil || len(files) == 0 {
		return File{}, false, err
	}
	return files[0], true, nil
}

// TagsRow is a row's ID with everything the tag reader returned for it.
type TagsRow struct {
	ID       int64
	TagsJSON string
}

// FilesWithoutLists lists the rows read before the index kept genres and
// composers, for SetLists to fill in.
func (s *Store) FilesWithoutLists() ([]TagsRow, error) {
	rows, err := s.db.Query(`SELECT id, tags_json FROM files WHERE genre IS NULL OR composer IS NULL ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("reading the index: %w", err)
	}
	defer rows.Close()
	var out []TagsRow
	for rows.Next() {
		var r TagsRow
		if err := rows.Scan(&r.ID, &r.TagsJSON); err != nil {
			return nil, fmt.Errorf("reading the index: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// FileLists are the genres and the composers of the row with an ID, each
// joined by ListSeparator.
type FileLists struct {
	ID              int64
	Genre, Composer string
}

// SetLists records the genres and composers of rows, in one transaction.
func (s *Store) SetLists(lists []FileLists) error {
	if len(lists) == 0 {
		return nil
	}
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("writing the index: %w", err)
	}
	defer tx.Rollback()
	stmt, err := tx.Prepare(`UPDATE files SET genre = ?, composer = ? WHERE id = ?`)
	if err != nil {
		return fmt.Errorf("writing the index: %w", err)
	}
	defer stmt.Close()
	for _, l := range lists {
		if _, err := stmt.Exec(l.Genre, l.Composer, l.ID); err != nil {
			return fmt.Errorf("writing the index: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("writing the index: %w", err)
	}
	return nil
}
