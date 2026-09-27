# Tag-based music file organizers: survey

Researched: 2026-09-27

Question: What other programs rename or move music files into folders based on their tags?

Method: I used only primary sources: official docs, official websites, source repositories, and official wikis. Every claim below links to the page it came from. "Not documented" means the pages I checked did not cover the point. It does not mean the feature is missing.

## Summary

Many tools can do this. They fall into four groups:

- **Library managers with a CLI**
  - [beets](#beets) (Python, MIT) is the most established one. It matches files against MusicBrainz, then moves or copies them into a templated `$albumartist/$album/$track $title` tree. Its `move`/`update` commands reorganize an existing library, and it can detect duplicates.
  - [wrtag](#wrtag) (Go, GPL-3.0) is the closest in spirit to this repo: a Go CLI built around MusicBrainz, with `move`/`copy`/`sync`, "keep-file" extras, and a consistency check that prevents duplicates.
- **Taggers with a rename-to-folders feature (GUI):** [MusicBrainz Picard](#musicbrainz-picard), [Mp3tag](#mp3tag), [TagScanner](#tagscanner), [Kid3](#kid3) (which also has `kid3-cli`), [puddletag](#puddletag), [OneTagger](#onetagger) (which also ships a CLI build), and the commercial [Jaikoz](#jaikoz) and [SongKong](#songkong) (SongKong also has a CLI).
- **Players or library apps that auto-organize:** [foobar2000 File Operations](#foobar2000-file-operations-foo_fileops), [MediaMonkey](#mediamonkey), and [MusicBee](#musicbee-partially-verified). I could only partly verify MusicBee.
- **Download managers that rename on import:** [Lidarr](#lidarr).

Two points stand out. First, **none of the tools' documentation describes rewriting the `FILE` lines inside `.cue` sheets after renaming audio files.** Some tools move `.cue` files along with the audio (Picard, beets via the third-party filetote plugin, MusicBee per its wiki), but none documents fixing the references inside them. Second, only Lidarr, beets and wrtag make MusicBrainz IDs central to organizing. SongKong, Jaikoz and Picard use MusicBrainz or AcoustID for matching, then rename.

## Comparison table

Legend: Y = documented, N = documented as absent, ? = not documented. Details and citations are in the per-tool sections.

| Tool | Platforms | License / cost | Interface | Moves into tag-pattern folders | MusicBrainz | Companion files (art/cue/log) | Duplicate handling | Re-organize existing library in place | Rewrites cue `FILE` lines |
|---|---|---|---|---|---|---|---|---|---|
| beets | Cross-platform (Python) | MIT, free | CLI | Y | Y (autotagger, IDs) | Via third-party plugins (filetote, copyartifacts); art as `cover.jpg` | Y (import dup action; `duplicates` plugin, optional checksum) | Y (`move`, `update`) | ? |
| wrtag | Linux, macOS, Windows | GPL-3.0, free | CLI + web UI | Y | Y (core) | Y (`-keep-file`, covers) | Y ("no duplicates" validation) | Y (`move` in place, `sync`) | ? |
| MusicBrainz Picard | Linux, macOS, Windows | GPL-2.0+, free | GUI | Y | Y (official MB tagger, fingerprints) | Y ("Move additional files", e.g. `*.cue *.log`) | ? | Y (move/rename on save) | ? |
| Mp3tag | Windows (free download); macOS (paid) | Proprietary | GUI | Y (`\` in Tag→Filename) | Y (lookup) | ? | ? | Y (rename in place) | ? |
| foobar2000 File Operations | Windows (fb2k also macOS/mobile) | Freeware | GUI | Y | ? | Y ("Move entire source folder content") | ? | Y | ? |
| MediaMonkey | Windows (+ Android app) | Free; Gold (paid) for background rules | GUI | Y | Auto-Tag (sources not named on page) | Prompts to move accompanying files | Y (title or content hash) | Y | ? |
| MusicBee | Windows | Free | GUI | Unverified | ? | Unverified | ? | Unverified | ? |
| TagScanner | Windows 7–11 | Download with donations; license not stated | GUI | Rename by tags; folder wizard | Y (lookup) | ? | ? | ? | ? |
| Kid3 | Linux, Windows, macOS, Android | GPL, free | GUI + `kid3-cli` | Y (filename + "Rename/Create folder") | Y (release + fingerprint import) | ? | ? | Y | ? |
| puddletag | Linux (primarily) | GPLv3, free | GUI | Y (`/` in Tag→File; Tag to Dir) | Y (lookup) | ? | ? | Y | ? |
| OneTagger | Windows, macOS, Linux | GPL-3.0, free | GUI + CLI build | Renames by template (folders not stated) | Y (lookup) | ? | ? | ? | ? |
| Lidarr | Windows, Linux, macOS, others | GPL-3.0, free | Web app (server) | Y | Y (core, matching weighted on MBIDs) | Only sidecars matching the track's name, by extension | ? | Y ("Rename Files" bulk action) | ? |
| SongKong | macOS, Windows, Linux, NAS, Docker | Commercial (free preview) | GUI + CLI | Y | Y (+ AcoustID) | Y (options for non-music files) | Y (AcoustID + MB release) | Y | ? |
| Jaikoz | macOS, Windows, Linux | Commercial (free trial) | GUI | Y | Y (+ AcoustID) | ? | Y (filters by MB Recording Id / AcoustID) | Y | ? |

## Per-tool details

### beets

- **What it is:** "the media library management system for obsessive music geeks." It is written in Python, MIT-licensed, and cross-platform. It gets metadata from "MusicBrainz, Discogs, and Beatport." ([repo](https://github.com/beetbox/beets))
- **Interface:** CLI ([repo](https://github.com/beetbox/beets))
- **Folder patterns:** The default paths are `default: $albumartist/$album%aunique{}/$track $title`, `singleton: Non-Album/$artist/$title`, and `comp: Compilations/$album%aunique{}/$track $title`. ([config](https://beets.readthedocs.io/en/stable/reference/config.html))
- **Album artist vs track artist:** The docs recommend `$albumartist` over `$artist` so that compilations don't get split across folders. Each field falls back to the other when one is missing. ([path formats](https://beets.readthedocs.io/en/stable/reference/pathformat.html))
- **Same-name albums:** `%aunique{}` tells apart albums that share a name by adding things like `[2008]`. ([path formats](https://beets.readthedocs.io/en/stable/reference/pathformat.html))
- **Multi-disc:** `disc` and `disctotal` are available in templates. With `per_disc_numbering`, the docs suggest a pattern like `$disc-$track`. ([config](https://beets.readthedocs.io/en/stable/reference/config.html), [path formats](https://beets.readthedocs.io/en/stable/reference/pathformat.html))
- **Move, copy or leave in place:**
  - Import copies by default. `-m` moves instead, and `-C`/`-M`/`-W` leave files untouched. ([CLI](https://beets.readthedocs.io/en/stable/reference/cli.html))
  - Config offers `move`, `copy`, `link`, `hardlink` and `reflink`. ([config](https://beets.readthedocs.io/en/stable/reference/config.html))
- **Re-organizing an existing library:** `beet move` "acts as a library consolidator: items matching the query are renamed into your library directory structure". `beet update` re-reads tags and renames files by default. ([CLI](https://beets.readthedocs.io/en/stable/reference/cli.html))
- **Duplicates:**
  - On import, `duplicate_action` can be `skip`, `keep`, `remove`, `merge`, `upgrade` or `ask`. `duplicate_keys` defaults to `albumartist album` for albums and `artist title` for items. ([config](https://beets.readthedocs.io/en/stable/reference/config.html))
  - The core `duplicates` plugin works on MusicBrainz IDs by default. It can take an arbitrary `checksum` command and can delete, move, copy, remove or merge duplicates. ([duplicates plugin](https://beets.readthedocs.io/en/stable/plugins/duplicates.html))
- **Companion files:**
  - The core `fetchart` plugin saves `cover.jpg` in the album directory, and `art_filename` is configurable. ([fetchart](https://beets.readthedocs.io/en/stable/plugins/fetchart.html), [config](https://beets.readthedocs.io/en/stable/reference/config.html))
  - Other non-music files need third-party plugins: `copyartifacts` and `filetote`. ([plugin index](https://beets.readthedocs.io/en/stable/plugins/index.html))
  - filetote (MIT) matches extras by extension, filename or glob, including `.cue` and `.log`. It renames them with path templates and works during import, `move`, `modify` and `update`. ([filetote](https://github.com/gtronset/beets-filetote))
- **Cue sheets:** The plugin index has no core cue plugin. None of the pages checked describe rewriting `.cue` contents. ([plugin index](https://beets.readthedocs.io/en/stable/plugins/index.html))

### wrtag

- **What it is:** "Fast automated music tagging and organisation based on MusicBrainz." It is written in Go, GPL-3.0-licensed, and ships static binaries for Linux, macOS and Windows. ([README](https://github.com/sentriz/wrtag))
- **Interface:** The `wrtag` CLI has `copy`, `move` and `reflink` subcommands for single release folders and `sync` for bulk work. `wrtagweb` is a web UI with a job queue. ([README](https://github.com/sentriz/wrtag))
- **Folder patterns:** `path-format` is a Go `text/template` filled from MusicBrainz release data. Validation ensures all tracks of a release, including multi-disc releases, land in one directory. ([README](https://github.com/sentriz/wrtag))
- **Multi-disc:** `.Media.Position`, `.Media.Title` and similar fields are available. An example pattern prefixes `d01` when the release has more than one medium. ([README](https://github.com/sentriz/wrtag))
- **Album artist:** wrtag writes `ALBUMARTIST`, `ALBUMARTISTS` and their credit variants. Paths use `.Release.Artists`. The example path adds track artists only for compilations. ([README](https://github.com/sentriz/wrtag))
- **MusicBrainz:**
  - It matches releases from existing tags or filenames. `MUSICBRAINZ_ALBUMID` or `-mbid` overrides the match, and a low match score needs confirmation. ([README](https://github.com/sentriz/wrtag))
  - `sync` re-tags a release directly when `MUSICBRAINZ_ALBUMID` is present. Otherwise it re-tags only on a high match score. ([README](https://github.com/sentriz/wrtag))
- **Companion files:** It organizes "music files, covers, and configurable extra files". `-keep-file` defines "an extra file path to keep when moving/copying". It can fetch covers from the Cover Art Archive. ([README](https://github.com/sentriz/wrtag))
- **Duplicates:** "Validation to ensure your library is always consistent with no duplicates or unrecognised paths." ([README](https://github.com/sentriz/wrtag))
- **In-place re-organize:** "a re-tag is just a move pointed at some music that is already in place". `sync` re-processes the whole library. ([README](https://github.com/sentriz/wrtag))
- **Cue sheets:** not documented.

### MusicBrainz Picard

- **What it is:** "The official MusicBrainz tagger." It is GPL-2.0-or-later, runs on Linux, macOS and Windows, and is a GUI. It uses the MusicBrainz database and audio fingerprints. ([repo](https://github.com/metabrainz/picard))
- **Moving and renaming:** Picard has "Move files when saving" to a destination directory, "Rename files when saving", and "Delete empty directories". ([file naming options](https://picard-docs.musicbrainz.org/en/v3.0/config/options_filerenaming.html))
- **Companion files:** "Move additional files" takes patterns such as `Folder.jpg *.png *.cue *.log`. Unless overwrite is selected, an extra file that already exists at the destination is skipped. ([file naming options](https://picard-docs.musicbrainz.org/en/v3.0/config/options_filerenaming.html))
- **Default naming script:** From the source code ([defaults.py](https://raw.githubusercontent.com/metabrainz/picard/master/picard/const/defaults.py)):
  ```
  $if2(%albumartist%,%artist%)/
  $if(%albumartist%,%album%/,)
  $if($gt(%totaldiscs%,1),$if($gt(%totaldiscs%,9),$num(%discnumber%,2),%discnumber%)-,)$if($and(%albumartist%,%tracknumber%),$num(%tracknumber%,2) ,)$if(%_multiartist%,%artist% - ,)%title%
  ```
  - It falls back from album artist to track artist.
  - It adds a disc prefix only when `totaldiscs > 1`.
  - It adds the track artist to the filename for multi-artist albums.
- **Cover art:** Cover images can be saved as separate files using a name template (default `cover`) or embedded in the audio files. ([cover options](https://picard-docs.musicbrainz.org/en/v3.0/config/options_cover.html), [defaults.py](https://raw.githubusercontent.com/metabrainz/picard/master/picard/const/defaults.py))
- **Duplicates:** not documented in the pages checked.
- **Cue rewriting:** not documented. `.cue` files can only be moved as "additional files".

### Mp3tag

- **Platforms and cost:** Windows builds (Windows 7–11) are a download, and the site has a donate link but no license terms on the pages checked. The Mac version is sold standalone and on the Mac App Store, with a trial. ([download](https://www.mp3tag.de/en/download.html), [Mac](https://mp3tag.app/))
- **Features:** It can "Rename files based on the tag information" and import from "Discogs, freedb, MusicBrainz". ([home](https://www.mp3tag.de/en/))
- **Folders:** With a backslash in a Convert → Tag - Filename format string, "Mp3tag creates a folder from the part of the format string in front of a backslash". Absolute destinations such as `D:\Music\%artist% - %year% - %album%\$num(%track%,2). %title%` are allowed. ([Rename Files](https://docs.mp3tag.de/converters/rename-files/))
- **Unsafe characters in tags:** A setting replaces folder separators found in tag values. ([Files options](https://docs.mp3tag.de/customization/options/files/))
- **Cover art:** The actions list includes "Export Cover to File". There are no move or copy actions for audio files. ([actions](https://docs.mp3tag.de/actions/))
- **Not documented:** moving companion files, duplicates, cue sheets, and multi-disc or album-artist defaults.

### foobar2000 File Operations (foo_fileops)

- **What it is:** foobar2000 is freeware for Windows, macOS, Android and iOS. ([foobar2000.org](https://www.foobar2000.org/)) File Operations is a first-party component by Peter Pawlowski, the foobar2000 author. ([Hydrogenaudio wiki, archived](https://web.archive.org/web/2025id_/https://wiki.hydrogenaudio.org/index.php?title=Foobar2000:File_Operations_(foo_fileops)))
- **Operations:** It can "copy, move, rename, and delete files from within foobar2000." The File Name Pattern uses title formatting, and `\` creates subfolders. It has "Copy/Move Entire Source Folder Content", described as "Useful when you have supplementary files such as cover art", plus "Remove Empty Source Folders", a Preview, and Presets. The Media Library and playlists update automatically. (same wiki page)
- **Not documented** on that page: MusicBrainz, duplicates, and cue rewriting.

### MediaMonkey

- **Platforms and cost:** Windows desktop and an Android app, with free and Gold (paid) tiers. ([home](https://www.mediamonkey.com/))
- **What's paid:** Gold adds "Automated Organization": "custom rules for re-organizing files in the background". It also raises the Auto-Tag limit from 20 to 100 files and lifts a one-fingerprint-per-second restriction. ([Gold features](https://www.mediamonkey.com/wiki/WebHelp:Upgrading_to_the_Gold_Version/5.0))
- **Organizing:**
  - Auto-Organize moves and renames files using masks, for example `C:/Music/<Artist>/<Album> - <Title>.xxx`.
  - It can run when files are added or edited.
  - When media files leave a folder, "you will be prompted whether to move any accompanying files (e.g. Artwork or Lyrics files)". "Delete emptied folders" is optional.
  - When two files would get the same path, "MediaMonkey will append a number in parenthesis".
  - "There is no undo!"

  ([Organizing Files and Folders](https://www.mediamonkey.com/wiki/WebHelp:Changing_Filenames/5.0))
- **Album artist and multi-disc:** The masks include `<Album Artist>` and `<Disc#>`, and `$if()` supports conditions. The docs give a compilation example: `\<Album Artist> - <Album>\<Track#> $If(<Artist>=<Album Artist>,,<Artist> - )<Title>`. ([masks](https://www.mediamonkey.com/wiki/WebHelp:Configuring_Directory_and_File_Formats/5.0))
- **Duplicates:** MediaMonkey can find "Duplicate Titles", or duplicates with identical audio signatures (a content analysis enabled in Library options). ([Deleting Duplicates](https://mediamonkey.com/wiki/Deleting_Duplicates))
- **Lookup:** Auto-Tag looks up online sources. The page does not name them. ([Online Lookup](https://www.mediamonkey.com/wiki/WebHelp:Getting_Track_Information_from_the_Internet/5.0))
- **Cue sheets:** not documented.

### MusicBee (partially verified)

- **Verified:** Windows 7–11, free. ([getmusicbee.com](https://getmusicbee.com/))
- **Not verified:** Auto-organize details are covered only on the community MusicBee Wiki (Fandom, `musicbee.fandom.com/wiki/File_Organization`) and in the official FAQ. Both blocked automated fetching (HTTP 402/403 and a Cloudflare challenge), so I could not verify them from a primary page.
- **What search-engine excerpts of that wiki say (unconfirmed):**
  - Files are organized by a naming template.
  - Files missing template tags are skipped by default.
  - An option moves "non-media files (artwork, cue files, lyrics files, etc.)" along with the audio.
- **Before relying on these,** check them in the app or on the wiki yourself.

### TagScanner

- **Platform and version:** Windows 7/8/8.1/10/11, version 6.1.21. The download page asks for donations and does not state a license. ([download](https://www.xdlab.ru/en/download.htm))
- **Features:** "Rename files based on the tag and file information". It can import "album information and covers from online databases like Discogs or MusicBrainz". The 6.1.21 release notes mention an "improved folder renaming wizard". ([home](https://www.xdlab.ru/en/))
- **Not documented** on the official pages checked: moving files into a folder tree, companion files, duplicates, and cue.

### Kid3

- **What it is:** GPL, KDE project, for Linux, Windows, macOS and Android. Features include generating filenames from tags, creating directories from tags, and importing from MusicBrainz and Discogs. ([kid3.kde.org](https://kid3.kde.org/))
- **Folders:**
  - Tools → Rename Folder renames the current folder from tags. "If a folder separator '/' is found in the format, multiple folders are created".
  - It can create a new folder instead of renaming, and it shows a preview.
  - Codes include `%{artist}`, `%{album}`, `%{year}`, and the aggregates `%{max-year}`/`%{unq-year}`.

  ([handbook source](https://raw.githubusercontent.com/KDE/kid3/master/doc/en/index.docbook))
- **CLI:** `kid3-cli` offers `renamedir FORMAT create|rename|dryrun` and `fromtag` (filename from tags). (same)
- **MusicBrainz:** Import "From MusicBrainz Release" and "From MusicBrainz Fingerprint", the latter using Chromaprint/AcoustID. (same)
- **Not documented:** album artist and multi-disc defaults, companion files, duplicates, and cue.

### puddletag

- **What it is:** A GUI tag editor "(primarily created) for GNU/Linux similar to the Windows program, Mp3tag". It supports "renaming files based on their tags" and lookups via Amazon, Discogs, FreeDB and MusicBrainz, with optional Chromaprint/AcoustID. ([README](https://github.com/puddletag/puddletag)) License: GPLv3. ([docs](https://docs.puddletag.net/))
- **Folders:** In Tag to filename, "Files can also be moved, by specifying '/' as a directory delimeter". Slashes inside tag values are removed. "Tag to Dir" renames or moves directories, but only on the same filesystem. ([functions doc source](https://raw.githubusercontent.com/puddletag/puddletag/master/docs/_sources/source/function.txt))
- **Not documented:** companion files, duplicates, and cue.

### OneTagger

- **What it is:** GPL-3.0, for Windows, macOS and Linux. It fetches metadata from Beatport, Traxsource, Juno Download, Discogs, MusicBrainz and Spotify. ([repo](https://github.com/Marekkon5/onetagger))
- **Renaming:** Its "AUTO RENAME" module will "rename your files based on tags. The template string defines the format scheme of the new filename". CLI builds are offered for each OS. ([site](https://onetagger.github.io/))
- **Not documented:** whether renaming creates folders, companion files, duplicates, and cue.

### Lidarr

- **What it is:** A music collection manager "for Usenet and BitTorrent users". It is GPL-3.0 and runs on Windows, Linux, macOS, Raspberry Pi and more. ([repo](https://github.com/Lidarr/Lidarr))
- **Folder patterns:**
  - There are three templates: Standard Track Format, Multi Disc Track Format (for example `{Album Title}/{medium:00}-{track:00} {Track Title}`), and Artist Folder Format.
  - "Delete Empty Folders" is optional.
  - Filenames with illegal characters get replaced characters.

  ([settings](https://wiki.servarr.com/lidarr/settings))
- **Tokens:** They include `{Artist MbId}`, `{Album MbId}`, and `{Track ArtistName}` for Various Artists albums. Changing a template later forces a full rename of every file. ([naming guide](https://wiki.servarr.com/lidarr/naming-guide))
- **MusicBrainz:** Import matching gives heavy weight to MusicBrainz Recording and Release IDs. Files without MBIDs fall back to text matching. ([FAQ](https://wiki.servarr.com/lidarr/faq))
- **Companion files:** "Import Extra Files" only picks up sidecars whose name starts with the track's filename, filtered by an extension list. It "ignores files like `cover.jpg` or `album.nfo`". Matched extras are renamed to match the track. ([settings](https://wiki.servarr.com/lidarr/settings))
- **Re-organize:** There is a bulk "Rename Files" action for selected artists. "Library Import" adopts an already-organized collection without moving files. ([library](https://wiki.servarr.com/lidarr/library))
- **Not documented:** cue rewriting and file-level duplicate detection. Lidarr documents quality upgrades instead ([repo](https://github.com/Lidarr/Lidarr)).
- **Source note:** I read the wiki pages from their source repository, [Servarr/Wiki](https://github.com/Servarr/Wiki).

### SongKong

- **What it is:** Commercial, with a free preview mode. It runs on macOS, Windows, Linux, NAS and Docker. It identifies music via MusicBrainz, Discogs, AcoustID and Bandcamp, finds duplicates, and "organizes your files". ([site](https://www.jthink.net/songkong/en/))
- **Renaming:** Rename masks are written as JavaScript expressions. There are preset masks, for example for Plex and iTunes, and separate handling for compilations. "File Mover" treats matched and unmatched songs differently. ([Folder and File Renaming](https://www.jthink.net/songkong/filerenamer.jsp))
- **From the manual:** ([help](https://www.jthink.net/songkong/help/help.html))
  - A Move Folder moves songs "as they are matched".
  - Duplicates by default are songs with "the same acoustid fingerprint, and have been matched to the same song on the same MusicBrainz release". They can be deleted or moved.
  - Settings control what happens to non-music files in a folder when its songs move elsewhere, with options such as moving the artwork along.
  - CLI modes: `-m` fix, `-w` watch, `-d` delete duplicates, `-r` undo.
- **Cue sheets:** not documented.

### Jaikoz

- **What it is:** Commercial, with a trial, for macOS, Windows and Linux. It matches songs via MusicBrainz, Discogs and AcoustID, renames files and organizes folders by tags, and removes duplicates. ([site](https://www.jthink.net/jaikoz/))
- **Renaming:** Metadata-to-filename and subfolder masks use JavaScript, with separate masks for compilation and standard albums. ([File and Folder Correct](https://jthink.net/jaikoz/jsp/help/linux/ch06s03.html))
- **Menus:** "Save And Move..." and "Save And Copy..." are available. Duplicate filters match on the same MB Recording Id, the same AcoustID, or both. ([menus](https://jthink.net/jaikoz/jsp/help/linux/ch09.html))
- **Not documented:** companion files and cue.

## How this compares to our tool (analysis)

This section is my interpretation. It relies on the cited facts above and on reading `main.go`.

Our tool's behavior, for reference:
- Path is `Album Artist/Album (Year)/[disc-]track - Title.ext`, falling back to the track artist when there is no album artist.
- A disc prefix is added only when there are multiple discs or the disc number is above 1.
- Files need MusicBrainz ID tags to be organized; everything else goes to `_Untagged`.
- Companion files follow their album or go to `_Other`.
- Duplicate detection is SHA-256 (byte-identical) at the destination. A different file with the same name gets a " (n)" suffix.
- `-reorganize` re-sorts a library in place.
- `.cue` `FILE` lines are rewritten after renames.
- There is a dry-run mode, and emptied directories are cleaned up.

**Features ours has that others don't document:**
- **Rewriting `.cue` `FILE` references.** This is the clearest difference. Picard, filetote and (reportedly) MusicBee move `.cue` files, but no tool's docs say the paths inside are updated. A renamed album with a cue sheet usually breaks in those tools.
- **Directory-level companion handling with a catch-all.** Lidarr only takes sidecars named like the track and explicitly ignores `cover.jpg`. foobar2000 moves the whole source folder's content. Picard and wrtag use pattern lists. Ours assigns each non-audio file to the album its directory belongs to, and sends the rest to `_Other`. I found no documented equivalent of that `_Other` bucket.
- **Byte-exact dedup at the destination, built in.** beets' duplicates plugin can do checksums, but only through a user-supplied command. MediaMonkey hashes content, but inside its library database. SongKong and Jaikoz use AcoustID plus MBIDs, which catches *acoustic* duplicates and not only byte copies.
- **A single static binary with no database and no network access.** Only wrtag is comparable (Go, static binaries), and it contacts MusicBrainz.

**Features others have that ours lacks:**
- **Actual MusicBrainz lookups and tag writing** (beets, wrtag, Picard, Lidarr, SongKong, Jaikoz, Kid3). Ours only checks that MBID tags exist; it never fetches or corrects metadata.
- **User-configurable path templates** (every tool surveyed). Our layout is fixed.
- **Separate handling for compilations and Various Artists:**
  - beets has a `comp:` path.
  - Picard's default adds `%artist% -` for multi-artist albums.
  - MediaMonkey's docs show a `$If(<Artist>=<Album Artist>...)` example.
  - wrtag adds track artists for compilations.
- **Disambiguating albums with the same name:** beets `%aunique{}`, and wrtag's release disambiguation.
- **Acoustic-fingerprint duplicate detection:** SongKong, Jaikoz, MediaMonkey.
- **Undo:** SongKong `-r`. MediaMonkey explicitly has none, and neither does ours.
- **Cover art fetching:** beets fetchart, wrtag CAA, Picard, MediaMonkey.

**Closest peers:**
- **wrtag:** same language, a CLI, MusicBrainz-centric, extra files, a no-duplicates check, and in-place re-organizing.
- **beets:** a CLI with `move`/`update`, plus the filetote plugin for extras.

Comparing against these two is the most useful. Cue-sheet rewriting stands out as the most distinctive feature of our tool.

## Skipped or only partly covered

- **MusicBee:** partly verified, as described above. Its organize features could not be fetched from a primary page.
- **Small GitHub projects** that turned up in search (`robinst/brainztag`, `AndreaBonn/audio-filename-fixer`, `alisadeghiaghili/music-organizer`, `angelm62/music-organizer`, `ygb4520-cmd/music-library-organizer`): skipped. They are not notable enough to be useful comparisons, and I did not review their docs in depth.
- **Tag&Rename and similar commercial Windows taggers:** not researched. They weren't requested and weren't needed to answer the question.
- **foobar2000 cue handling, and MusicBrainz via third-party components:** not verified. The only File Operations doc reachable was an archived Hydrogenaudio wiki page; the live wiki is behind a Cloudflare challenge.

## Sources

- beets: https://github.com/beetbox/beets ; https://beets.readthedocs.io/en/stable/reference/config.html ; https://beets.readthedocs.io/en/stable/reference/pathformat.html ; https://beets.readthedocs.io/en/stable/reference/cli.html ; https://beets.readthedocs.io/en/stable/plugins/index.html ; https://beets.readthedocs.io/en/stable/plugins/duplicates.html ; https://beets.readthedocs.io/en/stable/plugins/fetchart.html
- beets-filetote: https://github.com/gtronset/beets-filetote
- wrtag: https://github.com/sentriz/wrtag
- Picard: https://github.com/metabrainz/picard ; https://picard-docs.musicbrainz.org/en/v3.0/config/options_filerenaming.html ; https://picard-docs.musicbrainz.org/en/v3.0/config/options_cover.html ; https://raw.githubusercontent.com/metabrainz/picard/master/picard/const/defaults.py
- Mp3tag: https://www.mp3tag.de/en/ ; https://www.mp3tag.de/en/download.html ; https://mp3tag.app/ ; https://docs.mp3tag.de/converters/rename-files/ ; https://docs.mp3tag.de/customization/options/files/ ; https://docs.mp3tag.de/actions/
- foobar2000: https://www.foobar2000.org/ ; https://wiki.hydrogenaudio.org/index.php?title=Foobar2000:File_Operations_(foo_fileops) (read via archive: https://web.archive.org/web/2025id_/https://wiki.hydrogenaudio.org/index.php?title=Foobar2000:File_Operations_(foo_fileops))
- MediaMonkey: https://www.mediamonkey.com/ ; https://www.mediamonkey.com/wiki/WebHelp:Changing_Filenames/5.0 ; https://www.mediamonkey.com/wiki/WebHelp:Configuring_Directory_and_File_Formats/5.0 ; https://www.mediamonkey.com/wiki/WebHelp:Upgrading_to_the_Gold_Version/5.0 ; https://mediamonkey.com/wiki/Deleting_Duplicates ; https://www.mediamonkey.com/wiki/WebHelp:Getting_Track_Information_from_the_Internet/5.0
- MusicBee: https://getmusicbee.com/
- TagScanner: https://www.xdlab.ru/en/ ; https://www.xdlab.ru/en/download.htm
- Kid3: https://kid3.kde.org/ ; https://raw.githubusercontent.com/KDE/kid3/master/doc/en/index.docbook
- puddletag: https://github.com/puddletag/puddletag ; https://docs.puddletag.net/ ; https://raw.githubusercontent.com/puddletag/puddletag/master/docs/_sources/source/function.txt
- OneTagger: https://github.com/Marekkon5/onetagger ; https://onetagger.github.io/
- Lidarr: https://github.com/Lidarr/Lidarr ; https://wiki.servarr.com/lidarr/settings ; https://wiki.servarr.com/lidarr/naming-guide ; https://wiki.servarr.com/lidarr/faq ; https://wiki.servarr.com/lidarr/library ; https://github.com/Servarr/Wiki
- SongKong: https://www.jthink.net/songkong/en/ ; https://www.jthink.net/songkong/filerenamer.jsp ; https://www.jthink.net/songkong/help/help.html
- Jaikoz: https://www.jthink.net/jaikoz/ ; https://jthink.net/jaikoz/jsp/help/linux/ch06s03.html ; https://jthink.net/jaikoz/jsp/help/linux/ch09.html
