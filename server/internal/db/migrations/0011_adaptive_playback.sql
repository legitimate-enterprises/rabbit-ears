-- A 240p rung sits under the standard ones.
ALTER TABLE session_plan_entry ADD COLUMN floor_rung INTEGER NOT NULL DEFAULT 0 CHECK (floor_rung IN (0,1));

-- HLS segments muxed as MPEG-TS for a client that plays only those.
ALTER TABLE session_plan_entry ADD COLUMN ts_segments INTEGER NOT NULL DEFAULT 0 CHECK (ts_segments IN (0,1));

-- The variant the player was on, as its playlist declares it.
ALTER TABLE throughput_sample ADD COLUMN rung_height INTEGER;
ALTER TABLE throughput_sample ADD COLUMN rung_bitrate INTEGER;
