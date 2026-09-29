-- A running run's latest progress, rewritten every few seconds so another
-- process (the TUI) can follow it: the phase it is in and how many files of
-- the phase's total it has done (total 0 when unknown), as of progress_at.
ALTER TABLE runs ADD COLUMN phase TEXT NOT NULL DEFAULT '';
ALTER TABLE runs ADD COLUMN progress_done INTEGER NOT NULL DEFAULT 0;
ALTER TABLE runs ADD COLUMN progress_total INTEGER NOT NULL DEFAULT 0;
ALTER TABLE runs ADD COLUMN progress_at TEXT;
