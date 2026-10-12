package tagwrite

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"go.senan.xyz/taglib"

	"github.com/zachjamesgreen/tagshelf/internal/fixtures"
	"github.com/zachjamesgreen/tagshelf/internal/marker"
)

// TestOtherTags saves, to a file of every format, tags that have no field of
// their own: one value, a list, one of several lines and one with a space in
// its name, with an AcoustID ID. Each reads back as written, the file gets
// no marker, and a tag given no value is taken off.
func TestOtherTags(t *testing.T) {
	lyrics := "first line\nsecond line"
	for ext, from := range taggableExtensions {
		t.Run(ext, func(t *testing.T) {
			path := copyFixture(t, tagged(t, from), "song"+ext)
			audio := audioSum(t, path)
			want := Values{
				Other("MOOD"): {"calm"}, Other("LABEL"): {"Warp", "Skam"}, Other("LYRICS"): {lyrics}, Other("MY TAG"): {"x; y"},
				AcoustID: {"aaaaaaaa-0000-4000-8000-00000000000a"},
			}
			if ext == ".wma" {
				// A WMA file has no place for a name with a space in it: the
				// save is refused, and says so, and the file is as it was.
				orig := mustRead(t, path)
				var cannot *CannotHoldError
				if _, err := (Writer{}).SaveUnmarked(path, want); !errors.As(err, &cannot) || cannot.Name != "MY TAG" || !errors.Is(err, ErrNotVerified) {
					t.Errorf("a tag named MY TAG = %v, want it refused as one the file cannot hold", err)
				}
				if string(mustRead(t, path)) != string(orig) {
					t.Error("the refused save changed the file")
				}
				delete(want, Other("MY TAG"))
			}
			res, err := Writer{}.SaveUnmarked(path, want)
			if err != nil {
				t.Fatal(err)
			}
			if !res.Changed {
				t.Error("the save changed nothing")
			}
			got, err := ReadAll(path)
			if err != nil {
				t.Fatal(err)
			}
			for field, vs := range want {
				have := got[field]
				if ext == ".wma" && len(vs) > 1 {
					// TagLib reads a list of a WMA file turned by one.
					have, vs = slices.Sorted(slices.Values(have)), slices.Sorted(slices.Values(vs))
				}
				if !slices.Equal(have, vs) {
					t.Errorf("%s reads %q, want %q", field, got[field], vs)
				}
			}
			if len(got[Marker]) != 0 || got[Title][0] != fixtures.Title {
				t.Errorf("the marker is %q and the title %q", got[Marker], got[Title])
			}
			if now := audioSum(t, path); now != audio {
				t.Error("the audio changed")
			}
			if res, err := (Writer{}).SaveUnmarked(path, Values{Other("MOOD"): {"calm"}}); err != nil || res.Changed {
				t.Errorf("a second save of a value the file has: changed %t, %v", res.Changed, err)
			}

			if _, err := (Writer{}).SaveUnmarked(path, Values{Other("MOOD"): {}, Other("LABEL"): {"Warp"}, AcoustID: {}}); err != nil {
				t.Fatal(err)
			}
			got, err = ReadAll(path)
			if err != nil {
				t.Fatal(err)
			}
			if _, ok := got[Other("MOOD")]; ok || !slices.Equal(got[Other("LABEL")], []string{"Warp"}) || len(got[AcoustID]) != 0 ||
				!slices.Equal(got[Other("LYRICS")], []string{lyrics}) {
				t.Errorf("after taking two off and shortening a list: %v", got)
			}
		})
	}
}

// TestReadAllLeavesOutWhatHasAField checks that the tags with a field of
// their own are not listed a second time among the others, and that a
// picture is named as a tag that is not text.
func TestReadAllLeavesOutWhatHasAField(t *testing.T) {
	// The smallest PNG there is: one transparent pixel.
	png := []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR\x00\x00\x00\x01\x00\x00\x00\x01\x08\x06\x00\x00\x00\x1f\x15\xc4\x89" +
		"\x00\x00\x00\rIDATx\x9cc\x00\x01\x00\x00\x05\x00\x01\r\n-\xb4\x00\x00\x00\x00IEND\xaeB`\x82")
	for _, ext := range []string{".mp3", ".flac", ".m4a", ".ogg"} {
		t.Run(ext, func(t *testing.T) {
			path := copyFixture(t, fixtures.Tagged(ext), "song"+ext)
			if err := taglib.WriteImage(path, png); err != nil {
				t.Fatal(err)
			}
			got, err := ReadAll(path)
			if err != nil {
				t.Fatal(err)
			}
			for field := range got {
				if name, ok := field.OtherName(); ok && taken[name] {
					t.Errorf("%s is listed among the other tags", name)
				}
			}
			if len(got[NotText]) == 0 {
				t.Errorf("the picture is not named among the tags that are not text: %v", got)
			}
			for field, vs := range got {
				if len(strings.Join(vs, "")) > 1000 {
					t.Errorf("%s holds %d characters: a picture read as text?", field, len(strings.Join(vs, "")))
				}
			}
			// The picture is still there after a save.
			if _, err := (Writer{}).SaveUnmarked(path, Values{Other("MOOD"): {"calm"}}); err != nil {
				t.Fatal(err)
			}
			if img, err := taglib.ReadImage(path); err != nil || len(img) != len(png) {
				t.Errorf("after a save the picture is %d bytes (%v), want %d", len(img), err, len(png))
			}
		})
	}
}

func TestNewOther(t *testing.T) {
	for name, want := range map[string]Field{"mood": Other("MOOD"), " My Tag_2 ": Other("MY TAG_2"), "X": Other("X")} {
		if got, err := NewOther(name); err != nil || got != want {
			t.Errorf("NewOther(%q) = %q, %v; want %q", name, got, err, want)
		}
	}
	for _, name := range []string{"", "  ", "a=b", "tag:x", "émotion", "title", "Genre", "tracktotal", "TAGSHELF_MANUAL", "musicbrainz_albumid", "coverart"} {
		if got, err := NewOther(name); err == nil {
			t.Errorf("NewOther(%q) = %q, want it refused", name, got)
		}
	}
	// A save refuses the name of a tag that has a field, however it came by it.
	path := copyFixture(t, fixtures.Tagged(".flac"), "song.flac")
	for _, field := range []Field{Other("TITLE"), Other(""), NotText} {
		if _, err := (Writer{}).SaveUnmarked(path, Values{field: {"x"}}); err == nil || errors.Is(err, ErrNotVerified) {
			t.Errorf("a save of %q = %v, want it refused before anything is written", field, err)
		}
	}
}

// TestSetMarker checks the marker set by hand: it is written as given in
// place of the one a save would write, an empty one takes it off, and one
// that says neither how nor when is refused.
func TestSetMarker(t *testing.T) {
	path := copyFixture(t, fixtures.Manual(".flac"), "song.flac")
	auto := marker.New(marker.Hand, day)

	res, err := Writer{}.Save(path, Values{Title: {"New"}, SetMarker: {" MusicBrainz  2020-01-02 "}}, auto)
	if err != nil {
		t.Fatal(err)
	}
	if got := markerOf(t, path); got != "musicbrainz 2020-01-02" {
		t.Errorf("the marker is %q, want the one set by hand", got)
	}
	if !slices.Equal(res.After[Marker], []string{"musicbrainz 2020-01-02"}) || !slices.Equal(res.Before[Marker], []string{fixtures.ManualMarker}) {
		t.Errorf("the record of the save: before %v, after %v", res.Before, res.After)
	}
	if _, ok := res.After[SetMarker]; ok {
		t.Error("the record names the marker twice")
	}

	// Taken off, with nothing else changed, by a save that marks nothing.
	res, err = Writer{}.SaveUnmarked(path, Values{SetMarker: {}})
	if err != nil || !res.Changed {
		t.Fatalf("taking the marker off: changed %t, %v", res.Changed, err)
	}
	if got := markerOf(t, path); got != "" {
		t.Errorf("the marker is still %q", got)
	}
	if res, err := (Writer{}).SaveUnmarked(path, Values{SetMarker: {}}); err != nil || res.Changed {
		t.Errorf("taking it off a file that has none: changed %t, %v", res.Changed, err)
	}
	// And the undo of that puts it back.
	if _, err := (Writer{}).Restore(path, res.Before, res.After); err != nil {
		t.Fatal(err)
	}

	for _, bad := range [][]string{{"hand"}, {"2026-10-12"}, {"picard 2026-10-12"}, {"hand yesterday"}, {"hand 2026-10-12", "hand 2026-10-13"}} {
		if _, err := (Writer{}).SaveUnmarked(path, Values{SetMarker: bad}); err == nil || errors.Is(err, ErrNotVerified) {
			t.Errorf("a marker of %q = %v, want it refused", bad, err)
		}
	}
}

// A WMA file can be given a marker by hand, and cannot lose one.
func TestSetMarkerOnWMA(t *testing.T) {
	path := copyFixture(t, fixtures.Tagged(".wma"), "song.wma")
	if _, err := (Writer{}).SaveUnmarked(path, Values{SetMarker: {"hand 2026-10-12"}}); err != nil {
		t.Fatal(err)
	}
	if got := markerOf(t, path); got != "hand 2026-10-12" {
		t.Fatalf("the marker is %q", got)
	}
	orig := mustRead(t, path)
	if _, err := (Writer{}).SaveUnmarked(path, Values{SetMarker: {}}); !errors.Is(err, ErrMarkerStays) {
		t.Errorf("taking the marker off a WMA file = %v, want ErrMarkerStays", err)
	}
	if string(mustRead(t, path)) != string(orig) {
		t.Error("the refused save changed the file")
	}
}
