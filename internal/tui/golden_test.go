package tui

import (
	"flag"
	"os"
	"path/filepath"
	"testing"
)

var update = flag.Bool("update", false, "rewrite the golden View() snapshots in testdata")

// TestGoldenViews compares whole screens at 120x32, without colours, against
// testdata/*.golden. Run go test ./internal/tui -update to accept changes.
//
// The golden files hold the screens of Windows, with its paths. On another
// system the paths in them are read as that system writes them (see p), and
// -update is refused: a Windows screen cannot be told from another one.
func TestGoldenViews(t *testing.T) {
	tests := []struct {
		name string
		src  func() *fakeSource
		keys []string
	}{
		{"home", func() *fakeSource { return &fakeSource{snap: sampleSnapshot()} }, nil},
		{"home_locked", lockedSource, nil},
		{"home_empty", func() *fakeSource { return &fakeSource{} }, nil},
		{"untagged_locked", func() *fakeSource {
			src := lockedSource()
			src.snap.Untagged = untaggedSource().snap.Untagged
			src.snap.Overview.Untagged = int64(len(src.snap.Untagged))
			return src
		}, []string{"2"}},
		{"help", func() *fakeSource { return &fakeSource{snap: sampleSnapshot()} }, []string{"?"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := newModel(t, tt.src(), 120, 32)
			for _, k := range tt.keys {
				press(t, m, k)
			}
			got := screen(m)
			checkFits(t, got, 120, 32)
			golden(t, filepath.Join("testdata", tt.name+".golden"), got)
		})
	}
}

// TestGoldenLoading is the screen before the first load has come back.
func TestGoldenLoading(t *testing.T) {
	m := New(&fakeSource{}, Header{Destination: p(`D:\Music`), DryRun: true})
	m.Update(windowSize(120, 32))
	golden(t, filepath.Join("testdata", "loading.golden"), screen(m))
}

func golden(t *testing.T, path, got string) {
	t.Helper()
	if *update {
		if filepath.Separator != '\\' {
			t.Fatal("the golden files hold Windows paths: run -update on Windows")
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run with -update to create it)", err)
	}
	if want := p(string(data)); got != want {
		t.Errorf("view differs from %s (run with -update to accept):\n--- got ---\n%s\n--- want ---\n%s", path, got, want)
	}
}
