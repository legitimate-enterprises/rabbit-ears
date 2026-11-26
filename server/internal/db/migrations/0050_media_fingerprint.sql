-- What identifies the file by its media alone (see probe.Fingerprint);
-- NULL until it is read.
ALTER TABLE media_file ADD COLUMN fingerprint TEXT;
