package musicbrainz

import (
	"reflect"
	"strings"
	"testing"

	"github.com/zachjamesgreen/tagshelf/internal/rules"
)

const (
	duaLipaID = "6f1a58bf-9b1b-49cf-a44a-6cefad7ae04f"
	dlGroupID = "4a0f4b1c-2d3e-4f5a-8b6c-7d8e9f0a1b2c"
)

// TestTagsForFixtureReleases checks the mapping against Picard's tag table
// (https://picard-docs.musicbrainz.org/en/appendices/tag_mapping.html) for the hand-written lookups in testdata.
func TestTagsForFixtureReleases(t *testing.T) {
	deluxe := lookupFixture(t, "release-dua-lipa-deluxe.json")
	various := lookupFixture(t, "release-various-artists.json")

	tests := []struct {
		desc string
		rel  *Release
		ref  TrackRef
		want Tags
	}{
		{
			"featured artist on disc 1",
			deluxe, TrackRef{Medium: 0, Index: 1},
			Tags{
				Title: "Lost in Your Light", Artist: "Dua Lipa feat. Miguel",
				AlbumArtist: "Dua Lipa", Album: "Dua Lipa", Date: "2017-06-02",
				Track: 2, TrackTotal: 12, Disc: 1, DiscTotal: 2,
				RecordingID:    "9c0d0002-1b2c-4d3e-8f4a-5b6c7d8e9f02",
				ReleaseTrackID: "7a0b0002-1b2c-4d3e-8f4a-5b6c7d8e9f02",
				AlbumID:        deluxeID,
				AlbumArtistIDs: []string{duaLipaID},
				ArtistIDs:      []string{duaLipaID, "5b7a6c3e-7f4b-4f0c-9a0c-2d8e4b7f1a93"},
				ReleaseGroupID: dlGroupID,
			},
		},
		{
			"disc 2 counts its own tracks",
			deluxe, TrackRef{Medium: 1, Index: 2},
			Tags{
				Title: "New Love", Artist: "Dua Lipa",
				AlbumArtist: "Dua Lipa", Album: "Dua Lipa", Date: "2017-06-02",
				Track: 3, TrackTotal: 5, Disc: 2, DiscTotal: 2,
				RecordingID:    "9c0d000f-1b2c-4d3e-8f4a-5b6c7d8e9f0f",
				ReleaseTrackID: "7a0b000f-1b2c-4d3e-8f4a-5b6c7d8e9f0f",
				AlbumID:        deluxeID,
				AlbumArtistIDs: []string{duaLipaID},
				ArtistIDs:      []string{duaLipaID},
				ReleaseGroupID: dlGroupID,
			},
		},
		{
			// The track title wins over the recording's "Despacito (Remix)",
			// and the month-only date is cut to the year.
			"various artists compilation",
			various, TrackRef{Medium: 0, Index: 2},
			Tags{
				Title: "Despacito (remix)", Artist: "Luis Fonsi & Daddy Yankee feat. Justin Bieber",
				AlbumArtist: "Various Artists", Album: "Hits of the Summer 2017", Date: "2017",
				Track: 3, TrackTotal: 6, Disc: 1, DiscTotal: 1, Compilation: true,
				RecordingID:    "a1b2c3d4-0002-4e5f-8a9b-0c1d2e3f4a5b",
				ReleaseTrackID: "b2c3d4e5-0003-4f6a-9b0c-1d2e3f4a5b6c",
				AlbumID:        variousID,
				AlbumArtistIDs: []string{VariousArtistsID},
				ArtistIDs: []string{
					"d68a4b0e-3c2a-4c8f-9b1e-6f7a2d3c4b5e",
					"2c3d4e5f-6a7b-4c8d-9e0f-1a2b3c4d5e6f",
					"e0140a67-e4d1-4f13-8a01-364355bee46e",
				},
				ReleaseGroupID: "6d7e8f9a-0b1c-4d2e-9f3a-4b5c6d7e8f9a",
			},
		},
	}
	for _, tt := range tests {
		if got := TagsFor(tt.rel, tt.ref); !reflect.DeepEqual(got, tt.want) {
			t.Errorf("%s:\n got %+v\nwant %+v", tt.desc, got, tt.want)
		}
	}

	// Every track of both releases gets a date that is never YYYY-MM.
	for _, rel := range []*Release{deluxe, various} {
		for _, ref := range rel.Tracks() {
			if d := TagsFor(rel, ref).Date; len(d) != 4 && len(d) != 10 {
				t.Errorf("%s %v: date %q", rel.Title, ref, d)
			}
		}
	}
}

func TestTagDate(t *testing.T) {
	tests := map[string]string{
		"2017-06-02": "2017-06-02",
		"2017-06":    "2017",
		"2017":       "2017",
		"":           "",
		"20":         "",
		"2017-6-2":   "2017",
		"????":       "",
	}
	for in, want := range tests {
		if got := tagDate(in); got != want {
			t.Errorf("tagDate(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestTagsForFallbacksAndClearing(t *testing.T) {
	va := ArtistCredit{Name: "Various Artists", Artist: Artist{ID: VariousArtistsID, Name: "Various Artists"}}
	band := ArtistCredit{Name: "Band", Artist: Artist{ID: "band-id", Name: "Band"}}

	tests := []struct {
		desc string
		rel  Release
		want Tags
	}{
		{
			// No date, release group or track ID: those tags are cleared. The
			// track has no title or credit, so the recording's stand in; the
			// medium has no track count, so its track list does.
			"missing values",
			Release{
				ID: "rel", Title: "Album", ArtistCredit: []ArtistCredit{band},
				Media: []Medium{{Position: 1, Tracks: []Track{{
					Position:  1,
					Recording: &TrackRecording{ID: "rec", Title: "Song", ArtistCredit: []ArtistCredit{band}},
				}}}},
			},
			Tags{
				Title: "Song", Artist: "Band", AlbumArtist: "Band", Album: "Album",
				Track: 1, TrackTotal: 1, Disc: 1, DiscTotal: 1,
				RecordingID: "rec", AlbumID: "rel",
				AlbumArtistIDs: []string{"band-id"}, ArtistIDs: []string{"band-id"},
			},
		},
		{
			// Picard compares the whole album artist ID, so this is no
			// compilation and keeps its credited album artist.
			"various artists with someone else",
			Release{
				ID: "rel", Title: "Split", Date: "1999-01-02",
				ArtistCredit: []ArtistCredit{{Name: "Various Artists", JoinPhrase: " & ", Artist: va.Artist}, band},
				Media: []Medium{{Position: 1, TrackCount: 1, Tracks: []Track{{
					ID: "trk", Position: 1, Title: "Song", ArtistCredit: []ArtistCredit{band},
				}}}},
			},
			Tags{
				Title: "Song", Artist: "Band", AlbumArtist: "Various Artists & Band", Album: "Split",
				Date: "1999-01-02", Track: 1, TrackTotal: 1, Disc: 1, DiscTotal: 1,
				ReleaseTrackID: "trk", AlbumID: "rel",
				AlbumArtistIDs: []string{VariousArtistsID, "band-id"}, ArtistIDs: []string{"band-id"},
			},
		},
		{
			// A VA credit under another name still writes "Various Artists".
			"various artists credited by another name",
			Release{
				ID: "rel", Title: "Hits", Date: "2001",
				ArtistCredit: []ArtistCredit{{Name: "Diverse Interpreten", Artist: va.Artist}},
				Media: []Medium{{Position: 1, TrackCount: 1, Tracks: []Track{{
					ID: "trk", Position: 1, Title: "Song", ArtistCredit: []ArtistCredit{band},
				}}}},
			},
			Tags{
				Title: "Song", Artist: "Band", AlbumArtist: "Various Artists", Album: "Hits",
				Date: "2001", Track: 1, TrackTotal: 1, Disc: 1, DiscTotal: 1, Compilation: true,
				ReleaseTrackID: "trk", AlbumID: "rel",
				AlbumArtistIDs: []string{VariousArtistsID}, ArtistIDs: []string{"band-id"},
			},
		},
		{
			// The ID is the one the rules place a compilation by, compared
			// as they compare it: whatever its case. It is written as it came.
			"various artists by an upper-case ID",
			Release{
				ID: "rel", Title: "Hits", Date: "2001",
				ArtistCredit: []ArtistCredit{{Name: "Various Artists", Artist: Artist{ID: strings.ToUpper(VariousArtistsID)}}},
				Media: []Medium{{Position: 1, TrackCount: 1, Tracks: []Track{{
					ID: "trk", Position: 1, Title: "Song", ArtistCredit: []ArtistCredit{band},
				}}}},
			},
			Tags{
				Title: "Song", Artist: "Band", AlbumArtist: "Various Artists", Album: "Hits",
				Date: "2001", Track: 1, TrackTotal: 1, Disc: 1, DiscTotal: 1, Compilation: true,
				ReleaseTrackID: "trk", AlbumID: "rel",
				AlbumArtistIDs: []string{strings.ToUpper(VariousArtistsID)}, ArtistIDs: []string{"band-id"},
			},
		},
	}
	for _, tt := range tests {
		if got := TagsFor(&tt.rel, TrackRef{}); !reflect.DeepEqual(got, tt.want) {
			t.Errorf("%s:\n got %+v\nwant %+v", tt.desc, got, tt.want)
		}
	}
	// One definition of the ID, and one way to compare it.
	if VariousArtistsID != rules.VariousArtistsMBID || !rules.HasVariousArtistsID([]string{VariousArtistsID}) {
		t.Errorf("VariousArtistsID = %q, the rules' is %q", VariousArtistsID, rules.VariousArtistsMBID)
	}
}
