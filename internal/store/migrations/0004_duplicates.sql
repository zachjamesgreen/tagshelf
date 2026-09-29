-- Duplicates: identical incoming files are now moved to the duplicates folder
-- rather than skipped, so a run counts duplicates moved.
ALTER TABLE runs RENAME COLUMN skipped TO duplicates;

-- One row per decision on a duplicate group. Groups themselves are derived by
-- query; members is the group's file IDs at review time, a sorted JSON array.
-- A group needs review again once its current members are not a subset of a
-- reviewed set. keeper is the file kept, NULL for keep-both; decided_by says
-- whether a run (auto) or a person (user) decided. No foreign key on keeper or
-- members: file rows come and go with the files. run_id is the run that
-- carried the decision out, so undoing it can withdraw the decision.
CREATE TABLE dup_reviews (
    id          INTEGER PRIMARY KEY,
    kind        TEXT    NOT NULL CHECK (kind IN ('exact', 'release-track', 'possible')),
    members     TEXT    NOT NULL CHECK (json_valid(members)),
    keeper      INTEGER,
    outcome     TEXT    NOT NULL CHECK (outcome IN ('resolved', 'keep-both')),
    decided_by  TEXT    NOT NULL CHECK (decided_by IN ('auto', 'user')),
    run_id      INTEGER REFERENCES runs (id),
    reviewed_at TEXT    NOT NULL
);

CREATE INDEX dup_reviews_run ON dup_reviews (run_id);
