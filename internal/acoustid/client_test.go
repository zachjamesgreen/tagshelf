package acoustid

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/zachjamesgreen/tagshelf/internal/pacing"
	"github.com/zachjamesgreen/tagshelf/internal/pacing/pacingtest"
)

// testClient is a client that asks the stand-in server srv, with a limiter
// of its own on a clock that does not wait. No test asks the real service.
func testClient(t *testing.T, srv *httptest.Server) *Client {
	t.Helper()
	c, err := New("the-key")
	if err != nil {
		t.Fatal(err)
	}
	c.baseURL = srv.URL
	c.http = srv.Client()
	c.limiter = pacing.NewLimiter(pacingtest.NewClock(), minInterval, maxInterval)
	return c
}

func TestKey(t *testing.T) {
	old := releaseKey
	t.Cleanup(func() { releaseKey = old })
	releaseKey = ""
	if got := Key(""); got != "" {
		t.Errorf("with no key anywhere Key = %q", got)
	}
	if _, err := New(Key(" ")); !errors.Is(err, ErrNoKey) {
		t.Errorf("New without a key = %v, want ErrNoKey", err)
	}
	releaseKey = "built-in"
	if got := Key(""); got != "built-in" {
		t.Errorf("Key = %q, want the one the build was given", got)
	}
	if got := Key(" mine "); got != "mine" {
		t.Errorf("Key = %q, want the configured one, which comes first", got)
	}
}

// A lookup sends the key, the fingerprint and the length, and nothing that
// says whose file it is; the answer's tracks come back in its order.
func TestLookup(t *testing.T) {
	var got *http.Request
	var form map[string][]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r
		if err := r.ParseForm(); err != nil {
			t.Error(err)
		}
		form = r.PostForm
		w.Write([]byte(`{"status":"ok","results":[
			{"id":"aid-1","score":0.97,"recordings":[{"id":"rec-a"},{"id":"rec-b"}]},
			{"id":"aid-2","score":0.41},
			{"id":"aid-3","score":0.2,"recordings":[{"id":""}]}]}`))
	}))
	defer srv.Close()
	c := testClient(t, srv)

	results, err := c.Lookup(context.Background(), "AQAAfingerprint", 187)
	if err != nil {
		t.Fatal(err)
	}
	want := []Result{{ID: "aid-1", Score: 0.97, Recordings: []string{"rec-a", "rec-b"}}, {ID: "aid-2", Score: 0.41}, {ID: "aid-3", Score: 0.2}}
	if !reflect.DeepEqual(results, want) {
		t.Errorf("results = %+v\nwant      %+v", results, want)
	}
	if got.Method != http.MethodPost || got.URL.Path != "/lookup" || got.URL.RawQuery != "" {
		t.Errorf("the request was %s %s", got.Method, got.URL)
	}
	wantForm := map[string][]string{"client": {"the-key"}, "format": {"json"}, "meta": {"recordingids"}, "duration": {"187"}, "fingerprint": {"AQAAfingerprint"}}
	if !reflect.DeepEqual(form, wantForm) {
		t.Errorf("the request carried %v\nwant                %v", form, wantForm)
	}
	if ua := got.Header.Get("User-Agent"); !strings.HasPrefix(ua, "tagshelf/") || !strings.Contains(ua, ProjectURL) || strings.Contains(ua, "@") {
		t.Errorf("User-Agent = %q, want tagshelf, its version and its address, and no contact", ua)
	}

	if _, err := c.Lookup(context.Background(), "", 187); err == nil {
		t.Error("a lookup without a fingerprint was sent")
	}
	if _, err := c.Lookup(context.Background(), "AQAA", 0); err == nil {
		t.Error("a lookup without a length was sent")
	}
}

func TestLookupErrors(t *testing.T) {
	tests := []struct {
		name   string
		status int
		body   string
		want   error
		says   string
	}{
		{"a key AcoustID does not know", 400, `{"status":"error","error":{"code":4,"message":"invalid API key"}}`, ErrKey, ""},
		{"too many requests, by code", 400, `{"status":"error","error":{"code":14,"message":"rate limit exceeded"}}`, ErrBusy, ""},
		{"too many requests, by status", 429, `slow down`, ErrBusy, ""},
		{"the service down", 503, `<html>`, ErrBusy, ""},
		{"a fingerprint it cannot read", 400, `{"status":"error","error":{"code":3,"message":"invalid fingerprint"}}`, nil, "invalid fingerprint"},
		{"an answer that is not JSON", 200, `<html>`, nil, "reading the answer"},
		{"another status", 404, `{}`, nil, "404"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tt.status)
				w.Write([]byte(tt.body))
			}))
			defer srv.Close()
			_, err := testClient(t, srv).Lookup(context.Background(), "AQAA", 100)
			if err == nil || (tt.want != nil && !errors.Is(err, tt.want)) || (tt.says != "" && !strings.Contains(err.Error(), tt.says)) {
				t.Errorf("err = %v, want %v %q", err, tt.want, tt.says)
			}
			if err != nil && strings.Contains(err.Error(), "the-key") {
				t.Errorf("the error names the key: %v", err)
			}
		})
	}

	// A service that is not there: the message does not carry the key.
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	c := testClient(t, srv)
	srv.Close()
	if _, err := c.Lookup(context.Background(), "AQAA", 100); !errors.Is(err, ErrUnreachable) || strings.Contains(err.Error(), "the-key") {
		t.Errorf("with the service gone: %v", err)
	}

	// A cancelled request says so.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.Lookup(ctx, "AQAA", 100); !errors.Is(err, context.Canceled) {
		t.Errorf("cancelled: %v", err)
	}
}

// A lookup AcoustID is too busy for makes the next one wait twice as long,
// and one it answers brings the wait back down.
func TestLookupBacksOffWhenBusy(t *testing.T) {
	busy := true
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if busy {
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.Write([]byte(`{"status":"ok","results":[]}`))
	}))
	defer srv.Close()
	c := testClient(t, srv)
	clk := pacingtest.NewClock()
	c.limiter = pacing.NewLimiter(clk, minInterval, maxInterval)

	for range 2 {
		if _, err := c.Lookup(context.Background(), "AQAA", 100); !errors.Is(err, ErrBusy) {
			t.Fatalf("err = %v, want ErrBusy", err)
		}
	}
	busy = false
	if _, err := c.Lookup(context.Background(), "AQAA", 100); err != nil {
		t.Fatal(err)
	}
	if got, want := clk.Slept(), []time.Duration{2 * minInterval, 4 * minInterval}; !reflect.DeepEqual(got, want) {
		t.Errorf("the lookups after a busy answer waited %v, want %v", got, want)
	}
	if got := c.limiter.Interval(); got != 2*minInterval {
		t.Errorf("the interval after an answer = %v, want %v", got, 2*minInterval)
	}
}
