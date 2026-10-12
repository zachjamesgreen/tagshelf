package tag

import "strings"

// A tag may hold several genres or composers. Metadata's Genre and Composer
// return the first; Genres and Composers return them all, from the map Raw
// returns, in which every reader of this package keeps the values of a list
// in one string with a NUL between them.
//
// They take the map, not the Metadata, so that they also read a map that was
// kept as JSON, where a byte that is not UTF-8 has become U+FFFD.

// The names a list is kept under, by format, in the order they are tried.
// The last one is compared without regard to case: Vorbis comments and APEv2
// items are named as their writer spelled them.
var (
	genreKeys    = []string{"TCON", "TCO", "\xa9gen", "�gen", "WM/Genre", "IGNR"}
	composerKeys = []string{"TCOM", "TCM", "\xa9wrt", "�wrt", "WM/Composer", "IMUS"}
)

// Genres lists the genres in raw, the map a Metadata's Raw returns. The
// numbered genres of ID3 are given by name.
func Genres(raw map[string]interface{}) []string {
	key, values := list(raw, genreKeys, "genre")
	if key == "TCON" || key == "TCO" {
		for i, v := range values {
			values[i] = id3v2genre(v)
		}
	}
	return values
}

// Composers lists the composers in raw, the map a Metadata's Raw returns.
func Composers(raw map[string]interface{}) []string {
	_, values := list(raw, composerKeys, "composer")
	return values
}

// list returns the values of the first of keys that raw has any for, or else
// of the entry named name whatever its case, with that key.
func list(raw map[string]interface{}, keys []string, name string) (string, []string) {
	for _, k := range keys {
		if values := split(raw[k]); len(values) > 0 {
			return k, values
		}
	}
	for k, v := range raw {
		if strings.EqualFold(k, name) {
			if values := split(v); len(values) > 0 {
				return k, values
			}
		}
	}
	return "", nil
}

// split gives the values of a list kept in one string, without the empty
// ones. A value that is not a string has none.
func split(v interface{}) []string {
	s, ok := v.(string)
	if !ok {
		return nil
	}
	var values []string
	for _, part := range strings.Split(s, "\x00") {
		if part = strings.TrimSpace(part); part != "" {
			values = append(values, part)
		}
	}
	return values
}
