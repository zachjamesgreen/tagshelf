package engine

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"tagshelf/internal/fixtures"
	"tagshelf/internal/store"
)

// noTags is the tag check and restore of runs that saved no tags.
func noTags(t *testing.T) (TagCheck, TagRestore) {
	return func(path, after string) error {
			t.Errorf("tag check of %s", path)
			return nil
		}, func(_ *store.Store, _ int64, path, _, _ string) error {
			t.Errorf("tag restore of %s", path)
			return nil
		}
}

// lastRun returns the latest run recorded in s.
func lastRun(t *testing.T, s *store.Store) store.Run {
	t.Helper()
	runs, err := s.Runs()
	if err != nil || len(runs) == 0 {
		t.Fatalf("runs = %+v, %v", runs, err)
	}
	return runs[len(runs)-1]
}

// organizedRip is a source folder organized into a library: a tagged song
// with artwork and a cue sheet that the run rewrote, and a song without tags.
func organizedRip(t *testing.T) (src, lib string, s *store.Store, run store.Run) {
	t.Helper()
	tmp := t.TempDir()
	src, lib = filepath.Join(tmp, "src"), filepath.Join(tmp, "lib")
	writeTaggedMP3(t, filepath.Join(src, "rip", "Song.mp3"), id3Tags{albumArtist: "Singer", album: "Album", title: "Song", track: "1"}, theAudio)
	writeTestFile(t, filepath.Join(src, "rip", "cover.jpg"), "art")
	writeTestFile(t, filepath.Join(src, "rip", "album.cue"), cueOf("Song.mp3"))
	writeTestFile(t, filepath.Join(src, "loose.mp3"), "no tags here")
	s = openStore(t)
	if finish := Run(context.Background(), Options{Source: src, Destination: lib, Workers: 1, Store: s}); finish.State != StateOK {
		t.Fatalf("organize = %+v", finish)
	}
	return src, lib, s, lastRun(t, s)
}

func cueOf(name string) string {
	return "FILE \"" + name + "\" MP3\r\n  TRACK 01 AUDIO\r\n    INDEX 01 00:00:00\r\n"
}

func TestUndoPutsAnOrganizeRunBack(t *testing.T) {
	src, lib, s, run := organizedRip(t)
	album := filepath.Join(lib, "Singer", "Album")
	mustExist(t, filepath.Join(album, "01 - Song.mp3"), filepath.Join(album, "album.cue.orig"), filepath.Join(lib, "_Untagged", "loose.mp3"))
	if got := readTestFile(t, filepath.Join(album, "album.cue")); got != cueOf("01 - Song.mp3") {
		t.Fatalf("the organize run did not rewrite the cue sheet: %q", got)
	}

	check, restore := noTags(t)
	plan, err := PlanUndo(s, run.ID, check)
	if err != nil || plan.Refusal != "" || plan.Moves != 4 || plan.Cues != 1 || plan.TagSaves != 0 || len(plan.Skipped) != 0 {
		t.Fatalf("plan = %+v, %v; want 4 moves and a cue sheet, nothing skipped", plan, err)
	}
	// Planning changes nothing.
	mustExist(t, filepath.Join(album, "01 - Song.mp3"), filepath.Join(album, "album.cue.orig"))

	var c collector
	finish := Undo(context.Background(), Options{Destination: lib, Store: s, Sink: c.sink}, run.ID, check, restore)
	if finish.State != StateOK || finish.Counters.OtherMoved != 4 || finish.Counters.CueRewritten != 1 || finish.Counters.InPlace != 0 {
		t.Fatalf("undo = %+v, want 4 files moved back and a cue sheet restored", finish)
	}
	mustExist(t, filepath.Join(src, "rip", "Song.mp3"), filepath.Join(src, "rip", "cover.jpg"), filepath.Join(src, "loose.mp3"))
	if got := readTestFile(t, filepath.Join(src, "rip", "album.cue")); got != cueOf("Song.mp3") {
		t.Errorf("the cue sheet after the undo = %q, want the original", got)
	}
	mustNotExist(t, filepath.Join(src, "rip", "album.cue.orig"), filepath.Join(lib, "Singer"), filepath.Join(lib, "_Untagged", "loose.mp3"))

	undo := lastRun(t, s)
	if undo.Kind != store.KindUndo || undo.State != store.StateOK {
		t.Errorf("the undo's run = %+v", undo)
	}
	if was, err := s.Run(run.ID); err != nil || was.State != store.StateUndone || was.UndoneBy != undo.ID {
		t.Errorf("the undone run = %+v, %v; want it undone by run %d", was, err, undo.ID)
	}
	if first, ok := c.events[0].(RunStarted); !ok || first.Kind != KindUndo {
		t.Errorf("first event = %+v, want an undo's RunStarted", c.events[0])
	}
	// The index followed the two songs back to the source.
	rows := indexedFiles(t, s)
	for _, song := range []string{filepath.Join(src, "rip", "Song.mp3"), filepath.Join(src, "loose.mp3")} {
		if row, ok := rows[song]; !ok || row.RootKind != store.RootSource {
			t.Errorf("row of %s after the undo = %+v, %t; want it under the source root", song, row, ok)
		}
	}
	if len(rows) != 2 {
		t.Errorf("index after the undo = %v, want the two songs and no row left in the library", rows)
	}
	// The library itself and its untagged folder are not cleaned away.
	mustExist(t, lib, filepath.Join(lib, "_Untagged"))

	// It is not undone twice.
	again := Undo(context.Background(), Options{Destination: lib, Store: s}, run.ID, check, restore)
	if again.State != StateNotStarted || again.Err == nil || !strings.Contains(again.Err.Error(), "undone already") {
		t.Errorf("a second undo = %+v", again)
	}
	if plan, err := PlanUndo(s, run.ID, check); err != nil || plan.Refusal == "" || !plan.Nothing() {
		t.Errorf("the plan of a second undo = %+v, %v", plan, err)
	}
}

// What is not as the run left it stays as it is, and is listed.
func TestUndoSkipsWhatChangedSince(t *testing.T) {
	src, lib, s, run := organizedRip(t)
	song := filepath.Join(lib, "Singer", "Album", "01 - Song.mp3")
	// The song was edited since, and something new is where the loose song
	// came from.
	edited := readTestFile(t, song) + "more"
	writeTestFile(t, song, edited)
	writeTestFile(t, filepath.Join(src, "loose.mp3"), "another file")

	check, restore := noTags(t)
	plan, err := PlanUndo(s, run.ID, check)
	if err != nil || plan.Moves != 2 || plan.Cues != 1 || len(plan.Skipped) != 2 {
		t.Fatalf("plan = %+v, %v; want 2 moves, a cue sheet and 2 skipped", plan, err)
	}
	reasons := map[string]string{}
	for _, skip := range plan.Skipped {
		reasons[skip.Path] = skip.Reason
	}
	if !strings.Contains(reasons[song], "changed since") || !strings.Contains(reasons[filepath.Join(lib, "_Untagged", "loose.mp3")], "old path") {
		t.Errorf("skipped = %+v", plan.Skipped)
	}

	finish := Undo(context.Background(), Options{Destination: lib, Store: s}, run.ID, check, restore)
	if finish.State != StateErrors || finish.Counters.Errors != 2 || finish.Counters.OtherMoved != 2 {
		t.Fatalf("undo = %+v, want 2 moved back and 2 skipped", finish)
	}
	// Nothing was overwritten or lost.
	if got := readTestFile(t, song); got != edited {
		t.Errorf("the edited song was touched")
	}
	if got := readTestFile(t, filepath.Join(src, "loose.mp3")); got != "another file" {
		t.Errorf("the file at the old path was overwritten: %q", got)
	}
	mustExist(t, filepath.Join(lib, "_Untagged", "loose.mp3"), filepath.Join(src, "rip", "cover.jpg"), filepath.Join(src, "rip", "album.cue"))
	errs, err := s.RunErrors(lastRun(t, s).ID)
	if err != nil || len(errs) != 2 || !strings.Contains(errs[0].Message, "skipped: ") || !strings.Contains(errs[1].Message, "skipped: ") {
		t.Errorf("the undo's errors = %+v, %v; want the two skipped files", errs, err)
	}
	// Both skips can pass (an index run, the old path cleared), so the run
	// waits to be undone again.
	if was, err := s.Run(run.ID); err != nil || was.State == store.StateUndone {
		t.Errorf("the run after an undo with steps that can still be undone = %+v, %v", was, err)
	}
	if !strings.Contains(finish.NotUndone, "2 of its steps were skipped for a reason that can pass") {
		t.Errorf("NotUndone = %q", finish.NotUndone)
	}
}

// A file a later run moved is not the undo's to move.
func TestUndoSkipsWhatALaterRunMoved(t *testing.T) {
	_, lib, s, run := organizedRip(t)
	// The loose song gets tags and a reorganize files it in the library.
	loose := filepath.Join(lib, "_Untagged", "loose.mp3")
	writeTaggedMP3(t, loose, id3Tags{albumArtist: "Other", album: "LP", title: "Loose", track: "2"}, "other audio")
	if finish := Run(context.Background(), Options{Source: lib, Destination: lib, Workers: 1, Store: s}); finish.State != StateOK || finish.Counters.Organized != 1 {
		t.Fatalf("reorganize = %+v", finish)
	}
	// And something else lands where it was.
	writeTestFile(t, loose, "a newcomer")

	check, _ := noTags(t)
	plan, err := PlanUndo(s, run.ID, check)
	if err != nil || len(plan.Skipped) != 1 || plan.Skipped[0].Path != loose || !strings.Contains(plan.Skipped[0].Reason, "later run") {
		t.Fatalf("plan = %+v, %v; want the loose song skipped as moved by a later run", plan, err)
	}
}

// taggedRip is an indexed library with a rip in _Untagged that a tag save,
// recorded as a tag write, turned into a library file with its artwork.
func taggedRip(t *testing.T) (lib, ripped, dst string, s *store.Store, check TagCheck, restore TagRestore) {
	t.Helper()
	lib, ripped, _, _, s = untaggedLibrary(t)
	save, check, restore := recordedSaves(t)
	finish := SaveTags(context.Background(), Options{Destination: lib, Store: s}, []int64{indexedFiles(t, s)[ripped].ID}, save)
	if finish.State != StateOK || finish.Counters.Organized != 1 {
		t.Fatalf("save tags = %+v", finish)
	}
	return lib, ripped, manualLibraryPath(lib, ".flac"), s, check, restore
}

func TestUndoOfATagSaveRestoresTagsAndPaths(t *testing.T) {
	lib, ripped, dst, s, check, restore := taggedRip(t)
	saved := lastRun(t, s)
	art := filepath.Join(filepath.Dir(ripped), "cover.jpg")
	mustExist(t, dst, filepath.Join(filepath.Dir(dst), "cover.jpg"))

	plan, err := PlanUndo(s, saved.ID, check)
	if err != nil || plan.Moves != 2 || plan.TagSaves != 1 || len(plan.Skipped) != 0 {
		t.Fatalf("plan = %+v, %v; want 2 moves and a tag save", plan, err)
	}
	finish := Undo(context.Background(), Options{Destination: lib, Store: s}, saved.ID, check, restore)
	if finish.State != StateOK {
		t.Fatalf("undo = %+v", finish)
	}
	mustExist(t, ripped, art)
	mustNotExist(t, dst, filepath.Dir(dst))
	if got := readTestFile(t, ripped); got != readTestFile(t, fixtures.Path(fixtures.RipperFLAC)) {
		t.Error("the rip does not have its old tags back")
	}
	row := indexedFiles(t, s)[ripped]
	if row.ID == 0 || row.RootKind != store.RootUntagged || row.Manual != "" {
		t.Errorf("the rip's index row after the undo = %+v, want it untagged and unmarked", row)
	}
	undo := lastRun(t, s)
	if writes, err := s.TagWrites(undo.ID); err != nil || len(writes) != 1 || writes[0].Path != ripped {
		t.Errorf("the undo's tag writes = %+v, %v", writes, err)
	}

	// The undo can itself be undone: the save is made again.
	plan, err = PlanUndo(s, undo.ID, check)
	if err != nil || plan.Moves != 2 || plan.TagSaves != 1 || len(plan.Skipped) != 0 {
		t.Fatalf("the plan to undo the undo = %+v, %v", plan, err)
	}
	finish = Undo(context.Background(), Options{Destination: lib, Store: s}, undo.ID, check, restore)
	if finish.State != StateOK {
		t.Fatalf("undoing the undo = %+v", finish)
	}
	mustExist(t, dst, filepath.Join(filepath.Dir(dst), "cover.jpg"))
	mustNotExist(t, ripped, art)
	if got := readTestFile(t, dst); got != readTestFile(t, fixtures.Manual(".flac")) {
		t.Error("the file does not have the saved tags again")
	}
	if row := indexedFiles(t, s)[dst]; row.RootKind != store.RootLibrary || row.Manual == "" {
		t.Errorf("the file's index row after the redo = %+v", row)
	}
}

// Tags edited since the save are not written over.
func TestUndoSkipsTagsEditedSince(t *testing.T) {
	lib, ripped, dst, s, check, restore := taggedRip(t)
	saved := lastRun(t, s)
	// Edited in place, and indexed again, so only the tags tell.
	writeTestFile(t, dst, readTestFile(t, fixtures.Path(fixtures.ManualNoAlbumFLAC)))
	indexLibrary(t, s, lib)

	plan, err := PlanUndo(s, saved.ID, check)
	if err != nil || plan.TagSaves != 0 || len(plan.Skipped) == 0 {
		t.Fatalf("plan = %+v, %v; want the tag save skipped", plan, err)
	}
	finish := Undo(context.Background(), Options{Destination: lib, Store: s}, saved.ID, check, restore)
	if finish.State != StateErrors {
		t.Fatalf("undo = %+v, want it to end with the skip as an error", finish)
	}
	// The file went back, with the tags it has now.
	if got := readTestFile(t, ripped); got != readTestFile(t, fixtures.Path(fixtures.ManualNoAlbumFLAC)) {
		t.Error("the edited tags were written over")
	}
}

func TestUndoOfADuplicateResolutionReopensTheGroup(t *testing.T) {
	lib, kept, copyPath, art, s := reviewLibrary(t)
	g := onlyGroup(t, s)
	finish := ResolveDuplicates(context.Background(), Options{Destination: lib, Store: s},
		[]Resolution{{Kind: g.Kind, Members: g.IDs(), Keeper: indexedFiles(t, s)[kept].ID}})
	if finish.State != StateOK {
		t.Fatalf("resolve = %+v", finish)
	}
	check, restore := noTags(t)
	if finish := Undo(context.Background(), Options{Destination: lib, Store: s}, lastRun(t, s).ID, check, restore); finish.State != StateOK {
		t.Fatalf("undo = %+v", finish)
	}
	mustExist(t, kept, copyPath, art)
	mustNotExist(t, filepath.Join(lib, "_Duplicates", "Singer"))
	if rs := reviews(t, s); len(rs) != 0 {
		t.Errorf("reviews after the undo = %+v, want the decision withdrawn", rs)
	}
	if n, err := s.GroupsNeedingReview(PossibleKey); err != nil || n != 1 {
		t.Errorf("groups needing review = %d, %v; want the group back", n, err)
	}
}

func TestUndoRefusals(t *testing.T) {
	_, lib, s, run := organizedRip(t)
	check, restore := noTags(t)
	if finish := Undo(context.Background(), Options{Destination: lib}, run.ID, check, restore); finish.State != StateNotStarted {
		t.Errorf("without a database: %+v", finish)
	}
	if finish := Undo(context.Background(), Options{Destination: lib, Store: s, DryRun: true}, run.ID, check, restore); finish.State != StateNotStarted {
		t.Errorf("in a dry run: %+v", finish)
	}
	if finish := Undo(context.Background(), Options{Destination: lib, Store: s}, 9999, check, restore); finish.State != StateNotStarted {
		t.Errorf("of a run that is not there: %+v", finish)
	}
	// An index run moved nothing.
	indexLibrary(t, s, lib)
	if plan, err := PlanUndo(s, lastRun(t, s).ID, check); err != nil || plan.Refusal == "" {
		t.Errorf("the plan to undo an index run = %+v, %v", plan, err)
	}
	// A cancelled undo leaves the run to be undone again.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if finish := Undo(ctx, Options{Destination: lib, Store: s}, run.ID, check, restore); finish.State != StateInterrupted {
		t.Errorf("cancelled: %+v", finish)
	}
	if was, err := s.Run(run.ID); err != nil || was.State == store.StateUndone {
		t.Errorf("a cancelled undo marked the run undone: %+v, %v", was, err)
	}
	mustExist(t, filepath.Join(lib, "Singer", "Album", "01 - Song.mp3"))
}

// Runs are undone last first: a file a later run moved on, and that run's
// undo brought back, is where the earlier run left it.
func TestUndoInLastInFirstOutOrder(t *testing.T) {
	src, lib, s, organize := organizedRip(t)
	// The loose song gets tags and a reorganize files it in the library.
	loose := filepath.Join(lib, "_Untagged", "loose.mp3")
	writeTaggedMP3(t, loose, id3Tags{albumArtist: "Other", album: "LP", title: "Loose", track: "2"}, "other audio")
	tagged := readTestFile(t, loose)
	if finish := Run(context.Background(), Options{Source: lib, Destination: lib, Workers: 1, Store: s}); finish.State != StateOK || finish.Counters.Organized != 1 {
		t.Fatalf("reorganize = %+v", finish)
	}
	reorganize := lastRun(t, s)
	check, restore := noTags(t)
	if finish := Undo(context.Background(), Options{Destination: lib, Store: s}, reorganize.ID, check, restore); finish.State != StateOK {
		t.Fatalf("undoing the reorganize = %+v", finish)
	}
	mustExist(t, loose)

	plan, err := PlanUndo(s, organize.ID, check)
	if err != nil || plan.Moves != 4 || len(plan.Skipped) != 0 {
		t.Fatalf("the plan to undo the organize = %+v, %v; want every file back", plan, err)
	}
	if finish := Undo(context.Background(), Options{Destination: lib, Store: s}, organize.ID, check, restore); finish.State != StateOK {
		t.Fatalf("undoing the organize = %+v", finish)
	}
	if got := readTestFile(t, filepath.Join(src, "loose.mp3")); got != tagged {
		t.Error("the loose song is not back in the source")
	}
}

// A step that fails where it should have worked leaves the run to be undone
// again, and a cue sheet edited since is not replaced: it goes back as
// edited, with the original the run kept beside it.
func TestUndoThatFailsCanBeTriedAgain(t *testing.T) {
	src, lib, s, run := organizedRip(t)
	cue := filepath.Join(lib, "Singer", "Album", "album.cue")
	edited := cueOf("01 - Song.mp3") + "REM my note\r\n"
	writeTestFile(t, cue, edited)
	// The folder the rip came from cannot be made again: a file is in its
	// place.
	if err := os.RemoveAll(filepath.Join(src, "rip")); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(src, "rip"), "in the way")

	check, restore := noTags(t)
	finish := Undo(context.Background(), Options{Destination: lib, Store: s}, run.ID, check, restore)
	if finish.State != StateErrors || finish.Counters.OtherMoved != 1 {
		t.Fatalf("undo = %+v, want only the loose song back", finish)
	}
	if was, err := s.Run(run.ID); err != nil || was.State == store.StateUndone {
		t.Fatalf("a run whose undo failed in part is marked undone: %+v, %v", was, err)
	}
	if !strings.Contains(finish.NotUndone, "steps failed") {
		t.Errorf("NotUndone = %q, want it to say that steps failed", finish.NotUndone)
	}
	if got := readTestFile(t, cue); got != edited {
		t.Errorf("the edited cue sheet was replaced: %q", got)
	}

	// With the way clear the rest goes back, the edited cue sheet as it is.
	if err := os.Remove(filepath.Join(src, "rip")); err != nil {
		t.Fatal(err)
	}
	finish = Undo(context.Background(), Options{Destination: lib, Store: s}, run.ID, check, restore)
	if finish.Counters.OtherMoved != 4 || finish.NotUndone != "" {
		t.Fatalf("the second undo = %+v, want the rip's three files and the cue sheet's original back", finish)
	}
	if was, err := s.Run(run.ID); err != nil || was.State != store.StateUndone {
		t.Errorf("the run after its second undo = %+v, %v; want it undone", was, err)
	}
	if got := readTestFile(t, filepath.Join(src, "rip", "album.cue")); got != edited {
		t.Errorf("the cue sheet back in the source = %q, want it as edited", got)
	}
	mustExist(t, filepath.Join(src, "rip", "Song.mp3"), filepath.Join(src, "rip", "cover.jpg"))
	// Finding 60: the original used to stay behind in the library, alone in
	// a folder that could then never be removed.
	if got := readTestFile(t, filepath.Join(src, "rip", "album.cue.orig")); got != cueOf("Song.mp3") {
		t.Errorf("the original beside the cue sheet = %q, want the sheet as the rip had it", got)
	}
	mustNotExist(t, filepath.Join(lib, "Singer"))
	errs, err := s.RunErrors(lastRun(t, s).ID)
	if err != nil || !slices.ContainsFunc(errs, func(e store.RunError) bool {
		return strings.HasSuffix(e.Path, "album.cue") && strings.Contains(e.Message, "edited") && strings.Contains(e.Message, "renames back")
	}) {
		t.Errorf("the undo's errors = %+v, %v; want the cue sheet skipped as edited and naming renamed files", errs, err)
	}
}
