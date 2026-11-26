-- Dolby Vision profile from the DOVI configuration record; 7 carries an
-- enhancement layer that HDR10-only clients cannot use.
ALTER TABLE track ADD COLUMN dv_profile INTEGER;

-- The secret a session's stream URLs carry in place of a bearer token.
ALTER TABLE playback_session ADD COLUMN stream_key TEXT;
CREATE UNIQUE INDEX playback_session_stream_key ON playback_session(stream_key);

-- What the encoder needs to reproduce an entry. Track IDs go NULL when a
-- re-probe replaces the file's tracks; the encoder then falls back to the
-- file's first track of that kind.
ALTER TABLE session_plan_entry ADD COLUMN audio_track_id INTEGER REFERENCES track(id) ON DELETE SET NULL;
ALTER TABLE session_plan_entry ADD COLUMN audio_channels INTEGER NOT NULL DEFAULT 0;
ALTER TABLE session_plan_entry ADD COLUMN subtitle_track_id INTEGER REFERENCES track(id) ON DELETE SET NULL;
ALTER TABLE session_plan_entry ADD COLUMN video_codec TEXT NOT NULL DEFAULT '';
ALTER TABLE session_plan_entry ADD COLUMN encoder TEXT NOT NULL DEFAULT '' CHECK (encoder IN ('','hardware','software'));
ALTER TABLE session_plan_entry ADD COLUMN tone_map INTEGER NOT NULL DEFAULT 0 CHECK (tone_map IN (0,1));
ALTER TABLE session_plan_entry ADD COLUMN top_height INTEGER NOT NULL DEFAULT 0;
ALTER TABLE session_plan_entry ADD COLUMN top_bitrate INTEGER NOT NULL DEFAULT 0;
ALTER TABLE session_plan_entry ADD COLUMN single_rung INTEGER NOT NULL DEFAULT 1 CHECK (single_rung IN (0,1));

-- Player telemetry the adaptive ladder reads.
CREATE TABLE throughput_sample (
  id INTEGER PRIMARY KEY,
  session_id INTEGER NOT NULL REFERENCES playback_session(id) ON DELETE CASCADE,
  at INTEGER NOT NULL,
  measured_bitrate INTEGER NOT NULL,
  buffer_ms INTEGER NOT NULL,
  stall_count INTEGER NOT NULL,
  current_rung INTEGER
);
CREATE INDEX throughput_sample_session ON throughput_sample(session_id, at);
