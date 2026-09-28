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

- `sum.go`: `Sum` hashes only the audio frames of MP3 files, so adding, editing or removing ID3v2 or ID3v1 tags keeps the hash. `SumID3v2` used to seek back to byte 0 and hash the ID3v2 tag too. It also skipped an extended header twice and ignored the 10-byte v2.4 footer (flag `0x10`). It now skips `10 + synchsafe size`, plus 10 for a footer. `SumID3v1` and `SumID3v2` used to drop the last 128 bytes whether or not they were a tag. Now they drop them only when they start with `TAG`. Every MP3 sum changes once. The fallback for formats `Sum` does not recognise (WAV, WMA, APE, MPC, .aac without ID3v2) goes through `SumID3v1`, so those now hash to the end of the file too. FLAC, OGG and MP4 sums are unchanged. Tests: `sum_retag_test.go`.
- `sum.go`: `Sum` hashes only the audio of OGG files and of every MP4 brand, so a retag keeps the hash. MP4 used to take the atom path only for brand `M4A `, and OGG always hashed the whole file. The new `SumOGG` hashes every packet except the Vorbis comment and `OpusTags` headers, so page sequence numbers, CRCs and page boundaries, which shift when the comment changes size, do not count. `SumAtoms` also reads 64-bit atom sizes (size field 1) and an `mdat` that runs to the end of the file (size field 0), which large M4B files use. FLAC, MP3 and M4A sums are unchanged. Tests: `sum_retag_test.go`.
- `mbz`: `Extract` returns an `IDs` struct with named fields (`RecordingID`, `ReleaseTrackID`, `AlbumID`, `ReleaseGroupID`, `ArtistIDs`, `AlbumArtistIDs`) in place of `Info` and its string keys. The old `Track` / `Recording` keys held different IDs per format. Now UFID, MP4 `MusicBrainz Track Id` and Vorbis `MUSICBRAINZ_TRACKID` are the recording, and `MusicBrainz Release Track Id` / `MUSICBRAINZ_RELEASETRACKID` the release track, as Picard writes them. Key names and TXXX descriptions match case-insensitively (TagLib writes them upper case). Values are split on NUL, `/` and `;`; the single-valued IDs keep the first part. Raw tags are read in sorted key order, so the same key wins on every run. The Vorbis reader still keeps only the last of repeated keys, so FLAC/Ogg artist ID lists hold one ID until that reader fix. `MUSICBRAINZ_RECORDINGID` is also read as the recording. The AcoustID, disc ID and TRM keys are no longer read. Tests: `mbz/mbz_test.go`.
- `mp4.go`: freeform (`----`) values skip the 4-byte locale of their `data` atom; they used to start with four NULs. A `data` atom too short for those 8 bytes is skipped. Test: `mp4_freeform_test.go`.
- Import paths rewritten from `github.com/dhowden/tag` to `tagshelf/internal/tag`.
