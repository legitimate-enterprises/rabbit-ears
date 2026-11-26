-- migrate: foreign_keys off
-- Music and YouTube join the item tree: artists and channels are roots,
-- albums hang off their album artist, songs off their album and videos off
-- their channel. A song's artist_id names who performs it when that is not
-- the album artist. sort_title is the title without a leading "The ", which
-- music sorts and files letters by. A CHECK cannot change in place, so the
-- table is rebuilt with every id kept.
CREATE TABLE item_new (
  id INTEGER PRIMARY KEY,
  library_id INTEGER NOT NULL REFERENCES library(id) ON DELETE CASCADE,
  parent_id INTEGER REFERENCES item(id) ON DELETE CASCADE,
  kind TEXT NOT NULL CHECK (kind IN ('movie','show','season','episode','artist','album','song','channel','video')),
  path TEXT,
  parsed_title TEXT NOT NULL,
  parsed_year INTEGER,
  season_number INTEGER,
  episode_number INTEGER,
  disc_number INTEGER,
  track_number INTEGER,
  artist_id INTEGER REFERENCES item(id) ON DELETE SET NULL,
  sort_title TEXT,
  added_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL,
  CHECK ((kind IN ('season','episode','album','song','video')) = (parent_id IS NOT NULL)),
  CHECK (kind = 'song' OR artist_id IS NULL)
);
INSERT INTO item_new (id, library_id, parent_id, kind, path, parsed_title, parsed_year, season_number, episode_number, added_at, updated_at)
  SELECT id, library_id, parent_id, kind, path, parsed_title, parsed_year, season_number, episode_number, added_at, updated_at FROM item;
DROP TABLE item;
ALTER TABLE item_new RENAME TO item;

CREATE UNIQUE INDEX item_path ON item(library_id, kind, path) WHERE path IS NOT NULL;
CREATE UNIQUE INDEX item_season ON item(parent_id, season_number) WHERE kind = 'season';
CREATE UNIQUE INDEX item_episode ON item(parent_id, episode_number) WHERE kind = 'episode';
CREATE INDEX item_parent ON item(parent_id);
CREATE INDEX item_library_kind ON item(library_id, kind);
CREATE INDEX item_kind_title ON item(kind, parsed_title COLLATE NOCASE, id);
CREATE INDEX item_kind_sort ON item(kind, sort_title COLLATE NOCASE, id) WHERE sort_title IS NOT NULL;
CREATE INDEX item_artist ON item(artist_id) WHERE artist_id IS NOT NULL;
CREATE INDEX item_kind_added ON item(kind, added_at);

-- A provider's string id: a MusicBrainz MBID, or a YouTube channel or video id.
ALTER TABLE item_metadata ADD COLUMN external_id TEXT;
CREATE INDEX item_metadata_external ON item_metadata(provider, external_id) WHERE external_id IS NOT NULL;
