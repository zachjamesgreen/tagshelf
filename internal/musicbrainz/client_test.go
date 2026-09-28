package musicbrainz

import (
	"context"
	"errors"
	"flag"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// Recorded responses live in testdata and are served by httptest. To refresh
// them from the live service (one request per second, a handful of requests):
//
//	go test ./internal/musicbrainz -run TestRecordFixtures -record -contact you@example.com
//
// Tests only assert what a re-recording keeps true, so refreshing should not
// need test changes unless MusicBrainz's data itself moved.
var (
	record  = flag.Bool("record", false, "refresh testdata from the live MusicBrainz API")
	contact = flag.String("contact", "", "musicbrainz_contact to send when recording")
)

// fixture is one recorded response: a recording search, a release search
// (release set) or a release lookup (lookup set).
type fixture struct {
	file    string // under testdata
	query   Query
	release *ReleaseQuery
	lookup  string
	offset  int
}

// The release lookups use hand-authored MBIDs, so -record reports them as not
// found until they are replaced with real releases.
var fixtures = []fixture{
	{file: "recording-blow-your-mind.json", query: Query{Title: "Blow Your Mind", Artist: "Dua Lipa", Length: 178 * time.Second}},
	{file: "recording-no-hits.json", query: Query{Title: "zzqx tagshelf no such recording", Artist: "qqzx nobody"}},
	{file: "recording-new-rules.json", query: Query{Title: "New Rules", Artist: "Dua Lipa", Length: 209 * time.Second}},
	{file: "recording-dreams.json", query: Query{Title: "Dreams", Artist: "Dua Lipa"}},
	{file: "release-search-dua-lipa.json", release: &ReleaseQuery{Album: "Dua Lipa", Artist: "Dua Lipa", Tracks: 17}},
	{file: "release-dua-lipa-deluxe.json", lookup: deluxeID},
	{file: "release-various-artists.json", lookup: variousID},
}

// Release IDs of the recorded lookups.
const (
	deluxeID  = "d1a2b3c4-5e6f-4a7b-8c9d-0e1f2a3b4c5d"
	variousID = "5c6d7e8f-9a0b-4c1d-8e2f-3a4b5c6d7e8f"
)

// url is the fixture's request URL under base.
func (f fixture) url(base string) (string, error) {
	switch {
	case f.lookup != "":
		return releaseLookupURL(base, f.lookup)
	case f.release != nil:
		return releaseSearchURL(base, *f.release, f.offset)
	default:
		return recordingSearchURL(base, f.query, f.offset)
	}
}

func (f fixture) requestURI(t *testing.T) string {
	t.Helper()
	u, err := f.url("/ws/2")
	if err != nil {
		t.Fatal(err)
	}
	return u
}

func TestRecordFixtures(t *testing.T) {
	if !*record {
		t.Skip("run with -record to refresh testdata from musicbrainz.org")
	}
	c, err := New(*contact)
	if err != nil {
		t.Fatalf("-record needs -contact: %v", err)
	}
	for _, f := range fixtures {
		u, err := f.url(DefaultBaseURL)
		if err != nil {
			t.Fatal(err)
		}
		body, err := c.fetch(context.Background(), u)
		if err != nil {
			t.Errorf("%s: %v", f.file, err)
			continue
		}
		if err := os.WriteFile(filepath.Join("testdata", f.file), body, 0o644); err != nil {
			t.Fatal(err)
		}
		t.Logf("recorded %s", f.file)
	}
}

// replay serves the recorded fixtures and counts requests; anything else is
// a 404 so an unexpected query shows up as a test failure.
type replay struct {
	t        *testing.T
	requests atomic.Int32
	agents   chan string
}

func (rp *replay) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	rp.requests.Add(1)
	select {
	case rp.agents <- r.UserAgent():
	default:
	}
	for _, f := range fixtures {
		if r.URL.RequestURI() == f.requestURI(rp.t) {
			body, err := os.ReadFile(filepath.Join("testdata", f.file))
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(body)
			return
		}
	}
	w.WriteHeader(http.StatusNotFound)
	_, _ = w.Write([]byte(`{"error":"Not Found","help":"For usage, please see: https://musicbrainz.org/development/mmd"}`))
}

// testClient returns a client for srv with its own limiter on a fake clock.
func testClient(t *testing.T, srv *httptest.Server) (*Client, *fakeClock) {
	t.Helper()
	c, err := New("tests@example.com")
	if err != nil {
		t.Fatal(err)
	}
	clk := newFakeClock()
	c.baseURL = srv.URL + "/ws/2"
	c.http = srv.Client()
	c.limiter = newLimiter(clk)
	return c, clk
}

func TestNewWithoutContactIsDisabled(t *testing.T) {
	for _, contact := range []string{"", "   "} {
		c, err := New(contact)
		if !errors.Is(err, ErrDisabled) || c != nil {
			t.Errorf("New(%q) = %v, %v; want nil, ErrDisabled", contact, c, err)
		}
	}
	if !strings.Contains(ErrDisabled.Error(), "musicbrainz_contact") {
		t.Errorf("disabled error does not name the config key: %v", ErrDisabled)
	}
}

func TestUserAgent(t *testing.T) {
	// Test binaries have no module version, so the version is "dev".
	if got, want := UserAgent("me@example.com"), "tagshelf/dev ( me@example.com )"; got != want {
		t.Errorf("UserAgent = %q, want %q", got, want)
	}
}

func TestSearchRecordingsReplaysFixture(t *testing.T) {
	rp := &replay{t: t, agents: make(chan string, 1)}
	srv := httptest.NewServer(rp)
	defer srv.Close()
	c, _ := testClient(t, srv)

	res, err := c.SearchRecordings(context.Background(), fixtures[0].query, 0)
	if err != nil {
		t.Fatal(err)
	}
	if ua := <-rp.agents; ua != UserAgent("tests@example.com") {
		t.Errorf("User-Agent = %q", ua)
	}
	if len(res.Recordings) == 0 {
		t.Fatal("no recordings decoded")
	}
	rec := res.Recordings[0]
	if !strings.Contains(strings.ToLower(rec.Title), "blow your mind") || rec.ID == "" || rec.Length == 0 || rec.Score == 0 {
		t.Errorf("first recording decoded badly: %+v", rec)
	}
	if !strings.Contains(Credit(rec.ArtistCredit), "Dua Lipa") {
		t.Errorf("artist credit = %q", Credit(rec.ArtistCredit))
	}
	if len(rec.Releases) == 0 || len(rec.Releases[0].Media) == 0 || len(rec.Releases[0].Media[0].Track) == 0 {
		t.Fatalf("releases, media or track missing: %+v", rec.Releases)
	}

	cands := Rank(fixtures[0].query, res.Recordings, nil)
	if len(cands) == 0 || cands[0].Score < 70 {
		t.Errorf("best candidate scored too low: %+v", cands)
	}
}

func TestSearchRecordingsNoHits(t *testing.T) {
	srv := httptest.NewServer(&replay{t: t})
	defer srv.Close()
	c, _ := testClient(t, srv)

	res, err := c.SearchRecordings(context.Background(), fixtures[1].query, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Recordings) != 0 || res.Count != 0 {
		t.Errorf("expected no hits, got %+v", res)
	}
}

func TestSearchRecordingsCachesResponses(t *testing.T) {
	rp := &replay{t: t}
	srv := httptest.NewServer(rp)
	defer srv.Close()
	c, clk := testClient(t, srv)

	for range 3 {
		if _, err := c.SearchRecordings(context.Background(), fixtures[0].query, 0); err != nil {
			t.Fatal(err)
		}
	}
	if n := rp.requests.Load(); n != 1 {
		t.Errorf("sent %d requests, want 1 (the rest from the cache)", n)
	}
	if len(clk.Slept()) != 0 {
		t.Errorf("cache hits waited on the limiter: %v", clk.Slept())
	}

	// A different page is a different request.
	if _, err := c.SearchRecordings(context.Background(), fixtures[0].query, PageSize); err == nil {
		t.Errorf("expected the unrecorded page to 404")
	}
	if n := rp.requests.Load(); n != 2 {
		t.Errorf("sent %d requests, want 2", n)
	}
}

func TestSearchRecordingsSendsFmtJSONAndLimit(t *testing.T) {
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.URL.RawQuery
		_, _ = w.Write([]byte(`{"count":0,"offset":25,"recordings":[]}`))
	}))
	defer srv.Close()
	c, _ := testClient(t, srv)

	if _, err := c.SearchRecordings(context.Background(), Query{Title: "a/b"}, 25); err != nil {
		t.Fatal(err)
	}
	want := "fmt=json&limit=25&offset=25&query=recording%3A%28a%5C%2Fb%29"
	if got != want {
		t.Errorf("query string = %s, want %s", got, want)
	}
}

func TestSearchRecordingsEmptyQuery(t *testing.T) {
	srv := httptest.NewServer(&replay{t: t})
	defer srv.Close()
	c, _ := testClient(t, srv)
	if _, err := c.SearchRecordings(context.Background(), Query{}, 0); err == nil {
		t.Error("expected an error for an empty query")
	}
}

func TestFetchBacksOffOn503(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) <= 3 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte(`{"count":0,"offset":0,"recordings":[]}`))
	}))
	defer srv.Close()
	c, clk := testClient(t, srv)

	if _, err := c.SearchRecordings(context.Background(), Query{Title: "x"}, 0); err != nil {
		t.Fatal(err)
	}
	if n := calls.Load(); n != 4 {
		t.Errorf("calls = %d, want 4", n)
	}
	got := clk.Slept()
	want := []time.Duration{2 * time.Second, 4 * time.Second, 8 * time.Second}
	if len(got) != len(want) {
		t.Fatalf("slept %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("slept %v, want %v", got, want)
		}
	}
	// The success halved the interval.
	if c.limiter.interval != 4*time.Second {
		t.Errorf("interval after success = %v, want 4s", c.limiter.interval)
	}
}

func TestFetchGivesUpAfterMaxBackoff(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()
	c, clk := testClient(t, srv)

	_, err := c.SearchRecordings(context.Background(), Query{Title: "x"}, 0)
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("err = %v, want ErrUnavailable", err)
	}
	// 1 attempt plus retries after 2, 4, 8, 16 and 32 s.
	if n := calls.Load(); n != 6 {
		t.Errorf("calls = %d, want 6", n)
	}
	var total time.Duration
	for _, d := range clk.Slept() {
		total += d
	}
	if total != 62*time.Second {
		t.Errorf("total wait = %v, want 62s", total)
	}
}

func TestFetchReportsServiceError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"Invalid query"}`))
	}))
	defer srv.Close()
	c, _ := testClient(t, srv)

	_, err := c.SearchRecordings(context.Background(), Query{Title: "x"}, 0)
	if err == nil || !strings.Contains(err.Error(), "HTTP 400: Invalid query") {
		t.Errorf("err = %v", err)
	}
}

func TestFetchCancelledWhileBackingOff(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cancel()
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()
	c, _ := testClient(t, srv)

	if _, err := c.SearchRecordings(ctx, Query{Title: "x"}, 0); !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
}
