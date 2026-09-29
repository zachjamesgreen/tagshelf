package rules

import "time"

// Copy is what the suggested-keeper rule knows of one file in a duplicate
// group. Properties that could not be read are zero.
type Copy struct {
	Lossless   bool
	Bitrate    int // kbit/s
	SampleRate int // Hz
	BitDepth   int
	Length     time.Duration
	// InLibrary is set for a file in the library proper, as opposed to the
	// source or the untagged and other folders.
	InLibrary bool
	// MusicBrainzIDs counts the MusicBrainz IDs the tags carry.
	MusicBrainzIDs int
	Manual         bool // carries the manual marker
	Art            bool // carries embedded art
	// IndexOrder is the order the files were first indexed in, lowest first.
	IndexOrder int64
}

// lengthMargin is how much longer a copy must be for its length to count: a
// truncated rip is short by more than rounding.
const lengthMargin = 2 * time.Second

// BetterCopy reports whether a is the better copy to keep of two duplicates:
// lossless over lossy; higher bitrate, then sample rate and bit depth; longer
// by more than 2 s; in the library over anywhere else; more complete tags
// (MusicBrainz IDs, manual marker, art); indexed first.
func BetterCopy(a, b Copy) bool {
	if a.Lossless != b.Lossless {
		return a.Lossless
	}
	if a.Bitrate != b.Bitrate {
		return a.Bitrate > b.Bitrate
	}
	if a.SampleRate != b.SampleRate {
		return a.SampleRate > b.SampleRate
	}
	if a.BitDepth != b.BitDepth {
		return a.BitDepth > b.BitDepth
	}
	if d := a.Length - b.Length; d > lengthMargin || d < -lengthMargin {
		return d > 0
	}
	if a.InLibrary != b.InLibrary {
		return a.InLibrary
	}
	if sa, sb := a.tagScore(), b.tagScore(); sa != sb {
		return sa > sb
	}
	return a.IndexOrder < b.IndexOrder
}

// tagScore is how complete a copy's tags are.
func (c Copy) tagScore() int {
	score := c.MusicBrainzIDs
	if c.Manual {
		score++
	}
	if c.Art {
		score++
	}
	return score
}

// SuggestedKeeper returns the index of the copy BetterCopy prefers over all
// the others, or -1 when there are none.
func SuggestedKeeper(copies []Copy) int {
	best := -1
	for i, c := range copies {
		if best < 0 || BetterCopy(c, copies[best]) {
			best = i
		}
	}
	return best
}
