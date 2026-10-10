// Copyright 2015, David Howden
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package tag

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"
)

func newMetadataVorbis() *metadataVorbis {
	return &metadataVorbis{
		c: make(map[string]string),
	}
}

type metadataVorbis struct {
	c map[string]string // the vorbis comments by lower-cased key; repeated keys NUL-joined
	p *Picture

	left firstSkip // the first comment or picture left out, see Skipped
}

func (m *metadataVorbis) skipped() error { return m.left.err }

// get returns the first value of the comment named k (lower case).
func (m *metadataVorbis) get(k string) string {
	v, _, _ := strings.Cut(m.c[k], "\x00")
	return v
}

// readVorbisString reads a string of n bytes from r, which holds the rest of
// the block or packet it is in. A length that runs past the end is refused
// before anything is allocated for it.
func readVorbisString(r *bytes.Reader, n uint) (string, error) {
	if n > uint(r.Len()) {
		return "", fmt.Errorf("length %d runs past the %d bytes left of the block", n, r.Len())
	}
	return readString(r, n)
}

// readVorbisComment reads the Vorbis comment in r, which holds the block or
// packet and nothing after it. A key written more than once keeps every
// value, joined with NUL in the order they came, as the ASF reader does and
// as mbz expects of a list of IDs. A comment with no "=", and a picture that
// cannot be decoded, are left out.
func (m *metadataVorbis) readVorbisComment(r *bytes.Reader) error {
	vendorLen, err := readUint32LittleEndian(r)
	if err != nil {
		return err
	}

	vendor, err := readVorbisString(r, uint(vendorLen))
	if err != nil {
		return err
	}

	commentsLen, err := readUint32LittleEndian(r)
	if err != nil {
		return err
	}

	// The values of each key are joined once, at the end: joining as they
	// come copies all the earlier ones for every repeat.
	values := map[string][]string{"vendor": {vendor}}
	for i := uint32(0); i < commentsLen && err == nil; i++ {
		var l uint32
		var s string
		if l, err = readUint32LittleEndian(r); err != nil {
			break
		}
		if s, err = readVorbisString(r, uint(l)); err != nil {
			break
		}
		if k, v, ok := strings.Cut(s, "="); ok {
			k = strings.ToLower(k)
			values[k] = append(values[k], v)
		} else {
			m.left.skip("comment %d left out: it has no \"=\"", i+1)
		}
	}
	for k, vs := range values {
		m.c[k] = strings.Join(vs, "\x00")
	}
	if err != nil {
		return err
	}

	if b64data := m.get("metadata_block_picture"); b64data != "" {
		if data, err := base64.StdEncoding.DecodeString(b64data); err != nil {
			m.left.skip("METADATA_BLOCK_PICTURE left out: %v", err)
		} else if err := m.readPictureBlock(bytes.NewReader(data)); err != nil {
			m.left.skip("METADATA_BLOCK_PICTURE left out: %v", err)
		}
	}

	return nil
}

// readPictureBlock reads the picture in r, which holds the block and nothing
// after it. Each length is checked against what is left of the block.
func (m *metadataVorbis) readPictureBlock(r *bytes.Reader) error {
	b, err := readInt(r, 4)
	if err != nil {
		return err
	}
	// A type outside the list (above 20) has no name.
	var pictureType string
	if b >= 0 && b <= 0xFF {
		pictureType = pictureTypes[byte(b)]
	}
	mimeLen, err := readUint(r, 4)
	if err != nil {
		return err
	}
	mime, err := readVorbisString(r, mimeLen)
	if err != nil {
		return err
	}

	ext := ""
	switch mime {
	case "image/jpeg":
		ext = "jpg"
	case "image/png":
		ext = "png"
	case "image/gif":
		ext = "gif"
	}

	descLen, err := readUint(r, 4)
	if err != nil {
		return err
	}
	desc, err := readVorbisString(r, descLen)
	if err != nil {
		return err
	}

	// We skip width <32>, height <32>, colorDepth <32>, coloresUsed <32>
	_, err = readInt(r, 4) // width
	if err != nil {
		return err
	}
	_, err = readInt(r, 4) // height
	if err != nil {
		return err
	}
	_, err = readInt(r, 4) // color depth
	if err != nil {
		return err
	}
	_, err = readInt(r, 4) // colors used
	if err != nil {
		return err
	}

	dataLen, err := readUint(r, 4)
	if err != nil {
		return err
	}
	if dataLen > uint(r.Len()) {
		return fmt.Errorf("picture data length %d runs past the %d bytes left of the block", dataLen, r.Len())
	}
	data, err := readBytes(r, dataLen)
	if err != nil {
		return err
	}

	m.p = &Picture{
		Ext:         ext,
		MIMEType:    mime,
		Type:        pictureType,
		Description: desc,
		Data:        data,
	}
	return nil
}

func (m *metadataVorbis) Format() Format {
	return VORBIS
}

func (m *metadataVorbis) Raw() map[string]interface{} {
	raw := make(map[string]interface{}, len(m.c))
	for k, v := range m.c {
		raw[k] = v
	}
	return raw
}

func (m *metadataVorbis) Title() string {
	return m.get("title")
}

func (m *metadataVorbis) Artist() string {
	// ARTIST
	// The artist generally considered responsible for the work. In popular music
	// this is usually the performing band or singer. For classical music it would
	// be the composer. For an audio book it would be the author of the original text.
	return m.get("artist")
}

func (m *metadataVorbis) Album() string {
	return m.get("album")
}

func (m *metadataVorbis) AlbumArtist() string {
	// This field isn't actually included in the standard, though
	// it is commonly assigned to albumartist. Some taggers write it
	// with a space.
	if s := m.get("albumartist"); s != "" {
		return s
	}
	return m.get("album artist")
}

func (m *metadataVorbis) Composer() string {
	if m.get("composer") != "" {
		return m.get("composer")
	}
	// PERFORMER
	// The artist(s) who performed the work. In classical music this would be the
	// conductor, orchestra, soloists. In an audio book it would be the actor who
	// did the reading. In popular music this is typically the same as the ARTIST
	// and is omitted.
	if m.get("performer") != "" {
		return m.get("performer")
	}
	return m.get("artist")
}

func (m *metadataVorbis) Genre() string {
	return m.get("genre")
}

func (m *metadataVorbis) Year() int {
	// The date need to follow the international standard https://en.wikipedia.org/wiki/ISO_8601
	// and obviously the VorbisComment standard https://wiki.xiph.org/VorbisComment#Date_and_time
	// Taggers write other forms too: a date that does not start with a
	// year gives 0, not the year 1 of an unparsed time.
	if year := leadingYear(m.get("date")); year != 0 {
		return year
	}
	// Fallback on year tag as some files use that.
	return leadingYear(m.get("year"))
}

func (m *metadataVorbis) Track() (int, int) {
	// https://wiki.xiph.org/Field_names
	return m.xOfN("tracknumber", "tracktotal", "totaltracks")
}

func (m *metadataVorbis) Disc() (int, int) {
	// https://wiki.xiph.org/Field_names
	return m.xOfN("discnumber", "disctotal", "totaldiscs")
}

// xOfN reads a number that may be written "N/M", as many taggers do, with a
// separate total key taking precedence over M: the first of totals that
// holds a number. Picard writes both TRACKTOTAL and TOTALTRACKS, other
// taggers only the second.
func (m *metadataVorbis) xOfN(number string, totals ...string) (int, int) {
	x, n := parseXofN(strings.TrimSpace(m.get(number)))
	for _, total := range totals {
		if t, err := strconv.Atoi(strings.TrimSpace(m.get(total))); err == nil {
			n = t
			break
		}
	}
	return x, n
}

func (m *metadataVorbis) Lyrics() string {
	return m.get("lyrics")
}

func (m *metadataVorbis) Comment() string {
	if m.get("comment") != "" {
		return m.get("comment")
	}
	return m.get("description")
}

func (m *metadataVorbis) Picture() *Picture {
	return m.p
}
