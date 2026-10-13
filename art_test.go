package main

import (
	"bytes"
	"context"
	"image"
	"image/jpeg"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/zachjamesgreen/tagshelf/internal/coverart"
	"github.com/zachjamesgreen/tagshelf/internal/engine"
	"github.com/zachjamesgreen/tagshelf/internal/fixtures"
	"github.com/zachjamesgreen/tagshelf/internal/store"
)

// artLibrary makes an indexed library of one album folder whose two files
// name a MusicBrainz release, puts a stand-in in the place of the Cover Art
// Archive, and returns the arguments that name the library with what the
// stand-in was asked.
func artLibrary(t *testing.T) (album string, args []string, logs *bytes.Buffer, asked *[]string) {
	t.Helper()
	tmp, config, logs := cliEnv(t)
	lib := filepath.Join(tmp, "lib")
	album = filepath.Join(lib, "Artist", "Album")
	for name, src := range map[string]string{"01.flac": fixtures.Tagged(".flac"), "02.mp3": fixtures.Tagged(".mp3")} {
		data, err := os.ReadFile(src)
		if err != nil {
			t.Fatal(err)
		}
		writeFile(t, filepath.Join(album, name), string(data))
	}
	args = []string{"-config", config, "-dest", lib}
	if got := run(context.Background(), append(slices.Clone(args), "-index")); got != exitOK {
		t.Fatalf("index run = %d\n%s", got, logs)
	}
	logs.Reset()

	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, image.NewGray(image.Rect(0, 0, 1200, 1200)), nil); err != nil {
		t.Fatal(err)
	}
	asked = new([]string)
	old := coverFetch
	coverFetch = func(_ context.Context, release, group string, size int) (coverart.Picture, error) {
		*asked = append(*asked, release)
		return coverart.Describe(buf.Bytes())
	}
	t.Cleanup(func() { coverFetch = old })
	return album, args, logs, asked
}

// TestArtWritesCoverFiles runs `tagshelf art -file` on an indexed library:
// the album folder gets its cover from the archive, the run is in the
// history, and a second run finds nothing left to do.
func TestArtWritesCoverFiles(t *testing.T) {
	album, args, logs, asked := artLibrary(t)
	args = append([]string{"art", "-file"}, args...)
	cover := filepath.Join(album, "cover.jpg")

	if got := run(context.Background(), append(slices.Clone(args), "-dry-run")); got != exitOK {
		t.Fatalf("the dry run = %d\n%s", got, logs)
	}
	if _, err := os.Stat(cover); !os.IsNotExist(err) || len(*asked) != 0 {
		t.Errorf("the dry run wrote a cover (%v) or asked the archive %d times", err, len(*asked))
	}
	for _, want := range []string{"[DRY-RUN] [COVER]", "if it has one", "Covers that would be written: 1"} {
		if !strings.Contains(logs.String(), want) {
			t.Errorf("the dry run's output lacks %q:\n%s", want, logs)
		}
	}

	logs.Reset()
	if got := run(context.Background(), args); got != exitOK {
		t.Fatalf("run(%q) = %d\n%s", args, got, logs)
	}
	if info, err := os.Stat(cover); err != nil || info.Size() == 0 {
		t.Fatalf("no cover file: %v", err)
	}
	if !slices.Equal(*asked, []string{fixtures.ReleaseID}) {
		t.Errorf("the archive was asked for %q", *asked)
	}
	for _, want := range []string{"[COVER]", "from the Cover Art Archive", "Covers written:           1", "Errors encountered:       0"} {
		if !strings.Contains(logs.String(), want) {
			t.Errorf("the output lacks %q:\n%s", want, logs)
		}
	}

	logs.Reset()
	if got := run(context.Background(), args); got != exitOK || !strings.Contains(logs.String(), "Covers written:           0") || len(*asked) != 1 {
		t.Errorf("a second run = %d, asked %d times:\n%s", got, len(*asked), logs)
	}
}

// The setting in the config file turns it on as the flag does, and the run
// is recorded so that it can be undone.
func TestArtGoesByTheConfigFile(t *testing.T) {
	album, args, logs, _ := artLibrary(t)
	config := args[1]
	writeFile(t, config, "cover_art_file: true\ncover_art_replace: always\n")
	if got := run(context.Background(), append([]string{"art"}, args...)); got != exitOK {
		t.Fatalf("art = %d\n%s", got, logs)
	}
	if _, err := os.Stat(filepath.Join(album, "cover.jpg")); err != nil {
		t.Errorf("no cover file: %v", err)
	}
	if !strings.Contains(logs.String(), "Replace:      always") {
		t.Errorf("the settings printed:\n%s", logs)
	}
	st, err := store.OpenLibraryReader(args[3], "")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	runs, err := st.Runs()
	if err != nil || len(runs) != 2 || runs[1].Counters.Covers != 1 || runs[1].Kind != store.KindTUIAction {
		t.Fatalf("the history = %+v, %v", runs, err)
	}
	plan, err := engine.PlanUndo(st, runs[1].ID, nil)
	if err != nil || plan.Covers != 1 {
		t.Errorf("the undo's plan = %+v, %v", plan, err)
	}
}

// TestArtRefusals checks what keeps tagshelf art from starting, each said
// with what to do about it, and that nothing is asked or written.
func TestArtRefusals(t *testing.T) {
	album, args, logs, asked := artLibrary(t)
	tests := []struct {
		name string
		args []string
		want string
	}{
		{"no cover art setting on", append([]string{"art"}, args...), "no cover art setting is on"},
		{"a word that is none of the three", append([]string{"art", "-file", "-replace", "bigger"}, args...), "must be never, smaller or always, not bigger"},
		{"no library", []string{"art", "-file", "-config", args[1]}, "no library folder is set"},
		{"a library that is not there", []string{"art", "-file", "-config", args[1], "-dest", filepath.Join(album, "none")}, "has no index yet"},
		{"a flag of a run", append([]string{"art", "-file", "-source", album}, args...), "there is no flag -source"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			logs.Reset()
			if got := run(context.Background(), tt.args); got != exitCannotStart {
				t.Errorf("run(%q) = %d, want %d", tt.args, got, exitCannotStart)
			}
			if !strings.Contains(logs.String(), tt.want) {
				t.Errorf("the output lacks %q:\n%s", tt.want, logs)
			}
		})
	}
	if _, err := os.Stat(filepath.Join(album, "cover.jpg")); !os.IsNotExist(err) || len(*asked) != 0 {
		t.Errorf("a refused run wrote a cover (%v) or asked the archive %d times", err, len(*asked))
	}
}
