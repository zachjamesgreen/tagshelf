package tui

import (
	"cmp"
	"context"
	"errors"
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
	// Identify listens to each of files and says what AcoustID makes of it:
	// each is called with the index of a file and what was heard, or what
	// stood in the way, as the answers come, in no order and never twice at
	// once. It returns when every file is done, when each returns false or
	// when ctx is done. An answer on record that is not old is given without
	// asking; with again, AcoustID is asked whatever is on record.
	Identify(ctx context.Context, files []store.File, again bool, each func(i int, heard Heard, err error) bool)
}

// soundDown reports whether err is of the service, the key or the network
// and not of the file: the files that follow would fail the same way.
func soundDown(err error) bool {
	for _, down := range []error{acoustid.ErrNoKey, acoustid.ErrKey, acoustid.ErrBusy, acoustid.ErrTimeout, acoustid.ErrUnreachable} {
		if errors.Is(err, down) {
			return true
		}
	}
	return false
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
		sound.Identify(ctx, []store.File{f}, again, func(_ int, heard Heard, err error) bool {
			msg.heard, msg.err = heard, err
			return true
		})
		if msg.err = cmp.Or(msg.err, ctx.Err()); msg.err != nil {
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
	// Only what MusicBrainz gave is listed by sound: AcoustID may name more
	// recordings than are asked for.
	hits, _ := soundHits(msg.heard.Results)
	l.sound = make(map[string]soundHit, len(msg.recs))
	for _, rec := range msg.recs {
		if hit, ok := hits[strings.ToLower(rec.ID)]; ok {
			l.sound[strings.ToLower(rec.ID)] = hit
		}
	}
	l.heard, l.heardAt, l.heardFromRecord = msg.recs, msg.heard.Asked, msg.heard.FromRecord
	old := ""
	if msg.heard.FromRecord {
		old = onRecordNote(time.Since(msg.heard.Asked))
	}
	switch {
	case len(msg.heard.Results) == 0:
		l.problem = "AcoustID does not know this audio" + old
	case len(msg.recs) == 0:
		l.problem = "AcoustID knows this audio, but no MusicBrainz recording is linked to it" + old
	}
	l.rank()
	if len(msg.recs) == 0 {
		// What was listed by sound before is gone.
		if l.step = l.back; l.step == stepPick && len(l.matches) == 0 {
			l.step = stepQuery
		}
		return
	}
	l.cursor = 0
	l.shown = min(max(l.shown, matchPage, l.bySound), len(l.matches))
	l.step = stepPick
}

// rank makes the list of matches: what AcoustID heard first, best first by
// its score, and then what the search found that is not among those. It is
// the one place the list is made, and keeps what is shown and the cursor
// within it.
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
	l.shown = min(l.shown, len(l.matches))
	l.cursor = max(min(l.cursor, l.shown-1), 0)
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
	if c.Recording == nil {
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
		line += onRecordNote(now.Sub(l.heardAt))
	}
	return line
}

// onRecordNote says, after what AcoustID's answer gave, that the answer was
// on record for d and how to get a new one.
func onRecordNote(d time.Duration) string {
	return "; its answer is " + age(d) + " old, ctrl+s asks again"
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
