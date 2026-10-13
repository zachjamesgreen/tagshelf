package engine

import (
	"context"
	"crypto/sha256"
	"path/filepath"
	"slices"
	"testing"

	"go.senan.xyz/taglib"

	"github.com/zachjamesgreen/tagshelf/internal/coverart"
	"github.com/zachjamesgreen/tagshelf/internal/store"
	"github.com/zachjamesgreen/tagshelf/internal/tagwrite"
)

// savePicture is the PictureSave of the tests, and of tagshelf itself: the
// safe save of tagwrite, put on record in the store.
func savePicture(st *store.Store, runID int64, path string, picture []byte) error {
	_, err := tagwrite.SavePicture(path, picture, func(before []byte, apply func() error) error {
		sum := sha256.Sum256(picture)
		return st.RecordPicture(runID, path, before, sum[:], apply)
	})
	return err
}

// embedded runs a cover art run that puts pictures into files, and returns
// how it ended with the pictures it reported.
func embedded(t *testing.T, lib string, s *store.Store, c CoverOptions, dry bool) (RunFinished, []CoverEmbedded, []CoverSkipped) {
	t.Helper()
	c.Embed = true
	var col collector
	finish := CoverArt(context.Background(), Options{Destination: lib, Store: s, Sink: col.sink, DryRun: dry, SavePicture: savePicture}, c)
	var put []CoverEmbedded
	var skipped []CoverSkipped
	for _, e := range col.events {
		switch e := e.(type) {
		case CoverEmbedded:
			put = append(put, e)
		case CoverSkipped:
			skipped = append(skipped, e)
		}
	}
	return finish, put, skipped
}

func pictureOf(t *testing.T, path string) []byte {
	t.Helper()
	pic, err := taglib.ReadImage(path)
	if err != nil {
		t.Fatal(err)
	}
	return pic
}

// The cover file of an album's folder goes into each of its audio files that
// holds no picture, as it is, and the index follows. The archive is not
// asked, and a folder without a cover or a release is left, with why.
func TestCoverEmbedFromTheFolder(t *testing.T) {
	lib, album, _ := coverLibrary(t)
	cover := testPicture(t, "png", 60)
	writeTestFile(t, filepath.Join(album, "Folder.png"), string(cover))
	s := openStore(t)
	indexLibrary(t, s, lib)
	a := &archive{picture: testPicture(t, "jpeg", 500)}

	finish, put, skipped := embedded(t, lib, s, CoverOptions{Fetch: a.fetch}, false)
	if finish.State != StateOK || finish.Counters.Pictures != 2 || finish.Counters.Covers != 0 || finish.Counters.Errors != 0 {
		t.Fatalf("finish = %+v, want a picture in each of the album's two files", finish)
	}
	flac, mp3 := filepath.Join(album, "01.flac"), filepath.Join(album, "02.mp3")
	for _, path := range []string{flac, mp3} {
		if got := pictureOf(t, path); string(got) != string(cover) {
			t.Errorf("%s holds a picture of %d bytes, want the folder's cover as it is", filepath.Base(path), len(got))
		}
		if row := indexedFiles(t, s)[path]; !row.HasArt {
			t.Errorf("the index does not know %s holds a picture now", filepath.Base(path))
		}
	}
	if len(put) != 2 || put[0].From != CoverFromFolder || put[0].Replaced || len(a.asked) != 0 {
		t.Errorf("put = %+v, the archive asked %v", put, a.asked)
	}
	if len(skipped) != 1 || !skipped[0].Embedding || skipped[0].Dir != filepath.Join(lib, "Loose") {
		t.Errorf("skipped = %+v", skipped)
	}
	moves, err := s.Moves(finish.RunID)
	if err != nil || len(moves) != 2 || moves[0].Reason != store.ReasonPicture || len(moves[0].CueBefore) != 0 || len(moves[0].CueAfter) != 32 {
		t.Errorf("the run recorded %+v, %v", moves, err)
	}

	// A second run finds a picture in each and does nothing.
	if finish, put, _ := embedded(t, lib, s, CoverOptions{Fetch: a.fetch}, false); finish.Counters.Pictures != 0 || len(put) != 0 {
		t.Errorf("a second run put %+v", put)
	}

	// The undo takes them out again.
	plan, err := PlanUndo(s, finish.RunID, nil)
	if err != nil || plan.Pictures != 2 || len(plan.Skipped) != 0 {
		t.Fatalf("the undo's plan = %+v, %v", plan, err)
	}
	undone := Undo(context.Background(), Options{Destination: lib, Store: s, SavePicture: savePicture}, finish.RunID, nil, nil)
	if undone.State != StateOK || undone.Counters.Pictures != 2 {
		t.Fatalf("the undo = %+v", undone)
	}
	for _, path := range []string{flac, mp3} {
		if got := pictureOf(t, path); len(got) != 0 {
			t.Errorf("after the undo %s still holds a picture", filepath.Base(path))
		}
		if row := indexedFiles(t, s)[path]; row.HasArt {
			t.Errorf("the index still has %s holding a picture", filepath.Base(path))
		}
	}
}

// A folder without a cover file has the archive asked, in its middle size,
// and with the cover file wanted too, the file comes first and is what goes
// into the audio.
func TestCoverEmbedFromTheArchive(t *testing.T) {
	lib, album, _ := coverLibrary(t)
	s := openStore(t)
	indexLibrary(t, s, lib)
	a := &archive{picture: testPicture(t, "jpeg", 500)}
	finish, put, _ := embedded(t, lib, s, CoverOptions{Fetch: a.fetch, Folders: []string{album}}, false)
	if finish.State != StateOK || finish.Counters.Pictures != 2 || len(put) != 2 || put[0].From != CoverFromRelease {
		t.Fatalf("finish = %+v, put %+v", finish, put)
	}
	if len(a.asked) != 1 || a.asked[0][2] != coverart.EmbedSize {
		t.Errorf("the archive was asked %v, want once for the album, in the size for files", a.asked)
	}
	if got := pictureOf(t, filepath.Join(album, "01.flac")); string(got) != string(a.picture) {
		t.Error("the file does not hold the archive's picture")
	}
	mustNotExist(t, filepath.Join(album, "cover.jpg"))

	// With both wanted: one request for the file, and the file's picture in
	// the audio.
	lib, album, _ = coverLibrary(t)
	s = openStore(t)
	indexLibrary(t, s, lib)
	a = &archive{picture: testPicture(t, "jpeg", 1200)}
	finish, put, _ = embedded(t, lib, s, CoverOptions{File: true, Fetch: a.fetch, Folders: []string{album}}, false)
	if finish.Counters.Covers != 1 || finish.Counters.Pictures != 2 || len(put) != 2 || put[0].From != CoverFromFolder || len(a.asked) != 1 {
		t.Fatalf("with both wanted: finish %+v, put %+v, asked %v", finish, put, a.asked)
	}
	mustExist(t, filepath.Join(album, "cover.jpg"))
}

// Without a cover file, the picture one of the album's files holds goes into
// the others, and the archive is not asked.
func TestCoverEmbedFromAnotherFile(t *testing.T) {
	lib, album, _ := coverLibrary(t)
	flac, mp3 := filepath.Join(album, "01.flac"), filepath.Join(album, "02.mp3")
	own := testPicture(t, "png", 80)
	if err := taglib.WriteImage(flac, own); err != nil {
		t.Fatal(err)
	}
	s := openStore(t)
	indexLibrary(t, s, lib)
	a := &archive{picture: testPicture(t, "jpeg", 500)}
	finish, put, _ := embedded(t, lib, s, CoverOptions{Fetch: a.fetch, Folders: []string{album}}, false)
	if finish.State != StateOK || finish.Counters.Pictures != 1 || len(put) != 1 || put[0].Path != mp3 || put[0].From != CoverFromFile || len(a.asked) != 0 {
		t.Fatalf("finish = %+v, put %+v, the archive asked %v", finish, put, a.asked)
	}
	if string(pictureOf(t, mp3)) != string(own) || string(pictureOf(t, flac)) != string(own) {
		t.Error("the two files do not hold the one picture")
	}
}

// A picture a file holds is kept, unless the setting has the archive's take
// its place: when it is larger, or always. The picture that gave way is on
// record, and the undo puts it back.
func TestCoverEmbedReplacesOnlyWhenTold(t *testing.T) {
	old := testPicture(t, "png", 200)
	tests := []struct {
		name, replace string
		archive       int
		replaced      bool
	}{
		{"never", CoverNever, 500, false},
		{"smaller, and the archive's is larger", CoverSmaller, 500, true},
		{"smaller, and the archive's is no larger", CoverSmaller, 200, false},
		{"always", CoverAlways, 100, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			lib, album, _ := coverLibrary(t)
			flac, mp3 := filepath.Join(album, "01.flac"), filepath.Join(album, "02.mp3")
			for _, path := range []string{flac, mp3} {
				if err := taglib.WriteImage(path, old); err != nil {
					t.Fatal(err)
				}
			}
			s := openStore(t)
			indexLibrary(t, s, lib)
			a := &archive{picture: testPicture(t, "jpeg", tt.archive)}
			finish, put, _ := embedded(t, lib, s, CoverOptions{Fetch: a.fetch, Replace: tt.replace, Folders: []string{album}}, false)
			if finish.State != StateOK {
				t.Fatalf("finish = %+v", finish)
			}
			if !tt.replaced {
				if len(put) != 0 || string(pictureOf(t, flac)) != string(old) {
					t.Errorf("the picture the file held was not kept: put %+v", put)
				}
				if tt.replace == CoverNever && len(a.asked) != 0 {
					t.Errorf("the archive was asked %v", a.asked)
				}
				return
			}
			if len(put) != 2 || !put[0].Replaced || string(pictureOf(t, mp3)) != string(a.picture) {
				t.Fatalf("put = %+v", put)
			}
			moves, err := s.Moves(finish.RunID)
			if err != nil || len(moves) != 2 || string(moves[0].CueBefore) != string(old) {
				t.Errorf("the run recorded %d moves (%v); want the picture that gave way, whole", len(moves), err)
			}
			undone := Undo(context.Background(), Options{Destination: lib, Store: s, SavePicture: savePicture}, finish.RunID, nil, nil)
			if undone.State != StateOK || string(pictureOf(t, flac)) != string(old) || string(pictureOf(t, mp3)) != string(old) {
				t.Errorf("the undo = %+v; the files do not hold their own picture again", undone)
			}
		})
	}
}

// A picture changed since the run put it in is left by the undo. A dry run
// writes nothing and asks nothing, and without a way to write pictures the
// run does not start.
func TestCoverEmbedUndoAndDryRun(t *testing.T) {
	lib, album, _ := coverLibrary(t)
	s := openStore(t)
	indexLibrary(t, s, lib)
	a := &archive{picture: testPicture(t, "jpeg", 500)}
	flac := filepath.Join(album, "01.flac")

	// Without a cover in the folder the archive would be asked; with one,
	// the dry run says the folder's would go in.
	finish, put, _ := embedded(t, lib, s, CoverOptions{Fetch: a.fetch}, true)
	if finish.State != StateOK || len(put) != 2 || len(a.asked) != 0 || len(pictureOf(t, flac)) != 0 ||
		!slices.ContainsFunc(put, func(p CoverEmbedded) bool { return p.Asked && p.From == CoverFromRelease }) {
		t.Errorf("the dry run without a cover file: finish %+v, put %+v, asked %v", finish, put, a.asked)
	}
	writeTestFile(t, filepath.Join(album, "cover.png"), string(testPicture(t, "png", 60)))
	finish, put, _ = embedded(t, lib, s, CoverOptions{Fetch: a.fetch}, true)
	if finish.State != StateOK || len(put) != 2 || put[0].Asked || put[0].From != CoverFromFolder || len(a.asked) != 0 || len(pictureOf(t, flac)) != 0 {
		t.Errorf("the dry run with a cover file: finish %+v, put %+v, asked %v", finish, put, a.asked)
	}

	finish, _, _ = embedded(t, lib, s, CoverOptions{Folders: []string{album}}, false)
	if finish.Counters.Pictures != 2 {
		t.Fatalf("finish = %+v", finish)
	}
	if err := taglib.WriteImage(flac, testPicture(t, "jpeg", 70)); err != nil {
		t.Fatal(err)
	}
	plan, err := PlanUndo(s, finish.RunID, nil)
	if err != nil || plan.Pictures != 1 || len(plan.Skipped) != 1 || plan.Skipped[0].Path != flac {
		t.Errorf("with one picture changed since, the plan = %+v, %v", plan, err)
	}

	refused := CoverArt(context.Background(), Options{Destination: lib, Store: s}, CoverOptions{Embed: true})
	if refused.State != StateNotStarted {
		t.Errorf("without a way to write pictures: %+v", refused)
	}
}
