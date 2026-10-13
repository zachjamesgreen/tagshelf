package tui

import (
	"fmt"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/zachjamesgreen/tagshelf/internal/store"
	"github.com/zachjamesgreen/tagshelf/internal/tagwrite"
)

// searchLimit is how many files a library search lists at most.
const searchLimit = 200

// libraryTab is the Library tab's state: a search over the library's index
// and the files it found.
type libraryTab struct {
	query  string // as typed
	typing bool   // keys go to the query

	searched string // the query the results are for, "" before any search
	results  []store.File
	problem  string // why the last search failed
	seq      int    // of the search going on; an answer to another is dropped
	waiting  bool
	// stale is set when the library changed while another tab showed: the
	// results are read again when this one shows.
	stale bool

	cursor int
	curID  int64 // the file under the cursor, followed across searches
}

// librarySearchMsg is what a search found.
type librarySearchMsg struct {
	seq   int
	query string
	files []store.File
	err   error
	// again is set for a search run again because the library changed, not
	// because a person asked.
	again bool
}

// libraryAlbumMsg is the files of the album a release match was asked for.
type libraryAlbumMsg struct {
	files []store.File
	err   error
}

// searchLibrary searches the library for query off the UI goroutine; again
// says the search is being repeated because the library changed.
func (m *Model) searchLibrary(query string, again bool) tea.Cmd {
	lib := &m.library
	lib.seq++
	lib.waiting, lib.stale = true, false
	seq, src := lib.seq, m.src
	return func() tea.Msg {
		files, err := src.Search(query, searchLimit+1)
		return librarySearchMsg{seq: seq, query: query, files: files, err: err, again: again}
	}
}

// librarySearched takes what a search found, keeping the cursor on its file.
func (m *Model) librarySearched(msg librarySearchMsg) {
	lib := &m.library
	if msg.seq != lib.seq {
		return
	}
	lib.waiting = false
	if msg.err != nil && msg.again {
		// The results in hand are better than none: the next change to the
		// library reads them again.
		m.setToast(false, "Cannot read the search results again: %v", msg.err)
		return
	}
	lib.searched = msg.query
	if msg.err != nil {
		lib.problem, lib.results = msg.err.Error(), nil
		return
	}
	lib.problem, lib.results = "", msg.files
	cursor := 0
	for i, f := range lib.results {
		if f.ID == lib.curID {
			cursor = i
		}
	}
	lib.moveTo(cursor)
}

// libraryReloaded searches again after the library changed, so that a file
// fixed from a result shows as it is now.
func (m *Model) libraryReloaded() tea.Cmd {
	lib := &m.library
	switch {
	case lib.searched == "" || lib.waiting:
		// Nothing to read again, or a search is on its way already.
		return nil
	case m.tab != TabLibrary:
		lib.stale = true
		return nil
	}
	return m.searchLibrary(lib.searched, true)
}

// libraryShown reads results that went stale while another tab showed.
func (m *Model) libraryShown() tea.Cmd {
	if lib := &m.library; m.tab == TabLibrary && lib.stale && lib.searched != "" && !lib.waiting {
		return m.searchLibrary(lib.searched, true)
	}
	return nil
}

// libraryPaste types pasted text into the query.
func (m *Model) libraryPaste(text string) {
	if lib := &m.library; m.tab == TabLibrary && lib.typing {
		lib.query += strings.Join(strings.Fields(text), " ")
	}
}

// shown are the results listed: no more than the limit.
func (lib *libraryTab) shown() []store.File {
	return lib.results[:min(len(lib.results), searchLimit)]
}

func (lib *libraryTab) moveTo(i int) {
	lib.cursor = max(min(i, len(lib.shown())-1), 0)
	lib.curID = 0
	if f, ok := lib.current(); ok {
		lib.curID = f.ID
	}
}

// current is the file under the cursor.
func (lib *libraryTab) current() (store.File, bool) {
	if s := lib.shown(); lib.cursor >= 0 && lib.cursor < len(s) {
		return s[lib.cursor], true
	}
	return store.File{}, false
}

// libraryKey handles a key on the Library tab, reporting whether it was one.
func (m *Model) libraryKey(k string) (tea.Cmd, bool) {
	lib := &m.library
	if lib.typing {
		switch k {
		case "esc":
			lib.typing = false
		case "enter":
			lib.typing = false
			if strings.TrimSpace(lib.query) == "" {
				// A search on its way is no longer wanted.
				lib.seq++
				lib.waiting, lib.stale = false, false
				lib.searched, lib.results, lib.problem = "", nil, ""
				return nil, true
			}
			return m.searchLibrary(lib.query, false), true
		case "backspace":
			if r := []rune(lib.query); len(r) > 0 {
				lib.query = string(r[:len(r)-1])
			}
		case "ctrl+u":
			lib.query = ""
		case "ctrl+c":
			return nil, false
		}
		// What a key types has gone into the query already; the other keys
		// mean nothing here.
		return nil, true
	}

	page := max(m.bodyHeight()-4, 1)
	f, ok := lib.current()
	switch k {
	case "/", "s":
		lib.typing = true
	case "up", "k":
		lib.moveTo(lib.cursor - 1)
	case "down", "j":
		lib.moveTo(lib.cursor + 1)
	case "pgup":
		lib.moveTo(lib.cursor - page)
	case "pgdown":
		lib.moveTo(lib.cursor + page)
	case "e":
		if ok {
			return m.openEditor(f), true
		}
	case "c":
		if ok {
			return m.coverArt(albumFolders([]string{f.Path}), false), true
		}
	case "l":
		if ok {
			return m.openLookup(f, ""), true
		}
	case "L":
		if ok {
			// The whole album: the files in the result's folder.
			src := m.src
			return func() tea.Msg {
				files, err := src.Album(f)
				return libraryAlbumMsg{files: files, err: err}
			}, true
		}
	default:
		return nil, false
	}
	return nil, true
}

// libraryAlbum opens a release match on the album read for it.
func (m *Model) libraryAlbum(msg libraryAlbumMsg) tea.Cmd {
	if m.tab != TabLibrary || m.edit != nil || m.look != nil || m.match != nil {
		return nil
	}
	switch {
	case msg.err != nil:
		m.setToast(false, "Cannot read the album: %v", msg.err)
		return nil
	case len(msg.files) == 0:
		m.setToast(false, "The album is no longer in the index: search again")
		return nil
	}
	// What cannot be tagged cannot be matched; the rest of the album can.
	var files []store.UntaggedFile
	for _, f := range msg.files {
		if tagwrite.Supported(f.Path) {
			files = append(files, store.UntaggedFile{File: f})
		}
	}
	if len(files) == 0 {
		m.setToast(false, "tagshelf cannot tag the files of this album")
		return nil
	}
	return m.openMatch(files)
}

// The width of a result's line without its flexible columns.
const libraryFixedWidth = 2 + 4 + 5 + 5 + 5 + 6*2

// libraryBody draws the Library tab in h lines.
func (m *Model) libraryBody(h int) string {
	lib := &m.library
	var b strings.Builder
	b.WriteString(boldStyle.Render("Library") + dimStyle.Render(fmt.Sprintf("  %s files", formatCount(m.snap.Overview.Library))) + "\n")
	prompt := "  Search: "
	switch {
	case lib.typing:
		b.WriteString(accentStyle.Render(prompt) + clean(lib.query) + "█" + "\n")
	case lib.query == "":
		b.WriteString(dimStyle.Render(prompt+"press / and type an artist, album or title") + "\n")
	default:
		b.WriteString(dimStyle.Render(prompt) + clean(lib.query) + "\n")
	}
	switch {
	case lib.problem != "":
		b.WriteString("\n  " + badStyle.Render("Cannot search the library: "+clean(lib.problem)) + "\n")
		return b.String()
	case lib.searched == "":
		if lib.waiting {
			b.WriteString("\n  " + dimStyle.Render("Searching…") + "\n")
		}
		return b.String()
	case len(lib.results) == 0:
		b.WriteString("\n  " + dimStyle.Render(fmt.Sprintf("Nothing in the library matches %q", lib.searched)) + "\n")
		return b.String()
	}

	shown := lib.shown()
	count := plural(int64(len(shown)), "file", "files")
	if len(lib.results) > searchLimit {
		count = fmt.Sprintf("the first %s; add a word to narrow it down", count)
	}
	b.WriteString(dimStyle.Render(fmt.Sprintf("  %s for %q", count, lib.searched)) + "\n")
	flex := max(m.width-libraryFixedWidth, 30)
	artistW, albumW := flex*28/100, flex*34/100
	titleW := flex - artistW - albumW
	row := func(artist, album, year, track, title, length, format string) string {
		return fmt.Sprintf("%s  %s  %4s  %5s  %s  %5s  %s", cell(artist, artistW), cell(album, albumW), year, track, cell(title, titleW), length, format)
	}
	b.WriteString(dimStyle.Render("  "+row("Artist", "Album", "Year", "Track", "Title", "Len", "")) + "\n")
	room := max(h-4, 1)
	first := window(lib.cursor, room)
	for i := first; i < len(shown) && i < first+room; i++ {
		f := shown[i]
		artist := f.Artist
		if artist == "" {
			artist = f.AlbumArtist
		}
		var year, track string
		if f.Year > 0 {
			year = strconv.Itoa(f.Year)
		}
		if f.Track > 0 {
			track = fmt.Sprintf("%02d", f.Track)
			if f.Disc > 0 && (f.DiscTotal > 1 || f.Disc > 1) {
				track = fmt.Sprintf("%d-%02d", f.Disc, f.Track)
			}
		}
		line := row(artist, f.Album, year, track, f.Title, length(f.Length), clean(f.Format))
		if i == lib.cursor && !lib.typing {
			b.WriteString("› " + activeTab.Render(line) + "\n")
		} else {
			b.WriteString("  " + line + "\n")
		}
	}
	return b.String()
}
