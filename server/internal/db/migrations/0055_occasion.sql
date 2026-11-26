-- The TMDB keywords of a movie or show, as its last remote fetch gave them.
CREATE TABLE item_keyword (
  item_id INTEGER NOT NULL REFERENCES item(id) ON DELETE CASCADE,
  keyword_id INTEGER NOT NULL,
  PRIMARY KEY (item_id, keyword_id)
);
CREATE INDEX item_keyword_keyword ON item_keyword(keyword_id);

-- Keywords come only from TMDB.
CREATE VIEW item_keyword_shown AS
  SELECT k.* FROM item_keyword k CROSS JOIN metadata_setting z WHERE z.use_remote;

-- A seasonal label whose movies and shows surface around an anchor: a
-- holiday of the table built into the server, or a month and day.
CREATE TABLE occasion (
  id INTEGER PRIMARY KEY,
  name TEXT NOT NULL UNIQUE COLLATE NOCASE,
  holiday TEXT,
  month INTEGER CHECK (month BETWEEN 1 AND 12),
  day INTEGER CHECK (day BETWEEN 1 AND 31),
  days_before INTEGER NOT NULL CHECK (days_before >= 0),
  days_after INTEGER NOT NULL CHECK (days_after >= 0),
  CHECK ((holiday IS NULL) = (month IS NOT NULL) AND (month IS NULL) = (day IS NULL))
);

-- Members are the movies and shows carrying any of an occasion's keywords
-- or added by hand, less those removed by hand.
CREATE TABLE occasion_keyword (
  occasion_id INTEGER NOT NULL REFERENCES occasion(id) ON DELETE CASCADE,
  keyword_id INTEGER NOT NULL,
  PRIMARY KEY (occasion_id, keyword_id)
);

CREATE TABLE occasion_item (
  occasion_id INTEGER NOT NULL REFERENCES occasion(id) ON DELETE CASCADE,
  item_id INTEGER NOT NULL REFERENCES item(id) ON DELETE CASCADE,
  PRIMARY KEY (occasion_id, item_id)
);
CREATE INDEX occasion_item_item ON occasion_item(item_id);

CREATE TABLE occasion_exclusion (
  occasion_id INTEGER NOT NULL REFERENCES occasion(id) ON DELETE CASCADE,
  item_id INTEGER NOT NULL REFERENCES item(id) ON DELETE CASCADE,
  PRIMARY KEY (occasion_id, item_id)
);
CREATE INDEX occasion_exclusion_item ON occasion_exclusion(item_id);

INSERT INTO occasion (name, holiday, days_before, days_after) VALUES ('Christmas', 'christmas', 30, 5), ('Halloween', 'halloween', 30, 1);
INSERT INTO occasion_keyword (occasion_id, keyword_id)
  SELECT id, 207317 FROM occasion WHERE name = 'Christmas'
  UNION ALL SELECT id, 3335 FROM occasion WHERE name = 'Halloween';
