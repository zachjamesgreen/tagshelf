-- The index: tagshelf's record of every audio file under the library's roots,
-- re-read whenever a file's size or modification time changes.

-- One row per kind of root, holding where it is now. A root that moves (a new
-- drive letter, say) rewrites only its path; its files keep their rows.
CREATE TABLE roots (
    id   INTEGER PRIMARY KEY,
    kind TEXT    NOT NULL UNIQUE CHECK (kind IN ('library', 'untagged', 'other', 'duplicates', 'source')),
    path TEXT    NOT NULL
);

-- One row per audio file. path_key is rel_path in the case-folded form paths
-- are compared in, so a file differing only in case is one row.
-- audio_sum is tag.Sum, NULL when the file could not be hashed; mtime is Unix
-- nanoseconds; length_ms and the other properties come from go-taglib and are
-- 0 when unknown; manual is the manual marker as written, '' when there is
-- none; read_error says what could not be read, '' when everything was.
CREATE TABLE files (
    id                  INTEGER PRIMARY KEY,
    root_id             INTEGER NOT NULL REFERENCES roots (id),
    rel_path            TEXT    NOT NULL,
    path_key            TEXT    NOT NULL,
    format              TEXT    NOT NULL DEFAULT '',
    artist              TEXT    NOT NULL DEFAULT '',
    album_artist        TEXT    NOT NULL DEFAULT '',
    album               TEXT    NOT NULL DEFAULT '',
    title               TEXT    NOT NULL DEFAULT '',
    track               INTEGER NOT NULL DEFAULT 0,
    track_total         INTEGER NOT NULL DEFAULT 0,
    disc                INTEGER NOT NULL DEFAULT 0,
    disc_total          INTEGER NOT NULL DEFAULT 0,
    year                INTEGER NOT NULL DEFAULT 0,
    mb_release_id       TEXT    NOT NULL DEFAULT '',
    mb_release_track_id TEXT    NOT NULL DEFAULT '',
    mb_recording_id     TEXT    NOT NULL DEFAULT '',
    manual              TEXT    NOT NULL DEFAULT '',
    lossless            INTEGER NOT NULL DEFAULT 0 CHECK (lossless IN (0, 1)),
    bitrate             INTEGER NOT NULL DEFAULT 0,
    sample_rate         INTEGER NOT NULL DEFAULT 0,
    bit_depth           INTEGER NOT NULL DEFAULT 0,
    channels            INTEGER NOT NULL DEFAULT 0,
    length_ms           INTEGER NOT NULL DEFAULT 0,
    audio_sum           TEXT,
    size                INTEGER NOT NULL,
    mtime               INTEGER NOT NULL,
    indexed_at          TEXT    NOT NULL,
    tags_json           TEXT    NOT NULL DEFAULT '{}',
    read_error          TEXT    NOT NULL DEFAULT '',
    UNIQUE (root_id, path_key)
);

-- Exact duplicates share an audio hash; release-track duplicates share a
-- release-track ID.
CREATE INDEX files_audio_sum ON files (audio_sum) WHERE audio_sum IS NOT NULL;
CREATE INDEX files_release_track ON files (mb_release_track_id) WHERE mb_release_track_id <> '';

-- How many files a run read into the index.
ALTER TABLE runs ADD COLUMN indexed INTEGER NOT NULL DEFAULT 0;
