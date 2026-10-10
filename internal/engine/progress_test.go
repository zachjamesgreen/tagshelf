package engine

import (
	"context"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestPhaseTrackerProgress(t *testing.T) {
	start := time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)
	counters := Counters{Organized: 3}

	tests := []struct {
		name                 string
		files, bytes         int64 // phase totals
		doneFiles, doneBytes int64
		elapsed              time.Duration
		want                 Progress
	}{
		{"part done", 100, 1000, 20, 400, 10 * time.Second, Progress{
			Phase: PhaseAudio, Files: 20, FilesTotal: 100, Bytes: 400, BytesTotal: 1000,
			Rate: 2, ByteRate: 40, ETA: 40 * time.Second,
		}},
		{"nothing done yet", 100, 1000, 0, 0, 10 * time.Second, Progress{
			Phase: PhaseAudio, FilesTotal: 100, BytesTotal: 1000,
		}},
		{"no time passed", 100, 1000, 5, 50, 0, Progress{
			Phase: PhaseAudio, Files: 5, FilesTotal: 100, Bytes: 50, BytesTotal: 1000,
		}},
		{"all done", 100, 1000, 100, 1000, 10 * time.Second, Progress{
			Phase: PhaseAudio, Files: 100, FilesTotal: 100, Bytes: 1000, BytesTotal: 1000,
			Rate: 10, ByteRate: 100,
		}},
		{"total unknown", 0, 0, 30, 300, 10 * time.Second, Progress{
			Phase: PhaseAudio, Files: 30, Bytes: 300, Rate: 3, ByteRate: 30,
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var p phaseTracker
			p.set(PhaseAudio, tt.files, tt.bytes, start)
			p.doneFiles.Store(tt.doneFiles)
			p.doneBytes.Store(tt.doneBytes)

			at := start.Add(tt.elapsed)
			want := tt.want
			want.Counters = counters
			want.At = at
			if got := p.progress(counters, at); got != want {
				t.Errorf("progress =\n%+v, want\n%+v", got, want)
			}
		})
	}
}

// The index phase reads every byte, so its ETA goes by the bytes left and
// not by the files: here most of the bytes are in files not yet read.
func TestPhaseTrackerIndexETAGoesByBytes(t *testing.T) {
	start := time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)
	var p phaseTracker
	p.set(PhaseIndex, 100, 1000, start)
	p.doneFiles.Store(50)
	p.doneBytes.Store(100)
	got := p.progress(Counters{}, start.Add(10*time.Second))
	if got.Phase != PhaseIndex || got.ByteRate != 10 || got.ETA != 90*time.Second {
		t.Errorf("progress = %+v, want 900 bytes left at 10 a second: 90 s, not the 10 s the files left would give", got)
	}
	// With every byte read there is nothing to wait for.
	p.doneBytes.Store(1000)
	if got := p.progress(Counters{}, start.Add(10*time.Second)); got.ETA != 0 {
		t.Errorf("ETA with every byte read = %v, want 0", got.ETA)
	}
}

// phases returns the phases in the order they started.
func (c *collector) phases() []Phase {
	c.mu.Lock()
	defer c.mu.Unlock()
	var got []Phase
	for _, e := range c.events {
		if p, ok := e.(PhaseStarted); ok {
			got = append(got, p.Phase)
		}
	}
	return got
}

// checkPhaseOrder checks that every FilePlaced and Progress event arrives
// during the phase it belongs to.
func (c *collector) checkPhaseOrder(t *testing.T) {
	t.Helper()
	c.mu.Lock()
	defer c.mu.Unlock()
	phase := Phase(-1)
	for i, e := range c.events {
		switch e := e.(type) {
		case PhaseStarted:
			phase = e.Phase
		case Progress:
			if e.Phase != phase {
				t.Errorf("event %d: Progress for %v during %v", i, e.Phase, phase)
			}
		case FilePlaced:
			want := PhaseAudio
			switch e.Outcome {
			case Other, Companion:
				want = PhaseCompanions
			case Renamed:
				want = PhaseRetrySuffixed
			}
			// Non-audio duplicates and in-place hits belong to companions.
			if phase == PhaseCompanions && (e.Outcome == Duplicate || e.Outcome == InPlace) {
				want = PhaseCompanions
			}
			if phase != want {
				t.Errorf("event %d: %v placed during %v, want %v", i, e.Outcome, phase, want)
			}
		case CueRewritten:
			if phase != PhaseCue {
				t.Errorf("event %d: CueRewritten during %v", i, phase)
			}
		case DirRemoved:
			if phase != PhaseCleanup {
				t.Errorf("event %d: DirRemoved during %v", i, phase)
			}
		}
	}
}

// slowSink collects events and slows file placement down, so that a fast
// progress ticker gets to send Progress during every phase.
func (c *collector) slowSink(e Event) {
	c.sink(e)
	if _, ok := e.(FilePlaced); ok {
		time.Sleep(3 * time.Millisecond)
	}
}

func fastProgress(t *testing.T) {
	old := progressInterval
	progressInterval = time.Millisecond
	t.Cleanup(func() { progressInterval = old })
}

func TestRunPhaseOrderOrganize(t *testing.T) {
	fastProgress(t)
	tmpDir := t.TempDir()
	source := filepath.Join(tmpDir, "src")
	dest := filepath.Join(tmpDir, "lib")
	writeTaggedMP3(t, filepath.Join(source, "Rip", "song.mp3"), id3Tags{albumArtist: "Singer", album: "Album", title: "Song", track: "1"}, "audio one")
	writeTaggedMP3(t, filepath.Join(source, "Rip", "other.mp3"), id3Tags{albumArtist: "Singer", album: "Album", title: "Other", track: "2"}, "audio two")
	writeTestFile(t, filepath.Join(source, "loose.mp3"), "no tags")
	writeTestFile(t, filepath.Join(source, "Rip", "cover.jpg"), "art")
	writeTestFile(t, filepath.Join(source, "Rip", "album.cue"), "FILE \"song.mp3\" MP3\n")

	var c collector
	Run(context.Background(), Options{Source: source, Destination: dest, Workers: 2, Sink: c.slowSink})

	want := []Phase{PhaseWalk, PhaseAudio, PhaseCompanions, PhaseCue, PhaseCleanup}
	if got := c.phases(); !reflect.DeepEqual(got, want) {
		t.Errorf("phases = %v, want %v", got, want)
	}
	c.checkPhaseOrder(t)

	// The audio phase knows how many files and bytes it has to move.
	var audioBytes int64
	for _, e := range c.events {
		if p, ok := e.(PhaseStarted); ok && p.Phase == PhaseAudio {
			audioBytes = p.Bytes
			if p.Files != 3 {
				t.Errorf("audio phase Files = %d, want 3", p.Files)
			}
		}
	}
	if audioBytes <= 0 {
		t.Errorf("audio phase Bytes = %d, want the audio files' total size", audioBytes)
	}
	var sawProgress bool
	for _, e := range c.events {
		if p, ok := e.(Progress); ok && p.Phase == PhaseAudio {
			sawProgress = true
			if p.FilesTotal != 3 || p.BytesTotal != audioBytes || p.Files > 3 {
				t.Errorf("audio Progress = %+v, want totals 3 files, %d bytes", p, audioBytes)
			}
		}
	}
	if !sawProgress {
		t.Error("no Progress during the audio phase")
	}
}

func TestRunPhaseOrderReorganize(t *testing.T) {
	fastProgress(t)
	lib, _, _ := swappedLibrary(t)
	writeTestFile(t, filepath.Join(lib, "stray.mp3"), "no tags")

	var c collector
	Run(context.Background(), Options{Source: lib, Destination: lib, Workers: 1, Sink: c.slowSink})

	want := []Phase{PhaseWalk, PhaseAudio, PhaseCompanions, PhaseRetrySuffixed, PhaseCue, PhaseCleanup}
	if got := c.phases(); !reflect.DeepEqual(got, want) {
		t.Errorf("phases = %v, want %v", got, want)
	}
	c.checkPhaseOrder(t)
}

func TestRunPhaseOrderDryRunWithoutCues(t *testing.T) {
	lib := t.TempDir()
	writeTestFile(t, filepath.Join(lib, "stray.mp3"), "no tags")

	var c collector
	Run(context.Background(), Options{Source: lib, Destination: lib, Workers: 1, DryRun: true, NoCueRewrite: true, Sink: c.sink})

	want := []Phase{PhaseWalk, PhaseAudio, PhaseCompanions}
	if got := c.phases(); !reflect.DeepEqual(got, want) {
		t.Errorf("phases = %v, want %v", got, want)
	}
}

func TestPhaseNames(t *testing.T) {
	want := map[Phase]string{
		PhaseWalk: "walk", PhaseIndex: "index", PhaseAudio: "audio", PhaseCompanions: "companions",
		PhaseRetrySuffixed: "retry-suffixed", PhaseCue: "cue", PhaseCleanup: "cleanup",
	}
	for p, name := range want {
		if got := p.String(); got != name {
			t.Errorf("Phase(%d).String() = %q, want %q", p, got, name)
		}
	}
}
