-- A device's claim, for one of its profiles, on one title's file to fetch:
-- the file itself, or a conversion while it is made and fetched.
CREATE TABLE download (
  id INTEGER PRIMARY KEY,
  device_id INTEGER NOT NULL REFERENCES device(id) ON DELETE CASCADE,
  account_id INTEGER NOT NULL REFERENCES account(id) ON DELETE CASCADE,
  item_id INTEGER NOT NULL REFERENCES item(id) ON DELETE CASCADE,
  edition_id INTEGER REFERENCES edition(id) ON DELETE CASCADE,
  media_file_id INTEGER NOT NULL REFERENCES media_file(id) ON DELETE CASCADE,
  conversion_id INTEGER REFERENCES conversion(id) ON DELETE CASCADE,
  quality TEXT NOT NULL,
  created_at INTEGER NOT NULL
);
CREATE INDEX download_device ON download(device_id, account_id);
CREATE INDEX download_conversion ON download(conversion_id);
