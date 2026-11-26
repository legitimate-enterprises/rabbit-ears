-- When the device was locked at Who's watching; NULL once a profile is
-- unlocked on it.
ALTER TABLE device ADD COLUMN locked_at INTEGER;
