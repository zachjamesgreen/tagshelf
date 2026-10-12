-- The genres and the composers of a file, every value of the tag joined by
-- "; ". NULL for a row read before this migration: the next run fills it in
-- from the row's tags_json, without reading the file again.
ALTER TABLE files ADD COLUMN genre TEXT;
ALTER TABLE files ADD COLUMN composer TEXT;
