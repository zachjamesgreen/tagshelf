package rules

import (
	"testing"
	"time"
)

func TestBetterCopyOrder(t *testing.T) {
	base := Copy{Bitrate: 320, SampleRate: 44100, BitDepth: 16, Length: 200 * time.Second, IndexOrder: 1}
	with := func(f func(*Copy)) Copy {
		c := base
		f(&c)
		return c
	}
	tests := []struct {
		name          string
		better, worse Copy
	}{
		{"lossless over lossy", with(func(c *Copy) { c.Lossless, c.Bitrate, c.IndexOrder = true, 128, 9 }), base},
		{"higher bitrate", with(func(c *Copy) { c.Bitrate, c.IndexOrder = 321, 9 }), base},
		{"higher sample rate", with(func(c *Copy) { c.SampleRate, c.IndexOrder = 48000, 9 }), base},
		{"higher bit depth", with(func(c *Copy) { c.BitDepth, c.IndexOrder = 24, 9 }), base},
		{"more than 2 s longer", with(func(c *Copy) { c.Length, c.IndexOrder = 203*time.Second, 9 }), base},
		{"in the library", with(func(c *Copy) { c.InLibrary, c.IndexOrder = true, 9 }), base},
		{"MusicBrainz IDs", with(func(c *Copy) { c.MusicBrainzIDs, c.IndexOrder = 1, 9 }), base},
		{"manual marker", with(func(c *Copy) { c.Manual, c.IndexOrder = true, 9 }), base},
		{"art", with(func(c *Copy) { c.Art, c.IndexOrder = true, 9 }), base},
		{"more IDs", with(func(c *Copy) { c.MusicBrainzIDs, c.IndexOrder = 3, 9 }), with(func(c *Copy) { c.MusicBrainzIDs, c.Manual = 1, true })},
		{"indexed first", base, with(func(c *Copy) { c.IndexOrder = 2 })},
		// Each rule outranks every one after it.
		{"quality before location", with(func(c *Copy) { c.Bitrate, c.IndexOrder = 321, 9 }), with(func(c *Copy) { c.InLibrary, c.MusicBrainzIDs = true, 3 })},
		{"location before tags", with(func(c *Copy) { c.InLibrary, c.IndexOrder = true, 9 }), with(func(c *Copy) { c.MusicBrainzIDs, c.Manual, c.Art = 3, true, true })},
		{"length before location", with(func(c *Copy) { c.Length, c.IndexOrder = 203*time.Second, 9 }), with(func(c *Copy) { c.InLibrary = true })},
		// Each neighbouring pair of the quality rules: the better copy wins on
		// the earlier rule and loses on the later.
		{"lossless before bitrate", with(func(c *Copy) { c.Lossless, c.Bitrate, c.IndexOrder = true, 128, 9 }), with(func(c *Copy) { c.Bitrate = 999 })},
		{"bitrate before sample rate", with(func(c *Copy) { c.Bitrate, c.IndexOrder = 321, 9 }), with(func(c *Copy) { c.SampleRate = 48000 })},
		{"sample rate before bit depth", with(func(c *Copy) { c.SampleRate, c.IndexOrder = 48000, 9 }), with(func(c *Copy) { c.BitDepth = 24 })},
		{"bit depth before length", with(func(c *Copy) { c.BitDepth, c.IndexOrder = 24, 9 }), with(func(c *Copy) { c.Length = 203 * time.Second })},
		{"bitrate before length", with(func(c *Copy) { c.Bitrate, c.IndexOrder = 321, 9 }), with(func(c *Copy) { c.Length = 203 * time.Second })},
		{"sample rate before length", with(func(c *Copy) { c.SampleRate, c.IndexOrder = 48000, 9 }), with(func(c *Copy) { c.Length = 203 * time.Second })},
		{"tags before index order", with(func(c *Copy) { c.Art, c.IndexOrder = true, 9 }), base},
		{"2 s or less is no longer", with(func(c *Copy) { c.Length, c.InLibrary, c.IndexOrder = 198*time.Second, true, 9 }), base},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if !BetterCopy(tt.better, tt.worse) {
				t.Errorf("BetterCopy(%+v, %+v) = false, want true", tt.better, tt.worse)
			}
			if BetterCopy(tt.worse, tt.better) {
				t.Errorf("BetterCopy(%+v, %+v) = true, want false", tt.worse, tt.better)
			}
		})
	}
}

func TestSuggestedKeeper(t *testing.T) {
	copies := []Copy{
		{IndexOrder: 5},
		{IndexOrder: 3, MusicBrainzIDs: 2},
		{IndexOrder: 1},
		{IndexOrder: 4, MusicBrainzIDs: 2},
	}
	if got := SuggestedKeeper(copies); got != 1 {
		t.Errorf("SuggestedKeeper = %d, want 1", got)
	}
	if got := SuggestedKeeper(nil); got != -1 {
		t.Errorf("SuggestedKeeper(nil) = %d, want -1", got)
	}
}
