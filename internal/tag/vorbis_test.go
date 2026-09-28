package tag

import "testing"

func TestVorbisTrackAndDisc(t *testing.T) {
	table := []struct {
		c                          map[string]string
		track, tracks, disc, discs int
	}{
		{map[string]string{"tracknumber": "3", "tracktotal": "12"}, 3, 12, 0, 0},
		{map[string]string{"tracknumber": "3/12", "discnumber": "1/2"}, 3, 12, 1, 2},
		{map[string]string{"tracknumber": "3/12", "tracktotal": "13"}, 3, 13, 0, 0},
		{map[string]string{"tracknumber": " 3 / 12 ", "discnumber": "2", "disctotal": "3"}, 3, 12, 2, 3},
		{map[string]string{"discnumber": "1/2", "disctotal": "4"}, 0, 0, 1, 4},
	}

	for ii, tt := range table {
		m := &metadataVorbis{c: tt.c}
		if x, n := m.Track(); x != tt.track || n != tt.tracks {
			t.Errorf("[%d] Track() = %d, %d, expected: %d, %d", ii, x, n, tt.track, tt.tracks)
		}
		if x, n := m.Disc(); x != tt.disc || n != tt.discs {
			t.Errorf("[%d] Disc() = %d, %d, expected: %d, %d", ii, x, n, tt.disc, tt.discs)
		}
	}
}
