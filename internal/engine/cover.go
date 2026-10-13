package engine

import (
	"bytes"
	"cmp"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/zachjamesgreen/tagshelf/internal/coverart"
	"github.com/zachjamesgreen/tagshelf/internal/rules"
	"github.com/zachjamesgreen/tagshelf/internal/store"
	"github.com/zachjamesgreen/tagshelf/internal/tag"
	"github.com/zachjamesgreen/tagshelf/internal/tag/mbz"
)

// When a cover an album already has is replaced by the Cover Art Archive's.
const (
	CoverNever   = "never"   // it is kept, and the archive is not asked
	CoverSmaller = "smaller" // when the archive's is larger, in pixels
	CoverAlways  = "always"  // whenever the archive has one
)

// Where a cover that was written came from.
const (
	CoverFromFile    = "a picture in its files"
	CoverFromRelease = "the Cover Art Archive"
)

// CoverFetch fetches the front cover of a release, or failing that of its
// release group, from the Cover Art Archive, no larger than size pixels.
// *coverart.Client's Front is one.
type CoverFetch func(ctx context.Context, release, group string, size int) (coverart.Picture, error)

// CoverOptions say what a cover art run does.
type CoverOptions struct {
	// File has a cover file written into each album folder that has none.
	File bool
	// Embed has the album's cover put into each of its audio files that
	// holds no picture.
	Embed bool
	// Replace is when a cover a folder already has is replaced: CoverNever,
	// CoverSmaller or CoverAlways. Empty is CoverNever.
	Replace string
	// Fetch asks the Cover Art Archive. With nil, covers come from the
	// pictures the files hold alone.
	Fetch CoverFetch
	// Folders are the album folders to see to; nil is every folder of the
	// library that holds audio.
	Folders []string
}

// CoverArt gives album folders of the library the cover they lack, as one
// run recorded like any other and undone like any other.
//
// An album is one folder. A folder that has a cover (an image file named
// cover, folder or front) keeps it, unless c.Replace says the archive's
// takes its place. A folder without one gets a file named cover, made from
// the first picture its audio files hold or, when they hold none, from the
// Cover Art Archive: the front cover of the release its files name, or else
// of the release group. The archive is asked only when every file of the
// folder that names a release names the same one.
//
// A dry run asks the archive nothing and writes nothing: it says which
// folders it would see to, and from where.
func CoverArt(ctx context.Context, opts Options, c CoverOptions) RunFinished {
	opts.Source = opts.Destination
	o := newOrganizer(opts)
	cfg := o.cfg
	o.emit(RunStarted{Kind: KindTUIAction, DryRun: cfg.DryRun, Destination: cfg.Destination})
	notStarted := func(err error) RunFinished {
		finish := RunFinished{State: StateNotStarted, Err: err}
		o.emit(finish)
		return finish
	}
	st := cfg.Store
	if st == nil {
		return notStarted(errors.New("cover art needs the library's database"))
	}
	if c.Replace == "" {
		c.Replace = CoverNever
	}
	if c.Embed && cfg.SavePicture == nil && !cfg.DryRun {
		return notStarted(errors.New("putting pictures into files needs a way to write them"))
	}
	albums, err := coverFolders(st, c.Folders)
	if err != nil {
		return notStarted(err)
	}
	id, err := st.StartRun(storeKinds[KindTUIAction], cfg.DryRun, "", cfg.Destination, time.Now())
	if err != nil {
		return notStarted(err)
	}
	o.runID = id

	o.startPhase(PhaseCovers, int64(len(albums)), 0)
	for _, album := range albums {
		if ctx.Err() != nil {
			break
		}
		// The file first: the cover it writes is what goes into the files.
		if c.File {
			o.coverFile(ctx, album, c)
		}
		if c.Embed && ctx.Err() == nil {
			o.coverEmbed(ctx, album, c)
		}
		o.phase.advance(0)
	}
	return o.endAction(ctx)
}

// coverAlbum is an album folder and its audio files as the index has them,
// in disc and track order.
type coverAlbum struct {
	dir   string
	files []store.File
}

// coverFolders lists the album folders a cover art run sees to: those named,
// or every folder of the library that holds audio, each in path order.
func coverFolders(st *store.Store, only []string) ([]coverAlbum, error) {
	files, err := st.LibraryFiles()
	if err != nil {
		return nil, fmt.Errorf("reading the index: %w", err)
	}
	want := map[string]bool{}
	for _, dir := range only {
		want[rules.PathKey(dir)] = true
	}
	by := map[string]*coverAlbum{}
	var order []string
	for _, f := range files {
		dir := filepath.Dir(f.Path)
		key := rules.PathKey(dir)
		if only != nil && !want[key] {
			continue
		}
		if by[key] == nil {
			by[key] = &coverAlbum{dir: dir}
			order = append(order, key)
		}
		by[key].files = append(by[key].files, f)
	}
	slices.Sort(order)
	albums := make([]coverAlbum, 0, len(order))
	for _, key := range order {
		a := by[key]
		slices.SortStableFunc(a.files, func(x, y store.File) int {
			return cmp.Or(cmp.Compare(x.Disc, y.Disc), cmp.Compare(x.Track, y.Track), cmp.Compare(rules.PathKey(x.Path), rules.PathKey(y.Path)))
		})
		albums = append(albums, *a)
	}
	return albums, nil
}

// coverFile gives one album folder its cover file, or leaves it be.
func (o *organizer) coverFile(ctx context.Context, a coverAlbum, c CoverOptions) {
	skip := func(why string) { o.emit(CoverSkipped{Dir: a.dir, Why: why}) }
	existing, has, err := coverart.InFolder(a.dir)
	if err != nil {
		o.fail(a.dir, "", OpCover, err)
		return
	}
	if has && c.Replace == CoverNever {
		return // the usual case, and nothing to say
	}

	var pic coverart.Picture
	from := ""
	if !has {
		// What the album already has comes first: the picture of the first of
		// its files that holds one.
		for _, f := range a.files {
			if !f.HasArt {
				continue
			}
			if p, err := readEmbedded(f.Path); err == nil {
				pic, from = p, CoverFromFile
				break
			}
		}
	}
	if from == "" {
		release, group, ok := o.releaseOf(a)
		switch {
		case !ok:
			if !has {
				skip("its files name more than one release, so the Cover Art Archive is not asked")
			}
			return
		case release == "":
			if !has {
				skip("its files name no MusicBrainz release, and hold no picture")
			}
			return
		case c.Fetch == nil:
			if !has {
				skip("its files hold no picture")
			}
			return
		}
		var old coverart.Picture
		if has {
			if old, err = coverart.ReadFile(existing); err != nil {
				skip("the cover it has cannot be read, and is left as it is: " + err.Error())
				return
			}
			if c.Replace == CoverSmaller && max(old.Width, old.Height) >= coverart.Large {
				return // as large as what the archive would send
			}
		}
		if o.cfg.DryRun {
			o.emit(CoverWritten{Dir: a.dir, Path: coverPath(a.dir, ".jpg"), From: CoverFromRelease, Asked: true})
			o.stats.Covers.Add(1)
			return
		}
		pic, err = c.Fetch(ctx, release, group, coverart.Large)
		switch {
		case errors.Is(err, coverart.ErrNone):
			if !has {
				skip("the Cover Art Archive has no front cover for its release")
			}
			return
		case err != nil:
			o.fail(a.dir, "", OpCover, err)
			return
		}
		if has && c.Replace == CoverSmaller && max(pic.Width, pic.Height) <= max(old.Width, old.Height) {
			return // the archive's is no larger
		}
		from = CoverFromRelease
	}

	path := coverPath(a.dir, pic.Ext)
	replaced := ""
	if has {
		replaced = existing
	}
	if o.cfg.DryRun {
		o.emit(CoverWritten{Dir: a.dir, Path: path, From: from, Replaced: replaced})
		o.stats.Covers.Add(1)
		return
	}
	if err := o.writeCover(path, replaced, pic.Data); err != nil {
		o.fail(a.dir, path, OpCover, err)
		return
	}
	o.stats.Covers.Add(1)
	o.emit(CoverWritten{Dir: a.dir, Path: path, From: from, Replaced: replaced})
}

// readEmbedded reads the picture an audio file holds. Tests put one in its
// place.
var readEmbedded = coverart.Embedded

// coverPath is the cover file of the folder dir, for a picture of the kind
// ext is the extension of.
func coverPath(dir, ext string) string {
	return filepath.Join(dir, coverart.FileName+ext)
}

// releaseOf finds the MusicBrainz release an album's files name, with its
// release group. ok is false when they name more than one release; release
// is empty when they name none.
func (o *organizer) releaseOf(a coverAlbum) (release, group string, ok bool) {
	var of store.File
	for _, f := range a.files {
		switch {
		case f.ReleaseID == "":
		case release == "":
			release, of = f.ReleaseID, f
		case !sameID(release, f.ReleaseID):
			return "", "", false
		}
	}
	if release == "" {
		return "", "", true
	}
	// The index does not keep the release group: it is read from the file.
	if file, err := os.Open(of.Path); err == nil {
		if md, err := tag.ReadFrom(file); err == nil {
			group = mbz.Extract(md).ReleaseGroupID
		}
		file.Close()
	}
	return release, group, true
}

func sameID(a, b string) bool {
	return len(a) == len(b) && bytes.EqualFold([]byte(a), []byte(b))
}

// writeCover writes data as the cover file at path, in place of the image
// file at replaced when that is not empty, and records it: the record comes
// first, so that no cover is written that undo does not know of.
func (o *organizer) writeCover(path, replaced string, data []byte) error {
	var before []byte
	from := path
	if replaced != "" {
		var err error
		if before, err = os.ReadFile(replaced); err != nil {
			return err
		}
		from = replaced
	}
	sum := sha256.Sum256(data)
	if err := o.cfg.Store.RecordCover(o.runID, from, path, before, sum[:]); err != nil {
		return err
	}
	if replaced != "" && rules.PathKey(replaced) == rules.PathKey(path) {
		info, err := os.Stat(path)
		if err != nil {
			return err
		}
		return replaceFile(path, data, info.Mode().Perm())
	}
	if err := writeFileExclusive(path, data, 0o644); err != nil {
		return err
	}
	if replaced != "" {
		// Its picture is on record, whole: the cover the folder shows is the
		// new one alone.
		return os.Remove(replaced)
	}
	return nil
}

// coverStep works out how to undo a cover a run wrote: the file goes, if it
// is still the picture the run wrote, and the one it replaced comes back.
func (u *undoer) coverStep(op undoOp) (do func(o *organizer) error, plan func(), skip *UndoSkip) {
	m := op.move
	real, ok := u.where(m.To)
	if !ok {
		return nil, nil, &UndoSkip{Path: m.To, Reason: "it is no longer where the run left it"}
	}
	now, err := os.ReadFile(real)
	if err != nil {
		return nil, nil, &UndoSkip{Path: m.To, Reason: "it cannot be read: " + err.Error()}
	}
	if sum := sha256.Sum256(now); !bytes.Equal(sum[:], m.CueAfter) {
		return nil, nil, &UndoSkip{Path: m.To, Reason: "it is not the picture the run wrote, so it is left as it is"}
	}
	// The replaced file goes back beside the cover, wherever that is now.
	old := filepath.Join(filepath.Dir(real), filepath.Base(m.From))
	return func(o *organizer) error {
		switch {
		case len(m.CueBefore) == 0:
			// The run made the file, and it is as the run made it.
			if err := os.Remove(real); err != nil {
				return err
			}
		case rules.PathKey(old) == rules.PathKey(real):
			info, err := os.Stat(real)
			if err != nil {
				return err
			}
			if err := replaceFile(real, m.CueBefore, info.Mode().Perm()); err != nil {
				return err
			}
		default:
			if err := writeFileExclusive(old, m.CueBefore, 0o644); err != nil {
				return err
			}
			if err := os.Remove(real); err != nil {
				return err
			}
		}
		o.stats.Covers.Add(1)
		return nil
	}, func() {}, nil
}
