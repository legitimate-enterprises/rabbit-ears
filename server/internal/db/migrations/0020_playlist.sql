-- A profile's own playlist of one kind: video (movies, episodes, YouTube
-- videos) or music (songs).
CREATE TABLE playlist (
  id INTEGER PRIMARY KEY,
  account_id INTEGER NOT NULL REFERENCES account(id) ON DELETE CASCADE,
  name TEXT NOT NULL,
  kind TEXT NOT NULL CHECK (kind IN ('video','music')),
  last_played_at INTEGER,
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL
);
CREATE INDEX playlist_account ON playlist(account_id, name COLLATE NOCASE);

-- position orders entries from 0. It is not unique: a move rewrites several
-- rows, and a deleted item leaves a gap until the next write renumbers.
CREATE TABLE playlist_entry (
  id INTEGER PRIMARY KEY,
  playlist_id INTEGER NOT NULL REFERENCES playlist(id) ON DELETE CASCADE,
  item_id INTEGER NOT NULL REFERENCES item(id) ON DELETE CASCADE,
  position INTEGER NOT NULL CHECK (position >= 0),
  added_at INTEGER NOT NULL
);
CREATE INDEX playlist_entry_order ON playlist_entry(playlist_id, position);
CREATE INDEX playlist_entry_item ON playlist_entry(item_id);
