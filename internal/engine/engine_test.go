package engine

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf16"

	"tagshelf/internal/fixtures"
	"tagshelf/internal/rules"
)

func TestComputeSHA256(t *testing.T) {
	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "test.txt")
	content := []byte("hello world audio test")
	if err := os.WriteFile(filePath, content, 0644); err != nil {
		t.Fatalf("Failed to write test file: %v", err)
	}

	hash1, err := computeSHA256(filePath)
	if err != nil {
		t.Fatalf("computeSHA256 failed: %v", err)
	}

	hash2, err := computeSHA256(filePath)
	if err != nil {
		t.Fatalf("computeSHA256 failed on second run: %v", err)
	}

	if hash1 != hash2 {
		t.Errorf("Hashes do not match: %s != %s", hash1, hash2)
	}
	if len(hash1) != 64 {
		t.Errorf("Expected 64 char hex hash, got %d: %s", len(hash1), hash1)
	}
}

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
	target, outcome, err := o.resolveDestination(srcFile, dstFile)
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
	srcFile := filepath.Join(tmpDir, "src.mp3")
	dstFile := filepath.Join(tmpDir, "dst.mp3")
	data := []byte("audio data sample 1")

	if err := os.WriteFile(srcFile, data, 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dstFile, data, 0644); err != nil {
		t.Fatal(err)
	}

	o := testOrganizer(tmpDir)
	target, outcome, err := o.resolveDestination(srcFile, dstFile)
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
	if _, err := os.Stat(filepath.Join(tmpDir, "dst (1).mp3")); !os.IsNotExist(err) {
		t.Errorf("Expected no suffixed copy to be created for a duplicate")
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
	target, outcome, err := o.resolveDestination(srcFile, dstFile)
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
	target, outcome, err := o.resolveDestination(srcFile, dstFile)
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
	target, outcome, err := o.resolveDestination(srcFile, dstFile)
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
	target, outcome, err := o.resolveDestination(srcFile, srcFile)
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

	targetA, outcomeA, err := o.resolveDestination(srcA, desired)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	targetB, outcomeB, err := o.resolveDestination(srcB, desired)
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
	if _, _, err := o.resolveDestination(srcFile, filepath.Join(blocker, "dst.mp3")); err == nil {
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
	target, _, err := o.resolveDestination(srcFile, dstFile)
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
	target, outcome, err := o.resolveDestination(srcFile, dstFile)
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
	parent := filepath.Join("C:", "Music")
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
		{filepath.Join("C:", "Other"), false},
		{filepath.Join("C:"), false},
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

	cleanupEmptyDirectories([]string{srcDir}, []string{destDir, untaggedDir}, nil)

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
	src := filepath.Join("C:", "src")
	albumA := filepath.Join("C:", "dest", "Artist", "A")
	albumB := filepath.Join("C:", "dest", "Artist", "B")
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
		got, ok := companionDir(src, tree, tt.dir)
		if got != tt.want || ok != tt.wantOK {
			t.Errorf("companionDir(%q) = %q, %t; want %q, %t", tt.dir, got, ok, tt.want, tt.wantOK)
		}
	}
}

func TestRunDryRunLeavesCompanions(t *testing.T) {
	tmpDir := t.TempDir()
	sourceDir := filepath.Join(tmpDir, "source", "rip")
	if err := os.MkdirAll(sourceDir, 0755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"track.mp3", "album.cue", "cover.png"} {
		if err := os.WriteFile(filepath.Join(sourceDir, name), []byte("data "+name), 0644); err != nil {
			t.Fatal(err)
		}
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
	for _, name := range []string{"track.mp3", "album.cue", "cover.png"} {
		if _, err := os.Stat(filepath.Join(sourceDir, name)); err != nil {
			t.Errorf("A dry run must leave %s in place: %v", name, err)
		}
	}
	if _, err := os.Stat(filepath.Join(tmpDir, "dest")); !os.IsNotExist(err) {
		t.Errorf("A dry run must not create the destination")
	}
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
		t.Errorf("Expected %d untagged moves, got %d (skipped %d)", count, got, stats.Skipped)
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

// TestRunIsIdempotent checks that a second run over the same
// destination neither duplicates files nor strands them.
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
		t.Errorf("Expected 1 file reported as already in place, got %d (organized %d, untagged %d, skipped %d)",
			stats.InPlace, stats.Organized, stats.Untagged, stats.Skipped)
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
	var cache DirectoryCache
	targetDir := filepath.Join(tmpDir, "artist", "album")

	// First call creates dir
	if err := cache.EnsureDir(targetDir); err != nil {
		t.Fatalf("EnsureDir failed on first call: %v", err)
	}
	if fi, err := os.Stat(targetDir); err != nil || !fi.IsDir() {
		t.Fatalf("Directory was not created")
	}

	// Second call should return nil and use cache
	if err := cache.EnsureDir(targetDir); err != nil {
		t.Fatalf("EnsureDir failed on cached call: %v", err)
	}
}

// TestDirectoryCacheIsPerRun guards against a process-global cache handing stale
// "already created" answers to a later run.
func TestDirectoryCacheIsPerRun(t *testing.T) {
	tmpDir := t.TempDir()
	dir := filepath.Join(tmpDir, "artist", "album")

	first := testOrganizer(tmpDir)
	if err := first.dirCache.EnsureDir(dir); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Join(tmpDir, "artist")); err != nil {
		t.Fatal(err)
	}

	second := testOrganizer(tmpDir)
	if err := second.dirCache.EnsureDir(dir); err != nil {
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

func BenchmarkComputeSHA256(b *testing.B) {
	tmpDir := b.TempDir()
	sampleFile := filepath.Join(tmpDir, "bench_sample.bin")
	data := make([]byte, 1024*1024) // 1MB sample
	for i := range data {
		data[i] = byte(i % 256)
	}
	if err := os.WriteFile(sampleFile, data, 0644); err != nil {
		b.Fatal(err)
	}

	b.ResetTimer()
	b.SetBytes(int64(len(data)))
	for i := 0; i < b.N; i++ {
		_, err := computeSHA256(sampleFile)
		if err != nil {
			b.Fatal(err)
		}
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
		if _, _, err := o.resolveDestination(srcFile, dstFile); err != nil {
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
	frame("UFID", []byte("http://musicbrainz.org\x00c83984e7-47b2-4d76-8098-b80c10a48ad1"))

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
	// Artist-level art and audio without tags must stay where they are.
	writeTestFile(t, filepath.Join(lib, "Singer feat. Guest", "artist.jpg"), "artist art")
	writeTestFile(t, filepath.Join(lib, "Hand Placed", "track.mp3"), "no tags here")

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
	if _, err := os.Stat(untagged); err != nil {
		t.Errorf("The untagged folder itself must never be cleaned up: %v", err)
	}
}

// TestReorganizeSwapGetsCleanNames covers two files trading names: the first
// one moved finds its name still held and lands on " (1)", and the retry moves
// it onto the clean name once the other file has left.
func TestReorganizeSwapGetsCleanNames(t *testing.T) {
	lib := t.TempDir()
	album := filepath.Join(lib, "Singer", "Album")
	// Tags were corrected after the last run: the names are now swapped.
	writeTaggedMP3(t, filepath.Join(album, "01 - A.mp3"), id3Tags{albumArtist: "Singer", album: "Album", title: "B", track: "2"}, "really B")
	writeTaggedMP3(t, filepath.Join(album, "02 - B.mp3"), id3Tags{albumArtist: "Singer", album: "Album", title: "A", track: "1"}, "really A, longer")

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

func TestCueLeftAloneWhenAudioIsElsewhere(t *testing.T) {
	tmpDir := t.TempDir()
	source := filepath.Join(tmpDir, "source")
	rip := filepath.Join(source, "rip")
	cue := "FILE \"missing.flac\" WAVE\n  TRACK 01 AUDIO\n"
	writeTestFile(t, filepath.Join(rip, "album.cue"), cue)

	other := filepath.Join(tmpDir, "other")
	stats := Run(context.Background(), Options{Source: source, Destination: filepath.Join(tmpDir, "dest"), Other: other, Workers: 1}).Counters

	moved := filepath.Join(other, "rip", "album.cue")
	if got := readTestFile(t, moved); got != cue {
		t.Errorf("A cue whose audio is not next to it must be left unchanged, got %q", got)
	}
	if _, err := os.Stat(moved + ".orig"); !os.IsNotExist(err) {
		t.Errorf("No .orig should be written when nothing was rewritten")
	}
	if stats.CueRewritten != 0 {
		t.Errorf("Expected no cue rewrites, got %d", stats.CueRewritten)
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
