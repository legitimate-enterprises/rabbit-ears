-- One row per YouTube library: how often its channels are checked and what
-- it downloads and keeps.
CREATE TABLE youtube_settings (
  library_id INTEGER PRIMARY KEY REFERENCES library(id) ON DELETE CASCADE,
  check_interval_minutes INTEGER NOT NULL DEFAULT 30 CHECK (check_interval_minutes BETWEEN 15 AND 1440),
  max_height INTEGER NOT NULL DEFAULT 1080 CHECK (max_height BETWEEN 240 AND 4320),
  keep_newest INTEGER NOT NULL DEFAULT 200 CHECK (keep_newest BETWEEN 1 AND 1000),
  delete_watched INTEGER NOT NULL DEFAULT 1 CHECK (delete_watched IN (0,1)),
  sponsor_marking INTEGER NOT NULL DEFAULT 1 CHECK (sponsor_marking IN (0,1))
);

-- A channel its library checks for new videos. Removing the subscription
-- stops downloads and keeps the channel item and its videos.
CREATE TABLE channel_subscription (
  id INTEGER PRIMARY KEY,
  channel_id INTEGER NOT NULL UNIQUE REFERENCES item(id) ON DELETE CASCADE,
  address TEXT NOT NULL,
  status TEXT NOT NULL DEFAULT 'queued' CHECK (status IN ('queued','downloading','up_to_date','failed')),
  download_current INTEGER,
  download_total INTEGER,
  last_checked_at INTEGER,
  last_error TEXT,
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL
);

-- A channel's own playlists as YouTube lists them; the channel check writes
-- them, and a video row exists only for videos in the library.
CREATE TABLE channel_playlist (
  id INTEGER PRIMARY KEY,
  channel_id INTEGER NOT NULL REFERENCES item(id) ON DELETE CASCADE,
  external_id TEXT NOT NULL,
  title TEXT NOT NULL,
  position INTEGER NOT NULL,
  UNIQUE (channel_id, external_id)
);

CREATE TABLE channel_playlist_video (
  playlist_id INTEGER NOT NULL REFERENCES channel_playlist(id) ON DELETE CASCADE,
  video_id INTEGER NOT NULL REFERENCES item(id) ON DELETE CASCADE,
  position INTEGER NOT NULL,
  PRIMARY KEY (playlist_id, video_id)
);
CREATE INDEX channel_playlist_video_video ON channel_playlist_video(video_id);
