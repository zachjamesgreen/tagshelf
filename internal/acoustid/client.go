// Package acoustid asks the AcoustID web service which recordings a
// fingerprint belongs to. A request carries the fingerprint, the length of
// the file in seconds and the key of the application, and nothing else:
// no file name, no tags and no contact.
package acoustid

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/zachjamesgreen/tagshelf/internal/version"
)

// DefaultBaseURL is the AcoustID web service.
const DefaultBaseURL = "https://api.acoustid.org/v2"

// ProjectURL is where tagshelf lives, which a request names in its
// User-Agent.
const ProjectURL = "https://github.com/zachjamesgreen/tagshelf"

// releaseKey is the key of the tagshelf application at AcoustID. The
// repository does not hold it: the release workflow sets it when it builds
// the downloads, with -ldflags "-X .../internal/acoustid.releaseKey=...".
// A build from source has none, and goes by acoustid_key in the config file.
var releaseKey string

// Key is the key to ask AcoustID with: the one configured, or else the one
// this build was given. It is empty when there is neither.
func Key(configured string) string {
	if key := strings.TrimSpace(configured); key != "" {
		return key
	}
	return releaseKey
}

// ErrNoKey is returned by New when there is no key to ask with.
var ErrNoKey = errors.New("identifying by sound needs an AcoustID key: register an application at https://acoustid.org/new-application and set acoustid_key in the config file to its API key")

// ErrKey is returned when AcoustID does not know the key.
var ErrKey = errors.New("AcoustID does not accept the key: check acoustid_key in the config file")

// ErrBusy is returned when AcoustID is not taking requests, or says there
// were too many.
var ErrBusy = errors.New("AcoustID is not accepting requests right now; try again later")

// ErrTimeout is returned when a request got no answer in time.
var ErrTimeout = errors.New("AcoustID did not answer in time; try again later")

// requestTimeout bounds one whole exchange.
const requestTimeout = 30 * time.Second

// interval is the least time between two requests. AcoustID allows three a
// second; this stays under it.
const interval = 400 * time.Millisecond

// maxAnswer bounds an answer read from the service.
const maxAnswer = 4 << 20

// pace spaces every request the process makes.
var pace struct {
	sync.Mutex
	next time.Time
}

// Client asks the AcoustID web service. It is safe for concurrent use.
type Client struct {
	baseURL   string
	key       string
	userAgent string
	http      *http.Client
	// wait waits until the next request may go; tests put nothing in its
	// place to go without waiting.
	wait func(ctx context.Context) error
}

// New returns a client that asks with key. It returns ErrNoKey when key is
// empty.
func New(key string) (*Client, error) {
	key = strings.TrimSpace(key)
	if key == "" {
		return nil, ErrNoKey
	}
	return &Client{
		baseURL:   DefaultBaseURL,
		key:       key,
		userAgent: "tagshelf/" + version.String() + " ( " + ProjectURL + " )",
		http:      &http.Client{Timeout: requestTimeout},
		wait:      waitTurn,
	}, nil
}

// waitTurn waits until a request may go, an interval after the one before.
func waitTurn(ctx context.Context) error {
	pace.Lock()
	now := time.Now()
	at := pace.next
	if at.Before(now) {
		at = now
	}
	pace.next = at.Add(interval)
	pace.Unlock()
	if d := time.Until(at); d > 0 {
		t := time.NewTimer(d)
		defer t.Stop()
		select {
		case <-t.C:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}

// Result is one track AcoustID knows that a fingerprint matches.
type Result struct {
	// ID is the AcoustID of the track.
	ID string `json:"id"`
	// Score is how well the fingerprint matches it, from 0 to 1.
	Score float64 `json:"score"`
	// Recordings are the MusicBrainz recordings linked to the track, by ID.
	Recordings []string `json:"recordings"`
}

// answer is what the service sends.
type answer struct {
	Status string `json:"status"`
	Error  struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
	Results []struct {
		ID         string  `json:"id"`
		Score      float64 `json:"score"`
		Recordings []struct {
			ID string `json:"id"`
		} `json:"recordings"`
	} `json:"results"`
}

// The codes of the errors AcoustID answers with that are told apart.
const (
	codeInvalidKey  = 4
	codeTooManyReqs = 14
)

// Lookup asks which tracks the fingerprint of a file duration seconds long
// belongs to. The results are as AcoustID orders them, best first; there
// are none when it knows no such audio.
func (c *Client) Lookup(ctx context.Context, fingerprint string, duration int) ([]Result, error) {
	if fingerprint == "" || duration <= 0 {
		return nil, errors.New("AcoustID lookup: a fingerprint and a length are needed")
	}
	if err := c.wait(ctx); err != nil {
		return nil, err
	}
	form := url.Values{
		"client":      {c.key},
		"format":      {"json"},
		"meta":        {"recordingids"},
		"duration":    {strconv.Itoa(duration)},
		"fingerprint": {fingerprint},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/lookup", strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", c.userAgent)
	req.Header.Set("Accept", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, c.failed(ctx, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxAnswer+1))
	if err != nil {
		return nil, c.failed(ctx, err)
	}
	if len(body) > maxAnswer {
		return nil, errors.New("AcoustID lookup: the answer is too large")
	}
	var a answer
	decodeErr := json.Unmarshal(body, &a)
	switch {
	case decodeErr == nil && a.Status == "error" && a.Error.Code == codeInvalidKey:
		return nil, ErrKey
	case resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500,
		decodeErr == nil && a.Status == "error" && a.Error.Code == codeTooManyReqs:
		return nil, ErrBusy
	case decodeErr == nil && a.Status == "error":
		return nil, fmt.Errorf("AcoustID lookup: %s", a.Error.Message)
	case resp.StatusCode != http.StatusOK:
		return nil, fmt.Errorf("AcoustID lookup: the service answered %s", resp.Status)
	case decodeErr != nil:
		return nil, fmt.Errorf("AcoustID lookup: reading the answer: %w", decodeErr)
	case a.Status != "ok":
		return nil, fmt.Errorf("AcoustID lookup: the service answered with the status %q", a.Status)
	}
	results := make([]Result, 0, len(a.Results))
	for _, r := range a.Results {
		res := Result{ID: r.ID, Score: r.Score}
		for _, rec := range r.Recordings {
			if rec.ID != "" {
				res.Recordings = append(res.Recordings, rec.ID)
			}
		}
		results = append(results, res)
	}
	return results, nil
}

// failed says what a request that got no answer failed with. The key is
// part of the request, and stays out of the message.
func (c *Client) failed(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	var netErr net.Error
	if errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &netErr) && netErr.Timeout()) {
		return ErrTimeout
	}
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		err = urlErr.Err
	}
	return fmt.Errorf("AcoustID lookup: %w", err)
}
