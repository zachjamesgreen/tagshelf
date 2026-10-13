package musicbrainz

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestReleaseQueryFor(t *testing.T) {
	tests := []struct {
		desc  string
		files []FileInfo
		want  ReleaseQuery
	}{
		{"nothing", nil, ReleaseQuery{}},
		{
			"shared tags, tagged total beats selection size",
			[]FileInfo{
				{Title: "Genesis", Artist: "Dua Lipa", Album: "Dua Lipa", Year: 2017, TrackTotal: 12},
				{Title: "IDGAF", Artist: "Dua Lipa", Album: "Dua Lipa", Year: 2017, TrackTotal: 12},
			},
			ReleaseQuery{Album: "Dua Lipa", Artist: "Dua Lipa", Tracks: 12, Year: 2017},
		},
		{
			"various artists: no artist shared by more than half",
			[]FileInfo{
				{Title: "New Rules", Artist: "Dua Lipa", Album: "Hits"},
				{Title: "Shape of You", Artist: "Ed Sheeran", Album: "Hits"},
				{Title: "Feels", Artist: "Calvin Harris", Album: "Hits"},
				{Title: "Dreams", Artist: "Dua Lipa", Album: "Hits"},
			},
			ReleaseQuery{Album: "Hits", Tracks: 4},
		},
		{
			"several discs: per-disc totals are ignored",
			[]FileInfo{
				{Title: "a", Album: "X", Disc: 1, TrackTotal: 12},
				{Title: "b", Album: "X", Disc: 2, TrackTotal: 5},
			},
			ReleaseQuery{Album: "X", Tracks: 2},
		},
		{
			"untagged files take the folder hint",
			[]FileInfo{
				{Path: "01 - Genesis.mp3", Folder: "Dua Lipa - Dua Lipa (2017)"},
				{Path: "02 - Lost in Your Light.mp3", Folder: "Dua Lipa - Dua Lipa (2017)"},
				{Path: "03 - Hotter Than Hell.mp3", Folder: "Dua Lipa - Dua Lipa (2017)"},
			},
			ReleaseQuery{Album: "Dua Lipa", Artist: "Dua Lipa", Tracks: 3},
		},
	}
	for _, tt := range tests {
		if got := ReleaseQueryFor(tt.files); got != tt.want {
			t.Errorf("%s: got %+v, want %+v", tt.desc, got, tt.want)
		}
	}
}

func TestBoostQueries(t *testing.T) {
	files := []FileInfo{
		{},
		{Title: "A"},
		{Path: "02 - B.mp3"},
		{Title: "C", Length: time.Minute},
		{Title: "D"},
	}
	// The first three files with a title, whether or not the release query
	// holds anything: what was typed is not something to search a file by.
	for _, rq := range []ReleaseQuery{{}, {Album: "Typed", Artist: "Typed", Tracks: 5, Year: 2001}} {
		got := BoostQueries(files, rq)
		var titles []string
		for _, q := range got {
			titles = append(titles, q.Title)
		}
		if want := []string{"A", "B", "C"}; !reflect.DeepEqual(titles, want) {
			t.Errorf("with %+v: boost titles = %v, want %v", rq, titles, want)
		}
	}
}

// What a boost query takes from the release query typed, the file's tags,
// its name and its folder's name.
func TestBoostQueryPrecedence(t *testing.T) {
	typed := ReleaseQuery{Album: "Geogaddi", Artist: "Boards of Canada", Tracks: 23, Year: 2002}
	tests := []struct {
		desc string
		file FileInfo
		rq   ReleaseQuery
		want Query
		skip bool
	}{
		{
			desc: "album: the typed one beats the tag",
			file: FileInfo{Title: "Julie and Candy", Artist: "Boards of Canada", Album: "Unknown Album"},
			rq:   ReleaseQuery{Album: "Geogaddi"},
			want: Query{Title: "Julie and Candy", Artist: "Boards of Canada", Album: "Geogaddi"},
		},
		{
			desc: "album: none typed leaves the tag",
			file: FileInfo{Title: "Julie and Candy", Album: "Geogaddi"},
			rq:   ReleaseQuery{Artist: "Boards of Canada"},
			want: Query{Title: "Julie and Candy", Artist: "Boards of Canada", Album: "Geogaddi"},
		},
		{
			desc: "album: the folder's name is never used",
			file: FileInfo{Path: "05 - Julie and Candy.flac", Folder: "Boards of Canada - Geogaddi (2002)"},
			rq:   ReleaseQuery{Artist: "Boards of Canada"},
			want: Query{Title: "Julie and Candy", Artist: "Boards of Canada", Track: 5},
		},
		{
			desc: "artist: the typed one beats the tag",
			file: FileInfo{Title: "Track 5", Artist: "Unknown Artist", Album: "Unknown Album", Track: 5},
			rq:   typed,
			want: Query{Title: "Track 5", Artist: "Boards of Canada", Album: "Geogaddi", Track: 5},
		},
		{
			desc: "artist: the one in the file name beats the typed one",
			file: FileInfo{Path: "02 - Lisa Gerrard - Sorrow.flac", Folder: "Gladiator"},
			rq:   ReleaseQuery{Album: "Gladiator", Artist: "Hans Zimmer"},
			want: Query{Title: "Sorrow", Artist: "Lisa Gerrard", Album: "Gladiator", Track: 2},
		},
		{
			desc: "artist: the one in the file name beats the tag",
			file: FileInfo{Artist: "Various Artists", Track: 3, Path: "01 - Lisa Gerrard - Sorrow.flac"},
			want: Query{Title: "Sorrow", Artist: "Lisa Gerrard", Track: 3},
		},
		{
			desc: "artist: a file with a title tag is not searched by its name",
			file: FileInfo{Title: "Sorrow", Path: "02 - Lisa Gerrard - Sorrow.flac", Folder: "Hans Zimmer - Gladiator"},
			rq:   ReleaseQuery{Artist: "Hans Zimmer"},
			want: Query{Title: "Sorrow", Artist: "Hans Zimmer"},
		},
		{
			desc: "artist: the typed one beats the folder's name",
			file: FileInfo{Path: "05 Track 05.flac", Folder: "Somebody Else - Something Else"},
			rq:   typed,
			want: Query{Title: "Track 05", Artist: "Boards of Canada", Album: "Geogaddi", Track: 5},
		},
		{
			desc: "artist: none typed leaves the tag, which beats the folder's name",
			file: FileInfo{Artist: "Low", Path: "01 Words.flac", Folder: "Dirty Three - Split EP"},
			rq:   ReleaseQuery{Album: "Split EP"},
			want: Query{Title: "Words", Artist: "Low", Album: "Split EP", Track: 1},
		},
		{
			desc: "artist: none typed, tagged or in the file name falls back on the folder's name",
			file: FileInfo{Path: "01 Words.flac", Folder: "Low - Split EP"},
			rq:   ReleaseQuery{Album: "Split EP"},
			want: Query{Title: "Words", Artist: "Low", Album: "Split EP", Track: 1},
		},
		{
			desc: "spaces are nothing typed",
			file: FileInfo{Title: "Words", Artist: "Low", Album: "Split EP"},
			rq:   ReleaseQuery{Album: "  ", Artist: " "},
			want: Query{Title: "Words", Artist: "Low", Album: "Split EP"},
		},
		{
			desc: "the file's own year, numbers and length go along; the release query's do not",
			file: FileInfo{Title: "Words", Year: 1994, Track: 1, TrackTotal: 11, Disc: 2, Length: 345 * time.Second},
			rq:   typed,
			want: Query{Title: "Words", Artist: "Boards of Canada", Album: "Geogaddi", Year: 1994, Track: 1, TrackTotal: 11, Length: 345 * time.Second},
		},
		{
			desc: "a file with no title would only repeat what was typed",
			file: FileInfo{Artist: "Low", Album: "Split EP", Length: time.Minute, Folder: "Low - Split EP"},
			rq:   typed,
			skip: true,
		},
		{
			desc: "a file with no title and nothing typed",
			file: FileInfo{Artist: "Low", Length: time.Minute},
			skip: true,
		},
	}
	for _, tt := range tests {
		got := BoostQueries([]FileInfo{tt.file}, tt.rq)
		switch {
		case tt.skip:
			if len(got) != 0 {
				t.Errorf("%s: got %+v, want the file skipped", tt.desc, got)
			}
		case len(got) != 1 || got[0] != tt.want:
			t.Errorf("%s:\n got %+v\nwant %+v", tt.desc, got, tt.want)
		}
	}
}

func TestReleaseQueryLucene(t *testing.T) {
	tests := []struct {
		q    ReleaseQuery
		want string
	}{
		{ReleaseQuery{}, ""},
		{ReleaseQuery{Year: 2017}, ""},
		{ReleaseQuery{Album: "Dua Lipa", Artist: "Dua Lipa", Tracks: 17, Year: 2017}, "release:(dua lipa) artist:(dua lipa) tracks:(17)"},
		{ReleaseQuery{Album: "Back in Black", Artist: "AC/DC"}, `release:(back in black) artist:(ac\/dc)`},
		// A barcode is searched for alone, in both the ways it may be written.
		{ReleaseQuery{Album: "Dua Lipa", Tracks: 17, Barcode: "190295819984"}, "barcode:(190295819984 OR 0190295819984)"},
		{ReleaseQuery{Barcode: "0190295819984"}, "barcode:(0190295819984 OR 190295819984)"},
		{ReleaseQuery{Barcode: "5099749423862"}, "barcode:(5099749423862)"},
		{ReleaseQuery{Barcode: "12345670"}, "barcode:(12345670)"},
	}
	for _, tt := range tests {
		if got := tt.q.Lucene(); got != tt.want {
			t.Errorf("%+v.Lucene() = %q, want %q", tt.q, got, tt.want)
		}
	}
}

func TestReleaseLookupURL(t *testing.T) {
	got, err := releaseLookupURL("/ws/2", "D1A2B3C4-5E6F-4A7B-8C9D-0E1F2A3B4C5D")
	if err != nil {
		t.Fatal(err)
	}
	want := "/ws/2/release/d1a2b3c4-5e6f-4a7b-8c9d-0e1f2a3b4c5d?fmt=json&inc=artist-credits+recordings+release-groups+media+genres+isrcs"
	if got != want {
		t.Errorf("url = %s, want %s", got, want)
	}
	for _, bad := range []string{"", "not-an-id", "../recording/x", deluxeID + "/"} {
		if _, err := releaseLookupURL("/ws/2", bad); err == nil {
			t.Errorf("releaseLookupURL(%q) accepted", bad)
		}
	}
}

func TestReleaseFormat(t *testing.T) {
	tests := []struct {
		formats []string
		want    string
	}{
		{nil, ""},
		{[]string{"CD"}, "CD"},
		{[]string{"CD", "CD"}, "2×CD"},
		{[]string{"CD", "CD", "DVD-Video"}, "2×CD + DVD-Video"},
		{[]string{""}, "(unknown)"},
	}
	for _, tt := range tests {
		var r Release
		for _, f := range tt.formats {
			r.Media = append(r.Media, Medium{Format: f})
		}
		if got := r.Format(); got != tt.want {
			t.Errorf("Format(%v) = %q, want %q", tt.formats, got, tt.want)
		}
	}
}

// searchRelease is a release search hit.
func searchRelease(id, title, artist string, tracks int, date string) Release {
	return Release{
		ID: id, Title: title, TrackCount: tracks, Date: date, Status: "Official",
		ArtistCredit: credit(artist),
		ReleaseGroup: ReleaseGroup{PrimaryType: "Album"},
	}
}

// hit is a recording search hit on the given releases.
func hit(releases ...Release) Recording {
	return Recording{ID: "rec", Title: "t", Releases: releases}
}

func TestRankReleases(t *testing.T) {
	std := searchRelease("std", "Album", "Band", 12, "2017-06-02")
	dlx := searchRelease("dlx", "Album", "Band", 17, "2017-06-02")
	other := searchRelease("oth", "Other Album", "Other Band", 12, "1999")
	// A release only the recording searches found, without an artist credit.
	single := Release{ID: "sgl", Title: "Song", TrackCount: 1, ReleaseGroup: ReleaseGroup{PrimaryType: "Single"}}

	type want struct {
		id           string
		score, found int
	}
	tests := []struct {
		desc     string
		q        ReleaseQuery
		releases []Release
		hits     [][]Recording
		want     []want
	}{
		{
			// Album 5, artist 4, tracks 4, year 4: std 17/17, dlx 13/17,
			// other: album 1-6/11 of 5, artist 1-6/10 of 4, tracks 4, year 0:
			// 7.87/17.
			"release search only",
			ReleaseQuery{Album: "Album", Artist: "Band", Tracks: 12, Year: 2017},
			[]Release{dlx, other, std},
			nil,
			[]want{{"std", 100, 0}, {"dlx", 76, 0}, {"oth", 46, 0}},
		},
		{
			// dlx found by all three: (5+4+4+13)/26; std by one: (5+4+13/3)/26.
			// The single, found by one file, scores 1/3 of 13 and 0 album
			// similarity ("album" vs "song") and a wrong track count: 4.33/22.
			"boosted by recording searches",
			ReleaseQuery{Album: "Album", Artist: "Band", Tracks: 17},
			[]Release{std, dlx},
			[][]Recording{
				{hit(dlx, std)},
				{hit(dlx), hit(single, dlx)}, // two hits on dlx count once
				{hit(dlx)},
			},
			[]want{{"dlx", 100, 3}, {"std", 51, 1}, {"sgl", 20, 1}},
		},
		{
			// Nothing typed: the share of the files' searches that found
			// a release is all of its score.
			"recording searches alone",
			ReleaseQuery{},
			[]Release{single, std, dlx},
			[][]Recording{{hit(std, dlx)}, {hit(dlx)}},
			[]want{{"dlx", 100, 2}, {"std", 50, 1}, {"sgl", 0, 0}},
		},
	}
	for _, tt := range tests {
		got := RankReleases(tt.q, tt.releases, tt.hits, nil)
		var ids []want
		for _, c := range got {
			ids = append(ids, want{c.Release.ID, c.Score, c.Found})
			if c.Searched != len(tt.hits) {
				t.Errorf("%s: %s searched = %d, want %d", tt.desc, c.Release.ID, c.Searched, len(tt.hits))
			}
		}
		if !reflect.DeepEqual(ids, tt.want) {
			t.Errorf("%s:\n got %v\nwant %v", tt.desc, ids, tt.want)
		}
	}
}

func TestRankReleasesTieBreaks(t *testing.T) {
	a := searchRelease("a", "Album", "Band", 10, "")
	b := searchRelease("b", "Album", "Band", 10, "")
	b.ReleaseGroup.SecondaryTypes = []string{"Live"}
	c := searchRelease("c", "Album", "Band", 10, "")
	q := ReleaseQuery{Album: "Album"}

	got := RankReleases(q, []Release{b, a, c}, nil, func(id string) bool { return id == "c" })
	var ids []string
	for _, cand := range got {
		ids = append(ids, cand.Release.ID)
	}
	// Official albums first (a, c), the library's c before a, then the live b.
	if want := []string{"c", "a", "b"}; !reflect.DeepEqual(ids, want) {
		t.Errorf("order = %v, want %v", ids, want)
	}
	if !got[0].InLibrary || got[1].InLibrary {
		t.Errorf("InLibrary not set as reported: %+v", got)
	}

	// Before either of those: the release more of the files' searches found.
	// Album 5 and found 13: "more" is one letter in eight like the album and
	// found by two of three, (0.625 + 8.67)/18; "fewer" has the album's name
	// and is found by one, (5 + 4.33)/18. Both show 52, and everything else
	// speaks for "fewer": it is first, official and in the library.
	q = ReleaseQuery{Album: "abcdefgh"}
	more := Release{ID: "more", Title: "axxxxxxx"}
	fewer := searchRelease("fewer", "abcdefgh", "Band", 10, "")
	hits := [][]Recording{{hit(more, fewer)}, {hit(more)}, {}}
	got = RankReleases(q, []Release{fewer, more}, hits, func(id string) bool { return id == "fewer" })
	if len(got) != 2 || got[0].Score != 52 || got[1].Score != 52 {
		t.Fatalf("the two releases do not tie at 52: %+v", got)
	}
	if got[0].Release.ID != "more" || got[0].Found != 2 || got[1].Found != 1 {
		t.Errorf("the release found by 2 of 3 is not first: %s (found %d), %s (found %d)",
			got[0].Release.ID, got[0].Found, got[1].Release.ID, got[1].Found)
	}
}

func TestSearchReleaseCandidatesReplaysFixtures(t *testing.T) {
	rp := newReplay(t)
	srv := httptest.NewServer(rp)
	defer srv.Close()
	c, _ := testClient(t, srv)

	q := *duaLipaSearch.release
	boost := []Query{blowYourMind.query, newRules.query, dreams.query}
	got, err := c.SearchReleaseCandidates(context.Background(), q, boost, nil)
	if err != nil {
		t.Fatal(err)
	}
	if n := rp.requests.Load(); n != 4 {
		t.Errorf("sent %d requests, want 4 (one release search, three recording searches)", n)
	}
	if len(got) == 0 || got[0].Release.ID != deluxeID {
		t.Fatalf("best candidate is not the deluxe edition: %+v", got)
	}
	if got[0].Found != 3 || got[0].Searched != 3 || got[0].Score != 100 {
		t.Errorf("deluxe candidate = %+v", got[0])
	}
	if f := got[0].Release.Format(); f != "2×CD" {
		t.Errorf("deluxe format = %q", f)
	}
	// Releases only the recording searches found are offered too.
	var single bool
	for _, cand := range got {
		if cand.Release.Title == "New Rules" {
			single = true
		}
	}
	if !single {
		t.Error("the New Rules single, found only by a recording search, is missing")
	}
}

// Searches that were made and found nothing are not "nothing to search for":
// there are no candidates, and no error.
func TestSearchReleaseCandidatesFindingNothing(t *testing.T) {
	var requests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		_, _ = w.Write([]byte(`{"count":0,"offset":0,"releases":[],"recordings":[]}`))
	}))
	defer srv.Close()
	c, _ := testClient(t, srv)
	ctx := context.Background()

	got, err := c.SearchReleaseCandidates(ctx, ReleaseQuery{Album: "An album nobody has"}, nil, nil)
	if err != nil || len(got) != 0 {
		t.Errorf("a release search that found nothing = %+v, %v; want nothing and no error", got, err)
	}
	if n := requests.Load(); n != 1 {
		t.Errorf("sent %d requests, want 1", n)
	}
	// The same with a file's search along, and with the files' searches alone.
	got, err = c.SearchReleaseCandidates(ctx, ReleaseQuery{Album: "Another nobody has"}, []Query{{Title: "x"}}, nil)
	if err != nil || len(got) != 0 {
		t.Errorf("with a recording search = %+v, %v; want nothing and no error", got, err)
	}
	got, err = c.SearchReleaseCandidates(ctx, ReleaseQuery{}, []Query{{Title: "y"}}, nil)
	if err != nil || len(got) != 0 {
		t.Errorf("recording searches alone = %+v, %v; want nothing and no error", got, err)
	}
	if n := requests.Load(); n != 4 {
		t.Errorf("sent %d requests, want 4", n)
	}
}

// A release match is over at its first search that fails: the error says
// which, and no search after it is sent.
func TestSearchReleaseCandidatesStopsAtAFailedSearch(t *testing.T) {
	tests := []struct {
		desc     string
		fail     int // the request that fails, counting from 1
		want     string
		requests int32
	}{
		{"the release search", 1, "MusicBrainz release search: HTTP 500", 1},
		{"the second file's search", 3, "MusicBrainz recording search: HTTP 500", 3},
	}
	for _, tt := range tests {
		var requests atomic.Int32
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if int(requests.Add(1)) == tt.fail {
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			_, _ = w.Write([]byte(`{"count":0,"offset":0,"releases":[],"recordings":[]}`))
		}))
		c, _ := testClient(t, srv)
		boost := []Query{{Title: "a"}, {Title: "b"}, {Title: "c"}}
		got, err := c.SearchReleaseCandidates(context.Background(), ReleaseQuery{Album: "X"}, boost, nil)
		if err == nil || !strings.Contains(err.Error(), tt.want) || got != nil {
			t.Errorf("%s fails: got %+v, %v; want an error saying %q", tt.desc, got, err, tt.want)
		}
		if n := requests.Load(); n != tt.requests {
			t.Errorf("%s fails: sent %d requests, want %d", tt.desc, n, tt.requests)
		}
		srv.Close()
	}
}

func TestSearchReleasesSendsQuery(t *testing.T) {
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.URL.Path + "?" + r.URL.RawQuery
		_, _ = w.Write([]byte(`{"count":0,"offset":25,"releases":[]}`))
	}))
	defer srv.Close()
	c, _ := testClient(t, srv)

	if _, err := c.SearchReleases(context.Background(), ReleaseQuery{Album: "X", Tracks: 3}, 25); err != nil {
		t.Fatal(err)
	}
	want := "/ws/2/release?fmt=json&limit=25&offset=25&query=release%3A%28x%29+tracks%3A%283%29"
	if got != want {
		t.Errorf("request = %s, want %s", got, want)
	}
}

func TestLookupReleaseReplaysFixture(t *testing.T) {
	rp := newReplay(t)
	srv := httptest.NewServer(rp)
	defer srv.Close()
	c, _ := testClient(t, srv)

	for range 2 {
		rel, err := c.LookupRelease(context.Background(), deluxeID)
		if err != nil {
			t.Fatal(err)
		}
		if rel.ID != deluxeID || len(rel.Media) != 2 || rel.ReleaseGroup.ID == "" || len(rel.ArtistCredit) == 0 {
			t.Fatalf("release decoded badly: %+v", rel)
		}
		tr := rel.Track(TrackRef{Medium: 0, Index: 1})
		if tr.Recording == nil || tr.Recording.ID == "" || len(tr.ArtistCredit) != 2 {
			t.Errorf("track decoded without recording or credits: %+v", tr)
		}
	}
	if n := rp.requests.Load(); n != 1 {
		t.Errorf("sent %d requests, want 1 (the second from the cache)", n)
	}
	if _, err := c.LookupRelease(context.Background(), "nope"); err == nil || !strings.Contains(err.Error(), "release ID") {
		t.Errorf("bad ID error = %v", err)
	}
}

// lookupFixture decodes a hand-written release lookup from testdata.
func lookupFixture(t *testing.T, file string) *Release {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", file))
	if err != nil {
		t.Fatal(err)
	}
	var rel Release
	if err := json.Unmarshal(b, &rel); err != nil {
		t.Fatal(err)
	}
	return &rel
}

// A barcode searched for scores by whether it is the release's, however the
// two are written, with the weight of a title.
func TestRankReleasesByBarcode(t *testing.T) {
	mine := searchRelease("mine", "Album", "Band", 12, "2017-06-02")
	mine.Barcode = "0190295819984"
	reissue := searchRelease("reissue", "Album", "Band", 12, "2019")
	reissue.Barcode = "190295819999"
	bare := searchRelease("bare", "Album", "Band", 12, "2017")

	got := RankReleases(ReleaseQuery{Barcode: "190295819984"}, []Release{bare, reissue, mine}, nil, nil)
	if len(got) != 3 || got[0].Release.ID != "mine" || got[0].Score != 100 || got[1].Score != 0 || got[2].Score != 0 {
		t.Errorf("by the barcode alone: %+v", got)
	}
	// Album 5 and barcode 13: the others have the album only, 5/18.
	got = RankReleases(ReleaseQuery{Album: "Album", Barcode: "190295819984"}, []Release{bare, reissue, mine}, nil, nil)
	if got[0].Release.ID != "mine" || got[0].Score != 100 || got[1].Score != 28 {
		t.Errorf("by the barcode and the album: %+v", got)
	}
}

func TestSearchReleasesByBarcodeSendsItAlone(t *testing.T) {
	var query string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query = r.URL.Query().Get("query")
		_, _ = w.Write([]byte(`{"count":1,"releases":[{"id":"` + deluxeID + `","title":"Dua Lipa","barcode":"190295819984"}]}`))
	}))
	defer srv.Close()
	c, _ := testClient(t, srv)
	res, err := c.SearchReleases(context.Background(), ReleaseQuery{Album: "Dua Lipa", Artist: "Dua Lipa", Tracks: 17, Barcode: "190295819984"}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if query != "barcode:(190295819984 OR 0190295819984)" {
		t.Errorf("query = %q", query)
	}
	if len(res.Releases) != 1 || res.Releases[0].Barcode != "190295819984" {
		t.Errorf("releases = %+v", res.Releases)
	}
}
