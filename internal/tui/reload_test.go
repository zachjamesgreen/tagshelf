package tui

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/zachjamesgreen/tagshelf/internal/engine"
	"github.com/zachjamesgreen/tagshelf/internal/store"
)

// everySource is a library with files in _Untagged, duplicate groups waiting
// and a run history.
func everySource() *fakeSource {
	src := untaggedSource()
	src.snap.Groups = dupSource().snap.Groups
	src.snap.Runs = historySource().snap.Runs
	return src
}

// A load reads the lists of the tab showing and no others. A list left
// unread is read when its tab shows, and again once the library has changed.
func TestLoadReadsOnlyTheListsOfTheTabShowing(t *testing.T) {
	src := everySource()
	m := newModel(t, src, 120, 32)
	want := []Parts{{}}
	check := func(when string, more ...Parts) {
		t.Helper()
		if want = append(want, more...); !reflect.DeepEqual(src.asked, want) {
			t.Fatalf("%s: the loads asked for %+v, want %+v", when, src.asked, want)
		}
	}
	check("on Home")
	if len(m.snap.Untagged)+len(m.snap.Groups)+len(m.snap.Runs) != 0 {
		t.Errorf("Home read %d files, %d groups and %d runs", len(m.snap.Untagged), len(m.snap.Groups), len(m.snap.Runs))
	}

	// Until its list has come, a tab says it is being read rather than that
	// it is empty.
	_, cmd := m.Update(keyMsg("2"))
	if got := screen(m); !strings.Contains(got, "Reading the library") || strings.Contains(got, "No files") {
		t.Errorf("the Untagged tab before its files are read:\n%s", got)
	}
	do(t, m, cmd)
	check("on Untagged", Parts{Untagged: true})
	if got := screen(m); !strings.Contains(got, "01 track.flac") {
		t.Errorf("the Untagged tab once read:\n%s", got)
	}
	press(t, m, "3")
	check("on Duplicates", Parts{Groups: true})
	press(t, m, "5")
	check("on Runs", Parts{Runs: true})
	press(t, m, "4")
	check("on Library")

	// Nothing has changed: what was read is still good.
	for _, k := range []string{"2", "3", "5", "1"} {
		press(t, m, k)
		tickNow(t, m)
	}
	check("back on tabs already read")

	// A change is read for the tab showing, and for the others when they show.
	press(t, m, "5")
	src.version++
	src.snap.Untagged = src.snap.Untagged[:1]
	tickNow(t, m)
	check("a change with Runs showing", Parts{Runs: true})
	if len(m.snap.Untagged) != 5 {
		t.Errorf("the files of a tab not showing were read: %d of them", len(m.snap.Untagged))
	}
	press(t, m, "2")
	check("back on Untagged after the change", Parts{Untagged: true})
	if got := screen(m); !strings.Contains(got, "Untagged (1 file)") {
		t.Errorf("the Untagged tab after the change:\n%s", got)
	}
	press(t, m, "5")
	check("back on Runs, read since the change")
}

// A poll that comes back after the tab changed leaves the new tab's list to
// be read.
func TestLoadFollowsATabChangedMeanwhile(t *testing.T) {
	src := everySource()
	m := newModel(t, src, 120, 32)
	src.version++
	_, poll := m.Update(tickMsg{}) // reads for Home
	m.tab = TabRuns
	do(t, m, poll)
	if last := src.asked[len(src.asked)-1]; !last.Runs || len(m.snap.Runs) != 7 {
		t.Errorf("after the tab changed during a poll the loads asked for %+v and %d runs are in hand", src.asked, len(m.snap.Runs))
	}
}

// While a run is going the library changes with every file: it is read
// again at most every runReload, and at once when the run ends or a tab
// needs a list.
func TestReloadsWaitWhileARunIsGoing(t *testing.T) {
	for name, locked := range map[string]bool{"a run started here": false, "a run in another process": true} {
		t.Run(name, func(t *testing.T) {
			f := newFakeRunner()
			src := everySource()
			src.version = 1
			if locked {
				l := lockedSource()
				src.locked, src.holder, src.snap.Active = l.locked, l.holder, l.snap.Active
			}
			m := newModel(t, src, 120, 32)
			m.runner = f
			now := started
			m.now = func() time.Time { return now }
			m.lastLoad = now
			if !locked {
				press(t, m, "o")
				waitStart(t, f)
			}

			change := func(after time.Duration) int {
				t.Helper()
				loads := src.loads
				now = now.Add(after)
				src.version++
				src.snap.Overview.Library++
				tickNow(t, m)
				return src.loads - loads
			}
			if n := change(time.Second); n != 0 {
				t.Errorf("a change 1 s after the last load was read %d times", n)
			}
			if n := change(time.Second); n != 0 {
				t.Errorf("a change 2 s after the last load was read %d times", n)
			}
			if m.snap.Overview.Library != 18204 {
				t.Errorf("the library count moved to %d without a load", m.snap.Overview.Library)
			}
			if n := change(time.Second); n != 1 || m.snap.Overview.Library != 18207 {
				t.Errorf("3 s after the last load: %d loads, library count %d; want the change read once", n, m.snap.Overview.Library)
			}
			if n := change(time.Second); n != 0 {
				t.Errorf("a change 1 s after that load was read %d times", n)
			}
			// Nothing waits when a tab needs its list.
			loads := src.loads
			press(t, m, "5")
			if src.loads != loads+1 || len(m.snap.Runs) == 0 {
				t.Errorf("switching tab during the run: %d loads, %d runs", src.loads-loads, len(m.snap.Runs))
			}

			loads = src.loads
			if locked {
				// The other process ends its run and lets the lock go.
				src.locked, src.holder, src.snap.Active = false, store.LockHolder{}, nil
				tickNow(t, m)
			} else {
				f.end(t, engine.RunFinished{})
				do(t, m, runEnded(t, m))
			}
			if src.loads != loads+1 {
				t.Errorf("the end of the run was followed by %d loads, want 1", src.loads-loads)
			}
			// With no run going a change is read at once.
			if n := change(time.Second); n != 1 {
				t.Errorf("a change after the run was read %d times", n)
			}
		})
	}
}

// A failure of one read alone shows as a toast, and the next good poll
// clears it.
func TestEachReadCanFail(t *testing.T) {
	for name, fail := range map[string]func(*fakeSource, error){
		"the lock":     func(src *fakeSource, err error) { src.lockErr = err },
		"the snapshot": func(src *fakeSource, err error) { src.loadErr = err },
	} {
		src := &fakeSource{version: 1, snap: sampleSnapshot()}
		m := newModel(t, src, 120, 32)
		fail(src, errors.New("disk gone"))
		src.version++
		src.snap.Overview.Untagged = 99
		tickNow(t, m)
		if got := screen(m); !strings.Contains(got, "Cannot read the library: disk gone") || !strings.Contains(got, "12 untagged files") {
			t.Errorf("%s failing:\n%s", name, got)
		}
		fail(src, nil)
		tickNow(t, m)
		if got := screen(m); strings.Contains(got, "Cannot read") || !strings.Contains(got, "99 untagged files") {
			t.Errorf("after %s could be read again:\n%s", name, got)
		}
	}
}

// The real source reads the counts every tab shows with each load, and each
// list only when asked.
func TestLibrarySourceLoadsTheListsAskedFor(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "tagshelf.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	lib := filepath.Join(dir, "Music")
	libRoot, err := st.SetRoot(store.RootLibrary, lib)
	if err != nil {
		t.Fatal(err)
	}
	untRoot, err := st.SetRoot(store.RootUntagged, filepath.Join(lib, "_Untagged"))
	if err != nil {
		t.Fatal(err)
	}
	song := store.File{Format: "MP3", Sum: "same", Size: 1, ModTime: time.Unix(1, 0), TagsJSON: `{"comment":"long"}`}
	one, two := song, song
	one.RootID, one.RelPath = libRoot, "a.mp3"
	two.RootID, two.RelPath = untRoot, "b.mp3"
	if err := st.PutFiles([]store.File{one, two}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.StartRun(store.KindIndex, false, "", lib, time.Now()); err != nil {
		t.Fatal(err)
	}

	src := librarySource{st: st, destination: lib, pid: -1}
	snap, err := src.Load(Parts{})
	if err != nil {
		t.Fatal(err)
	}
	if snap.Overview.Library != 1 || snap.Overview.Untagged != 1 || snap.NeedReview != 1 || snap.Active == nil {
		t.Errorf("a load of no list: overview %+v, %d groups to review, active run %v", snap.Overview, snap.NeedReview, snap.Active)
	}
	if len(snap.Groups)+len(snap.Untagged)+len(snap.Runs) != 0 {
		t.Errorf("a load of no list read %d groups, %d untagged files and %d runs", len(snap.Groups), len(snap.Untagged), len(snap.Runs))
	}
	all := Parts{Untagged: true, Groups: true, Runs: true}
	if snap, err = src.Load(all); err != nil {
		t.Fatal(err)
	}
	if snap.Parts != all || len(snap.Groups) != 1 || snap.NeedReview != 1 || len(snap.Untagged) != 1 || len(snap.Runs) != 1 {
		t.Fatalf("a load of every list: %+v", snap)
	}
	g := snap.Groups[0]
	if g.Kind != store.GroupExact || len(g.Files) != 2 || g.Files[0].Path != filepath.Join(lib, "a.mp3") || g.Files[1].RootKind != store.RootUntagged {
		t.Errorf("the group = %+v", g)
	}
	// None of it carries the tags as the reader returned them.
	if g.Files[0].TagsJSON != "" || snap.Untagged[0].TagsJSON != "" {
		t.Errorf("the lists carry tags_json: %q, %q", g.Files[0].TagsJSON, snap.Untagged[0].TagsJSON)
	}
}

// A run on the same library that uses another database holds the library's
// lock and not this database's: the TUI is read-only all the same.
func TestLibrarySourceSeesARunWithAnotherDatabase(t *testing.T) {
	dir := t.TempDir()
	lib := filepath.Join(dir, "Music")
	st, err := store.Open(filepath.Join(dir, "tagshelf.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	src := librarySource{st: st, destination: lib, pid: -1}
	if _, held, err := src.Lock(); err != nil || held {
		t.Fatalf("with no run: held = %t, %v", held, err)
	}
	running, err := store.AcquireLibraryLock(lib)
	if err != nil {
		t.Fatal(err)
	}
	defer running.Release()
	h, held, err := src.Lock()
	if err != nil || !held || h.PID != os.Getpid() {
		t.Errorf("with the library locked: holder %+v, held = %t, %v; want it seen", h, held, err)
	}
	// This process's own lock is not another's.
	src.pid = os.Getpid()
	if _, held, err := src.Lock(); err != nil || held {
		t.Errorf("this process's own library lock: held = %t, %v", held, err)
	}
}

// BenchmarkLibrarySourceLoad times one reload of a library of 50,000 files,
// for each tab that asks for a list of its own.
func BenchmarkLibrarySourceLoad(b *testing.B) {
	st, err := store.OpenReader(bigLibrary(b, 50000))
	if err != nil {
		b.Fatal(err)
	}
	defer st.Close()
	src := librarySource{st: st, pid: -1}
	for _, tt := range []struct {
		name  string
		parts Parts
	}{
		{"Home", Parts{}},
		{"Untagged", Parts{Untagged: true}},
		{"Duplicates", Parts{Groups: true}},
		{"Runs", Parts{Runs: true}},
		{"Everything", Parts{Untagged: true, Groups: true, Runs: true}},
	} {
		b.Run(tt.name, func(b *testing.B) {
			b.ReportAllocs()
			for range b.N {
				snap, err := src.Load(tt.parts)
				if err != nil || snap.NeedReview != 500 || snap.Overview.Untagged != 5000 {
					b.Fatalf("%d groups to review, %d untagged, %v", snap.NeedReview, snap.Overview.Untagged, err)
				}
				if tt.parts.Untagged != (len(snap.Untagged) == 5000) || tt.parts.Groups != (len(snap.Groups) == 500) || tt.parts.Runs != (len(snap.Runs) == 200) {
					b.Fatalf("read %d untagged files, %d groups and %d runs", len(snap.Untagged), len(snap.Groups), len(snap.Runs))
				}
			}
		})
	}
}
