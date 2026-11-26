-- migrate: foreign_keys off
-- A playlist holds one library kind: movies, tv or music. A video playlist
-- holding only movies becomes movies (an empty one too), one holding only
-- episodes becomes tv, and any other is deleted with its entries. A CHECK
-- cannot change in place, so the table is rebuilt with every id kept.
CREATE TABLE playlist_new (
  id INTEGER PRIMARY KEY,
  account_id INTEGER NOT NULL REFERENCES account(id) ON DELETE CASCADE,
  name TEXT NOT NULL,
  kind TEXT NOT NULL CHECK (kind IN ('movies','tv','music')),
  last_played_at INTEGER,
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL
);
INSERT INTO playlist_new (id, account_id, name, kind, last_played_at, created_at, updated_at)
  SELECT p.id, p.account_id, p.name,
    CASE
      WHEN p.kind = 'music' THEN 'music'
      WHEN NOT EXISTS (SELECT 1 FROM playlist_entry e JOIN item i ON i.id = e.item_id WHERE e.playlist_id = p.id AND i.kind != 'movie') THEN 'movies'
      WHEN NOT EXISTS (SELECT 1 FROM playlist_entry e JOIN item i ON i.id = e.item_id WHERE e.playlist_id = p.id AND i.kind != 'episode') THEN 'tv'
    END,
    p.last_played_at, p.created_at, p.updated_at
  FROM playlist p
  WHERE p.kind = 'music'
    OR NOT EXISTS (SELECT 1 FROM playlist_entry e JOIN item i ON i.id = e.item_id WHERE e.playlist_id = p.id AND i.kind != 'movie')
    OR NOT EXISTS (SELECT 1 FROM playlist_entry e JOIN item i ON i.id = e.item_id WHERE e.playlist_id = p.id AND i.kind != 'episode');
DELETE FROM playlist_entry WHERE playlist_id NOT IN (SELECT id FROM playlist_new);
DROP TABLE playlist;
ALTER TABLE playlist_new RENAME TO playlist;
CREATE INDEX playlist_account ON playlist(account_id, name COLLATE NOCASE);
