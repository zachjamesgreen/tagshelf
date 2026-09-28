// Package fixtures locates the shared audio test fixtures in testdata/audio at
// the repository root, and holds the tag values the tagged ones carry.
//
// Every supported format has an untagged.<ext> and a tagged.<ext> fixture:
// about a second of silence made by ffmpeg, or for APE and MPC, which ffmpeg
// cannot encode, a TagLib test file. Regenerate them with `go generate
// ./internal/fixtures`, which needs ffmpeg on PATH; tests never do. See
// testdata/audio/README.md for where each file comes from.
package fixtures

//go:generate go run gen.go

import (
	"os"
	"path/filepath"
)

// Extensions lists the formats that have an untagged.<ext> and a
// tagged.<ext> fixture. Each call returns a new slice.
func Extensions() []string {
	return []string{".mp3", ".flac", ".ogg", ".m4a", ".m4b", ".wav", ".wma", ".aac", ".ape", ".mpc"}
}

// Extra fixtures beyond the untagged/tagged pair.
const (
	// UntaggedOddWAV has an odd-sized data chunk followed by its pad byte.
	UntaggedOddWAV = "untagged-odd.wav"
	// UntaggedSV8MPC is a Musepack SV8 header (the other MPC fixtures are SV7).
	UntaggedSV8MPC = "untagged-sv8.mpc"
	// TaggedID3v2AAC is the ADTS file with a leading ID3v2 tag, as go-taglib
	// writes it, instead of the APEv2 tag Picard appends.
	TaggedID3v2AAC = "tagged-id3v2.aac"
	// TaggedVAM4A is a Various Artists compilation as Picard tags one, but
	// with no album artist name: only the album artist ID marks it.
	TaggedVAM4A = "tagged-va.m4a"
)

// The tag values every tagged fixture carries, written under the key names
// MusicBrainz Picard uses for its format. The WMA fixture has no track total,
// because Picard writes none for WMA.
const (
	Title       = "Silence Café"
	Artist      = "Fixture Artist"
	AlbumArtist = "Fixture Album Artist"
	Album       = "Fixture Album"
	Year        = 2001
	Track       = 3
	TrackTotal  = 10
	Disc        = 1
	DiscTotal   = 2

	ArtistID       = "11111111-1111-4111-8111-111111111111"
	AlbumArtistID  = "22222222-2222-4222-8222-222222222222"
	ReleaseID      = "33333333-3333-4333-8333-333333333333"
	ReleaseGroupID = "44444444-4444-4444-8444-444444444444"
	RecordingID    = "55555555-5555-4555-8555-555555555555"
	ReleaseTrackID = "66666666-6666-4666-8666-666666666666"
)

// Dir returns the absolute path of testdata/audio. It looks up from the
// working directory, which go test and go generate set to the package being
// run, for the module root.
func Dir() string {
	dir, err := os.Getwd()
	if err != nil {
		panic("fixtures: " + err.Error())
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return filepath.Join(dir, "testdata", "audio")
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			panic("fixtures: no go.mod above the working directory")
		}
		dir = parent
	}
}

// Path returns the absolute path of the named fixture, such as "tagged.flac".
func Path(name string) string {
	return filepath.Join(Dir(), name)
}

// Untagged returns the path of the untagged fixture for ext, such as ".flac".
func Untagged(ext string) string {
	return Path("untagged" + ext)
}

// Tagged returns the path of the tagged fixture for ext, such as ".flac".
func Tagged(ext string) string {
	return Path("tagged" + ext)
}
