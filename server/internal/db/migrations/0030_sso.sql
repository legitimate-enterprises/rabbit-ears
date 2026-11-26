-- An OpenID Connect provider adults sign in with. client_secret is NULL for
-- a public client. required_group, when set, must be in the ID token's
-- groups claim; links_by_email lets a first sign-in claim the one account
-- with the token's email.
CREATE TABLE sso_provider (
  id INTEGER PRIMARY KEY,
  name TEXT NOT NULL,
  issuer TEXT NOT NULL,
  client_id TEXT NOT NULL,
  client_secret TEXT,
  required_group TEXT,
  links_by_email INTEGER NOT NULL DEFAULT 0 CHECK (links_by_email IN (0,1)),
  enabled INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0,1)),
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL
);

-- An account's identity at a provider: subject is the provider's stable ID
-- for the user, email what the provider gave when it was linked.
CREATE TABLE account_identity (
  id INTEGER PRIMARY KEY,
  account_id INTEGER NOT NULL REFERENCES account(id) ON DELETE CASCADE,
  provider_id INTEGER NOT NULL REFERENCES sso_provider(id) ON DELETE CASCADE,
  subject TEXT NOT NULL,
  email TEXT,
  created_at INTEGER NOT NULL,
  UNIQUE (provider_id, subject),
  UNIQUE (account_id, provider_id)
);
