package tag

import (
	"slices"
	"strings"
	"testing"
)

// TestSkipped reads tags with one element the reader cannot decode, in every
// format that leaves such an element out: the rest of the tag is read, and
// Skipped names what was left out. A tag read in full has nothing skipped.
func TestSkipped(t *testing.T) {
	title := id3Frame(3, "TIT2", latin1("Title"))
	comment := func(extra ...string) []byte {
		return vorbisComment("v", append(slices.Clone(vorbisBasics), extra...)...)
	}
	info := infoList("INAM", "Info Title")
	for name, tc := range map[string]struct {
		file []byte
		want string // part of the error, "" for none
	}{
		"ID3v2, whole":             {slices.Concat(id3Tag(3, 0, title), mp3Audio), ""},
		"ID3v2, UFID with no NUL":  {slices.Concat(id3Tag(3, 0, title, id3Frame(3, "UFID", []byte("no terminator"))), mp3Audio), "UFID"},
		"ID3v2, compressed frame":  {slices.Concat(id3Tag(3, 0, title, id3FrameFlags(3, "TALB", 0x80, latin1("Album"))), mp3Audio), "TALB"},
		"MP4, whole":               {mp4File(mp4Basics()...), ""},
		"MP4, empty cover":         {mp4File(append(mp4Basics(), atom("covr", dataAtom(0, nil)))...), "covr"},
		"FLAC, whole":              {flacFile(flacBlock(4, true, comment())), ""},
		"FLAC, comment with no =":  {flacFile(flacBlock(4, true, comment("no equals sign"))), `"="`},
		"FLAC, picture cut short":  {flacFile(flacBlock(6, false, []byte{0, 0, 0, 3, 0, 0}), flacBlock(4, true, comment())), "PICTURE"},
		"OGG, picture not base64":  {oggVorbisFile(comment("METADATA_BLOCK_PICTURE=not base64!"), []byte("audio")), "METADATA_BLOCK_PICTURE"},
		"WAV, unreadable ID3":      {wavFile(wavFmt, info, wavChunk("data", []byte{0, 0}), wavChunk("id3 ", []byte("ID3\x09\x00\x00\x00\x00\x00\x00"))), "id3 "},
		"WAV, ID3 with a bad UFID": {wavFile(wavFmt, wavChunk("data", []byte{0, 0}), wavChunk("id3 ", id3Tag(3, 0, title, id3Frame(3, "UFID", []byte("no terminator"))))), "UFID"},
	} {
		m := readFromBytes(t, name, tc.file)
		if m.Title() == "" {
			t.Errorf("%s: no title read, want the rest of the tag", name)
		}
		err := Skipped(m)
		switch {
		case tc.want == "" && err != nil:
			t.Errorf("%s: Skipped = %v, want nil", name, err)
		case tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)):
			t.Errorf("%s: Skipped = %v, want an error naming %s", name, err, tc.want)
		}
	}
	if err := Skipped(nil); err != nil {
		t.Errorf("Skipped(nil) = %v", err)
	}
}

// TestVorbisTotalsUnderEitherName reads the totals from TOTALTRACKS and
// TOTALDISCS when TRACKTOTAL and DISCTOTAL are not there, and prefers the
// latter when both are.
func TestVorbisTotalsUnderEitherName(t *testing.T) {
	for name, tc := range map[string]struct {
		comments      []string
		tracks, discs int
	}{
		"TOTALTRACKS and TOTALDISCS alone": {[]string{"TRACKNUMBER=3", "TOTALTRACKS=12", "DISCNUMBER=1", "TOTALDISCS=2"}, 12, 2},
		"both names":                       {[]string{"TRACKNUMBER=3", "TRACKTOTAL=10", "TOTALTRACKS=12", "DISCNUMBER=1", "DISCTOTAL=4", "TOTALDISCS=2"}, 10, 4},
		"over the total of N/M":            {[]string{"TRACKNUMBER=3/9", "TOTALTRACKS=12", "DISCNUMBER=1/9", "TOTALDISCS=2"}, 12, 2},
		"a total that is not a number":     {[]string{"TRACKNUMBER=3", "TRACKTOTAL=many", "TOTALTRACKS=12", "DISCNUMBER=1", "TOTALDISCS=2"}, 12, 2},
	} {
		for format, file := range map[string][]byte{
			"FLAC": flacFile(flacBlock(4, true, vorbisComment("v", append(slices.Clone(vorbisBasics), tc.comments...)...))),
			"OGG":  oggVorbisFile(vorbisComment("v", append(slices.Clone(vorbisBasics), tc.comments...)...), []byte("audio")),
		} {
			m := readFromBytes(t, name, file)
			if x, n := m.Track(); x != 3 || n != tc.tracks {
				t.Errorf("%s, %s: Track() = %d, %d, want 3, %d", format, name, x, n, tc.tracks)
			}
			if x, n := m.Disc(); x != 1 || n != tc.discs {
				t.Errorf("%s, %s: Disc() = %d, %d, want 1, %d", format, name, x, n, tc.discs)
			}
		}
	}
}
