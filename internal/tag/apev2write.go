package tag

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"slices"
	"strings"
	"unicode/utf8"
)

const (
	apeVersion     = 2000
	apeFlagsHeader = apeFlagHasHeader | apeFlagIsHeader
	apeFlagsFooter = apeFlagHasHeader
)

// WriteAPEv2Tags sets text items of the APEv2 tag of f, an APE, MPC, .aac or
// MP3 file opened for reading and writing, in place. The new tag replaces
// the old one where it was, or else goes at the end of the file before any
// ID3v1 tag. It always has a header and a footer. A leading ID3v2 tag, the
// audio and any ID3v1 tags are left as they are.
//
// set maps keys to values; several values are stored separated by NUL. Keys
// match the existing items case-insensitively, and each item set is written
// under the key's case in set. A key with no values removes its item. Items
// not in set, binary ones such as cover art included, are kept in their
// order; new items follow in key order. If no items are left, the tag is
// removed.
func WriteAPEv2Tags(f *os.File, set map[string][]string) error {
	if err := checkAPEv2Set(set); err != nil {
		return err
	}
	audioStart, err := skipID3v2(f, 0)
	if err != nil {
		return err
	}
	end, err := f.Seek(0, io.SeekEnd)
	if err != nil {
		return fmt.Errorf("error seeking to end of file: %v", err)
	}
	if audioStart > end {
		return fmt.Errorf("ID3v2 tag runs %d bytes past the end of the file", audioStart-end)
	}
	t, err := findTrailingTags(f, audioStart, end)
	if err != nil {
		return err
	}

	// The old tag, or the place for a new one, is [tagStart, tagEnd).
	tagStart, tagEnd := t.audioEnd, t.audioEnd
	var items []apev2Item
	if t.ape {
		tagStart, tagEnd = t.apeStart, t.apeItems+t.apeSize
		if tagStart < t.apeItems {
			h, err := readAt(f, tagStart, apeBlockSize)
			if err != nil {
				return err
			}
			if len(h) < apeBlockSize || string(h[:8]) != apePreamble || binary.LittleEndian.Uint32(h[20:24])&apeFlagIsHeader == 0 {
				return errors.New("APEv2 footer announces a header that is not there")
			}
		}
		b, err := readAt(f, t.apeItems, int(t.apeSize-apeBlockSize))
		if err != nil {
			return err
		}
		if items, err = parseAPEv2Items(b, t.apeCount); err != nil {
			return err
		}
	}

	tag, err := renderAPEv2(updateAPEv2Items(items, set))
	if err != nil {
		return err
	}
	tail, err := readAt(f, tagEnd, int(end-tagEnd))
	if err != nil {
		return err
	}
	if int64(len(tail)) != end-tagEnd {
		return errors.New("file shorter than its tags")
	}
	if _, err := f.WriteAt(slices.Concat(tag, tail), tagStart); err != nil {
		return err
	}
	return f.Truncate(tagStart + int64(len(tag)) + int64(len(tail)))
}

// checkAPEv2Set checks keys and values before anything is written. Keys are
// 2 to 255 printable ASCII characters and not ID3, TAG, OggS or MP+, which
// readers take for other tags; values are UTF-8 without NUL.
func checkAPEv2Set(set map[string][]string) error {
	seen := map[string]bool{}
	for key, values := range set {
		lower := strings.ToLower(key)
		if len(key) < 2 || len(key) > 255 {
			return fmt.Errorf("APEv2 key %q is not 2 to 255 characters", key)
		}
		for i := 0; i < len(key); i++ {
			if key[i] < 0x20 || key[i] > 0x7e {
				return fmt.Errorf("APEv2 key %q is not printable ASCII", key)
			}
		}
		switch lower {
		case "id3", "tag", "oggs", "mp+":
			return fmt.Errorf("APEv2 key %q is reserved", key)
		}
		if seen[lower] {
			return fmt.Errorf("APEv2 key %q is given twice", key)
		}
		seen[lower] = true
		for _, v := range values {
			if !utf8.ValidString(v) || strings.IndexByte(v, 0) >= 0 {
				return fmt.Errorf("APEv2 value %q of %q is not UTF-8 text without NUL", v, key)
			}
		}
	}
	return nil
}

// updateAPEv2Items applies set to items: see WriteAPEv2Tags.
func updateAPEv2Items(items []apev2Item, set map[string][]string) []apev2Item {
	byLower := map[string]string{} // lower-cased key to the key in set
	for k := range set {
		byLower[strings.ToLower(k)] = k
	}
	text := func(key string) apev2Item {
		return apev2Item{key: key, value: []byte(strings.Join(set[key], "\x00"))}
	}

	var out []apev2Item
	done := map[string]bool{} // lower-cased keys already in out
	for _, it := range items {
		lower := strings.ToLower(it.key)
		if done[lower] {
			continue // a second item with the same key, which the spec forbids
		}
		done[lower] = true
		if k, ok := byLower[lower]; ok {
			if len(set[k]) > 0 {
				out = append(out, text(k))
			}
			continue
		}
		out = append(out, it)
	}
	for _, k := range slices.Sorted(maps.Keys(set)) {
		if !done[strings.ToLower(k)] && len(set[k]) > 0 {
			out = append(out, text(k))
		}
	}
	return out
}

// renderAPEv2 renders a tag with header and footer, or nothing for no items.
func renderAPEv2(items []apev2Item) ([]byte, error) {
	if len(items) == 0 {
		return nil, nil
	}
	var body []byte
	for _, it := range items {
		body = binary.LittleEndian.AppendUint32(body, uint32(len(it.value)))
		body = binary.LittleEndian.AppendUint32(body, it.flags)
		body = append(append(append(body, it.key...), 0), it.value...)
	}
	size := len(body) + apeBlockSize // items and footer
	if size > 1<<31 {
		return nil, errors.New("APEv2 tag too large")
	}
	block := func(flags uint32) []byte {
		b := []byte(apePreamble)
		b = binary.LittleEndian.AppendUint32(b, apeVersion)
		b = binary.LittleEndian.AppendUint32(b, uint32(size))
		b = binary.LittleEndian.AppendUint32(b, uint32(len(items)))
		b = binary.LittleEndian.AppendUint32(b, flags)
		return append(b, make([]byte, 8)...)
	}
	return slices.Concat(block(apeFlagsHeader), body, block(apeFlagsFooter)), nil
}
