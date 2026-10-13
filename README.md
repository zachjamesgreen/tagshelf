# tagshelf

[![CI](https://github.com/zachjamesgreen/tagshelf/actions/workflows/ci.yml/badge.svg)](https://github.com/zachjamesgreen/tagshelf/actions/workflows/ci.yml)

tagshelf sorts audio files into a music library by their tags. It moves each file to `Artist\Album (Year)\NN - Title.ext`, sets identical copies aside without deleting them, and keeps a record of every move so that a run can be undone. It has a command line for running it and a terminal UI for the files that need a person: fixing tags, looking tracks up on MusicBrainz, and deciding which copy of a duplicate to keep.

```text
 tagshelf  T:\Music  library 345d9616
 1 Home   2 Untagged 2   3 Duplicates 1   4 Library   5 Runs   6 Settings
────────────────────────────────────────────────────────────────────────────────────────────────────
Duplicates (1 group to review)
  Song                                  Kind           Copies  Keeps
› Fixture Artist - Silence Café         release-track       2  …bum (2001)\1-03 - Silence Café.flac

Fixture Artist - Silence Café  release-track, 2 copies
  ★ library    Fixture Album Artist\Fixture Album (2001)\1-03 - Silence Café.flac
               FLAC  lossless 8 kHz/16       0:01  Fixture Artist / Fixture Album / Silence Café
    library    Fixture Album Artist\Fixture Album (2001)\1-03 - Silence Café.mp3
               MP3   8 kbps 8 kHz            0:01  Fixture Artist / Fixture Album / Silence Café


↑↓/n select  k keeper  enter resolve  b keep both  c confirm  A all exact   1-5 tabs  R reload  ? h…
```

## Contents

- [Status](#status)
- [Install](#install)
- [Quick start](#quick-start)
- [How files are organized](#how-files-are-organized)
- [Command line](#command-line)
- [Config file](#config-file)
- [The terminal UI](#the-terminal-ui)
- [MusicBrainz lookups](#musicbrainz-lookups)
- [Undo](#undo)
- [Where tagshelf keeps its data](#where-tagshelf-keeps-its-data)
- [On Linux](#on-linux)
- [Building and testing](#building-and-testing)
- [Contributing](#contributing)
- [License and credits](#license-and-credits)

## Status

- **Windows and Linux.** tagshelf is written on Windows 11 and tested on both. The Linux download is one static program for any distribution, on amd64 or arm64: see [On Linux](#on-linux). There is no macOS download.
- **Version 0.x.** Flags, config keys and the database may still change between versions. The database is upgraded on first use by a newer version, and a copy of the old one is kept beside it.
- **Back up first.** tagshelf moves and renames your files and can write tags into them. It never deletes an audio file, and it records what it does, but a tool this young should not be the only thing between you and your only copy. Start with a dry run.

## Install

### Download for Windows

1. Go to [Releases](https://github.com/zachjamesgreen/tagshelf/releases) and download `tagshelf-<version>-windows-amd64.zip`.
2. Unzip it anywhere and run `tagshelf.exe` from a terminal (Windows Terminal or PowerShell). Double-clicking it opens the [terminal UI](#the-terminal-ui) in a window of its own.

The exe is not code-signed, so Windows SmartScreen may warn the first time you run it. To check that the download is the one published here, compare its SHA-256 with the `.sha256` file beside it on the release page:

```powershell
Get-FileHash .\tagshelf-v0.3.0-windows-amd64.zip -Algorithm SHA256
```

### Download for Linux

1. Go to [Releases](https://github.com/zachjamesgreen/tagshelf/releases) and download `tagshelf-<version>-linux-amd64.tar.gz`, or `-linux-arm64` for a 64-bit ARM machine, with the `.sha256` file beside it.
2. Check it, unpack it and put the program on your path:

```bash
sha256sum -c tagshelf-v0.3.0-linux-amd64.tar.gz.sha256
tar -xzf tagshelf-v0.3.0-linux-amd64.tar.gz
install -D tagshelf-v0.3.0-linux-amd64/tagshelf ~/.local/bin/tagshelf
```

The program needs nothing installed, not even a C library, so the same file runs on Debian, Ubuntu, Fedora, Arch, openSUSE and Alpine.

### With Go

With [Go](https://go.dev/dl/) 1.26 or newer, on either system:

```bash
go install github.com/zachjamesgreen/tagshelf@latest
```

No C compiler is needed.

## Quick start

Say your unsorted music is in `D:\Incoming` and you want the library in `D:\Music`. The examples in this README are written for Windows. On Linux only the paths differ: `~/Incoming` and `~/Music`, say, as in `tagshelf -source ~/Incoming -dest ~/Music -dry-run`.

1. **Preview.** A dry run moves nothing and prints what it would do:

   ```powershell
   tagshelf -source D:\Incoming -dest D:\Music -dry-run
   ```

2. **Run it.** The same command without `-dry-run`:

   ```powershell
   tagshelf -source D:\Incoming -dest D:\Music
   ```

3. **Deal with the rest in the terminal UI.** Files without usable tags are now in `D:\Music\_Untagged`, and copies of songs you already have are either set aside or waiting for you to choose:

   ```powershell
   tagshelf tui -source D:\Incoming -dest D:\Music
   ```

To avoid typing the folders each time, put them in a [config file](#config-file). The quickest way is `tagshelf tui` with nothing after it: it opens on its Settings tab, where you type the two folders and press `s`.

To tidy a library that is already in place, for example after retagging albums in another program:

```powershell
tagshelf -reorganize D:\Music -dry-run
```

## How files are organized

### What goes into the library

A file **qualifies** for the library when it has both:

- an artist, an album and a title; and
- either MusicBrainz IDs in its tags (as [MusicBrainz Picard](https://picard.musicbrainz.org/) writes them), or tagshelf's **manual marker**, which tagshelf writes when you edit the tags a file is filed under, pick a MusicBrainz match, or accept its tags as they are in the terminal UI.

Tags that a CD ripper or a download came with do not qualify on their own, however complete they look. That is deliberate: those are the tags that are most often wrong, and the terminal UI lets you accept them in one key press when they are right.

### Where a qualifying file goes

| Kind | Path under the library |
|---|---|
| Album track | `Album Artist\Album (Year)\03 - Title.ext` |
| Multi-disc album | `Album Artist\Album (Year)\2-03 - Title.ext` |
| Various Artists compilation | `Various Artists\Album (Year)\03 - Track Artist - Title.ext` |

- On Linux the same paths are written with `/`. The names are the same on both systems, so a library can be copied from one to the other.
- The album artist tag is used for the folder, or the track artist when there is none.
- `(Year)` is left out when the file has no year.
- Characters Windows does not allow in names (`< > : " / \ | ? *`) are replaced with `_`.
- A name that would start with a full stop starts with `_` instead: `.38 Special` is filed as `_38 Special`. A name with a full stop first is hidden on Linux.
- Letter case never makes a second folder or file. `the beatles` goes into the `The Beatles` that is there, and a file whose name is taken in another case counts as taken.
- If the name is taken by a different file, the new one gets ` (1)`, ` (2)` and so on. tagshelf never writes over a file.

### The other folders

| Folder | What is in it |
|---|---|
| `_Untagged` | Audio that does not qualify yet. It keeps its place under the folder it came from. |
| `_Duplicates` | Copies set aside in favour of a better one. tagshelf never deletes them; that is for you to do. |
| `_Other` | Non-audio files that do not belong to an album, such as stray text files. |

Each can be put somewhere else with a flag or a config key.

### Files that travel with the music

Cover art, booklets, logs and cue sheets follow the audio of their folder into its album folder. When a cue sheet's audio is renamed, the `FILE` lines in the sheet are rewritten to match, and the original is kept beside it as `album.cue.orig`.

### What a run leaves where it is

- **Links.** A symbolic link or a junction inside a walked folder is neither followed nor moved, whether it leads to a folder or to a file. The run says so with a warning. A source or library folder that is itself a symbolic link is different: the run works in the folder it leads to, and says which that is.
- **What is not a file.** A named pipe, a socket or a device is left alone, with a warning.
- **The system's own folders at the top of a drive.** `System Volume Information` and `$RECYCLE.BIN` are passed over without a word. So are `lost+found` and the trash folders `.Trash` and `.Trash-<user number>` at the top of a mounted volume on Linux.

### Duplicates

tagshelf compares the audio itself, not the tags, so the same recording with different tags is still found.

- **Exact copies** have identical audio. When a run brings in a copy of a song the library already has, it keeps the better one and moves the other to `_Duplicates`. Set `auto_resolve_exact: false` to have these wait for you instead.
- **Copies that were already in the library** before the run are never settled by a run. They are listed on the Duplicates tab.
- **The same release track in two encodings**, such as a FLAC and an MP3 that MusicBrainz IDs say are one track of one release, always waits for you.
- **Possible copies** have matching tags but no MusicBrainz IDs to prove it. These always wait for you too, and you can confirm or dismiss them.

The better copy is the lossless one; then the one with the higher bitrate, sample rate and bit depth; then the longer one, if by more than two seconds; then the one already in the library; then the one with the more complete tags. The Duplicates tab marks its suggestion with a star, and you can choose another.

### Audio formats

MP3, FLAC, Ogg Vorbis (`.ogg`, `.oga`), Opus, MP4 (`.m4a`, `.m4b`, `.m4p`, `.m4r`), WMA, WAV, AAC (`.aac`), Monkey's Audio (`.ape`), Musepack (`.mpc`) and DSF. Any other file is treated as a non-audio file.

## Command line

```text
tagshelf [flags]          one run, then exit
tagshelf tui [flags]      the terminal UI
tagshelf report [flags]   a list of the library's files, as CSV
tagshelf -version
tagshelf -h               the help; tagshelf tui -h and tagshelf report -h for theirs
```

`tagshelf` with nothing after it prints the help and does nothing else. (Started by a double click, where the help would vanish with the window, it opens the terminal UI instead.) It takes at least one flag to start a run, even when a config file holds every setting: `tagshelf -config config.yaml`, or `tagshelf -dry-run` beside a `config.yaml`.

A command that was typed wrong (a flag tagshelf does not have, a word that is not a flag, no source or library folder) prints the help and then what was wrong. Any other reason a run cannot start is printed as two lines, what is wrong and what to do about it:

```text
tagshelf cannot start: the source folder D:\Incomming does not exist.
Check the path given with -source, or as source in the config file.
```

| Flag | Meaning |
|---|---|
| `-source DIR` | The folder to organize. |
| `-dest DIR` | The library. |
| `-reorganize DIR` | Re-sort a library in place. The same as giving `DIR` as both source and destination. |
| `-dry-run` | Preview: move, rename and write nothing. `-dry-run=false` turns off a `dry_run: true` in the config file. |
| `-index` | Only bring the index of the library up to date. Needs only `-dest`. |
| `-rescan` | Read every file again, even if its size and time have not changed. |
| `-untagged DIR` | Where audio that does not qualify goes. Default `<dest>\_Untagged`. |
| `-other DIR` | Where stray non-audio files go. Default `<dest>\_Other`. |
| `-duplicates DIR` | Where copies are set aside. Default `<dest>\_Duplicates`. |
| `-compilations NAME` | The artist folder for Various Artists compilations. Default `Various Artists`. |
| `-no-cue-rewrite` | Leave the `FILE` lines of moved cue sheets alone. |
| `-workers N` | How many files to work on at once. Default: the number of CPUs. |
| `-config FILE` | The config file to read. |
| `-db FILE` | The database file, if not the default. |
| `-verbose` | Print a line for every file. |

`tagshelf tui` takes the same flags except `-reorganize`, `-index`, `-rescan` and `-verbose`.

Anything on the command line that is not a flag stops tagshelf before it does anything, so a mistyped command cannot start a run by accident.

Ctrl-C stops a run after the files it is in the middle of. A second Ctrl-C exits at once. On Linux a run stops the same way when it is told to end (`SIGTERM`) or its terminal is closed (`SIGHUP`).

A run ends with warnings for what went less than right without failing a file: a link it left alone, a cue sheet it could not rewrite, or a file whose length and bit rate could not be read into the index.

| Exit code | Meaning |
|---|---|
| 0 | The run finished and every file went well. |
| 1 | The run finished, but some files failed. They are listed at the end. |
| 2 | The run could not start: bad flags, config or database. Nothing was touched. |
| 130 | Ctrl-C, or one of the two signals, stopped the run. |

### The report

`tagshelf report` lists every audio file the index knows, a row each, as CSV. It only reads: nothing in the library or its database changes, and it can be made while a run is going.

```powershell
tagshelf report -dest "D:\Music" -o report.csv
```

| Flag | Meaning |
|---|---|
| `-o FILE` | Write the report to this file, which must not exist yet. Without it the report is printed. |
| `-dest DIR` | The library to report on. |
| `-db FILE` | The database file, if not the default. |
| `-config FILE` | The config file to take the library from. |

The report covers the library, `_Untagged`, `_Duplicates` and `_Other`; the `folder` column says which a file is in. The columns are what the index knows about a file, not a verdict on it. To find the low bit rates or the files without MusicBrainz IDs, sort or filter the column in a spreadsheet.

| Column | What it holds |
|---|---|
| `folder` | `library`, `untagged`, `duplicates` or `other`. |
| `path` | The full path of the file. |
| `format`, `lossless` | The file type, and `yes` or `no`. |
| `bitrate_kbps`, `sample_rate_hz`, `bit_depth`, `length_seconds`, `size_bytes` | The audio's measures. One that is not known is left empty. |
| `artist`, `album_artist`, `album`, `title`, `year`, `track`, `disc`, `genre`, `composer` | The tags. Several genres or composers are joined by `; `. |
| `musicbrainz_release_id`, `musicbrainz_recording_id` | The MusicBrainz IDs in the tags. |
| `manual_marker` | The manual marker, such as `hand 2026-09-27`, for a file that is manually tagged. |
| `embedded_art` | `yes` when the file holds a picture. A cover image beside the file does not count. |
| `duplicate_group` | The number and kind of each duplicate group the file is in that still waits for your decision, such as `3 exact`. Files with the same number are copies of one song. |
| `read_error` | What could not be read from the file. |

After the report, tagshelf prints the counts: the files in each folder, those without MusicBrainz IDs, without embedded art, in duplicate groups, and not fully read.

The report is as fresh as the index, which the last run brought up to date; the summary says when that was. For a fresh report, run `tagshelf -index` first.

A file written with `-o` starts with a byte order mark, so that Excel reads its names as UTF-8. A printed report has none.

## Config file

tagshelf reads the first of these that exists:

1. the file named with `-config`;
2. `config.yaml` in the folder it is started in;
3. your own config file, so that tagshelf can be started from any folder: `%APPDATA%\tagshelf\config.yaml` on Windows, `~/.config/tagshelf/config.yaml` on Linux.

Flags override it. To make one, use the [Settings tab](#settings) of the terminal UI, or copy the example: [`config.example.yaml`](config.example.yaml) for Windows, [`config.example.linux.yaml`](config.example.linux.yaml) for Linux. Each download holds the one for its system as `config.example.yaml`. The last lines of `tagshelf -h` name the config file that would be read, and a run prints the one it used.

A `config.yaml` in the folder that is not YAML at all, such as one belonging to another program, is passed over with a warning.

A folder may be written from `~`, your home folder, as in `~\Music` or `~/Music`. That works in a config file, in the Settings tab and in a flag. Only `~` alone is known: `~name`, another user's home folder, is refused.

A config file alone does not start a run: see [Command line](#command-line).

```yaml
source: "D:\\Incoming"
destination: "D:\\Music"
dry_run: true
```

| Key | Default | Meaning |
|---|---|---|
| `source` | none | The folder to organize. |
| `destination` | none | The library. Set it to the same path as `source` to reorganize in place. |
| `untagged` | `<destination>\_Untagged` | Where audio that does not qualify goes. |
| `other` | `<destination>\_Other` | Where stray non-audio files go. |
| `duplicates` | `<destination>\_Duplicates` | Where copies are set aside. |
| `auto_resolve_exact` | `true` | Whether a run settles exact copies it brings in. |
| `compilations` | `Various Artists` | The artist folder for compilations. |
| `workers` | `0` | Files worked on at once; `0` means the number of CPUs. |
| `dry_run` | `false` | Preview only. The example file sets it to `true`. |
| `verbose` | `false` | Print a line for every file. |
| `cue_rewrite` | `true` | Fix the `FILE` lines of moved cue sheets. |
| `musicbrainz_contact` | none | An email address or URL for MusicBrainz. Lookups are off until it is set. |
| `genre_from_musicbrainz` | `fill` | What a MusicBrainz match does with the genre: `fill` writes it to a file that has none, `replace` writes it whenever MusicBrainz has one, `off` never writes it. |
| `database` | see below | The database file. |

A key tagshelf does not know stops the run and is named in the message, so a misspelt `dry_run` is never silently ignored.

## The terminal UI

```powershell
tagshelf tui
```

It opens on the library named by the flags or the config file, and can stay open while a run goes on in another window.

It also opens when set-up is not finished. With no library folder set it opens on [Settings](#settings). On a library that has no index yet, Home says so: `i` indexes it, or `o` organizes the source into it, and the library is shown once the run has ended.

A run that cannot start from Home says why and what to do, in place: no source folder set, a source folder that is not there, or another run working on the library. Where a setting is the cause, `6` goes straight to it.

```text
 tagshelf  T:\Music  library 345d9616
 1 Home   2 Untagged 2   3 Duplicates 1   4 Library   5 Runs   6 Settings
────────────────────────────────────────────────────────────────────────────────────────────────────
Library
  Library              2 files
  _Untagged            2 files
  _Other               0 files
  _Duplicates          1 file

Needs attention
  2 untagged files                                           2 Untagged
  1 duplicate group to review                                3 Duplicates


o organize  r reorganize  i index  d dry run   1-6 tabs  R reload  ? help  q quit
```

| Tab | What it is for |
|---|---|
| **1 Home** | Counts, what needs attention, and starting a run. |
| **2 Untagged** | The files in `_Untagged`: accept their tags, edit them, or look them up. |
| **3 Duplicates** | Groups of copies waiting for a decision. |
| **4 Library** | Search the library, and edit or re-match files in it. |
| **5 Runs** | Every run and action, what each moved, and undo. |
| **6 Settings** | The config file: folders and how runs behave. |

| Key | What it does |
|---|---|
| `1`-`6`, `tab` | Switch tab. |
| `?` | Show every key. |
| `q` | Quit. It asks first while a run or an action is going. |
| `esc` | Back. |
| `R` | Reload now. |
| **Home** | |
| `o` | Organize the source into the library. |
| `r` | Reorganize the library in place. |
| `i` | Bring the index up to date, moving nothing. |
| `d` | Turn dry run on or off. |
| `ctrl+c` | Interrupt the run. |
| **Untagged** | |
| `space`, `g` | Select the file, or its whole group. |
| `v` | Group by original folder, by album tag, or not at all. |
| `a` | Accept the tags of the selected files as they are. |
| `e` | Edit the file's tags. `ctrl+s` saves; `ctrl+n` adds a tag; `ctrl+f` writes the field under the cursor to every file in the folder. |
| `l` | Look the file up on MusicBrainz. |
| `L` | Match the selection or group to a MusicBrainz release, all at once. |
| **Duplicates** | |
| `k` | Choose which copy to keep. |
| `enter` | Resolve the group: keep that copy, set the others aside. |
| `b` | Keep both. |
| `c` | Confirm that a possible group is one song. |
| `A` | Resolve every exact group. |
| **Library** | |
| `/` | Search by artist, album or title. `e`, `l` and `L` work on a result. |
| **Runs** | |
| `enter` | Open the run to see what it moved. |
| `u` | Undo the run. Right after any action, `u` undoes that action. |
| **Settings** | |
| `enter` | Change the setting: type a value, or turn it on or off. |
| `tab` | While typing a path, complete the folder name. |
| `s` | Save to the config file. |
| `esc` | Drop the changes that are not saved. |

Once a file's tags qualify, saving them also moves it into the library. Every save shows where the file will go before you confirm.

### The tag editor

`e` on a file opens every tag it has that is text. First come the title, artist, album artist, album, year, track, disc, genre, composer, comment, and whether it is a compilation. Under them are the file's other tags, each by its name, then its MusicBrainz IDs and AcoustID ID, and last the manual marker.

- **Several genres or composers** are written with a semicolon between them, as in `Rock; Jazz`, and saved as separate values. So is any other tag that holds several values in the file.
- **Other tags** go by the same name in every kind of file, such as `MOOD`, `LABEL` or `ISRC`; tagshelf stores each the way the file's format wants. `ctrl+n` adds one: type its name, then its value. A name may hold letters, digits, spaces and `_`, and is written in capitals. A tag left empty is taken off the file. If the format has no place for a tag of that name, the save is refused and the file is left as it was.
- **Text of several lines**, such as a long comment or lyrics, is edited in a box where `enter` starts a new line and `tab` moves on.
- **The IDs** must have the form of an ID. They come from a lookup, so there is seldom a reason to type one.
- **The marker** can be taken off, or set to `hand` or `musicbrainz` with a day, as in `hand 2026-10-12`. What you type there is what the file gets, whatever else the save changes. It cannot be taken off a WMA file.
- **What is not text**, such as a picture, is named under the form and kept by every save.
- **What marks a file.** Changing a tag the file is filed under (title, artist, album artist, album, year, track, disc, compilation) or a MusicBrainz ID marks it as tagged by hand, and so does saving the form without a change, which vouches for the tags as they are. Changing only the genre, the composer, the comment or the other tags does not: it says nothing of whether the rest is right, so a ripper's file stays in `_Untagged`. The editor says which it will be before you save.
- **The whole folder at once.** `ctrl+f` on the album artist, album, year, genre, composer, comment or one of the other tags asks, with the number of files, and then writes that field to every file in the same folder. An emptied field takes the tag off them all. It is one action, which `u` undoes whole. The editor closes afterwards, so save or take back any other change first.
- **What the file has stays as it is until you change it.** A value is shown with a space in place of anything that is not a printable character. A field you leave alone is not written, whatever the file holds there.

### Settings

The Settings tab edits the config file tagshelf read: the one named with `-config`, the `config.yaml` in the folder, or else [your own](#where-tagshelf-keeps-its-data). The top line names it. If it does not exist yet, the first save makes it, from the same commented template as the example file, with dry run on so that the first run of a new set-up is a preview.

- **Change several settings, then press `s`.** Changed rows are marked with `*`. Nothing is written before `s`, and leaving the tab with changes asks first.
- **Paths can be typed or pasted.** `tab` completes a folder name, and a note under the field says whether the folder exists. A path may start with `~`, your home folder, and is saved as you typed it.
- **A save is checked first.** A source folder that does not exist, a library folder inside the source, two folders that are the same, or a contact that is not an email address or URL is refused, with the reason beside the setting. Nothing is written until all of them pass.
- **Some saves ask first.** A library folder that does not exist is made if you say yes. Changing the library folder of a library that is open says plainly that it opens a different library and moves no music.
- **A save takes effect at once.** The library is opened again with the new settings. Saving is refused while a run or an action is going.
- **Only the lines you changed are rewritten.** Comments, the order of the keys and everything else in the file stay as they were. If the file was changed in an editor since the tab read it, the save is refused so that neither set of changes is lost.
- **A flag still wins for the session.** Started as `tagshelf tui -dest E:\Other`, the row shows both the file's value and the session's. What you save is the file's.
- **Changing where files go does not move them.** After changing the compilations name or one of the three folders, press `r` on Home to reorganize.

`d` on Home still turns dry run on or off for the session alone, without touching the file.

## MusicBrainz lookups

Lookups use the public [MusicBrainz](https://musicbrainz.org/) web service, which asks every program to say who is running it. Set a contact before the first lookup:

```yaml
musicbrainz_contact: "you@example.com"
```

The contact is sent to MusicBrainz in the `User-Agent` header of each request and nowhere else. tagshelf sends at most one request a second, as MusicBrainz asks, and sends only what you see in the search form: titles, artist and album names, track counts and lengths. No audio and no file paths leave your computer.

- **Look up one file (`l`)**: searches by the file's tags, or by its file name when it has none. You pick a recording and a release, see the tags before and after, and confirm.
- **Match a release (`L`)**: tags a whole album at once. tagshelf puts each file on a track by title, number and length; you can move files between tracks before confirming.

A match also brings the **genre**, where MusicBrainz has one. The votes for each genre are added up over the recording, the release and its release group; of the five with the most votes, those with at least nine tenths of the leader's are written, each word with a capital, as [Picard](https://picard.musicbrainz.org/) picks them. By default a genre is only written to a file that has none. `genre_from_musicbrainz: replace` writes it over the file's own, and `off` leaves genres alone. A match that has no genre never takes one away. The composer and the comment are not looked up; they are yours to type.

## Undo

Every run and every action in the terminal UI is recorded. Undo puts back what one of them did: files go back where they were, tags are restored to what they held, and rewritten cue sheets get their old text back.

Undo is careful rather than thorough. It skips a file when:

- the file has changed since tagshelf last read it;
- the file is no longer where the run left it;
- something else now sits at the file's old path.

Skipped files are listed before you confirm. When a skip can be cured, for example by bringing the index up to date, the run stays available to undo again. Undo is itself recorded, so it can be undone.

What undo does not do: bring back a file you deleted, or restore tags another program has since changed.

## Where tagshelf keeps its data

| What | On Windows | On Linux |
|---|---|---|
| Your config file | `%APPDATA%\tagshelf\config.yaml` | `~/.config/tagshelf/config.yaml` |
| Database (index and run history) | `%LOCALAPPDATA%\tagshelf\libraries\<library-id>\tagshelf.db` | `~/.local/share/tagshelf/libraries/<library-id>/tagshelf.db` |
| The tag reader's cache | In your temp folder | `~/.cache/tagshelf` |

Your config file is read when no other is named or found, and the Settings tab makes it on the first save. The database is there unless `-db` or `database` says otherwise. On Linux `XDG_CONFIG_HOME`, `XDG_DATA_HOME` and `XDG_CACHE_HOME` move the three, as for any program, and the folders tagshelf makes there are yours alone (mode 0700).

The rest is the same on both:

| What | Where |
|---|---|
| Database backups | Beside the database, as `tagshelf.db.bak-v<N>`, made before each upgrade |
| Library ID | `.tagshelf-library` in the library folder |
| Lock | `.tagshelf-lock` in the library folder, and `tagshelf.db.lock` beside the database |
| Cue sheet originals | `<name>.cue.orig` beside each rewritten cue sheet |
| Temporary copies | `.tagshelf-…` files beside a file being saved or copied. One left behind by a crash is removed by a later run once it is an hour old. |

Only one tagshelf changes a library at a time. A second run on the same library is refused and told which process holds it; the terminal UI can stay open and watch.

The index is only a record of the files. If the database is lost, `tagshelf -index -dest D:\Music` rebuilds it; the run history, and so the ability to undo earlier runs, is what would be gone.

To stop using tagshelf, delete the database folder, your config file and the two `.tagshelf-` files in the library. Your music stays as it is. The only thing tagshelf leaves in the files is the manual marker, a tag named `TAGSHELF_MANUAL`.

## On Linux

tagshelf does the same on Linux as on Windows. What differs is listed here.

- **Paths.** A folder is written as Linux writes it, and may start with `~`. A Windows path such as `D:\Music` is refused, in a flag and in a config file: on Linux it would name a folder called `D:\Music` inside the folder tagshelf was started in.
- **Letter case.** Linux can hold `The Beatles` and `the beatles` side by side. tagshelf never makes the second: it uses the one that is there. If both are there already, it uses the one that sorts first, says so, and a reorganize moves what is in the other into it.
- **A folder that is a link.** If your source or library folder is a symbolic link, as `~/Music` often is, tagshelf works in the folder it leads to.
- **No double click.** `tagshelf` with nothing after it prints the help. Start the terminal UI with `tagshelf tui`.
- **The terminal.** The terminal UI needs a terminal that shows UTF-8, which every desktop terminal and tmux do. On the bare text console of a machine without a desktop some of its marks, such as `★`, may not show; that has not been tried.
- **Pasting.** Pasting through the terminal works everywhere. The paste key of the text fields themselves needs `xclip`, `xsel` or `wl-clipboard` installed.

Things to know before you rely on it:

- **One library, two systems.** The same library folder can be used from Windows and from Linux, on a machine that starts both or over a share. Each system keeps its own index and run history for it, so a run can be undone only on the system that made it.
- **One computer at a time.** Two computers working on one library at the same moment is not supported. The lock that stops two runs on one computer is not known to hold over NFS or SMB.
- **A home folder on a network drive.** The database must be on a local disk. If your home folder is on NFS, put it elsewhere with `-db` or `database`.
- **Names over 255 bytes.** Linux limits a name to 255 bytes, which a long title in a script other than Latin can pass. Such a file is left where it is, with an error. tagshelf does not shorten names.
- **What has been run.** Every change is tested on Ubuntu, on amd64 and arm64. Each release is also run on Debian, Fedora, Arch, openSUSE and Alpine before it is published. NixOS should work, and has not been tried.

## Building and testing

You need [Go](https://go.dev/dl/) 1.26 or newer, on Windows or Linux. Nothing else: the test audio files are in the repository.

```bash
git clone https://github.com/zachjamesgreen/tagshelf
cd tagshelf
go build .
go test ./...
```

The checks CI runs on every pull request, on Windows and on Linux (amd64 and arm64):

```bash
gofmt -l .                                                    # must print nothing
go vet ./...
go run honnef.co/go/tools/cmd/staticcheck@2026.2.1 ./...
go run ./internal/notices                                     # must leave THIRD_PARTY_NOTICES unchanged
go test ./... -count=1
```

Useful to know:

- The tests never read your `config.yaml`, your music or your application data, and never use the network. The MusicBrainz tests talk to a server inside the test.
- The terminal UI's screens are compared with files in `internal/tui/testdata`. After changing a screen on purpose, run `go test ./internal/tui -update` on Windows and read the diff: the files hold Windows screens, and on Linux the paths in them are read as Linux writes them.
- The test audio in `testdata/audio` is described in its own [README](testdata/audio/README.md). It is regenerated with `go generate ./internal/fixtures`, which needs ffmpeg 7.1; you only need that to add a fixture.

### How the code is laid out

| Package | What it does |
|---|---|
| `main` (`main.go`, `cli.go`, `tui_cmd.go`) | Flags, the command line's output, starting the terminal UI. |
| `internal/config` | The config file and its defaults. |
| `internal/engine` | Runs: walking folders, moving files, the index, duplicates, tag saves, undo. |
| `internal/rules` | Where a file belongs, and which copy of a duplicate is better. |
| `internal/store` | The SQLite database, run history and the library lock. |
| `internal/tag` | Reading tags and hashing audio. A changed copy of [dhowden/tag](https://github.com/dhowden/tag); see its [UPSTREAM.md](internal/tag/UPSTREAM.md). |
| `internal/tagwrite` | Writing tags, through [go-taglib](https://github.com/sentriz/go-taglib). |
| `internal/marker` | The manual marker. |
| `internal/musicbrainz` | The MusicBrainz client, ranking of results, and matching files to tracks. |
| `internal/tui` | The terminal UI. |
| `internal/fixtures` | The test audio files. |

The words the project uses for things (library, run, qualifies, manual marker and so on) are defined in [CONTEXT.md](CONTEXT.md).

## Contributing

Bug reports and small fixes are welcome. For anything larger, open an issue first so we can agree on it before you write it. This is a one-person project in spare time, so replies may be slow. See [CONTRIBUTING.md](CONTRIBUTING.md).

## License and credits

tagshelf is released under the [MIT License](LICENSE). The software it is built from is listed, with its licences, in [THIRD_PARTY_NOTICES](THIRD_PARTY_NOTICES).

It stands on:

- [dhowden/tag](https://github.com/dhowden/tag) by David Howden, the tag reader that `internal/tag` grew from (BSD 2-Clause);
- [TagLib](https://taglib.org/), through [go-taglib](https://github.com/sentriz/go-taglib) by Senan Kelly, for writing tags (LGPL 2.1);
- [MusicBrainz](https://musicbrainz.org/), whose volunteers maintain the data the lookups use;
- [Bubble Tea](https://github.com/charmbracelet/bubbletea), [Lip Gloss](https://github.com/charmbracelet/lipgloss) and [Huh](https://github.com/charmbracelet/huh) by Charm, for the terminal UI;
- [modernc.org/sqlite](https://modernc.org/sqlite), SQLite without a C compiler.
