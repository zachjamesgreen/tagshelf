// Package rules holds tagshelf's pure placement rules: where a tagged file
// belongs in the library and whether its tags are good enough to put it there.
// Nothing here touches the filesystem.
package rules

import (
	"fmt"
	"path/filepath"
	"runtime"
	"slices"
	"strings"

	"github.com/zachjamesgreen/tagshelf/internal/marker"
	"github.com/zachjamesgreen/tagshelf/internal/tag"
	"github.com/zachjamesgreen/tagshelf/internal/tag/mbz"
)

// nameStandIn is the name of a file or folder whose tag has nothing left once
// it is made safe, such as "..." or "?".
const nameStandIn = "_"

// SanitizeName makes a tag value safe as a file or folder name, replacing
// characters Windows forbids. A value with nothing left becomes "_". Whether
// a tag is missing is asked of the tag itself (see blank), never of the
// result: any name returned here may be a real value.
func SanitizeName(name string) string {
	// Replace characters forbidden in Windows / general filesystems: <>:"/\|?*
	invalidChars := `<>:"/\|?*`
	var sb strings.Builder
	for _, r := range strings.TrimSpace(name) {
		if strings.ContainsRune(invalidChars, r) || r < 32 {
			sb.WriteRune('_')
		} else {
			sb.WriteRune(r)
		}
	}

	cleaned := strings.TrimSpace(sb.String())
	// Strip trailing periods or spaces which Windows forbids in file/dir names
	cleaned = strings.TrimRight(cleaned, ". ")
	if cleaned == "" {
		return nameStandIn
	}
	return cleaned
}

// blank reports whether a tag has no value.
func blank(value string) bool {
	return strings.TrimSpace(value) == ""
}

func isMBZTrackOrAlbumKey(k string) bool {
	if strings.Contains(k, "musicbrainz_trackid") ||
		strings.Contains(k, "musicbrainz_recordingid") ||
		strings.Contains(k, "musicbrainz_albumid") ||
		strings.Contains(k, "musicbrainz track id") ||
		strings.Contains(k, "musicbrainz release track id") ||
		strings.Contains(k, "musicbrainz album id") {
		return true
	}
	if strings.Contains(k, "musicbrainz") &&
		(strings.Contains(k, "track") || strings.Contains(k, "recording") || strings.Contains(k, "album") || strings.Contains(k, "release")) {
		return true
	}
	return false
}

func isNonEmpty(v any) bool {
	switch val := v.(type) {
	case string:
		return strings.TrimSpace(val) != ""
	case []byte:
		return len(val) > 0
	case *tag.Comm:
		return val != nil && strings.TrimSpace(val.Text) != ""
	case tag.Comm:
		return strings.TrimSpace(val.Text) != ""
	case *tag.UFID:
		return val != nil && len(val.Identifier) > 0
	case tag.UFID:
		return len(val.Identifier) > 0
	default:
		return false
	}
}

// HasMusicBrainzTags reports whether the tags carry a MusicBrainz track,
// recording, album or release ID, one of the two ways (with the manual
// marker) a file can qualify for the library.
func HasMusicBrainzTags(m tag.Metadata) bool {
	if m == nil {
		return false
	}

	raw := m.Raw()
	if len(raw) == 0 {
		return false
	}

	for k, v := range raw {
		kl := strings.ToLower(k)

		// Check UFID frames (ID3)
		if strings.HasPrefix(kl, "ufid") || strings.HasPrefix(kl, "ufi") {
			switch u := v.(type) {
			case *tag.UFID:
				if u != nil && len(u.Identifier) > 0 && strings.Contains(strings.ToLower(u.Provider), "musicbrainz") {
					return true
				}
			case tag.UFID:
				if len(u.Identifier) > 0 && strings.Contains(strings.ToLower(u.Provider), "musicbrainz") {
					return true
				}
			}
		}

		// Check TXXX frames (ID3 user defined text)
		if strings.HasPrefix(kl, "txxx") || strings.HasPrefix(kl, "txx") {
			var desc, text string
			switch comm := v.(type) {
			case *tag.Comm:
				if comm != nil {
					desc, text = comm.Description, comm.Text
				}
			case tag.Comm:
				desc, text = comm.Description, comm.Text
			}
			if strings.TrimSpace(text) != "" {
				descLower := strings.ToLower(desc)
				if isMBZTrackOrAlbumKey(descLower) {
					return true
				}
			}
		}

		// Direct tags (Vorbis, MP4, APE, or raw tag names)
		if isMBZTrackOrAlbumKey(kl) && isNonEmpty(v) {
			return true
		}
	}

	return false
}

// DefaultCompilations is the artist folder for Various Artists albums when the
// config does not name one.
const DefaultCompilations = "Various Artists"

// VariousArtistsMBID is MusicBrainz's special "Various Artists" artist, which
// Picard writes as the album artist ID of multi-artist compilations.
const VariousArtistsMBID = "89ad4ac3-39f7-470e-963a-56509c546377"

// isVariousArtistsName reports whether an album artist tag spells out Various
// Artists by hand.
func isVariousArtistsName(name string) bool {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "various artists", "various", "va", "v.a.":
		return true
	}
	return false
}

// Tags are the tags a file's place in the library is worked out from.
type Tags struct {
	Artist, AlbumArtist, Album, Title string
	Year, Track, Disc, DiscTotal      int
	// VariousArtistsID is set when the MusicBrainz album artist IDs name the
	// special Various Artists artist.
	VariousArtistsID bool
}

// TagsOf reads from m the tags LibraryPathFor goes by.
func TagsOf(m tag.Metadata) Tags {
	t := Tags{Artist: m.Artist(), AlbumArtist: m.AlbumArtist(), Album: m.Album(), Title: m.Title(), Year: m.Year()}
	t.Track, _ = m.Track()
	t.Disc, t.DiscTotal = m.Disc()
	// The IDs only count without an album artist to go by.
	if albumArtist := strings.TrimSpace(t.AlbumArtist); albumArtist == "" || isVariousArtistsName(albumArtist) {
		t.VariousArtistsID = HasVariousArtistsID(mbz.Extract(m).AlbumArtistIDs)
	}
	return t
}

// HasVariousArtistsID reports whether MusicBrainz album artist IDs name the
// special Various Artists artist.
func HasVariousArtistsID(ids []string) bool {
	return slices.ContainsFunc(ids, func(id string) bool {
		return strings.EqualFold(id, VariousArtistsMBID)
	})
}

// isCompilation reports whether a track belongs to a Various Artists
// compilation. A real album artist tag always wins, so the MusicBrainz ID only
// counts when the album artist is empty or itself a Various Artists name. The
// iTunes compilation flag is deliberately ignored: it is also set on
// single-artist best-ofs and would pull them out of their artist's folder.
func (t Tags) isCompilation() bool {
	albumArtist := strings.TrimSpace(t.AlbumArtist)
	if isVariousArtistsName(albumArtist) {
		return true
	}
	return albumArtist == "" && t.VariousArtistsID
}

// LibraryPath is where an organized file belongs:
// Album Artist/Album (Year)/[disc-]track - Title.ext, or for a compilation
// compilations/Album (Year)/[disc-]track - Track Artist - Title.ext. It
// reports false when the tags lack an artist, album or title.
func LibraryPath(dest, compilations string, m tag.Metadata, ext string) (string, bool) {
	return LibraryPathFor(dest, compilations, TagsOf(m), ext)
}

// LibraryPathFor is LibraryPath for tags that are not in a file, such as
// those a person is still typing.
func LibraryPathFor(dest, compilations string, t Tags, ext string) (string, bool) {
	compilation := t.isCompilation()
	artist := t.AlbumArtist
	switch {
	case compilation:
		artist = compilations
	case blank(artist):
		// No album artist tag: fall back to the track artist.
		artist = t.Artist
	}
	album := t.Album
	if blank(artist) || blank(album) || blank(t.Title) {
		return "", false
	}
	if t.Year > 0 {
		album = fmt.Sprintf("%s (%d)", strings.TrimSpace(album), t.Year)
	}

	name := SanitizeName(t.Title)
	if compilation && !blank(t.Artist) {
		// The folder no longer says who performs each track, so the name does.
		name = SanitizeName(t.Artist) + " - " + name
	}
	if t.Track > 0 {
		if t.Disc > 0 && (t.DiscTotal > 1 || t.Disc > 1) {
			name = fmt.Sprintf("%d-%02d - %s", t.Disc, t.Track, name)
		} else {
			name = fmt.Sprintf("%02d - %s", t.Track, name)
		}
	}
	return filepath.Join(dest, SanitizeName(artist), SanitizeName(album), name+ext), true
}

// Qualifies reports whether a file qualifies for the library, and if so its
// LibraryPath. It qualifies when its tags carry MusicBrainz IDs or a manual
// marker (any method), and an artist (album artist, falling back to track
// artist), album and title. Ripper tags without the marker do not qualify.
func Qualifies(dest, compilations string, m tag.Metadata, ext string) (string, bool) {
	if m == nil {
		return "", false
	}
	if !HasMusicBrainzTags(m) {
		if _, marked := marker.Read(m); !marked {
			return "", false
		}
	}
	return LibraryPath(dest, compilations, m, ext)
}

// PathKey normalizes a destination path for reservation lookups. Windows
// paths are case-insensitive, so two spellings differing only in case name the
// same file and must share a single reservation.
func PathKey(path string) string {
	key := filepath.Clean(path)
	if runtime.GOOS == "windows" {
		key = strings.ToLower(key)
	}
	return key
}
