-- The custom image the owner of a playlist or a station set, one at most
-- each: a JPEG the server made of an upload or a fetched link, or a title's
-- artwork by reference. Ids are never reused, since clients cache by them.
CREATE TABLE cover (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  playlist_id INTEGER UNIQUE REFERENCES playlist(id) ON DELETE CASCADE,
  station_id INTEGER UNIQUE REFERENCES station(id) ON DELETE CASCADE,
  item_id INTEGER REFERENCES item(id) ON DELETE CASCADE,
  backdrop INTEGER NOT NULL DEFAULT 0 CHECK (backdrop IN (0,1)),
  jpeg BLOB,
  created_at INTEGER NOT NULL,
  CHECK ((playlist_id IS NULL) != (station_id IS NULL)),
  CHECK ((item_id IS NULL) != (jpeg IS NULL))
);
CREATE INDEX cover_item ON cover(item_id);
