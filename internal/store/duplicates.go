package store

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"slices"
	"time"
)

// GroupKind is how the files of a duplicate group match.
type GroupKind string

const (
	GroupExact        GroupKind = "exact"         // same audio hash
	GroupReleaseTrack GroupKind = "release-track" // same MusicBrainz release track
	GroupPossible     GroupKind = "possible"      // matching tags, no MusicBrainz IDs
)

// ReviewOutcome is what a review decided.
type ReviewOutcome string

const (
	OutcomeResolved ReviewOutcome = "resolved"  // one copy kept, the rest moved to the duplicates folder
	OutcomeKeepBoth ReviewOutcome = "keep-both" // every copy stays
)

// Decider says who decided a review.
type Decider string

const (
	ByAuto Decider = "auto" // a run, resolving an exact group on its own
	ByUser Decider = "user" // a person, in the TUI
)

// Review is a decision on a duplicate group. Members are the group's file IDs
// when it was reviewed; Keeper is 0 for keep-both, and RunID 0 when no run
// carried the decision out.
type Review struct {
	ID      int64
	Kind    GroupKind
	Members []int64
	Keeper  int64
	Outcome ReviewOutcome
	By      Decider
	RunID   int64
	At      time.Time
}

// RecordReview records a decision on a duplicate group. A zero At means now.
func (s *Store) RecordReview(r Review) error {
	members := slices.Clone(r.Members)
	slices.Sort(members)
	data, err := json.Marshal(members)
	if err != nil {
		return err
	}
	at := r.At
	if at.IsZero() {
		at = time.Now()
	}
	if _, err := s.db.Exec(`INSERT INTO dup_reviews (kind, members, keeper, outcome, decided_by, run_id, reviewed_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		string(r.Kind), string(data), nullID(r.Keeper), string(r.Outcome), string(r.By), nullID(r.RunID), formatTime(at)); err != nil {
		return fmt.Errorf("recording the review of a duplicate group: %w", err)
	}
	return nil
}

func nullID(id int64) sql.NullInt64 {
	return sql.NullInt64{Int64: id, Valid: id != 0}
}

// Reviewed reports whether a group of this kind with these members has been
// reviewed: whether some review of that kind covered every one of them. A
// group with a member no review has seen needs review again.
func (s *Store) Reviewed(kind GroupKind, members []int64) (bool, error) {
	data, err := json.Marshal(members)
	if err != nil {
		return false, err
	}
	var n int
	err = s.db.QueryRow(`SELECT count(*) FROM dup_reviews r WHERE r.kind = ?
		AND NOT EXISTS (SELECT 1 FROM json_each(?) m
			WHERE m.value NOT IN (SELECT value FROM json_each(r.members)))`,
		string(kind), string(data)).Scan(&n)
	if err != nil {
		return false, fmt.Errorf("reading duplicate reviews: %w", err)
	}
	return n > 0, nil
}

// Reviews lists every decision on a duplicate group, oldest first.
func (s *Store) Reviews() ([]Review, error) {
	rows, err := s.db.Query(`SELECT id, kind, members, keeper, outcome, decided_by, run_id, reviewed_at
		FROM dup_reviews ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var reviews []Review
	for rows.Next() {
		var r Review
		var kind, members, outcome, by string
		var keeper, runID sql.NullInt64
		var at sql.NullString
		if err := rows.Scan(&r.ID, &kind, &members, &keeper, &outcome, &by, &runID, &at); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(members), &r.Members); err != nil {
			return nil, fmt.Errorf("review %d: %w", r.ID, err)
		}
		if r.At, err = parseTime(at); err != nil {
			return nil, err
		}
		r.Kind, r.Outcome, r.By = GroupKind(kind), ReviewOutcome(outcome), Decider(by)
		r.Keeper, r.RunID = keeper.Int64, runID.Int64
		reviews = append(reviews, r)
	}
	return reviews, rows.Err()
}

// emptyAudioSum is tag.Sum of a file with no audio at all (the SHA-1 of
// nothing). Such files share it without being copies of anything.
const emptyAudioSum = "da39a3ee5e6b4b0d3255bfef95601890afd80709"

// ExactGroups lists the indexed files that share their audio hash with
// another, whatever root they are in, as one group per hash. Each group is in
// the order its files were indexed, and groups in the order of their first
// file. Files without audio are no one's duplicates.
func (s *Store) ExactGroups() ([][]File, error) {
	files, err := s.queryFiles(`SELECT `+fileColumns+` FROM files f JOIN roots r ON r.id = f.root_id
		WHERE f.audio_sum IN (SELECT audio_sum FROM files WHERE audio_sum IS NOT NULL AND audio_sum <> ?
			GROUP BY audio_sum HAVING count(*) > 1)
		ORDER BY f.id`, emptyAudioSum)
	if err != nil {
		return nil, fmt.Errorf("finding exact duplicates: %w", err)
	}
	var groups [][]File
	index := make(map[string]int)
	for _, f := range files {
		i, ok := index[f.Sum]
		if !ok {
			i = len(groups)
			index[f.Sum] = i
			groups = append(groups, nil)
		}
		groups[i] = append(groups[i], f)
	}
	return groups, nil
}

// inCollection reports whether a root holds files that are part of the
// library's collection: the library and its untagged and other folders, not
// the duplicates folder or a source.
func inCollection(kind RootKind) bool {
	return kind == RootLibrary || kind == RootUntagged || kind == RootOther
}

// ExactGroupsNeedingReview counts the exact duplicate groups within the
// library, untagged and other folders that no review covers.
func (s *Store) ExactGroupsNeedingReview() (int, error) {
	groups, err := s.ExactGroups()
	if err != nil {
		return 0, err
	}
	n := 0
	for _, g := range groups {
		var members []int64
		for _, f := range g {
			if inCollection(f.RootKind) {
				members = append(members, f.ID)
			}
		}
		if len(members) < 2 {
			continue
		}
		reviewed, err := s.Reviewed(GroupExact, members)
		if err != nil {
			return 0, err
		}
		if !reviewed {
			n++
		}
	}
	return n, nil
}
