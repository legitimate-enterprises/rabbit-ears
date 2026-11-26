-- A set of movies: a TMDB franchise collection, server-wide, or a custom
-- one kept by one household. Only TMDB ones carry provider fields.
CREATE TABLE collection (
  id INTEGER PRIMARY KEY,
  source TEXT NOT NULL CHECK (source IN ('tmdb','custom')),
  household_id INTEGER REFERENCES household(id) ON DELETE CASCADE,
  tmdb_id INTEGER UNIQUE,
  name TEXT NOT NULL,
  overview TEXT,
  poster_path TEXT,
  backdrop_path TEXT,
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL,
  CHECK (CASE source WHEN 'tmdb' THEN tmdb_id IS NOT NULL AND household_id IS NULL
    ELSE tmdb_id IS NULL AND household_id IS NOT NULL AND overview IS NULL AND poster_path IS NULL AND backdrop_path IS NULL END)
);
CREATE INDEX collection_household ON collection(household_id, name COLLATE NOCASE);

-- A movie belongs to at most one TMDB collection; the scanner keeps that.
CREATE TABLE collection_movie (
  collection_id INTEGER NOT NULL REFERENCES collection(id) ON DELETE CASCADE,
  item_id INTEGER NOT NULL REFERENCES item(id) ON DELETE CASCADE,
  PRIMARY KEY (collection_id, item_id)
);
CREATE INDEX collection_movie_item ON collection_movie(item_id);

-- A TMDB collection goes with its last movie, however that movie leaves:
-- a rescan, an item or library deleted, or a refresh that moved it.
CREATE TRIGGER collection_tmdb_empty AFTER DELETE ON collection_movie
BEGIN
  DELETE FROM collection WHERE id = OLD.collection_id AND source = 'tmdb'
    AND NOT EXISTS (SELECT 1 FROM collection_movie WHERE collection_id = OLD.collection_id);
END;

-- An entry is one item or one collection. The copy keeps every id.
CREATE TABLE playlist_entry_new (
  id INTEGER PRIMARY KEY,
  playlist_id INTEGER NOT NULL REFERENCES playlist(id) ON DELETE CASCADE,
  item_id INTEGER REFERENCES item(id) ON DELETE CASCADE,
  collection_id INTEGER REFERENCES collection(id) ON DELETE CASCADE,
  position INTEGER NOT NULL CHECK (position >= 0),
  added_at INTEGER NOT NULL,
  CHECK ((item_id IS NULL) != (collection_id IS NULL))
);
INSERT INTO playlist_entry_new (id, playlist_id, item_id, position, added_at)
  SELECT id, playlist_id, item_id, position, added_at FROM playlist_entry;
DROP TABLE playlist_entry;
ALTER TABLE playlist_entry_new RENAME TO playlist_entry;
CREATE INDEX playlist_entry_order ON playlist_entry(playlist_id, position);
CREATE INDEX playlist_entry_item ON playlist_entry(item_id);
CREATE INDEX playlist_entry_collection ON playlist_entry(collection_id);
