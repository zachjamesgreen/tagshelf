package store

import (
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// RunProgress is how far a running run has got, as it last recorded: the
// phase it is in and the files of the phase's total it has done. Total is 0
// when the phase's size is not known up front.
type RunProgress struct {
	Phase       string
	Done, Total int64
	At          time.Time // when it was recorded; zero before the first time
}

// RecordProgress records a running run's latest progress, for other processes
// to follow.
func (s *Store) RecordProgress(runID int64, p RunProgress) error {
	_, err := s.db.Exec(`UPDATE runs SET phase = ?, progress_done = ?, progress_total = ?, progress_at = ? WHERE id = ?`,
		p.Phase, p.Done, p.Total, formatTime(p.At), runID)
	if err != nil {
		return fmt.Errorf("recording the progress of run %d: %w", runID, err)
	}
	return nil
}

// ActiveRun is a run recorded as running, with its latest progress and the
// last moves it made.
type ActiveRun struct {
	Run      Run
	Progress RunProgress
	Moves    []Move // oldest first
}

// ActiveRun returns the newest run recorded as running, with up to moves of
// its latest moves, or nil when no run is. A run recorded as running whose
// process died stays so until the next OpenLibrary, so callers check the
// library lock to know whether it really is.
func (s *Store) ActiveRun(moves int) (*ActiveRun, error) {
	var a ActiveRun
	var id int64
	var progressAt sql.NullString
	err := s.db.QueryRow(`SELECT id, phase, progress_done, progress_total, progress_at
		FROM runs WHERE state = ? ORDER BY id DESC LIMIT 1`, string(StateRunning)).Scan(
		&id, &a.Progress.Phase, &a.Progress.Done, &a.Progress.Total, &progressAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading the running run: %w", err)
	}
	if a.Progress.At, err = parseTime(progressAt); err != nil {
		return nil, err
	}
	r, err := s.run(id)
	if err != nil {
		return nil, err
	}
	a.Run = *r
	if a.Moves, err = s.lastMoves(id, moves); err != nil {
		return nil, err
	}
	return &a, nil
}

// lastMoves returns up to n of the latest moves of a run, oldest first.
func (s *Store) lastMoves(runID int64, n int) ([]Move, error) {
	if n <= 0 {
		return nil, nil
	}
	rows, err := s.db.Query(`SELECT from_path, to_path, reason, moved_at FROM moves
		WHERE run_id = ? ORDER BY id DESC LIMIT ?`, runID, n)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var moves []Move
	for rows.Next() {
		var m Move
		var reason string
		var at sql.NullString
		if err := rows.Scan(&m.From, &m.To, &reason, &at); err != nil {
			return nil, err
		}
		if m.At, err = parseTime(at); err != nil {
			return nil, err
		}
		m.Reason = MoveReason(reason)
		moves = append(moves, m)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i, j := 0, len(moves)-1; i < j; i, j = i+1, j-1 {
		moves[i], moves[j] = moves[j], moves[i]
	}
	return moves, nil
}
