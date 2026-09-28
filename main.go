package main

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"tagshelf/internal/config"
	"tagshelf/internal/rules"
	"tagshelf/internal/tag"
)

// maxCollisionSuffix bounds the " (n)" search so that a permanently unusable
// destination name can never spin a worker forever.
const maxCollisionSuffix = 10000

type Stats struct {
	TotalFound atomic.Int64
	Organized  atomic.Int64
	Untagged   atomic.Int64
	Skipped    atomic.Int64
	InPlace    atomic.Int64
	Errors     atomic.Int64
	// Non-audio files (cue sheets, artwork, logs, ...). Their duplicates,
	// in-place hits and errors are counted in Skipped, InPlace and Errors.
	OtherFound atomic.Int64
	OtherMoved atomic.Int64
	// Reorganize only: audio that does not qualify for the library stays put.
	LeftInPlace atomic.Int64
	// Files moved onto their clean name after first landing on a " (n)" one.
	Retried      atomic.Int64
	CueRewritten atomic.Int64
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

func main() {
	log.SetFlags(log.LstdFlags | log.Lshortfile)

	configPath := flag.String("config", "", "Path to YAML configuration file")
	sourceFlag := flag.String("source", "", "Source directory containing audio files")
	destFlag := flag.String("dest", "", "Destination directory for organized music")
	untaggedFlag := flag.String("untagged", "", "Directory for untagged / incomplete files")
	otherFlag := flag.String("other", "", "Directory for non-audio files that do not belong to a tagged album (default: <dest>/_Other)")
	compilationsFlag := flag.String("compilations", "", "Artist folder name for Various Artists compilations (default: \""+rules.DefaultCompilations+"\")")
	workersFlag := flag.Int("workers", 0, "Number of concurrent worker goroutines (default: NumCPU)")
	dryRunFlag := flag.Bool("dry-run", false, "Preview actions without moving or deleting files")
	verboseFlag := flag.Bool("verbose", false, "Enable verbose per-file logging")
	reorganizeFlag := flag.String("reorganize", "", "Re-sort an already organized library in place (sets -source and -dest to this directory)")
	noCueRewriteFlag := flag.Bool("no-cue-rewrite", false, "Do not rewrite the FILE lines of moved cue sheets")

	flag.Parse()

	// Only flags the user actually typed may override the config file, so that
	// -dry-run=false and -verbose=false can turn a config setting back off.
	wasSet := make(map[string]bool)
	flag.Visit(func(f *flag.Flag) { wasSet[f.Name] = true })

	ov := config.Overrides{
		Source:       *sourceFlag,
		Destination:  *destFlag,
		Untagged:     *untaggedFlag,
		Other:        *otherFlag,
		Compilations: *compilationsFlag,
	}
	if *reorganizeFlag != "" {
		ov.Source = *reorganizeFlag
		ov.Destination = *reorganizeFlag
	}
	if wasSet["no-cue-rewrite"] {
		rewrite := !*noCueRewriteFlag
		ov.CueRewrite = &rewrite
	}
	if wasSet["workers"] {
		ov.Workers = *workersFlag
	}
	if wasSet["dry-run"] {
		ov.DryRun = dryRunFlag
	}
	if wasSet["verbose"] {
		ov.Verbose = verboseFlag
	}

	cfg, err := config.Load(*configPath, ov)
	if err != nil {
		log.Fatalf("Error: %v", err)
	}

	if cfg.Source == "" {
		log.Fatal("Error: source directory must be specified via -source or config file")
	}
	if cfg.Destination == "" {
		log.Fatal("Error: destination directory must be specified via -dest or config file")
	}

	cfg = cfg.WithDefaults()

	absSource, err := filepath.Abs(cfg.Source)
	if err != nil {
		log.Fatalf("Invalid source path: %v", err)
	}
	absDest, err := filepath.Abs(cfg.Destination)
	if err != nil {
		log.Fatalf("Invalid destination path: %v", err)
	}
	absUntagged, err := filepath.Abs(cfg.Untagged)
	if err != nil {
		log.Fatalf("Invalid untagged path: %v", err)
	}
	absOther, err := filepath.Abs(cfg.Other)
	if err != nil {
		log.Fatalf("Invalid other path: %v", err)
	}

	cfg.Source = absSource
	cfg.Destination = absDest
	cfg.Untagged = absUntagged
	cfg.Other = absOther

	log.Println("================ Music Organizer ================")
	if cfg.Reorganizing() {
		log.Println("Mode:         reorganize library in place")
	}
	log.Printf("Source:       %s\n", cfg.Source)
	log.Printf("Destination:  %s\n", cfg.Destination)
	log.Printf("Untagged:     %s\n", cfg.Untagged)
	log.Printf("Other:        %s\n", cfg.Other)
	log.Printf("Compilations: %s\n", cfg.Compilations)
	log.Printf("Workers:      %d\n", cfg.Workers)
	log.Printf("Dry-Run:      %t\n", cfg.DryRun)
	log.Printf("Verbose:      %t\n", cfg.Verbose)
	log.Printf("Cue rewrite:  %t\n", cfg.RewriteCues())
	log.Println("==================================================")

	stats := RunOrganizer(cfg)

	log.Println("================= Process Summary =================")
	log.Printf("Total audio files found:  %d\n", stats.TotalFound.Load())
	log.Printf("Organized files moved:    %d\n", stats.Organized.Load())
	log.Printf("Untagged files moved:     %d\n", stats.Untagged.Load())
	log.Printf("Non-audio files found:    %d\n", stats.OtherFound.Load())
	log.Printf("Non-audio files moved:    %d\n", stats.OtherMoved.Load())
	log.Printf("Duplicate files skipped:  %d\n", stats.Skipped.Load())
	log.Printf("Already in place:         %d\n", stats.InPlace.Load())
	if cfg.Reorganizing() {
		log.Printf("Not qualifying, left:     %d\n", stats.LeftInPlace.Load())
		log.Printf("Moved to clean name:      %d\n", stats.Retried.Load())
	}
	log.Printf("Cue sheets rewritten:     %d\n", stats.CueRewritten.Load())
	log.Printf("Errors encountered:       %d\n", stats.Errors.Load())
	log.Println("==================================================")
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
)

// organizer holds the per-run state shared by the worker goroutines. Keeping it
// per-run rather than in package globals means one run can never hand stale
// directory or destination state to the next.
type organizer struct {
	cfg      config.Config
	stats    *Stats
	dirCache DirectoryCache
	reserved sync.Map // rules.PathKey(path) -> struct{}
	touched  sync.Map // source directory -> struct{}

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
}

type suffixedMove struct {
	src, desired, final string
}

func newOrganizer(cfg config.Config) *organizer {
	return &organizer{
		cfg:    cfg.WithDefaults(),
		stats:  &Stats{},
		albums: make(map[string]map[string]struct{}),
	}
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
	if err := copyFile(src, dst); err != nil {
		return err
	}

	if err := os.Remove(src); err != nil {
		return fmt.Errorf("failed to remove source after copy: %w", err)
	}

	return nil
}

// copyFile copies src over dst, restoring the permissions and modification time
// that a rename would have carried across on its own. Without that, every
// cross-drive move silently resets both.
func copyFile(src, dst string) error {
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
		log.Printf("Warning: could not preserve permissions on %q: %v\n", dst, err)
	}
	if err := os.Chtimes(dst, time.Now(), srcInfo.ModTime()); err != nil {
		log.Printf("Warning: could not preserve modification time on %q: %v\n", dst, err)
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

func RunOrganizer(cfg config.Config) *Stats {
	// Normalized here as well as in main, because RunOrganizer is the exported
	// entry point: a Config with Workers <= 0 would otherwise start no workers
	// at all and silently discard every job.
	o := newOrganizer(cfg)
	cfg = o.cfg
	stats := o.stats

	stopTicker := make(chan struct{})
	tickerDone := make(chan struct{})
	ticker := time.NewTicker(500 * time.Millisecond)

	startTime := time.Now()
	var lastProcessed int64
	lastTime := startTime

	go func() {
		defer close(tickerDone)
		defer ticker.Stop()
		for {
			select {
			case <-stopTicker:
				return
			case now := <-ticker.C:
				org := stats.Organized.Load()
				unt := stats.Untagged.Load()
				other := stats.OtherMoved.Load()
				skip := stats.Skipped.Load()
				inPlace := stats.InPlace.Load() + stats.LeftInPlace.Load()
				errCount := stats.Errors.Load()
				totalProcessed := org + unt + other + skip + inPlace + errCount

				elapsed := now.Sub(lastTime).Seconds()
				rate := 0.0
				if elapsed > 0 {
					rate = float64(totalProcessed-lastProcessed) / elapsed
				}
				lastProcessed = totalProcessed
				lastTime = now

				log.Printf("[PROGRESS] Processed: %d (Organized: %d, Untagged: %d, Non-audio: %d, Skipped: %d, In-place: %d, Errors: %d) | %.1f files/sec\n",
					totalProcessed, org, unt, other, skip, inPlace, errCount, rate)
			}
		}
	}()

	// The whole tree is listed before anything moves. A reorganize walks the
	// very folders it moves files into, and a streaming walk would pick those
	// files, or the placeholders claim leaves, up a second time.
	roots := o.walkRoots()
	var audio []string
	var companions []companionFile
	for _, root := range roots {
		o.walk(root, roots, func(path string) {
			if isAudioFile(path) {
				stats.TotalFound.Add(1)
				audio = append(audio, path)
			} else {
				stats.OtherFound.Add(1)
				companions = append(companions, companionFile{path: path, root: root})
			}
		})
	}

	runJobs(cfg.Workers, audio, o.processFile)

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
	runJobs(cfg.Workers, companions, func(c companionFile) { o.processCompanion(c, tree) })

	if cfg.Reorganizing() && !cfg.DryRun {
		o.retrySuffixed()
	}

	if cfg.RewriteCues() {
		for _, c := range companions {
			if strings.EqualFold(filepath.Ext(c.path), ".cue") {
				o.rewriteCue(c.path)
			}
		}
	}

	close(stopTicker)
	<-tickerDone

	// Clean up emptied directories if not in dry-run
	if !cfg.DryRun {
		excluded := []string{cfg.Destination, cfg.Untagged, cfg.Other}
		if cfg.Reorganizing() {
			// The walked roots are the library itself; roots are never removed.
			excluded = nil
		}
		cleanupEmptyDirectories(roots, excluded, &o.touched)
	}

	return stats
}

// companionFile is a non-audio file and the walk root it was found under.
type companionFile struct {
	path, root string
}

// walkRoots lists the directories to scan. A normal run scans the source. A
// reorganize scans the library plus the untagged and other folders, wherever
// they are, so that files tagged since the last run make it into the library.
func (o *organizer) walkRoots() []string {
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

// walk lists the files under root, calling fn for each. A normal run skips the
// output folders; a reorganize skips the other roots, which it walks on their
// own.
func (o *organizer) walk(root string, roots []string, fn func(path string)) {
	skip := []string{o.cfg.Destination, o.cfg.Untagged, o.cfg.Other}
	if o.cfg.Reorganizing() {
		skip = roots
	}

	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			log.Printf("Walk error at %q: %v\n", path, err)
			o.stats.Errors.Add(1)
			return nil
		}
		if d.IsDir() {
			if path != root && containsPath(skip, path) {
				return filepath.SkipDir
			}
			return nil
		}
		fn(path)
		return nil
	})
	if err != nil {
		log.Printf("Walk directory failed: %v\n", err)
		o.stats.Errors.Add(1)
	}
}

// runJobs runs fn over items on the given number of goroutines and waits for
// them to finish.
func runJobs[T any](workers int, items []T, fn func(T)) {
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
	for _, item := range items {
		jobs <- item
	}
	close(jobs)
	wg.Wait()
}

// placeKind says which category a file is being placed as, for stats and logs.
type placeKind int

const (
	kindOrganized placeKind = iota
	kindUntagged
	kindCompanion // non-audio file following its album
	kindOther     // non-audio file sent to cfg.Other
)

func (k placeKind) label() string {
	switch k {
	case kindOrganized:
		return "ORGANIZE"
	case kindUntagged:
		return "UNTAGGED"
	case kindCompanion:
		return "COMPANION"
	default:
		return "OTHER"
	}
}

func (o *organizer) countMoved(kind placeKind) {
	switch kind {
	case kindOrganized:
		o.stats.Organized.Add(1)
	case kindUntagged:
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
		log.Printf("Error opening file %q: %v\n", path, err)
		stats.Errors.Add(1)
		return
	}

	metadata, tagErr := tag.ReadFrom(file)
	file.Close()

	ext := filepath.Ext(path)
	if tagErr == nil && metadata != nil && rules.HasMusicBrainzTags(metadata) {
		if desiredDst, ok := rules.LibraryPath(cfg.Destination, cfg.Compilations, metadata, ext); ok {
			o.recordAlbum(filepath.Dir(path), filepath.Dir(desiredDst))
			o.place(path, desiredDst, kindOrganized)
			return
		}
	}

	o.recordAlbum(filepath.Dir(path), "")

	if cfg.Reorganizing() {
		// A reorganize never pushes files out of the library.
		o.placed.Store(rules.PathKey(path), path)
		if cfg.Verbose {
			log.Printf("[LEFT] %q does not qualify for the library, leaving it in place\n", path)
		}
		stats.LeftInPlace.Add(1)
		return
	}

	// Route to untagged directory, flattened into root
	baseName := rules.SanitizeName(strings.TrimSuffix(filepath.Base(path), ext))
	desiredDst := filepath.Join(cfg.Untagged, fmt.Sprintf("%s%s", baseName, ext))
	o.place(path, desiredDst, kindUntagged)
}

// processCompanion moves a non-audio file next to the album its folder's audio
// went to. Failing that, a normal run sends it into cfg.Other under its path
// relative to source, and a reorganize leaves it where it is.
func (o *organizer) processCompanion(c companionFile, tree map[string]map[string]struct{}) {
	path := c.path
	if dir, ok := companionDir(c.root, tree, filepath.Dir(path)); ok {
		o.place(path, filepath.Join(dir, filepath.Base(path)), kindCompanion)
		return
	}

	if o.cfg.Reorganizing() {
		o.placed.Store(rules.PathKey(path), path)
		return
	}

	rel, err := filepath.Rel(o.cfg.Source, path)
	if err != nil {
		log.Printf("Error computing relative path for %q: %v\n", path, err)
		o.stats.Errors.Add(1)
		return
	}
	o.place(path, filepath.Join(o.cfg.Other, rel), kindOther)
}

// place resolves, reserves and moves path to desiredDst, recording the outcome.
func (o *organizer) place(path, desiredDst string, kind placeKind) {
	cfg, stats := o.cfg, o.stats

	finalDst, outcome, err := o.resolveDestination(path, desiredDst)
	if err != nil {
		log.Printf("Error resolving destination for %q -> %q: %v\n", path, desiredDst, err)
		stats.Errors.Add(1)
		return
	}

	switch outcome {
	case outcomeDuplicate:
		o.placed.Store(rules.PathKey(path), finalDst)
		log.Printf("[SKIPPED] %q -> identical copy already at %q\n", path, finalDst)
		stats.Skipped.Add(1)
		return
	case outcomeInPlace:
		o.placed.Store(rules.PathKey(path), finalDst)
		if cfg.Verbose {
			log.Printf("[IN-PLACE] %q is already at its destination\n", path)
		}
		stats.InPlace.Add(1)
		return
	}

	if cfg.DryRun {
		o.placed.Store(rules.PathKey(path), finalDst)
		if cfg.Verbose {
			log.Printf("[DRY-RUN] [%s] %q -> %q\n", kind.label(), path, finalDst)
		}
		o.countMoved(kind)
		return
	}

	if err := o.moveFile(path, finalDst); err != nil {
		o.release(finalDst)
		log.Printf("Error moving file %q -> %q: %v\n", path, finalDst, err)
		stats.Errors.Add(1)
		return
	}

	o.placed.Store(rules.PathKey(path), finalDst)
	o.touched.Store(filepath.Dir(path), struct{}{})
	if rules.PathKey(finalDst) != rules.PathKey(desiredDst) {
		o.suffixedMu.Lock()
		o.suffixed = append(o.suffixed, suffixedMove{src: path, desired: desiredDst, final: finalDst})
		o.suffixedMu.Unlock()
	}

	if cfg.Verbose {
		label := kind.label()
		if kind == kindOrganized {
			label = "MOVED"
		}
		log.Printf("[%s] %q -> %q\n", label, path, finalDst)
	}
	o.countMoved(kind)
}

// retrySuffixed gives files that landed on a " (n)" name a second chance at
// their clean name. During a reorganize that name is often held by a file that
// is itself about to move; once it has, the clean name is free.
func (o *organizer) retrySuffixed() {
	for _, s := range o.suffixed {
		if _, err := os.Lstat(s.desired); !errors.Is(err, fs.ErrNotExist) {
			continue
		}
		claimed, err := o.claim(s.desired)
		if err != nil || !claimed {
			continue
		}
		if err := o.moveFile(s.final, s.desired); err != nil {
			o.release(s.desired)
			log.Printf("Error moving %q to its clean name %q: %v\n", s.final, s.desired, err)
			o.stats.Errors.Add(1)
			continue
		}
		o.placed.Store(rules.PathKey(s.src), s.desired)
		if o.cfg.Verbose {
			log.Printf("[RENAMED] %q -> %q\n", s.final, s.desired)
		}
		o.stats.Retried.Add(1)
	}
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
		log.Printf("Error reading cue sheet %q: %v\n", readFrom, err)
		o.stats.Errors.Add(1)
		return
	}

	lines := strings.SplitAfter(string(data), "\n")
	var renames []string
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
			log.Printf("Warning: not rewriting cue sheet %q: %q is not next to it after the move\n", final, name)
			return
		}
		if newName == name {
			continue
		}
		if !utf8.ValidString(string(data)) && !isASCII(newName) {
			// Writing a UTF-8 name into a cue in another encoding would
			// garble it.
			log.Printf("Warning: not rewriting cue sheet %q: it is not UTF-8 and %q is not plain ASCII\n", final, newName)
			return
		}
		// Always quoted: the new name may contain spaces.
		lines[i] = bom + body[:quotedStart(m)] + `"` + newName + `"` + body[quotedEnd(m):]
		renames = append(renames, fmt.Sprintf("%q -> %q", name, newName))
	}
	if len(renames) == 0 {
		return // already correct
	}
	rewritten := strings.Join(lines, "")

	if o.cfg.DryRun {
		if o.cfg.Verbose {
			log.Printf("[DRY-RUN] [CUE] %q: %s\n", final, strings.Join(renames, ", "))
		}
		o.stats.CueRewritten.Add(1)
		return
	}

	info, err := os.Stat(final)
	if err != nil {
		log.Printf("Error reading cue sheet %q: %v\n", final, err)
		o.stats.Errors.Add(1)
		return
	}
	if err := writeFileExclusive(final+".orig", data, info.Mode().Perm()); err != nil && !errors.Is(err, fs.ErrExist) {
		log.Printf("Error saving original of cue sheet %q: %v\n", final, err)
		o.stats.Errors.Add(1)
		return
	}
	if err := os.WriteFile(final, []byte(rewritten), info.Mode().Perm()); err != nil {
		log.Printf("Error rewriting cue sheet %q: %v\n", final, err)
		o.stats.Errors.Add(1)
		return
	}
	if o.cfg.Verbose {
		log.Printf("[CUE] %q: %s\n", final, strings.Join(renames, ", "))
	}
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
// without, everything under roots.
func cleanupEmptyDirectories(roots []string, excluded []string, touchedDirs ...*sync.Map) {
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
			if err := os.Remove(dir); err == nil {
				log.Printf("[CLEANUP] Removed empty directory %q\n", dir)
			}
		}
	}
}
