package engine

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/jpeg"
	"image/png"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"go.senan.xyz/taglib"

	"github.com/zachjamesgreen/tagshelf/internal/coverart"
	"github.com/zachjamesgreen/tagshelf/internal/fixtures"
	"github.com/zachjamesgreen/tagshelf/internal/store"
)

// testPicture makes a picture of size by size pixels, a JPEG or a PNG.
func testPicture(t *testing.T, format string, size int) []byte {
	t.Helper()
	var buf bytes.Buffer
	img := image.NewGray(image.Rect(0, 0, size, size))
	var err error
	if format == "png" {
		err = png.Encode(&buf, img)
	} else {
		err = jpeg.Encode(&buf, img, nil)
	}
	if err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// archive stands in for the Cover Art Archive: it answers with what it was
// given and notes what it was asked.
type archive struct {
	picture []byte
	err     error
	asked   [][3]any
}

func (a *archive) fetch(_ context.Context, release, group string, size int) (coverart.Picture, error) {
	a.asked = append(a.asked, [3]any{release, group, size})
	if a.err != nil {
		return coverart.Picture{}, a.err
	}
	return coverart.Describe(a.picture)
}

// coverLibrary is an indexed library of one album folder with two files of
// one MusicBrainz release, and beside it a folder of one file without tags.
func coverLibrary(t *testing.T) (lib, album string, s *store.Store) {
	t.Helper()
	lib = filepath.Join(t.TempDir(), "lib")
	album = filepath.Join(lib, "Artist", "Album")
	writeTestFile(t, filepath.Join(album, "01.flac"), readTestFile(t, fixtures.Tagged(".flac")))
	writeTestFile(t, filepath.Join(album, "02.mp3"), readTestFile(t, fixtures.Tagged(".mp3")))
	writeTestFile(t, filepath.Join(lib, "Loose", "x.mp3"), readTestFile(t, fixtures.Untagged(".mp3")))
	return lib, album, nil
}

// covered runs a cover art run over the library and returns how it ended
// with what it reported.
func covered(t *testing.T, lib string, s *store.Store, c CoverOptions, dry bool) (RunFinished, *collector) {
	t.Helper()
	c.File = true
	var col collector
	finish := CoverArt(context.Background(), Options{Destination: lib, Store: s, Sink: col.sink, DryRun: dry}, c)
	return finish, &col
}

func (c *collector) coversWritten() (written []CoverWritten, skipped []CoverSkipped) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, e := range c.events {
		switch e := e.(type) {
		case CoverWritten:
			written = append(written, e)
		case CoverSkipped:
			skipped = append(skipped, e)
		}
	}
	return written, skipped
}

// A folder whose files hold a picture gets it as its cover file, as it is,
// and the archive is not asked. A folder without a picture or a release is
// left, with why.
func TestCoverArtFromThePictureInTheFiles(t *testing.T) {
	lib, album, _ := coverLibrary(t)
	art := testPicture(t, "png", 40)
	if err := taglib.WriteImage(filepath.Join(album, "02.mp3"), art); err != nil {
		t.Fatal(err)
	}
	s := openStore(t)
	indexLibrary(t, s, lib)
	a := &archive{picture: testPicture(t, "jpeg", 1200)}

	finish, col := covered(t, lib, s, CoverOptions{Fetch: a.fetch}, false)
	if finish.State != StateOK || finish.Counters.Covers != 1 || finish.Counters.Errors != 0 {
		t.Fatalf("finish = %+v, want one cover written", finish)
	}
	cover := filepath.Join(album, "cover.png")
	if got := readTestFile(t, cover); got != string(art) {
		t.Errorf("the cover file is %d bytes, want the picture of the file, %d bytes, as it is", len(got), len(art))
	}
	if len(a.asked) != 0 {
		t.Errorf("the archive was asked %v, though the files hold a picture", a.asked)
	}
	written, skipped := col.coversWritten()
	if len(written) != 1 || written[0].Path != cover || written[0].From != CoverFromFile || written[0].Replaced != "" {
		t.Errorf("written = %+v", written)
	}
	if len(skipped) != 1 || skipped[0].Dir != filepath.Join(lib, "Loose") {
		t.Errorf("skipped = %+v, want the folder without a picture or a release", skipped)
	}
	moves, err := s.Moves(finish.RunID)
	if err != nil || len(moves) != 1 || moves[0].Reason != store.ReasonCover || moves[0].To != cover || moves[0].From != cover ||
		len(moves[0].CueBefore) != 0 || len(moves[0].CueAfter) != 32 {
		t.Errorf("the run recorded %+v, %v", moves, err)
	}

	// A second run finds the cover and does nothing.
	finish, col = covered(t, lib, s, CoverOptions{Fetch: a.fetch}, false)
	if written, _ := col.coversWritten(); finish.Counters.Covers != 0 || len(written) != 0 {
		t.Errorf("a second run wrote %+v", written)
	}
}

// A folder without a picture is given the front cover of the release its
// files name, in the largest size, with the release group to fall back on.
func TestCoverArtFromTheArchive(t *testing.T) {
	lib, album, _ := coverLibrary(t)
	s := openStore(t)
	indexLibrary(t, s, lib)
	a := &archive{picture: testPicture(t, "jpeg", 1200)}

	finish, col := covered(t, lib, s, CoverOptions{Fetch: a.fetch}, false)
	if finish.State != StateOK || finish.Counters.Covers != 1 {
		t.Fatalf("finish = %+v, want one cover written", finish)
	}
	if want := [][3]any{{fixtures.ReleaseID, fixtures.ReleaseGroupID, coverart.Large}}; !slices.Equal(a.asked, want) {
		t.Errorf("the archive was asked %v, want %v", a.asked, want)
	}
	cover := filepath.Join(album, "cover.jpg")
	if got := readTestFile(t, cover); got != string(a.picture) {
		t.Error("the cover file is not the picture the archive sent")
	}
	if written, _ := col.coversWritten(); len(written) != 1 || written[0].From != CoverFromRelease {
		t.Errorf("written = %+v", written)
	}
}

// What the archive answers: none is a folder left with why, anything else an
// error of the run, and the folder has no cover either way.
func TestCoverArtWhenTheArchiveHasNone(t *testing.T) {
	lib, album, _ := coverLibrary(t)
	s := openStore(t)
	indexLibrary(t, s, lib)
	a := &archive{err: coverart.ErrNone}
	finish, col := covered(t, lib, s, CoverOptions{Fetch: a.fetch}, false)
	if _, skipped := col.coversWritten(); finish.State != StateOK || finish.Counters.Covers != 0 || len(skipped) != 2 {
		t.Errorf("with none at the archive: finish %+v, skipped %+v", finish, skipped)
	}
	a.err = coverart.ErrBusy
	finish, _ = covered(t, lib, s, CoverOptions{Fetch: a.fetch}, false)
	if finish.State != StateErrors || finish.Counters.Errors != 1 {
		t.Errorf("with the archive busy: finish %+v, want one error", finish)
	}
	// Without the archive at all, only the files' own pictures count.
	finish, col = covered(t, lib, s, CoverOptions{}, false)
	if _, skipped := col.coversWritten(); finish.State != StateOK || len(skipped) != 2 {
		t.Errorf("without the archive: finish %+v, skipped %+v", finish, skipped)
	}
	mustNotExist(t, filepath.Join(album, "cover.jpg"), filepath.Join(album, "cover.png"))
}

// The archive is asked only when the files of a folder agree on a release.
func TestCoverArtOfAFolderOfTwoReleases(t *testing.T) {
	lib, album, _ := coverLibrary(t)
	other := filepath.Join(album, "03.flac")
	writeTestFile(t, other, readTestFile(t, fixtures.Tagged(".flac")))
	if err := taglib.WriteTags(other, map[string][]string{taglib.MusicBrainzAlbumID: {"aaaaaaaa-0000-4000-8000-00000000000b"}}, 0); err != nil {
		t.Fatal(err)
	}
	s := openStore(t)
	indexLibrary(t, s, lib)
	a := &archive{picture: testPicture(t, "jpeg", 500)}
	finish, col := covered(t, lib, s, CoverOptions{Fetch: a.fetch}, false)
	_, skipped := col.coversWritten()
	if finish.Counters.Covers != 0 || len(a.asked) != 0 || len(skipped) != 2 {
		t.Errorf("finish %+v, asked %v, skipped %+v", finish, a.asked, skipped)
	}
}

// A cover the folder has is kept, whatever it is called, unless the setting
// says the archive's takes its place: when it is larger, or always.
func TestCoverArtReplacesOnlyWhenTold(t *testing.T) {
	small, large := testPicture(t, "png", 300), testPicture(t, "jpeg", 1200)
	tests := []struct {
		name, replace, has string
		archive            []byte
		asked, replaced    bool
	}{
		{"never", CoverNever, "folder.png", large, false, false},
		{"the setting left out is never", "", "folder.png", large, false, false},
		{"smaller, and the archive's is larger", CoverSmaller, "folder.png", large, true, true},
		{"smaller, and the archive's is no larger", CoverSmaller, "folder.png", testPicture(t, "jpeg", 300), true, false},
		{"always", CoverAlways, "Front.png", testPicture(t, "jpeg", 250), true, true},
		{"always, over a file of the same name", CoverAlways, "cover.jpg", large, true, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			lib, album, _ := coverLibrary(t)
			had := filepath.Join(album, tt.has)
			writeTestFile(t, had, string(small))
			s := openStore(t)
			indexLibrary(t, s, lib)
			a := &archive{picture: tt.archive}

			finish, col := covered(t, lib, s, CoverOptions{Fetch: a.fetch, Replace: tt.replace, Folders: []string{album}}, false)
			if finish.State != StateOK || (len(a.asked) > 0) != tt.asked {
				t.Fatalf("finish %+v, the archive asked %d times, want asked %t", finish, len(a.asked), tt.asked)
			}
			written, _ := col.coversWritten()
			if !tt.replaced {
				if len(written) != 0 || readTestFile(t, had) != string(small) {
					t.Errorf("the cover the folder had was not kept: written %+v", written)
				}
				mustNotExist(t, filepath.Join(album, "cover.jpeg"))
				return
			}
			cover := filepath.Join(album, "cover.jpg")
			if len(written) != 1 || written[0].Replaced != had || readTestFile(t, cover) != string(tt.archive) {
				t.Fatalf("written = %+v", written)
			}
			if tt.has != "cover.jpg" {
				mustNotExist(t, had)
			}
			moves, err := s.Moves(finish.RunID)
			if err != nil || len(moves) != 1 || moves[0].From != had || !bytes.Equal(moves[0].CueBefore, small) {
				t.Errorf("the run recorded %+v, %v; want the replaced picture whole", moves, err)
			}

			// And the undo puts the folder's own back, under its own name.
			plan, err := PlanUndo(s, finish.RunID, nil)
			if err != nil || plan.Covers != 1 || len(plan.Skipped) != 0 {
				t.Fatalf("the undo's plan = %+v, %v", plan, err)
			}
			undone := Undo(context.Background(), Options{Destination: lib, Store: s}, finish.RunID, nil, nil)
			if undone.State != StateOK || undone.Counters.Covers != 1 {
				t.Fatalf("the undo = %+v", undone)
			}
			if readTestFile(t, had) != string(small) {
				t.Error("the folder's own cover is not back")
			}
			if tt.has != "cover.jpg" {
				mustNotExist(t, cover)
			}
		})
	}
}

// Undoing a run takes its cover file away again, if it is still the picture
// the run wrote: one changed since is left.
func TestUndoTakesACoverAwayAgain(t *testing.T) {
	lib, album, _ := coverLibrary(t)
	s := openStore(t)
	indexLibrary(t, s, lib)
	a := &archive{picture: testPicture(t, "jpeg", 1200)}
	finish, _ := covered(t, lib, s, CoverOptions{Fetch: a.fetch}, false)
	cover := filepath.Join(album, "cover.jpg")
	mustExist(t, cover)

	writeTestFile(t, cover, string(testPicture(t, "jpeg", 80)))
	plan, err := PlanUndo(s, finish.RunID, nil)
	if err != nil || plan.Covers != 0 || len(plan.Skipped) != 1 {
		t.Fatalf("with the cover changed since, the plan = %+v, %v", plan, err)
	}
	writeTestFile(t, cover, string(a.picture))
	undone := Undo(context.Background(), Options{Destination: lib, Store: s}, finish.RunID, nil, nil)
	if undone.State != StateOK || undone.Counters.Covers != 1 {
		t.Fatalf("the undo = %+v", undone)
	}
	mustNotExist(t, cover)
	mustExist(t, filepath.Join(album, "01.flac"), filepath.Join(album, "02.mp3"))
}

// A dry run asks the archive nothing and writes nothing; it says what it
// would do. Without a database there is nowhere to record a run.
func TestCoverArtDryRun(t *testing.T) {
	lib, album, _ := coverLibrary(t)
	s := openStore(t)
	indexLibrary(t, s, lib)
	a := &archive{picture: testPicture(t, "jpeg", 1200)}
	finish, col := covered(t, lib, s, CoverOptions{Fetch: a.fetch}, true)
	written, _ := col.coversWritten()
	if finish.State != StateOK || len(a.asked) != 0 || len(written) != 1 || !written[0].Asked || written[0].From != CoverFromRelease {
		t.Errorf("finish %+v, asked %v, written %+v", finish, a.asked, written)
	}
	mustNotExist(t, filepath.Join(album, "cover.jpg"))
	if moves, err := s.Moves(finish.RunID); err != nil || len(moves) != 0 {
		t.Errorf("the dry run recorded %d moves (%v)", len(moves), err)
	}

	if finish := CoverArt(context.Background(), Options{Destination: lib}, CoverOptions{File: true}); finish.State != StateNotStarted {
		t.Errorf("without a database: %+v", finish)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	stopped := CoverArt(ctx, Options{Destination: lib, Store: s}, CoverOptions{File: true, Fetch: a.fetch})
	if stopped.State != StateInterrupted || len(a.asked) != 0 {
		t.Errorf("stopped before it began: %+v, asked %v", stopped, a.asked)
	}
	if _, err := os.Stat(filepath.Join(album, "cover.jpg")); !errors.Is(err, os.ErrNotExist) {
		t.Error("a run stopped before it began wrote a cover")
	}
}
