-- A YouTube library is a folder the scan reads: each top-level folder is a
-- channel, each video file beneath it a video. Channels without a folder
-- go with their videos; the scan finds their files again.
DROP TABLE channel_video;
DROP TABLE channel_playlist_video;
DROP TABLE channel_playlist;
DROP TABLE channel_subscription;
DROP TABLE youtube_settings;
DELETE FROM task WHERE kind = 'youtube-downloads' OR kind LIKE 'youtube:%';
DELETE FROM item WHERE kind = 'channel' AND path IS NULL;

-- A video's publish time, and the sum of its sidecar files' modification
-- times when the scan last read them: a sidecar that arrives, changes or
-- goes changes the sum, and the scan reads them again.
ALTER TABLE item_metadata ADD COLUMN published_at INTEGER;
ALTER TABLE item_metadata ADD COLUMN sidecar_stamp INTEGER;
