package store

import (
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// Playlist is a playlist of the library as the index has it: where it is,
// and its size and modification time when its entries were read.
type Playlist struct {
	Path    string
	Size    int64
	ModTime time.Time
}

// Playlists lists the playlists the index knows, by the key of their path.
func (s *Store) Playlists() (map[string]Playlist, error) {
	rows, err := s.db.Query(`SELECT path, path_key, size, mtime FROM playlists`)
	if err != nil {
		return nil, fmt.Errorf("reading the playlists: %w", err)
	}
	defer rows.Close()
	out := map[string]Playlist{}
	for rows.Next() {
		var p Playlist
		var key string
		var mtime int64
		if err := rows.Scan(&p.Path, &key, &p.Size, &mtime); err != nil {
			return nil, fmt.Errorf("reading the playlists: %w", err)
		}
		p.ModTime = time.Unix(0, mtime)
		out[key] = p
	}
	return out, rows.Err()
}

// SetPlaylist records the playlist p with the files it names, each by the
// key of its whole path, in the place of what the index had for it.
func (s *Store) SetPlaylist(p Playlist, targets []string) error {
	fail := func(err error) error { return fmt.Errorf("recording the playlist %q: %w", p.Path, err) }
	tx, err := s.db.Begin()
	if err != nil {
		return fail(err)
	}
	defer tx.Rollback()
	key := PathKey(p.Path)
	if err := deletePlaylist(tx, key); err != nil {
		return fail(err)
	}
	res, err := tx.Exec(`INSERT INTO playlists (path, path_key, size, mtime) VALUES (?, ?, ?, ?)`, p.Path, key, p.Size, p.ModTime.UnixNano())
	if err != nil {
		return fail(err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return fail(err)
	}
	seen := map[string]bool{}
	for _, target := range targets {
		if seen[target] {
			continue
		}
		seen[target] = true
		if _, err := tx.Exec(`INSERT INTO playlist_entries (playlist_id, target_key) VALUES (?, ?)`, id, target); err != nil {
			return fail(err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fail(err)
	}
	return nil
}

func deletePlaylist(tx *sql.Tx, key string) error {
	var id int64
	switch err := tx.QueryRow(`SELECT id FROM playlists WHERE path_key = ?`, key).Scan(&id); {
	case errors.Is(err, sql.ErrNoRows):
		return nil
	case err != nil:
		return err
	}
	if _, err := tx.Exec(`DELETE FROM playlist_entries WHERE playlist_id = ?`, id); err != nil {
		return err
	}
	_, err := tx.Exec(`DELETE FROM playlists WHERE id = ?`, id)
	return err
}

// DeletePlaylists takes the playlists at paths out of the index.
func (s *Store) DeletePlaylists(paths []string) error {
	if len(paths) == 0 {
		return nil
	}
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("removing playlists from the index: %w", err)
	}
	defer tx.Rollback()
	for _, path := range paths {
		if err := deletePlaylist(tx, PathKey(path)); err != nil {
			return fmt.Errorf("removing the playlist %q from the index: %w", path, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("removing playlists from the index: %w", err)
	}
	return nil
}

// manyTargets is the number of files above which PlaylistsNaming reads the
// whole table once and does not ask for each file.
const manyTargets = 200

// PlaylistsNaming lists the playlists that name one of the files whose path
// keys are given, each once.
func (s *Store) PlaylistsNaming(keys []string) ([]string, error) {
	fail := func(err error) ([]string, error) {
		return nil, fmt.Errorf("finding the playlists that name a file: %w", err)
	}
	if len(keys) == 0 {
		return nil, nil
	}
	found := map[string]bool{}
	var out []string
	add := func(path string) {
		if !found[path] {
			found[path] = true
			out = append(out, path)
		}
	}
	const named = `SELECT p.path, e.target_key FROM playlist_entries e JOIN playlists p ON p.id = e.playlist_id`
	if len(keys) > manyTargets {
		want := make(map[string]bool, len(keys))
		for _, k := range keys {
			want[k] = true
		}
		rows, err := s.db.Query(named + ` ORDER BY p.path`)
		if err != nil {
			return fail(err)
		}
		defer rows.Close()
		for rows.Next() {
			var path, target string
			if err := rows.Scan(&path, &target); err != nil {
				return fail(err)
			}
			if want[target] {
				add(path)
			}
		}
		if err := rows.Err(); err != nil {
			return fail(err)
		}
		return out, nil
	}
	stmt, err := s.db.Prepare(named + ` WHERE e.target_key = ? ORDER BY p.path`)
	if err != nil {
		return fail(err)
	}
	defer stmt.Close()
	for _, key := range keys {
		rows, err := stmt.Query(key)
		if err != nil {
			return fail(err)
		}
		for rows.Next() {
			var path, target string
			if err := rows.Scan(&path, &target); err != nil {
				rows.Close()
				return fail(err)
			}
			add(path)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return fail(err)
		}
	}
	return out, nil
}

// RecordPlaylistRewrite records that a run rewrote the entries of the
// playlist at path: before and after are what it held, whole. Undoing the
// run gives it back what it held before, while it still holds what the run
// left.
func (s *Store) RecordPlaylistRewrite(runID int64, path string, before, after []byte) error {
	return s.recordMove(runID, path, path, ReasonPlaylistRewrite, append([]byte{}, before...), append([]byte{}, after...))
}
