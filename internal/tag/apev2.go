package tag

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"path"
	"strconv"
	"strings"
)

// APEv2 tags (https://wiki.hydrogenaud.io/index.php?title=APEv2_specification)
// sit at the end of APE, MPC and .aac files: a 32-byte header, the items and
// a 32-byte footer, little-endian. The layout of a file is
// [ID3v2] audio [APEv2] [ID3v1], though mutagen appends APEv2 after an
// existing ID3v1, and TagLib then appends another ID3v1.

const (
	apePreamble      = "APETAGEX"
	apeBlockSize     = 32      // the header and the footer
	apeFlagHasHeader = 1 << 31 // the tag has a header
	apeFlagIsHeader  = 1 << 29 // this block is the header, not the footer
)

// trailingTags is where the audio ends and the tags after it are.
type trailingTags struct {
	audioEnd int64 // the offset after the last audio byte

	ape      bool   // there is an APEv2 tag
	apeStart int64  // the offset of its header, or of its first item without one
	apeItems int64  // the offset of its first item
	apeSize  int64  // the bytes from its first item to the end of its footer
	apeCount uint32 // its item count
}

// findTrailingTags locates the APEv2 and ID3v1 tags that end the audio of a
// file whose audio starts at start and which is end bytes long: an ID3v1 tag,
// then an APEv2 tag ending where that starts, then another ID3v1 tag before
// the APEv2 one.
func findTrailingTags(r io.ReadSeeker, start, end int64) (trailingTags, error) {
	t := trailingTags{audioEnd: end}
	var err error
	if t.audioEnd, err = stripID3v1(r, start, t.audioEnd); err != nil {
		return t, err
	}

	if t.audioEnd-start < apeBlockSize {
		return t, nil
	}
	if _, err := r.Seek(t.audioEnd-apeBlockSize, io.SeekStart); err != nil {
		return t, fmt.Errorf("error seeking to APEv2 footer: %v", err)
	}
	b, err := readBytes(r, apeBlockSize)
	if err != nil {
		return t, fmt.Errorf("error reading APEv2 footer: %v", err)
	}
	if string(b[:8]) != apePreamble {
		return t, nil
	}
	size := int64(binary.LittleEndian.Uint32(b[12:16])) // items and footer
	flags := binary.LittleEndian.Uint32(b[20:24])
	tagStart := t.audioEnd - size
	if flags&apeFlagHasHeader != 0 {
		tagStart -= apeBlockSize
	}
	if flags&apeFlagIsHeader != 0 || size < apeBlockSize || tagStart < start {
		return t, nil // not a footer we can use
	}

	t.ape = true
	t.apeStart = tagStart
	t.apeItems = t.audioEnd - size
	t.apeSize = size
	t.apeCount = binary.LittleEndian.Uint32(b[16:20])
	t.audioEnd, err = stripID3v1(r, start, tagStart)
	return t, err
}

// stripID3v1 returns end, less 128 bytes if an ID3v1 tag ends there. Bytes
// that are the footer of an APEv2 tag are not taken for one.
func stripID3v1(r io.ReadSeeker, start, end int64) (int64, error) {
	if end-start < 128 {
		return end, nil
	}
	if _, err := r.Seek(end-128, io.SeekStart); err != nil {
		return 0, fmt.Errorf("error seeking to ID3v1 tag: %v", err)
	}
	b, err := readBytes(r, 128)
	if err != nil {
		return 0, fmt.Errorf("error reading ID3v1 tag: %v", err)
	}
	if string(b[:3]) != "TAG" || string(b[128-apeBlockSize:128-apeBlockSize+8]) == apePreamble {
		return end, nil
	}
	return end - 128, nil
}

// id3v2TagSize returns the full size of the ID3v2 tag whose 10-byte header is
// h: the header, the synchsafe size (which covers any extended header and
// padding) and the 10-byte footer a v2.4 tag carries when flag 0x10 is set.
func id3v2TagSize(h []byte) int64 {
	size := 10 + int64(get7BitChunkedInt(h[6:10]))
	if h[3] == 4 && getBit(h[5], 4) {
		size += 10
	}
	return size
}

// skipID3v2 returns the offset after a leading ID3v2 tag at start, or start
// when there is none.
func skipID3v2(r io.ReadSeeker, start int64) (int64, error) {
	if _, err := r.Seek(start, io.SeekStart); err != nil {
		return 0, err
	}
	b, err := readBytes(r, 10)
	if err != nil || string(b[:3]) != "ID3" {
		return start, nil
	}
	return start + id3v2TagSize(b), nil
}

// audioFileType identifies the container from the first bytes of its audio
// (after any leading ID3v2 tag): the formats that carry APEv2 tags, and MP3.
func audioFileType(b []byte) FileType {
	switch {
	case len(b) >= 4 && string(b[:4]) == "MAC ":
		return APE
	case len(b) >= 3 && string(b[:3]) == "MP+", len(b) >= 4 && string(b[:4]) == "MPCK":
		return MPC
	case len(b) >= 2 && b[0] == 0xFF && b[1]&0xF6 == 0xF0: // ADTS: sync word, layer 0
		return AAC
	case len(b) >= 2 && b[0] == 0xFF && b[1]&0xE0 == 0xE0:
		return MP3
	}
	return UnknownFileType
}

// carriesAPEv2 reports whether files of type ft keep their tags in APEv2.
func carriesAPEv2(ft FileType) bool {
	return ft == APE || ft == MPC || ft == AAC
}

// fileTypeMetadata corrects the file type of a tag whose format does not tell
// it, such as an ID3v2 tag in front of AAC audio.
type fileTypeMetadata struct {
	Metadata
	fileType FileType
}

func (m fileTypeMetadata) FileType() FileType { return m.fileType }

// readAPEv2File reads an APE, MPC or AAC file without a leading ID3v2 tag:
// its APEv2 tag, or else its ID3v1 tag.
func readAPEv2File(r io.ReadSeeker) (Metadata, error) {
	start, err := r.Seek(0, io.SeekCurrent)
	if err != nil {
		return nil, fmt.Errorf("error reading current position: %v", err)
	}
	m, err := ReadAPEv2Tags(r)
	if err != ErrNoTagsFound {
		return m, err
	}
	b, err := readAt(r, start, 4)
	if err != nil {
		return nil, err
	}
	end, err := r.Seek(0, io.SeekEnd)
	if err != nil {
		return nil, err
	}
	if end-start < 128 {
		return nil, ErrNoTagsFound // too short for ID3v1
	}
	m, err = ReadID3v1Tags(r)
	if err == ErrNotID3v1 {
		return nil, ErrNoTagsFound
	}
	if err != nil {
		return nil, err
	}
	return fileTypeMetadata{m, audioFileType(b)}, nil
}

// readID3v2File reads a file that starts with an ID3v2 tag. For APE and MPC
// the APEv2 tag wins: TagLib skips a leading ID3v2 tag in those files and
// never updates it, so it is stale. For AAC, which TagLib writes ID3v2 to,
// and MP3 the ID3v2 tag is read.
func readID3v2File(r io.ReadSeeker) (Metadata, error) {
	start, err := r.Seek(0, io.SeekCurrent)
	if err != nil {
		return nil, fmt.Errorf("error reading current position: %v", err)
	}
	audioStart, err := skipID3v2(r, start)
	if err != nil {
		return nil, err
	}
	head, err := readAt(r, audioStart, 4)
	if err != nil {
		return nil, err
	}
	ft := audioFileType(head)
	if ft == APE || ft == MPC {
		if _, err := r.Seek(start, io.SeekStart); err != nil {
			return nil, err
		}
		m, err := ReadAPEv2Tags(r)
		if err != ErrNoTagsFound {
			return m, err
		}
	}

	if _, err := r.Seek(start, io.SeekStart); err != nil {
		return nil, err
	}
	m, err := ReadID3v2Tags(r)
	if err != nil || !carriesAPEv2(ft) {
		return m, err
	}
	return fileTypeMetadata{m, ft}, nil
}

// readAt reads up to n bytes at off; fewer at the end of the file.
func readAt(r io.ReadSeeker, off int64, n int) ([]byte, error) {
	if _, err := r.Seek(off, io.SeekStart); err != nil {
		return nil, err
	}
	b := make([]byte, n)
	k, err := io.ReadFull(r, b)
	if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
		return nil, err
	}
	return b[:k], nil
}

// ReadAPEv2Tags reads the APEv2 tag at the end of the file in the
// io.ReadSeeker, starting from its current position, which may hold an ID3v2
// tag. Returns ErrNoTagsFound if there is no APEv2 tag.
func ReadAPEv2Tags(r io.ReadSeeker) (Metadata, error) {
	start, err := r.Seek(0, io.SeekCurrent)
	if err != nil {
		return nil, fmt.Errorf("error reading current position: %v", err)
	}
	audioStart, err := skipID3v2(r, start)
	if err != nil {
		return nil, err
	}
	end, err := r.Seek(0, io.SeekEnd)
	if err != nil {
		return nil, fmt.Errorf("error seeking to end of file: %v", err)
	}
	if audioStart > end {
		return nil, fmt.Errorf("ID3v2 tag runs %d bytes past the end of the file", audioStart-end)
	}

	head, err := readAt(r, audioStart, 4)
	if err != nil {
		return nil, err
	}
	m := &metadataAPEv2{fileType: audioFileType(head), raw: map[string]interface{}{}, items: map[string]interface{}{}}

	t, err := findTrailingTags(r, audioStart, end)
	if err != nil {
		return nil, err
	}
	if !t.ape {
		return nil, ErrNoTagsFound
	}
	if _, err := r.Seek(t.apeItems, io.SeekStart); err != nil {
		return nil, fmt.Errorf("error seeking to APEv2 items: %v", err)
	}
	b, err := readBytes(r, uint(t.apeSize-apeBlockSize))
	if err != nil {
		return nil, fmt.Errorf("error reading APEv2 items: %v", err)
	}
	if err := m.readItems(b, t.apeCount); err != nil {
		return nil, err
	}
	return m, nil
}

var errAPEv2Item = errors.New("invalid APEv2 item")

// apev2Item is one item of an APEv2 tag as stored.
type apev2Item struct {
	key   string
	flags uint32 // bits 2-1: 0 UTF-8 text, 1 binary, 2 external link; bit 0: read-only
	value []byte
}

// parseAPEv2Items parses count items from b. A count larger than the items
// that fit, which some writers leave, stops at the end of b.
func parseAPEv2Items(b []byte, count uint32) ([]apev2Item, error) {
	var items []apev2Item
	for i := uint32(0); i < count && len(b) >= 8; i++ {
		size := uint64(binary.LittleEndian.Uint32(b[0:4]))
		flags := binary.LittleEndian.Uint32(b[4:8])
		b = b[8:]

		k := bytes.IndexByte(b, 0)
		if k < 1 {
			return nil, errAPEv2Item
		}
		key := string(b[:k])
		b = b[k+1:]
		if size > uint64(len(b)) {
			return nil, fmt.Errorf("%w: %q runs past the end of the tag", errAPEv2Item, key)
		}
		items = append(items, apev2Item{key: key, flags: flags, value: append([]byte(nil), b[:size]...)})
		b = b[size:]
	}
	return items, nil
}

// readItems parses count items from b into m.
func (m *metadataAPEv2) readItems(b []byte, count uint32) error {
	items, err := parseAPEv2Items(b, count)
	if err != nil {
		return err
	}
	for _, it := range items {
		var v interface{}
		if it.flags>>1&3 == 1 { // binary
			v = it.value
		} else { // UTF-8 text, or an external link
			v = string(it.value)
		}
		m.raw[it.key] = v
		m.items[strings.ToLower(it.key)] = v
	}
	return nil
}

// metadataAPEv2 is an APEv2 tag. Keys match case-insensitively, as the spec
// recommends; Raw keeps each key's own case.
type metadataAPEv2 struct {
	fileType FileType
	raw      map[string]interface{}
	items    map[string]interface{} // raw, keyed by the lower-cased key
}

// text returns the first value of the first of the text items named by keys
// (lower case) that has one. Multiple values are separated by NUL.
func (m *metadataAPEv2) text(keys ...string) string {
	for _, k := range keys {
		if s, ok := m.items[k].(string); ok {
			if i := strings.IndexByte(s, 0); i >= 0 {
				s = s[:i]
			}
			if s != "" {
				return s
			}
		}
	}
	return ""
}

func (m *metadataAPEv2) Format() Format              { return APEv2 }
func (m *metadataAPEv2) FileType() FileType          { return m.fileType }
func (m *metadataAPEv2) Raw() map[string]interface{} { return m.raw }
func (m *metadataAPEv2) Title() string               { return m.text("title") }
func (m *metadataAPEv2) Album() string               { return m.text("album") }
func (m *metadataAPEv2) Artist() string              { return m.text("artist") }
func (m *metadataAPEv2) AlbumArtist() string         { return m.text("album artist", "albumartist") }
func (m *metadataAPEv2) Composer() string            { return m.text("composer") }
func (m *metadataAPEv2) Genre() string               { return m.text("genre") }
func (m *metadataAPEv2) Lyrics() string              { return m.text("lyrics") }
func (m *metadataAPEv2) Comment() string             { return m.text("comment") }
func (m *metadataAPEv2) Year() int                   { return leadingYear(m.text("year", "date")) }
func (m *metadataAPEv2) Track() (int, int)           { return parseXofN(strings.TrimSpace(m.text("track"))) }
func (m *metadataAPEv2) Disc() (int, int)            { return parseXofN(strings.TrimSpace(m.text("disc"))) }

// leadingYear reads a year written alone or as the start of an ISO 8601 date.
func leadingYear(s string) int {
	s = strings.TrimSpace(s)
	if year, err := strconv.Atoi(s); err == nil {
		return year
	}
	if len(s) > 4 && s[4] == '-' {
		if year, err := strconv.Atoi(s[:4]); err == nil {
			return year
		}
	}
	return 0
}

// Picture returns the front cover, a binary item holding a file name, a NUL
// and the image.
func (m *metadataAPEv2) Picture() *Picture {
	b, ok := m.items["cover art (front)"].([]byte)
	if !ok {
		return nil
	}
	name, data, ok := bytes.Cut(b, []byte{0})
	if !ok {
		return nil
	}
	ext := strings.ToLower(strings.TrimPrefix(path.Ext(string(name)), "."))
	var mime string
	switch ext {
	case "jpg", "jpeg":
		mime = "image/jpeg"
	case "png":
		mime = "image/png"
	}
	return &Picture{Ext: ext, MIMEType: mime, Type: pictureTypes[0x03], Description: string(name), Data: data}
}
