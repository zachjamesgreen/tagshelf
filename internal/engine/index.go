package engine

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"go.senan.xyz/taglib"

	"github.com/zachjamesgreen/tagshelf/internal/marker"
	"github.com/zachjamesgreen/tagshelf/internal/rules"
	"github.com/zachjamesgreen/tagshelf/internal/store"
	"github.com/zachjamesgreen/tagshelf/internal/tag"
	"github.com/zachjamesgreen/tagshelf/internal/tag/mbz"
)

// Index rows are committed in batches of this many files, or after this long,
// whichever comes first, so that an interrupted index keeps what it read.
// Tests shorten them.
var (
	indexBatchFiles    = 500
	indexBatchInterval = 5 * time.Second
	// afterIndexRead is called as each file has been read. Tests use it to
	// interrupt an index part way.
	afterIndexRead = func(path string) {}
)

// indexRoot is a folder the index covers, with what the walk found in it.
type indexRoot struct {
	kind store.RootKind
	path string
	// audio lists the audio files the walk found under the root.
	audio []walkedFile
	// complete is set when the walk listed the whole root, so that files it
	// did not find are known to be gone.
	complete bool
	// missing is set when the folder was not there to be walked.
	missing bool
}

// indexJob is a file the index phase reads.
type indexJob struct {
	walkedFile
	rootID int64
	rel    string
	// known is set when the index held a row for the file before the run,
	// and oldSum is the audio hash of that row.
	known  bool
	oldSum string
}

// modTimeTolerance is how far apart two modification times of a file of one
// size may be and still be the same file. File systems keep times more or
// less finely (NTFS to 100 ns, exFAT to 10 ms, FAT32 to 2 s, many network
// shares to 1 s), so a library indexed on one and copied to another, or
// indexed before moves recorded the time their destination really has, would
// otherwise never match its index again.
const modTimeTolerance = 2 * time.Second

// sameStat reports whether a size and modification time are those the index
// recorded: the same size, and a time within modTimeTolerance.
func sameStat(size int64, modTime time.Time, indexedSize int64, indexedModTime time.Time) bool {
	if size != indexedSize {
		return false
	}
	d := modTime.Sub(indexedModTime)
	return d >= -modTimeTolerance && d <= modTimeTolerance
}

// asOnDisk reports whether the file info describes is the one the index
// recorded with this size and modification time.
func asOnDisk(info os.FileInfo, indexedSize int64, indexedModTime time.Time) bool {
	return info.Mode().IsRegular() && sameStat(info.Size(), info.ModTime(), indexedSize, indexedModTime)
}

// indexRoots lists the folders the index covers: the library, the untagged,
// other and duplicates folders, and the source when the run organizes one. A
// run without a store keeps no index.
func (o *organizer) indexRoots() []*indexRoot {
	cfg := o.cfg
	if cfg.Store == nil {
		return nil
	}
	var roots []*indexRoot
	var paths []string
	add := func(kind store.RootKind, path string) {
		if path == "" || containsPath(paths, path) {
			return
		}
		paths = append(paths, path)
		roots = append(roots, &indexRoot{kind: kind, path: path})
	}
	add(store.RootLibrary, cfg.Destination)
	add(store.RootUntagged, cfg.Untagged)
	add(store.RootOther, cfg.Other)
	add(store.RootDuplicates, cfg.Duplicates)
	if !cfg.IndexOnly && !cfg.Reorganizing() {
		add(store.RootSource, cfg.Source)
	}
	return roots
}

// refreshIndex compares what the walk found with the index. Files that are
// new, or whose size or modification time changed (every file with Rescan),
// are returned to be read; rows of files that are gone are deleted. It also
// reports whether the index was empty.
//
// A root that is missing while the index holds files in it is an error, and
// keeps its rows: an unplugged drive or an unmounted share looks just like
// that, and deleting the rows would give every file a new ID when it is back.
// A missing root with no rows is simply not there yet.
func (o *organizer) refreshIndex(ctx context.Context, roots []*indexRoot) (jobs []indexJob, first bool) {
	st := o.cfg.Store
	n, err := st.CountFiles()
	if err != nil {
		o.fail(st.Path(), "", OpRecord, err)
		return nil, false
	}
	first = n == 0
	for _, root := range roots {
		id, err := st.SetRoot(root.kind, root.path)
		if err != nil {
			o.fail(root.path, "", OpRecord, err)
			continue
		}
		rows, err := st.RootFiles(id)
		if err != nil {
			o.fail(root.path, "", OpRecord, err)
			continue
		}
		for _, f := range root.audio {
			rel, err := filepath.Rel(root.path, f.path)
			if err != nil {
				o.fail(f.path, "", OpRelPath, err)
				continue
			}
			key := store.PathKey(rel)
			row, known := rows[key]
			delete(rows, key)
			switch {
			case !known || o.cfg.Rescan || !sameStat(f.size, f.modTime, row.Size, row.ModTime):
				jobs = append(jobs, indexJob{walkedFile: f, rootID: id, rel: rel, known: known, oldSum: row.Sum})
			case row.RelPath != rel:
				// Same file, respelled, as when a folder above it was renamed.
				if err := st.RespellFile(row.ID, rel); err != nil {
					o.fail(f.path, "", OpRecord, err)
				}
			}
		}
		if root.missing {
			if len(rows) > 0 {
				o.fail(root.path, "", OpWalkRoot, fmt.Errorf("the %s folder is not there, though the index holds %d files in it: "+
					"nothing was removed from the index (is its drive connected?)", root.kind, len(rows)))
			}
			continue
		}
		// A walk cut short says nothing about the files it did not reach.
		if !root.complete || ctx.Err() != nil {
			continue
		}
		var gone []int64
		for _, row := range rows {
			gone = append(gone, row.ID)
		}
		if err := st.DeleteFiles(gone); err != nil {
			o.fail(root.path, "", OpRecord, err)
		}
	}
	return jobs, first
}

// indexFiles reads jobs into the index on the run's workers, committing in
// batches. Once ctx is cancelled no new files are read; those already read
// are committed.
func (o *organizer) indexFiles(ctx context.Context, jobs []indexJob) {
	st := o.cfg.Store
	results := make(chan store.File)
	done := make(chan struct{})
	go func() {
		defer close(done)
		var batch []store.File
		flush := func() {
			if len(batch) == 0 {
				return
			}
			if err := st.PutFiles(batch); err != nil {
				o.fail(st.Path(), "", OpRecord, err)
			} else {
				o.stats.Indexed.Add(int64(len(batch)))
			}
			batch = batch[:0]
		}
		ticker := time.NewTicker(indexBatchInterval)
		defer ticker.Stop()
		for {
			select {
			case f, ok := <-results:
				if !ok {
					flush()
					return
				}
				batch = append(batch, f)
				if len(batch) >= indexBatchFiles {
					flush()
				}
			case <-ticker.C:
				flush()
			}
		}
	}()

	runJobs(ctx, o.cfg.Workers, jobs, func(j indexJob) {
		f, details, err := readIndexFile(j)
		if err != nil {
			o.fail(j.path, "", OpIndex, err)
		} else {
			if details != nil {
				// Said aloud, and not only noted in the row: when every
				// file fails here, it is the reader that is broken.
				o.emit(Warning{Kind: WarnDetailsUnread, Path: j.path, Err: details})
			}
			// What the index held before the run decides whether the run
			// adds the file to a duplicate group, not that it was read:
			// a rescan reads every file, and none of them is new.
			if !j.known || j.oldSum != f.Sum {
				o.added.Store(rules.PathKey(j.path), struct{}{})
			}
			results <- f
		}
		o.phase.advance(j.size)
		afterIndexRead(j.path)
	})
	close(results)
	<-done
}

// indexedBefore reports whether a run before this one walked the library to
// the end. A database that cannot say counts as no: fewer files are then
// taken for new, and none is moved for it.
func (o *organizer) indexedBefore() bool {
	st := o.cfg.Store
	before, err := st.IndexedBefore(o.runID)
	if err != nil {
		o.fail(st.Path(), "", OpRecord, err)
		return false
	}
	return before
}

// readProperties reads the audio details of a file: its length, bit rate and
// the like. Tests replace it to have the reader fail.
var readProperties = taglib.ReadProperties

// readIndexFile reads a file's tags, audio hash and properties into an index
// row. Only a file that cannot be opened fails; what cannot be read of one
// that can is noted in the row's ReadError, so a damaged file is not re-read
// on every run. So is the first element of its tags the reader had to leave
// out, though the rest of them were read. details is why its audio details
// (length, bit rate and the like) could not be read, nil when they were.
func readIndexFile(j indexJob) (f store.File, details, err error) {
	f = store.File{RootID: j.rootID, RelPath: j.rel, Size: j.size, ModTime: j.modTime, IndexedAt: time.Now()}
	file, err := os.Open(j.path)
	if err != nil {
		return f, nil, err
	}
	defer file.Close()

	var problems []string
	md, err := tag.ReadFrom(file)
	switch {
	case err == nil:
		fillTags(&f, md)
		if skipped := tag.Skipped(md); skipped != nil {
			problems = append(problems, "tags: "+skipped.Error())
		}
	case errors.Is(err, tag.ErrNoTagsFound):
	default:
		problems = append(problems, "tags: "+err.Error())
	}

	if _, err := file.Seek(0, io.SeekStart); err != nil {
		problems = append(problems, "hash: "+err.Error())
	} else if f.Sum, err = tag.Sum(file); err != nil {
		f.Sum = ""
		problems = append(problems, "hash: "+err.Error())
	}

	if props, err := readProperties(j.path); err != nil {
		problems = append(problems, "properties: "+err.Error())
		details = err
	} else {
		f.Lossless = isLossless(props)
		f.Bitrate = int(props.BitRate)
		f.SampleRate = int(props.SampleRate)
		f.BitDepth = int(props.BitDepth)
		f.Channels = int(props.Channels)
		f.Length = props.Length
		// go-taglib lists the images of every format; the tag reader finds
		// those of most.
		f.HasArt = f.HasArt || len(props.Images) > 0
	}
	f.ReadError = strings.Join(problems, "; ")
	return f, details, nil
}

// fillTags copies what the index keeps of md into f.
func fillTags(f *store.File, md tag.Metadata) {
	f.Format = string(md.FileType())
	f.Artist = md.Artist()
	f.AlbumArtist = md.AlbumArtist()
	f.Album = md.Album()
	f.Title = md.Title()
	f.Track, f.TrackTotal = md.Track()
	f.Disc, f.DiscTotal = md.Disc()
	f.Year = md.Year()
	ids := mbz.Extract(md)
	f.ReleaseID, f.ReleaseTrackID, f.RecordingID = ids.AlbumID, ids.ReleaseTrackID, ids.RecordingID
	if m, ok := marker.Read(md); ok {
		f.Manual = m.String()
	}
	f.HasArt = md.Picture() != nil
	f.TagsJSON = rawJSON(md.Raw())
}

// isLossless reports whether go-taglib's properties describe lossless audio.
// A WAV or AIFF file is a container: it is lossless when it holds PCM, which
// go-taglib names as its inner codec, and not when it holds ADPCM or MP3.
func isLossless(p taglib.Properties) bool {
	switch p.Format {
	case "flac", "ape", "wavpack", "tta", "dsf", "dsdiff":
		return true
	}
	switch p.InnerCodec {
	case "alac", "flac", "pcm":
		return true
	}
	return strings.Contains(p.InnerCodec, "lossless")
}

// pictureComments are the Vorbis comments that hold a picture as base64
// text: METADATA_BLOCK_PICTURE, and COVERART, which it replaced.
var pictureComments = map[string]bool{"metadata_block_picture": true, "coverart": true}

// rawJSON encodes everything the tag reader returned. Pictures and other
// binary values are described rather than copied: the index is no place for
// cover art. That goes for a picture a FLAC or OGG file holds as a base64
// comment too, which the reader returns as a string like any other.
func rawJSON(raw map[string]any) string {
	out := make(map[string]any, len(raw))
	for k, v := range raw {
		if s, ok := v.(string); ok && pictureComments[strings.ToLower(k)] {
			out[k] = map[string]any{"picture": "", "bytes": base64.StdEncoding.DecodedLen(len(s))}
			continue
		}
		switch v := v.(type) {
		case *tag.Picture:
			if v != nil {
				out[k] = map[string]any{"picture": v.Type, "mime": v.MIMEType, "description": v.Description, "bytes": len(v.Data)}
			}
		case []byte:
			if len(v) <= 64 {
				out[k] = map[string]any{"base64": base64.StdEncoding.EncodeToString(v)}
			} else {
				out[k] = map[string]any{"bytes": len(v)}
			}
		default:
			if _, err := json.Marshal(v); err != nil {
				out[k] = fmt.Sprint(v)
			} else {
				out[k] = v
			}
		}
	}
	data, err := json.Marshal(out)
	if err != nil {
		return "{}"
	}
	return string(data)
}
