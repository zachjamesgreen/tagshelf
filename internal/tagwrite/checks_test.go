package tagwrite

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"go.senan.xyz/taglib"

	"tagshelf/internal/fixtures"
	"tagshelf/internal/marker"
	"tagshelf/internal/tag"
)

// writeWrong makes Save and Restore write their copy with write for the rest
// of the test.
func writeWrong(t *testing.T, write func(path string, keys map[string][]string, opts taglib.WriteOption) error) {
	t.Helper()
	real := writeTags
	writeTags = write
	t.Cleanup(func() { writeTags = real })
}

// refused checks that a save failed with ErrNotVerified and said why, left
// the original as it was and no copy behind.
func refused(t *testing.T, err error, why, path string, orig []byte) {
	t.Helper()
	if !errors.Is(err, ErrNotVerified) || !strings.Contains(err.Error(), why) {
		t.Errorf("Save = %v, want ErrNotVerified saying %q", err, why)
	}
	if !bytes.Equal(mustRead(t, path), orig) {
		t.Error("the original changed")
	}
	if l := leftovers(t, path); l != nil {
		t.Errorf("temporary files left behind: %v", l)
	}
}

// damageAudio flips one bit of the file at path, at the last place where
// that changes tag.Sum and nothing either reader returns, so that only the
// audio check can tell. It reports whether there is such a place.
func damageAudio(t *testing.T, path string) bool {
	t.Helper()
	b := mustRead(t, path)
	tags := func() (map[string][]string, map[string]interface{}, bool) {
		keys, err := taglib.ReadTags(path)
		if err != nil {
			return nil, nil, false
		}
		md, err := readTags(path)
		if errors.Is(err, tag.ErrNoTagsFound) {
			return keys, nil, true
		}
		if err != nil {
			return nil, nil, false
		}
		return keys, md.Raw(), true
	}
	sum0, err := tag.Sum(bytes.NewReader(b))
	keys0, raw0, ok := tags()
	if err != nil || !ok {
		t.Fatalf("%s cannot be read: %v", path, err)
	}
	// Few places that change the hash are worth trying: go-taglib is slow
	// to start, and in an OGG file its page checksums catch every one.
	for i, tries := len(b)-1, 0; i >= 0 && tries < 8; i -= 5 {
		b[i] ^= 1
		if s, err := tag.Sum(bytes.NewReader(b)); err == nil && s != sum0 {
			tries++
			if err := os.WriteFile(path, b, 0o644); err != nil {
				t.Fatal(err)
			}
			if keys, raw, ok := tags(); ok && reflect.DeepEqual(keys, keys0) && reflect.DeepEqual(raw, raw0) {
				return true
			}
		}
		b[i] ^= 1
	}
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatal(err)
	}
	return false
}

// TestDamagedAudioIsRefused has go-taglib damage the audio of the copy in a
// way no tag reader sees: the save is refused in every format. In an OGG
// file no flipped bit gets past the readers, so there is nothing to try.
func TestDamagedAudioIsRefused(t *testing.T) {
	for ext, from := range taggableExtensions {
		t.Run(ext, func(t *testing.T) {
			path := copyFixture(t, fixtures.Tagged(from), "song"+ext)
			orig := mustRead(t, path)
			damaged := false
			writeWrong(t, func(tmp string, keys map[string][]string, opts taglib.WriteOption) error {
				if err := taglib.WriteTags(tmp, keys, opts); err != nil {
					return err
				}
				damaged = damageAudio(t, tmp)
				return nil
			})
			_, err := Writer{}.Save(path, Values{Title: {"X"}}, marker.New(marker.Hand, day))
			if !damaged {
				if ext != ".ogg" {
					t.Fatal("found no bit to flip that only the audio check can see")
				}
				t.Skip("no flipped bit gets past the readers")
			}
			refused(t, err, "the audio changed", path, orig)
		})
	}
}

// TestSameAudio checks the audio check itself: one changed byte of a WMA
// file's Data Object, a copy that is gone, and an original that tag.Sum
// cannot hash, which only a WMA file is refused for.
func TestSameAudio(t *testing.T) {
	b := mustRead(t, fixtures.Tagged(".wma"))
	path := writeTemp(t, b, "song.wma")
	if err := sameAudio(path, writeTemp(t, b, "same.wma"), true); err != nil {
		t.Errorf("sameAudio of a file and its copy = %v", err)
	}
	damaged := slices.Clone(b)
	damaged[asfHeaderSize(b)+100] ^= 0xff
	for _, strict := range []bool{true, false} {
		if err := sameAudio(path, writeTemp(t, damaged, "damaged.wma"), strict); !errors.Is(err, ErrNotVerified) {
			t.Errorf("sameAudio with a changed Data Object, strict %t = %v, want ErrNotVerified", strict, err)
		}
		if err := sameAudio(path, filepath.Join(t.TempDir(), "missing.wma"), strict); !errors.Is(err, ErrNotVerified) {
			t.Errorf("sameAudio with no copy, strict %t = %v, want ErrNotVerified", strict, err)
		}
	}

	// A WAV file without a data chunk.
	wav := mustRead(t, fixtures.Untagged(".wav"))
	noData := writeTemp(t, wav[:bytes.Index(wav, []byte("data"))], "song.wav")
	if _, err := sum(noData); err == nil {
		t.Fatal("tag.Sum hashed a WAV file without a data chunk")
	}
	if err := sameAudio(noData, noData, false); err != nil {
		t.Errorf("sameAudio of an original without a hash = %v, want nil", err)
	}
	if err := sameAudio(noData, noData, true); !errors.Is(err, ErrNotVerified) {
		t.Errorf("sameAudio of an original without a hash, strict = %v, want ErrNotVerified", err)
	}
}

// TestWrongCopyIsRefused has go-taglib write something other than it was
// given: a save whose copy does not read back as written is not made.
func TestWrongCopyIsRefused(t *testing.T) {
	m := marker.New(marker.Hand, day)
	for _, tt := range []struct {
		name, ext string
		change    func(keys map[string][]string)
		why       string
	}{
		{"another title", ".flac", func(keys map[string][]string) { keys[taglib.Title] = []string{"Other"} }, "title is"},
		{"another title", ".mp3", func(keys map[string][]string) { keys[taglib.Title] = []string{"Other"} }, "title is"},
		{"no marker", ".m4a", func(keys map[string][]string) { delete(keys, marker.Key) }, "marker is"},
		{"another marker", ".ogg", func(keys map[string][]string) { keys[marker.Key] = []string{"hand 2020-01-01"} }, "marker is"},
		{"an artist ID short", ".flac", func(keys map[string][]string) {
			keys[taglib.MusicBrainzArtistID] = keys[taglib.MusicBrainzArtistID][:1]
		}, "musicbrainz_artistid is"},
		{"another artist ID", ".wma", func(keys map[string][]string) {
			keys[taglib.MusicBrainzArtistID] = []string{"aaaaaaaa-0000-4000-8000-000000000005", "aaaaaaaa-0000-4000-8000-00000000000f"}
		}, "musicbrainz_artistid is"},
		{"nothing written", ".wav", func(keys map[string][]string) { clear(keys) }, " is "},
	} {
		t.Run(tt.name+tt.ext, func(t *testing.T) {
			path := copyFixture(t, fixtures.Tagged(tt.ext), "song"+tt.ext)
			orig := mustRead(t, path)
			writeWrong(t, func(tmp string, keys map[string][]string, opts taglib.WriteOption) error {
				tt.change(keys)
				return taglib.WriteTags(tmp, keys, opts)
			})
			_, err := Writer{}.Save(path, musicBrainzSave(), m)
			refused(t, err, tt.why, path, orig)
		})
	}
}

// TestVerifyReader gives the check on what internal/tag reads a file that
// holds other values than those wanted, one field at a time.
func TestVerifyReader(t *testing.T) {
	md := read(t, fixtures.Tagged(".flac"))
	held := Values{
		Title: {fixtures.Title}, Artist: {fixtures.Artist}, AlbumArtist: {fixtures.AlbumArtist}, Album: {fixtures.Album},
		Date: {"2001"}, Track: {"3/10"}, Disc: {"1/2"},
		RecordingID: {fixtures.RecordingID}, ReleaseTrackID: {fixtures.ReleaseTrackID}, AlbumID: {fixtures.ReleaseID},
		ReleaseGroupID: {fixtures.ReleaseGroupID}, ArtistIDs: {fixtures.ArtistID}, AlbumArtistIDs: {fixtures.AlbumArtistID},
	}
	if err := verifyReader(md, held); err != nil {
		t.Fatalf("verifyReader with the values of the file = %v", err)
	}
	other := Values{
		Title: {"Other"}, Artist: {"Other"}, AlbumArtist: {"Other"}, Album: {}, Date: {"2002-01-01"}, Track: {"3/11"}, Disc: {"2/2"},
		RecordingID: {"x"}, ReleaseTrackID: {}, AlbumID: {"x"}, ReleaseGroupID: {"x"},
		ArtistIDs: {fixtures.ArtistID, "x"}, AlbumArtistIDs: {},
	}
	for field, vs := range other {
		err := verifyReader(md, Values{field: vs})
		if !errors.Is(err, ErrNotVerified) || !strings.Contains(err.Error(), string(field)+" reads as") {
			t.Errorf("verifyReader wanting %s %q = %v, want ErrNotVerified naming the field", field, vs, err)
		}
	}
}

// TestCheckMarker gives the marker check a file with another marker, with
// one where none is wanted, and with none where one is.
func TestCheckMarker(t *testing.T) {
	marked, plain := read(t, fixtures.Manual(".flac")), read(t, fixtures.Tagged(".flac"))
	for _, tt := range []struct {
		name string
		md   tag.Metadata
		want []string
		ok   bool
	}{
		{"the marker wanted", marked, []string{fixtures.ManualMarker}, true},
		{"none wanted, none there", plain, []string{}, true},
		{"another day", marked, []string{"hand 2026-09-28"}, false},
		{"another method", marked, []string{"musicbrainz 2026-09-27"}, false},
		{"none wanted", marked, nil, false},
		{"none there", plain, []string{fixtures.ManualMarker}, false},
	} {
		err := checkMarker(tt.md, tt.want, "the marker")
		if tt.ok && err != nil || !tt.ok && !errors.Is(err, ErrNotVerified) {
			t.Errorf("%s: checkMarker = %v, want refused: %t", tt.name, err, !tt.ok)
		}
	}
}

// TestVerify gives the whole check files that go-taglib reads as wanted and
// internal/tag does not: a WMA file's marker, which go-taglib never reads,
// and the order of its artist IDs, which go-taglib reads rotated.
func TestVerify(t *testing.T) {
	path := copyFixture(t, fixtures.Tagged(".wma"), "song.wma")
	ids := []string{"aaaaaaaa-0000-4000-8000-000000000005", "aaaaaaaa-0000-4000-8000-000000000006", "aaaaaaaa-0000-4000-8000-000000000007"}
	m := marker.New(marker.Hand, day)
	res, err := Writer{}.Save(path, Values{ArtistIDs: ids}, m)
	if err != nil {
		t.Fatal(err)
	}
	f := formats[".wma"]
	if err := verify(f, path, res.After); err != nil {
		t.Fatalf("verify of what was saved = %v", err)
	}
	for name, want := range map[string]Values{
		"another marker":     {Marker: {"musicbrainz 2026-09-28"}},
		"no marker":          {Marker: {}},
		"IDs in other order": {ArtistIDs: {ids[1], ids[0], ids[2]}},
		"other IDs":          {ArtistIDs: {ids[0], ids[1], "x"}},
		"another title":      {Title: {"Other"}},
	} {
		if err := verify(f, path, want); !errors.Is(err, ErrNotVerified) {
			t.Errorf("%s: verify = %v, want ErrNotVerified", name, err)
		}
	}
	if err := verify(formats[".mp3"], path, Values{}); !errors.Is(err, ErrNotVerified) {
		t.Errorf("verify of a WMA file as .mp3 = %v, want ErrNotVerified", err)
	}

	// A file without tags passes only when nothing is wanted.
	for _, ext := range []string{".mp3", ".wav", ".aac", ".ape", ".mpc"} {
		bare := copyFixture(t, fixtures.Untagged(ext), "song"+ext)
		if err := verify(formats[ext], bare, Values{Title: {}, Marker: {}}); err != nil {
			t.Errorf("%s: verify of a file without tags, nothing wanted = %v", ext, err)
		}
		if err := verify(formats[ext], bare, Values{Marker: {m.String()}}); !errors.Is(err, ErrNotVerified) {
			t.Errorf("%s: verify of a file without tags, a marker wanted = %v, want ErrNotVerified", ext, err)
		}
	}
}

// TestVerifyAPE gives the check on the APEv2 side of an .aac file a tag
// that went stale behind the ID3v2 tag's back.
func TestVerifyAPE(t *testing.T) {
	m := marker.New(marker.Hand, day)
	saved := func(t *testing.T) (string, Values) {
		path := copyFixture(t, fixtures.Tagged(".aac"), "song.aac")
		res, err := Writer{}.Save(path, Values{Title: {"T"}, Track: {"5/7"}}, m)
		if err != nil {
			t.Fatal(err)
		}
		if err := verify(formats[".aac"], path, res.After); err != nil {
			t.Fatalf("verify of what was saved = %v", err)
		}
		return path, res.After
	}
	for name, tt := range map[string]struct {
		set map[string][]string
		why string
	}{
		"another title":  {map[string][]string{"Title": {"Stale"}}, "APEv2 title is"},
		"a second key":   {map[string][]string{"Date": {"1999"}}, ""}, // not a field that was written
		"no track":       {map[string][]string{"Track": nil}, "APEv2 track is"},
		"another marker": {map[string][]string{marker.Key: {"hand 2020-01-01"}}, "the APEv2 marker reads as"},
		"no marker":      {map[string][]string{marker.Key: nil}, "the APEv2 marker reads as"},
	} {
		t.Run(name, func(t *testing.T) {
			path, after := saved(t)
			setAPEItems(t, path, tt.set)
			err := verify(formats[".aac"], path, after)
			if tt.why == "" {
				if err != nil {
					t.Errorf("verify = %v, want nil", err)
				}
				return
			}
			if !errors.Is(err, ErrNotVerified) || !strings.Contains(err.Error(), tt.why) {
				t.Errorf("verify = %v, want ErrNotVerified saying %q", err, tt.why)
			}
		})
	}

	t.Run("no APEv2 tag", func(t *testing.T) {
		path, after := saved(t)
		b := mustRead(t, path)
		if err := os.WriteFile(path, b[:bytes.Index(b, []byte("APETAGEX"))], 0o644); err != nil {
			t.Fatal(err)
		}
		if err := verify(formats[".aac"], path, after); !errors.Is(err, ErrNotVerified) || !strings.Contains(err.Error(), "the APEv2 tag") {
			t.Errorf("verify = %v, want ErrNotVerified naming the APEv2 tag", err)
		}
	})
}

// failingHistory makes the save and then fails to record it, as a commit
// that fails does.
type failingHistory struct{ err error }

func (h failingHistory) RecordTagWrite(_ int64, _, _, _ string, apply func() error) error {
	if err := apply(); err != nil {
		return err
	}
	return h.err
}

// TestRecordFailsAfterTheRename checks that a save whose record fails once
// the file is replaced reports the failure and leaves no copy behind. The
// file has the new tags by then.
func TestRecordFailsAfterTheRename(t *testing.T) {
	path := copyFixture(t, fixtures.Tagged(".flac"), "song.flac")
	failed := errors.New("commit failed")
	_, err := Writer{History: failingHistory{failed}}.Save(path, Values{Title: {"X"}}, marker.New(marker.Hand, day))
	if !errors.Is(err, failed) || !strings.Contains(err.Error(), "song.flac") {
		t.Errorf("Save = %v, want the record's error, naming the file", err)
	}
	if l := leftovers(t, path); l != nil {
		t.Errorf("temporary files left behind: %v", l)
	}
	if got := read(t, path).Title(); got != "X" {
		t.Errorf("Title() = %q, want the save made", got)
	}
}

// TestRenameRefused saves to a file another program holds open, which
// Windows does not let a rename replace.
func TestRenameRefused(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("only Windows refuses to replace an open file")
	}
	path := copyFixture(t, fixtures.Tagged(".flac"), "song.flac")
	orig := mustRead(t, path)
	held, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()
	hist := &history{}
	_, err = Writer{History: hist}.Save(path, Values{Title: {"X"}}, marker.New(marker.Hand, day))
	if err == nil || errors.Is(err, ErrNotVerified) || !strings.Contains(err.Error(), "song.flac") {
		t.Errorf("Save = %v, want the rename's error, naming the file", err)
	}
	if len(hist.rows) != 0 {
		t.Errorf("the save was recorded: %+v", hist.rows)
	}
	if !bytes.Equal(mustRead(t, path), orig) {
		t.Error("the original changed")
	}
	if l := leftovers(t, path); l != nil {
		t.Errorf("temporary files left behind: %v", l)
	}
}

// meddlingHistory writes the file itself just before the rename, as another
// program might while the copy is being written.
type meddlingHistory struct {
	path string
	data []byte
}

func (h meddlingHistory) RecordTagWrite(_ int64, _, _, _ string, apply func() error) error {
	if err := os.WriteFile(h.path, h.data, 0o644); err != nil {
		return err
	}
	return apply()
}

// TestFileChangedMeanwhile checks that a file written by something else
// between the copy and the rename keeps what that wrote.
func TestFileChangedMeanwhile(t *testing.T) {
	path := copyFixture(t, fixtures.Tagged(".flac"), "song.flac")
	other := mustRead(t, fixtures.Manual(".flac"))
	_, err := Writer{History: meddlingHistory{path, other}}.Save(path, Values{Title: {"X"}}, marker.New(marker.Hand, day))
	if !errors.Is(err, errChangedMeanwhile) {
		t.Errorf("Save = %v, want errChangedMeanwhile", err)
	}
	if !bytes.Equal(mustRead(t, path), other) {
		t.Error("the file does not hold what the other program wrote")
	}
	if l := leftovers(t, path); l != nil {
		t.Errorf("temporary files left behind: %v", l)
	}
}

// TestCopyCannotBeWritten saves to a file in a folder that is gone by the
// time of the copy.
func TestCopyCannotBeWritten(t *testing.T) {
	path := copyFixture(t, fixtures.Tagged(".flac"), "song.flac")
	orig, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	gone := filepath.Join(filepath.Dir(path), "gone", "song.flac")
	if _, err := copyTemp(gone); err == nil {
		t.Error("copyTemp of a missing file succeeded")
	}
	if err := (Writer{}).apply(formats[".flac"], gone, Values{}, Values{Marker: {"hand 2026-09-28"}}); err == nil {
		t.Error("apply to a missing file succeeded")
	}
	if l := leftovers(t, path); l != nil {
		t.Errorf("temporary files left behind: %v", l)
	}
	if now, err := os.Stat(path); err != nil || !now.ModTime().Equal(orig.ModTime()) {
		t.Errorf("the file beside it was touched: %v", err)
	}
}

// TestReadOnlyFile saves to and restores a read-only file, which Windows
// does not let a rename replace: the tags are written and the file is still
// read-only.
func TestReadOnlyFile(t *testing.T) {
	path := copyFixture(t, fixtures.Tagged(".flac"), "song.flac")
	if err := os.Chmod(path, 0o444); err != nil {
		t.Fatal(err)
	}
	readOnly := func(when string) {
		t.Helper()
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm()&0o200 != 0 {
			t.Errorf("%s the file is writable: %v", when, info.Mode())
		}
	}

	res, err := Writer{}.Save(path, Values{Title: {"X"}}, marker.New(marker.Hand, day))
	if err != nil {
		t.Fatal(err)
	}
	if got := read(t, path).Title(); got != "X" {
		t.Errorf("Title() = %q, want X", got)
	}
	readOnly("after the save")
	if _, err := (Writer{}).Restore(path, res.Before, res.After); err != nil {
		t.Fatal(err)
	}
	if got := read(t, path).Title(); got != fixtures.Title {
		t.Errorf("Title() after the restore = %q, want %q", got, fixtures.Title)
	}
	readOnly("after the restore")
	if l := leftovers(t, path); l != nil {
		t.Errorf("temporary files left behind: %v", l)
	}
}

// TestReadOnlyFileKeptWhenTheRenameFails checks that a read-only file whose
// save fails at the rename is read-only afterwards.
func TestReadOnlyFileKeptWhenTheRenameFails(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("only Windows refuses to replace an open file")
	}
	path := copyFixture(t, fixtures.Tagged(".flac"), "song.flac")
	if err := os.Chmod(path, 0o444); err != nil {
		t.Fatal(err)
	}
	held, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()
	if _, err := (Writer{}).Save(path, Values{Title: {"X"}}, marker.New(marker.Hand, day)); err == nil {
		t.Fatal("Save to an open file succeeded")
	}
	if info, err := os.Stat(path); err != nil || info.Mode().Perm()&0o200 != 0 {
		t.Errorf("the file is writable after the failed save: %v, %v", info.Mode(), err)
	}
	if l := leftovers(t, path); l != nil {
		t.Errorf("temporary files left behind: %v", l)
	}
}

// TestRemoveStaleCopies checks that only the copies a crashed save left are
// removed: files named as a save names its copy, of a format that is tagged,
// in the folder itself, and old.
func TestRemoveStaleCopies(t *testing.T) {
	dir := t.TempDir()
	old := time.Now().Add(-2 * time.Hour)
	file := func(name string, at time.Time) string {
		t.Helper()
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(path, at, at); err != nil {
			t.Fatal(err)
		}
		return path
	}
	stale := []string{file(".tagshelf-1234567890.flac", old), file(".tagshelf-22.MP3", old), file(".tagshelf-333.wma", old)}
	if err := os.Chmod(stale[2], 0o444); err != nil { // a copy that had its original's permissions already
		t.Fatal(err)
	}
	kept := []string{
		file(".tagshelf-4444.flac", time.Now()),               // a save going on now
		file(".tagshelf-55555.flac", old.Add(90*time.Minute)), // half an hour old
		file(".tagshelf-library", old),                        // the library's ID
		file(".tagshelf-666.txt", old),                        // not a format that is tagged
		file(".tagshelf-777", old),
		file(".tagshelf-x88.flac", old), // not a name a save gives
		file("tagshelf-999.flac", old),
		file("song.flac", old),
		file(filepath.Join("Album", ".tagshelf-1010.flac"), old), // a folder below
	}
	folder := filepath.Join(dir, ".tagshelf-1111.flac")
	if err := os.Mkdir(folder, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(folder, old, old); err != nil {
		t.Fatal(err)
	}

	removed, err := RemoveStaleCopies(dir, StaleAfter)
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(removed)
	slices.Sort(stale)
	if !slices.Equal(removed, stale) {
		t.Errorf("removed %q, want %q", removed, stale)
	}
	for _, path := range stale {
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s is still there: %v", filepath.Base(path), err)
		}
	}
	for _, path := range append(kept, folder) {
		if _, err := os.Stat(path); err != nil {
			t.Errorf("%s was removed: %v", filepath.Base(path), err)
		}
	}

	if _, err := RemoveStaleCopies(filepath.Join(dir, "missing"), StaleAfter); err == nil {
		t.Error("RemoveStaleCopies of a missing folder = nil")
	}
}

// TestStaleCopyIsACopyOfASave checks the name RemoveStaleCopies looks for
// against the copies a save really makes.
func TestStaleCopyIsACopyOfASave(t *testing.T) {
	for ext, from := range taggableExtensions {
		path := copyFixture(t, fixtures.Tagged(from), "song"+ext)
		tmp, err := copyTemp(path)
		if err != nil {
			t.Fatal(err)
		}
		old := time.Now().Add(-2 * StaleAfter)
		if err := os.Chtimes(tmp, old, old); err != nil {
			t.Fatal(err)
		}
		removed, err := RemoveStaleCopies(filepath.Dir(path), StaleAfter)
		if err != nil || !slices.Equal(removed, []string{tmp}) {
			t.Errorf("%s: removed %q, %v; want %q", ext, removed, err, tmp)
		}
	}
}
