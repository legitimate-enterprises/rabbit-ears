-- A device signed in through a device link. It never carries the admin
-- scope: linked devices are TVs, and the admin UI is web only.
ALTER TABLE device ADD COLUMN linked INTEGER NOT NULL DEFAULT 0 CHECK (linked IN (0,1));
