// Copyright 2015, David Howden
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package tag

import (
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"
)

func newMetadataVorbis() *metadataVorbis {
	return &metadataVorbis{
		c: make(map[string]string),
	}
}

type metadataVorbis struct {
	c map[string]string // the vorbis comments by lower-cased key; repeated keys NUL-joined
	p *Picture
}

// get returns the first value of the comment named k (lower case).
func (m *metadataVorbis) get(k string) string {
	v, _, _ := strings.Cut(m.c[k], "\x00")
	return v
}

// add records a comment. A key written more than once keeps every value,
// joined with NUL in the order they came, as the ASF reader does and as mbz
// expects of a list of IDs.
func (m *metadataVorbis) add(k, v string) {
	if old, ok := m.c[k]; ok {
		m.c[k] = old + "\x00" + v
		return
	}
	m.c[k] = v
}

func (m *metadataVorbis) readVorbisComment(r io.Reader) error {
	vendorLen, err := readUint32LittleEndian(r)
	if err != nil {
		return err
	}

	vendor, err := readString(r, uint(vendorLen))
	if err != nil {
		return err
	}
	m.c["vendor"] = vendor

	commentsLen, err := readUint32LittleEndian(r)
	if err != nil {
		return err
	}

	for i := uint32(0); i < commentsLen; i++ {
		l, err := readUint32LittleEndian(r)
		if err != nil {
			return err
		}
		s, err := readString(r, uint(l))
		if err != nil {
			return err
		}
		k, v, err := parseComment(s)
		if err != nil {
			return err
		}
		m.add(strings.ToLower(k), v)
	}

	if _, ok := m.c["metadata_block_picture"]; ok {
		b64data := m.get("metadata_block_picture")
		data, err := base64.StdEncoding.DecodeString(b64data)
		if err != nil {
			return err
		}
		m.readPictureBlock(bytes.NewReader(data))
	}

	return nil
}

func (m *metadataVorbis) readPictureBlock(r io.Reader) error {
	b, err := readInt(r, 4)
	if err != nil {
		return err
	}
	pictureType, ok := pictureTypes[byte(b)]
	if !ok {
		return fmt.Errorf("invalid picture type: %v", b)
	}
	mimeLen, err := readUint(r, 4)
	if err != nil {
		return err
	}
	mime, err := readString(r, mimeLen)
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
	desc, err := readString(r, descLen)
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

	dataLen, err := readInt(r, 4)
	if err != nil {
		return err
	}
	data := make([]byte, dataLen)
	_, err = io.ReadFull(r, data)
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

func parseComment(c string) (k, v string, err error) {
	kv := strings.SplitN(c, "=", 2)
	if len(kv) != 2 {
		err = errors.New("vorbis comment must contain '='")
		return
	}
	k = kv[0]
	v = kv[1]
	return
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
	// it is commonly assigned to albumartist.
	return m.get("albumartist")
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
	var dateFormat string

	// The date need to follow the international standard https://en.wikipedia.org/wiki/ISO_8601
	// and obviously the VorbisComment standard https://wiki.xiph.org/VorbisComment#Date_and_time
	switch len(m.get("date")) {
	case 0:
		// Fallback on year tag as some files use that.
		if len(m.get("year")) != 0 {
			year, err := strconv.Atoi(m.get("year"))
			if err == nil {
				return year
			}
		}
		return 0
	case 4:
		dateFormat = "2006"
	case 7:
		dateFormat = "2006-01"
	case 10:
		dateFormat = "2006-01-02"
	}

	t, _ := time.Parse(dateFormat, m.get("date"))
	return t.Year()
}

func (m *metadataVorbis) Track() (int, int) {
	// https://wiki.xiph.org/Field_names
	return m.xOfN("tracknumber", "tracktotal")
}

func (m *metadataVorbis) Disc() (int, int) {
	// https://wiki.xiph.org/Field_names
	return m.xOfN("discnumber", "disctotal")
}

// xOfN reads a number that may be written "N/M", as many taggers do, with a
// separate total key taking precedence over M.
func (m *metadataVorbis) xOfN(number, total string) (int, int) {
	x, n := parseXofN(strings.TrimSpace(m.get(number)))
	if t, err := strconv.Atoi(strings.TrimSpace(m.get(total))); err == nil {
		n = t
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
