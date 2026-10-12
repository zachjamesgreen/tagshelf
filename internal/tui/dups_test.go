package tui

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/zachjamesgreen/tagshelf/internal/engine"
	"github.com/zachjamesgreen/tagshelf/internal/store"
)

// dupSource is a library with three groups waiting for review: a release
// track as FLAC and MP3 (with the recording also on a compilation), an exact
// pair, and a possible pair.
func dupSource() *fakeSource {
	file := func(id int64, kind store.RootKind, rel string, f store.File) store.File {
		f.ID, f.RootKind, f.RelPath = id, kind, filepath.FromSlash(rel)
		root := p(`D:\Music`)
		if kind == store.RootUntagged {
			root = p(`D:\Music\_Untagged`)
		}
		f.Path = filepath.Join(root, f.RelPath)
		return f
	}
	song := store.File{Artist: "Boards of Canada", Album: "Geogaddi", Title: "Julie and Candy", Track: 5,
		Length: 330 * time.Second, ReleaseTrackID: "t1", RecordingID: "r1"}
	flac, mp3 := song, song
	flac.Format, flac.Lossless, flac.SampleRate, flac.BitDepth, flac.Bitrate = "FLAC", true, 44100, 16, 900
	mp3.Format, mp3.Bitrate, mp3.SampleRate = "MP3", 192, 44100
	exact := store.File{Format: "MP3", Bitrate: 320, SampleRate: 44100, Artist: "Singer", Album: "Album", Title: "Song", Track: 1, Length: 201 * time.Second}
	hand := store.File{Format: "MP3", Bitrate: 128, SampleRate: 44100, Artist: "Hand Tagged", Album: "Tape", Title: "Demo", Track: 2, Length: 95 * time.Second}

	src := &fakeSource{snap: sampleSnapshot()}
	src.snap.Groups = []store.Group{
		{Kind: store.GroupReleaseTrack,
			Files: []store.File{
				file(11, store.RootLibrary, "Boards of Canada/Geogaddi (2002)/05 - Julie and Candy.mp3", mp3),
				file(12, store.RootLibrary, "Boards of Canada/Geogaddi (2002) [FLAC]/05 - Julie and Candy.flac", flac),
			},
			AlsoOn: []store.File{file(19, store.RootLibrary, "Various Artists/Warp 20/11 - Julie and Candy.mp3", mp3)}},
		{Kind: store.GroupExact, Files: []store.File{
			file(21, store.RootLibrary, "Singer/Album/01 - Song.mp3", exact),
			file(22, store.RootUntagged, "rips/song copy.mp3", exact),
		}},
		{Kind: store.GroupPossible, Files: []store.File{
			file(31, store.RootLibrary, "Hand Tagged/Tape/02 - Demo.mp3", hand),
			file(32, store.RootUntagged, "demo.mp3", hand),
		}},
	}
	src.snap.NeedReview = 3
	return src
}

func newDupModel(t *testing.T, src *fakeSource) (*Model, *fakeRunner) {
	t.Helper()
	f := newFakeRunner()
	m := newModel(t, src, 120, 32)
	m.runner = f
	press(t, m, "3")
	return m, f
}

func TestDuplicatesTabShowsGroupsAndDetail(t *testing.T) {
	m, _ := newDupModel(t, dupSource())
	got := screen(m)
	for _, want := range []string{
		"Duplicates (3", "Boards of Canada - Julie and Candy", "release-track", "Singer - Song", "exact", "Hand Tagged - Demo", "possible",
		// The detail of the first group: the FLAC is the suggested keeper.
		"★", filepath.Join("Geogaddi (2002) [FLAC]", "05 - Julie and Candy.flac"), "FLAC", "lossless", "192 kbps", "5:30",
		"Also on", filepath.Join("Various Artists", "Warp 20"),
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the duplicates tab lacks %q:\n%s", want, got)
		}
	}
	if k := m.dups.keeper(m.snap.Groups[0]); k != 12 {
		t.Errorf("suggested keeper = file %d, want the FLAC (12)", k)
	}
}

func TestResolveSelectedGroupWithChosenKeeper(t *testing.T) {
	src := dupSource()
	m, f := newDupModel(t, src)
	f.resolveFinish = engine.RunFinished{State: engine.StateOK, Counters: engine.Counters{Duplicates: 1}}
	press(t, m, "k") // the MP3 instead of the suggested FLAC
	loads := src.loads
	press(t, m, "enter")

	want := [][]engine.Resolution{{{Kind: store.GroupReleaseTrack, Members: []int64{11, 12}, Keeper: 11}}}
	if !reflect.DeepEqual(f.resolved, want) {
		t.Errorf("resolved = %+v, want %+v", f.resolved, want)
	}
	if !m.toastOK || !strings.Contains(m.toast, "1 file moved") {
		t.Errorf("toast = %q (ok %t)", m.toast, m.toastOK)
	}
	if src.loads == loads {
		t.Error("the library was not reloaded after resolving")
	}
}

func TestNextAndKeepBoth(t *testing.T) {
	m, f := newDupModel(t, dupSource())
	press(t, m, "n")
	press(t, m, "b")
	want := []engine.Resolution{{Kind: store.GroupExact, Members: []int64{21, 22}}}
	if !reflect.DeepEqual(f.keptBoth, want) || len(f.resolved) != 0 {
		t.Errorf("kept both = %+v, resolved = %+v; want %+v and nothing", f.keptBoth, f.resolved, want)
	}
	if !m.toastOK || !strings.Contains(m.toast, "Kept both") {
		t.Errorf("toast = %q (ok %t)", m.toast, m.toastOK)
	}
	// n wraps round to the first group.
	press(t, m, "n")
	press(t, m, "n")
	if m.dups.cursor != 0 {
		t.Errorf("cursor = %d after wrapping, want 0", m.dups.cursor)
	}
}

func TestPossibleGroupMustBeConfirmedFirst(t *testing.T) {
	m, f := newDupModel(t, dupSource())
	press(t, m, "down")
	press(t, m, "down")
	press(t, m, "enter")
	if len(f.resolved) != 0 || m.toastOK || !strings.Contains(m.toast, "c ") {
		t.Fatalf("resolved %+v, toast %q: want a refusal pointing at c", f.resolved, m.toast)
	}
	if strings.Contains(screen(m), "★") {
		t.Error("an unconfirmed possible group shows a keeper")
	}
	press(t, m, "c")
	if !strings.Contains(screen(m), "★") {
		t.Errorf("a confirmed possible group shows no keeper:\n%s", screen(m))
	}
	press(t, m, "enter")
	if len(f.resolved) != 1 || f.resolved[0][0].Kind != store.GroupPossible || !reflect.DeepEqual(f.resolved[0][0].Members, []int64{31, 32}) {
		t.Errorf("resolved = %+v, want the possible group", f.resolved)
	}
}

func TestResolveAllExactAsksFirst(t *testing.T) {
	m, f := newDupModel(t, dupSource())
	press(t, m, "A")
	if got := screen(m); !strings.Contains(got, "Resolve 1 exact group") || !strings.Contains(got, "1 file") {
		t.Fatalf("no summary to confirm:\n%s", got)
	}
	press(t, m, "n")
	if len(f.resolved) != 0 || m.dups.confirmAll != nil {
		t.Fatal("n did not cancel")
	}
	// The copy chosen with k is the one kept, not the suggested one.
	press(t, m, "down")
	press(t, m, "k")
	press(t, m, "A")
	press(t, m, "y")
	want := [][]engine.Resolution{{{Kind: store.GroupExact, Members: []int64{21, 22}, Keeper: 22}}}
	if !reflect.DeepEqual(f.resolved, want) {
		t.Errorf("resolved = %+v, want only the exact group, keeping the copy chosen: %+v", f.resolved, want)
	}
}

func TestDuplicateActionsDisabled(t *testing.T) {
	locked := dupSource()
	locked.locked, locked.holder = true, store.LockHolder{PID: 8812, Started: started}
	dry := func(m *Model) { m.header.DryRun = true }
	for name, tt := range map[string]struct {
		src   *fakeSource
		setup func(*Model)
		toast string
	}{
		"another process holds the lock": {locked, nil, "pid 8812"},
		"dry run":                        {dupSource(), dry, "Dry run"},
	} {
		m, f := newDupModel(t, tt.src)
		if tt.setup != nil {
			tt.setup(m)
		}
		for _, k := range []string{"enter", "b", "A"} {
			press(t, m, k)
			if len(f.resolved) != 0 || len(f.keptBoth) != 0 || m.dups.confirmAll != nil {
				t.Errorf("%s: %s acted", name, k)
			}
			if m.toastOK || !strings.Contains(m.toast, tt.toast) {
				t.Errorf("%s: %s toast = %q, want it to mention %q", name, k, m.toast, tt.toast)
			}
		}
	}
}

func TestResolveFailureShowsToast(t *testing.T) {
	m, f := newDupModel(t, dupSource())
	f.resolveFinish = engine.RunFinished{State: engine.StateErrors, Counters: engine.Counters{Errors: 1}}
	press(t, m, "enter")
	if m.toastOK || !strings.Contains(m.toast, "5 Runs") {
		t.Errorf("toast = %q (ok %t), want the errors pointed at the Runs tab", m.toast, m.toastOK)
	}

	f.err = &store.LockedError{Path: p(`D:\db.lock`), Holder: store.LockHolder{PID: 4242, Started: started}}
	press(t, m, "b")
	if m.toastOK || !strings.Contains(m.toast, "4242") {
		t.Errorf("toast = %q (ok %t), want the lock error", m.toast, m.toastOK)
	}
}

func TestEmptyDuplicatesTab(t *testing.T) {
	m, f := newDupModel(t, &fakeSource{snap: sampleSnapshot()})
	for _, k := range []string{"enter", "b", "k", "c", "n", "A"} {
		press(t, m, k)
	}
	if got := screen(m); !strings.Contains(got, "No duplicate groups to review") {
		t.Errorf("empty duplicates tab:\n%s", got)
	}
	if len(f.resolved)+len(f.keptBoth) != 0 {
		t.Error("an action ran with no groups")
	}
}

func TestGoldenDuplicates(t *testing.T) {
	m, _ := newDupModel(t, dupSource())
	got := screen(m)
	checkFits(t, got, 120, 32)
	golden(t, filepath.Join("testdata", "duplicates.golden"), got)
}

// A "resolve all" question is dropped when the library reloads under it: its
// answer would be about groups as they no longer are.
func TestResolveAllQuestionDroppedOnReload(t *testing.T) {
	src := dupSource()
	m, f := newDupModel(t, src)
	press(t, m, "A")
	if m.dups.confirmAll == nil {
		t.Fatal("A did not ask")
	}
	src.snap.Groups, src.snap.NeedReview = nil, 0
	src.version++
	tickNow(t, m)
	if m.dups.confirmAll != nil {
		t.Error("the question outlived the reload")
	}
	press(t, m, "y")
	if len(f.resolved) != 0 {
		t.Errorf("y resolved %+v after the groups were gone", f.resolved)
	}
	if !strings.Contains(screen(m), "No duplicate groups to review") {
		t.Errorf("screen after the reload:\n%s", screen(m))
	}
}
