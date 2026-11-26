-- max_transcodes is how many video transcodes run at once, and
-- transcode_cache_cap_bytes how much the segment cache holds before idle
-- sessions' segments are pruned.
ALTER TABLE server_info ADD COLUMN max_transcodes INTEGER NOT NULL DEFAULT 3 CHECK (max_transcodes BETWEEN 1 AND 8);
ALTER TABLE server_info ADD COLUMN transcode_cache_cap_bytes INTEGER NOT NULL DEFAULT 20000000000
  CHECK (transcode_cache_cap_bytes BETWEEN 1000000000 AND 1000000000000);
