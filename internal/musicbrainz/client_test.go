package musicbrainz

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// The responses in testdata are written by hand, in the shape MusicBrainz's
// documentation gives its answers (https://musicbrainz.org/doc/MusicBrainz_API). They
// are not recordings of the live service: their IDs are made up, and a page
// holds a few hits where a real one holds 25. The tests that serve them show
// that the client reads that shape, not that the service still sends it.
// Nothing in this package's tests may call the live service; TestMain makes
// a request to it fail.

// fixture is one hand-written response: to a recording search, a release
// search (release set) or a release lookup (lookup set).
type fixture struct {
	file    string // under testdata
	query   Query
	release *ReleaseQuery
	lookup  string
	offset  int
}

// Made-up release IDs of the lookups.
const (
	deluxeID  = "d1a2b3c4-5e6f-4a7b-8c9d-0e1f2a3b4c5d"
	variousID = "5c6d7e8f-9a0b-4c1d-8e2f-3a4b5c6d7e8f"
)

var (
	blowYourMind  = fixture{file: "recording-blow-your-mind.json", query: Query{Title: "Blow Your Mind", Artist: "Dua Lipa", Length: 178 * time.Second}}
	noHits        = fixture{file: "recording-no-hits.json", query: Query{Title: "zzqx tagshelf no such recording", Artist: "qqzx nobody"}}
	newRules      = fixture{file: "recording-new-rules.json", query: Query{Title: "New Rules", Artist: "Dua Lipa", Length: 209 * time.Second}}
	dreams        = fixture{file: "recording-dreams.json", query: Query{Title: "Dreams", Artist: "Dua Lipa"}}
	duaLipaSearch = fixture{file: "release-search-dua-lipa.json", release: &ReleaseQuery{Album: "Dua Lipa", Artist: "Dua Lipa", Tracks: 17}}
	deluxeLookup  = fixture{file: "release-dua-lipa-deluxe.json", lookup: deluxeID}
	variousLookup = fixture{file: "release-various-artists.json", lookup: variousID}
)

// fixtures is every response the replay serves.
var fixtures = []fixture{blowYourMind, noHits, newRules, dreams, duaLipaSearch, deluxeLookup, variousLookup}

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

// replay serves the fixtures and counts requests; anything else is a 404 so
// an unexpected query shows up as a test failure.
type replay struct {
	files    map[string]string // by request URI
	requests atomic.Int32
	agents   chan string
}

// newReplay works out the fixtures' request URIs here, on the test's own
// goroutine, where a failure can still stop the test.
func newReplay(t *testing.T) *replay {
	t.Helper()
	rp := &replay{files: map[string]string{}, agents: make(chan string, 1)}
	for _, f := range fixtures {
		u, err := f.url("/ws/2")
		if err != nil {
			t.Fatal(err)
		}
		rp.files[u] = f.file
	}
	return rp
}

func (rp *replay) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	rp.requests.Add(1)
	select {
	case rp.agents <- r.UserAgent():
	default:
	}
	if file, ok := rp.files[r.URL.RequestURI()]; ok {
		body, err := os.ReadFile(filepath.Join("testdata", file))
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
		return
	}
	w.WriteHeader(http.StatusNotFound)
	_, _ = w.Write([]byte(`{"error":"Not Found","help":"For usage, please see: https://musicbrainz.org/development/mmd"}`))
}

// testClient returns a client for srv with its own limiter on a fake clock.
// It is put together here, field by field, and not by New: New's client is
// one edit of the base URL away from the live service.
func testClient(t *testing.T, srv *httptest.Server) (*Client, *fakeClock) {
	t.Helper()
	clk := newFakeClock()
	return &Client{
		baseURL:   srv.URL + "/ws/2",
		userAgent: UserAgent("tests@example.com"),
		http:      &http.Client{}, // http.DefaultTransport: see TestMain
		limiter:   newLimiter(clk),
		cache:     map[string][]byte{},
	}, clk
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

func TestNew(t *testing.T) {
	c, err := New(" me@example.com ")
	if err != nil {
		t.Fatal(err)
	}
	if c.baseURL != DefaultBaseURL || c.userAgent != UserAgent("me@example.com") || c.limiter != sharedLimiter {
		t.Errorf("New's client = %+v", c)
	}
	// A request that stalls ends by itself.
	if c.http == http.DefaultClient || c.http.Timeout != requestTimeout || requestTimeout <= 0 {
		t.Errorf("New's HTTP client has the timeout %v, want %v", c.http.Timeout, requestTimeout)
	}
}

func TestUserAgent(t *testing.T) {
	// Test binaries have no module version, so the version is "dev".
	if got, want := UserAgent("me@example.com"), "tagshelf/dev ( me@example.com )"; got != want {
		t.Errorf("UserAgent = %q, want %q", got, want)
	}
}

func TestSearchRecordingsReplaysFixture(t *testing.T) {
	rp := newReplay(t)
	srv := httptest.NewServer(rp)
	defer srv.Close()
	c, _ := testClient(t, srv)

	res, err := c.SearchRecordings(context.Background(), blowYourMind.query, 0)
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

	cands := Rank(blowYourMind.query, res.Recordings, nil)
	if len(cands) == 0 || cands[0].Score < 70 {
		t.Errorf("best candidate scored too low: %+v", cands)
	}
}

func TestSearchRecordingsNoHits(t *testing.T) {
	srv := httptest.NewServer(newReplay(t))
	defer srv.Close()
	c, _ := testClient(t, srv)

	res, err := c.SearchRecordings(context.Background(), noHits.query, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Recordings) != 0 || res.Count != 0 {
		t.Errorf("expected no hits, got %+v", res)
	}
}

func TestSearchRecordingsCachesResponses(t *testing.T) {
	rp := newReplay(t)
	srv := httptest.NewServer(rp)
	defer srv.Close()
	c, clk := testClient(t, srv)

	for range 3 {
		if _, err := c.SearchRecordings(context.Background(), blowYourMind.query, 0); err != nil {
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
	if _, err := c.SearchRecordings(context.Background(), blowYourMind.query, PageSize); err == nil {
		t.Errorf("expected the page there is no fixture for to 404")
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

// An empty query is refused here: it is not sent for the service to refuse.
func TestEmptyQueriesAreNotSent(t *testing.T) {
	rp := newReplay(t)
	srv := httptest.NewServer(rp)
	defer srv.Close()
	c, _ := testClient(t, srv)
	ctx := context.Background()

	_, err := c.SearchRecordings(ctx, Query{}, 0)
	if err == nil || !strings.Contains(err.Error(), "nothing to search for: fill in a title") {
		t.Errorf("empty recording search: err = %v", err)
	}
	// A year only ranks, so a release query of one is empty too.
	_, err = c.SearchReleases(ctx, ReleaseQuery{Year: 2017}, 0)
	if err == nil || !strings.Contains(err.Error(), "nothing to search for: fill in an album") {
		t.Errorf("empty release search: err = %v", err)
	}
	_, err = c.SearchReleaseCandidates(ctx, ReleaseQuery{}, nil, nil)
	if err == nil || !strings.Contains(err.Error(), "nothing to search for: fill in an album") {
		t.Errorf("empty release match: err = %v", err)
	}
	if n := rp.requests.Load(); n != 0 {
		t.Errorf("sent %d requests for empty queries, want none", n)
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
	tests := []struct {
		desc string
		code int
		body string
		want string
	}{
		{"the service's own message", http.StatusBadRequest, `{"error":"Invalid query"}`, "HTTP 400: Invalid query"},
		{"a gateway's page, which is not JSON", http.StatusBadGateway, "<html><body>Bad Gateway</body></html>", "HTTP 502 Bad Gateway"},
		{"JSON without a message", http.StatusNotFound, `{}`, "HTTP 404 Not Found"},
	}
	for _, tt := range tests {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(tt.code)
			_, _ = w.Write([]byte(tt.body))
		}))
		c, _ := testClient(t, srv)
		_, err := c.SearchRecordings(context.Background(), Query{Title: "x"}, 0)
		if err == nil || !strings.HasSuffix(err.Error(), tt.want) {
			t.Errorf("%s: err = %v, want it to end in %q", tt.desc, err, tt.want)
		}
		srv.Close()
	}
}

// The person drops the lookup while it waits out a 503's back-off: the wait
// ends with the context's error and nothing more is sent.
func TestFetchCancelledWhileBackingOff(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()
	c, clk := testClient(t, srv)
	clk.onSleep = cancel

	if _, err := c.SearchRecordings(ctx, Query{Title: "x"}, 0); !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
	if n := calls.Load(); n != 1 {
		t.Errorf("calls = %d, want 1: the back-off was not waited out", n)
	}
	if got := clk.Slept(); len(got) != 0 {
		t.Errorf("slept %v after the lookup was dropped", got)
	}
}

// A connection that stalls, before the answer or in the middle of it, ends
// with ErrTimeout instead of holding the lookup for good.
func TestRequestTimesOut(t *testing.T) {
	tests := []struct {
		desc    string
		handler func(w http.ResponseWriter, stall <-chan struct{})
	}{
		{"no answer", func(w http.ResponseWriter, stall <-chan struct{}) {
			<-stall
		}},
		{"an answer that stops part way", func(w http.ResponseWriter, stall <-chan struct{}) {
			_, _ = w.Write([]byte(`{"count":0,`))
			w.(http.Flusher).Flush()
			<-stall
		}},
	}
	for _, tt := range tests {
		stall := make(chan struct{})
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			tt.handler(w, stall)
		}))
		c, _ := testClient(t, srv)
		c.http = &http.Client{Timeout: 100 * time.Millisecond}

		done := make(chan error, 1)
		go func() {
			_, err := c.SearchRecordings(context.Background(), Query{Title: "x"}, 0)
			done <- err
		}()
		select {
		case err := <-done:
			if !errors.Is(err, ErrTimeout) {
				t.Errorf("%s: err = %v, want ErrTimeout", tt.desc, err)
			}
		case <-time.After(10 * time.Second):
			t.Errorf("%s: the request did not time out", tt.desc)
		}
		close(stall)
		srv.Close()
	}

	// A lookup the caller dropped keeps the caller's error.
	ctx, cancel := context.WithCancel(context.Background())
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cancel()
		<-r.Context().Done()
	}))
	defer srv.Close()
	c, _ := testClient(t, srv)
	if _, err := c.SearchRecordings(ctx, Query{Title: "x"}, 0); !errors.Is(err, context.Canceled) {
		t.Errorf("a dropped lookup: err = %v, want context.Canceled", err)
	}
}

// Only an answer that decodes is kept: an error page that came with a 200,
// or an answer cut short, is asked for again the next time.
func TestUnreadableAnswersAreNotCached(t *testing.T) {
	const good = `{"count":1,"offset":0,"recordings":[{"id":"r1","title":"x"}]}`
	tests := []struct {
		desc string
		bad  func(w http.ResponseWriter)
		want string
	}{
		{"not JSON", func(w http.ResponseWriter) {
			_, _ = w.Write([]byte("<html><body>Please try again</body></html>"))
		}, "decoding response"},
		{"JSON of another shape", func(w http.ResponseWriter) {
			_, _ = w.Write([]byte(`{"count":"many","recordings":7}`))
		}, "decoding response"},
		{"cut short", func(w http.ResponseWriter) {
			// More is promised than is sent, so the read of the body fails.
			w.Header().Set("Content-Length", "500")
			_, _ = w.Write([]byte(good[:20]))
		}, "unexpected EOF"},
	}
	for _, tt := range tests {
		var calls atomic.Int32
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if calls.Add(1) == 1 {
				tt.bad(w)
				return
			}
			_, _ = w.Write([]byte(good))
		}))
		c, _ := testClient(t, srv)
		ctx, q := context.Background(), Query{Title: "x"}

		if _, err := c.SearchRecordings(ctx, q, 0); err == nil || !strings.Contains(err.Error(), tt.want) {
			t.Errorf("%s: err = %v, want one saying %q", tt.desc, err, tt.want)
		}
		for range 2 {
			res, err := c.SearchRecordings(ctx, q, 0)
			if err != nil || len(res.Recordings) != 1 || res.Recordings[0].ID != "r1" {
				t.Errorf("%s: the same search again = %+v, %v; want the good answer", tt.desc, res, err)
			}
		}
		if n := calls.Load(); n != 2 {
			t.Errorf("%s: sent %d requests, want 2: the bad answer, then the good one, which is kept", tt.desc, n)
		}
		srv.Close()
	}
}

// The client is used from several goroutines at once: the TUI starts a
// search while the one before is still on its way. The race detector needs
// cgo, which this project builds without, so this only shows that every
// caller gets its answer.
func TestClientFromSeveralGoroutines(t *testing.T) {
	rp := newReplay(t)
	srv := httptest.NewServer(rp)
	defer srv.Close()
	c, _ := testClient(t, srv)

	searches := []fixture{blowYourMind, newRules, dreams, noHits}
	var wg sync.WaitGroup
	errs := make(chan error, 64)
	for i := range 32 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			f := searches[i%len(searches)]
			res, err := c.SearchRecordings(context.Background(), f.query, 0)
			if err == nil && (len(res.Recordings) == 0) != (f.file == noHits.file) {
				err = errors.New(f.file + ": the wrong answer")
			}
			if err == nil {
				_, err = c.LookupRelease(context.Background(), deluxeID)
			}
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Error(err)
		}
	}
	// Everything asked for is in the cache now.
	before := rp.requests.Load()
	for _, f := range searches {
		if _, err := c.SearchRecordings(context.Background(), f.query, 0); err != nil {
			t.Error(err)
		}
	}
	if n := rp.requests.Load(); n != before {
		t.Errorf("sent %d more requests for answers already had", n-before)
	}
}

func TestFlexInt(t *testing.T) {
	tests := []struct {
		json string
		want int
	}{
		// The live service sends numbers; its documentation shows strings.
		{`{"id":"r","score":100}`, 100},
		{`{"id":"r","score":"87"}`, 87},
		{`{"id":"r","score":null}`, 0},
		{`{"id":"r"}`, 0},
	}
	for _, tt := range tests {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(`{"count":1,"recordings":[` + tt.json + `]}`))
		}))
		c, _ := testClient(t, srv)
		res, err := c.SearchRecordings(context.Background(), Query{Title: "x"}, 0)
		if err != nil || len(res.Recordings) != 1 || int(res.Recordings[0].Score) != tt.want {
			t.Errorf("%s: got %+v, %v; want the score %d", tt.json, res, err, tt.want)
		}
		srv.Close()
	}
}
