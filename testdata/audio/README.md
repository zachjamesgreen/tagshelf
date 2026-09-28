# Audio fixtures

Small audio files for testing tag reading, hashing and writing without ffmpeg installed. Tests find them through `internal/fixtures`, which also holds the tag values below as constants.

Every supported format has an `untagged.<ext>` and a `tagged.<ext>`. The ffmpeg-made ones are one second of 8 kHz mono silence. The APE and MPC ones are TagLib test files: `untagged.ape` is 3.6 s of 44.1 kHz audio, `untagged.mpc` a 0.08 s click, and `untagged-sv8.mpc` only a stream header. The tagged files carry the same values under the key names MusicBrainz Picard uses for that format:

| Field | Value |
|---|---|
| Title | Silence Café |
| Artist | Fixture Artist |
| Album artist | Fixture Album Artist |
| Album | Fixture Album |
| Year | 2001 |
| Track | 3 of 10 (WMA: 3, no total, since Picard writes none for WMA) |
| Disc | 1 of 2 |
| MusicBrainz artist ID | `11111111-1111-4111-8111-111111111111` |
| MusicBrainz album artist ID | `22222222-2222-4222-8222-222222222222` |
| MusicBrainz release (album) ID | `33333333-3333-4333-8333-333333333333` |
| MusicBrainz release group ID | `44444444-4444-4444-8444-444444444444` |
| MusicBrainz recording ID | `55555555-5555-4555-8555-555555555555` |
| MusicBrainz release track ID | `66666666-6666-4666-8666-666666666666` |

## Regenerating

```sh
go generate ./internal/fixtures
```

This runs `internal/fixtures/gen.go`, which needs ffmpeg on PATH (these were made with ffmpeg 7.1). It rewrites every file here except the three TagLib files. ffmpeg runs with bitexact flags, so the same ffmpeg version gives the same bytes.

## Where each file comes from

| File | Source | Tags |
|---|---|---|
| `untagged.mp3` | ffmpeg, `libmp3lame` 8 kb/s, no Xing header, no ID3 | none |
| `tagged.mp3` | `untagged.mp3` with an ID3v2.4 tag prepended by `gen.go` | ID3v2.4: text frames, `TXXX:MusicBrainz …`, recording ID in `UFID` (`http://musicbrainz.org`) |
| `untagged.flac`, `tagged.flac` | ffmpeg, `flac` | Vorbis comment written by ffmpeg; recording ID in `MUSICBRAINZ_TRACKID` |
| `untagged.ogg`, `tagged.ogg` | ffmpeg, `libvorbis` | as FLAC; both also carry ffmpeg's `encoder` comment |
| `untagged.m4a`, `tagged.m4a` | ffmpeg, `aac` in the `ipod` muxer, brand `M4A ` | `ilst` atoms by ffmpeg, then `----:com.apple.iTunes:MusicBrainz …` atoms added by `gen.go` |
| `untagged.m4b`, `tagged.m4b` | as M4A, brand `M4B ` | as M4A |
| `tagged-va.m4a` | as M4A, without an album artist name | as `tagged.m4a`, but the album artist ID is Various Artists (`89ad4ac3-39f7-470e-963a-56509c546377`) |
| `manual.flac`, `manual.ogg` | as `tagged.flac` / `tagged.ogg`, tagged by hand | the fixture tags without MusicBrainz IDs, plus the manual marker `TAGSHELF_MANUAL=hand 2026-09-27` |
| `manual-noalbum.flac` | as `manual.flac` | as `manual.flac`, without `ALBUM` |
| `ripper.flac` | as `manual.flac` | as `manual.flac`, without the manual marker: ripper tags |
| `manual.m4a` | as `tagged.m4a`, without the MusicBrainz atoms | `ilst` atoms by ffmpeg, then a `----:com.apple.iTunes:TAGSHELF_MANUAL` atom added by `gen.go` |
| `manual.mp3` | `untagged.mp3` with an ID3v2.4 tag prepended by `gen.go` | text frames as `tagged.mp3`, `TXXX:TAGSHELF_MANUAL`, no MusicBrainz frames |
| `untagged.wav` | ffmpeg, `pcm_s16le` 8 kHz mono | none |
| `untagged-odd.wav` | ffmpeg, `pcm_u8` at 8001 Hz: an odd-sized `data` chunk plus its pad byte | none |
| `tagged.wav` | `untagged.wav` with an `id3 ` chunk appended by `gen.go`, as mutagen/Picard write it | the ID3v2.4 tag from `tagged.mp3` |
| `untagged.wma`, `tagged.wma` | ffmpeg, `wmav2` (no Index objects) | title and artist in the Content Description; `WM/…` and `MusicBrainz/…` descriptors written by ffmpeg |
| `untagged.aac` | ffmpeg, `aac` in the `adts` muxer | none |
| `tagged.aac` | `untagged.aac` with an APEv2 tag appended by `gen.go`, as Picard writes .aac | APEv2 with header and footer, Picard's key names (`Musicbrainz_Albumid`, `musicbrainz_trackid` for the recording, …) |
| `tagged-id3v2.aac` | `untagged.aac` with the ID3v2.4 tag prepended by `gen.go`, as go-taglib writes .aac | ID3v2.4 as `tagged.mp3` |
| `untagged.ape` | TagLib `tests/data/mac-399.ape` (Monkey's Audio 3.99), unchanged | none |
| `tagged.ape` | `untagged.ape` with the APEv2 tag appended by `gen.go` | APEv2 as `tagged.aac` |
| `untagged.mpc` | TagLib `tests/data/click.mpc` (Musepack SV7), unchanged | none |
| `untagged-sv8.mpc` | TagLib `tests/data/sv8_header.mpc` (Musepack SV8 header only), unchanged | none |
| `tagged.mpc` | `untagged.mpc` with the APEv2 tag appended by `gen.go` | APEv2 as `tagged.aac` |

ffmpeg makes its audio from the `lavfi` `anullsrc` silence source, so those files carry no third-party content.

## TagLib files and license

`untagged.ape`, `untagged.mpc` and `untagged-sv8.mpc` are copied unchanged from TagLib v2.1.1, <https://github.com/taglib/taglib/tree/v2.1.1/tests/data>. `tagged.ape` and `tagged.mpc` are those files with a tag appended.

| File here | TagLib file | SHA-256 |
|---|---|---|
| `untagged.ape` | `mac-399.ape` | `8c851b4f52bbbd822262040ae8961b8c27f8f702d1be3fa50cca283d009aadea` |
| `untagged.mpc` | `click.mpc` | `178f44eb55353512ef611ccca4d10557af7c2d329a6078a019a5d66d522d25ad` |
| `untagged-sv8.mpc` | `sv8_header.mpc` | `651c5f6f7badbb327cb51461468e6217b14624d7d3c1eee8ba86e26714e0a080` |

> TagLib is distributed under the GNU Lesser General Public License (LGPL) version 2.1 and the Mozilla Public License (MPL) version 1.1. See <https://github.com/taglib/taglib/blob/v2.1.1/COPYING.LGPL> and <https://github.com/taglib/taglib/blob/v2.1.1/COPYING.MPL>.

Copies of both license texts are in `LICENSES/`. These files are test data only. They are not built into the tagshelf binary.
