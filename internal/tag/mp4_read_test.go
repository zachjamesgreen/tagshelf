package tag

import (
	"bytes"
	"encoding/binary"
	"slices"
	"testing"
)

// allGetters calls every getter of m, for the tests that a damaged value
// makes none of them panic.
func allGetters(m Metadata) {
	m.Format()
	m.FileType()
	m.Title()
	m.Album()
	m.Artist()
	m.AlbumArtist()
	m.Composer()
	m.Year()
	m.Genre()
	m.Track()
	m.Disc()
	m.Picture()
	m.Lyrics()
	m.Comment()
	m.Raw()
}

// TestMP4GettersDoNotPanic is the regression test for the MP4 getters, which
// asserted the type they expected of a stored value. A text atom that
// declares another class, and a freeform atom named like one of the reader's
// own keys, made Title, Artist, Year, Track or Disc panic.
func TestMP4GettersDoNotPanic(t *testing.T) {
	trkn := atom("trkn", dataAtom(atomClassImplicit, []byte{0, 0, 0, 3, 0, 10, 0, 0}))
	disk := atom("disk", dataAtom(atomClassImplicit, []byte{0, 0, 0, 1, 0, 2}))
	for name, items := range map[string][][]byte{
		"title as an integer":    {atom("\xa9nam", dataAtom(atomClassInteger, []byte{7}))},
		"artist as a picture":    {atom("\xa9ART", dataAtom(atomClassJPEG, jpegData))},
		"year as an integer":     {atom("\xa9day", dataAtom(atomClassInteger, []byte{7}))},
		"comment as an integer":  {atom("\xa9cmt", dataAtom(atomClassInteger, []byte{7}))},
		"lyrics as a picture":    {atom("\xa9lyr", dataAtom(atomClassPNG, pngData))},
		"freeform trkn_count":    {freeformItem("trkn_count", "x")},
		"freeform disk_count":    {freeformItem("disk_count", "x")},
		"freeform trkn and disk": {freeformItem("trkn", "x"), freeformItem("disk", "x")},
		"freeform title":         {freeformItem("\xa9nam", "Freeform")},
		"picture as text":        {atom("covr", dataAtom(atomClassText, []byte("no picture")))},
	} {
		// The damaged item among good ones, before and after the real
		// track and disc numbers.
		for _, order := range [][][]byte{
			slices.Concat(mp4Basics(), [][]byte{trkn, disk}, items),
			slices.Concat(items, [][]byte{trkn, disk}, mp4Basics()),
		} {
			m := readFromBytes(t, name, mp4File(order...))
			allGetters(m)
			if x, n := m.Track(); x != 3 || n != 10 {
				t.Errorf("%s: Track() = %d, %d, want 3, 10", name, x, n)
			}
			if x, n := m.Disc(); x != 1 || n != 2 {
				t.Errorf("%s: Disc() = %d, %d, want 1, 2", name, x, n)
			}
			if m.Album() != "Album" {
				t.Errorf("%s: Album() = %q, want Album", name, m.Album())
			}
		}

		m := readFromBytes(t, name, mp4File(items...))
		allGetters(m)
		if m.Title() != "" || m.Artist() != "" || m.Year() != 0 || m.Comment() != "" || m.Lyrics() != "" || m.Picture() != nil {
			t.Errorf("%s: a value was read from an atom that holds none", name)
		}
	}
}

// TestMP4NestedContainers is the regression test for container atoms, which
// were read by recursion: each moov, udta, meta or ilst atom kept a stack
// frame until the end of the file, and 3,300,000 empty ones (26 MB)
// overflowed the stack, which ends the process.
func TestMP4NestedContainers(t *testing.T) {
	file := slices.Concat(mp4Ftyp, bytes.Repeat(atom("moov", nil), 3_300_000), mp4Moov(mp4Basics()...))
	checkMP4Basics(t, "after 3,300,000 empty moov atoms", readFromBytes(t, "nested", file))
}

// TestMP4DamagedAtomSize is the regression test for a tag atom whose size
// runs past the end of the file: the reader used to follow it and load the
// rest of the file, 200 MiB here, into memory.
func TestMP4DamagedAtomSize(t *testing.T) {
	head := slices.Concat(mp4Ftyp, mp4Moov(mp4Basics()...))
	i := bytes.Index(head, []byte("\xa9nam"))
	head[i-4] |= 0x80

	f := &sparseFile{head: head, size: 200 * mib}
	var err error
	n := allocated(func() { _, err = ReadFrom(f) })
	if err == nil {
		t.Error("no error for a title atom that runs past the end of the file")
	}
	if n > mib {
		t.Errorf("reading allocated %d MiB", n/mib)
	}

	// The same in a freeform atom: the size of its "data" atom.
	head = slices.Concat(mp4Ftyp, mp4Moov(mp4Basics()...))
	i = bytes.LastIndex(head, []byte("data"))
	head[i-4] |= 0x80
	f = &sparseFile{head: head, size: 200 * mib}
	var m Metadata
	n = allocated(func() { m, err = ReadFrom(f) })
	if err != nil {
		t.Fatal(err)
	}
	if n > mib {
		t.Errorf("freeform atom: reading allocated %d MiB", n/mib)
	}
	if m.Title() != "Title" || m.Raw()["MusicBrainz Album Id"] != nil {
		t.Errorf("freeform atom: read title %q and ID %v, want the title and no ID", m.Title(), m.Raw()["MusicBrainz Album Id"])
	}
}

// TestMP4TruncatedFile reads the tags of a file cut short in the audio that
// follows them, and returns without tags, as before, when the cut is in an
// atom that comes before them.
func TestMP4TruncatedFile(t *testing.T) {
	file := slices.Concat(mp4Ftyp, mp4Moov(mp4Basics()...), atom("mdat", make([]byte, 1000)))
	checkMP4Basics(t, "audio cut short", readFromBytes(t, "truncated", file[:len(file)-500]))

	file = slices.Concat(mp4Ftyp, atom("mdat", make([]byte, 1000)), mp4Moov(mp4Basics()...))
	m := readFromBytes(t, "cut before the tags", file[:len(mp4Ftyp)+500])
	if len(m.Raw()) != 0 {
		t.Errorf("cut before the tags: read %v", m.Raw())
	}
}

// TestMP4UndecodableAtomIsSkipped is the regression test for an item the
// reader cannot decode, which used to make the whole file unreadable.
func TestMP4UndecodableAtomIsSkipped(t *testing.T) {
	gif := append([]byte("GIF89a"), make([]byte, 20)...)
	bmp := append([]byte("BM"), make([]byte, 20)...)
	for name, tt := range map[string]struct {
		item    []byte
		picture string // the extension of the picture read, if any
	}{
		"implicit cover, JPEG":      {atom("covr", dataAtom(atomClassImplicit, jpegData)), "jpeg"},
		"implicit cover, PNG":       {atom("covr", dataAtom(atomClassImplicit, pngData)), "png"},
		"implicit cover, unknown":   {atom("covr", dataAtom(atomClassImplicit, []byte("what is this"))), ""},
		"GIF cover":                 {atom("covr", dataAtom(atomClassGIF, gif)), "gif"},
		"BMP cover":                 {atom("covr", dataAtom(atomClassBMP, bmp)), "bmp"},
		"empty cover":               {atom("covr", nil), ""},
		"cover with an empty value": {atom("covr", dataAtom(atomClassJPEG, nil)), ""},
		"implicit text":             {atom("\xa9gen", dataAtom(atomClassImplicit, []byte("Jazz"))), ""},
		"text of an unknown class":  {atom("\xa9gen", dataAtom(99, []byte("Jazz"))), ""},
		"track of 4 bytes":          {atom("trkn", dataAtom(atomClassImplicit, []byte{0, 0, 0, 3})), ""},
		"track of 2 bytes":          {atom("trkn", dataAtom(atomClassImplicit, []byte{0, 0})), ""},
		"item with no data atom":    {atom("\xa9gen", atom("name", []byte("x"))), ""},
		"data atom of 3 bytes":      {atom("\xa9gen", atom("data", []byte{0, 0, 1})), ""},
		"child of size 0":           {atom("\xa9gen", make([]byte, 16)), ""},
		"freeform with no name":     {atom("----", atom("mean", append([]byte{0, 0, 0, 0}, "com.apple.iTunes"...))), ""},
		"freeform with a bad child": {atom("----", []byte{0, 0, 0, 4, 'm', 'e', 'a', 'n'}), ""},
		"copyright in udta form":    {atom("cprt", []byte{0, 0, 0, 0, 0x55, 0xC4, 'C', 0}), ""},
	} {
		items := slices.Insert(mp4Basics(), 1, tt.item)
		m := readFromBytes(t, name, mp4File(items...))
		checkMP4Basics(t, name, m)
		allGetters(m)
		switch p := m.Picture(); {
		case tt.picture == "" && p != nil:
			t.Errorf("%s: Picture() = %v, want none", name, p)
		case tt.picture != "" && (p == nil || p.Ext != tt.picture || p.MIMEType != "image/"+tt.picture || len(p.Data) == 0):
			t.Errorf("%s: Picture() = %v, want a %s", name, p, tt.picture)
		}
	}

	m := readFromBytes(t, "track of 4 bytes", mp4File(atom("trkn", dataAtom(atomClassImplicit, []byte{0, 0, 0, 3}))))
	if x, n := m.Track(); x != 3 || n != 0 {
		t.Errorf("track of 4 bytes: Track() = %d, %d, want 3, 0", x, n)
	}

	// UTF-16 text, class 2, is big-endian.
	m = readFromBytes(t, "UTF-16", mp4File(atom("\xa9nam", dataAtom(atomClassUTF16, utf16Units("Títle", binary.BigEndian)))))
	if got := m.Title(); got != "Títle" {
		t.Errorf("UTF-16 title = %q, want Títle", got)
	}
}

// TestMP4MultipleValues is the regression test for a text item with several
// values, which mutagen and so Picard write as several "data" atoms: the
// reader used to return the second atom's header inside the string.
func TestMP4MultipleValues(t *testing.T) {
	m := readFromBytes(t, "multiple values", mp4File(
		textItem("\xa9ART", "First Artist", "Second Artist"),
		textItem("aART", "One"),
	))
	if got := m.Artist(); got != "First Artist" {
		t.Errorf("Artist() = %q, want the first value", got)
	}
	if got := m.Raw()["\xa9ART"]; got != "First Artist\x00Second Artist" {
		t.Errorf("Raw artist = %q, want both values, NUL-separated", got)
	}
	if got := m.AlbumArtist(); got != "One" {
		t.Errorf("AlbumArtist() = %q, want One", got)
	}
}

// TestMP4LargeAtomBeforeTags is the regression test for an atom with a
// 64-bit size (size field 1) before the tags, as the audio of a file over
// 4 GiB has when it comes before moov: the reader took the size for 1 and
// found no tags.
func TestMP4LargeAtomBeforeTags(t *testing.T) {
	audio := []byte("audio data")
	mdat := binary.BigEndian.AppendUint32(nil, 1)
	mdat = append(mdat, "mdat"...)
	mdat = binary.BigEndian.AppendUint64(mdat, uint64(16+len(audio)))
	mdat = append(mdat, audio...)

	file := slices.Concat(mp4Ftyp, mdat, mp4Moov(mp4Basics()...))
	checkMP4Basics(t, "64-bit mdat before moov", readFromBytes(t, "large", file))

	// With the tags first, and the audio running to the end of the file
	// (size field 0).
	toEOF := append(binary.BigEndian.AppendUint32(nil, 0), "mdat"...)
	file = slices.Concat(mp4Ftyp, mp4Moov(mp4Basics()...), toEOF, audio)
	checkMP4Basics(t, "mdat to the end of the file", readFromBytes(t, "to EOF", file))

	// A 64-bit size that cannot be right ends the read, and is an error
	// in a tag atom.
	bad := binary.BigEndian.AppendUint32(nil, 1)
	bad = append(bad, "\xa9nam"...)
	bad = binary.BigEndian.AppendUint64(bad, 8)
	if _, err := ReadFrom(bytes.NewReader(slices.Concat(mp4Ftyp, bad, make([]byte, 64)))); err == nil {
		t.Error("no error for a title atom whose 64-bit size is smaller than its header")
	}
	for _, size := range []uint32{2, 7} {
		bad := binary.BigEndian.AppendUint32(nil, size)
		bad = append(bad, "\xa9nam"...)
		if _, err := ReadFrom(bytes.NewReader(slices.Concat(mp4Ftyp, bad, make([]byte, 64)))); err == nil {
			t.Errorf("no error for a title atom of size %d", size)
		}
	}
}

// TestMP4FileType is the regression test for the file type of MP4 files,
// which was never set: the major brand of the ftyp atom tells it.
func TestMP4FileType(t *testing.T) {
	for brand, want := range map[string]FileType{"M4A ": M4A, "M4B ": M4B, "M4P ": M4P, "isom": UnknownFileType, "mp42": UnknownFileType} {
		file := mp4File(mp4Basics()...)
		copy(file[8:12], brand)
		m := readFromBytes(t, brand, file)
		if m.Format() != MP4 || m.FileType() != want {
			t.Errorf("brand %q: format %q, file type %q, want %q, %q", brand, m.Format(), m.FileType(), MP4, want)
		}
	}
	for name, want := range map[string]FileType{"tagged.m4a": M4A, "untagged.m4a": M4A, "tagged.m4b": M4B} {
		if m := readFromBytes(t, name, fixtureBytes(t, name)); m.FileType() != want {
			t.Errorf("%s: file type %q, want %q", name, m.FileType(), want)
		}
	}
}

// TestMP4TrackAndDiscNumbers is the regression test for track and disc
// numbers, which are 16 bits: the reader took the low byte, so track 300 of
// 400 read as 44 of 144.
func TestMP4TrackAndDiscNumbers(t *testing.T) {
	m := readFromBytes(t, "track 300", mp4File(
		atom("trkn", dataAtom(atomClassImplicit, []byte{0, 0, 0x01, 0x2C, 0x01, 0x90, 0, 0})),
		atom("disk", dataAtom(atomClassImplicit, []byte{0, 0, 0x01, 0x01, 0x01, 0x02})),
	))
	if x, n := m.Track(); x != 300 || n != 400 {
		t.Errorf("Track() = %d, %d, want 300, 400", x, n)
	}
	if x, n := m.Disc(); x != 257 || n != 258 {
		t.Errorf("Disc() = %d, %d, want 257, 258", x, n)
	}
	raw := m.Raw()
	if raw["trkn"] != 300 || raw["trkn_count"] != 400 || raw["disk"] != 257 || raw["disk_count"] != 258 {
		t.Errorf("Raw = %v, want the same numbers", raw)
	}
}

// TestMP4ArtistAtomOrder is the regression test for a file with both "©ART"
// and "©art", whose artist depended on the order of a map.
func TestMP4ArtistAtomOrder(t *testing.T) {
	file := mp4File(textItem("\xa9art", "Lower"), textItem("\xa9ART", "Upper"))
	for range 50 {
		if got := readFromBytes(t, "both artist atoms", file).Artist(); got != "Upper" {
			t.Fatalf("Artist() = %q, want that of ©ART on every read", got)
		}
	}
	if got := atoms.Name("artist"); !slices.Equal(got, []string{"\xa9ART", "\xa9art"}) {
		t.Errorf("atoms.Name(artist) = %q", got)
	}
}

// TestMP4iTunesForm reads an M4A file tagged the way iTunes tags one: text,
// track and disc numbers, the compilation flag, the tempo, a JPEG cover and
// the MusicBrainz IDs Picard adds as freeform atoms. It is built here to the
// format; no file iTunes wrote was at hand.
func TestMP4iTunesForm(t *testing.T) {
	for name, cover := range map[string][]byte{
		"JPEG cover": atom("covr", dataAtom(atomClassJPEG, jpegData)),
		"PNG cover":  atom("covr", dataAtom(atomClassPNG, pngData)),
	} {
		m := readFromBytes(t, name, mp4File(
			textItem("\xa9nam", "Silence Café"),
			textItem("\xa9ART", "Artist"),
			textItem("aART", "Album Artist"),
			textItem("\xa9alb", "Album"),
			textItem("\xa9day", "2001-12-05T08:00:00Z"),
			textItem("\xa9gen", "Jazz"),
			textItem("\xa9wrt", "Composer"),
			textItem("\xa9cmt", "A comment"),
			textItem("\xa9lyr", "Lyrics"),
			atom("trkn", dataAtom(atomClassImplicit, []byte{0, 0, 0, 3, 0, 10, 0, 0})),
			atom("disk", dataAtom(atomClassImplicit, []byte{0, 0, 0, 1, 0, 2})),
			atom("cpil", dataAtom(atomClassInteger, []byte{1})),
			atom("tmpo", dataAtom(atomClassInteger, []byte{0, 120})),
			cover,
			freeformItem("MusicBrainz Album Id", "album-id"),
			freeformItem("iTunNORM", " 00000000 00000000"),
		))
		track, tracks := m.Track()
		disc, discs := m.Disc()
		if m.Format() != MP4 || m.FileType() != M4A || m.Title() != "Silence Café" || m.Artist() != "Artist" ||
			m.AlbumArtist() != "Album Artist" || m.Album() != "Album" || m.Year() != 2001 || m.Genre() != "Jazz" ||
			m.Composer() != "Composer" || m.Comment() != "A comment" || m.Lyrics() != "Lyrics" ||
			track != 3 || tracks != 10 || disc != 1 || discs != 2 {
			t.Errorf("%s: read %s %s %q / %q / %q / %q, year %d, genre %q, composer %q, comment %q, lyrics %q, track %d/%d, disc %d/%d",
				name, m.Format(), m.FileType(), m.Title(), m.Artist(), m.AlbumArtist(), m.Album(), m.Year(), m.Genre(),
				m.Composer(), m.Comment(), m.Lyrics(), track, tracks, disc, discs)
		}
		raw := m.Raw()
		if raw["cpil"] != 1 || raw["tmpo"] != 120 || raw["MusicBrainz Album Id"] != "album-id" {
			t.Errorf("%s: cpil %v, tmpo %v, album ID %v, want 1, 120, album-id", name, raw["cpil"], raw["tmpo"], raw["MusicBrainz Album Id"])
		}
		want := jpegData
		ext := "jpeg"
		if name == "PNG cover" {
			want, ext = pngData, "png"
		}
		if p := m.Picture(); p == nil || p.Ext != ext || p.MIMEType != "image/"+ext || !bytes.Equal(p.Data, want) {
			t.Errorf("%s: Picture() = %v", name, p)
		}
	}
}
