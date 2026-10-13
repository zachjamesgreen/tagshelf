package musicbrainz

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"math"
	"net/url"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// releaseInc is what a release lookup includes: everything the tags come
// from. With genres, the release, its release group and its recordings each
// list theirs, and with isrcs each recording lists its ISRCs.
const releaseInc = "artist-credits+recordings+release-groups+media+genres+isrcs"

// MaxBoostFiles is how many files of a release match get a recording search
// to boost the release candidates.
const MaxBoostFiles = 3

// MaxMatchFiles is the most files a release match takes. No release holds
// more, and a selection that large is a slip (a whole folder of loose files)
// that would have every file's tags read and weighed against every track.
const MaxMatchFiles = 500

// weightFound is the weight of the recording searches that found a release,
// in release ranking: the files' titles and lengths stand behind it, so it
// weighs as a title does in single-track ranking.
const weightFound = weightTitle

// weightBarcode is the weight of the barcode searched for being the
// release's, in release ranking: it says which release it is, as a title says
// which recording.
const weightBarcode = weightTitle

// ReleaseQuery is what a release match searches for: the editable fields the
// TUI shows before sending. Year is not sent but takes part in ranking.
type ReleaseQuery struct {
	Album  string
	Artist string
	// Tracks is the number of tracks on the whole release.
	Tracks int
	Year   int
	// Barcode is the release's barcode, as CleanBarcode gives it. With one,
	// it alone is searched for, and the other fields only rank what it finds.
	Barcode string
}

// ReleaseQueryFor builds the initial release query for a selection of files:
// the album and year most of them share, the artist when more than half of
// them share it (a Various Artists selection searches without one), and a
// track count of the tagged total or the selection size, whichever is larger.
// A selection spanning several discs uses its size, since tagged totals count
// one disc.
func ReleaseQueryFor(files []FileInfo) ReleaseQuery {
	var albums, artists []string
	var years, totals []int
	discs := map[int]bool{}
	for _, f := range files {
		q := QueryFor(f)
		albums = append(albums, q.Album)
		artists = append(artists, q.Artist)
		years = append(years, q.Year)
		totals = append(totals, q.TrackTotal)
		discs[max(f.Disc, 1)] = true
	}
	rq := ReleaseQuery{Tracks: len(files)}
	rq.Album, _ = mostCommon(albums, "")
	if artist, n := mostCommon(artists, ""); 2*n > len(files) {
		rq.Artist = artist
	}
	rq.Year, _ = mostCommon(years, 0)
	if total, _ := mostCommon(totals, 0); len(discs) == 1 && total > rq.Tracks {
		rq.Tracks = total
	}
	return rq
}

// mostCommon returns the value other than zero that occurs most often and how
// often; ties go to the first seen.
func mostCommon[T comparable](vals []T, zero T) (T, int) {
	counts := map[T]int{}
	best, bestN := zero, 0
	for _, v := range vals {
		if v == zero {
			continue
		}
		counts[v]++
		if counts[v] > bestN {
			best, bestN = v, counts[v]
		}
	}
	return best, bestN
}

// BoostQueries returns the recording queries for up to MaxBoostFiles files
// of a selection, given the release query rq the person is searching with.
// Each query says what its file is, so that a release it finds is evidence
// from the file:
//
//   - The title is the title tag, or else the title in the file name. A file
//     with neither is skipped: its query would only repeat rq.
//   - The album is rq's when it has one, or else the album tag. The folder's
//     name is not used: the person has seen its suggestion in the form.
//   - The artist is the one in the file name ("01 - Artist - Title", read as
//     QueryFor reads it, for a file without a title tag), since a track of
//     its own artist is not credited to the release's. Then rq's artist, which
//     beats the tag: a wrong tag is the reason to type one. Then the artist
//     tag, and last, for a file without a title tag, the folder's name.
func BoostQueries(files []FileInfo, rq ReleaseQuery) []Query {
	var out []Query
	for _, f := range files {
		if len(out) == MaxBoostFiles {
			break
		}
		if q, ok := boostQuery(f, rq); ok {
			out = append(out, q)
		}
	}
	return out
}

// boostQuery is the recording query for one file of a release match searched
// for with rq, as BoostQueries describes; false when the file has no title to
// search for.
func boostQuery(f FileInfo, rq ReleaseQuery) (Query, bool) {
	q := Query{
		Title:      strings.TrimSpace(f.Title),
		Artist:     strings.TrimSpace(f.Artist),
		Album:      strings.TrimSpace(f.Album),
		Year:       f.Year,
		Track:      f.Track,
		TrackTotal: f.TrackTotal,
		Length:     f.Length,
	}
	var nameArtist, folderArtist string
	if q.Title == "" {
		var track int
		track, nameArtist, q.Title = parseFileName(filepath.Base(f.Path))
		if q.Track == 0 {
			q.Track = track
		}
		folderArtist, _ = parseFolder(f.Folder)
	}
	if q.Title == "" {
		return Query{}, false
	}
	if album := strings.TrimSpace(rq.Album); album != "" {
		q.Album = album
	}
	switch artist := strings.TrimSpace(rq.Artist); {
	case nameArtist != "":
		q.Artist = nameArtist
	case artist != "":
		q.Artist = artist
	case q.Artist == "":
		q.Artist = folderArtist
	}
	return q, true
}

// Lucene renders the release query as escaped field:(value) terms without
// AND, as Query.Lucene does. On the release index "tracks" is the track count
// of the whole release.
func (q ReleaseQuery) Lucene() string {
	if q.Barcode != "" {
		// A barcode names its release; nothing else narrows it down.
		return "barcode:(" + strings.Join(barcodeForms(q.Barcode), " OR ") + ")"
	}
	var terms []string
	add := func(field, value string) {
		if value = strings.TrimSpace(value); value != "" {
			terms = append(terms, field+":("+escapeLucene(value)+")")
		}
	}
	add("release", q.Album)
	add("artist", q.Artist)
	if q.Tracks > 0 {
		add("tracks", strconv.Itoa(q.Tracks))
	}
	return strings.Join(terms, " ")
}

// SearchReleases runs a release search and returns one page of PageSize hits
// starting at offset.
func (c *Client) SearchReleases(ctx context.Context, q ReleaseQuery, offset int) (*ReleaseSearch, error) {
	u, err := releaseSearchURL(c.baseURL, q, offset)
	if err != nil {
		return nil, err
	}
	var res ReleaseSearch
	if err := c.get(ctx, u, &res); err != nil {
		return nil, fmt.Errorf("MusicBrainz release search: %w", err)
	}
	return &res, nil
}

// errNoReleaseQuery is the error for a release query with nothing to send.
var errNoReleaseQuery = errors.New("nothing to search for: fill in an album, artist, track count or barcode")

// releaseSearchURL is the URL of one page of a release search.
func releaseSearchURL(base string, q ReleaseQuery, offset int) (string, error) {
	lucene := q.Lucene()
	if lucene == "" {
		return "", errNoReleaseQuery
	}
	params := url.Values{
		"query": {lucene},
		"limit": {strconv.Itoa(PageSize)},
	}
	if offset > 0 {
		params.Set("offset", strconv.Itoa(offset))
	}
	return serviceURL(base, "release", params), nil
}

// mbidPattern matches a MusicBrainz ID.
var mbidPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// IsID reports whether s has the form of a MusicBrainz ID, which is also the
// form of an AcoustID ID.
func IsID(s string) bool { return mbidPattern.MatchString(s) }

// LookupRelease fetches a release with its media, tracks, recordings, artist
// credits and release group: everything its tags come from.
func (c *Client) LookupRelease(ctx context.Context, id string) (*Release, error) {
	u, err := releaseLookupURL(c.baseURL, id)
	if err != nil {
		return nil, err
	}
	var rel Release
	if err := c.get(ctx, u, &rel); err != nil {
		return nil, fmt.Errorf("MusicBrainz release lookup: %w", err)
	}
	return &rel, nil
}

// releaseLookupURL is the lookup URL of release id. The inc values are joined
// with a literal "+", as the API documents, so it is written out by hand.
func releaseLookupURL(base, id string) (string, error) {
	if !mbidPattern.MatchString(id) {
		return "", fmt.Errorf("not a MusicBrainz release ID: %q", id)
	}
	return base + "/release/" + strings.ToLower(id) + "?fmt=json&inc=" + releaseInc, nil
}

// ReleaseCandidate is one release offered for a release match, scored against
// the release query and the recording searches of the boost files.
type ReleaseCandidate struct {
	// Score is the match from 0 to 100.
	Score   int
	Release *Release
	// InLibrary reports that the release is already in the library.
	InLibrary bool
	// Found is how many of the boost files' recording searches found the
	// release, out of Searched.
	Found, Searched int
}

// Format describes the release's media, such as "CD", "2×CD" or
// "CD + DVD-Video"; empty when unknown.
func (r Release) Format() string {
	var parts []string
	var counts []int
	for _, m := range r.Media {
		f := m.Format
		if f == "" {
			f = "(unknown)"
		}
		if n := len(parts); n > 0 && parts[n-1] == f {
			counts[n-1]++
			continue
		}
		parts = append(parts, f)
		counts = append(counts, 1)
	}
	for i := range parts {
		if counts[i] > 1 {
			parts[i] = strconv.Itoa(counts[i]) + "×" + parts[i]
		}
	}
	return strings.Join(parts, " + ")
}

// SearchReleaseCandidates runs the searches of a release match: a release
// search for q and a recording search for each of boost (see BoostQueries).
// Releases the recording searches found are candidates too. The candidates
// are ranked as RankReleases does. Searches that find nothing give no
// candidates and no error; with nothing to search for it is an error.
func (c *Client) SearchReleaseCandidates(ctx context.Context, q ReleaseQuery, boost []Query, inLibrary func(releaseID string) bool) ([]ReleaseCandidate, error) {
	if q.Lucene() == "" && len(boost) == 0 {
		return nil, errNoReleaseQuery
	}
	var releases []Release
	if q.Lucene() != "" {
		res, err := c.SearchReleases(ctx, q, 0)
		if err != nil {
			return nil, err
		}
		releases = res.Releases
	}
	hits := make([][]Recording, 0, len(boost))
	for _, bq := range boost {
		res, err := c.SearchRecordings(ctx, bq, 0)
		if err != nil {
			return nil, err
		}
		hits = append(hits, res.Recordings)
	}
	return RankReleases(q, releases, hits, inLibrary), nil
}

// RankReleases scores releases from a release search, together with the
// releases on the recordings each boost file's search found (hits, one list per
// file), against q and sorts them best first. Album, artist, track count and
// year score with Picard's weights; the share of boost files whose search
// found the release scores with the weight of a title. A release found only
// by the recording searches is a candidate too. Ties on the displayed score go
// to releases more boost files found, then official Albums, then releases
// already in the library (inLibrary may be nil), then the service's order.
// RankReleases does not modify its arguments.
func RankReleases(q ReleaseQuery, releases []Release, hits [][]Recording, inLibrary func(releaseID string) bool) []ReleaseCandidate {
	var out []ReleaseCandidate
	index := map[string]int{}
	addRelease := func(r *Release) int {
		if i, ok := index[r.ID]; ok {
			return i
		}
		index[r.ID] = len(out)
		out = append(out, ReleaseCandidate{Release: r, Searched: len(hits)})
		return len(out) - 1
	}
	for i := range releases {
		addRelease(&releases[i])
	}
	for _, recs := range hits {
		found := map[string]bool{}
		for i := range recs {
			for j := range recs[i].Releases {
				r := &recs[i].Releases[j]
				if r.ID == "" || found[r.ID] {
					continue
				}
				found[r.ID] = true
				k := addRelease(r) // may grow out, so index after the call
				out[k].Found++
			}
		}
	}

	for i := range out {
		c := &out[i]
		r := c.Release
		c.InLibrary = inLibrary != nil && r.ID != "" && inLibrary(r.ID)
		var total, weights float64
		part := func(score float64, weight int) {
			total += score * float64(weight)
			weights += float64(weight)
		}
		if q.Album != "" {
			part(similarity(q.Album, r.Title), weightAlbum)
		}
		if q.Artist != "" && len(r.ArtistCredit) > 0 {
			part(similarity(q.Artist, Credit(r.ArtistCredit)), weightArtist)
		}
		if q.Tracks > 0 && r.TrackCount > 0 {
			part(boolScore(q.Tracks == r.TrackCount), weightTrackTotal)
		}
		if q.Year > 0 && r.Year() > 0 {
			part(boolScore(q.Year == r.Year()), weightDate)
		}
		if q.Barcode != "" {
			part(boolScore(SameBarcode(q.Barcode, r.Barcode)), weightBarcode)
		}
		if c.Searched > 0 {
			part(float64(c.Found)/float64(c.Searched), weightFound)
		}
		if weights > 0 {
			c.Score = int(math.Round(100 * total / weights))
		}
	}

	slices.SortStableFunc(out, func(a, b ReleaseCandidate) int {
		if c := cmp.Compare(b.Score, a.Score); c != 0 {
			return c
		}
		if c := cmp.Compare(b.Found, a.Found); c != 0 {
			return c
		}
		if c := compareBool(b.Release.OfficialAlbum(), a.Release.OfficialAlbum()); c != 0 {
			return c
		}
		return compareBool(b.InLibrary, a.InLibrary)
	})
	return out
}
