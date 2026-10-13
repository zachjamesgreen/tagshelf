package tui

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"charm.land/bubbles/v2/key"
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

// MusicBrainz is what the TUI asks of the MusicBrainz web service.
// *musicbrainz.Client implements it.
type MusicBrainz interface {
	SearchRecordings(ctx context.Context, q musicbrainz.Query, offset int) (*musicbrainz.RecordingSearch, error)
	SearchReleaseCandidates(ctx context.Context, q musicbrainz.ReleaseQuery, boost []musicbrainz.Query, inLibrary func(releaseID string) bool) ([]musicbrainz.ReleaseCandidate, error)
	LookupRelease(ctx context.Context, id string) (*musicbrainz.Release, error)
	RecordingsByID(ctx context.Context, ids []string) ([]musicbrainz.Recording, error)
}

// lookupStep is where a lookup has got to.
type lookupStep int

const (
	stepDisabled  lookupStep = iota // no contact is configured
	stepQuery                       // editing what to search for
	stepSearching                   // waiting for matches
	stepPick                        // choosing among the matches
	stepMore                        // waiting for more matches
	stepLoading                     // waiting for the release of the match picked
	stepConfirm                     // looking at what would be written
	stepSaving                      // writing it
	stepListening                   // listening to the file and asking AcoustID
)

// listening is what a lookup says while it identifies its file by sound.
const listening = "Listening to the file and asking AcoustID…"

// matchPage is how many more matches each "more" shows.
const matchPage = musicbrainz.PageSize

// lookup is the full-screen MusicBrainz lookup of one indexed file.
type lookup struct {
	file store.File
	from Tab // the tab it was opened from, for the breadcrumb
	step lookupStep

	// base is the query the file's tags and name gave; the form edits its
	// title, artist, album and length.
	base                         musicbrainz.Query
	title, artist, album, length string
	form                         *huh.Form
	last                         huh.Field // the form's last field
	problem                      string    // what went wrong with the last request

	// Each request carries the sequence number it was made with; an answer
	// to any other is dropped. cancel stops the request going on.
	seq    int
	cancel context.CancelFunc

	query     musicbrainz.Query       // as last searched
	recs      []musicbrainz.Recording // every recording fetched for it
	total     int                     // how many MusicBrainz has
	inLibrary map[string]bool
	matches   []musicbrainz.Candidate // recs ranked
	shown     int                     // how many of matches are listed
	cursor    int

	// What identifying the file by its sound gave: the recordings AcoustID
	// named as MusicBrainz has them, and by recording ID what it says of
	// each of those. sound is nil until it has been asked. The first bySound
	// of matches are those. back is the step listening began at.
	heard           []musicbrainz.Recording
	sound           map[string]soundHit
	bySound         int
	heardAt         time.Time
	heardFromRecord bool
	back            lookupStep

	picked musicbrainz.Candidate
	tags   musicbrainz.Tags // what the pick would write
	orig   tagwrite.Values  // what the file has now
}

// lookupFoundMsg is a page of search results.
type lookupFoundMsg struct {
	seq       int
	recs      []musicbrainz.Recording
	total     int
	inLibrary map[string]bool
	err       error
}

// lookupPickedMsg is the tags a picked match would write, with the file's
// own.
type lookupPickedMsg struct {
	seq  int
	tags musicbrainz.Tags
	orig tagwrite.Values
	err  error
}

// lookupDoneMsg says the save a lookup confirmed has ended.
type lookupDoneMsg struct{ saved }

// openLookup opens the lookup on f, which a run took from the path from ("" when
// none did).
func (m *Model) openLookup(f store.File, from string) tea.Cmd {
	if !tagwrite.Supported(f.Path) {
		m.setToast(false, "tagshelf cannot tag %s files", strings.TrimPrefix(strings.ToLower(filepath.Ext(f.Path)), "."))
		return nil
	}
	l := &lookup{file: f, from: m.tab}
	m.look = l
	if m.mb == nil {
		l.step = stepDisabled
		return nil
	}
	l.base = musicbrainz.QueryFor(fileInfo(f, from))
	l.title, l.artist, l.album, l.length = l.base.Title, l.base.Artist, l.base.Album, length(l.base.Length)
	l.step = stepQuery

	keys := huh.NewDefaultKeyMap()
	keys.Quit.SetEnabled(false) // ctrl+c is the TUI's
	keys.Input.Prev = key.NewBinding(key.WithKeys("shift+tab", "up"))
	keys.Input.Next = key.NewBinding(key.WithKeys("tab", "down"))
	input := func(title string, value *string) *huh.Input {
		return huh.NewInput().Title(fmt.Sprintf("%-7s", title)).Value(value).Inline(true)
	}
	l.last = input("Length", &l.length).Placeholder("m:ss")
	l.form = huh.NewForm(huh.NewGroup(
		input("Title", &l.title),
		input("Artist", &l.artist),
		input("Album", &l.album),
		l.last,
	)).WithKeyMap(keys).WithShowHelp(false).WithWidth(m.editorWidth())
	return l.form.Init()
}

// fileInfo is what a lookup goes by of the indexed file f, which a run took
// from the path from ("" when none did).
func fileInfo(f store.File, from string) musicbrainz.FileInfo {
	info := musicbrainz.FileInfo{
		Title: f.Title, Artist: f.Artist, Album: f.Album, Year: f.Year,
		Track: f.Track, TrackTotal: f.TrackTotal, Disc: f.Disc, Length: f.Length, Path: f.Path,
	}
	if info.Artist == "" {
		info.Artist = f.AlbumArtist
	}
	if from != "" {
		info.Folder = filepath.Base(filepath.Dir(from))
	}
	return info
}

// rulesTags are MusicBrainz tags as a file's place in the library is worked
// out from.
func rulesTags(t musicbrainz.Tags) rules.Tags {
	tags := rules.Tags{
		Artist: t.Artist, AlbumArtist: t.AlbumArtist, Album: t.Album, Title: t.Title,
		Track: t.Track, Disc: t.Disc, DiscTotal: t.DiscTotal,
		VariousArtistsID: rules.HasVariousArtistsID(t.AlbumArtistIDs),
	}
	if len(t.Date) >= 4 {
		tags.Year, _ = strconv.Atoi(t.Date[:4])
	}
	return tags
}

// parseLength reads a length typed as m:ss or as seconds; nothing is no
// length.
func parseLength(s string) (time.Duration, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, nil
	}
	min, sec, hasMin := strings.Cut(s, ":")
	if !hasMin {
		min, sec = "0", s
	}
	mins, err1 := strconv.Atoi(min)
	secs, err2 := strconv.Atoi(sec)
	if err1 != nil || err2 != nil || mins < 0 || secs < 0 || (hasMin && secs > 59) {
		return 0, fmt.Errorf("a length is m:ss, not %q", s)
	}
	return time.Duration(mins)*time.Minute + time.Duration(secs)*time.Second, nil
}

// request starts a request to MusicBrainz for the lookup that is open,
// dropping the one going on. Sequence numbers are the model's, so that an
// answer to a lookup since closed is never taken for another's.
func (m *Model) request() (context.Context, int) {
	l := m.look
	l.stop()
	ctx, cancel := context.WithCancel(context.Background())
	l.cancel = cancel
	m.lookSeq++
	l.seq = m.lookSeq
	return ctx, l.seq
}

// stop drops the request going on, if any; its answer will not be taken.
func (l *lookup) stop() {
	if l.cancel != nil {
		l.cancel()
		l.cancel = nil
	}
	l.seq = 0
}

// closeLookup leaves the lookup.
func (m *Model) closeLookup() {
	if m.look != nil {
		m.look.stop()
	}
	m.look = nil
}

// search asks MusicBrainz for what the form holds.
func (m *Model) search() tea.Cmd {
	l := m.look
	d, err := parseLength(l.length)
	if err != nil {
		l.problem = err.Error()
		return nil
	}
	q := l.base
	q.Title, q.Artist, q.Album, q.Length = strings.TrimSpace(l.title), strings.TrimSpace(l.artist), strings.TrimSpace(l.album), d
	// A length alone matches every song of that length.
	if q.Title == "" && q.Artist == "" && q.Album == "" {
		l.problem = "Nothing to search for: type a title, an artist or an album"
		return nil
	}
	l.query, l.recs, l.total, l.problem = q, nil, 0, ""
	// What was heard stays listed, whatever comes of the search.
	l.rank()
	l.shown, l.cursor = l.bySound, 0
	l.step = stepSearching
	return m.fetch(0)
}

// fetch gets the page of recordings starting at offset for the query last
// searched.
func (m *Model) fetch(offset int) tea.Cmd {
	l := m.look
	ctx, seq := m.request()
	mb, src, q, known := m.mb, m.src, l.query, l.inLibrary
	return func() tea.Msg {
		msg := lookupFoundMsg{seq: seq, inLibrary: known}
		if msg.inLibrary == nil {
			// A library that cannot be read only costs the ranking a
			// tie-break.
			msg.inLibrary, _ = src.LibraryReleases()
		}
		res, err := mb.SearchRecordings(ctx, q, offset)
		if err != nil {
			msg.err = err
			return msg
		}
		msg.recs, msg.total = res.Recordings, res.Count
		return msg
	}
}

// lookupFound takes a page of results into the list of matches.
func (m *Model) lookupFound(msg lookupFoundMsg) {
	l := m.look
	if l == nil || msg.seq != l.seq || (l.step != stepSearching && l.step != stepMore) {
		return
	}
	l.cancel = nil
	if msg.err != nil {
		l.problem = msg.err.Error()
		// A first search goes back to the query; more of one stays put.
		l.step = stepQuery
		if len(l.matches) > 0 {
			l.step = stepPick
		}
		return
	}
	// The match under the cursor, to stay on it when more are ranked in.
	var at *musicbrainz.Candidate
	if l.cursor < len(l.matches) {
		c := l.matches[l.cursor]
		at = &c
	}
	l.inLibrary = msg.inLibrary
	l.recs = append(l.recs, msg.recs...)
	l.total = max(msg.total, len(l.recs))
	if len(msg.recs) == 0 {
		// The service has no more than it gave, whatever it counted.
		l.total = len(l.recs)
	}
	l.rank()
	l.shown = min(max(l.shown, l.bySound)+matchPage, len(l.matches))
	if at != nil {
		for i, c := range l.matches {
			if sameMatch(c, *at) {
				l.cursor = i
				// It may have been ranked past the page it was on.
				l.shown = max(l.shown, i+1)
				break
			}
		}
	}
	l.step = stepPick
	if len(l.matches) == 0 {
		l.problem = "MusicBrainz has no match for that: change the query and search again"
		l.step = stepQuery
	}
}

// sameMatch reports whether a and b are the same recording on the same
// release.
func sameMatch(a, b musicbrainz.Candidate) bool {
	if a.Recording.ID != b.Recording.ID || (a.Release == nil) != (b.Release == nil) {
		return false
	}
	return a.Release == nil || a.Release.ID == b.Release.ID
}

// hasMore reports whether "more" has anything to show.
func (l *lookup) hasMore() bool {
	return l.shown < len(l.matches) || len(l.recs) < l.total
}

// pick asks for the release of the match under the cursor and the file's
// tags, to show what would change.
func (m *Model) pick() tea.Cmd {
	l := m.look
	if l.cursor < 0 || l.cursor >= l.shown {
		return nil
	}
	c := l.matches[l.cursor]
	if c.Release == nil {
		l.problem = "That recording is on no release, so it has no album to file the song under"
		return nil
	}
	l.picked, l.problem = c, ""
	l.step = stepLoading
	ctx, seq := m.request()
	mb, src, path := m.mb, m.src, l.file.Path
	return func() tea.Msg {
		msg := lookupPickedMsg{seq: seq}
		rel, err := mb.LookupRelease(ctx, c.Release.ID)
		if err != nil {
			msg.err = err
			return msg
		}
		ref, ok := trackOf(rel, c)
		if !ok {
			msg.err = errors.New("MusicBrainz no longer lists that recording on the release")
			return msg
		}
		msg.tags = musicbrainz.TagsFor(rel, ref)
		if msg.orig, err = src.Tags(path); err != nil {
			msg.err = fmt.Errorf("reading the file's tags: %w", err)
		}
		return msg
	}
}

// trackOf finds the track of the looked-up release rel that the match c is:
// the one with its recording, at its position when the recording is on the
// release more than once.
func trackOf(rel *musicbrainz.Release, c musicbrainz.Candidate) (musicbrainz.TrackRef, bool) {
	var found musicbrainz.TrackRef
	ok := false
	for _, ref := range rel.Tracks() {
		t := rel.Track(ref)
		if t.Recording == nil || t.Recording.ID != c.Recording.ID {
			continue
		}
		if rel.Media[ref.Medium].Position == c.Disc && t.Position == c.Track {
			return ref, true
		}
		if !ok {
			found, ok = ref, true
		}
	}
	return found, ok
}

// lookupPicked shows what the picked match would write.
func (m *Model) lookupPicked(msg lookupPickedMsg) {
	l := m.look
	if l == nil || msg.seq != l.seq || l.step != stepLoading {
		return
	}
	l.cancel = nil
	if msg.err != nil {
		l.problem = msg.err.Error()
		l.step = stepPick
		return
	}
	l.tags, l.orig = msg.tags, msg.orig
	if l.orig == nil {
		l.orig = tagwrite.Values{}
	}
	l.step = stepConfirm
}

// lookupFields are the fields a MusicBrainz match writes, in the order the
// diff shows them, with their names there.
var lookupFields = []struct {
	field tagwrite.Field
	name  string
}{
	{tagwrite.Title, "Title"}, {tagwrite.Artist, "Artist"}, {tagwrite.AlbumArtist, "Album artist"}, {tagwrite.Album, "Album"},
	{tagwrite.Date, "Date"}, {tagwrite.Track, "Track"}, {tagwrite.Disc, "Disc"}, {tagwrite.Compilation, "Compilation"},
	{tagwrite.Genre, "Genre"},
	{tagwrite.RecordingID, "Recording ID"}, {tagwrite.ReleaseTrackID, "Track ID"}, {tagwrite.AlbumID, "Release ID"},
	{tagwrite.ReleaseGroupID, "Release group ID"}, {tagwrite.ArtistIDs, "Artist IDs"}, {tagwrite.AlbumArtistIDs, "Album artist IDs"},
	{tagwrite.AcoustID, "AcoustID"},
}

// lookupIDsFrom is where in lookupFields the MusicBrainz IDs start; the
// fields before it are the ones a person reads.
const lookupIDsFrom = 9

// withGenres is values with the genres MusicBrainz has for the match, when
// the setting mode has a match write them to a file whose genres are has:
// always, or only when it has none. With no genres from MusicBrainz the
// file's own are never touched.
func withGenres(values tagwrite.Values, genres []string, mode string, has []string) tagwrite.Values {
	switch {
	case len(genres) == 0, mode == config.GenreOff:
		return values
	case mode == config.GenreReplace, len(has) == 0:
		values = maps.Clone(values)
		values[tagwrite.Genre] = slices.Clone(genres)
	}
	return values
}

// genresOf reads the genres the file at path has now, for a save that fills
// in a missing genre to know whether it is missing. A file that cannot be
// read is taken to have one: its genre is then left alone.
func genresOf(path string) []string {
	v, err := tagwrite.Read(path)
	if err != nil {
		return []string{"?"}
	}
	return v[tagwrite.Genre]
}

// lookupValues are the tags t as a save takes them: every field a match
// writes is there, and one MusicBrainz has no value for is cleared.
func lookupValues(t musicbrainz.Tags, path string) tagwrite.Values {
	one := func(s string) []string {
		if s == "" {
			return []string{}
		}
		return []string{s}
	}
	xOfN := func(n, total int) []string {
		switch {
		case n <= 0:
			return []string{}
		case total <= 0:
			return []string{strconv.Itoa(n)}
		}
		return []string{strconv.Itoa(n) + "/" + strconv.Itoa(total)}
	}
	many := func(ids []string) []string { return append([]string{}, ids...) }
	v := tagwrite.Values{
		tagwrite.Title: one(t.Title), tagwrite.Artist: one(t.Artist), tagwrite.AlbumArtist: one(t.AlbumArtist),
		tagwrite.Album: one(t.Album), tagwrite.Date: one(t.Date),
		tagwrite.Track: xOfN(t.Track, t.TrackTotal), tagwrite.Disc: xOfN(t.Disc, t.DiscTotal),
		tagwrite.RecordingID: one(t.RecordingID), tagwrite.ReleaseTrackID: one(t.ReleaseTrackID),
		tagwrite.AlbumID: one(t.AlbumID), tagwrite.ReleaseGroupID: one(t.ReleaseGroupID),
		tagwrite.ArtistIDs: many(t.ArtistIDs), tagwrite.AlbumArtistIDs: many(t.AlbumArtistIDs),
	}
	if tagwrite.HasCompilation(path) {
		v[tagwrite.Compilation] = []string{}
		if t.Compilation {
			v[tagwrite.Compilation] = []string{"1"}
		}
	}
	return v
}

// lookupWrites are the tags the picked match would write to the file: the
// match's, its genres when the setting has them written, and the AcoustID of
// the audio when the match is one AcoustID heard.
func (m *Model) lookupWrites(l *lookup) tagwrite.Values {
	v := withGenres(lookupValues(l.tags, l.file.Path), l.tags.Genres, m.genreMode, l.orig[tagwrite.Genre])
	if hit, ok := l.heardAs(l.picked); ok && hit.acoustID != "" {
		v[tagwrite.AcoustID] = []string{hit.acoustID}
	}
	return v
}

// lookupSave is the tag save of a confirmed match: its tags and the manual
// marker, saying a person picked them from MusicBrainz.
func lookupSave(values tagwrite.Values, now time.Time) engine.TagSave {
	return tagSave(values, marker.MusicBrainz, now)
}

// placed is where the file would go with the picked match's tags.
func (m *Model) placed(l *lookup) (line string, lacking bool) {
	if outside(l.file) {
		return staysLine, false
	}
	return m.placeLine(l.file, rulesTags(l.tags))
}

// confirmLookup writes the picked match's tags off the UI goroutine.
func (m *Model) confirmLookup() tea.Cmd {
	l := m.look
	if !m.canChange() {
		return nil
	}
	l.step = stepSaving
	save := lookupSave(m.lookupWrites(l), time.Now())
	return m.saveOne(l.file.ID, save, func(s saved) tea.Msg { return lookupDoneMsg{s} })
}

// lookupDone says how the save went: the lookup closes on a save that worked
// and stays on the diff, with what went wrong, on one that did not.
func (m *Model) lookupDone(msg lookupDoneMsg) tea.Cmd {
	m.ended()
	l := m.look
	mine := l != nil && l.file.ID == msg.id && l.step == stepSaving
	if mine {
		l.step = stepConfirm
	}
	if m.failed(msg.saved) {
		if mine && msg.err == nil {
			// The tags may have been written, and the file moved, before it
			// went wrong: the diff shown is no longer true. Picking again
			// reads the file as it is now.
			if msg.moved != "" {
				l.file.Path = msg.moved
			}
			l.step = stepPick
		}
	} else {
		var where string
		var lacking bool
		at := msg.moved
		if mine {
			where, lacking = m.placed(l)
			if at == "" {
				at = l.file.Path
			}
			m.closeLookup()
		}
		m.savedToast(msg.saved, where, lacking)
		if at != "" {
			// The album the file is on now may lack a cover.
			return tea.Batch(m.poll(true, false), m.coverArt(albumFolders([]string{at}), true))
		}
	}
	return m.poll(true, false)
}

// lookupKey handles a key while the lookup is open.
func (m *Model) lookupKey(msg tea.KeyPressMsg) tea.Cmd {
	l := m.look
	k := msg.String()
	m.toast, m.readErr = "", false
	switch k {
	case "ctrl+c":
		return m.key(k)
	case "ctrl+e":
		// Tagging by hand is always there to fall back on.
		if l.step != stepSaving {
			f := l.file
			m.closeLookup()
			return m.openEditor(f)
		}
		return nil
	}
	switch l.step {
	case stepDisabled:
		if k == "esc" {
			m.closeLookup()
		}
	case stepQuery:
		switch k {
		case "esc":
			m.closeLookup()
		case "enter":
			return m.search()
		case "ctrl+s":
			return m.listen()
		default:
			// The form never gets the key that would complete it: a
			// completed form cannot be edited again.
			if l.form.GetFocusedField() == l.last && (k == "tab" || k == "down") {
				return nil
			}
			_, cmd := l.form.Update(msg)
			return cmd
		}
	case stepMore:
		if k == "esc" {
			l.stop()
			l.step = stepPick
		}
	case stepListening:
		if k == "esc" {
			l.stop()
			l.step = l.back
		}
	case stepSearching:
		if k == "esc" {
			l.stop()
			l.step = stepQuery
			if len(l.matches) > 0 {
				l.step = stepPick
			}
		}
	case stepPick:
		l.problem = ""
		page := max(m.bodyHeight()-5, 1)
		switch k {
		case "esc", "/":
			l.step = stepQuery
		case "up", "k":
			l.cursor = max(l.cursor-1, 0)
		case "down", "j":
			l.cursor = min(l.cursor+1, l.shown-1)
		case "pgup":
			l.cursor = max(l.cursor-page, 0)
		case "pgdown":
			l.cursor = min(l.cursor+page, l.shown-1)
		case "m":
			switch {
			case l.shown < len(l.matches):
				l.shown = min(l.shown+matchPage, len(l.matches))
			case len(l.recs) < l.total:
				l.step = stepMore
				return m.fetch(len(l.recs))
			default:
				l.problem = "MusicBrainz has no more matches for that"
			}
		case "enter":
			return m.pick()
		case "ctrl+s":
			return m.listen()
		}
	case stepLoading:
		if k == "esc" {
			l.stop()
			l.step = stepPick
		}
	case stepConfirm:
		switch k {
		case "esc":
			l.step = stepPick
		case "enter", "y":
			return m.confirmLookup()
		}
	}
	return nil
}

// lookupCrumb is the breadcrumb shown in place of the tabs.
func (m *Model) lookupCrumb() string {
	l := m.look
	crumb := " " + dimStyle.Render(l.from.String()+" › ") + boldStyle.Render("Look up") + dimStyle.Render(" › ") + clean(filepath.Base(l.file.Path))
	switch l.step {
	case stepListening:
		if l.back == stepPick {
			crumb += dimStyle.Render(" › ") + "Matches"
		}
	case stepPick, stepMore, stepLoading:
		crumb += dimStyle.Render(" › ") + "Matches"
	case stepConfirm, stepSaving:
		crumb += dimStyle.Render(" › Matches › ") + "Confirm"
	}
	return crumb
}

// lookupHelp is the key help of the step the lookup is on.
func (m *Model) lookupHelp() string {
	switch m.look.step {
	case stepDisabled:
		return "ctrl+e edit the tags by hand  esc back"
	case stepQuery:
		return "↑↓/tab field  enter search  ctrl+s by sound  ctrl+e edit by hand  esc back"
	case stepSearching, stepMore, stepLoading, stepListening:
		return "esc stop waiting"
	case stepPick:
		return "↑↓ move  enter pick  m more  / change the query  ctrl+s by sound  ctrl+e edit by hand  esc back"
	case stepConfirm:
		return "enter write these tags  ctrl+e edit by hand  esc back to the matches"
	}
	return ""
}

// lookupBody draws the lookup in h lines.
func (m *Model) lookupBody(h int) string {
	l := m.look
	var b strings.Builder
	head := "  " + m.shortPath(l.file.Path)
	if d := length(l.file.Length); d != "" {
		head += "  " + d
	}
	b.WriteString(dimStyle.Render(head) + "\n\n")
	switch l.step {
	case stepDisabled:
		lookupsOff(&b, "ctrl+e edits the tags by hand")
	case stepListening:
		if l.back == stepPick {
			m.matchesBody(&b, h-2)
			break
		}
		fallthrough
	case stepQuery, stepSearching:
		b.WriteString("  " + boldStyle.Render("Search MusicBrainz for") + "\n\n")
		b.WriteString(strings.TrimRight(l.form.View(), "\n") + "\n")
		if l.problem != "" {
			b.WriteString("\n  " + badStyle.Render(clean(l.problem)) + "\n")
		}
		switch l.step {
		case stepSearching:
			b.WriteString("\n  " + dimStyle.Render("Searching MusicBrainz…") + "\n")
		case stepListening:
			b.WriteString("\n  " + dimStyle.Render(listening) + "\n")
		}
	case stepPick, stepMore, stepLoading:
		m.matchesBody(&b, h-2)
	case stepConfirm, stepSaving:
		m.diffBody(&b)
	}
	return b.String()
}

// lookupsOff says why there is no MusicBrainz to ask and how to turn it on;
// until says what can be done in the meantime.
func lookupsOff(b *strings.Builder, until string) {
	b.WriteString("  " + boldStyle.Render("MusicBrainz lookups are off") + "\n\n")
	b.WriteString("  MusicBrainz asks every program that uses it to say who is asking.\n")
	b.WriteString("  To turn lookups on, add your email address or a URL to the config file:\n\n")
	b.WriteString("      " + accentStyle.Render("musicbrainz_contact: you@example.com") + "\n\n")
	b.WriteString("  and start tagshelf tui again. Until then " + until + ".\n")
}

// showValues writes a field's values for a diff, clean.
func showValues(vs []string) string {
	if len(vs) == 0 {
		return "(none)"
	}
	return clean(strings.Join(vs, ", "))
}

// The width of a match's line without its flexible columns.
const matchFixedWidth = 2 + 3 + 4 + 2 + 13 + 7 + 6 + 8*2

// matchesBody draws the ranked matches in h lines.
func (m *Model) matchesBody(b *strings.Builder, h int) {
	l := m.look
	count := fmt.Sprintf("%s of %s", formatCount(int64(l.shown)), plural(int64(len(l.matches)), "match", "matches"))
	if l.hasMore() {
		count += "; m shows more"
	}
	if by := l.heardLine(time.Now()); by != "" {
		count += "; " + by
	}
	b.WriteString("  " + boldStyle.Render("Matches") + dimStyle.Render("  "+strings.TrimRight(cell(count, max(m.width-12, 20)), " ")) + "\n")
	flex := max(m.width-matchFixedWidth, 30)
	titleW, artistW := flex*36/100, flex*28/100
	releaseW := flex - titleW - artistW
	row := func(score, title, artist, release, year, country, format, pos, delta string) string {
		return fmt.Sprintf("%3s  %s  %s  %s  %4s  %-2s  %s  %7s  %6s",
			score, cell(title, titleW), cell(artist, artistW), cell(release, releaseW), year, cell(country, 2), cell(format, 13), pos, delta)
	}
	b.WriteString(dimStyle.Render("  "+row("", "Title", "Artist", "Release", "Year", "", "Format", "Track", "Length")) + "\n")

	rows := max(h-2, 1)
	if l.problem != "" || l.step != stepPick {
		rows = max(rows-2, 1)
	}
	first := window(l.cursor, rows)
	for i := first; i < l.shown && i < first+rows; i++ {
		c := l.matches[i]
		var release, year, country, pos, diff string
		if c.Release != nil {
			release, country = c.Release.Title, c.Release.Country
			if c.InLibrary {
				release += " (in library)"
			}
			if y := c.Release.Year(); y > 0 {
				year = strconv.Itoa(y)
			}
		}
		if c.Track > 0 {
			pos = strconv.Itoa(c.Track)
			if c.TrackTotal > 0 {
				pos += "/" + strconv.Itoa(c.TrackTotal)
			}
			if c.Disc > 1 {
				pos = strconv.Itoa(c.Disc) + "-" + pos
			}
		}
		if c.HasLengthDelta {
			diff = delta(c.LengthDelta)
		}
		score := strconv.Itoa(c.Score)
		if hit, ok := l.hit(i); ok {
			score = percent(hit.score)
		}
		line := row(score, c.Title(), c.Artist(), release, year, country, c.Format, pos, diff)
		if i == l.cursor {
			b.WriteString("› " + activeTab.Render(line) + "\n")
		} else {
			b.WriteString("  " + line + "\n")
		}
	}
	if l.problem != "" {
		b.WriteString("\n  " + badStyle.Render(clean(l.problem)) + "\n")
	}
	switch l.step {
	case stepLoading:
		b.WriteString("\n  " + dimStyle.Render("Reading the release from MusicBrainz…") + "\n")
	case stepMore:
		b.WriteString("\n  " + dimStyle.Render("Asking MusicBrainz for more…") + "\n")
	case stepListening:
		b.WriteString("\n  " + dimStyle.Render(listening) + "\n")
	}
}

// diffBody draws what the picked match would write, field by field.
func (m *Model) diffBody(b *strings.Builder) {
	l := m.look
	c := l.picked
	what := c.Title() + " by " + c.Artist()
	if c.Release != nil {
		what += ", on " + c.Release.Title
	}
	b.WriteString("  " + boldStyle.Render("Tag it as ") + clean(what) + "\n\n")
	values := m.lookupWrites(l)
	valueW := max((m.width-2-17-4)/2, 12)
	show := showValues
	changed := 0
	for _, f := range lookupFields {
		now, ok := values[f.field]
		if !ok {
			continue
		}
		old := l.orig[f.field]
		if strings.Join(old, "\x00") == strings.Join(now, "\x00") {
			b.WriteString(dimStyle.Render(fmt.Sprintf("  %-17s%s", f.name, cell(show(now), valueW))) + "\n")
			continue
		}
		changed++
		fmt.Fprintf(b, "  %-17s%s -> %s\n", f.name, dimStyle.Render(cell(show(old), valueW)), accentStyle.Render(show(now)))
	}
	b.WriteString("\n")
	mark := "musicbrainz, today"
	if cur := firstValue(l.orig[tagwrite.Marker]); cur != "" && changed == 0 {
		mark = clean(cur) + " (nothing to write)"
	}
	b.WriteString("  " + dimStyle.Render("Marker:        ") + mark + "\n")
	line, _ := m.placed(l)
	b.WriteString("  " + line + "\n")
	if l.step == stepSaving {
		b.WriteString("\n  " + dimStyle.Render("Saving…") + "\n")
	}
}
