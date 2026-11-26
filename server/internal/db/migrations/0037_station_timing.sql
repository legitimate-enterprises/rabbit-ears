-- When a program starts after the one before ends: continuous at once,
-- half_hour at the next hour or half hour, hour at the next hour. A
-- station keeps the timing its kind had.
ALTER TABLE station ADD COLUMN timing TEXT NOT NULL DEFAULT 'continuous' CHECK (timing IN ('continuous','half_hour','hour'));
UPDATE station SET timing = CASE (SELECT kind FROM playlist WHERE playlist.id = station.playlist_id)
  WHEN 'movies' THEN 'hour' WHEN 'tv' THEN 'half_hour' ELSE 'continuous' END;
