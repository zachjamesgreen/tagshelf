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
- [Building and testing](#building-and-testing)
- [Contributing](#contributing)
- [License and credits](#license-and-credits)

## Status

- **Windows only, for now.** tagshelf is written on Windows 11 and released for Windows. Its tests also pass on Linux, but there is no Linux download yet.
- **Version 0.x.** Flags, config keys and the database may still change between versions. The database is upgraded on first use by a newer version, and a copy of the old one is kept beside it.
- **Back up first.** tagshelf moves and renames your files and can write tags into them. It never deletes an audio file, and it records what it does, but a tool this young should not be the only thing between you and your only copy. Start with a dry run.

## Install

### Download

1. Go to [Releases](https://github.com/zachjamesgreen/tagshelf/releases) and download `tagshelf-<version>-windows-amd64.zip`.
2. Unzip it anywhere and run `tagshelf.exe` from a terminal (Windows Terminal or PowerShell). Double-clicking it opens the [terminal UI](#the-terminal-ui) in a window of its own.

The exe is not code-signed, so Windows SmartScreen may warn the first time you run it. To check that the download is the one published here, compare its SHA-256 with the `.sha256` file beside it on the release page:

```powershell
Get-FileHash .\tagshelf-v0.1.0-windows-amd64.zip -Algorithm SHA256
```

### With Go

With [Go](https://go.dev/dl/) 1.26 or newer:

```powershell
go install github.com/zachjamesgreen/tagshelf@latest
```

No C compiler is needed.

## Quick start

Say your unsorted music is in `D:\Incoming` and you want the library in `D:\Music`.

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
- either MusicBrainz IDs in its tags (as [MusicBrainz Picard](https://picard.musicbrainz.org/) writes them), or tagshelf's **manual marker**, which tagshelf writes when you edit a file's tags, pick a MusicBrainz match, or accept its tags as they are in the terminal UI.

Tags that a CD ripper or a download came with do not qualify on their own, however complete they look. That is deliberate: those are the tags that are most often wrong, and the terminal UI lets you accept them in one key press when they are right.

### Where a qualifying file goes

| Kind | Path under the library |
|---|---|
| Album track | `Album Artist\Album (Year)\03 - Title.ext` |
| Multi-disc album | `Album Artist\Album (Year)\2-03 - Title.ext` |
| Various Artists compilation | `Various Artists\Album (Year)\03 - Track Artist - Title.ext` |

- The album artist tag is used for the folder, or the track artist when there is none.
- `(Year)` is left out when the file has no year.
- Characters Windows does not allow in names (`< > : " / \ | ? *`) are replaced with `_`.
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
- **The system's own folders at the top of a drive.** `System Volume Information` and `$RECYCLE.BIN` are passed over without a word.

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
tagshelf -version
tagshelf -h               the help; tagshelf tui -h for the terminal UI's
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

Ctrl-C stops a run after the files it is in the middle of. A second Ctrl-C exits at once.

A run ends with warnings for what went less than right without failing a file: a link it left alone, a cue sheet it could not rewrite, or a file whose length and bit rate could not be read into the index.

| Exit code | Meaning |
|---|---|
| 0 | The run finished and every file went well. |
| 1 | The run finished, but some files failed. They are listed at the end. |
| 2 | The run could not start: bad flags, config or database. Nothing was touched. |
| 130 | Ctrl-C stopped the run. |

## Config file

tagshelf reads the first of these that exists:

1. the file named with `-config`;
2. `config.yaml` in the folder it is started in;
3. your own config file, `%APPDATA%\tagshelf\config.yaml`, so that tagshelf can be started from any folder.

Flags override it. To make one, use the [Settings tab](#settings) of the terminal UI, or copy [`config.example.yaml`](config.example.yaml). The last lines of `tagshelf -h` name the config file that would be read, and a run prints the one it used.

A `config.yaml` in the folder that is not YAML at all, such as one belonging to another program, is passed over with a warning.

A folder may be written from `~`, your home folder, as in `~\Music`. That works in a config file, in the Settings tab and in a flag. Only `~` alone is known: `~name`, another user's home folder, is refused.

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
| `e` | Edit the file's tags. `ctrl+s` saves. |
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

### Settings

The Settings tab edits the config file tagshelf read: the one named with `-config`, the `config.yaml` in the folder, or else your own, `%APPDATA%\tagshelf\config.yaml`. The top line names it. If it does not exist yet, the first save makes it, from the same commented template as `config.example.yaml`, with dry run on so that the first run of a new set-up is a preview.

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

## Undo

Every run and every action in the terminal UI is recorded. Undo puts back what one of them did: files go back where they were, tags are restored to what they held, and rewritten cue sheets get their old text back.

Undo is careful rather than thorough. It skips a file when:

- the file has changed since tagshelf last read it;
- the file is no longer where the run left it;
- something else now sits at the file's old path.

Skipped files are listed before you confirm. When a skip can be cured, for example by bringing the index up to date, the run stays available to undo again. Undo is itself recorded, so it can be undone.

What undo does not do: bring back a file you deleted, or restore tags another program has since changed.

## Where tagshelf keeps its data

| What | Where |
|---|---|
| Your config file | `%APPDATA%\tagshelf\config.yaml`, read when no other is named or found. The Settings tab makes it on the first save. |
| Database (index and run history) | `%LOCALAPPDATA%\tagshelf\libraries\<library-id>\tagshelf.db`, unless `-db` or `database` says otherwise |
| Database backups | Beside the database, as `tagshelf.db.bak-v<N>`, made before each upgrade |
| Library ID | `.tagshelf-library` in the library folder |
| Lock | `.tagshelf-lock` in the library folder, and `tagshelf.db.lock` beside the database |
| Cue sheet originals | `<name>.cue.orig` beside each rewritten cue sheet |
| Temporary copies | `.tagshelf-…` files beside a file being saved or copied. One left behind by a crash is removed by a later run once it is an hour old. |

Only one tagshelf changes a library at a time. A second run on the same library is refused and told which process holds it; the terminal UI can stay open and watch.

The index is only a record of the files. If the database is lost, `tagshelf -index -dest D:\Music` rebuilds it; the run history, and so the ability to undo earlier runs, is what would be gone.

To stop using tagshelf, delete the database folder, your config file and the two `.tagshelf-` files in the library. Your music stays as it is. The only thing tagshelf leaves in the files is the manual marker, a tag named `TAGSHELF_MANUAL`.

## Building and testing

You need [Go](https://go.dev/dl/) 1.26 or newer, on Windows or Linux. Nothing else: the test audio files are in the repository.

```powershell
git clone https://github.com/zachjamesgreen/tagshelf
cd tagshelf
go build -o tagshelf.exe .
go test ./...
```

The checks CI runs on every pull request, on Windows and on Linux (amd64 and arm64):

```powershell
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
