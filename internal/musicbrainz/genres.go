package musicbrainz

import (
	"cmp"
	"slices"
	"strings"
	"unicode"
)

// Genre is a genre people have given an entity, with how many did.
type Genre struct {
	Name  string `json:"name"`
	Count int    `json:"count"`
}

// How genres are picked for a track, which is how Picard picks them when its
// genre option is on, with its defaults.
const (
	// maxGenres is the most genres a track gets.
	maxGenres = 5
	// minGenreShare is the share, in percent, of the leading genre's votes a
	// genre needs to be kept.
	minGenreShare = 90
)

// GenresFor lists the genres of track ref of a looked-up release, nil when
// MusicBrainz has none. The votes for a genre are added up over the track's
// recording, the release and its release group. Of the genres with the most
// votes, at most maxGenres are kept, and of those only the ones with at
// least minGenreShare percent of the leading genre's votes. They are named
// with a capital to each word and listed by name.
func GenresFor(rel *Release, ref TrackRef) []string {
	votes := map[string]int{}
	add := func(genres []Genre) {
		for _, g := range genres {
			if name := strings.TrimSpace(g.Name); name != "" && g.Count > 0 {
				votes[name] += g.Count
			}
		}
	}
	add(rel.Genres)
	add(rel.ReleaseGroup.Genres)
	if r := rel.Media[ref.Medium].Tracks[ref.Index].Recording; r != nil {
		add(r.Genres)
	}
	if len(votes) == 0 {
		return nil
	}

	names := make([]string, 0, len(votes))
	for name := range votes {
		names = append(names, name)
	}
	slices.SortFunc(names, func(a, b string) int {
		return cmp.Or(cmp.Compare(votes[b], votes[a]), cmp.Compare(a, b))
	})
	top := votes[names[0]]
	var kept []string
	for _, name := range names[:min(len(names), maxGenres)] {
		if 100*votes[name]/top >= minGenreShare {
			kept = append(kept, titleCase(name))
		}
	}
	slices.Sort(kept)
	return kept
}

// titleCase gives every word of a genre a capital: MusicBrainz names genres
// in lower case. A letter starts a word unless a letter, a digit or an
// apostrophe comes before it, so "children's music" is "Children's Music"
// and "r&b" is "R&B".
func titleCase(s string) string {
	var b strings.Builder
	inWord := false
	for _, r := range s {
		if !inWord {
			r = unicode.ToUpper(r)
		}
		inWord = unicode.IsLetter(r) || unicode.IsDigit(r) || r == '\'' || r == '’'
		b.WriteRune(r)
	}
	return b.String()
}
