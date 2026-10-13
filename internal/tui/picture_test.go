package tui

import (
	"fmt"
	"image"
	"reflect"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi/kitty"

	"github.com/zachjamesgreen/tagshelf/internal/tagwrite"
	"github.com/zachjamesgreen/tagshelf/internal/termpic"
)

// The tests do not wait for a frame to be written before an image is drawn.
func init() { picDelay = 0 }

// plainPicture is a picture of w by h pixels of one colour.
func plainPicture(w, h int) image.Image {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for i := 0; i < len(img.Pix); i += 4 {
		copy(img.Pix[i:], []byte{200, 40, 40, 255})
	}
	return img
}

// sent runs a command and whatever it leads to, as pump does, and returns
// what was sent to the terminal past the screen on the way: each raw write,
// and "clear" for the screen drawn again from nothing.
func sent(t *testing.T, m *Model, cmd tea.Cmd) []string {
	t.Helper()
	var out []string
	var run func(cmd tea.Cmd)
	run = func(cmd tea.Cmd) {
		if cmd == nil {
			return
		}
		got := make(chan tea.Msg, 1)
		go func() { got <- cmd() }()
		var msg tea.Msg
		select {
		case msg = <-got:
		case <-time.After(pumpWait):
			return // a timer, as pump leaves behind
		}
		if msg == nil {
			return
		}
		if v := reflect.ValueOf(msg); v.Kind() == reflect.Slice && v.Type().Elem() == reflect.TypeOf(tea.Cmd(nil)) {
			for i := range v.Len() {
				run(v.Index(i).Interface().(tea.Cmd))
			}
			return
		}
		switch msg := msg.(type) {
		case tea.QuitMsg, tickMsg, runTickMsg:
			return
		case tea.RawMsg:
			out = append(out, fmt.Sprint(msg.Msg))
			return
		}
		if fmt.Sprintf("%T", msg) == "tea.clearScreenMsg" {
			out = append(out, "clear")
			return
		}
		_, next := m.Update(msg)
		run(next)
	}
	run(cmd)
	return out
}

// keySent sends a key and returns what it sent to the terminal past the screen.
func keySent(t *testing.T, m *Model, k string) []string {
	t.Helper()
	_, cmd := m.Update(keyMsg(k))
	return sent(t, m, cmd)
}

// pictureModel is a model on the Untagged tab whose file 104 holds pic, in a
// terminal of w by h cells that answered what it draws with says.
func pictureModel(t *testing.T, w, h int, pic image.Image, says ...tea.Msg) *Model {
	t.Helper()
	src := untaggedSource()
	m := newModel(t, src, w, h)
	m.runner = newFakeRunner()
	for _, msg := range says {
		m.Update(msg)
	}
	press(t, m, "2")
	cursorTo(t, m, 104)
	cur, _ := m.untagged.current()
	src.tags = map[string]tagwrite.Values{cur.Path: {tagwrite.Title: {"Live"}}}
	if pic != nil {
		src.pictures = map[string]image.Image{cur.Path: pic}
	}
	return m
}

var (
	saysSixel = uv.PrimaryDeviceAttributesEvent{61, 4, 6, 22}
	saysKitty = uv.KittyGraphicsEvent{Options: kitty.Options{ID: 31}, Payload: []byte("OK")}
)

// blockLines counts the lines of the screen that end in a picture drawn as
// blocks, and gives how many cells wide the picture is.
func blockLines(s string) (lines, cols int) {
	for _, l := range strings.Split(s, "\n") {
		if n := len(l) - len(strings.TrimRight(l, "▀")); n > 0 {
			lines++
			cols = n / len("▀")
		}
	}
	return lines, cols
}

func TestEditorShowsThePictureAsBlocks(t *testing.T) {
	m := pictureModel(t, 120, 32, plainPicture(300, 300))
	if got := keySent(t, m, "e"); len(got) != 0 {
		t.Errorf("a terminal that was not heard to draw images was sent %q", got)
	}
	got := screen(m)
	checkFits(t, got, 120, 32)
	if lines, cols := blockLines(got); lines != 16 || cols != 32 {
		t.Errorf("the picture is %d lines of %d cells, want 16 of 32:\n%s", lines, cols, got)
	}
	// It is in colour, and the form keeps clear of it.
	if !strings.Contains(m.View().Content, "\x1b[38;2;200;40;40;48;2;200;40;40m▀") {
		t.Error("the blocks do not have the picture's colour")
	}
	for _, l := range strings.Split(got, "\n") {
		if strings.Contains(l, "Title") && !strings.HasSuffix(l, "▀") {
			t.Errorf("the picture is not beside the form's first line: %q", l)
		}
	}

	// A wide picture is as wide as there is room, and lower.
	m = pictureModel(t, 120, 32, plainPicture(400, 100))
	keySent(t, m, "e")
	if lines, cols := blockLines(screen(m)); lines != 8 || cols != 58 {
		t.Errorf("a wide picture is %d lines of %d cells, want 8 of 58", lines, cols)
	}
}

func TestEditorWithoutAPictureOrRoom(t *testing.T) {
	m := pictureModel(t, 120, 32, nil)
	keySent(t, m, "e")
	if lines, _ := blockLines(screen(m)); lines != 0 {
		t.Errorf("a file without a picture is shown one:\n%s", screen(m))
	}
	// Too narrow for a form and a picture, and too low.
	for _, size := range [][2]int{{70, 32}, {120, 12}} {
		m = pictureModel(t, size[0], size[1], plainPicture(300, 300))
		keySent(t, m, "e")
		got := screen(m)
		checkFits(t, got, size[0], size[1])
		if lines, _ := blockLines(got); lines != 0 {
			t.Errorf("at %dx%d there is a picture:\n%s", size[0], size[1], got)
		}
	}
	// Made wide enough, it shows.
	m.Update(windowSize(120, 32))
	if lines, _ := blockLines(screen(m)); lines == 0 {
		t.Errorf("no picture once there is room:\n%s", screen(m))
	}
}

func TestEditorDrawsThePictureAsSixel(t *testing.T) {
	m := pictureModel(t, 120, 32, plainPicture(300, 300), saysSixel)
	got := keySent(t, m, "e")
	// At the right edge, on the form's first line: line 6, column 89.
	if len(got) != 1 || !strings.HasPrefix(got[0], "\x1b7\x1b[6;89H\x1bP0;1q\"1;1;320;320") || !strings.HasSuffix(got[0], "\x1b\\\x1b8") {
		t.Fatalf("opening the editor sent %.60q, want one Sixel image", got)
	}
	view := screen(m)
	checkFits(t, view, 120, 32)
	if lines, _ := blockLines(view); lines != 0 {
		t.Errorf("blocks are drawn under the image:\n%s", view)
	}
	// The cells under it are not ones the screen would clear.
	for _, l := range strings.Split(view, "\n") {
		if strings.Contains(l, "Title") && !strings.HasSuffix(l, strings.Repeat(" ", 26)) {
			t.Errorf("the line beside the image ends %q", l[len(l)-40:])
		}
	}

	// Typing leaves it alone.
	if got := keySent(t, m, "x"); len(got) != 0 {
		t.Errorf("typing sent %.40q", got)
	}
	// The terminal's size changing takes it off the screen: drawn again.
	_, cmd := m.Update(windowSize(130, 32))
	if got := sent(t, m, cmd); len(got) != 1 || !strings.HasPrefix(got[0], "\x1b7\x1b[6;99H\x1bP") {
		t.Errorf("a new size sent %.40q, want the image at its new place", got)
	}
	// Closing the editor draws the screen again without it.
	keySent(t, m, "esc")
	if got := keySent(t, m, "esc"); !reflect.DeepEqual(got, []string{"clear"}) {
		t.Errorf("closing the editor sent %.40q, want the screen cleared", got)
	}
	if m.pics.drawn != nil {
		t.Error("the image is still taken to be on the screen")
	}
}

func TestSixelIsDrawnAgainBesideWideText(t *testing.T) {
	m := pictureModel(t, 120, 32, plainPicture(300, 300), saysSixel)
	keySent(t, m, "e")
	// A line with a character two cells wide is written from its start when
	// it changes, which wipes the image off it.
	_, cmd := m.Update(textKey("語"))
	m.View()
	got := sent(t, m, cmd)
	_, cmd = m.Update(struct{}{})
	got = append(got, sent(t, m, cmd)...)
	if len(got) != 1 || !strings.HasPrefix(got[0], "\x1b7\x1b[6;89H\x1bP") {
		t.Fatalf("a wide character beside the image sent %.40q, want the image again", got)
	}
	m.View()
	_, cmd = m.Update(struct{}{})
	if got := sent(t, m, cmd); len(got) != 0 {
		t.Errorf("with nothing changed it was sent again: %.40q", got)
	}
}

func TestEditorDrawsThePictureByKitty(t *testing.T) {
	m := pictureModel(t, 120, 32, plainPicture(300, 300), saysSixel, saysKitty)
	if m.pics.mode != termpic.Kitty {
		t.Fatal("a terminal that draws both ways is not drawn to by Kitty's")
	}
	got := keySent(t, m, "e")
	if len(got) != 1 || !strings.HasPrefix(got[0], "\x1b7\x1b[6;89H\x1b_G") || !strings.Contains(got[0], "c=32,r=16") {
		t.Fatalf("opening the editor sent %.80q, want one Kitty image of 32 by 16 cells", got)
	}
	// Saved and closed: taken off by its ID, with nothing else redrawn.
	hit(t, m, "ctrl+s")
	if m.edit != nil {
		t.Fatalf("the editor did not close:\n%s", screen(m))
	}
	_, cmd := m.Update(struct{}{})
	got = sent(t, m, cmd)
	if m.pics.drawn != nil {
		t.Errorf("the image is still taken to be on the screen; sent %q", got)
	}
}

func TestKittyImageIsTakenOffOnClosing(t *testing.T) {
	m := pictureModel(t, 120, 32, plainPicture(300, 300), saysKitty)
	keySent(t, m, "e")
	if got := keySent(t, m, "esc"); len(got) != 1 || !strings.HasPrefix(got[0], "\x1b_Ga=d,d=I,i=7421") {
		t.Errorf("closing the editor sent %q, want the image deleted", got)
	}
}

func TestCellSizeShapesThePicture(t *testing.T) {
	// Cells of 8 by 16 pixels: 16 lines are 256 pixels, and so 32 cells.
	m := pictureModel(t, 120, 32, plainPicture(300, 300), saysSixel, uv.CellSizeEvent{Width: 8, Height: 16})
	got := keySent(t, m, "e")
	if len(got) != 1 || !strings.Contains(got[0], "\"1;1;256;256") {
		t.Errorf("sent %.60q, want an image of 256 by 256 pixels", got)
	}
	// Square cells: twice as many lines as before would be needed, so the
	// picture is 16 lines and 16 cells.
	m = pictureModel(t, 120, 32, plainPicture(300, 300), uv.CellSizeEvent{Width: 10, Height: 10})
	keySent(t, m, "e")
	if lines, cols := blockLines(screen(m)); lines != 16 || cols != 16 {
		t.Errorf("with square cells the picture is %d lines of %d cells, want 16 of 16", lines, cols)
	}
}

func TestPictureOfAnotherFileIsDropped(t *testing.T) {
	m := pictureModel(t, 120, 32, nil)
	keySent(t, m, "e")
	m.Update(editorPicMsg{id: 999, img: plainPicture(10, 10)})
	if m.edit.pic != nil {
		t.Error("the editor took the picture of a file it is not open on")
	}
}
