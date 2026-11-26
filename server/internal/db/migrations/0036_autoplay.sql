-- no_autoplay turns Up next's countdown off for a profile; 0, the default,
-- plays the next item when one ends.
ALTER TABLE account ADD COLUMN no_autoplay INTEGER NOT NULL DEFAULT 0 CHECK (no_autoplay IN (0,1));
