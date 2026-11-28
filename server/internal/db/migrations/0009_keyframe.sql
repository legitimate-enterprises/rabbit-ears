-- The keyframes a media file's copied video is cut on (transcode.Bounds),
-- as probed at the file's modification time source_modified_at: for each
-- multiple of the segment length, the keyframe a seek there lands on, at_us
-- microseconds into the file. A failed probe has its row, failed set, and
-- no keyframes. Probing again replaces both.

CREATE TABLE keyframe_probe (
  media_file_id BLOB NOT NULL PRIMARY KEY REFERENCES media_file(id) ON DELETE CASCADE,
  source_modified_at INTEGER NOT NULL,
  failed INTEGER NOT NULL CHECK (failed IN (0,1))
);

CREATE TABLE keyframe (
  media_file_id BLOB NOT NULL REFERENCES media_file(id) ON DELETE CASCADE,
  at_us INTEGER NOT NULL,
  PRIMARY KEY (media_file_id, at_us)
);
