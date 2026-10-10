package engine

import (
	"bytes"
	"context"
	"encoding/binary"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"tagshelf/internal/fixtures"
	"tagshelf/internal/store"
)

// A copy put aside in _Duplicates stays there through later reorganizes,
// with and without a database: the walk of the library leaves the folder
// out, or every copy that was put aside would return on the next run.
func TestReorganizeLeavesDuplicatesFolderAlone(t *testing.T) {
	for _, withStore := range []bool{false, true} {
		lib := filepath.Join(t.TempDir(), "lib")
		writeTaggedMP3(t, songPath(lib), songTags, theAudio)
		aside := filepath.Join(lib, "_Duplicates", "Rip", "copy.mp3")
		writeTaggedMP3(t, aside, id3Tags{albumArtist: "Singer", album: "Album", title: "Other Title", track: "2"}, theAudio)
		art := filepath.Join(lib, "_Duplicates", "Rip", "cover.jpg")
		writeTestFile(t, art, "art")
		opts := Options{Source: lib, Destination: lib, Workers: 1}
		if withStore {
			opts.Store = openStore(t)
		}
		finish := Run(context.Background(), opts)
		if finish.State != StateOK || finish.Counters.TotalFound != 1 || finish.Counters.Organized != 0 || finish.Counters.OtherFound != 0 {
			t.Errorf("with a database %t: reorganize = %+v, want only the library's song found, in place", withStore, finish)
		}
		mustExist(t, aside, art, songPath(lib))
		mustNotExist(t, filepath.Join(lib, "Singer", "Album", "02 - Other Title.mp3"))
	}
}

// An organize whose source folder holds the library organizes only what lies
// outside the library: the walk of the source leaves the library out, and
// its untagged, other and duplicates folders.
func TestOrganizeFromAFolderHoldingTheLibrary(t *testing.T) {
	for _, withStore := range []bool{false, true} {
		source := filepath.Join(t.TempDir(), "music")
		lib := filepath.Join(source, "Library")
		handPlaced := filepath.Join(lib, "Hand Placed", "track.mp3")
		writeTestFile(t, handPlaced, "no tags here")
		notes := filepath.Join(lib, "Hand Placed", "notes.txt")
		writeTestFile(t, notes, "notes")
		aside := filepath.Join(lib, "_Duplicates", "Rip", "copy.mp3")
		writeTaggedMP3(t, aside, id3Tags{albumArtist: "Singer", album: "Album", title: "Other Title", track: "2"}, "aside audio")
		emptyAlbum := filepath.Join(lib, "Empty Artist", "Empty Album")
		if err := os.MkdirAll(emptyAlbum, 0755); err != nil {
			t.Fatal(err)
		}
		writeTaggedMP3(t, filepath.Join(source, "New", "song.mp3"), songTags, theAudio)

		opts := Options{Source: source, Destination: lib, Workers: 1}
		if withStore {
			opts.Store = openStore(t)
		}
		finish := Run(context.Background(), opts)
		mustExist(t, handPlaced, notes, aside, songPath(lib), emptyAlbum)
		if finish.Counters.TotalFound != 1 || finish.Counters.Organized != 1 || finish.Counters.OtherFound != 0 || finish.Counters.Errors != 0 {
			t.Errorf("with a database %t: counters = %+v, want the one new song found and organized", withStore, finish.Counters)
		}
	}
}

// volumeRootAt makes dir count as the top of a volume for the length of the
// test.
func volumeRootAt(t *testing.T, dir string) {
	t.Helper()
	was := atVolumeRoot
	t.Cleanup(func() { atVolumeRoot = was })
	atVolumeRoot = func(d string) bool { return d == dir }
}

// When a walked folder is the top of a volume, the folders Windows keeps
// there are left out. The recycle bin holds deleted songs as whole files
// with their tags: a reorganize of a library at M:\ used to take them back
// among the albums. A folder of the same name anywhere else is like any
// other.
func TestWalkSkipsSystemFoldersAtAVolumeRoot(t *testing.T) {
	lib := filepath.Join(t.TempDir(), "M")
	volumeRootAt(t, lib)
	binned := filepath.Join(lib, "$RECYCLE.BIN", "S-1-5-21", "$R1ABCDE.mp3")
	writeTaggedMP3(t, binned, id3Tags{albumArtist: "Singer", album: "Album", title: "Deleted", track: "9"}, "deleted audio")
	writeTestFile(t, filepath.Join(lib, "$Recycle.Bin", "S-1-5-21", "desktop.ini"), "ini")
	system := filepath.Join(lib, "System Volume Information", "tracking.log")
	writeTestFile(t, system, "log")
	writeTaggedMP3(t, filepath.Join(lib, "dropped.mp3"), songTags, theAudio)
	// Not at the top of the volume: an album that happens to have the name.
	writeTaggedMP3(t, filepath.Join(lib, "Old", "System Volume Information", "b.mp3"),
		id3Tags{albumArtist: "Singer", album: "Album", title: "Other", track: "2"}, "other audio")

	s := openStore(t)
	finish := Run(context.Background(), Options{Source: lib, Destination: lib, Workers: 1, Store: s})
	if finish.State != StateOK || finish.Counters.TotalFound != 2 || finish.Counters.Organized != 2 || finish.Counters.OtherFound != 0 {
		t.Errorf("reorganize = %+v, want the two songs outside the system folders organized", finish)
	}
	mustExist(t, binned, system, songPath(lib), filepath.Join(lib, "Singer", "Album", "02 - Other.mp3"))
	mustNotExist(t, filepath.Join(lib, "Singer", "Album", "09 - Deleted.mp3"))
	if rows := indexedFiles(t, s); len(rows) != 2 {
		t.Errorf("index = %v, want the two songs and nothing from the recycle bin", rows)
	}

	if isSystemFolder(filepath.Join(lib, "Music")) || !isSystemFolder(filepath.Join(lib, "system volume information")) {
		t.Error("isSystemFolder does not go by the two names, in any case")
	}
}

// oggCRC is the checksum of an Ogg page.
func oggCRC(b []byte) uint32 {
	var crc uint32
	for _, x := range b {
		crc ^= uint32(x) << 24
		for i := 0; i < 8; i++ {
			if crc&0x80000000 != 0 {
				crc = crc<<1 ^ 0x04c11db7
			} else {
				crc <<= 1
			}
		}
	}
	return crc
}

// opusFile renders an Ogg Opus stream with the comments given and one packet
// of audio.
func opusFile(audio string, comments ...string) []byte {
	page := func(flags byte, sequence uint32, packet []byte) []byte {
		b := []byte("OggS")
		b = append(b, 0, flags)
		b = binary.LittleEndian.AppendUint64(b, 0)
		b = binary.LittleEndian.AppendUint32(b, 7)
		b = binary.LittleEndian.AppendUint32(b, sequence)
		b = append(b, 0, 0, 0, 0, 1, byte(len(packet)))
		b = append(b, packet...)
		binary.LittleEndian.PutUint32(b[22:], oggCRC(b))
		return b
	}
	tags := []byte("OpusTags")
	tags = binary.LittleEndian.AppendUint32(tags, 1)
	tags = append(tags, 'v')
	tags = binary.LittleEndian.AppendUint32(tags, uint32(len(comments)))
	for _, c := range comments {
		tags = binary.LittleEndian.AppendUint32(tags, uint32(len(c)))
		tags = append(tags, c...)
	}
	return slices.Concat(
		page(2, 0, []byte("OpusHead\x01\x02\x38\x01\x80\xbb\x00\x00\x00\x00\x00")),
		page(0, 1, tags),
		page(4, 2, []byte(audio)))
}

// .opus, .oga and .dsf files are audio: found as such, read into the index
// with a hash of their audio alone, and placed by their tags. They used to
// be "non-audio files": moved to _Other, or into an album beside its audio,
// and listed nowhere.
func TestMoreAudioExtensions(t *testing.T) {
	tmp := t.TempDir()
	source, lib := filepath.Join(tmp, "src"), filepath.Join(tmp, "lib")
	qualifying := []string{"TITLE=Title", "ARTIST=Artist", "ALBUM=Album", "TRACKNUMBER=4", "MUSICBRAINZ_ALBUMID=11111111-1111-4111-8111-111111111111"}
	opus := filepath.Join(source, "a", "song.opus")
	writeTestFile(t, opus, string(opusFile("opus audio packet", qualifying...)))
	// The same audio under other tags: the hash must not see the difference.
	retagged := opusFile("opus audio packet", "TITLE=Another title altogether")
	oga := filepath.Join(source, "b", "song.oga")
	writeTestFile(t, oga, readTestFile(t, fixtures.Tagged(".ogg")))
	dsf := filepath.Join(source, "c", "sample.dsf")
	writeTestFile(t, dsf, readTestFile(t, filepath.Join("..", "tag", "testdata", "with_tags", "sample.dsf")))
	dsfSum := wantSum(t, dsf)
	// A DSF file keeps its tags after its audio, where the eight bytes at 20
	// point: a change there is a change of tags.
	data := []byte(readTestFile(t, dsf))
	at := binary.LittleEndian.Uint64(data[20:28])
	if at == 0 || at+40 > uint64(len(data)) {
		t.Fatalf("sample.dsf has its tags at %d of %d bytes", at, len(data))
	}
	edited := bytes.Clone(data)
	edited[len(edited)-1] ^= 0xff
	editedPath := filepath.Join(tmp, "edited.dsf")
	writeTestFile(t, editedPath, string(edited))
	if got := wantSum(t, editedPath); got != dsfSum {
		t.Errorf("the hash of a DSF file changed with its tags: %s, was %s", got, dsfSum)
	}
	retaggedPath := filepath.Join(tmp, "retagged.opus")
	writeTestFile(t, retaggedPath, string(retagged))
	if a, b := wantSum(t, opus), wantSum(t, retaggedPath); a != b {
		t.Errorf("the hash of an Opus file changed with its tags: %s and %s", a, b)
	}
	// What stays out: the reader knows neither container.
	aiff := filepath.Join(source, "d", "song.aiff")
	writeTestFile(t, aiff, "FORM\x00\x00\x00\x04AIFF")
	wv := filepath.Join(source, "d", "song.wv")
	writeTestFile(t, wv, "wvpk")

	s := openStore(t)
	finish := Run(context.Background(), Options{Source: source, Destination: lib, Workers: 1, Store: s})
	if finish.Counters.TotalFound != 3 || finish.Counters.OtherFound != 2 || finish.Counters.Errors != 0 {
		t.Fatalf("run = %+v, want the three found as audio and .aiff and .wv as other files", finish)
	}
	placedOpus := filepath.Join(lib, "Artist", "Album", "04 - Title.opus")
	placedDSF := filepath.Join(lib, "_Untagged", "sample.dsf")
	mustExist(t, placedOpus, placedDSF, filepath.Join(lib, "_Other", "d", "song.aiff"), filepath.Join(lib, "_Other", "d", "song.wv"))
	rows := indexedFiles(t, s)
	var placedOGA string
	for path := range rows {
		if strings.HasSuffix(path, ".oga") {
			placedOGA = path
		}
	}
	if len(rows) != 3 || placedOGA == "" || rows[placedOGA].RootKind != store.RootLibrary || rows[placedOGA].Title != fixtures.Title {
		t.Fatalf("index = %v, want the three files, the .oga in the library with its tags", rows)
	}
	if rows[placedOGA].Sum != wantSum(t, fixtures.Untagged(".ogg")) {
		t.Errorf("the .oga's hash is not that of the same audio without tags")
	}
	if row := rows[placedOpus]; row.Sum == "" || row.Title != "Title" || row.Track != 4 {
		t.Errorf("row of the .opus = %+v, want its tags and a hash", row)
	}
	if row := rows[placedDSF]; row.Sum != dsfSum || row.Format != "DSF" || row.Title == "" {
		t.Errorf("row of the .dsf = %+v, want its tags and the hash of its audio", row)
	}
}

// staleCopies replaces what a run is given to remove a tag save's stale
// copies with: it records the folders it was called for, and removes the
// files named like a save's copy, as tagwrite does.
func staleCopies(calls *[]string) func(dir string) ([]string, error) {
	return func(dir string) ([]string, error) {
		*calls = append(*calls, dir)
		var removed []string
		entries, err := os.ReadDir(dir)
		if err != nil {
			return nil, err
		}
		for _, e := range entries {
			if strings.HasPrefix(e.Name(), ".tagshelf-1") {
				path := filepath.Join(dir, e.Name())
				if err := os.Remove(path); err != nil {
					return removed, err
				}
				removed = append(removed, path)
			}
		}
		return removed, nil
	}
}

// A real run clears out the temporary copies a crash left in the folders it
// walks, before it removes emptied folders: such a copy used to stay for
// good, at the file's full size, and kept its folder from being removed. A
// dry run and an index run remove nothing, and the library's ID and lock
// files are never touched.
func TestRunRemovesStaleCopies(t *testing.T) {
	build := func(t *testing.T) (source, lib, saveCopy, oldMove, newMove string) {
		tmp := t.TempDir()
		source, lib = filepath.Join(tmp, "src"), filepath.Join(tmp, "lib")
		writeTaggedMP3(t, filepath.Join(source, "Rip", "song.mp3"), songTags, theAudio)
		saveCopy = filepath.Join(source, "Rip", ".tagshelf-123456.mp3")
		writeTestFile(t, saveCopy, "half a save")
		writeTaggedMP3(t, filepath.Join(lib, "Old", "Album", "01 - Old.mp3"), id3Tags{albumArtist: "Old", album: "Album", title: "Old", track: "1"}, "old audio")
		oldMove = filepath.Join(lib, "Old", "Album", ".tagshelf-move-111.mp3")
		writeTestFile(t, oldMove, "half a copy")
		long := time.Now().Add(-2 * staleMoveCopyAge)
		if err := os.Chtimes(oldMove, long, long); err != nil {
			t.Fatal(err)
		}
		// One just written belongs to a move going on now.
		newMove = filepath.Join(lib, "Old", "Album", ".tagshelf-move-222.mp3")
		writeTestFile(t, newMove, "a copy going on")
		writeTestFile(t, filepath.Join(lib, store.LibraryIDFile), "id")
		writeTestFile(t, filepath.Join(lib, store.LibraryLockFile), "")
		return
	}

	t.Run("a real run", func(t *testing.T) {
		source, lib, saveCopy, oldMove, newMove := build(t)
		var calls []string
		var c collector
		finish := Run(context.Background(), Options{Source: source, Destination: lib, Workers: 1, Store: openStore(t),
			RemoveStaleCopies: staleCopies(&calls), Sink: c.sink})
		if finish.State != StateOK {
			t.Fatalf("run = %+v", finish)
		}
		slices.Sort(calls)
		if want := []string{filepath.Join(lib, "Old", "Album"), filepath.Join(source, "Rip")}; !reflect.DeepEqual(calls, want) {
			t.Errorf("asked to clear %q, want once each of %q: the folders with a file of tagshelf's own", calls, want)
		}
		mustNotExist(t, saveCopy, oldMove, filepath.Join(source, "Rip"))
		mustExist(t, newMove, filepath.Join(lib, store.LibraryIDFile), filepath.Join(lib, store.LibraryLockFile))
		var told []string
		for _, w := range c.warnings(WarnStaleCopy) {
			if w.Err != nil {
				t.Errorf("warning %+v", w)
			}
			told = append(told, w.Path)
		}
		slices.Sort(told)
		if want := []string{oldMove, saveCopy}; !reflect.DeepEqual(told, want) {
			t.Errorf("reported as removed: %q, want %q", told, want)
		}
	})

	for name, opts := range map[string]Options{
		"a dry run":    {DryRun: true},
		"an index run": {IndexOnly: true},
	} {
		t.Run(name, func(t *testing.T) {
			source, lib, saveCopy, oldMove, newMove := build(t)
			var calls []string
			opts.Source, opts.Destination, opts.Workers, opts.Store = source, lib, 1, openStore(t)
			opts.RemoveStaleCopies = staleCopies(&calls)
			if finish := Run(context.Background(), opts); finish.State != StateOK {
				t.Fatalf("run = %+v", finish)
			}
			if len(calls) != 0 {
				t.Errorf("asked to clear %q, want nothing removed", calls)
			}
			mustExist(t, saveCopy, oldMove, newMove)
		})
	}

	// A TUI action clears the folders it moves files out of.
	t.Run("a resolution", func(t *testing.T) {
		lib, kept, copyPath, _, s := reviewLibrary(t)
		g := onlyGroup(t, s)
		stale := filepath.Join(filepath.Dir(copyPath), ".tagshelf-123456.mp3")
		writeTestFile(t, stale, "half a save")
		var calls []string
		finish := ResolveDuplicates(context.Background(), Options{Destination: lib, Store: s, RemoveStaleCopies: staleCopies(&calls)},
			[]Resolution{{Kind: g.Kind, Members: g.IDs(), Keeper: indexedFiles(t, s)[kept].ID}})
		if finish.State != StateOK {
			t.Fatalf("resolve = %+v", finish)
		}
		mustNotExist(t, stale, filepath.Dir(copyPath))
	})
}

// A dry run creates nothing, so the names it hands out are reserved in
// memory alone: two different songs whose tags give them one name are
// planned onto two names.
func TestDryRunPlansCollidingFilesOntoTwoNames(t *testing.T) {
	tmp := t.TempDir()
	source, lib := filepath.Join(tmp, "src"), filepath.Join(tmp, "lib")
	writeTaggedMP3(t, filepath.Join(source, "a.mp3"), songTags, "first audio, one length")
	writeTaggedMP3(t, filepath.Join(source, "b.mp3"), songTags, "second audio, of another length")
	var c collector
	finish := Run(context.Background(), Options{Source: source, Destination: lib, Workers: 4, DryRun: true, Sink: c.sink})
	if finish.Counters.Organized != 2 {
		t.Fatalf("dry run = %+v", finish)
	}
	var planned []string
	for _, e := range c.events {
		if p, ok := e.(FilePlaced); ok {
			planned = append(planned, p.Destination)
		}
	}
	slices.Sort(planned)
	want := []string{filepath.Join(lib, "Singer", "Album", "01 - Song (1).mp3"), songPath(lib)}
	if !reflect.DeepEqual(planned, want) {
		t.Errorf("planned destinations = %q, want %q", planned, want)
	}
	mustNotExist(t, lib)
}

// A name is claimed twice over: in memory against the run's own workers, and
// by creating the file exclusively against anything else that takes it
// between the look and the claim.
func TestClaimGuards(t *testing.T) {
	dir := t.TempDir()
	o := testOrganizer(dir)
	taken := filepath.Join(dir, "Album", "taken.mp3")
	writeTestFile(t, taken, "someone else's file")
	if ok, err := o.claim(taken); ok || err != nil {
		t.Errorf("claim of a name a file already has = %t, %v; want it refused without an error", ok, err)
	}
	if got := readTestFile(t, taken); got != "someone else's file" {
		t.Errorf("the file at a refused claim now holds %q", got)
	}
	// The refused claim holds nothing: once the file is gone the name is free.
	if err := os.Remove(taken); err != nil {
		t.Fatal(err)
	}
	if ok, err := o.claim(taken); !ok || err != nil {
		t.Errorf("claim of the freed name = %t, %v; want it", ok, err)
	}

	// In memory: the placeholder is removed behind the organizer's back, and
	// the name is still its own.
	if err := os.Remove(taken); err != nil {
		t.Fatal(err)
	}
	if ok, err := o.claim(taken); ok || err != nil {
		t.Errorf("second claim of a name the run holds = %t, %v; want it refused", ok, err)
	}
	mustNotExist(t, taken)
}

// Cleanup leaves the folders it is told to leave alone, and everything in
// them, also when they lie inside a walked root: the output folders of a
// library that lies inside its source.
func TestCleanupLeavesExcludedFoldersAlone(t *testing.T) {
	root := filepath.Join(t.TempDir(), "src")
	out := filepath.Join(root, "Library")
	kept := filepath.Join(out, "Empty Artist", "Empty Album")
	gone := filepath.Join(root, "Old", "Empty")
	for _, dir := range []string{kept, gone} {
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatal(err)
		}
	}
	var removed []string
	cleanupEmptyDirectories([]string{root}, []string{out}, func(dir string) { removed = append(removed, dir) }, nil)
	mustExist(t, kept, root)
	mustNotExist(t, gone, filepath.Join(root, "Old"))
	if want := []string{gone, filepath.Join(root, "Old")}; !reflect.DeepEqual(removed, want) {
		t.Errorf("removed %q, want %q, deepest first", removed, want)
	}
}

// The library, its untagged folder and its other folder are never removed,
// however empty a reorganize or an undo leaves them.
func TestRootsAreNeverRemoved(t *testing.T) {
	lib := filepath.Join(t.TempDir(), "lib")
	untagged, other := filepath.Join(lib, "_Untagged"), filepath.Join(lib, "_Other")
	writeTaggedMP3(t, filepath.Join(untagged, "fixed.mp3"), songTags, theAudio)
	if err := os.MkdirAll(other, 0755); err != nil {
		t.Fatal(err)
	}
	s := openStore(t)
	var c collector
	finish := Run(context.Background(), Options{Source: lib, Destination: lib, Workers: 1, Store: s, Sink: c.sink})
	if finish.State != StateOK || finish.Counters.Organized != 1 {
		t.Fatalf("reorganize = %+v", finish)
	}
	if entries, err := os.ReadDir(untagged); err != nil || len(entries) != 0 {
		t.Fatalf("the untagged folder holds %v, %v; want it there and empty", entries, err)
	}
	mustExist(t, other, lib)

	// The undo empties the library itself of everything but those folders.
	check, restore := noTags(t)
	if undo := Undo(context.Background(), Options{Destination: lib, Store: s}, lastRun(t, s).ID, check, restore); undo.State != StateOK {
		t.Fatalf("undo = %+v", undo)
	}
	mustExist(t, lib, other, filepath.Join(untagged, "fixed.mp3"))
	mustNotExist(t, filepath.Join(lib, "Singer"))
}

// Two different songs whose tags give them one name: the second keeps its
// " (1)" name, and later reorganizes leave both where they are, with one
// index row each.
func TestSuffixedFileStaysPut(t *testing.T) {
	lib := filepath.Join(t.TempDir(), "lib")
	writeTaggedMP3(t, filepath.Join(lib, "in", "a.mp3"), songTags, "first audio, one length")
	writeTaggedMP3(t, filepath.Join(lib, "in", "b.mp3"), songTags, "second audio, of another length")
	s := openStore(t)
	want := []string{"Singer/Album/01 - Song (1).mp3", "Singer/Album/01 - Song.mp3"}
	for i := 0; i < 3; i++ {
		finish := Run(context.Background(), Options{Source: lib, Destination: lib, Workers: 1, Store: s})
		if got := exactNames(t, lib); !reflect.DeepEqual(got, want) {
			t.Fatalf("run %d: library = %q, want %q", i+1, got, want)
		}
		if i > 0 && (finish.Counters.InPlace != 2 || finish.Counters.Organized != 0 || finish.Counters.Retried != 0 || finish.Counters.Errors != 0) {
			t.Errorf("run %d moved something: %+v", i+1, finish.Counters)
		}
	}
	if rows := indexedFiles(t, s); len(rows) != 2 {
		t.Errorf("index = %v, want the two songs", rows)
	}
}
