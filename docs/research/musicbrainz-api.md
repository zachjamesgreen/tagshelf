# MusicBrainz API facts for lookup

Researched: 2026-09-27

Question: What does the MusicBrainz web service offer for finding a release and recording for an untagged file? What exact tag keys does MusicBrainz Picard write, so the planned TUI writes the same keys the organizer already reads?

Method: I used only primary sources.

- MusicBrainz API docs:
  - [MusicBrainz API][api] (revision 79405)
  - [Search][search] (revision 78397)
  - [Rate Limiting][rate] (revision 78895)
  - [Examples][examples]
- Picard documentation: [Tag Mapping appendix][picard-map]
- Picard source, pinned at commit [`1cbe55c`][picard-commit] (master on 2026-09-27). This is the ground truth for tag keys.
- Two live API calls, one lookup and one search, made on 2026-09-27 to confirm the JSON field names.
- This repo's own reader code: `main.go`, `internal/tag`, `internal/tag/mbz`.

## Summary

- **Search, then look up.** Search `/ws/2/recording?query=…` with Lucene fields `recording`, `artist`, `release`, `dur`/`qdur`, `tnum`, `tracks`, `tracksrelease`. Pick a release from the hit's `releases[]`. Then fetch `/ws/2/release/<MBID>?inc=artist-credits+recordings+release-groups+media` to get the full tracklist, artist credits and every ID Picard writes. Picard itself does a recording search built from the file's title, artist, album, track number, track total, `qdur` and ISRC ([file.py L1309-1319][pc-file-lookup]).
- **Rate limit:** at most **1 request per second per IP**, on average. Going over it gets *all* requests from the IP rejected with HTTP 503 until the rate drops. You need a **meaningful User-Agent**, `AppName/version ( contact-url-or-email )`. No API key is needed ([API][api-rate], [Rate Limiting][rate]).
- **Format:** XML is the default. Use `fmt=json` or `Accept: application/json`; `fmt` wins if both are set. For reading, the two are "effectively equivalent" ([API][api]).
- **Tag keys:** Picard's keys differ per format. The main trap is that **`musicbrainz_trackid` means different things in different formats**:
  - The Vorbis key `MUSICBRAINZ_TRACKID` holds the *recording* ID.
  - The ID3 frame `TXXX:MusicBrainz Release Track Id` and the MP4 atom `MusicBrainz Release Track Id` hold the *release track* ID.
  - ID3 stores the recording ID in `UFID:http://musicbrainz.org`, and MP4 stores it in `MusicBrainz Track Id`.
  - The full table is [below](#tag-keys-per-format-for-the-fields-tagshelf-needs).
- **tagshelf's gate:** any non-empty MusicBrainz track, recording, album or release key passes `hasMusicBrainzTags` (`main.go` L352-439). Writing the full Picard set also satisfies `isCompilation` (`main.go` L553-563) and `libraryPath` (`main.go` L569-607).

## 1. Search endpoints

Common parameters, all from [Search][search]:

- **URL:** `https://musicbrainz.org/ws/2/<entity>?query=<lucene>&limit=&offset=&fmt=&dismax=`
- **`query`:** required.
- **`limit`:** 1-100; the default is 25.
- **`offset`:** used for paging.
- **`dismax=true`:** switches Solr from edismax to dismax, which "will escape certain special query syntax characters by default". It is the same as the website's plain "Indexed search".
- **Lucene syntax:** fully supported, e.g. `"we will rock you" AND arid:<mbid>`.
- **Escaping:** to search literally, escape Lucene special characters with a backslash, on top of URL encoding (example: `ac%5C%2Fdc`).
- **Missing fields:** `-field:*` matches entities where that field is empty.
- **Scores:** every hit carries a relevance `score` (`ext:score` in XML), 0-100. It is an integer in live JSON; the docs example shows `"100"` as a string ([Search][search], live call).

Picard's own query builder is a useful model. It escapes the characters `+ - & | ! ( ) { } [ ] ^ " ~ * ? : \ /` and joins `field:(value)` terms with spaces, with no `AND`. So Lucene's default OR scores the candidates rather than filtering them ([musicbrainz.py L45-50][pc-lucene]).

### Recording search (`/ws/2/recording`)

The default field is `recording`. Fields useful for identifying a file ([Search § Recording][search]):

| Field | Meaning |
|---|---|
| `recording` | (part of) the recording's name, or the name of a linked track (diacritics ignored); `recordingaccent` keeps diacritics |
| `artist` / `artistname` / `creditname` | combined credited artist string incl. join phrases / any individual artist name / credited name |
| `arid`, `reid`, `rgid`, `rid`, `tid` | artist, release, release-group, recording, track MBIDs |
| `release` | (part of) the name of any release that includes this recording |
| `dur` | recording duration in **milliseconds** |
| `qdur` | duration **quantized: ms / 2000** |
| `tnum` | track position on a medium (starts at 1; pregap = 0) |
| `number` | free-text track number (e.g. "A4") |
| `position` | medium position within its release |
| `tracks` | number of tracks on **any medium** containing this recording |
| `tracksrelease` | number of tracks on **any release (whole)** containing this recording |
| `isrc`, `date`, `firstreleasedate`, `country`, `format`, `status`, `primarytype`, `secondarytype`, `video` | filters |

Each recording hit includes `length` in ms, `artist-credit`, and `releases[]`. Each release in that list has `id`, `title`, `status`, `release-group` (`id`, `primary-type`, `secondary-types`), `date`, `country` and `track-count`, which is the whole release. It also has `media[]` with the medium `position`, `format`, `track-count` and `track-offset`, plus a one-element `track[]` holding the matching track's `id`, `number`, `title` and `length`.

So one search already tells you:

- which release the recording appears on;
- the disc number;
- the track number;
- the medium and release track counts;
- the release-track MBID.

Sources: [Search § Recording XML/JSON][search], and a live call on 2026-09-27 to `recording?query=recording:"Blow Your Mind" AND artist:"Dua Lipa" AND qdur:89&fmt=json`. The live call returned `media[0] = {position:1, format:"Digital Media", track:[{id, number:"6", title, length:178583}], track-count:12, track-offset:5}`.

### Release search (`/ws/2/release`)

The default field is `release`. Fields ([Search § Release][search]):

| Field | Meaning |
|---|---|
| `release`, `releaseaccent` | release title |
| `artist`, `artistname`, `creditname`, `arid` | release artist |
| `reid`, `rgid` | release MBID and its release group |
| `tracks` | **total tracks on the release** |
| `tracksmedium` | tracks on any one medium |
| `mediums` | number of media |
| `barcode`, `catno`, `label`, `laid`, `asin`, `date`, `country`, `format`, `status`, `primarytype`, `secondarytype`, `packaging`, `lang`, `script` | filters |

Note the naming clash: `tracks` means the whole release on the release index, but any one medium on the recording index. On the recording index, the whole-release count is `tracksrelease`.

### Release-group search (`/ws/2/release-group`)

The default field is `releasegroup`. Fields: `releasegroup`/`releasegroupaccent`, `release`, `artist`/`artistname`/`creditname`/`arid`, `rgid`, `reid`, `primarytype`, `secondarytype`, `firstreleasedate`, `releases` (the number of releases in the group), `status`, `tag` ([Search § Release Group][search]). This search is useful for an "album first" flow: pick the release group, then browse its releases.

## 2. Lookups and `inc=`

A lookup is `GET /ws/2/<entity>/<MBID>?inc=a+b+c`. Separate `inc` values with `+` ([API § Lookups][api]).

- **Release subqueries:** `collections`, `labels`, `recordings`, `release-groups`.
- **Recording subqueries:** `releases`, `release-groups`.
- **Modifiers:**
  - `media` returns the track count and format of each medium.
  - `artist-credits` returns artist credits for releases and recordings.
  - `isrcs` and `discids` do what their names say.
- **Filters:** includes that return releases also accept `type=` and `status=`.
- **The 25-item cap:** linked-entity lists in a lookup are capped at 25; use browse for the rest. This matters for `recording?inc=releases`, not for a release's own tracklist.

All of the above is from [API § Subqueries, inc= arguments which affect subqueries][api].

**For tagging, use:** `GET /ws/2/release/<MBID>?inc=artist-credits+recordings+release-groups+media&fmt=json`

That response contains ([Examples § Release][examples], live call on 2026-09-27):

| Level | Fields |
|---|---|
| Release | `id` → `musicbrainz_albumid`; `title` → album; `date`; `country`; `status`; `barcode`; `artist-credit[]` → album artist and album-artist IDs; `release-group` (`id`, `primary-type`, `secondary-types`, `first-release-date`) |
| `media[]` | `position` → disc number; `format`; `track-count`; `track-offset`; `tracks[]` |
| `tracks[]` | `id` → release track ID; `position` → track number; `number` (free text); `title`; `length` (ms); `artist-credit[]`; `recording` (`id`, `title`, `length`, `artist-credit`, `first-release-date`) |

A few details to keep in mind:

- **Field names differ by endpoint.** In search results the per-medium list is `track`, singular. In lookups it is `tracks`.
- **Artist credits are ordered lists.** Each entry has `artist` (`id`, `name`, `sort-name`), a credited `name`, and a `joinphrase`. The display string is the concatenation of `name + joinphrase`. Picard builds the tag value the same way ([mbjson.py L653-673][pc-ac]).
- **XML trims track titles.** When a release includes recordings, the XML API omits a track's title if it equals the recording title. JSON does not ([API § Subqueries][api]).

Picard's own release request asks for more than tagshelf needs. It always requests `aliases annotation artist-credits artists collections discids isrcs labels media recordings release-groups`, and adds relationship includes on top ([album.py L929-941][pc-inc]).

### Non-MBID lookups worth knowing

These are from [API § Non-MBID Lookups][api]:

- **`/ws/2/isrc/<isrc>`** returns recordings. Useful if a file carries `TSRC`/`ISRC`.
- **`/ws/2/discid/<id>?toc=…`** returns releases, with fuzzy TOC matching. This only applies to CD rips with a TOC.

## 3. Rate limit and User-Agent

- **One call per second.** "All users of the API must ensure that each of their client applications never make more than ONE call per second." Going over "may" get the IP blocked ([API § Application rate limiting][api-rate]).
- **How throttling works** ([Rate Limiting][rate]):
  - **Per IP:** 1 request per second on average. Over that, **100 %** of the IP's requests get a 503 until the rate falls.
  - **Anonymous User-Agents:** empty, `Java`, `Python-urllib`, `Jakarta Commons-HttpClient`, and similar are pooled at 50 requests per second across everyone using them.
  - **Global:** 300 requests per second.
  - **Response:** a denial is `503 Service Unavailable`.
- **User-Agent format:** `Application name/<version> ( contact-url )` or `Application name/<version> ( contact-email )`. Example: `MyAwesomeTagger/1.2.0 ( me@example.com )`. For tagshelf that means something like `tagshelf/<version> ( <repo URL> )` ([Rate Limiting § Provide meaningful User-Agent strings][rate]).
- **Response headers.** The live responses carried `X-RateLimit-Limit`, `X-RateLimit-Remaining` and `X-RateLimit-Reset`. The docs do not describe them, so treat them as informational.
- **What Picard does.** It starts at a 1000 ms minimum delay per host. On 503 it backs off exponentially up to about 32 s (2^5 × 1000 ms), then halves the delay again on recovery ([ratecontrol.py L52-55, L194-222][pc-rate]). A single `time.Tick(time.Second)` gate in the TUI plus retry-with-backoff on 503 would match this.
- **Other rules.** No API key is needed, and non-commercial use is free ([API § General FAQ][api]). Don't schedule bulk jobs at fixed clock times, and don't poll for changes ([Rate Limiting § Scheduling][rate]).

## 4. JSON vs XML

These are from the [API][api] introduction and FAQ:

- **XML is the default.** Ask for JSON with `fmt=json` or `Accept: application/json`; `fmt` wins if both are set.
- **Reading is equivalent in either format.** Only XML accepts submissions (tags, ratings, barcodes, ISRCs), which tagshelf doesn't need.
- **Recommendation:** use JSON. Go's `encoding/json` maps the hyphenated keys (`artist-credit`, `track-count`, `release-group`) with struct tags.

## 5. Tag keys per format for the fields tagshelf needs

These are the keys **Picard writes** (source-pinned). The last column is what **tagshelf reads** through `internal/tag`/`mbz`. Picard sources:

- ID3: [id3.py L157-240][pc-id3], UFID [L98, L813-815][pc-ufid]
- Vorbis: [vorbis.py L150-158, L347-351][pc-vorbis]
- MP4: [mp4.py L81-173][pc-mp4]
- Where the values come from: [mbjson.py][pc-mbjson] and [album.py][pc-album]

It agrees with Picard's documentation table ([Tag Mapping][picard-map]).

| Picard name | Value source (release lookup JSON) | ID3v2.4 | ID3v2.3 | Vorbis (FLAC/Ogg; case-insensitive) | MP4 / iTunes | tagshelf reads it via |
|---|---|---|---|---|---|---|
| `title` | `tracks[].title` | `TIT2` | `TIT2` | `TITLE` | `©nam` | `m.Title()` |
| `artist` | `tracks[].artist-credit` joined with join phrases | `TPE1` | `TPE1` | `ARTIST` | `©ART` | `m.Artist()` |
| `album` | `title` | `TALB` | `TALB` | `ALBUM` | `©alb` | `m.Album()` |
| `albumartist` | release `artist-credit` joined (VA → Picard's `va_name`, default "Various Artists") | `TPE2` | `TPE2` | `ALBUMARTIST` | `aART` | `m.AlbumArtist()` |
| `date` | release `date` (`YYYY`, `YYYY-MM` or `YYYY-MM-DD`) | `TDRC` | `TYER` (+`TDAT`) | `DATE` | `©day` | `m.Year()` |
| `tracknumber` / `totaltracks` | `tracks[].position` / `media[].track-count` | `TRCK` = `n/N` | `TRCK` = `n/N` | `TRACKNUMBER` = `n`; total in `TRACKTOTAL` **and** `TOTALTRACKS` | `trkn` (n, N) | `m.Track()` |
| `discnumber` / `totaldiscs` | `media[].position` / `len(media)` | `TPOS` = `n/N` | `TPOS` = `n/N` | `DISCNUMBER`; total in `DISCTOTAL` **and** `TOTALDISCS` | `disk` (n, N) | `m.Disc()` |
| `musicbrainz_albumid` | release `id` | `TXXX:MusicBrainz Album Id` | same | `MUSICBRAINZ_ALBUMID` | `----:com.apple.iTunes:MusicBrainz Album Id` | `hasMusicBrainzTags` |
| `musicbrainz_recordingid` | `tracks[].recording.id` | `UFID` owner `http://musicbrainz.org` | same | **`MUSICBRAINZ_TRACKID`** | `----:com.apple.iTunes:MusicBrainz Track Id` | `hasMusicBrainzTags` |
| `musicbrainz_trackid` (release track) | `tracks[].id` | `TXXX:MusicBrainz Release Track Id` | same | **`MUSICBRAINZ_RELEASETRACKID`** | `----:com.apple.iTunes:MusicBrainz Release Track Id` | `hasMusicBrainzTags` |
| `musicbrainz_albumartistid` | release `artist-credit[].artist.id` (multi-valued) | `TXXX:MusicBrainz Album Artist Id` | same | `MUSICBRAINZ_ALBUMARTISTID` | `----:com.apple.iTunes:MusicBrainz Album Artist Id` | `mbz.AlbumArtist` → `isCompilation` |
| `musicbrainz_artistid` | track `artist-credit[].artist.id` (multi-valued) | `TXXX:MusicBrainz Artist Id` | same | `MUSICBRAINZ_ARTISTID` | `----:com.apple.iTunes:MusicBrainz Artist Id` | (mbz only) |
| `musicbrainz_releasegroupid` | `release-group.id` | `TXXX:MusicBrainz Release Group Id` | same | `MUSICBRAINZ_RELEASEGROUPID` | `----:com.apple.iTunes:MusicBrainz Release Group Id` | (mbz only) |
| `originaldate` | `release-group.first-release-date` | `TDOR` | `TORY` (year only) | `ORIGINALDATE` (+ `ORIGINALYEAR`) | none (no MP4 mapping in mp4.py; docs say n/a) | not read |
| `compilation` | `1` when album artist ID is VA `89ad4ac3-…` | `TCMP` | `TCMP` | `COMPILATION` | `cpil` | deliberately ignored |
| `releasetype` / `releasestatus` | `release-group.primary-type`+`secondary-types` / `status` | `TXXX:MusicBrainz Album Type` / `…Album Status` | same | `RELEASETYPE` / `RELEASESTATUS` | `----:com.apple.iTunes:MusicBrainz Album Type` / `…Album Status` | not read |

Where each row's details come from:

- **Track and disc numbers:** Picard sets `tracknumber` from track `position`, not the free-text `number`. `discnumber` comes from medium `position`, and `totaltracks` from medium `track-count` ([mbjson.py L101-110, L888-896][pc-mbjson]). `totaldiscs` is `len(release['media'])` ([album.py L554-561][pc-album]).
- **Compilation flag:** Picard sets `compilation=1` only when the album artist ID is Various Artists ([album.py L741-757][pc-album-va]).
- **ID3v2.3 conversion:** Picard saves v2.3 through mutagen's `update_to_v23`, and cuts `originaldate` to the year. Picard's docs map `date` to `TYER`+`TDAT` and `originaldate` to `TORY` for v2.3 ([id3.py L655-679][pc-v23]; [Tag Mapping][picard-map]).
- **Multi-valued fields in ID3v2.3:** they are flattened with the configured `id3v23_join_with` separator ([id3.py L681-685][pc-v23]).
- **Vorbis key case:** Vorbis comment keys are written upper-case ([vorbis.py L347][pc-vorbis]). tagshelf lower-cases them on read (`internal/tag/vorbis.go` L59).
- **MP4 freeform atoms:** these use `mean` = `com.apple.iTunes` and `name` = the label shown after the second colon above. tagshelf keys them by `name` alone (`internal/tag/mp4.go` L234-286).

### Observations about tagshelf's reader

These come from reading the code, not from the sources above.

1. **`mbz.Track` and `mbz.Recording` hold different IDs per format.** This follows from `mbz.set` resolving both the internal name and the display alias (`internal/tag/mbz/mbz.go`):
   - For Vorbis, `musicbrainz_trackid` lands in `mbz.Track` but holds the *recording* ID. `MUSICBRAINZ_RELEASETRACKID` is not recognized at all.
   - For ID3 and MP4, `mbz.Recording` holds the recording ID and `mbz.Track` holds the release track ID.
   - Only `mbz.AlbumArtist` is used today, so this is harmless. The TUI should not trust `mbz.Get(mbz.Track)` to mean the same thing across formats.
2. **ID3v2.4 month-only dates read as year 0.** `metadataID3v2.Year()` parses `TDRC` with `Atoi` and then `time.DateOnly`, so a `YYYY-MM` date (which MusicBrainz allows) returns 0. That drops the "(Year)" from the folder name. Vorbis handles 4, 7 and 10-character dates, and MP4 takes the first four characters. The writer should either write `YYYY` or `YYYY-MM-DD` only, or the reader should learn `YYYY-MM`.
3. **MP4 freeform values may start with NUL bytes.** Unverified, and worth a test. `readCustomAtom` takes `data` payloads as `b[4:]`, which skips the version and flags but, as far as I can tell, not the 4-byte locale field. If so, freeform values start with four NULs. `strings.TrimSpace` does not remove them, so `isCompilation`'s comparison with the Various Artists MBID would fail for MP4. The repo's `testdata/with_tags/sample.m4a` has no MusicBrainz atoms to check this against.
4. **The Vorbis reader keeps one value per key.** It keeps a single string per key (`m.c[k] = v`), so for a repeated multi-valued `MUSICBRAINZ_ALBUMARTISTID`, only the last value survives.
5. **The Vorbis track and disc totals use Picard's spelling.** The reader reads `tracktotal`/`disctotal`, and Picard writes those as well as `TOTALTRACKS`/`TOTALDISCS`, so both line up.
6. **`internal/tag` is read-only.** The TUI needs a separate writer for ID3v2, Vorbis/FLAC and MP4.

## 6. Matching by duration and track count

- **Search by duration.** The recording index has `dur` (exact ms) and `qdur` (ms / 2000, so 2-second buckets). Picard sends `qdur:(<length_ms // 2000>)` ([file.py L1316][pc-file-lookup]). A tolerant query could OR the neighbouring buckets, e.g. `qdur:(88 OR 89 OR 90)`, or use a Lucene range `dur:[176000 TO 181000]`.
- **Search by track count.**
  - On the recording index, `tracks` is the per-medium count, compared against the file's `TRCK` total, and `tracksrelease` is the whole release.
  - Also on the recording index, `tnum` is the position on the medium and `position` is the disc number.
  - On the release index, `tracks` is the total and `tracksmedium` is the per-medium count ([Search][search]).
- **Picard's scoring as a template.**
  - When matching a file against candidates, Picard weights the fields as follows ([file.py L137-151][pc-weights]):

    | Field | Weight |
    |---|---|
    | title | 13 |
    | length | 10 |
    | album | 5 |
    | artist | 4 |
    | date | 4 |
    | totaltracks | 4 |
    | tracknumber | 4 |
    | ISRC, barcode, catalog number | 28, 28, 22 |

  - The length score falls linearly from 1.0 at 0 ms difference to 0.0 at **30 000 ms** ([matching.py L74, L439-447][pc-length]).
  - In the generic `Metadata.compare`, track and disc numbers and totals are exact-match 0/1 terms ([metadata.py L67-75, L127-157][pc-compare]).
- **tagshelf cannot read duration yet.** `tag.Metadata` has no duration or length method, so the TUI needs another way to get the file's length, such as a decoder or `ffprobe`, before it can use `dur`/`qdur`.
- **Search results carry enough to rank without a lookup.** Each recording hit carries its `length` and, per release, `track-count`, the medium `track-count`, the medium `position` and the track `number`. Candidates can be ranked from the search response alone, and only the chosen release needs a lookup.

[api]: https://musicbrainz.org/doc/MusicBrainz_API
[api-rate]: https://musicbrainz.org/doc/MusicBrainz_API#Application_rate_limiting_and_identification
[search]: https://musicbrainz.org/doc/MusicBrainz_API/Search
[rate]: https://musicbrainz.org/doc/MusicBrainz_API/Rate_Limiting
[examples]: https://musicbrainz.org/doc/MusicBrainz_API/Examples
[picard-map]: https://picard-docs.musicbrainz.org/en/latest/appendices/tag_mapping.html
[picard-commit]: https://github.com/metabrainz/picard/tree/1cbe55c755e5c47dbfcfb2410b8a20622d3fdd4b
[pc-id3]: https://github.com/metabrainz/picard/blob/1cbe55c755e5c47dbfcfb2410b8a20622d3fdd4b/picard/formats/id3.py#L157-L240
[pc-ufid]: https://github.com/metabrainz/picard/blob/1cbe55c755e5c47dbfcfb2410b8a20622d3fdd4b/picard/formats/id3.py#L813-L815
[pc-v23]: https://github.com/metabrainz/picard/blob/1cbe55c755e5c47dbfcfb2410b8a20622d3fdd4b/picard/formats/id3.py#L655-L685
[pc-vorbis]: https://github.com/metabrainz/picard/blob/1cbe55c755e5c47dbfcfb2410b8a20622d3fdd4b/picard/formats/vorbis.py#L150-L158
[pc-mp4]: https://github.com/metabrainz/picard/blob/1cbe55c755e5c47dbfcfb2410b8a20622d3fdd4b/picard/formats/mp4.py#L81-L173
[pc-mbjson]: https://github.com/metabrainz/picard/blob/1cbe55c755e5c47dbfcfb2410b8a20622d3fdd4b/picard/mbjson.py#L101-L124
[pc-ac]: https://github.com/metabrainz/picard/blob/1cbe55c755e5c47dbfcfb2410b8a20622d3fdd4b/picard/mbjson.py#L653-L724
[pc-album]: https://github.com/metabrainz/picard/blob/1cbe55c755e5c47dbfcfb2410b8a20622d3fdd4b/picard/album.py#L554-L561
[pc-album-va]: https://github.com/metabrainz/picard/blob/1cbe55c755e5c47dbfcfb2410b8a20622d3fdd4b/picard/album.py#L741-L757
[pc-inc]: https://github.com/metabrainz/picard/blob/1cbe55c755e5c47dbfcfb2410b8a20622d3fdd4b/picard/album.py#L929-L941
[pc-file-lookup]: https://github.com/metabrainz/picard/blob/1cbe55c755e5c47dbfcfb2410b8a20622d3fdd4b/picard/file.py#L1309-L1319
[pc-weights]: https://github.com/metabrainz/picard/blob/1cbe55c755e5c47dbfcfb2410b8a20622d3fdd4b/picard/file.py#L137-L151
[pc-compare]: https://github.com/metabrainz/picard/blob/1cbe55c755e5c47dbfcfb2410b8a20622d3fdd4b/picard/metadata.py#L67-L157
[pc-length]: https://github.com/metabrainz/picard/blob/1cbe55c755e5c47dbfcfb2410b8a20622d3fdd4b/picard/matching.py#L439-L447
[pc-lucene]: https://github.com/metabrainz/picard/blob/1cbe55c755e5c47dbfcfb2410b8a20622d3fdd4b/picard/webservice/api_helpers/musicbrainz.py#L45-L50
[pc-rate]: https://github.com/metabrainz/picard/blob/1cbe55c755e5c47dbfcfb2410b8a20622d3fdd4b/picard/webservice/ratecontrol.py#L52-L222
