package tui

import (
	"cmp"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync/atomic"

	tea "charm.land/bubbletea/v2"

	"github.com/zachjamesgreen/tagshelf/internal/acoustid"
	"github.com/zachjamesgreen/tagshelf/internal/musicbrainz"
	"github.com/zachjamesgreen/tagshelf/internal/tagwrite"
)

// maxHeardEach is how many of the recordings AcoustID names for one file of
// a release match are looked up: its best.
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
	err    error
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
	again := x.heard != nil
	x.back, x.step, x.problem = x.step, matchListening, ""
	x.listened = new(atomic.Int32)
	ctx, seq := m.matchRequest()
	sound, mb, src, files, done := m.sound, m.mb, m.src, x.files, x.listened
	return func() tea.Msg {
		msg := matchHeardMsg{seq: seq, heard: map[int]map[string]soundHit{}}
		var ids []string
		seen := map[string]bool{}
		for i, f := range files {
			heard, err := sound.Identify(ctx, f.File, again)
			done.Add(1)
			switch {
			case ctx.Err() != nil:
				msg.err = ctx.Err()
				return msg
			case errors.Is(err, acoustid.ErrNoKey), errors.Is(err, acoustid.ErrKey):
				// Nothing more will come of the files that follow.
				msg.err = err
				return msg
			case err != nil:
				// One file that cannot be read does not stop the rest.
				if msg.failed++; msg.why == nil {
					msg.why = err
				}
				continue
			}
			hits, order := soundHits(heard.Results)
			if len(order) > maxHeardEach {
				order = order[:maxHeardEach]
			}
			if len(order) == 0 {
				continue
			}
			msg.heard[i] = map[string]soundHit{}
			for _, id := range order {
				msg.heard[i][id] = hits[id]
				if !seen[id] {
					seen[id] = true
					ids = append(ids, id)
				}
			}
		}
		if msg.failed == len(files) {
			msg.err = msg.why
			return msg
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
	x.heard = msg.heard
	x.bySound = soundReleases(msg.recs, msg.heard, len(x.files), msg.inLibrary)
	switch {
	case len(msg.heard) == 0:
		x.problem = "AcoustID does not know the audio of these files"
	case len(x.bySound) == 0:
		x.problem = "AcoustID knows the audio, but no MusicBrainz release is linked to it"
	case msg.failed > 0:
		x.problem = fmt.Sprintf("%s could not be listened to: %v", plural(int64(msg.failed), "file", "files"), msg.why)
	}
	x.merge()
	if len(x.bySound) > 0 {
		x.cursor = 0
		x.step = matchPick
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
// what the search found that is not among them.
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
}

// applySound puts each file AcoustID knows on the track of the release
// picked that holds a recording it named for the file, whatever the tags
// and names said. A track two files claim goes to the first.
func (x *match) applySound() {
	if len(x.heard) == 0 || x.release == nil || x.mapping == nil {
		return
	}
	on := map[int]musicbrainz.TrackRef{}
	for _, a := range x.mapping.Assignments {
		on[a.File] = a.Track
	}
	taken := map[musicbrainz.TrackRef]bool{}
	for i := range x.files {
		hits := x.heard[i]
		if len(hits) == 0 {
			continue
		}
		for _, ref := range x.release.Tracks() {
			t := x.release.Track(ref)
			if t.Recording == nil || taken[ref] {
				continue
			}
			if _, ok := hits[strings.ToLower(t.Recording.ID)]; !ok {
				continue
			}
			taken[ref] = true
			if cur, ok := on[i]; !ok || cur != ref {
				x.mapping.Reassign(i, ref)
			}
			break
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

// heardNote says, over the releases, that some are by sound.
func (x *match) heardNote() string {
	if len(x.bySound) == 0 {
		return ""
	}
	return "by sound first: the score is the share of the files a release holds"
}
