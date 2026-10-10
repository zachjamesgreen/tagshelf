package tag

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"
)

// riffChunk is a chunk of a RIFF file: its ID, and where its data starts.
// size is the data size the chunk header gives, without the pad byte that
// follows odd sizes.
type riffChunk struct {
	id   string
	data int64
	size int64
}

// walkRIFF calls f for each top-level chunk of the RIFF file in r, which
// starts at start and is end bytes long, until f returns false. A chunk that
// runs past end is passed to f and ends the walk.
func walkRIFF(r io.ReadSeeker, start, end int64, f func(riffChunk) (bool, error)) error {
	for off := start + 12; off+8 <= end; {
		if _, err := r.Seek(off, io.SeekStart); err != nil {
			return err
		}
		b, err := readBytes(r, 8)
		if err != nil {
			return fmt.Errorf("error reading RIFF chunk header: %v", err)
		}
		c := riffChunk{id: string(b[:4]), data: off + 8, size: int64(binary.LittleEndian.Uint32(b[4:8]))}
		more, err := f(c)
		if err != nil || !more {
			return err
		}
		off = c.data + c.size + c.size%2
	}
	return nil
}

// isWAV reports whether b, the first bytes of a file, start a RIFF WAVE file.
func isWAV(b []byte) bool {
	return len(b) >= 11 && string(b[0:4]) == "RIFF" && string(b[8:11]) == "WAV"
}

// ReadWAVTags reads the tags of the WAV file in the io.ReadSeeker, starting
// from its current position: the ID3v2 tag in an "ID3 " or "id3 " chunk,
// with each field it lacks taken from a LIST/INFO chunk. Returns
// ErrNoTagsFound if there is neither.
func ReadWAVTags(r io.ReadSeeker) (Metadata, error) {
	start, err := r.Seek(0, io.SeekCurrent)
	if err != nil {
		return nil, fmt.Errorf("error reading current position: %v", err)
	}
	end, err := r.Seek(0, io.SeekEnd)
	if err != nil {
		return nil, fmt.Errorf("error seeking to end of file: %v", err)
	}
	if _, err := r.Seek(start, io.SeekStart); err != nil {
		return nil, err
	}
	b, err := readBytes(r, 12)
	if err != nil {
		return nil, err
	}
	if string(b[0:4]) != "RIFF" || string(b[8:12]) != "WAVE" {
		return nil, fmt.Errorf("expected RIFF WAVE header")
	}

	m := metadataWAV{}
	var id3Err error
	err = walkRIFF(r, start, end, func(c riffChunk) (bool, error) {
		if c.data+c.size > end {
			return false, nil // truncated: nothing more to read
		}
		switch c.id {
		case "ID3 ", "id3 ":
			if m.id3 != nil {
				return true, nil
			}
			b, err := readBytes(r, uint(c.size))
			if err != nil {
				return false, fmt.Errorf("error reading %q chunk: %v", c.id, err)
			}
			// A tag that cannot be read does not hide the LIST/INFO
			// chunk: its error is only returned without one.
			id3, err := ReadID3v2Tags(bytes.NewReader(b))
			if err != nil {
				if id3Err == nil {
					id3Err = fmt.Errorf("error reading %q chunk: %v", c.id, err)
				}
				return true, nil
			}
			m.id3 = id3
		case "LIST":
			if c.size < 4 {
				return true, nil
			}
			b, err := readBytes(r, uint(c.size))
			if err != nil {
				return false, fmt.Errorf("error reading LIST chunk: %v", err)
			}
			if string(b[:4]) == "INFO" && m.info == nil {
				m.info = readINFO(b[4:])
			}
		}
		return true, nil
	})
	if err != nil {
		return nil, err
	}
	if m.id3 == nil && m.info == nil {
		if id3Err != nil {
			return nil, id3Err
		}
		return nil, ErrNoTagsFound
	}
	if m.id3 == nil {
		m.left = id3Err
	}
	return m, nil
}

// readINFO parses the sub-chunks of a LIST/INFO chunk, keyed by their IDs
// (INAM, IART, ...). Values are NUL-terminated; they are UTF-8 as TagLib
// writes them, or else Windows-1252 as Picard writes them.
func readINFO(b []byte) map[string]string {
	info := map[string]string{}
	for len(b) >= 8 {
		id := string(b[:4])
		size := int(binary.LittleEndian.Uint32(b[4:8]))
		b = b[8:]
		if size > len(b) {
			break
		}
		v := strings.TrimRight(string(b[:size]), "\x00")
		if !utf8.ValidString(v) {
			v = decodeWindows1252(v)
		}
		info[id] = strings.TrimSpace(v)
		b = b[min(size+size%2, len(b)):]
	}
	return info
}

// windows1252 maps the bytes 0x80-0x9F of Windows-1252 to Unicode; the other
// bytes are Latin-1. Undefined bytes map to U+FFFD.
var windows1252 = [32]rune{
	'€', '�', '‚', 'ƒ', '„', '…', '†', '‡', 'ˆ', '‰', 'Š', '‹', 'Œ', '�', 'Ž', '�',
	'�', '‘', '’', '“', '”', '•', '–', '—', '˜', '™', 'š', '›', 'œ', '�', 'ž', 'Ÿ',
}

func decodeWindows1252(s string) string {
	var sb strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 0x80 && c < 0xA0 {
			sb.WriteRune(windows1252[c-0x80])
		} else {
			sb.WriteRune(rune(c))
		}
	}
	return sb.String()
}

// metadataWAV is the ID3v2 tag of a WAV file, with the LIST/INFO values as a
// per-field fallback. INFO never carries MusicBrainz IDs.
type metadataWAV struct {
	id3  Metadata          // nil without an ID3 chunk
	info map[string]string // nil without a LIST/INFO chunk
	left error             // an ID3 chunk that could not be read, see Skipped
}

func (m metadataWAV) skipped() error {
	if m.left != nil || m.id3 == nil {
		return m.left
	}
	return Skipped(m.id3)
}

// text returns the ID3 value f reads, or else the first INFO value of ids.
func (m metadataWAV) text(f func(Metadata) string, ids ...string) string {
	if m.id3 != nil {
		if s := f(m.id3); s != "" {
			return s
		}
	}
	return m.infoText(ids...)
}

// infoText returns the first INFO value of ids.
func (m metadataWAV) infoText(ids ...string) string {
	for _, id := range ids {
		if s := m.info[id]; s != "" {
			return s
		}
	}
	return ""
}

func (m metadataWAV) Format() Format {
	if m.id3 != nil {
		return m.id3.Format()
	}
	return INFO
}

func (m metadataWAV) FileType() FileType  { return WAV }
func (m metadataWAV) Title() string       { return m.text(Metadata.Title, "INAM") }
func (m metadataWAV) Album() string       { return m.text(Metadata.Album, "IPRD") }
func (m metadataWAV) Artist() string      { return m.text(Metadata.Artist, "IART") }
func (m metadataWAV) AlbumArtist() string { return m.text(Metadata.AlbumArtist) }
func (m metadataWAV) Composer() string    { return m.text(Metadata.Composer) }
func (m metadataWAV) Genre() string       { return m.text(Metadata.Genre, "IGNR") }
func (m metadataWAV) Comment() string     { return m.text(Metadata.Comment, "ICMT") }
func (m metadataWAV) Lyrics() string      { return m.text(Metadata.Lyrics) }

func (m metadataWAV) Year() int {
	if m.id3 != nil {
		if y := m.id3.Year(); y != 0 {
			return y
		}
	}
	return leadingYear(m.info["ICRD"])
}

// Track reads ITRK, which Picard writes, or IPRT, which TagLib writes.
func (m metadataWAV) Track() (int, int) {
	if m.id3 != nil {
		if x, n := m.id3.Track(); x != 0 {
			return x, n
		}
	}
	return parseXofN(m.infoText("ITRK", "IPRT"))
}

func (m metadataWAV) Disc() (int, int) {
	if m.id3 != nil {
		return m.id3.Disc()
	}
	return 0, 0
}

func (m metadataWAV) Picture() *Picture {
	if m.id3 != nil {
		return m.id3.Picture()
	}
	return nil
}

// Raw returns the ID3 frames, plus the INFO values under their chunk IDs
// where no frame has the same name.
func (m metadataWAV) Raw() map[string]interface{} {
	raw := map[string]interface{}{}
	for k, v := range m.info {
		raw[k] = v
	}
	if m.id3 != nil {
		for k, v := range m.id3.Raw() {
			raw[k] = v
		}
	}
	return raw
}
