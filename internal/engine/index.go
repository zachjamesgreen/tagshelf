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

	"tagshelf/internal/marker"
	"tagshelf/internal/rules"
	"tagshelf/internal/store"
	"tagshelf/internal/tag"
	"tagshelf/internal/tag/mbz"
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
}

// indexJob is a file the index phase reads.
type indexJob struct {
	walkedFile
	rootID int64
	rel    string
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
			case !known || o.cfg.Rescan || row.Size != f.size || !row.ModTime.Equal(f.modTime):
				jobs = append(jobs, indexJob{walkedFile: f, rootID: id, rel: rel})
			case row.RelPath != rel:
				// Same file, respelled, as when a folder above it was renamed.
				if err := st.RespellFile(row.ID, rel); err != nil {
					o.fail(f.path, "", OpRecord, err)
				}
			}
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
		f, err := readIndexFile(j)
		if err != nil {
			o.fail(j.path, "", OpIndex, err)
		} else {
			o.reread.Store(rules.PathKey(j.path), struct{}{})
			results <- f
		}
		o.phase.advance(j.size)
		afterIndexRead(j.path)
	})
	close(results)
	<-done
}

// readIndexFile reads a file's tags, audio hash and properties into an index
// row. Only a file that cannot be opened fails; what cannot be read of one
// that can is noted in the row's ReadError, so a damaged file is not re-read
// on every run.
func readIndexFile(j indexJob) (store.File, error) {
	f := store.File{RootID: j.rootID, RelPath: j.rel, Size: j.size, ModTime: j.modTime, IndexedAt: time.Now()}
	file, err := os.Open(j.path)
	if err != nil {
		return f, err
	}
	defer file.Close()

	var problems []string
	md, err := tag.ReadFrom(file)
	switch {
	case err == nil:
		fillTags(&f, md)
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

	if props, err := taglib.ReadProperties(j.path); err != nil {
		problems = append(problems, "properties: "+err.Error())
	} else {
		f.Lossless = isLossless(props)
		f.Bitrate = int(props.BitRate)
		f.SampleRate = int(props.SampleRate)
		f.BitDepth = int(props.BitDepth)
		f.Channels = int(props.Channels)
		f.Length = props.Length
	}
	f.ReadError = strings.Join(problems, "; ")
	return f, nil
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
	f.TagsJSON = rawJSON(md.Raw())
}

// isLossless reports whether go-taglib's properties describe lossless audio.
func isLossless(p taglib.Properties) bool {
	switch p.Format {
	case "flac", "wav", "aiff", "ape", "wavpack", "tta", "dsf", "dsdiff":
		return true
	}
	switch p.InnerCodec {
	case "alac", "flac", "pcm":
		return true
	}
	return strings.Contains(p.InnerCodec, "lossless")
}

// rawJSON encodes everything the tag reader returned. Pictures and other
// binary values are described rather than copied: the index is no place for
// cover art.
func rawJSON(raw map[string]any) string {
	out := make(map[string]any, len(raw))
	for k, v := range raw {
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
