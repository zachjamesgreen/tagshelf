package engine

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"go.senan.xyz/taglib"

	"tagshelf/internal/fixtures"
	"tagshelf/internal/store"
	"tagshelf/internal/tag"
)

// coverArt is a picture large enough to tell if the index copied it.
func coverArt() []byte {
	png := []byte("\x89PNG\r\n\x1a\n")
	return append(png, bytes.Repeat([]byte("art "), 16*1024)...)
}

// withArt copies the tagged fixture for ext to path with coverArt embedded,
// and reports whether go-taglib could embed it in that format.
func withArt(t *testing.T, path, ext string) bool {
	t.Helper()
	writeTestFile(t, path, readTestFile(t, fixtures.Tagged(ext)))
	if err := taglib.WriteImage(path, coverArt()); err != nil {
		return false
	}
	// Not every format takes a picture; one that did reads it back.
	img, err := taglib.ReadImage(path)
	return err == nil && len(img) > 0
}

// The index says whether a file has embedded art, in every format, and never
// holds the picture itself. "Has art" used to be read from the tags the index
// kept, which described a picture only for MP3 and MP4: a FLAC keeps its
// picture outside them, and an OGG file holds it as a base64 comment, which
// was copied whole into the index.
func TestIndexRecordsEmbeddedArtOfEveryFormat(t *testing.T) {
	lib := filepath.Join(t.TempDir(), "lib")
	var with []string
	for _, ext := range fixtures.Extensions() {
		path := filepath.Join(lib, "_Untagged", "art"+ext)
		if withArt(t, path, ext) {
			with = append(with, path)
		}
		writeTestFile(t, filepath.Join(lib, "_Untagged", "plain"+ext), readTestFile(t, fixtures.Tagged(ext)))
	}
	for _, ext := range []string{".mp3", ".flac", ".ogg", ".m4a", ".wma"} {
		if !slices.Contains(with, filepath.Join(lib, "_Untagged", "art"+ext)) {
			t.Fatalf("go-taglib embedded no picture in the %s fixture; files with art: %q", ext, with)
		}
	}
	s := openStore(t)
	indexLibrary(t, s, lib)
	rows := indexedFiles(t, s)
	for path, row := range rows {
		wantArt := slices.Contains(with, path)
		if row.HasArt != wantArt {
			t.Errorf("%s: HasArt = %t, want %t", filepath.Base(path), row.HasArt, wantArt)
		}
		if len(row.TagsJSON) > 8*1024 {
			t.Errorf("%s: the index holds %d bytes of tags, want the picture described and not copied", filepath.Base(path), len(row.TagsJSON))
		}
		if keeperCopy(row).Art != wantArt {
			t.Errorf("%s: the keeper rule sees art: %t, want %t", filepath.Base(path), keeperCopy(row).Art, wantArt)
		}
	}
}

// Of two copies of one recording alike in everything else, the one with
// embedded art is the suggested keeper, whichever was indexed first.
func TestArtDecidesTheSuggestedKeeper(t *testing.T) {
	for _, ext := range []string{".flac", ".ogg", ".mp3"} {
		lib := filepath.Join(t.TempDir(), "lib")
		plain := filepath.Join(lib, "_Untagged", "a plain"+ext)
		writeTestFile(t, plain, readTestFile(t, fixtures.Tagged(ext)))
		art := filepath.Join(lib, "_Untagged", "b art"+ext)
		if !withArt(t, art, ext) {
			t.Fatalf("no picture embedded in the %s fixture", ext)
		}
		s := openStore(t)
		indexLibrary(t, s, lib)
		g := onlyGroup(t, s)
		if g.Kind != store.GroupExact || len(g.Files) != 2 {
			t.Fatalf("%s: group = %+v, want the two copies as an exact group", ext, g)
		}
		if kept := g.Files[SuggestedKeeper(g.Files)]; kept.Path != art {
			t.Errorf("%s: the suggested keeper is %s, want the copy with art", ext, filepath.Base(kept.Path))
		}
	}
}

// A row read before the index recorded art is still seen to have it when its
// tags describe a picture.
func TestRowsFromBeforeArtWasRecorded(t *testing.T) {
	lib := filepath.Join(t.TempDir(), "lib")
	if !withArt(t, filepath.Join(lib, "_Untagged", "art.mp3"), ".mp3") {
		t.Fatal("no picture embedded in the MP3 fixture")
	}
	writeTestFile(t, filepath.Join(lib, "_Untagged", "plain.mp3"), readTestFile(t, fixtures.Tagged(".mp3")))
	s := openStore(t)
	indexLibrary(t, s, lib)
	execSQL(t, s, `UPDATE files SET has_art = NULL`)
	rows := indexedFiles(t, s)
	if !rows[filepath.Join(lib, "_Untagged", "art.mp3")].HasArt || rows[filepath.Join(lib, "_Untagged", "plain.mp3")].HasArt {
		t.Errorf("rows without has_art = %+v, want art seen for the MP3 whose tags describe a picture", rows)
	}
}

func TestRawJSONDescribesPictures(t *testing.T) {
	picture := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{0xAB}, 3<<18))
	got := rawJSON(map[string]any{
		"metadata_block_picture": picture,
		"COVERART":               picture,
		"APIC":                   &tag.Picture{Type: "Cover (front)", MIMEType: "image/png", Data: make([]byte, 1<<20)},
		"WM/Picture":             make([]byte, 1<<20),
		"short":                  []byte{1, 2, 3},
		"title":                  "Song",
	})
	if len(got) > 600 {
		t.Fatalf("rawJSON is %d bytes long, want every picture described", len(got))
	}
	for _, want := range []string{`"title":"Song"`, `"metadata_block_picture":{"bytes":786432,"picture":""}`, `"COVERART":{`,
		`"WM/Picture":{"bytes":1048576}`, `"short":{"base64":"AQID"}`, `"picture":"Cover (front)"`} {
		if !strings.Contains(got, want) {
			t.Errorf("rawJSON = %s\nwant it to hold %s", got, want)
		}
	}
}

// A WAV or AIFF file is lossless when it holds PCM, which go-taglib names as
// its inner codec; one that holds ADPCM or MP3 has none and is not. Every
// WAV used to count as lossless, and so was kept over a better lossy copy.
func TestIsLossless(t *testing.T) {
	for _, tt := range []struct {
		format, inner string
		want          bool
	}{
		{"wav", "pcm", true},
		{"wav", "", false},
		{"aiff", "pcm", true},
		{"aiff", "", false},
		{"flac", "", true},
		{"ape", "", true},
		{"dsf", "", true},
		{"mp4", "alac", true},
		{"mp4", "aac", false},
		{"ogg", "flac", true},
		{"ogg", "vorbis", false},
		{"mpeg", "", false},
	} {
		if got := isLossless(taglib.Properties{Format: tt.format, InnerCodec: tt.inner}); got != tt.want {
			t.Errorf("isLossless(%s holding %q) = %t, want %t", tt.format, tt.inner, got, tt.want)
		}
	}
	// What go-taglib really says of a PCM WAV file.
	props, err := taglib.ReadProperties(fixtures.Tagged(".wav"))
	if err != nil || !isLossless(props) {
		t.Errorf("properties of the WAV fixture = %+v, %v; want them lossless", props, err)
	}
}

// flacWith renders a FLAC file with this audio and these Vorbis comments.
func flacWith(audio string, comments ...string) []byte {
	block := func(typ byte, last bool, body []byte) []byte {
		if last {
			typ |= 0x80
		}
		return append([]byte{typ, byte(len(body) >> 16), byte(len(body) >> 8), byte(len(body))}, body...)
	}
	comment := binary.LittleEndian.AppendUint32(nil, 1)
	comment = append(comment, 'v')
	comment = binary.LittleEndian.AppendUint32(comment, uint32(len(comments)))
	for _, c := range comments {
		comment = binary.LittleEndian.AppendUint32(comment, uint32(len(c)))
		comment = append(comment, c...)
	}
	return slices.Concat([]byte("fLaC"), block(0, false, make([]byte, 34)), block(4, true, comment), []byte(audio))
}

// A tag element the reader cannot decode no longer makes the file
// unreadable: the rest of its tags are read, so it qualifies and is
// organized. The index row says what was left out.
func TestIndexNotesTheTagElementLeftOut(t *testing.T) {
	tmp := t.TempDir()
	source, lib := filepath.Join(tmp, "src"), filepath.Join(tmp, "lib")
	qualifying := []string{"TITLE=Title", "ARTIST=Artist", "ALBUM=Album", "TRACKNUMBER=2", "MUSICBRAINZ_ALBUMID=11111111-1111-4111-8111-111111111111"}
	writeTestFile(t, filepath.Join(source, "damaged.flac"), string(flacWith(theAudio, append(qualifying, "a comment with no equals sign")...)))
	writeTestFile(t, filepath.Join(source, "whole.flac"), string(flacWith("other "+theAudio, append(qualifying[1:], "TITLE=Whole")...)))

	s := openStore(t)
	finish := Run(context.Background(), Options{Source: source, Destination: lib, Workers: 1, Store: s})
	if finish.Counters.Organized != 2 || finish.Counters.Errors != 0 {
		t.Fatalf("run = %+v, want both files organized", finish)
	}
	rows := indexedFiles(t, s)
	damaged := rows[filepath.Join(lib, "Artist", "Album", "02 - Title.flac")]
	if damaged.Title != "Title" || damaged.Sum == "" || !strings.Contains(damaged.ReadError, `tags: comment 6 left out: it has no "="`) {
		t.Errorf("row of the damaged file = %+v, want its tags, its hash, and the comment left out noted in ReadError", damaged)
	}
	if whole := rows[filepath.Join(lib, "Artist", "Album", "02 - Whole.flac")]; strings.Contains(whole.ReadError, "tags:") {
		t.Errorf("ReadError of the whole file = %q, want nothing about its tags", whole.ReadError)
	}
}

// The files the tests write by hand as "a song without tags" have no audio
// the reader knows, which the index says in ReadError; a real file without
// tags has none.
func TestReadErrorOfHandMadeAndRealFiles(t *testing.T) {
	lib := filepath.Join(t.TempDir(), "lib")
	short := filepath.Join(lib, "_Untagged", "short.mp3")
	writeTestFile(t, short, "no tags")
	real := filepath.Join(lib, "_Untagged", "real.mp3")
	writeTestFile(t, real, readTestFile(t, fixtures.Untagged(".mp3")))
	s := openStore(t)
	indexLibrary(t, s, lib)
	rows := indexedFiles(t, s)
	if row := rows[short]; row.ReadError == "" || row.Sum != "" {
		t.Errorf("row of a file of seven bytes = %+v, want a read error and no hash", row)
	}
	if row := rows[real]; row.ReadError != "" || row.Sum == "" || row.Bitrate == 0 || row.Length == 0 {
		t.Errorf("row of the untagged fixture = %+v, want it read in full: a hash, a bitrate, a length", row)
	}
}

// The songs the tests write by hand each have a MusicBrainz recording ID of
// their own, as real files do: they all used to carry one and the same.
func TestHandMadeSongsHaveTheirOwnRecordingIDs(t *testing.T) {
	lib := filepath.Join(t.TempDir(), "lib")
	other := id3Tags{albumArtist: "Singer", album: "Album", title: "Other", track: "2"}
	writeTaggedMP3(t, songPath(lib), songTags, theAudio)
	second := filepath.Join(lib, "Singer", "Album", "02 - Other.mp3")
	writeTaggedMP3(t, second, other, "other "+theAudio)
	again := filepath.Join(lib, "_Untagged", "again.mp3")
	writeTaggedMP3(t, again, songTags, "again "+theAudio)
	s := openStore(t)
	indexLibrary(t, s, lib)
	rows := indexedFiles(t, s)
	a, b, c := rows[songPath(lib)].RecordingID, rows[second].RecordingID, rows[again].RecordingID
	if len(a) != 36 || len(b) != 36 || a == b || a != c {
		t.Errorf("recording IDs = %q, %q and %q; want one for each song, and the same for the same song", a, b, c)
	}
}
