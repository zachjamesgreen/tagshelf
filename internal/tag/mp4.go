// Copyright 2015, David Howden
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package tag

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
)

// The classes a "data" atom declares for its value.
const (
	atomClassImplicit = 0 // automatic based on atom name
	atomClassText     = 1 // UTF-8
	atomClassUTF16    = 2 // UTF-16, big-endian
	atomClassGIF      = 12
	atomClassJPEG     = 13
	atomClassPNG      = 14
	atomClassInteger  = 21 // big-endian, of 1 to 8 bytes
	atomClassBMP      = 27
)

// NB: atoms does not include "----", this is handled separately
var atoms = atomNames(map[string]string{
	"\xa9alb": "album",
	"\xa9art": "artist",
	"\xa9ART": "artist",
	"aART":    "album_artist",
	"\xa9day": "year",
	"\xa9nam": "title",
	"\xa9gen": "genre",
	"trkn":    "track",
	"\xa9wrt": "composer",
	"\xa9too": "encoder",
	"cprt":    "copyright",
	"covr":    "picture",
	"\xa9grp": "grouping",
	"keyw":    "keyword",
	"\xa9lyr": "lyrics",
	"\xa9cmt": "comment",
	"tmpo":    "tempo",
	"cpil":    "compilation",
	"disk":    "disc",
})

var means = map[string]bool{
	"com.apple.iTunes":          true,
	"com.mixedinkey.mixedinkey": true,
	"com.serato.dj":             true,
}

// Detect PNG and JPEG images whatever class is declared
var (
	pngHeader  = []byte{137, 80, 78, 71, 13, 10, 26, 10}
	jpegHeader = []byte{0xFF, 0xD8, 0xFF}
)

type atomNames map[string]string

// Name returns the atoms that hold n, in a fixed order: when a file has two
// of them, such as "©ART" and "©art", the same one is read on every run.
func (f atomNames) Name(n string) []string {
	var res []string
	for k, v := range f {
		if v == n {
			res = append(res, k)
		}
	}
	sort.Strings(res)
	return res
}

// reservedAtomName reports whether name is a key the reader itself stores a
// value under, which a freeform ("----") atom of that name must not take.
func reservedAtomName(name string) bool {
	_, ok := atoms[name]
	return ok || name == "trkn_count" || name == "disk_count"
}

// metadataMP4 is the implementation of Metadata for MP4 tag (atom) data.
type metadataMP4 struct {
	fileType FileType
	data     map[string]interface{}

	// The numbers of the trkn and disk atoms, which Raw also has.
	track, trackCount int
	disc, discCount   int
}

// ReadAtoms reads MP4 metadata atoms from the io.ReadSeeker into a Metadata, returning
// non-nil error if there was a problem.
func ReadAtoms(r io.ReadSeeker) (Metadata, error) {
	m := metadataMP4{
		data:     make(map[string]interface{}),
		fileType: UnknownFileType,
	}
	err := m.readAtoms(r)
	return m, err
}

// readAtoms walks the atoms of the file from the reader's position. The
// atoms that lead to the tags (moov, udta, meta, ilst) are entered and their
// children read in the same loop, without recursion, so nesting costs no
// stack. Every size is checked against what is left of the file. A tag atom
// that cannot be decoded is left out and the atoms after it are still read.
func (m *metadataMP4) readAtoms(r io.ReadSeeker) error {
	pos, err := r.Seek(0, io.SeekCurrent)
	if err != nil {
		return err
	}
	end, err := r.Seek(0, io.SeekEnd)
	if err != nil {
		return err
	}
	if _, err := r.Seek(pos, io.SeekStart); err != nil {
		return err
	}

	for end-pos >= 8 {
		name, size, header, err := readAtomHeader(r, end-pos)
		if err != nil {
			return err
		}

		_, tagAtom := atoms[name]
		tagAtom = tagAtom || name == "----"
		if size < header || size > end-pos {
			// The atom is cut short or its size is damaged, and
			// nothing after it can be found.
			if tagAtom {
				return fmt.Errorf("invalid size %d of %q atom, with %d bytes left of the file", size, name, end-pos)
			}
			return nil
		}
		next := pos + size

		switch name {
		case "meta":
			// next_item_id (int32)
			if size-header < 4 {
				return nil
			}
			header += 4
			fallthrough

		case "moov", "udta", "ilst":
			pos += header
			if _, err := r.Seek(pos, io.SeekStart); err != nil {
				return err
			}
			continue

		case "ftyp":
			if b, err := readUpTo(r, 4); err == nil {
				m.fileType = brandFileType(string(b))
			}
		}

		if tagAtom {
			b, err := readBytes(r, uint(size-header))
			if err != nil {
				return err
			}
			if name == "----" {
				m.readCustomAtom(b)
			} else {
				m.readAtomData(name, b)
			}
		}

		pos = next
		if _, err := r.Seek(pos, io.SeekStart); err != nil {
			return err
		}
	}
	return nil
}

// brandFileType is the file type the major brand of the ftyp atom names.
func brandFileType(brand string) FileType {
	switch brand {
	case "M4A ":
		return M4A
	case "M4B ":
		return M4B
	case "M4P ":
		return M4P
	}
	return UnknownFileType
}

// readAtomHeader reads the header of an atom with left bytes of the file
// from its start. size is that of the whole atom and header that of its
// header: 16 bytes when the size field is 1 and the real size follows the
// name as 64 bits, or else 8. A size field of 0 means the atom runs to the
// end of the file. A size that cannot be right comes back as it is, smaller
// than header or larger than left.
func readAtomHeader(r io.Reader, left int64) (name string, size, header int64, err error) {
	b, err := readBytes(r, 8)
	if err != nil {
		return
	}
	name = string(b[4:8])
	size = int64(binary.BigEndian.Uint32(b[:4]))
	header = 8

	switch size {
	case 0:
		size = left

	case 1:
		header = 16
		if left < header {
			return
		}
		b, err = readBytes(r, 8)
		if err != nil {
			return
		}
		size = left + 1 // too large, unless it fits
		if large := binary.BigEndian.Uint64(b); large <= uint64(left) {
			size = int64(large)
		}
	}
	return
}

// mp4Value is the value of one "data" atom.
type mp4Value struct {
	class int
	data  []byte
}

// walkAtoms calls f for each atom in b, the body of an atom that holds
// others, until one does not fit.
func walkAtoms(b []byte, f func(name string, body []byte)) {
	for len(b) >= 8 {
		size := binary.BigEndian.Uint32(b[:4])
		if size < 8 || uint64(size) > uint64(len(b)) {
			return
		}
		f(string(b[4:8]), b[8:size])
		b = b[size:]
	}
}

// dataAtoms returns the values of the "data" atoms in b, the body of an
// item. An item with more than one value has one "data" atom for each.
func dataAtoms(b []byte) []mp4Value {
	var values []mp4Value
	walkAtoms(b, func(name string, body []byte) {
		// 4: atom version (1 byte) + atom flags (3 bytes), the class
		// 4: NULL (usually locale indicator)
		if name == "data" && len(body) >= 8 {
			values = append(values, mp4Value{class: getInt(body[1:4]), data: body[8:]})
		}
	})
	return values
}

// readAtomData stores the value of the item named name, whose body is b.
// Nothing is stored for an item that cannot be decoded.
func (m *metadataMP4) readAtomData(name string, b []byte) {
	values := dataAtoms(b)
	if len(values) == 0 {
		return
	}
	v := values[0]

	switch name {
	case "trkn", "disk":
		// Two bytes of padding, then the number and the total, 16
		// bits each.
		if len(v.data) < 4 {
			return
		}
		x := int(binary.BigEndian.Uint16(v.data[2:4]))
		n := 0
		if len(v.data) >= 6 {
			n = int(binary.BigEndian.Uint16(v.data[4:6]))
		}
		m.data[name] = x
		m.data[name+"_count"] = n
		if name == "trkn" {
			m.track, m.trackCount = x, n
		} else {
			m.disc, m.discCount = x, n
		}
		return

	case "covr":
		if p := mp4Picture(v); p != nil {
			m.data[name] = p
		}
		return
	}

	switch v.class {
	case atomClassText, atomClassUTF16:
		// Every value is kept, NUL-separated, for Raw.
		var texts []string
		for _, v := range values {
			switch v.class {
			case atomClassText:
				texts = append(texts, string(v.data))
			case atomClassUTF16:
				if s, err := decodeUTF16(v.data, binary.BigEndian); err == nil {
					texts = append(texts, s)
				}
			}
		}
		m.data[name] = strings.Join(texts, "\x00")

	case atomClassInteger:
		if len(v.data) < 1 || len(v.data) > 8 {
			return
		}
		m.data[name] = getInt(v.data)
	}
}

// mp4Picture is the picture in a covr value, or nil if it holds none. The
// data decides the format where it can, as the declared class is often
// "implicit" or wrong.
func mp4Picture(v mp4Value) *Picture {
	var ext string
	switch {
	case len(v.data) == 0:
		return nil
	case bytes.HasPrefix(v.data, pngHeader):
		ext = "png"
	case bytes.HasPrefix(v.data, jpegHeader):
		ext = "jpeg"
	case v.class == atomClassPNG:
		ext = "png"
	case v.class == atomClassJPEG:
		ext = "jpeg"
	case v.class == atomClassGIF:
		ext = "gif"
	case v.class == atomClassBMP:
		ext = "bmp"
	default:
		return nil
	}
	return &Picture{
		Ext:      ext,
		MIMEType: "image/" + ext,
		Data:     v.data,
	}
}

// Generic atom.
// Should have 3 sub atoms : mean, name and data.
// We check that mean is "com.apple.iTunes" or others and we use the subname as
// the name, and move to the data atom.
// Data atom could have multiple data values, each with a header.
// The value is stored under the subname, unless that is a key of the reader's
// own. If anything goes wrong, nothing is stored.
func (m *metadataMP4) readCustomAtom(b []byte) {
	subNames := make(map[string]string)
	var data []string

	walkAtoms(b, func(subName string, b []byte) {
		switch subName {
		case "mean", "name":
			if len(b) >= 4 {
				subNames[subName] = string(b[4:])
			}
		case "data":
			// Type indicator and locale, 4 bytes each, come before the
			// value. A data atom too short for them is skipped.
			if len(b) >= 8 {
				data = append(data, string(b[8:]))
			}
		}
	})

	name := subNames["name"]
	if !means[subNames["mean"]] || name == "" || len(data) == 0 || reservedAtomName(name) {
		return
	}
	m.data[name] = strings.Join(data, ";") // add delimiter if multiple data fields
}

func (metadataMP4) Format() Format       { return MP4 }
func (m metadataMP4) FileType() FileType { return m.fileType }

func (m metadataMP4) Raw() map[string]interface{} { return m.data }

// getString returns the first value of the first of the atoms n that holds
// text. An atom that declares another class holds no string.
func (m metadataMP4) getString(n []string) string {
	for _, k := range n {
		if s, ok := m.data[k].(string); ok {
			s, _, _ = strings.Cut(s, "\x00")
			return s
		}
	}
	return ""
}

func (m metadataMP4) Title() string {
	return m.getString(atoms.Name("title"))
}

func (m metadataMP4) Artist() string {
	return m.getString(atoms.Name("artist"))
}

func (m metadataMP4) Album() string {
	return m.getString(atoms.Name("album"))
}

func (m metadataMP4) AlbumArtist() string {
	return m.getString(atoms.Name("album_artist"))
}

func (m metadataMP4) Composer() string {
	return m.getString(atoms.Name("composer"))
}

func (m metadataMP4) Genre() string {
	return m.getString(atoms.Name("genre"))
}

func (m metadataMP4) Year() int {
	date := m.getString(atoms.Name("year"))
	if len(date) >= 4 {
		year, _ := strconv.Atoi(date[:4])
		return year
	}
	return 0
}

func (m metadataMP4) Track() (int, int) {
	return m.track, m.trackCount
}

func (m metadataMP4) Disc() (int, int) {
	return m.disc, m.discCount
}

func (m metadataMP4) Lyrics() string {
	return m.getString([]string{"\xa9lyr"})
}

func (m metadataMP4) Comment() string {
	return m.getString([]string{"\xa9cmt"})
}

func (m metadataMP4) Picture() *Picture {
	v, ok := m.data["covr"]
	if !ok {
		return nil
	}
	p, _ := v.(*Picture)
	return p
}
