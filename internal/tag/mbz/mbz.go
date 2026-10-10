// Package mbz extracts MusicBrainz Picard-specific tags from general tag metadata.
// See https://picard.musicbrainz.org/docs/mappings/ for more information.
package mbz

import (
	"maps"
	"slices"
	"strings"

	"github.com/zachjamesgreen/tagshelf/internal/tag"
)

// UFIDProviderURL is the URL that we match inside a UFID tag.
const UFIDProviderURL = "http://musicbrainz.org"

// IDs are the MusicBrainz identifiers of one file, read the same way from
// every format. Empty fields mean the file does not carry that ID.
type IDs struct {
	RecordingID    string
	ReleaseTrackID string
	AlbumID        string // the release
	ReleaseGroupID string
	ArtistIDs      []string
	AlbumArtistIDs []string
}

// one keeps the first ID of a value, which may still carry a NUL terminator
// or further values.
func one(value string) string {
	if ids := splitIDs(value); len(ids) > 0 {
		return ids[0]
	}
	return ""
}

// setters maps lower-cased key names to the ID they hold. The Vorbis and
// APEv2 names, the MP4 freeform and ID3 TXXX names and the ASF attribute
// names never collide, so one table serves every format. Every "track id"
// name is the recording: Picard writes the release track under "release
// track id". In ID3 the recording is normally a UFID frame, which Extract
// reads separately.
var setters = map[string]func(ids *IDs, value string){
	"musicbrainz_trackid":          setRecording,
	"musicbrainz track id":         setRecording,
	"musicbrainz_recordingid":      setRecording, // not Picard, but seen in the wild
	"musicbrainz_releasetrackid":   func(ids *IDs, v string) { ids.ReleaseTrackID = one(v) },
	"musicbrainz release track id": func(ids *IDs, v string) { ids.ReleaseTrackID = one(v) },
	"musicbrainz_albumid":          func(ids *IDs, v string) { ids.AlbumID = one(v) },
	"musicbrainz album id":         func(ids *IDs, v string) { ids.AlbumID = one(v) },
	"musicbrainz_releasegroupid":   func(ids *IDs, v string) { ids.ReleaseGroupID = one(v) },
	"musicbrainz release group id": func(ids *IDs, v string) { ids.ReleaseGroupID = one(v) },
	"musicbrainz_artistid":         func(ids *IDs, v string) { ids.ArtistIDs = splitIDs(v) },
	"musicbrainz artist id":        func(ids *IDs, v string) { ids.ArtistIDs = splitIDs(v) },
	"musicbrainz_albumartistid":    func(ids *IDs, v string) { ids.AlbumArtistIDs = splitIDs(v) },
	"musicbrainz album artist id":  func(ids *IDs, v string) { ids.AlbumArtistIDs = splitIDs(v) },
	"musicbrainz/track id":         setRecording, // ASF
	"musicbrainz/release track id": func(ids *IDs, v string) { ids.ReleaseTrackID = one(v) },
	"musicbrainz/album id":         func(ids *IDs, v string) { ids.AlbumID = one(v) },
	"musicbrainz/release group id": func(ids *IDs, v string) { ids.ReleaseGroupID = one(v) },
	"musicbrainz/artist id":        func(ids *IDs, v string) { ids.ArtistIDs = splitIDs(v) },
	"musicbrainz/album artist id":  func(ids *IDs, v string) { ids.AlbumArtistIDs = splitIDs(v) },
}

func setRecording(ids *IDs, v string) { ids.RecordingID = one(v) }

// set records value under the ID that key names, if it names one. Keys match
// case-insensitively: TagLib writes the TXXX descriptions in upper case.
func (ids *IDs) set(key, value string) {
	if f, ok := setters[strings.ToLower(strings.TrimSpace(key))]; ok {
		f(ids, value)
	}
}

// splitIDs splits a list of IDs as the readers hand it over: NUL-separated
// (ID3v2.4), joined with "/" (Picard's ID3v2.3 default) or with ";" (the MP4
// reader, for several data atoms). None of these can occur in an MBID.
func splitIDs(value string) []string {
	var ids []string
	for _, id := range strings.FieldsFunc(value, func(r rune) bool { return r == 0 || r == '/' || r == ';' }) {
		if id = strings.TrimSpace(id); id != "" {
			ids = append(ids, id)
		}
	}
	return ids
}

// extractID3 reads the TXXX frames and the MusicBrainz UFID frame.
func extractID3(m tag.Metadata) IDs {
	var txxx, ufid string
	switch m.Format() {
	case tag.ID3v2_2:
		txxx, ufid = "TXX", "UFI"
	default:
		txxx, ufid = "TXXX", "UFID"
	}

	var ids IDs
	var ufidRecording string
	raw := m.Raw()
	for _, k := range sortedKeys(raw) {
		v := raw[k]
		switch {
		case strings.HasPrefix(k, txxx):
			if c, ok := v.(*tag.Comm); ok {
				ids.set(c.Description, c.Text)
			}
		case strings.HasPrefix(k, ufid):
			if id, ok := v.(*tag.UFID); ok && id.Provider == UFIDProviderURL {
				ufidRecording = one(string(id.Identifier))
			}
		}
	}
	// The UFID frame is where Picard keeps the recording; a TXXX "track id"
	// only stands in for a missing one, whatever order the frames come in.
	if ufidRecording != "" {
		ids.RecordingID = ufidRecording
	}
	return ids
}

// extractKeyValue reads formats whose raw tags are plain key/value strings:
// Vorbis comments, MP4 freeform atoms and ASF attributes.
func extractKeyValue(m tag.Metadata) IDs {
	var ids IDs
	raw := m.Raw()
	for _, k := range sortedKeys(raw) {
		if s, ok := raw[k].(string); ok {
			ids.set(k, s)
		}
	}
	return ids
}

// sortedKeys fixes the order raw tags are read in, so that when two keys
// name the same ID the same one wins on every run.
func sortedKeys(raw map[string]any) []string {
	return slices.Sorted(maps.Keys(raw))
}

// Extract returns the MusicBrainz IDs Picard (or TagLib) wrote into m.
func Extract(m tag.Metadata) IDs {
	switch m.Format() {
	case tag.ID3v2_2, tag.ID3v2_3, tag.ID3v2_4:
		return extractID3(m)
	}
	return extractKeyValue(m)
}
