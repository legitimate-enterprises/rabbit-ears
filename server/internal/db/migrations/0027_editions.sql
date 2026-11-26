-- migrate: foreign_keys off
-- A title (item) has editions, each one cut of it; an edition holds files,
-- interchangeable copies of that cut, and a file may sit in the editions of
-- several titles (a file holding two episodes). tag is the {edition-Name}
-- of the file names, NULL for an untagged cut; runtime_ms is the cut's
-- runtime, which tells untagged cuts apart.
CREATE TABLE edition (
  id INTEGER PRIMARY KEY,
  item_id INTEGER NOT NULL REFERENCES item(id) ON DELETE CASCADE,
  tag TEXT,
  runtime_ms INTEGER
);
CREATE INDEX edition_item ON edition(item_id);
CREATE UNIQUE INDEX edition_tag ON edition(item_id, tag COLLATE NOCASE) WHERE tag IS NOT NULL;

CREATE TABLE edition_file (
  edition_id INTEGER NOT NULL REFERENCES edition(id) ON DELETE CASCADE,
  media_file_id INTEGER NOT NULL REFERENCES media_file(id) ON DELETE CASCADE,
  PRIMARY KEY (edition_id, media_file_id)
);
CREATE INDEX edition_file_file ON edition_file(media_file_id);

-- Each item's files become one edition per {edition-Name} tag a movie's
-- file names carry, the untagged ones one edition; any other item's files
-- one untagged edition.
CREATE TEMP TABLE file_tag AS
  SELECT f.id, f.item_id, f.size_bytes, f.duration_ms,
    CASE WHEN i.kind = 'movie' AND f.at > 0 AND instr(substr(f.path, f.at + 9), '}') > 1
      THEN NULLIF(trim(substr(f.path, f.at + 9, instr(substr(f.path, f.at + 9), '}') - 1)), '') END AS tag
  FROM (SELECT id, item_id, path, size_bytes, duration_ms, instr(lower(path), '{edition-') AS at FROM media_file) f
  JOIN item i ON i.id = f.item_id;
INSERT INTO edition (item_id, tag, runtime_ms)
  SELECT k.item_id, k.tag, (SELECT g.duration_ms FROM file_tag g WHERE g.item_id = k.item_id
      AND lower(COALESCE(g.tag, '')) = lower(COALESCE(k.tag, '')) ORDER BY g.size_bytes DESC, g.id LIMIT 1)
  FROM (SELECT item_id, MIN(tag) AS tag FROM file_tag GROUP BY item_id, lower(COALESCE(tag, ''))) k
  ORDER BY k.item_id, k.tag IS NOT NULL, k.tag;
INSERT INTO edition_file (edition_id, media_file_id)
  SELECT e.id, t.id FROM file_tag t JOIN edition e ON e.item_id = t.item_id AND lower(COALESCE(e.tag, '')) = lower(COALESCE(t.tag, ''));
DROP TABLE file_tag;

-- media_file loses item_id; the copy keeps every id, so tracks, chapters,
-- previews and sessions still point at their file.
CREATE TABLE media_file_new (
  id INTEGER PRIMARY KEY,
  path TEXT NOT NULL UNIQUE,
  size_bytes INTEGER NOT NULL,
  modified_at INTEGER NOT NULL,
  container TEXT,
  duration_ms INTEGER,
  bitrate INTEGER,
  probed_at INTEGER,
  previews_failed_for INTEGER
);
INSERT INTO media_file_new (id, path, size_bytes, modified_at, container, duration_ms, bitrate, probed_at, previews_failed_for)
  SELECT id, path, size_bytes, modified_at, container, duration_ms, bitrate, probed_at, previews_failed_for FROM media_file;
DROP TABLE media_file;
ALTER TABLE media_file_new RENAME TO media_file;

-- A file goes with the last edition holding it.
CREATE TRIGGER edition_file_orphan AFTER DELETE ON edition_file
BEGIN
  DELETE FROM media_file WHERE id = OLD.media_file_id
    AND NOT EXISTS (SELECT 1 FROM edition_file WHERE media_file_id = OLD.media_file_id);
END;

CREATE VIEW item_file AS
  SELECT e.item_id, ef.edition_id, ef.media_file_id FROM edition e JOIN edition_file ef ON ef.edition_id = e.id;

-- A title's entry names the edition it plays, the default; a container's
-- names none.
ALTER TABLE playlist_entry ADD COLUMN edition_id INTEGER REFERENCES edition(id) ON DELETE CASCADE;
UPDATE playlist_entry SET edition_id = (SELECT e.id FROM edition e WHERE e.item_id = playlist_entry.item_id ORDER BY e.tag IS NOT NULL, e.id LIMIT 1)
  WHERE item_id IS NOT NULL;
CREATE INDEX playlist_entry_edition ON playlist_entry(edition_id) WHERE edition_id IS NOT NULL;

-- The resume position belongs to one edition, the default.
ALTER TABLE watch_state ADD COLUMN edition_id INTEGER REFERENCES edition(id) ON DELETE SET NULL;
UPDATE watch_state SET edition_id = (SELECT e.id FROM edition e WHERE e.item_id = watch_state.item_id ORDER BY e.tag IS NOT NULL, e.id LIMIT 1);
