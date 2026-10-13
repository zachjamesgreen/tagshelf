-- How many audio files a run put a picture into.
ALTER TABLE runs ADD COLUMN pictures INTEGER NOT NULL DEFAULT 0;
