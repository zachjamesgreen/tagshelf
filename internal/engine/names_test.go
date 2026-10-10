package engine

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
)

// The tests here are about names that differ only in letter case. Most of
// them hold on every system: on Windows the file system sees to it, and
// elsewhere tagshelf does (see ownFolding). The ones that start from two such
// names side by side need a file system that can hold both.

// A file that belongs in a folder the library has in another letter case
// goes into that folder. A second folder beside it would make a library that
// cannot be copied to a disk that ignores case.
func TestNewFilesJoinTheFolderThatIsThere(t *testing.T) {
	tmp := t.TempDir()
	source, lib := filepath.Join(tmp, "src"), filepath.Join(tmp, "lib")
	writeTaggedMP3(t, filepath.Join(lib, "abba", "gold", "01 - One.mp3"), id3Tags{albumArtist: "abba", album: "gold", title: "One", track: "1"}, "one")
	writeTaggedMP3(t, filepath.Join(source, "new.mp3"), id3Tags{albumArtist: "ABBA", album: "Gold", title: "Two", track: "2"}, "two")
	writeTestFile(t, filepath.Join(source, "cover.jpg"), "art")

	s := openStore(t)
	finish := Run(context.Background(), Options{Source: source, Destination: lib, Workers: 1, Store: s})
	if finish.State != StateOK || finish.Counters.Organized != 1 {
		t.Fatalf("run = %+v", finish)
	}
	want := []string{"abba/gold/01 - One.mp3", "abba/gold/02 - Two.mp3", "abba/gold/cover.jpg"}
	if got := exactNames(t, lib); !reflect.DeepEqual(got, want) {
		t.Fatalf("library = %q, want %q", got, want)
	}
	// The index knows the file where it is: nothing is left to read.
	if again := Run(context.Background(), Options{Destination: lib, IndexOnly: true, Store: s}); again.Counters.Indexed != 0 || again.State != StateOK {
		t.Errorf("the index run after it = %+v, want nothing to read", again)
	}
	if ownFolding {
		if _, ok := indexedFiles(t, s)[filepath.Join(lib, "abba", "gold", "02 - Two.mp3")]; !ok {
			t.Errorf("index = %v, want the row of the new song under the folder as it is spelled", indexedFiles(t, s))
		}
	}

	// And it goes back where it came from.
	check, restore := noTags(t)
	if undo := Undo(context.Background(), Options{Destination: lib, Store: s}, finish.RunID, check, restore); undo.State != StateOK {
		t.Fatalf("undo = %+v", undo)
	}
	if got, want := exactNames(t, lib), []string{"abba/gold/01 - One.mp3"}; !reflect.DeepEqual(got, want) {
		t.Errorf("library after the undo = %q, want %q", got, want)
	}
	mustExist(t, filepath.Join(source, "new.mp3"), filepath.Join(source, "cover.jpg"))
}

// Two new albums of one run whose tags differ only in letter case share a
// folder: the first of them names it. A preview says the same.
func TestOneRunMakesOneFolderForNamesThatDifferInCase(t *testing.T) {
	for _, dryRun := range []bool{true, false} {
		tmp := t.TempDir()
		source, lib := filepath.Join(tmp, "src"), filepath.Join(tmp, "lib")
		writeTaggedMP3(t, filepath.Join(source, "a.mp3"), id3Tags{albumArtist: "ABBA", album: "Gold", title: "One", track: "1"}, "one")
		writeTaggedMP3(t, filepath.Join(source, "b.mp3"), id3Tags{albumArtist: "abba", album: "gold", title: "Two", track: "2"}, "two")
		writeTaggedMP3(t, filepath.Join(source, "c.mp3"), id3Tags{albumArtist: "Abba", album: "GOLD", title: "one", track: "1"}, "three")

		var c collector
		finish := Run(context.Background(), Options{Source: source, Destination: lib, Workers: 1, DryRun: dryRun, Sink: c.sink})
		if finish.State != StateOK || finish.Counters.Organized != 3 {
			t.Fatalf("dry run %t: run = %+v", dryRun, finish)
		}
		want := []string{"ABBA/Gold/01 - One.mp3", "ABBA/Gold/01 - one (1).mp3", "ABBA/Gold/02 - Two.mp3"}
		if dryRun {
			if !ownFolding {
				// Windows makes them one folder when they are made; the
				// preview names each as its tags do.
				continue
			}
			var got []string
			for _, dst := range c.destinations() {
				rel, _ := filepath.Rel(lib, dst)
				got = append(got, filepath.ToSlash(rel))
			}
			if !reflect.DeepEqual(got, want) {
				t.Errorf("the preview puts the files at %q, want %q", got, want)
			}
			continue
		}
		if got := exactNames(t, lib); !reflect.DeepEqual(got, want) {
			t.Errorf("library = %q, want %q", got, want)
		}
	}
}

// A file whose name is taken in another letter case is a clash like any
// other: it gets a " (1)" name, and the file that was there is not touched.
func TestANameTakenInAnotherCaseIsAClash(t *testing.T) {
	tmp := t.TempDir()
	source, lib := filepath.Join(tmp, "src"), filepath.Join(tmp, "lib")
	there := filepath.Join(lib, "Singer", "Album", "01 - Song.mp3")
	writeTaggedMP3(t, there, songTags, theAudio)
	was := readTestFile(t, there)
	writeTaggedMP3(t, filepath.Join(source, "new.mp3"), id3Tags{albumArtist: "singer", album: "album", title: "song", track: "1"}, "another recording")

	finish := Run(context.Background(), Options{Source: source, Destination: lib, Workers: 1})
	if finish.State != StateOK || finish.Counters.Organized != 1 {
		t.Fatalf("run = %+v", finish)
	}
	want := []string{"Singer/Album/01 - Song.mp3", "Singer/Album/01 - song (1).mp3"}
	if got := exactNames(t, lib); !reflect.DeepEqual(got, want) {
		t.Errorf("library = %q, want %q", got, want)
	}
	if readTestFile(t, there) != was {
		t.Error("the file that was there was written over")
	}
}

// A reorganize that renames a folder to the case its tags ask for takes
// along the files it moved into that folder in the same run: the index
// knows each of them under the new name, and undo puts all of it back.
func TestCaseFixTakesAlongWhatMovedIn(t *testing.T) {
	lib := filepath.Join(t.TempDir(), "lib")
	writeTaggedMP3(t, filepath.Join(lib, "abba", "Gold", "01 - One.mp3"), id3Tags{albumArtist: "ABBA", album: "Gold", title: "One", track: "1"}, "one")
	writeTaggedMP3(t, filepath.Join(lib, "Loose", "x.mp3"), id3Tags{albumArtist: "ABBA", album: "Gold", title: "Two", track: "2"}, "two")
	writeTestFile(t, filepath.Join(lib, "abba", "Gold", "album.cue"), "FILE \"01 - One.mp3\" MP3\n")

	s := openStore(t)
	finish := Run(context.Background(), Options{Source: lib, Destination: lib, Workers: 1, Store: s})
	if finish.State != StateOK {
		t.Fatalf("reorganize = %+v", finish)
	}
	want := []string{"ABBA/Gold/01 - One.mp3", "ABBA/Gold/02 - Two.mp3", "ABBA/Gold/album.cue"}
	if got := exactNames(t, lib); !reflect.DeepEqual(got, want) {
		t.Fatalf("library = %q, want %q", got, want)
	}
	rows := indexedFiles(t, s)
	for _, name := range want[:2] {
		if _, ok := rows[filepath.Join(lib, filepath.FromSlash(name))]; !ok {
			t.Errorf("index = %v, want a row for %s as it is spelled now", rows, name)
		}
	}
	if again := Run(context.Background(), Options{Destination: lib, IndexOnly: true, Store: s}); again.Counters.Indexed != 0 || again.State != StateOK {
		t.Errorf("the index run after it = %+v, want nothing to read", again)
	}
	// A second reorganize finds everything in place.
	if again := Run(context.Background(), Options{Source: lib, Destination: lib, Workers: 1, Store: s}).Counters; again.InPlace != 3 || again.Organized != 0 || again.Retried != 0 {
		t.Errorf("second reorganize = %+v, want all 3 in place", again)
	}

	check, restore := noTags(t)
	if undo := Undo(context.Background(), Options{Destination: lib, Store: s}, finish.RunID, check, restore); undo.State != StateOK {
		t.Fatalf("undo = %+v", undo)
	}
	back := []string{"Loose/x.mp3", "abba/Gold/01 - One.mp3", "abba/Gold/album.cue"}
	if got := exactNames(t, lib); !reflect.DeepEqual(got, back) {
		t.Errorf("library after the undo = %q, want %q", got, back)
	}
}

// Two folders that differ only in letter case, as a library made on Linux by
// hand or by another program can hold. The one that sorts first is the
// folder. A run that brings in a file leaves what is there alone, puts the
// file in that one and says so. A reorganize moves the other's files into
// it, and undo moves them back.
func TestFoldersThatDifferOnlyInCase(t *testing.T) {
	tmp := t.TempDir()
	if !caseSensitive(t, tmp) {
		t.Skip("this file system cannot hold two names that differ only in case")
	}
	source, lib := filepath.Join(tmp, "src"), filepath.Join(tmp, "lib")
	writeTaggedMP3(t, filepath.Join(lib, "The Beatles", "Help", "01 - One.mp3"), id3Tags{albumArtist: "The Beatles", album: "Help", title: "One", track: "1"}, "one")
	writeTaggedMP3(t, filepath.Join(lib, "the beatles", "Help", "02 - Two.mp3"), id3Tags{albumArtist: "the beatles", album: "Help", title: "Two", track: "2"}, "two")
	writeTaggedMP3(t, filepath.Join(source, "new.mp3"), id3Tags{albumArtist: "the beatles", album: "help", title: "Three", track: "3"}, "three")

	s := openStore(t)
	var c collector
	finish := Run(context.Background(), Options{Source: source, Destination: lib, Workers: 1, Store: s, Sink: c.sink})
	if finish.State != StateOK || finish.Counters.Organized != 1 {
		t.Fatalf("run = %+v", finish)
	}
	want := []string{"The Beatles/Help/01 - One.mp3", "The Beatles/Help/03 - Three.mp3", "the beatles/Help/02 - Two.mp3"}
	if got := exactNames(t, lib); !reflect.DeepEqual(got, want) {
		t.Fatalf("library = %q, want %q: the new file in the folder that sorts first, the rest left", got, want)
	}
	if w := c.warnings(WarnCaseTwins); len(w) != 1 || w[0].Path != filepath.Join(lib, "The Beatles") || w[0].Name != "the beatles" {
		t.Errorf("warnings = %+v, want one that names the two folders", w)
	}

	var again collector
	reorganize := Run(context.Background(), Options{Source: lib, Destination: lib, Workers: 1, Store: s, Sink: again.sink})
	if reorganize.State != StateOK || reorganize.Counters.Organized != 1 || reorganize.Counters.Errors != 0 {
		t.Fatalf("reorganize = %+v", reorganize)
	}
	merged := []string{"The Beatles/Help/01 - One.mp3", "The Beatles/Help/02 - Two.mp3", "The Beatles/Help/03 - Three.mp3"}
	if got := exactNames(t, lib); !reflect.DeepEqual(got, merged) {
		t.Fatalf("library after the reorganize = %q, want %q", got, merged)
	}
	mustNotExist(t, filepath.Join(lib, "the beatles"))
	if w := again.warnings(WarnCaseTwins); len(w) != 1 {
		t.Errorf("warnings of the reorganize = %+v, want the two folders named once", w)
	}
	if rows := indexedFiles(t, s); len(rows) != 3 {
		t.Errorf("index = %v, want the three songs", rows)
	} else if _, ok := rows[filepath.Join(lib, "The Beatles", "Help", "02 - Two.mp3")]; !ok {
		t.Errorf("index = %v, want the song that moved under its new folder", rows)
	}

	// Their tags still disagree on the spelling, so nothing flips: the next
	// reorganize leaves all three where they are, and has no pair to name.
	var third collector
	if next := Run(context.Background(), Options{Source: lib, Destination: lib, Workers: 1, Store: s, Sink: third.sink}).Counters; next.InPlace != 3 || next.Organized != 0 || next.Retried != 0 {
		t.Errorf("the reorganize after it = %+v, want all 3 in place", next)
	}
	if got := exactNames(t, lib); !reflect.DeepEqual(got, merged) {
		t.Errorf("library after the third run = %q, want %q", got, merged)
	}
	if w := third.warnings(WarnCaseTwins); len(w) != 0 {
		t.Errorf("warnings with one folder left = %+v", w)
	}

	check, restore := noTags(t)
	if undo := Undo(context.Background(), Options{Destination: lib, Store: s}, reorganize.RunID, check, restore); undo.State != StateOK {
		t.Fatalf("undo of the reorganize = %+v", undo)
	}
	if got := exactNames(t, lib); !reflect.DeepEqual(got, want) {
		t.Errorf("library after the undo = %q, want %q", got, want)
	}
}

// Two files in one folder that differ only in letter case. A reorganize
// gives the second a " (1)" name. Neither is ever written over, also not
// when the tags of the first ask for the very name the second has.
func TestFilesThatDifferOnlyInCase(t *testing.T) {
	tmp := t.TempDir()
	if !caseSensitive(t, tmp) {
		t.Skip("this file system cannot hold two names that differ only in case")
	}
	lib := filepath.Join(tmp, "lib")
	first := filepath.Join(lib, "Singer", "Album", "01 - Song.mp3")
	second := filepath.Join(lib, "Singer", "Album", "01 - song.mp3")
	lower := id3Tags{albumArtist: "Singer", album: "Album", title: "song", track: "1"}
	// Both ask for "01 - song.mp3", which is the name the second has.
	writeTaggedMP3(t, first, lower, "the first recording")
	writeTaggedMP3(t, second, lower, "the second recording")
	wasFirst, wasSecond := readTestFile(t, first), readTestFile(t, second)

	s := openStore(t)
	var c collector
	finish := Run(context.Background(), Options{Source: lib, Destination: lib, Workers: 1, Store: s, Sink: c.sink})
	if finish.State != StateOK || finish.Counters.Errors != 0 {
		t.Fatalf("reorganize = %+v", finish)
	}
	want := []string{"Singer/Album/01 - song (1).mp3", "Singer/Album/01 - song.mp3"}
	if got := exactNames(t, lib); !reflect.DeepEqual(got, want) {
		t.Fatalf("library = %q, want %q", got, want)
	}
	// The first has the name its tags ask for, the second the name beside it.
	if got := readTestFile(t, filepath.Join(lib, "Singer", "Album", "01 - song.mp3")); got != wasFirst {
		t.Error("01 - song.mp3 does not hold the first recording")
	}
	if got := readTestFile(t, filepath.Join(lib, "Singer", "Album", "01 - song (1).mp3")); got != wasSecond {
		t.Error("01 - song (1).mp3 does not hold the second recording: a file was written over")
	}
	if w := c.warnings(WarnCaseTwins); len(w) != 1 || w[0].Name != "01 - song.mp3" {
		t.Errorf("warnings = %+v, want the two files named", w)
	}
	if again := Run(context.Background(), Options{Destination: lib, IndexOnly: true, Store: s}); again.Counters.Indexed != 0 || again.State != StateOK {
		t.Errorf("the index run after it = %+v, want nothing to read", again)
	}
	// It settles: the next reorganize moves nothing.
	if next := Run(context.Background(), Options{Source: lib, Destination: lib, Workers: 1, Store: s}).Counters; next.InPlace != 2 || next.Organized != 0 || next.Retried != 0 {
		t.Errorf("the reorganize after it = %+v, want both in place", next)
	}
	if got := exactNames(t, lib); !reflect.DeepEqual(got, want) {
		t.Errorf("library after the second reorganize = %q, want %q", got, want)
	}
}

// A file is not renamed onto a name that is taken: where a folder can hold
// "Song" and "song", renaming the one to the case of the other would write
// over it.
func TestCaseVariantIsNotANameThatIsThere(t *testing.T) {
	dir := t.TempDir()
	writeTestFile(t, filepath.Join(dir, "Song.mp3"), "one")
	if got, err := caseVariant(dir, "song.mp3"); err != nil || got != "Song.mp3" {
		t.Errorf("caseVariant = %q, %v; want the name in its other case", got, err)
	}
	if got, err := caseVariant(dir, "Song.mp3"); err != nil || got != "" {
		t.Errorf("caseVariant of a name that is there = %q, %v; want none", got, err)
	}
	if got, err := caseVariant(dir, "other.mp3"); err != nil || got != "" {
		t.Errorf("caseVariant of a name that is not there in any case = %q, %v", got, err)
	}
	if !caseSensitive(t, dir) {
		return
	}
	writeTestFile(t, filepath.Join(dir, "song.mp3"), "two")
	if got, err := caseVariant(dir, "song.mp3"); err != nil || got != "" {
		t.Errorf("caseVariant with both names there = %q, %v; want none, so that neither is renamed onto the other", got, err)
	}
	if did, err := renameCase(filepath.Join(dir, "song.mp3")); did || err != nil {
		t.Errorf("renameCase with both names there = %t, %v", did, err)
	}
	if readTestFile(t, filepath.Join(dir, "Song.mp3")) != "one" || readTestFile(t, filepath.Join(dir, "song.mp3")) != "two" {
		t.Error("a file was written over")
	}
}

// TestNamesResolve checks how a path is spelled from what its folders hold.
func TestNamesResolve(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "The Beatles", "Help"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(root, "The Beatles", "Help", "01 - One.mp3"), "one")
	var n names
	rel := func(parts ...string) string { return filepath.Join(parts...) }

	tests := []struct{ ask, want string }{
		{rel("the beatles", "HELP", "01 - one.MP3"), rel("The Beatles", "Help", "01 - One.mp3")},
		{rel("The Beatles", "Help", "02 - Two.mp3"), rel("The Beatles", "Help", "02 - Two.mp3")},
		{rel("THE BEATLES", "Revolver", "01 - Taxman.mp3"), rel("The Beatles", "Revolver", "01 - Taxman.mp3")},
		// What a path before this one planned is there for the next.
		{rel("the beatles", "revolver", "02 - Eleanor Rigby.mp3"), rel("The Beatles", "Revolver", "02 - Eleanor Rigby.mp3")},
		{rel("Björk", "Post", "01.mp3"), rel("Björk", "Post", "01.mp3")},
		{rel("BJÖRK", "post", "02.mp3"), rel("Björk", "Post", "02.mp3")},
		// Letters with and without an accent are different letters.
		{rel("Bjork", "Post", "01.mp3"), rel("Bjork", "Post", "01.mp3")},
	}
	for _, tt := range tests {
		if got := n.resolve(root, tt.ask, true); got != filepath.Join(root, tt.want) {
			t.Errorf("resolve(%q) = %q, want %q", tt.ask, got, filepath.Join(root, tt.want))
		}
	}
	// Looking a path up plans nothing.
	if got := n.resolve(root, rel("Nina", "x.mp3"), false); got != filepath.Join(root, "Nina", "x.mp3") {
		t.Errorf("resolve of a path that is not there = %q", got)
	}
	if got := n.resolve(root, rel("NINA", "x.mp3"), true); got != filepath.Join(root, "NINA", "x.mp3") {
		t.Errorf("resolve after a look-up = %q, want the spelling asked for: the look-up planned nothing", got)
	}
	// A folder renamed is known by its new name, with what is in it.
	if err := os.Rename(filepath.Join(root, "The Beatles"), filepath.Join(root, "the beatles")); err != nil {
		t.Fatal(err)
	}
	n.renamed(root, "The Beatles", "the beatles")
	if got, want := n.resolve(root, rel("THE BEATLES", "help", "01 - one.mp3"), false), filepath.Join(root, "the beatles", "Help", "01 - One.mp3"); got != want {
		t.Errorf("resolve after the rename = %q, want %q", got, want)
	}
	if twins := n.takeTwins(); len(twins) != 0 {
		t.Errorf("twins = %+v, want none", twins)
	}
}

// Of two names that differ only in case the one that sorts first is used,
// and for the name of a folder the first that is a folder.
func TestNamesResolveTwins(t *testing.T) {
	root := t.TempDir()
	if !caseSensitive(t, root) {
		t.Skip("this file system cannot hold two names that differ only in case")
	}
	for _, dir := range []string{"abba", "ABBA", "Abba"} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeTestFile(t, filepath.Join(root, "QUEEN"), "a file, where a folder is asked for")
	if err := os.MkdirAll(filepath.Join(root, "Queen"), 0o755); err != nil {
		t.Fatal(err)
	}
	var n names
	if got, want := n.resolve(root, filepath.Join("aBBa", "x.mp3"), true), filepath.Join(root, "ABBA", "x.mp3"); got != want {
		t.Errorf("resolve = %q, want %q", got, want)
	}
	if got, want := n.resolve(root, filepath.Join("queen", "x.mp3"), true), filepath.Join(root, "Queen", "x.mp3"); got != want {
		t.Errorf("resolve = %q, want %q: the folder, not the file that sorts before it", got, want)
	}
	if got, want := n.resolve(root, "queen", true), filepath.Join(root, "QUEEN"); got != want {
		t.Errorf("resolve of a file name = %q, want %q", got, want)
	}
	n.resolve(root, filepath.Join("abba", "y.mp3"), true)
	twins := n.takeTwins()
	var got []string
	for _, tw := range twins {
		rel, _ := filepath.Rel(root, tw.used)
		got = append(got, rel+" over "+tw.other)
	}
	want := []string{"ABBA over Abba", "ABBA over abba", "Queen over QUEEN"}
	if strings.Join(got, ", ") != strings.Join(want, ", ") {
		t.Errorf("twins = %q, want %q, each told once", got, want)
	}
}

// destinations lists where the files a run placed went, or in a preview
// would go, in order.
func (c *collector) destinations() []string {
	var out []string
	for _, e := range c.events {
		if p, ok := e.(FilePlaced); ok {
			out = append(out, p.Destination)
		}
	}
	sort.Strings(out)
	return out
}
