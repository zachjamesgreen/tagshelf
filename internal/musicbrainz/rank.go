package musicbrainz

import (
	"cmp"
	"math"
	"slices"
	"strings"
	"time"
	"unicode"
)

// Picard's weights for comparing a file with a track (file.py).
const (
	weightTitle      = 13
	weightLength     = 10
	weightAlbum      = 5
	weightArtist     = 4
	weightDate       = 4
	weightTrackTotal = 4
	weightTrack      = 4
)

// lengthTolerance is the length difference at which the length score reaches
// zero, as in Picard's matching.
const lengthTolerance = 30 * time.Second

// Candidate is one (recording, release) pair from a recording search, scored
// against the query. Release is nil for a recording on no release.
type Candidate struct {
	// Score is the match from 0 to 100.
	Score     int
	Recording *Recording
	Release   *Release
	// InLibrary reports that the release is already in the library.
	InLibrary bool

	// Where the recording sits on the release; zero when unknown.
	Track      int // position on the medium
	TrackTotal int // tracks on the medium
	Disc       int // medium position
	// LengthDelta is the recording's length minus the file's; meaningful only
	// when HasLengthDelta is set.
	LengthDelta    time.Duration
	HasLengthDelta bool

	// Format is the medium's format, such as "CD" or "Digital Media".
	Format string
}

// Title is the recording title.
func (c Candidate) Title() string { return c.Recording.Title }

// Artist is the recording's credited artist.
func (c Candidate) Artist() string { return Credit(c.Recording.ArtistCredit) }

// Rank pairs every recording with each of its releases, scores the pairs
// against q with Picard's weights and sorts them best first. Ties on the
// displayed score prefer official Album releases, then releases already in the
// library (inLibrary reports a release ID as such; nil means none are), then
// the service's own order. Rank does not modify recs.
func Rank(q Query, recs []Recording, inLibrary func(releaseID string) bool) []Candidate {
	var out []Candidate
	for i := range recs {
		rec := &recs[i]
		if len(rec.Releases) == 0 {
			out = append(out, candidate(q, rec, nil, inLibrary))
			continue
		}
		for j := range rec.Releases {
			out = append(out, candidate(q, rec, &rec.Releases[j], inLibrary))
		}
	}
	slices.SortStableFunc(out, func(a, b Candidate) int {
		if c := cmp.Compare(b.Score, a.Score); c != 0 {
			return c
		}
		if c := compareBool(officialAlbum(b), officialAlbum(a)); c != 0 {
			return c
		}
		return compareBool(b.InLibrary, a.InLibrary)
	})
	return out
}

func officialAlbum(c Candidate) bool { return c.Release != nil && c.Release.OfficialAlbum() }

func compareBool(a, b bool) int {
	switch {
	case a == b:
		return 0
	case a:
		return 1
	default:
		return -1
	}
}

func candidate(q Query, rec *Recording, rel *Release, inLibrary func(string) bool) Candidate {
	c := Candidate{Recording: rec, Release: rel}
	if rel != nil {
		c.InLibrary = inLibrary != nil && rel.ID != "" && inLibrary(rel.ID)
		if len(rel.Media) > 0 {
			m := rel.Media[0]
			c.Disc = m.Position
			c.TrackTotal = m.TrackCount
			c.Format = m.Format
			if len(m.Track) > 0 {
				c.Track = m.TrackOffset + 1
			}
		}
	}

	recLen := time.Duration(rec.Length) * time.Millisecond
	if rel != nil && len(rel.Media) > 0 && len(rel.Media[0].Track) > 0 && rel.Media[0].Track[0].Length > 0 {
		recLen = time.Duration(rel.Media[0].Track[0].Length) * time.Millisecond
	}
	if q.Length > 0 && recLen > 0 {
		c.LengthDelta = recLen - q.Length
		c.HasLengthDelta = true
	}

	var total, weights float64
	part := func(score float64, weight int) {
		total += score * float64(weight)
		weights += float64(weight)
	}
	if q.Title != "" {
		part(similarity(q.Title, rec.Title), weightTitle)
	}
	if q.ISRC != "" {
		// It says which recording it is, as a title does.
		part(boolScore(slices.Contains(rec.ISRCs, q.ISRC)), weightTitle)
	}
	if q.Artist != "" {
		part(similarity(q.Artist, Credit(rec.ArtistCredit)), weightArtist)
	}
	if q.Length > 0 && recLen > 0 {
		d := c.LengthDelta.Abs()
		part(1-float64(min(d, lengthTolerance))/float64(lengthTolerance), weightLength)
	}
	if rel != nil {
		if q.Album != "" {
			part(similarity(q.Album, rel.Title), weightAlbum)
		}
		if q.Year > 0 && rel.Year() > 0 {
			part(boolScore(q.Year == rel.Year()), weightDate)
		}
		if q.TrackTotal > 0 && c.TrackTotal > 0 {
			part(boolScore(q.TrackTotal == c.TrackTotal), weightTrackTotal)
		}
		if q.Track > 0 && c.Track > 0 {
			part(boolScore(q.Track == c.Track), weightTrack)
		}
	}
	if weights > 0 {
		c.Score = int(math.Round(100 * total / weights))
	}
	return c
}

func boolScore(b bool) float64 {
	if b {
		return 1
	}
	return 0
}

// similarity compares two strings from 0 (nothing alike) to 1 (equal after
// normalizing case, punctuation and spacing), by edit distance.
func similarity(a, b string) float64 {
	ra, rb := []rune(normalize(a)), []rune(normalize(b))
	longest := max(len(ra), len(rb))
	if longest == 0 {
		return 1
	}
	return 1 - float64(levenshtein(ra, rb))/float64(longest)
}

// normalize lower-cases s, turns punctuation into spaces and collapses runs
// of spaces.
func normalize(s string) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return unicode.ToLower(r)
		}
		return ' '
	}, s)
	return strings.Join(strings.Fields(s), " ")
}

func levenshtein(a, b []rune) int {
	prev := make([]int, len(b)+1)
	cur := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		cur[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev, cur = cur, prev
	}
	return prev[len(b)]
}
