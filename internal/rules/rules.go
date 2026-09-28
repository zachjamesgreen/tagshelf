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

	"tagshelf/internal/marker"
	"tagshelf/internal/tag"
	"tagshelf/internal/tag/mbz"
)

// SanitizeName makes a tag value safe as a file or folder name, replacing
// characters Windows forbids. Empty results become "Unknown".
func SanitizeName(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return "Unknown"
	}

	// Replace characters forbidden in Windows / general filesystems: <>:"/\|?*
	invalidChars := `<>:"/\|?*`
	var sb strings.Builder
	for _, r := range name {
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
		return "Unknown"
	}
	return cleaned
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

// isCompilation reports whether a track belongs to a Various Artists
// compilation. A real album artist tag always wins, so the MusicBrainz ID only
// counts when the album artist is empty or itself a Various Artists name. The
// iTunes compilation flag is deliberately ignored: it is also set on
// single-artist best-ofs and would pull them out of their artist's folder.
func isCompilation(m tag.Metadata) bool {
	albumArtist := strings.TrimSpace(m.AlbumArtist())
	if isVariousArtistsName(albumArtist) {
		return true
	}
	if albumArtist != "" {
		return false
	}
	return slices.ContainsFunc(mbz.Extract(m).AlbumArtistIDs, func(id string) bool {
		return strings.EqualFold(id, VariousArtistsMBID)
	})
}

// LibraryPath is where an organized file belongs:
// Album Artist/Album (Year)/[disc-]track - Title.ext, or for a compilation
// compilations/Album (Year)/[disc-]track - Track Artist - Title.ext. It
// reports false when the tags lack an artist, album or title.
func LibraryPath(dest, compilations string, m tag.Metadata, ext string) (string, bool) {
	compilation := isCompilation(m)
	var artist string
	if compilation {
		artist = SanitizeName(compilations)
	} else {
		artist = SanitizeName(m.AlbumArtist())
		if artist == "Unknown" {
			// No album artist tag: fall back to the track artist.
			artist = SanitizeName(m.Artist())
		}
	}
	album := m.Album()
	title := SanitizeName(m.Title())
	if artist == "Unknown" || SanitizeName(album) == "Unknown" || title == "Unknown" {
		return "", false
	}
	if year := m.Year(); year > 0 {
		album = fmt.Sprintf("%s (%d)", strings.TrimSpace(album), year)
	}

	name := title
	if compilation {
		// The folder no longer says who performs each track, so the name does.
		if trackArtist := SanitizeName(m.Artist()); trackArtist != "Unknown" {
			name = trackArtist + " - " + title
		}
	}
	if track, _ := m.Track(); track > 0 {
		disc, discs := m.Disc()
		if disc > 0 && (discs > 1 || disc > 1) {
			name = fmt.Sprintf("%d-%02d - %s", disc, track, name)
		} else {
			name = fmt.Sprintf("%02d - %s", track, name)
		}
	}
	return filepath.Join(dest, artist, SanitizeName(album), name+ext), true
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
