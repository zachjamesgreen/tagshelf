// Copyright 2015, David Howden
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package tag

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"strings"
	"unicode/utf16"
)

// DefaultUTF16WithBOMByteOrder is the byte order used when the "UTF16 with BOM" encoding
// is specified without a corresponding BOM in the data.
var DefaultUTF16WithBOMByteOrder binary.ByteOrder = binary.LittleEndian

func readWFrame(b []byte) (string, error) {
	// Frame text is always encoded in ISO-8859-1
	b = append([]byte{0}, b...)
	return readTFrame(b)
}

func readTFrame(b []byte) (string, error) {
	if len(b) == 0 {
		return "", nil
	}

	txt, err := decodeText(b[0], b[1:])
	if err != nil {
		return "", err
	}
	// A NUL ends the text, and in 2.4 separates the values of a frame that
	// holds several: those are kept, NUL-separated, for Raw. A value in
	// UTF-16 may bring its own byte order mark.
	txt = strings.ReplaceAll(txt, "\x00"+string(rune(0xFEFF)), "\x00")
	return strings.Trim(txt, "\x00"), nil
}

const (
	encodingISO8859      byte = 0
	encodingUTF16WithBOM byte = 1
	encodingUTF16        byte = 2
	encodingUTF8         byte = 3
)

func decodeText(enc byte, b []byte) (string, error) {
	if len(b) == 0 {
		return "", nil
	}

	switch enc {
	case encodingISO8859: // ISO-8859-1
		return decodeISO8859(b), nil

	case encodingUTF16WithBOM: // UTF-16 with byte order marker
		if len(b) == 1 {
			return "", nil
		}
		return decodeUTF16WithBOM(b)

	case encodingUTF16: // UTF-16 without byte order (assuming BigEndian)
		if len(b) == 1 {
			return "", nil
		}
		return decodeUTF16(b, binary.BigEndian)

	case encodingUTF8: // UTF-8
		return string(b), nil

	default: // Fallback to ISO-8859-1
		return decodeISO8859(b), nil
	}
}

var singleZero = []byte{0}

// dataSplit cuts b at the terminator of the description it starts with. In
// UTF-16 that is a zero code unit, which starts at an even offset: two zero
// bytes elsewhere are the halves of two characters, as in "aĀ".
func dataSplit(b []byte, enc byte) [][]byte {
	if enc == encodingUTF16 || enc == encodingUTF16WithBOM {
		for i := 0; i+1 < len(b); i += 2 {
			if b[i] == 0 && b[i+1] == 0 {
				return [][]byte{b[:i], b[i+2:]}
			}
		}
		return [][]byte{b}
	}
	return bytes.SplitN(b, singleZero, 2)
}

// decodeISO8859 decodes text labelled ISO-8859-1 as Windows-1252, which is
// what files tagged on Windows hold under that label. The two differ only in
// 0x80-0x9F: control characters in ISO-8859-1, and curly quotes, dashes and
// the like in Windows-1252.
func decodeISO8859(b []byte) string {
	return decodeWindows1252(string(b))
}

func decodeUTF16WithBOM(b []byte) (string, error) {
	if len(b) < 2 {
		return "", errors.New("invalid encoding: expected at least 2 bytes for UTF-16 byte order mark")
	}

	var bo binary.ByteOrder
	switch {
	case b[0] == 0xFE && b[1] == 0xFF:
		bo = binary.BigEndian
		b = b[2:]

	case b[0] == 0xFF && b[1] == 0xFE:
		bo = binary.LittleEndian
		b = b[2:]

	default:
		bo = DefaultUTF16WithBOMByteOrder
	}
	return decodeUTF16(b, bo)
}

func decodeUTF16(b []byte, bo binary.ByteOrder) (string, error) {
	// An odd byte at the end, such as a one-byte terminator, is left out.
	b = b[:len(b)&^1]
	s := make([]uint16, 0, len(b)/2)
	for i := 0; i < len(b); i += 2 {
		s = append(s, bo.Uint16(b[i:i+2]))
	}
	return string(utf16.Decode(s)), nil
}

// Comm is a type used in COMM, UFID, TXXX, WXXX and USLT tag.
// It's a text with a description and a specified language
// For WXXX, TXXX and UFID, we don't set a Language
type Comm struct {
	Language    string
	Description string
	Text        string
}

// String returns a string representation of the underlying Comm instance.
func (t Comm) String() string {
	if t.Language != "" {
		return fmt.Sprintf("Text{Lang: '%v', Description: '%v', %v lines}",
			t.Language, t.Description, strings.Count(t.Text, "\n"))
	}
	return fmt.Sprintf("Text{Description: '%v', %v}", t.Description, t.Text)
}

// IDv2.{3,4}
// -- Header
// <Header for 'Unsynchronised lyrics/text transcription', ID: "USLT">
// <Header for 'Comment', ID: "COMM">
// -- readTextWithDescrFrame(data, true, true)
// Text encoding       $xx
// Language            $xx xx xx
// Content descriptor  <text string according to encoding> $00 (00)
// Lyrics/text         <full text string according to encoding>
// -- Header
// <Header for         'User defined text information frame', ID: "TXXX">
// <Header for         'User defined URL link frame', ID: "WXXX">
// -- readTextWithDescrFrame(data, false, <isDataEncoded>)
// Text encoding       $xx
// Description         <text string according to encoding> $00 (00)
// Value               <text string according to encoding>
func readTextWithDescrFrame(b []byte, hasLang bool, encoded bool) (*Comm, error) {
	if len(b) == 0 {
		return nil, errors.New("error decoding tag description text: invalid encoding")
	}
	enc := b[0]
	b = b[1:]

	c := &Comm{}
	if hasLang {
		if len(b) < 3 {
			return nil, errors.New("hasLang set but not enough data for language information")
		}
		c.Language = string(b[:3])
		b = b[3:]
	}

	descTextSplit := dataSplit(b, enc)
	if len(descTextSplit) == 0 {
		return nil, errors.New("error decoding tag description text: invalid encoding")
	}

	desc, err := decodeText(enc, descTextSplit[0])
	if err != nil {
		return nil, fmt.Errorf("error decoding tag description text: %v", err)
	}
	c.Description = desc

	if len(descTextSplit) == 1 {
		return c, nil
	}

	if !encoded {
		enc = byte(0)
	}
	text, err := decodeText(enc, descTextSplit[1])
	if err != nil {
		return nil, fmt.Errorf("error decoding tag text: %v", err)
	}
	c.Text = text

	return c, nil
}

// UFID is composed of a provider (frequently a URL and a binary identifier)
// The identifier can be a text (Musicbrainz use texts, but not necessary)
type UFID struct {
	Provider   string
	Identifier []byte
}

func (u UFID) String() string {
	return fmt.Sprintf("%v (%v)", u.Provider, string(u.Identifier))
}

func readUFID(b []byte) (*UFID, error) {
	result := bytes.SplitN(b, singleZero, 2)
	if len(result) != 2 {
		return nil, errors.New("expected to split UFID data into 2 pieces")
	}

	return &UFID{
		Provider:   string(result[0]),
		Identifier: result[1],
	}, nil
}

var pictureTypes = map[byte]string{
	0x00: "Other",
	0x01: "32x32 pixels 'file icon' (PNG only)",
	0x02: "Other file icon",
	0x03: "Cover (front)",
	0x04: "Cover (back)",
	0x05: "Leaflet page",
	0x06: "Media (e.g. lable side of CD)",
	0x07: "Lead artist/lead performer/soloist",
	0x08: "Artist/performer",
	0x09: "Conductor",
	0x0A: "Band/Orchestra",
	0x0B: "Composer",
	0x0C: "Lyricist/text writer",
	0x0D: "Recording Location",
	0x0E: "During recording",
	0x0F: "During performance",
	0x10: "Movie/video screen capture",
	0x11: "A bright coloured fish",
	0x12: "Illustration",
	0x13: "Band/artist logotype",
	0x14: "Publisher/Studio logotype",
}

// Picture is a type which represents an attached picture extracted from metadata.
type Picture struct {
	Ext         string // Extension of the picture file.
	MIMEType    string // MIMEType of the picture.
	Type        string // Type of the picture (see pictureTypes).
	Description string // Description.
	Data        []byte // Raw picture data.
}

// String returns a string representation of the underlying Picture instance.
func (p Picture) String() string {
	return fmt.Sprintf("Picture{Ext: %v, MIMEType: %v, Type: %v, Description: %v, Data.Size: %v}",
		p.Ext, p.MIMEType, p.Type, p.Description, len(p.Data))
}

// IDv2.2
// -- Header
// Attached picture   "PIC"
// Frame size         $xx xx xx
// -- readPICFrame
// Text encoding      $xx
// Image format       $xx xx xx
// Picture type       $xx
// Description        <textstring> $00 (00)
// Picture data       <binary data>
func readPICFrame(b []byte) (*Picture, error) {
	if len(b) < 5 {
		return nil, errors.New("invalid PIC frame")
	}

	enc := b[0]
	ext := string(b[1:4])
	picType := b[4]

	descDataSplit := dataSplit(b[5:], enc)
	if len(descDataSplit) != 2 {
		return nil, errors.New("error decoding PIC description text: invalid encoding")
	}
	desc, err := decodeText(enc, descDataSplit[0])
	if err != nil {
		return nil, fmt.Errorf("error decoding PIC description text: %v", err)
	}

	// Files write the format in upper case ("JPG", "PNG").
	var mimeType string
	switch strings.ToLower(ext) {
	case "jpeg", "jpg":
		mimeType = "image/jpeg"
	case "png":
		mimeType = "image/png"
	}

	return &Picture{
		Ext:         ext,
		MIMEType:    mimeType,
		Type:        pictureTypes[picType],
		Description: desc,
		Data:        descDataSplit[1],
	}, nil
}

// IDv2.{3,4}
// -- Header
// <Header for 'Attached picture', ID: "APIC">
// -- readAPICFrame
// Text encoding   $xx
// MIME type       <text string> $00
// Picture type    $xx
// Description     <text string according to encoding> $00 (00)
// Picture data    <binary data>
func readAPICFrame(b []byte) (*Picture, error) {
	if len(b) == 0 {
		return nil, errors.New("error decoding APIC: invalid encoding")
	}
	enc := b[0]
	mimeDataSplit := bytes.SplitN(b[1:], singleZero, 2)
	if len(mimeDataSplit) != 2 {
		return nil, errors.New("error decoding APIC: invalid encoding")
	}

	mimeType := string(mimeDataSplit[0])

	b = mimeDataSplit[1]
	if len(b) < 1 {
		return nil, fmt.Errorf("error decoding APIC mimetype")
	}
	picType := b[0]

	descDataSplit := dataSplit(b[1:], enc)
	if len(descDataSplit) != 2 {
		return nil, errors.New("error decoding APIC description text: invalid encoding")
	}
	desc, err := decodeText(enc, descDataSplit[0])
	if err != nil {
		return nil, fmt.Errorf("error decoding APIC description text: %v", err)
	}

	var ext string
	switch mimeType {
	case "image/jpeg":
		ext = "jpg"
	case "image/png":
		ext = "png"
	}

	return &Picture{
		Ext:         ext,
		MIMEType:    mimeType,
		Type:        pictureTypes[picType],
		Description: desc,
		Data:        descDataSplit[1],
	}, nil
}
