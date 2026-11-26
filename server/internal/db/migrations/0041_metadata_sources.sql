-- Where titles' metadata comes from: local data (the files' tags, art and
-- sidecars) or remote (TMDB, MusicBrainz, the Cover Art Archive, YouTube
-- avatars), and whether the other fills what the first lacks.
ALTER TABLE server_info ADD COLUMN metadata_source TEXT NOT NULL DEFAULT 'local' CHECK (metadata_source IN ('local','remote'));
ALTER TABLE server_info ADD COLUMN metadata_mode TEXT NOT NULL DEFAULT 'only' CHECK (metadata_mode IN ('only','preferred'));

-- An item has a local row, a remote row, or both; the settings pick per
-- field when it is read, so neither is ever deleted for being unused.
-- Local rows carry provider 'local'.
CREATE TABLE item_metadata_new (
  item_id INTEGER NOT NULL REFERENCES item(id) ON DELETE CASCADE,
  source TEXT NOT NULL DEFAULT 'remote' CHECK (source IN ('local','remote')),
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
  retry_at INTEGER,
  external_id TEXT,
  published_at INTEGER,
  sidecar_stamp INTEGER,
  PRIMARY KEY (item_id, source)
);

-- YouTube's sidecars and folder posters are local; a channel's
-- avatar and an album's Cover Art Archive cover and match are remote; an
-- album's own cover is local; an album no provider was asked about has
-- no remote row, so the next lookup still finds it.
INSERT INTO item_metadata_new (item_id, source, provider, status, title, overview, poster_path, fetched_at, external_id, published_at, sidecar_stamp)
  SELECT m.item_id, 'local', 'local', m.status,
    CASE WHEN i.kind IN ('channel','video') THEN m.title END,
    CASE WHEN i.kind IN ('channel','video') THEN m.overview END,
    CASE WHEN i.kind = 'video' OR i.kind = 'channel' AND m.poster_path = i.path || '/poster.jpg' OR i.kind = 'album' AND instr(m.poster_path, '/caa/') = 0
      THEN m.poster_path END,
    m.fetched_at,
    CASE WHEN i.kind IN ('channel','video') THEN m.external_id END,
    m.published_at, m.sidecar_stamp
  FROM item_metadata m JOIN item i ON i.id = m.item_id
  WHERE i.kind IN ('channel','video') OR i.kind = 'album' AND instr(m.poster_path, '/caa/') = 0;

INSERT INTO item_metadata_new (item_id, source, provider, provider_id, status, title, overview, release_date, runtime_minutes, content_rating,
    poster_path, backdrop_path, fetched_at, retry_at, external_id)
  SELECT m.item_id, 'remote', m.provider, m.provider_id, m.status,
    CASE WHEN i.kind != 'channel' THEN m.title END,
    CASE WHEN i.kind != 'channel' THEN m.overview END,
    m.release_date, m.runtime_minutes, m.content_rating,
    CASE WHEN i.kind = 'channel' AND m.poster_path = i.path || '/poster.jpg' OR i.kind = 'album' AND instr(m.poster_path, '/caa/') = 0
      THEN NULL ELSE m.poster_path END,
    m.backdrop_path, m.fetched_at, m.retry_at,
    CASE WHEN i.kind != 'channel' THEN m.external_id END
  FROM item_metadata m JOIN item i ON i.id = m.item_id
  WHERE i.kind != 'video' AND (i.kind != 'channel' OR m.retry_at IS NOT NULL)
    AND NOT (i.kind = 'album' AND m.fetched_at IS NULL AND m.retry_at IS NULL);

DROP TABLE item_metadata;
ALTER TABLE item_metadata_new RENAME TO item_metadata;
CREATE INDEX item_metadata_retry ON item_metadata(retry_at) WHERE retry_at IS NOT NULL;
CREATE INDEX item_metadata_external ON item_metadata(provider, external_id) WHERE external_id IS NOT NULL;

-- A genre link is local (a tag) or remote (a provider's); music's were
-- read from tags, or filled by MusicBrainz only where tags gave none.
CREATE TABLE item_genre_new (
  item_id INTEGER NOT NULL REFERENCES item(id) ON DELETE CASCADE,
  genre_id INTEGER NOT NULL REFERENCES genre(id) ON DELETE CASCADE,
  source TEXT NOT NULL DEFAULT 'remote' CHECK (source IN ('local','remote')),
  PRIMARY KEY (item_id, genre_id, source)
);
INSERT INTO item_genre_new (item_id, genre_id, source)
  SELECT g.item_id, g.genre_id, CASE l.kind WHEN 'music' THEN 'local' ELSE 'remote' END
  FROM item_genre g JOIN item i ON i.id = g.item_id JOIN library l ON l.id = i.library_id;
DROP TABLE item_genre;
ALTER TABLE item_genre_new RENAME TO item_genre;
CREATE INDEX item_genre_genre ON item_genre(genre_id);

-- The settings as flags, defaults before first run: which source a field
-- is taken from first, and whether each may be used at all.
CREATE VIEW metadata_setting AS
  SELECT COALESCE(s.metadata_source, 'local') = 'local' AS local_first,
    NOT (COALESCE(s.metadata_source, 'local') = 'remote' AND COALESCE(s.metadata_mode, 'only') = 'only') AS use_local,
    NOT (COALESCE(s.metadata_source, 'local') = 'local' AND COALESCE(s.metadata_mode, 'only') = 'only') AS use_remote
  FROM (SELECT 1) LEFT JOIN server_info s ON s.id = 1;

-- What an item shows: each field from the first source the settings allow
-- that has it. poster_from and backdrop_from name the provider whose image
-- won, 'local' for a file. An item with no usable row has none.
CREATE VIEW item_shown AS
  SELECT i.id AS item_id,
    CASE WHEN z.use_remote AND r.item_id IS NOT NULL THEN r.provider ELSE '' END AS provider,
    CASE WHEN z.use_remote THEN r.provider_id END AS provider_id,
    CASE WHEN z.use_remote AND r.item_id IS NOT NULL THEN r.status ELSE 'unmatched' END AS status,
    CASE WHEN z.local_first THEN COALESCE(CASE WHEN z.use_local THEN l.title END, CASE WHEN z.use_remote THEN r.title END)
      ELSE COALESCE(CASE WHEN z.use_remote THEN r.title END, CASE WHEN z.use_local THEN l.title END) END AS title,
    CASE WHEN z.local_first THEN COALESCE(CASE WHEN z.use_local THEN l.overview END, CASE WHEN z.use_remote THEN r.overview END)
      ELSE COALESCE(CASE WHEN z.use_remote THEN r.overview END, CASE WHEN z.use_local THEN l.overview END) END AS overview,
    CASE WHEN z.local_first THEN COALESCE(CASE WHEN z.use_local THEN l.release_date END, CASE WHEN z.use_remote THEN r.release_date END)
      ELSE COALESCE(CASE WHEN z.use_remote THEN r.release_date END, CASE WHEN z.use_local THEN l.release_date END) END AS release_date,
    CASE WHEN z.local_first THEN COALESCE(CASE WHEN z.use_local THEN l.runtime_minutes END, CASE WHEN z.use_remote THEN r.runtime_minutes END)
      ELSE COALESCE(CASE WHEN z.use_remote THEN r.runtime_minutes END, CASE WHEN z.use_local THEN l.runtime_minutes END) END AS runtime_minutes,
    CASE WHEN z.local_first THEN COALESCE(CASE WHEN z.use_local THEN l.content_rating END, CASE WHEN z.use_remote THEN r.content_rating END)
      ELSE COALESCE(CASE WHEN z.use_remote THEN r.content_rating END, CASE WHEN z.use_local THEN l.content_rating END) END AS content_rating,
    CASE WHEN z.local_first THEN COALESCE(CASE WHEN z.use_local THEN l.poster_path END, CASE WHEN z.use_remote THEN r.poster_path END)
      ELSE COALESCE(CASE WHEN z.use_remote THEN r.poster_path END, CASE WHEN z.use_local THEN l.poster_path END) END AS poster_path,
    CASE WHEN z.local_first THEN COALESCE(CASE WHEN z.use_local THEN l.backdrop_path END, CASE WHEN z.use_remote THEN r.backdrop_path END)
      ELSE COALESCE(CASE WHEN z.use_remote THEN r.backdrop_path END, CASE WHEN z.use_local THEN l.backdrop_path END) END AS backdrop_path,
    CASE WHEN z.local_first THEN COALESCE(CASE WHEN z.use_local THEN l.external_id END, CASE WHEN z.use_remote THEN r.external_id END)
      ELSE COALESCE(CASE WHEN z.use_remote THEN r.external_id END, CASE WHEN z.use_local THEN l.external_id END) END AS external_id,
    CASE WHEN z.use_remote THEN r.fetched_at ELSE l.fetched_at END AS fetched_at,
    CASE WHEN z.use_remote THEN r.retry_at END AS retry_at,
    CASE WHEN z.use_remote AND r.poster_path IS NOT NULL AND NOT (z.local_first AND z.use_local AND l.poster_path IS NOT NULL)
      THEN r.provider ELSE 'local' END AS poster_from,
    CASE WHEN z.use_remote AND r.backdrop_path IS NOT NULL AND NOT (z.local_first AND z.use_local AND l.backdrop_path IS NOT NULL)
      THEN r.provider ELSE 'local' END AS backdrop_from
  FROM item i CROSS JOIN metadata_setting z
    LEFT JOIN item_metadata l ON l.item_id = i.id AND l.source = 'local'
    LEFT JOIN item_metadata r ON r.item_id = i.id AND r.source = 'remote'
  WHERE z.use_local AND l.item_id IS NOT NULL OR z.use_remote AND r.item_id IS NOT NULL;

-- The genre links an item shows: its local or its remote ones, whichever
-- the settings take first and it has.
CREATE VIEW item_genre_shown AS
  SELECT g.item_id, g.genre_id FROM item_genre g CROSS JOIN metadata_setting z
  WHERE g.source = CASE
    WHEN z.local_first AND z.use_local AND EXISTS (SELECT 1 FROM item_genre x WHERE x.item_id = g.item_id AND x.source = 'local') THEN 'local'
    WHEN NOT z.local_first AND z.use_remote AND EXISTS (SELECT 1 FROM item_genre x WHERE x.item_id = g.item_id AND x.source = 'remote') THEN 'remote'
    WHEN z.local_first AND z.use_remote THEN 'remote'
    WHEN NOT z.local_first AND z.use_local THEN 'local' END;

-- Credits and people come only from TMDB.
CREATE VIEW credit_shown AS
  SELECT c.* FROM credit c CROSS JOIN metadata_setting z WHERE z.use_remote;
