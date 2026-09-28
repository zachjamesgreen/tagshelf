package tag

import (
	"crypto/sha1"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"unicode/utf16"
)

// ASF (WMA) support. Every ASF object is a 16-byte GUID, a little-endian
// 64-bit size that includes the 24-byte object header, and its data. The file
// is a Header Object, then the Data Object, then any Index objects. Tags live
// in the Header Object's children: the Content Description (title, author,
// ...), the Extended Content Description (WM/* and MusicBrainz/* attributes)
// and, inside the Header Extension, the Metadata and Metadata Library objects.
// See the ASF specification revision 01.20.02, sections 3 and 4.

// asfGUID returns the on-disk bytes of the GUID written as
// XXXXXXXX-XXXX-XXXX-XXXX-XXXXXXXXXXXX: the first three groups are
// little-endian, the last two are stored as written.
func asfGUID(s string) string {
	h := strings.ReplaceAll(s, "-", "")
	var b [16]byte
	for i := range b {
		n, err := strconv.ParseUint(h[2*i:2*i+2], 16, 8)
		if err != nil {
			panic("tag: bad ASF GUID " + s)
		}
		b[i] = byte(n)
	}
	b[0], b[1], b[2], b[3] = b[3], b[2], b[1], b[0]
	b[4], b[5] = b[5], b[4]
	b[6], b[7] = b[7], b[6]
	return string(b[:])
}

var (
	asfHeaderGUID                     = asfGUID("75B22630-668E-11CF-A6D9-00AA0062CE6C")
	asfDataGUID                       = asfGUID("75B22636-668E-11CF-A6D9-00AA0062CE6C")
	asfContentDescriptionGUID         = asfGUID("75B22633-668E-11CF-A6D9-00AA0062CE6C")
	asfExtendedContentDescriptionGUID = asfGUID("D2D0A440-E307-11D2-97F0-00A0C95EA850")
	asfHeaderExtensionGUID            = asfGUID("5FBF03B5-A92E-11CF-8EE3-00C00C205365")
	asfMetadataGUID                   = asfGUID("C5F8CBEA-5BAF-4877-8467-AA8C44FA4CCA")
	asfMetadataLibraryGUID            = asfGUID("44231C94-9498-49D1-A141-1D134E457054")
)

// isASF reports whether b starts with (a prefix of) the Header Object GUID.
func isASF(b []byte) bool {
	return len(b) > 0 && strings.HasPrefix(asfHeaderGUID, string(b))
}

// ASF attribute value types (spec 3.11 and 4.7).
const (
	asfUnicode = 0
	asfBytes   = 1
	asfBool    = 2
	asfDWORD   = 3
	asfQWORD   = 4
	asfWORD    = 5
	asfGUIDVal = 6
)

// asfContentDescriptionNames are the Raw names of the five Content
// Description fields, as TagLib and mutagen name them.
var asfContentDescriptionNames = [5]string{"Title", "Author", "Copyright", "Description", "Rating"}

type asfAttribute struct {
	name   string // as first written
	values []interface{}
}

type metadataASF struct {
	attrs map[string]*asfAttribute // by lower-cased name
	order []string                 // lower-cased names in the order first read
}

// ReadASFTags reads ASF (WMA) metadata from the io.ReadSeeker, returning the resulting metadata in
// a Metadata implementation, or non-nil error if there was a problem. Attribute names in Raw are
// as written (the Content Description fields are "Title", "Author", "Copyright", "Description"
// and "Rating"). Text, numbers, booleans ("true"/"false") and GUIDs read as strings, and an
// attribute written more than once has its values joined with NUL. Byte arrays read as []byte,
// the first one only.
func ReadASFTags(r io.ReadSeeker) (Metadata, error) {
	b, err := readBytes(r, 30)
	if err != nil {
		return nil, err
	}
	if string(b[0:16]) != asfHeaderGUID {
		return nil, errors.New("expected ASF Header Object")
	}
	size := binary.LittleEndian.Uint64(b[16:24])
	count := binary.LittleEndian.Uint32(b[24:28])
	// The header is read whole. Cover art can make it large, but not this
	// large: a bigger size is a corrupt file.
	if size < 30 || size > 1<<28 {
		return nil, fmt.Errorf("invalid ASF Header Object size %d", size)
	}
	body, err := readBytes(r, uint(size-30))
	if err != nil {
		return nil, fmt.Errorf("error reading ASF Header Object: %v", err)
	}

	m := &metadataASF{attrs: make(map[string]*asfAttribute)}
	// The Content Description comes first so its title and author win over
	// ECD attributes of the same name, which ffmpeg also writes.
	var ecd, ext [][]byte
	err = walkASFObjects(body, int(count), func(guid string, data []byte) error {
		switch guid {
		case asfContentDescriptionGUID:
			return m.readContentDescription(data)
		case asfExtendedContentDescriptionGUID:
			ecd = append(ecd, data)
		case asfHeaderExtensionGUID:
			ext = append(ext, data)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	for _, data := range ecd {
		if err := m.readExtendedContentDescription(data); err != nil {
			return nil, err
		}
	}
	for _, data := range ext {
		if err := m.readHeaderExtension(data); err != nil {
			return nil, err
		}
	}
	return m, nil
}

// walkASFObjects calls fn with the GUID and data of each object in b, at
// most max of them (max < 0 means no limit).
func walkASFObjects(b []byte, max int, fn func(guid string, data []byte) error) error {
	for n := 0; len(b) > 0 && (max < 0 || n < max); n++ {
		if len(b) < 24 {
			return errors.New("truncated ASF object header")
		}
		size := binary.LittleEndian.Uint64(b[16:24])
		if size < 24 || size > uint64(len(b)) {
			return fmt.Errorf("invalid ASF object size %d", size)
		}
		if err := fn(string(b[0:16]), b[24:size]); err != nil {
			return err
		}
		b = b[size:]
	}
	return nil
}

func (m *metadataASF) add(name string, v interface{}) {
	k := strings.ToLower(name)
	a, ok := m.attrs[k]
	if !ok {
		a = &asfAttribute{name: name}
		m.attrs[k] = a
		m.order = append(m.order, k)
	}
	for _, x := range a.values {
		if s, ok := x.(string); ok && s == v {
			return // the same text in two places, such as the title in CD and ECD
		}
	}
	a.values = append(a.values, v)
}

// asfReader reads little-endian fields from a byte slice, remembering the
// first time it runs out.
type asfReader struct {
	b   []byte
	err error
}

func (r *asfReader) bytes(n int) []byte {
	if r.err != nil {
		return nil
	}
	if n < 0 || n > len(r.b) {
		r.err = errors.New("truncated ASF object")
		return nil
	}
	b := r.b[:n]
	r.b = r.b[n:]
	return b
}

func (r *asfReader) uint16() int {
	if b := r.bytes(2); b != nil {
		return int(binary.LittleEndian.Uint16(b))
	}
	return 0
}

func (r *asfReader) uint32() int {
	if b := r.bytes(4); b != nil {
		return int(binary.LittleEndian.Uint32(b))
	}
	return 0
}

func (m *metadataASF) readContentDescription(b []byte) error {
	r := &asfReader{b: b}
	var lengths [5]int
	for i := range lengths {
		lengths[i] = r.uint16()
	}
	for i, n := range lengths {
		s := decodeASFString(r.bytes(n))
		if r.err != nil {
			return fmt.Errorf("ASF Content Description: %v", r.err)
		}
		if s != "" {
			m.add(asfContentDescriptionNames[i], s)
		}
	}
	return nil
}

func (m *metadataASF) readExtendedContentDescription(b []byte) error {
	r := &asfReader{b: b}
	count := r.uint16()
	for i := 0; i < count && r.err == nil; i++ {
		name := decodeASFString(r.bytes(r.uint16()))
		typ := r.uint16()
		value := r.bytes(r.uint16())
		if r.err == nil && name != "" {
			m.add(name, decodeASFValue(typ, value))
		}
	}
	if r.err != nil {
		return fmt.Errorf("ASF Extended Content Description: %v", r.err)
	}
	return nil
}

// readHeaderExtension reads the Metadata and Metadata Library objects nested
// in the Header Extension, after its reserved GUID and WORD and its data size.
func (m *metadataASF) readHeaderExtension(b []byte) error {
	r := &asfReader{b: b}
	r.bytes(18)
	data := r.bytes(r.uint32())
	if r.err != nil {
		return fmt.Errorf("ASF Header Extension: %v", r.err)
	}
	return walkASFObjects(data, -1, func(guid string, data []byte) error {
		if guid == asfMetadataGUID || guid == asfMetadataLibraryGUID {
			return m.readMetadataRecords(data)
		}
		return nil
	})
}

// readMetadataRecords reads a Metadata or Metadata Library object. Both hold
// records of: language index, stream number, name length, data type (all
// WORD), data length (DWORD), name, data. Stream and language are ignored.
func (m *metadataASF) readMetadataRecords(b []byte) error {
	r := &asfReader{b: b}
	count := r.uint16()
	for i := 0; i < count && r.err == nil; i++ {
		r.bytes(4) // language list index, stream number
		nameLen := r.uint16()
		typ := r.uint16()
		dataLen := r.uint32()
		name := decodeASFString(r.bytes(nameLen))
		value := r.bytes(dataLen)
		if r.err == nil && name != "" {
			m.add(name, decodeASFValue(typ, value))
		}
	}
	if r.err != nil {
		return fmt.Errorf("ASF metadata records: %v", r.err)
	}
	return nil
}

// decodeASFString decodes UTF-16LE text, dropping the NUL terminator and an
// odd trailing byte.
func decodeASFString(b []byte) string {
	s := make([]uint16, len(b)/2)
	for i := range s {
		s[i] = binary.LittleEndian.Uint16(b[2*i:])
	}
	for len(s) > 0 && s[len(s)-1] == 0 {
		s = s[:len(s)-1]
	}
	return string(utf16.Decode(s))
}

// decodeASFValue returns text, numbers, booleans and GUIDs as strings and
// anything else as bytes. A BOOL is 4 bytes in the Extended Content
// Description and 2 in the Metadata objects, so numbers are read by length.
func decodeASFValue(typ int, b []byte) interface{} {
	switch typ {
	case asfUnicode:
		return decodeASFString(b)
	case asfBool, asfDWORD, asfQWORD, asfWORD:
		var n uint64
		switch len(b) {
		case 2:
			n = uint64(binary.LittleEndian.Uint16(b))
		case 4:
			n = uint64(binary.LittleEndian.Uint32(b))
		case 8:
			n = binary.LittleEndian.Uint64(b)
		default:
			return append([]byte(nil), b...)
		}
		if typ == asfBool {
			return strconv.FormatBool(n != 0)
		}
		return strconv.FormatUint(n, 10)
	case asfGUIDVal:
		if len(b) == 16 {
			return fmt.Sprintf("%08X-%04X-%04X-%X-%X",
				binary.LittleEndian.Uint32(b[0:4]), binary.LittleEndian.Uint16(b[4:6]),
				binary.LittleEndian.Uint16(b[6:8]), b[8:10], b[10:16])
		}
	}
	return append([]byte(nil), b...)
}

// getString returns the first text value of the named attribute, matching
// the name case-insensitively.
func (m *metadataASF) getString(name string) string {
	if a, ok := m.attrs[strings.ToLower(name)]; ok {
		for _, v := range a.values {
			if s, ok := v.(string); ok {
				return strings.TrimSpace(s)
			}
		}
	}
	return ""
}

func (m *metadataASF) Format() Format     { return ASF }
func (m *metadataASF) FileType() FileType { return WMA }

func (m *metadataASF) Raw() map[string]interface{} {
	raw := make(map[string]interface{}, len(m.attrs))
	for _, k := range m.order {
		a := m.attrs[k]
		var texts []string
		for _, v := range a.values {
			switch v := v.(type) {
			case string:
				texts = append(texts, v)
			case []byte:
				if _, ok := raw[a.name]; !ok {
					raw[a.name] = v
				}
			}
		}
		if len(texts) > 0 {
			raw[a.name] = strings.Join(texts, "\x00")
		}
	}
	return raw
}

func (m *metadataASF) Title() string       { return m.getString("Title") }
func (m *metadataASF) Artist() string      { return m.getString("Author") }
func (m *metadataASF) Album() string       { return m.getString("WM/AlbumTitle") }
func (m *metadataASF) AlbumArtist() string { return m.getString("WM/AlbumArtist") }
func (m *metadataASF) Composer() string    { return m.getString("WM/Composer") }
func (m *metadataASF) Genre() string       { return m.getString("WM/Genre") }
func (m *metadataASF) Lyrics() string      { return m.getString("WM/Lyrics") }
func (m *metadataASF) Comment() string     { return m.getString("Description") }

// Year reads the leading year of WM/Year, which may be a full date.
func (m *metadataASF) Year() int {
	s := m.getString("WM/Year")
	if len(s) > 4 {
		s = s[:4]
	}
	y, _ := strconv.Atoi(s)
	return y
}

// Track reads WM/TrackNumber (text, possibly "N/M", or a DWORD), falling back
// to the zero-based WM/Track that old Windows Media Player versions wrote.
func (m *metadataASF) Track() (int, int) {
	if s := m.getString("WM/TrackNumber"); s != "" {
		return parseXofN(s)
	}
	if n, err := strconv.Atoi(m.getString("WM/Track")); err == nil {
		return n + 1, 0
	}
	return 0, 0
}

// Disc reads WM/PartOfSet, which Picard writes as "N/M".
func (m *metadataASF) Disc() (int, int) {
	return parseXofN(m.getString("WM/PartOfSet"))
}

// Picture returns the first WM/Picture: a picture type byte, a DWORD data
// length, a NUL-terminated UTF-16LE MIME type and description, then the data.
func (m *metadataASF) Picture() *Picture {
	a, ok := m.attrs[strings.ToLower("WM/Picture")]
	if !ok {
		return nil
	}
	for _, v := range a.values {
		b, ok := v.([]byte)
		if !ok || len(b) < 5 {
			continue
		}
		typ, n := b[0], binary.LittleEndian.Uint32(b[1:5])
		rest := b[5:]
		mime, rest, ok1 := cutASFString(rest)
		desc, rest, ok2 := cutASFString(rest)
		if !ok1 || !ok2 || uint64(n) > uint64(len(rest)) {
			continue
		}
		p := &Picture{MIMEType: mime, Description: desc, Data: rest[:n]}
		if t, ok := pictureTypes[typ]; ok {
			p.Type = t
		}
		switch mime {
		case "image/jpeg":
			p.Ext = "jpg"
		case "image/png":
			p.Ext = "png"
		case "image/gif":
			p.Ext = "gif"
		}
		return p
	}
	return nil
}

// cutASFString splits b after a NUL-terminated UTF-16LE string.
func cutASFString(b []byte) (s string, rest []byte, ok bool) {
	for i := 0; i+1 < len(b); i += 2 {
		if b[i] == 0 && b[i+1] == 0 {
			return decodeASFString(b[:i]), b[i+2:], true
		}
	}
	return "", nil, false
}

// SumASF constructs a checksum of the ASF (WMA) audio data provided by the io.ReadSeeker which is
// metadata invariant: the Data Object's packets, from 50 bytes into it (past its File ID and
// packet count) to its end. Index objects after it are left out, and so is everything in the
// Header Object, where the tags live.
func SumASF(r io.ReadSeeker) (string, error) {
	start, err := r.Seek(0, io.SeekCurrent)
	if err != nil {
		return "", fmt.Errorf("error reading current position: %v", err)
	}
	b, err := readBytes(r, 24)
	if err != nil {
		return "", fmt.Errorf("error reading ASF Header Object: %v", err)
	}
	if string(b[0:16]) != asfHeaderGUID {
		return "", errors.New("expected ASF Header Object")
	}
	headerSize := binary.LittleEndian.Uint64(b[16:24])
	if headerSize < 30 || headerSize > 1<<62 {
		return "", fmt.Errorf("invalid ASF Header Object size %d", headerSize)
	}
	_, err = r.Seek(start+int64(headerSize), io.SeekStart)
	if err != nil {
		return "", fmt.Errorf("error seeking to ASF Data Object: %v", err)
	}

	b, err = readBytes(r, 50)
	if err != nil {
		return "", fmt.Errorf("error reading ASF Data Object: %v", err)
	}
	if string(b[0:16]) != asfDataGUID {
		return "", errors.New("expected ASF Data Object after the Header Object")
	}
	h := sha1.New()
	dataSize := binary.LittleEndian.Uint64(b[16:24])
	if dataSize == 0 {
		// Size 0 is allowed while broadcasting: the packets run to the
		// end of the file.
		_, err = io.Copy(h, r)
	} else {
		if dataSize < 50 || dataSize > 1<<62 {
			return "", fmt.Errorf("invalid ASF Data Object size %d", dataSize)
		}
		_, err = io.CopyN(h, r, int64(dataSize-50))
	}
	if err != nil {
		return "", fmt.Errorf("error reading ASF data packets: %v", err)
	}
	return hashSum(h), nil
}
