-- When ffprobe last failed to read the file and what it said; NULL once a
-- probe of it succeeds.
ALTER TABLE media_file ADD COLUMN probe_failed_at INTEGER;
ALTER TABLE media_file ADD COLUMN probe_error TEXT;
