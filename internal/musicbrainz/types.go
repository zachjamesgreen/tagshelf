package musicbrainz

import (
	"encoding/json"
	"strconv"
	"strings"
)

// RecordingSearch is a page of /ws/2/recording search results.
type RecordingSearch struct {
	Count      int         `json:"count"`
	Offset     int         `json:"offset"`
	Recordings []Recording `json:"recordings"`
}

// Recording is a recording search hit. Each of its releases carries only the
// medium and track the recording appears on.
type Recording struct {
	ID           string         `json:"id"`
	Title        string         `json:"title"`
	Length       int            `json:"length"` // milliseconds, 0 when unknown
	Score        flexInt        `json:"score"`
	ArtistCredit []ArtistCredit `json:"artist-credit"`
	Releases     []Release      `json:"releases"`
}

// ArtistCredit is one entry of an ordered artist credit.
type ArtistCredit struct {
	Name       string `json:"name"`
	JoinPhrase string `json:"joinphrase"`
	Artist     Artist `json:"artist"`
}

// Artist is a MusicBrainz artist.
type Artist struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	SortName string `json:"sort-name"`
}

// Release is a release as it appears in search results and lookups.
type Release struct {
	ID           string         `json:"id"`
	Title        string         `json:"title"`
	Status       string         `json:"status"`
	Date         string         `json:"date"`
	Country      string         `json:"country"`
	TrackCount   int            `json:"track-count"` // whole release
	ArtistCredit []ArtistCredit `json:"artist-credit"`
	ReleaseGroup ReleaseGroup   `json:"release-group"`
	Media        []Medium       `json:"media"`
}

// ReleaseGroup is the release group a release belongs to.
type ReleaseGroup struct {
	ID             string   `json:"id"`
	Title          string   `json:"title"`
	PrimaryType    string   `json:"primary-type"`
	SecondaryTypes []string `json:"secondary-types"`
}

// Medium is one disc (or other medium) of a release. Search results list the
// matching track under "track"; lookups list every track under "tracks".
type Medium struct {
	Position    int     `json:"position"`
	Format      string  `json:"format"`
	TrackCount  int     `json:"track-count"`
	TrackOffset int     `json:"track-offset"`
	Track       []Track `json:"track"`
	Tracks      []Track `json:"tracks"`
}

// Track is a release track.
type Track struct {
	ID       string `json:"id"`
	Number   string `json:"number"`
	Position int    `json:"position"`
	Title    string `json:"title"`
	Length   int    `json:"length"` // milliseconds
}

// Credit joins an artist credit into its display string, as Picard does.
func Credit(credits []ArtistCredit) string {
	var sb strings.Builder
	for _, c := range credits {
		sb.WriteString(c.Name)
		sb.WriteString(c.JoinPhrase)
	}
	return sb.String()
}

// Year is the release's year, or 0 when it has no date.
func (r Release) Year() int {
	if len(r.Date) < 4 {
		return 0
	}
	y, err := strconv.Atoi(r.Date[:4])
	if err != nil {
		return 0
	}
	return y
}

// OfficialAlbum reports whether the release is an official release in an
// Album release group without secondary types (not a live album, compilation
// or soundtrack).
func (r Release) OfficialAlbum() bool {
	return r.Status == "Official" && r.ReleaseGroup.PrimaryType == "Album" && len(r.ReleaseGroup.SecondaryTypes) == 0
}

// flexInt decodes a JSON number or a numeric string: the search docs show
// scores as strings while the live service sends numbers.
type flexInt int

func (f *flexInt) UnmarshalJSON(b []byte) error {
	var num json.Number
	if err := json.Unmarshal(b, &num); err != nil {
		return err
	}
	if num == "" {
		*f = 0
		return nil
	}
	n, err := num.Float64()
	if err != nil {
		return err
	}
	*f = flexInt(n)
	return nil
}
