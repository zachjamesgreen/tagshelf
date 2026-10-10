package engine

import (
	"context"
	"path/filepath"
	"reflect"
	"testing"
)

// In a reorganize a non-audio file follows the audio of its own folder and of
// no other. A song dropped loose into the library root used to take the
// image and notes of every artist folder with it into its album: the climb
// from a folder without audio reached the root, where the song lay.
func TestReorganizeLooseRootSongTakesNothingWithIt(t *testing.T) {
	lib := t.TempDir()
	writeTaggedMP3(t, filepath.Join(lib, "Other Artist", "Other Album", "01 - Tune.mp3"),
		id3Tags{albumArtist: "Other Artist", album: "Other Album", title: "Tune", track: "1"}, "tune audio")
	writeTestFile(t, filepath.Join(lib, "Other Artist", "artist.jpg"), "artist art")
	writeTestFile(t, filepath.Join(lib, "Other Artist", "Other Album", "cover.jpg"), "cover")
	writeTestFile(t, filepath.Join(lib, "Notes", "readme.txt"), "my notes")
	// Dropped into the library root by hand, with a file beside it.
	writeTaggedMP3(t, filepath.Join(lib, "dropped.mp3"), songTags, theAudio)
	writeTestFile(t, filepath.Join(lib, "desktop.ini"), "ini")

	finish := Run(context.Background(), Options{Source: lib, Destination: lib, Workers: 1})
	if finish.State != StateOK || finish.Counters.Organized != 1 || finish.Counters.OtherMoved != 0 {
		t.Errorf("reorganize = %+v, want the dropped song organized and no other file moved", finish)
	}
	want := []string{
		"Notes/readme.txt",
		"Other Artist/Other Album/01 - Tune.mp3",
		"Other Artist/Other Album/cover.jpg",
		"Other Artist/artist.jpg",
		"Singer/Album/01 - Song.mp3",
		"desktop.ini",
	}
	if got := exactNames(t, lib); !reflect.DeepEqual(got, want) {
		t.Errorf("library = %q\nwant      %q", got, want)
	}
}

// The same from the untagged folder: one song there that now has tags does
// not take the folders beside it along.
func TestReorganizeLooseUntaggedSongTakesNothingWithIt(t *testing.T) {
	lib := t.TempDir()
	scan := filepath.Join(lib, "_Untagged", "Scans of something", "front.jpg")
	writeTestFile(t, scan, "a scan")
	loose := filepath.Join(lib, "_Untagged", "notes.txt")
	writeTestFile(t, loose, "notes")
	writeTaggedMP3(t, filepath.Join(lib, "_Untagged", "fixed.mp3"), songTags, theAudio)

	finish := Run(context.Background(), Options{Source: lib, Destination: lib, Workers: 1})
	if finish.Counters.Organized != 1 || finish.Counters.OtherMoved != 0 {
		t.Errorf("reorganize = %+v, want only the song moved", finish)
	}
	mustExist(t, scan, loose, songPath(lib))
}

// A rip in _Untagged that got its tags still takes its own folder's files
// along, artwork and all.
func TestReorganizeTakesAFoldersOwnFiles(t *testing.T) {
	lib := t.TempDir()
	rip := filepath.Join(lib, "_Untagged", "rip")
	writeTaggedMP3(t, filepath.Join(rip, "song.mp3"), songTags, theAudio)
	writeTestFile(t, filepath.Join(rip, "cover.jpg"), "art")
	writeTestFile(t, filepath.Join(rip, "Scans", "back.jpg"), "scan")

	finish := Run(context.Background(), Options{Source: lib, Destination: lib, Workers: 1})
	if finish.Counters.Organized != 1 || finish.Counters.OtherMoved != 1 {
		t.Errorf("reorganize = %+v, want the song and the cover beside it moved", finish)
	}
	mustExist(t, songPath(lib), filepath.Join(lib, "Singer", "Album", "cover.jpg"),
		// A folder below has no audio of its own: it stays, as it always did.
		filepath.Join(rip, "Scans", "back.jpg"))
}

// An organize from a folder that holds one album among other things: the
// album's own files follow it, scans in a folder below it too, and a document
// elsewhere in the source goes to _Other, not into the album. It used to
// follow the source's only album from anywhere.
func TestOrganizeDoesNotSweepTheSourceIntoItsOnlyAlbum(t *testing.T) {
	tmp := t.TempDir()
	source, lib := filepath.Join(tmp, "Downloads"), filepath.Join(tmp, "lib")
	writeTaggedMP3(t, filepath.Join(source, "Rip", "CD1", "song.mp3"), songTags, theAudio)
	writeTestFile(t, filepath.Join(source, "Rip", "cover.jpg"), "art")
	writeTestFile(t, filepath.Join(source, "Rip", "Scans", "back.jpg"), "scan")
	writeTestFile(t, filepath.Join(source, "Taxes", "2025.pdf"), "tax return")

	finish := Run(context.Background(), Options{Source: source, Destination: lib, Workers: 1})
	if finish.State != StateOK {
		t.Fatalf("organize = %+v", finish)
	}
	want := []string{
		"Singer/Album/01 - Song.mp3",
		"Singer/Album/Scans/back.jpg",
		"Singer/Album/cover.jpg",
		"_Other/Taxes/2025.pdf",
	}
	if got := exactNames(t, lib); !reflect.DeepEqual(got, want) {
		t.Errorf("library = %q\nwant      %q", got, want)
	}
}
