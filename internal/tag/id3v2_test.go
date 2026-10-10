// Copyright 2015, David Howden
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package tag

import (
	"bytes"
	"testing"
)

func TestUnsynchroniser(t *testing.T) {
	tests := []struct {
		input  []byte
		output []byte
	}{
		{
			input:  []byte{},
			output: []byte{},
		},

		{
			input:  []byte{0x00},
			output: []byte{0x00},
		},

		{
			input:  []byte{0xFF},
			output: []byte{0xFF},
		},

		{
			input:  []byte{0xFF, 0x00},
			output: []byte{0xFF},
		},

		{
			input:  []byte{0xFF, 0x00, 0x00},
			output: []byte{0xFF, 0x00},
		},

		{
			input:  []byte{0xFF, 0x00, 0x01},
			output: []byte{0xFF, 0x01},
		},

		{
			input:  []byte{0xFF, 0x00, 0xFF, 0x00},
			output: []byte{0xFF, 0xFF},
		},

		{
			input:  []byte{0xFF, 0x00, 0xFF, 0xFF, 0x00},
			output: []byte{0xFF, 0xFF, 0xFF},
		},

		{
			input:  []byte{0x00, 0x01, 0x02},
			output: []byte{0x00, 0x01, 0x02},
		},
	}

	for ii, tt := range tests {
		got := removeUnsynchronisation(tt.input)
		if !bytes.Equal(got, tt.output) {
			t.Errorf("[%d] got: %v, expected %v", ii, got, tt.output)
		}
	}
}

func TestGenreExpension(t *testing.T) {
	var tests = map[string]string{
		"Test":         "Test",
		"((17)":        "(17)",
		"(17) Test":    "Rock Test",
		"(17)Test":     "Rock Test",
		"(17)":         "Rock",
		"Test(17)":     "Test Rock",
		"Test (17)":    "Test Rock",
		"(17)(93)":     "Rock Psychedelic Rock",
		"(17)Test(93)": "Rock Test Psychedelic Rock",
		"(175)":		"Post-Punk",
		"(187)":		"Indie Rock",
		"(191)":		"Psybient",
	}
	for g, r := range tests {
		got := id3v2genre(g)

		if got != r {
			t.Errorf("[%v] got: %v, expected %v", g, got, r)
		}
	}
}
