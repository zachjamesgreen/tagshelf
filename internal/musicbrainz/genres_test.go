package musicbrainz

import (
	"encoding/json"
	"slices"
	"testing"
)

// genreRelease is a release of one track with the given genres on the
// release, its release group and the track's recording.
func genreRelease(release, group, recording []Genre) *Release {
	return &Release{
		Genres:       release,
		ReleaseGroup: ReleaseGroup{Genres: group},
		Media:        []Medium{{Tracks: []Track{{Recording: &TrackRecording{Genres: recording}}}}},
	}
}

// TestGenresFor checks which genres a track gets: the votes of the release,
// its release group and the recording added up, at most five of the leaders,
// and of those only the ones within a tenth of the first.
func TestGenresFor(t *testing.T) {
	tests := []struct {
		name                      string
		release, group, recording []Genre
		want                      []string
	}{
		{"none anywhere", nil, nil, nil, nil},
		{"one, on the release group", nil, []Genre{{"electronic", 4}}, nil, []string{"Electronic"}},
		{"votes are added up across the three",
			[]Genre{{"rock", 2}}, []Genre{{"rock", 3}, {"pop", 6}}, []Genre{{"rock", 1}, {"jazz", 5}},
			// rock 6, pop 6, jazz 5: jazz has 83 in a hundred of the lead.
			[]string{"Pop", "Rock"}},
		{"a genre well behind the first is left out", nil, []Genre{{"ambient", 10}, {"idm", 9}, {"downtempo", 8}}, nil,
			[]string{"Ambient", "Idm"}},
		{"at most five", nil, []Genre{{"a", 3}, {"b", 3}, {"c", 3}, {"d", 3}, {"e", 3}, {"f", 3}}, nil,
			[]string{"A", "B", "C", "D", "E"}},
		{"a genre no one voted for", nil, []Genre{{"rock", 0}, {" ", 4}}, nil, nil},
		{"listed by name, not by votes", nil, []Genre{{"trip hop", 10}, {"hip hop", 9}}, nil, []string{"Hip Hop", "Trip Hop"}},
		{"a track without a recording", []Genre{{"rock", 1}}, nil, nil, []string{"Rock"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rel := genreRelease(tt.release, tt.group, tt.recording)
			if tt.name == "a track without a recording" {
				rel.Media[0].Tracks[0].Recording = nil
			}
			if got := GenresFor(rel, TrackRef{}); !slices.Equal(got, tt.want) {
				t.Errorf("GenresFor = %q, want %q", got, tt.want)
			}
			if got := TagsFor(rel, TrackRef{}).Genres; !slices.Equal(got, tt.want) {
				t.Errorf("TagsFor gives the genres %q, want %q", got, tt.want)
			}
		})
	}
}

func TestTitleCase(t *testing.T) {
	for in, want := range map[string]string{
		"hip hop": "Hip Hop", "children's music": "Children's Music", "r&b": "R&B", "drum and bass": "Drum And Bass",
		"synth-pop": "Synth-Pop", "2-step": "2-Step", "": "", "Rock": "Rock", "électro": "Électro",
	} {
		if got := titleCase(in); got != want {
			t.Errorf("titleCase(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestReleaseLookupReadsGenres checks the genres are read from where a
// release lookup with inc=genres lists them.
func TestReleaseLookupReadsGenres(t *testing.T) {
	const body = `{
		"id": "rel", "genres": [{"name": "rock", "count": 2, "id": "g1", "disambiguation": ""}],
		"release-group": {"id": "rg", "genres": [{"name": "pop", "count": 1}]},
		"media": [{"position": 1, "tracks": [{"id": "t", "recording": {"id": "rec", "genres": [{"name": "jazz", "count": 3}]}}]}]
	}`
	var rel Release
	if err := json.Unmarshal([]byte(body), &rel); err != nil {
		t.Fatal(err)
	}
	if len(rel.Genres) != 1 || rel.Genres[0] != (Genre{"rock", 2}) || rel.ReleaseGroup.Genres[0].Name != "pop" ||
		rel.Media[0].Tracks[0].Recording.Genres[0] != (Genre{"jazz", 3}) {
		t.Errorf("the release reads as %+v", rel)
	}
	// jazz 3 leads; rock 2 is 66 in a hundred of it.
	if got := GenresFor(&rel, TrackRef{}); !slices.Equal(got, []string{"Jazz"}) {
		t.Errorf("GenresFor = %q", got)
	}
}
