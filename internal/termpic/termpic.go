// Package termpic writes a picture to a terminal: as a real image where the
// terminal draws them, by the Kitty graphics protocol or as Sixel, and
// otherwise as coloured blocks, which every terminal draws.
package termpic

import (
	"bytes"
	"fmt"
	"image"
	"strconv"
	"strings"

	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/ansi/kitty"
	"github.com/charmbracelet/x/ansi/sixel"
)

// Mode is how a terminal draws a picture.
type Mode int

const (
	// Blocks draws a picture as text: half a cell to a pixel, in colour.
	Blocks Mode = iota
	// Sixel draws it as a Sixel image, which lies in the cells it covers and
	// goes when they are written or erased.
	Sixel
	// Kitty draws it by the Kitty graphics protocol, over the cells, where it
	// stays until it is deleted.
	Kitty
)

// The size in pixels a cell is taken to have when the terminal has not said:
// that of the VT340, which is also what Windows Terminal draws Sixel by.
const (
	DefaultCellWidth  = 10
	DefaultCellHeight = 20
)

// HasSixel reports whether a terminal's answer to RequestAttributes, its
// primary device attributes, says it draws Sixel.
func HasSixel(attrs []int) bool {
	for _, a := range attrs {
		if a == 4 {
			return true
		}
	}
	return false
}

// kittyQueryID is the image ID the question of KittyQuery is asked with, which
// its answer carries back.
const kittyQueryID = 31

// RequestAttributes asks a terminal for its primary device attributes and
// the size of a cell in pixels. Every terminal takes the questions; not
// every one answers.
const RequestAttributes = ansi.RequestPrimaryDeviceAttributes + "\x1b[16t"

// KittyQuery asks a terminal whether it draws by the Kitty graphics protocol:
// one that does answers, see KittyAnswer. It is only for a terminal that may
// well: one that has never heard of the protocol may show the question.
var KittyQuery = ansi.KittyGraphics([]byte("AAAA"),
	"i="+strconv.Itoa(kittyQueryID), "s=1", "v=1", "a=q", "t=d", "f=24")

// MayKitty reports whether the environment names a terminal that may draw by
// the Kitty graphics protocol, so that it is worth asking.
func MayKitty(getenv func(string) string) bool {
	if getenv("TMUX") != "" {
		return false // tmux passes neither the question nor the pictures on
	}
	term := strings.ToLower(getenv("TERM"))
	switch strings.ToLower(getenv("TERM_PROGRAM")) {
	case "ghostty", "wezterm", "kitty":
		return true
	}
	return strings.Contains(term, "kitty") || strings.Contains(term, "ghostty") ||
		getenv("KITTY_WINDOW_ID") != "" || getenv("KONSOLE_VERSION") != ""
}

// KittyAnswer reports whether a Kitty graphics answer with the image ID id
// and the given payload says yes to KittyQuery.
func KittyAnswer(id int, payload []byte) bool {
	return id == kittyQueryID && string(payload) == "OK"
}

// BlockLines draws img as text of as many cells as it has pixels across, and
// half as many lines as it has pixels down: each cell an upper half block,
// coloured as the pixel it is over the colour of the pixel below.
func BlockLines(img image.Image) []string {
	b := img.Bounds()
	lines := make([]string, 0, (b.Dy()+1)/2)
	rgb := func(x, y int) (r, g, bl uint32) {
		if y >= b.Max.Y {
			y = b.Max.Y - 1
		}
		r, g, bl, _ = img.At(x, y).RGBA()
		return r >> 8, g >> 8, bl >> 8
	}
	for y := b.Min.Y; y < b.Max.Y; y += 2 {
		var sb strings.Builder
		for x := b.Min.X; x < b.Max.X; x++ {
			tr, tg, tb := rgb(x, y)
			br, bg, bb := rgb(x, y+1)
			fmt.Fprintf(&sb, "\x1b[38;2;%d;%d;%d;48;2;%d;%d;%dm▀", tr, tg, tb, br, bg, bb)
		}
		sb.WriteString("\x1b[m")
		lines = append(lines, sb.String())
	}
	return lines
}

// at wraps what draws a picture so that it is drawn with its top left corner
// in the cell at column x of line y, both counted from 0, and the cursor is
// left where it was.
func at(x, y int, draw string) string {
	return ansi.SaveCursor + ansi.CursorPosition(x+1, y+1) + draw + ansi.RestoreCursor
}

// SixelAt is what draws img, pixel for pixel, as a Sixel image with its top
// left corner in the cell at column x of line y.
func SixelAt(img image.Image, x, y int) (string, error) {
	var buf bytes.Buffer
	if err := new(sixel.Encoder).Encode(&buf, img); err != nil {
		return "", err
	}
	// The second parameter leaves a pixel that has no colour as it is.
	return at(x, y, ansi.SixelGraphics(0, 1, 0, buf.Bytes())), nil
}

// KittyAt is what draws img by the Kitty graphics protocol as the image with
// the ID id, over cols by rows cells with the top left one at column x of
// line y. The terminal fits the image to the cells.
func KittyAt(img image.Image, id, x, y, cols, rows int) (string, error) {
	var buf bytes.Buffer
	err := kitty.EncodeGraphics(&buf, img, &kitty.Options{
		Action: kitty.TransmitAndPut, Transmission: kitty.Direct, Format: kitty.RGBA,
		ID: id, ImageWidth: img.Bounds().Dx(), ImageHeight: img.Bounds().Dy(),
		Columns: cols, Rows: rows, DoNotMoveCursor: true, Quiet: 2, Chunk: true,
	})
	if err != nil {
		return "", err
	}
	return at(x, y, buf.String()), nil
}

// KittyDelete is what takes the image with the ID id off the screen and out
// of the terminal's memory.
func KittyDelete(id int) string {
	return ansi.KittyGraphics(nil, "a=d", "d=I", "i="+strconv.Itoa(id), "q=2")
}
