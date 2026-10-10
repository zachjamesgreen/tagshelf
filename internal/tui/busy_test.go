package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"tagshelf/internal/engine"
	"tagshelf/internal/tagwrite"
)

// While an action is being carried out nothing else that changes the
// library starts, from any tab.
func TestNothingElseStartsWhileAnActionIsCarriedOut(t *testing.T) {
	src := everySource()
	src.plans = undoSource().plans
	m, f := newUntaggedModel(t, src)
	f.gate = newGate()
	accept := start(t, m, "a")
	f.gate.reached(t)

	refused := func(what string) {
		t.Helper()
		if m.toast != stillBusy || m.toastOK {
			t.Errorf("%s while an action is carried out: toast %q (ok %t), want %q", what, m.toast, m.toastOK, stillBusy)
		}
		if len(f.saved) != 1 || len(f.resolved)+len(f.keptBoth)+len(f.undone) != 0 || m.run != nil {
			t.Fatalf("%s while an action is carried out started something: saved %v, resolved %v, kept both %v, undone %v, run %v",
				what, f.saved, f.resolved, f.keptBoth, f.undone, m.run != nil)
		}
		m.toast = ""
	}
	press(t, m, "a")
	refused("a second accept")
	press(t, m, "1")
	for _, k := range []string{"o", "r", "i"} {
		press(t, m, k)
		refused(k + " on Home")
	}
	press(t, m, "3")
	for _, k := range []string{"enter", "b", "A"} {
		press(t, m, k)
		refused(k + " on Duplicates")
	}
	hit(t, m, "5", "down", "down", "u")
	if m.undo == nil || m.undo.plan == nil {
		t.Fatalf("the undo preflight did not open:\n%s", screen(m))
	}
	hit(t, m, "enter")
	refused("an undo")
	if m.undo == nil || m.undo.working {
		t.Errorf("the refused undo left the preflight closed or working")
	}
	hit(t, m, "esc", "2", "e")
	if m.edit == nil || m.edit.form == nil {
		t.Fatalf("the editor did not open:\n%s", screen(m))
	}
	typeText(t, m, "x")
	hit(t, m, "ctrl+s")
	refused("a save from the editor")
	hit(t, m, "esc", "esc")

	f.saveFinish = engine.RunFinished{State: engine.StateOK, Counters: engine.Counters{LeftInPlace: 1}}
	f.gate.release(t)
	accept.answer(t, m)
	if m.busy != nil || !strings.Contains(m.toast, "stays in _Untagged") {
		t.Errorf("after the action ended: busy %t, toast %q", m.busy != nil, m.toast)
	}
	// And then they start again.
	f.gate = nil
	press(t, m, "a")
	if len(f.saved) != 2 {
		t.Errorf("an accept after the action ended saved %v", f.saved)
	}
}

// While a run is going, nothing a tab does changes the library.
func TestNothingChangesTheLibraryWhileARunIsGoing(t *testing.T) {
	src := everySource()
	m, f := newUntaggedModel(t, src)
	press(t, m, "1")
	press(t, m, "o")
	waitStart(t, f)
	const want = "A run is going; wait for it to end"
	press(t, m, "2")
	press(t, m, "a")
	if m.toast != want || len(f.saved) != 0 {
		t.Errorf("accept during a run: toast %q, saved %v", m.toast, f.saved)
	}
	press(t, m, "3")
	for _, k := range []string{"enter", "b", "A"} {
		m.toast = ""
		press(t, m, k)
		if m.toast != want || len(f.resolved)+len(f.keptBoth) != 0 || m.dups.confirmAll != nil {
			t.Errorf("%s on Duplicates during a run: toast %q, resolved %v, kept both %v", k, m.toast, f.resolved, f.keptBoth)
		}
	}
	f.end(t, engine.RunFinished{})
	runEnded(t, m)
}

// q asks before it stops an action part way, as it does for a run.
func TestQuitDuringAnActionAsksFirst(t *testing.T) {
	m, f := newUntaggedModel(t, untaggedSource())
	f.gate = newGate()
	accept := start(t, m, "a")
	f.gate.reached(t)

	if msg := press(t, m, "q"); msg == (tea.QuitMsg{}) {
		t.Fatal("q quit without asking while an action was carried out")
	}
	if got := screen(m); !strings.Contains(got, "Stop what is being done and quit?") {
		t.Fatalf("no question:\n%s", got)
	}
	press(t, m, "n")
	if strings.Contains(screen(m), "quit?") || m.quitting || f.stopped.Load() {
		t.Fatal("n did not let the action go on")
	}

	press(t, m, "q")
	if msg := press(t, m, "y"); msg == (tea.QuitMsg{}) {
		t.Fatal("quit before the action ended")
	}
	// y stopped it: its end is what comes back, and then the TUI quits.
	if msg := accept.answer(t, m); msg != (tea.QuitMsg{}) {
		t.Errorf("after the action ended the model gave %#v, want it to quit", msg)
	}
	if !f.stopped.Load() || m.busy != nil {
		t.Errorf("the action was stopped = %t, still busy = %t", f.stopped.Load(), m.busy != nil)
	}

	// ctrl+c at the question quits at once, as it does without it.
	m, f = newUntaggedModel(t, untaggedSource())
	f.gate = newGate()
	start(t, m, "a")
	f.gate.reached(t)
	press(t, m, "q")
	if msg := press(t, m, "ctrl+c"); msg != (tea.QuitMsg{}) {
		t.Errorf("ctrl+c at the question gave %#v, want it to quit", msg)
	}
	m.stopAction()

	// With nothing going on q quits without asking.
	m, _ = newUntaggedModel(t, untaggedSource())
	if msg := press(t, m, "q"); msg != (tea.QuitMsg{}) {
		t.Errorf("q with nothing going on gave %#v, want it to quit", msg)
	}
}

// The editor and the undo preflight take no key but ctrl+c while they work.
func TestEditorAndUndoIgnoreKeysWhileTheyWork(t *testing.T) {
	m, f, _ := editorOn(t, 104, tagwrite.Values{tagwrite.Title: {"Live"}})
	typeText(t, m, "!")
	f.gate = newGate()
	saving := start(t, m, "ctrl+s")
	f.gate.reached(t)
	if got := screen(m); !strings.Contains(got, "Saving…") {
		t.Errorf("the editor does not say it is saving:\n%s", got)
	}
	typeText(t, m, "xyz")
	hit(t, m, "esc", "ctrl+s", "enter")
	if m.edit == nil {
		t.Fatal("a key closed the editor while it was saving")
	}
	if m.edit.title != "Live!" || len(f.saved) != 1 || !m.edit.saving {
		t.Errorf("keys during the save: title %q, saved %v, saving %t", m.edit.title, f.saved, m.edit.saving)
	}
	f.gate.release(t)
	saving.answer(t, m)
	if m.edit != nil || !strings.HasPrefix(m.toast, "Saved") {
		t.Errorf("after the save: editor open %t, toast %q", m.edit != nil, m.toast)
	}

	m, f, _ = undoOn(t, 2)
	f.gate = newGate()
	undoing := start(t, m, "enter")
	f.gate.reached(t)
	if got := screen(m); !strings.Contains(got, "Undoing…") || !strings.Contains(got, "undoing…") {
		t.Errorf("the preflight does not say it is undoing:\n%s", got)
	}
	hit(t, m, "esc", "n", "enter", "y", "down")
	if m.undo == nil || !m.undo.working || len(f.undone) != 1 {
		t.Fatalf("keys during the undo: preflight open %t, undone %v", m.undo != nil, f.undone)
	}
	f.undoFinish = engine.RunFinished{RunID: 8, State: engine.StateOK, Counters: engine.Counters{OtherMoved: 46}}
	f.gate.release(t)
	undoing.answer(t, m)
	if m.undo != nil || !strings.Contains(m.toast, "Run 5 undone: 46 files moved back") {
		t.Errorf("after the undo: preflight open %t, toast %q", m.undo != nil, m.toast)
	}
}

// esc stops waiting for MusicBrainz at every step of a lookup: the request
// is dropped and its answer, when it comes, changes nothing.
func TestLookupStopsWaiting(t *testing.T) {
	mb := julie()
	mb.pages[0].Count = 30 // more to fetch
	m, _, _ := lookupOn(t, 104, mb)
	l := m.look

	mb.gate = newGate()
	searching := start(t, m, "enter")
	mb.gate.reached(t)
	if got := screen(m); l.step != stepSearching || !strings.Contains(got, "Searching MusicBrainz…") || !strings.Contains(got, "esc stop waiting") {
		t.Fatalf("while searching: step %d\n%s", l.step, got)
	}
	hit(t, m, "x", "enter") // only esc means anything
	if l.step != stepSearching || len(mb.queries) != 1 {
		t.Errorf("a key while searching: step %d, %d searches", l.step, len(mb.queries))
	}
	hit(t, m, "esc")
	// Stopped, the request returns; nothing comes of its answer.
	searching.answer(t, m)
	if l.step != stepQuery || l.problem != "" || len(l.matches) != 0 {
		t.Fatalf("after stopping the search: step %d, problem %q, %d matches", l.step, l.problem, len(l.matches))
	}

	mb.gate = nil
	hit(t, m, "enter")
	if l.step != stepPick || len(l.matches) != 3 {
		t.Fatalf("the search: step %d, %d matches", l.step, len(l.matches))
	}
	mb.gate = newGate()
	more := start(t, m, "m")
	mb.gate.reached(t)
	if got := screen(m); l.step != stepMore || !strings.Contains(got, "Asking MusicBrainz for more…") {
		t.Fatalf("while asking for more: step %d\n%s", l.step, got)
	}
	hit(t, m, "esc")
	more.answer(t, m)
	if l.step != stepPick || l.problem != "" || len(l.matches) != 3 {
		t.Fatalf("after stopping more: step %d, problem %q, %d matches", l.step, l.problem, len(l.matches))
	}

	toRelease(t, m, "rel-album")
	reading := start(t, m, "enter")
	mb.gate.reached(t)
	if got := screen(m); l.step != stepLoading || !strings.Contains(got, "Reading the release from MusicBrainz…") {
		t.Fatalf("while reading the release: step %d\n%s", l.step, got)
	}
	hit(t, m, "esc")
	reading.answer(t, m)
	if l.step != stepPick || l.problem != "" {
		t.Fatalf("after stopping the read of the release: step %d, problem %q", l.step, l.problem)
	}
}

// An answer from MusicBrainz that comes after its request was stopped or
// overtaken, or after its lookup was closed, is not taken for the answer to
// what was asked since.
func TestLookupDropsLateAnswers(t *testing.T) {
	mb := julie()
	m, _, _ := lookupOn(t, 104, mb)
	l := m.look

	// A search stopped, another typed: the first one's answer comes while
	// the second is awaited.
	_, first := m.Update(keyMsg("enter"))
	hit(t, m, "esc")
	typeText(t, m, " at Home")
	_, second := m.Update(keyMsg("enter"))
	m.Update(first())
	if l.step != stepSearching || len(l.matches) != 0 {
		t.Fatalf("the answer to a stopped search was taken: step %d, %d matches", l.step, len(l.matches))
	}
	m.Update(second())
	if l.step != stepPick || l.query.Title != "Live at Home" || len(l.matches) != 3 {
		t.Fatalf("the second search: step %d, query %+v, %d matches", l.step, l.query, len(l.matches))
	}

	// A release read stopped, another match picked.
	toRelease(t, m, "rel-comp")
	_, stopped := m.Update(keyMsg("enter"))
	hit(t, m, "esc")
	toRelease(t, m, "rel-album")
	other := *mb.release
	other.Title = "Another Pressing"
	late := stopped() // with the release as it was when asked
	mb.release = &other
	_, picked := m.Update(keyMsg("enter"))
	m.Update(late)
	if l.step != stepLoading {
		t.Fatalf("the answer to a stopped read was taken: step %d", l.step)
	}
	m.Update(picked())
	if l.step != stepConfirm || l.tags.Album != "Another Pressing" {
		t.Fatalf("the second pick: step %d, tags %+v", l.step, l.tags)
	}

	// A lookup closed and another opened: the first one's answers are not
	// the second one's.
	hit(t, m, "esc", "esc")
	_, old := m.Update(keyMsg("enter"))
	hit(t, m, "esc", "esc")
	if m.look != nil {
		t.Fatal("the lookup did not close")
	}
	m.Update(old())
	if m.look != nil {
		t.Fatal("a late answer opened a lookup again")
	}
	hit(t, m, "l")
	if m.look == nil || m.look.step != stepQuery {
		t.Fatalf("the second lookup did not open:\n%s", screen(m))
	}
	_, search := m.Update(keyMsg("enter"))
	m.Update(old())
	if m.look.step != stepSearching || len(m.look.matches) != 0 {
		t.Errorf("an answer to the closed lookup was taken by the next: step %d, %d matches", m.look.step, len(m.look.matches))
	}
	m.Update(search())
	if m.look.step != stepPick {
		t.Errorf("the second lookup's own answer: step %d", m.look.step)
	}
}

// toRelease puts the lookup's cursor on the match on the release with the
// given ID.
func toRelease(t *testing.T, m *Model, id string) {
	t.Helper()
	for i, c := range m.look.matches {
		if c.Release != nil && c.Release.ID == id {
			m.look.cursor = i
			return
		}
	}
	t.Fatalf("no match on release %s:\n%s", id, screen(m))
}

// The same for a release match: esc stops waiting, and late answers are
// dropped.
func TestReleaseMatchStopsWaitingAndDropsLateAnswers(t *testing.T) {
	mb := geogaddi()
	m, _ := newUntaggedModel(t, matchSource())
	m.mb = mb
	cursorTo(t, m, 101)
	hit(t, m, "L")
	x := m.match
	if x == nil || x.step != matchQuery {
		t.Fatalf("the release match did not open:\n%s", screen(m))
	}

	mb.gate = newGate()
	searching := start(t, m, "enter")
	mb.gate.reached(t)
	if got := screen(m); x.step != matchSearching || !strings.Contains(got, "Searching MusicBrainz…") {
		t.Fatalf("while searching: step %d\n%s", x.step, got)
	}
	hit(t, m, "x", "enter")
	if x.step != matchSearching || len(mb.releaseQueries) != 1 {
		t.Errorf("a key while searching: step %d, %d searches", x.step, len(mb.releaseQueries))
	}
	hit(t, m, "esc")
	searching.answer(t, m)
	if x.step != matchQuery || x.problem != "" || len(x.releases) != 0 {
		t.Fatalf("after stopping the search: step %d, problem %q, %d releases", x.step, x.problem, len(x.releases))
	}

	mb.gate = nil
	hit(t, m, "enter")
	if x.step != matchPick {
		t.Fatalf("the search: step %d", x.step)
	}
	mb.gate = newGate()
	reading := start(t, m, "enter")
	mb.gate.reached(t)
	if got := screen(m); x.step != matchLoading || !strings.Contains(got, "Reading the release from MusicBrainz…") {
		t.Fatalf("while reading the release: step %d\n%s", x.step, got)
	}
	hit(t, m, "esc")
	reading.answer(t, m)
	if x.step != matchPick || x.problem != "" || x.release != nil {
		t.Fatalf("after stopping the read of the release: step %d, problem %q", x.step, x.problem)
	}
	mb.gate = nil

	// A search stopped and overtaken: its answer is not the second one's.
	hit(t, m, "esc")
	_, first := m.Update(keyMsg("enter"))
	late := first()
	hit(t, m, "esc")
	mb.candidates = mb.candidates[1:]
	_, second := m.Update(keyMsg("enter"))
	m.Update(late)
	if x.step != matchSearching || len(x.releases) != 0 {
		t.Fatalf("the answer to a stopped search was taken: step %d, %d releases", x.step, len(x.releases))
	}
	m.Update(second())
	if x.step != matchPick || len(x.releases) != 1 {
		t.Fatalf("the second search: step %d, %d releases", x.step, len(x.releases))
	}

	// A release read stopped and overtaken.
	_, stopped := m.Update(keyMsg("enter"))
	lateRelease := stopped()
	hit(t, m, "esc")
	_, picked := m.Update(keyMsg("enter"))
	m.Update(lateRelease)
	if x.step != matchLoading || x.release != nil {
		t.Fatalf("the answer to a stopped read was taken: step %d", x.step)
	}
	m.Update(picked())
	if x.step != matchMap {
		t.Fatalf("the second pick: step %d", x.step)
	}

	// A match closed: its answers go nowhere, and not to the next one.
	hit(t, m, "esc", "esc")
	_, old := m.Update(keyMsg("enter"))
	lateSearch := old()
	hit(t, m, "esc", "esc")
	if m.match != nil {
		t.Fatal("the release match did not close")
	}
	m.Update(lateSearch)
	hit(t, m, "L")
	if m.match == nil || m.match.step != matchQuery {
		t.Fatalf("the second release match did not open:\n%s", screen(m))
	}
	_, search := m.Update(keyMsg("enter"))
	m.Update(lateSearch)
	if m.match.step != matchSearching || len(m.match.releases) != 0 {
		t.Errorf("an answer to the closed match was taken by the next: step %d", m.match.step)
	}
	m.Update(search())
	if m.match.step != matchPick {
		t.Errorf("the second match's own answer: step %d", m.match.step)
	}
}

// What was read for a flow since closed is not shown by the one opened
// after it: an undo plan, the editor's tags, a run's detail, and the end of
// a run that is not the one going.
func TestLateAnswersForAFlowSinceClosedAreDropped(t *testing.T) {
	// The plan to undo run 5, read while the preflight of run 4 is open.
	src := undoSource()
	m := newModel(t, src, 120, 32)
	m.runner = newFakeRunner()
	hit(t, m, "5", "down", "down")
	_, plan5 := m.Update(keyMsg("u"))
	hit(t, m, "esc", "down")
	_, plan4 := m.Update(keyMsg("u"))
	m.Update(plan5())
	if m.undo == nil || m.undo.run.ID != 4 || m.undo.plan != nil {
		t.Fatalf("the plan of run 5 was shown for run 4: %+v", m.undo)
	}
	m.Update(plan4())
	if m.undo.plan == nil || m.undo.plan.Refusal == "" {
		t.Fatalf("the plan of run 4: %+v", m.undo.plan)
	}
	hit(t, m, "esc")
	m.Update(plan5())
	if m.undo != nil {
		t.Error("a late plan opened the preflight again")
	}

	// The detail of run 4, read while that of run 5 is awaited.
	_, detail4 := m.Update(keyMsg("enter"))
	hit(t, m, "esc", "up")
	_, detail5 := m.Update(keyMsg("enter"))
	m.Update(detail4())
	if d := m.runs.detail; d == nil || d.run.ID != 5 || d.loaded {
		t.Fatalf("the detail of run 4 was shown for run 5: %+v", d)
	}
	m.Update(detail5())
	if d := m.runs.detail; !d.loaded || len(d.detail.Moves) != 2 {
		t.Fatalf("the detail of run 5: %+v", d)
	}
	hit(t, m, "esc")
	m.Update(detail4())
	if m.runs.detail != nil || m.toast != "" {
		t.Errorf("a late detail opened a run again: toast %q", m.toast)
	}

	// The tags of one file, read while the editor is open on another.
	usrc := untaggedSource()
	um, _ := newUntaggedModel(t, usrc)
	usrc.tags = map[string]tagwrite.Values{}
	for _, f := range usrc.snap.Untagged {
		usrc.tags[f.Path] = tagwrite.Values{tagwrite.Title: {"Title of " + f.RelPath}}
	}
	cursorTo(t, um, 103)
	_, tags103 := um.Update(keyMsg("e"))
	hit(t, um, "esc", "down")
	_, tags104 := um.Update(keyMsg("e"))
	um.Update(tags103())
	if um.edit == nil || um.edit.file.ID != 104 || um.edit.form != nil {
		t.Fatalf("the tags of file 103 were shown for file 104: %+v", um.edit)
	}
	pump(t, um, func() tea.Msg { return tags104() })
	if um.edit.form == nil || um.edit.title != "Title of live.mp3" {
		t.Fatalf("the editor on file 104: title %q", um.edit.title)
	}
	hit(t, um, "esc")
	um.Update(tags103())
	if um.edit != nil {
		t.Error("late tags opened the editor again")
	}

	// The end of a run that is not the one going.
	f := newFakeRunner()
	rm := newRunModel(t, f)
	press(t, rm, "o")
	waitStart(t, f)
	going := rm.run
	rm.toast = ""
	if _, cmd := rm.Update(runDoneMsg{run: &liveRun{kind: RunIndex}}); cmd != nil || rm.run != going || rm.toast != "" || rm.last != nil {
		t.Errorf("the end of another run ended this one: run going %t, toast %q", rm.run != nil, rm.toast)
	}
	f.end(t, engine.RunFinished{})
	runEnded(t, rm)
}
