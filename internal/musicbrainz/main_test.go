package musicbrainz

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/zachjamesgreen/tagshelf/internal/pacing"
	"github.com/zachjamesgreen/tagshelf/internal/pacing/pacingtest"
)

// TestMain keeps every test of this package off the network: whatever goes
// through http.DefaultTransport, as a Client's requests do, fails unless it
// is for this machine, where the httptest servers listen. A test that came
// by the real base URL then fails instead of querying musicbrainz.org.
func TestMain(m *testing.M) {
	http.DefaultTransport = loopbackOnly{http.DefaultTransport}
	os.Exit(m.Run())
}

// loopbackOnly passes on requests to this machine and refuses the rest.
type loopbackOnly struct{ next http.RoundTripper }

func (l loopbackOnly) RoundTrip(req *http.Request) (*http.Response, error) {
	host := req.URL.Hostname()
	if ip := net.ParseIP(host); host != "localhost" && (ip == nil || !ip.IsLoopback()) {
		return nil, fmt.Errorf("a test asked for %s: tests only talk to this machine", req.URL.Host)
	}
	return l.next.RoundTrip(req)
}

// CloseIdleConnections lets an httptest server close what it left open.
func (l loopbackOnly) CloseIdleConnections() {
	if c, ok := l.next.(interface{ CloseIdleConnections() }); ok {
		c.CloseIdleConnections()
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func TestRequestsOffThisMachineFail(t *testing.T) {
	var reached []string
	guard := loopbackOnly{roundTripFunc(func(req *http.Request) (*http.Response, error) {
		reached = append(reached, req.URL.Host)
		return nil, errors.New("passed on")
	})}
	tests := []struct {
		url    string
		passed bool
	}{
		{DefaultBaseURL + "/recording?query=x", false},
		{"http://musicbrainz.org/ws/2/release/x", false},
		{"http://192.0.2.1/", false},
		{"http://127.0.0.1.example.com/", false},
		{"http://127.0.0.1:8080/ws/2", true},
		{"http://[::1]:8080/ws/2", true},
		{"http://localhost:8080/ws/2", true},
	}
	for _, tt := range tests {
		req, err := http.NewRequest(http.MethodGet, tt.url, nil)
		if err != nil {
			t.Fatal(err)
		}
		reached = nil
		_, err = guard.RoundTrip(req)
		if passed := len(reached) == 1; passed != tt.passed {
			t.Errorf("%s: passed on = %t, want %t", tt.url, passed, tt.passed)
		}
		if !tt.passed && (err == nil || !strings.Contains(err.Error(), "tests only talk to this machine")) {
			t.Errorf("%s: err = %v", tt.url, err)
		}
	}

	// TestMain put the guard in, and a client as New makes it, with the real
	// base URL, is stopped by it. With no guard, or one that just let
	// something through, this must not be tried.
	if t.Failed() {
		t.FailNow()
	}
	if _, ok := http.DefaultTransport.(loopbackOnly); !ok {
		t.Fatalf("http.DefaultTransport is a %T: TestMain did not put the guard in", http.DefaultTransport)
	}
	c, err := New("tests@example.com")
	if err != nil {
		t.Fatal(err)
	}
	c.limiter = pacing.NewLimiter(pacingtest.NewClock(), minInterval, maxInterval)
	_, err = c.SearchRecordings(context.Background(), Query{Title: "x"}, 0)
	if err == nil || !strings.Contains(err.Error(), "a test asked for musicbrainz.org") {
		t.Errorf("a search on the real base URL: err = %v, want the guard's refusal", err)
	}
}
