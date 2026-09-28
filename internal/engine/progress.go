package engine

import (
	"sync"
	"sync/atomic"
	"time"
)

// phaseTracker follows the current phase and how far it has got.
type phaseTracker struct {
	// mu is held while PhaseStarted and Progress are sent, so that no
	// Progress for one phase reaches the sink after the next has started.
	mu           sync.Mutex
	phase        Phase
	started      time.Time
	files, bytes int64 // the phase's totals, 0 when unknown

	doneFiles, doneBytes atomic.Int64
}

// set starts phase with the given totals. The caller holds mu.
func (p *phaseTracker) set(phase Phase, files, bytes int64, now time.Time) {
	p.phase = phase
	p.started = now
	p.files, p.bytes = files, bytes
	p.doneFiles.Store(0)
	p.doneBytes.Store(0)
}

// advance records one file, of the given size, done in the current phase.
func (p *phaseTracker) advance(size int64) {
	p.doneFiles.Add(1)
	p.doneBytes.Add(size)
}

// progress reports the current phase at now. The caller holds mu.
func (p *phaseTracker) progress(c Counters, now time.Time) Progress {
	pr := Progress{
		Counters:   c,
		Phase:      p.phase,
		Files:      p.doneFiles.Load(),
		FilesTotal: p.files,
		Bytes:      p.doneBytes.Load(),
		BytesTotal: p.bytes,
		At:         now,
	}
	elapsed := now.Sub(p.started).Seconds()
	if elapsed <= 0 || pr.Files == 0 {
		return pr
	}
	pr.Rate = float64(pr.Files) / elapsed
	pr.ByteRate = float64(pr.Bytes) / elapsed
	// Moving a file is mostly a rename, whatever its size, so the ETA goes by
	// files rather than bytes.
	if left := pr.FilesTotal - pr.Files; left > 0 {
		pr.ETA = time.Duration(float64(left) / pr.Rate * float64(time.Second))
	}
	return pr
}

// startPhase begins phase and tells the sink.
func (o *organizer) startPhase(phase Phase, files, bytes int64) {
	o.phase.mu.Lock()
	defer o.phase.mu.Unlock()
	o.phase.set(phase, files, bytes, time.Now())
	o.emit(PhaseStarted{Phase: phase, Files: files, Bytes: bytes})
}

// sendProgress tells the sink how the current phase is going.
func (o *organizer) sendProgress(now time.Time) {
	o.phase.mu.Lock()
	defer o.phase.mu.Unlock()
	o.emit(o.phase.progress(o.stats.snapshot(), now))
}
