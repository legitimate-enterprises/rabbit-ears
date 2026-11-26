-- tmdb_id is NULL for rows an admin added by hand.
CREATE TABLE genre (
  id INTEGER PRIMARY KEY,
  name TEXT NOT NULL UNIQUE COLLATE NOCASE,
  tmdb_id INTEGER UNIQUE
);

CREATE TABLE item_genre (
  item_id INTEGER NOT NULL REFERENCES item(id) ON DELETE CASCADE,
  genre_id INTEGER NOT NULL REFERENCES genre(id) ON DELETE CASCADE,
  PRIMARY KEY (item_id, genre_id)
);
CREATE INDEX item_genre_genre ON item_genre(genre_id);

CREATE TABLE person (
  id INTEGER PRIMARY KEY,
  tmdb_id INTEGER UNIQUE,
  name TEXT NOT NULL,
  profile_path TEXT
);
CREATE INDEX person_name ON person(name COLLATE NOCASE);

-- position is billing order within an item's cast, or listing order within its crew.
CREATE TABLE credit (
  id INTEGER PRIMARY KEY,
  item_id INTEGER NOT NULL REFERENCES item(id) ON DELETE CASCADE,
  person_id INTEGER NOT NULL REFERENCES person(id) ON DELETE CASCADE,
  kind TEXT NOT NULL CHECK (kind IN ('cast','crew')),
  character TEXT,
  job TEXT,
  department TEXT,
  position INTEGER NOT NULL,
  UNIQUE (item_id, kind, position)
);
CREATE INDEX credit_person ON credit(person_id);

-- Items matched before genres and credits were fetched have none; making them
-- and the metadata task due now lets the next metadata run fill them in.
UPDATE item_metadata SET retry_at = 0 WHERE status = 'matched' AND provider_id IS NOT NULL;
UPDATE task SET next_run_at = 0 WHERE kind = 'metadata';
