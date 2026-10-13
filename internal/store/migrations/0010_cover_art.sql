-- A run can now write a cover file into an album's folder, put a picture into
-- an audio file, and rewrite a playlist. Each is recorded among the run's
-- moves, as a cue sheet's rewrite is, so that undo takes it back in its turn:
--
--   cover             to_path is the cover file written and from_path the
--                     image file it took the place of, which is to_path
--                     itself when there was none or it had that name.
--                     cue_after is the SHA-256 of the picture written, and
--                     cue_before the picture replaced, whole, NULL for none.
--   picture           the same for a picture put into the audio file at
--                     to_path.
--   playlist-rewrite  as cue-rewrite, for a playlist: its text before and
--                     after.
--
-- SQLite cannot add a value to a CHECK, so the table is rebuilt with every
-- row and ID kept. Nothing refers to it by foreign key.
CREATE TABLE moves_rebuilt (
    id         INTEGER PRIMARY KEY,
    run_id     INTEGER NOT NULL REFERENCES runs (id),
    from_path  TEXT    NOT NULL,
    to_path    TEXT    NOT NULL,
    reason     TEXT    NOT NULL CHECK (reason IN ('organized', 'untagged', 'other', 'duplicate', 'companion', 'renamed',
        'cue-rewrite', 'cover', 'picture', 'playlist-rewrite')),
    moved_at   TEXT    NOT NULL,
    seq        INTEGER,
    cue_before BLOB,
    cue_after  BLOB
);

INSERT INTO moves_rebuilt (id, run_id, from_path, to_path, reason, moved_at, seq, cue_before, cue_after)
SELECT id, run_id, from_path, to_path, reason, moved_at, seq, cue_before, cue_after FROM moves ORDER BY id;

DROP TABLE moves;
ALTER TABLE moves_rebuilt RENAME TO moves;

CREATE INDEX moves_run ON moves (run_id);

-- How many covers a run wrote.
ALTER TABLE runs ADD COLUMN covers INTEGER NOT NULL DEFAULT 0;
