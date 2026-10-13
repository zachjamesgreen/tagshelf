// Package playlist reads M3U playlists and writes them back with some of
// their entries changed and everything else as it was: the comment lines,
// the line endings, the byte order mark and the encoding.
package playlist

import (
	"bytes"
	"errors"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"unicode/utf8"
)

// MaxSize is the most bytes a file may have to be read as a playlist. A
// playlist of a hundred thousand long entries is well under it.
const MaxSize = 32 << 20

// ErrEncoding says a playlist in Windows-1252 was given an entry with a
// character that encoding does not have.
var ErrEncoding = errors.New("the playlist is in Windows-1252, which cannot hold the new name")

// Is reports whether path has the name of a playlist: .m3u or .m3u8.
func Is(path string) bool {
	ext := filepath.Ext(path)
	return strings.EqualFold(ext, ".m3u") || strings.EqualFold(ext, ".m3u8")
}

// List is a playlist as it was read.
type List struct {
	bom   bool
	latin bool // Windows-1252; otherwise UTF-8
	lines []line
}

// line is one line: the white space before its text, the text, the white
// space after it and the line ending.
type line struct {
	lead, text, trail, end string
}

// Entry is one entry of a playlist that names a file.
type Entry struct {
	// Line is its place in the list, for Set.
	Line int
	// Text is the entry as written: a path.
	Text string
}

var utf8BOM = []byte{0xEF, 0xBB, 0xBF}

// Parse reads the playlist data, which was in a file of the name name. A
// .m3u8 is UTF-8. A .m3u is UTF-8 when it is valid UTF-8, and otherwise
// Windows-1252.
func Parse(data []byte, name string) *List {
	l := &List{}
	if bytes.HasPrefix(data, utf8BOM) {
		l.bom = true
		data = data[len(utf8BOM):]
	}
	text := string(data)
	if !utf8.Valid(data) && !l.bom && !strings.EqualFold(filepath.Ext(name), ".m3u8") {
		l.latin = true
		text = fromLatin(data)
	}
	for _, raw := range strings.SplitAfter(text, "\n") {
		if raw == "" {
			continue
		}
		body := strings.TrimRight(raw, "\r\n")
		ln := line{end: raw[len(body):]}
		rest := strings.TrimLeft(body, " \t")
		ln.lead = body[:len(body)-len(rest)]
		ln.text = strings.TrimRight(rest, " \t")
		ln.trail = rest[len(ln.text):]
		l.lines = append(l.lines, ln)
	}
	return l
}

// Entries are the entries that name files, in order. Comment lines, empty
// lines and entries that are addresses on a network are not among them.
func (l *List) Entries() []Entry {
	var out []Entry
	for i, ln := range l.lines {
		if ln.text == "" || strings.HasPrefix(ln.text, "#") || IsURL(ln.text) {
			continue
		}
		out = append(out, Entry{Line: i, Text: ln.text})
	}
	return out
}

// NotFiles counts the entries that are not files: addresses on a network.
func (l *List) NotFiles() int {
	n := 0
	for _, ln := range l.lines {
		if ln.text != "" && !strings.HasPrefix(ln.text, "#") && IsURL(ln.text) {
			n++
		}
	}
	return n
}

// Set gives the entry on line i the text text.
func (l *List) Set(i int, text string) {
	l.lines[i].text = text
}

// Bytes is the playlist as it is written: as it was read, but for the
// entries Set changed. It is ErrEncoding when a playlist in Windows-1252
// has been given a character that encoding lacks.
func (l *List) Bytes() ([]byte, error) {
	var b strings.Builder
	for _, ln := range l.lines {
		b.WriteString(ln.lead)
		b.WriteString(ln.text)
		b.WriteString(ln.trail)
		b.WriteString(ln.end)
	}
	if l.latin {
		return toLatin(b.String())
	}
	out := []byte(b.String())
	if l.bom {
		out = append(append([]byte{}, utf8BOM...), out...)
	}
	return out, nil
}

// url matches the start of an address: a scheme of two letters or more, so
// that the drive of C:\Music is not one.
var url = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9+.-]+://`)

// IsURL reports whether an entry is an address on a network, not a file.
func IsURL(text string) bool {
	return url.MatchString(text)
}

// Style is how an entry writes its path.
type Style struct {
	// Absolute is set for a whole path, not one from the playlist's folder.
	Absolute bool
	// Rooted is set for a whole path that names no drive: \Music\a.mp3 on
	// Windows, which is on the drive of the playlist.
	Rooted bool
	// Backslash is set when the folders are parted by \ and not by /.
	Backslash bool
}

// drive matches the drive at the start of a Windows path.
var drive = regexp.MustCompile(`^[A-Za-z]:`)

// StyleOf says how the entry text writes its path.
func StyleOf(text string) Style {
	s := Style{Backslash: strings.Contains(text, `\`) && !strings.Contains(text, "/")}
	switch {
	case drive.MatchString(text), strings.HasPrefix(text, `\\`), runtime.GOOS == "windows" && strings.HasPrefix(text, "//"):
		s.Absolute = true
	case strings.HasPrefix(text, "/"), strings.HasPrefix(text, `\`):
		s.Absolute = true
		s.Rooted = runtime.GOOS == "windows"
	}
	return s
}

// Resolve finds the file the entry text of a playlist in the folder dir
// names, as a whole path of this system. It reports false for an entry that
// names no file here: a path with a drive, or of a share, read on a system
// that has neither.
func Resolve(dir, text string) (string, bool) {
	style := StyleOf(text)
	if runtime.GOOS != "windows" {
		if drive.MatchString(text) || strings.HasPrefix(text, `\\`) {
			return "", false
		}
		if style.Backslash {
			text = strings.ReplaceAll(text, `\`, "/")
		}
	}
	path := filepath.FromSlash(text)
	switch {
	case style.Rooted:
		path = filepath.VolumeName(dir) + path
	case !style.Absolute:
		path = filepath.Join(dir, path)
	}
	if !filepath.IsAbs(path) {
		return "", false // C:name, a path from wherever drive C happens to be
	}
	return filepath.Clean(path), true
}

// Format writes the path of the file at target for a playlist in the folder
// dir, in the style style: from the playlist's folder or whole, with \ or /.
// A file on another drive than the playlist is written whole whatever the
// style, there being no path to it from the playlist's folder.
func Format(dir, target string, style Style) string {
	path := target
	switch {
	case style.Rooted && strings.EqualFold(filepath.VolumeName(dir), filepath.VolumeName(target)):
		path = target[len(filepath.VolumeName(target)):]
	case !style.Absolute:
		if rel, err := filepath.Rel(dir, target); err == nil {
			path = rel
		}
	}
	if style.Backslash {
		return strings.ReplaceAll(path, "/", `\`)
	}
	return filepath.ToSlash(path)
}

// high are the characters of Windows-1252 at 0x80 to 0x9F, where it parts
// from Latin-1. The five bytes it leaves undefined stand for the control
// characters of the same number, so that every byte comes back as it was.
var high = [32]rune{
	0x20AC, 0x0081, 0x201A, 0x0192, 0x201E, 0x2026, 0x2020, 0x2021, 0x02C6, 0x2030, 0x0160, 0x2039, 0x0152, 0x008D, 0x017D, 0x008F,
	0x0090, 0x2018, 0x2019, 0x201C, 0x201D, 0x2022, 0x2013, 0x2014, 0x02DC, 0x2122, 0x0161, 0x203A, 0x0153, 0x009D, 0x017E, 0x0178,
}

func fromLatin(data []byte) string {
	var b strings.Builder
	for _, c := range data {
		switch {
		case c >= 0x80 && c <= 0x9F:
			b.WriteRune(high[c-0x80])
		default:
			b.WriteRune(rune(c))
		}
	}
	return b.String()
}

func toLatin(text string) ([]byte, error) {
	out := make([]byte, 0, len(text))
next:
	for _, r := range text {
		if r < 0x80 || (r >= 0xA0 && r <= 0xFF) {
			out = append(out, byte(r))
			continue
		}
		for i, h := range high {
			if h == r {
				out = append(out, byte(0x80+i))
				continue next
			}
		}
		return nil, ErrEncoding
	}
	return out, nil
}
