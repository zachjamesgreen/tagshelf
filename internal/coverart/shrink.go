package coverart

import (
	"bytes"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
)

// EmbedSize is the size, in pixels, of a picture put into an audio file:
// the archive's middle size, and what a large picture of a folder is made
// smaller to.
const EmbedSize = Medium

// EmbedLimit is the most bytes a folder's picture may have to go into its
// audio files as it is. A larger one is made smaller first: it would be
// written once for every file of the album.
const EmbedLimit = 500 << 10

// maxPixels bounds the picture that is decoded to be made smaller: decoded,
// each pixel takes four bytes or more.
const maxPixels = 64 << 20

// jpegQuality is the quality a picture made smaller is written with.
const jpegQuality = 90

// ForEmbedding gives pic as it goes into an audio file: as it is when it has
// no more than EmbedLimit bytes, and otherwise made smaller, to EmbedSize
// pixels on its longer side, as a JPEG. The picture given is not changed.
func ForEmbedding(pic Picture) (Picture, error) {
	if len(pic.Data) <= EmbedLimit {
		return pic, nil
	}
	return Shrink(pic, EmbedSize)
}

// Shrink makes pic smaller, so that its longer side has size pixels, and
// returns it as a JPEG. A picture no larger than that is only written again.
func Shrink(pic Picture, size int) (Picture, error) {
	if pic.Width <= 0 || pic.Height <= 0 || pic.Width*pic.Height > maxPixels {
		return Picture{}, fmt.Errorf("a picture of %d by %d pixels is too large to make smaller", pic.Width, pic.Height)
	}
	src, _, err := image.Decode(bytes.NewReader(pic.Data))
	if err != nil {
		return Picture{}, errors.New("it is not a JPEG or PNG picture")
	}
	w, h := pic.Width, pic.Height
	if longer := max(w, h); longer > size {
		w, h = max(w*size/longer, 1), max(h*size/longer, 1)
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, average(src, w, h), &jpeg.Options{Quality: jpegQuality}); err != nil {
		return Picture{}, err
	}
	return Describe(buf.Bytes())
}

// average draws src at w by h pixels, each the average of the pixels of src
// it covers: the plain way to make a picture smaller, and a good one when it
// is made much smaller. What shows through a picture that is not opaque is
// white.
func average(src image.Image, w, h int) image.Image {
	b := src.Bounds()
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		y0, y1 := b.Min.Y+y*b.Dy()/h, b.Min.Y+(y+1)*b.Dy()/h
		y1 = max(y1, y0+1)
		for x := range w {
			x0, x1 := b.Min.X+x*b.Dx()/w, b.Min.X+(x+1)*b.Dx()/w
			x1 = max(x1, x0+1)
			var r, g, bl, n uint64
			for sy := y0; sy < y1; sy++ {
				for sx := x0; sx < x1; sx++ {
					// Premultiplied by its alpha, each of 16 bits: what is
					// missing of the alpha is the white behind it.
					cr, cg, cb, ca := src.At(sx, sy).RGBA()
					white := uint64(0xffff - ca)
					r += uint64(cr) + white
					g += uint64(cg) + white
					bl += uint64(cb) + white
					n++
				}
			}
			dst.SetRGBA(x, y, color.RGBA{R: uint8(r / n >> 8), G: uint8(g / n >> 8), B: uint8(bl / n >> 8), A: 0xff})
		}
	}
	return dst
}
