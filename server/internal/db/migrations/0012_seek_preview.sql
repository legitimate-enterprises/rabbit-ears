-- One set of seek preview frames per media file, replaced when the file changes.
CREATE TABLE seek_preview (
  id INTEGER PRIMARY KEY,
  media_file_id INTEGER NOT NULL UNIQUE REFERENCES media_file(id) ON DELETE CASCADE,
  dir TEXT NOT NULL,
  interval_ms INTEGER NOT NULL CHECK (interval_ms > 0),
  count INTEGER NOT NULL CHECK (count > 0),
  width INTEGER NOT NULL CHECK (width > 0),
  height INTEGER NOT NULL CHECK (height > 0),
  created_at INTEGER NOT NULL
);
