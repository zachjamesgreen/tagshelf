# internal/tag format extensions: WAV, APEv2 and ASF

Researched: 2026-09-27

Question (ticket 13): ticket 08 decided that `internal/tag` gains:

- readers for WAV (ID3 chunk), APEv2 (APE, MPC, .aac) and ASF (WMA)
- an audio-only `tag.Sum` for those formats
- a marker-only ASF writer
- an APEv2 writer that uses Picard's key names

This note collects the facts the spec needs:

- the ASF object layout, and whether growing the header is safe
- APEv2 layout and placement, and Picard's key names
- the audio byte range `tag.Sum` should hash for each format
- how to read the WAV ID3 chunk, and whether to read LIST/INFO too
- rough code size, and the test fixtures that are available

Method: I read these primary sources:

- the ASF specification: I read the text of revision 01.20.02, June 2004 ([PDF](https://mail.kde.org/pipermail/taglib-devel/attachments/20041010/da87efc4/ASF_Specification-0001.pdf)). Microsoft's own download is revision 01.20.03, December 2004 ([asf_specification.doc](https://download.microsoft.com/download/7/9/0/790fecaa-f64a-4a5e-a430-0bccdab3f1b4/asf_specification.doc)). I downloaded it but did not convert it, so section numbers below are from 01.20.02.
- the APEv2 specification that TagLib bundles ([ape-tag-format.txt](https://github.com/taglib/taglib/blob/v2.1.1/taglib/ape/ape-tag-format.txt))
- TagLib v2.1.1 source, which go-taglib v0.14.0 bundles
- Picard at commit [`1cbe55c`](https://github.com/metabrainz/picard/tree/1cbe55c755e5c47dbfcfb2410b8a20622d3fdd4b)
- mutagen at commit [`ada28b2`](https://github.com/quodlibet/mutagen/tree/ada28b2cc92c515f3f26640a6feef6516d195872)
- tagshelf's `internal/tag`

I also ran a throwaway spike on this Windows machine (Go 1.27.1, ffmpeg 7.1, go-taglib v0.14.0, mutagen from source). Results from the spike are marked **[spike]**. The last section says how to reproduce it.

## Summary

- **Growing the ASF header is safe.**
  - The spec defines no absolute file offsets outside the header. The Simple Index uses packet numbers. The Index, Media Object Index and Timecode Index use byte offsets "relative to the start of the first ASF Data Packet". The Marker Object uses "a byte offset into the Data Object".
  - The spike added a descriptor to the Extended Content Description Object of three WMA files. One of them has both an Index and a Simple Index. Full decodes and a decode after seeking to 3 s were byte-identical in ffmpeg, and Windows' own property handler still read duration and tags **[spike]**.
  - The marker writer must rewrite four things: the Header Object size, the header object count (only if it adds an object), the Extended Content Description size and descriptor count, and the File Properties **File Size**.
  - TagLib and mutagen both leave File Size stale **[spike]**, so readers must not trust it. tagshelf should set it to the real new file length.
- **Two ASF traps:**
  1. A Digital Signature Object signs the header bytes between File Properties and itself. The writer must refuse files that have one.
  2. go-taglib **silently drops a new** `TAGSHELF_MANUAL` ASF key, **keeps an existing one**, and **never returns it from `ReadTags`** **[spike]**. So ticket 08's plan to use go-taglib for the verify step cannot check the WMA marker. The `internal/tag` ASF reader has to exist before WMA tagging.
- **APEv2** is a 32-byte header, items, then a 32-byte footer, all little-endian. Keys are 2–255 printable ASCII characters and match case-insensitively.
  - The tag sits at the end of the file, **before** any ID3v1 tag. ID3v2 can only appear at the start.
  - Picard writes `Artist`, `Album Artist`, `Album`, `Title`, `Track` (`n/total`), `Disc` (`n/total`) and `Year`.
  - It writes the MusicBrainz IDs as `Musicbrainz_Artistid`, `Musicbrainz_Albumartistid`, `Musicbrainz_Albumid` and `Musicbrainz_Releasegroupid`. The **recording** ID goes in lowercase `musicbrainz_trackid`, and the **release track** ID in lowercase `musicbrainz_releasetrackid`.
- **`tag.Sum` audio ranges.** In each case below, the hash stayed the same through go-taglib, mutagen and marker writes **[spike]**. Today's `tag.Sum` changes on every retag of all five formats **[spike]**.
  - WAV: the `data` chunk payload.
  - ASF: the Data Object minus its 50-byte header.
  - APE, MPC and ADTS: everything after a leading ID3v2 tag, with the trailing ID3v1 and APEv2 tags stripped. Strip ID3v1 again after APEv2, because mutagen appends APEv2 *after* an existing ID3v1 **[spike]**.
- **WAV:** walk the RIFF chunks, then call the existing `ReadID3v2Tags` on an `io.NewSectionReader` over the `ID3 ` or `id3 ` chunk. This works unchanged **[spike]**.
  - Reading LIST/INFO is optional, as a per-field fallback in the way Picard does it. It never carries MBIDs or the marker.
- **Size:** about 1,100–1,300 lines of Go plus tests. ffmpeg can generate WAV, WMA and ADTS fixtures. ffmpeg cannot encode APE or MPC, so for those either synthesise the container in tests or borrow TagLib's `tests/data` files (LGPL/MPL).

## ASF (WMA)

### Object layout

Every ASF object is a 16-byte GUID, a QWORD size (which includes the 24-byte object header), and then the data. Everything is little-endian (spec §2.1).

The file is a Header Object, then the Data Object, then any other top-level objects, then the Index objects last (§2.2). Objects inside the header "may appear in any order" (§2.3).

| Object | Layout (bytes) | Size-dependent fields |
|---|---|---|
| Header `75B22630-668E-11CF-A6D9-00AA0062CE6C` (§3.1) | GUID 16, Object Size 8, Number of Header Objects 4 (DWORD), Reserved1 = `0x01`, Reserved2 = `0x02`. Children start at offset 30. | **Object Size** is 30 plus the sum of the children. **Count** is the number of children; the Header Extension counts as one. |
| File Properties `8CABDCA1-A947-11CF-8EE4-00C00C205365` (§3.2), mandatory | data offsets: File ID 0–15, **File Size 16**, Creation Date 24, Data Packets Count 32, Play Duration 40, Send Duration 48, Preroll 56, Flags 64 (bit 0 Broadcast, bit 1 Seekable), Min/Max Packet Size 68/72, Max Bitrate 76. Object is 104 bytes. | **File Size**: "the size, in bytes, of the entire file", invalid if the Broadcast flag is set. |
| Content Description `75B22633-…` (§3.10), optional | five WORD byte-lengths, then Title, Author, Copyright, Description, Rating as UTF-16LE | Picard and TagLib store **title and artist here, not in the Extended Content Description** ([picard asf.py L120-124](https://github.com/metabrainz/picard/blob/1cbe55c755e5c47dbfcfb2410b8a20622d3fdd4b/picard/formats/asf.py#L120-L124)). |
| Extended Content Description `D2D0A440-E307-11D2-97F0-00A0C95EA850` (§3.11), optional, one only | Content Descriptors Count (WORD), then for each descriptor: Name Length (WORD, bytes), Name (UTF-16LE, NUL-terminated), Value Type (WORD: 0 string, 1 bytes, 2 BOOL(32), 3 DWORD, 4 QWORD, 5 WORD), Value Length (WORD), Value | **Object Size**, **Count**. Values are at most 65,535 bytes, because the length field is a WORD. |
| Header Extension `5FBF03B5-A92E-11CF-8EE3-00C00C205365` (§3.4), mandatory | Reserved GUID, Reserved WORD (6), Data Size (DWORD), then nested objects (Metadata §4.7, Metadata Library §4.8, …) | Its own sizes, if touched. The marker writer doesn't need to touch it. |
| Digital Signature `2211B3FC-BD23-11D2-B4B7-00A0C955FC6E` (§3.16) | signs "the portion of their header that lies between the end of the File Properties Object and the beginning of the Digital Signature Object" | Any ECD edit before it breaks the signature. **Refuse the file.** |
| Padding `1806D474-CADF-4509-A4BA-9AABCB96AAE8` (§3.17) | ignored bytes | The spec's intended use: "the size of the Padding Object can be increased to compensate …, without having to move or rewrite the data contained in the Data Object". |
| Data `75B22636-668E-11CF-A6D9-00AA0062CE6C` (§5.1) | GUID 16, Object Size 8, File ID 16, Total Data Packets 8, Reserved `0x0101` 2. **Packets start at +50.** | Object Size may be 0 only if Broadcast is set. |

"Implementations shall ignore any standard or non-standard object that they do not know how to handle" (§2.2).

### Is growing the header safe? Yes (verified)

The unverified claim from ticket 08 holds.

- **Spec**
  - Simple Index Object (§6.1): each Index Entry is a **Packet Number** (DWORD) and a Packet Count (WORD). These are packet numbers, not bytes.
  - Index Object (§6.2): "Offset field values … are byte offsets that, when combined with the Block Position value of the Index Block, indicate the starting location in bytes of an ASF Data Packet **relative to the start of the first ASF Data Packet in the file**". Block Positions are "relative to the beginning of the first Data Packet (for example, the beginning of the Data Object + 50 bytes)".
  - Media Object Index (§6.3) and Timecode Index (§6.4) use the same Block Position scheme.
  - Marker Object (§3.7): "a byte offset into the Data Object".
  - Data packets (§5.2) carry send times, durations, lengths and in-media-object offsets. None of these is an absolute file position.
  - `grep` over the whole spec text for "beginning of the file", "absolute" and "file offset" finds nothing else.
- **Prior art:** TagLib and mutagen both grow or shrink the header by moving the rest of the file.
  - TagLib `ASF::File::save` renders all header children, writes `data.size() + 30` and the child count at offset 16, then `insert(data, 30, headerSize - 30)` ([asffile.cpp L526-600](https://github.com/taglib/taglib/blob/v2.1.1/taglib/asf/asffile.cpp#L526-L600)).
  - mutagen does the same with `resize_bytes`, and adds a Padding Object for future edits ([mutagen asf/\_\_init\_\_.py L248-310](https://github.com/quodlibet/mutagen/blob/ada28b2cc92c515f3f26640a6feef6516d195872/mutagen/asf/__init__.py#L248-L310), [\_objects.py `render_full` L137-172](https://github.com/quodlibet/mutagen/blob/ada28b2cc92c515f3f26640a6feef6516d195872/mutagen/asf/_objects.py#L137-L172)).
- **[spike]** A throwaway Go marker writer (about 110 lines) appended `TAGSHELF_MANUAL` to the ECD object, rewrote the header size, and patched File Size. It ran on:
  - `ff.wma` (ffmpeg, no index)
  - TagLib's `silence-1.wma`
  - TagLib's `lossless.wma`, which has an **Index Object and a Simple Index Object** after the Data Object

  In all three files:
  - the Data Object moved by +70 to +84 bytes and the Index objects moved with it
  - `ffmpeg -f md5` of the full decode, and of a decode after `-ss 3` on `lossless.wma`, were identical before and after
  - ffprobe and mutagen read the new tag and the same duration
  - the Windows Shell property handler (`System.Media.Duration`, `System.Title`) read all of them correctly

### What the marker writer must do

1. Read the 30-byte Header Object header. Check the GUID and Reserved2 = `0x02`. Walk exactly `count` children within `Object Size`.
2. **Refuse** the file if it has a Digital Signature Object, or the Broadcast flag set, or a Data Object with size 0.
   - Refusing encrypted files is optional, since the header edit does not touch the packets. They are flagged by the Content Encryption `2211B3FB-…`, Extended Content Encryption `298AE614-…` and Advanced Content Encryption `7A079BB6-…` objects ([asffile.cpp L89-91](https://github.com/taglib/taglib/blob/v2.1.1/taglib/asf/asffile.cpp#L89-L91)).
3. Find the ECD object, or create one and add 1 to the header count.
   - Remove any descriptor whose name equals `TAGSHELF_MANUAL`, compared case-insensitively.
   - Append a type-0 descriptor with a NUL-terminated UTF-16LE name and value.
   - Recompute the ECD Object Size and Descriptors Count.
4. If a top-level Padding Object has room, shrink it by the growth. The header size stays the same and the Data Object doesn't move. That is the spec's intended use (§3.17). Otherwise, grow the header: write the new header and then copy the rest of the file after it (tagshelf writes to a temp copy anyway).
5. Set the new Header Object Size.
6. Set File Properties **File Size = the real length of the new file**, not old value + delta.
   - TagLib writes the File Properties object back untouched ([BaseObject::render L195-198](https://github.com/taglib/taglib/blob/v2.1.1/taglib/asf/asffile.cpp#L195-L198)).
   - mutagen likewise stores and re-renders raw bytes ([\_objects.py L255-267](https://github.com/quodlibet/mutagen/blob/ada28b2cc92c515f3f26640a6feef6516d195872/mutagen/asf/_objects.py#L255-L267)).
   - So a file that TagLib or mutagen has already written carries a stale value. **[spike]**: after a go-taglib write, File Size said 87012 on an 88188-byte file. Adding the growth to that value would keep it wrong. Readers and `tag.Sum` must not rely on File Size.
7. Leave File ID alone. The spec says it "shall be regenerated every time the file is modified in any way" and must match the Data Object's File ID (§3.2, §5.1). Neither TagLib nor mutagen does this, and regenerating it would mean writing inside the Data Object header.

Things that do **not** change: Data Packets Count, Data Object size, all Index objects, and the Header Extension. The Header Extension only changes if the writer puts the marker in Metadata or Metadata Library, which it shouldn't.

### go-taglib interaction [spike]

- A go-taglib `WriteTags` with `TAGSHELF_MANUAL` on a fresh WMA wrote every standard field but **no marker**. This confirms ticket 08.
- A go-taglib write on a file that already had the marker **kept it**. TagLib parses unknown ECD attributes into its attribute map and re-renders them ([asffile.cpp L293-301, L559-584](https://github.com/taglib/taglib/blob/v2.1.1/taglib/asf/asffile.cpp#L559-L584)).
- `taglib.ReadTags` never lists `TAGSHELF_MANUAL` for WMA, because it is not in TagLib's ASF property map. So the write order "go-taglib, then marker writer" works, but **only `internal/tag` can verify the marker**.
- TagLib writes MusicBrainz IDs with Picard's names, such as `MusicBrainz/Album Artist Id` and `MusicBrainz/Track Id` for the recording **[spike]**. That matches [picard asf.py L163-174](https://github.com/metabrainz/picard/blob/1cbe55c755e5c47dbfcfb2410b8a20622d3fdd4b/picard/formats/asf.py#L163-L174).

### ASF reader

The reader needs these values:

| Value | Where it lives |
|---|---|
| Title, Author | Content Description Object |
| `WM/AlbumTitle`, `WM/AlbumArtist`, `WM/Year`, `WM/TrackNumber`, `WM/PartOfSet`, `MusicBrainz/*`, `TAGSHELF_MANUAL` | ECD Object |

- TagLib puts an attribute in Metadata or Metadata Library, inside the Header Extension, when:
  - it has a stream or language,
  - its value is over 65,535 bytes (for example `WM/Picture`), or
  - it is a GUID ([asffile.cpp L563-583](https://github.com/taglib/taglib/blob/v2.1.1/taglib/asf/asffile.cpp#L563-L583)).
- mutagen does the same ([asf/\_\_init\_\_.py L261-285](https://github.com/quodlibet/mutagen/blob/ada28b2cc92c515f3f26640a6feef6516d195872/mutagen/asf/__init__.py#L261-L285)).
- So Metadata Library is only needed for art. `WM/TrackNumber` may be a DWORD (type 3) rather than a string, so accept both.

## APEv2 (APE, MPC, .aac)

### Layout ([APE Tag Specification 2.000](https://github.com/taglib/taglib/blob/v2.1.1/taglib/ape/ape-tag-format.txt))

- **Header and footer** are 32 bytes each:
  - `APETAGEX`
  - Version (u32 LE): 2000
  - Tag Size (u32): the items plus the footer, **excluding the header**
  - Item Count (u32)
  - Flags (u32)
  - 8 zero bytes
- **Flags:**
  - bit 31: has header
  - bit 30: has *no* footer (mutagen `HAS_NO_FOOTER`, [apev2.py L60-62](https://github.com/quodlibet/mutagen/blob/ada28b2cc92c515f3f26640a6feef6516d195872/mutagen/apev2.py#L60-L62))
  - bit 29: this is the header
  - The spec's table reads "Contains Footer" for bit 30, but writers leave it clear. Both TagLib and mutagen write footer `0x80000000` and header `0xA0000000` **[spike]**.
  - Item flag bits 2–1 give the type: 0 = UTF-8 text, 1 = binary, 2 = external link. Bit 0 is read-only.
- **Item:**
  - value size (u32)
  - flags (u32)
  - key: 2–255 bytes, ASCII 0x20–0x7E, not `ID3`, `TAG`, `OggS` or `MP+` ([mutagen `is_valid_apev2_key` L43-51](https://github.com/quodlibet/mutagen/blob/ada28b2cc92c515f3f26640a6feef6516d195872/mutagen/apev2.py#L43-L51))
  - a NUL byte
  - the value, which is not NUL-terminated
- Multiple text values are separated by NUL ([mutagen APETextValue L592-625](https://github.com/quodlibet/mutagen/blob/ada28b2cc92c515f3f26640a6feef6516d195872/mutagen/apev2.py#L592-L625)). Picard stores cover art as the binary item `Cover Art (Front)`, whose value is `filename\0data`.
- **Keys** are "case sensitive, however it is illegal to use keys which only differ in case and it is recommended that tag reading not be case sensitive" (spec §4).
  - mutagen stores keys lower-cased but remembers the original case for writing ([`_CIDictProxy` L233-260](https://github.com/quodlibet/mutagen/blob/ada28b2cc92c515f3f26640a6feef6516d195872/mutagen/apev2.py#L233-L260)).
  - TagLib keys its item map on the upper-cased key and keeps each item's own key ([apetag.cpp L411-418, L495](https://github.com/taglib/taglib/blob/v2.1.1/taglib/ape/apetag.cpp#L411-L418)).
  - tagshelf should do the same: match case-insensitively, and keep the existing case when replacing an item.
- **Item order:** the spec says "should be sorted ascending by size" but that is not required. mutagen sorts by length ([L437-440](https://github.com/quodlibet/mutagen/blob/ada28b2cc92c515f3f26640a6feef6516d195872/mutagen/apev2.py#L437-L440)), TagLib by key.

### Placement

The layout is `[ID3v2] audio [APEv2 (header, items, footer)] [ID3v1]`.

- **Finding the tag**
  - TagLib first looks for ID3v1 (`TAG` at -128, unless the bytes there are `APETAGEX`), then for an APE footer at 32 bytes before ID3v1, or else at the end of the file ([tagutils.cpp L39-105](https://github.com/taglib/taglib/blob/v2.1.1/taglib/tagutils.cpp#L39-L105)).
  - mutagen also handles `APE footer + ID3v1`, `APE + Lyrics3v2 + ID3v1`, and a tag at the start of the file ([`__find_metadata` L120-171](https://github.com/quodlibet/mutagen/blob/ada28b2cc92c515f3f26640a6feef6516d195872/mutagen/apev2.py#L120-L171)).
- **APE (Monkey's Audio), in TagLib:**
  - An ID3v2 tag at the start is detected only to skip it for audio properties. It is never parsed as a tag and never written.
  - APE is written at its old location, or else before ID3v1, or else at the end.
  - ID3v1 is kept only if it already existed ([apefile.cpp L164-201, L239-294](https://github.com/taglib/taglib/blob/v2.1.1/taglib/ape/apefile.cpp#L164-L201)).
  - **[spike]**: TagLib's `mac-399-id3v2.ape` keeps its stale ID3v2 through a go-taglib write. Today's `tag.ReadFrom` reads that stale ID3v2 title (`TitleXXXX`) instead of the APE tag. So the new reader must detect `MAC ` after an ID3v2 tag and prefer APEv2.
- **MPC:** the same scheme ([mpcfile.cpp L124-200](https://github.com/taglib/taglib/blob/v2.1.1/taglib/mpc/mpcfile.cpp#L124-L145)). SV7 starts with `MP+`, SV8 with `MPCK`.
- **.aac (ADTS):**
  - TagLib opens `.aac` as `MPEG::File` ([fileref.cpp L156](https://github.com/taglib/taglib/blob/v2.1.1/taglib/fileref.cpp#L156)), which recognises ADTS headers ([mpegheader.cpp L113, L195-215](https://github.com/taglib/taglib/blob/v2.1.1/taglib/mpeg/mpegheader.cpp#L195-L215)).
  - `setProperties` writes ID3v2 (and ID3v1 if one exists). It never writes APE.
  - `save()` re-renders any existing APE tag unchanged, and with `Duplicate` it appends ID3v1 ([mpegfile.cpp L185-300](https://github.com/taglib/taglib/blob/v2.1.1/taglib/mpeg/mpegfile.cpp#L185-L193)).
  - **[spike]** on an .aac that already had Picard's APE tag, a go-taglib write:
    - inserted ID3v2.4 with the new title
    - appended ID3v1
    - left the APE items stale (`Title=P Title`), re-sorted by key but with their case kept

    This confirms that tagshelf's own APEv2 writer is needed for .aac.
- **mutagen/Picard quirk [spike]:** `APEv2.save` truncates from an *existing* APE tag's start, which also drops a trailing ID3v1. When there is no APE tag yet, it appends at EOF, **after** an existing ID3v1, giving `audio TAG APETAGEX` ([apev2.py L400-459](https://github.com/quodlibet/mutagen/blob/ada28b2cc92c515f3f26640a6feef6516d195872/mutagen/apev2.py#L400-L459)). A later TagLib write then appends a second ID3v1 after the APE tag **[spike]**.
- **What tagshelf's APEv2 writer should do:**
  - Replace an existing tag in place.
  - Otherwise insert before a trailing ID3v1, or at EOF.
  - Always write a header and a footer with version 2000.
  - Keep items it doesn't own, such as cover art, ReplayGain and so on.
  - Never touch ID3v2 or ID3v1.

### Picard key names (`picard/formats/apev2.py`)

`_get_tag_name` checks, in order:

1. the name's original case from the loaded file (`__casemap`)
2. `date`→`Year`, `tracknumber`/`totaltracks`→`Track`, `discnumber`/`totaldiscs`→`Disc`
3. the `__translate` table
4. otherwise Python `str.title()`

([L111-142](https://github.com/metabrainz/picard/blob/1cbe55c755e5c47dbfcfb2410b8a20622d3fdd4b/picard/formats/apev2.py#L111-L142), [L284-300](https://github.com/metabrainz/picard/blob/1cbe55c755e5c47dbfcfb2410b8a20622d3fdd4b/picard/formats/apev2.py#L284-L300))

| tagshelf field | Picard internal name | APEv2 key written by Picard | TagLib's key (go-taglib) |
|---|---|---|---|
| artist | `artist` | `Artist` | `ARTIST` |
| album artist | `albumartist` | `Album Artist` | `ALBUM ARTIST` |
| album | `album` | `Album` | `ALBUM` |
| title | `title` | `Title` | `TITLE` |
| track | `tracknumber` + `totaltracks` | `Track` = `3/12` ([L231-234](https://github.com/metabrainz/picard/blob/1cbe55c755e5c47dbfcfb2410b8a20622d3fdd4b/picard/formats/apev2.py#L231-L234)) | `TRACK` |
| disc | `discnumber` + `totaldiscs` | `Disc` = `1/2` | `DISC` |
| year/date | `date` | `Year` (read back as `date`, sanitised) | `YEAR` |
| MB artist ID | `musicbrainz_artistid` | `Musicbrainz_Artistid` | `MUSICBRAINZ_ARTISTID` |
| MB album artist ID | `musicbrainz_albumartistid` | `Musicbrainz_Albumartistid` | `MUSICBRAINZ_ALBUMARTISTID` |
| MB release ID | `musicbrainz_albumid` | `Musicbrainz_Albumid` | `MUSICBRAINZ_ALBUMID` |
| MB release group ID | `musicbrainz_releasegroupid` | `Musicbrainz_Releasegroupid` | `MUSICBRAINZ_RELEASEGROUPID` |
| MB **recording** ID | `musicbrainz_recordingid` | `musicbrainz_trackid` (lowercase, translated) | `MUSICBRAINZ_TRACKID` |
| MB **release track** ID | `musicbrainz_trackid` | `musicbrainz_releasetrackid` (lowercase, translated) | `MUSICBRAINZ_RELEASETRACKID` |
| marker | n/a | Picard would write `Tagshelf_Manual` | `TAGSHELF_MANUAL` (decided in ticket 08) |

The `str.title()` output was checked with Python, and the whole Picard column was written with mutagen **[spike]**.

- Picard reads keys lower-cased, so every casing in this table interoperates with Picard and TagLib ([L156-202](https://github.com/metabrainz/picard/blob/1cbe55c755e5c47dbfcfb2410b8a20622d3fdd4b/picard/formats/apev2.py#L156-L202); [apetag.cpp keyConversions L185-199](https://github.com/taglib/taglib/blob/v2.1.1/taglib/ape/apetag.cpp#L185-L199)).
- **Picard's .aac settings:** `aac_save_ape` defaults to True, and `remove_ape_from_aac` to False ([options.py L183, L524](https://github.com/metabrainz/picard/blob/1cbe55c755e5c47dbfcfb2410b8a20622d3fdd4b/picard/options.py#L183)). `AACFile` writes APEv2 only ([apev2.py L393-419](https://github.com/metabrainz/picard/blob/1cbe55c755e5c47dbfcfb2410b8a20622d3fdd4b/picard/formats/apev2.py#L393-L419)).
- **`mbz` mismatch:** `mbz` maps the raw key `musicbrainz_trackid` to `mbz.Track`, the *release track* ([mbz.go](../../internal/tag/mbz/mbz.go)). Picard uses that key for the *recording* in Vorbis and APEv2. The APEv2 reader, and today's Vorbis path, must map `musicbrainz_trackid`→`Recording` and `musicbrainz_releasetrackid`→`Track`. Nothing reads `mbz.Track` or `mbz.Recording` today.

## `tag.Sum`: audio byte ranges

Today, `Sum` hashes everything after a leading ID3v2 tag, minus the last 128 bytes, even when there is no ID3v1 tag. For a file without a leading ID3v2 tag it drops the last 128 bytes unconditionally ([sum.go](../../internal/tag/sum.go)). **[spike]**: across a go-taglib write it changed for every WAV, WMA, APE, MPC and .aac fixture.

| Format | Detect (after skipping any leading ID3v2) | Hash |
|---|---|---|
| WAV | `RIFF`....`WAVE` | the `data` chunk payload `[off+8, off+8+size)`, **excluding** the pad byte after an odd size. Walk chunks with `size + size%2` ([rifffile.cpp L282-344](https://github.com/taglib/taglib/blob/v2.1.1/taglib/riff/rifffile.cpp#L282-L344)). |
| ASF | Header GUID | `[H+50, H+DataObjectSize)`, where `H` is the Header Object size. This skips the Data Object's File ID and packet-count header, and leaves out the Index objects. |
| APE | `MAC ` | `[end of ID3v2, start of trailing tags)` |
| MPC | `MP+` (SV7) or `MPCK` (SV8) | the same |
| ADTS .aac | 12-bit sync `0xFFF` with layer bits `00` (`b[1] & 0xF6 == 0xF0`). This distinguishes it from MP3, whose layer bits are non-zero. | the same |

- **Trailing tags:**
  1. Strip ID3v1 (`TAG` at -128, but not when those bytes are `APETAGEX`).
  2. Strip an APEv2 tag whose footer ends there: `size`, plus 32 more if the header flag is set.
  3. Strip ID3v1 again, for the mutagen `audio TAG APE` order.
  4. Optionally strip Lyrics3v2, as mutagen does.
- **Leading ID3v2:** use `10 + synchsafe size`, plus 10 more if the v2.4 footer flag (`0x10`) is set. `SumID3v2` misses that footer today. TagLib uses `completeTagSize`.
- **[spike]:** the same SHA-1 held across all of these:
  - WAV (even, and odd-sized `data`): ffmpeg original, go-taglib write, mutagen `id3 ` write
  - WMA: original, marker writer, go-taglib on top, a second marker, mutagen
  - APE: plain, with ID3v2, TagLib's `mac-399-tagged` (APE + ID3v1), go-taglib write
  - MPC: plain and go-taglib write
  - .aac: plain, Picard-style APE, go-taglib ID3v2+ID3v1, both orders mixed
- **Keep these branches ahead of the current fallback:** `Sum` falls through to `SumID3v1`, which never checks for `TAG`, so an unknown format loses its last 128 bytes.
- Also, today's `Sum` goes to `SumID3v2` for APE, MPC and .aac files that start with ID3v2. That path would hash the APE tag.

## WAV

- **Chunk names:** TagLib reads `ID3 ` or `id3 ` and writes `ID3 ` ([wavfile.cpp L148-182, L198-232](https://github.com/taglib/taglib/blob/v2.1.1/taglib/riff/wav/wavfile.cpp#L198-L232)). mutagen (Picard) normalises to and writes `id3 ` ([wave.py L38-45, L128-140](https://github.com/quodlibet/mutagen/blob/ada28b2cc92c515f3f26640a6feef6516d195872/mutagen/wave.py#L38-L45)).
- **Where the chunk goes:** both append new chunks at the end, after `data`, pad to even, and rewrite the RIFF size ([rifffile.cpp L186-244, L361-372](https://github.com/taglib/taglib/blob/v2.1.1/taglib/riff/rifffile.cpp#L186-L244)).
- **What go-taglib does to a WAV [spike]:**
  - It removed ffmpeg's `LIST/INFO` (ISFT) that sat before `data`, so `data` moved from offset 78 to 44.
  - It then appended `ID3 ` (ID3v2.4) and a new `LIST/INFO`. `setProperties` writes both tags ([wavfile.cpp L132-136](https://github.com/taglib/taglib/blob/v2.1.1/taglib/riff/wav/wavfile.cpp#L132-L136)).
  - Whole-file hashes therefore change, while the `data` payload hash does not.
- **Reading the chunk:** `readID3v2Header` and `readID3v2Frames` take a plain reader and count bytes relative to the tag. So `tag.ReadID3v2Tags(io.NewSectionReader(f, chunkOff+8, chunkSize))` works unchanged, and `mbz` works on the result.
  - **[spike]:** it read TagLib's v2.4 chunk (all fields, TXXX, UFID, `TXXX:TAGSHELF_MANUAL`) and mutagen's v2.3 `id3 ` chunk.
- **INFO:**
  - TagLib maps IART, IPRD, INAM, ICRD, IPRT (track), IGNR and others. It encodes them as UTF-8 ([infotag.cpp L61-69, L170-199](https://github.com/taglib/taglib/blob/v2.1.1/taglib/riff/wav/infotag.cpp#L170-L199)).
  - Picard writes INFO by default (`write_wave_riff_info`), encoded as `windows-1252` ([options.py L674, L693](https://github.com/metabrainz/picard/blob/1cbe55c755e5c47dbfcfb2410b8a20622d3fdd4b/picard/options.py#L693)). It uses ITRK for the track, not IPRT. On load it fills only fields the ID3 tag lacks ([wav.py L38-59, L180-190](https://github.com/metabrainz/picard/blob/1cbe55c755e5c47dbfcfb2410b8a20622d3fdd4b/picard/formats/wav.py#L175-L205)).
  - INFO never carries MBIDs or the marker.
  - **Recommendation:** read the ID3 chunk first. Read INFO only as a per-field fallback for title, artist, album, date and track, which covers WAVs tagged by Explorer, foobar2000 or Audacity. Decode it as UTF-8 if valid, else as Windows-1252. That costs about 60–80 lines, and it can be deferred.
- **Out of scope:** RF64/BW64 (files over 4 GB), and a `data` size of 0 or `0xFFFFFFFF` in streamed WAVs. Treat these as "no Sum" or as "hash to EOF".

## Rough size

Estimated against `internal/tag` today: `id3v1.go` 144 lines, `vorbis.go` 266 and `mp4.go` 379.

| Piece | Lines (Go, excluding tests) |
|---|---|
| Detection after a leading ID3v2 (`MAC `, `MP+`/`MPCK`, ADTS, `RIFF/WAVE`, ASF GUID) in `ReadFrom`, `Identify` and `Sum` | 50–80 |
| RIFF chunk walker + WAV ID3 reader | 60–90 |
| WAV INFO fallback (optional) | 60–80 |
| Trailing-tag locator (ID3v1, APEv2, repeat, Lyrics3v2), shared by the reader, writer and `Sum` | 60–90 |
| APEv2 reader + `Metadata` implementation (case-insensitive keys, `n/total`, binary art) | 200–250 |
| APEv2 writer (render, replace in place or insert before ID3v1, keep foreign items, Picard key names) | 130–180 |
| ASF reader (header walk, Content Description, ECD, Metadata Library for `WM/Picture`, typed values) + `Metadata` | 250–320 |
| ASF marker-only writer (the spike version was about 110 lines, without padding reuse or refusals) | 130–180 |
| `Sum` for the five formats | 60–80 |
| **Total** | **about 1,000–1,350**, plus about 600–800 lines of tests |

## Test fixtures

- **ffmpeg** (tagshelf can generate and commit its own; the `lavfi` sine source has no copyright). Command lines are in the spike section.
  - WAV: `pcm_s16le` for even and `pcm_u8` at 8001 Hz for an odd `data` size (ffmpeg writes the pad byte)
  - WMA: `wmav2`; ffmpeg writes no Index objects
  - ADTS: `-c:a aac -f adts`
  - **No APE or MPC encoder** exists in ffmpeg 7.1 (`ffmpeg -encoders`) **[spike]**
- **TagLib v2.1.1 `tests/data`** ([tree](https://github.com/taglib/taglib/tree/v2.1.1/tests/data); LGPL-2.1/MPL-1.1):
  - `mac-399.ape` (85 KB), `mac-399-id3v2.ape`, `mac-399-tagged.ape` (APE + ID3v1)
  - `mac-396.ape`, `mac-390-hdr.ape`, `zerodiv.ape`, `longloop.ape`
  - `click.mpc` (1.6 KB, SV7), `sv8_header.mpc`, `sv4/5_header.mpc`
  - `silence-1.wma`, and `lossless.wma` (99 KB, **with Index + Simple Index**)
  - `empty1s.aac`
  - WAVs: `empty.wav`, `alaw.wav`, `float64.wav`, `uint8we.wav`, `duplicate_tags.wav`, `invalid-chunk.wav`, `zero-size-chunk.wav`, `pcm_with_fact_chunk.wav`, `segfault.wav`, `infloop.wav`
- **mutagen `tests/data`** (GPL-2.0+):
  - `silence-2s-PCM-*-ID3v23.wav`, `silence-{1,2,3}.wma`, `issue_29.wma`
  - `empty.aac`, `adif.aac` (ADIF: out of scope)
  - `click.mpc`, `apev2-lyricsv2.mp3`, `audacious-trailing-id32-apev2.mp3`, `145-invalid-item-count.apev2`
- **Suggestion:** `internal/tag` is BSD-2, and tagshelf never decodes audio. So for APE and MPC, synthesise the container in Go tests: the magic bytes plus opaque payload, then APE/ID3 tags built by the writer or by mutagen. Add third-party files only after Zach decides on licensing.
  - `lossless.wma` is the only fixture found that has ASF Index objects. It is the most valuable one to borrow, or else to generate with Windows Media Encoder or Expression Encoder.

## Reproducing the spike

The spike was a scratch module (`go 1.25`, `go.senan.xyz/taglib v0.14.0`) holding a copy of `internal/tag`, with these commands:

- `dump`: walks the ASF header and top-level objects
- `mark`: the marker writer described above
- `sum`: the audio ranges proposed above
- `wav`: walks RIFF chunks and parses the ID3 chunk with `tag.ReadID3v2Tags` on an `io.SectionReader`
- `ape`: prints the ID3v2/APEv2/ID3v1 layout
- `tlwrite` and `tlread`: go-taglib with all tagshelf fields plus `TAGSHELF_MANUAL`
- `cur`: today's `tag.ReadFrom` and `tag.Sum`

Fixtures:

- `ffmpeg -f lavfi -i sine=f=440:d=10 -c:a wmav2 -b:a 64k ff.wma`
- `… -c:a aac -f adts ff.aac`
- `… sine=f=440:d=1:sample_rate=8001 -c:a pcm_u8 odd.wav`
- TagLib's `mac-399*.ape`, `click.mpc`, `silence-1.wma` and `lossless.wma`

Picard-style tags were written with mutagen's `APEv2`, `WAVE` and `ASF` classes, using the key names in the table above.

Checks:

- `ffmpeg -f md5` of the full decode, and of a decode after `-ss 3`
- `ffprobe` and mutagen read-back
- the Windows Shell `ExtendedProperty("System.Media.Duration")` and `System.Title`

## Sources

- ASF specification rev 01.20.02 (June 2004), as read: https://mail.kde.org/pipermail/taglib-devel/attachments/20041010/da87efc4/ASF_Specification-0001.pdf ; rev 01.20.03 from Microsoft: https://download.microsoft.com/download/7/9/0/790fecaa-f64a-4a5e-a430-0bccdab3f1b4/asf_specification.doc ; overview: https://www.loc.gov/preservation/digital/formats/fdd/fdd000067.shtml
- APEv2 specification (bundled with TagLib): https://github.com/taglib/taglib/blob/v2.1.1/taglib/ape/ape-tag-format.txt ; https://wiki.hydrogenaud.io/index.php?title=APE_key
- TagLib v2.1.1: https://github.com/taglib/taglib/tree/v2.1.1 (asf/asffile.cpp, ape/apefile.cpp, ape/apetag.cpp, mpc/mpcfile.cpp, mpeg/mpegfile.cpp, mpeg/mpegheader.cpp, riff/rifffile.cpp, riff/wav/wavfile.cpp, riff/wav/infotag.cpp, tagutils.cpp, fileref.cpp, tests/data)
- Picard @ 1cbe55c: https://github.com/metabrainz/picard/tree/1cbe55c755e5c47dbfcfb2410b8a20622d3fdd4b (picard/formats/apev2.py, asf.py, wav.py, mutagenext/aac.py, mutagenext/apev2.py, options.py)
- mutagen @ ada28b2: https://github.com/quodlibet/mutagen/tree/ada28b2cc92c515f3f26640a6feef6516d195872 (mutagen/apev2.py, mutagen/asf/\_\_init\_\_.py, mutagen/asf/\_objects.py, mutagen/wave.py, tests/data)
- go-taglib v0.14.0: https://github.com/sentriz/go-taglib/tree/v0.14.0
- tagshelf: `internal/tag/{sum.go,tag.go,id.go,id3v2.go,vorbis.go}`, `internal/tag/mbz/mbz.go`, `docs/research/tag-writing.md`
