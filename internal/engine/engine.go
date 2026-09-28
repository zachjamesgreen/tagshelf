// Package engine organizes audio files into a library. Run does one organize
// or reorganize pass and reports what happens to a sink as events.
package engine

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"tagshelf/internal/rules"
	"tagshelf/internal/store"
	"tagshelf/internal/tag"
)

// maxCollisionSuffix bounds the " (n)" search so that a permanently unusable
// destination name can never spin a worker forever.
const maxCollisionSuffix = 10000

// progressInterval is how often a run sends Progress. Tests shorten it.
var progressInterval = 250 * time.Millisecond

// Counters are a run's totals.
type Counters struct {
	TotalFound int64
	Organized  int64
	Untagged   int64
	Skipped    int64
	InPlace    int64
	Errors     int64
	// Non-audio files (cue sheets, artwork, logs, ...). Their duplicates,
	// in-place hits and errors are counted in Skipped, InPlace and Errors.
	OtherFound int64
	OtherMoved int64
	// Reorganize only: audio that does not qualify for the library stays put.
	LeftInPlace int64
	// Files moved onto their clean name after first landing on a " (n)" one.
	Retried      int64
	CueRewritten int64
	// Audio files read into the index.
	Indexed int64
}

// stats is the live, concurrently updated form of Counters.
type stats struct {
	TotalFound   atomic.Int64
	Organized    atomic.Int64
	Untagged     atomic.Int64
	Skipped      atomic.Int64
	InPlace      atomic.Int64
	Errors       atomic.Int64
	OtherFound   atomic.Int64
	OtherMoved   atomic.Int64
	LeftInPlace  atomic.Int64
	Retried      atomic.Int64
	CueRewritten atomic.Int64
	Indexed      atomic.Int64
}

func (s *stats) snapshot() Counters {
	return Counters{
		TotalFound:   s.TotalFound.Load(),
		Organized:    s.Organized.Load(),
		Untagged:     s.Untagged.Load(),
		Skipped:      s.Skipped.Load(),
		InPlace:      s.InPlace.Load(),
		Errors:       s.Errors.Load(),
		OtherFound:   s.OtherFound.Load(),
		OtherMoved:   s.OtherMoved.Load(),
		LeftInPlace:  s.LeftInPlace.Load(),
		Retried:      s.Retried.Load(),
		CueRewritten: s.CueRewritten.Load(),
		Indexed:      s.Indexed.Load(),
	}
}

// Options configure a run. A Source equal to Destination makes it a
// reorganize of that library.
type Options struct {
	Source       string
	Destination  string
	Untagged     string // default: <Destination>/_Untagged
	Other        string // default: <Destination>/_Other
	Duplicates   string // default: <Destination>/_Duplicates
	Compilations string // default: rules.DefaultCompilations
	Workers      int    // default: the number of CPUs
	DryRun       bool
	NoCueRewrite bool // leave the FILE lines of moved cue sheets alone
	Sink         Sink // may be nil
	// Store records the run, its moves and its errors, and keeps the index.
	// Nil records nothing and keeps no index.
	Store *store.Store
	// Rescan re-reads every file into the index, not just new and changed ones.
	Rescan bool
	// IndexOnly brings the index of the library, untagged, other and
	// duplicates folders up to date and moves nothing. Source is ignored.
	IndexOnly bool
}

// Reorganizing reports whether the run re-sorts an existing library in place.
func (o Options) Reorganizing() bool {
	return !o.IndexOnly && o.Source != "" && rules.PathKey(o.Source) == rules.PathKey(o.Destination)
}

// withDefaults fills in the values a run needs but the caller may have left
// at zero.
func (o Options) withDefaults() Options {
	if o.Untagged == "" && o.Destination != "" {
		o.Untagged = filepath.Join(o.Destination, "_Untagged")
	}
	if o.Other == "" && o.Destination != "" {
		o.Other = filepath.Join(o.Destination, "_Other")
	}
	if o.Duplicates == "" && o.Destination != "" {
		o.Duplicates = filepath.Join(o.Destination, "_Duplicates")
	}
	if o.Compilations == "" {
		o.Compilations = rules.DefaultCompilations
	}
	if o.Workers <= 0 {
		o.Workers = runtime.NumCPU()
	}
	if o.Workers < 1 {
		o.Workers = 1
	}
	if o.Sink == nil {
		o.Sink = func(Event) {}
	}
	return o
}

var supportedExtensions = map[string]bool{
	".mp3":  true,
	".flac": true,
	".m4a":  true,
	".ogg":  true,
	".wma":  true,
	".aac":  true,
	".ape":  true,
	".mpc":  true,
	".m4p":  true,
	".m4b":  true,
	".m4r":  true,
	".wav":  true,
}

func isAudioFile(path string) bool {
	ext := strings.ToLower(filepath.Ext(path))
	return supportedExtensions[ext]
}

type DirectoryCache struct {
	created sync.Map
}

func (c *DirectoryCache) EnsureDir(dir string) error {
	if _, ok := c.created.Load(dir); ok {
		return nil
	}
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	c.created.Store(dir, struct{}{})
	return nil
}

func computeSHA256(filePath string) (string, error) {
	f, err := os.Open(filePath)
	if err != nil {
		return "", err
	}
	defer f.Close()

	h := sha256.New()
	// io.Copy takes *os.File's WriteTo fast path; an explicit buffer would only
	// be ignored.
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// collisionOutcome says what should happen to a source file once its
// destination has been resolved.
type collisionOutcome int

const (
	// outcomeMove: the returned path is reserved and the file should be moved there.
	outcomeMove collisionOutcome = iota
	// outcomeDuplicate: a byte-identical copy already sits at the returned path.
	outcomeDuplicate
	// outcomeInPlace: the source file already *is* the file at the returned path.
	outcomeInPlace
	// outcomeCaseFix: as outcomeInPlace, but the returned path spells the file
	// or a folder above it in another case, which a case-insensitive file
	// system treats as the same path. Reorganize only.
	outcomeCaseFix
)

// organizer holds the per-run state shared by the worker goroutines. Keeping it
// per-run rather than in package globals means one run can never hand stale
// directory or destination state to the next.
type organizer struct {
	cfg      Options
	stats    *stats
	dirCache DirectoryCache
	reserved sync.Map // rules.PathKey(path) -> struct{}
	touched  sync.Map // source directory -> struct{}
	phase    phaseTracker
	runID    int64 // the run's row in cfg.Store

	// albumsMu guards albums: source directory -> album directories its audio
	// files were organized into. Untagged audio is recorded as "".
	albumsMu sync.Mutex
	albums   map[string]map[string]struct{}

	// placed maps rules.PathKey(source path) to where that file's content now
	// lives: its new path, its unchanged path, or an identical copy. In a dry
	// run it holds the planned paths. Cue rewriting resolves FILE lines with it.
	placed sync.Map

	// suffixed lists files that landed on a " (n)" name because their clean
	// name was taken; a reorganize retries them once everything has moved.
	suffixedMu sync.Mutex
	suffixed   []suffixedMove

	// caseFixes lists files in place apart from letter case; a reorganize
	// renames them onto the exact spelling once everything has moved.
	caseFixesMu sync.Mutex
	caseFixes   []caseFix
}

type caseFix struct {
	src, desired string
}

type suffixedMove struct {
	src, desired, final string
}

func newOrganizer(opts Options) *organizer {
	return &organizer{
		cfg:    opts.withDefaults(),
		stats:  &stats{},
		albums: make(map[string]map[string]struct{}),
	}
}

func (o *organizer) emit(e Event) {
	o.cfg.Sink(e)
}

func (o *organizer) fail(path, dst string, op Operation, err error) {
	o.stats.Errors.Add(1)
	e := FileError{Path: path, Destination: dst, Op: op, Err: err}
	if o.cfg.Store != nil && o.runID != 0 {
		// A database that fails here has already failed the write being
		// reported, or will fail FinishRun; either way the log has the error.
		_ = o.cfg.Store.RecordError(o.runID, path, errorMessage(e))
	}
	o.emit(e)
}

// errorMessage is how a FileError is worded in the run's error log.
func errorMessage(e FileError) string {
	if e.Destination != "" {
		return fmt.Sprintf("%s -> %s: %v", e.Op, e.Destination, e.Err)
	}
	return fmt.Sprintf("%s: %v", e.Op, e.Err)
}

// recordMove logs a move the run made. A move that cannot be logged counts
// as an error: undo would not know about it.
func (o *organizer) recordMove(from, to string, reason store.MoveReason) {
	if o.cfg.Store == nil {
		return
	}
	if err := o.cfg.Store.RecordMove(o.runID, from, to, reason); err != nil {
		o.fail(from, to, OpRecord, err)
	}
}

// moveReasons maps the outcome of a move to why the database says it moved.
var moveReasons = map[Outcome]store.MoveReason{
	Organized: store.ReasonOrganized,
	Untagged:  store.ReasonUntagged,
	Other:     store.ReasonOther,
	Companion: store.ReasonCompanion,
	Renamed:   store.ReasonRenamed,
}

// placedAt reports where the file that was at src now lives.
func (o *organizer) placedAt(src string) (string, bool) {
	v, ok := o.placed.Load(rules.PathKey(src))
	if !ok {
		return "", false
	}
	return v.(string), true
}

// recordAlbum notes where an audio file from srcDir went, so that the non-audio
// files next to it can follow. albumDir is "" for untagged audio.
func (o *organizer) recordAlbum(srcDir, albumDir string) {
	o.albumsMu.Lock()
	defer o.albumsMu.Unlock()
	set, ok := o.albums[srcDir]
	if !ok {
		set = make(map[string]struct{})
		o.albums[srcDir] = set
	}
	set[albumDir] = struct{}{}
}

// buildAlbumTree propagates each directory's album destinations up to source,
// so that every directory maps to the albums of all audio in its subtree.
func buildAlbumTree(source string, albums map[string]map[string]struct{}) map[string]map[string]struct{} {
	tree := make(map[string]map[string]struct{})
	for dir, set := range albums {
		curr := dir
		for {
			dst, ok := tree[curr]
			if !ok {
				dst = make(map[string]struct{})
				tree[curr] = dst
			}
			for album := range set {
				dst[album] = struct{}{}
			}
			if curr == source || !isSubdirectory(source, curr) {
				break
			}
			curr = filepath.Dir(curr)
		}
	}
	return tree
}

// companionDir picks the album directory a non-audio file in dir belongs with.
//
// It climbs from dir towards source until it reaches a directory whose subtree
// holds any audio. If all of that audio went to one tagged album, the file
// follows it, keeping its path below that directory (so Rip/Scans/front.jpg
// lands in Album/Scans). Mixed albums or untagged audio report false, as does
// a file with no audio anywhere above it: guessing wrong would bury artwork in
// an unrelated album.
func companionDir(source string, tree map[string]map[string]struct{}, dir string) (string, bool) {
	curr := dir
	for {
		if set := tree[curr]; len(set) > 0 {
			if len(set) != 1 {
				return "", false
			}
			for album := range set {
				if album == "" {
					return "", false
				}
				rel, err := filepath.Rel(curr, dir)
				if err != nil {
					return "", false
				}
				return filepath.Join(album, rel), true
			}
		}
		if curr == source || !isSubdirectory(source, curr) {
			return "", false
		}
		curr = filepath.Dir(curr)
	}
}

// claim takes exclusive ownership of a destination path. It reports false (with
// no error) when another worker or another process got there first.
//
// The in-memory map serializes workers inside this process; the O_EXCL create
// closes the window against anything outside it. Without this, two workers that
// both saw a free path would both rename into it and os.Rename would silently
// overwrite the first file, which by then no longer exists at its source.
func (o *organizer) claim(path string) (bool, error) {
	key := rules.PathKey(path)
	if _, loaded := o.reserved.LoadOrStore(key, struct{}{}); loaded {
		return false, nil
	}

	if o.cfg.DryRun {
		// Reserve in memory only: a preview must not touch the filesystem.
		return true, nil
	}

	dir := filepath.Dir(path)
	if err := o.dirCache.EnsureDir(dir); err != nil {
		o.reserved.Delete(key)
		return false, fmt.Errorf("failed to create directory %q: %w", dir, err)
	}

	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0666)
	if err != nil {
		o.reserved.Delete(key)
		if errors.Is(err, fs.ErrExist) {
			return false, nil
		}
		return false, fmt.Errorf("failed to reserve destination %q: %w", path, err)
	}
	if err := f.Close(); err != nil {
		o.reserved.Delete(key)
		os.Remove(path)
		return false, fmt.Errorf("failed to reserve destination %q: %w", path, err)
	}
	return true, nil
}

// release gives up a claim that never got used, removing the empty placeholder
// left behind by claim.
func (o *organizer) release(path string) {
	o.reserved.Delete(rules.PathKey(path))
	if o.cfg.DryRun {
		return
	}
	if info, err := os.Stat(path); err == nil && !info.IsDir() && info.Size() == 0 {
		os.Remove(path)
	}
}

// resolveDestination picks and reserves the path a source file should move to.
//
// Collisions are resolved by content, not by name: an identical copy already at
// the destination means the source is a duplicate and is left alone, while a
// *different* file under the same name gets the next free " (n)" suffix so it
// still makes it into the library.
func (o *organizer) resolveDestination(srcPath, desiredDst string) (string, collisionOutcome, error) {
	srcInfo, err := os.Stat(srcPath)
	if err != nil {
		return "", outcomeMove, fmt.Errorf("error stating source %q: %w", srcPath, err)
	}

	// Hashed at most once per source file, and only if some candidate is close
	// enough in size to be worth comparing.
	srcHash := ""
	sameContent := func(candidate string, candInfo os.FileInfo) (bool, error) {
		if candInfo.Size() != srcInfo.Size() {
			return false, nil
		}
		if srcHash == "" {
			srcHash, err = computeSHA256(srcPath)
			if err != nil {
				return false, fmt.Errorf("error hashing source %q: %w", srcPath, err)
			}
		}
		candHash, err := computeSHA256(candidate)
		if err != nil {
			return false, fmt.Errorf("error hashing destination %q: %w", candidate, err)
		}
		return srcHash == candHash, nil
	}

	dir := filepath.Dir(desiredDst)
	ext := filepath.Ext(desiredDst)
	base := strings.TrimSuffix(filepath.Base(desiredDst), ext)

	for i := 0; i <= maxCollisionSuffix; i++ {
		candidate := desiredDst
		if i > 0 {
			candidate = filepath.Join(dir, fmt.Sprintf("%s (%d)%s", base, i, ext))
		}

		// Another worker owns this name; do not stat or hash its placeholder.
		if _, taken := o.reserved.Load(rules.PathKey(candidate)); taken {
			continue
		}

		candInfo, statErr := os.Stat(candidate)
		switch {
		case statErr == nil:
			if os.SameFile(srcInfo, candInfo) {
				// The file is already where it belongs; renaming it would only
				// manufacture a "(1)" copy, and again on every later run.
				if o.cfg.Reorganizing() && candidate != srcPath && strings.EqualFold(candidate, srcPath) {
					return candidate, outcomeCaseFix, nil
				}
				return candidate, outcomeInPlace, nil
			}
			if candInfo.IsDir() {
				continue
			}
			identical, err := sameContent(candidate, candInfo)
			if err != nil {
				return "", outcomeMove, err
			}
			if identical {
				return candidate, outcomeDuplicate, nil
			}
			continue
		case errors.Is(statErr, fs.ErrNotExist):
			// Free, fall through and try to claim it.
		default:
			// Anything other than "not there" (a permissions problem, say) must
			// be reported rather than retried forever.
			return "", outcomeMove, fmt.Errorf("error stating destination %q: %w", candidate, statErr)
		}

		claimed, err := o.claim(candidate)
		if err != nil {
			return "", outcomeMove, err
		}
		if !claimed {
			continue
		}
		return candidate, outcomeMove, nil
	}

	return "", outcomeMove, fmt.Errorf("no free destination name for %q after %d attempts", desiredDst, maxCollisionSuffix)
}

func (o *organizer) moveFile(src, dst string) error {
	dstDir := filepath.Dir(dst)
	if err := o.dirCache.EnsureDir(dstDir); err != nil {
		return fmt.Errorf("failed to create directory %q: %w", dstDir, err)
	}

	// Fast atomic rename (replaces the placeholder left by claim)
	if err := os.Rename(src, dst); err == nil {
		return nil
	}

	// Fallback to copy and remove across filesystems/drives
	if err := copyFile(src, dst, func(w Warning) { o.emit(w) }); err != nil {
		return err
	}

	if err := os.Remove(src); err != nil {
		// On Windows a file another program holds open fails both the rename
		// and this; take the copy back so the file is not left in two places.
		os.Remove(dst)
		return fmt.Errorf("failed to remove source after copy: %w", err)
	}

	return nil
}

// copyFile copies src over dst, restoring the permissions and modification time
// that a rename would have carried across on its own. Without that, every
// cross-drive move silently resets both. Failing to restore either is only
// reported to warn.
func copyFile(src, dst string, warn func(Warning)) error {
	srcInfo, err := os.Stat(src)
	if err != nil {
		return fmt.Errorf("failed to stat source: %w", err)
	}

	in, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("failed to open source for copy: %w", err)
	}
	defer in.Close()

	out, err := os.Create(dst)
	if err != nil {
		return fmt.Errorf("failed to create destination for copy: %w", err)
	}

	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		os.Remove(dst)
		return fmt.Errorf("failed to copy file data: %w", err)
	}

	if err := out.Sync(); err != nil {
		out.Close()
		os.Remove(dst)
		return fmt.Errorf("failed to sync destination: %w", err)
	}

	if err := out.Close(); err != nil {
		os.Remove(dst)
		return fmt.Errorf("failed to close destination: %w", err)
	}

	if err := os.Chmod(dst, srcInfo.Mode().Perm()); err != nil {
		warn(Warning{Kind: WarnPermissions, Path: dst, Err: err})
	}
	if err := os.Chtimes(dst, time.Now(), srcInfo.ModTime()); err != nil {
		warn(Warning{Kind: WarnModTime, Path: dst, Err: err})
	}

	return nil
}

func isSubdirectory(parent, sub string) bool {
	rel, err := filepath.Rel(parent, sub)
	if err != nil {
		return false
	}
	if rel == "." || rel == ".." {
		return false
	}
	// A plain HasPrefix(rel, "..") would also reject real children whose names
	// start with dots, e.g. parent/..hidden.
	return !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// Run does one organize or reorganize pass, reporting to opts.Sink as it goes,
// and returns the RunFinished it ends with. RunStarted is always the first
// event and RunFinished the last.
//
// Cancelling ctx stops the run from handing out new files: moves already under
// way finish, the remaining files stay where they are, suffixed files are not
// retried and emptied directories are not cleaned up. Cue sheets that did move
// are still rewritten, since a later run would not revisit them.
func Run(ctx context.Context, opts Options) RunFinished {
	o := newOrganizer(opts)
	cfg := o.cfg
	stats := o.stats

	roots := o.walkRoots()
	indexRoots := o.indexRoots()
	walkDirs := slices.Clone(roots)
	for _, r := range indexRoots {
		if !containsPath(walkDirs, r.path) {
			walkDirs = append(walkDirs, r.path)
		}
	}
	kind := KindOrganize
	switch {
	case cfg.IndexOnly:
		kind = KindIndex
	case cfg.Reorganizing():
		kind = KindReorganize
	}
	source := cfg.Source
	if cfg.IndexOnly {
		source = ""
	}
	o.emit(RunStarted{Kind: kind, DryRun: cfg.DryRun, Source: source, Destination: cfg.Destination, Roots: walkDirs})

	if cfg.Store != nil {
		// A run that cannot be recorded does not touch any file.
		id, err := cfg.Store.StartRun(storeKinds[kind], cfg.DryRun, source, cfg.Destination, time.Now())
		if err != nil {
			finish := RunFinished{State: StateNotStarted, Err: err}
			o.emit(finish)
			return finish
		}
		o.runID = id
	}

	stopTicker := make(chan struct{})
	tickerDone := make(chan struct{})
	ticker := time.NewTicker(progressInterval)

	go func() {
		defer close(tickerDone)
		defer ticker.Stop()
		for {
			select {
			case <-stopTicker:
				return
			case now := <-ticker.C:
				o.sendProgress(now)
			}
		}
	}()

	// The whole tree is listed before anything moves. A reorganize walks the
	// very folders it moves files into, and a streaming walk would pick those
	// files, or the placeholders claim leaves, up a second time. The folders
	// the index covers are walked too, to find what changed in them.
	o.startPhase(PhaseWalk, 0, 0)
	var audio []walkedFile
	var companions []companionFile
	var audioBytes, companionBytes int64
	skip := append(slices.Clone(walkDirs), cfg.Destination, cfg.Untagged, cfg.Other, cfg.Duplicates)
	for _, dir := range walkDirs {
		isRunRoot := containsPath(roots, dir)
		var idx *indexRoot
		for _, r := range indexRoots {
			if rules.PathKey(r.path) == rules.PathKey(dir) {
				idx = r
			}
		}
		if !isRunRoot {
			// A folder only the index covers may not be there yet.
			if _, err := os.Stat(dir); errors.Is(err, fs.ErrNotExist) {
				idx.complete = true
				continue
			}
		}
		complete := o.walk(ctx, dir, skip, func(f walkedFile) {
			audioFile := isAudioFile(f.path)
			if idx != nil && audioFile && f.infoOK {
				idx.audio = append(idx.audio, f)
				if cfg.IndexOnly {
					stats.TotalFound.Add(1)
				}
			}
			o.phase.advance(f.size)
			if !isRunRoot {
				return
			}
			if audioFile {
				stats.TotalFound.Add(1)
				audio = append(audio, f)
				audioBytes += f.size
			} else {
				stats.OtherFound.Add(1)
				companions = append(companions, companionFile{walkedFile: f, root: dir})
				companionBytes += f.size
			}
		})
		if idx != nil {
			idx.complete = complete
		}
	}

	// Files the index lacks, or that changed since it read them, are read
	// before anything moves, so every file the run looks at has a row.
	if len(indexRoots) > 0 && ctx.Err() == nil {
		jobs, first := o.refreshIndex(ctx, indexRoots)
		if len(jobs) > 0 || cfg.IndexOnly {
			var bytes int64
			for _, j := range jobs {
				bytes += j.size
			}
			o.startIndexPhase(int64(len(jobs)), bytes, first && len(jobs) > 0)
			o.indexFiles(ctx, jobs)
		}
	}

	if cfg.IndexOnly {
		close(stopTicker)
		<-tickerDone
		return o.end(o.finish(ctx.Err() != nil))
	}

	o.startPhase(PhaseAudio, int64(len(audio)), audioBytes)
	runJobs(ctx, cfg.Workers, audio, func(f walkedFile) {
		o.processFile(f.path)
		o.phase.advance(f.size)
	})

	// Non-audio files go only after every audio file has been placed, because
	// where they go depends on where their neighbours went.
	tree := o.albums
	if !o.cfg.Reorganizing() {
		// A fresh rip may keep its audio in CD1/CD2 subfolders with the
		// artwork above them, so match on whole subtrees. An organized
		// library holds audio directly in album folders, and matching on
		// subtrees there would sweep artist-level images into an album.
		tree = buildAlbumTree(cfg.Source, o.albums)
	}
	o.startPhase(PhaseCompanions, int64(len(companions)), companionBytes)
	runJobs(ctx, cfg.Workers, companions, func(c companionFile) {
		o.processCompanion(c, tree)
		o.phase.advance(c.size)
	})

	if cfg.Reorganizing() && !cfg.DryRun && ctx.Err() == nil {
		o.startPhase(PhaseRetrySuffixed, int64(len(o.suffixed)+len(o.caseFixes)), 0)
		o.retrySuffixed(ctx)
		o.fixCase(ctx)
	}

	if !cfg.NoCueRewrite {
		var cues []string
		for _, c := range companions {
			if strings.EqualFold(filepath.Ext(c.path), ".cue") {
				cues = append(cues, c.path)
			}
		}
		o.startPhase(PhaseCue, int64(len(cues)), 0)
		for _, cue := range cues {
			o.rewriteCue(cue)
			o.phase.advance(0)
		}
	}

	interrupted := ctx.Err() != nil

	// Clean up emptied directories if not in dry-run
	if !cfg.DryRun && !interrupted {
		excluded := []string{cfg.Destination, cfg.Untagged, cfg.Other, cfg.Duplicates}
		if cfg.Reorganizing() {
			// The walked roots are the library itself; roots are never removed.
			excluded = nil
		}
		o.startPhase(PhaseCleanup, 0, 0)
		cleanupEmptyDirectories(roots, excluded, func(dir string) {
			o.emit(DirRemoved{Path: dir})
			o.phase.advance(0)
		}, &o.touched)
	}

	close(stopTicker)
	<-tickerDone

	return o.end(o.finish(interrupted))
}

// end records how the run ended and sends its RunFinished.
func (o *organizer) end(finish RunFinished) RunFinished {
	if st := o.cfg.Store; st != nil {
		if err := st.FinishRun(o.runID, store.Counters(finish.Counters), storeStates[finish.State], time.Now()); err != nil {
			o.fail(st.Path(), "", OpRecord, err)
			finish = o.finish(finish.State == StateInterrupted)
		}
	}
	o.emit(finish)
	return finish
}

// finish sums up how the run ended.
func (o *organizer) finish(interrupted bool) RunFinished {
	finish := RunFinished{Counters: o.stats.snapshot(), State: StateOK}
	switch {
	case interrupted:
		finish.State = StateInterrupted
	case finish.Counters.Errors > 0:
		finish.State = StateErrors
	}
	return finish
}

// storeKinds and storeStates map run kinds and end states to their database
// names.
var (
	storeKinds = map[RunKind]store.RunKind{
		KindOrganize:   store.KindOrganize,
		KindReorganize: store.KindReorganize,
		KindIndex:      store.KindIndex,
	}
	storeStates = map[RunState]store.RunState{
		StateOK:          store.StateOK,
		StateErrors:      store.StateErrors,
		StateInterrupted: store.StateInterrupted,
	}
)

// walkedFile is a file found by the walk, with its size and modification
// time. infoOK is false when they could not be read.
type walkedFile struct {
	path    string
	size    int64
	modTime time.Time
	infoOK  bool
}

// companionFile is a non-audio file and the walk root it was found under.
type companionFile struct {
	walkedFile
	root string
}

// walkRoots lists the directories to scan. A normal run scans the source. A
// reorganize scans the library plus the untagged and other folders, wherever
// they are, so that files tagged since the last run make it into the library.
func (o *organizer) walkRoots() []string {
	if o.cfg.IndexOnly {
		return nil
	}
	if !o.cfg.Reorganizing() {
		return []string{o.cfg.Source}
	}
	roots := []string{o.cfg.Destination}
	for _, dir := range []string{o.cfg.Untagged, o.cfg.Other} {
		if dir == "" || containsPath(roots, dir) {
			continue
		}
		if info, err := os.Stat(dir); err != nil || !info.IsDir() {
			continue
		}
		roots = append(roots, dir)
	}
	return roots
}

func containsPath(paths []string, path string) bool {
	key := rules.PathKey(path)
	for _, p := range paths {
		if rules.PathKey(p) == key {
			return true
		}
	}
	return false
}

// walk lists the files under root, calling fn for each, and reports whether
// it listed all of them. Folders in skip below root are left out: the output
// folders and the other walked folders, which are walked on their own.
func (o *organizer) walk(ctx context.Context, root string, skip []string, fn func(f walkedFile)) bool {
	complete := true
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if ctx.Err() != nil {
			complete = false
			return filepath.SkipAll
		}
		if err != nil {
			complete = false
			o.fail(path, "", OpWalk, err)
			return nil
		}
		if d.IsDir() {
			if path != root && containsPath(skip, path) {
				return filepath.SkipDir
			}
			return nil
		}
		if d.Name() == store.LibraryIDFile {
			// A library's identity stays in its root, and one found in a
			// source belongs to some other library.
			return nil
		}
		f := walkedFile{path: path}
		if info, err := d.Info(); err == nil {
			f.size, f.modTime, f.infoOK = info.Size(), info.ModTime(), true
		} else {
			// A file the index cannot check is kept as it is.
			complete = false
		}
		fn(f)
		return nil
	})
	if err != nil {
		complete = false
		o.fail(root, "", OpWalkRoot, err)
	}
	return complete
}

// runJobs runs fn over items on the given number of goroutines and waits for
// them to finish. Once ctx is cancelled no more items are handed out; the ones
// already taken run to the end.
func runJobs[T any](ctx context.Context, workers int, items []T, fn func(T)) {
	jobs := make(chan T)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for item := range jobs {
				fn(item)
			}
		}()
	}
feed:
	for _, item := range items {
		if ctx.Err() != nil {
			break
		}
		select {
		case jobs <- item:
		case <-ctx.Done():
			break feed
		}
	}
	close(jobs)
	wg.Wait()
}

func (o *organizer) countMoved(kind Outcome) {
	switch kind {
	case Organized:
		o.stats.Organized.Add(1)
	case Untagged:
		o.stats.Untagged.Add(1)
	default:
		o.stats.OtherMoved.Add(1)
	}
}

func (o *organizer) processFile(path string) {
	cfg, stats := o.cfg, o.stats

	file, err := os.Open(path)
	if err != nil {
		// Its neighbours cannot follow an album that was never worked out.
		o.recordAlbum(filepath.Dir(path), "")
		o.fail(path, "", OpOpen, err)
		return
	}

	metadata, tagErr := tag.ReadFrom(file)
	file.Close()

	ext := filepath.Ext(path)
	if tagErr == nil {
		if desiredDst, ok := rules.Qualifies(cfg.Destination, cfg.Compilations, metadata, ext); ok {
			o.recordAlbum(filepath.Dir(path), filepath.Dir(desiredDst))
			o.place(path, desiredDst, Organized)
			return
		}
	}

	o.recordAlbum(filepath.Dir(path), "")

	if cfg.Reorganizing() {
		// A reorganize never pushes files out of the library.
		o.placed.Store(rules.PathKey(path), path)
		o.emit(FilePlaced{Path: path, Destination: path, Outcome: Left})
		stats.LeftInPlace.Add(1)
		return
	}

	// Route to untagged directory, flattened into root
	baseName := rules.SanitizeName(strings.TrimSuffix(filepath.Base(path), ext))
	desiredDst := filepath.Join(cfg.Untagged, fmt.Sprintf("%s%s", baseName, ext))
	o.place(path, desiredDst, Untagged)
}

// processCompanion moves a non-audio file next to the album its folder's audio
// went to. Failing that, a normal run sends it into cfg.Other under its path
// relative to source, and a reorganize leaves it where it is.
func (o *organizer) processCompanion(c companionFile, tree map[string]map[string]struct{}) {
	path := c.path
	if dir, ok := companionDir(c.root, tree, filepath.Dir(path)); ok {
		o.place(path, filepath.Join(dir, filepath.Base(path)), Companion)
		return
	}

	if o.cfg.Reorganizing() {
		o.placed.Store(rules.PathKey(path), path)
		return
	}

	rel, err := filepath.Rel(o.cfg.Source, path)
	if err != nil {
		o.fail(path, "", OpRelPath, err)
		return
	}
	o.place(path, filepath.Join(o.cfg.Other, rel), Other)
}

// place resolves, reserves and moves path to desiredDst, recording the outcome.
func (o *organizer) place(path, desiredDst string, kind Outcome) {
	cfg, stats := o.cfg, o.stats

	finalDst, outcome, err := o.resolveDestination(path, desiredDst)
	if err != nil {
		o.fail(path, desiredDst, OpResolve, err)
		return
	}

	switch outcome {
	case outcomeDuplicate:
		o.placed.Store(rules.PathKey(path), finalDst)
		o.emit(FilePlaced{Path: path, Destination: finalDst, Outcome: Duplicate})
		stats.Skipped.Add(1)
		return
	case outcomeInPlace, outcomeCaseFix:
		if outcome == outcomeCaseFix && !cfg.DryRun {
			o.caseFixesMu.Lock()
			o.caseFixes = append(o.caseFixes, caseFix{src: path, desired: finalDst})
			o.caseFixesMu.Unlock()
		}
		o.placed.Store(rules.PathKey(path), finalDst)
		o.emit(FilePlaced{Path: path, Destination: finalDst, Outcome: InPlace})
		stats.InPlace.Add(1)
		return
	}

	if cfg.DryRun {
		o.placed.Store(rules.PathKey(path), finalDst)
		o.emit(FilePlaced{Path: path, Destination: finalDst, Outcome: kind})
		o.countMoved(kind)
		return
	}

	if err := o.moveFile(path, finalDst); err != nil {
		o.release(finalDst)
		o.fail(path, finalDst, OpMove, err)
		return
	}

	o.recordMove(path, finalDst, moveReasons[kind])
	o.placed.Store(rules.PathKey(path), finalDst)
	o.touched.Store(filepath.Dir(path), struct{}{})
	if rules.PathKey(finalDst) != rules.PathKey(desiredDst) {
		o.suffixedMu.Lock()
		o.suffixed = append(o.suffixed, suffixedMove{src: path, desired: desiredDst, final: finalDst})
		o.suffixedMu.Unlock()
	}

	o.emit(FilePlaced{Path: path, Destination: finalDst, Outcome: kind})
	o.countMoved(kind)
}

// retrySuffixed gives files that landed on a " (n)" name a second chance at
// their clean name. During a reorganize that name is often held by a file that
// is itself about to move; once it has, the clean name is free.
func (o *organizer) retrySuffixed(ctx context.Context) {
	for _, s := range o.suffixed {
		if ctx.Err() != nil {
			return
		}
		o.retryOne(s)
		o.phase.advance(0)
	}
}

func (o *organizer) retryOne(s suffixedMove) {
	if _, err := os.Lstat(s.desired); !errors.Is(err, fs.ErrNotExist) {
		return
	}
	claimed, err := o.claim(s.desired)
	if err != nil || !claimed {
		return
	}
	if err := o.moveFile(s.final, s.desired); err != nil {
		o.release(s.desired)
		o.fail(s.final, s.desired, OpRename, err)
		return
	}
	o.recordMove(s.final, s.desired, store.ReasonRenamed)
	o.placed.Store(rules.PathKey(s.src), s.desired)
	o.emit(FilePlaced{Path: s.final, Destination: s.desired, Outcome: Renamed})
	o.stats.Retried.Add(1)
}

// fixCase renames files that are in place apart from letter case, and the
// folders above them, onto the exact spelling their tags ask for. A folder is
// renamed only when every file placed under it asks for the same spelling;
// otherwise two albums whose tags disagree would flip it on every run.
func (o *organizer) fixCase(ctx context.Context) {
	if len(o.caseFixes) == 0 {
		return
	}
	spellings := make(map[string]map[string]struct{}) // rules.PathKey(dir) -> spellings asked for
	o.placed.Range(func(_, v any) bool {
		for _, dir := range o.libraryDirs(v.(string)) {
			key := rules.PathKey(dir)
			if spellings[key] == nil {
				spellings[key] = make(map[string]struct{})
			}
			spellings[key][dir] = struct{}{}
		}
		return true
	})

	fixes := slices.Clone(o.caseFixes)
	sort.Slice(fixes, func(i, j int) bool { return fixes[i].desired < fixes[j].desired })
	renamed := make(map[string]bool) // rules.PathKey(dir) -> whether this run renamed it
	for _, f := range fixes {
		if ctx.Err() != nil {
			return
		}
		changed := false
		for _, dir := range o.libraryDirs(f.desired) {
			key := rules.PathKey(dir)
			if len(spellings[key]) != 1 {
				continue
			}
			did, seen := renamed[key]
			if !seen {
				var err error
				if did, err = renameCase(dir); err != nil {
					o.fail(dir, dir, OpRename, err)
				}
				renamed[key] = did
			}
			changed = changed || did
		}
		did, err := renameCase(f.desired)
		if err != nil {
			o.fail(f.src, f.desired, OpRename, err)
		}
		if changed || did {
			o.recordMove(f.src, f.desired, store.ReasonRenamed)
			o.emit(FilePlaced{Path: f.src, Destination: f.desired, Outcome: Renamed})
			o.stats.Retried.Add(1)
		}
		o.phase.advance(0)
	}
}

// libraryDirs lists the folders above path inside the destination, top down.
func (o *organizer) libraryDirs(path string) []string {
	if !isSubdirectory(o.cfg.Destination, filepath.Dir(path)) {
		return nil
	}
	rel, err := filepath.Rel(o.cfg.Destination, filepath.Dir(path))
	if err != nil {
		return nil
	}
	var dirs []string
	dir := o.cfg.Destination
	for _, part := range strings.Split(rel, string(filepath.Separator)) {
		dir = filepath.Join(dir, part)
		dirs = append(dirs, dir)
	}
	return dirs
}

// renameCase gives the file or folder at path the exact spelling of its last
// element when the file system holds it under another case, and reports
// whether it renamed anything.
func renameCase(path string) (bool, error) {
	parent, name := filepath.Dir(path), filepath.Base(path)
	entries, err := os.ReadDir(parent)
	if err != nil {
		return false, err
	}
	for _, e := range entries {
		if e.Name() == name {
			return false, nil
		}
	}
	for _, e := range entries {
		if strings.EqualFold(e.Name(), name) {
			if err := os.Rename(filepath.Join(parent, e.Name()), path); err != nil {
				return false, err
			}
			return true, nil
		}
	}
	return false, nil
}

// cueFileLine matches a cue sheet FILE command: FILE "name" TYPE, or with an
// unquoted name. Group 1 is everything up to the name, group 2 the quoted
// name, group 3 the unquoted one.
var cueFileLine = regexp.MustCompile(`^(\s*(?i:FILE)\s+)(?:"([^"]*)"|(\S+))`)

// rewriteCue points the FILE lines of a moved cue sheet at the new names of
// its audio. It only rewrites when every referenced file now sits in the cue's
// own folder, changes nothing but the names, and keeps the first original as
// <name>.cue.orig.
func (o *organizer) rewriteCue(src string) {
	final, ok := o.placedAt(src)
	if !ok {
		return // never placed, e.g. an error was already reported
	}
	cueDir := filepath.Dir(final)

	// The source still holds the original bytes in a dry run, and an identical
	// copy of them for a duplicate; otherwise the cue is at its final path.
	readFrom := final
	if o.cfg.DryRun {
		readFrom = src
	}
	data, err := os.ReadFile(readFrom)
	if err != nil {
		o.fail(readFrom, "", OpReadCue, err)
		return
	}

	isUTF8 := utf8.Valid(data)
	lines := strings.SplitAfter(string(data), "\n")
	var renames []Rename
	for i, line := range lines {
		// A UTF-8 byte order mark would hide a FILE command on the first line.
		body := strings.TrimPrefix(line, "\xef\xbb\xbf")
		bom := line[:len(line)-len(body)]
		m := cueFileLine.FindStringSubmatchIndex(body)
		if m == nil {
			continue
		}
		nameStart, nameEnd := m[4], m[5]
		if nameStart < 0 {
			nameStart, nameEnd = m[6], m[7]
		}
		name := body[nameStart:nameEnd]

		newName, ok := o.cueTarget(filepath.Dir(src), cueDir, name)
		if !ok {
			o.emit(Warning{Kind: WarnCueNotBeside, Path: final, Name: name})
			return
		}
		if newName == name {
			continue
		}
		if !isUTF8 && !isASCII(newName) {
			// Writing a UTF-8 name into a cue in another encoding would
			// garble it.
			o.emit(Warning{Kind: WarnCueEncoding, Path: final, Name: newName})
			return
		}
		// Always quoted: the new name may contain spaces.
		lines[i] = bom + body[:quotedStart(m)] + `"` + newName + `"` + body[quotedEnd(m):]
		renames = append(renames, Rename{From: name, To: newName})
	}
	if len(renames) == 0 {
		return // already correct
	}
	rewritten := strings.Join(lines, "")

	if o.cfg.DryRun {
		o.emit(CueRewritten{Path: final, Renames: renames})
		o.stats.CueRewritten.Add(1)
		return
	}

	info, err := os.Stat(final)
	if err != nil {
		o.fail(final, "", OpReadCue, err)
		return
	}
	if err := writeFileExclusive(final+".orig", data, info.Mode().Perm()); err != nil && !errors.Is(err, fs.ErrExist) {
		o.fail(final, final+".orig", OpSaveCueOriginal, err)
		return
	}
	if err := os.WriteFile(final, []byte(rewritten), info.Mode().Perm()); err != nil {
		o.fail(final, "", OpRewriteCue, err)
		return
	}
	// Undo restores the cue sheet from its .orig.
	o.recordMove(final, final+".orig", store.ReasonCueRewrite)
	o.emit(CueRewritten{Path: final, Renames: renames})
	o.stats.CueRewritten.Add(1)
}

// cueTarget resolves a FILE name from a cue that was in srcDir and is now in
// cueDir, reporting the name that file has next to the cue now.
func (o *organizer) cueTarget(srcDir, cueDir, name string) (string, bool) {
	if filepath.Base(name) != name {
		return "", false // relative paths into other folders are left alone
	}
	if placed, ok := o.placedAt(filepath.Join(srcDir, name)); ok {
		if rules.PathKey(filepath.Dir(placed)) != rules.PathKey(cueDir) {
			return "", false
		}
		return filepath.Base(placed), true
	}
	// Not something this run handled: fine if it is already next to the cue.
	if _, err := os.Stat(filepath.Join(cueDir, name)); err == nil {
		return name, true
	}
	return "", false
}

// quotedStart and quotedEnd bound the name in a cueFileLine match, including
// its quotes when it had them.
func quotedStart(m []int) int {
	if m[4] >= 0 {
		return m[4] - 1
	}
	return m[6]
}

func quotedEnd(m []int) int {
	if m[4] >= 0 {
		return m[5] + 1
	}
	return m[7]
}

func isASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] >= utf8.RuneSelf {
			return false
		}
	}
	return true
}

// writeFileExclusive writes data to a new file, failing with fs.ErrExist if
// the file is already there.
func writeFileExclusive(path string, data []byte, perm os.FileMode) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, perm)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		os.Remove(path)
		return err
	}
	return f.Close()
}

// cleanupEmptyDirectories removes empty directories under roots, never
// removing a root itself or anything inside an excluded directory. With
// touchedDirs it only considers those directories and their ancestors;
// without, everything under roots. removed, if not nil, is told about each
// directory removed.
func cleanupEmptyDirectories(roots []string, excluded []string, removed func(dir string), touchedDirs ...*sync.Map) {
	isExcluded := func(path string) bool {
		if containsPath(roots, path) {
			return true
		}
		for _, out := range excluded {
			if rules.PathKey(path) == rules.PathKey(out) || isSubdirectory(out, path) {
				return true
			}
		}
		return false
	}
	insideRoot := func(path string) bool {
		for _, root := range roots {
			if isSubdirectory(root, path) {
				return true
			}
		}
		return false
	}

	var dirs []string
	if len(touchedDirs) > 0 && touchedDirs[0] != nil {
		dirSet := make(map[string]struct{})
		touchedDirs[0].Range(func(key, value any) bool {
			dir, ok := key.(string)
			if !ok {
				return true
			}
			for curr := dir; insideRoot(curr); {
				if !isExcluded(curr) {
					dirSet[curr] = struct{}{}
				}
				parent := filepath.Dir(curr)
				if parent == curr {
					break
				}
				curr = parent
			}
			return true
		})
		for d := range dirSet {
			dirs = append(dirs, d)
		}
	} else {
		for _, root := range roots {
			_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
				if err != nil || !d.IsDir() {
					return nil
				}
				// Do not delete roots or excluded directories inside them
				if isExcluded(path) {
					return nil
				}
				dirs = append(dirs, path)
				return nil
			})
		}
	}

	// Sort deepest directories first so subdirectories are removed before parents
	sort.Slice(dirs, func(i, j int) bool {
		return len(dirs[i]) > len(dirs[j])
	})

	for _, dir := range dirs {
		entries, err := os.ReadDir(dir)
		if err == nil && len(entries) == 0 {
			if err := os.Remove(dir); err == nil && removed != nil {
				removed(dir)
			}
		}
	}
}
