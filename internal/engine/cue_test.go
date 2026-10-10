package engine

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"tagshelf/internal/store"
)

// A dry run plans the rewrite of a cue sheet and counts it, and leaves the
// sheet as it is, with no .orig beside it.
func TestDryRunLeavesCueAlone(t *testing.T) {
	cue := "FILE \"Song.mp3\" MP3\r\n"
	check := func(t *testing.T, sheet string, c *collector, finish RunFinished) {
		t.Helper()
		if got := readTestFile(t, sheet); got != cue {
			t.Errorf("a dry run rewrote the cue sheet: %q", got)
		}
		mustNotExist(t, sheet+".orig")
		if finish.Counters.CueRewritten != 1 {
			t.Errorf("CueRewritten = %d, want the planned rewrite counted", finish.Counters.CueRewritten)
		}
		planned := c.count(func(e Event) bool {
			r, ok := e.(CueRewritten)
			return ok && len(r.Renames) == 1 && r.Renames[0] == Rename{From: "Song.mp3", To: "01 - Song.mp3"}
		})
		if planned != 1 {
			t.Errorf("%d CueRewritten events with the planned rename, want 1", planned)
		}
	}

	t.Run("reorganize", func(t *testing.T) {
		lib := t.TempDir()
		album := filepath.Join(lib, "Singer", "Album")
		writeTaggedMP3(t, filepath.Join(album, "Song.mp3"), songTags, theAudio)
		writeTestFile(t, filepath.Join(album, "album.cue"), cue)
		var c collector
		finish := Run(context.Background(), Options{Source: lib, Destination: lib, Workers: 1, DryRun: true, Sink: c.sink})
		check(t, filepath.Join(album, "album.cue"), &c, finish)
	})
	t.Run("organize", func(t *testing.T) {
		tmp := t.TempDir()
		source, lib := filepath.Join(tmp, "src"), filepath.Join(tmp, "lib")
		writeTaggedMP3(t, filepath.Join(source, "rip", "Song.mp3"), songTags, theAudio)
		writeTestFile(t, filepath.Join(source, "rip", "album.cue"), cue)
		var c collector
		finish := Run(context.Background(), Options{Source: source, Destination: lib, Workers: 1, DryRun: true, Sink: c.sink})
		check(t, filepath.Join(source, "rip", "album.cue"), &c, finish)
		mustNotExist(t, lib)
	})
}

// The forms a cue sheet's FILE lines come in.
func TestCueForms(t *testing.T) {
	tags := func(title, track string) id3Tags {
		return id3Tags{albumArtist: "Singer", album: "Album", title: title, track: track}
	}
	for _, tt := range []struct {
		name, cue, want string
		rewritten       int64
		warning         WarningKind // when not rewritten
	}{
		{"byte order mark", "\xef\xbb\xbfFILE \"one.mp3\" MP3\r\n  TRACK 01 AUDIO\r\n",
			"\xef\xbb\xbfFILE \"01 - One.mp3\" MP3\r\n  TRACK 01 AUDIO\r\n", 1, 0},
		{"unquoted name", "FILE one.mp3 MP3\n  TRACK 01 AUDIO\n",
			"FILE \"01 - One.mp3\" MP3\n  TRACK 01 AUDIO\n", 1, 0},
		{"two files, lower case command, no final newline", "file \"one.mp3\" MP3\n  TRACK 01 AUDIO\nfile \"two.mp3\" MP3\n  TRACK 02 AUDIO",
			"file \"01 - One.mp3\" MP3\n  TRACK 01 AUDIO\nfile \"02 - Two.mp3\" MP3\n  TRACK 02 AUDIO", 1, 0},
		{"latin-1 text elsewhere in the sheet", "PERFORMER \"Bj\xf6rk\"\nFILE \"one.mp3\" MP3\n",
			"PERFORMER \"Bj\xf6rk\"\nFILE \"01 - One.mp3\" MP3\n", 1, 0},
		{"latin-1 sheet that would need a name outside ASCII", "PERFORMER \"Bj\xf6rk\"\nFILE \"accent.mp3\" MP3\n",
			"PERFORMER \"Bj\xf6rk\"\nFILE \"accent.mp3\" MP3\n", 0, WarnCueEncoding},
		{"a path in the name", "FILE \"sub\\one.mp3\" MP3\n", "FILE \"sub\\one.mp3\" MP3\n", 0, WarnCueNotBeside},
		{"a file that is nowhere", "FILE \"missing.flac\" WAVE\n", "FILE \"missing.flac\" WAVE\n", 0, WarnCueNotBeside},
		{"a file the run did not handle, beside the sheet", "FILE \"one.mp3\" MP3\nFILE \"notes.txt\" BINARY\n",
			"FILE \"01 - One.mp3\" MP3\nFILE \"notes.txt\" BINARY\n", 1, 0},
		{"names that are right already", "FILE \"01 - One.mp3\" MP3\n", "FILE \"01 - One.mp3\" MP3\n", 0, -1},
	} {
		tmp := t.TempDir()
		source, lib := filepath.Join(tmp, "src"), filepath.Join(tmp, "lib")
		album := filepath.Join(lib, "Singer", "Album")
		first := "one.mp3"
		if tt.name == "names that are right already" {
			first = "01 - One.mp3"
		}
		writeTaggedMP3(t, filepath.Join(source, "rip", first), tags("One", "1"), "audio one")
		writeTaggedMP3(t, filepath.Join(source, "rip", "two.mp3"), tags("Two", "2"), "audio two")
		writeTaggedMP3(t, filepath.Join(source, "rip", "accent.mp3"), tags("Café", "3"), "audio three")
		writeTestFile(t, filepath.Join(source, "rip", "album.cue"), tt.cue)
		// Already in the library, where the sheet is going.
		writeTestFile(t, filepath.Join(album, "notes.txt"), "notes")

		var c collector
		finish := Run(context.Background(), Options{Source: source, Destination: lib, Workers: 1, Sink: c.sink})
		if finish.Counters.Errors != 0 || finish.Counters.CueRewritten != tt.rewritten {
			t.Errorf("%s: run = %+v, want %d sheets rewritten", tt.name, finish, tt.rewritten)
		}
		if got := readTestFile(t, filepath.Join(album, "album.cue")); got != tt.want {
			t.Errorf("%s: cue = %q, want %q", tt.name, got, tt.want)
		}
		_, err := os.Stat(filepath.Join(album, "album.cue.orig"))
		if hasOrig := err == nil; hasOrig != (tt.rewritten == 1) {
			t.Errorf("%s: an original kept: %t, want one only beside a rewritten sheet", tt.name, hasOrig)
		}
		if tt.rewritten == 0 && tt.warning >= 0 {
			if w := c.warnings(tt.warning); len(w) != 1 || w[0].Path != filepath.Join(album, "album.cue") {
				t.Errorf("%s: warnings of kind %d = %+v, want one for the sheet", tt.name, tt.warning, w)
			}
		}
	}
}

// A cue sheet whose audio the run placed in another folder than the sheet is
// left unchanged, with a warning.
func TestCueLeftAloneWhenAudioIsElsewhere(t *testing.T) {
	tmp := t.TempDir()
	source, lib := filepath.Join(tmp, "src"), filepath.Join(tmp, "lib")
	writeTaggedMP3(t, filepath.Join(source, "rip", "one.mp3"), id3Tags{albumArtist: "Singer", album: "Album", title: "One", track: "1"}, "audio one")
	writeTaggedMP3(t, filepath.Join(source, "rip", "two.mp3"), id3Tags{albumArtist: "Singer", album: "Another", title: "Two", track: "2"}, "audio two")
	cue := "FILE \"one.mp3\" MP3\nFILE \"two.mp3\" MP3\n"
	writeTestFile(t, filepath.Join(source, "rip", "album.cue"), cue)

	var c collector
	finish := Run(context.Background(), Options{Source: source, Destination: lib, Workers: 1, Sink: c.sink})
	// Audio of two albums: the sheet belongs to neither and goes to _Other.
	moved := filepath.Join(lib, "_Other", "rip", "album.cue")
	if got := readTestFile(t, moved); got != cue {
		t.Errorf("a cue whose audio is not next to it must be left unchanged, got %q", got)
	}
	mustNotExist(t, moved+".orig")
	if finish.Counters.CueRewritten != 0 || finish.Counters.Organized != 2 {
		t.Errorf("run = %+v, want both songs organized and no cue rewritten", finish)
	}
	if w := c.warnings(WarnCueNotBeside); len(w) != 1 || w[0].Path != moved || w[0].Name != "one.mp3" {
		t.Errorf("warnings = %+v, want one naming the first file that is elsewhere", w)
	}
}

// A cue sheet rewritten by two runs: the first original stays in .orig, each
// run records what the sheet held before and after its own rewrite, and
// undoing a run gives the sheet back what it held before that run. Undoing
// the later run used to restore the sheet from the earlier run's .orig, which
// left it naming a file that is not there and used the .orig up.
func TestCueRewrittenTwiceIsUndoneRunByRun(t *testing.T) {
	src, lib, s, first := organizedRip(t)
	album := filepath.Join(lib, "Singer", "Album")
	cue, orig := filepath.Join(album, "album.cue"), filepath.Join(album, "album.cue.orig")
	original, afterFirst, afterSecond := cueOf("Song.mp3"), cueOf("01 - Song.mp3"), cueOf("01 - Tune.mp3")

	// The song is retitled and a reorganize renames it.
	writeTaggedMP3(t, filepath.Join(album, "01 - Song.mp3"), id3Tags{albumArtist: "Singer", album: "Album", title: "Tune", track: "1"}, theAudio)
	if finish := Run(context.Background(), Options{Source: lib, Destination: lib, Workers: 1, Store: s}); finish.State != StateOK || finish.Counters.CueRewritten != 1 {
		t.Fatalf("reorganize = %+v, want the cue sheet rewritten again", finish)
	}
	second := lastRun(t, s)
	if got := readTestFile(t, cue); got != afterSecond {
		t.Fatalf("the cue sheet after the second run = %q", got)
	}
	// Finding 54: the second rewrite must not write over the first original.
	if got := readTestFile(t, orig); got != original {
		t.Fatalf("the original after the second rewrite = %q, want the first one kept", got)
	}
	moves, err := s.Moves(second.ID)
	if err != nil {
		t.Fatal(err)
	}
	var rewrite *store.Move
	for i := range moves {
		if moves[i].Reason == store.ReasonCueRewrite {
			rewrite = &moves[i]
		}
	}
	if rewrite == nil || string(rewrite.CueBefore) != afterFirst || string(rewrite.CueAfter) != afterSecond {
		t.Fatalf("the second run's cue rewrite = %+v, want it recorded with the sheet before and after", rewrite)
	}

	check, restore := noTags(t)
	if undo := Undo(context.Background(), Options{Destination: lib, Store: s}, second.ID, check, restore); undo.State != StateOK || undo.Counters.CueRewritten != 1 {
		t.Fatalf("undo of the second run = %+v", undo)
	}
	mustExist(t, filepath.Join(album, "01 - Song.mp3"))
	if got := readTestFile(t, cue); got != afterFirst {
		t.Errorf("the cue sheet after undoing the second run = %q, want it as the first run left it: %q", got, afterFirst)
	}
	if got := readTestFile(t, orig); got != original {
		t.Errorf("the original after undoing the second run = %q, want the first run's kept for its own undo", got)
	}

	plan, err := PlanUndo(s, first.ID, check)
	if err != nil || plan.Cues != 1 || len(plan.Skipped) != 0 {
		t.Fatalf("plan to undo the first run = %+v, %v; want its cue sheet restored too", plan, err)
	}
	if undo := Undo(context.Background(), Options{Destination: lib, Store: s}, first.ID, check, restore); undo.State != StateOK {
		t.Fatalf("undo of the first run = %+v", undo)
	}
	if got := readTestFile(t, filepath.Join(src, "rip", "album.cue")); got != original {
		t.Errorf("the cue sheet back in the source = %q, want the original", got)
	}
	mustNotExist(t, filepath.Join(src, "rip", "album.cue.orig"), orig, album)
}

// A cue rewrite recorded before runs kept what they changed is undone as it
// was then: the .orig takes the sheet's place.
func TestUndoOfACueRewriteRecordedWithoutItsText(t *testing.T) {
	lib := filepath.Join(t.TempDir(), "lib")
	album := filepath.Join(lib, "Singer", "Album")
	cue := filepath.Join(album, "album.cue")
	writeTestFile(t, cue, cueOf("01 - Song.mp3"))
	writeTestFile(t, cue+".orig", cueOf("Song.mp3"))
	s := openStore(t)
	if _, err := s.SetRoot(store.RootLibrary, lib); err != nil {
		t.Fatal(err)
	}
	run, err := s.StartRun(store.KindReorganize, false, lib, lib, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := s.RecordMove(run, cue, cue+".orig", store.ReasonCueRewrite); err != nil {
		t.Fatal(err)
	}
	if err := s.FinishRun(run, store.Counters{CueRewritten: 1}, store.StateOK, time.Now()); err != nil {
		t.Fatal(err)
	}
	check, restore := noTags(t)
	undo := Undo(context.Background(), Options{Destination: lib, Store: s}, run, check, restore)
	if undo.State != StateOK || undo.Counters.CueRewritten != 1 || undo.NotUndone != "" {
		t.Fatalf("undo = %+v", undo)
	}
	if got := readTestFile(t, cue); got != cueOf("Song.mp3") {
		t.Errorf("the cue sheet = %q, want its original back", got)
	}
	mustNotExist(t, cue+".orig")

	// Without the .orig there is nothing to restore such a rewrite from.
	again, err := s.StartRun(store.KindReorganize, false, lib, lib, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := s.RecordMove(again, cue, cue+".orig", store.ReasonCueRewrite); err != nil {
		t.Fatal(err)
	}
	if err := s.FinishRun(again, store.Counters{CueRewritten: 1}, store.StateOK, time.Now()); err != nil {
		t.Fatal(err)
	}
	plan, err := PlanUndo(s, again, check)
	if err != nil || !plan.Nothing() || len(plan.Skipped) != 1 || !strings.Contains(plan.Skipped[0].Reason, ".orig, is gone") || plan.Skipped[0].Later {
		t.Errorf("plan without the .orig = %+v, %v; want the cue sheet skipped for good", plan, err)
	}
}
