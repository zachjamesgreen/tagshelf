package engine

import (
	"context"
	"errors"
	"io/fs"
	"os"

	"github.com/zachjamesgreen/tagshelf/internal/store"
	"github.com/zachjamesgreen/tagshelf/internal/tag"
)

// ReadListed reads the audio file at path as a list of files shows it: its
// tags as an index row would hold them, its size and its time, without the
// audio hash and the audio details, which take reading the whole file. It is
// for files that are not in the index. link is set for a symbolic link,
// which tagshelf does not write through.
//
// A file whose tags cannot be read is still listed, with why in ReadError:
// only one that cannot be opened at all is an error.
func ReadListed(path string) (f store.File, link bool, err error) {
	info, err := os.Lstat(path)
	if err != nil {
		return f, false, err
	}
	link = info.Mode()&fs.ModeSymlink != 0
	if link {
		if info, err = os.Stat(path); err != nil {
			return f, true, err
		}
	}
	f = store.File{Path: path, RelPath: info.Name(), Size: info.Size(), ModTime: info.ModTime()}
	file, err := os.Open(path)
	if err != nil {
		return f, link, err
	}
	defer file.Close()
	md, err := tag.ReadFrom(file)
	switch {
	case err == nil:
		fillTags(&f, md)
		if skipped := tag.Skipped(md); skipped != nil {
			f.ReadError = "tags: " + skipped.Error()
		}
	case errors.Is(err, tag.ErrNoTagsFound):
	default:
		f.ReadError = "tags: " + err.Error()
	}
	return f, link, nil
}

// SaveFiles saves the tags a person chose to files that are not in the
// index, such as those of a folder outside the library, as one run of kind
// KindTUIAction recorded like any other, so that it can be undone. The files
// stay where they are whatever their tags say, and nothing is indexed.
//
// A file is left alone, with an error, when it is no longer on disk as it
// was listed (its size and time are those of f), or when it is a link.
// Cancelling ctx stops before the next file.
func SaveFiles(ctx context.Context, opts Options, files []store.File, save TagSave) RunFinished {
	o, refused := startAction(opts, "saving tags", KindTUIAction)
	if refused != nil {
		return *refused
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
		o.saveListed(f, save)
		o.phase.advance(f.Size)
	}
	return o.endAction(ctx)
}

// saveListed saves to one file that is not in the index, where it is.
func (o *organizer) saveListed(f store.File, save TagSave) {
	info, err := os.Lstat(f.Path)
	switch {
	case err != nil:
		o.fail(f.Path, "", OpSaveTags, errors.New("it is gone since it was listed"))
		return
	case info.Mode()&fs.ModeSymlink != 0:
		o.fail(f.Path, "", OpSaveTags, errors.New("it is a link, and tagshelf does not change a file through one"))
		return
	case !asOnDisk(info, f.Size, f.ModTime):
		o.fail(f.Path, "", OpSaveTags, errors.New("it has changed since it was listed"))
		return
	}
	if err := save(o.cfg.Store, o.runID, f); err != nil {
		o.fail(f.Path, "", OpSaveTags, err)
		return
	}
	o.stats.InPlace.Add(1)
	o.emit(FilePlaced{Path: f.Path, Destination: f.Path, Outcome: InPlace})
}
