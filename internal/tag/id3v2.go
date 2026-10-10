// Copyright 2015, David Howden
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package tag

import (
	"bytes"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
)

// Genre definitions 0-79 follow the ID3 tag specification of 1999.
// More genres have been successively introduced in later Winamp versions.
// https://en.wikipedia.org/wiki/List_of_ID3v1_genres#Extension_by_Winamp
var id3v2Genres = [...]string{
	"Blues", "Classic Rock", "Country", "Dance", "Disco", "Funk", "Grunge",
	"Hip-Hop", "Jazz", "Metal", "New Age", "Oldies", "Other", "Pop", "R&B",
	"Rap", "Reggae", "Rock", "Techno", "Industrial", "Alternative", "Ska",
	"Death Metal", "Pranks", "Soundtrack", "Euro-Techno", "Ambient",
	"Trip-Hop", "Vocal", "Jazz+Funk", "Fusion", "Trance", "Classical",
	"Instrumental", "Acid", "House", "Game", "Sound Clip", "Gospel",
	"Noise", "AlternRock", "Bass", "Soul", "Punk", "Space", "Meditative",
	"Instrumental Pop", "Instrumental Rock", "Ethnic", "Gothic",
	"Darkwave", "Techno-Industrial", "Electronic", "Pop-Folk",
	"Eurodance", "Dream", "Southern Rock", "Comedy", "Cult", "Gangsta",
	"Top 40", "Christian Rap", "Pop/Funk", "Jungle", "Native American",
	"Cabaret", "New Wave", "Psychedelic", "Rave", "Showtunes", "Trailer",
	"Lo-Fi", "Tribal", "Acid Punk", "Acid Jazz", "Polka", "Retro",
	"Musical", "Rock & Roll", "Hard Rock", "Folk", "Folk-Rock",
	"National Folk", "Swing", "Fast Fusion", "Bebob", "Latin", "Revival",
	"Celtic", "Bluegrass", "Avantgarde", "Gothic Rock", "Progressive Rock",
	"Psychedelic Rock", "Symphonic Rock", "Slow Rock", "Big Band",
	"Chorus", "Easy Listening", "Acoustic", "Humour", "Speech", "Chanson",
	"Opera", "Chamber Music", "Sonata", "Symphony", "Booty Bass", "Primus",
	"Porn Groove", "Satire", "Slow Jam", "Club", "Tango", "Samba",
	"Folklore", "Ballad", "Power Ballad", "Rhythmic Soul", "Freestyle",
	"Duet", "Punk Rock", "Drum Solo", "A capella", "Euro-House", "Dance Hall",
	"Goa", "Drum & Bass", "Club-House", "Hardcore", "Terror", "Indie",
	"Britpop", "Negerpunk", "Polsk Punk", "Beat", "Christian Gangsta Rap",
	"Heavy Metal", "Black Metal", "Crossover", "Contemporary Christian",
	"Christian Rock ", "Merengue", "Salsa", "Thrash Metal", "Anime", "JPop",
	"Synthpop",
	"Christmas", "Art Rock", "Baroque", "Bhangra", "Big Beat", "Breakbeat",
	"Chillout", "Downtempo", "Dub", "EBM", "Eclectic", "Electro",
	"Electroclash", "Emo", "Experimental", "Garage", "Global", "IDM",
	"Illbient", "Industro-Goth", "Jam Band", "Krautrock", "Leftfield", "Lounge",
	"Math Rock", "New Romantic", "Nu-Breakz", "Post-Punk", "Post-Rock", "Psytrance",
	"Shoegaze", "Space Rock", "Trop Rock", "World Music", "Neoclassical", "Audiobook",
	"Audio Theatre", "Neue Deutsche Welle", "Podcast", "Indie Rock", "G-Funk", "Dubstep",
	"Garage Rock", "Psybient",
}

// id3v2Header is a type which represents an ID3v2 tag header.
type id3v2Header struct {
	Version           Format
	Unsynchronisation bool
	ExtendedHeader    bool
	Experimental      bool
	Size              uint
}

// readID3v2Header reads the 10-byte ID3v2 header from the given io.Reader.
// An extended header is part of the tag's body: see skipID3v2ExtendedHeader.
func readID3v2Header(r io.Reader) (h *id3v2Header, err error) {
	b, err := readBytes(r, 10)
	if err != nil {
		return nil, fmt.Errorf("expected to read 10 bytes (ID3v2Header): %v", err)
	}

	if string(b[0:3]) != "ID3" {
		return nil, fmt.Errorf("expected to read \"ID3\"")
	}

	b = b[3:]
	var vers Format
	switch uint(b[0]) {
	case 2:
		vers = ID3v2_2
	case 3:
		vers = ID3v2_3
	case 4:
		vers = ID3v2_4
	case 0, 1:
		fallthrough
	default:
		return nil, fmt.Errorf("ID3 version: %v, expected: 2, 3 or 4", uint(b[0]))
	}

	// NB: We ignore b[1] (the revision) as we don't currently rely on it.
	h = &id3v2Header{
		Version:           vers,
		Unsynchronisation: getBit(b[2], 7),
		ExtendedHeader:    getBit(b[2], 6),
		Experimental:      getBit(b[2], 5),
		Size:              uint(get7BitChunkedInt(b[3:7])),
	}
	return h, nil
}

// skipID3v2ExtendedHeader returns b, the body of a tag, without the extended
// header it starts with. Only 2.3 and 2.4 have one.
func skipID3v2ExtendedHeader(b []byte, vers Format) ([]byte, error) {
	if vers != ID3v2_3 && vers != ID3v2_4 {
		return b, nil
	}
	if len(b) < 4 {
		return nil, fmt.Errorf("expected to read 4 bytes (%v extended header len): %v", vers, io.ErrUnexpectedEOF)
	}
	// In 2.3 the size leaves out its own 4 bytes; in 2.4 it is a synchsafe
	// int that counts them.
	size := uint(getInt(b[:4])) + 4
	if vers == ID3v2_4 {
		size = uint(get7BitChunkedInt(b[:4]))
	}
	if size < 4 || size > uint(len(b)) {
		return nil, fmt.Errorf("expected to read %d bytes (%v skip extended header): %v", size, vers, io.ErrUnexpectedEOF)
	}
	return b[size:], nil
}

// id3v2FrameFlags is a type which represents the flags which can be set on an ID3v2 frame.
type id3v2FrameFlags struct {
	// Message (ID3 2.3.0 and 2.4.0)
	TagAlterPreservation  bool
	FileAlterPreservation bool
	ReadOnly              bool

	// Format (ID3 2.3.0 and 2.4.0)
	Compression   bool
	Encryption    bool
	GroupIdentity bool
	// ID3 2.4.0 only (see http://id3.org/id3v2.4.0-structure sec 4.1)
	Unsynchronisation   bool
	DataLengthIndicator bool
}

func readID3v23FrameFlags(b []byte) *id3v2FrameFlags {
	msg := b[0]
	fmt := b[1]

	return &id3v2FrameFlags{
		TagAlterPreservation:  getBit(msg, 7),
		FileAlterPreservation: getBit(msg, 6),
		ReadOnly:              getBit(msg, 5),
		Compression:           getBit(fmt, 7),
		Encryption:            getBit(fmt, 6),
		GroupIdentity:         getBit(fmt, 5),
	}
}

func readID3v24FrameFlags(b []byte) *id3v2FrameFlags {
	msg := b[0]
	fmt := b[1]

	return &id3v2FrameFlags{
		TagAlterPreservation:  getBit(msg, 6),
		FileAlterPreservation: getBit(msg, 5),
		ReadOnly:              getBit(msg, 4),
		GroupIdentity:         getBit(fmt, 6),
		Compression:           getBit(fmt, 3),
		Encryption:            getBit(fmt, 2),
		Unsynchronisation:     getBit(fmt, 1),
		DataLengthIndicator:   getBit(fmt, 0),
	}
}

// id3v24FrameSize returns the size of the 2.4 frame whose header starts b,
// the rest of the tag. Sizes are synchsafe (7 bits to a byte), but old iTunes
// versions wrote plain 32-bit ones. The two differ from 128 bytes up; then
// the plain reading is taken when a byte has its top bit set, or when only it
// is followed by another frame, padding or the end of the tag.
func id3v24FrameSize(b []byte) uint {
	synchsafe := uint(get7BitChunkedInt(b[4:8]))
	plain := uint(getInt(b[4:8]))
	if synchsafe == plain {
		return plain
	}
	if (b[4]|b[5]|b[6]|b[7])&0x80 != 0 {
		return plain
	}
	if !id3v2FrameFollows(b[10:], synchsafe) && id3v2FrameFollows(b[10:], plain) {
		return plain
	}
	return synchsafe
}

// id3v2FrameFollows reports whether b ends n bytes in, or holds a frame name
// there, or padding: zero bytes to its end.
func id3v2FrameFollows(b []byte, n uint) bool {
	if n >= uint(len(b)) {
		return n == uint(len(b))
	}
	b = b[n:]
	if b[0] == 0 {
		return len(bytes.TrimLeft(b, "\x00")) == 0
	}
	if len(b) < 4 {
		return false
	}
	for _, c := range b[:4] {
		if (c < 'A' || c > 'Z') && (c < '0' || c > '9') {
			return false
		}
	}
	return true
}

// readID3v2Frames reads the ID3v2 frames in b, the body of the tag h heads,
// with whole-tag unsynchronisation and any extended header already removed.
// Every size is checked against what is left of b. A frame that cannot be
// decoded is left out and the frames after it are still read: its bytes are
// known, so the reader stays in step.
func readID3v2Frames(b []byte, h *id3v2Header) (map[string]interface{}, error) {
	var left firstSkip
	result := make(map[string]interface{})
	seen := make(map[string]int) // frames stored so far, by name

	nameSize, headerSize := 4, 10
	if h.Version == ID3v2_2 {
		nameSize, headerSize = 3, 6
	}

	for len(b) >= headerSize {
		// A frame name never starts with a zero byte: this is padding.
		if b[0] == 0 {
			break
		}
		name := string(b[:nameSize])

		var size uint
		var flags *id3v2FrameFlags
		switch h.Version {
		case ID3v2_2:
			size = uint(getInt(b[3:6]))

		case ID3v2_3:
			size = uint(getInt(b[4:8]))
			flags = readID3v23FrameFlags(b[8:10])

		case ID3v2_4:
			size = id3v24FrameSize(b)
			flags = readID3v24FrameFlags(b[8:10])
		}
		b = b[headerSize:]

		// A frame that runs past the tag is corrupted padding (see
		// http://id3.org/Compliance%20Issues) or has a damaged size.
		if size > uint(len(b)) {
			break
		}
		data := b[:size]
		b = b[size:]

		// An empty frame holds nothing, but frames may follow it.
		if size == 0 {
			continue
		}

		if flags != nil {
			// Compressed and encrypted frames are not decoded.
			if flags.Compression || flags.Encryption {
				left.skip("%s frame left out: it is compressed or encrypted", name)
				continue
			}

			// In 2.4 the size is that of the stored bytes: after the
			// data length indicator, unsynchronisation is undone frame by
			// frame, when the tag header or the frame asks for it.
			if flags.DataLengthIndicator {
				if len(data) < 4 {
					left.skip("%s frame left out: it is too short for its data length indicator", name)
					continue
				}
				data = data[4:]
			}
			if h.Version == ID3v2_4 && (h.Unsynchronisation || flags.Unsynchronisation) {
				data = removeUnsynchronisation(data)
			}
		}

		var v interface{}
		var err error
		switch {
		case name == "TXXX" || name == "TXX":
			v, err = readTextWithDescrFrame(data, false, true) // no lang, but enc

		case name[0] == 'T':
			v, err = readTFrame(data)

		case name == "UFID" || name == "UFI":
			v, err = readUFID(data)

		case name == "WXXX" || name == "WXX":
			v, err = readTextWithDescrFrame(data, false, false) // no lang, no enc

		case name[0] == 'W':
			v, err = readWFrame(data)

		case name == "COMM" || name == "COM" || name == "USLT" || name == "ULT":
			v, err = readTextWithDescrFrame(data, true, true) // both lang and enc

		case name == "APIC":
			v, err = readAPICFrame(data)

		case name == "PIC":
			v, err = readPICFrame(data)

		default:
			v = data
		}
		if err != nil {
			left.skip("%s frame left out: %v", name, err)
			continue
		}

		// There can be multiple tag with the same name. Append a number to the
		// name if there is more than one.
		rawName := name
		if n := seen[name]; n > 0 {
			rawName = name + "_" + strconv.Itoa(n-1)
		}
		seen[name]++
		result[rawName] = v
	}
	return result, left.err
}

// removeUnsynchronisation returns b without the zero byte that
// unsynchronisation puts after every 0xFF.
func removeUnsynchronisation(b []byte) []byte {
	if !bytes.Contains(b, []byte{0xFF, 0x00}) {
		return b
	}
	out := make([]byte, 0, len(b))
	ff := false
	for _, x := range b {
		if ff && x == 0x00 {
			ff = false
			continue
		}
		out = append(out, x)
		ff = (x == 0xFF)
	}
	return out
}

// ReadID3v2Tags parses ID3v2.{2,3,4} tags from the io.ReadSeeker into a Metadata, returning
// non-nil error on failure.
func ReadID3v2Tags(r io.ReadSeeker) (Metadata, error) {
	h, err := readID3v2Header(r)
	if err != nil {
		return nil, err
	}

	// The tag is read whole, or as far as the file goes, and then taken
	// apart in memory, so no size in it can make the reader go further.
	b, err := readUpTo(r, h.Size)
	if err != nil {
		return nil, err
	}

	// 2.2 and 2.3 unsynchronise the tag as a whole; 2.4 does it per frame.
	if h.Unsynchronisation && h.Version != ID3v2_4 {
		b = removeUnsynchronisation(b)
	}
	if h.ExtendedHeader {
		b, err = skipID3v2ExtendedHeader(b, h.Version)
		if err != nil {
			return nil, err
		}
	}
	frames, left := readID3v2Frames(b, h)
	return metadataID3v2{header: h, frames: frames, left: left}, nil
}

var id3v2genreRe = regexp.MustCompile(`(.*[^(]|.* |^)\(([0-9]+)\) *(.*)$`)

//  id3v2genre parse a id3v2 genre tag and expand the numeric genres
func id3v2genre(genre string) string {
	c := true
	for c {
		orig := genre
		if match := id3v2genreRe.FindStringSubmatch(genre); len(match) > 0 {
			if genreID, err := strconv.Atoi(match[2]); err == nil {
				if genreID < len(id3v2Genres) {
					genre = id3v2Genres[genreID]
					if match[1] != "" {
						genre = strings.TrimSpace(match[1]) + " " + genre
					}
					if match[3] != "" {
						genre = genre + " " + match[3]
					}
				}
			}
		}
		c = (orig != genre)
	}
	return strings.Replace(genre, "((", "(", -1)
}
