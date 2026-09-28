// Copyright 2015, David Howden
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package tag

import "testing"

func TestParseXofN(t *testing.T) {
	table := []struct {
		str  string
		x, n int
	}{
		{"", 0, 0},
		{"1", 1, 0},
		{"0/2", 0, 2},
		{"1/2", 1, 2},
		{"1 / 2", 1, 2},
		{"1/", 1, 0},
		{"/2", 0, 2},
	}

	for ii, tt := range table {
		gotX, gotN := parseXofN(tt.str)
		if gotX != tt.x || gotN != tt.n {
			t.Errorf("[%d] parseXofN(%v) = %d, %d, expected: %d, %d", ii, tt.str, gotX, gotN, tt.x, tt.n)
		}
	}
}

func TestID3v2Year(t *testing.T) {
	table := []struct {
		format Format
		frame  string
		value  string
		year   int
	}{
		{ID3v2_3, "TYER", "1997", 1997},
		{ID3v2_4, "TDRC", "1997", 1997},
		{ID3v2_4, "TDRC", "1997-05", 1997},
		{ID3v2_4, "TDRC", "1997-05-12", 1997},
		{ID3v2_4, "TDRC", "1997-05-12T10", 1997},
		{ID3v2_4, "TDRC", "1997-05-12T10:30:00", 1997},
		{ID3v2_4, "TDRC", "", 0},
		{ID3v2_4, "TDRC", "May 1997", 0},
		{ID3v2_4, "TDRC", "19x7-05", 0},
	}

	for ii, tt := range table {
		m := metadataID3v2{
			header: &id3v2Header{Version: tt.format},
			frames: map[string]interface{}{tt.frame: tt.value},
		}
		if got := m.Year(); got != tt.year {
			t.Errorf("[%d] Year() for %s=%q = %d, expected: %d", ii, tt.frame, tt.value, got, tt.year)
		}
	}
}
