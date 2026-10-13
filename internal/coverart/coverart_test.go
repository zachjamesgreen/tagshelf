package coverart

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"go.senan.xyz/taglib"

	"github.com/zachjamesgreen/tagshelf/internal/fixtures"
)

const (
	releaseID = "aaaaaaaa-0000-4000-8000-000000000001"
	groupID   = "aaaaaaaa-0000-4000-8000-000000000002"
)

// picture makes a picture of w by h pixels in the given format.
func picture(t *testing.T, format string, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	img.Set(0, 0, color.RGBA{R: 200, A: 255})
	var buf bytes.Buffer
	var err error
	switch format {
	case "jpeg":
		err = jpeg.Encode(&buf, img, nil)
	case "png":
		err = png.Encode(&buf, img)
	case "gif":
		err = gif.Encode(&buf, img, nil)
	}
	if err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// archive is a stand-in for the Cover Art Archive: it serves the pictures it
// was given by path, by way of a redirect as the real one does, and notes
// what it was asked.
type archive struct {
	*httptest.Server
	mu       sync.Mutex
	pictures map[string][]byte // by path, as in /release/<id>/front-500
	status   map[string]int    // what to answer in place of a picture
	asked    []string
	agents   []string
}

func newArchive(t *testing.T) (*archive, *Client) {
	t.Helper()
	a := &archive{pictures: map[string][]byte{}, status: map[string]int{}}
	a.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		a.mu.Lock()
		defer a.mu.Unlock()
		if kept, ok := strings.CutPrefix(r.URL.Path, "/kept"); ok {
			w.Write(a.pictures[kept])
			return
		}
		a.asked = append(a.asked, r.URL.Path)
		a.agents = append(a.agents, r.Header.Get("User-Agent"))
		if code := a.status[r.URL.Path]; code != 0 {
			w.WriteHeader(code)
			return
		}
		if _, ok := a.pictures[r.URL.Path]; !ok {
			http.NotFound(w, r)
			return
		}
		http.Redirect(w, r, "/kept"+r.URL.Path, http.StatusTemporaryRedirect)
	}))
	t.Cleanup(a.Close)
	c := New()
	c.baseURL = a.URL
	c.pace = &pace{now: time.Now, sleep: sleep} // no waiting between the test's requests
	return a, c
}

// TestFront checks which picture is fetched: the release's own in the size
// asked for, or else in the next size below, or else the release group's.
func TestFront(t *testing.T) {
	large, medium, groups := picture(t, "jpeg", 1200, 1200), picture(t, "jpeg", 500, 480), picture(t, "png", 250, 250)
	a, c := newArchive(t)
	a.pictures["/release/"+releaseID+"/front-1200"] = large
	a.pictures["/release/"+releaseID+"/front-500"] = medium
	a.pictures["/release-group/"+groupID+"/front-250"] = groups

	pic, err := c.Front(context.Background(), releaseID, groupID, Large)
	if err != nil || pic.Width != 1200 || pic.Ext != ".jpg" || !bytes.Equal(pic.Data, large) {
		t.Errorf("Front, large = %dx%d %s, %v; want the release's picture of 1200", pic.Width, pic.Height, pic.Ext, err)
	}
	pic, err = c.Front(context.Background(), releaseID, groupID, Medium)
	if err != nil || pic.Width != 500 || pic.Height != 480 {
		t.Errorf("Front, medium = %dx%d, %v", pic.Width, pic.Height, err)
	}

	// A release without the size asked for gives its next size below.
	delete(a.pictures, "/release/"+releaseID+"/front-1200")
	a.asked = nil
	pic, err = c.Front(context.Background(), releaseID, groupID, Large)
	if err != nil || pic.Width != 500 {
		t.Errorf("without the large size: %dx%d, %v; want the one of 500", pic.Width, pic.Height, err)
	}
	if want := []string{"/release/" + releaseID + "/front-1200", "/release/" + releaseID + "/front-500"}; !equal(a.asked, want) {
		t.Errorf("the archive was asked %q, want %q", a.asked, want)
	}

	// A release without any gives its release group's, which may be a PNG.
	delete(a.pictures, "/release/"+releaseID+"/front-500")
	pic, err = c.Front(context.Background(), releaseID, groupID, Large)
	if err != nil || pic.Ext != ".png" || pic.Width != 250 || !bytes.Equal(pic.Data, groups) {
		t.Errorf("from the release group: %dx%d %s, %v", pic.Width, pic.Height, pic.Ext, err)
	}
	// Neither has one; and an ID that is none is not asked about.
	if _, err := c.Front(context.Background(), releaseID, "", Large); !errors.Is(err, ErrNone) {
		t.Errorf("with no picture anywhere: %v, want ErrNone", err)
	}
	a.asked = nil
	if _, err := c.Front(context.Background(), "not an id", "", Large); !errors.Is(err, ErrNone) || len(a.asked) != 0 {
		t.Errorf("with no ID to ask about: %v, after %d requests", err, len(a.asked))
	}
}

func equal(a, b []string) bool {
	return strings.Join(a, "\n") == strings.Join(b, "\n")
}

// A request names tagshelf and where it lives, and no person.
func TestFrontNamesTagshelfAndNoOne(t *testing.T) {
	a, c := newArchive(t)
	a.pictures["/release/"+releaseID+"/front-500"] = picture(t, "jpeg", 500, 500)
	if _, err := c.Front(context.Background(), releaseID, "", Medium); err != nil {
		t.Fatal(err)
	}
	agent := a.agents[0]
	if !strings.HasPrefix(agent, "tagshelf/") || !strings.Contains(agent, ProjectURL) || strings.Contains(agent, "@") {
		t.Errorf("the request's User-Agent is %q", agent)
	}
}

// What is wrong at the archive's end is told apart: busy, an error of its
// own, something that is no picture, and one too large to be a cover.
func TestFrontProblems(t *testing.T) {
	a, c := newArchive(t)
	path := "/release/" + releaseID + "/front-500"
	a.status[path] = http.StatusServiceUnavailable
	if _, err := c.Front(context.Background(), releaseID, groupID, Medium); !errors.Is(err, ErrBusy) {
		t.Errorf("a busy archive: %v, want ErrBusy", err)
	}
	a.status[path] = http.StatusBadGateway
	if _, err := c.Front(context.Background(), releaseID, groupID, Medium); err == nil || !strings.Contains(err.Error(), "502") {
		t.Errorf("an error of the archive's: %v", err)
	}
	delete(a.status, path)
	for name, data := range map[string][]byte{
		"a web page": []byte("<html>not found</html>"), "a GIF": picture(t, "gif", 10, 10), "nothing": nil,
	} {
		a.pictures[path] = data
		if _, err := c.Front(context.Background(), releaseID, "", Medium); err == nil || !strings.Contains(err.Error(), "cannot be used") {
			t.Errorf("%s in place of a picture: %v", name, err)
		}
	}
	a.pictures[path] = append(picture(t, "jpeg", 10, 10), make([]byte, maxPicture)...)
	if _, err := c.Front(context.Background(), releaseID, "", Medium); err == nil || !strings.Contains(err.Error(), "larger than") {
		t.Errorf("a picture too large: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.Front(ctx, releaseID, "", Medium); !errors.Is(err, context.Canceled) {
		t.Errorf("with the request given up: %v", err)
	}
}

// The requests of a process are spaced, and one given up while it waits
// takes no turn.
func TestPace(t *testing.T) {
	now := time.Unix(1000, 0)
	var slept []time.Duration
	p := &pace{every: time.Second, now: func() time.Time { return now }}
	p.sleep = func(_ context.Context, d time.Duration) error {
		slept = append(slept, d)
		now = now.Add(d)
		return nil
	}
	for range 3 {
		if err := p.wait(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if len(slept) != 2 || slept[0] != time.Second || slept[1] != time.Second {
		t.Errorf("three requests at once slept %v, want a second before the second and the third", slept)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := p.wait(ctx); !errors.Is(err, context.Canceled) {
		t.Errorf("a request given up: %v", err)
	}
}

func TestDescribe(t *testing.T) {
	if pic, err := Describe(picture(t, "png", 30, 20)); err != nil || pic.Ext != ".png" || pic.Width != 30 || pic.Height != 20 {
		t.Errorf("a PNG = %+v, %v", pic, err)
	}
	for name, data := range map[string][]byte{"a GIF": picture(t, "gif", 5, 5), "text": []byte("cover"), "nothing": nil} {
		if _, err := Describe(data); err == nil {
			t.Errorf("%s is taken for a cover", name)
		}
	}
}

// TestInFolder checks which image file of a folder is its cover: the one
// named cover before folder before front, whatever its case, and no other.
func TestInFolder(t *testing.T) {
	dir := t.TempDir()
	write := func(name string) string {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, picture(t, "jpeg", 4, 4), 0o644); err != nil {
			t.Fatal(err)
		}
		return path
	}
	if _, ok, err := InFolder(dir); ok || err != nil {
		t.Errorf("an empty folder has a cover (%v)", err)
	}
	write("back.jpg")
	write("cover.txt")
	write("cover.gif")
	if err := os.Mkdir(filepath.Join(dir, "front.jpg"), 0o755); err != nil {
		t.Fatal(err)
	}
	if path, ok, _ := InFolder(dir); ok {
		t.Errorf("%s is taken for the cover", path)
	}
	front := write("Front.PNG")
	if path, ok, _ := InFolder(dir); !ok || path != front {
		t.Errorf("the cover is %q, want %q", path, front)
	}
	folder := write("folder.jpeg")
	if path, ok, _ := InFolder(dir); !ok || path != folder {
		t.Errorf("the cover is %q, want %q", path, folder)
	}
	cover := write("COVER.jpg")
	if path, ok, _ := InFolder(dir); !ok || path != cover {
		t.Errorf("the cover is %q, want %q", path, cover)
	}
	if _, _, err := InFolder(filepath.Join(dir, "none")); err == nil {
		t.Error("a folder that is not there has no error")
	}

	if pic, err := ReadFile(cover); err != nil || pic.Ext != ".jpg" || pic.Width != 4 {
		t.Errorf("ReadFile = %+v, %v", pic, err)
	}
	text := filepath.Join(dir, "notes.png")
	if err := os.WriteFile(text, []byte("not a picture"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadFile(text); err == nil {
		t.Error("a file that is no picture reads as one")
	}
}

// TestEmbedded reads the picture an audio file of each kind holds.
func TestEmbedded(t *testing.T) {
	art := picture(t, "png", 12, 9)
	for _, ext := range []string{".mp3", ".flac", ".m4a", ".ogg"} {
		data, err := os.ReadFile(fixtures.Tagged(ext))
		if err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(t.TempDir(), "song"+ext)
		if err := os.WriteFile(path, data, 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := Embedded(path); !errors.Is(err, ErrNoPicture) {
			t.Errorf("%s without a picture: %v, want ErrNoPicture", ext, err)
		}
		if err := taglib.WriteImage(path, art); err != nil {
			t.Fatal(err)
		}
		if pic, err := Embedded(path); err != nil || pic.Ext != ".png" || pic.Width != 12 || pic.Height != 9 || !bytes.Equal(pic.Data, art) {
			t.Errorf("%s: Embedded = %dx%d %s, %v", ext, pic.Width, pic.Height, pic.Ext, err)
		}
	}
}

// TestForEmbedding checks what goes into an audio file: a picture of a
// modest number of bytes as it is, and a larger one made smaller, as a JPEG
// of 500 pixels on its longer side with its shape kept.
func TestForEmbedding(t *testing.T) {
	small, err := Describe(picture(t, "png", 900, 600))
	if err != nil {
		t.Fatal(err)
	}
	if got, err := ForEmbedding(small); err != nil || !bytes.Equal(got.Data, small.Data) {
		t.Errorf("a picture of %d bytes was changed (%v)", len(small.Data), err)
	}

	// Noise does not pack: a picture of well over the limit.
	img := image.NewRGBA(image.Rect(0, 0, 1600, 800))
	seed := uint32(1)
	for i := range img.Pix {
		seed = seed*1664525 + 1013904223
		img.Pix[i] = byte(seed >> 24)
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	large, err := Describe(buf.Bytes())
	if err != nil || len(large.Data) <= EmbedLimit {
		t.Fatalf("the large picture is %d bytes (%v)", len(large.Data), err)
	}
	got, err := ForEmbedding(large)
	if err != nil || got.Ext != ".jpg" || got.Width != 500 || got.Height != 250 || len(got.Data) >= len(large.Data) {
		t.Errorf("made smaller: %dx%d %s, %d bytes, %v", got.Width, got.Height, got.Ext, len(got.Data), err)
	}
	if !bytes.Equal(large.Data, buf.Bytes()) {
		t.Error("the picture given was changed")
	}

	// A picture that is one colour all over stays that colour.
	red := image.NewRGBA(image.Rect(0, 0, 40, 40))
	for i := 0; i < len(red.Pix); i += 4 {
		red.Pix[i], red.Pix[i+3] = 200, 255
	}
	buf.Reset()
	if err := png.Encode(&buf, red); err != nil {
		t.Fatal(err)
	}
	pic, _ := Describe(buf.Bytes())
	out, err := Shrink(pic, 10)
	if err != nil || out.Width != 10 || out.Height != 10 {
		t.Fatalf("Shrink = %dx%d, %v", out.Width, out.Height, err)
	}
	decoded, err := jpeg.Decode(bytes.NewReader(out.Data))
	if err != nil {
		t.Fatal(err)
	}
	if r, g, b, _ := decoded.At(5, 5).RGBA(); r>>8 < 190 || r>>8 > 210 || g>>8 > 12 || b>>8 > 12 {
		t.Errorf("the middle of the red picture is (%d, %d, %d)", r>>8, g>>8, b>>8)
	}
	if _, err := Shrink(Picture{Data: []byte("x"), Width: 100000, Height: 100000}, 500); err == nil {
		t.Error("a picture of ten thousand megapixels was taken on")
	}
}
