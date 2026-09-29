package rules

import "testing"

func TestPossibleKey(t *testing.T) {
	same := [][2][4]string{
		{{"Björk", "Homogenic", "2", "Jóga"}, {"björk", "HOMOGENIC", "2", "jóga"}},
		{{"The Band", "Album  One", "1", "Song, Pt. 1"}, {"the band", "album one", "1", "song pt 1"}},
		{{"AC/DC", "Back in Black", "6", "Back in Black"}, {"AC DC", "Back In Black", "6", "back-in-black"}},
	}
	for _, tt := range same {
		a := PossibleKey(tt[0][0], tt[0][1], atoi(tt[0][2]), tt[0][3])
		b := PossibleKey(tt[1][0], tt[1][1], atoi(tt[1][2]), tt[1][3])
		if a == "" || a != b {
			t.Errorf("PossibleKey(%q) = %q, PossibleKey(%q) = %q; want the same non-empty key", tt[0], a, tt[1], b)
		}
	}

	differ := [][2][4]string{
		{{"Singer", "Album", "1", "Song"}, {"Singer", "Album", "2", "Song"}},
		{{"Singer", "Album", "1", "Song"}, {"Singer", "Other", "1", "Song"}},
		{{"Singer", "Album", "1", "Song"}, {"Singer", "Album", "1", "Song (Live)"}},
	}
	for _, tt := range differ {
		a := PossibleKey(tt[0][0], tt[0][1], atoi(tt[0][2]), tt[0][3])
		b := PossibleKey(tt[1][0], tt[1][1], atoi(tt[1][2]), tt[1][3])
		if a == b {
			t.Errorf("PossibleKey(%q) and PossibleKey(%q) are both %q", tt[0], tt[1], a)
		}
	}

	// Without an artist, album or title there is nothing to compare.
	for _, tt := range [][4]string{{"", "Album", "1", "Song"}, {"Singer", "", "1", "Song"}, {"Singer", "Album", "1", ""}, {"!!", "Album", "1", "Song"}} {
		if k := PossibleKey(tt[0], tt[1], atoi(tt[2]), tt[3]); k != "" {
			t.Errorf("PossibleKey(%q) = %q, want none", tt, k)
		}
	}
}

func atoi(s string) int {
	n := 0
	for _, c := range s {
		n = n*10 + int(c-'0')
	}
	return n
}
