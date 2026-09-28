package musicbrainz

import (
	"testing"
	"time"
)

func credit(name string) []ArtistCredit {
	return []ArtistCredit{{Name: name, Artist: Artist{Name: name}}}
}

// release builds a search-result release whose one medium holds the
// recording at track (1-based) of mediumTracks.
func release(id, title, status, primary string, secondary []string, date string, track, mediumTracks int) Release {
	return Release{
		ID: id, Title: title, Status: status, Date: date,
		ReleaseGroup: ReleaseGroup{PrimaryType: primary, SecondaryTypes: secondary},
		Media: []Medium{{
			Position: 1, Format: "CD", TrackCount: mediumTracks, TrackOffset: track - 1,
			Track: []Track{{ID: id + "-t", Number: "x"}},
		}},
	}
}

func TestSimilarity(t *testing.T) {
	tests := []struct {
		a, b string
		want float64
	}{
		{"Blow Your Mind", "blow your mind", 1},
		{"Blow Your Mind!", "Blow  your  mind", 1},
		{"", "", 1},
		{"abc", "", 0},
		{"abcd", "abce", 0.75},
		{"Café", "cafe", 0.75},
	}
	for _, tt := range tests {
		if got := similarity(tt.a, tt.b); got != tt.want {
			t.Errorf("similarity(%q, %q) = %v, want %v", tt.a, tt.b, got, tt.want)
		}
	}
}

func TestRankScores(t *testing.T) {
	rec := Recording{ID: "r", Title: "Song", Length: 200000, ArtistCredit: credit("Band")}
	perfect := release("a", "Album", "Official", "Album", nil, "2001-05-01", 3, 10)
	rec.Releases = []Release{perfect}

	tests := []struct {
		desc string
		q    Query
		want int
	}{
		{"nothing to compare scores zero", Query{}, 0},
		{"title only", Query{Title: "song"}, 100},
		{"everything matches", Query{Title: "Song", Artist: "Band", Album: "Album", Year: 2001, Track: 3, TrackTotal: 10, Length: 200 * time.Second}, 100},
		// Length 15 s off scores 0.5 of weight 10, title 1 of 13: (13+5)/23.
		{"length half off", Query{Title: "Song", Length: 215 * time.Second}, 78},
		// Past 30 s the length scores nothing: 13/23.
		{"length far off", Query{Title: "Song", Length: 260 * time.Second}, 57},
		// Wrong year, track and total: (13+4+5)/(13+4+5+4+4+4) = 22/34.
		{"wrong numbers and year", Query{Title: "Song", Artist: "Band", Album: "Album", Year: 1999, Track: 4, TrackTotal: 12}, 65},
		// Title "Other" vs "Song": 1 - 5/5 = 0; artist matches: 4/17.
		{"wrong title", Query{Title: "Other", Artist: "Band"}, 24},
	}
	for _, tt := range tests {
		got := Rank(tt.q, []Recording{rec}, nil)
		if len(got) != 1 {
			t.Fatalf("%s: %d candidates", tt.desc, len(got))
		}
		if got[0].Score != tt.want {
			t.Errorf("%s: score = %d, want %d", tt.desc, got[0].Score, tt.want)
		}
	}
}

func TestRankCandidateFields(t *testing.T) {
	rec := Recording{ID: "r", Title: "Song", Length: 200000, ArtistCredit: []ArtistCredit{
		{Name: "A", JoinPhrase: " feat. "}, {Name: "B"},
	}}
	rel := release("a", "Album", "Official", "Album", nil, "2001", 6, 12)
	rel.Media[0].Position = 2
	rel.Media[0].Format = "Digital Media"
	rel.Media[0].Track[0].Length = 201500
	rec.Releases = []Release{rel}

	c := Rank(Query{Title: "Song", Length: 200 * time.Second}, []Recording{rec}, nil)[0]
	if c.Title() != "Song" || c.Artist() != "A feat. B" {
		t.Errorf("title/artist = %q/%q", c.Title(), c.Artist())
	}
	if c.Track != 6 || c.TrackTotal != 12 || c.Disc != 2 || c.Format != "Digital Media" {
		t.Errorf("position = %d/%d disc %d %q", c.Track, c.TrackTotal, c.Disc, c.Format)
	}
	// The track's own length wins over the recording's.
	if !c.HasLengthDelta || c.LengthDelta != 1500*time.Millisecond {
		t.Errorf("length delta = %v (%v)", c.LengthDelta, c.HasLengthDelta)
	}
	if c.Release.Year() != 2001 {
		t.Errorf("year = %d", c.Release.Year())
	}

	noLen := Rank(Query{Title: "Song"}, []Recording{rec}, nil)[0]
	if noLen.HasLengthDelta {
		t.Error("length delta without a file length")
	}
}

func TestRankOrder(t *testing.T) {
	q := Query{Title: "Song", Artist: "Band", Length: 200 * time.Second}
	recs := []Recording{
		{
			ID: "live", Title: "Song (live)", Length: 260000, ArtistCredit: credit("Band"),
			Releases: []Release{release("live-album", "Live", "Official", "Album", []string{"Live"}, "2005", 1, 10)},
		},
		{
			ID: "studio", Title: "Song", Length: 200000, ArtistCredit: credit("Band"),
			Releases: []Release{
				release("comp", "Hits", "Official", "Album", []string{"Compilation"}, "2010", 5, 20),
				release("promo", "Song", "Promotion", "Single", nil, "2000", 1, 2),
				release("album", "Album", "Official", "Album", nil, "2001", 3, 10),
				release("album-lib", "Album (Deluxe)", "Official", "Album", nil, "2002", 3, 14),
				release("single", "Song", "Official", "Single", nil, "2000", 1, 2),
			},
		},
		{ID: "standalone", Title: "Song", Length: 200000, ArtistCredit: credit("Band")},
	}
	inLibrary := func(id string) bool { return id == "album-lib" || id == "single" }

	got := Rank(q, recs, inLibrary)
	var ids []string
	for _, c := range got {
		id := "(none)"
		if c.Release != nil {
			id = c.Release.ID
		}
		ids = append(ids, id)
	}
	// All studio pairs score 100 (album, year and numbers are not in the
	// query); official albums come first, the one in the library first among
	// them, then the rest in service order; the live take scores lowest.
	want := []string{"album-lib", "album", "single", "comp", "promo", "(none)", "live-album"}
	if len(ids) != len(want) {
		t.Fatalf("order = %v, want %v", ids, want)
	}
	for i := range want {
		if ids[i] != want[i] {
			t.Fatalf("order = %v, want %v", ids, want)
		}
	}
	if !got[0].InLibrary || got[1].InLibrary {
		t.Errorf("InLibrary flags wrong: %v %v", got[0].InLibrary, got[1].InLibrary)
	}
	if got[0].Score != 100 || got[len(got)-1].Score >= 100 {
		t.Errorf("scores: first %d, last %d", got[0].Score, got[len(got)-1].Score)
	}
}

func TestRankAlbumAndNumbersDecideBetweenReleases(t *testing.T) {
	rec := Recording{ID: "r", Title: "Song", Length: 200000, ArtistCredit: credit("Band"), Releases: []Release{
		release("comp", "Hits", "Official", "Album", []string{"Compilation"}, "2010", 5, 20),
		release("album", "Album", "Official", "Album", nil, "2001", 3, 10),
	}}
	got := Rank(Query{Title: "Song", Album: "Album", Track: 3, TrackTotal: 10, Year: 2001}, []Recording{rec}, nil)
	if got[0].Release.ID != "album" || got[0].Score != 100 || got[1].Score >= 50 {
		t.Errorf("got %s %d, %s %d", got[0].Release.ID, got[0].Score, got[1].Release.ID, got[1].Score)
	}
}
