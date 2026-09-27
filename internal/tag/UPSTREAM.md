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

- Import paths rewritten from `github.com/dhowden/tag` to `tagshelf/internal/tag`.
