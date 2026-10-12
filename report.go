package main

import (
	"cmp"
	"encoding/csv"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"log"
	"math"
	"os"
	"slices"
	"strconv"
	"strings"

	"github.com/zachjamesgreen/tagshelf/internal/config"
	"github.com/zachjamesgreen/tagshelf/internal/engine"
	"github.com/zachjamesgreen/tagshelf/internal/store"
)

var reportHelp = helpScreen{
	intro: `tagshelf report lists every audio file the library's index knows, a row
each, as CSV: where it is, its format and quality, its tags, and what is
missing. It reads the index as it is and changes nothing.

Usage:
  tagshelf report [flags]

Examples:
  tagshelf report -dest D:\Music -o report.csv
      Write the report on the library at D:\Music to report.csv.
  tagshelf -index -dest D:\Music
      Bring the index up to date first, for a fresh report.
`,
	groups: []flagGroup{
		{"The report", []string{"o"}},
		{"The library", []string{"dest", "db"}},
		{"Other", []string{"config"}},
	},
	extra: [][2]string{{"-h", "Show this help."}},
}

// reportFlags defines the flags of `tagshelf report` on set. The report
// reads one library, so of the folders it takes only that and its database.
func reportFlags(set *flag.FlagSet) (flags *libraryFlags, out *string) {
	set.SetOutput(io.Discard)
	set.Usage = func() {}
	flags = &libraryFlags{
		set:    set,
		help:   reportHelp,
		config: set.String("config", "", "Take the library from this config `file`. Flags override what it says."),
		dest:   set.String("dest", "", "The library: the `folder` to report on."),
		db:     set.String("db", "", "The library's database `file`, when it is not in its usual place."),
	}
	out = set.String("o", "", "Write the report to this `file`, which must not exist yet. Without it the report is printed.")
	return flags, out
}

// runReport runs `tagshelf report [flags]` and returns the process exit code.
// It only reads: the index is taken as the last run left it, and no lock is
// held, so it can be made while a run is going.
func runReport(args []string) int {
	set := flag.NewFlagSet("tagshelf report", flag.ContinueOnError)
	flags, out := reportFlags(set)
	if code, done := flags.parse(args); done {
		return code
	}
	cfg, file, err := config.LoadFile(*flags.config, config.Overrides{Destination: *flags.dest, Database: *flags.db})
	if err != nil {
		return configProblem(err)
	}
	if cfg.Destination == "" {
		return flags.misuse("no library folder is set", here(noDestinationTodo))
	}
	// The other folders of the config file are not the report's concern: it
	// finds the library's files by the index.
	cfg, _, problem := usablePaths(config.Config{Destination: cfg.Destination, Database: cfg.Database},
		pathOrigin{flags: flags.typedPaths(), file: file.Path})
	if problem != nil {
		return problem.refusal().print()
	}

	st, err := store.OpenLibraryReader(cfg.Destination, cfg.Database)
	if err != nil {
		return libraryProblem(err, cfg.Destination)
	}
	defer st.Close()
	rep, err := readReport(st)
	if err != nil {
		fmt.Fprintf(log.Writer(), "The report could not be made: %v.\n", err)
		return exitErrors
	}

	if *out == "" {
		err = rep.write(stdout)
	} else {
		err = writeReportFile(*out, rep)
		if errors.Is(err, fs.ErrExist) {
			return cannotStart("the report file "+*out+" already exists", "Name another file with -o. tagshelf does not write over a file.")
		}
	}
	if err != nil {
		fmt.Fprintf(log.Writer(), "The report could not be written: %v.\n", err)
		return exitErrors
	}
	rep.summary(log.Writer(), cfg.Destination, *out)
	return exitOK
}

// utf8BOM starts a report written to a file: without it Excel reads a CSV
// file in the system's old code page and garbles every name outside it.
const utf8BOM = "\uFEFF"

// writeReportFile writes the report to a new file at path. A file that is
// there already is left as it is, and the error is fs.ErrExist.
func writeReportFile(path string, rep report) (err error) {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return err
	}
	defer func() {
		if cerr := f.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			os.Remove(path) // half a report is worse than none
		}
	}()
	if _, err := io.WriteString(f, utf8BOM); err != nil {
		return err
	}
	return rep.write(f)
}

// reportColumns are the columns of the report, in order. Every one is a fact
// about the file as the index has it; the report passes no judgement.
var reportColumns = []string{
	"folder", "path", "format", "lossless", "bitrate_kbps", "sample_rate_hz", "bit_depth", "length_seconds", "size_bytes",
	"artist", "album_artist", "album", "title", "year", "track", "disc", "genre", "composer",
	"musicbrainz_release_id", "musicbrainz_recording_id", "manual_marker", "embedded_art", "duplicate_group", "read_error",
}

// report is what `tagshelf report` found in an index.
type report struct {
	rows [][]string

	// The counts of the summary.
	perFolder                   map[store.RootKind]int
	noIDs, noArt, unread        int
	duplicateFiles, duplicateGr int

	// asOf is the last run that went through the whole library, nil when
	// none has.
	asOf *store.Run
}

// reportFolders are the folders the report covers, in the order it lists
// them. The source folder of a run is not one: its files are on their way.
var reportFolders = []store.RootKind{store.RootLibrary, store.RootUntagged, store.RootDuplicates, store.RootOther}

// readReport reads the index of st into a report.
func readReport(st *store.Store) (report, error) {
	files, err := st.Files()
	if err != nil {
		return report{}, fmt.Errorf("reading the index: %w", err)
	}
	groups, err := st.GroupsToReview(engine.PossibleKey)
	if err != nil {
		return report{}, fmt.Errorf("finding the duplicates: %w", err)
	}
	runs, err := st.Runs()
	if err != nil {
		return report{}, fmt.Errorf("reading the run history: %w", err)
	}
	rep := buildReport(files, groups)
	for i := len(runs) - 1; i >= 0 && rep.asOf == nil; i-- {
		r := runs[i]
		whole := r.Kind == store.KindOrganize || r.Kind == store.KindReorganize || r.Kind == store.KindIndex
		if whole && !r.DryRun && !r.Ended.IsZero() {
			rep.asOf = &r
		}
	}
	return rep, nil
}

// buildReport makes the rows and the counts from the indexed files and the
// duplicate groups that wait for a decision. Rows go folder by folder, each
// in path order.
func buildReport(files []store.File, groups []store.Group) report {
	order := map[store.RootKind]int{}
	for i, kind := range reportFolders {
		order[kind] = i + 1
	}
	files = slices.DeleteFunc(slices.Clone(files), func(f store.File) bool { return order[f.RootKind] == 0 })
	slices.SortFunc(files, func(a, b store.File) int {
		return cmp.Or(cmp.Compare(order[a.RootKind], order[b.RootKind]), cmp.Compare(store.PathKey(a.Path), store.PathKey(b.Path)), cmp.Compare(a.ID, b.ID))
	})

	// A file can be in a confirmed group and in a possible one.
	inGroups := map[int64][]string{}
	for i, g := range groups {
		for _, f := range g.Files {
			inGroups[f.ID] = append(inGroups[f.ID], fmt.Sprintf("%d %s", i+1, g.Kind))
		}
	}

	rep := report{perFolder: map[store.RootKind]int{}, duplicateGr: len(groups), duplicateFiles: len(inGroups)}
	for _, f := range files {
		rep.perFolder[f.RootKind]++
		if f.ReleaseID == "" && f.ReleaseTrackID == "" && f.RecordingID == "" {
			rep.noIDs++
		}
		if !f.HasArt {
			rep.noArt++
		}
		if f.ReadError != "" {
			rep.unread++
		}
		rep.rows = append(rep.rows, []string{
			string(f.RootKind), f.Path, f.Format, yesNo(f.Lossless), number(f.Bitrate), number(f.SampleRate), number(f.BitDepth),
			number(int(math.Round(f.Length.Seconds()))), strconv.FormatInt(f.Size, 10),
			f.Artist, f.AlbumArtist, f.Album, f.Title, number(f.Year), number(f.Track), number(f.Disc), f.Genre, f.Composer,
			f.ReleaseID, f.RecordingID, f.Manual, yesNo(f.HasArt), strings.Join(inGroups[f.ID], "; "), f.ReadError,
		})
	}
	return rep
}

func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

// number writes a count or a measure, and nothing for one that is not known.
func number(n int) string {
	if n == 0 {
		return ""
	}
	return strconv.Itoa(n)
}

// write writes the report as CSV: the column names, then a row per file.
func (r report) write(w io.Writer) error {
	out := csv.NewWriter(w)
	if err := out.Write(reportColumns); err != nil {
		return err
	}
	if err := out.WriteAll(r.rows); err != nil { // flushes
		return err
	}
	return out.Error()
}

// summary prints the counts of the report on the library at dest, and how old
// the index is that it was made from. written is the file it went to, empty
// when it was printed.
func (r report) summary(w io.Writer, dest, written string) {
	line := func(name, value string) { fmt.Fprintf(w, "  %-28s %s\n", name+":", value) }
	fmt.Fprintf(w, "Report on the library at %s\n", dest)
	if r.asOf == nil {
		line("Index", "no run has gone through the library yet. Run tagshelf -index first.")
	} else {
		line("Index as of", r.asOf.Ended.Local().Format("2006-01-02 15:04")+". Run tagshelf -index first for a fresh one.")
	}
	total := 0
	var parts []string
	for _, kind := range reportFolders {
		if n := r.perFolder[kind]; n > 0 {
			total += n
			parts = append(parts, fmt.Sprintf("%s %d", kind, n))
		}
	}
	files := strconv.Itoa(total)
	if len(parts) > 0 {
		files += " (" + strings.Join(parts, ", ") + ")"
	}
	line("Files", files)
	line("Without MusicBrainz IDs", strconv.Itoa(r.noIDs))
	line("Without embedded cover art", strconv.Itoa(r.noArt))
	line("In duplicate groups", fmt.Sprintf("%d, in %d %s waiting for a decision", r.duplicateFiles, r.duplicateGr, plural(r.duplicateGr, "group", "groups")))
	line("Not fully read", strconv.Itoa(r.unread))
	if written != "" {
		line("Written to", written)
	}
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}
