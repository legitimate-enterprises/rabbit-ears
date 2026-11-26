-- What a title's names and tags stated when its match was made from them
-- or last found to agree with them; NULL for a match made by hand or
-- before this was recorded, which is only ever checked, never replaced.
ALTER TABLE item_metadata ADD COLUMN identity_stamp TEXT;

-- The MusicBrainz release and release group a song's tags name. A row
-- naming neither says the tags were read and name none.
CREATE TABLE song_release (
  item_id INTEGER PRIMARY KEY REFERENCES item(id) ON DELETE CASCADE,
  release_id TEXT,
  release_group_id TEXT
);

-- A title whose identity an admin must look at, or a file no title could
-- be made for (path, without item_id). A scan raises one, keeps it while
-- its reason holds and deletes it once it does not; dismissing keeps it
-- from being listed as open while it holds.
CREATE TABLE identity_review (
  id INTEGER PRIMARY KEY,
  library_id INTEGER NOT NULL REFERENCES library(id) ON DELETE CASCADE,
  item_id INTEGER REFERENCES item(id) ON DELETE CASCADE,
  path TEXT,
  reason TEXT NOT NULL CHECK (reason IN ('conflicting_identity','invalid_identity','tied_candidates','conflicting_years',
    'ambiguous_move','no_match','unrecognized')),
  raised_at INTEGER NOT NULL,
  dismissed_at INTEGER,
  CHECK ((item_id IS NULL) != (path IS NULL))
);
CREATE UNIQUE INDEX identity_review_item ON identity_review(item_id, reason) WHERE item_id IS NOT NULL;
CREATE UNIQUE INDEX identity_review_path ON identity_review(library_id, path, reason) WHERE item_id IS NULL;
CREATE INDEX identity_review_library ON identity_review(library_id, raised_at);

-- What a review rests on, in the order to show it: the match the title
-- has, what its names and tags state, and the candidates it could be.
CREATE TABLE identity_evidence (
  id INTEGER PRIMARY KEY,
  review_id INTEGER NOT NULL REFERENCES identity_review(id) ON DELETE CASCADE,
  position INTEGER NOT NULL,
  role TEXT NOT NULL CHECK (role IN ('current','stated','candidate')),
  provider TEXT CHECK (provider IN ('tmdb','tvdb','imdb','musicbrainz')),
  external_id TEXT,
  title TEXT,
  year INTEGER,
  country TEXT,
  path TEXT,
  UNIQUE (review_id, position)
);
