// Package coverart finds a front cover for an album: among the image files
// of its folder, in the pictures its audio files hold, and at the Cover Art
// Archive, by the MusicBrainz IDs in the tags.
package coverart

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	_ "image/jpeg" // the two kinds of picture a cover may be
	_ "image/png"
	"io"
	"net"
	"net/http"
	"regexp"
	"strconv"
	"sync"
	"time"

	"github.com/zachjamesgreen/tagshelf/internal/version"
)

// DefaultBaseURL is the Cover Art Archive.
const DefaultBaseURL = "https://coverartarchive.org"

// ProjectURL is where tagshelf lives. It is what a request names in place of
// a person: the archive asks for no contact, and the address of whoever runs
// tagshelf would otherwise reach the Internet Archive too, which serves the
// pictures.
const ProjectURL = "https://github.com/zachjamesgreen/tagshelf"

// The sizes, in pixels, the archive serves a cover in beside its original.
// A cover is asked for in one of them: an original has no limit to its size,
// and may be a PDF.
const (
	Small  = 250
	Medium = 500
	Large  = 1200
)

var sizes = []int{Large, Medium, Small}

// ErrNone is returned when the archive has no front cover for a release or
// its release group.
var ErrNone = errors.New("the Cover Art Archive has no front cover for it")

// ErrBusy is returned when the archive is not taking requests.
var ErrBusy = errors.New("the Cover Art Archive is not accepting requests right now; try again later")

// ErrTimeout is returned when a request got no answer in time.
var ErrTimeout = errors.New("the Cover Art Archive did not answer in time; try again later")

// maxPicture bounds a picture read from the archive or from a file: its
// largest size is a JPEG of 1200 pixels, some hundreds of kilobytes.
const maxPicture = 16 << 20

// requestTimeout bounds one whole exchange, redirects and all.
const requestTimeout = 30 * time.Second

// Picture is a cover: its bytes as they were found, never converted.
type Picture struct {
	Data []byte
	// Ext is the extension of a file of its kind: ".jpg" or ".png".
	Ext           string
	Width, Height int
}

// Describe checks that data is a JPEG or a PNG picture and returns it as
// one, with its size in pixels read from its header.
func Describe(data []byte) (Picture, error) {
	cfg, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return Picture{}, errors.New("it is not a JPEG or PNG picture")
	}
	ext := map[string]string{"jpeg": ".jpg", "png": ".png"}[format]
	if ext == "" || cfg.Width <= 0 || cfg.Height <= 0 {
		return Picture{}, errors.New("it is not a JPEG or PNG picture")
	}
	return Picture{Data: data, Ext: ext, Width: cfg.Width, Height: cfg.Height}, nil
}

// Client fetches front covers from the Cover Art Archive. It is safe for
// concurrent use; all clients of a process share one pace.
type Client struct {
	baseURL   string
	userAgent string
	http      *http.Client
	pace      *pace
}

// New returns a client that names tagshelf, its version and where it lives.
func New() *Client {
	return &Client{
		baseURL:   DefaultBaseURL,
		userAgent: "tagshelf/" + version.String() + " ( " + ProjectURL + " )",
		http:      &http.Client{Timeout: requestTimeout},
		pace:      sharedPace,
	}
}

// mbid matches a MusicBrainz ID.
var mbid = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// Front fetches the front cover of the release with the ID release, or
// failing that of the release group with the ID group, no larger than size
// pixels: in that size where the archive has it, or else in the largest size
// below it. Either ID may be empty. The error is ErrNone when the archive has
// a front cover for neither.
func (c *Client) Front(ctx context.Context, release, group string, size int) (Picture, error) {
	for _, of := range []struct{ kind, id string }{{"release", release}, {"release-group", group}} {
		if !mbid.MatchString(of.id) {
			continue
		}
		for _, s := range sizes {
			if s > size {
				continue
			}
			pic, err := c.fetch(ctx, c.baseURL+"/"+of.kind+"/"+of.id+"/front-"+strconv.Itoa(s))
			switch {
			case err == nil:
				return pic, nil
			case errors.Is(err, ErrNone):
				// Not in this size, or not at all: the archive answers the
				// same for both.
				continue
			}
			return Picture{}, err
		}
	}
	return Picture{}, ErrNone
}

// fetch gets the picture at u, following the archive's redirects to where
// the picture is kept.
func (c *Client) fetch(ctx context.Context, u string) (Picture, error) {
	if err := c.pace.wait(ctx); err != nil {
		return Picture{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return Picture{}, err
	}
	req.Header.Set("User-Agent", c.userAgent)
	req.Header.Set("Accept", "image/jpeg, image/png")
	resp, err := c.http.Do(req)
	if err != nil {
		return Picture{}, timedOut(ctx, err)
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusNotFound:
		return Picture{}, ErrNone
	case http.StatusServiceUnavailable, http.StatusTooManyRequests:
		return Picture{}, ErrBusy
	default:
		return Picture{}, fmt.Errorf("the Cover Art Archive answered HTTP %d %s", resp.StatusCode, http.StatusText(resp.StatusCode))
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxPicture+1))
	if err != nil {
		return Picture{}, timedOut(ctx, err)
	}
	if len(data) > maxPicture {
		return Picture{}, fmt.Errorf("the picture the Cover Art Archive sent is larger than %d MB", maxPicture>>20)
	}
	pic, err := Describe(data)
	if err != nil {
		return Picture{}, fmt.Errorf("what the Cover Art Archive sent cannot be used: %w", err)
	}
	return pic, nil
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

// sharedPace spaces every request the process makes to the archive.
var sharedPace = &pace{every: time.Second, now: time.Now, sleep: sleep}

// pace spaces requests a fixed interval apart. The archive sets no limit;
// one request a second is what a person browsing would make.
type pace struct {
	every time.Duration
	now   func() time.Time
	sleep func(ctx context.Context, d time.Duration) error

	mu   sync.Mutex
	next time.Time // the earliest start of the next request
}

// wait blocks until the caller may send a request, or ctx is done.
func (p *pace) wait(ctx context.Context) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		p.mu.Lock()
		now := p.now()
		d := p.next.Sub(now)
		if d <= 0 {
			p.next = now.Add(p.every)
		}
		p.mu.Unlock()
		if d <= 0 {
			return nil
		}
		if err := p.sleep(ctx, d); err != nil {
			return err
		}
	}
}

func sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
