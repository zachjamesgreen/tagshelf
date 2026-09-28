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
	got := BoostQueries(files)
	var titles []string
	for _, q := range got {
		titles = append(titles, q.Title)
	}
	if want := []string{"A", "B", "C"}; !reflect.DeepEqual(titles, want) {
		t.Errorf("boost titles = %v, want %v", titles, want)
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
	want := "/ws/2/release/d1a2b3c4-5e6f-4a7b-8c9d-0e1f2a3b4c5d?fmt=json&inc=artist-credits+recordings+release-groups+media"
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
			"ties go to more boost files, then official albums",
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
}

func TestSearchReleaseCandidatesReplaysFixtures(t *testing.T) {
	rp := &replay{t: t}
	srv := httptest.NewServer(rp)
	defer srv.Close()
	c, _ := testClient(t, srv)

	q := *fixtures[4].release
	boost := []Query{fixtures[0].query, fixtures[2].query, fixtures[3].query}
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

func TestSearchReleaseCandidatesNeedsSomethingToSearch(t *testing.T) {
	srv := httptest.NewServer(&replay{t: t})
	defer srv.Close()
	c, _ := testClient(t, srv)
	if _, err := c.SearchReleaseCandidates(context.Background(), ReleaseQuery{}, nil, nil); err == nil {
		t.Error("expected an error for an empty release match")
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
	rp := &replay{t: t}
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

// lookupFixture decodes a recorded release lookup.
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
