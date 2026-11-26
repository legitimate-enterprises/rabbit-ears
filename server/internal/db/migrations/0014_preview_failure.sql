-- The file modification instant whose seek previews failed, so the file is
-- skipped until it changes.
ALTER TABLE media_file ADD COLUMN previews_failed_for INTEGER;
