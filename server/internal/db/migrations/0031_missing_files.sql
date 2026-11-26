-- When a scan or a playback first found the file gone from disk; NULL
-- while it is there.
ALTER TABLE media_file ADD COLUMN missing_at INTEGER;
