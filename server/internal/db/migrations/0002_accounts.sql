CREATE TABLE household (
  id INTEGER PRIMARY KEY,
  name TEXT NOT NULL,
  adult_id INTEGER NOT NULL UNIQUE REFERENCES account(id) ON DELETE CASCADE,
  created_at INTEGER NOT NULL
);

-- Pending adults have no password hash and no household until they accept.
CREATE TABLE account (
  id INTEGER PRIMARY KEY,
  kind TEXT NOT NULL CHECK (kind IN ('adult','kid')),
  email TEXT COLLATE NOCASE UNIQUE,
  name TEXT NOT NULL,
  is_admin INTEGER NOT NULL DEFAULT 0 CHECK (is_admin IN (0,1)),
  household_id INTEGER REFERENCES household(id) ON DELETE CASCADE,
  password_hash TEXT,
  birthday TEXT,
  pin_hash TEXT,
  pin_failures INTEGER NOT NULL DEFAULT 0,
  pin_locked_until INTEGER,
  filter_mode TEXT NOT NULL DEFAULT 'ratings' CHECK (filter_mode IN ('ratings','allow_list')),
  movie_ceiling TEXT,
  tv_ceiling TEXT,
  revoked_at INTEGER,
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL,
  CHECK (kind = 'adult' OR household_id IS NOT NULL),
  CHECK (kind = 'adult' OR (is_admin = 0 AND email IS NULL AND password_hash IS NULL)),
  CHECK (kind = 'kid' OR (movie_ceiling IS NULL AND tv_ceiling IS NULL))
);
CREATE INDEX account_household ON account(household_id);

CREATE TABLE account_allow (
  account_id INTEGER NOT NULL REFERENCES account(id) ON DELETE CASCADE,
  item_id INTEGER NOT NULL REFERENCES item(id) ON DELETE CASCADE,
  PRIMARY KEY (account_id, item_id)
);

CREATE TABLE device (
  id INTEGER PRIMARY KEY,
  token_hash TEXT NOT NULL UNIQUE,
  name TEXT NOT NULL,
  platform TEXT NOT NULL,
  account_id INTEGER NOT NULL REFERENCES account(id) ON DELETE CASCADE,
  household_id INTEGER NOT NULL REFERENCES household(id) ON DELETE CASCADE,
  last_seen_at INTEGER,
  revoked_at INTEGER,
  created_at INTEGER NOT NULL
);
CREATE INDEX device_account ON device(account_id);
CREATE INDEX device_household ON device(household_id);

CREATE TABLE join_code (
  id INTEGER PRIMARY KEY,
  code TEXT NOT NULL UNIQUE,
  account_id INTEGER NOT NULL REFERENCES account(id) ON DELETE CASCADE,
  expires_at INTEGER NOT NULL,
  used_at INTEGER,
  revoked_at INTEGER,
  created_at INTEGER NOT NULL
);
CREATE INDEX join_code_account ON join_code(account_id);
