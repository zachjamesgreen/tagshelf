package engine

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"

	"github.com/zachjamesgreen/tagshelf/internal/coverart"
	"github.com/zachjamesgreen/tagshelf/internal/store"
)

// CoverFromFolder is where a picture put into a file came from when it was
// the cover file of the album's folder.
const CoverFromFolder = "the cover in its folder"

// PictureSave makes picture the front cover the audio file at path holds, in
// the place of the picture it has, or with nil takes that one out, and puts
// the save on record under the run runID. The engine does not know how a
// picture is written into a file of each kind.
type PictureSave func(st *store.Store, runID int64, path string, picture []byte) error

// coverEmbed puts the album's cover into each of its audio files that holds
// no picture, and, when the setting has the archive's cover replace one, in
// the place of the picture a file has.
//
// What the album already has comes first: the cover file of its folder, or
// else the first picture one of its files holds, goes into the files that
// hold none, made smaller when it is large. Failing that, and to replace a
// picture, the Cover Art Archive is asked, in its middle size.
func (o *organizer) coverEmbed(ctx context.Context, a coverAlbum, c CoverOptions) {
	var bare, pictured []store.File
	for _, f := range a.files {
		if f.HasArt {
			pictured = append(pictured, f)
		} else {
			bare = append(bare, f)
		}
	}
	if c.Replace == CoverNever {
		pictured = nil
	}
	if len(bare) == 0 && len(pictured) == 0 {
		return
	}
	skip := func(why string) { o.emit(CoverSkipped{Dir: a.dir, Why: why, Embedding: true}) }

	// The folder's own cover, for the files that hold nothing.
	var local *coverart.Picture
	if len(bare) > 0 {
		if path, has, err := coverart.InFolder(a.dir); err != nil {
			o.fail(a.dir, "", OpCover, err)
			return
		} else if has {
			pic, err := coverart.ReadFile(path)
			if err == nil {
				pic, err = coverart.ForEmbedding(pic)
			}
			if err != nil {
				skip("the cover in its folder cannot be put into its files: " + err.Error())
			} else {
				local = &pic
			}
		}
	}

	// Or else the picture one of the album's other files holds.
	localFrom := CoverFromFolder
	if local == nil && len(bare) > 0 {
		for _, f := range a.files {
			if !f.HasArt {
				continue
			}
			pic, err := readEmbedded(f.Path)
			if err == nil {
				pic, err = coverart.ForEmbedding(pic)
			}
			if err == nil {
				local, localFrom = &pic, CoverFromFile
				break
			}
		}
	}

	// The archive's, for the files the album has nothing for, and for those
	// whose picture it may replace.
	var remote *coverart.Picture
	asked := false
	if (local == nil && len(bare) > 0) || len(pictured) > 0 {
		release, group, ok := o.releaseOf(a)
		switch {
		case !ok:
			if local == nil && len(bare) > 0 {
				skip("its files name more than one release, so the Cover Art Archive is not asked")
			}
		case release == "" || c.Fetch == nil:
			if local == nil && len(bare) > 0 {
				skip("its folder has no cover, and its files name no MusicBrainz release to ask the Cover Art Archive for")
			}
		case o.cfg.DryRun:
			asked = true
		default:
			pic, err := c.Fetch(ctx, release, group, coverart.EmbedSize)
			switch {
			case errors.Is(err, coverart.ErrNone):
				if local == nil && len(bare) > 0 {
					skip("the Cover Art Archive has no front cover for its release")
				}
			case err != nil:
				o.fail(a.dir, "", OpCover, err)
			default:
				remote = &pic
			}
		}
	}

	put := func(f store.File, pic *coverart.Picture, from string, replaced bool) {
		if o.cfg.DryRun {
			o.emit(CoverEmbedded{Path: f.Path, From: from, Replaced: replaced, Asked: pic == nil})
			o.stats.Pictures.Add(1)
			return
		}
		if err := o.cfg.SavePicture(o.cfg.Store, o.runID, f.Path, pic.Data); err != nil {
			o.fail(f.Path, "", OpCover, err)
			return
		}
		// The file is another size now, and holds a picture: its row follows.
		if err := o.reindex(f); err != nil {
			o.fail(f.Path, "", OpIndex, err)
		}
		o.stats.Pictures.Add(1)
		o.emit(CoverEmbedded{Path: f.Path, From: from, Replaced: replaced})
	}
	for _, f := range bare {
		if ctx.Err() != nil {
			return
		}
		switch {
		case local != nil:
			put(f, local, localFrom, false)
		case remote != nil || asked:
			put(f, remote, CoverFromRelease, false)
		}
	}
	for _, f := range pictured {
		if ctx.Err() != nil {
			return
		}
		switch {
		case asked:
			put(f, nil, CoverFromRelease, true)
		case remote == nil:
		case c.Replace == CoverSmaller:
			old, err := readEmbedded(f.Path)
			if err != nil || max(old.Width, old.Height) >= max(remote.Width, remote.Height) {
				continue // it cannot be measured, or is no smaller
			}
			put(f, remote, CoverFromRelease, true)
		default:
			put(f, remote, CoverFromRelease, true)
		}
	}
}

// pictureStep works out how to undo a picture a run put into a file: the
// one it replaced goes back, or the picture comes out, if the file still
// holds the picture the run put in.
func (u *undoer) pictureStep(op undoOp) (do func(o *organizer) error, plan func(), skip *UndoSkip) {
	m := op.move
	real, ok := u.where(m.To)
	if !ok {
		return nil, nil, &UndoSkip{Path: m.To, Reason: "it is no longer where the run left it"}
	}
	now, err := readPicture(real)
	if err != nil {
		return nil, nil, &UndoSkip{Path: m.To, Reason: "its picture cannot be read: " + err.Error()}
	}
	if sum := sha256.Sum256(now); !bytes.Equal(sum[:], m.CueAfter) {
		return nil, nil, &UndoSkip{Path: m.To, Reason: "it no longer holds the picture the run put in, so it is left as it is"}
	}
	return func(o *organizer) error {
		if o.cfg.SavePicture == nil {
			return errors.New("this undo cannot write pictures")
		}
		if err := o.cfg.SavePicture(o.cfg.Store, o.runID, real, m.CueBefore); err != nil {
			return err
		}
		o.stats.Pictures.Add(1)
		if row, indexed, err := o.cfg.Store.FileAt(real); err != nil {
			return err
		} else if indexed {
			return o.reindex(row)
		}
		return nil
	}, func() {}, nil
}

// readPicture reads the first picture an audio file holds, as it is, and
// nothing for a file that holds none. Tests put one in its place.
var readPicture = func(path string) ([]byte, error) {
	pic, err := coverart.Embedded(path)
	switch {
	case errors.Is(err, coverart.ErrNoPicture):
		return nil, nil
	case err != nil:
		return nil, err
	}
	return pic.Data, nil
}
