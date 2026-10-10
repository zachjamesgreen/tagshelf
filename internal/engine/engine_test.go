package engine

import (
	"context"
	"crypto/sha1"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf16"

	"tagshelf/internal/fixtures"
	"tagshelf/internal/rules"
)

// testOrganizer builds an organizer whose destinations live under dir, for
// exercising destination resolution directly.
func testOrganizer(dir string) *organizer {
	return newOrganizer(Options{Source: dir, Destination: dir, Untagged: dir, Workers: 1})
}

func TestResolveDestinationNoCollision(t *testing.T) {
	tmpDir := t.TempDir()
	srcFile := filepath.Join(tmpDir, "src.mp3")
	dstFile := filepath.Join(tmpDir, "dst.mp3")

	if err := os.WriteFile(srcFile, []byte("audio data sample 1"), 0644); err != nil {
		t.Fatal(err)
	}

	o := testOrganizer(tmpDir)
	target, outcome, err := o.resolveDestination(srcFile, dstFile, false)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	if outcome != outcomeMove {
		t.Errorf("Expected outcomeMove for non-existent destination, got %v", outcome)
	}
	if target != dstFile {
		t.Errorf("Expected target %q, got %q", dstFile, target)
	}
	// The claim must be visible on disk so that nothing else can take the name.
	if _, err := os.Stat(target); err != nil {
		t.Errorf("Expected destination to be reserved on disk: %v", err)
	}
}

func TestResolveDestinationIdenticalContentIsDuplicate(t *testing.T) {
	tmpDir := t.TempDir()
	srcFile := filepath.Join(tmpDir, "src.jpg")
	dstFile := filepath.Join(tmpDir, "dst.jpg")
	data := []byte("art data sample 1")

	if err := os.WriteFile(srcFile, data, 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dstFile, data, 0644); err != nil {
		t.Fatal(err)
	}

	o := testOrganizer(tmpDir)
	target, outcome, err := o.resolveDestination(srcFile, dstFile, true)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	if outcome != outcomeDuplicate {
		t.Fatalf("Expected outcomeDuplicate for byte-identical destination, got %v", outcome)
	}
	if target != dstFile {
		t.Errorf("Expected duplicate to report %q, got %q", dstFile, target)
	}
	// A second copy must not have been manufactured.
	if _, err := os.Stat(filepath.Join(tmpDir, "dst (1).jpg")); !os.IsNotExist(err) {
		t.Errorf("Expected no suffixed copy to be created for a duplicate")
	}

	// Audio is compared through the index, never byte for byte here: an
	// identical file under the name is just a name collision.
	target, outcome, err = o.resolveDestination(srcFile, dstFile, false)
	if err != nil || outcome != outcomeMove || target != filepath.Join(tmpDir, "dst (1).jpg") {
		t.Errorf("without comparing: %q, %v, %v; want a suffixed move", target, outcome, err)
	}
}

func TestSameBytes(t *testing.T) {
	dir := t.TempDir()
	big := strings.Repeat("0123456789", 20000) // spans several buffers
	files := map[string]string{"a": big, "b": big, "c": big[:len(big)-1] + "x", "d": big + "y", "e": "", "f": ""}
	for name, body := range files {
		writeTestFile(t, filepath.Join(dir, name), body)
	}
	tests := []struct {
		a, b string
		want bool
	}{{"a", "b", true}, {"a", "c", false}, {"a", "d", false}, {"d", "a", false}, {"e", "f", true}, {"e", "a", false}}
	for _, tt := range tests {
		got, err := sameBytes(filepath.Join(dir, tt.a), filepath.Join(dir, tt.b))
		if err != nil || got != tt.want {
			t.Errorf("sameBytes(%s, %s) = %v, %v; want %v", tt.a, tt.b, got, err, tt.want)
		}
	}
}

func TestResolveDestinationDifferentContentGetsSuffix(t *testing.T) {
	tmpDir := t.TempDir()
	srcFile := filepath.Join(tmpDir, "src.mp3")
	dstFile := filepath.Join(tmpDir, "dst.mp3")

	if err := os.WriteFile(srcFile, []byte("0123456789"), 0644); err != nil {
		t.Fatal(err)
	}
	// Same size, different content.
	if err := os.WriteFile(dstFile, []byte("abcdefghij"), 0644); err != nil {
		t.Fatal(err)
	}

	o := testOrganizer(tmpDir)
	target, outcome, err := o.resolveDestination(srcFile, dstFile, false)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	if outcome != outcomeMove {
		t.Fatalf("Expected a distinct file to be organized under a suffix, got %v", outcome)
	}
	expected := filepath.Join(tmpDir, "dst (1).mp3")
	if target != expected {
		t.Errorf("Expected target %q, got %q", expected, target)
	}
}

func TestResolveDestinationDifferentSizeGetsSuffix(t *testing.T) {
	tmpDir := t.TempDir()
	srcFile := filepath.Join(tmpDir, "src.mp3")
	dstFile := filepath.Join(tmpDir, "dst.mp3")

	if err := os.WriteFile(srcFile, []byte("0123456789"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dstFile, []byte("01234567890123456789"), 0644); err != nil {
		t.Fatal(err)
	}

	o := testOrganizer(tmpDir)
	target, outcome, err := o.resolveDestination(srcFile, dstFile, false)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	if outcome != outcomeMove {
		t.Fatalf("Expected a differently sized file to get a suffix, got %v", outcome)
	}
	expected := filepath.Join(tmpDir, "dst (1).mp3")
	if target != expected {
		t.Errorf("Expected target %q, got %q", expected, target)
	}
}

func TestResolveDestinationSuffixSkipsOccupiedNames(t *testing.T) {
	tmpDir := t.TempDir()
	srcFile := filepath.Join(tmpDir, "src.mp3")
	dstFile := filepath.Join(tmpDir, "dst.mp3")

	if err := os.WriteFile(srcFile, []byte("aaaaaaaaaa"), 0644); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"dst.mp3", "dst (1).mp3", "dst (2).mp3"} {
		// Distinct content and distinct sizes so none of them is a duplicate.
		body := make([]byte, 11+len(name))
		for i := range body {
			body[i] = byte('a' + i%23)
		}
		if err := os.WriteFile(filepath.Join(tmpDir, name), body, 0644); err != nil {
			t.Fatal(err)
		}
	}

	o := testOrganizer(tmpDir)
	target, outcome, err := o.resolveDestination(srcFile, dstFile, false)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	if outcome != outcomeMove {
		t.Fatalf("Expected outcomeMove, got %v", outcome)
	}
	expected := filepath.Join(tmpDir, "dst (3).mp3")
	if target != expected {
		t.Errorf("Expected target %q, got %q", expected, target)
	}
}

func TestResolveDestinationFileAlreadyInPlace(t *testing.T) {
	tmpDir := t.TempDir()
	srcFile := filepath.Join(tmpDir, "song.mp3")
	if err := os.WriteFile(srcFile, []byte("already organized"), 0644); err != nil {
		t.Fatal(err)
	}

	o := testOrganizer(tmpDir)
	target, outcome, err := o.resolveDestination(srcFile, srcFile, false)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	if outcome != outcomeInPlace {
		t.Fatalf("Expected outcomeInPlace when source is already its destination, got %v", outcome)
	}
	if target != srcFile {
		t.Errorf("Expected target %q, got %q", srcFile, target)
	}
	if _, err := os.Stat(filepath.Join(tmpDir, "song (1).mp3")); !os.IsNotExist(err) {
		t.Errorf("A file already at its destination must not be duplicated")
	}
}

func TestResolveDestinationReservesAcrossWorkers(t *testing.T) {
	tmpDir := t.TempDir()
	srcA := filepath.Join(tmpDir, "a", "track.mp3")
	srcB := filepath.Join(tmpDir, "b", "track.mp3")
	for path, body := range map[string]string{srcA: "content of a", srcB: "different content of b"} {
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0644); err != nil {
			t.Fatal(err)
		}
	}

	desired := filepath.Join(tmpDir, "out", "track.mp3")
	o := testOrganizer(tmpDir)

	targetA, outcomeA, err := o.resolveDestination(srcA, desired, false)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	targetB, outcomeB, err := o.resolveDestination(srcB, desired, false)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	if outcomeA != outcomeMove || outcomeB != outcomeMove {
		t.Fatalf("Expected both files to be movable, got %v and %v", outcomeA, outcomeB)
	}
	if targetA == targetB {
		t.Fatalf("Two sources were handed the same destination %q", targetA)
	}
}

func TestResolveDestinationReportsUnusableParent(t *testing.T) {
	tmpDir := t.TempDir()
	srcFile := filepath.Join(tmpDir, "src.mp3")
	if err := os.WriteFile(srcFile, []byte("audio"), 0644); err != nil {
		t.Fatal(err)
	}
	// A regular file where a destination directory should be: resolution must
	// report the problem rather than loop looking for a free suffix.
	blocker := filepath.Join(tmpDir, "blocker")
	if err := os.WriteFile(blocker, []byte("not a directory"), 0644); err != nil {
		t.Fatal(err)
	}

	o := testOrganizer(tmpDir)
	if _, _, err := o.resolveDestination(srcFile, filepath.Join(blocker, "dst.mp3"), false); err == nil {
		t.Errorf("Expected an error when the destination parent is a regular file")
	}
}

func TestReleaseRemovesUnusedReservation(t *testing.T) {
	tmpDir := t.TempDir()
	srcFile := filepath.Join(tmpDir, "src.mp3")
	dstFile := filepath.Join(tmpDir, "out", "dst.mp3")
	if err := os.WriteFile(srcFile, []byte("audio"), 0644); err != nil {
		t.Fatal(err)
	}

	o := testOrganizer(tmpDir)
	target, _, err := o.resolveDestination(srcFile, dstFile, false)
	if err != nil {
		t.Fatal(err)
	}

	o.release(target)
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Errorf("Expected the empty placeholder to be removed on release")
	}
	if _, taken := o.reserved.Load(rules.PathKey(target)); taken {
		t.Errorf("Expected the in-memory reservation to be dropped on release")
	}
}

func TestResolveDestinationDryRunTouchesNothing(t *testing.T) {
	tmpDir := t.TempDir()
	srcFile := filepath.Join(tmpDir, "src.mp3")
	if err := os.WriteFile(srcFile, []byte("audio"), 0644); err != nil {
		t.Fatal(err)
	}

	o := newOrganizer(Options{Source: tmpDir, Destination: tmpDir, Untagged: tmpDir, Workers: 1, DryRun: true})
	dstFile := filepath.Join(tmpDir, "out", "dst.mp3")
	target, outcome, err := o.resolveDestination(srcFile, dstFile, false)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	if outcome != outcomeMove || target != dstFile {
		t.Fatalf("Expected a plain move to %q, got %q (%v)", dstFile, target, outcome)
	}
	if _, err := os.Stat(filepath.Dir(dstFile)); !os.IsNotExist(err) {
		t.Errorf("A dry run must not create destination directories")
	}
	if _, err := os.Stat(dstFile); !os.IsNotExist(err) {
		t.Errorf("A dry run must not create destination placeholders")
	}
}

func TestMoveFile(t *testing.T) {
	tmpDir := t.TempDir()
	src := filepath.Join(tmpDir, "sub", "source.mp3")
	dst := filepath.Join(tmpDir, "organized", "dest.mp3")

	if err := os.MkdirAll(filepath.Dir(src), 0755); err != nil {
		t.Fatal(err)
	}
	content := []byte("binary music content")
	if err := os.WriteFile(src, content, 0644); err != nil {
		t.Fatal(err)
	}

	o := testOrganizer(tmpDir)
	if err := o.moveFile(src, dst); err != nil {
		t.Fatalf("moveFile failed: %v", err)
	}

	if _, err := os.Stat(src); !os.IsNotExist(err) {
		t.Errorf("Source file should no longer exist after move")
	}

	readBack, err := os.ReadFile(dst)
	if err != nil {
		t.Fatalf("Failed to read destination file: %v", err)
	}
	if string(readBack) != string(content) {
		t.Errorf("Destination file content mismatch")
	}
}

// TestCopyFilePreservesAttributes covers the cross-volume fallback, which is
// the common path for this tool (I:\Music to a different drive).
func TestCopyFilePreservesAttributes(t *testing.T) {
	tmpDir := t.TempDir()
	src := filepath.Join(tmpDir, "source.mp3")
	dst := filepath.Join(tmpDir, "dest.mp3")

	content := []byte("binary music content")
	if err := os.WriteFile(src, content, 0644); err != nil {
		t.Fatal(err)
	}
	// A modification time well in the past, as a real music file would have.
	want := time.Date(2019, 3, 14, 9, 26, 53, 0, time.UTC)
	if err := os.Chtimes(src, want, want); err != nil {
		t.Fatal(err)
	}
	srcInfo, err := os.Stat(src)
	if err != nil {
		t.Fatal(err)
	}

	if err := copyFile(src, dst, func(w Warning) { t.Errorf("unexpected warning: %+v", w) }); err != nil {
		t.Fatalf("copyFile failed: %v", err)
	}

	dstInfo, err := os.Stat(dst)
	if err != nil {
		t.Fatalf("Failed to stat destination: %v", err)
	}
	readBack, err := os.ReadFile(dst)
	if err != nil {
		t.Fatal(err)
	}
	if string(readBack) != string(content) {
		t.Errorf("Destination content mismatch")
	}
	if !dstInfo.ModTime().Equal(srcInfo.ModTime()) {
		t.Errorf("Modification time not preserved: got %v, want %v", dstInfo.ModTime(), srcInfo.ModTime())
	}
	if dstInfo.Mode().Perm() != srcInfo.Mode().Perm() {
		t.Errorf("Permissions not preserved: got %v, want %v", dstInfo.Mode().Perm(), srcInfo.Mode().Perm())
	}
}

func TestIsSubdirectory(t *testing.T) {
	// Absolute paths, as a run has them. ("C:" joined with a name is a path
	// relative to that drive's current folder.)
	root := filepath.FromSlash("/")
	if runtime.GOOS == "windows" {
		root = `C:\`
	}
	parent := filepath.Join(root, "Music")
	tests := []struct {
		sub      string
		expected bool
	}{
		{filepath.Join(parent, "Artist"), true},
		{filepath.Join(parent, "Artist", "Album"), true},
		// A child whose name merely starts with dots is still a child.
		{filepath.Join(parent, "..hidden"), true},
		{filepath.Join(parent, "..hidden", "Album"), true},
		{parent, false},
		{filepath.Join(root, "Other"), false},
		{filepath.Join(root, "Music2", "Artist"), false},
		{root, false},
	}

	for _, tt := range tests {
		if got := isSubdirectory(parent, tt.sub); got != tt.expected {
			t.Errorf("isSubdirectory(%q, %q) = %t, expected %t", parent, tt.sub, got, tt.expected)
		}
	}
}

func TestCleanupEmptyDirectories(t *testing.T) {
	tmpDir := t.TempDir()
	srcDir := filepath.Join(tmpDir, "source")
	destDir := filepath.Join(tmpDir, "dest")
	untaggedDir := filepath.Join(tmpDir, "untagged")

	emptySubDir := filepath.Join(srcDir, "emptyArtist", "emptyAlbum")
	nonEmptySubDir := filepath.Join(srcDir, "populatedArtist", "album")

	if err := os.MkdirAll(emptySubDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(nonEmptySubDir, 0755); err != nil {
		t.Fatal(err)
	}
	// Add a remaining file to nonEmptySubDir
	if err := os.WriteFile(filepath.Join(nonEmptySubDir, "info.txt"), []byte("non-audio"), 0644); err != nil {
		t.Fatal(err)
	}

	cleanupEmptyDirectories([]string{srcDir}, []string{destDir, untaggedDir}, nil, nil)

	if _, err := os.Stat(emptySubDir); !os.IsNotExist(err) {
		t.Errorf("Expected emptySubDir to be deleted")
	}
	if _, err := os.Stat(nonEmptySubDir); os.IsNotExist(err) {
		t.Errorf("Expected nonEmptySubDir to be preserved")
	}
}

func TestRunEndToEnd(t *testing.T) {
	tmpDir := t.TempDir()
	sourceDir := filepath.Join(tmpDir, "source")
	destDir := filepath.Join(tmpDir, "dest")
	untaggedDir := filepath.Join(tmpDir, "untagged")

	subDir := filepath.Join(sourceDir, "folder1")
	if err := os.MkdirAll(subDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Create an untagged audio file (simple mock file with .mp3 extension)
	untaggedAudio := filepath.Join(subDir, "unknown_track.mp3")
	if err := os.WriteFile(untaggedAudio, []byte("fake mp3 data without tags"), 0644); err != nil {
		t.Fatal(err)
	}

	// Create a non-audio file next to untagged audio: it goes to Other
	coverArt := filepath.Join(subDir, "cover.jpg")
	if err := os.WriteFile(coverArt, []byte("fake jpg art"), 0644); err != nil {
		t.Fatal(err)
	}

	otherDir := filepath.Join(tmpDir, "other")
	cfg := Options{
		Source:      sourceDir,
		Destination: destDir,
		Untagged:    untaggedDir,
		Other:       otherDir,
		Workers:     2,
		DryRun:      false,
	}

	stats := Run(context.Background(), cfg).Counters

	if stats.TotalFound != 1 {
		t.Errorf("Expected 1 audio file found, got %d", stats.TotalFound)
	}
	if stats.Untagged != 1 {
		t.Errorf("Expected 1 untagged file moved, got %d", stats.Untagged)
	}
	if stats.Errors != 0 {
		t.Errorf("Expected no errors, got %d", stats.Errors)
	}

	// Verify untagged file arrived in untaggedDir root
	expectedUntaggedFile := filepath.Join(untaggedDir, "unknown_track.mp3")
	if _, err := os.Stat(expectedUntaggedFile); os.IsNotExist(err) {
		t.Errorf("Expected file at %q", expectedUntaggedFile)
	}

	if stats.OtherFound != 1 || stats.OtherMoved != 1 {
		t.Errorf("Expected 1 non-audio file found and moved, got %d found, %d moved",
			stats.OtherFound, stats.OtherMoved)
	}

	// cover.jpg keeps its path relative to source under Other
	if _, err := os.Stat(filepath.Join(otherDir, "folder1", "cover.jpg")); err != nil {
		t.Errorf("Expected cover.jpg under the other directory: %v", err)
	}
	if _, err := os.Stat(subDir); !os.IsNotExist(err) {
		t.Errorf("Expected the emptied source folder to be cleaned up")
	}
}

func TestCompanionDir(t *testing.T) {
	src := filepath.FromSlash("/src")
	albumA := filepath.FromSlash("/dest/Artist/A")
	albumB := filepath.FromSlash("/dest/Artist/B")
	set := func(albums ...string) map[string]struct{} {
		s := make(map[string]struct{})
		for _, a := range albums {
			s[a] = struct{}{}
		}
		return s
	}

	tree := buildAlbumTree(src, map[string]map[string]struct{}{
		filepath.Join(src, "Rip"):                set(albumA),
		filepath.Join(src, "Multi", "CD1"):       set(albumB),
		filepath.Join(src, "Multi", "CD2"):       set(albumB),
		filepath.Join(src, "Artist", "AlbumA"):   set(albumA),
		filepath.Join(src, "Artist", "Untagged"): set(""),
		filepath.Join(src, "Mixed"):              set(albumA, ""),
		filepath.Join(src, "Several", "AlbumA"):  set(albumA),
		filepath.Join(src, "Several", "AlbumB"):  set(albumB),
	})

	tests := []struct {
		dir    string
		want   string
		wantOK bool
	}{
		{filepath.Join(src, "Rip"), albumA, true},
		{filepath.Join(src, "Rip", "Scans"), filepath.Join(albumA, "Scans"), true},
		{filepath.Join(src, "Multi"), albumB, true},
		// A sibling of a tagged album must not fall into it.
		{filepath.Join(src, "Artist", "Untagged"), "", false},
		{filepath.Join(src, "Artist", "Untagged", "art"), "", false},
		{filepath.Join(src, "Mixed"), "", false},
		{filepath.Join(src, "Several"), "", false},
		{filepath.Join(src, "NoAudio"), "", false},
		{src, "", false},
	}
	for _, tt := range tests {
		got, ok := companionDir(src, tree, tt.dir, true)
		if got != tt.want || ok != tt.wantOK {
			t.Errorf("companionDir(%q) = %q, %t; want %q, %t", tt.dir, got, ok, tt.want, tt.wantOK)
		}
	}

	// Without the climb, as in a reorganize, only a folder's own audio counts.
	if got, ok := companionDir(src, tree, filepath.Join(src, "Rip", "Scans"), false); ok {
		t.Errorf("companionDir(Rip/Scans) without the climb = %q, want none", got)
	}
	if got, ok := companionDir(src, tree, filepath.Join(src, "Rip"), false); !ok || got != albumA {
		t.Errorf("companionDir(Rip) without the climb = %q, %t; want %q", got, ok, albumA)
	}
}

// TestCompanionDirStopsBelowTheRoot is the downloads folder of finding 27: a
// source whose only audio is one album. A file elsewhere in the source does
// not follow that album, and neither does one below a folder that has none;
// the files of the album's own folders still do, as do those lying in the
// source itself when the source is the album.
func TestCompanionDirStopsBelowTheRoot(t *testing.T) {
	src := filepath.FromSlash("/downloads")
	album := filepath.FromSlash("/music/Artist/Album")
	one := func(a string) map[string]struct{} { return map[string]struct{}{a: {}} }
	tree := buildAlbumTree(src, map[string]map[string]struct{}{
		filepath.Join(src, "Rip", "CD1"): one(album),
		filepath.Join(src, "Rip", "CD2"): one(album),
	})
	for _, tt := range []struct {
		dir    string
		want   string
		wantOK bool
	}{
		{filepath.Join(src, "Taxes"), "", false},
		{filepath.Join(src, "Taxes", "2025"), "", false},
		{filepath.Join(src, "Rip"), album, true},
		{filepath.Join(src, "Rip", "Scans"), filepath.Join(album, "Scans"), true},
		{filepath.Join(src, "Rip", "CD1", "Booklet"), filepath.Join(album, "Booklet"), true},
		// The source itself: its own folder is the album's rip.
		{src, album, true},
	} {
		got, ok := companionDir(src, tree, tt.dir, true)
		if got != tt.want || ok != tt.wantOK {
			t.Errorf("companionDir(%q) = %q, %t; want %q, %t", tt.dir, got, ok, tt.want, tt.wantOK)
		}
	}

	// A song lying directly in the source is followed by what lies beside
	// it, and by nothing below.
	loose := map[string]map[string]struct{}{src: one(album)}
	if got, ok := companionDir(src, buildAlbumTree(src, loose), filepath.Join(src, "Notes"), true); ok {
		t.Errorf("a file below a source with a loose song follows it to %q", got)
	}
	if got, ok := companionDir(src, buildAlbumTree(src, loose), src, true); !ok || got != album {
		t.Errorf("a file beside a loose song in the source = %q, %t; want %q", got, ok, album)
	}
}

func TestRunDryRunLeavesCompanions(t *testing.T) {
	tmpDir := t.TempDir()
	sourceDir := filepath.Join(tmpDir, "source", "rip")
	if err := os.MkdirAll(sourceDir, 0755); err != nil {
		t.Fatal(err)
	}
	contents := map[string]string{"track.mp3": "data track.mp3", "album.cue": "FILE \"track.mp3\" MP3\n", "cover.png": "data cover.png"}
	for name, content := range contents {
		writeTestFile(t, filepath.Join(sourceDir, name), content)
	}

	stats := Run(context.Background(), Options{
		Source:      filepath.Join(tmpDir, "source"),
		Destination: filepath.Join(tmpDir, "dest"),
		Workers:     2,
		DryRun:      true,
	}).Counters

	if stats.OtherFound != 2 || stats.OtherMoved != 2 {
		t.Errorf("Expected 2 non-audio files counted, got %d found, %d moved",
			stats.OtherFound, stats.OtherMoved)
	}
	for name, content := range contents {
		if got := readTestFile(t, filepath.Join(sourceDir, name)); got != content {
			t.Errorf("A dry run must leave %s as it is, got %q", name, got)
		}
	}
	mustNotExist(t, filepath.Join(tmpDir, "dest"), filepath.Join(sourceDir, "album.cue.orig"))
}

func TestRunDefaultsWorkers(t *testing.T) {
	tmpDir := t.TempDir()
	sourceDir := filepath.Join(tmpDir, "source")
	if err := os.MkdirAll(sourceDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sourceDir, "track.mp3"), []byte("fake mp3 data"), 0644); err != nil {
		t.Fatal(err)
	}

	// Workers: 0 is what the shipped config.yaml contains; Run must
	// normalize it rather than start no workers and drop every job.
	stats := Run(context.Background(), Options{
		Source:      sourceDir,
		Destination: filepath.Join(tmpDir, "dest"),
		Untagged:    filepath.Join(tmpDir, "untagged"),
		Workers:     0,
	}).Counters

	if stats.TotalFound != 1 {
		t.Fatalf("Expected 1 audio file found, got %d", stats.TotalFound)
	}
	if stats.Untagged != 1 {
		t.Errorf("Expected the file to be processed with Workers=0, got Untagged=%d Errors=%d",
			stats.Untagged, stats.Errors)
	}
	if _, err := os.Stat(filepath.Join(tmpDir, "untagged", "track.mp3")); err != nil {
		t.Errorf("Expected the file to arrive in the untagged directory: %v", err)
	}
}

// TestRunPreservesCollidingFiles is the regression test for the case
// that used to destroy data: many distinct files that all flatten onto the same
// untagged name, handled concurrently.
func TestRunPreservesCollidingFiles(t *testing.T) {
	tmpDir := t.TempDir()
	sourceDir := filepath.Join(tmpDir, "source")
	untaggedDir := filepath.Join(tmpDir, "untagged")

	const count = 24
	wantBodies := make(map[string]bool, count)
	for i := 0; i < count; i++ {
		dir := filepath.Join(sourceDir, fmt.Sprintf("album%02d", i))
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatal(err)
		}
		// Distinct content, identical basename.
		body := fmt.Sprintf("fake mp3 body number %03d", i)
		wantBodies[body] = true
		if err := os.WriteFile(filepath.Join(dir, "01 Intro.mp3"), []byte(body), 0644); err != nil {
			t.Fatal(err)
		}
	}

	stats := Run(context.Background(), Options{
		Source:      sourceDir,
		Destination: filepath.Join(tmpDir, "dest"),
		Untagged:    untaggedDir,
		Workers:     8,
	}).Counters

	if stats.Errors != 0 {
		t.Errorf("Expected no errors, got %d", stats.Errors)
	}
	if got := stats.Untagged; got != count {
		t.Errorf("Expected %d untagged moves, got %d (duplicates %d)", count, got, stats.Duplicates)
	}

	entries, err := os.ReadDir(untaggedDir)
	if err != nil {
		t.Fatal(err)
	}
	gotBodies := make(map[string]bool, len(entries))
	for _, e := range entries {
		data, err := os.ReadFile(filepath.Join(untaggedDir, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		if gotBodies[string(data)] {
			t.Errorf("Duplicate content in destination: %q", data)
		}
		gotBodies[string(data)] = true
	}

	if len(gotBodies) != count {
		t.Errorf("Expected %d distinct files in %q, found %d — files were lost", count, untaggedDir, len(gotBodies))
	}
	for body := range wantBodies {
		if !gotBodies[body] {
			t.Errorf("File with content %q never made it to the destination", body)
		}
	}
}

// TestRunIsIdempotent checks that a file already at its place is left
// there: the second run takes the untagged folder, where the first run put
// the file, as its source, and neither duplicates the file nor strands it.
func TestRunIsIdempotent(t *testing.T) {
	tmpDir := t.TempDir()
	sourceDir := filepath.Join(tmpDir, "source")
	untaggedDir := filepath.Join(tmpDir, "untagged")
	if err := os.MkdirAll(sourceDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sourceDir, "track.mp3"), []byte("fake mp3 data"), 0644); err != nil {
		t.Fatal(err)
	}

	cfg := Options{
		Source:      sourceDir,
		Destination: filepath.Join(tmpDir, "dest"),
		Untagged:    untaggedDir,
		Workers:     2,
	}
	Run(context.Background(), cfg)

	// Re-run over the untagged directory itself: every file is already in place.
	stats := Run(context.Background(), Options{
		Source:      untaggedDir,
		Destination: filepath.Join(tmpDir, "dest"),
		Untagged:    untaggedDir,
		Workers:     2,
	}).Counters

	if stats.InPlace != 1 {
		t.Errorf("Expected 1 file reported as already in place, got %d (organized %d, untagged %d, duplicates %d)",
			stats.InPlace, stats.Organized, stats.Untagged, stats.Duplicates)
	}
	entries, err := os.ReadDir(untaggedDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("Expected the untagged directory to still hold exactly 1 file, got %v", names)
	}
}

func TestDirectoryCache(t *testing.T) {
	tmpDir := t.TempDir()
	var cache directoryCache
	targetDir := filepath.Join(tmpDir, "artist", "album")

	// First call creates dir
	if err := cache.ensureDir(targetDir); err != nil {
		t.Fatalf("ensureDir failed on first call: %v", err)
	}
	if fi, err := os.Stat(targetDir); err != nil || !fi.IsDir() {
		t.Fatalf("Directory was not created")
	}

	// The second call answers from the cache: it does not look, so a folder
	// removed behind its back is not made again.
	if err := os.Remove(targetDir); err != nil {
		t.Fatal(err)
	}
	if err := cache.ensureDir(targetDir); err != nil {
		t.Fatalf("ensureDir failed on cached call: %v", err)
	}
	mustNotExist(t, targetDir)
}

// TestDirectoryCacheIsPerRun guards against a process-global cache handing stale
// "already created" answers to a later run.
func TestDirectoryCacheIsPerRun(t *testing.T) {
	tmpDir := t.TempDir()
	dir := filepath.Join(tmpDir, "artist", "album")

	first := testOrganizer(tmpDir)
	if err := first.dirCache.ensureDir(dir); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Join(tmpDir, "artist")); err != nil {
		t.Fatal(err)
	}

	second := testOrganizer(tmpDir)
	if err := second.dirCache.ensureDir(dir); err != nil {
		t.Fatalf("A fresh run must recreate the directory: %v", err)
	}
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		t.Errorf("Expected %q to exist again", dir)
	}
}

func TestTargetedDirectoryCleanup(t *testing.T) {
	tmpDir := t.TempDir()
	sourceDir := filepath.Join(tmpDir, "source")
	destDir := filepath.Join(tmpDir, "dest")
	untaggedDir := filepath.Join(tmpDir, "untagged")

	touchedArtist := filepath.Join(sourceDir, "touchedArtist", "album")
	untouchedArtist := filepath.Join(sourceDir, "untouchedArtist", "album")

	if err := os.MkdirAll(touchedArtist, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(untouchedArtist, 0755); err != nil {
		t.Fatal(err)
	}

	var touchedDirs sync.Map
	touchedDirs.Store(touchedArtist, struct{}{})

	cleanupEmptyDirectories([]string{sourceDir}, []string{destDir, untaggedDir}, nil, &touchedDirs)

	if _, err := os.Stat(touchedArtist); !os.IsNotExist(err) {
		t.Errorf("Expected touchedArtist/album to be removed")
	}
	if _, err := os.Stat(filepath.Dir(touchedArtist)); !os.IsNotExist(err) {
		t.Errorf("Expected touchedArtist parent to be removed")
	}
	if _, err := os.Stat(untouchedArtist); os.IsNotExist(err) {
		t.Errorf("Expected untouchedArtist/album to be preserved because it was not touched")
	}
}

// TestTargetedDirectoryCleanupUnderDottedParent covers directories whose name
// starts with dots, which the old isSubdirectory check treated as "outside".
func TestTargetedDirectoryCleanupUnderDottedParent(t *testing.T) {
	tmpDir := t.TempDir()
	sourceDir := filepath.Join(tmpDir, "source")
	dotted := filepath.Join(sourceDir, "..hidden", "album")
	if err := os.MkdirAll(dotted, 0755); err != nil {
		t.Fatal(err)
	}

	var touchedDirs sync.Map
	touchedDirs.Store(dotted, struct{}{})

	cleanupEmptyDirectories([]string{sourceDir}, []string{filepath.Join(tmpDir, "dest"), filepath.Join(tmpDir, "untagged")}, nil, &touchedDirs)

	if _, err := os.Stat(dotted); !os.IsNotExist(err) {
		t.Errorf("Expected %q to be cleaned up", dotted)
	}
	if _, err := os.Stat(filepath.Join(sourceDir, "..hidden")); !os.IsNotExist(err) {
		t.Errorf("Expected the ..hidden parent to be cleaned up")
	}
}

func BenchmarkResolveDestinationCollision(b *testing.B) {
	tmpDir := b.TempDir()
	srcFile := filepath.Join(tmpDir, "src.bin")
	dstFile := filepath.Join(tmpDir, "dst.bin")

	data1 := make([]byte, 512*1024)   // 512KB
	data2 := make([]byte, 512*1024+1) // different size
	if err := os.WriteFile(srcFile, data1, 0644); err != nil {
		b.Fatal(err)
	}
	if err := os.WriteFile(dstFile, data2, 0644); err != nil {
		b.Fatal(err)
	}

	// Dry run so that resolution reserves names in memory only and each
	// iteration measures the same work.
	cfg := Options{Source: tmpDir, Destination: tmpDir, Untagged: tmpDir, Workers: 1, DryRun: true}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		o := newOrganizer(cfg)
		if _, _, err := o.resolveDestination(srcFile, dstFile, false); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkProcessFile(b *testing.B) {
	tmpDir := b.TempDir()
	sourceDir := filepath.Join(tmpDir, "src")
	if err := os.MkdirAll(sourceDir, 0755); err != nil {
		b.Fatal(err)
	}

	cfg := Options{
		Source:      sourceDir,
		Destination: filepath.Join(tmpDir, "dst"),
		Untagged:    filepath.Join(tmpDir, "untagged"),
		Workers:     1,
		DryRun:      true,
	}

	testFile := filepath.Join(sourceDir, "test.mp3")
	if err := os.WriteFile(testFile, []byte("fake audio content"), 0644); err != nil {
		b.Fatal(err)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		// A fresh organizer per iteration: destination reservations are
		// per-run state, so reusing one would walk the suffix chain.
		newOrganizer(cfg).processFile(testFile)
	}
}

// id3Tags are the fields writeTaggedMP3 stores.
type id3Tags struct {
	artist, albumArtist, album, title, year, track, disc string
	albumArtistID                                        string // TXXX MusicBrainz Album Artist Id
}

// recordingID is the MusicBrainz recording ID writeTaggedMP3 gives a song
// with these tags: one for each song, as real files have, and the same again
// for the same song.
func recordingID(tags id3Tags) string {
	sum := sha1.Sum([]byte(tags.artist + "\x00" + tags.albumArtist + "\x00" + tags.album + "\x00" + tags.title))
	h := fmt.Sprintf("%x", sum)
	return h[0:8] + "-" + h[8:12] + "-4" + h[13:16] + "-8" + h[17:20] + "-" + h[20:32]
}

// writeTaggedMP3 writes a file with a real ID3v2.3 tag, MusicBrainz UFID
// included, followed by body, so that runs exercise the actual tag reader.
func writeTaggedMP3(t *testing.T, path string, tags id3Tags, body string) {
	t.Helper()
	var frames []byte
	frame := func(id string, data []byte) {
		n := len(data)
		frames = append(frames, id...)
		frames = append(frames, byte(n>>24), byte(n>>16), byte(n>>8), byte(n), 0, 0)
		frames = append(frames, data...)
	}
	text := func(id, value string) {
		if value == "" {
			return
		}
		if isASCII(value) {
			frame(id, append([]byte{0}, value...))
			return
		}
		// UTF-16 with a byte order mark, the only Unicode encoding ID3v2.3 has.
		data := []byte{1, 0xff, 0xfe}
		for _, u := range utf16.Encode([]rune(value)) {
			data = append(data, byte(u), byte(u>>8))
		}
		frame(id, data)
	}
	text("TPE1", tags.artist)
	text("TPE2", tags.albumArtist)
	text("TALB", tags.album)
	text("TIT2", tags.title)
	text("TYER", tags.year)
	text("TRCK", tags.track)
	text("TPOS", tags.disc)
	if tags.albumArtistID != "" {
		frame("TXXX", []byte("\x00MusicBrainz Album Artist Id\x00"+tags.albumArtistID))
	}
	frame("UFID", []byte("http://musicbrainz.org\x00"+recordingID(tags)))

	n := len(frames)
	header := []byte{'I', 'D', '3', 3, 0, 0, byte(n >> 21 & 0x7f), byte(n >> 14 & 0x7f), byte(n >> 7 & 0x7f), byte(n & 0x7f)}
	data := append(append(header, frames...), body...)

	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatal(err)
	}
}

func writeTestFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0644); err != nil {
		t.Fatal(err)
	}
}

func readTestFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("Expected %q to exist: %v", path, err)
	}
	return string(data)
}

func TestRunReorganizesLibrary(t *testing.T) {
	lib := t.TempDir()
	oldAlbum := filepath.Join(lib, "Singer feat. Guest", "Album")
	writeTaggedMP3(t, filepath.Join(oldAlbum, "Song.mp3"), id3Tags{
		artist: "Singer feat. Guest", albumArtist: "Singer", album: "Album", title: "Song",
		year: "2001", track: "3/10", disc: "1/1",
	}, "audio")
	writeTestFile(t, filepath.Join(oldAlbum, "cover.jpg"), "art")
	originalCue := "REM GENRE Pop\r\nFILE \"Song.mp3\" MP3\r\n  TRACK 01 AUDIO\r\n    INDEX 01 00:00:00\r\n"
	writeTestFile(t, filepath.Join(oldAlbum, "album.cue"), originalCue)
	// Artist-level art and audio without tags must stay where they are, also
	// with a song lying loose in the library root for a climb to find.
	writeTestFile(t, filepath.Join(lib, "Singer feat. Guest", "artist.jpg"), "artist art")
	writeTestFile(t, filepath.Join(lib, "Hand Placed", "track.mp3"), "no tags here")
	writeTaggedMP3(t, filepath.Join(lib, "dropped.mp3"), id3Tags{albumArtist: "Dropped", album: "In", title: "Loose", track: "1"}, "dropped audio")

	cfg := Options{Source: lib, Destination: lib, Workers: 2}
	stats := Run(context.Background(), cfg).Counters

	if stats.Errors != 0 {
		t.Fatalf("Expected no errors, got %d", stats.Errors)
	}
	newAlbum := filepath.Join(lib, "Singer", "Album (2001)")
	if readTestFile(t, filepath.Join(newAlbum, "03 - Song.mp3")) == "" {
		t.Errorf("Expected the organized track to have content")
	}
	readTestFile(t, filepath.Join(newAlbum, "cover.jpg"))
	wantCue := "REM GENRE Pop\r\nFILE \"03 - Song.mp3\" MP3\r\n  TRACK 01 AUDIO\r\n    INDEX 01 00:00:00\r\n"
	if got := readTestFile(t, filepath.Join(newAlbum, "album.cue")); got != wantCue {
		t.Errorf("Cue sheet not rewritten as expected:\n got %q\nwant %q", got, wantCue)
	}
	if got := readTestFile(t, filepath.Join(newAlbum, "album.cue.orig")); got != originalCue {
		t.Errorf("Expected the original cue sheet in album.cue.orig, got %q", got)
	}
	readTestFile(t, filepath.Join(lib, "Singer feat. Guest", "artist.jpg"))
	readTestFile(t, filepath.Join(lib, "Hand Placed", "track.mp3"))
	if stats.LeftInPlace != 1 {
		t.Errorf("Expected 1 non-qualifying file left in place, got %d", stats.LeftInPlace)
	}
	if _, err := os.Stat(oldAlbum); !os.IsNotExist(err) {
		t.Errorf("Expected the emptied old album folder to be removed")
	}
	for _, dir := range []string{"_Untagged", "_Other"} {
		if _, err := os.Stat(filepath.Join(lib, dir)); !os.IsNotExist(err) {
			t.Errorf("A reorganize must not push anything into %s", dir)
		}
	}

	// A second pass finds everything in place and leaves the cue alone.
	again := Run(context.Background(), cfg).Counters
	if again.Organized != 0 || again.OtherMoved != 0 || again.CueRewritten != 0 {
		t.Errorf("Expected a second reorganize to change nothing, got organized %d, non-audio %d, cues %d",
			again.Organized, again.OtherMoved, again.CueRewritten)
	}
	if got := readTestFile(t, filepath.Join(newAlbum, "album.cue.orig")); got != originalCue {
		t.Errorf("A later run must keep the first original, got %q", got)
	}

	// The song is retitled, so a third pass rewrites the sheet again: the
	// original it finds kept is not written over.
	writeTaggedMP3(t, filepath.Join(newAlbum, "03 - Song.mp3"), id3Tags{
		artist: "Singer feat. Guest", albumArtist: "Singer", album: "Album", title: "Tune",
		year: "2001", track: "3/10", disc: "1/1",
	}, "audio")
	third := Run(context.Background(), cfg).Counters
	if third.CueRewritten != 1 || third.Errors != 0 {
		t.Fatalf("Expected the third reorganize to rewrite the cue sheet again, got %+v", third)
	}
	wantCue = "REM GENRE Pop\r\nFILE \"03 - Tune.mp3\" MP3\r\n  TRACK 01 AUDIO\r\n    INDEX 01 00:00:00\r\n"
	if got := readTestFile(t, filepath.Join(newAlbum, "album.cue")); got != wantCue {
		t.Errorf("Cue sheet after the second rewrite:\n got %q\nwant %q", got, wantCue)
	}
	if got := readTestFile(t, filepath.Join(newAlbum, "album.cue.orig")); got != originalCue {
		t.Errorf("A second rewrite must keep the first original, got %q", got)
	}
}

func TestReorganizePullsTaggedFilesFromUntagged(t *testing.T) {
	tmpDir := t.TempDir()
	lib := filepath.Join(tmpDir, "lib")
	untagged := filepath.Join(tmpDir, "untagged")
	if err := os.MkdirAll(lib, 0755); err != nil {
		t.Fatal(err)
	}
	writeTaggedMP3(t, filepath.Join(untagged, "fixed.mp3"), id3Tags{
		albumArtist: "Singer", album: "Album", title: "Fixed", track: "1",
	}, "audio")
	writeTestFile(t, filepath.Join(untagged, "still broken.mp3"), "no tags")

	stats := Run(context.Background(), Options{Source: lib, Destination: lib, Untagged: untagged, Workers: 2}).Counters

	readTestFile(t, filepath.Join(lib, "Singer", "Album", "01 - Fixed.mp3"))
	readTestFile(t, filepath.Join(untagged, "still broken.mp3"))
	if stats.Organized != 1 || stats.LeftInPlace != 1 {
		t.Errorf("Expected 1 organized and 1 left, got %d and %d", stats.Organized, stats.LeftInPlace)
	}
	// That the untagged folder is never cleaned up, however empty, is
	// TestRootsAreNeverRemoved: here it still holds a file.
	mustExist(t, untagged)
}

// swappedLibrary is a library whose two songs must trade names: their tags
// were corrected after the last run. a is "01 - A.mp3", which holds song B,
// and b the other way round.
func swappedLibrary(t *testing.T) (lib, a, b string) {
	t.Helper()
	lib = t.TempDir()
	album := filepath.Join(lib, "Singer", "Album")
	a, b = filepath.Join(album, "01 - A.mp3"), filepath.Join(album, "02 - B.mp3")
	writeTaggedMP3(t, a, id3Tags{albumArtist: "Singer", album: "Album", title: "B", track: "2"}, "really B")
	writeTaggedMP3(t, b, id3Tags{albumArtist: "Singer", album: "Album", title: "A", track: "1"}, "really A, longer")
	return lib, a, b
}

// TestReorganizeSwapGetsCleanNames covers two files trading names: the first
// one moved finds its name still held and lands on " (1)", and the retry moves
// it onto the clean name once the other file has left.
func TestReorganizeSwapGetsCleanNames(t *testing.T) {
	lib, a, _ := swappedLibrary(t)
	album := filepath.Dir(a)

	stats := Run(context.Background(), Options{Source: lib, Destination: lib, Workers: 1}).Counters

	if stats.Errors != 0 {
		t.Fatalf("Expected no errors, got %d", stats.Errors)
	}
	entries, err := os.ReadDir(album)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	if len(names) != 2 || names[0] != "01 - A.mp3" || names[1] != "02 - B.mp3" {
		t.Fatalf("Expected exactly the two clean names, got %v", names)
	}
	if got := readTestFile(t, filepath.Join(album, "02 - B.mp3")); !strings.HasSuffix(got, "really B") {
		t.Errorf("02 - B.mp3 holds the wrong file: %q", got)
	}
	if stats.Retried != 1 {
		t.Errorf("Expected 1 retried rename, got %d", stats.Retried)
	}
}

func TestCueRewriteCanBeDisabled(t *testing.T) {
	lib := t.TempDir()
	album := filepath.Join(lib, "old")
	writeTaggedMP3(t, filepath.Join(album, "Song.mp3"), id3Tags{albumArtist: "Singer", album: "Album", title: "Song", track: "1"}, "audio")
	cue := "FILE \"Song.mp3\" MP3\n"
	writeTestFile(t, filepath.Join(album, "album.cue"), cue)

	Run(context.Background(), Options{Source: lib, Destination: lib, Workers: 1, NoCueRewrite: true})

	if got := readTestFile(t, filepath.Join(lib, "Singer", "Album", "album.cue")); got != cue {
		t.Errorf("With cue_rewrite off the cue must be moved unchanged, got %q", got)
	}
}

// TestReorganizeGathersCompilation checks that a compilation scattered across
// track-artist folders is gathered into the compilations folder, using the
// MusicBrainz album artist ID read by the real tag reader.
func TestReorganizeGathersCompilation(t *testing.T) {
	lib := t.TempDir()
	for i, artist := range []string{"Singer A", "Singer B feat. C"} {
		writeTaggedMP3(t, filepath.Join(lib, artist, "Hits", fmt.Sprintf("track%d.mp3", i)), id3Tags{
			artist: artist, album: "Hits", title: fmt.Sprintf("Song %d", i+1), year: "2012",
			track: fmt.Sprint(i + 1), albumArtistID: rules.VariousArtistsMBID,
		}, fmt.Sprintf("audio %d", i))
	}

	stats := Run(context.Background(), Options{Source: lib, Destination: lib, Workers: 2}).Counters

	if stats.Errors != 0 || stats.Organized != 2 {
		t.Fatalf("Expected 2 organized files and no errors, got %d and %d", stats.Organized, stats.Errors)
	}
	album := filepath.Join(lib, "Various Artists", "Hits (2012)")
	readTestFile(t, filepath.Join(album, "01 - Singer A - Song 1.mp3"))
	readTestFile(t, filepath.Join(album, "02 - Singer B feat. C - Song 2.mp3"))
	for _, artist := range []string{"Singer A", "Singer B feat. C"} {
		if _, err := os.Stat(filepath.Join(lib, artist)); !os.IsNotExist(err) {
			t.Errorf("Expected the scattered %q folder to be cleaned up", artist)
		}
	}
}

// TestPicardCompilationM4AGoesToCompilations is the regression test for MP4
// MusicBrainz IDs, which used to read with four leading NULs, so an M4A known
// as Various Artists only by its album artist ID stayed out of the
// compilations folder.
func TestPicardCompilationM4AGoesToCompilations(t *testing.T) {
	tmpDir := t.TempDir()
	source := filepath.Join(tmpDir, "src")
	dest := filepath.Join(tmpDir, "lib")
	writeTestFile(t, filepath.Join(source, "va.m4a"), readTestFile(t, fixtures.Path(fixtures.TaggedVAM4A)))

	stats := Run(context.Background(), Options{Source: source, Destination: dest, Workers: 1}).Counters

	if stats.Organized != 1 || stats.Errors != 0 {
		t.Fatalf("Expected 1 organized file and no errors, got %d and %d", stats.Organized, stats.Errors)
	}
	album := filepath.Join(dest, rules.DefaultCompilations, fmt.Sprintf("%s (%d)", fixtures.Album, fixtures.Year))
	entries, err := os.ReadDir(album)
	if err != nil || len(entries) != 1 {
		t.Fatalf("Expected the file in %q, got %v (%v)", album, entries, err)
	}
}

// manualLibraryPath is where a manual.<ext> fixture (disc 1 of 2) belongs in
// dest.
func manualLibraryPath(dest, ext string) string {
	return filepath.Join(dest, fixtures.AlbumArtist, fmt.Sprintf("%s (%d)", fixtures.Album, fixtures.Year),
		fmt.Sprintf("%d-%02d - %s%s", fixtures.Disc, fixtures.Track, fixtures.Title, ext))
}

// TestManualMarkerQualifies covers files tagged by hand in tagshelf: with the
// manual marker and an artist, album and title they organize like MusicBrainz
// tagged files; without the album, or with ripper tags and no marker, they go
// to _Untagged.
func TestManualMarkerQualifies(t *testing.T) {
	tmpDir := t.TempDir()
	source := filepath.Join(tmpDir, "src")
	dest := filepath.Join(tmpDir, "lib")
	untagged := filepath.Join(tmpDir, "untagged")
	for _, ext := range fixtures.ManualExtensions() {
		writeTestFile(t, filepath.Join(source, "hand"+ext), readTestFile(t, fixtures.Manual(ext)))
	}
	writeTestFile(t, filepath.Join(source, "noalbum.flac"), readTestFile(t, fixtures.Path(fixtures.ManualNoAlbumFLAC)))
	writeTestFile(t, filepath.Join(source, "ripped.flac"), readTestFile(t, fixtures.Path(fixtures.RipperFLAC)))

	stats := Run(context.Background(), Options{Source: source, Destination: dest, Untagged: untagged, Workers: 2}).Counters

	if want := int64(len(fixtures.ManualExtensions())); stats.Organized != want || stats.Untagged != 2 || stats.Errors != 0 {
		t.Errorf("Expected %d organized, 2 untagged and no errors, got %d, %d and %d",
			want, stats.Organized, stats.Untagged, stats.Errors)
	}
	for _, ext := range fixtures.ManualExtensions() {
		readTestFile(t, manualLibraryPath(dest, ext))
	}
	readTestFile(t, filepath.Join(untagged, "noalbum.flac"))
	readTestFile(t, filepath.Join(untagged, "ripped.flac"))
}

// TestReorganizePullsManuallyTaggedFiles checks a reorganize applies the same
// rule: a marked file in _Untagged joins the library once its tags are
// complete, and one still missing its album stays put.
func TestReorganizePullsManuallyTaggedFiles(t *testing.T) {
	tmpDir := t.TempDir()
	lib := filepath.Join(tmpDir, "lib")
	untagged := filepath.Join(tmpDir, "untagged")
	if err := os.MkdirAll(lib, 0755); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(untagged, "hand.flac"), readTestFile(t, fixtures.Manual(".flac")))
	writeTestFile(t, filepath.Join(untagged, "noalbum.flac"), readTestFile(t, fixtures.Path(fixtures.ManualNoAlbumFLAC)))
	writeTestFile(t, filepath.Join(lib, "Loose", "ripped.flac"), readTestFile(t, fixtures.Path(fixtures.RipperFLAC)))

	stats := Run(context.Background(), Options{Source: lib, Destination: lib, Untagged: untagged, Workers: 2}).Counters

	if stats.Organized != 1 || stats.LeftInPlace != 2 || stats.Errors != 0 {
		t.Errorf("Expected 1 organized, 2 left and no errors, got %d, %d and %d",
			stats.Organized, stats.LeftInPlace, stats.Errors)
	}
	readTestFile(t, manualLibraryPath(lib, ".flac"))
	readTestFile(t, filepath.Join(untagged, "noalbum.flac"))
	readTestFile(t, filepath.Join(lib, "Loose", "ripped.flac"))
}
