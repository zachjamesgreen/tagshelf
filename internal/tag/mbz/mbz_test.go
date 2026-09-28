package mbz_test

import (
	"os"
	"reflect"
	"testing"

	"tagshelf/internal/fixtures"
	"tagshelf/internal/tag"
	"tagshelf/internal/tag/mbz"
)

var fixtureIDs = mbz.IDs{
	RecordingID:    fixtures.RecordingID,
	ReleaseTrackID: fixtures.ReleaseTrackID,
	AlbumID:        fixtures.ReleaseID,
	ReleaseGroupID: fixtures.ReleaseGroupID,
	ArtistIDs:      []string{fixtures.ArtistID},
	AlbumArtistIDs: []string{fixtures.AlbumArtistID},
}

// TestExtractFixtures reads every ID from each format internal/tag reads,
// written under Picard's key names.
func TestExtractFixtures(t *testing.T) {
	for _, name := range []string{"tagged.mp3", "tagged.flac", "tagged.ogg", "tagged.m4a", "tagged.m4b", fixtures.TaggedID3v2AAC,
		"tagged.wav", "tagged.ape", "tagged.mpc", "tagged.aac"} {
		t.Run(name, func(t *testing.T) {
			f, err := os.Open(fixtures.Path(name))
			if err != nil {
				t.Fatal(err)
			}
			defer f.Close()
			m, err := tag.ReadFrom(f)
			if err != nil {
				t.Fatal(err)
			}
			if got := mbz.Extract(m); !reflect.DeepEqual(got, fixtureIDs) {
				t.Errorf("Extract = %+v\nwant      %+v", got, fixtureIDs)
			}
		})
	}
}

// fakeMetadata carries only what Extract looks at.
type fakeMetadata struct {
	tag.Metadata
	format tag.Format
	raw    map[string]any
}

func (m fakeMetadata) Format() tag.Format     { return m.format }
func (m fakeMetadata) Raw() map[string]any    { return m.raw }
func (m fakeMetadata) FileType() tag.FileType { return tag.UnknownFileType }

// TestExtractUpperCaseTXXX covers TagLib, which writes the TXXX
// descriptions in upper case.
func TestExtractUpperCaseTXXX(t *testing.T) {
	m := fakeMetadata{format: tag.ID3v2_4, raw: map[string]any{
		"TXXX":   &tag.Comm{Description: "MUSICBRAINZ ALBUM ID", Text: "album"},
		"TXXX_0": &tag.Comm{Description: "MUSICBRAINZ ALBUM ARTIST ID", Text: "aa"},
		"TXXX_1": &tag.Comm{Description: "MUSICBRAINZ ARTIST ID", Text: "a"},
		"TXXX_2": &tag.Comm{Description: "MUSICBRAINZ RELEASE GROUP ID", Text: "rg"},
		"TXXX_3": &tag.Comm{Description: "MUSICBRAINZ RELEASE TRACK ID", Text: "rt"},
		"UFID":   &tag.UFID{Provider: "http://musicbrainz.org", Identifier: []byte("rec")},
	}}
	want := mbz.IDs{RecordingID: "rec", ReleaseTrackID: "rt", AlbumID: "album", ReleaseGroupID: "rg", ArtistIDs: []string{"a"}, AlbumArtistIDs: []string{"aa"}}
	if got := mbz.Extract(m); !reflect.DeepEqual(got, want) {
		t.Errorf("Extract = %+v, want %+v", got, want)
	}
}

// TestExtractRecordingPerFormat pins which key holds the recording ID: UFID
// for ID3, "MusicBrainz Track Id" for MP4, and MUSICBRAINZ_TRACKID for
// Vorbis, where Picard puts the release track in MUSICBRAINZ_RELEASETRACKID.
func TestExtractRecordingPerFormat(t *testing.T) {
	tests := []struct {
		name string
		m    fakeMetadata
		want mbz.IDs
	}{
		{"id3", fakeMetadata{format: tag.ID3v2_3, raw: map[string]any{
			"UFID": &tag.UFID{Provider: "http://musicbrainz.org", Identifier: []byte("rec")},
			"TXXX": &tag.Comm{Description: "MusicBrainz Release Track Id", Text: "rt"},
		}}, mbz.IDs{RecordingID: "rec", ReleaseTrackID: "rt"}},
		{"id3 other UFID owner", fakeMetadata{format: tag.ID3v2_4, raw: map[string]any{
			"UFID": &tag.UFID{Provider: "http://example.com", Identifier: []byte("x")},
		}}, mbz.IDs{}},
		{"mp4", fakeMetadata{format: tag.MP4, raw: map[string]any{
			"MusicBrainz Track Id":         "rec",
			"MusicBrainz Release Track Id": "rt",
		}}, mbz.IDs{RecordingID: "rec", ReleaseTrackID: "rt"}},
		{"vorbis", fakeMetadata{format: tag.VORBIS, raw: map[string]any{
			"musicbrainz_trackid":        "rec",
			"musicbrainz_releasetrackid": "rt",
		}}, mbz.IDs{RecordingID: "rec", ReleaseTrackID: "rt"}},
	}
	for _, tt := range tests {
		if got := mbz.Extract(tt.m); !reflect.DeepEqual(got, tt.want) {
			t.Errorf("%s: Extract = %+v, want %+v", tt.name, got, tt.want)
		}
	}
}

// TestExtractUFIDWinsOverTXXX: a TXXX "track id" is only a stand-in for the
// UFID recording. Raw() is a map, so run it often enough to see both orders.
func TestExtractUFIDWinsOverTXXX(t *testing.T) {
	m := fakeMetadata{format: tag.ID3v2_4, raw: map[string]any{
		"UFID": &tag.UFID{Provider: "http://musicbrainz.org", Identifier: []byte("ufid")},
		"TXXX": &tag.Comm{Description: "MusicBrainz Track Id", Text: "txxx"},
	}}
	for range 50 {
		if got := mbz.Extract(m).RecordingID; got != "ufid" {
			t.Fatalf("RecordingID = %q, want the UFID's", got)
		}
	}
	delete(m.raw, "UFID")
	if got := mbz.Extract(m).RecordingID; got != "txxx" {
		t.Errorf("without a UFID, RecordingID = %q, want the TXXX's", got)
	}
}

// TestExtractMultipleArtistIDs covers the ways a list of artist IDs arrives:
// NUL-separated in ID3v2.4, "/"-joined by Picard in ID3v2.3, and ";"-joined
// by the MP4 reader from several data atoms.
func TestExtractMultipleArtistIDs(t *testing.T) {
	for _, tt := range []struct {
		name string
		m    fakeMetadata
	}{
		{"v2.4", fakeMetadata{format: tag.ID3v2_4, raw: map[string]any{"TXXX": &tag.Comm{Description: "MusicBrainz Artist Id", Text: "a\x00b"}}}},
		{"v2.3", fakeMetadata{format: tag.ID3v2_3, raw: map[string]any{"TXXX": &tag.Comm{Description: "MusicBrainz Artist Id", Text: "a / b"}}}},
		{"mp4", fakeMetadata{format: tag.MP4, raw: map[string]any{"MusicBrainz Artist Id": "a;b"}}},
	} {
		if got := mbz.Extract(tt.m).ArtistIDs; !reflect.DeepEqual(got, []string{"a", "b"}) {
			t.Errorf("%s: ArtistIDs = %q, want [a b]", tt.name, got)
		}
	}
}

// TestExtractStripsNULs covers values written NUL-terminated, or with more
// than one value, where a single ID is expected.
func TestExtractStripsNULs(t *testing.T) {
	m := fakeMetadata{format: tag.ID3v2_4, raw: map[string]any{
		"TXXX":   &tag.Comm{Description: "MusicBrainz Album Id", Text: "album\x00"},
		"TXXX_0": &tag.Comm{Description: "MusicBrainz Release Group Id", Text: "rg\x00other"},
	}}
	got := mbz.Extract(m)
	if got.AlbumID != "album" || got.ReleaseGroupID != "rg" {
		t.Errorf("AlbumID = %q, ReleaseGroupID = %q, want album and rg", got.AlbumID, got.ReleaseGroupID)
	}
}

func TestExtractRecordingIDKey(t *testing.T) {
	m := fakeMetadata{format: tag.VORBIS, raw: map[string]any{"musicbrainz_recordingid": "rec"}}
	if got := mbz.Extract(m).RecordingID; got != "rec" {
		t.Errorf("RecordingID = %q, want rec", got)
	}
}
