-- A station is continuous programming from one of its owner's playlists,
-- whose kind it takes. Its household is its owner's. rating is the high
-- water mark of the playlist's content, NULL for none; loop numbers the
-- randomized loop station_loop holds.
CREATE TABLE station (
  id INTEGER PRIMARY KEY,
  account_id INTEGER NOT NULL REFERENCES account(id) ON DELETE CASCADE,
  playlist_id INTEGER NOT NULL REFERENCES playlist(id),
  name TEXT NOT NULL,
  shuffled INTEGER NOT NULL DEFAULT 0 CHECK (shuffled IN (0,1)),
  visibility TEXT NOT NULL CHECK (visibility IN ('private','household','server')),
  rating TEXT,
  loop INTEGER NOT NULL DEFAULT 0,
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL
);
CREATE INDEX station_account ON station(account_id);
CREATE INDEX station_playlist ON station(playlist_id);

-- A limited schedule: programs start only on these weekdays (0 Sunday) and
-- within these ranges of local minutes after midnight; none of either is
-- continuous. A range whose end is at or before its start runs past
-- midnight.
CREATE TABLE station_day (
  station_id INTEGER NOT NULL REFERENCES station(id) ON DELETE CASCADE,
  weekday INTEGER NOT NULL CHECK (weekday BETWEEN 0 AND 6),
  PRIMARY KEY (station_id, weekday)
);
CREATE TABLE station_time (
  id INTEGER PRIMARY KEY,
  station_id INTEGER NOT NULL REFERENCES station(id) ON DELETE CASCADE,
  from_minute INTEGER NOT NULL CHECK (from_minute BETWEEN 0 AND 1439),
  to_minute INTEGER NOT NULL CHECK (to_minute BETWEEN 0 AND 1439)
);
CREATE INDEX station_time_station ON station_time(station_id);

-- The schedule: what plays when, by item and edition. entry_id, loop and
-- position place the program in its loop, so the next can follow it.
CREATE TABLE station_program (
  id INTEGER PRIMARY KEY,
  station_id INTEGER NOT NULL REFERENCES station(id) ON DELETE CASCADE,
  item_id INTEGER NOT NULL REFERENCES item(id) ON DELETE CASCADE,
  edition_id INTEGER NOT NULL REFERENCES edition(id) ON DELETE CASCADE,
  entry_id INTEGER REFERENCES playlist_entry(id) ON DELETE SET NULL,
  loop INTEGER NOT NULL,
  position INTEGER NOT NULL,
  starts_at INTEGER NOT NULL,
  ends_at INTEGER NOT NULL CHECK (ends_at > starts_at)
);
CREATE INDEX station_program_time ON station_program(station_id, starts_at);
CREATE INDEX station_program_item ON station_program(item_id);
CREATE INDEX station_program_edition ON station_program(edition_id);
CREATE INDEX station_program_entry ON station_program(entry_id);

-- A randomized station's loop in the order it plays. Entry and item are a
-- key into the play order, not references: a key whose row is gone is
-- skipped.
CREATE TABLE station_loop (
  station_id INTEGER NOT NULL REFERENCES station(id) ON DELETE CASCADE,
  position INTEGER NOT NULL,
  entry_id INTEGER NOT NULL,
  item_id INTEGER NOT NULL,
  PRIMARY KEY (station_id, position)
);

-- A station session marks nothing watched, saves no position and counts
-- as no play.
ALTER TABLE playback_session ADD COLUMN station INTEGER NOT NULL DEFAULT 0 CHECK (station IN (0,1));
