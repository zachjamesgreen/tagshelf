//go:build ignore

// gen regenerates the audio fixtures in testdata/audio. Run it with
// `go generate ./internal/fixtures`; it needs ffmpeg on PATH.
//
// ffmpeg encodes about a second of silence for every format it can write,
// and tags what it can. Go then adds what ffmpeg cannot write the way
// MusicBrainz Picard does: the ID3v2.4 tags (with the recording ID in UFID),
// the WAV id3 chunk, the MP4 "----" MusicBrainz atoms, and APEv2 tags. APE and
// MPC have no ffmpeg encoder, so their untagged fixtures are TagLib test files
// committed as they are, and only their tagged copies are made here.
//
// ffmpeg runs with bitexact flags, so the output is the same on every run
// with the same ffmpeg version.
package main

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"tagshelf/internal/fixtures"
	"tagshelf/internal/rules"
)

func main() {
	log.SetFlags(0)
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		log.Fatal("gen needs ffmpeg on PATH")
	}
	dir := fixtures.Dir()

	// Untagged, straight from ffmpeg.
	for _, f := range []struct {
		name string
		args []string
	}{
		{"untagged.mp3", []string{"-c:a", "libmp3lame", "-b:a", "8k", "-write_xing", "0", "-id3v2_version", "0", "-f", "mp3"}},
		{"untagged.flac", []string{"-c:a", "flac", "-f", "flac"}},
		{"untagged.ogg", []string{"-c:a", "libvorbis", "-q:a", "0", "-f", "ogg"}},
		{"untagged.m4a", []string{"-c:a", "aac", "-b:a", "16k", "-f", "ipod"}},
		{"untagged.m4b", []string{"-c:a", "aac", "-b:a", "16k", "-brand", "M4B ", "-f", "ipod"}},
		{"untagged.wav", []string{"-c:a", "pcm_s16le", "-f", "wav"}},
		{"untagged.wma", []string{"-c:a", "wmav2", "-b:a", "32k", "-f", "asf"}},
		{"untagged.aac", []string{"-c:a", "aac", "-b:a", "16k", "-f", "adts"}},
	} {
		ffmpeg(filepath.Join(dir, f.name), 8000, f.args...)
	}
	// 8001 one-byte samples: an odd data chunk, which ffmpeg pads.
	ffmpeg(filepath.Join(dir, fixtures.UntaggedOddWAV), 8001, "-c:a", "pcm_u8", "-f", "wav")

	// Tagged by ffmpeg, with Picard's key names for the format.
	vorbis := []string{
		"TITLE=" + fixtures.Title,
		"ARTIST=" + fixtures.Artist,
		"ALBUMARTIST=" + fixtures.AlbumArtist,
		"ALBUM=" + fixtures.Album,
		"DATE=" + strconv.Itoa(fixtures.Year),
		"TRACKNUMBER=" + strconv.Itoa(fixtures.Track),
		"TRACKTOTAL=" + strconv.Itoa(fixtures.TrackTotal),
		"DISCNUMBER=" + strconv.Itoa(fixtures.Disc),
		"DISCTOTAL=" + strconv.Itoa(fixtures.DiscTotal),
		"MUSICBRAINZ_ARTISTID=" + fixtures.ArtistID,
		"MUSICBRAINZ_ALBUMARTISTID=" + fixtures.AlbumArtistID,
		"MUSICBRAINZ_ALBUMID=" + fixtures.ReleaseID,
		"MUSICBRAINZ_RELEASEGROUPID=" + fixtures.ReleaseGroupID,
		"MUSICBRAINZ_TRACKID=" + fixtures.RecordingID, // Picard: the recording
		"MUSICBRAINZ_RELEASETRACKID=" + fixtures.ReleaseTrackID,
	}
	ffmpeg(filepath.Join(dir, "tagged.flac"), 8000, withMetadata(vorbis, "-c:a", "flac", "-f", "flac")...)
	ffmpeg(filepath.Join(dir, "tagged.ogg"), 8000, withMetadata(vorbis, "-c:a", "libvorbis", "-q:a", "0", "-f", "ogg")...)

	asf := []string{
		"title=" + fixtures.Title, // Content Description
		"artist=" + fixtures.Artist,
		"WM/AlbumArtist=" + fixtures.AlbumArtist,
		"WM/AlbumTitle=" + fixtures.Album,
		"WM/Year=" + strconv.Itoa(fixtures.Year),
		"WM/TrackNumber=" + strconv.Itoa(fixtures.Track),
		"WM/PartOfSet=" + fmt.Sprintf("%d/%d", fixtures.Disc, fixtures.DiscTotal),
		"MusicBrainz/Artist Id=" + fixtures.ArtistID,
		"MusicBrainz/Album Artist Id=" + fixtures.AlbumArtistID,
		"MusicBrainz/Album Id=" + fixtures.ReleaseID,
		"MusicBrainz/Release Group Id=" + fixtures.ReleaseGroupID,
		"MusicBrainz/Track Id=" + fixtures.RecordingID,
		"MusicBrainz/Release Track Id=" + fixtures.ReleaseTrackID,
	}
	ffmpeg(filepath.Join(dir, "tagged.wma"), 8000, withMetadata(asf, "-c:a", "wmav2", "-b:a", "32k", "-f", "asf")...)

	mp4 := []string{
		"title=" + fixtures.Title,
		"artist=" + fixtures.Artist,
		"album_artist=" + fixtures.AlbumArtist,
		"album=" + fixtures.Album,
		"date=" + strconv.Itoa(fixtures.Year),
		"track=" + fmt.Sprintf("%d/%d", fixtures.Track, fixtures.TrackTotal),
		"disc=" + fmt.Sprintf("%d/%d", fixtures.Disc, fixtures.DiscTotal),
	}
	for _, f := range []struct{ name, brand string }{{"tagged.m4a", "M4A "}, {"tagged.m4b", "M4B "}} {
		path := filepath.Join(dir, f.name)
		ffmpeg(path, 8000, withMetadata(mp4, "-c:a", "aac", "-b:a", "16k", "-brand", f.brand, "-f", "ipod")...)
		write(path, addMP4FreeForm(read(path), fixtures.AlbumArtistID))
	}
	// A Various Artists compilation known only by its album artist ID: no
	// album artist name, which would mark it a compilation on its own.
	va := filepath.Join(dir, fixtures.TaggedVAM4A)
	ffmpeg(va, 8000, withMetadata(slices.DeleteFunc(slices.Clone(mp4), func(kv string) bool {
		return strings.HasPrefix(kv, "album_artist=")
	}), "-c:a", "aac", "-b:a", "16k", "-f", "ipod")...)
	write(va, addMP4FreeForm(read(va), rules.VariousArtistsMBID))

	// Tagged in Go.
	id3 := id3v24()
	ape := apev2()
	write(filepath.Join(dir, "tagged.mp3"), slices.Concat(id3, read(filepath.Join(dir, "untagged.mp3"))))
	write(filepath.Join(dir, "tagged.wav"), addWAVChunk(read(filepath.Join(dir, "untagged.wav")), "id3 ", id3))
	write(filepath.Join(dir, "tagged.aac"), slices.Concat(read(filepath.Join(dir, "untagged.aac")), ape))
	write(filepath.Join(dir, fixtures.TaggedID3v2AAC), slices.Concat(id3, read(filepath.Join(dir, "untagged.aac"))))
	write(filepath.Join(dir, "tagged.ape"), slices.Concat(read(filepath.Join(dir, "untagged.ape")), ape))
	write(filepath.Join(dir, "tagged.mpc"), slices.Concat(read(filepath.Join(dir, "untagged.mpc")), ape))

	manual(dir, vorbis, mp4)
}

// manual writes the hand-tagged fixtures: the fixture tags without
// MusicBrainz IDs, plus the manual marker under each format's key, and the
// FLAC variants without album and without marker.
func manual(dir string, vorbis, mp4 []string) {
	noIDs := slices.DeleteFunc(slices.Clone(vorbis), func(kv string) bool {
		return strings.HasPrefix(kv, "MUSICBRAINZ_")
	})
	marked := append(slices.Clone(noIDs), "TAGSHELF_MANUAL="+fixtures.ManualMarker)
	for _, f := range []struct {
		name string
		tags []string
		args []string
	}{
		{"manual.flac", marked, []string{"-c:a", "flac", "-f", "flac"}},
		{"manual.ogg", marked, []string{"-c:a", "libvorbis", "-q:a", "0", "-f", "ogg"}},
		{fixtures.ManualNoAlbumFLAC, slices.DeleteFunc(slices.Clone(marked), func(kv string) bool {
			return strings.HasPrefix(kv, "ALBUM=")
		}), []string{"-c:a", "flac", "-f", "flac"}},
		{fixtures.RipperFLAC, noIDs, []string{"-c:a", "flac", "-f", "flac"}},
	} {
		ffmpeg(filepath.Join(dir, f.name), 8000, withMetadata(f.tags, f.args...)...)
	}

	m4a := filepath.Join(dir, "manual.m4a")
	ffmpeg(m4a, 8000, withMetadata(mp4, "-c:a", "aac", "-b:a", "16k", "-f", "ipod")...)
	write(m4a, addMP4Atoms(read(m4a), [][2]string{{"TAGSHELF_MANUAL", fixtures.ManualMarker}}))

	write(filepath.Join(dir, "manual.mp3"), slices.Concat(manualID3v24(), read(filepath.Join(dir, "untagged.mp3"))))
}

// manualID3v24 builds an ID3v2.4 tag with the fixture tags and the manual
// marker in TXXX:TAGSHELF_MANUAL, but no MusicBrainz IDs.
func manualID3v24() []byte {
	var frames bytes.Buffer
	frame := func(id string, data []byte) {
		frames.WriteString(id)
		frames.Write(synchsafe(len(data)))
		frames.Write([]byte{0, 0})
		frames.Write(data)
	}
	text := func(id, value string) { frame(id, slices.Concat([]byte{3}, []byte(value))) } // 3: UTF-8

	text("TIT2", fixtures.Title)
	text("TPE1", fixtures.Artist)
	text("TPE2", fixtures.AlbumArtist)
	text("TALB", fixtures.Album)
	text("TDRC", strconv.Itoa(fixtures.Year))
	text("TRCK", fmt.Sprintf("%d/%d", fixtures.Track, fixtures.TrackTotal))
	text("TPOS", fmt.Sprintf("%d/%d", fixtures.Disc, fixtures.DiscTotal))
	frame("TXXX", slices.Concat([]byte{3}, []byte("TAGSHELF_MANUAL"), []byte{0}, []byte(fixtures.ManualMarker)))

	return slices.Concat([]byte{'I', 'D', '3', 4, 0, 0}, synchsafe(frames.Len()), frames.Bytes())
}

// ffmpeg encodes a second of mono silence at rate Hz into out.
func ffmpeg(out string, rate int, args ...string) {
	full := []string{"-v", "error", "-y",
		"-f", "lavfi", "-i", fmt.Sprintf("anullsrc=r=%d:cl=mono", rate), "-t", "1",
		"-map_metadata", "-1", "-fflags", "+bitexact", "-flags:a", "+bitexact",
	}
	full = append(full, args...)
	full = append(full, out)
	cmd := exec.Command("ffmpeg", full...)
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		log.Fatalf("ffmpeg %s: %v", filepath.Base(out), err)
	}
	log.Printf("encoded %s", filepath.Base(out))
}

func withMetadata(kv []string, args ...string) []string {
	var out []string
	for _, m := range kv {
		out = append(out, "-metadata", m)
	}
	return append(out, args...)
}

// id3v24 builds an ID3v2.4 tag the way Picard writes one: MusicBrainz IDs in
// TXXX frames, and the recording ID in a UFID frame.
func id3v24() []byte {
	var frames bytes.Buffer
	frame := func(id string, data []byte) {
		frames.WriteString(id)
		frames.Write(synchsafe(len(data)))
		frames.Write([]byte{0, 0})
		frames.Write(data)
	}
	text := func(id, value string) { frame(id, slices.Concat([]byte{3}, []byte(value))) } // 3: UTF-8
	txxx := func(desc, value string) {
		frame("TXXX", slices.Concat([]byte{3}, []byte(desc), []byte{0}, []byte(value)))
	}

	text("TIT2", fixtures.Title)
	text("TPE1", fixtures.Artist)
	text("TPE2", fixtures.AlbumArtist)
	text("TALB", fixtures.Album)
	text("TDRC", strconv.Itoa(fixtures.Year))
	text("TRCK", fmt.Sprintf("%d/%d", fixtures.Track, fixtures.TrackTotal))
	text("TPOS", fmt.Sprintf("%d/%d", fixtures.Disc, fixtures.DiscTotal))
	txxx("MusicBrainz Artist Id", fixtures.ArtistID)
	txxx("MusicBrainz Album Artist Id", fixtures.AlbumArtistID)
	txxx("MusicBrainz Album Id", fixtures.ReleaseID)
	txxx("MusicBrainz Release Group Id", fixtures.ReleaseGroupID)
	txxx("MusicBrainz Release Track Id", fixtures.ReleaseTrackID)
	frame("UFID", slices.Concat([]byte("http://musicbrainz.org"), []byte{0}, []byte(fixtures.RecordingID)))

	return slices.Concat([]byte{'I', 'D', '3', 4, 0, 0}, synchsafe(frames.Len()), frames.Bytes())
}

func synchsafe(n int) []byte {
	return []byte{byte(n >> 21 & 0x7f), byte(n >> 14 & 0x7f), byte(n >> 7 & 0x7f), byte(n & 0x7f)}
}

// apev2 builds an APEv2 tag with header and footer, with Picard's key names.
func apev2() []byte {
	items := [][2]string{
		{"Title", fixtures.Title},
		{"Artist", fixtures.Artist},
		{"Album Artist", fixtures.AlbumArtist},
		{"Album", fixtures.Album},
		{"Year", strconv.Itoa(fixtures.Year)},
		{"Track", fmt.Sprintf("%d/%d", fixtures.Track, fixtures.TrackTotal)},
		{"Disc", fmt.Sprintf("%d/%d", fixtures.Disc, fixtures.DiscTotal)},
		{"Musicbrainz_Artistid", fixtures.ArtistID},
		{"Musicbrainz_Albumartistid", fixtures.AlbumArtistID},
		{"Musicbrainz_Albumid", fixtures.ReleaseID},
		{"Musicbrainz_Releasegroupid", fixtures.ReleaseGroupID},
		{"musicbrainz_trackid", fixtures.RecordingID}, // Picard: the recording
		{"musicbrainz_releasetrackid", fixtures.ReleaseTrackID},
	}
	var body bytes.Buffer
	for _, it := range items {
		binary.Write(&body, binary.LittleEndian, uint32(len(it[1])))
		binary.Write(&body, binary.LittleEndian, uint32(0)) // UTF-8 text, read-write
		body.WriteString(it[0])
		body.WriteByte(0)
		body.WriteString(it[1])
	}
	const hasHeader, isHeader = 1 << 31, 1 << 29
	block := func(flags uint32) []byte {
		var b bytes.Buffer
		b.WriteString("APETAGEX")
		binary.Write(&b, binary.LittleEndian, uint32(2000))
		binary.Write(&b, binary.LittleEndian, uint32(body.Len()+32)) // items and footer
		binary.Write(&b, binary.LittleEndian, uint32(len(items)))
		binary.Write(&b, binary.LittleEndian, flags)
		b.Write(make([]byte, 8))
		return b.Bytes()
	}
	return slices.Concat(block(hasHeader|isHeader), body.Bytes(), block(hasHeader))
}

// addWAVChunk appends a chunk to a RIFF file and fixes the RIFF size.
func addWAVChunk(wav []byte, id string, data []byte) []byte {
	if string(wav[:4]) != "RIFF" || string(wav[8:12]) != "WAVE" {
		log.Fatal("addWAVChunk: not a WAV file")
	}
	chunk := slices.Concat([]byte(id), le32(len(data)), data)
	if len(data)%2 == 1 {
		chunk = append(chunk, 0)
	}
	out := slices.Concat(wav, chunk)
	copy(out[4:8], le32(len(out)-8))
	return out
}

func le32(n int) []byte {
	return binary.LittleEndian.AppendUint32(nil, uint32(n))
}

// addMP4FreeForm appends Picard's "----" MusicBrainz atoms to moov/udta/meta/ilst,
// with albumArtistID as the album artist ID.
// The file's mdat must come before moov, as ffmpeg writes it, so that growing
// moov moves no audio and no chunk offset needs fixing.
func addMP4FreeForm(mp4 []byte, albumArtistID string) []byte {
	return addMP4Atoms(mp4, [][2]string{
		{"MusicBrainz Artist Id", fixtures.ArtistID},
		{"MusicBrainz Album Artist Id", albumArtistID},
		{"MusicBrainz Album Id", fixtures.ReleaseID},
		{"MusicBrainz Release Group Id", fixtures.ReleaseGroupID},
		{"MusicBrainz Track Id", fixtures.RecordingID},
		{"MusicBrainz Release Track Id", fixtures.ReleaseTrackID},
	})
}

// addMP4Atoms appends a "----:com.apple.iTunes:<name>" atom for each name and
// value to moov/udta/meta/ilst. The file's mdat must come before moov.
func addMP4Atoms(mp4 []byte, kvs [][2]string) []byte {
	var atoms []byte
	for _, kv := range kvs {
		mean := box("mean", slices.Concat(make([]byte, 4), []byte("com.apple.iTunes")))
		name := box("name", slices.Concat(make([]byte, 4), []byte(kv[0])))
		data := box("data", slices.Concat([]byte{0, 0, 0, 1}, make([]byte, 4), []byte(kv[1]))) // 1: UTF-8
		atoms = append(atoms, box("----", slices.Concat(mean, name, data))...)
	}

	// Offsets of moov, udta, meta and ilst, outermost first.
	var path []int
	start, end := 0, len(mp4)
	seenMdat := false
	for _, want := range []string{"moov", "udta", "meta", "ilst"} {
		off := -1
		for o := start; o+8 <= end; {
			size := int(binary.BigEndian.Uint32(mp4[o:]))
			typ := string(mp4[o+4 : o+8])
			if typ == "mdat" {
				seenMdat = true
			}
			if typ == want {
				off = o
				break
			}
			if size < 8 {
				log.Fatalf("addMP4FreeForm: bad box size at %d", o)
			}
			o += size
		}
		if off < 0 {
			log.Fatalf("addMP4FreeForm: no %s box", want)
		}
		if want == "moov" && !seenMdat {
			log.Fatal("addMP4FreeForm: moov comes before mdat")
		}
		path = append(path, off)
		start = off + 8
		if want == "meta" {
			start += 4 // full box: version and flags
		}
		end = off + int(binary.BigEndian.Uint32(mp4[off:]))
	}

	out := slices.Concat(mp4[:end], atoms, mp4[end:])
	for _, off := range path {
		size := binary.BigEndian.Uint32(out[off:])
		binary.BigEndian.PutUint32(out[off:], size+uint32(len(atoms)))
	}
	return out
}

func box(typ string, payload []byte) []byte {
	return slices.Concat(binary.BigEndian.AppendUint32(nil, uint32(8+len(payload))), []byte(typ), payload)
}

func read(path string) []byte {
	b, err := os.ReadFile(path)
	if err != nil {
		log.Fatal(err)
	}
	return b
}

func write(path string, data []byte) {
	if err := os.WriteFile(path, data, 0o644); err != nil {
		log.Fatal(err)
	}
	log.Printf("wrote %s", filepath.Base(path))
}
