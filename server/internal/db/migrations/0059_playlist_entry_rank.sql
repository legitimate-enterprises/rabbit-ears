-- rank orders a playlist's entries: a fractional-indexing key (playlist
-- rank.go), compared bytewise, ties by id. A move writes only the moved
-- entry. Existing entries take the keys a list built by appending gets,
-- a0 to az, b00 to bzz, c000 and on, in their old order.
DROP INDEX playlist_entry_order;
ALTER TABLE playlist_entry ADD COLUMN rank TEXT NOT NULL DEFAULT '';
-- m counts from the first key of n's integer length: 62 keys of one digit,
-- 3844 of two, 238328 of three.
WITH o AS (
  SELECT id, n, CASE WHEN n < 62 THEN n WHEN n < 3906 THEN n - 62 WHEN n < 242234 THEN n - 3906 ELSE n - 242234 END AS m,
    '0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz' AS d
  FROM (SELECT id, ROW_NUMBER() OVER (PARTITION BY playlist_id ORDER BY position, id) - 1 AS n FROM playlist_entry)
)
UPDATE playlist_entry SET rank = CASE
    WHEN n < 62 THEN 'a' || substr(d, m + 1, 1)
    WHEN n < 3906 THEN 'b' || substr(d, m / 62 + 1, 1) || substr(d, m % 62 + 1, 1)
    WHEN n < 242234 THEN 'c' || substr(d, m / 3844 + 1, 1) || substr(d, m / 62 % 62 + 1, 1) || substr(d, m % 62 + 1, 1)
    ELSE 'd' || substr(d, m / 238328 + 1, 1) || substr(d, m / 3844 % 62 + 1, 1) || substr(d, m / 62 % 62 + 1, 1) || substr(d, m % 62 + 1, 1)
  END
  FROM o WHERE o.id = playlist_entry.id;
ALTER TABLE playlist_entry DROP COLUMN position;
CREATE INDEX playlist_entry_order ON playlist_entry(playlist_id, rank, id);
