-- What a channel check wants of a channel: each video to download, in the
-- channel's newest-first order, and each video deleted from the library,
-- which no later check downloads again. Downloaded videos are items.
CREATE TABLE channel_video (
  channel_id INTEGER NOT NULL REFERENCES item(id) ON DELETE CASCADE,
  external_id TEXT NOT NULL,
  state TEXT NOT NULL CHECK (state IN ('queued','skipped')),
  position INTEGER NOT NULL,
  PRIMARY KEY (channel_id, external_id)
);
CREATE INDEX channel_video_queued ON channel_video(channel_id, position) WHERE state = 'queued';
