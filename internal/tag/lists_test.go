package tag

import (
	"encoding/json"
	"slices"
	"testing"
)

// TestGenresAndComposers checks the lists read from the map of each format's
// reader: every value of a list, in order, without the empty ones.
func TestGenresAndComposers(t *testing.T) {
	tests := []struct {
		name      string
		raw       map[string]interface{}
		genres    []string
		composers []string
	}{
		{"ID3v2.4, a list of each", map[string]interface{}{"TCON": "Rock\x00Hip Hop", "TCOM": "Ann\x00Bob"},
			[]string{"Rock", "Hip Hop"}, []string{"Ann", "Bob"}},
		{"ID3v2.2", map[string]interface{}{"TCO": "Rock", "TCM": "Ann"}, []string{"Rock"}, []string{"Ann"}},
		{"ID3 genres by number", map[string]interface{}{"TCON": "(17)\x00(7)"}, []string{"Rock", "Hip-Hop"}, nil},
		{"Vorbis comments, named in lower case", map[string]interface{}{"genre": "Rock\x00Hip Hop", "composer": "Ann"},
			[]string{"Rock", "Hip Hop"}, []string{"Ann"}},
		{"APEv2, named as written", map[string]interface{}{"Genre": "Rock", "COMPOSER": "Ann\x00Bob"},
			[]string{"Rock"}, []string{"Ann", "Bob"}},
		{"MP4", map[string]interface{}{"\xa9gen": "Rock\x00Hip Hop", "\xa9wrt": "Ann"}, []string{"Rock", "Hip Hop"}, []string{"Ann"}},
		{"ASF", map[string]interface{}{"WM/Genre": "Rock\x00Hip Hop", "WM/Composer": "Ann"}, []string{"Rock", "Hip Hop"}, []string{"Ann"}},
		{"WAV: the ID3 chunk before the INFO one", map[string]interface{}{"TCON": "Rock\x00Hip Hop", "IGNR": "Rock"},
			[]string{"Rock", "Hip Hop"}, nil},
		{"WAV with an INFO chunk alone", map[string]interface{}{"IGNR": "Rock"}, []string{"Rock"}, nil},
		{"empty values and spaces are dropped", map[string]interface{}{"genre": " Rock \x00\x00 ", "composer": "\x00"}, []string{"Rock"}, nil},
		{"a value that is not text", map[string]interface{}{"genre": 17, "composer": []string{"Ann"}}, nil, nil},
		{"no tags", nil, nil, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Genres(tt.raw); !slices.Equal(got, tt.genres) {
				t.Errorf("Genres = %q, want %q", got, tt.genres)
			}
			if got := Composers(tt.raw); !slices.Equal(got, tt.composers) {
				t.Errorf("Composers = %q, want %q", got, tt.composers)
			}

			// The same from a map that was kept as JSON, as the index keeps
			// it: an MP4 name is not UTF-8, and comes back changed.
			data, err := json.Marshal(tt.raw)
			if err != nil {
				t.Fatal(err)
			}
			var kept map[string]interface{}
			if err := json.Unmarshal(data, &kept); err != nil {
				t.Fatal(err)
			}
			if got := Genres(kept); !slices.Equal(got, tt.genres) {
				t.Errorf("Genres of the map kept as JSON = %q, want %q", got, tt.genres)
			}
			if got := Composers(kept); !slices.Equal(got, tt.composers) {
				t.Errorf("Composers of the map kept as JSON = %q, want %q", got, tt.composers)
			}
		})
	}
}
