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
	// Seq is the save's place among its run's moves and tag saves, from 1 in
	// the order they were made; 0 for a save recorded before they were
	// numbered.
	Seq int
}

// RecordTagWrite records that a run saved tags to the file at path, with the
// written fields' values before and after as JSON objects, and has apply make
// the save itself (for tagwrite, the rename over the original). apply is not
// called when the row cannot be recorded, and the row is taken back if apply
// fails. apply's error is returned as it is.
//
// The row is committed before apply runs, so that nothing can fail between
// the file being replaced and its save being on record: a save without a
// record could not be undone. What can be left behind instead, by a crash or
// a row that cannot be taken back, is a record without a save, which undo
// skips because the file does not hold the tags the record says it was left
// with.
func (s *Store) RecordTagWrite(runID int64, path, before, after string, apply func() error) error {
	res, err := s.db.Exec(`INSERT INTO tag_writes (run_id, path, before, after, written_at, seq)
		VALUES (?, ?, ?, ?, ?, `+nextSeq+`)`,
		runID, path, before, after, formatTime(time.Now()), runID, runID)
	if err != nil {
		return fmt.Errorf("recording the tag save of %q: %w", path, err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return fmt.Errorf("recording the tag save of %q: %w", path, err)
	}
	if err := apply(); err != nil {
		s.db.Exec(`DELETE FROM tag_writes WHERE id = ?`, id)
		return err
	}
	return nil
}

// TagWrites lists the tag saves a run made, in the order it made them.
func (s *Store) TagWrites(runID int64) ([]TagWrite, error) {
	rows, err := s.db.Query(`SELECT path, before, after, written_at, COALESCE(seq, 0) FROM tag_writes WHERE run_id = ? ORDER BY id`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var writes []TagWrite
	for rows.Next() {
		var w TagWrite
		var at sql.NullString
		if err := rows.Scan(&w.Path, &w.Before, &w.After, &at, &w.Seq); err != nil {
			return nil, err
		}
		if w.At, err = parseTime(at); err != nil {
			return nil, err
		}
		writes = append(writes, w)
	}
	return writes, rows.Err()
}
