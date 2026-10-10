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
		// The year frame of another version, left by a tagger that
		// changed the tag's version: these used to read as 0.
		{ID3v2_3, "TDRC", "2001", 2001},
		{ID3v2_3, "TDRC", "2001-05-12", 2001},
		{ID3v2_4, "TYER", "2001", 2001},
		{ID3v2_2, "TYE", "2001", 2001},
		{ID3v2_3, "TYER", "2001 ", 2001},
		{ID3v2_4, "TDRC", "20010512", 2001}, // used to read as the year 20010512
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

	// The frame of the tag's own version wins over a leftover one.
	m := metadataID3v2{
		header: &id3v2Header{Version: ID3v2_4},
		frames: map[string]interface{}{"TDRC": "2005", "TYER": "1999"},
	}
	if got := m.Year(); got != 2005 {
		t.Errorf("Year() with TDRC=2005 and TYER=1999 in a 2.4 tag = %d, expected: 2005", got)
	}
}
