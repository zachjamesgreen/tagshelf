package engine

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"go.senan.xyz/taglib"

	"github.com/zachjamesgreen/tagshelf/internal/fixtures"
)

// A symbolic link is neither followed nor moved, whatever it leads to. One to
// a folder would have the run organize files that live somewhere else. One to
// a file used to be moved as if it were the file: the link went into the
// library, the music stayed behind, and a relative link broke.
func TestWalkLeavesLinksAlone(t *testing.T) {
	tmp := t.TempDir()
	source, lib, elsewhere := filepath.Join(tmp, "src"), filepath.Join(tmp, "lib"), filepath.Join(tmp, "elsewhere")
	writeTaggedMP3(t, filepath.Join(source, "Rip", "song.mp3"), songTags, theAudio)
	behind := filepath.Join(elsewhere, "other.mp3")
	writeTaggedMP3(t, behind, id3Tags{albumArtist: "Singer", album: "Album", title: "Other", track: "2"}, "other audio")

	links := map[string]string{
		filepath.Join(source, "Rip", "Folder"):     elsewhere,
		filepath.Join(source, "Rip", "linked.mp3"): behind,
		// Relative, as a link made beside its file is.
		filepath.Join(source, "Rip", "near.mp3"): filepath.Join("..", "..", "elsewhere", "other.mp3"),
		filepath.Join(source, "Rip", "gone.mp3"): filepath.Join(elsewhere, "nothing.mp3"),
	}
	for link, target := range links {
		if err := os.Symlink(target, link); err != nil {
			t.Skipf("cannot make a symbolic link: %v", err)
		}
	}

	var c collector
	finish := Run(context.Background(), Options{Source: source, Destination: lib, Workers: 1, Sink: c.sink})
	if finish.State != StateOK || finish.Counters.TotalFound != 1 || finish.Counters.OtherFound != 0 || finish.Counters.Errors != 0 {
		t.Errorf("run = %+v, want only the song in the source found", finish)
	}
	mustExist(t, behind, songPath(lib))
	warned := map[string]bool{}
	for _, w := range c.warnings(WarnLinkSkipped) {
		warned[w.Path] = true
	}
	for link, target := range links {
		info, err := os.Lstat(link)
		if err != nil || info.Mode()&fs.ModeSymlink == 0 {
			t.Errorf("%s after the run: %v, %v; want the link where it was", link, info, err)
		}
		if got, err := os.Readlink(link); err != nil || got != target {
			t.Errorf("%s leads to %q, %v; want %q still", link, got, err, target)
		}
		if !warned[link] {
			t.Errorf("no warning says %s was left alone: %+v", link, c.warnings(WarnLinkSkipped))
		}
	}
	if len(warned) != len(links) {
		t.Errorf("warnings = %+v, want one for each of the %d links", c.warnings(WarnLinkSkipped), len(links))
	}
	mustNotExist(t, filepath.Join(lib, "_Other"), filepath.Join(lib, "_Untagged"), filepath.Join(lib, "Singer", "Album", "02 - Other.mp3"))
}

// The folders a system keeps at the top of a volume are known by name: those
// of Windows everywhere, since a disk Windows has used holds them wherever it
// is mounted, and those of Linux off Windows.
func TestIsSystemFolder(t *testing.T) {
	top := filepath.Join(t.TempDir(), "volume")
	volumeRootAt(t, top)
	tests := []struct {
		name          string
		anywhere, off bool // a system folder on every system, or off Windows only
	}{
		{"System Volume Information", true, false},
		{"$RECYCLE.BIN", true, false},
		{"$Recycle.Bin", true, false},
		{"lost+found", false, true},
		{".Trash", false, true},
		{".Trash-1000", false, true},
		{".Trash-0", false, true},
		{".Trash-", false, false},
		{".Trash-old", false, false},
		{".Trash-1000 songs", false, false},
		{"Lost+Found", false, false},
		{"Music", false, false},
	}
	for _, tt := range tests {
		want := tt.anywhere || (tt.off && runtime.GOOS != "windows")
		if got := isSystemFolder(filepath.Join(top, tt.name)); got != want {
			t.Errorf("isSystemFolder(%q at the top of a volume) = %t, want %t", tt.name, got, want)
		}
		if isSystemFolder(filepath.Join(top, "Album", tt.name)) {
			t.Errorf("isSystemFolder(%q below the top of a volume) = true", tt.name)
		}
	}
}

// A file whose audio details cannot be read is said aloud, and not only
// noted in its index row. It happens when the reader itself is broken, as it
// was for the second user of a Linux machine, whose every file then failed
// this way while the run ended with nothing to show for it.
func TestIndexWarnsOfAudioDetailsItCannotRead(t *testing.T) {
	lib := filepath.Join(t.TempDir(), "lib")
	unread := filepath.Join(lib, "_Untagged", "unread.mp3")
	writeTestFile(t, unread, readTestFile(t, fixtures.Untagged(".mp3")))
	real := filepath.Join(lib, "_Untagged", "real.flac")
	writeTestFile(t, real, readTestFile(t, fixtures.Untagged(".flac")))
	was := readProperties
	t.Cleanup(func() { readProperties = was })
	readProperties = func(path string) (taglib.Properties, error) {
		if path == unread {
			return taglib.Properties{}, errors.New("permission denied")
		}
		return was(path)
	}

	s := openStore(t)
	var c collector
	finish := Run(context.Background(), Options{Destination: lib, IndexOnly: true, Store: s, Sink: c.sink})
	if finish.State != StateOK || finish.Counters.Errors != 0 {
		t.Fatalf("index = %+v, want it to end well: a file that reads badly is no error", finish)
	}
	rows := indexedFiles(t, s)
	if row := rows[unread]; !strings.Contains(row.ReadError, "properties: permission denied") || row.Sum == "" || row.Bitrate != 0 {
		t.Fatalf("row of the file = %+v, want its hash, and its audio details unread", row)
	}
	if row := rows[real]; row.ReadError != "" || row.Bitrate == 0 {
		t.Fatalf("row of the other file = %+v, want it read in full", row)
	}
	w := c.warnings(WarnDetailsUnread)
	if len(w) != 1 || w[0].Path != unread || w[0].Err == nil || w[0].Err.Error() != "permission denied" {
		t.Fatalf("warnings = %+v, want one for %s, with why", w, unread)
	}

	// A file is read once: the next run finds the row as it is, and has
	// nothing to say.
	var again collector
	Run(context.Background(), Options{Destination: lib, IndexOnly: true, Store: s, Sink: again.sink})
	if w := again.warnings(WarnDetailsUnread); len(w) != 0 {
		t.Errorf("the second run warns again: %+v", w)
	}
}
