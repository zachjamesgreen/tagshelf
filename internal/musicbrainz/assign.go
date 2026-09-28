package musicbrainz

import (
	"math"
	"slices"
	"time"
)

// Length differences in a release match: up to lengthClose a file's length
// supports its track fully; past it the support weakens, and past lengthFar
// it is gone and the assignment is flagged.
const (
	lengthClose = 3 * time.Second
	lengthFar   = 10 * time.Second
)

// minAssignScore is the lowest pair score (0 to 1) Assign puts a file on a
// track with; a file that fits no track better stays unassigned.
const minAssignScore = 0.5

// weightDisc is the disc number's weight in a release match, the same as a
// track number's.
const weightDisc = weightTrack

// TrackRef locates a track of a looked-up release: Release.Media[Medium].Tracks[Index].
type TrackRef struct {
	Medium int
	Index  int
}

// Tracks lists every track of a looked-up release in order, across all media.
func (r *Release) Tracks() []TrackRef {
	var out []TrackRef
	for m := range r.Media {
		for i := range r.Media[m].Tracks {
			out = append(out, TrackRef{Medium: m, Index: i})
		}
	}
	return out
}

// Track returns the track ref points at.
func (r *Release) Track(ref TrackRef) *Track {
	return &r.Media[ref.Medium].Tracks[ref.Index]
}

// trackLength is the track's length, or its recording's when the track has
// none; zero when neither is known.
func trackLength(t *Track) time.Duration {
	ms := t.Length
	if ms == 0 && t.Recording != nil {
		ms = t.Recording.Length
	}
	return time.Duration(ms) * time.Millisecond
}

// LengthMatch grades how well a file's length fits its track.
type LengthMatch int

const (
	// LengthUnknown: the file or the track has no length.
	LengthUnknown LengthMatch = iota
	// LengthClose: within 3 s.
	LengthClose
	// LengthWeak: more than 3 s and up to 10 s apart.
	LengthWeak
	// LengthFlagged: more than 10 s apart; the TUI warns at confirm.
	LengthFlagged
)

// Assignment puts one file on one release track.
type Assignment struct {
	// File indexes the files of the release match.
	File  int
	Track TrackRef
	// Score is how well the file fits the track, from 0 to 100.
	Score int
	// Length grades the length difference; LengthDelta is the track's length
	// minus the file's, meaningful unless Length is LengthUnknown.
	Length      LengthMatch
	LengthDelta time.Duration
}

// Flagged reports that the assignment deserves a warning at confirm.
func (a Assignment) Flagged() bool { return a.Length == LengthFlagged }

// Mapping is a release match's assignment of files to the tracks of one
// looked-up release: each file on at most one track, each track with at most
// one file.
type Mapping struct {
	// Assignments are in track order.
	Assignments []Assignment
	// Unassigned lists the files on no track, in file order; they are not
	// written.
	Unassigned []int
	// EmptyTracks lists the tracks with no file, in track order.
	EmptyTracks []TrackRef

	release *Release
	files   []FileInfo
}

// Pair scores file f on the track ref of rel, as Assign and Reassign do.
func Pair(rel *Release, ref TrackRef, f FileInfo) Assignment {
	a := Assignment{Track: ref}
	s := pairScore(rel, ref, QueryFor(f), f.Disc)
	a.Score = int(math.Round(100 * s.score))
	a.Length, a.LengthDelta = s.length, s.delta
	return a
}

type pairResult struct {
	score  float64
	length LengthMatch
	delta  time.Duration
}

// pairScore scores query q (with disc number disc, 0 when unknown) against a
// release track by title similarity, length and existing track and disc
// numbers, with Picard's weights (title 13, length 10, track number 4) and
// the disc number weighed as the track number.
func pairScore(rel *Release, ref TrackRef, q Query, disc int) pairResult {
	medium := &rel.Media[ref.Medium]
	t := &medium.Tracks[ref.Index]
	var res pairResult
	var total, weights float64
	part := func(score float64, weight int) {
		total += score * float64(weight)
		weights += float64(weight)
	}
	if q.Title != "" {
		title := t.Title
		if title == "" && t.Recording != nil {
			title = t.Recording.Title
		}
		part(titleScore(q.Title, title), weightTitle)
	}
	if tl := trackLength(t); q.Length > 0 && tl > 0 {
		res.delta = tl - q.Length
		d := res.delta.Abs()
		switch {
		case d <= lengthClose:
			res.length = LengthClose
			part(1, weightLength)
		case d <= lengthFar:
			res.length = LengthWeak
			part(float64(lengthFar-d)/float64(lengthFar-lengthClose), weightLength)
		default:
			res.length = LengthFlagged
			part(0, weightLength)
		}
	}
	if q.Track > 0 {
		match := t.Position == q.Track
		// Numbered straight through the release, without disc numbers.
		if !match && disc == 0 && len(rel.Media) > 1 {
			match = absolutePosition(rel, ref) == q.Track
		}
		part(boolScore(match), weightTrack)
	}
	if disc > 0 {
		part(boolScore(medium.Position == disc), weightDisc)
	}
	if weights > 0 {
		res.score = total / weights
	}
	return res
}

// titleScore compares a file's title with a track's for assignment. Unrelated
// titles are still about a third similar by edit distance, enough for a close
// length to pull a file onto the wrong track, so similarity up to one half
// scores 0 and the rest is stretched over 0 to 1.
func titleScore(a, b string) float64 {
	return max(0, 2*similarity(a, b)-1)
}

// absolutePosition is the track's 1-based position counting every track of
// the media before it.
func absolutePosition(rel *Release, ref TrackRef) int {
	n := ref.Index + 1
	for m := range ref.Medium {
		n += len(rel.Media[m].Tracks)
	}
	return n
}

// Assign maps files one-to-one onto the tracks of a looked-up release, across
// all media, maximizing the total pair score (see Pair). A file whose best
// fit scores below one half stays unassigned, as does every file beyond the
// release's track count; a partial selection leaves tracks empty.
func Assign(rel *Release, files []FileInfo) *Mapping {
	tracks := rel.Tracks()
	n := max(len(files), len(tracks))
	// Cost matrix for a minimum-cost assignment: a pair below the threshold
	// costs as much as leaving the file unassigned (padding rows and columns).
	cost := make([][]float64, n)
	for i := range cost {
		cost[i] = make([]float64, n)
	}
	scores := make([][]float64, len(files))
	for i, f := range files {
		q := QueryFor(f)
		scores[i] = make([]float64, len(tracks))
		for j, ref := range tracks {
			s := pairScore(rel, ref, q, f.Disc).score
			scores[i][j] = s
			if s >= minAssignScore {
				cost[i][j] = -s
			}
		}
	}

	m := &Mapping{release: rel, files: files}
	onTrack := make([]int, len(tracks))
	for j := range onTrack {
		onTrack[j] = -1
	}
	for i, j := range hungarian(cost) {
		if i < len(files) && j < len(tracks) && scores[i][j] >= minAssignScore {
			onTrack[j] = i
		}
	}
	m.rebuild(tracks, onTrack)
	return m
}

// Reassign puts file on track ref. A file already on that track becomes
// unassigned, and the file leaves any track it was on.
func (m *Mapping) Reassign(file int, ref TrackRef) {
	tracks, onTrack := m.state()
	for j := range onTrack {
		if onTrack[j] == file {
			onTrack[j] = -1
		}
	}
	if j := slices.Index(tracks, ref); j >= 0 {
		onTrack[j] = file
	}
	m.rebuild(tracks, onTrack)
}

// Unassign takes file off its track.
func (m *Mapping) Unassign(file int) {
	tracks, onTrack := m.state()
	for j := range onTrack {
		if onTrack[j] == file {
			onTrack[j] = -1
		}
	}
	m.rebuild(tracks, onTrack)
}

// Flagged lists the assignments that deserve a warning at confirm.
func (m *Mapping) Flagged() []Assignment {
	var out []Assignment
	for _, a := range m.Assignments {
		if a.Flagged() {
			out = append(out, a)
		}
	}
	return out
}

// state is the release's tracks and, per track, the file on it or -1.
func (m *Mapping) state() ([]TrackRef, []int) {
	tracks := m.release.Tracks()
	onTrack := make([]int, len(tracks))
	for j := range onTrack {
		onTrack[j] = -1
	}
	for _, a := range m.Assignments {
		if j := slices.Index(tracks, a.Track); j >= 0 {
			onTrack[j] = a.File
		}
	}
	return tracks, onTrack
}

// rebuild fills the mapping's lists from the file on each track.
func (m *Mapping) rebuild(tracks []TrackRef, onTrack []int) {
	m.Assignments, m.Unassigned, m.EmptyTracks = nil, nil, nil
	assigned := make([]bool, len(m.files))
	for j, ref := range tracks {
		i := onTrack[j]
		if i < 0 || i >= len(m.files) {
			m.EmptyTracks = append(m.EmptyTracks, ref)
			continue
		}
		assigned[i] = true
		a := Pair(m.release, ref, m.files[i])
		a.File = i
		m.Assignments = append(m.Assignments, a)
	}
	for i, ok := range assigned {
		if !ok {
			m.Unassigned = append(m.Unassigned, i)
		}
	}
}

// hungarian solves the square assignment problem for cost, minimizing the
// total, and returns the column of each row (Kuhn-Munkres with potentials,
// O(n³)).
func hungarian(cost [][]float64) []int {
	n := len(cost)
	if n == 0 {
		return nil
	}
	inf := math.Inf(1)
	u := make([]float64, n+1)
	v := make([]float64, n+1)
	p := make([]int, n+1) // p[j]: row matched to column j (1-based), 0 for none
	way := make([]int, n+1)
	for i := 1; i <= n; i++ {
		p[0] = i
		j0 := 0
		minv := make([]float64, n+1)
		used := make([]bool, n+1)
		for j := range minv {
			minv[j] = inf
		}
		for {
			used[j0] = true
			i0, delta, j1 := p[j0], inf, 0
			for j := 1; j <= n; j++ {
				if used[j] {
					continue
				}
				cur := cost[i0-1][j-1] - u[i0] - v[j]
				if cur < minv[j] {
					minv[j], way[j] = cur, j0
				}
				if minv[j] < delta {
					delta, j1 = minv[j], j
				}
			}
			for j := 0; j <= n; j++ {
				if used[j] {
					u[p[j]] += delta
					v[j] -= delta
				} else {
					minv[j] -= delta
				}
			}
			j0 = j1
			if p[j0] == 0 {
				break
			}
		}
		for j0 != 0 {
			j1 := way[j0]
			p[j0] = p[j1]
			j0 = j1
		}
	}
	rowCol := make([]int, n)
	for j := 1; j <= n; j++ {
		if p[j] > 0 {
			rowCol[p[j]-1] = j - 1
		}
	}
	return rowCol
}
