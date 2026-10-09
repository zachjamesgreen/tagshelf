package tagwrite

import (
	"fmt"
	"maps"
	"os"
	"slices"
	"strings"

	"tagshelf/internal/marker"
	"tagshelf/internal/tag"
)

// apeKeys are the APEv2 keys of each field: first the one Picard writes,
// then others readers take for the same field, which a save removes so that
// no stale value is left behind. Keys match case-insensitively.
var apeKeys = map[Field][]string{
	Title:          {"Title"},
	Artist:         {"Artist"},
	AlbumArtist:    {"Album Artist", "AlbumArtist"},
	Album:          {"Album"},
	Date:           {"Year", "Date"},
	Track:          {"Track"},
	Disc:           {"Disc"},
	Compilation:    {"Compilation"},
	RecordingID:    {"musicbrainz_trackid", "MUSICBRAINZ_RECORDINGID"}, // Picard's name for the recording
	ReleaseTrackID: {"musicbrainz_releasetrackid"},
	AlbumID:        {"Musicbrainz_Albumid"},
	ReleaseGroupID: {"Musicbrainz_Releasegroupid"},
	ArtistIDs:      {"Musicbrainz_Artistid"},
	AlbumArtistIDs: {"Musicbrainz_Albumartistid"},
	Marker:         {marker.Key},
}

// toAPE turns values into APEv2 items for tag.WriteAPEv2Tags: each field
// under Picard's key, its other keys removed, and a field with no values
// removed. Track and disc are N/M under one key; several values are
// separated by NUL.
func toAPE(v Values) map[string][]string {
	set := map[string][]string{}
	for field, vs := range v {
		keys := apeKeys[field]
		set[keys[0]] = vs
		for _, k := range keys[1:] {
			set[k] = nil
		}
	}
	return set
}

// fromAPE reads the named fields from the raw items of an APEv2 tag, in the
// form normalize gives them. A field stored under more than one of its keys
// reads every value, so a stale second key never matches a single value.
func fromAPE(raw map[string]any, named []Field) Values {
	v := Values{}
	for _, field := range named {
		vs := []string{}
		for _, k := range slices.Sorted(maps.Keys(raw)) {
			s, ok := raw[k].(string)
			if !ok || !slices.ContainsFunc(apeKeys[field], func(key string) bool { return strings.EqualFold(key, k) }) {
				continue
			}
			vs = append(vs, clean(strings.Split(s, "\x00"))...)
		}
		if (field == Track || field == Disc) && len(vs) == 1 {
			if norm, err := normalizeXofN(vs[0]); err == nil {
				vs[0] = norm
			}
		}
		v[field] = vs
	}
	return v
}

// readAPE reads the APEv2 tag of the file at path.
func readAPE(path string) (tag.Metadata, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	return tag.ReadAPEv2Tags(file)
}

// writeAPE writes values, the marker among them, to the APEv2 tag of the
// file at path, replacing the fields' items and keeping every other item.
func writeAPE(path string, v Values) (err error) {
	file, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return err
	}
	defer func() {
		if cerr := file.Close(); err == nil {
			err = cerr
		}
	}()
	if err := tag.WriteAPEv2Tags(file, toAPE(v)); err != nil {
		return fmt.Errorf("writing the APEv2 tag: %w", err)
	}
	return file.Sync()
}

// apeUpToDate reports whether the APEv2 tag of the file at path has a marker
// and every value of want but the marker.
func apeUpToDate(path string, want Values) bool {
	md, err := readAPE(path)
	if err != nil {
		return false
	}
	if _, ok := marker.Read(md); !ok {
		return false
	}
	return equalExcept(fromAPE(md.Raw(), slices.Collect(maps.Keys(want))), want, Marker)
}

// verifyAPE reads the APEv2 tag of the written copy back: it must hold
// exactly the values written and the marker m, and internal/tag must read
// the fields from it as it does from the ID3v2 tag.
func verifyAPE(path string, want Values) error {
	md, err := readAPE(path)
	if err != nil {
		return fmt.Errorf("%w: the APEv2 tag: %v", ErrNotVerified, err)
	}
	if md.FileType() != tag.AAC {
		return fmt.Errorf("%w: the APEv2 tag is on %s audio", ErrNotVerified, describe(md))
	}
	if err := checkMarker(md, want[Marker], "the APEv2 marker"); err != nil {
		return err
	}
	got := fromAPE(md.Raw(), slices.Collect(maps.Keys(want)))
	for field, vs := range want {
		if !slices.Equal(got[field], vs) {
			return fmt.Errorf("%w: APEv2 %s is %q, want %q", ErrNotVerified, field, got[field], vs)
		}
	}
	if err := verifyReader(md, want); err != nil {
		return fmt.Errorf("the APEv2 tag: %w", err)
	}
	return nil
}
