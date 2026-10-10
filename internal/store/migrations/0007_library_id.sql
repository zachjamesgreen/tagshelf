-- Which library the database belongs to: the ID in the library's
-- .tagshelf-library file, recorded the first time the database is opened for
-- a library. A database is refused for any other library, since its roots and
-- index would be rewritten for that library's files. No row until then, and
-- never more than one.
CREATE TABLE library (
    only_row INTEGER PRIMARY KEY CHECK (only_row = 1),
    id       TEXT    NOT NULL
);
