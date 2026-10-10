-- File IDs are never used again. Without AUTOINCREMENT a new row takes the
-- highest ID in the table plus one, so the ID of the newest file was handed
-- out again once that file's row was deleted, and a duplicate review, which
-- names its files by ID alone, came to cover a different file.
--
-- SQLite cannot add AUTOINCREMENT to a column, so the table is rebuilt with
-- every row and ID kept. Nothing refers to it by foreign key (dup_reviews
-- deliberately does not), and its two indexes are made again below.
CREATE TABLE files_rebuilt (
    id                  INTEGER PRIMARY KEY AUTOINCREMENT,
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

INSERT INTO files_rebuilt (id, root_id, rel_path, path_key, format,
    artist, album_artist, album, title, track, track_total, disc, disc_total, year,
    mb_release_id, mb_release_track_id, mb_recording_id, manual,
    lossless, bitrate, sample_rate, bit_depth, channels, length_ms,
    audio_sum, size, mtime, indexed_at, tags_json, read_error)
SELECT id, root_id, rel_path, path_key, format,
    artist, album_artist, album, title, track, track_total, disc, disc_total, year,
    mb_release_id, mb_release_track_id, mb_recording_id, manual,
    lossless, bitrate, sample_rate, bit_depth, channels, length_ms,
    audio_sum, size, mtime, indexed_at, tags_json, read_error
FROM files ORDER BY id;

DROP TABLE files;
ALTER TABLE files_rebuilt RENAME TO files;

CREATE INDEX files_audio_sum ON files (audio_sum) WHERE audio_sum IS NOT NULL;
CREATE INDEX files_release_track ON files (mb_release_track_id) WHERE mb_release_track_id <> '';

-- The next ID must also be above every ID a review names: a reviewed file
-- whose row is gone already may have had the highest ID there was.
INSERT INTO sqlite_sequence (name, seq)
SELECT 'files', 0 WHERE NOT EXISTS (SELECT 1 FROM sqlite_sequence WHERE name = 'files');

UPDATE sqlite_sequence
SET seq = max(seq,
    (SELECT coalesce(max(m.value), 0) FROM dup_reviews r, json_each(r.members) m),
    (SELECT coalesce(max(keeper), 0) FROM dup_reviews))
WHERE name = 'files';
