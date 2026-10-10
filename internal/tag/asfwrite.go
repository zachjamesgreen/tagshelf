package tag

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"unicode/utf16"
)

// The ASF writer only ever changes the Header Object. Nothing outside it
// holds an absolute file offset: the Simple Index counts packets, and the
// Index, Media Object Index and Timecode Index objects count bytes from the
// first data packet (ASF specification 01.20.02, sections 3.7 and 6). So
// the header can grow or shrink and the Data Object and index objects be
// copied after it byte for byte.

var (
	asfFilePropertiesGUID   = asfGUID("8CABDCA1-A947-11CF-8EE4-00C00C205365")
	asfDigitalSignatureGUID = asfGUID("2211B3FC-BD23-11D2-B4B7-00A0C955FC6E")
	asfPaddingGUID          = asfGUID("1806D474-CADF-4509-A4BA-9AABCB96AAE8")
	asfSimpleIndexGUID      = asfGUID("33000890-E5B1-11CF-89F4-00A0C90349CB")
	asfIndexGUID            = asfGUID("D6E229D3-35DA-11D1-9034-00A0C90349BE")
)

// ErrASFRefused is returned by WriteASFAttribute for an ASF file it will not
// change: a signed file or a broadcast stream.
var ErrASFRefused = errors.New("cannot change the tags of this WMA file")

// asfMaxHeaderSize is the largest Header Object ReadASFTags reads.
const asfMaxHeaderSize = 1 << 28

// WriteASFAttribute copies the ASF (WMA) file in r to w with the text attribute name set to value
// in the Extended Content Description Object. Every attribute of that name (matched
// case-insensitively, as ReadASFTags does) is removed from it, the new one is appended, and the
// object is created when the file has none. Only the Header Object changes: a Padding Object
// with room takes up the change in size, or else the header grows or shrinks and everything
// after it is copied unchanged. The File Properties File Size is set to the new file's length.
// Attributes in the Header Extension's Metadata objects are left alone.
//
// Files with a Digital Signature Object, the Broadcast flag or a Data Object of unknown size are
// refused with an error wrapping ErrASFRefused. Nothing is written to w unless the whole file
// checks out; after a write error, w holds a partial file.
func WriteASFAttribute(w io.Writer, r io.ReadSeeker, name, value string) error {
	size, err := r.Seek(0, io.SeekEnd)
	if err != nil {
		return err
	}
	if _, err := r.Seek(0, io.SeekStart); err != nil {
		return err
	}
	b, err := readBytes(r, 30)
	if err != nil {
		return fmt.Errorf("error reading ASF Header Object: %v", err)
	}
	if string(b[0:16]) != asfHeaderGUID {
		return errors.New("expected ASF Header Object")
	}
	headerSize := binary.LittleEndian.Uint64(b[16:24])
	count := binary.LittleEndian.Uint32(b[24:28])
	if headerSize < 30 || headerSize > asfMaxHeaderSize || headerSize > uint64(size) {
		return fmt.Errorf("invalid ASF Header Object size %d", headerSize)
	}
	if b[29] != 2 {
		return fmt.Errorf("unexpected ASF Header Object reserved value %d", b[29])
	}
	reserved := [2]byte{b[28], b[29]}
	body, err := readBytes(r, uint(headerSize-30))
	if err != nil {
		return fmt.Errorf("error reading ASF Header Object: %v", err)
	}

	// The children, in order, as GUID and data.
	type object struct {
		guid string
		data []byte
	}
	var children []object
	used := 0
	err = walkASFObjects(body, int(count), func(guid string, data []byte) error {
		children = append(children, object{guid, data})
		used += 24 + len(data)
		return nil
	})
	if err != nil {
		return err
	}
	if len(children) != int(count) || used != len(body) {
		return fmt.Errorf("ASF Header Object count %d does not match its %d bytes of objects", count, len(body))
	}

	fileProps, padding := -1, -1
	var ecds []int
	for i, c := range children {
		switch c.guid {
		case asfDigitalSignatureGUID:
			return fmt.Errorf("%w: it is digitally signed, and changing its tags would break the signature", ErrASFRefused)
		case asfFilePropertiesGUID:
			if fileProps >= 0 {
				return errors.New("ASF Header Object has two File Properties Objects")
			}
			fileProps = i
		case asfExtendedContentDescriptionGUID:
			ecds = append(ecds, i)
		case asfPaddingGUID:
			if padding < 0 {
				padding = i
			}
		}
	}
	if fileProps < 0 {
		return errors.New("ASF Header Object has no File Properties Object")
	}
	props := children[fileProps].data
	if len(props) < 80 {
		return errors.New("ASF File Properties Object too short")
	}
	if binary.LittleEndian.Uint32(props[64:68])&1 != 0 {
		return fmt.Errorf("%w: it is marked as a broadcast stream", ErrASFRefused)
	}

	// The Data Object must follow, whole: a size of 0 means a broadcast
	// whose length is not known.
	b, err = readBytes(r, 24)
	if err != nil || string(b[0:16]) != asfDataGUID {
		return errors.New("expected ASF Data Object after the Header Object")
	}
	dataSize := binary.LittleEndian.Uint64(b[16:24])
	if dataSize == 0 {
		return fmt.Errorf("%w: its Data Object size is 0, as in a broadcast stream", ErrASFRefused)
	}
	if dataSize < 50 || dataSize > uint64(size)-headerSize {
		return fmt.Errorf("invalid ASF Data Object size %d in a file of %d bytes", dataSize, size)
	}

	descriptor, err := asfTextDescriptor(name, value)
	if err != nil {
		return err
	}
	if len(ecds) == 0 {
		children = append(children, object{guid: asfExtendedContentDescriptionGUID, data: []byte{0, 0}})
		ecds = append(ecds, len(children)-1)
	}
	for k, i := range ecds {
		var add []byte
		if k == 0 {
			add = descriptor
		}
		if children[i].data, err = replaceASFDescriptors(children[i].data, name, add); err != nil {
			return err
		}
	}

	newUsed := 0
	for _, c := range children {
		newUsed += 24 + len(c.data)
	}
	if padding >= 0 {
		if n := len(children[padding].data) - (newUsed - used); n >= 0 {
			children[padding].data = make([]byte, n)
			newUsed = used
		}
	}
	newHeaderSize := uint64(30 + newUsed)
	if newHeaderSize > asfMaxHeaderSize {
		return fmt.Errorf("ASF Header Object would be %d bytes", newHeaderSize)
	}

	// File Size is set to the real new length, not adjusted: TagLib and
	// mutagen leave it stale.
	props = slices.Clone(props)
	binary.LittleEndian.PutUint64(props[16:24], newHeaderSize+uint64(size)-headerSize)
	children[fileProps].data = props

	header := make([]byte, 0, newHeaderSize)
	header = append(header, asfHeaderGUID...)
	header = binary.LittleEndian.AppendUint64(header, newHeaderSize)
	header = binary.LittleEndian.AppendUint32(header, uint32(len(children)))
	header = append(header, reserved[:]...)
	for _, c := range children {
		header = append(header, c.guid...)
		header = binary.LittleEndian.AppendUint64(header, uint64(24+len(c.data)))
		header = append(header, c.data...)
	}

	if _, err := r.Seek(int64(headerSize), io.SeekStart); err != nil {
		return err
	}
	if _, err := w.Write(header); err != nil {
		return err
	}
	n, err := io.Copy(w, r)
	if err != nil {
		return err
	}
	if n != size-int64(headerSize) {
		return fmt.Errorf("copied %d bytes after the ASF Header Object, want %d", n, size-int64(headerSize))
	}
	return nil
}

// asfTextDescriptor renders an Extended Content Description descriptor
// holding text.
func asfTextDescriptor(name, value string) ([]byte, error) {
	n, v := encodeASFString(name), encodeASFString(value)
	if name == "" || len(n) > 0xffff || len(v) > 0xffff {
		return nil, fmt.Errorf("ASF attribute %.20q too long or unnamed", name)
	}
	b := binary.LittleEndian.AppendUint16(nil, uint16(len(n)))
	b = append(b, n...)
	b = binary.LittleEndian.AppendUint16(b, asfUnicode)
	b = binary.LittleEndian.AppendUint16(b, uint16(len(v)))
	return append(b, v...), nil
}

// encodeASFString encodes NUL-terminated UTF-16LE text.
func encodeASFString(s string) []byte {
	var b []byte
	for _, u := range utf16.Encode([]rune(s)) {
		b = binary.LittleEndian.AppendUint16(b, u)
	}
	return append(b, 0, 0)
}

// replaceASFDescriptors returns the data of an Extended Content Description
// Object without the descriptors called name (matched case-insensitively)
// and with add, if any, appended. Other descriptors are kept byte for byte,
// in order. Bytes after the last descriptor the count covers are dropped.
func replaceASFDescriptors(data []byte, name string, add []byte) ([]byte, error) {
	r := &asfReader{b: data}
	count := r.uint16()
	kept := 0
	out := []byte{0, 0}
	for i := 0; i < count && r.err == nil; i++ {
		start := len(data) - len(r.b)
		n := decodeASFString(r.bytes(r.uint16()))
		r.bytes(2)
		r.bytes(r.uint16())
		if r.err == nil && !strings.EqualFold(n, name) {
			out = append(out, data[start:len(data)-len(r.b)]...)
			kept++
		}
	}
	if r.err != nil {
		return nil, fmt.Errorf("ASF Extended Content Description: %v", r.err)
	}
	if add != nil {
		out = append(out, add...)
		kept++
	}
	if kept > 0xffff {
		return nil, errors.New("ASF Extended Content Description has too many descriptors")
	}
	binary.LittleEndian.PutUint16(out, uint16(kept))
	return out, nil
}
