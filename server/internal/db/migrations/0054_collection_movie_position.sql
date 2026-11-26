-- A custom collection is hand-ordered while its movies carry positions, 0
-- to n-1 in play order; with none it keeps release order.
ALTER TABLE collection_movie ADD COLUMN position INTEGER CHECK (position >= 0);
