package engine

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

// lastFinished returns the RunFinished event that must close every run.
func (c *collector) lastFinished(t *testing.T) RunFinished {
	t.Helper()
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.events) == 0 {
		t.Fatal("no events")
	}
	finish, ok := c.events[len(c.events)-1].(RunFinished)
	if !ok {
		t.Fatalf("Last event must be RunFinished, got %T", c.events[len(c.events)-1])
	}
	return finish
}

func (c *collector) count(match func(Event) bool) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := 0
	for _, e := range c.events {
		if match(e) {
			n++
		}
	}
	return n
}

// TestRunCancelledMidRun cancels the context as the first file is placed:
// that move finishes, nothing else is handed out, and cleanup is skipped.
func TestRunCancelledMidRun(t *testing.T) {
	tmpDir := t.TempDir()
	source := filepath.Join(tmpDir, "src")
	dest := filepath.Join(tmpDir, "lib")
	const albums = 5
	var songs []string
	for i := range albums {
		song := filepath.Join(source, fmt.Sprintf("Rip%d", i), "song.mp3")
		writeTaggedMP3(t, song, id3Tags{albumArtist: "Singer", album: fmt.Sprintf("Album %d", i), title: "Song", track: "1"}, fmt.Sprintf("audio %d", i))
		songs = append(songs, song)
	}
	art := filepath.Join(source, "Rip0", "cover.jpg")
	writeTestFile(t, art, "art")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var c collector
	sink := func(e Event) {
		c.sink(e)
		if _, ok := e.(FilePlaced); ok {
			cancel()
		}
	}
	finish := Run(ctx, Options{Source: source, Destination: dest, Workers: 1, Sink: sink})

	if finish.State != StateInterrupted {
		t.Errorf("State = %v, want StateInterrupted", finish.State)
	}
	if got := c.lastFinished(t); got != finish {
		t.Errorf("RunFinished event = %+v, Run returned %+v", got, finish)
	}
	if n := finish.Counters.Organized; n < 1 || n >= albums {
		t.Errorf("Organized = %d, want at least 1 and fewer than %d", n, albums)
	}

	// Every song is whole in exactly one place, and no claim placeholder is
	// left behind in the library.
	left := 0
	for _, song := range songs {
		if _, err := os.Stat(song); err == nil {
			left++
		}
	}
	var inLibrary int
	filepath.WalkDir(dest, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if info.Size() == 0 {
			t.Errorf("empty placeholder left in the library: %s", path)
		}
		inLibrary++
		return nil
	})
	if left+inLibrary != albums || int64(inLibrary) != finish.Counters.Organized {
		t.Errorf("%d songs left in source and %d in the library, want %d in all with %d organized",
			left, inLibrary, albums, finish.Counters.Organized)
	}

	if _, err := os.Stat(art); err != nil {
		t.Errorf("companion was moved after cancellation: %v", err)
	}
	if n := c.count(func(e Event) bool { _, ok := e.(DirRemoved); return ok }); n != 0 {
		t.Errorf("cleanup ran after cancellation: %d directories removed", n)
	}
}

func TestRunCancelledBeforeStart(t *testing.T) {
	tmpDir := t.TempDir()
	source := filepath.Join(tmpDir, "src")
	song := filepath.Join(source, "Rip", "song.mp3")
	writeTaggedMP3(t, song, id3Tags{albumArtist: "Singer", album: "Album", title: "Song", track: "1"}, "audio")

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var c collector
	finish := Run(ctx, Options{Source: source, Destination: filepath.Join(tmpDir, "lib"), Workers: 2, Sink: c.sink})

	if finish.State != StateInterrupted {
		t.Errorf("State = %v, want StateInterrupted", finish.State)
	}
	if _, err := os.Stat(song); err != nil {
		t.Errorf("song moved by a cancelled run: %v", err)
	}
	if _, ok := c.events[0].(RunStarted); !ok {
		t.Errorf("First event must be RunStarted, got %T", c.events[0])
	}
	c.lastFinished(t)
}

func TestRunStateErrors(t *testing.T) {
	tmpDir := t.TempDir()
	source := filepath.Join(tmpDir, "src")
	writeTestFile(t, filepath.Join(source, "loose.mp3"), "no tags")
	// The untagged folder is a file, so the untagged song cannot be placed.
	untagged := filepath.Join(tmpDir, "untagged")
	writeTestFile(t, untagged, "in the way")

	finish := Run(context.Background(), Options{Source: source, Destination: filepath.Join(tmpDir, "lib"), Untagged: untagged, Workers: 1})

	if finish.State != StateErrors || finish.Counters.Errors == 0 {
		t.Errorf("RunFinished = %+v, want StateErrors with errors counted", finish)
	}
}
