package musicbrainz

import (
	"fmt"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Query is what a single-track lookup searches for: the editable fields the
// TUI shows before sending. Year is not sent (Picard does not send it either)
// but it takes part in ranking.
type Query struct {
	Title      string
	Artist     string
	Album      string
	Year       int
	Track      int
	TrackTotal int
	Length     time.Duration
	// ISRC is the recording's ISRC, as CleanISRC gives it. With one, it alone
	// is searched for, and the other fields only rank what it finds.
	ISRC string
}

// FileInfo is what tagshelf knows about a file before a lookup: its tags, its
// length from the index, its path and the folder it originally came from.
type FileInfo struct {
	Title      string
	Artist     string
	Album      string
	Year       int
	Track      int
	TrackTotal int
	// Disc is the disc number tag; only release match uses it.
	Disc   int
	Length time.Duration
	// Path is the file's current path; only its base name is used.
	Path string
	// Folder is the name of the folder the file originally came from, used as
	// a hint for artist and album. Empty means no hint.
	Folder string
}

// QueryFor builds the initial lookup query for a file. Tags win; when the
// title tag is empty, the title (and any empty artist) is parsed from the
// file name, and the original folder name fills an empty artist or album.
// The track number is the tag's alone: a number a file name starts with is a
// guess ("99 Luftballons"), and the lookup form does not show the track for
// it to be put right.
func QueryFor(f FileInfo) Query {
	q, trackTagged := fileQuery(f)
	if !trackTagged {
		q.Track = 0
	}
	return q
}

// fileQuery is QueryFor with the track number a file name starts with taken
// too, as a release match wants it. trackTagged reports that the track
// number, if there is one, is the tag's.
func fileQuery(f FileInfo) (q Query, trackTagged bool) {
	q = Query{
		Title:      strings.TrimSpace(f.Title),
		Artist:     strings.TrimSpace(f.Artist),
		Album:      strings.TrimSpace(f.Album),
		Year:       f.Year,
		Track:      f.Track,
		TrackTotal: f.TrackTotal,
		Length:     f.Length,
	}
	if q.Title != "" {
		return q, true
	}

	track, artist, title := parseFileName(filepath.Base(f.Path))
	q.Title = title
	if q.Artist == "" {
		q.Artist = artist
	}
	trackTagged = q.Track != 0
	if q.Track == 0 {
		q.Track = track
	}

	folderArtist, folderAlbum := parseFolder(f.Folder)
	if q.Artist == "" {
		q.Artist = folderArtist
	}
	if q.Album == "" {
		q.Album = folderAlbum
	}
	return q, trackTagged
}

var (
	// "01 - Title", "01. Title", "01 Title", "1-01 Title" (disc-track): the
	// disc, the number, what parts it from the rest, and the rest.
	leadingTrack = regexp.MustCompile(`^(\d{1,2}[-.])?(\d{1,3})(\s*[-.)]\s*|\s+)(.+)$`)
	// A trailing "(1999)" or "[1999]" on a folder name.
	trailingYear = regexp.MustCompile(`\s*[(\[]\d{4}[)\]]$`)
)

// parseFileName splits a file name of the forms "01 - Artist - Title",
// "01 - Title", "Artist - Title" or "Title" into its parts. The extension is
// dropped, and underscores stand for spaces when the name has no spaces.
//
// A number with only a space after it may as well start an artist's name
// ("50 Cent - In da Club"). It is a track number when it has a leading zero
// or a disc before it, or when no " - " follows, so that there is no artist
// it could belong to. "99 Luftballons" still reads as track 99: nothing in
// the name tells it from "12 Some Song".
func parseFileName(name string) (track int, artist, title string) {
	name = strings.TrimSuffix(name, filepath.Ext(name))
	if !strings.Contains(name, " ") {
		name = strings.ReplaceAll(name, "_", " ")
	}
	name = strings.TrimSpace(name)

	if m := leadingTrack.FindStringSubmatch(name); m != nil {
		disc, num, sep, rest := m[1], m[2], m[3], m[4]
		bare := strings.TrimSpace(sep) == ""
		if !bare || disc != "" || num[0] == '0' || !strings.Contains(rest, " - ") {
			if n, err := strconv.Atoi(num); err == nil {
				track = n
				name = strings.TrimSpace(rest)
			}
		}
	}

	if a, t, ok := strings.Cut(name, " - "); ok {
		a, t = strings.TrimSpace(a), strings.TrimSpace(t)
		if a != "" && t != "" {
			return track, a, t
		}
	}
	return track, "", name
}

// parseFolder reads an "Artist - Album" folder name as both; any other name
// is taken as the album. A trailing year in brackets is dropped.
func parseFolder(folder string) (artist, album string) {
	folder = strings.TrimSpace(trailingYear.ReplaceAllString(strings.TrimSpace(folder), ""))
	if folder == "" {
		return "", ""
	}
	if a, b, ok := strings.Cut(folder, " - "); ok {
		a, b = strings.TrimSpace(a), strings.TrimSpace(b)
		if a != "" && b != "" {
			return a, b
		}
	}
	return "", folder
}

// Lucene renders the query as Picard does: escaped field:(value) terms joined
// by spaces without AND, so every term scores rather than filters. The length
// is sent as qdur (ms / 2000) ORed with its neighbouring buckets, because a
// file's length rarely matches the recording's to the bucket.
func (q Query) Lucene() string {
	if q.ISRC != "" {
		// An ISRC names its recordings; nothing else narrows it down.
		return "isrc:" + q.ISRC
	}
	var terms []string
	add := func(field, value string) {
		if value = strings.TrimSpace(value); value != "" {
			terms = append(terms, field+":("+escapeLucene(value)+")")
		}
	}
	add("recording", q.Title)
	add("artist", q.Artist)
	add("release", q.Album)
	if q.Track > 0 {
		add("tnum", strconv.Itoa(q.Track))
	}
	if q.TrackTotal > 0 {
		add("tracks", strconv.Itoa(q.TrackTotal))
	}
	if q.Length > 0 {
		terms = append(terms, "qdur:("+strings.Join(qdurBuckets(q.Length), " OR ")+")")
	}
	return strings.Join(terms, " ")
}

// qdurBuckets returns the length's qdur bucket with its neighbours.
func qdurBuckets(length time.Duration) []string {
	b := int(length.Milliseconds() / 2000)
	var out []string
	for n := b - 1; n <= b+1; n++ {
		if n >= 0 {
			out = append(out, strconv.Itoa(n))
		}
	}
	return out
}

// isrcPattern matches an ISRC without its hyphens: a country, a registrant,
// a year and a number.
var isrcPattern = regexp.MustCompile(`^[A-Z]{2}[A-Z0-9]{3}[0-9]{7}$`)

// CleanISRC reads an ISRC as a person types it or a tag holds it, with or
// without hyphens and in either case, and returns it as MusicBrainz has it:
// twelve letters and digits. Nothing is no ISRC.
func CleanISRC(s string) (string, error) {
	isrc := strings.ToUpper(strings.NewReplacer("-", "", " ", "").Replace(strings.TrimSpace(s)))
	if isrc == "" {
		return "", nil
	}
	if !isrcPattern.MatchString(isrc) {
		return "", fmt.Errorf("an ISRC is two letters, three letters or digits and seven digits, as in GBAHT1600302, not %q", strings.TrimSpace(s))
	}
	return isrc, nil
}

// barcodePattern matches a barcode without its spaces: an EAN-8, a UPC of
// twelve digits, an EAN-13 or a GTIN of fourteen.
var barcodePattern = regexp.MustCompile(`^[0-9]{8,14}$`)

// CleanBarcode reads a barcode as a person types it or a tag holds it, with
// or without spaces and hyphens, and returns its digits. Nothing is no
// barcode.
func CleanBarcode(s string) (string, error) {
	code := strings.NewReplacer("-", "", " ", "").Replace(strings.TrimSpace(s))
	if code == "" {
		return "", nil
	}
	if !barcodePattern.MatchString(code) {
		return "", fmt.Errorf("a barcode is 8 to 14 digits, not %q", strings.TrimSpace(s))
	}
	return code, nil
}

// barcodeForms are the ways the barcode code may be written: as it is, and
// since a UPC of twelve digits is the EAN-13 that starts with a zero, as the
// other of the two as well.
func barcodeForms(code string) []string {
	switch {
	case len(code) == 12:
		return []string{code, "0" + code}
	case len(code) == 13 && code[0] == '0':
		return []string{code, code[1:]}
	}
	return []string{code}
}

// SameBarcode reports whether a and b are one barcode, however each is
// written. No barcode is not the same as any.
func SameBarcode(a, b string) bool {
	return a != "" && b != "" && slices.Contains(barcodeForms(a), b)
}

// luceneSpecial is Picard's set of characters escaped in query values.
const luceneSpecial = `+-&|!(){}[]^"~*?:\/`

// escapeLucene backslash-escapes Lucene's special characters and lower-cases
// the value. MusicBrainz search ignores case, and lower case keeps a title
// like "AND" or "NOT" from being read as an operator.
func escapeLucene(s string) string {
	var sb strings.Builder
	for _, r := range strings.ToLower(s) {
		if strings.ContainsRune(luceneSpecial, r) {
			sb.WriteByte('\\')
		}
		sb.WriteRune(r)
	}
	return sb.String()
}
