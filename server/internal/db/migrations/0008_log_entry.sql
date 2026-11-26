CREATE TABLE log_entry (
  id INTEGER PRIMARY KEY,
  at INTEGER NOT NULL,
  level TEXT NOT NULL CHECK (level IN ('debug','info','warn','error')),
  source TEXT NOT NULL,
  message TEXT NOT NULL,
  session_id INTEGER,
  item_id INTEGER,
  ffmpeg_command TEXT,
  transcode_reason TEXT
);
CREATE INDEX log_entry_at ON log_entry(at);
CREATE INDEX log_entry_source ON log_entry(source, at);
