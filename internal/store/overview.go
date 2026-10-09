package store

import (
	"database/sql"
	"errors"
	"fmt"
)

// Overview is the state of a library at a glance: how many files each of its
// folders holds and how the last run went.
type Overview struct {
	Library, Untagged, Other, Duplicates int64 // indexed audio files per folder

	// LastRun is the newest run that is no longer running, nil when there is
	// none.
	LastRun *Run
}

// Overview reads the library's counts and its last finished run.
func (s *Store) Overview() (Overview, error) {
	var o Overview
	rows, err := s.db.Query(`SELECT r.kind, count(*) FROM files f JOIN roots r ON r.id = f.root_id GROUP BY r.kind`)
	if err != nil {
		return o, fmt.Errorf("counting indexed files: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var kind string
		var n int64
		if err := rows.Scan(&kind, &n); err != nil {
			return o, err
		}
		switch RootKind(kind) {
		case RootLibrary:
			o.Library = n
		case RootUntagged:
			o.Untagged = n
		case RootOther:
			o.Other = n
		case RootDuplicates:
			o.Duplicates = n
		}
	}
	if err := rows.Err(); err != nil {
		return o, err
	}

	var id int64
	err = s.db.QueryRow(`SELECT id FROM runs WHERE state <> ? ORDER BY id DESC LIMIT 1`, string(StateRunning)).Scan(&id)
	switch {
	case errors.Is(err, sql.ErrNoRows):
	case err != nil:
		return o, fmt.Errorf("reading the last run: %w", err)
	default:
		if o.LastRun, err = s.run(id); err != nil {
			return o, err
		}
	}
	return o, nil
}

// run reads one row of the run history.
func (s *Store) run(id int64) (*Run, error) {
	var r Run
	var started, ended sql.NullString
	var kind, state string
	c := &r.Counters
	err := s.db.QueryRow(`SELECT id, started_at, ended_at, kind, dry_run, source, destination,
		total_found, organized, untagged, duplicates, in_place, errors,
		other_found, other_moved, left_in_place, retried, cue_rewritten, indexed, state, COALESCE(undone_by, 0)
		FROM runs WHERE id = ?`, id).Scan(&r.ID, &started, &ended, &kind, &r.DryRun, &r.Source, &r.Destination,
		&c.TotalFound, &c.Organized, &c.Untagged, &c.Duplicates, &c.InPlace, &c.Errors,
		&c.OtherFound, &c.OtherMoved, &c.LeftInPlace, &c.Retried, &c.CueRewritten, &c.Indexed, &state, &r.UndoneBy)
	if err != nil {
		return nil, fmt.Errorf("reading run %d: %w", id, err)
	}
	if r.Started, err = parseTime(started); err != nil {
		return nil, err
	}
	if r.Ended, err = parseTime(ended); err != nil {
		return nil, err
	}
	r.Kind, r.State = RunKind(kind), RunState(state)
	return &r, nil
}
