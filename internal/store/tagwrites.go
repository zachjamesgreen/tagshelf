package store

import (
	"database/sql"
	"fmt"
	"time"
)

// TagWrite is a tag save a run made: the file, and the values of the fields
// it wrote before and after, as JSON.
type TagWrite struct {
	Path          string
	Before, After string
	At            time.Time
}

// RecordTagWrite records that a run saved tags to the file at path, with the
// written fields' values before and after as JSON objects. apply makes the
// save itself (for tagwrite, the rename over the original) inside the same
// transaction: the row is kept only if apply succeeds, and apply is not
// called when the row cannot be recorded. apply's error is returned as it is.
func (s *Store) RecordTagWrite(runID int64, path, before, after string, apply func() error) error {
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("recording the tag save of %q: %w", path, err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`INSERT INTO tag_writes (run_id, path, before, after, written_at) VALUES (?, ?, ?, ?, ?)`,
		runID, path, before, after, formatTime(time.Now())); err != nil {
		return fmt.Errorf("recording the tag save of %q: %w", path, err)
	}
	if err := apply(); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("recording the tag save of %q: %w", path, err)
	}
	return nil
}

// TagWrites lists the tag saves a run made, in the order it made them.
func (s *Store) TagWrites(runID int64) ([]TagWrite, error) {
	rows, err := s.db.Query(`SELECT path, before, after, written_at FROM tag_writes WHERE run_id = ? ORDER BY id`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var writes []TagWrite
	for rows.Next() {
		var w TagWrite
		var at sql.NullString
		if err := rows.Scan(&w.Path, &w.Before, &w.After, &at); err != nil {
			return nil, err
		}
		if w.At, err = parseTime(at); err != nil {
			return nil, err
		}
		writes = append(writes, w)
	}
	return writes, rows.Err()
}
