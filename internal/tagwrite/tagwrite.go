// Package tagwrite saves tags to audio files safely. Every save copies the
// file, writes the tags to the copy with go-taglib, reads the copy back to
// check the manual marker and every written field, and only then renames it
// over the original, so a save that goes wrong leaves the original as it was.
// A save writes the manual marker too, unless it is one of tags that say
// nothing of where a file belongs (SaveUnmarked). It is skipped when it would
// change nothing, and is recorded in the tag history with the written
// fields' values before and after.
//
// MP3, FLAC, OGG (.ogg, .oga and .opus), the MP4 family (.m4a, .m4b, .m4r,
// .m4p), WAV, APE, MPC, WMA, DSF and .aac can be tagged: every format a run
// indexes. An Ogg stream may hold Vorbis or Opus audio under any of its three
// extensions; one that holds FLAC is refused, because internal/tag does not
// read its tags back. An .aac file is tagged on both sides: go-taglib
// writes ID3v2, internal/tag the APEv2 tag Picard reads, and both must read
// back. go-taglib drops the marker from WMA, so internal/tag's ASF writer adds
// it, and reads a list of a WMA file rotated, so a save gives it back the
// lists it does not write in the order they had. TagLib has no ASF key for
// the compilation flag, so WMA saves leave it out. TagLib adds an ID3v1 tag to every MP3 and .aac file it saves; a save
// takes it off again unless the file had one.
//
// Before a copy replaces the original, its audio must have the original's
// tag.Sum, where the original has one, and the original must be as it was
// when it was copied.
package tagwrite

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"time"

	"go.senan.xyz/taglib"

	"github.com/zachjamesgreen/tagshelf/internal/marker"
	"github.com/zachjamesgreen/tagshelf/internal/tag"
	"github.com/zachjamesgreen/tagshelf/internal/tag/mbz"
)

// ErrUnsupported is returned for a file whose format tagshelf cannot tag.
var ErrUnsupported = errors.New("tagging not supported for this format")

// ErrNotVerified is returned when a written copy does not read back with the
// marker and the values written; the original is left untouched.
var ErrNotVerified = errors.New("tags did not read back as written")

// Field is a tag tagshelf writes, named the same in every format.
type Field string

// The fields a save can write. Every field holds a single value except the
// two lists of artist IDs, the genres and the composers.
const (
	Title          Field = "title"
	Artist         Field = "artist"
	AlbumArtist    Field = "albumartist"
	Album          Field = "album"
	Date           Field = "date"        // YYYY or YYYY-MM-DD
	Track          Field = "track"       // N or N/M, M being the track total
	Disc           Field = "disc"        // N or N/M, M being the disc total
	Compilation    Field = "compilation" // "1" for a compilation
	Genre          Field = "genre"
	Composer       Field = "composer"
	Comment        Field = "comment"
	RecordingID    Field = "musicbrainz_recordingid"
	ReleaseTrackID Field = "musicbrainz_releasetrackid"
	AlbumID        Field = "musicbrainz_albumid" // the release
	ReleaseGroupID Field = "musicbrainz_releasegroupid"
	ArtistIDs      Field = "musicbrainz_artistid"
	AlbumArtistIDs Field = "musicbrainz_albumartistid"
	AcoustID       Field = "acoustid_id"

	// Marker is the manual marker. Save writes it from its marker argument;
	// it appears only in a Result's Before and After.
	Marker Field = "marker"

	// SetMarker sets the marker to its one value, or with no value takes it
	// off, in place of the marker a save writes of its own accord: a person
	// changed it by hand. It appears only in what a save is given; what was
	// written is under Marker in a Result.
	SetMarker Field = "set_marker"

	// NotText lists, in what ReadAll returns, the names of the tags that are
	// not text, such as a picture: they are shown, kept by every save, and
	// cannot be set.
	NotText Field = "not_text"

	// TrackStored and DiscStored hold a track or disc number as it stood in
	// the file, each go-taglib key as KEY=value, when that is not how Save
	// writes the number: with a leading zero, or in Vorbis comments with the
	// total under one key only. Save and Restore add them to a Result's
	// Before, and Restore writes them back as they were.
	TrackStored Field = "track_stored"
	DiscStored  Field = "disc_stored"
)

// storedForm names the field that holds each number as it stood in the file.
var storedForm = map[Field]Field{Track: TrackStored, Disc: DiscStored}

// multiValued are the fields that may hold more than one value.
var multiValued = map[Field]bool{ArtistIDs: true, AlbumArtistIDs: true, Genre: true, Composer: true}

// MultiValued reports whether field may hold more than one value.
func MultiValued(field Field) bool { return multiValued[field] }

// placing are the fields that decide where a file belongs: those its path in
// the library is made from, and the MusicBrainz IDs, by which it qualifies.
var placing = map[Field]bool{
	Title: true, Artist: true, AlbumArtist: true, Album: true, Date: true, Track: true, Disc: true, Compilation: true,
	RecordingID: true, ReleaseTrackID: true, AlbumID: true, ReleaseGroupID: true, ArtistIDs: true, AlbumArtistIDs: true,
}

// Places reports whether values name a field that decides where a file
// belongs. A person who changes one of those has chosen the file's tags, and
// the save marks it as manually tagged; a change to its genre, composer or
// comment alone says nothing of the kind.
func Places(values Values) bool {
	for field := range values {
		if placing[field] {
			return true
		}
	}
	return false
}

// simpleKeys are the go-taglib property names of the fields that map to one
// key in every format.
var simpleKeys = map[Field]string{
	Title:          taglib.Title,
	Artist:         taglib.Artist,
	AlbumArtist:    taglib.AlbumArtist,
	Album:          taglib.Album,
	Date:           taglib.Date,
	Compilation:    taglib.Compilation,
	Genre:          taglib.Genre,
	Composer:       taglib.Composer,
	Comment:        taglib.Comment,
	RecordingID:    taglib.MusicBrainzTrackID, // Picard's name for the recording
	ReleaseTrackID: taglib.MusicBrainzReleaseTrackID,
	AlbumID:        taglib.MusicBrainzAlbumID,
	ReleaseGroupID: taglib.MusicBrainzReleaseGroupID,
	ArtistIDs:      taglib.MusicBrainzArtistID,
	AlbumArtistIDs: taglib.MusicBrainzAlbumArtistID,
	AcoustID:       taglib.AcoustIDID,
	Marker:         marker.Key,
}

// otherPrefix starts a Field that names a tag by the name go-taglib gives
// it, the same in every format: one of the tags the fields above do not
// cover, such as MOOD or LABEL.
const otherPrefix = "tag:"

// Other is the field of the tag go-taglib names name.
func Other(name string) Field { return Field(otherPrefix + name) }

// OtherName is the go-taglib name of a field made by Other, and whether f is
// one.
func (f Field) OtherName() (string, bool) { return strings.CutPrefix(string(f), otherPrefix) }

// taken are the go-taglib names the named fields are kept under, which no
// field made by Other may name, and those of pictures kept as text.
var taken = func() map[string]bool {
	names := map[string]bool{
		taglib.TrackNumber: true, taglib.DiscNumber: true,
		"TRACKTOTAL": true, "TOTALTRACKS": true, "DISCTOTAL": true, "TOTALDISCS": true,
		"METADATA_BLOCK_PICTURE": true, "COVERART": true,
	}
	for _, name := range simpleKeys {
		names[name] = true
	}
	return names
}()

// otherName matches a name a person may give a new tag.
var otherName = regexp.MustCompile(`^[A-Z0-9_][A-Z0-9_ ]*[A-Z0-9_]$|^[A-Z0-9_]$`)

// NewOther is the field for a tag a person names name, which is written in
// capitals whatever its case was. A name may hold letters and digits of the
// plain alphabet, spaces and underscores, and must not be the name of a tag
// that has a field of its own.
func NewOther(name string) (Field, error) {
	name = strings.ToUpper(strings.TrimSpace(name))
	switch {
	case name == "":
		return "", errors.New("a tag needs a name")
	case !otherName.MatchString(name):
		return "", fmt.Errorf("a tag's name may hold letters, digits, spaces and _ only, not %q", name)
	case taken[name]:
		return "", fmt.Errorf("%s has a field of its own", name)
	}
	return Other(name), nil
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
	id3v1  bool                    // TagLib adds an ID3v1 tag when it saves, which is taken off again
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
	".mp3":  {id3v1: true, is: fileType(tag.MP3)},
	".aac":  {id3v1: true, ape: true, is: fileType(tag.AAC)},
	".flac": {vorbis: true, is: fileType(tag.FLAC)},
	".ogg":  {vorbis: true, is: fileType(tag.OGG)},
	".oga":  {vorbis: true, is: fileType(tag.OGG)},
	".opus": {vorbis: true, is: fileType(tag.OGG)},
	".m4a":  {is: isMP4},
	".m4b":  {is: isMP4},
	".m4r":  {is: isMP4},
	".m4p":  {is: isMP4},
	".wav":  {is: fileType(tag.WAV)},
	".ape":  {is: fileType(tag.APE)},
	".mpc":  {is: fileType(tag.MPC)},
	".wma":  {asf: true, is: fileType(tag.WMA)},
	".dsf":  {is: fileType(tag.DSF)},
}

// Supported reports whether files with the extension of path can be tagged.
func Supported(path string) bool {
	_, ok := formats[strings.ToLower(filepath.Ext(path))]
	return ok
}

// editable are the fields Read returns.
var editable = []Field{Title, Artist, AlbumArtist, Album, Date, Track, Disc, Compilation, Genre, Composer, Comment,
	RecordingID, ReleaseTrackID, AlbumID, ReleaseGroupID, ArtistIDs, AlbumArtistIDs, Marker}

// Read returns every field of the file at path that Save can write, in the
// form Save takes them, with the marker. A field the file lacks has no
// values.
func Read(path string) (Values, error) {
	f, ok := formats[strings.ToLower(filepath.Ext(path))]
	if !ok {
		return nil, ErrUnsupported
	}
	v, err := current(f, path, editable)
	if err != nil {
		return nil, fmt.Errorf("reading the tags of %q: %w", path, err)
	}
	return v, nil
}

// ReadAll is Read with the rest of the file's tags: its AcoustID ID, every
// other tag that is text, each under the field Other makes of its name, and
// under NotText the names of those that are not.
func ReadAll(path string) (Values, error) {
	f, ok := formats[strings.ToLower(filepath.Ext(path))]
	if !ok {
		return nil, ErrUnsupported
	}
	v, keys, err := currentKeys(f, path, append(slices.Clone(editable), AcoustID))
	if err != nil {
		return nil, fmt.Errorf("reading the tags of %q: %w", path, err)
	}
	for name, vs := range keys {
		if vs = clean(vs); !taken[name] && len(vs) > 0 {
			v[Other(name)] = vs
		}
	}
	if names := notText(path); len(names) > 0 {
		v[NotText] = names
	}
	return v, nil
}

// notText lists the names internal/tag gives the tags of the file at path
// that are not text: pictures and other bytes.
func notText(path string) []string {
	md, err := readTags(path)
	if err != nil {
		return nil
	}
	var names []string
	picture := false
	for name, value := range md.Raw() {
		switch value.(type) {
		case *tag.Picture:
			picture = true
			names = append(names, name)
		case []byte:
			names = append(names, name)
		case string:
			if n := strings.ToUpper(name); n == "METADATA_BLOCK_PICTURE" || n == "COVERART" {
				picture = true
				names = append(names, name)
			}
		}
	}
	if !picture && md.Picture() != nil {
		// A FLAC file keeps its pictures beside its comments, not among them.
		names = append(names, "PICTURE")
	}
	slices.Sort(names)
	return names
}

// current reads the named fields of the file at path as they are now.
func current(f format, path string, named []Field) (Values, error) {
	v, _, err := currentKeys(f, path, named)
	return v, err
}

// currentKeys is current, with the go-taglib properties the values were read
// from.
func currentKeys(f format, path string, named []Field) (Values, map[string][]string, error) {
	keys, err := taglib.ReadTags(path)
	if err != nil {
		return nil, nil, err
	}
	v := fromKeys(f, keys, named)
	if f.asf {
		asfBefore(path, v)
	}
	return v, keys, nil
}

// beforeWrite reads the named fields as a write records them: as current
// does, with the stored form of each number that a write of its value would
// not give back.
func beforeWrite(f format, path string, named []Field) (Values, error) {
	v, keys, err := currentKeys(f, path, named)
	if err != nil {
		return nil, err
	}
	for field, stored := range storedForm {
		vs, ok := v[field]
		if _, named := v[stored]; !ok || named {
			continue
		}
		if s := asStored(f, keys, field); !slices.Equal(s, asStored(f, toKeys(f, Values{field: vs}), field)) {
			v[stored] = s
		}
	}
	return v, nil
}

// Check reports what is wrong with value as the single value of field, nil
// when Save would take it.
func Check(field Field, value string) error {
	_, err := normalize(Values{field: {value}})
	return err
}

// HasCompilation reports whether the compilation flag can be saved to the
// file at path: TagLib has no key for it in WMA files.
func HasCompilation(path string) bool {
	f, ok := formats[strings.ToLower(filepath.Ext(path))]
	return ok && !f.asf
}

// Save writes fields and the marker m to the file at path, through a copy
// that must read back with both before it replaces the original. When the
// file already has every value and a marker it is left alone and nothing is
// recorded. Fields not named are left as they are.
func (w Writer) Save(path string, fields Values, m marker.Marker) (Result, error) {
	return w.save(path, fields, &m)
}

// SaveUnmarked is Save without the marker: the file keeps the one it has, or
// stays without one. It is for fields that say nothing of where a file
// belongs (see Places). When the file already has every value it is left
// alone and nothing is recorded.
func (w Writer) SaveUnmarked(path string, fields Values) (Result, error) {
	return w.save(path, fields, nil)
}

// save writes fields, and the marker m unless it is nil.
func (w Writer) save(path string, fields Values, m *marker.Marker) (Result, error) {
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

	// A marker set by hand goes in as it is given, whatever m is.
	set, byHand := want[SetMarker]
	delete(want, SetMarker)
	named := slices.Collect(maps.Keys(want))
	if m != nil || byHand {
		named = append(named, Marker)
	}
	before, err := beforeWrite(f, path, named)
	if err != nil {
		return Result{}, fmt.Errorf("saving tags to %q: reading its tags: %w", path, err)
	}
	after := maps.Clone(want)
	unchanged := equalExcept(before, after, "")
	switch {
	case byHand:
		if f.asf && len(set) == 0 && len(before[Marker]) > 0 {
			return Result{}, fmt.Errorf("saving tags to %q: %w", path, ErrMarkerStays)
		}
		after[Marker] = set
		unchanged = equalExcept(before, after, "")
	case m != nil:
		after[Marker] = []string{m.String()}
		unchanged = len(before[Marker]) > 0 && equalExcept(before, after, Marker)
	}
	if unchanged && (!f.ape || apeUpToDate(path, after)) {
		return Result{Before: before, After: before}, nil
	}
	if err := w.apply(f, path, before, after); err != nil {
		return Result{}, fmt.Errorf("saving tags to %q: %w", path, err)
	}
	return Result{Changed: true, Before: before, After: after}, nil
}

// CannotHoldError is why a save was refused whose copy came back without a
// tag it was given: the file's format has no place for a tag of that name.
// It comes wrapped with ErrNotVerified.
type CannotHoldError struct{ Name string }

func (e *CannotHoldError) Error() string {
	return fmt.Sprintf("this kind of file cannot hold a tag named %s", e.Name)
}

// ErrMarkerStays is returned for a save that would take the marker off a WMA
// file, which tagshelf cannot do.
var ErrMarkerStays = errors.New("tagshelf cannot take the marker off a WMA file")

// ErrChanged is returned by Restore for a file whose tags are no longer what
// the save being undone left.
var ErrChanged = errors.New("its tags have changed since they were saved")

// Matches reports whether the file at path holds exactly the values of want
// for the fields want names, the marker among them if it does.
func Matches(path string, want Values) (bool, error) {
	f, ok := formats[strings.ToLower(filepath.Ext(path))]
	if !ok {
		return false, ErrUnsupported
	}
	cur, err := current(f, path, slices.Collect(maps.Keys(want)))
	if err != nil {
		return false, fmt.Errorf("reading the tags of %q: %w", path, err)
	}
	return equalExcept(cur, want, ""), nil
}

// Restore undoes a save: the file at path, which must hold the values of
// from, the save's After, gets those of to, its Before, the marker included.
// A marker the file did not have is taken away again, except from a WMA
// file, which keeps it: tagshelf cannot remove it there. A track or disc
// number that to holds in its stored form goes back in that form, or by its
// value alone in a format that does not keep the form. The write goes
// through the same copy and check as Save and is recorded the same way. A
// file that does not hold from is left alone with ErrChanged.
func (w Writer) Restore(path string, to, from Values) (Result, error) {
	f, ok := formats[strings.ToLower(filepath.Ext(path))]
	if !ok {
		return Result{}, ErrUnsupported
	}
	if err := checkContent(f, path); err != nil {
		return Result{}, fmt.Errorf("restoring the tags of %q: %w", path, err)
	}
	before, err := beforeWrite(f, path, slices.Collect(maps.Keys(from)))
	if err != nil {
		return Result{}, fmt.Errorf("restoring the tags of %q: reading its tags: %w", path, err)
	}
	if !equalExcept(before, from, "") {
		return Result{}, fmt.Errorf("restoring the tags of %q: %w", path, ErrChanged)
	}
	after := Values{}
	for field, vs := range to {
		if field == TrackStored || field == DiscStored {
			after[field] = nonNil(vs) // as it stood, spaces and all
			continue
		}
		after[field] = clean(vs)
	}
	if f.asf {
		delete(after, Compilation)
		if len(after[Marker]) == 0 && len(before[Marker]) > 0 {
			after[Marker] = before[Marker]
		}
	}
	if equalExcept(before, after, "") {
		return Result{Before: before, After: before}, nil
	}
	err = w.apply(f, path, before, after)
	_, track := after[TrackStored]
	_, disc := after[DiscStored]
	if errors.Is(err, ErrNotVerified) && (track || disc) {
		// The format does not keep a number as it stood (MP4 holds integers):
		// its value goes back all the same.
		delete(after, TrackStored)
		delete(after, DiscStored)
		if equalExcept(before, after, "") {
			return Result{Before: before, After: before}, nil
		}
		err = w.apply(f, path, before, after)
	}
	if err != nil {
		return Result{}, fmt.Errorf("restoring the tags of %q: %w", path, err)
	}
	return Result{Changed: true, Before: before, After: after}, nil
}

// apply writes after, with the marker if it names it, to the file at path
// through a copy that must read back with it, and records the write with
// before.
func (w Writer) apply(f format, path string, before, after Values) error {
	orig, err := os.Stat(path)
	if err != nil {
		return err
	}
	tmp, err := copyTemp(path)
	if err != nil {
		return err
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
		keepASFOrder(path, keys)
	}
	if err := writeTags(tmp, keys, 0); err != nil {
		return err
	}
	if f.id3v1 {
		if err := dropNewID3v1(path, tmp); err != nil {
			return err
		}
	}
	if f.ape {
		if err := writeAPE(tmp, after); err != nil {
			return err
		}
	}
	if f.asf && len(after[Marker]) > 0 {
		if err := writeASFMarker(tmp, after[Marker][0]); err != nil {
			return err
		}
	}
	if err := verify(f, tmp, after); err != nil {
		return err
	}
	if err := sameAudio(path, tmp, f.asf); err != nil {
		return err
	}

	// The copy takes the original's permissions only now: go-taglib could
	// not have written a read-only copy.
	if err := os.Chmod(tmp, orig.Mode().Perm()); err != nil {
		return err
	}
	rename := func() error {
		// A file another program wrote since it was copied keeps what that
		// program wrote.
		if now, err := os.Stat(path); err != nil {
			return err
		} else if now.Size() != orig.Size() || !now.ModTime().Equal(orig.ModTime()) {
			return errChangedMeanwhile
		}
		return replace(tmp, path, orig.Mode().Perm())
	}
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
		return err
	}
	keep = true
	return nil
}

// errChangedMeanwhile is returned for a file that something else wrote
// between the copy and the rename; the file is left as that left it.
var errChangedMeanwhile = errors.New("the file changed while its tags were being written")

// replace renames the written copy at tmp, which has the original's
// permissions perm already, over the original at path. Windows does not
// replace a read-only file, so there the original is made writable first.
func replace(tmp, path string, perm os.FileMode) error {
	readOnly := runtime.GOOS == "windows" && perm&0o200 == 0
	if readOnly {
		if err := os.Chmod(path, perm|0o200); err != nil {
			return err
		}
	}
	if err := os.Rename(tmp, path); err != nil {
		if readOnly {
			os.Chmod(path, perm)
		}
		return err
	}
	return nil
}

// dropNewID3v1 takes the ID3v1 tag off the end of the written copy at tmp
// unless the original at path ends with one. TagLib saves every MPEG file
// with both tags, filling a missing ID3v1 tag from the ID3v2 one, and
// go-taglib has no option against it. ID3v1 holds 30 Latin-1 characters a
// field, so the tag has nothing the ID3v2 tag lacks, and cuts or garbles the
// rest.
func dropNewID3v1(path, tmp string) error {
	if had, err := endsWithID3v1(path); err != nil || had {
		return err
	}
	if has, err := endsWithID3v1(tmp); err != nil || !has {
		return err
	}
	info, err := os.Stat(tmp)
	if err != nil {
		return err
	}
	return os.Truncate(tmp, info.Size()-id3v1Size)
}

const id3v1Size = 128

// endsWithID3v1 reports whether the file at path ends with an ID3v1 tag.
// The footer of an APEv2 tag is not taken for one.
func endsWithID3v1(path string) (bool, error) {
	file, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || info.Size() < id3v1Size {
		return false, err
	}
	b := make([]byte, id3v1Size)
	if _, err := file.ReadAt(b, info.Size()-id3v1Size); err != nil {
		return false, err
	}
	return string(b[:3]) == "TAG" && string(b[id3v1Size-32:id3v1Size-24]) != "APETAGEX", nil
}

// writeTags is go-taglib's writer. Tests put one in its place that writes
// the copy wrong, to see the checks refuse it.
var writeTags = taglib.WriteTags

// checkMarker checks the marker internal/tag reads from md against the one
// written: want's only value, or none.
func checkMarker(md tag.Metadata, want []string, what string) error {
	got, ok := marker.Read(md)
	switch {
	case len(want) == 0 && ok:
		return fmt.Errorf("%w: %s reads as %q, want none", ErrNotVerified, what, got.String())
	case len(want) > 0 && (!ok || got.String() != want[0]):
		return fmt.Errorf("%w: %s reads as %q, want %q", ErrNotVerified, what, got.String(), want[0])
	}
	return nil
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
		if k == Marker || k == NotText {
			return nil, fmt.Errorf("%s is not a field to set", k)
		}
		if k == SetMarker {
			set, err := normalizeMarker(vs)
			if err != nil {
				return nil, err
			}
			out[k] = set
			continue
		}
		if name, ok := k.OtherName(); ok {
			if name == "" || taken[name] {
				return nil, fmt.Errorf("the tag %q has a field of its own", name)
			}
		} else if _, ok := simpleKeys[k]; !ok && k != Track && k != Disc {
			return nil, fmt.Errorf("unknown field %q", k)
		}
		clean := []string{}
		for _, v := range vs {
			if v = strings.TrimSpace(v); v != "" {
				clean = append(clean, v)
			}
		}
		if len(clean) > 1 && !isList(k) {
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

// isList reports whether field may hold more than one value: the fields
// listed in multiValued, and every tag named by Other.
func isList(field Field) bool {
	_, other := field.OtherName()
	return other || multiValued[field]
}

// normalizeMarker checks a marker set by hand: none, which takes it off, or
// one that says how the tags were chosen and on what day.
func normalizeMarker(vs []string) ([]string, error) {
	vs = clean(vs)
	if len(vs) == 0 {
		return vs, nil
	}
	m, ok := marker.Parse(vs[0])
	if len(vs) > 1 || !ok || m.Method == marker.Unknown || m.Date.IsZero() {
		return nil, errors.New("a marker is hand or musicbrainz and a day, as in hand 2026-10-12")
	}
	return []string{m.String()}, nil
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
		case TrackStored, DiscStored: // below, over the number's own keys
		default:
			keys[keyOf(field)] = nonNil(vs)
		}
	}
	for field, stored := range storedForm {
		vs, ok := v[stored]
		if !ok {
			continue
		}
		own := numberKeys(f, field)
		for _, k := range own {
			keys[k] = []string{}
		}
		for _, kv := range vs {
			if k, value, ok := strings.Cut(kv, "="); ok && slices.Contains(own, k) {
				keys[k] = append(keys[k], value)
			}
		}
	}
	return keys
}

// numberKeys are the go-taglib keys that hold the track or disc number in
// format f.
func numberKeys(f format, field Field) []string {
	keys := []string{taglib.TrackNumber, "TRACKTOTAL", "TOTALTRACKS"}
	if field == Disc {
		keys = []string{taglib.DiscNumber, "DISCTOTAL", "TOTALDISCS"}
	}
	if !f.vorbis {
		return keys[:1]
	}
	return keys
}

// asStored returns the track or disc number of go-taglib properties as it
// is stored: every value of its keys as KEY=value.
func asStored(f format, keys map[string][]string, field Field) []string {
	out := []string{}
	for _, k := range numberKeys(f, field) {
		for _, v := range keys[k] {
			if v != "" {
				out = append(out, k+"="+v)
			}
		}
	}
	return out
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

// keyOf is the go-taglib name a field that maps to one key is kept under.
func keyOf(field Field) string {
	if name, ok := field.OtherName(); ok {
		return name
	}
	return simpleKeys[field]
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
		case TrackStored:
			v[field] = asStored(f, keys, Track)
		case DiscStored:
			v[field] = asStored(f, keys, Disc)
		default:
			v[field] = clean(keys[keyOf(field)])
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
	// crash is never organized as a song. RemoveStaleCopies knows the name.
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
	_, marked := before[Marker]
	if marked {
		before[Marker] = []string{}
	}
	md, err := readTags(path)
	if err != nil {
		return
	}
	for k, v := range md.Raw() {
		if s, ok := v.(string); ok && marked && strings.EqualFold(k, marker.Key) {
			before[Marker] = clean(strings.Split(s, "\x00"))
		}
	}
	if _, ok := before[Genre]; ok {
		before[Genre] = clean(tag.Genres(md.Raw()))
	}
	if _, ok := before[Composer]; ok {
		before[Composer] = clean(tag.Composers(md.Raw()))
	}
	ids := mbz.Extract(md)
	if _, ok := before[ArtistIDs]; ok {
		before[ArtistIDs] = clean(ids.ArtistIDs)
	}
	if _, ok := before[AlbumArtistIDs]; ok {
		before[AlbumArtistIDs] = clean(ids.AlbumArtistIDs)
	}
}

// keepASFOrder adds to keys, which a save is about to write to a copy of the
// WMA file at path, every list of the file the save does not write, in the
// order the file has it. TagLib reads a list rotated (see asfBefore) and
// would write it back as it read it, turning it by one with every save;
// given the list in its order, it writes it as it was.
//
// Only a list TagLib returns under a property name can be given back to it,
// and only one that is found again, value for value, among the attributes
// internal/tag reads: text, since TagLib returns nothing for other types. A
// list under a name TagLib has no property for is still turned.
func keepASFOrder(path string, keys map[string][]string) {
	read, err := taglib.ReadTags(path)
	if err != nil {
		return
	}
	md, err := readTags(path)
	if err != nil {
		return
	}
	var lists [][]string
	for _, v := range md.Raw() {
		if s, ok := v.(string); ok && strings.Contains(s, "\x00") {
			lists = append(lists, strings.Split(s, "\x00"))
		}
	}
	sorted := func(vs []string) []string { return slices.Sorted(slices.Values(vs)) }
	for k, vs := range read {
		if _, written := keys[k]; written || len(vs) < 2 || slices.Contains(vs, "") {
			continue
		}
		// The one order the file has these values in, if it has one.
		var order []string
		for _, l := range lists {
			if !slices.Equal(sorted(l), sorted(vs)) {
				continue
			}
			if order != nil && !slices.Equal(order, l) {
				order = nil
				break
			}
			order = l
		}
		if order != nil && !slices.Equal(order, vs) {
			keys[k] = order
		}
	}
}

// writeASFMarker sets the marker in the WMA file at path, a temporary copy:
// internal/tag's ASF writer copies it to a second temporary file beside it,
// which then replaces it.
func writeASFMarker(path, value string) (err error) {
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
	if err := tag.WriteASFAttribute(dst, src, marker.Key, value); err != nil {
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
// of the original at path. An original that tag.Sum cannot hash has nothing
// to compare the copy with: it passes unless strict, as a WMA file is, whose
// header internal/tag itself rewrites.
func sameAudio(path, tmp string, strict bool) error {
	a, err := sum(path)
	if err != nil && !strict {
		return nil
	}
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
func verify(f format, path string, want Values) error {
	keys, err := taglib.ReadTags(path)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrNotVerified, err)
	}
	got := fromKeys(f, keys, slices.Collect(maps.Keys(want)))
	for field, vs := range want {
		if f.asf && field == Marker {
			continue // go-taglib never reads it; checked with internal/tag below
		}
		if f.asf && isList(field) {
			// go-taglib reads the list rotated (see asfBefore); internal/tag
			// checks the order below.
			if !slices.Equal(slices.Sorted(slices.Values(got[field])), slices.Sorted(slices.Values(vs))) {
				if name, ok := field.OtherName(); ok && len(got[field]) == 0 {
					return fmt.Errorf("%w: %w", ErrNotVerified, &CannotHoldError{Name: name})
				}
				return fmt.Errorf("%w: %s is %q, want %q", ErrNotVerified, field, got[field], vs)
			}
			continue
		}
		if !slices.Equal(got[field], vs) {
			if name, ok := field.OtherName(); ok && len(got[field]) == 0 {
				return fmt.Errorf("%w: %w", ErrNotVerified, &CannotHoldError{Name: name})
			}
			return fmt.Errorf("%w: %s is %q, want %q", ErrNotVerified, field, got[field], vs)
		}
	}

	md, err := readTags(path)
	if errors.Is(err, tag.ErrNoTagsFound) && wantsNothing(want) {
		// A restore of a file that had no tags leaves it with none again:
		// no tags to tell, as checkContent finds it before a first save.
		return nil
	}
	if err != nil {
		return fmt.Errorf("%w: %v", ErrNotVerified, err)
	}
	if !f.is(md) {
		return fmt.Errorf("%w: it reads as %s", ErrNotVerified, describe(md))
	}
	if marked, ok := want[Marker]; ok {
		if err := checkMarker(md, marked, "the marker"); err != nil {
			return err
		}
	}
	if err := verifyReader(md, want); err != nil {
		return err
	}
	if f.ape {
		return verifyAPE(path, want)
	}
	return nil
}

// wantsNothing reports whether want has no value for any field, the marker
// among them.
func wantsNothing(want Values) bool {
	for _, vs := range want {
		if len(vs) > 0 {
			return false
		}
	}
	return true
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
			if _, stored := want[storedForm[field]]; stored {
				// Put back as it stood, which go-taglib has checked key by
				// key: internal/tag reads that as it did before the save,
				// and of the total keys it knows one.
				ok = true
				break
			}
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
		case Genre:
			// The index lists the genres and the composers by this reader.
			list := tag.Genres(md.Raw())
			got, ok = list, slices.Equal(list, vs) || len(list) == 0 && len(vs) == 0
		case Composer:
			list := tag.Composers(md.Raw())
			got, ok = list, slices.Equal(list, vs) || len(list) == 0 && len(vs) == 0
		default:
			ok = true // compilation and comment: checked through go-taglib only
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

// StaleAfter is how old a temporary copy must be before RemoveStaleCopies
// takes it for one a crash left: no save takes anywhere near as long.
const StaleAfter = time.Hour

// tempCopy matches the names copyTemp gives its copies, for the extensions
// that can be tagged.
var tempCopy = regexp.MustCompile(`^\.tagshelf-[0-9]+(\.[A-Za-z0-9]+)$`)

// RemoveStaleCopies removes the temporary copies in the folder dir, not the
// folders below it, that a save which crashed left behind: the files named
// as copyTemp names them and last written more than age ago, so that a save
// going on now keeps its copy. It returns the paths removed. A copy that
// cannot be removed is left, and the first such error returned after the
// rest have been tried.
func RemoveStaleCopies(dir string, age time.Duration) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var removed []string
	var first error
	for _, e := range entries {
		m := tempCopy.FindStringSubmatch(e.Name())
		if m == nil || !e.Type().IsRegular() || !Supported(m[1]) {
			continue
		}
		info, err := e.Info()
		if err != nil || time.Since(info.ModTime()) <= age {
			continue // gone already, or a save in progress
		}
		path := filepath.Join(dir, e.Name())
		if err := os.Remove(path); err != nil {
			if first == nil {
				first = err
			}
			continue
		}
		removed = append(removed, path)
	}
	return removed, first
}
