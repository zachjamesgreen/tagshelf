package engine

import (
	"context"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
)

// collector is a Sink that keeps every event, as a test sees them.
type collector struct {
	mu     sync.Mutex
	events []Event
}

func (c *collector) sink(e Event) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.events = append(c.events, e)
}

// fileErrors returns the FileError events, in the order they came.
func (c *collector) fileErrors() []FileError {
	c.mu.Lock()
	defer c.mu.Unlock()
	var errs []FileError
	for _, e := range c.events {
		if fe, ok := e.(FileError); ok {
			errs = append(errs, fe)
		}
	}
	return errs
}

// warnings returns the Warning events of the given kind.
func (c *collector) warnings(kind WarningKind) []Warning {
	c.mu.Lock()
	defer c.mu.Unlock()
	var warnings []Warning
	for _, e := range c.events {
		if w, ok := e.(Warning); ok && w.Kind == kind {
			warnings = append(warnings, w)
		}
	}
	return warnings
}

// placed returns the outcome of each FilePlaced event, keyed by path.
func (c *collector) placed() map[string]Outcome {
	got := make(map[string]Outcome)
	for _, e := range c.events {
		if p, ok := e.(FilePlaced); ok {
			got[p.Path] = p.Outcome
		}
	}
	return got
}

func TestRunReportsEvents(t *testing.T) {
	tmpDir := t.TempDir()
	source := filepath.Join(tmpDir, "src")
	dest := filepath.Join(tmpDir, "lib")
	tagged := filepath.Join(source, "Rip", "song.mp3")
	loose := filepath.Join(source, "loose.mp3")
	art := filepath.Join(source, "Rip", "cover.jpg")
	writeTaggedMP3(t, tagged, id3Tags{albumArtist: "Singer", album: "Album", title: "Song", track: "1"}, "audio")
	writeTestFile(t, loose, "no tags")
	writeTestFile(t, art, "art")

	var c collector
	counters := Run(context.Background(), Options{Source: source, Destination: dest, Workers: 2, Sink: c.sink}).Counters

	if len(c.events) < 2 {
		t.Fatalf("Expected at least RunStarted and RunFinished, got %d events", len(c.events))
	}
	start, ok := c.events[0].(RunStarted)
	if !ok {
		t.Fatalf("First event must be RunStarted, got %T", c.events[0])
	}
	want := RunStarted{Kind: KindOrganize, Source: source, Destination: dest, Roots: []string{source}}
	if !reflect.DeepEqual(start, want) {
		t.Errorf("RunStarted = %+v, want %+v", start, want)
	}
	finish, ok := c.events[len(c.events)-1].(RunFinished)
	if !ok {
		t.Fatalf("Last event must be RunFinished, got %T", c.events[len(c.events)-1])
	}
	if finish.State != StateOK || finish.Counters != counters {
		t.Errorf("RunFinished = %+v, want StateOK with %+v", finish, counters)
	}

	wantPlaced := map[string]Outcome{tagged: Organized, loose: Untagged, art: Companion}
	if got := c.placed(); !reflect.DeepEqual(got, wantPlaced) {
		t.Errorf("FilePlaced outcomes = %v, want %v", got, wantPlaced)
	}
	var removed []string
	for _, e := range c.events {
		switch e := e.(type) {
		case FileError:
			t.Errorf("Unexpected FileError: %+v", e)
		case DirRemoved:
			removed = append(removed, e.Path)
		}
	}
	if want := []string{filepath.Join(source, "Rip")}; !reflect.DeepEqual(removed, want) {
		t.Errorf("DirRemoved = %v, want %v", removed, want)
	}
}

func TestRunReportsFileErrors(t *testing.T) {
	tmpDir := t.TempDir()
	source := filepath.Join(tmpDir, "src")
	song := filepath.Join(source, "song.mp3")
	writeTestFile(t, song, "no tags")
	// A file where the untagged folder should be makes every move there fail.
	blocker := filepath.Join(tmpDir, "blocker")
	writeTestFile(t, blocker, "not a directory")

	var c collector
	counters := Run(context.Background(), Options{
		Source: source, Destination: filepath.Join(tmpDir, "lib"), Untagged: filepath.Join(blocker, "untagged"),
		Workers: 1, Sink: c.sink,
	}).Counters

	errs := c.fileErrors()
	if len(errs) != 1 || errs[0].Path != song || errs[0].Op != OpResolve || errs[0].Err == nil {
		t.Fatalf("Expected one OpResolve FileError for %q, got %+v", song, errs)
	}
	if counters.Errors != 1 {
		t.Errorf("Expected 1 error counted, got %d", counters.Errors)
	}
}

func TestReorganizeReportsLeftAndRenamed(t *testing.T) {
	lib, _, _ := swappedLibrary(t)
	stray := filepath.Join(lib, "stray.mp3")
	writeTestFile(t, stray, "no tags")

	var c collector
	Run(context.Background(), Options{Source: lib, Destination: lib, Workers: 1, Sink: c.sink})

	if start := c.events[0].(RunStarted); start.Kind != KindReorganize {
		t.Errorf("Expected a reorganize, got kind %v", start.Kind)
	}
	var renamed int
	for _, e := range c.events {
		if p, ok := e.(FilePlaced); ok && p.Outcome == Renamed {
			renamed++
		}
	}
	if renamed != 1 {
		t.Errorf("Expected one Renamed event, got %d", renamed)
	}
	if got := c.placed()[stray]; got != Left {
		t.Errorf("Expected %q to be Left, got %v", stray, got)
	}
}
