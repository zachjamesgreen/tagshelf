package tui

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/zachjamesgreen/tagshelf/internal/engine"
	"github.com/zachjamesgreen/tagshelf/internal/fixtures"
	"github.com/zachjamesgreen/tagshelf/internal/marker"
	"github.com/zachjamesgreen/tagshelf/internal/store"
	"github.com/zachjamesgreen/tagshelf/internal/tag"
)

// undoSource is the run history with a plan for undoing run 5: most of it
// can be, and two files are left as they are.
func undoSource() *fakeSource {
	src := historySource()
	src.plans = map[int64]engine.UndoPlan{
		5: {Run: src.snap.Runs[2], Moves: 46, TagSaves: 3, Cues: 1, Skipped: []engine.UndoSkip{
			{Path: `D:\Music\Singer\Album (2001)\01 - One.flac`, Reason: "it has changed since it was indexed"},
			{Path: `D:\Music\_Untagged\loose.mp3`, Reason: `something else is at its old path now (E:\Incoming\loose.mp3)`, Later: true},
		}},
		4: {Run: src.snap.Runs[3], Refusal: "a dry run changed nothing"},
	}
	return src
}

// undoOn opens the undo preflight of the run on the given line of the Runs
// tab.
func undoOn(t *testing.T, line int) (*Model, *fakeRunner, *fakeSource) {
	t.Helper()
	src := undoSource()
	f := newFakeRunner()
	m := newModel(t, src, 120, 32)
	m.runner = f
	hit(t, m, "5")
	for range line {
		hit(t, m, "down")
	}
	hit(t, m, "u")
	if m.undo == nil || m.undo.plan == nil {
		t.Fatalf("the undo preflight did not open:\n%s", screen(m))
	}
	return m, f, src
}

func TestUndoPreflightShowsWhatWouldBeDone(t *testing.T) {
	m, f, src := undoOn(t, 2)
	if !reflect.DeepEqual(src.planned, []int64{5}) {
		t.Fatalf("planned the undo of %v, want run 5", src.planned)
	}
	got := screen(m)
	for _, want := range []string{
		"Runs › Undo › run 5", "Undo run 5: organize of", `E:\Incoming -> D:\Music`,
		"Can be undone: 46 files move back, 3 files get their earlier tags back, 1 cue sheet gets its original back",
		"No file is overwritten", "Left as they are (2):",
		filepath.Join("Singer", "Album (2001)", "01 - One.flac"), "it has changed since it was indexed",
		"something else is at its old path now", "enter undo it",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the preflight lacks %q:\n%s", want, got)
		}
	}
	// Nothing happens until it is confirmed.
	if len(f.undone) != 0 {
		t.Fatalf("undone before confirming: %v", f.undone)
	}

	f.undoFinish = engine.RunFinished{RunID: 8, State: engine.StateErrors, Counters: engine.Counters{OtherMoved: 46, CueRewritten: 1, Errors: 2}}
	loads := src.loads
	hit(t, m, "enter")
	if !reflect.DeepEqual(f.undone, []int64{5}) || m.undo != nil {
		t.Fatalf("undone %v, preflight open %t", f.undone, m.undo != nil)
	}
	for _, want := range []string{"Run 5 undone: 46 files moved back", "1 cue sheet restored", "2 skipped or failed: see 5 Runs"} {
		if !strings.Contains(m.toast, want) {
			t.Errorf("toast %q lacks %q", m.toast, want)
		}
	}
	if src.loads == loads {
		t.Error("the library was not reloaded after the undo")
	}
}

func TestUndoPreflightRefusalsAndEsc(t *testing.T) {
	// A run that cannot be undone says why, and enter does nothing.
	m, f, _ := undoOn(t, 3)
	if got := screen(m); !strings.Contains(got, "This run cannot be undone: a dry run changed nothing") {
		t.Fatalf("the refusal:\n%s", got)
	}
	hit(t, m, "enter", "y")
	if len(f.undone) != 0 || m.undo == nil {
		t.Fatalf("a run that cannot be undone: undone %v, preflight open %t", f.undone, m.undo != nil)
	}
	hit(t, m, "esc")
	if m.undo != nil || !strings.Contains(screen(m), "Runs (7)") {
		t.Fatalf("esc did not go back to the runs:\n%s", screen(m))
	}

	// u works from a run's detail too.
	hit(t, m, "up", "enter", "u")
	if m.undo == nil || m.undo.run.ID != 5 {
		t.Fatalf("u on a run's detail: %+v", m.undo)
	}
	// While another process holds the lock, or in a dry run, nothing is undone.
	m.header.DryRun = true
	hit(t, m, "enter")
	if len(f.undone) != 0 || m.undo == nil || !strings.Contains(m.toast, "Dry run") {
		t.Errorf("an undo in a dry run: undone %v, toast %q", f.undone, m.toast)
	}
	m.header.DryRun = false
	f.err = &store.LockedError{Path: `D:\db.lock`, Holder: store.LockHolder{PID: 4242, Started: started}}
	hit(t, m, "enter")
	if m.undo == nil || m.undo.working || !strings.Contains(m.toast, "4242") {
		t.Errorf("an undo that could not start: preflight open %t, toast %q", m.undo != nil, m.toast)
	}
}

// After an action the toast offers to undo it, until the next key.
func TestToastOffersToUndoTheLastAction(t *testing.T) {
	src := untaggedSource()
	src.plans = map[int64]engine.UndoPlan{42: {Run: store.Run{ID: 42, Kind: store.KindTUIAction, Started: started}, Moves: 1, TagSaves: 1}}
	m, f := newUntaggedModel(t, src)
	f.saveEvents = []engine.Event{engine.FilePlaced{Destination: `D:\Music\A\B\01 - C.mp3`, Outcome: engine.Organized}}
	f.saveFinish = engine.RunFinished{RunID: 42, State: engine.StateOK, Counters: engine.Counters{Organized: 1}}
	hit(t, m, "a")
	if !strings.Contains(m.toast, "Moved to") || !strings.HasSuffix(m.toast, "(u undo)") {
		t.Fatalf("toast = %q, want it to offer the undo", m.toast)
	}
	hit(t, m, "u")
	if m.undo == nil || !reflect.DeepEqual(src.planned, []int64{42}) {
		t.Fatalf("u did not open the undo of the action: planned %v", src.planned)
	}
	if got := screen(m); !strings.Contains(got, "Untagged › Undo › run 42") || !strings.Contains(got, "1 file moves back, 1 file gets its earlier tags back") {
		t.Fatalf("the preflight of the action:\n%s", got)
	}
	f.undoFinish = engine.RunFinished{RunID: 43, State: engine.StateOK, Counters: engine.Counters{OtherMoved: 1}}
	hit(t, m, "enter")
	// The undo is an action too.
	if !reflect.DeepEqual(f.undone, []int64{42}) || !strings.Contains(m.toast, "Run 42 undone: 1 file moved back") || !strings.HasSuffix(m.toast, "(u undo)") {
		t.Fatalf("undone %v, toast %q", f.undone, m.toast)
	}

	// Any other key ends the offer.
	hit(t, m, "down", "u")
	if m.undo != nil || len(src.planned) != 1 {
		t.Errorf("u after another key opened an undo: planned %v", src.planned)
	}
	// An action that failed outright offers nothing.
	f.saveFinish = engine.RunFinished{RunID: 44, State: engine.StateErrors, Counters: engine.Counters{Errors: 1}}
	f.saveEvents = nil
	hit(t, m, "a")
	if strings.Contains(m.toast, "(u undo)") {
		t.Errorf("toast %q offers to undo a run that did nothing", m.toast)
	}
}

func TestGoldenUndo(t *testing.T) {
	m, _, _ := undoOn(t, 2)
	got := screen(m)
	checkFits(t, got, 120, 32)
	golden(t, filepath.Join("testdata", "undo.golden"), got)
}

// Accepting a rip's tags and undoing it, with real files and a real
// database: the file goes back without its marker, and undoing the undo
// puts it in the library again with it.
func TestUndoOfAcceptedTagsForReal(t *testing.T) {
	dir := t.TempDir()
	lib := filepath.Join(dir, "lib")
	ripped := filepath.Join(lib, "_Untagged", "ripped.flac")
	data, err := os.ReadFile(fixtures.Path(fixtures.RipperFLAC))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(ripped), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(ripped, data, 0644); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(filepath.Join(dir, "tagshelf.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	opts := engine.Options{Destination: lib, Store: st}
	index := opts
	index.IndexOnly = true
	if finish := engine.Run(context.Background(), index); finish.State != engine.StateOK {
		t.Fatalf("index = %+v", finish)
	}
	untagged, err := st.UntaggedFiles()
	if err != nil || len(untagged) != 1 {
		t.Fatalf("untagged = %+v, %v", untagged, err)
	}

	var dst string
	sink := func(e engine.Event) {
		if p, ok := e.(engine.FilePlaced); ok && p.Outcome == engine.Organized {
			dst = p.Destination
		}
	}
	accept := opts
	accept.Sink = sink
	accepted := engine.SaveTags(context.Background(), accept, []int64{untagged[0].ID}, acceptSave(started))
	if accepted.State != engine.StateOK || accepted.RunID == 0 || dst == "" {
		t.Fatalf("accept = %+v, moved to %q", accepted, dst)
	}
	marked := func(path string) bool {
		t.Helper()
		f, err := os.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		md, err := tag.ReadFrom(f)
		if err != nil {
			t.Fatal(err)
		}
		_, ok := marker.Read(md)
		return ok
	}
	if !marked(dst) {
		t.Fatal("the accepted file has no marker")
	}

	src := librarySource{st: st}
	plan, err := src.UndoPlan(accepted.RunID)
	if err != nil || plan.Moves != 1 || plan.TagSaves != 1 || len(plan.Skipped) != 0 {
		t.Fatalf("plan = %+v, %v; want a move and a tag save", plan, err)
	}
	undone := engine.Undo(context.Background(), opts, accepted.RunID, undoCheck, undoRestore)
	if undone.State != engine.StateOK {
		t.Fatalf("undo = %+v", undone)
	}
	if _, err := os.Stat(dst); !os.IsNotExist(err) {
		t.Errorf("the file is still in the library (%v)", err)
	}
	if marked(ripped) {
		t.Error("the rip still has the marker after the undo")
	}
	if left, err := st.UntaggedFiles(); err != nil || len(left) != 1 || left[0].Manual != "" {
		t.Errorf("untagged after the undo = %+v, %v; want the rip, unmarked", left, err)
	}

	// The undo undone: accepted again.
	if plan, err = src.UndoPlan(undone.RunID); err != nil || plan.Moves != 1 || plan.TagSaves != 1 || len(plan.Skipped) != 0 {
		t.Fatalf("the plan to undo the undo = %+v, %v", plan, err)
	}
	if redone := engine.Undo(context.Background(), opts, undone.RunID, undoCheck, undoRestore); redone.State != engine.StateOK {
		t.Fatalf("undoing the undo = %+v", redone)
	}
	if !marked(dst) {
		t.Error("the file is not back in the library with its marker")
	}
	// The first run stays undone, and cannot be undone again.
	if plan, err = src.UndoPlan(accepted.RunID); err != nil || plan.Refusal == "" {
		t.Errorf("the plan to undo the first run again = %+v, %v", plan, err)
	}
}
