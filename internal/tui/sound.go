package tui

import (
	"cmp"
	"context"
	"fmt"
	"math"
	"slices"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/zachjamesgreen/tagshelf/internal/acoustid"
	"github.com/zachjamesgreen/tagshelf/internal/musicbrainz"
	"github.com/zachjamesgreen/tagshelf/internal/store"
)

// Sound identifies files by their sound: it computes a file's AcoustID
// fingerprint and asks AcoustID which recordings it belongs to.
type Sound interface {
	// Identify listens to f and says what AcoustID makes of it. An answer on
	// record that is not old is given without asking; with again, AcoustID
	// is asked whatever is on record.
	Identify(ctx context.Context, f store.File, again bool) (Heard, error)
}

// Heard is what AcoustID made of a file.
type Heard struct {
	// Results are the tracks it matched, best first.
	Results []acoustid.Result
	// Asked is when AcoustID gave them. FromRecord is set when they were
	// taken from the record, and AcoustID was not asked now.
	Asked      time.Time
	FromRecord bool
}

// soundHit is what AcoustID says of one recording.
type soundHit struct {
	score    float64 // 0 to 1
	acoustID string
}

// maxHeard is how many of the recordings AcoustID names are looked up at
// MusicBrainz: its best.
const maxHeard = 25

// soundHits are the recordings the results name, each with the best score
// it is named with and the AcoustID of that result, and their IDs best
// first.
func soundHits(results []acoustid.Result) (hits map[string]soundHit, ids []string) {
	hits = map[string]soundHit{}
	for _, r := range results {
		for _, id := range r.Recordings {
			id = strings.ToLower(id)
			if hit, ok := hits[id]; ok && hit.score >= r.Score {
				continue
			} else if !ok {
				ids = append(ids, id)
			}
			hits[id] = soundHit{score: r.Score, acoustID: r.ID}
		}
	}
	slices.SortStableFunc(ids, func(a, b string) int { return cmp.Compare(hits[b].score, hits[a].score) })
	if len(ids) > maxHeard {
		ids = ids[:maxHeard]
	}
	return hits, ids
}

// percent writes a score of 0 to 1 for the score column of a match.
func percent(score float64) string {
	p := int(math.Round(score * 100))
	if p >= 100 {
		return "100"
	}
	return fmt.Sprintf("%d%%", max(p, 0))
}

// lookupHeardMsg is what identifying the lookup's file by its sound gave.
type lookupHeardMsg struct {
	seq       int
	heard     Heard
	recs      []musicbrainz.Recording
	inLibrary map[string]bool
	err       error
}

// listen identifies the lookup's file by its sound: AcoustID's matches join
// the list, first. A second time in the same lookup asks AcoustID again,
// whatever is on record.
func (m *Model) listen() tea.Cmd {
	l := m.look
	if m.sound == nil {
		l.problem = "Identifying by sound is not available here"
		return nil
	}
	again := l.sound != nil
	if l.query == (musicbrainz.Query{}) {
		// Not searched yet: the matches are ranked by what the form holds.
		l.query = l.formQuery()
	}
	l.back, l.step, l.problem = l.step, stepListening, ""
	ctx, seq := m.request()
	sound, mb, src, f, known := m.sound, m.mb, m.src, l.file, l.inLibrary
	return func() tea.Msg {
		msg := lookupHeardMsg{seq: seq, inLibrary: known}
		if msg.inLibrary == nil {
			msg.inLibrary, _ = src.LibraryReleases()
		}
		if msg.heard, msg.err = sound.Identify(ctx, f, again); msg.err != nil {
			return msg
		}
		if _, ids := soundHits(msg.heard.Results); len(ids) > 0 {
			msg.recs, msg.err = mb.RecordingsByID(ctx, ids)
		}
		return msg
	}
}

// formQuery is the query the form holds, with a length it cannot read left
// out.
func (l *lookup) formQuery() musicbrainz.Query {
	q := l.base
	q.Title, q.Artist, q.Album = strings.TrimSpace(l.title), strings.TrimSpace(l.artist), strings.TrimSpace(l.album)
	if d, err := parseLength(l.length); err == nil {
		q.Length = d
	}
	return q
}

// lookupHeard takes what AcoustID made of the file into the list of matches.
func (m *Model) lookupHeard(msg lookupHeardMsg) {
	l := m.look
	if l == nil || msg.seq != l.seq || l.step != stepListening {
		return
	}
	l.cancel = nil
	if msg.err != nil {
		l.problem, l.step = msg.err.Error(), l.back
		return
	}
	l.inLibrary = msg.inLibrary
	l.sound, _ = soundHits(msg.heard.Results)
	l.heard, l.heardAt, l.heardFromRecord = msg.recs, msg.heard.Asked, msg.heard.FromRecord
	switch {
	case len(msg.heard.Results) == 0:
		l.problem = "AcoustID does not know this audio"
	case len(msg.recs) == 0:
		l.problem = "AcoustID knows this audio, but no MusicBrainz recording is linked to it"
	}
	l.rank()
	if len(msg.recs) == 0 {
		l.step = l.back
		return
	}
	l.cursor = 0
	l.shown = max(l.shown, min(max(matchPage, l.bySound), len(l.matches)))
	l.step = stepPick
}

// rank makes the list of matches: what AcoustID heard first, best first by
// its score, and then what the search found that AcoustID did not name.
func (l *lookup) rank() {
	in := func(id string) bool { return l.inLibrary[id] }
	score := func(c musicbrainz.Candidate) float64 {
		hit, _ := l.heardAs(c)
		return hit.score
	}
	heard := musicbrainz.Rank(l.query, l.heard, in)
	slices.SortStableFunc(heard, func(a, b musicbrainz.Candidate) int { return cmp.Compare(score(b), score(a)) })
	l.bySound = len(heard)
	l.matches = heard
	for _, c := range musicbrainz.Rank(l.query, l.recs, in) {
		if _, named := l.heardAs(c); named {
			continue
		}
		l.matches = append(l.matches, c)
	}
}

// hit is what AcoustID says of the match at i, when it is one it heard.
func (l *lookup) hit(i int) (soundHit, bool) {
	if i < 0 || i >= l.bySound || i >= len(l.matches) {
		return soundHit{}, false
	}
	return l.heardAs(l.matches[i])
}

// heardAs is what AcoustID says of the recording of the match c, when it is
// among those it heard.
func (l *lookup) heardAs(c musicbrainz.Candidate) (soundHit, bool) {
	if c.Recording == nil || len(l.heard) == 0 {
		return soundHit{}, false
	}
	hit, ok := l.sound[strings.ToLower(c.Recording.ID)]
	return hit, ok
}

// heardLine says, under the matches, that some are by sound and how old
// AcoustID's answer is.
func (l *lookup) heardLine(now time.Time) string {
	if l.bySound == 0 {
		return ""
	}
	line := "by sound first, with AcoustID's score as %"
	if l.heardFromRecord {
		line += "; its answer is " + age(now.Sub(l.heardAt)) + " old, ctrl+s asks again"
	}
	return line
}

// age writes how long ago something was, in days or less.
func age(d time.Duration) string {
	switch days := int(d.Hours() / 24); {
	case days >= 2:
		return fmt.Sprintf("%d days", days)
	case days == 1:
		return "a day"
	case d >= 2*time.Hour:
		return fmt.Sprintf("%d hours", int(d.Hours()))
	default:
		return "under two hours"
	}
}
