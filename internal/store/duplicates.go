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

// Group is a set of files in the collection that are, or may be, copies of
// one song. Files are in the order they were indexed. AlsoOn lists files of
// the same recording on another release: information, never duplicates.
type Group struct {
	Kind   GroupKind
	Files  []File
	AlsoOn []File
}

// IDs lists the group's file IDs, as a review records them.
func (g Group) IDs() []int64 {
	ids := make([]int64, len(g.Files))
	for i, f := range g.Files {
		ids[i] = f.ID
	}
	return ids
}

// Groups lists the duplicate groups among the files of the collection. A
// confirmed group joins files with the same audio or the same MusicBrainz
// release track, transitively; it is exact when all of it has the same audio.
// Files without MusicBrainz IDs whose possibleKey matches form a possible
// group. It is never merged into a confirmed group, though its files may also
// be in one: it is left out only when all its files are one confirmed group
// already, since it would then add nothing to review. possibleKey is
// rules.PossibleKey applied to a file, "" for a file with nothing to compare;
// store cannot import rules. Confirmed groups come first, each kind in the
// order of its first file.
func (s *Store) Groups(possibleKey func(File) string) ([]Group, error) {
	files, err := s.queryFiles(`SELECT `+fileColumns+` FROM files f JOIN roots r ON r.id = f.root_id
		WHERE r.kind IN (?, ?, ?) ORDER BY f.id`,
		string(RootLibrary), string(RootUntagged), string(RootOther))
	if err != nil {
		return nil, fmt.Errorf("finding duplicate groups: %w", err)
	}

	parent := make([]int, len(files))
	for i := range parent {
		parent[i] = i
	}
	var find func(int) int
	find = func(i int) int {
		if parent[i] != i {
			parent[i] = find(parent[i])
		}
		return parent[i]
	}
	join := func(first map[string]int, key string, i int) {
		if j, ok := first[key]; ok {
			parent[find(i)] = find(j)
		} else {
			first[key] = i
		}
	}
	bySum, byTrack := make(map[string]int), make(map[string]int)
	byRecording := make(map[string][]int)
	for i, f := range files {
		if f.Sum != "" && f.Sum != emptyAudioSum {
			join(bySum, f.Sum, i)
		}
		if f.ReleaseTrackID != "" {
			join(byTrack, f.ReleaseTrackID, i)
		}
		if f.RecordingID != "" {
			byRecording[f.RecordingID] = append(byRecording[f.RecordingID], i)
		}
	}

	var groups []Group
	group := make([]int, len(files)) // index into groups + 1; 0 for none
	members := make(map[int][]int)   // root -> files, in index order
	var roots []int
	for i := range files {
		r := find(i)
		if len(members[r]) == 0 {
			roots = append(roots, r)
		}
		members[r] = append(members[r], i)
	}
	for _, r := range roots {
		m := members[r]
		if len(m) < 2 {
			continue
		}
		g := Group{Kind: GroupExact}
		for _, i := range m {
			g.Files = append(g.Files, files[i])
			if files[i].Sum != files[m[0]].Sum {
				g.Kind = GroupReleaseTrack
			}
			group[i] = len(groups) + 1
		}
		g.AlsoOn = alsoOn(files, m, byRecording, group, len(groups)+1)
		groups = append(groups, g)
	}

	byKey := make(map[string][]int)
	var keys []string
	for i, f := range files {
		if f.ReleaseID != "" || f.ReleaseTrackID != "" || f.RecordingID != "" {
			continue
		}
		key := possibleKey(f)
		if key == "" {
			continue
		}
		if len(byKey[key]) == 0 {
			keys = append(keys, key)
		}
		byKey[key] = append(byKey[key], i)
	}
	for _, key := range keys {
		m := byKey[key]
		if len(m) < 2 || sameGroup(group, m) {
			continue
		}
		g := Group{Kind: GroupPossible}
		for _, i := range m {
			g.Files = append(g.Files, files[i])
		}
		groups = append(groups, g)
	}
	return groups, nil
}

// alsoOn lists the files outside group g that share a recording with one of
// its members m but sit on a release none of them is on.
func alsoOn(files []File, m []int, byRecording map[string][]int, group []int, g int) []File {
	releases := make(map[string]bool)
	for _, i := range m {
		releases[files[i].ReleaseID] = true
	}
	seen := make(map[int]bool)
	var also []int
	for _, i := range m {
		for _, j := range byRecording[files[i].RecordingID] {
			if group[j] == g || seen[j] || releases[files[j].ReleaseID] {
				continue
			}
			seen[j] = true
			also = append(also, j)
		}
	}
	slices.Sort(also)
	var out []File
	for _, j := range also {
		out = append(out, files[j])
	}
	return out
}

// sameGroup reports whether the files m are all in one confirmed group.
func sameGroup(group []int, m []int) bool {
	for _, i := range m {
		if group[i] == 0 || group[i] != group[m[0]] {
			return false
		}
	}
	return true
}

// GroupsNeedingReview counts the duplicate groups of the collection that no
// review covers: new groups, and reviewed ones that have gained a member.
// possibleKey is as for Groups.
func (s *Store) GroupsNeedingReview(possibleKey func(File) string) (int, error) {
	groups, err := s.GroupsToReview(possibleKey)
	return len(groups), err
}

// GroupsToReview lists the duplicate groups of the collection that no review
// covers, in the order of Groups. possibleKey is as for Groups.
func (s *Store) GroupsToReview(possibleKey func(File) string) ([]Group, error) {
	groups, err := s.Groups(possibleKey)
	if err != nil {
		return nil, err
	}
	var waiting []Group
	for _, g := range groups {
		reviewed, err := s.Reviewed(g.Kind, g.IDs())
		if err != nil {
			return nil, err
		}
		if !reviewed {
			waiting = append(waiting, g)
		}
	}
	return waiting, nil
}
