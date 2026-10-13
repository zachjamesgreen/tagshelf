package main

import (
	"bytes"
	"context"
	"encoding/csv"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/zachjamesgreen/tagshelf/internal/fixtures"
	"github.com/zachjamesgreen/tagshelf/internal/store"
)

// TestBuildReport checks the rows and the counts made from an index: folder
// by folder in path order, a column per fact, and nothing for the source
// folder of a run, whose files are on their way.
func TestBuildReport(t *testing.T) {
	lib := fixtures.OSPath(`D:\Music`)
	at := func(kind store.RootKind, rel string) string {
		if kind == store.RootLibrary {
			return filepath.Join(lib, rel)
		}
		return filepath.Join(lib, "_"+string(kind), rel)
	}
	files := []store.File{
		{ID: 1, RootKind: store.RootUntagged, Path: at(store.RootUntagged, "b.mp3"), Format: "MP3", Bitrate: 128, ReadError: "tags: cut short"},
		{ID: 2, RootKind: store.RootLibrary, Path: at(store.RootLibrary, filepath.Join("Zed", "Album", "01 - One.flac")), Format: "FLAC",
			Lossless: true, Bitrate: 900, SampleRate: 44100, BitDepth: 16, Length: 61400 * time.Millisecond, Size: 1234,
			Artist: "Zed", AlbumArtist: "Zed", Album: "Album", Title: "One", Year: 2001, Track: 1, Disc: 2,
			ReleaseID: "rel", RecordingID: "rec", HasArt: true},
		{ID: 3, RootKind: store.RootLibrary, Path: at(store.RootLibrary, filepath.Join("Abel", "Album", "01 - One.mp3")), Manual: "hand 2026-09-27"},
		{ID: 4, RootKind: store.RootSource, Path: fixtures.OSPath(`D:\Incoming\new.mp3`)},
		{ID: 5, RootKind: store.RootDuplicates, Path: at(store.RootDuplicates, "copy.mp3"), ReleaseTrackID: "track"},
	}
	groups := []store.Group{
		{Kind: store.GroupExact, Files: []store.File{files[1], files[2]}},
		{Kind: store.GroupPossible, Files: []store.File{files[2], files[0]}},
	}

	rep := buildReport(files, groups)

	var paths []string
	for _, row := range rep.rows {
		if len(row) != len(reportColumns) {
			t.Fatalf("a row has %d values for %d columns: %q", len(row), len(reportColumns), row)
		}
		paths = append(paths, row[1])
	}
	want := []string{files[2].Path, files[1].Path, files[0].Path, files[4].Path}
	if !slices.Equal(paths, want) {
		t.Errorf("rows are for\n%q, want\n%q", paths, want)
	}

	col := func(row []string, name string) string { return row[slices.Index(reportColumns, name)] }
	flac := rep.rows[1]
	for name, want := range map[string]string{
		"folder": "library", "format": "FLAC", "lossless": "yes", "bitrate_kbps": "900", "sample_rate_hz": "44100",
		"bit_depth": "16", "length_seconds": "61", "size_bytes": "1234", "artist": "Zed", "album_artist": "Zed",
		"album": "Album", "title": "One", "year": "2001", "track": "1", "disc": "2",
		"musicbrainz_release_id": "rel", "musicbrainz_recording_id": "rec", "manual_marker": "",
		"embedded_art": "yes", "duplicate_group": "1 exact", "read_error": "",
	} {
		if got := col(flac, name); got != want {
			t.Errorf("%s of the FLAC file is %q, want %q", name, got, want)
		}
	}
	manual := rep.rows[0]
	for name, want := range map[string]string{
		// What is not known is left empty, not written as a nought.
		"lossless": "no", "bitrate_kbps": "", "year": "", "track": "", "length_seconds": "",
		"manual_marker": "hand 2026-09-27", "embedded_art": "no", "duplicate_group": "1 exact; 2 possible",
	} {
		if got := col(manual, name); got != want {
			t.Errorf("%s of the manually tagged file is %q, want %q", name, got, want)
		}
	}
	if got := col(rep.rows[2], "read_error"); got != "tags: cut short" {
		t.Errorf("read_error of the untagged file is %q", got)
	}
	if got := col(rep.rows[3], "folder"); got != "duplicates" {
		t.Errorf("folder of the copy set aside is %q", got)
	}

	if rep.perFolder[store.RootLibrary] != 2 || rep.perFolder[store.RootUntagged] != 1 || rep.perFolder[store.RootDuplicates] != 1 || rep.perFolder[store.RootSource] != 0 {
		t.Errorf("files per folder are %v", rep.perFolder)
	}
	// The file with only a release track ID has an ID.
	if rep.noIDs != 2 || rep.noArt != 3 || rep.unread != 1 || rep.duplicateFiles != 3 || rep.duplicateGr != 2 {
		t.Errorf("counts are noIDs %d, noArt %d, unread %d, duplicates %d in %d groups; want 2, 3, 1, 3 in 2",
			rep.noIDs, rep.noArt, rep.unread, rep.duplicateFiles, rep.duplicateGr)
	}
}

// reportLibrary makes a library of a tagged FLAC file, a manually tagged MP3
// file and a file without tags, indexes it, and returns the arguments that
// name it.
func reportLibrary(t *testing.T) (tmp string, args []string, logs *bytes.Buffer) {
	t.Helper()
	tmp, config, logs := cliEnv(t)
	lib := filepath.Join(tmp, "lib")
	for dst, src := range map[string]string{
		filepath.Join(lib, "A", "B", "one.flac"):  fixtures.Tagged(".flac"),
		filepath.Join(lib, "A", "B", "two.mp3"):   fixtures.Manual(".mp3"),
		filepath.Join(lib, "_Untagged", "x.flac"): fixtures.Untagged(".flac"),
	} {
		data, err := os.ReadFile(src)
		if err != nil {
			t.Fatal(err)
		}
		writeFile(t, dst, string(data))
	}
	args = []string{"-config", config, "-dest", lib}
	if got := run(context.Background(), append(slices.Clone(args), "-index")); got != exitOK {
		t.Fatalf("index run = %d\n%s", got, logs)
	}
	logs.Reset()
	return tmp, args, logs
}

// TestReportWritesAFile runs `tagshelf report -o` on an indexed library: the
// file is CSV that Excel reads as UTF-8, with a row per file, and the counts
// are printed. A second report does not write over the first.
func TestReportWritesAFile(t *testing.T) {
	tmp, args, logs := reportLibrary(t)
	out := filepath.Join(tmp, "report.csv")
	args = append([]string{"report"}, append(args, "-o", out)...)

	if got := run(context.Background(), args); got != exitOK {
		t.Fatalf("run(%q) = %d, want %d\n%s", args, got, exitOK, logs)
	}
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	body, hasBOM := strings.CutPrefix(string(data), utf8BOM)
	if !hasBOM {
		t.Error("the file does not start with a byte order mark")
	}
	rows, err := csv.NewReader(strings.NewReader(body)).ReadAll()
	if err != nil {
		t.Fatalf("the file is not CSV: %v", err)
	}
	if !slices.Equal(rows[0], reportColumns) {
		t.Errorf("the first row is %q, want the column names", rows[0])
	}
	var got []string
	for _, row := range rows[1:] {
		got = append(got, row[0]+" "+filepath.Base(row[1])+" "+row[2])
	}
	if want := []string{"library one.flac FLAC", "library two.mp3 MP3", "untagged x.flac FLAC"}; !slices.Equal(got, want) {
		t.Errorf("the rows are %q, want %q", got, want)
	}
	if path := rows[1][1]; !filepath.IsAbs(path) {
		t.Errorf("the path %q is not a full one", path)
	}

	for _, want := range []string{
		"Report on the library at " + filepath.Join(tmp, "lib"), "Index as of:", "Run tagshelf -index first for a fresh one.",
		"Files:                       3 (library 2, untagged 1)", "Without MusicBrainz IDs:     2",
		// The two FLAC fixtures hold the same audio.
		"Without embedded cover art:  3", "In duplicate groups:         2, in 1 group waiting for a decision",
		"Not fully read:              0", "Written to:                  " + out,
	} {
		if !strings.Contains(logs.String(), want) {
			t.Errorf("the summary lacks %q:\n%s", want, logs)
		}
	}

	logs.Reset()
	if got := run(context.Background(), args); got != exitCannotStart {
		t.Errorf("a second report to the same file = %d, want %d", got, exitCannotStart)
	}
	if want := "tagshelf cannot start: the report file " + out + " already exists."; !strings.Contains(logs.String(), want) {
		t.Errorf("the refusal lacks %q:\n%s", want, logs)
	}
	if again, err := os.ReadFile(out); err != nil || !bytes.Equal(again, data) {
		t.Errorf("the first report was changed (%v)", err)
	}
}

// TestReportPrintsWithoutO checks that the report goes to standard output
// when no file is named, as plain CSV with nothing else in it: the counts go
// where the messages go, so that the output can be piped into a file.
func TestReportPrintsWithoutO(t *testing.T) {
	_, args, logs := reportLibrary(t)
	var printed bytes.Buffer
	stdout = &printed

	if got := run(context.Background(), append([]string{"report"}, args...)); got != exitOK {
		t.Fatalf("report = %d, want %d\n%s", got, exitOK, logs)
	}
	if strings.HasPrefix(printed.String(), utf8BOM) {
		t.Error("the printed report starts with a byte order mark")
	}
	rows, err := csv.NewReader(&printed).ReadAll()
	if err != nil || len(rows) != 4 {
		t.Fatalf("printed %d rows of CSV (%v), want the names and 3 files", len(rows), err)
	}
	if !strings.Contains(logs.String(), "Files:") || strings.Contains(logs.String(), "Written to:") {
		t.Errorf("the summary is not as for a printed report:\n%s", logs)
	}
}

// TestReportChangesNothing checks that a report leaves the library's
// database as it was and records no run.
func TestReportChangesNothing(t *testing.T) {
	tmp, args, logs := reportLibrary(t)
	db := filepath.Join(tmp, "tagshelf.db")
	// The library made above has its database in the default place; a
	// second one, named, is easier to look at.
	args = append(args, "-db", db)
	if got := run(context.Background(), append(slices.Clone(args), "-index")); got != exitOK {
		t.Fatalf("index run = %d\n%s", got, logs)
	}
	runsBefore := countRuns(t, db)

	if got := run(context.Background(), append([]string{"report", "-o", filepath.Join(tmp, "r.csv")}, args...)); got != exitOK {
		t.Fatalf("report = %d\n%s", got, logs)
	}
	if after := countRuns(t, db); after != runsBefore {
		t.Errorf("the history has %d runs after the report, %d before", after, runsBefore)
	}
}

func countRuns(t *testing.T, db string) int {
	t.Helper()
	st, err := store.OpenReader(db)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	runs, err := st.Runs()
	if err != nil {
		t.Fatal(err)
	}
	return len(runs)
}

// TestReportRefusals checks what keeps a report from being made, each said
// in a sentence with what to do about it, and that nothing is written.
func TestReportRefusals(t *testing.T) {
	tmp, config, logs := cliEnv(t)
	lib := filepath.Join(tmp, "lib")
	writeFile(t, filepath.Join(lib, "song.mp3"), "no tags")
	out := filepath.Join(tmp, "report.csv")

	tests := []struct {
		name string
		args []string
		want string
	}{
		{"no library", []string{"report", "-config", config, "-o", out}, "tagshelf cannot start: no library folder is set."},
		{"no index yet", []string{"report", "-config", config, "-dest", lib, "-o", out}, "has no index yet."},
		{"an argument that is no flag", []string{"report", "-config", config, "-dest", lib, out}, "tagshelf report takes only flags."},
		{"a flag of a run", []string{"report", "-config", config, "-dest", lib, "-dry-run"}, "there is no flag -dry-run."},
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
			if _, err := os.Stat(out); !os.IsNotExist(err) {
				t.Errorf("a report file was made: %v", err)
			}
		})
	}
}
