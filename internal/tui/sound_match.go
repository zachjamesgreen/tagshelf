package tui

import (
	"cmp"
	"fmt"
	"slices"
	"strings"
	"sync/atomic"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/zachjamesgreen/tagshelf/internal/musicbrainz"
	"github.com/zachjamesgreen/tagshelf/internal/store"
	"github.com/zachjamesgreen/tagshelf/internal/tagwrite"
)

// maxHeardEach is how many of the recordings AcoustID names for one file of
// a release match are looked up at MusicBrainz: its best. A file is put on a
// track by any of those AcoustID named.
const maxHeardEach = 5

// matchHeardMsg is what identifying a release match's files by their sound
// gave.
type matchHeardMsg struct {
	seq int
	// heard is, by file, what AcoustID says of each recording it named.
	heard     map[int]map[string]soundHit
	recs      []musicbrainz.Recording
	inLibrary map[string]bool
	// failed is how many files could not be listened to or asked about, and
	// why the first of them.
	failed int
	why    error
	// fromRecord is how many of the answers were on record, and oldest when
	// the oldest of those was given.
	fromRecord int
	oldest     time.Time
	err        error
}

// matchListen identifies every file of the release match by its sound. The
// releases that hold their recordings join the list, first, ranked by how
// many of the files each holds.
func (m *Model) matchListen() tea.Cmd {
	x := m.match
	if m.sound == nil {
		x.problem = "Identifying by sound is not available here"
		return nil
	}
	if n := len(x.files); n > musicbrainz.MaxMatchFiles {
		x.problem = fmt.Sprintf("%d files are too many for one release (at most %d): select fewer", n, musicbrainz.MaxMatchFiles)
		return nil
	}
	// A second time asks AcoustID again, whatever is on record. After files
	// that failed it is those that are tried again: the answers the others
	// got are on record.
	again := x.heard != nil && x.failed == 0
	x.back, x.step, x.problem = x.step, matchListening, ""
	x.listened = new(atomic.Int32)
	ctx, seq := m.matchRequest()
	sound, mb, src, done := m.sound, m.mb, m.src, x.listened
	// A list of its own: the match's is written anew when the index changes.
	files := make([]store.File, len(x.files))
	for i, f := range x.files {
		files[i] = f.File
	}
	return func() tea.Msg {
		msg := matchHeardMsg{seq: seq, heard: map[int]map[string]soundHit{}}
		best := map[int][]string{}
		sound.Identify(ctx, files, again, func(i int, heard Heard, err error) bool {
			done.Add(1)
			switch {
			case err != nil && soundDown(err):
				// Nothing more will come of the files that follow.
				msg.err = err
				return false
			case err != nil:
				// One file that cannot be read does not stop the rest.
				if msg.failed++; msg.why == nil {
					msg.why = err
				}
				return true
			}
			if heard.FromRecord {
				if msg.fromRecord++; msg.oldest.IsZero() || heard.Asked.Before(msg.oldest) {
					msg.oldest = heard.Asked
				}
			}
			if hits, order := soundHits(heard.Results); len(order) > 0 {
				msg.heard[i] = hits
				best[i] = order[:min(len(order), maxHeardEach)]
			}
			return true
		})
		if msg.err = cmp.Or(msg.err, ctx.Err()); msg.err != nil {
			return msg
		}
		if msg.failed == len(files) {
			msg.err = msg.why
			return msg
		}
		var ids []string
		seen := map[string]bool{}
		for i := range files {
			for _, id := range best[i] {
				if !seen[id] {
					seen[id] = true
					ids = append(ids, id)
				}
			}
		}
		for chunk := range slices.Chunk(ids, musicbrainz.MaxByID) {
			recs, err := mb.RecordingsByID(ctx, chunk)
			if err != nil {
				msg.err = err
				return msg
			}
			msg.recs = append(msg.recs, recs...)
		}
		// A library that cannot be read only costs the ranking a tie-break.
		msg.inLibrary, _ = src.LibraryReleases()
		return msg
	}
}

// matchHeard takes what AcoustID made of the files into the list of
// releases.
func (m *Model) matchHeard(msg matchHeardMsg) {
	x := m.match
	if x == nil || msg.seq != x.seq || x.step != matchListening {
		return
	}
	x.cancel = nil
	x.step = x.back
	if msg.err != nil {
		x.problem = msg.err.Error()
		return
	}
	x.heard, x.failed = msg.heard, msg.failed
	x.heardOld = 0
	if msg.fromRecord > 0 && msg.failed == 0 {
		x.heardOld = max(time.Since(msg.oldest), time.Nanosecond)
	}
	x.bySound = soundReleases(msg.recs, msg.heard, len(x.files), msg.inLibrary)
	switch {
	case msg.failed > 0:
		x.problem = fmt.Sprintf("%s could not be listened to: %v", plural(int64(msg.failed), "file", "files"), msg.why)
	case len(msg.heard) == 0:
		x.problem = "AcoustID does not know the audio of these files" + x.onRecordNote()
	case len(x.bySound) == 0:
		x.problem = "AcoustID knows the audio, but no MusicBrainz release is linked to it" + x.onRecordNote()
	}
	x.merge()
	switch {
	case len(x.bySound) > 0:
		x.cursor = 0
		x.step = matchPick
	case len(x.releases) == 0 && x.step == matchPick:
		// What was listed by sound before is gone, and nothing was searched.
		x.step = matchQuery
	}
}

// soundReleases are the releases that hold recordings AcoustID named for
// the files, each with how many of the files it holds, the most first. The
// score of each is the share of all the files, in a hundred.
func soundReleases(recs []musicbrainz.Recording, heard map[int]map[string]soundHit, files int, inLibrary map[string]bool) []musicbrainz.ReleaseCandidate {
	type held struct {
		release *musicbrainz.Release
		files   map[int]bool
		order   int
	}
	by := map[string]*held{}
	for r := range recs {
		rec := &recs[r]
		id := strings.ToLower(rec.ID)
		for i, hits := range heard {
			if _, ok := hits[id]; !ok {
				continue
			}
			for j := range rec.Releases {
				rel := &rec.Releases[j]
				h := by[rel.ID]
				if h == nil {
					h = &held{release: rel, files: map[int]bool{}, order: len(by)}
					by[rel.ID] = h
				}
				h.files[i] = true
			}
		}
	}
	all := make([]*held, 0, len(by))
	for _, h := range by {
		all = append(all, h)
	}
	off := func(h *held) int {
		// How far the release's size is from the number of files.
		if h.release.TrackCount == 0 {
			return files
		}
		return max(h.release.TrackCount-files, files-h.release.TrackCount)
	}
	slices.SortFunc(all, func(a, b *held) int {
		return cmp.Or(
			cmp.Compare(len(b.files), len(a.files)),
			compareBools(b.release.OfficialAlbum(), a.release.OfficialAlbum()),
			compareBools(inLibrary[b.release.ID], inLibrary[a.release.ID]),
			cmp.Compare(off(a), off(b)),
			cmp.Compare(a.order, b.order),
		)
	})
	out := make([]musicbrainz.ReleaseCandidate, 0, len(all))
	for _, h := range all {
		out = append(out, musicbrainz.ReleaseCandidate{
			Score: len(h.files) * 100 / max(files, 1), Release: h.release, InLibrary: inLibrary[h.release.ID],
			Found: len(h.files), Searched: files,
		})
	}
	return out
}

func compareBools(a, b bool) int {
	switch {
	case a == b:
		return 0
	case a:
		return 1
	}
	return -1
}

// merge makes the list of releases: those found by sound first, and then
// what the search found that is not among them. It is the one place the list
// is made, and keeps the cursor within it.
func (x *match) merge() {
	x.releases = slices.Clone(x.bySound)
	named := map[string]bool{}
	for _, c := range x.bySound {
		named[c.Release.ID] = true
	}
	for _, c := range x.found {
		if !named[c.Release.ID] {
			x.releases = append(x.releases, c)
		}
	}
	x.cursor = max(min(x.cursor, len(x.releases)-1), 0)
}

// applySound puts each file AcoustID knows on the track of the release
// picked that holds a recording it named for the file, whatever the tags
// and names said. Where a file could go on several tracks, or several files
// on one, the file and track AcoustID is surest of go together first; of
// those it is as sure of, the first track and the first file do.
func (x *match) applySound() {
	if len(x.heard) == 0 || x.release == nil || x.mapping == nil {
		return
	}
	type claim struct {
		file  int
		track musicbrainz.TrackRef
		score float64
	}
	var claims []claim
	for _, ref := range x.release.Tracks() {
		t := x.release.Track(ref)
		if t.Recording == nil {
			continue
		}
		id := strings.ToLower(t.Recording.ID)
		for i := range x.files {
			if hit, ok := x.heard[i][id]; ok {
				claims = append(claims, claim{file: i, track: ref, score: hit.score})
			}
		}
	}
	slices.SortStableFunc(claims, func(a, b claim) int { return cmp.Compare(b.score, a.score) })
	on := map[int]musicbrainz.TrackRef{}
	for _, a := range x.mapping.Assignments {
		on[a.File] = a.Track
	}
	placed, taken := map[int]bool{}, map[musicbrainz.TrackRef]bool{}
	for _, c := range claims {
		if placed[c.file] || taken[c.track] {
			continue
		}
		placed[c.file], taken[c.track] = true, true
		if cur, ok := on[c.file]; !ok || cur != c.track {
			x.mapping.Reassign(c.file, c.track)
		}
	}
}

// acoustID is the AcoustID of the audio of the file of assignment a, when
// AcoustID named the recording of its track for it.
func (x *match) acoustID(a musicbrainz.Assignment) string {
	t := x.release.Track(a.Track)
	if t.Recording == nil {
		return ""
	}
	return x.heard[a.File][strings.ToLower(t.Recording.ID)].acoustID
}

// withAcoustID is values with the AcoustID of the file of assignment a.
func (x *match) withAcoustID(values tagwrite.Values, a musicbrainz.Assignment) tagwrite.Values {
	if id := x.acoustID(a); id != "" {
		values[tagwrite.AcoustID] = []string{id}
	}
	return values
}

// listeningLine says how far the listening has got.
func (x *match) listeningLine() string {
	n := 0
	if x.listened != nil {
		n = int(x.listened.Load())
	}
	return fmt.Sprintf("Listening to the files and asking AcoustID: %d of %d…", min(n+1, len(x.files)), len(x.files))
}

// heardNote says, over the releases, that some are by sound, and how old
// AcoustID's answers are when they were on record.
func (x *match) heardNote() string {
	if len(x.bySound) == 0 {
		return ""
	}
	if note := x.onRecordNote(); note != "" {
		// Said shortly, so that both fit the line.
		return "by sound first, by the share of the files held" + note
	}
	return "by sound first: the score is the share of the files a release holds"
}

// onRecordNote says, after what AcoustID's answers gave, that some were on
// record, how old the oldest is and how to get new ones.
func (x *match) onRecordNote() string {
	if x.heardOld == 0 {
		return ""
	}
	return "; answers up to " + age(x.heardOld) + " old, ctrl+s asks again"
}
