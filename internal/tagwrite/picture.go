package tagwrite

import (
	"bytes"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"go.senan.xyz/taglib"

	"github.com/zachjamesgreen/tagshelf/internal/marker"
)

// A picture is put into a file as its tags are: into a copy, which must read
// back with the picture and with everything else as it was before it takes
// the original's place.

// readImage and writeImage are go-taglib's, for the first picture a file
// holds. Tests put others in their place, to see the checks refuse a copy
// written wrong.
var (
	readImage  = taglib.ReadImage
	writeImage = taglib.WriteImage
)

// Picture returns the first picture the audio file at path holds, as it is,
// and nil when it holds none.
func Picture(path string) ([]byte, error) {
	if _, ok := formats[strings.ToLower(filepath.Ext(path))]; !ok {
		return nil, ErrUnsupported
	}
	pic, err := readImage(path)
	if err != nil {
		return nil, fmt.Errorf("reading the picture of %q: %w", path, err)
	}
	if len(pic) == 0 {
		return nil, nil
	}
	return pic, nil
}

// SavePicture makes picture the front cover the file at path holds, in the
// place of the first picture it has, or with nil takes that picture out. The
// tags, the manual marker among them, and the audio are left as they are,
// and a file that already holds the picture is left alone.
//
// record is how the save is put on record: it is given the picture the file
// held before, nil for none, and must call apply, which puts the written
// copy in the original's place, keeping its record only if apply succeeds.
// A nil record puts nothing on record.
func SavePicture(path string, picture []byte, record func(before []byte, apply func() error) error) (changed bool, err error) {
	f, ok := formats[strings.ToLower(filepath.Ext(path))]
	if !ok {
		return false, ErrUnsupported
	}
	fail := func(err error) (bool, error) {
		return false, fmt.Errorf("putting a picture into %q: %w", path, err)
	}
	if err := checkContent(f, path); err != nil {
		return fail(err)
	}
	before, err := readImage(path)
	if err != nil {
		return fail(err)
	}
	if bytes.Equal(before, picture) {
		return false, nil
	}
	tags, err := taglib.ReadTags(path)
	if err != nil {
		return fail(err)
	}
	orig, err := os.Stat(path)
	if err != nil {
		return fail(err)
	}
	tmp, err := copyTemp(path)
	if err != nil {
		return fail(err)
	}
	keep := false
	defer func() {
		if !keep {
			os.Remove(tmp)
		}
	}()
	if err := writeImage(tmp, picture); err != nil {
		return fail(err)
	}
	if f.id3v1 {
		if err := dropNewID3v1(path, tmp); err != nil {
			return fail(err)
		}
	}
	if f.asf {
		// TagLib writes every list of a WMA file back turned by one (see
		// asfBefore): the lists are given back to it in the order they had.
		lists := map[string][]string{}
		keepASFOrder(path, lists)
		if len(lists) > 0 {
			if err := writeTags(tmp, lists, 0); err != nil {
				return fail(err)
			}
		}
	}
	if err := verifyPicture(f, path, tmp, picture, tags); err != nil {
		return fail(err)
	}
	if err := sameAudio(path, tmp, f.asf); err != nil {
		return fail(err)
	}
	if err := os.Chmod(tmp, orig.Mode().Perm()); err != nil {
		return fail(err)
	}
	rename := func() error {
		if now, err := os.Stat(path); err != nil {
			return err
		} else if now.Size() != orig.Size() || !now.ModTime().Equal(orig.ModTime()) {
			return errChangedMeanwhile
		}
		return replace(tmp, path, orig.Mode().Perm())
	}
	if record == nil {
		err = rename()
	} else {
		if len(before) == 0 {
			before = nil
		}
		err = record(before, rename)
	}
	if err != nil {
		return fail(err)
	}
	keep = true
	return true, nil
}

// verifyPicture reads the written copy at path back: it must hold the
// picture written, or none, every tag the original at orig had, each as it
// was, the same marker, and still be a file of its format to the reader
// tagshelf sorts by.
func verifyPicture(f format, orig, path string, picture []byte, tags map[string][]string) error {
	got, err := readImage(path)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrNotVerified, err)
	}
	if !bytes.Equal(got, picture) {
		return fmt.Errorf("%w: the picture reads back as %d bytes, want %d", ErrNotVerified, len(got), len(picture))
	}
	now, err := taglib.ReadTags(path)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrNotVerified, err)
	}
	if !maps.EqualFunc(now, tags, slices.Equal[[]string]) {
		return fmt.Errorf("%w: the tags changed with the picture", ErrNotVerified)
	}
	md, err := readTags(path)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrNotVerified, err)
	}
	if !f.is(md) {
		return fmt.Errorf("%w: it reads as %s", ErrNotVerified, describe(md))
	}
	// The marker, which go-taglib does not return for every format, by the
	// reader that does.
	was := ""
	if before, err := readTags(orig); err == nil {
		if m, ok := marker.Read(before); ok {
			was = m.String()
		}
	}
	var want []string
	if was != "" {
		want = []string{was}
	}
	return checkMarker(md, want, "the marker")
}
