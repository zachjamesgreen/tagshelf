package musicbrainz

import (
	"path/filepath"
	"regexp"
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
// title tag is empty, the title (and any empty artist or track number) is
// parsed from the file name, and the original folder name fills an empty
// artist or album.
func QueryFor(f FileInfo) Query {
	q := Query{
		Title:      strings.TrimSpace(f.Title),
		Artist:     strings.TrimSpace(f.Artist),
		Album:      strings.TrimSpace(f.Album),
		Year:       f.Year,
		Track:      f.Track,
		TrackTotal: f.TrackTotal,
		Length:     f.Length,
	}
	if q.Title != "" {
		return q
	}

	track, artist, title := parseFileName(filepath.Base(f.Path))
	q.Title = title
	if q.Artist == "" {
		q.Artist = artist
	}
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
	return q
}

var (
	// "01 - Title", "01. Title", "01 Title", "1-01 Title" (disc-track).
	leadingTrack = regexp.MustCompile(`^(?:\d{1,2}[-.])?(\d{1,3})(?:\s*[-.)]\s*|\s+)(.+)$`)
	// A trailing "(1999)" or "[1999]" on a folder name.
	trailingYear = regexp.MustCompile(`\s*[(\[]\d{4}[)\]]$`)
)

// parseFileName splits a file name of the forms "01 - Artist - Title",
// "01 - Title", "Artist - Title" or "Title" into its parts. The extension is
// dropped, and underscores stand for spaces when the name has no spaces.
func parseFileName(name string) (track int, artist, title string) {
	name = strings.TrimSuffix(name, filepath.Ext(name))
	if !strings.Contains(name, " ") {
		name = strings.ReplaceAll(name, "_", " ")
	}
	name = strings.TrimSpace(name)

	if m := leadingTrack.FindStringSubmatch(name); m != nil {
		if n, err := strconv.Atoi(m[1]); err == nil {
			track = n
			name = strings.TrimSpace(m[2])
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
