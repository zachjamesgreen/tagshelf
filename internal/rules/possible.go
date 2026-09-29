package rules

import (
	"strconv"
	"strings"
	"unicode"
)

// PossibleKey is the key that makes two files without MusicBrainz IDs a
// possible duplicate: their artist, album, track number and title, compared
// without case, punctuation or extra spaces. It is "" when the artist, album
// or title leaves nothing to compare.
func PossibleKey(artist, album string, track int, title string) string {
	artist, album, title = normalizeTag(artist), normalizeTag(album), normalizeTag(title)
	if artist == "" || album == "" || title == "" {
		return ""
	}
	return artist + "\x00" + album + "\x00" + strconv.Itoa(track) + "\x00" + title
}

// normalizeTag lower-cases s and keeps its letters and digits, with a single
// space wherever anything else stood.
func normalizeTag(s string) string {
	var b strings.Builder
	space := false
	for _, r := range strings.ToLower(s) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			if space && b.Len() > 0 {
				b.WriteByte(' ')
			}
			b.WriteRune(r)
			space = false
			continue
		}
		space = true
	}
	return b.String()
}
