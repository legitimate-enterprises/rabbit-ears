-- A playlist a station owns: listed nowhere, and deleted with the station.
ALTER TABLE playlist ADD COLUMN station_owned INTEGER NOT NULL DEFAULT 0 CHECK (station_owned IN (0,1));
