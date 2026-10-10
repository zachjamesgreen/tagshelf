// Package musicbrainz finds MusicBrainz recordings and releases for a file: a
// rate-limited, caching web service client, Picard-style query building and
// local ranking of the candidates. For a release match it also assigns files
// to the tracks of a looked-up release, and it maps a release track to the
// tags tagshelf writes.
package musicbrainz

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"time"
)

// DefaultBaseURL is the MusicBrainz web service root.
const DefaultBaseURL = "https://musicbrainz.org/ws/2"

// PageSize is how many hits one search request returns.
const PageSize = 25

// ErrDisabled is returned by New when no musicbrainz_contact is configured.
// MusicBrainz asks every client to identify itself with a way to reach its
// user, so lookups stay off until one is set.
var ErrDisabled = errors.New("MusicBrainz lookups are disabled: set musicbrainz_contact in the config file to your email address or a URL")

// ErrUnavailable is returned when MusicBrainz keeps answering 503 after the
// client has backed off to its longest interval.
var ErrUnavailable = errors.New("MusicBrainz is not accepting requests right now (rate limited or overloaded); try again later")

// ErrTimeout is returned when a request got no answer, or not all of one,
// within the client's timeout.
var ErrTimeout = errors.New("MusicBrainz did not answer in time; try again later")

// requestTimeout bounds one whole exchange, from connecting to the last byte
// of the body. A search answers in a second or two; http.DefaultClient would
// leave a lookup on a stalled connection waiting until the person gives up.
const requestTimeout = 30 * time.Second

// Client talks to the MusicBrainz web service. It is safe for concurrent use.
// All clients in a process share one rate limiter; each client has its own
// in-memory response cache.
type Client struct {
	baseURL   string
	userAgent string
	http      *http.Client
	limiter   *limiter

	mu    sync.Mutex
	cache map[string][]byte
}

// New returns a client that identifies itself with contact (an email address
// or URL). It returns ErrDisabled when contact is empty.
func New(contact string) (*Client, error) {
	contact = strings.TrimSpace(contact)
	if contact == "" {
		return nil, ErrDisabled
	}
	return &Client{
		baseURL:   DefaultBaseURL,
		userAgent: UserAgent(contact),
		http:      &http.Client{Timeout: requestTimeout},
		limiter:   sharedLimiter,
		cache:     map[string][]byte{},
	}, nil
}

// UserAgent is the User-Agent MusicBrainz asks for:
// "tagshelf/<version> ( <contact> )".
func UserAgent(contact string) string {
	return "tagshelf/" + version() + " ( " + contact + " )"
}

// version is the module version from the build info, or "dev" for a build
// without one (go run, go test, a plain go build of a checkout).
func version() string {
	if bi, ok := debug.ReadBuildInfo(); ok {
		if v := bi.Main.Version; v != "" && v != "(devel)" {
			return v
		}
	}
	return "dev"
}

// SearchRecordings runs a recording search and returns one page of PageSize
// hits starting at offset.
func (c *Client) SearchRecordings(ctx context.Context, q Query, offset int) (*RecordingSearch, error) {
	u, err := recordingSearchURL(c.baseURL, q, offset)
	if err != nil {
		return nil, err
	}
	var res RecordingSearch
	if err := c.get(ctx, u, &res); err != nil {
		return nil, fmt.Errorf("MusicBrainz recording search: %w", err)
	}
	return &res, nil
}

// recordingSearchURL is the URL of one page of a recording search.
func recordingSearchURL(base string, q Query, offset int) (string, error) {
	lucene := q.Lucene()
	if lucene == "" {
		return "", errors.New("nothing to search for: fill in a title, artist, album or length")
	}
	params := url.Values{
		"query": {lucene},
		"limit": {strconv.Itoa(PageSize)},
	}
	if offset > 0 {
		params.Set("offset", strconv.Itoa(offset))
	}
	return serviceURL(base, "recording", params), nil
}

// serviceURL is path under base with params and fmt=json.
func serviceURL(base, path string, params url.Values) string {
	params.Set("fmt", "json")
	return base + "/" + path + "?" + params.Encode()
}

// get fetches u and decodes the JSON response into v, answering from the
// cache when it can. Only responses that decode are cached.
func (c *Client) get(ctx context.Context, u string, v any) error {
	c.mu.Lock()
	body, cached := c.cache[u]
	c.mu.Unlock()
	if !cached {
		var err error
		body, err = c.fetch(ctx, u)
		if err != nil {
			return err
		}
	}
	if err := json.Unmarshal(body, v); err != nil {
		return fmt.Errorf("decoding response: %w", err)
	}
	if !cached {
		c.mu.Lock()
		c.cache[u] = body
		c.mu.Unlock()
	}
	return nil
}

// maxBody bounds a response read; search pages are tens of kilobytes.
const maxBody = 16 << 20

// fetch sends one GET through the rate limiter, retrying 503s with backoff.
func (c *Client) fetch(ctx context.Context, u string) ([]byte, error) {
	for {
		if err := c.limiter.wait(ctx); err != nil {
			return nil, err
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("User-Agent", c.userAgent)
		req.Header.Set("Accept", "application/json")

		resp, err := c.http.Do(req)
		if err != nil {
			return nil, timedOut(ctx, err)
		}
		body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
		resp.Body.Close()

		if resp.StatusCode == http.StatusServiceUnavailable {
			if !c.limiter.backoff() {
				return nil, ErrUnavailable
			}
			continue
		}
		if err != nil {
			return nil, timedOut(ctx, err)
		}
		c.limiter.recover()
		if resp.StatusCode != http.StatusOK {
			return nil, statusError(resp.StatusCode, body)
		}
		return body, nil
	}
}

// timedOut makes ErrTimeout of the error of a request that ran out of time.
// One the caller dropped or gave a deadline of its own keeps its error.
func timedOut(ctx context.Context, err error) error {
	var ne net.Error
	if ctx.Err() == nil && errors.As(err, &ne) && ne.Timeout() {
		return ErrTimeout
	}
	return err
}

// statusError describes a non-OK response, using the service's JSON error
// message when there is one.
func statusError(code int, body []byte) error {
	var e struct {
		Error string `json:"error"`
	}
	if json.Unmarshal(body, &e) == nil && e.Error != "" {
		return fmt.Errorf("HTTP %d: %s", code, e.Error)
	}
	return fmt.Errorf("HTTP %d %s", code, http.StatusText(code))
}
