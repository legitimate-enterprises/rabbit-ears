-- A profile's palette color; NULL shows the one its id picks. gravatar
-- shows adults' Gravatar pictures.
ALTER TABLE account ADD COLUMN color TEXT;
ALTER TABLE server_info ADD COLUMN gravatar INTEGER NOT NULL DEFAULT 0 CHECK (gravatar IN (0,1));
