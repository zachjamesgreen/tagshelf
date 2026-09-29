# internal/tag

A local copy of [github.com/dhowden/tag](https://github.com/dhowden/tag), an MP3 (ID3v1, ID3v2.2/2.3/2.4), MP4 (AAC, M4A, ALAC), OGG, FLAC and DSF metadata parsing library. Upstream has been inactive since 2024, so tagshelf carries the code itself and changes it as needed.

## Source

- Repository: https://github.com/dhowden/tag
- Commit: `3d75831295e8` (2024-04-17)
- License: BSD 2-Clause, Copyright 2015 David Howden (see `LICENSE`, which must stay with this code)

The commit "Import github.com/dhowden/tag at 3d75831 verbatim" holds the unmodified upstream files, so local changes can always be reviewed with:

```console
git diff <that commit> -- internal/tag
```

Dropped from upstream: `cmd/` (demo CLIs), `go.mod`, `go.sum`, `README.md` (replaced by this file) and the upstream CI/editor files (`.github/`, `.travis.yml`, `.editorconfig`).

## Usage

Detect and parse tag metadata from an `io.ReadSeeker` (i.e. an `*os.File`):

```go
m, err := tag.ReadFrom(f)
if err != nil {
	log.Fatal(err)
}
log.Print(m.Format()) // The detected format.
log.Print(m.Title())  // The title of the track (see Metadata interface for more details).
```

Parsed metadata is exported via a single `Metadata` interface, giving a consistent API for all supported formats. `Raw()` exposes the underlying tag names, which are not consistent across formats. `Sum` computes a metadata-invariant SHA1 checksum of the audio data only. The `mbz` subpackage extracts MusicBrainz identifiers from the raw tags.

## Local changes

Changes made after the import, newest first:

- `asfwrite.go`: new `WriteASFAttribute` copies a WMA (ASF) file with one text attribute set in the Extended Content Description Object, for the manual marker, which go-taglib drops. It removes every attribute of that name (case-insensitively) from each ECD object, appends the new one to the first, and creates the object (adding 1 to the header count) when there is none. Only the Header Object changes: a Padding Object with room absorbs the size change, or else the header grows or shrinks and the Data Object and index objects are copied after it byte for byte. The ECD size and count, the Header Object size and the File Properties File Size (set to the new file's real length, as TagLib and mutagen leave it stale) are rewritten; File ID is left alone. Files with a Digital Signature Object, the Broadcast flag or a Data Object size of 0 are refused with `ErrASFRefused`, and nothing is written for any file that does not check out. Tests: `asfwrite_test.go`.
- `vorbis.go`: a FLAC or OGG comment key written more than once keeps every value. `Raw` joins them with NUL in the order they appear, as the ASF reader does, so `mbz` reads every artist and album artist ID Picard or go-taglib writes as repeated `MUSICBRAINZ_ARTISTID` comments; it used to keep only the last. `Title`, `Artist`, `Year` and the other getters return the first value where they used to return the last. Tests: `vorbis_test.go`, and the multi-valued artist ID round trip in `internal/tagwrite`.
- `wav.go`, `apev2.go`, `sum.go`, `tag.go`: WAV, APE, MPC and .aac (ADTS) files are read, and `Sum` hashes only their audio, so a retag keeps it. `ReadWAVTags` reads the ID3v2 tag in an `ID3 ` or `id3 ` chunk with `ReadID3v2Tags`, and takes each field it lacks (title, artist, album, year, track, genre, comment) from a LIST/INFO chunk, decoded as UTF-8 or else Windows-1252; with INFO alone the format is the new `INFO`. `ReadAPEv2Tags` reads the APEv2 tag at the end of the file (new format `APEv2`), also when ID3v1 tags sit after it, before it (as mutagen leaves them) or both. Keys match case-insensitively and `Raw` keeps each key's own case, so `mbz` reads Picard's MusicBrainz keys. An APE or MPC file with a leading ID3v2 tag reads its APEv2 tag, since TagLib never updates that ID3v2 tag; it used to read the stale ID3v2 values. An .aac file with a leading ID3v2 tag still reads it, as go-taglib writes .aac that way. New file types `WAV`, `APE`, `MPC` and `AAC`. `SumWAV` hashes the `data` chunk payload without its pad byte (to the end of the file when its size is unknown); `Sum` used to hash whole WAV files. For every other file hashed from the start or after a leading ID3v2 tag (MP3, APE, MPC, AAC), `Sum` now leaves out a trailing APEv2 tag and an ID3v1 tag on either side of it; it used to leave out only a final ID3v1 tag, so the APEv2 tag counted. WAV, APE, MPC and .aac sums change once, as do MP3 sums of files with an APEv2 tag. `Identify` does not know the new formats. Lyrics3v2 tags are not skipped. Tests: `wav_test.go`, `apev2_test.go`, `sum_retag_test.go`, `mbz/mbz_test.go`.
- `asf.go`, `tag.go`, `id.go`, `sum.go`, `mbz`: WMA (ASF) files are read. `ReadFrom` and `Identify` report format `ASF` and file type `WMA`; they used to return `ErrNoTagsFound`. The reader takes title and artist from the Content Description Object (Raw names `Title`, `Author`, `Copyright`, `Description`, `Rating`) and every attribute of the Extended Content Description Object and of the Metadata and Metadata Library objects in the Header Extension, including `TAGSHELF_MANUAL`. Attribute names match case-insensitively. Raw values are strings (text; WORD, DWORD and QWORD as decimal; BOOL as `true`/`false`; GUIDs as `XXXXXXXX-XXXX-…`), with an attribute written more than once joined with NUL and a value repeated verbatim kept once; byte arrays are `[]byte`, the first only. `WM/TrackNumber` may be text (`N` or `N/M`) or a DWORD, with the zero-based `WM/Track` as a fallback; `WM/PartOfSet` gives the disc; `WM/Year` reads its leading year; `WM/Picture` is the picture. `mbz` reads Picard's ASF names (`MusicBrainz/Track Id` is the recording, `MusicBrainz/Release Track Id` the release track, and `MusicBrainz/Album Id`, `Release Group Id`, `Artist Id`, `Album Artist Id`). `Sum` hashes only the Data Object's packets (from 50 bytes into it to its end, or to the end of the file for a size of 0), so a retag keeps the hash; it used to hash the whole file. WMA files with MusicBrainz IDs now qualify for the library, and every WMA sum changes once. Tests: `asf_test.go`, `mbz/mbz_test.go`.
- `vorbis.go`, `id3v2metadata.go`: FLAC and OGG `TRACKNUMBER` and `DISCNUMBER` written as `N/M` read as N, with M as the total unless `TRACKTOTAL` / `DISCTOTAL` is set; they used to read as 0. ID3v2 years written as an ISO 8601 timestamp other than `yyyy` or `yyyy-MM-dd`, such as the v2.4 `TDRC` values `1997-05` and `1997-05-12T10:30`, read as their year; they used to read as 0. MP3 albums tagged that way gain a `(Year)` folder on the next reorganize. Tests: `vorbis_test.go`, `id3v2metadata_test.go`.
- `sum.go`: `Sum` hashes only the audio frames of MP3 files, so adding, editing or removing ID3v2 or ID3v1 tags keeps the hash. `SumID3v2` used to seek back to byte 0 and hash the ID3v2 tag too. It also skipped an extended header twice and ignored the 10-byte v2.4 footer (flag `0x10`). It now skips `10 + synchsafe size`, plus 10 for a footer. `SumID3v1` and `SumID3v2` used to drop the last 128 bytes whether or not they were a tag. Now they drop them only when they start with `TAG`. Every MP3 sum changes once. The fallback for formats `Sum` does not recognise (WAV, WMA, APE, MPC, .aac without ID3v2) goes through `SumID3v1`, so those now hash to the end of the file too. FLAC, OGG and MP4 sums are unchanged. Tests: `sum_retag_test.go`.
- `sum.go`: `Sum` hashes only the audio of OGG files and of every MP4 brand, so a retag keeps the hash. MP4 used to take the atom path only for brand `M4A `, and OGG always hashed the whole file. The new `SumOGG` hashes every packet except the Vorbis comment and `OpusTags` headers, so page sequence numbers, CRCs and page boundaries, which shift when the comment changes size, do not count. `SumAtoms` also reads 64-bit atom sizes (size field 1) and an `mdat` that runs to the end of the file (size field 0), which large M4B files use. FLAC, MP3 and M4A sums are unchanged. Tests: `sum_retag_test.go`.
- `mbz`: `Extract` returns an `IDs` struct with named fields (`RecordingID`, `ReleaseTrackID`, `AlbumID`, `ReleaseGroupID`, `ArtistIDs`, `AlbumArtistIDs`) in place of `Info` and its string keys. The old `Track` / `Recording` keys held different IDs per format. Now UFID, MP4 `MusicBrainz Track Id` and Vorbis `MUSICBRAINZ_TRACKID` are the recording, and `MusicBrainz Release Track Id` / `MUSICBRAINZ_RELEASETRACKID` the release track, as Picard writes them. Key names and TXXX descriptions match case-insensitively (TagLib writes them upper case). Values are split on NUL, `/` and `;`; the single-valued IDs keep the first part. Raw tags are read in sorted key order, so the same key wins on every run. The Vorbis reader still keeps only the last of repeated keys, so FLAC/Ogg artist ID lists hold one ID until that reader fix. `MUSICBRAINZ_RECORDINGID` is also read as the recording. The AcoustID, disc ID and TRM keys are no longer read. Tests: `mbz/mbz_test.go`.
- `mp4.go`: freeform (`----`) values skip the 4-byte locale of their `data` atom; they used to start with four NULs. A `data` atom too short for those 8 bytes is skipped. Test: `mp4_freeform_test.go`.
- Import paths rewritten from `github.com/dhowden/tag` to `tagshelf/internal/tag`.
