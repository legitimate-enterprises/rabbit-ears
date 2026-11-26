-- One file the server makes for downloads: a title's file converted into a
-- single MP4, or an M4A for a song, shared by every device that needs the
-- same one. Its columns are the conversion's plan entry; a source modified
-- since is another conversion. 0 in a track column is none.
CREATE TABLE conversion (
  id INTEGER PRIMARY KEY,
  media_file_id INTEGER NOT NULL REFERENCES media_file(id) ON DELETE CASCADE,
  source_modified_at INTEGER NOT NULL,
  verdict TEXT NOT NULL CHECK (verdict IN ('remux','audio_transcode','video_transcode')),
  video_action TEXT NOT NULL CHECK (video_action IN ('copy','convert')),
  video_codec TEXT NOT NULL DEFAULT '',
  tone_map INTEGER NOT NULL DEFAULT 0 CHECK (tone_map IN (0,1)),
  height INTEGER NOT NULL DEFAULT 0,
  bitrate INTEGER NOT NULL DEFAULT 0,
  audio_track_id INTEGER NOT NULL DEFAULT 0,
  audio_action TEXT NOT NULL CHECK (audio_action IN ('copy','convert')),
  audio_channels INTEGER NOT NULL DEFAULT 0,
  burn_track_id INTEGER NOT NULL DEFAULT 0,
  container TEXT NOT NULL CHECK (container IN ('mp4','m4a')),
  status TEXT NOT NULL CHECK (status IN ('queued','converting','ready','failed')),
  predicted_bytes INTEGER NOT NULL,
  size_bytes INTEGER,
  error TEXT,
  created_at INTEGER NOT NULL,
  started_at INTEGER,
  finished_at INTEGER,
  UNIQUE (media_file_id, source_modified_at, verdict, video_action, video_codec, tone_map, height, bitrate,
    audio_track_id, audio_action, audio_channels, burn_track_id)
);
CREATE INDEX conversion_queue ON conversion(status, id);
