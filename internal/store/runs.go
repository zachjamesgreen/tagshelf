package store

import (
	"database/sql"
	"fmt"
	"time"
)

// RunKind is what a run did.
type RunKind string

const (
	KindOrganize   RunKind = "organize"
	KindReorganize RunKind = "reorganize"
	KindIndex      RunKind = "index"
	KindTUIAction  RunKind = "tui-action"
	KindUndo       RunKind = "undo"
)

// RunState is where a run stands.
type RunState string

const (
	StateRunning     RunState = "running"
	StateOK          RunState = "ok"
	StateErrors      RunState = "errors"
	StateInterrupted RunState = "interrupted"
	StateCrashed     RunState = "crashed"
	StateUndone      RunState = "undone"
)

// MoveReason says why a run moved a file.
type MoveReason string

const (
	ReasonOrganized  MoveReason = "organized"   // into the library
	ReasonUntagged   MoveReason = "untagged"    // into the untagged folder
	ReasonOther      MoveReason = "other"       // non-audio into the other folder
	ReasonDuplicate  MoveReason = "duplicate"   // into the duplicates folder
	ReasonCompanion  MoveReason = "companion"   // non-audio next to its album
	ReasonRenamed    MoveReason = "renamed"     // onto its clean name or spelling
	ReasonCueRewrite MoveReason = "cue-rewrite" // a cue sheet's original kept as .cue.orig
)

// Counters are a run's totals, as the engine counts them.
type Counters struct {
	TotalFound   int64
	Organized    int64
	Untagged     int64
	Duplicates   int64 // files moved to the duplicates folder
	InPlace      int64
	Errors       int64
	OtherFound   int64
	OtherMoved   int64
	LeftInPlace  int64
	Retried      int64
	CueRewritten int64
	Indexed      int64 // files read into the index
}

// Run is a row of the run history. Ended is zero while it is running.
type Run struct {
	ID          int64
	Started     time.Time
	Ended       time.Time
	Kind        RunKind
	DryRun      bool
	Source      string
	Destination string
	Counters    Counters
	State       RunState
	// UndoneBy is the undo run that undid this one, 0 when none has.
	UndoneBy int64
}

// Move is a file a run moved.
type Move struct {
	ID       int64
	From, To string
	Reason   MoveReason
	At       time.Time
}

// RunError is a file a run failed on.
type RunError struct {
	Path, Message string
}

// timeFormat is how times are stored: sortable text, always UTC.
const timeFormat = time.RFC3339Nano

func formatTime(t time.Time) string {
	return t.UTC().Format(timeFormat)
}

func parseTime(s sql.NullString) (time.Time, error) {
	if !s.Valid {
		return time.Time{}, nil
	}
	return time.Parse(timeFormat, s.String)
}

// StartRun records a run as running and returns its ID.
func (s *Store) StartRun(kind RunKind, dryRun bool, source, destination string, at time.Time) (int64, error) {
	res, err := s.db.Exec(`INSERT INTO runs (started_at, kind, dry_run, source, destination, state) VALUES (?, ?, ?, ?, ?, ?)`,
		formatTime(at), string(kind), dryRun, source, destination, string(StateRunning))
	if err != nil {
		return 0, fmt.Errorf("recording the run: %w", err)
	}
	return res.LastInsertId()
}

// markCrashed marks every run still recorded as running as crashed. Only the
// holder of the library lock may call it: while the lock is ours no other
// process is running, so those runs' processes died before finishing them.
// Their end time stays unknown.
func (s *Store) markCrashed() error {
	if _, err := s.db.Exec(`UPDATE runs SET state = ? WHERE state = ?`, string(StateCrashed), string(StateRunning)); err != nil {
		return fmt.Errorf("marking crashed runs: %w", err)
	}
	return nil
}

// FinishRun records how a run ended.
func (s *Store) FinishRun(id int64, c Counters, state RunState, at time.Time) error {
	res, err := s.db.Exec(`UPDATE runs SET ended_at = ?, state = ?,
		total_found = ?, organized = ?, untagged = ?, duplicates = ?, in_place = ?, errors = ?,
		other_found = ?, other_moved = ?, left_in_place = ?, retried = ?, cue_rewritten = ?, indexed = ?
		WHERE id = ?`,
		formatTime(at), string(state),
		c.TotalFound, c.Organized, c.Untagged, c.Duplicates, c.InPlace, c.Errors,
		c.OtherFound, c.OtherMoved, c.LeftInPlace, c.Retried, c.CueRewritten, c.Indexed,
		id)
	if err != nil {
		return fmt.Errorf("recording the end of run %d: %w", id, err)
	}
	if n, err := res.RowsAffected(); err == nil && n != 1 {
		return fmt.Errorf("recording the end of run %d: no such run", id)
	}
	return nil
}

// RecordMove records that a run moved a file and, in the same transaction,
// moves the file's index row with it. A cue rewrite keeps the cue sheet where
// it is, so its row stays put.
func (s *Store) RecordMove(runID int64, from, to string, reason MoveReason) error {
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("recording the move of %q: %w", from, err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`INSERT INTO moves (run_id, from_path, to_path, reason, moved_at) VALUES (?, ?, ?, ?, ?)`,
		runID, from, to, string(reason), formatTime(time.Now())); err != nil {
		return fmt.Errorf("recording the move of %q: %w", from, err)
	}
	if reason != ReasonCueRewrite {
		if err := moveFileRow(tx, from, to); err != nil {
			return fmt.Errorf("moving %q in the index: %w", from, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("recording the move of %q: %w", from, err)
	}
	return nil
}

// RecordError records that a run failed on a file.
func (s *Store) RecordError(runID int64, path, message string) error {
	_, err := s.db.Exec(`INSERT INTO run_errors (run_id, path, message) VALUES (?, ?, ?)`, runID, path, message)
	if err != nil {
		return fmt.Errorf("recording the error on %q: %w", path, err)
	}
	return nil
}

// Runs lists the run history, oldest first.
func (s *Store) Runs() ([]Run, error) {
	rows, err := s.db.Query(`SELECT id, started_at, ended_at, kind, dry_run, source, destination,
		total_found, organized, untagged, duplicates, in_place, errors,
		other_found, other_moved, left_in_place, retried, cue_rewritten, indexed, state, COALESCE(undone_by, 0)
		FROM runs ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var runs []Run
	for rows.Next() {
		var r Run
		var started, ended sql.NullString
		var kind, state string
		c := &r.Counters
		if err := rows.Scan(&r.ID, &started, &ended, &kind, &r.DryRun, &r.Source, &r.Destination,
			&c.TotalFound, &c.Organized, &c.Untagged, &c.Duplicates, &c.InPlace, &c.Errors,
			&c.OtherFound, &c.OtherMoved, &c.LeftInPlace, &c.Retried, &c.CueRewritten, &c.Indexed, &state, &r.UndoneBy); err != nil {
			return nil, err
		}
		if r.Started, err = parseTime(started); err != nil {
			return nil, err
		}
		if r.Ended, err = parseTime(ended); err != nil {
			return nil, err
		}
		r.Kind, r.State = RunKind(kind), RunState(state)
		runs = append(runs, r)
	}
	return runs, rows.Err()
}

// Moves lists the moves a run made, in the order it made them.
func (s *Store) Moves(runID int64) ([]Move, error) {
	rows, err := s.db.Query(`SELECT id, from_path, to_path, reason, moved_at FROM moves WHERE run_id = ? ORDER BY id`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var moves []Move
	for rows.Next() {
		var m Move
		var reason string
		var at sql.NullString
		if err := rows.Scan(&m.ID, &m.From, &m.To, &reason, &at); err != nil {
			return nil, err
		}
		if m.At, err = parseTime(at); err != nil {
			return nil, err
		}
		m.Reason = MoveReason(reason)
		moves = append(moves, m)
	}
	return moves, rows.Err()
}

// RunErrors lists the files a run failed on, in the order it failed.
func (s *Store) RunErrors(runID int64) ([]RunError, error) {
	rows, err := s.db.Query(`SELECT path, message FROM run_errors WHERE run_id = ? ORDER BY id`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var errs []RunError
	for rows.Next() {
		var e RunError
		if err := rows.Scan(&e.Path, &e.Message); err != nil {
			return nil, err
		}
		errs = append(errs, e)
	}
	return errs, rows.Err()
}

// Run reads one run of the history.
func (s *Store) Run(id int64) (Run, error) {
	r, err := s.run(id)
	if err != nil {
		return Run{}, err
	}
	return *r, nil
}

// MarkUndone records that the run id was undone by the undo run by: it is
// then in state undone and is not undone again.
func (s *Store) MarkUndone(id, by int64) error {
	res, err := s.db.Exec(`UPDATE runs SET state = ?, undone_by = ? WHERE id = ?`, string(StateUndone), by, id)
	if err != nil {
		return fmt.Errorf("marking run %d undone: %w", id, err)
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return fmt.Errorf("marking run %d undone: no such run", id)
	}
	return nil
}

// MovesAfter lists the moves of files recorded after the move with the given
// ID, by any run, in the order they were made. Cue sheet rewrites, which
// move nothing, are left out.
func (s *Store) MovesAfter(moveID int64) ([]Move, error) {
	rows, err := s.db.Query(`SELECT id, from_path, to_path, reason FROM moves WHERE id > ? AND reason <> ? ORDER BY id`,
		moveID, string(ReasonCueRewrite))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var moves []Move
	for rows.Next() {
		var m Move
		var reason string
		if err := rows.Scan(&m.ID, &m.From, &m.To, &reason); err != nil {
			return nil, err
		}
		m.Reason = MoveReason(reason)
		moves = append(moves, m)
	}
	return moves, rows.Err()
}
