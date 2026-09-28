-- Run history: one row per run, the moves it made and the files it failed on.
-- Times are UTC in RFC 3339 with nanoseconds.

CREATE TABLE runs (
    id            INTEGER PRIMARY KEY,
    started_at    TEXT    NOT NULL,
    ended_at      TEXT,
    kind          TEXT    NOT NULL CHECK (kind IN ('organize', 'reorganize', 'index', 'tui-action', 'undo')),
    dry_run       INTEGER NOT NULL CHECK (dry_run IN (0, 1)),
    source        TEXT    NOT NULL,
    destination   TEXT    NOT NULL,
    total_found   INTEGER NOT NULL DEFAULT 0,
    organized     INTEGER NOT NULL DEFAULT 0,
    untagged      INTEGER NOT NULL DEFAULT 0,
    skipped       INTEGER NOT NULL DEFAULT 0,
    in_place      INTEGER NOT NULL DEFAULT 0,
    errors        INTEGER NOT NULL DEFAULT 0,
    other_found   INTEGER NOT NULL DEFAULT 0,
    other_moved   INTEGER NOT NULL DEFAULT 0,
    left_in_place INTEGER NOT NULL DEFAULT 0,
    retried       INTEGER NOT NULL DEFAULT 0,
    cue_rewritten INTEGER NOT NULL DEFAULT 0,
    state         TEXT    NOT NULL CHECK (state IN ('running', 'ok', 'errors', 'interrupted', 'crashed', 'undone')),
    undone_by     INTEGER REFERENCES runs (id)
);

CREATE TABLE moves (
    id        INTEGER PRIMARY KEY,
    run_id    INTEGER NOT NULL REFERENCES runs (id),
    from_path TEXT    NOT NULL,
    to_path   TEXT    NOT NULL,
    reason    TEXT    NOT NULL CHECK (reason IN ('organized', 'untagged', 'other', 'duplicate', 'companion', 'renamed', 'cue-rewrite')),
    moved_at  TEXT    NOT NULL
);

CREATE INDEX moves_run ON moves (run_id);

CREATE TABLE run_errors (
    id      INTEGER PRIMARY KEY,
    run_id  INTEGER NOT NULL REFERENCES runs (id),
    path    TEXT    NOT NULL,
    message TEXT    NOT NULL
);

CREATE INDEX run_errors_run ON run_errors (run_id);
