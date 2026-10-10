package musicbrainz

import (
	"regexp"

	"tagshelf/internal/rules"
)

// VariousArtistsID is the MusicBrainz ID of the special artist "Various
// Artists", the album artist of compilations. It is the rules package's,
// which decides by it where a compilation goes; compare IDs with
// rules.HasVariousArtistsID, which ignores case.
const VariousArtistsID = rules.VariousArtistsMBID

// VariousArtists is the album artist Picard writes for a Various Artists
// release (its default va_name).
const VariousArtists = "Various Artists"

// Tags is the tag set a MusicBrainz match writes to a file, by Picard name.
// Every field is written: a zero field (empty string, 0, nil, false) has no
// MusicBrainz value and clears that tag. Tags outside this set are left
// alone.
type Tags struct {
	Title       string // title
	Artist      string // artist: the track's artist credit
	AlbumArtist string // albumartist: the release's artist credit
	Album       string // album
	// Date is YYYY-MM-DD when MusicBrainz knows the day, else YYYY; never
	// YYYY-MM, which tagshelf's ID3v2.4 reader cannot read.
	Date       string
	Track      int // tracknumber: position on the medium
	TrackTotal int // totaltracks: tracks on the medium
	Disc       int // discnumber: medium position
	DiscTotal  int // totaldiscs: media on the release
	// Compilation is set only when Various Artists is the whole album artist,
	// as Picard sets it; the album artist is then "Various Artists".
	Compilation bool

	RecordingID    string   // musicbrainz_recordingid
	ReleaseTrackID string   // musicbrainz_trackid (the release track)
	AlbumID        string   // musicbrainz_albumid (the release)
	AlbumArtistIDs []string // musicbrainz_albumartistid, multi-valued
	ArtistIDs      []string // musicbrainz_artistid, multi-valued
	ReleaseGroupID string   // musicbrainz_releasegroupid
}

// TagsFor maps track ref of a looked-up release to the tags written for it,
// as Picard maps a release: the track's own title and artist credit win over
// its recording's.
func TagsFor(rel *Release, ref TrackRef) Tags {
	medium := &rel.Media[ref.Medium]
	t := &medium.Tracks[ref.Index]

	tags := Tags{
		Title:          t.Title,
		Artist:         Credit(t.ArtistCredit),
		AlbumArtist:    Credit(rel.ArtistCredit),
		Album:          rel.Title,
		Date:           tagDate(rel.Date),
		Track:          t.Position,
		TrackTotal:     medium.TrackCount,
		Disc:           medium.Position,
		DiscTotal:      len(rel.Media),
		ReleaseTrackID: t.ID,
		AlbumID:        rel.ID,
		AlbumArtistIDs: artistIDs(rel.ArtistCredit),
		ArtistIDs:      artistIDs(t.ArtistCredit),
		ReleaseGroupID: rel.ReleaseGroup.ID,
	}
	if tags.TrackTotal == 0 {
		tags.TrackTotal = len(medium.Tracks)
	}
	if r := t.Recording; r != nil {
		tags.RecordingID = r.ID
		if tags.Title == "" {
			tags.Title = r.Title
		}
		if len(t.ArtistCredit) == 0 {
			tags.Artist = Credit(r.ArtistCredit)
			tags.ArtistIDs = artistIDs(r.ArtistCredit)
		}
	}
	// Picard compares the whole album artist ID value, so a release credited
	// to Various Artists and someone else is not a compilation.
	if len(tags.AlbumArtistIDs) == 1 && rules.HasVariousArtistsID(tags.AlbumArtistIDs) {
		tags.Compilation = true
		tags.AlbumArtist = VariousArtists
	}
	return tags
}

// artistIDs lists the artist IDs of a credit in order, nil when it has none.
func artistIDs(credits []ArtistCredit) []string {
	var ids []string
	for _, c := range credits {
		if c.Artist.ID != "" {
			ids = append(ids, c.Artist.ID)
		}
	}
	return ids
}

var (
	fullDate = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)
	yearPart = regexp.MustCompile(`^\d{4}`)
)

// tagDate turns a MusicBrainz date (YYYY, YYYY-MM or YYYY-MM-DD) into the date
// written: the full date when the day is known, otherwise the year alone.
func tagDate(d string) string {
	if fullDate.MatchString(d) {
		return d
	}
	return yearPart.FindString(d)
}
