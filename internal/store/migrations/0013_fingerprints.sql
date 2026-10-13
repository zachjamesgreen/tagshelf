-- What listening to a file gave, by the hash of its audio (tag.Sum): its
-- AcoustID fingerprint and its length in whole seconds, which are kept for
-- good, and what AcoustID answered for it, which is asked again after a
-- while. answer is a JSON array of the tracks AcoustID matched, NULL until it
-- has been asked. A file retagged or moved keeps its hash, and so its row.
CREATE TABLE fingerprints (
    audio_sum   TEXT    PRIMARY KEY,
    duration    INTEGER NOT NULL,
    fingerprint TEXT    NOT NULL,
    made_at     TEXT    NOT NULL,
    answer      TEXT    CHECK (answer IS NULL OR json_valid(answer)),
    answered_at TEXT
);
