package engine

import (
	"context"
	"fmt"
	"os"
	"slices"

	"github.com/zachjamesgreen/tagshelf/internal/store"
)

// TagSave saves the tags a person chose to the indexed file f, recording the
// save in st under the run runID. The engine does not know what is written:
// it only reads the file again afterwards.
type TagSave func(st *store.Store, runID int64, f store.File) error

// SaveTags saves tags to the indexed files with the given IDs as one run of
// kind KindTUIAction, recorded like any other. Each file is saved to, read
// into the index again, and moved to its place in the library if it now
// qualifies; one that does not stays where it is. Non-audio files follow out
// of a folder all of whose audio left for one album.
//
// A file is left alone, with an error, when it is missing from the index or
// no longer on disk as it was indexed: the person chose about another file.
// Cancelling ctx stops before the next file.
func SaveTags(ctx context.Context, opts Options, ids []int64, save TagSave) RunFinished {
	o, refused := startAction(opts, "saving tags", KindTUIAction)
	if refused != nil {
		return *refused
	}
	st := o.cfg.Store
	ids = slices.Clone(ids)
	slices.Sort(ids)
	ids = slices.Compact(ids)
	files, err := st.FilesByID(ids)
	if err != nil {
		o.fail(st.Path(), "", OpRecord, err)
	}
	if err == nil {
		for _, id := range ids {
			if !slices.ContainsFunc(files, func(f store.File) bool { return f.ID == id }) {
				o.fail(st.Path(), "", OpSaveTags, fmt.Errorf("file %d is no longer in the index", id))
			}
		}
	}
	var bytes int64
	for _, f := range files {
		bytes += f.Size
	}
	o.startPhase(PhaseAudio, int64(len(files)), bytes)
	for _, f := range files {
		if ctx.Err() != nil {
			break
		}
		o.saveTags(f, save)
		o.phase.advance(f.Size)
	}
	return o.endAction(ctx)
}

// saveTags saves to one file and puts it where its new tags say.
func (o *organizer) saveTags(f store.File, save TagSave) {
	st := o.cfg.Store
	if err := asIndexed(f); err != nil {
		o.fail(f.Path, "", OpSaveTags, err)
		return
	}
	if err := save(st, o.runID, f); err != nil {
		o.fail(f.Path, "", OpSaveTags, err)
		return
	}

	// The index follows the save before the move, which takes the row along.
	// A row that could not follow is an error, but the file still goes where
	// its tags say: the next run reads it again, its size or time being off.
	if err := o.reindex(f); err != nil {
		o.fail(f.Path, "", OpIndex, err)
	}
	// As in a reorganize: into the library if it qualifies, else left alone.
	o.processFile(f.Path)
}

// reindex reads the file of the row f into the index again.
func (o *organizer) reindex(f store.File) error {
	info, err := os.Stat(f.Path)
	if err != nil {
		return err
	}
	row, err := readIndexFile(indexJob{
		walkedFile: walkedFile{path: f.Path, size: info.Size(), modTime: info.ModTime(), infoOK: true},
		rootID:     f.RootID, rel: f.RelPath,
	})
	if err != nil {
		return err
	}
	return o.cfg.Store.PutFiles([]store.File{row})
}
