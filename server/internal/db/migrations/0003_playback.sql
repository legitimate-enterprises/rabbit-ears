CREATE TABLE playback_session (
  id INTEGER PRIMARY KEY,
  device_id INTEGER NOT NULL REFERENCES device(id) ON DELETE CASCADE,
  account_id INTEGER NOT NULL REFERENCES account(id) ON DELETE CASCADE,
  item_id INTEGER NOT NULL REFERENCES item(id) ON DELETE CASCADE,
  media_file_id INTEGER NOT NULL REFERENCES media_file(id) ON DELETE CASCADE,
  position_ms INTEGER NOT NULL DEFAULT 0,
  current_entry INTEGER NOT NULL DEFAULT 0,
  started_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL,
  ended_at INTEGER
);
CREATE INDEX playback_session_active ON playback_session(device_id) WHERE ended_at IS NULL;

-- One row per rung of the ordered plan; a failed entry is marked bad and the
-- session moves to the next position.
CREATE TABLE session_plan_entry (
  session_id INTEGER NOT NULL REFERENCES playback_session(id) ON DELETE CASCADE,
  position INTEGER NOT NULL,
  verdict TEXT NOT NULL CHECK (verdict IN ('direct_play','remux','audio_transcode','video_transcode')),
  video_action TEXT NOT NULL CHECK (video_action IN ('copy','convert')),
  audio_action TEXT NOT NULL CHECK (audio_action IN ('copy','convert')),
  subtitle_action TEXT NOT NULL CHECK (subtitle_action IN ('none','sidecar','convert','burn')),
  bad INTEGER NOT NULL DEFAULT 0 CHECK (bad IN (0,1)),
  bad_reason TEXT,
  PRIMARY KEY (session_id, position)
);

CREATE TABLE watch_state (
  account_id INTEGER NOT NULL REFERENCES account(id) ON DELETE CASCADE,
  item_id INTEGER NOT NULL REFERENCES item(id) ON DELETE CASCADE,
  position_ms INTEGER NOT NULL DEFAULT 0,
  watched INTEGER NOT NULL DEFAULT 0 CHECK (watched IN (0,1)),
  play_count INTEGER NOT NULL DEFAULT 0,
  last_played_at INTEGER,
  updated_at INTEGER NOT NULL,
  PRIMARY KEY (account_id, item_id)
);
CREATE INDEX watch_state_recent ON watch_state(account_id, last_played_at);
