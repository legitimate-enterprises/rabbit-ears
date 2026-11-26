-- Phrases that title an occasion's Home row in place of its name, one at
-- random per load. Rows keep the order they were written in.
CREATE TABLE occasion_phrase (
  occasion_id INTEGER NOT NULL REFERENCES occasion(id) ON DELETE CASCADE,
  phrase TEXT NOT NULL COLLATE NOCASE CHECK (length(phrase) BETWEEN 1 AND 60),
  PRIMARY KEY (occasion_id, phrase)
);

-- The three #RRGGBB colors of the wash behind an occasion's screen, all
-- or none.
ALTER TABLE occasion ADD COLUMN color1 TEXT;
ALTER TABLE occasion ADD COLUMN color2 TEXT;
ALTER TABLE occasion ADD COLUMN color3 TEXT;

-- Holiday occasions made before phrases and colors, which the server gives
-- their holiday's built-ins once, at its next start.
CREATE TABLE occasion_seed (
  occasion_id INTEGER PRIMARY KEY REFERENCES occasion(id) ON DELETE CASCADE
);
INSERT INTO occasion_seed (occasion_id) SELECT id FROM occasion WHERE holiday IS NOT NULL;
