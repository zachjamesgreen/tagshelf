package coverart

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"go.senan.xyz/taglib"
)

// FileName is the name, without its extension, of the cover file tagshelf
// writes into an album's folder.
const FileName = "cover"

// coverNames are the names, without extension, an image file must have to
// count as a folder's cover, in the order they are looked for.
var coverNames = []string{FileName, "folder", "front"}

// pictureExts are the extensions of the image files that count.
var pictureExts = map[string]bool{".jpg": true, ".jpeg": true, ".png": true}

// InFolder finds the cover among the image files of the folder dir: the one
// named cover, or else folder, or else front, whatever the case of its name,
// with the extension of a JPEG or a PNG. ok is false when the folder has
// none.
func InFolder(dir string) (path string, ok bool, err error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", false, err
	}
	best := len(coverNames)
	for _, e := range entries {
		name := e.Name()
		ext := strings.ToLower(filepath.Ext(name))
		if e.IsDir() || !pictureExts[ext] {
			continue
		}
		base := strings.ToLower(strings.TrimSuffix(name, filepath.Ext(name)))
		for i, want := range coverNames[:best] {
			if base == want {
				path, ok, best = filepath.Join(dir, name), true, i
			}
		}
	}
	return path, ok, nil
}

// ReadFile reads the image file at path as a picture.
func ReadFile(path string) (Picture, error) {
	file, err := os.Open(path)
	if err != nil {
		return Picture{}, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxPicture+1))
	if err != nil {
		return Picture{}, err
	}
	if len(data) > maxPicture {
		return Picture{}, fmt.Errorf("it is larger than %d MB", maxPicture>>20)
	}
	return Describe(data)
}

// readImage is go-taglib's reader of the first picture a file holds. Tests
// put one in its place.
var readImage = taglib.ReadImage

// ErrNoPicture is returned for an audio file that holds no picture.
var ErrNoPicture = errors.New("it holds no picture")

// Embedded reads the first picture the audio file at path holds.
func Embedded(path string) (Picture, error) {
	data, err := readImage(path)
	if err != nil {
		return Picture{}, err
	}
	if len(data) == 0 {
		return Picture{}, ErrNoPicture
	}
	if len(data) > maxPicture {
		return Picture{}, fmt.Errorf("its picture is larger than %d MB", maxPicture>>20)
	}
	return Describe(data)
}
