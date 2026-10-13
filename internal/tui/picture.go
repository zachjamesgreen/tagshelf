package tui

import (
	"errors"
	"image"
	"os"
	"strings"
	"time"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"

	"github.com/zachjamesgreen/tagshelf/internal/coverart"
	"github.com/zachjamesgreen/tagshelf/internal/termpic"
)

// The tag editor shows the picture its file holds beside the form, at the
// right edge of the screen. A terminal that draws images is sent the picture
// itself, over cells the screen leaves empty for it; any other has it drawn
// as coloured blocks, which are text like the rest of the screen.
//
// Bubble Tea knows nothing of an image. It writes only the cells that
// change, so one drawn after the screen stays until its cells are written,
// and has to be taken off when the editor goes. See picSync.

const (
	// picThumb is the most pixels a side of the picture is kept at.
	picThumb = 640
	// picMaxRows is the most lines the picture takes, and picMinRows the
	// fewest it is worth showing in.
	picMaxRows = 16
	picMinRows = 5
	// picFormMin is the narrowest the form is made for a picture beside it,
	// and picGap the cells between the two.
	picFormMin = 58
	picGap     = 2
	// picTop is the line of the editor's body the picture starts on: the
	// form's first.
	picTop = 2
	// picID is the ID the image has in a terminal that draws by the Kitty
	// graphics protocol. There is never more than one.
	picID = 7421
)

// picDelay is how long after the screen changed an image is drawn: long
// enough for the frame it belongs on to have been written.
var picDelay = 60 * time.Millisecond

// readPicture reads the first picture the audio file at path holds, small
// enough to show; nil when it holds none.
func readPicture(path string) (image.Image, error) {
	pic, err := coverart.Embedded(path)
	if errors.Is(err, coverart.ErrNoPicture) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return coverart.Thumb(pic, picThumb)
}

// editorPicMsg is the picture of the file the editor opened on, nil when it
// has none that can be shown.
type editorPicMsg struct {
	id  int64
	img image.Image
}

// pictures is how this terminal draws a picture, and the image on it.
type pictures struct {
	mode  termpic.Mode
	asked bool // the terminal has been asked what it draws
	// cellW and cellH are the size of a cell in pixels as the terminal gave
	// it, 0 when it did not.
	cellW, cellH int
	// drawn is the image on the screen, nil when there is none, and pending
	// the one about to be drawn. seq numbers the drawings, so that one
	// overtaken is dropped.
	drawn, pending *picPlace
	seq            int
	// made is what draws the image at madeFor, kept for drawing it again.
	made    string
	madeFor picPlace
}

// picPlace is an image and where on the screen it is: its top left cell, the
// cells it takes, and for Sixel its size in pixels.
type picPlace struct {
	img        image.Image
	mode       termpic.Mode
	x, y       int
	cols, rows int
	w, h       int
	// wide is the text beside the image on lines that hold a character of
	// two cells. The screen writes such a line again from its start whenever
	// it changes, which takes a Sixel image off it.
	wide string
}

// picDrawMsg is what draws the image at place, made off the UI goroutine.
type picDrawMsg struct {
	seq   int
	place picPlace
	data  string
	err   error
}

// picProbe asks the terminal, once, whether it draws images: when its size
// is first known, which is when there is a terminal to ask.
func (m *Model) picProbe() tea.Cmd {
	if m.pics.asked {
		return nil
	}
	m.pics.asked = true
	ask := termpic.RequestAttributes
	if termpic.MayKitty(os.Getenv) {
		ask = termpic.KittyQuery + ask
	}
	return tea.Raw(ask)
}

// picAnswer takes what the terminal says of itself, reporting whether msg
// was that.
func (m *Model) picAnswer(msg tea.Msg) bool {
	p := &m.pics
	switch msg := msg.(type) {
	case uv.KittyGraphicsEvent:
		if termpic.KittyAnswer(msg.Options.ID, msg.Payload) {
			p.mode = termpic.Kitty
		}
	case uv.PrimaryDeviceAttributesEvent:
		if p.mode == termpic.Blocks && termpic.HasSixel(msg) {
			p.mode = termpic.Sixel
		}
	case uv.CellSizeEvent:
		if msg.Width > 0 && msg.Height > 0 {
			p.cellW, p.cellH = msg.Width, msg.Height
		}
	default:
		return false
	}
	m.editorResized()
	return true
}

// cell is the size of a cell in pixels, as far as it is known.
func (p *pictures) cell() (w, h int) {
	if p.cellW > 0 && p.cellH > 0 {
		return p.cellW, p.cellH
	}
	return termpic.DefaultCellWidth, termpic.DefaultCellHeight
}

// editorPicLoaded takes the picture read for the editor's file.
func (m *Model) editorPicLoaded(msg editorPicMsg) {
	e := m.edit
	if e == nil || e.file.ID != msg.id || msg.img == nil {
		return
	}
	e.pic = msg.img
	m.editorResized()
}

// editorResized gives the editor's form the room it has now.
func (m *Model) editorResized() {
	if m.edit != nil && m.edit.form != nil {
		m.edit.form.WithWidth(m.editorWidth()).WithHeight(m.editorHeight())
	}
}

// picBox is where the editor shows its file's picture: cols by rows cells
// against the right edge of the screen, from line picTop of the body, with
// the picture w by h pixels in them. ok is false when there is no picture or
// no room for one.
func (m *Model) picBox() (cols, rows, w, h int, ok bool) {
	e := m.edit
	if e == nil || e.pic == nil || m.width < minWidth || m.height < minHeight {
		return 0, 0, 0, 0, false
	}
	maxCols := m.width - 2 - picFormMin - picGap
	maxRows := min(m.bodyHeight()-picTop-1, picMaxRows)
	if maxRows < picMinRows || maxCols < 2*picMinRows {
		return 0, 0, 0, 0, false
	}
	cw, ch := m.pics.cell()
	size := e.pic.Bounds().Size()
	if size.X <= 0 || size.Y <= 0 {
		return 0, 0, 0, 0, false
	}
	// As large as fits, whatever its shape.
	w, h = maxCols*cw, size.Y*maxCols*cw/size.X
	if h > maxRows*ch {
		w, h = size.X*maxRows*ch/size.Y, maxRows*ch
	}
	w, h = max(w, 1), max(h, 1)
	cols, rows = (w+cw-1)/cw, (h+ch-1)/ch
	return cols, rows, w, h, true
}

// picWanted is the image that belongs on the screen as it is drawn now; ok
// is false when none does, as in a terminal that is shown blocks.
func (m *Model) picWanted() (picPlace, bool) {
	cols, rows, w, h, ok := m.picBox()
	if !ok || m.pics.mode == termpic.Blocks || m.edit.form == nil {
		return picPlace{}, false
	}
	place := picPlace{
		img: m.edit.pic, mode: m.pics.mode,
		x: m.width - cols, y: m.height - m.bodyHeight() - 2 + picTop,
		cols: cols, rows: rows, w: w, h: h,
	}
	if place.mode == termpic.Sixel {
		// As the editor would be drawn now, not as it last was.
		m.editorBody()
		place.wide = m.edit.beside
	}
	return place, true
}

// draw makes what draws the image at its place.
func (p picPlace) draw() (string, error) {
	if p.mode == termpic.Kitty {
		return termpic.KittyAt(p.img, picID, p.x, p.y, p.cols, p.rows)
	}
	return termpic.SixelAt(coverart.Resize(p.img, p.w, p.h), p.x, p.y)
}

// picSync sees to it, after the message msg was handled, that the image on
// the screen is the one that belongs there. An image that no longer does is
// taken off: by its ID in a terminal of the Kitty protocol, and for Sixel by
// drawing the whole screen again. One that is missing is drawn once the
// frame it belongs on has been written.
func (m *Model) picSync(msg tea.Msg) tea.Cmd {
	p := &m.pics
	if p.mode == termpic.Blocks {
		return nil
	}
	var off tea.Cmd
	if _, resized := msg.(tea.WindowSizeMsg); resized && p.drawn != nil {
		// The screen is drawn anew, which takes a Sixel image with it.
		if p.mode == termpic.Kitty {
			off = tea.Raw(termpic.KittyDelete(picID))
		}
		p.drawn = nil
	}
	want, ok := m.picWanted()
	if d := p.drawn; d != nil {
		was := *d
		was.wide = want.wide
		if !ok || was != want {
			off = tea.ClearScreen
			if p.mode == termpic.Kitty {
				off = tea.Raw(termpic.KittyDelete(picID))
			}
			p.drawn = nil
		}
	}
	if !ok {
		p.pending = nil
		return off
	}
	if (p.drawn != nil && *p.drawn == want) || (p.pending != nil && *p.pending == want) {
		return off
	}
	p.pending = &want
	p.seq++
	seq := p.seq
	made := ""
	plain := want
	plain.wide = ""
	if p.madeFor == plain {
		made = p.made
	}
	draw := func() tea.Msg {
		time.Sleep(picDelay)
		msg := picDrawMsg{seq: seq, place: want, data: made}
		if made == "" {
			msg.data, msg.err = want.draw()
		}
		return msg
	}
	if off == nil {
		return draw
	}
	return tea.Sequence(off, draw)
}

// picDrawn sends the terminal the image that was made, if it still belongs
// where it was made for.
func (m *Model) picDrawn(msg picDrawMsg) tea.Cmd {
	p := &m.pics
	if msg.seq != p.seq {
		return nil
	}
	p.pending = nil
	if msg.err != nil {
		// Blocks are always there to fall back on.
		p.mode = termpic.Blocks
		return nil
	}
	p.made, p.madeFor = msg.data, msg.place
	p.madeFor.wide = ""
	if want, ok := m.picWanted(); !ok || want != msg.place {
		return nil // picSync asks again for what belongs there now
	}
	p.drawn = &msg.place
	return tea.Raw(msg.data)
}

// picRows are the lines of the picture's box, each cols cells wide. For
// blocks they are the picture. For an image they are cells that look empty
// and are not: the screen clears a line to its end when only spaces are
// left on it, and moves lines that are alike up and down as one, and either
// would take the image with it. No-break spaces are not cleared, and the
// lines differ in which of their first cells are bold.
func (m *Model) picRows(cols, rows int) []string {
	e := m.edit
	if m.pics.mode == termpic.Blocks {
		if len(e.picLines) != rows || e.picCols != cols {
			e.picLines, e.picCols = termpic.BlockLines(coverart.Resize(e.pic, cols, 2*rows)), cols
		}
		return e.picLines
	}
	lines := make([]string, rows)
	for i := range lines {
		var b strings.Builder
		for bit := range min(cols-1, 6) {
			if (i+1)>>bit&1 == 1 {
				b.WriteString("\x1b[1m \x1b[22m")
			} else {
				b.WriteString(" ")
			}
		}
		b.WriteString(strings.Repeat(" ", cols-min(cols-1, 6)))
		lines[i] = b.String()
	}
	return lines
}

// withPicture puts the picture of the editor's file beside its body, when
// it has one and there is room.
func (m *Model) withPicture(body string) string {
	e := m.edit
	e.beside = ""
	cols, rows, _, _, ok := m.picBox()
	if !ok || e.form == nil {
		return body
	}
	lines := strings.Split(body, "\n")
	for len(lines) < picTop+rows {
		lines = append(lines, "")
	}
	left := m.width - cols
	var wide strings.Builder
	for i, row := range m.picRows(cols, rows) {
		line := ansi.Truncate(lines[picTop+i], left-1, "")
		if text := ansi.Strip(line); ansi.StringWidth(text) != utf8.RuneCountInString(text) {
			// With its styles: a cursor that blinks changes the line too.
			wide.WriteString(line + "\n")
		}
		pad := strings.Repeat(" ", max(left-ansi.StringWidth(line), 0))
		lines[picTop+i] = line + "\x1b[m" + pad + row
	}
	e.beside = wide.String()
	return strings.Join(lines, "\n")
}
