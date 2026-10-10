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

// ReviewedTogether reports whether some review of this kind holds two or
// more of members: whether a decision was made about these files as copies
// of each other. A keep-both review does for as long as its copies are
// there; a resolved one stops once the copies not kept have moved to the
// duplicates folder, since only its keeper is then left among a group's
// members. A group with such a review and a member the review has not seen
// needs review again, by a person: nothing may settle it on its own.
func (s *Store) ReviewedTogether(kind GroupKind, members []int64) (bool, error) {
	data, err := json.Marshal(members)
	if err != nil {
		return false, err
	}
	var n int
	err = s.db.QueryRow(`SELECT count(*) FROM dup_reviews r WHERE r.kind = ?
		AND (SELECT count(*) FROM json_each(?) m
			WHERE m.value IN (SELECT value FROM json_each(r.members))) >= 2`,
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

// Group is a set of files of the library, outside its duplicates folder, that
// are, or may be, copies of one song. Files are in the order they were indexed. AlsoOn lists files of
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

// Groups lists the duplicate groups among the files of the library outside
// its duplicates folder, where the copies already set aside are. A confirmed
// group joins files with the same audio or the same MusicBrainz release
// track, transitively; it is exact when all of it has the same audio, which
// files whose audio could not be hashed, or that have none, never show.
// Files without MusicBrainz IDs whose possibleKey matches form a possible
// group. It is never merged into a confirmed group, though its files may also
// be in one: it is left out only when all its files are one confirmed group
// already, since it would then add nothing to review. possibleKey is
// rules.PossibleKey applied to a file, "" for a file with nothing to compare;
// store cannot import rules. Confirmed groups come first, each kind in the
// order of its first file.
func (s *Store) Groups(possibleKey func(File) string) ([]Group, error) {
	groups, err := s.groupKeys(possibleKey)
	if err != nil {
		return nil, err
	}
	return groups, s.fillGroups(groups, fileColumns)
}

// briefColumns are fileColumns without tags_json, which is by far the
// largest and which only a row from before has_art existed is still read for.
const briefColumns = `f.id, f.root_id, r.kind, f.rel_path, r.path, f.format,
	f.artist, f.album_artist, f.album, f.title, f.track, f.track_total, f.disc, f.disc_total, f.year,
	f.mb_release_id, f.mb_release_track_id, f.mb_recording_id, f.manual,
	f.lossless, f.bitrate, f.sample_rate, f.bit_depth, f.channels, f.length_ms,
	f.audio_sum, f.size, f.mtime, f.indexed_at, CASE WHEN f.has_art IS NULL THEN f.tags_json ELSE '' END, f.read_error, f.has_art`

// fillGroups reads the given columns of the files of groups, which
// groupKeys made with only their IDs.
func (s *Store) fillGroups(groups []Group, columns string) error {
	var ids []int64
	for _, g := range groups {
		ids = append(ids, g.IDs()...)
		for _, f := range g.AlsoOn {
			ids = append(ids, f.ID)
		}
	}
	if len(ids) == 0 {
		return nil
	}
	data, err := json.Marshal(ids)
	if err != nil {
		return err
	}
	files, err := s.queryFiles(`SELECT `+columns+` FROM files f JOIN roots r ON r.id = f.root_id
		WHERE f.id IN (SELECT value FROM json_each(?))`, string(data))
	if err != nil {
		return fmt.Errorf("reading duplicate groups: %w", err)
	}
	byID := make(map[int64]File, len(files))
	for _, f := range files {
		byID[f.ID] = f
	}
	fill := func(fs []File) {
		for i := range fs {
			// A file deleted since the groups were found stays as its ID
			// alone.
			if f, ok := byID[fs[i].ID]; ok {
				fs[i] = f
			}
		}
	}
	for _, g := range groups {
		fill(g.Files)
		fill(g.AlsoOn)
	}
	return nil
}

// keyRow is what grouping goes by of one file: its audio hash, its
// MusicBrainz IDs and its possible key. Grouping looks at every file of the
// library, so it reads no more of each than this, and never tags_json.
type keyRow struct {
	id                             int64
	sum, release, track, recording string
	possible                       string // "" for a file with MusicBrainz IDs, or with nothing to compare
}

// groupKeys finds the groups Groups lists. Their files hold only their IDs:
// fillGroups reads the rest. possibleKey sees a file's ID, artist, album
// artist, album, title and track number.
func (s *Store) groupKeys(possibleKey func(File) string) ([]Group, error) {
	rows, err := s.db.Query(`SELECT f.id, COALESCE(f.audio_sum, ''), f.mb_release_id, f.mb_release_track_id, f.mb_recording_id,
		f.artist, f.album_artist, f.album, f.title, f.track
		FROM files f JOIN roots r ON r.id = f.root_id
		WHERE r.kind IN (?, ?, ?) ORDER BY f.id`,
		string(RootLibrary), string(RootUntagged), string(RootOther))
	if err != nil {
		return nil, fmt.Errorf("finding duplicate groups: %w", err)
	}
	defer rows.Close()
	var files []keyRow
	// One row to scan every file into: a row of its own for each would be
	// most of what finding the groups allocates.
	var k keyRow
	var f File
	for rows.Next() {
		if err := rows.Scan(&k.id, &k.sum, &k.release, &k.track, &k.recording,
			&f.Artist, &f.AlbumArtist, &f.Album, &f.Title, &f.Track); err != nil {
			return nil, fmt.Errorf("finding duplicate groups: %w", err)
		}
		k.possible = ""
		if k.release == "" && k.track == "" && k.recording == "" {
			f.ID = k.id
			k.possible = possibleKey(f)
		}
		files = append(files, k)
	}
	if err := rows.Err(); err != nil {
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
		if f.sum != "" && f.sum != emptyAudioSum {
			join(bySum, f.sum, i)
		}
		if f.track != "" {
			join(byTrack, f.track, i)
		}
		if f.recording != "" {
			byRecording[f.recording] = append(byRecording[f.recording], i)
		}
	}

	var groups []Group
	group := make([]int, len(files)) // index into groups + 1; 0 for none
	size := make([]int, len(files))  // by root, how many files it joins
	for i := range files {
		size[find(i)]++
	}
	members := make(map[int][]int) // root -> files, in index order
	var roots []int
	for i := range files {
		r := find(i)
		if size[r] < 2 {
			continue
		}
		if len(members[r]) == 0 {
			roots = append(roots, r)
		}
		members[r] = append(members[r], i)
	}
	for _, r := range roots {
		m := members[r]
		g := Group{Kind: GroupExact}
		if sum := files[m[0]].sum; sum == "" || sum == emptyAudioSum {
			// Joined by release track alone: no hash says the audio is the same.
			g.Kind = GroupReleaseTrack
		}
		for _, i := range m {
			g.Files = append(g.Files, File{ID: files[i].id})
			if files[i].sum != files[m[0]].sum {
				g.Kind = GroupReleaseTrack
			}
			group[i] = len(groups) + 1
		}
		g.AlsoOn = alsoOn(files, m, byRecording, group, len(groups)+1)
		groups = append(groups, g)
	}

	// Most keys are one file's alone: only those of two or more get a list.
	firstWith := make(map[string]int)
	byKey := make(map[string][]int)
	var keys []string
	for i, f := range files {
		if f.possible == "" {
			continue
		}
		first, seen := firstWith[f.possible]
		if !seen {
			firstWith[f.possible] = i
			continue
		}
		if len(byKey[f.possible]) == 0 {
			keys = append(keys, f.possible)
			byKey[f.possible] = []int{first}
		}
		byKey[f.possible] = append(byKey[f.possible], i)
	}
	slices.SortFunc(keys, func(a, b string) int { return firstWith[a] - firstWith[b] })
	for _, key := range keys {
		m := byKey[key]
		if len(m) < 2 || sameGroup(group, m) {
			continue
		}
		g := Group{Kind: GroupPossible}
		for _, i := range m {
			g.Files = append(g.Files, File{ID: files[i].id})
		}
		groups = append(groups, g)
	}
	return groups, nil
}

// alsoOn lists the files outside group g that share a recording with one of
// its members m but sit on a release none of them is on.
func alsoOn(files []keyRow, m []int, byRecording map[string][]int, group []int, g int) []File {
	releases := make(map[string]bool)
	for _, i := range m {
		releases[files[i].release] = true
	}
	seen := make(map[int]bool)
	var also []int
	for _, i := range m {
		for _, j := range byRecording[files[i].recording] {
			if group[j] == g || seen[j] || releases[files[j].release] {
				continue
			}
			seen[j] = true
			also = append(also, j)
		}
	}
	slices.Sort(also)
	var out []File
	for _, j := range also {
		out = append(out, File{ID: files[j].id})
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

// GroupsNeedingReview counts the duplicate groups Groups lists that no
// review covers: new groups, and reviewed ones that have gained a member.
// possibleKey is as for Groups.
func (s *Store) GroupsNeedingReview(possibleKey func(File) string) (int, error) {
	groups, err := s.groupsToReview(possibleKey)
	return len(groups), err
}

// GroupsToReview lists the duplicate groups Groups lists that no review
// covers, in their order there. possibleKey is as for Groups. The files come
// without TagsJSON, which nothing that shows or resolves a group reads.
func (s *Store) GroupsToReview(possibleKey func(File) string) ([]Group, error) {
	groups, err := s.groupsToReview(possibleKey)
	if err != nil {
		return nil, err
	}
	return groups, s.fillGroups(groups, briefColumns)
}

// groupsToReview is GroupsToReview with only the ID of each file.
func (s *Store) groupsToReview(possibleKey func(File) string) ([]Group, error) {
	groups, err := s.groupKeys(possibleKey)
	if err != nil || len(groups) == 0 {
		return nil, err
	}
	reviews, err := s.reviewIndex()
	if err != nil {
		return nil, err
	}
	var waiting []Group
	for _, g := range groups {
		if !reviews.covers(g.Kind, g.IDs()) {
			waiting = append(waiting, g)
		}
	}
	return waiting, nil
}

// reviewIndex is every review's members, read once to answer for many groups
// what Reviewed answers for one.
type reviewIndex struct {
	members [][]int64 // per review, sorted
	// with lists, by kind and file ID, the reviews of that kind the file is
	// a member of.
	with map[GroupKind]map[int64][]int
}

// reviewIndex reads the members of every review.
func (s *Store) reviewIndex() (*reviewIndex, error) {
	rows, err := s.db.Query(`SELECT kind, members FROM dup_reviews ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("reading duplicate reviews: %w", err)
	}
	defer rows.Close()
	x := &reviewIndex{with: make(map[GroupKind]map[int64][]int)}
	for rows.Next() {
		var kind, data string
		if err := rows.Scan(&kind, &data); err != nil {
			return nil, fmt.Errorf("reading duplicate reviews: %w", err)
		}
		var members []int64
		if err := json.Unmarshal([]byte(data), &members); err != nil {
			return nil, fmt.Errorf("reading duplicate reviews: %w", err)
		}
		slices.Sort(members)
		with := x.with[GroupKind(kind)]
		if with == nil {
			with = make(map[int64][]int)
			x.with[GroupKind(kind)] = with
		}
		for _, id := range members {
			with[id] = append(with[id], len(x.members))
		}
		x.members = append(x.members, members)
	}
	return x, rows.Err()
}

// covers reports whether some review of kind holds every one of members.
func (x *reviewIndex) covers(kind GroupKind, members []int64) bool {
	if len(members) == 0 {
		return false
	}
	for _, r := range x.with[kind][members[0]] {
		all := true
		for _, id := range members[1:] {
			if _, ok := slices.BinarySearch(x.members[r], id); !ok {
				all = false
				break
			}
		}
		if all {
			return true
		}
	}
	return false
}

// WithdrawReviews takes back the decisions the run runID carried out, so
// that their groups wait for review again, and reports how many there were.
func (s *Store) WithdrawReviews(runID int64) (int64, error) {
	res, err := s.db.Exec(`DELETE FROM dup_reviews WHERE run_id = ?`, runID)
	if err != nil {
		return 0, fmt.Errorf("withdrawing the reviews of run %d: %w", runID, err)
	}
	return res.RowsAffected()
}
