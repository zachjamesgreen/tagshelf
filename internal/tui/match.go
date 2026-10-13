package tui

import (
	"context"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/huh/v2"

	"github.com/zachjamesgreen/tagshelf/internal/config"
	"github.com/zachjamesgreen/tagshelf/internal/engine"
	"github.com/zachjamesgreen/tagshelf/internal/marker"
	"github.com/zachjamesgreen/tagshelf/internal/musicbrainz"
	"github.com/zachjamesgreen/tagshelf/internal/rules"
	"github.com/zachjamesgreen/tagshelf/internal/store"
	"github.com/zachjamesgreen/tagshelf/internal/tagwrite"
)

// matchStep is where a release match has got to.
type matchStep int

const (
	matchDisabled  matchStep = iota // no contact is configured
	matchQuery                      // editing what to search for
	matchSearching                  // waiting for releases
	matchPick                       // choosing among the releases
	matchLoading                    // waiting for the release picked
	matchMap                        // reviewing which file is which track
	matchConfirm                    // looking at what would be written
	matchSaving                     // writing it
	matchListening                  // listening to the files and asking AcoustID
)

// match is the full-screen release match of several indexed files: one
// MusicBrainz release is picked for all of them and each file is put on one
// of its tracks.
type match struct {
	files []store.UntaggedFile
	infos []musicbrainz.FileInfo // what a lookup goes by, per file
	from  Tab                    // the tab it was opened from, for the breadcrumb
	step  matchStep

	// What the query form's fields hold.
	album, artist, tracks, year string
	barcode                     string
	form                        *huh.Form
	last                        huh.Field // the form's last field
	problem                     string    // what went wrong with the last request

	seq    int // of the request going on, 0 when none is
	cancel context.CancelFunc

	searched musicbrainz.ReleaseQuery // as last searched
	releases []musicbrainz.ReleaseCandidate
	cursor   int // index into releases

	// found are the releases the search found, and bySound those that hold
	// recordings AcoustID named for the files: releases is the second and
	// then the rest of the first. heard is, by file, what AcoustID says of
	// each recording it named; nil until it has been asked. failed is how
	// many files could not be listened to then, and heardOld how old the
	// oldest answer taken from the record was, 0 when AcoustID was asked for
	// all or a second ctrl+s would not ask again. listened counts the files
	// done while it listens, and back is the step that began at.
	found    []musicbrainz.ReleaseCandidate
	bySound  []musicbrainz.ReleaseCandidate
	heard    map[int]map[string]soundHit
	failed   int
	heardOld time.Duration
	listened *atomic.Int32
	back     matchStep

	release *musicbrainz.Release // the release picked, looked up in full
	mapping *musicbrainz.Mapping
	orig    []tagwrite.Values // what each file has now
	row     int               // the cursor on the mapping screen
	grabbed int               // the file being moved to another track, -1 when none is
	scroll  int               // the first line of the diff shown
}

// matchFoundMsg is the releases a search found.
type matchFoundMsg struct {
	seq      int
	releases []musicbrainz.ReleaseCandidate
	err      error
}

// matchLoadedMsg is the picked release in full, with every file's own tags.
type matchLoadedMsg struct {
	seq     int
	release *musicbrainz.Release
	mapping *musicbrainz.Mapping // the files put on its tracks
	orig    []tagwrite.Values
	err     error
}

// matchDoneMsg says the save a release match confirmed has ended.
type matchDoneMsg struct {
	moved    []string // where the files that qualified went
	warnings string   // what the run warned of, "" when nothing
	finish   engine.RunFinished
	err      error
}

// openMatch opens a release match on files.
func (m *Model) openMatch(files []store.UntaggedFile) tea.Cmd {
	for _, f := range files {
		if !tagwrite.Supported(f.Path) {
			m.setToast(false, "tagshelf cannot tag %s files: leave %s out", strings.TrimPrefix(strings.ToLower(filepath.Ext(f.Path)), "."), filepath.Base(f.Path))
			return nil
		}
	}
	x := &match{files: files, from: m.tab, grabbed: -1}
	m.match = x
	if m.mb == nil {
		return nil
	}
	for _, f := range files {
		x.infos = append(x.infos, fileInfo(f.File, f.From))
	}
	q := musicbrainz.ReleaseQueryFor(x.infos)
	x.album, x.artist, x.tracks = q.Album, q.Artist, strconv.Itoa(q.Tracks)
	if q.Year > 0 {
		x.year = strconv.Itoa(q.Year)
	}
	x.step = matchQuery
	// The files' own barcode goes into the form once it is read: of the
	// first few files, which is where an album's would be.
	src := m.src
	paths := make([]string, 0, matchBarcodeFiles)
	for _, f := range files[:min(len(files), matchBarcodeFiles)] {
		paths = append(paths, f.Path)
	}
	tagged := func() tea.Msg {
		for _, path := range paths {
			v, _ := src.Tags(path)
			if code, _ := musicbrainz.CleanBarcode(firstValue(v[barcodeTag])); code != "" {
				return matchTaggedMsg{match: x, barcode: code}
			}
		}
		return matchTaggedMsg{match: x}
	}
	return tea.Batch(m.matchForm(x), tagged)
}

// matchBarcodeFiles is how many of a release match's files are read for the
// barcode they hold.
const matchBarcodeFiles = 3

// matchForm makes the form that asks what to search for, of what the release
// match holds.
func (m *Model) matchForm(x *match) tea.Cmd {
	x.last = queryInput("Barcode", &x.barcode).Placeholder("alone finds its release, whatever is above")
	x.form = huh.NewForm(huh.NewGroup(
		queryInput("Album", &x.album),
		queryInput("Artist", &x.artist),
		queryInput("Tracks", &x.tracks).Placeholder("how many the release has"),
		queryInput("Year", &x.year).Placeholder("only ranks the releases found"),
		x.last,
	)).WithKeyMap(queryKeys()).WithShowHelp(false).WithWidth(m.editorWidth())
	return x.form.Init()
}

// matchTaggedMsg is the barcode the files of a release match hold, "" when
// they hold none that reads as one.
type matchTaggedMsg struct {
	match   *match
	barcode string
}

// matchTagged puts the files' own barcode into the form, unless one has been
// typed since.
func (m *Model) matchTagged(msg matchTaggedMsg) tea.Cmd {
	x := m.match
	if x == nil || x != msg.match || x.step != matchQuery || msg.barcode == "" || strings.TrimSpace(x.barcode) != "" {
		return nil
	}
	// A form shows what it was made with: it is made again.
	x.barcode = msg.barcode
	return m.matchForm(x)
}

// matchRequest starts a request to MusicBrainz for the release match that is
// open, dropping the one going on.
func (m *Model) matchRequest() (context.Context, int) {
	x := m.match
	x.stop()
	ctx, cancel := context.WithCancel(context.Background())
	x.cancel = cancel
	m.lookSeq++
	x.seq = m.lookSeq
	return ctx, x.seq
}

// stop drops the request going on, if any; its answer will not be taken.
func (x *match) stop() {
	if x.cancel != nil {
		x.cancel()
		x.cancel = nil
	}
	x.seq = 0
}

// closeMatch leaves the release match.
func (m *Model) closeMatch() {
	if m.match != nil {
		m.match.stop()
	}
	m.match = nil
}

// matchSearch asks MusicBrainz for releases like what the form holds.
func (m *Model) matchSearch() tea.Cmd {
	x := m.match
	// A whole folder of loose files taken by a slip, not a release: nothing
	// is asked for it, and no file's tags are read.
	if n := len(x.files); n > musicbrainz.MaxMatchFiles {
		x.problem = fmt.Sprintf("%d files are too many for one release (at most %d): select fewer", n, musicbrainz.MaxMatchFiles)
		return nil
	}
	q := musicbrainz.ReleaseQuery{Album: strings.TrimSpace(x.album), Artist: strings.TrimSpace(x.artist)}
	for _, field := range []struct {
		name string
		text string
		into *int
	}{{"number of tracks", x.tracks, &q.Tracks}, {"year", x.year, &q.Year}} {
		s := strings.TrimSpace(field.text)
		if s == "" {
			continue
		}
		n, err := strconv.Atoi(s)
		if err != nil || n < 0 {
			x.problem = fmt.Sprintf("The %s is a number, not %q", field.name, s)
			return nil
		}
		*field.into = n
	}
	var err error
	if q.Barcode, err = musicbrainz.CleanBarcode(x.barcode); err != nil {
		x.problem = capitalize(err.Error())
		return nil
	}
	// A track count alone matches every release of that size.
	if q.Album == "" && q.Artist == "" && q.Barcode == "" {
		x.problem = "Nothing to search for: type an album, an artist or a barcode"
		return nil
	}
	x.problem, x.found, x.cursor = "", nil, 0
	x.searched = q
	x.merge()
	x.step = matchSearching
	ctx, seq := m.matchRequest()
	// The files' own searches take the album and artist typed here into
	// account: BoostQueries says how. A barcode needs none: it names the
	// release, and theirs would only add others to it.
	mb, src, boost := m.mb, m.src, musicbrainz.BoostQueries(x.infos, q)
	if q.Barcode != "" {
		boost = nil
	}
	return func() tea.Msg {
		// A library that cannot be read only costs the ranking a tie-break.
		inLibrary, _ := src.LibraryReleases()
		found, err := mb.SearchReleaseCandidates(ctx, q, boost, func(id string) bool { return inLibrary[id] })
		return matchFoundMsg{seq: seq, releases: found, err: err}
	}
}

// matchFound takes the releases a search found.
func (m *Model) matchFound(msg matchFoundMsg) {
	x := m.match
	if x == nil || msg.seq != x.seq || x.step != matchSearching {
		return
	}
	x.cancel = nil
	x.step = matchQuery
	switch {
	case msg.err != nil:
		x.problem = msg.err.Error()
	case len(msg.releases) == 0 && len(x.bySound) == 0:
		x.problem = "MusicBrainz has no release like that: change the query and search again"
		if code := x.searched.Barcode; code != "" {
			x.problem = "MusicBrainz has no release with the barcode " + code + ": clear it to search by the other fields"
		}
	default:
		x.found = msg.releases
		x.merge()
		x.step = matchPick
	}
}

// matchLoad asks for the release under the cursor in full and for every
// file's tags, to put the files on its tracks.
func (m *Model) matchLoad() tea.Cmd {
	x := m.match
	if x.cursor < 0 || x.cursor >= len(x.releases) {
		return nil
	}
	id := x.releases[x.cursor].Release.ID
	x.problem = ""
	x.step = matchLoading
	ctx, seq := m.matchRequest()
	mb, src, files, infos := m.mb, m.src, x.files, x.infos
	return func() tea.Msg {
		msg := matchLoadedMsg{seq: seq}
		if msg.release, msg.err = mb.LookupRelease(ctx, id); msg.err != nil {
			return msg
		}
		for _, f := range files {
			v, err := src.Tags(f.Path)
			if err != nil {
				msg.err = fmt.Errorf("reading the tags of %s: %w", filepath.Base(f.Path), err)
				return msg
			}
			if v == nil {
				v = tagwrite.Values{}
			}
			msg.orig = append(msg.orig, v)
		}
		// Putting many files on many tracks takes a while: not on the UI
		// goroutine, and stopped with the request.
		msg.mapping, msg.err = musicbrainz.Assign(ctx, msg.release, infos)
		return msg
	}
}

// matchLoaded puts the files on the tracks of the release picked.
func (m *Model) matchLoaded(msg matchLoadedMsg) {
	x := m.match
	if x == nil || msg.seq != x.seq || x.step != matchLoading {
		return
	}
	x.cancel = nil
	if msg.err != nil {
		x.problem = msg.err.Error()
		x.step = matchPick
		return
	}
	x.release, x.orig, x.mapping = msg.release, msg.orig, msg.mapping
	x.applySound()
	x.row, x.grabbed, x.scroll = 0, -1, 0
	x.step = matchMap
}

// mapRow is a line of the mapping screen: a track of the release with the
// file on it, if any, or a file on no track.
type mapRow struct {
	hasTrack bool
	track    musicbrainz.TrackRef
	file     int // index into the match's files, -1 for a track without one
	assigned *musicbrainz.Assignment
}

// rows lists the release's tracks in order, then the files on none.
func (x *match) rows() []mapRow {
	on := make(map[musicbrainz.TrackRef]*musicbrainz.Assignment)
	for i := range x.mapping.Assignments {
		a := &x.mapping.Assignments[i]
		on[a.Track] = a
	}
	var rows []mapRow
	for _, ref := range x.release.Tracks() {
		r := mapRow{hasTrack: true, track: ref, file: -1}
		if a := on[ref]; a != nil {
			r.file, r.assigned = a.File, a
		}
		rows = append(rows, r)
	}
	for _, f := range x.mapping.Unassigned {
		rows = append(rows, mapRow{file: f})
	}
	return rows
}

// values are the tags the file of assignment a would get, as a save takes
// them.
func (x *match) values(a musicbrainz.Assignment) tagwrite.Values {
	return x.withAcoustID(lookupValues(musicbrainz.TagsFor(x.release, a.Track), x.files[a.File].Path), a)
}

// confirmMatch writes every mapped file's tags off the UI goroutine, as one
// run.
func (m *Model) confirmMatch() tea.Cmd {
	x := m.match
	if !m.canChange() {
		return nil
	}
	values := make(map[int64]tagwrite.Values)
	genres := make(map[int64][]string)
	var ids []int64
	for _, a := range x.mapping.Assignments {
		id := x.files[a.File].ID
		ids = append(ids, id)
		values[id] = x.values(a)
		genres[id] = musicbrainz.TagsFor(x.release, a.Track).Genres
	}
	x.step = matchSaving
	mark := marker.New(marker.MusicBrainz, time.Now())
	mode := m.genreMode
	save := func(st *store.Store, runID int64, f store.File) error {
		v := values[f.ID]
		if g := genres[f.ID]; len(g) > 0 && mode != config.GenreOff {
			// Whether the file has a genre to keep is read now, from the
			// file as the save finds it.
			v = withGenres(v, g, mode, genresOf(f.Path))
		}
		_, err := tagwrite.Writer{History: st, RunID: runID}.Save(f.Path, v, mark)
		return err
	}
	return m.saveMany(ids, save, func(moved []string, warnings string, finish engine.RunFinished, err error) tea.Msg {
		return matchDoneMsg{moved: moved, warnings: warnings, finish: finish, err: err}
	})
}

// matchDone says how the save went. The match closes once the run has been
// made, whatever became of each file: what is left to do shows on the tab.
func (m *Model) matchDone(msg matchDoneMsg) tea.Cmd {
	m.ended()
	x := m.match
	if x != nil && x.step == matchSaving {
		x.step = matchConfirm
	}
	if msg.err != nil {
		m.setToast(false, "Cannot change the library: %v", msg.err)
		return m.poll(true, false)
	}
	at := msg.moved
	if x != nil {
		if len(at) == 0 {
			// Tagged where they are: the album is the folder they are in.
			for _, f := range x.files {
				at = append(at, f.Path)
			}
		}
		m.closeMatch()
	}
	m.movedToast(msg.moved, msg.warnings, msg.finish)
	if msg.finish.State == engine.StateOK && len(at) > 0 {
		// The albums the files are on now may lack a cover.
		return tea.Batch(m.poll(true, false), m.coverArt(albumFolders(at), true))
	}
	return m.poll(true, false)
}

// matchKey handles a key while the release match is open.
func (m *Model) matchKey(msg tea.KeyPressMsg) tea.Cmd {
	x := m.match
	k := msg.String()
	m.toast, m.readErr = "", false
	if k == "ctrl+c" {
		return m.key(k)
	}
	page := max(m.bodyHeight()-6, 1)
	switch x.step {
	case matchDisabled:
		if k == "esc" {
			m.closeMatch()
		}
	case matchQuery:
		switch k {
		case "esc":
			m.closeMatch()
		case "enter":
			return m.matchSearch()
		case "ctrl+s":
			return m.matchListen()
		default:
			// The form never gets the key that would complete it: a
			// completed form cannot be edited again.
			if x.form.GetFocusedField() == x.last && (k == "tab" || k == "down") {
				return nil
			}
			_, cmd := x.form.Update(msg)
			return cmd
		}
	case matchSearching:
		if k == "esc" {
			x.stop()
			x.step = matchQuery
		}
	case matchListening:
		if k == "esc" {
			x.stop()
			x.step = x.back
		}
	case matchPick:
		x.problem = ""
		switch k {
		case "esc", "/":
			x.step = matchQuery
		case "up", "k":
			x.cursor = max(x.cursor-1, 0)
		case "down", "j":
			x.cursor = min(x.cursor+1, len(x.releases)-1)
		case "pgup":
			x.cursor = max(x.cursor-page, 0)
		case "pgdown":
			x.cursor = min(x.cursor+page, len(x.releases)-1)
		case "enter":
			return m.matchLoad()
		case "ctrl+s":
			return m.matchListen()
		}
	case matchLoading:
		if k == "esc" {
			x.stop()
			x.step = matchPick
		}
	case matchMap:
		m.matchMapKey(k, page)
	case matchConfirm:
		switch k {
		case "esc":
			x.step = matchMap
		case "up", "k":
			x.scroll = max(x.scroll-1, 0)
		case "down", "j":
			x.scroll++
		case "pgup":
			x.scroll = max(x.scroll-page, 0)
		case "pgdown":
			x.scroll += page
		case "enter", "y":
			return m.confirmMatch()
		}
	}
	return nil
}

// matchMapKey handles a key on the mapping screen.
func (m *Model) matchMapKey(k string, page int) {
	x := m.match
	rows := x.rows()
	x.problem = ""
	x.row = max(min(x.row, len(rows)-1), 0)
	cur := rows[x.row]
	switch k {
	case "up", "k":
		x.row = max(x.row-1, 0)
	case "down", "j":
		x.row = min(x.row+1, len(rows)-1)
	case "pgup":
		x.row = max(x.row-page, 0)
	case "pgdown":
		x.row = min(x.row+page, len(rows)-1)
	case "esc":
		if x.grabbed >= 0 {
			x.grabbed = -1
			return
		}
		x.step = matchPick
	case "x":
		x.grabbed = -1
		if cur.hasTrack && cur.file >= 0 {
			x.mapping.Unassign(cur.file)
		}
	case "r":
		if cur.file < 0 {
			x.problem = "There is no file on this line to move"
			return
		}
		x.grabbed = cur.file
	case "enter":
		if x.grabbed >= 0 {
			if !cur.hasTrack {
				x.problem = "Move to a track of the release, then press enter; esc leaves the file where it was"
				return
			}
			x.mapping.Reassign(x.grabbed, cur.track)
			x.grabbed = -1
			return
		}
		if len(x.mapping.Assignments) == 0 {
			x.problem = "No file is on a track, so there is nothing to write"
			return
		}
		x.scroll = 0
		x.step = matchConfirm
	}
}

// matchCrumb is the breadcrumb shown in place of the tabs.
func (m *Model) matchCrumb() string {
	x := m.match
	crumb := " " + dimStyle.Render(x.from.String()+" › ") + boldStyle.Render("Release match") +
		dimStyle.Render(" › ") + plural(int64(len(x.files)), "file", "files")
	step := x.step
	if step == matchListening {
		step = x.back
	}
	switch step {
	case matchPick, matchLoading:
		crumb += dimStyle.Render(" › ") + "Releases"
	case matchMap:
		crumb += dimStyle.Render(" › Releases › ") + "Tracks"
	case matchConfirm, matchSaving:
		crumb += dimStyle.Render(" › Releases › Tracks › ") + "Confirm"
	}
	return crumb
}

// matchHelp is the key help of the step the release match is on.
func (m *Model) matchHelp() string {
	x := m.match
	switch x.step {
	case matchDisabled:
		return "esc back"
	case matchQuery:
		return "↑↓/tab field  enter search  ctrl+s by sound  esc back"
	case matchSearching, matchLoading, matchListening:
		return "esc stop waiting"
	case matchPick:
		return "↑↓ move  enter pick  / change the query  ctrl+s by sound  esc back"
	case matchMap:
		if x.grabbed >= 0 {
			return "↑↓ move to a track  enter put the file there  esc leave it where it was"
		}
		return "↑↓ move  r move the file to another track  x take it off  enter review the tags  esc back to the releases"
	case matchConfirm:
		return "↑↓ scroll  enter write these tags  esc back to the tracks"
	}
	return ""
}

// matchBody draws the release match in h lines.
func (m *Model) matchBody(h int) string {
	x := m.match
	var b strings.Builder
	switch x.step {
	case matchDisabled:
		b.WriteString("\n")
		lookupsOff(&b, "e edits a file's tags by hand")
	case matchListening:
		if x.back == matchPick {
			m.releasesBody(&b, h)
			break
		}
		fallthrough
	case matchQuery, matchSearching:
		b.WriteString("\n  " + boldStyle.Render("Search MusicBrainz for a release") +
			dimStyle.Render(fmt.Sprintf("  to tag %s as", plural(int64(len(x.files)), "file", "files"))) + "\n\n")
		b.WriteString(strings.TrimRight(x.form.View(), "\n") + "\n")
		if x.problem != "" {
			b.WriteString("\n  " + badStyle.Render(clean(x.problem)) + "\n")
		}
		switch x.step {
		case matchSearching:
			b.WriteString("\n  " + dimStyle.Render("Searching MusicBrainz…") + "\n")
		case matchListening:
			b.WriteString("\n  " + dimStyle.Render(x.listeningLine()) + "\n")
		}
	case matchPick, matchLoading:
		m.releasesBody(&b, h)
	case matchMap:
		m.mapBody(&b, h)
	case matchConfirm, matchSaving:
		m.matchDiffBody(&b, h)
	}
	return b.String()
}

// The width of a release's line without its flexible columns.
const releaseFixedWidth = 2 + 3 + 4 + 2 + 13 + 6 + 5 + 7*2

// releasesBody draws the releases found in h lines.
func (m *Model) releasesBody(b *strings.Builder, h int) {
	x := m.match
	count := plural(int64(len(x.releases)), "release", "releases")
	if note := x.heardNote(); note != "" {
		count += "; " + note
	}
	b.WriteString("\n  " + boldStyle.Render("Releases") + dimStyle.Render("  "+strings.TrimRight(cell(count, max(m.width-13, 20)), " ")) + "\n")
	flex := max(m.width-releaseFixedWidth, 24)
	titleW := flex * 55 / 100
	artistW := flex - titleW
	row := func(score, title, artist, year, country, format, tracks, found string) string {
		return fmt.Sprintf("%3s  %s  %s  %4s  %-2s  %s  %6s  %5s",
			score, cell(title, titleW), cell(artist, artistW), year, cell(country, 2), cell(format, 13), tracks, found)
	}
	b.WriteString(dimStyle.Render("  "+row("", "Release", "Artist", "Year", "", "Format", "Tracks", "Found")) + "\n")
	rows := max(h-3, 1)
	if x.problem != "" || x.step == matchLoading || x.step == matchListening {
		rows = max(rows-2, 1)
	}
	first := window(x.cursor, rows)
	for i := first; i < len(x.releases) && i < first+rows; i++ {
		c := x.releases[i]
		r := c.Release
		title := r.Title
		if c.InLibrary {
			title += " (in library)"
		}
		var year, found string
		if y := r.Year(); y > 0 {
			year = strconv.Itoa(y)
		}
		if c.Searched > 0 {
			// How many of the files searched for are on the release.
			found = fmt.Sprintf("%d/%d", c.Found, c.Searched)
		}
		line := row(strconv.Itoa(c.Score), title, musicbrainz.Credit(r.ArtistCredit), year, r.Country, r.Format(), strconv.Itoa(r.TrackCount), found)
		if i == x.cursor {
			b.WriteString("› " + activeTab.Render(line) + "\n")
		} else {
			b.WriteString("  " + line + "\n")
		}
	}
	if x.problem != "" {
		b.WriteString("\n  " + badStyle.Render(clean(x.problem)) + "\n")
	}
	switch x.step {
	case matchLoading:
		b.WriteString("\n  " + dimStyle.Render("Reading the release from MusicBrainz…") + "\n")
	case matchListening:
		b.WriteString("\n  " + dimStyle.Render(x.listeningLine()) + "\n")
	}
}

// releaseLine names the release picked.
func (x *match) releaseLine() string {
	r := x.release
	s := r.Title + " by " + musicbrainz.Credit(r.ArtistCredit)
	var parts []string
	if y := r.Year(); y > 0 {
		parts = append(parts, strconv.Itoa(y))
	}
	for _, p := range []string{r.Country, r.Format()} {
		if p != "" {
			parts = append(parts, p)
		}
	}
	if len(parts) > 0 {
		s += " (" + strings.Join(parts, ", ") + ")"
	}
	return clean(s)
}

// position names a track's place on its release, with the disc when the
// release has more than one.
func position(rel *musicbrainz.Release, ref musicbrainz.TrackRef) string {
	t := rel.Track(ref)
	if len(rel.Media) > 1 {
		return fmt.Sprintf("%d-%02d", rel.Media[ref.Medium].Position, t.Position)
	}
	return fmt.Sprintf("%02d", t.Position)
}

// delta writes a length difference with its sign, as m:ss.
func delta(d time.Duration) string {
	sign := "+"
	if d < 0 {
		sign = "-"
	}
	s := int(d.Abs().Round(time.Second).Seconds())
	return fmt.Sprintf("%s%d:%02d", sign, s/60, s%60)
}

// lengthNote says what an assignment's length difference means, "" when it
// is nothing to speak of.
func lengthNote(a musicbrainz.Assignment) string {
	switch a.Length {
	case musicbrainz.LengthFlagged:
		return "! length differs by " + strings.TrimLeft(delta(a.LengthDelta), "+-")
	case musicbrainz.LengthWeak:
		return "~ " + delta(a.LengthDelta)
	}
	return ""
}

// summary counts what the mapping leaves out and warns of.
func (x *match) summary() string {
	mp := x.mapping
	parts := []string{plural(int64(len(mp.Assignments)), "file", "files") + " on a track"}
	if n := len(mp.Unassigned); n > 0 {
		parts = append(parts, plural(int64(n), "file", "files")+" on none (not written)")
	}
	if n := len(mp.EmptyTracks); n > 0 {
		parts = append(parts, plural(int64(n), "track", "tracks")+" without a file")
	}
	return strings.Join(parts, ", ")
}

// window is the first of the lines shown when room of them fit and the one
// at cursor must be among them.
func window(cursor, room int) int {
	if cursor >= room {
		return cursor - room + 1
	}
	return 0
}

// mapBody draws the mapping screen in h lines: the release's tracks beside
// the files on them, then the files on no track.
func (m *Model) mapBody(b *strings.Builder, h int) {
	x := m.match
	b.WriteString("\n  " + boldStyle.Render(x.releaseLine()) + "\n")
	b.WriteString("  " + dimStyle.Render(x.summary()) + "\n\n")
	flex := max(m.width-(2+5+2+2+5+2+2+2+26), 30)
	titleW := flex / 2
	fileW := flex - titleW
	b.WriteString(dimStyle.Render(fmt.Sprintf("  %-5s  %s  %5s    %s", "Track", cell("Title", titleW), "Len", "File")) + "\n")

	rows := x.rows()
	x.row = max(min(x.row, len(rows)-1), 0)
	room := max(h-5, 1)
	if x.problem != "" || x.grabbed >= 0 {
		room = max(room-2, 1)
	}
	first := window(x.row, room)
	for i := first; i < len(rows) && i < first+room; i++ {
		r := rows[i]
		var line string
		if r.hasTrack {
			t := x.release.Track(r.track)
			ms := t.Length
			if ms == 0 && t.Recording != nil {
				ms = t.Recording.Length
			}
			line = fmt.Sprintf("%-5s  %s  %5s  ", position(x.release, r.track), cell(t.Title, titleW), length(time.Duration(ms)*time.Millisecond))
		} else {
			line = fmt.Sprintf("%-5s  %s  %5s  ", "", cell("(on no track: not written)", titleW), "")
		}
		switch {
		case r.file < 0:
			line += "  " + cell("(no file)", fileW)
		default:
			name := filepath.Base(x.files[r.file].Path)
			if r.file == x.grabbed {
				name = "[moving] " + name
			}
			line += "← " + cell(name, fileW)
			if r.assigned != nil {
				line += "  " + lengthNote(*r.assigned)
			}
		}
		line = strings.TrimRight(line, " ")
		switch {
		case i == x.row:
			b.WriteString("› " + activeTab.Render(line) + "\n")
		case r.file < 0:
			b.WriteString("  " + dimStyle.Render(line) + "\n")
		case r.assigned != nil && r.assigned.Flagged():
			b.WriteString("  " + accentStyle.Render(line) + "\n")
		default:
			b.WriteString("  " + line + "\n")
		}
	}
	switch {
	case x.problem != "":
		b.WriteString("\n  " + badStyle.Render(clean(x.problem)) + "\n")
	case x.grabbed >= 0:
		b.WriteString("\n  " + accentStyle.Render("Moving "+clean(filepath.Base(x.files[x.grabbed].Path))+": go to its track and press enter") + "\n")
	}
}

// matchDiffBody draws, in h lines, what every mapped file would be written
// with.
func (m *Model) matchDiffBody(b *strings.Builder, h int) {
	x := m.match
	mp := x.mapping
	b.WriteString("\n  " + boldStyle.Render("Tag "+plural(int64(len(mp.Assignments)), "file", "files")+" as ") + x.releaseLine() + "\n")

	// Where they go: one album folder, as a rule.
	dirs := make(map[string]string)
	lacking := 0
	stay := false
	for _, a := range mp.Assignments {
		f := x.files[a.File]
		if outside(f.File) {
			stay = true
			continue
		}
		dst, ok := rules.LibraryPathFor(m.header.Destination, m.compilations, rulesTags(musicbrainz.TagsFor(x.release, a.Track)), filepath.Ext(f.Path))
		if !ok {
			lacking++
			continue
		}
		dirs[rules.PathKey(filepath.Dir(dst))] = filepath.Dir(dst)
	}
	switch {
	case stay:
		b.WriteString("  " + staysLine + "\n")
	case len(dirs) == 1 && lacking == 0:
		for _, dir := range dirs {
			b.WriteString("  Will go to:    " + m.shortPath(dir) + "\n")
		}
	case len(dirs) > 0:
		b.WriteString(fmt.Sprintf("  Will go to %s of the library\n", plural(int64(len(dirs)), "folder", "folders")))
	}
	var notes []string
	if lacking > 0 {
		notes = append(notes, plural(int64(lacking), "file", "files")+" will stay where they are: MusicBrainz has no artist, album or title for their tracks")
	}
	if n := len(mp.Unassigned); n > 0 {
		notes = append(notes, plural(int64(n), "file is", "files are")+" on no track and will not be written")
	}
	if n := len(mp.Flagged()); n > 0 {
		notes = append(notes, plural(int64(n), "file differs", "files differ")+" in length from the track by more than 10 s")
	}
	for _, n := range notes {
		b.WriteString("  " + accentStyle.Render("Warning: "+n) + "\n")
	}
	b.WriteString("\n")

	valueW := max((m.width-6-13-4)/2, 12)
	show := showValues
	var lines []string
	for _, a := range mp.Assignments {
		f := x.files[a.File]
		t := x.release.Track(a.Track)
		head := boldStyle.Render(clean(filepath.Base(f.Path))) + dimStyle.Render(" -> ") + position(x.release, a.Track) + " " + clean(t.Title)
		if note := lengthNote(a); a.Flagged() {
			head += "  " + accentStyle.Render(note)
		}
		lines = append(lines, head)
		values, old := x.values(a), x.orig[a.File]
		changed, ids, codes := 0, false, false
		for i, lf := range lookupFields {
			now, ok := values[lf.field]
			if !ok || strings.Join(old[lf.field], "\x00") == strings.Join(now, "\x00") {
				continue
			}
			changed++
			// The fields a person reads, then the codes and the IDs as one
			// line.
			switch {
			case i >= lookupIDsFrom:
				ids = true
			case i >= lookupCodesFrom:
				codes = true
			default:
				lines = append(lines, fmt.Sprintf("    %-13s%s -> %s", lf.name, dimStyle.Render(cell(show(old[lf.field]), valueW)), accentStyle.Render(show(now))))
			}
		}
		switch {
		case changed == 0:
			lines = append(lines, dimStyle.Render("    already tagged like this"))
		case ids && codes:
			lines = append(lines, dimStyle.Render("    and its ISRC, barcode and MusicBrainz IDs"))
		case ids:
			lines = append(lines, dimStyle.Render("    and its MusicBrainz IDs"))
		case codes:
			lines = append(lines, dimStyle.Render("    and its ISRC and barcode"))
		}
	}
	room := max(h-5-len(notes), 1)
	if x.step == matchSaving {
		room = max(room-2, 1)
	}
	x.scroll = max(min(x.scroll, len(lines)-room), 0)
	for _, l := range lines[x.scroll:min(x.scroll+room, len(lines))] {
		b.WriteString("  " + l + "\n")
	}
	if x.step == matchSaving {
		b.WriteString("\n  " + dimStyle.Render("Saving…") + "\n")
	}
}
