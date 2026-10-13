-- The playlists of the library, and the files each names, so that a file
-- that moves has the playlists naming it found without reading them all.
-- path is whole; path_key and target_key are paths in the case-folded form
-- paths are compared in. The walk of a run or an index run keeps the table.
CREATE TABLE playlists (
    id       INTEGER PRIMARY KEY,
    path     TEXT    NOT NULL,
    path_key TEXT    NOT NULL UNIQUE,
    size     INTEGER NOT NULL,
    mtime    INTEGER NOT NULL
);

CREATE TABLE playlist_entries (
    playlist_id INTEGER NOT NULL REFERENCES playlists (id) ON DELETE CASCADE,
    target_key  TEXT    NOT NULL
);

CREATE INDEX playlist_entries_target ON playlist_entries (target_key);
CREATE INDEX playlist_entries_playlist ON playlist_entries (playlist_id);

-- How many playlists a run rewrote.
ALTER TABLE runs ADD COLUMN playlists INTEGER NOT NULL DEFAULT 0;
