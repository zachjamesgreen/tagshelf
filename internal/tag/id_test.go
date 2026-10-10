package tag

import (
	"bytes"
	"io"
	"os"
	"slices"
	"testing"
)

// TestIdentify checks the format and file type Identify reports for the
// formats it knows, and that it leaves the reader where it was: for a file
// with only an ID3v1 tag it used to leave it 3 bytes in.
func TestIdentify(t *testing.T) {
	sample := func(name string) []byte {
		b, err := os.ReadFile("testdata/" + name)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	brand := func(brand string) []byte {
		b := mp4File()
		copy(b[8:12], brand)
		return b
	}
	for name, tt := range map[string]struct {
		b        []byte
		format   Format
		fileType FileType
	}{
		"FLAC":    {sample("with_tags/sample.flac"), VORBIS, FLAC},
		"OGG":     {sample("with_tags/sample.ogg"), VORBIS, OGG},
		"M4A":     {sample("with_tags/sample.m4a"), MP4, M4A},
		"M4B":     {brand("M4B "), MP4, M4B},
		"M4P":     {brand("M4P "), MP4, M4P},
		"mp42":    {sample("without_tags/sample.mp4"), MP4, UnknownFileType},
		"ID3v2.2": {sample("with_tags/sample.id3v22.mp3"), ID3v2_2, MP3},
		"ID3v2.3": {sample("with_tags/sample.id3v23.mp3"), ID3v2_3, MP3},
		"ID3v2.4": {sample("with_tags/sample.id3v24.mp3"), ID3v2_4, MP3},
		"ID3v1":   {sample("with_tags/sample.id3v11.mp3"), ID3v1, MP3},
		"WMA":     {fixtureBytes(t, "tagged.wma"), ASF, WMA},
	} {
		// From an offset, as when the file is part of a larger reader.
		for _, start := range []int64{0, 5} {
			r := bytes.NewReader(slices.Concat(make([]byte, start), tt.b))
			r.Seek(start, io.SeekStart)
			format, fileType, err := Identify(r)
			if err != nil || format != tt.format || fileType != tt.fileType {
				t.Errorf("%s: Identify = %q, %q, %v, want %q, %q", name, format, fileType, err, tt.format, tt.fileType)
			}
			if pos, _ := r.Seek(0, io.SeekCurrent); pos != start {
				t.Errorf("%s: Identify left the reader at %d, want %d", name, pos, start)
			}
		}
	}

	for name, b := range map[string][]byte{
		"untagged MP3":       fixtureBytes(t, "untagged.mp3"),
		"shorter than a tag": mp3Audio[:64],
	} {
		r := bytes.NewReader(b)
		if _, _, err := Identify(r); err != ErrNoTagsFound {
			t.Errorf("%s: Identify = %v, want ErrNoTagsFound", name, err)
		}
		if pos, _ := r.Seek(0, io.SeekCurrent); pos != 0 {
			t.Errorf("%s: Identify left the reader at %d, want 0", name, pos)
		}
	}
	if _, _, err := Identify(bytes.NewReader([]byte("ID3\x05\x00\x00\x00\x00\x00\x00\x00"))); err == nil {
		t.Error("ID3v2.5: no error")
	}
	if _, _, err := Identify(bytes.NewReader([]byte("short"))); err == nil {
		t.Error("5 bytes: no error")
	}
}
