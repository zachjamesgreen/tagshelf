package termpic

import (
	"image"
	"image/color"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

// quarters is a picture of 4 by 4 pixels: red above blue on the left, green
// above white on the right.
func quarters() image.Image {
	img := image.NewRGBA(image.Rect(0, 0, 4, 4))
	for y := range 4 {
		for x := range 4 {
			c := color.RGBA{255, 0, 0, 255}
			switch {
			case x >= 2 && y >= 2:
				c = color.RGBA{255, 255, 255, 255}
			case x >= 2:
				c = color.RGBA{0, 255, 0, 255}
			case y >= 2:
				c = color.RGBA{0, 0, 255, 255}
			}
			img.SetRGBA(x, y, c)
		}
	}
	return img
}

func TestBlockLines(t *testing.T) {
	lines := BlockLines(quarters())
	if len(lines) != 2 {
		t.Fatalf("%d lines, want 2", len(lines))
	}
	for i, l := range lines {
		if got := ansi.Strip(l); got != "▀▀▀▀" {
			t.Errorf("line %d is %q, want four half blocks", i, got)
		}
		if !strings.HasSuffix(l, "\x1b[m") {
			t.Errorf("line %d leaves its colours on: %q", i, l)
		}
	}
	// Each cell has the pixel above as its colour and the one below behind it.
	if want := "\x1b[38;2;255;0;0;48;2;255;0;0m▀"; !strings.HasPrefix(lines[0], want) {
		t.Errorf("the first line starts %q, want %q", lines[0][:len(want)], want)
	}
	if want := "\x1b[38;2;255;255;255;48;2;255;255;255m▀\x1b[m"; !strings.HasSuffix(lines[1], want) {
		t.Errorf("the second line ends %q, want %q", lines[1], want)
	}

	// One pixel over another is one cell of both colours.
	two := image.NewRGBA(image.Rect(0, 0, 1, 2))
	two.SetRGBA(0, 0, color.RGBA{255, 0, 0, 255})
	two.SetRGBA(0, 1, color.RGBA{0, 0, 255, 255})
	if got := BlockLines(two); len(got) != 1 || got[0] != "\x1b[38;2;255;0;0;48;2;0;0;255m▀\x1b[m" {
		t.Errorf("red over blue is %q", got)
	}

	// An odd line at the bottom is drawn over itself.
	odd := image.NewRGBA(image.Rect(0, 0, 1, 3))
	if lines := BlockLines(odd); len(lines) != 2 {
		t.Errorf("3 pixels down are %d lines, want 2", len(lines))
	}
}

func TestSixelAt(t *testing.T) {
	got, err := SixelAt(quarters(), 9, 2)
	if err != nil {
		t.Fatal(err)
	}
	// The cursor is put there and back; a pixel is a pixel.
	if !strings.HasPrefix(got, "\x1b7\x1b[3;10H\x1bP0;1q\"1;1;4;4") || !strings.HasSuffix(got, "\x1b\\\x1b8") {
		t.Errorf("got %q", got)
	}
}

func TestKittyAt(t *testing.T) {
	big := image.NewRGBA(image.Rect(0, 0, 80, 80))
	got, err := KittyAt(big, 5, 9, 2, 8, 4)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(got, "\x1b7\x1b[3;10H\x1b_G") || !strings.HasSuffix(got, "\x1b\\\x1b8") {
		t.Errorf("got %.60q", got)
	}
	head, _, _ := strings.Cut(got, ";AAAA")
	for _, opt := range []string{"a=T", "i=5", "s=80", "v=80", "c=8", "r=4", "C=1", "q=2", "m=1"} {
		if !strings.Contains(head+",", opt+",") {
			t.Errorf("the image is sent without %s: %q", opt, head)
		}
	}
	// 25,600 bytes do not go in one piece.
	if n := strings.Count(got, "\x1b_G"); n < 2 {
		t.Errorf("sent in %d pieces", n)
	}
	if !strings.Contains(got, ",m=0;") {
		t.Error("the last piece does not say it is the last")
	}

	if got := KittyDelete(5); got != "\x1b_Ga=d,d=I,i=5,q=2\x1b\\" {
		t.Errorf("KittyDelete = %q", got)
	}
}

func TestWhatATerminalDraws(t *testing.T) {
	if !HasSixel([]int{61, 4, 6}) || HasSixel([]int{61, 6, 22}) || HasSixel(nil) {
		t.Error("HasSixel does not go by attribute 4")
	}
	if !KittyAnswer(31, []byte("OK")) || KittyAnswer(31, []byte("ENOTSUPPORTED")) || KittyAnswer(7, []byte("OK")) {
		t.Error("KittyAnswer does not go by the ID and OK")
	}
	env := func(vars map[string]string) func(string) string {
		return func(k string) string { return vars[k] }
	}
	for _, tt := range []struct {
		vars map[string]string
		want bool
	}{
		{map[string]string{"TERM": "xterm-kitty"}, true},
		{map[string]string{"TERM": "xterm-ghostty"}, true},
		{map[string]string{"TERM_PROGRAM": "WezTerm"}, true},
		{map[string]string{"KITTY_WINDOW_ID": "1"}, true},
		{map[string]string{"TERM": "xterm-kitty", "TMUX": "/tmp/tmux-1000/default,1,0"}, false},
		{map[string]string{"TERM": "xterm-256color", "WT_SESSION": "x"}, false},
		{nil, false},
	} {
		if got := MayKitty(env(tt.vars)); got != tt.want {
			t.Errorf("MayKitty(%v) = %v, want %v", tt.vars, got, tt.want)
		}
	}
	if !strings.HasPrefix(KittyQuery, "\x1b_Gi=31,") || !strings.Contains(KittyQuery, "a=q") {
		t.Errorf("KittyQuery = %q", KittyQuery)
	}
}
