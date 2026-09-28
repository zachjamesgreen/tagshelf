-- Tag history: one row per tag save, with the values of the fields it wrote
-- (the manual marker included) before and after, as JSON objects mapping each
-- field to its list of values. Times are UTC in RFC 3339 with nanoseconds.

CREATE TABLE tag_writes (
    id         INTEGER PRIMARY KEY,
    run_id     INTEGER NOT NULL REFERENCES runs (id),
    path       TEXT    NOT NULL,
    before     TEXT    NOT NULL CHECK (json_valid(before)),
    after      TEXT    NOT NULL CHECK (json_valid(after)),
    written_at TEXT    NOT NULL
);

CREATE INDEX tag_writes_run ON tag_writes (run_id);
