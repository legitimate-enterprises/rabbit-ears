-- A TV asking to be signed in by an adult elsewhere. status is as decided;
-- expired is derived from expires_at. Only a hash of the poll token is kept.
CREATE TABLE device_link (
  id INTEGER PRIMARY KEY,
  code TEXT NOT NULL UNIQUE,
  poll_token_hash TEXT NOT NULL UNIQUE,
  device_name TEXT NOT NULL,
  platform TEXT NOT NULL,
  source TEXT NOT NULL,
  status TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','approved','denied')),
  account_id INTEGER REFERENCES account(id) ON DELETE CASCADE,
  household_id INTEGER REFERENCES household(id) ON DELETE CASCADE,
  device_id INTEGER REFERENCES device(id) ON DELETE SET NULL,
  decided_at INTEGER,
  collected_at INTEGER,
  expires_at INTEGER NOT NULL,
  created_at INTEGER NOT NULL,
  CHECK ((status = 'pending') = (decided_at IS NULL)),
  CHECK (collected_at IS NULL OR status = 'approved')
);
CREATE INDEX device_link_household ON device_link(household_id);
CREATE INDEX device_link_source ON device_link(source, status);
