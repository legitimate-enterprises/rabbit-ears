-- Times are INTEGER Unix milliseconds (UTC instants).
CREATE TABLE library (
  id INTEGER PRIMARY KEY,
  name TEXT NOT NULL UNIQUE,
  kind TEXT NOT NULL CHECK (kind IN ('movies','tv','music','youtube')),
  scan_hour INTEGER NOT NULL DEFAULT 3 CHECK (scan_hour BETWEEN 0 AND 23),
  watch_files INTEGER NOT NULL DEFAULT 1 CHECK (watch_files IN (0,1)),
  last_scanned_at INTEGER,
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL
);

CREATE TABLE library_folder (
  id INTEGER PRIMARY KEY,
  library_id INTEGER NOT NULL REFERENCES library(id) ON DELETE CASCADE,
  path TEXT NOT NULL UNIQUE
);

-- File-derived fields live here; provider fields live in item_metadata.
CREATE TABLE item (
  id INTEGER PRIMARY KEY,
  library_id INTEGER NOT NULL REFERENCES library(id) ON DELETE CASCADE,
  parent_id INTEGER REFERENCES item(id) ON DELETE CASCADE,
  kind TEXT NOT NULL CHECK (kind IN ('movie','show','season','episode')),
  path TEXT,
  parsed_title TEXT NOT NULL,
  parsed_year INTEGER,
  season_number INTEGER,
  episode_number INTEGER,
  added_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL,
  CHECK ((kind IN ('season','episode')) = (parent_id IS NOT NULL))
);
CREATE UNIQUE INDEX item_path ON item(library_id, kind, path) WHERE path IS NOT NULL;
CREATE UNIQUE INDEX item_season ON item(parent_id, season_number) WHERE kind = 'season';
CREATE UNIQUE INDEX item_episode ON item(parent_id, episode_number) WHERE kind = 'episode';
CREATE INDEX item_parent ON item(parent_id);
CREATE INDEX item_library_kind ON item(library_id, kind);

CREATE TABLE item_metadata (
  item_id INTEGER PRIMARY KEY REFERENCES item(id) ON DELETE CASCADE,
  provider TEXT NOT NULL DEFAULT 'tmdb',
  provider_id INTEGER,
  status TEXT NOT NULL DEFAULT 'unmatched' CHECK (status IN ('unmatched','matched')),
  title TEXT,
  overview TEXT,
  release_date TEXT,
  runtime_minutes INTEGER,
  content_rating TEXT,
  poster_path TEXT,
  backdrop_path TEXT,
  fetched_at INTEGER,
  retry_at INTEGER
);
CREATE INDEX item_metadata_retry ON item_metadata(retry_at) WHERE retry_at IS NOT NULL;

CREATE TABLE media_file (
  id INTEGER PRIMARY KEY,
  item_id INTEGER NOT NULL REFERENCES item(id) ON DELETE CASCADE,
  path TEXT NOT NULL UNIQUE,
  size_bytes INTEGER NOT NULL,
  modified_at INTEGER NOT NULL,
  container TEXT,
  duration_ms INTEGER,
  bitrate INTEGER,
  probed_at INTEGER
);
CREATE INDEX media_file_item ON media_file(item_id);

CREATE TABLE track (
  id INTEGER PRIMARY KEY,
  media_file_id INTEGER NOT NULL REFERENCES media_file(id) ON DELETE CASCADE,
  stream_index INTEGER NOT NULL,
  kind TEXT NOT NULL CHECK (kind IN ('video','audio','subtitle')),
  codec TEXT NOT NULL,
  profile TEXT,
  level INTEGER,
  language TEXT,
  title TEXT,
  is_default INTEGER NOT NULL DEFAULT 0 CHECK (is_default IN (0,1)),
  is_forced INTEGER NOT NULL DEFAULT 0 CHECK (is_forced IN (0,1)),
  bitrate INTEGER,
  width INTEGER,
  height INTEGER,
  frame_rate REAL,
  bit_depth INTEGER,
  pixel_format TEXT,
  color_primaries TEXT,
  color_transfer TEXT,
  hdr_format TEXT,
  channels INTEGER,
  channel_layout TEXT,
  sample_rate INTEGER,
  subtitle_is_text INTEGER CHECK (subtitle_is_text IN (0,1)),
  UNIQUE (media_file_id, stream_index)
);

CREATE TABLE chapter (
  id INTEGER PRIMARY KEY,
  media_file_id INTEGER NOT NULL REFERENCES media_file(id) ON DELETE CASCADE,
  position INTEGER NOT NULL,
  start_ms INTEGER NOT NULL,
  end_ms INTEGER NOT NULL,
  title TEXT,
  thumbnail_path TEXT,
  UNIQUE (media_file_id, position)
);
