package musicbrainz

import (
	"testing"
	"time"
)

func TestParseFileName(t *testing.T) {
	tests := []struct {
		name          string
		track         int
		artist, title string
	}{
		{"01 - Dua Lipa - Blow Your Mind.mp3", 1, "Dua Lipa", "Blow Your Mind"},
		{"06 - Blow Your Mind.flac", 6, "", "Blow Your Mind"},
		{"06. Blow Your Mind.flac", 6, "", "Blow Your Mind"},
		{"06 Blow Your Mind.flac", 6, "", "Blow Your Mind"},
		{"1-06 Blow Your Mind.flac", 6, "", "Blow Your Mind"},
		{"Dua Lipa - Blow Your Mind.mp3", 0, "Dua Lipa", "Blow Your Mind"},
		{"Blow Your Mind.mp3", 0, "", "Blow Your Mind"},
		{"Dua_Lipa_-_Blow_Your_Mind.mp3", 0, "Dua Lipa", "Blow Your Mind"},
		{"Under_Score Title.mp3", 0, "", "Under_Score Title"},
		{"1979.mp3", 0, "", "1979"},
		// A number with only a space after it, no leading zero, and an
		// artist it can belong to is the artist's.
		{"50 Cent - In da Club.mp3", 0, "50 Cent", "In da Club"},
		{"3 Doors Down - Kryptonite.mp3", 0, "3 Doors Down", "Kryptonite"},
		{"30 Seconds to Mars - The Kill.mp3", 0, "30 Seconds to Mars", "The Kill"},
		{"50_Cent_-_In_da_Club.mp3", 0, "50 Cent", "In da Club"},
		// A leading zero, a disc or a separator makes it a track number.
		{"03 Doors Down - Kryptonite.mp3", 3, "Doors Down", "Kryptonite"},
		{"1-12 Dua Lipa - Homesick.mp3", 12, "Dua Lipa", "Homesick"},
		{"12. Dua Lipa - Homesick.mp3", 12, "Dua Lipa", "Homesick"},
		{"12 - Dua Lipa - Homesick.mp3", 12, "Dua Lipa", "Homesick"},
		{"12 Homesick.mp3", 12, "", "Homesick"},
		// Nothing in these two names tells the number from a track number,
		// so it still reads as one; QueryFor does not send it.
		{"99 Luftballons.mp3", 99, "", "Luftballons"},
		{"311 - Down.mp3", 311, "", "Down"},
		{"AC-DC - Back In Black.mp3", 0, "AC-DC", "Back In Black"},
		{"Artist - Title - Remix.mp3", 0, "Artist", "Title - Remix"},
	}
	for _, tt := range tests {
		track, artist, title := parseFileName(tt.name)
		if track != tt.track || artist != tt.artist || title != tt.title {
			t.Errorf("parseFileName(%q) = %d, %q, %q; want %d, %q, %q",
				tt.name, track, artist, title, tt.track, tt.artist, tt.title)
		}
	}
}

func TestQueryFor(t *testing.T) {
	tests := []struct {
		desc string
		in   FileInfo
		want Query
	}{
		{
			desc: "tags win over the file name",
			in: FileInfo{Title: " Blow Your Mind ", Artist: "Dua Lipa", Album: "Dua Lipa", Track: 6, TrackTotal: 12,
				Year: 2017, Length: 178 * time.Second, Path: "D:/x/01 - Other - Thing.mp3", Folder: "Foo - Bar"},
			want: Query{Title: "Blow Your Mind", Artist: "Dua Lipa", Album: "Dua Lipa", Track: 6, TrackTotal: 12,
				Year: 2017, Length: 178 * time.Second},
		},
		{
			// The path has forward slashes, which every system takes.
			desc: "empty title parses the file name and uses the folder hint",
			in:   FileInfo{Path: "D:/Incoming/Dua Lipa - Dua Lipa (2017)/06 - Blow Your Mind.mp3", Folder: "Dua Lipa - Dua Lipa (2017)"},
			want: Query{Title: "Blow Your Mind", Artist: "Dua Lipa", Album: "Dua Lipa"},
		},
		{
			desc: "artist from the file name beats the folder; folder without dash is the album",
			in:   FileInfo{Path: "01 - Dua Lipa - Blow Your Mind.mp3", Folder: "Greatest [2020]"},
			want: Query{Title: "Blow Your Mind", Artist: "Dua Lipa", Album: "Greatest"},
		},
		{
			desc: "existing partial tags are kept when the title is parsed",
			in:   FileInfo{Artist: "Tagged", Track: 3, Path: "01 - Other - Song.mp3"},
			want: Query{Title: "Song", Artist: "Tagged", Track: 3},
		},
		{
			desc: "a track number in the file name is a guess the form does not show, so it is not sent",
			in:   FileInfo{Path: "99 Luftballons.mp3", TrackTotal: 10, Length: 232 * time.Second},
			want: Query{Title: "Luftballons", TrackTotal: 10, Length: 232 * time.Second},
		},
		{
			desc: "an artist whose name starts with a number keeps it",
			in:   FileInfo{Path: "50 Cent - In da Club.mp3", Folder: "Get Rich or Die Tryin'"},
			want: Query{Title: "In da Club", Artist: "50 Cent", Album: "Get Rich or Die Tryin'"},
		},
		{
			desc: "no folder hint",
			in:   FileInfo{Path: "Song.ogg"},
			want: Query{Title: "Song"},
		},
	}
	for _, tt := range tests {
		if got := QueryFor(tt.in); got != tt.want {
			t.Errorf("%s: QueryFor = %+v, want %+v", tt.desc, got, tt.want)
		}
	}
}

func TestLucene(t *testing.T) {
	tests := []struct {
		desc string
		in   Query
		want string
	}{
		{"empty", Query{}, ""},
		{
			"all fields",
			Query{Title: "Blow Your Mind", Artist: "Dua Lipa", Album: "Dua Lipa", Year: 2017, Track: 6, TrackTotal: 12, Length: 178583 * time.Millisecond},
			"recording:(blow your mind) artist:(dua lipa) release:(dua lipa) tnum:(6) tracks:(12) qdur:(88 OR 89 OR 90)",
		},
		{"escaping", Query{Title: `What? (Live) [2:30] "x" a+b-c && d || !e {f} ^g ~h *i \j /k`, Artist: "AC/DC"},
			`recording:(what\? \(live\) \[2\:30\] \"x\" a\+b\-c \&\& d \|\| \!e \{f\} \^g \~h \*i \\j \/k) artist:(ac\/dc)`},
		{"operators are lower-cased", Query{Title: "NOT AND OR"}, "recording:(not and or)"},
		{"whitespace-only fields are skipped", Query{Title: "  ", Artist: "x"}, "artist:(x)"},
		{"bucket zero has no negative neighbour", Query{Length: 1500 * time.Millisecond}, "qdur:(0 OR 1)"},
		{"bucket boundary", Query{Length: 4 * time.Second}, "qdur:(1 OR 2 OR 3)"},
		{"just under a boundary", Query{Length: 3999 * time.Millisecond}, "qdur:(0 OR 1 OR 2)"},
	}
	for _, tt := range tests {
		if got := tt.in.Lucene(); got != tt.want {
			t.Errorf("%s: Lucene() =\n %s\nwant\n %s", tt.desc, got, tt.want)
		}
	}
}
