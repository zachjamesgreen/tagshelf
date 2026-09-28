# Choosing a tag-writing library

Researched: 2026-09-27

Question (ticket 03): How can tagshelf write tags to MP3 (ID3v2), FLAC and OGG (Vorbis comments), and M4A (MP4 atoms)? That includes custom fields: TXXX frames, arbitrary Vorbis keys and MP4 freeform (`----`) atoms. Does the answer change whether `internal/tag` should stay the reader?

The TUI needs to write artist, album artist, album, title, year, track, disc, the MusicBrainz IDs, and a custom "manually tagged" marker.

Method: I read the candidates' source code as published on the Go module proxy (`proxy.golang.org`), their `go.mod` files, READMEs and issue trackers, and TagLib's C++ source at the exact version go-taglib bundles. I also ran a small throwaway spike on this Windows machine (Go 1.27.1, `CGO_ENABLED=0`). It wrote tags with go-taglib to copies of `internal/tag/testdata`, then read them back with tagshelf's own `internal/tag` and `internal/tag/mbz`. Results from the spike are marked **[spike]**. The spike code is not in the repo, and the last section says how to reproduce it.

## Summary

**Recommendation: use [go-taglib](https://github.com/sentriz/go-taglib) (`go.senan.xyz/taglib` v0.14.0) as the only tag writer, for all four formats. Keep `internal/tag` as the reader.**

- It is the only candidate that writes all four formats, including custom fields, through one API. It needs no cgo: TagLib runs as an embedded Wasm module under wazero.
- It is actively maintained. Its last release was 2026-07-24, and the repo was pushed 2026-09-26.
- In the spike on Windows it kept embedded art, left the FLAC and M4A audio checksums and the MP3 audio frames byte-identical, and handled a non-ASCII path. A write took about 5 to 23 ms.
- It needs three pieces of tagshelf-side work:
  1. Raise `go.mod` from `go 1.21` to `go 1.25`.
  2. Wrap every write in a copy, write, verify, rename sequence. TagLib rewrites the file in place, which is not atomic.
  3. Accept that on MP3, TagLib writes MusicBrainz TXXX descriptions in UPPERCASE (`MUSICBRAINZ ALBUM ARTIST ID`), where Picard writes `MusicBrainz Album Artist Id`.

The research also found **three existing bugs in `internal/tag`**. The first one already affects tagshelf today:

1. M4A freeform values come back with 4 leading NUL bytes. This breaks MusicBrainz ID reads from Picard-tagged M4A files, including `isCompilation`'s Various Artists MBID check.
2. `mbz` matches TXXX descriptions case-sensitively.
3. Vorbis `TRACKNUMBER=3/12` reads as 0.

## Candidates at a glance

| | go-taglib | bogem/id3v2 + go-flac/flacvorbis + an OGG lib + go-mp4tag | Extend `internal/tag` |
|---|---|---|---|
| Formats written | MP3, FLAC, OGG, M4A, plus WAV, WMA and more ([README](https://github.com/sentriz/go-taglib/blob/v0.14.0/README.md)) | One library per format. There is no mature OGG writer (see below). | None today. Upstream is "metadata detection, parsing and artwork extraction" only ([upstream README](https://github.com/dhowden/tag/blob/master/README.md)). |
| Custom fields | Any key (TXXX / Vorbis key / `----:com.apple.iTunes:KEY`) | Yes in each library, but each has a different API | Would have to be built |
| cgo | No: Wasm via wazero ([README](https://github.com/sentriz/go-taglib/blob/v0.14.0/README.md)) | No | No |
| Minimum Go | 1.25.0 ([go.mod](https://proxy.golang.org/go.senan.xyz/taglib/@v/v0.14.0.mod)) | 1.13 / 1.20 / 1.21.6 | 1.21 (current) |
| Write safety | In-place rewrite (TagLib `FileStream::insert`) | Varies: temp + rename (id3v2), in-place (go-flac), truncate-then-copy (go-mp4tag) | Whatever we build |
| Maintenance | Active: v0.14.0 on 2026-07-24, pushed 2026-09-26 | id3v2: last release 2023-02, last commit 2023-05. go-mp4tag: repo gone (404). | On us |
| License | LGPL-2.1 (wrapper), TagLib LGPL/MPL | MIT / BSD-ish | BSD-2 (existing) |

## go-taglib (recommended)

### What it is

- A Go wrapper around TagLib compiled to Wasm. "By packaging an embedded **Wasm** binary, the library needs no external dependencies or CGo." It bundles TagLib v2.1.1. ([README](https://github.com/sentriz/go-taglib/blob/v0.14.0/README.md))
- The `.wasm` file is embedded with `//go:embed taglib.wasm` and runs on `github.com/tetratelabs/wazero`. ([taglib.go L14-20](https://github.com/sentriz/go-taglib/blob/v0.14.0/taglib.go#L14-L20))
- Latest version is v0.14.0 (2026-07-24). There are 23 tagged versions from v0.1.1 on. ([proxy list](https://proxy.golang.org/go.senan.xyz/taglib/@v/list), [@latest](https://proxy.golang.org/go.senan.xyz/taglib/@latest))
- Latest TagLib is v2.3.2 (2026-09-05) ([GitHub API, releases/latest](https://github.com/taglib/taglib/releases/latest)), so the bundled 2.1.1 lags a little. An upstream PR to bump TagLib to 2.2 is closed ([#19](https://github.com/sentriz/go-taglib/pull/19)).

### API

- `ReadTags(path) (map[string][]string, error)` and `WriteTags(path, map[string][]string, opts)` read and write tags. The only option is `Clear`, which removes existing tags not present in the map. `ReadProperties`, `ReadImage` and `WriteImage`/`WriteImageOptions` cover properties and art. ([README](https://github.com/sentriz/go-taglib/blob/v0.14.0/README.md), [taglib.go L137-346](https://github.com/sentriz/go-taglib/blob/v0.14.0/taglib.go#L137-L346))
- Tags are multi-valued. Keys use TagLib's normalised property names, with exported constants such as `taglib.AlbumArtist`, `taglib.MusicBrainzAlbumArtistID` and `taglib.MusicBrainzTrackID`. The README says "Non-standard allowed too". ([taglib.go L26-135](https://github.com/sentriz/go-taglib/blob/v0.14.0/taglib.go#L26-L135))
- The API is path-based, not `io.ReadSeeker`-based. For each call it instantiates a module that mounts only the file's parent directory: read-only for reads, read-write for writes ([taglib.go L401-431](https://github.com/sentriz/go-taglib/blob/v0.14.0/taglib.go#L401-L431)). Paths are passed through `filepath.ToSlash` ([L663-665](https://github.com/sentriz/go-taglib/blob/v0.14.0/taglib.go#L663-L665)).
- `WriteTags` is a thin shim over TagLib:
  1. `FileRef file(filename)`
  2. `properties = file.properties()`, cleared only if `Clear` is set
  3. `properties.replace(key, values)` for each key, or `erase` if the value is empty
  4. `file.setProperties(properties)`
  5. `file.save()`

  ([taglib.cpp L70-97](https://github.com/sentriz/go-taglib/blob/v0.14.0/taglib.cpp#L70-L97))

### Custom fields per format

TagLib's property map decides how a key is stored. `PropertyMap` uppercases every key on insert and replace ([tpropertymap.cpp L64-81](https://github.com/taglib/taglib/blob/v2.1.1/taglib/toolkit/tpropertymap.cpp#L64-L81)).

- **MP3 / ID3v2**
  - Standard keys map to frames (`ALBUMARTIST`→`TPE2`, `TRACKNUMBER`→`TRCK`, `DISCNUMBER`→`TPOS`, `DATE`→`TDRC`, …) ([id3v2frame.cpp L141-226](https://github.com/taglib/taglib/blob/v2.1.1/taglib/mpeg/id3v2/id3v2frame.cpp#L141-L226)).
  - `MUSICBRAINZ_TRACKID` with a single value becomes a `UFID` frame with owner `http://musicbrainz.org` ([id3v2framefactory.cpp L607-610](https://github.com/taglib/taglib/blob/v2.1.1/taglib/mpeg/id3v2/id3v2framefactory.cpp#L607-L610)). That matches what `mbz` reads as `musicbrainz_recordingid` ([mbz.go](../../internal/tag/mbz/mbz.go)).
  - Any other unknown key becomes a TXXX frame whose description is the key ([id3v2framefactory.cpp L635-638](https://github.com/taglib/taglib/blob/v2.1.1/taglib/mpeg/id3v2/id3v2framefactory.cpp#L635-L638)). For example, `TAGSHELF_MANUAL` becomes `TXXX:TAGSHELF_MANUAL` **[spike]**.
  - **Caveat:** MusicBrainz keys are translated to TXXX descriptions from a table written in upper case, such as `"MUSICBRAINZ ALBUM ARTIST ID"` ([textidentificationframe.cpp L144-157, L460-468](https://github.com/taglib/taglib/blob/v2.1.1/taglib/mpeg/id3v2/frames/textidentificationframe.cpp#L144-L157)). Picard writes and reads the mixed-case `MusicBrainz Album Artist Id`, and it matches those descriptions case-sensitively ([picard/formats/id3.py `__translate_freetext` and `_load_txxx_frame`](https://github.com/metabrainz/picard/blob/master/picard/formats/id3.py)).
  - So TXXX MusicBrainz frames that tagshelf writes on MP3 will not be recognised as MusicBrainz IDs by Picard or by today's `mbz`. The spike confirmed that `mbz.Extract` returned only the recording ID for MP3s written this way **[spike]**. TagLib itself reads them case-insensitively (`txxxToKey` uppercases), so go-taglib reads them back fine **[spike]**. TagLib master still has the same table ([master textidentificationframe.cpp](https://github.com/taglib/taglib/blob/master/taglib/mpeg/id3v2/frames/textidentificationframe.cpp)).
  - Because keys are uppercased before they reach TagLib, go-taglib has no way to choose the TXXX description's case. An open PR would add raw ID3v2 frame access ([#14 "ReadID3v2Frames, ReadID3v1Frames, WriteID3v2Frames"](https://github.com/sentriz/go-taglib/pull/14)).
  - An existing frame whose value is unchanged is left untouched, including its original casing. `setProperties` only deletes frames whose properties are not in the new map ([id3v2tag.cpp L426-470](https://github.com/taglib/taglib/blob/v2.1.1/taglib/mpeg/id3v2/id3v2tag.cpp#L426-L470)).
- **FLAC and OGG / Vorbis comments:** keys are written as-is (uppercased). `MUSICBRAINZ_ALBUMARTISTID`, `MUSICBRAINZ_ALBUMID`, `MUSICBRAINZ_TRACKID` and `TAGSHELF_MANUAL` all round-tripped through `internal/tag`/`mbz` **[spike]**. These are the same keys Picard uses.
- **M4A / MP4**
  - Known keys map to atoms. The MusicBrainz ones map to freeform atoms with Picard's exact mixed-case names, such as `----:com.apple.iTunes:MusicBrainz Album Artist Id` ([mp4itemfactory.cpp L367-376](https://github.com/taglib/taglib/blob/v2.1.1/taglib/mp4/mp4itemfactory.cpp#L367-L376)).
  - Any other key starting with A–Z becomes `----:com.apple.iTunes:KEY` ([mp4itemfactory.cpp L247-263](https://github.com/taglib/taglib/blob/v2.1.1/taglib/mp4/mp4itemfactory.cpp#L247-L263)). All of these round-tripped **[spike]**. They were read with the NUL-prefix bug described under "Consequences for `internal/tag`".
  - When atoms change size, TagLib updates the `stco`/`co64` chunk offsets ([mp4tag.cpp L162-200](https://github.com/taglib/taglib/blob/v2.1.1/taglib/mp4/mp4tag.cpp#L162)).

### Safety

- **Not atomic.**
  - `MPEG::File::save` writes the new ID3v2 tag with `insert(data, location, originalSize)` ([mpegfile.cpp L240-241](https://github.com/taglib/taglib/blob/v2.1.1/taglib/mpeg/mpegfile.cpp#L240-L241)).
  - `FileStream::insert` rewrites the open file in place. When the tag grows, it shifts the rest of the file through a buffer ([tfilestream.cpp L249-300](https://github.com/taglib/taglib/blob/v2.1.1/taglib/toolkit/tfilestream.cpp#L249)).
  - A crash or power loss mid-shift can leave a damaged file. tagshelf has to supply atomicity itself (see the recommendation).
- **Preserves other frames and art.**
  - ID3v2 frames that don't map to a property (for example `APIC`, `PRIV`, or unknown frames) produce a property map with only "unsupported data" entries ([id3v2frame.cpp L363-374](https://github.com/taglib/taglib/blob/v2.1.1/taglib/mpeg/id3v2/id3v2frame.cpp#L363-L374)).
  - `PropertyMap::contains` on such an empty map is trivially true ([tpropertymap.cpp L98-102](https://github.com/taglib/taglib/blob/v2.1.1/taglib/toolkit/tpropertymap.cpp#L98-L102)), so `setProperties` keeps those frames.
  - In the spike, art embedded with `WriteImage` survived a following `WriteTags` in all 9 test files **[spike]**.
- **Audio untouched.**
  - `tag.Sum` was unchanged for FLAC and M4A.
  - For MP3, the bytes between the ID3v2 tag and the ID3v1 tag were identical before and after 50 successive writes.
  - For OGG, `tag.Sum` changes, because `SumAll` hashes the whole file ([sum.go](../../internal/tag/sum.go)) and Ogg pages get rewritten. That is not a sign of damage.

  **[spike]**
- **Side effects on MP3:**
  - `save()` is `save(AllTags)` with defaults `StripOthers`, `ID3v2::v4` and `Duplicate` ([mpegfile.h L212-229](https://github.com/taglib/taglib/blob/v2.1.1/taglib/mpeg/mpegfile.h#L212-L229)). With `Duplicate`, if an ID3v2 tag exists, its content is copied into a newly created ID3v1 tag ([mpegfile.cpp L214-224](https://github.com/taglib/taglib/blob/v2.1.1/taglib/mpeg/mpegfile.cpp#L214-L224)).
  - So every MP3 write **appends a 128-byte ID3v1 tag** and **upgrades ID3v2.3 files to ID3v2.4**. The spike showed both on `sample.id3v23.mp3` **[spike]**.
  - This is harmless for tagshelf, whose reader prefers ID3v2. But it does change `tag.Sum` for MP3, because `SumID3v2` always drops the last 128 bytes ([sum.go L138-165](../../internal/tag/sum.go)). Don't use `tag.Sum` to compare files before and after a write.
- **Error handling:**
  - A read-only file returns `ErrSavingFile` **[spike]**.
  - Writing to a non-audio file named `.mp3` returned **no error** **[spike]**, because TagLib picks the parser by extension. Detect the format with `internal/tag` before writing.
  - Files with non-UTF-8 text (for example Latin-1 Vorbis comments) currently make ReadTags/WriteTags fail with an error. This is open as [#29](https://github.com/sentriz/go-taglib/issues/29), with a fix in open PR [#30](https://github.com/sentriz/go-taglib/pull/30).
  - An older crash ([#15](https://github.com/sentriz/go-taglib/issues/15)) was a wazero/Go-version mismatch, fixed in v0.7.1.
- **Concurrency:** the README says "Safe for concurrent use" ([README](https://github.com/sentriz/go-taglib/blob/v0.14.0/README.md)).

### Windows, cgo, Go version

- **No cgo.** The spike built and ran with `CGO_ENABLED=0` on Windows 11 **[spike]**.
- Upstream CI only runs on `ubuntu-latest` ([test.yaml](https://github.com/sentriz/go-taglib/blob/v0.14.0/.github/workflows/test.yaml)), so Windows support is only as tested as the spike. The spike covered drive-letter paths and a directory named `Björk – Ünïcødé 日本`, and both work **[spike]**.
- **Go 1.25.0 minimum** ([go.mod](https://proxy.golang.org/go.senan.xyz/taglib/@v/v0.14.0.mod)). Older lines (v0.6.0, v0.10.0) need Go 1.23 ([v0.6.0 .mod](https://proxy.golang.org/go.senan.xyz/taglib/@v/v0.6.0.mod), [v0.10.0 .mod](https://proxy.golang.org/go.senan.xyz/taglib/@v/v0.10.0.mod)), so no usable version keeps tagshelf on `go 1.21`. `go get` rewrote the directive to `go 1.25.0` automatically **[spike]**. The installed toolchain is Go 1.27.1, so this costs nothing locally.
- The dependencies are `github.com/tetratelabs/wazero` (a pseudo-version) and `golang.org/x/sys` ([go.mod](https://proxy.golang.org/go.senan.xyz/taglib/@v/v0.14.0.mod)).

### Performance

- Upstream benchmark on Linux: read 0.30 ms, write 1.85 ms ([README](https://github.com/sentriz/go-taglib/blob/v0.14.0/README.md#performance)).
- **[spike]** on this Windows machine:
  - Writes took 5 to 9 ms for the MP3, FLAC and M4A samples and about 22 ms for OGG.
  - A 3.2 MB MP3 averaged 7.3 ms per write and 0.6 ms per read over 50 iterations.
  - The first call in a process took 151 ms with a cold compilation cache and 12 ms warm. The compiled module is cached under `%TEMP%\go-taglib-wasm` ([taglib.go L353-395](https://github.com/sentriz/go-taglib/blob/v0.14.0/taglib.go#L353-L395)).
  - A minimal binary using it is 8.5 MB; the wasm blob itself is 388 KB.
- That is fast enough for a TUI that writes one album at a time.

## bogem/id3v2 plus per-format libraries (rejected)

- **MP3: `github.com/bogem/id3v2/v2`** v2.1.4, `go 1.13` ([go.mod](https://proxy.golang.org/github.com/bogem/id3v2/v2/@v/v2.1.4.mod))
  - It is now hosted at `n10v/id3v2`. The last release was 2023-02-09, the last commit 2023-05-09, and there are 24 open issues ([GitHub API](https://github.com/n10v/id3v2)).
  - It supports TXXX with any description (`UserDefinedTextFrame{Description, Value}`) and `UFIDFrame` ([user_defined_text_frame.go](https://github.com/n10v/id3v2/blob/v2.1.4/user_defined_text_frame.go), [ufid_frame.go](https://github.com/n10v/id3v2/blob/v2.1.4/ufid_frame.go)). That means it *can* write Picard-cased descriptions.
  - Only ID3v2.3 and v2.4 are supported: parsing a v2.2 tag returns `ErrUnsupportedVersion` ([parse.go L41-43](https://github.com/n10v/id3v2/blob/v2.1.4/parse.go#L41-L43)).
  - `Save()` writes to `<file>-id3v2` in the same directory and then `os.Rename`s it over the original, without fsync ([tag.go L321-381](https://github.com/n10v/id3v2/blob/v2.1.4/tag.go#L321-L381)). That is the safest save of all the candidates.
- **FLAC: `github.com/go-flac/go-flac/v2`** v2.0.4 (2025-08-06) and **`github.com/go-flac/flacvorbis/v2`** v2.0.2 (2024-08-02), both `go 1.20` ([go-flac .mod](https://proxy.golang.org/github.com/go-flac/go-flac/v2/@v/v2.0.4.mod), [flacvorbis .mod](https://proxy.golang.org/github.com/go-flac/flacvorbis/v2/@v/v2.0.2.mod)). Saving over the source file goes through `saveInPlace` ([flacfile.go L48-114](https://github.com/go-flac/go-flac/blob/v2.0.4/flacfile.go#L48)).
- **OGG:** there is no established pure-Go Vorbis comment writer.
  - The best match found, `github.com/gcottom/oggmeta`, is at v0.0.8. It was last pushed 2024-12-29 and has 1 star ([proxy](https://proxy.golang.org/github.com/gcottom/oggmeta/@latest), [repo](https://github.com/gcottom/oggmeta)).
  - Rewriting Vorbis comments means re-paging the Ogg stream with new CRCs and page sequence numbers.
- **M4A: `github.com/Sorrow446/go-mp4tag`** has only a pseudo-version (2024-01-30), and its GitHub repo now returns 404 ([proxy](https://proxy.golang.org/github.com/!sorrow446/go-mp4tag/@latest)).
  - It supports custom freeform tags (`Custom map[string]string`).
  - It writes to a temp file in `os.TempDir()` and then copies it back with `os.Create(dest)` and `io.Copy` ([utils.go L47-78](https://proxy.golang.org/github.com/!sorrow446/go-mp4tag/@v/v0.0.0-20240130220823-68ce31d53e37.zip)). That truncates the original before the copy, so a crash mid-copy loses the file. That is worse than in-place.
- **Verdict:** four libraries with four APIs, four sets of mapping rules to keep consistent with Picard, one abandoned and one missing repo, and no good OGG writer. The only real advantage is Picard-cased MP3 TXXX descriptions. If that ever matters, bogem/id3v2 could be added **for MP3 MusicBrainz TXXX frames only**, alongside go-taglib.

## Extending internal/tag to write (rejected)

- `internal/tag` is a read-only parser. The upstream README describes it as "detection, parsing and artwork extraction" ([upstream README](https://github.com/dhowden/tag/blob/master/README.md)), and upstream has been inactive since 2024 ([UPSTREAM.md](../../internal/tag/UPSTREAM.md)). It parses only what it needs. For example, `readCustomAtom` keeps only the `mean`, `name` and `data` payloads ([mp4.go L239-282](../../internal/tag/mp4.go)), so there is nothing to re-serialise unknown content from.
- Writing would mean building, and then maintaining against real-world malformed files, all of the following:
  - an ID3v2 serialiser that round-trips unknown frames and handles unsynchronisation and v2.3/v2.4 differences
  - FLAC metadata-block rewriting with padding reuse
  - Ogg page re-packetisation with CRC32 and granule/sequence handling
  - MP4 `ilst` rewriting with `stco`/`co64` offset fix-ups, which TagLib does in [mp4tag.cpp `updateOffsets`](https://github.com/taglib/taglib/blob/v2.1.1/taglib/mp4/mp4tag.cpp#L162)
- That is the bulk of TagLib, and the ticket's scope doesn't justify it.

## Does this change whether internal/tag stays the reader?

**No. Keep `internal/tag` as the reader, and use go-taglib only for writing.**

Reasons to keep the reader:

- The whole codebase reads through `tag.ReadFrom` on an `io.ReadSeeker` ([main.go L1149](../../main.go)). It is in-process, with no Wasm start-up cost and no dependency on Go 1.25.
- `Raw()` exposes typed frames (`*tag.Comm`, `*tag.UFID`), which `mbz` uses.
- go-taglib's read path would add 0.3 to 0.6 ms per file plus a module instantiation per call ([taglib.go L137-169](https://github.com/sentriz/go-taglib/blob/v0.14.0/taglib.go#L137-L169)). It also can't yet read files with non-UTF-8 text ([#29](https://github.com/sentriz/go-taglib/issues/29)), which `internal/tag` tolerates.

Using two libraries does mean the writer's key mapping and the reader's parsing must agree. The spike found where they don't. These are fixes to `internal/tag`, and they are worth doing regardless of the TUI:

1. **M4A freeform values carry 4 leading NUL bytes (existing bug, affects tagshelf today).**
   - An MP4 `data` atom's payload is 4 bytes of type/flags plus **4 bytes of locale** before the value. Both TagLib ([mp4itemfactory.cpp L643-652, L767-773](https://github.com/taglib/taglib/blob/v2.1.1/taglib/mp4/mp4itemfactory.cpp#L643-L652)) and mutagen, which Picard uses ([mutagen/mp4 `__render_freeform`](https://github.com/quodlibet/mutagen/blob/main/mutagen/mp4/__init__.py)), write it that way.
   - `readCustomAtom` strips only `b[4:]` for `data` ([mp4.go L266-267](../../internal/tag/mp4.go)), so every freeform value, including all MusicBrainz IDs in M4A, starts with `\x00\x00\x00\x00` **[spike]**.
   - `isCompilation` compares `strings.TrimSpace(mbz...Get(mbz.AlbumArtist))` with the Various Artists MBID ([main.go L562](../../main.go)). `TrimSpace` does not strip NUL, so the MBID fallback can never match for M4A.
   - Fix: use `b[8:]` for `data`.
2. **`mbz` matches TXXX descriptions case-sensitively** ([mbz.go `set`](../../internal/tag/mbz/mbz.go)). Make it case-insensitive, so it reads both Picard's `MusicBrainz Album Artist Id` and TagLib's `MUSICBRAINZ ALBUM ARTIST ID`.
3. **Vorbis `TRACKNUMBER`/`DISCNUMBER` in `N/M` form read as 0.** `metadataVorbis.Track` uses `strconv.Atoi` directly ([vorbis.go L239-251](../../internal/tag/vorbis.go)), and the spike showed `track=0/0` **[spike]**. Either parse `N/M` like `parseXofN` does for ID3 ([id3v2metadata.go L107-116](../../internal/tag/id3v2metadata.go)), or have the writer emit `TRACKNUMBER=3` + `TRACKTOTAL=12` for Vorbis. Picard uses the `TRACKTOTAL`/`TOTALTRACKS` convention ([picard/formats/vorbis.py](https://github.com/metabrainz/picard/blob/master/picard/formats/vorbis.py)). Doing both is best.

## Recommendation

Adopt `go.senan.xyz/taglib` (v0.14.0 or later) as tagshelf's only tag writer, behind a small `internal/tagwrite` package. Keep `internal/tag` as the reader.

**Why:**

1. It is the only candidate that covers MP3, FLAC, OGG and M4A with one API, and it writes custom fields in all four.
2. It needs no cgo, so it works on Windows with `CGO_ENABLED=0`; the spike verified this.
3. It is backed by TagLib, which preserves frames it doesn't understand and embedded art.
4. It is actively maintained. The alternatives are stale, missing, or have no OGG writer.
5. It is fast enough for the TUI.

**How:**

- Bump `go.mod` to `go 1.25`.
- Keep a fixed mapping in tagshelf from tagshelf fields to TagLib keys:
  - `ARTIST`, `ALBUMARTIST`, `ALBUM`, `TITLE`, `DATE`
  - `TRACKNUMBER`/`DISCNUMBER`, plus `TRACKTOTAL`/`DISCTOTAL` for Vorbis
  - the MusicBrainz IDs: `MUSICBRAINZ_ARTISTID`, `MUSICBRAINZ_ALBUMARTISTID`, `MUSICBRAINZ_ALBUMID`, `MUSICBRAINZ_RELEASEGROUPID`, `MUSICBRAINZ_RELEASETRACKID`, and `MUSICBRAINZ_TRACKID` (= recording, which becomes UFID on MP3)
  - one custom marker key, e.g. `TAGSHELF_MANUAL`, which becomes `TXXX:TAGSHELF_MANUAL`, a `tagshelf_manual` Vorbis key, or `----:com.apple.iTunes:TAGSHELF_MANUAL`
- Never pass `Clear`, so other tags survive.
- Make writes safe. For each file:
  1. Detect the format with `internal/tag` and refuse unknown formats.
  2. Copy it to a temp file **in the same directory**. It has to be the same directory because go-taglib mounts only the parent directory, and `os.Rename` must stay on one volume.
  3. Call `WriteTags` on the copy.
  4. Re-read the copy with `internal/tag` and check the written values. For FLAC and M4A, also check that `tag.Sum` is unchanged.
  5. `Sync` and close the copy, then `os.Rename` it over the original. On Windows that is `MoveFileEx(..., MOVEFILE_REPLACE_EXISTING)` ([Go os/file_windows.go `rename`](https://github.com/golang/go/blob/master/src/os/file_windows.go), [internal/syscall/windows `Rename`](https://github.com/golang/go/blob/master/src/internal/syscall/windows/syscall_windows.go)).
  6. On any failure, delete the temp file and leave the original untouched.
- Fix the three `internal/tag` issues above. The M4A NUL-prefix fix is worth doing now, independent of the TUI.
- Accept for now that MP3 MusicBrainz TXXX descriptions are written in upper case. TagLib, go-taglib and the fixed `mbz` all read them, but Picard will not recognise them as MusicBrainz IDs.
  - If Picard compatibility on MP3 becomes a requirement, there are two options. One is to write those few TXXX frames with bogem/id3v2 after the go-taglib write; that library supports v2.3 and v2.4 only. The other is to adopt go-taglib's raw-frame API if [#14](https://github.com/sentriz/go-taglib/pull/14) lands.
- Also expect every MP3 write to add an ID3v1 tag and upgrade ID3v2.3 to 2.4.

## Reproducing the spike

The spike needed three things:

- a copy of `go.mod` and `internal/tag` in a scratch module
- `go get go.senan.xyz/taglib@v0.14.0`
- a `main` that did the following for each file in `internal/tag/testdata/{with_tags,without_tags}` (MP3 v2.3/v2.4/untagged, FLAC, OGG, M4A):
  1. copy the file
  2. `WriteImage` the go-taglib `testdata/cover.jpg`
  3. `WriteTags` the fields above plus `TAGSHELF_MANUAL`
  4. read back with `tag.ReadFrom`, `mbz.Extract`, `tag.Sum` and `taglib.ReadTags`

A second program timed 50 writes and reads on a 3.2 MB MP3 in a non-ASCII directory. It compared the MP3 audio frames byte-for-byte, and tried a read-only file and a garbage `.mp3`.

## Sources

- go-taglib: https://github.com/sentriz/go-taglib (README, taglib.go, taglib.cpp, .github/workflows/test.yaml at v0.14.0; issues #14, #15, #19, #29, #30) ; https://proxy.golang.org/go.senan.xyz/taglib/@v/v0.14.0.mod ; https://proxy.golang.org/go.senan.xyz/taglib/@v/list
- TagLib v2.1.1: https://github.com/taglib/taglib/tree/v2.1.1 (id3v2frame.cpp, id3v2framefactory.cpp, id3v2tag.cpp, frames/textidentificationframe.cpp, mpegfile.cpp, mpegfile.h, mp4tag.cpp, mp4itemfactory.cpp, toolkit/tpropertymap.cpp, toolkit/tfilestream.cpp, toolkit/tfile.cpp) ; releases: https://github.com/taglib/taglib/releases
- bogem/id3v2 (n10v/id3v2): https://github.com/n10v/id3v2 ; https://proxy.golang.org/github.com/bogem/id3v2/v2/@v/v2.1.4.mod
- go-flac: https://github.com/go-flac/go-flac ; https://github.com/go-flac/flacvorbis ; module .mod files on proxy.golang.org
- go-mp4tag: https://proxy.golang.org/github.com/!sorrow446/go-mp4tag/@latest (GitHub repo returns 404)
- oggmeta: https://github.com/gcottom/oggmeta
- dhowden/tag: https://github.com/dhowden/tag
- Picard: https://github.com/metabrainz/picard/blob/master/picard/formats/id3.py ; https://github.com/metabrainz/picard/blob/master/picard/formats/vorbis.py
- mutagen: https://github.com/quodlibet/mutagen/blob/main/mutagen/mp4/__init__.py
- Go: https://github.com/golang/go/blob/master/src/os/file_windows.go ; https://github.com/golang/go/blob/master/src/internal/syscall/windows/syscall_windows.go
- tagshelf: `internal/tag/{mp4.go,vorbis.go,sum.go,id3v2metadata.go,UPSTREAM.md}`, `internal/tag/mbz/mbz.go`, `main.go`
