package tui

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zachjamesgreen/tagshelf/internal/store"
)

// bigLibrary makes the database of a library of the given number of indexed
// files, each with 1.5 KB of tags as the tag reader returned them, and
// returns its path. A tenth of the files are in _Untagged, each put there by
// a recorded move; 500 pairs of exact duplicates wait for review and 2,000
// more have a keep-both review on record; the history holds 200 runs.
func bigLibrary(tb testing.TB, files int) string {
	tb.Helper()
	dir := tb.TempDir()
	dbPath := filepath.Join(dir, "tagshelf.db")
	st, err := store.Open(dbPath)
	if err != nil {
		tb.Fatal(err)
	}
	defer st.Close()
	lib := filepath.Join(dir, "Music")
	libRoot, err := st.SetRoot(store.RootLibrary, lib)
	if err != nil {
		tb.Fatal(err)
	}
	untRoot, err := st.SetRoot(store.RootUntagged, filepath.Join(lib, "_Untagged"))
	if err != nil {
		tb.Fatal(err)
	}
	const waiting, reviewed = 500, 2000
	tags := `{"comment":"` + strings.Repeat("x", 1500) + `"}`
	now := time.Now()
	batch := make([]store.File, 0, 1000)
	flush := func() {
		if err := st.PutFiles(batch); err != nil {
			tb.Fatal(err)
		}
		batch = batch[:0]
	}
	for i := range files {
		f := store.File{RootID: libRoot, Format: "FLAC", Lossless: true, SampleRate: 44100, BitDepth: 16,
			Artist: fmt.Sprintf("Artist %04d", i/200), Album: fmt.Sprintf("Album %05d", i/12), Title: fmt.Sprintf("Song %06d", i),
			Track: i%12 + 1, Year: 2001, Length: 200 * time.Second, HasArt: i%3 == 0,
			Sum: fmt.Sprintf("%040x", i), Size: 1, ModTime: time.Unix(1, 0), IndexedAt: now, TagsJSON: tags}
		f.RelPath = filepath.Join(f.Artist, f.Album, fmt.Sprintf("%02d - %s.flac", f.Track, f.Title))
		if i%10 == 9 {
			f.RootID = untRoot
			f.RelPath = filepath.Join(fmt.Sprintf("rip %04d", i/12), fmt.Sprintf("%06d.flac", i))
		}
		if i < 2*(waiting+reviewed) && i%2 == 1 {
			// The copy of the file before it: the same audio under another name.
			f.Sum = fmt.Sprintf("%040x", i-1)
		}
		if batch = append(batch, f); len(batch) == cap(batch) {
			flush()
		}
	}
	flush()

	run := func(kind store.RunKind) int64 {
		id, err := st.StartRun(kind, false, filepath.Join(dir, "src"), lib, now)
		if err != nil {
			tb.Fatal(err)
		}
		return id
	}
	groups, err := st.Groups(func(store.File) string { return "" })
	if err != nil || len(groups) != waiting+reviewed {
		tb.Fatalf("%d groups, %v; want %d", len(groups), err, waiting+reviewed)
	}
	for _, g := range groups[waiting:] {
		if err := st.RecordReview(store.Review{Kind: g.Kind, Members: g.IDs(), Outcome: store.OutcomeKeepBoth, By: store.ByUser}); err != nil {
			tb.Fatal(err)
		}
	}
	var id int64
	for i := range 200 {
		id = run(store.KindOrganize)
		if err := st.FinishRun(id, store.Counters{Organized: int64(i)}, store.StateOK, now); err != nil {
			tb.Fatal(err)
		}
	}
	untagged, err := st.UntaggedFiles()
	if err != nil {
		tb.Fatal(err)
	}
	// A move recorded onto a file's own path leaves its row where it is.
	for _, f := range untagged {
		if err := st.RecordMove(id, filepath.Join(dir, "src", f.RelPath), f.Path, store.ReasonUntagged); err != nil {
			tb.Fatal(err)
		}
	}
	return dbPath
}
