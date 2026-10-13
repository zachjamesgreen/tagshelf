package engine

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zachjamesgreen/tagshelf/internal/fixtures"
	"github.com/zachjamesgreen/tagshelf/internal/store"
)

var (
	oneTags  = id3Tags{albumArtist: "Singer", album: "Album", title: "One", track: "1"}
	twoTags  = id3Tags{albumArtist: "Singer", album: "Album", title: "Two", track: "2"}
	tuneTags = id3Tags{albumArtist: "Band", album: "Record", title: "Tune", track: "1"}
)

// playlistEvents are the playlists a run said it rewrote, with the entries
// of each, and its warnings about playlists.
func (c *collector) playlistEvents() (rewritten map[string]int, warnings []Warning) {
	c.mu.Lock()
	defer c.mu.Unlock()
	rewritten = map[string]int{}
	for _, e := range c.events {
		switch e := e.(type) {
		case PlaylistRewritten:
			rewritten[e.Path] = e.Entries
		case Warning:
			if e.Kind == WarnPlaylistEntries || e.Kind == WarnPlaylistEncoding {
				warnings = append(warnings, e)
			}
		}
	}
	return rewritten, warnings
}

// A playlist whose files are all beside it goes with them to their album,
// and its entries get their new names. Nothing else in it changes. Undoing
// the run puts the playlist back as it was, where it was.
func TestPlaylistFollowsItsAlbum(t *testing.T) {
	tmp := t.TempDir()
	src, lib := filepath.Join(tmp, "src"), filepath.Join(tmp, "lib")
	writeTaggedMP3(t, filepath.Join(src, "rip", "one.mp3"), oneTags, theAudio)
	writeTaggedMP3(t, filepath.Join(src, "rip", "two.mp3"), twoTags, "other "+theAudio)
	const before = "#EXTM3U\r\n#EXTINF:1,Singer - One\r\none.mp3\r\n\r\n#EXTINF:2,Singer - Two\r\ntwo.mp3\r\n"
	writeTestFile(t, filepath.Join(src, "rip", "album.m3u"), before)
	s := openStore(t)

	var c collector
	finish := Run(context.Background(), Options{Source: src, Destination: lib, Workers: 1, Store: s, Sink: c.sink})
	if finish.State != StateOK || finish.Counters.Playlists != 1 {
		t.Fatalf("finish = %+v, want one playlist rewritten", finish)
	}
	list := filepath.Join(lib, "Singer", "Album", "album.m3u")
	const after = "#EXTM3U\r\n#EXTINF:1,Singer - One\r\n01 - One.mp3\r\n\r\n#EXTINF:2,Singer - Two\r\n02 - Two.mp3\r\n"
	if got := readTestFile(t, list); got != after {
		t.Errorf("the playlist holds %q, want %q", got, after)
	}
	mustNotExist(t, filepath.Join(lib, "_Playlists"), list+".orig")
	if rewritten, warnings := c.playlistEvents(); rewritten[list] != 2 || len(warnings) != 0 {
		t.Errorf("rewritten = %v, warnings = %+v", rewritten, warnings)
	}
	run := lastRun(t, s)
	moves, err := s.Moves(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	var recorded int
	for _, m := range moves {
		if m.Reason == store.ReasonPlaylistRewrite && m.To == list && string(m.CueBefore) == before && string(m.CueAfter) == after {
			recorded++
		}
	}
	if recorded != 1 || run.Counters.Playlists != 1 {
		t.Errorf("the run recorded %d rewrites of the playlist, and counts %d", recorded, run.Counters.Playlists)
	}

	// A second run finds nothing to change.
	if again := Run(context.Background(), Options{Source: lib, Destination: lib, Workers: 1, Store: s}); again.Counters.Playlists != 0 {
		t.Errorf("a reorganize of the library rewrote %d playlists", again.Counters.Playlists)
	}

	check, restore := noTags(t)
	plan, err := PlanUndo(s, run.ID, check)
	if err != nil || plan.Playlists != 1 || len(plan.Skipped) != 0 {
		t.Fatalf("the undo's plan = %+v, %v", plan, err)
	}
	undone := Undo(context.Background(), Options{Destination: lib, Store: s}, run.ID, check, restore)
	if undone.State != StateOK || undone.Counters.Playlists != 1 {
		t.Fatalf("the undo = %+v", undone)
	}
	if got := readTestFile(t, filepath.Join(src, "rip", "album.m3u")); got != before {
		t.Errorf("after the undo the playlist holds %q, want what it held before", got)
	}
	mustExist(t, filepath.Join(src, "rip", "one.mp3"), filepath.Join(src, "rip", "two.mp3"))
}

// A playlist that names files of several folders goes to the playlists
// folder. Each entry keeps its style. What is not a file, or names a file
// that is not there, is left as it is and counted.
func TestPlaylistOfSeveralFoldersGoesToThePlaylistsFolder(t *testing.T) {
	tmp := t.TempDir()
	src, lib := filepath.Join(tmp, "src"), filepath.Join(tmp, "lib")
	one := filepath.Join(src, "rip1", "one.mp3")
	writeTaggedMP3(t, one, oneTags, theAudio)
	writeTaggedMP3(t, filepath.Join(src, "rip2", "tune.mp3"), tuneTags, "other "+theAudio)
	mix := "#EXTM3U\nrip1/one.mp3\nrip2\\tune.mp3\n" + one + "\nhttp://example.com/stream\nrip1/gone.mp3\n"
	writeTestFile(t, filepath.Join(src, "mix.m3u8"), mix)

	for _, dry := range []bool{true, false} {
		var c collector
		s := openStore(t)
		finish := Run(context.Background(), Options{Source: src, Destination: lib, Workers: 1, Store: s, Sink: c.sink, DryRun: dry})
		if finish.State != StateOK || finish.Counters.Playlists != 1 {
			t.Fatalf("dry %t: finish = %+v, want one playlist rewritten", dry, finish)
		}
		list := filepath.Join(lib, "_Playlists", "mix.m3u8")
		rewritten, warnings := c.playlistEvents()
		if rewritten[list] != 3 || len(warnings) != 1 || warnings[0].Kind != WarnPlaylistEntries || warnings[0].Count != 2 || warnings[0].Path != list {
			t.Errorf("dry %t: rewritten = %v, warnings = %+v; want three entries, and two left alone", dry, rewritten, warnings)
		}
		if dry {
			// Nothing is written, and nothing moves.
			if got := readTestFile(t, filepath.Join(src, "mix.m3u8")); got != mix {
				t.Errorf("the dry run changed the playlist: %q", got)
			}
			mustNotExist(t, lib)
			continue
		}
		want := "#EXTM3U\n../Singer/Album/01 - One.mp3\n..\\Band\\Record\\01 - Tune.mp3\n" +
			filepath.Join(lib, "Singer", "Album", "01 - One.mp3") + "\nhttp://example.com/stream\nrip1/gone.mp3\n"
		if got := readTestFile(t, list); got != want {
			t.Errorf("the playlist holds\n%q, want\n%q", got, want)
		}
		mustNotExist(t, filepath.Join(src, "mix.m3u8"))
	}
}

// A reorganize renames the files of the library, and the library's
// playlists follow: the album's own, and one in the playlists folder, which
// stays there. With rewriting off, they are left as they are.
func TestReorganizeRewritesTheLibrarysPlaylists(t *testing.T) {
	for _, off := range []bool{false, true} {
		lib := filepath.Join(t.TempDir(), "lib")
		album := filepath.Join(lib, "Singer", "Album")
		writeTaggedMP3(t, filepath.Join(album, "song.mp3"), songTags, theAudio)
		own, fav := filepath.Join(album, "album.m3u"), filepath.Join(lib, "_Playlists", "fav.m3u8")
		writeTestFile(t, own, "song.mp3\n")
		writeTestFile(t, fav, "../Singer/Album/song.mp3\n")
		s := openStore(t)
		finish := Run(context.Background(), Options{Source: lib, Destination: lib, Workers: 1, Store: s, NoPlaylistRewrite: off})
		if finish.State != StateOK {
			t.Fatalf("finish = %+v", finish)
		}
		mustExist(t, songPath(lib))
		wantOwn, wantFav, count := "01 - Song.mp3\n", "../Singer/Album/01 - Song.mp3\n", int64(2)
		if off {
			wantOwn, wantFav, count = "song.mp3\n", "../Singer/Album/song.mp3\n", 0
		}
		if got := readTestFile(t, own); got != wantOwn {
			t.Errorf("off %t: the album's playlist holds %q, want %q", off, got, wantOwn)
		}
		if got := readTestFile(t, fav); got != wantFav {
			t.Errorf("off %t: the playlist in the playlists folder holds %q, want %q", off, got, wantFav)
		}
		if finish.Counters.Playlists != count {
			t.Errorf("off %t: %d playlists rewritten, want %d", off, finish.Counters.Playlists, count)
		}
	}
}

// Without a database the playlists a run meets are still rewritten.
func TestPlaylistRewriteWithoutADatabase(t *testing.T) {
	lib := filepath.Join(t.TempDir(), "lib")
	album := filepath.Join(lib, "Singer", "Album")
	writeTaggedMP3(t, filepath.Join(album, "song.mp3"), songTags, theAudio)
	writeTestFile(t, filepath.Join(album, "album.m3u"), "song.mp3\n")
	finish := Run(context.Background(), Options{Source: lib, Destination: lib, Workers: 1})
	if finish.State != StateOK || finish.Counters.Playlists != 1 || readTestFile(t, filepath.Join(album, "album.m3u")) != "01 - Song.mp3\n" {
		t.Errorf("finish = %+v, the playlist holds %q", finish, readTestFile(t, filepath.Join(album, "album.m3u")))
	}
}

// A tag save that moves a file of the library has the playlists that name
// it follow, found by the index.
func TestTagSaveRewritesPlaylists(t *testing.T) {
	lib := filepath.Join(t.TempDir(), "lib")
	old := filepath.Join(lib, "Wrong Artist", "Wrong Album", "01 - Song.flac")
	writeTestFile(t, old, readTestFile(t, fixtures.Path(fixtures.RipperFLAC)))
	fav := filepath.Join(lib, "_Playlists", "fav.m3u")
	writeTestFile(t, fav, "#EXTM3U\r\n..\\Wrong Artist\\Wrong Album\\01 - Song.flac\r\n")
	other := filepath.Join(lib, "_Playlists", "other.m3u")
	writeTestFile(t, other, "../Somebody/Else/01 - Song.flac\n")
	s := openStore(t)
	indexLibrary(t, s, lib)
	id := indexedFiles(t, s)[old].ID

	var c collector
	finish := SaveTags(context.Background(), Options{Destination: lib, Store: s, Sink: c.sink}, []int64{id}, overwrite(t, fixtures.Manual(".flac")))
	if finish.State != StateOK || finish.Counters.Organized != 1 || finish.Counters.Playlists != 1 {
		t.Fatalf("finish = %+v, want the file organized and one playlist rewritten", finish)
	}
	dst := manualLibraryPath(lib, ".flac")
	rel, err := filepath.Rel(filepath.Dir(fav), dst)
	if err != nil {
		t.Fatal(err)
	}
	want := "#EXTM3U\r\n" + strings.ReplaceAll(filepath.ToSlash(rel), "/", "\\") + "\r\n"
	if got := readTestFile(t, fav); got != want {
		t.Errorf("the playlist holds %q, want %q", got, want)
	}
	if got := readTestFile(t, other); got != "../Somebody/Else/01 - Song.flac\n" {
		t.Errorf("a playlist that does not name the file was changed: %q", got)
	}
	if rewritten, _ := c.playlistEvents(); len(rewritten) != 1 || rewritten[fav] != 1 {
		t.Errorf("rewritten = %v", rewritten)
	}

	// The index follows, so a second move is followed too.
	naming, err := s.PlaylistsNaming([]string{store.PathKey(dst)})
	if err != nil || len(naming) != 1 || naming[0] != fav {
		t.Errorf("the index has %v naming the file at its new place (%v)", naming, err)
	}
}

// The entry for a copy set aside as a duplicate is pointed at the copy kept.
func TestPlaylistEntryOfADuplicatePointsAtTheKeptCopy(t *testing.T) {
	lib, kept, copyPath, _, s := reviewLibrary(t)
	fav := filepath.Join(lib, "_Playlists", "fav.m3u8")
	writeTestFile(t, fav, "../Singer/Album (bonus)/01 - Song.mp3\n../Singer/Album/01 - Song.mp3\n")
	indexLibrary(t, s, lib)
	g := onlyGroup(t, s)
	finish := ResolveDuplicates(context.Background(), Options{Destination: lib, Store: s},
		[]Resolution{{Kind: g.Kind, Members: g.IDs(), Keeper: indexedFiles(t, s)[kept].ID}})
	if finish.State != StateOK || finish.Counters.Duplicates != 1 || finish.Counters.Playlists != 1 {
		t.Fatalf("finish = %+v", finish)
	}
	mustNotExist(t, copyPath)
	if got := readTestFile(t, fav); got != "../Singer/Album/01 - Song.mp3\n../Singer/Album/01 - Song.mp3\n" {
		t.Errorf("the playlist holds %q, want both entries at the kept copy", got)
	}
}

// A .m3u that is not UTF-8 is Windows-1252 and is written back so. One that
// would need a name that encoding cannot hold is left as it is, with a
// warning.
func TestPlaylistInWindows1252(t *testing.T) {
	for _, tt := range []struct {
		name, title, want string
		warning           bool
	}{
		{"a name it can hold", "Sång", "#EXTINF:1,Bj\xf6rk\r\n01 - S\xe5ng.mp3\r\n", false},
		{"a name it cannot hold", "日本", "#EXTINF:1,Bj\xf6rk\r\nsong.mp3\r\n", true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			lib := filepath.Join(t.TempDir(), "lib")
			album := filepath.Join(lib, "Singer", "Album")
			writeTaggedMP3(t, filepath.Join(album, "song.mp3"), id3Tags{albumArtist: "Singer", album: "Album", title: tt.title, track: "1"}, theAudio)
			list := filepath.Join(album, "album.m3u")
			writeTestFile(t, list, "#EXTINF:1,Bj\xf6rk\r\nsong.mp3\r\n")
			var c collector
			finish := Run(context.Background(), Options{Source: lib, Destination: lib, Workers: 1, Store: openStore(t), Sink: c.sink})
			if finish.State != StateOK {
				t.Fatalf("finish = %+v", finish)
			}
			if got := readTestFile(t, list); got != tt.want {
				t.Errorf("the playlist holds %q, want %q", got, tt.want)
			}
			rewritten, warnings := c.playlistEvents()
			if tt.warning != (len(warnings) == 1 && warnings[0].Kind == WarnPlaylistEncoding) || tt.warning == (len(rewritten) == 1) {
				t.Errorf("rewritten = %v, warnings = %+v", rewritten, warnings)
			}
		})
	}
}

// A playlist edited since the run rewrote it keeps its text in an undo, and
// its entries follow the files the undo moves back.
func TestUndoFollowsAnEditedPlaylist(t *testing.T) {
	tmp := t.TempDir()
	src, lib := filepath.Join(tmp, "src"), filepath.Join(tmp, "lib")
	writeTaggedMP3(t, filepath.Join(src, "rip", "one.mp3"), oneTags, theAudio)
	writeTestFile(t, filepath.Join(src, "rip", "album.m3u"), "one.mp3\n")
	s := openStore(t)
	if finish := Run(context.Background(), Options{Source: src, Destination: lib, Workers: 1, Store: s}); finish.State != StateOK || finish.Counters.Playlists != 1 {
		t.Fatalf("organize = %+v", finish)
	}
	run := lastRun(t, s)
	list := filepath.Join(lib, "Singer", "Album", "album.m3u")
	writeTestFile(t, list, "# mine\n01 - One.mp3\n")

	check, restore := noTags(t)
	plan, err := PlanUndo(s, run.ID, check)
	if err != nil || plan.Playlists != 0 || len(plan.Skipped) != 1 || plan.Skipped[0].Path != list {
		t.Fatalf("the undo's plan = %+v, %v; want the edited playlist's text left", plan, err)
	}
	Undo(context.Background(), Options{Destination: lib, Store: s}, run.ID, check, restore)
	mustExist(t, filepath.Join(src, "rip", "one.mp3"))
	if got := readTestFile(t, filepath.Join(src, "rip", "album.m3u")); got != "# mine\none.mp3\n" {
		t.Errorf("after the undo the edited playlist holds %q, want its own text with the entry following the file", got)
	}
}

// The index keeps the list of the library's playlists: one that changed is
// read again, and one that is gone is dropped.
func TestIndexKeepsThePlaylists(t *testing.T) {
	lib := filepath.Join(t.TempDir(), "lib")
	writeTaggedMP3(t, songPath(lib), songTags, theAudio)
	fav := filepath.Join(lib, "_Playlists", "fav.m3u8")
	writeTestFile(t, fav, "../Singer/Album/01 - Song.mp3\n")
	s := openStore(t)
	indexLibrary(t, s, lib)
	naming := func(path string) []string {
		t.Helper()
		got, err := s.PlaylistsNaming([]string{store.PathKey(path)})
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	if got := naming(songPath(lib)); len(got) != 1 || got[0] != fav {
		t.Fatalf("the index has %v naming the song, want the playlist", got)
	}

	other := filepath.Join(lib, "Singer", "Album", "02 - Other.mp3")
	writeTestFile(t, fav, "../Singer/Album/02 - Other.mp3\n# and a longer file\n")
	indexLibrary(t, s, lib)
	if got := naming(songPath(lib)); len(got) != 0 {
		t.Errorf("after the playlist changed the index still has %v naming the song", got)
	}
	if got := naming(other); len(got) != 1 {
		t.Errorf("the index has %v naming the playlist's new entry", got)
	}

	if err := os.Remove(fav); err != nil {
		t.Fatal(err)
	}
	indexLibrary(t, s, lib)
	if got, err := s.Playlists(); err != nil || len(got) != 0 {
		t.Errorf("after the playlist was deleted the index holds %v (%v)", got, err)
	}
}
