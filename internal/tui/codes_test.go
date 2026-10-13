package tui

import (
	"reflect"
	"strings"
	"testing"

	"github.com/zachjamesgreen/tagshelf/internal/musicbrainz"
	"github.com/zachjamesgreen/tagshelf/internal/tagwrite"
)

// lookupWithTags opens the lookup from the Untagged tab on file 104, whose
// tags the source says are tags.
func lookupWithTags(t *testing.T, mb *fakeMB, tags tagwrite.Values) *Model {
	t.Helper()
	src := untaggedSource()
	m, _ := newUntaggedModel(t, src)
	m.mb = mb
	cursorTo(t, m, 104)
	cur, _ := m.untagged.current()
	src.tags = map[string]tagwrite.Values{cur.Path: tags}
	hit(t, m, "l")
	if m.look == nil || m.look.form == nil {
		t.Fatalf("the lookup did not open:\n%s", screen(m))
	}
	return m
}

func TestLookupSearchesByTheFilesISRC(t *testing.T) {
	mb := julie()
	m := lookupWithTags(t, mb, tagwrite.Values{tagwrite.Title: {"Live"}, isrcTag: {"gb-bpw-01-00005", "GBBPW0100006"}})
	// The file's first ISRC is in the form, as MusicBrainz writes one.
	got := screen(m)
	for _, want := range []string{"ISRC", "> GBBPW0100005", "> Live"} {
		if !strings.Contains(got, want) {
			t.Errorf("the query step lacks %q:\n%s", want, got)
		}
	}
	hit(t, m, "enter")
	if len(mb.queries) != 1 || mb.queries[0].ISRC != "GBBPW0100005" || mb.queries[0].Title != "Live" {
		t.Fatalf("searched for %+v, want the ISRC with the title to rank by", mb.queries)
	}
	if m.look.step != stepPick {
		t.Fatalf("no matches to pick from:\n%s", screen(m))
	}
	// More of them are asked for by the same ISRC.
	if m.look.query.ISRC != "GBBPW0100005" {
		t.Errorf("the query kept is %+v", m.look.query)
	}
}

func TestLookupISRCTypedAndRefused(t *testing.T) {
	mb := &fakeMB{}
	m := lookupWithTags(t, mb, tagwrite.Values{tagwrite.Title: {"Live"}, isrcTag: {"not an isrc"}})
	// A tag that does not read as an ISRC is not put in the form.
	if m.look.isrc != "" {
		t.Fatalf("the form took %q for an ISRC", m.look.isrc)
	}
	hit(t, m, "down", "down", "down", "down")
	typeText(t, m, "GBBPW01")
	hit(t, m, "enter")
	if got := screen(m); len(mb.queries) != 0 || !strings.Contains(got, `An ISRC is two letters, three letters or digits and seven digits, as in GBAHT1600302, not "GBBPW01"`) {
		t.Fatalf("half an ISRC: %d searches\n%s", len(mb.queries), got)
	}
	// Typed with hyphens and small letters, it is searched for as it should be.
	hit(t, m, "ctrl+u")
	typeText(t, m, "gb-bpw-01-00005")
	hit(t, m, "enter")
	if len(mb.queries) != 1 || mb.queries[0].ISRC != "GBBPW0100005" {
		t.Fatalf("searched for %+v", mb.queries)
	}
	// MusicBrainz does not have it: the way on is to take it out.
	if got := screen(m); m.look.step != stepQuery || !strings.Contains(got, "MusicBrainz has no recording with the ISRC GBBPW0100005: clear it to search by the other fields") {
		t.Errorf("an ISRC nothing was found for:\n%s", got)
	}

	// An ISRC alone is something to search for; nothing at all is not.
	hit(t, m, "up", "up", "up", "up", "ctrl+u", "enter")
	if len(mb.queries) != 2 || mb.queries[1].Title != "" || mb.queries[1].ISRC != "GBBPW0100005" {
		t.Fatalf("searched for %+v, want the ISRC alone", mb.queries)
	}
	hit(t, m, "down", "down", "down", "down", "ctrl+u", "up", "up", "up", "ctrl+u", "enter")
	if got := screen(m); len(mb.queries) != 2 || !strings.Contains(got, "Nothing to search for: type a title, an artist, an album or an ISRC") {
		t.Errorf("an empty query: %d searches\n%s", len(mb.queries), got)
	}
}

func TestLookupISRCIsNotPutOverOneTyped(t *testing.T) {
	m := lookupWithTags(t, julie(), tagwrite.Values{tagwrite.Title: {"Live"}})
	hit(t, m, "down", "down", "down", "down")
	typeText(t, m, "US")
	if cmd := m.lookupTagged(lookupTaggedMsg{id: m.look.file.ID, isrc: "GBBPW0100005"}); cmd != nil || m.look.isrc != "US" {
		t.Errorf("the file's ISRC went over the one being typed: %q", m.look.isrc)
	}
	// Nor into the lookup of another file.
	hit(t, m, "ctrl+u")
	if cmd := m.lookupTagged(lookupTaggedMsg{id: 999, isrc: "GBBPW0100005"}); cmd != nil || m.look.isrc != "" {
		t.Errorf("another file's ISRC was taken: %q", m.look.isrc)
	}
}

func TestMatchWritesTheISRCsAndTheBarcode(t *testing.T) {
	mb := julie()
	mb.release.Barcode = "5021603101128"
	mb.release.Media[0].Tracks[1].Recording.ISRCs = []string{"GBBPW0100005", "GBBPW0100099"}
	m := lookupWithTags(t, mb, tagwrite.Values{tagwrite.Title: {"Live"}, barcodeTag: {"0000000000000"}})
	toAlbum(t, m)
	hit(t, m, "enter")
	if m.look.step != stepConfirm {
		t.Fatalf("not at the confirm step:\n%s", screen(m))
	}
	got := screen(m)
	for _, want := range []string{"ISRC", "(none)", "-> GBBPW0100005, GBBPW0100099", "Barcode", "0000000000000", "-> 5021603101128"} {
		if !strings.Contains(got, want) {
			t.Errorf("the diff lacks %q:\n%s", want, got)
		}
	}
	v := m.lookupWrites(m.look)
	if !reflect.DeepEqual(v[isrcTag], []string{"GBBPW0100005", "GBBPW0100099"}) || !reflect.DeepEqual(v[barcodeTag], []string{"5021603101128"}) {
		t.Errorf("writes ISRC %q and barcode %q", v[isrcTag], v[barcodeTag])
	}
	// Neither is a tag a file is filed under.
	if tagwrite.Places(tagwrite.Values{isrcTag: v[isrcTag], barcodeTag: v[barcodeTag]}) {
		t.Error("the ISRC and the barcode count as tags a file is filed under")
	}
}

func TestMatchWithoutCodesLeavesTheFilesOwn(t *testing.T) {
	m := lookupWithTags(t, julie(), tagwrite.Values{tagwrite.Title: {"Live"}, isrcTag: {"GBBPW0100005"}, barcodeTag: {"5021603101128"}})
	toAlbum(t, m)
	hit(t, m, "enter")
	v := m.lookupWrites(m.look)
	if _, ok := v[isrcTag]; ok {
		t.Errorf("a match without ISRCs writes %q over the file's", v[isrcTag])
	}
	if _, ok := v[barcodeTag]; ok {
		t.Errorf("a match without a barcode writes %q over the file's", v[barcodeTag])
	}
	if got := screen(m); strings.Contains(got, "Barcode") || strings.Contains(got, "ISRC") {
		t.Errorf("the diff names what is not written:\n%s", got)
	}
}

// matchWithBarcode opens a release match on the rip's folder, whose second
// file holds the barcode code.
func matchWithBarcode(t *testing.T, mb *fakeMB, code string) *Model {
	t.Helper()
	src := matchSource()
	if code != "" {
		src.tags[src.snap.Untagged[1].Path][barcodeTag] = []string{code}
	}
	m, _ := newUntaggedModel(t, src)
	m.mb = mb
	cursorTo(t, m, 101)
	hit(t, m, "L")
	if m.match == nil || m.match.form == nil {
		t.Fatalf("the release match did not open:\n%s", screen(m))
	}
	return m
}

func TestReleaseMatchSearchesByTheFilesBarcode(t *testing.T) {
	mb := geogaddi()
	m := matchWithBarcode(t, mb, "5 021603 101128")
	got := screen(m)
	for _, want := range []string{"Barcode", "> 5021603101128", "> Geogaddi"} {
		if !strings.Contains(got, want) {
			t.Errorf("the query step lacks %q:\n%s", want, got)
		}
	}
	hit(t, m, "enter")
	want := []musicbrainz.ReleaseQuery{{Album: "Geogaddi", Artist: "Boards of Canada", Tracks: 3, Barcode: "5021603101128"}}
	if !reflect.DeepEqual(mb.releaseQueries, want) {
		t.Fatalf("searched for %+v, want %+v", mb.releaseQueries, want)
	}
	// The barcode names the release: no file is searched for beside it.
	if len(mb.boosts) != 1 || len(mb.boosts[0]) != 0 {
		t.Errorf("searched for the files too: %+v", mb.boosts)
	}
	if m.match.step != matchPick {
		t.Errorf("no releases to pick from:\n%s", screen(m))
	}
}

func TestReleaseMatchBarcodeTypedAndRefused(t *testing.T) {
	mb := &fakeMB{}
	m := matchWithBarcode(t, mb, "")
	if m.match.barcode != "" {
		t.Fatalf("a barcode from nowhere: %q", m.match.barcode)
	}
	hit(t, m, "down", "down", "down", "down")
	typeText(t, m, "50216x")
	hit(t, m, "enter")
	if got := screen(m); len(mb.releaseQueries) != 0 || !strings.Contains(got, `A barcode is 8 to 14 digits, not "50216x"`) {
		t.Fatalf("a barcode that is none: %d searches\n%s", len(mb.releaseQueries), got)
	}
	hit(t, m, "ctrl+u")
	typeText(t, m, "5021603101128")
	// A barcode alone is something to search for.
	hit(t, m, "up", "up", "up", "ctrl+u", "up", "ctrl+u", "enter")
	if len(mb.releaseQueries) != 1 || mb.releaseQueries[0].Barcode != "5021603101128" || mb.releaseQueries[0].Album != "" {
		t.Fatalf("searched for %+v, want the barcode alone", mb.releaseQueries)
	}
	if got := screen(m); !strings.Contains(got, "MusicBrainz has no release with the barcode 5021603101128: clear it to search by the other fields") {
		t.Errorf("a barcode nothing was found for:\n%s", got)
	}
}

func TestReleaseMatchDiffNamesTheCodesInOneLine(t *testing.T) {
	mb := geogaddi()
	mb.release.Barcode = "5021603101128"
	m, _ := matchOn(t, mb)
	hit(t, m, "enter")
	got := screen(m)
	if !strings.Contains(got, "and its ISRC, barcode and MusicBrainz IDs") || strings.Contains(got, "-> 5021603101128") {
		t.Errorf("the diff of a release with a barcode:\n%s", got)
	}
	x := m.match
	v := x.values(x.mapping.Assignments[0])
	if !reflect.DeepEqual(v[barcodeTag], []string{"5021603101128"}) {
		t.Errorf("a file gets the barcode %q", v[barcodeTag])
	}
}
