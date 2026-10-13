# tagshelf

tagshelf sorts audio files into a music library by their tags, and keeps an index of that library for its TUI.

## Language

**Library**:
The organized destination folder plus its `_Untagged`, `_Other` and `_Duplicates` folders, identified by the ID in `.tagshelf-library`.
_Avoid_: collection, destination (when meaning the whole set)

**Index**:
tagshelf's cached record of every audio file in a library (tags, audio hash, format properties), rebuilt from the files whenever they change.
_Avoid_: catalog, scan cache

**Report**:
The CSV list `tagshelf report` makes of every indexed audio file, a row each, with what the index knows of it. It reads the index and changes nothing.
_Avoid_: export, scan, health check

**Run**:
One organize or reorganize pass over a library, started from the CLI or the TUI, dry or real.
_Avoid_: job, session, pass

**Manually tagged**:
A file whose tags a person chose in tagshelf, either typed by hand or picked from a MusicBrainz lookup, as opposed to tags written by a ripper or Picard.
_Avoid_: hand-tagged (when a MusicBrainz pick is meant too), user-tagged

**Placing tag**:
A tag that decides where a file belongs: one its path in the library is made from (title, artist, album artist, album, year, track, disc, compilation) or a MusicBrainz ID. A save that changes one makes the file manually tagged; a save of its genre, composer, comment or other tags alone does not.
_Avoid_: core tag, main tag

**Cover**:
The front picture of an album, which is one folder of the library: an image file in it named cover, folder or front. tagshelf writes one named cover into a folder that has none.
_Avoid_: artwork, album art, thumbnail

**Cover art run**:
An action that gives album folders the cover they lack, from the pictures in their files or else from the Cover Art Archive: after a match, by `c` on an album, or by `tagshelf art`.
_Avoid_: art fetch, art sync

**Files tab**:
The TUI tab that shows any folder of the computer and tags the audio in it. A file outside the library is tagged where it is and never moved; one inside the library's folders is the index's own and is filed as on the other tabs.
_Avoid_: browser, file manager, explorer

**Other tag**:
A tag of a file that the tag editor has no fixed field for, such as MOOD or LABEL, listed and edited by the name the tag library gives it, which is the same in every format.
_Avoid_: custom tag, arbitrary tag, extra tag, raw tag

**Folder write**:
Writing one field of the tag editor to every file in the folder of the file being edited, as one action.
_Avoid_: batch edit, apply to album, copy to all

**Manual marker**:
The tag in a file that records it as manually tagged, with how (hand or MusicBrainz) and when.
_Avoid_: manual flag, tagshelf tag

**Qualifies**:
A file qualifies for the library when it has MusicBrainz IDs or a manual marker, and an artist, album and title; everything else belongs in `_Untagged`.
_Avoid_: eligible, organizable

**Accept tags**:
Vouching for a file's existing tags as they are, which makes it manually tagged without changing any field.
_Avoid_: approve, confirm tags

**Release match**:
Tagging several files at once from one MusicBrainz release, each file assigned to one of its tracks.
_Avoid_: album lookup, batch tag

**Undo**:
Reversing a whole run or TUI action (its moves, cue rewrites and tag saves), skipping any file that has changed since; itself recorded as a run.
_Avoid_: rollback, revert

**Config file**:
The YAML file tagshelf takes its settings from: the one named with `-config`, the `config.yaml` in the working directory, or the user's own under `%APPDATA%`.
_Avoid_: configuration (for the file), preferences, options file

**Settings**:
The values in the config file, and the TUI tab that edits them. A flag can stand in for a setting for one session without changing the file.
_Avoid_: config (for the tab), preferences, options

**Library lock**:
The exclusive hold one process takes on a library before changing any of its files; only one process holds it at a time.
_Avoid_: run lock, mutex
