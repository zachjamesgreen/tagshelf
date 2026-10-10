-- Undo used to order a run's moves and tag saves by their recorded times,
-- which a coarse clock gives to several steps at once. seq numbers the moves
-- and tag saves of one run together, from 1, in the order they were made.
-- Rows recorded before this migration keep NULL and are ordered as before.
ALTER TABLE moves ADD COLUMN seq INTEGER;
ALTER TABLE tag_writes ADD COLUMN seq INTEGER;

-- A cue-rewrite move records what the cue sheet held before and after that
-- run rewrote it. Undoing the run writes the first back, whichever run made
-- the .cue.orig beside it, and only over a sheet that still holds the second.
-- NULL for every other move, and for rewrites recorded before this migration,
-- which undo restores from the .orig.
ALTER TABLE moves ADD COLUMN cue_before BLOB;
ALTER TABLE moves ADD COLUMN cue_after BLOB;

-- Whether the file has embedded art, in any format. NULL for rows read before
-- this migration, for which tags_json is searched for a picture as before.
ALTER TABLE files ADD COLUMN has_art INTEGER CHECK (has_art IN (0, 1));
