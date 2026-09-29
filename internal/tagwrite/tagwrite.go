// Package tagwrite saves tags to audio files safely. Every save copies the
// file, writes the tags to the copy with go-taglib, reads the copy back to
// check the manual marker and every written field, and only then renames it
// over the original, so a save that goes wrong leaves the original as it was.
// Each save writes the manual marker, is skipped when it would change
// nothing, and is recorded in the tag history with the written fields'
// values before and after.
//
// MP3, FLAC, OGG, the MP4 family (.m4a, .m4b, .m4r, .m4p), WAV, APE, MPC,
// WMA and .aac can be tagged. An .aac file is tagged on both sides: go-taglib
// writes ID3v2, internal/tag the APEv2 tag Picard reads, and both must read
// back. go-taglib drops the marker from WMA, so internal/tag's ASF writer adds
// it, and a WMA save checks that the audio's tag.Sum is unchanged. TagLib has
// no ASF key for the compilation flag, so WMA saves leave it out.
package tagwrite

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"go.senan.xyz/taglib"

	"tagshelf/internal/marker"
	"tagshelf/internal/tag"
	"tagshelf/internal/tag/mbz"
)

// ErrUnsupported is returned for a file whose format tagshelf cannot tag.
var ErrUnsupported = errors.New("tagging not supported for this format")

// ErrNotVerified is returned when a written copy does not read back with the
// marker and the values written; the original is left untouched.
var ErrNotVerified = errors.New("tags did not read back as written")

// Field is a tag tagshelf writes, named the same in every format.
type Field string

// The fields a save can write. Every field holds a single value except the
// two lists of artist IDs.
const (
	Title          Field = "title"
	Artist         Field = "artist"
	AlbumArtist    Field = "albumartist"
	Album          Field = "album"
	Date           Field = "date"        // YYYY or YYYY-MM-DD
	Track          Field = "track"       // N or N/M, M being the track total
	Disc           Field = "disc"        // N or N/M, M being the disc total
	Compilation    Field = "compilation" // "1" for a compilation
	RecordingID    Field = "musicbrainz_recordingid"
	ReleaseTrackID Field = "musicbrainz_releasetrackid"
	AlbumID        Field = "musicbrainz_albumid" // the release
	ReleaseGroupID Field = "musicbrainz_releasegroupid"
	ArtistIDs      Field = "musicbrainz_artistid"
	AlbumArtistIDs Field = "musicbrainz_albumartistid"

	// Marker is the manual marker. Save writes it from its marker argument;
	// it appears only in a Result's Before and After.
	Marker Field = "marker"
)

// multiValued are the fields that may hold more than one value.
var multiValued = map[Field]bool{ArtistIDs: true, AlbumArtistIDs: true}

// simpleKeys are the go-taglib property names of the fields that map to one
// key in every format.
var simpleKeys = map[Field]string{
	Title:          taglib.Title,
	Artist:         taglib.Artist,
	AlbumArtist:    taglib.AlbumArtist,
	Album:          taglib.Album,
	Date:           taglib.Date,
	Compilation:    taglib.Compilation,
	RecordingID:    taglib.MusicBrainzTrackID, // Picard's name for the recording
	ReleaseTrackID: taglib.MusicBrainzReleaseTrackID,
	AlbumID:        taglib.MusicBrainzAlbumID,
	ReleaseGroupID: taglib.MusicBrainzReleaseGroupID,
	ArtistIDs:      taglib.MusicBrainzArtistID,
	AlbumArtistIDs: taglib.MusicBrainzAlbumArtistID,
	Marker:         marker.Key,
}

// Values maps fields to their values. A field present with no values (or
// only empty ones) is cleared; a field absent is left alone.
type Values map[Field][]string

// Result is what a save did. Before and After hold the written fields and
// the marker, as they were and as they are now; with Changed false the file
// already had them and was not written.
type Result struct {
	Changed       bool
	Before, After Values
}

// History records tag saves. *store.Store implements it. RecordTagWrite
// must call apply inside the transaction that records the save, and keep
// the record only if apply succeeds.
type History interface {
	RecordTagWrite(runID int64, path, before, after string, apply func() error) error
}

// Writer saves tags and records each save under a run.
type Writer struct {
	History History // nil records nothing
	RunID   int64
}

// format is how one file extension is tagged.
type format struct {
	vorbis bool                    // Vorbis comments: track and disc totals are keys of their own
	ape    bool                    // an APEv2 tag is written too, after go-taglib's ID3v2 tag
	asf    bool                    // WMA: internal/tag writes and reads the marker, which go-taglib drops
	is     func(tag.Metadata) bool // whether internal/tag read the file as this format
}

func fileType(ft tag.FileType) func(tag.Metadata) bool {
	return func(md tag.Metadata) bool { return md.FileType() == ft }
}

// isMP4 accepts every MP4 brand: the reader reports the file type only for
// some of them.
func isMP4(md tag.Metadata) bool { return md.Format() == tag.MP4 }

var formats = map[string]format{
	".mp3":  {is: fileType(tag.MP3)},
	".aac":  {ape: true, is: fileType(tag.AAC)},
	".flac": {vorbis: true, is: fileType(tag.FLAC)},
	".ogg":  {vorbis: true, is: fileType(tag.OGG)},
	".m4a":  {is: isMP4},
	".m4b":  {is: isMP4},
	".m4r":  {is: isMP4},
	".m4p":  {is: isMP4},
	".wav":  {is: fileType(tag.WAV)},
	".ape":  {is: fileType(tag.APE)},
	".mpc":  {is: fileType(tag.MPC)},
	".wma":  {asf: true, is: fileType(tag.WMA)},
}

// Supported reports whether files with the extension of path can be tagged.
func Supported(path string) bool {
	_, ok := formats[strings.ToLower(filepath.Ext(path))]
	return ok
}

// Save writes fields and the marker m to the file at path, through a copy
// that must read back with both before it replaces the original. When the
// file already has every value and a marker it is left alone and nothing is
// recorded. Fields not named are left as they are.
func (w Writer) Save(path string, fields Values, m marker.Marker) (Result, error) {
	f, ok := formats[strings.ToLower(filepath.Ext(path))]
	if !ok {
		return Result{}, ErrUnsupported
	}
	want, err := normalize(fields)
	if err != nil {
		return Result{}, fmt.Errorf("saving tags to %q: %w", path, err)
	}
	if err := checkContent(f, path); err != nil {
		return Result{}, fmt.Errorf("saving tags to %q: %w", path, err)
	}
	if f.asf {
		// TagLib has no ASF key for the compilation flag, which tagshelf
		// ignores when sorting anyway.
		delete(want, Compilation)
	}

	current, err := taglib.ReadTags(path)
	if err != nil {
		return Result{}, fmt.Errorf("saving tags to %q: reading its tags: %w", path, err)
	}
	named := append(slices.Collect(maps.Keys(want)), Marker)
	before := fromKeys(f, current, named)
	if f.asf {
		asfBefore(path, before)
	}
	after := maps.Clone(want)
	after[Marker] = []string{m.String()}
	if len(before[Marker]) > 0 && equalExcept(before, after, Marker) && (!f.ape || apeUpToDate(path, after)) {
		return Result{Before: before, After: before}, nil
	}

	tmp, err := copyTemp(path)
	if err != nil {
		return Result{}, fmt.Errorf("saving tags to %q: %w", path, err)
	}
	keep := false
	defer func() {
		if !keep {
			os.Remove(tmp)
		}
	}()
	keys := toKeys(f, after)
	if f.asf {
		delete(keys, marker.Key) // go-taglib would drop a new one; an old one it keeps is replaced below
	}
	if err := taglib.WriteTags(tmp, keys, 0); err != nil {
		return Result{}, fmt.Errorf("saving tags to %q: %w", path, err)
	}
	if f.ape {
		if err := writeAPE(tmp, after); err != nil {
			return Result{}, fmt.Errorf("saving tags to %q: %w", path, err)
		}
	}
	if f.asf {
		if err := writeASFMarker(tmp, m); err != nil {
			return Result{}, fmt.Errorf("saving tags to %q: %w", path, err)
		}
	}
	if err := verify(f, tmp, after, m); err != nil {
		return Result{}, fmt.Errorf("saving tags to %q: %w", path, err)
	}
	if f.asf {
		if err := sameAudio(path, tmp); err != nil {
			return Result{}, fmt.Errorf("saving tags to %q: %w", path, err)
		}
	}

	// The copy takes the original's permissions only now: go-taglib could
	// not have written a read-only copy.
	if info, err := os.Stat(path); err == nil {
		if err := os.Chmod(tmp, info.Mode().Perm()); err != nil {
			return Result{}, fmt.Errorf("saving tags to %q: %w", path, err)
		}
	}
	rename := func() error { return os.Rename(tmp, path) }
	if w.History == nil {
		err = rename()
	} else {
		var b, a []byte
		if b, err = json.Marshal(before); err == nil {
			if a, err = json.Marshal(after); err == nil {
				err = w.History.RecordTagWrite(w.RunID, path, string(b), string(a), rename)
			}
		}
	}
	if err != nil {
		return Result{}, fmt.Errorf("saving tags to %q: %w", path, err)
	}
	keep = true
	return Result{Changed: true, Before: before, After: after}, nil
}

// equalExcept reports whether a and b hold the same values for every field
// of b but skip.
func equalExcept(a, b Values, skip Field) bool {
	for k, v := range b {
		if k != skip && !slices.Equal(a[k], v) {
			return false
		}
	}
	return true
}

// normalize checks fields and returns them in the form they read back in:
// empty values dropped, track and disc numbers without leading zeros.
func normalize(fields Values) (Values, error) {
	out := Values{}
	for k, vs := range fields {
		if k == Marker {
			return nil, errors.New("the marker is not a field to set")
		}
		if _, ok := simpleKeys[k]; !ok && k != Track && k != Disc {
			return nil, fmt.Errorf("unknown field %q", k)
		}
		clean := []string{}
		for _, v := range vs {
			if v = strings.TrimSpace(v); v != "" {
				clean = append(clean, v)
			}
		}
		if len(clean) > 1 && !multiValued[k] {
			return nil, fmt.Errorf("field %q takes one value, not %d", k, len(clean))
		}
		if len(clean) == 1 {
			var err error
			switch k {
			case Track, Disc:
				clean[0], err = normalizeXofN(clean[0])
			case Date:
				err = checkDate(clean[0])
			case Compilation:
				if clean[0] != "1" {
					err = fmt.Errorf("compilation is %q, want 1 or nothing", clean[0])
				}
			}
			if err != nil {
				return nil, err
			}
		}
		out[k] = clean
	}
	return out, nil
}

// normalizeXofN checks a track or disc number, N or N/M, and drops leading
// zeros.
func normalizeXofN(s string) (string, error) {
	n, m, hasTotal := strings.Cut(s, "/")
	x, err := strconv.Atoi(strings.TrimSpace(n))
	if err != nil || x < 0 {
		return "", fmt.Errorf("number %q is not N or N/M", s)
	}
	if !hasTotal {
		return strconv.Itoa(x), nil
	}
	y, err := strconv.Atoi(strings.TrimSpace(m))
	if err != nil || y < 0 {
		return "", fmt.Errorf("number %q is not N or N/M", s)
	}
	return strconv.Itoa(x) + "/" + strconv.Itoa(y), nil
}

// checkDate accepts YYYY and YYYY-MM-DD. YYYY-MM is refused: some readers
// take it for a full date and others lose it.
func checkDate(s string) error {
	if len(s) == 4 {
		if _, err := strconv.Atoi(s); err == nil {
			return nil
		}
	}
	if len(s) == 10 && s[4] == '-' && s[7] == '-' {
		if _, err := strconv.Atoi(s[:4]); err == nil {
			if _, err := strconv.Atoi(s[5:7]); err == nil {
				if _, err := strconv.Atoi(s[8:]); err == nil {
					return nil
				}
			}
		}
	}
	return fmt.Errorf("date %q is not YYYY or YYYY-MM-DD", s)
}

// toKeys turns values into go-taglib properties for format f. A field with
// no values becomes a key with none, which go-taglib removes.
func toKeys(f format, v Values) map[string][]string {
	keys := map[string][]string{}
	for field, vs := range v {
		switch field {
		case Track:
			xOfNKeys(f, keys, vs, taglib.TrackNumber, "TRACKTOTAL", "TOTALTRACKS")
		case Disc:
			xOfNKeys(f, keys, vs, taglib.DiscNumber, "DISCTOTAL", "TOTALDISCS")
		default:
			keys[simpleKeys[field]] = nonNil(vs)
		}
	}
	return keys
}

// xOfNKeys writes a track or disc number. Other formats take N/M under one
// key; in Vorbis comments the total goes under both of the keys Picard
// writes, since readers take a total key over the M of N/M.
func xOfNKeys(f format, keys map[string][]string, vs []string, number, total, total2 string) {
	if !f.vorbis {
		keys[number] = nonNil(vs)
		return
	}
	keys[number], keys[total], keys[total2] = []string{}, []string{}, []string{}
	if len(vs) == 0 {
		return
	}
	n, m, hasTotal := strings.Cut(vs[0], "/")
	keys[number] = []string{n}
	if hasTotal {
		keys[total], keys[total2] = []string{m}, []string{m}
	}
}

func nonNil(vs []string) []string {
	if vs == nil {
		return []string{}
	}
	return vs
}

// fromKeys reads the named fields from go-taglib properties of format f, in
// the form normalize gives them.
func fromKeys(f format, keys map[string][]string, named []Field) Values {
	v := Values{}
	for _, field := range named {
		switch field {
		case Track:
			v[field] = xOfNFromKeys(f, keys, taglib.TrackNumber, "TRACKTOTAL", "TOTALTRACKS")
		case Disc:
			v[field] = xOfNFromKeys(f, keys, taglib.DiscNumber, "DISCTOTAL", "TOTALDISCS")
		default:
			v[field] = clean(keys[simpleKeys[field]])
		}
	}
	return v
}

func xOfNFromKeys(f format, keys map[string][]string, number, total, total2 string) []string {
	n := first(keys[number])
	if f.vorbis {
		t := first(keys[total])
		if t == "" {
			t = first(keys[total2])
		}
		if t != "" {
			n, _, _ = strings.Cut(n, "/")
			n += "/" + t
		}
	}
	if n == "" {
		return []string{}
	}
	if norm, err := normalizeXofN(n); err == nil {
		return []string{norm}
	}
	return []string{n}
}

// clean drops empty values and surrounding space.
func clean(vs []string) []string {
	out := []string{}
	for _, v := range vs {
		if v = strings.TrimSpace(v); v != "" {
			out = append(out, v)
		}
	}
	return out
}

func first(vs []string) string {
	if len(vs) == 0 {
		return ""
	}
	return strings.TrimSpace(vs[0])
}

// checkContent refuses a file whose content is another format than its
// extension says, such as a FLAC file renamed .mp3: go-taglib picks the tag
// type by extension, and would write the wrong kind of tag into it. A file
// internal/tag finds no tags in (an MP3 without ID3) is left to the check
// after writing.
func checkContent(f format, path string) error {
	md, err := readTags(path)
	if err != nil {
		return nil
	}
	if !f.is(md) {
		return fmt.Errorf("%w: its content is %s, not what its extension says", ErrNotVerified, describe(md))
	}
	return nil
}

func describe(md tag.Metadata) string {
	if md.FileType() != tag.UnknownFileType {
		return string(md.FileType())
	}
	return string(md.Format())
}

func readTags(path string) (tag.Metadata, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	return tag.ReadFrom(file)
}

// copyTemp copies the file at path to a new file beside it, with the same
// extension so that go-taglib picks the same tag type, and returns the copy's
// path.
func copyTemp(path string) (_ string, err error) {
	src, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer src.Close()
	// The prefix is store.OwnFilePrefix, which runs skip, so a copy left by a
	// crash is never organized as a song.
	dst, err := os.CreateTemp(filepath.Dir(path), ".tagshelf-*"+filepath.Ext(path))
	if err != nil {
		return "", fmt.Errorf("copying before writing: %w", err)
	}
	defer func() {
		if cerr := dst.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			os.Remove(dst.Name())
		}
	}()
	if _, err := io.Copy(dst, src); err != nil {
		return "", fmt.Errorf("copying before writing: %w", err)
	}
	if err := dst.Sync(); err != nil {
		return "", fmt.Errorf("copying before writing: %w", err)
	}
	return dst.Name(), nil
}

// asfBefore fixes what go-taglib reads wrong from a WMA file, reading it with
// internal/tag instead: the marker, which go-taglib never returns, and the
// order of the artist ID lists. TagLib writes the first value of a list to
// the Extended Content Description and the rest to the Metadata Library,
// which comes first in the file, so go-taglib reads the list back rotated.
func asfBefore(path string, before Values) {
	before[Marker] = []string{}
	md, err := readTags(path)
	if err != nil {
		return
	}
	for k, v := range md.Raw() {
		if s, ok := v.(string); ok && strings.EqualFold(k, marker.Key) {
			before[Marker] = clean(strings.Split(s, "\x00"))
		}
	}
	ids := mbz.Extract(md)
	if _, ok := before[ArtistIDs]; ok {
		before[ArtistIDs] = clean(ids.ArtistIDs)
	}
	if _, ok := before[AlbumArtistIDs]; ok {
		before[AlbumArtistIDs] = clean(ids.AlbumArtistIDs)
	}
}

// writeASFMarker sets the marker in the WMA file at path, a temporary copy:
// internal/tag's ASF writer copies it to a second temporary file beside it,
// which then replaces it.
func writeASFMarker(path string, m marker.Marker) (err error) {
	src, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("writing the marker: %w", err)
	}
	defer src.Close()
	dst, err := os.CreateTemp(filepath.Dir(path), ".tagshelf-*"+filepath.Ext(path))
	if err != nil {
		return fmt.Errorf("writing the marker: %w", err)
	}
	defer func() {
		if err != nil {
			dst.Close()
			os.Remove(dst.Name())
		}
	}()
	if err := tag.WriteASFAttribute(dst, src, marker.Key, m.String()); err != nil {
		if errors.Is(err, tag.ErrASFRefused) {
			return err // says why already
		}
		return fmt.Errorf("writing the marker: %w", err)
	}
	if err := dst.Sync(); err != nil {
		return fmt.Errorf("writing the marker: %w", err)
	}
	if err := dst.Close(); err != nil {
		return fmt.Errorf("writing the marker: %w", err)
	}
	src.Close() // Windows renames over open files only with trouble
	if err := os.Rename(dst.Name(), path); err != nil {
		return fmt.Errorf("writing the marker: %w", err)
	}
	return nil
}

// sameAudio checks by tag.Sum that the written copy at tmp holds the audio
// of the original at path.
func sameAudio(path, tmp string) error {
	a, err := sum(path)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrNotVerified, err)
	}
	b, err := sum(tmp)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrNotVerified, err)
	}
	if a != b {
		return fmt.Errorf("%w: the audio changed", ErrNotVerified)
	}
	return nil
}

func sum(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	return tag.Sum(file)
}

// verify reads the written copy back with go-taglib, which must return
// exactly the values written, and with internal/tag, the reader tagshelf
// sorts and indexes by, which must find the file's format, the marker and
// every field it reads.
func verify(f format, path string, want Values, m marker.Marker) error {
	keys, err := taglib.ReadTags(path)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrNotVerified, err)
	}
	got := fromKeys(f, keys, slices.Collect(maps.Keys(want)))
	for field, vs := range want {
		if f.asf && field == Marker {
			continue // go-taglib never reads it; checked with internal/tag below
		}
		if f.asf && multiValued[field] {
			// go-taglib reads the list rotated (see asfBefore); internal/tag
			// checks the order below.
			if !slices.Equal(slices.Sorted(slices.Values(got[field])), slices.Sorted(slices.Values(vs))) {
				return fmt.Errorf("%w: %s is %q, want %q", ErrNotVerified, field, got[field], vs)
			}
			continue
		}
		if !slices.Equal(got[field], vs) {
			return fmt.Errorf("%w: %s is %q, want %q", ErrNotVerified, field, got[field], vs)
		}
	}

	md, err := readTags(path)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrNotVerified, err)
	}
	if !f.is(md) {
		return fmt.Errorf("%w: it reads as %s", ErrNotVerified, describe(md))
	}
	if got, ok := marker.Read(md); !ok || got.String() != m.String() {
		return fmt.Errorf("%w: the marker reads as %q, want %q", ErrNotVerified, got.String(), m.String())
	}
	if err := verifyReader(md, want); err != nil {
		return err
	}
	if f.ape {
		return verifyAPE(path, want, m)
	}
	return nil
}

// verifyReader checks the fields internal/tag reads against want.
func verifyReader(md tag.Metadata, want Values) error {
	ids := mbz.Extract(md)
	for field, vs := range want {
		var ok bool
		var got any
		switch field {
		case Title:
			got = md.Title()
			ok = got == first(vs)
		case Artist:
			got = md.Artist()
			ok = got == first(vs)
		case AlbumArtist:
			got = md.AlbumArtist()
			ok = got == first(vs)
		case Album:
			got = md.Album()
			ok = got == first(vs)
		case Date:
			got = md.Year()
			ok = got == leadingYear(first(vs))
		case Track, Disc:
			x, n := md.Track()
			if field == Disc {
				x, n = md.Disc()
			}
			got = strconv.Itoa(x) + "/" + strconv.Itoa(n)
			wx, wn := xOfN(first(vs))
			ok = x == wx && n == wn
		case RecordingID:
			got = ids.RecordingID
			ok = got == first(vs)
		case ReleaseTrackID:
			got = ids.ReleaseTrackID
			ok = got == first(vs)
		case AlbumID:
			got = ids.AlbumID
			ok = got == first(vs)
		case ReleaseGroupID:
			got = ids.ReleaseGroupID
			ok = got == first(vs)
		case ArtistIDs:
			got = ids.ArtistIDs
			ok = slices.Equal(ids.ArtistIDs, vs)
		case AlbumArtistIDs:
			got = ids.AlbumArtistIDs
			ok = slices.Equal(ids.AlbumArtistIDs, vs)
		default:
			ok = true // compilation: checked through go-taglib only
		}
		if !ok {
			return fmt.Errorf("%w: %s reads as %q, want %q", ErrNotVerified, field, got, vs)
		}
	}
	return nil
}

// xOfN splits a normalized N or N/M; an empty value is 0/0.
func xOfN(s string) (int, int) {
	n, m, _ := strings.Cut(s, "/")
	x, _ := strconv.Atoi(n)
	y, _ := strconv.Atoi(m)
	return x, y
}

// leadingYear is the year of a normalized date, or 0 for none.
func leadingYear(s string) int {
	if len(s) < 4 {
		return 0
	}
	y, _ := strconv.Atoi(s[:4])
	return y
}
