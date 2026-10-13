package tagwrite

import (
	"bytes"
	"errors"
	"image"
	"image/jpeg"
	"image/png"
	"reflect"
	"testing"

	"github.com/zachjamesgreen/tagshelf/internal/marker"
)

func testPicture(t *testing.T, format string, size int) []byte {
	t.Helper()
	var buf bytes.Buffer
	img := image.NewGray(image.Rect(0, 0, size, size))
	var err error
	if format == "png" {
		err = png.Encode(&buf, img)
	} else {
		err = jpeg.Encode(&buf, img, nil)
	}
	if err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// TestSavePicture puts a picture into a manually tagged file of every
// format, puts another in its place and takes it out again: each time the
// picture reads back as written, and the tags, the marker and the audio are
// as they were. A file of MPEG audio gains no ID3v1 tag by it.
func TestSavePicture(t *testing.T) {
	first, second := testPicture(t, "jpeg", 60), testPicture(t, "png", 40)
	for ext, from := range taggableExtensions {
		t.Run(ext, func(t *testing.T) {
			path := copyFixture(t, tagged(t, from), "song"+ext)
			if _, err := (Writer{}).Save(path, Values{Genre: {"Rock", "Jazz"}}, marker.New(marker.Hand, day)); err != nil {
				t.Fatal(err)
			}
			tags, err := ReadAll(path)
			if err != nil {
				t.Fatal(err)
			}
			delete(tags, NotText)
			audio := audioSum(t, path)
			hadV1, _ := endsWithID3v1(path)

			var recorded [][]byte
			record := func(before []byte, apply func() error) error {
				recorded = append(recorded, before)
				return apply()
			}
			for i, pic := range [][]byte{first, second, nil} {
				changed, err := SavePicture(path, pic, record)
				if err != nil || !changed {
					t.Fatalf("picture %d: changed %t, %v", i, changed, err)
				}
				got, err := Picture(path)
				if err != nil || !bytes.Equal(got, pic) {
					t.Errorf("picture %d reads back as %d bytes (%v), want %d", i, len(got), err, len(pic))
				}
				now, err := ReadAll(path)
				if err != nil {
					t.Fatal(err)
				}
				delete(now, NotText)
				if !reflect.DeepEqual(now, tags) {
					t.Errorf("picture %d changed the tags:\n got %v\nwant %v", i, now, tags)
				}
				if audioSum(t, path) != audio {
					t.Errorf("picture %d changed the audio", i)
				}
				if hasV1, _ := endsWithID3v1(path); hasV1 != hadV1 {
					t.Errorf("picture %d: the file ends with an ID3v1 tag: %t, before %t", i, hasV1, hadV1)
				}
			}
			// What each save replaced was on record: none, then the first,
			// then the second.
			if len(recorded) != 3 || recorded[0] != nil || !bytes.Equal(recorded[1], first) || !bytes.Equal(recorded[2], second) {
				t.Errorf("the saves recorded pictures of %d, %d and %d bytes before them", len(recorded[0]), len(recorded[1]), len(recorded[2]))
			}

			// A file that holds the picture already is left alone.
			if _, err := SavePicture(path, first, nil); err != nil {
				t.Fatal(err)
			}
			written := mustRead(t, path)
			calls := len(recorded)
			if changed, err := SavePicture(path, first, record); err != nil || changed || len(recorded) != calls {
				t.Errorf("a second save of the same picture: changed %t, %v", changed, err)
			}
			if !bytes.Equal(mustRead(t, path), written) {
				t.Error("the second save wrote the file")
			}
		})
	}
}

// A copy that reads back with another picture, or a save that cannot be put
// on record, leaves the original as it was.
func TestSavePictureLeavesTheOriginalWhenItGoesWrong(t *testing.T) {
	path := copyFixture(t, tagged(t, ".flac"), "song.flac")
	orig := mustRead(t, path)
	pic := testPicture(t, "jpeg", 30)

	old := writeImage
	writeImage = func(tmp string, _ []byte) error { return old(tmp, testPicture(t, "jpeg", 31)) }
	_, err := SavePicture(path, pic, nil)
	writeImage = old
	if !errors.Is(err, ErrNotVerified) {
		t.Errorf("a copy with another picture: %v, want ErrNotVerified", err)
	}

	full := errors.New("the disk is full")
	if _, err := SavePicture(path, pic, func([]byte, func() error) error { return full }); !errors.Is(err, full) {
		t.Errorf("a save that cannot be recorded: %v", err)
	}
	if !bytes.Equal(mustRead(t, path), orig) {
		t.Error("the original changed")
	}
	if _, err := SavePicture("song.xyz", pic, nil); !errors.Is(err, ErrUnsupported) {
		t.Errorf("a file tagshelf cannot tag: %v", err)
	}
	if _, err := Picture("song.xyz"); !errors.Is(err, ErrUnsupported) {
		t.Errorf("the picture of a file tagshelf cannot tag: %v", err)
	}
}
