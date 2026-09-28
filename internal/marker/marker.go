// Package marker reads and formats the manual marker: the TAGSHELF_MANUAL tag
// that records a file as manually tagged, with how (by hand or from a
// MusicBrainz lookup) and when, as "<method> <YYYY-MM-DD>".
//
// Each format keeps it under its own key: a TXXX:TAGSHELF_MANUAL frame in
// ID3v2, a TAGSHELF_MANUAL Vorbis comment in FLAC and OGG, a
// ----:com.apple.iTunes:TAGSHELF_MANUAL freeform atom in MP4, and an APEv2 or
// ASF item named TAGSHELF_MANUAL.
package marker

import (
	"maps"
	"slices"
	"strings"
	"time"

	"tagshelf/internal/tag"
)

// Key is the tag name the marker is stored under in every format.
const Key = "TAGSHELF_MANUAL"

// dateLayout is the marker's date, YYYY-MM-DD.
const dateLayout = time.DateOnly

// Method is how a person chose a file's tags.
type Method string

const (
	Hand        Method = "hand"        // typed by hand, or accepted as they were
	MusicBrainz Method = "musicbrainz" // picked from a MusicBrainz lookup
	Unknown     Method = "unknown"     // a marker whose method tagshelf does not recognise
)

// Marker is a parsed manual marker.
type Marker struct {
	Method Method
	Date   time.Time // the day it was written; zero when missing or unparseable
}

// New returns the marker for tags chosen by method on the day of when.
func New(method Method, when time.Time) Marker {
	y, m, d := when.Date()
	return Marker{Method: method, Date: time.Date(y, m, d, 0, 0, 0, 0, time.UTC)}
}

// String formats the marker as it is written into a file: "<method> <date>",
// or just the method when there is no date.
func (m Marker) String() string {
	if m.Date.IsZero() {
		return string(m.Method)
	}
	return string(m.Method) + " " + m.Date.Format(dateLayout)
}

// Parse reads a marker value. Any value with something besides spaces and
// NULs counts as a marker; a method other than hand or musicbrainz reads as
// Unknown, and a missing or malformed date as the zero time.
func Parse(value string) (Marker, bool) {
	fields := strings.Fields(strings.ReplaceAll(value, "\x00", " "))
	if len(fields) == 0 {
		return Marker{}, false
	}
	m := Marker{Method: Unknown}
	switch method := Method(strings.ToLower(fields[0])); method {
	case Hand, MusicBrainz:
		m.Method = method
	}
	if len(fields) > 1 {
		if date, err := time.Parse(dateLayout, fields[1]); err == nil {
			m.Date = date
		}
	}
	return m, true
}

// Read returns the marker in md, if it has one. Keys match
// case-insensitively: Vorbis keys read lower case, and TagLib writes TXXX
// descriptions upper case. When a file somehow holds several, the first
// non-empty one in sorted raw-key order wins, so the answer is stable.
func Read(md tag.Metadata) (Marker, bool) {
	if md == nil {
		return Marker{}, false
	}
	var values []string
	switch md.Format() {
	case tag.ID3v2_2, tag.ID3v2_3, tag.ID3v2_4:
		values = id3Values(md.Raw())
	default:
		// Vorbis comments, MP4 freeform atoms (keyed by name alone), and any
		// reader whose raw tags are plain key/value strings, such as APEv2
		// or ASF.
		values = keyValues(md.Raw())
	}
	for _, v := range values {
		if m, ok := Parse(v); ok {
			return m, true
		}
	}
	return Marker{}, false
}

// id3Values returns the text of every TXXX (TXX in ID3v2.2) frame described
// as the marker. The reader keys repeated frames TXXX, TXXX_0, TXXX_1, ….
func id3Values(raw map[string]any) []string {
	var values []string
	for _, k := range slices.Sorted(maps.Keys(raw)) {
		if !strings.HasPrefix(k, "TXX") {
			continue
		}
		var c tag.Comm
		switch v := raw[k].(type) {
		case *tag.Comm:
			if v == nil {
				continue
			}
			c = *v
		case tag.Comm:
			c = v
		default:
			continue
		}
		if strings.EqualFold(strings.TrimSpace(c.Description), Key) {
			values = append(values, c.Text)
		}
	}
	return values
}

// keyValues returns every string value stored under the marker's key.
func keyValues(raw map[string]any) []string {
	var values []string
	for _, k := range slices.Sorted(maps.Keys(raw)) {
		if s, ok := raw[k].(string); ok && strings.EqualFold(strings.TrimSpace(k), Key) {
			values = append(values, s)
		}
	}
	return values
}
