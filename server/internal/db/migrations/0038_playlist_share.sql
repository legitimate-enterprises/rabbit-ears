-- A playlist its owner shares with another profile: any adult on the
-- server, or a kid of the owner's household, who only reads.
CREATE TABLE playlist_share (
  playlist_id INTEGER NOT NULL REFERENCES playlist(id) ON DELETE CASCADE,
  account_id INTEGER NOT NULL REFERENCES account(id) ON DELETE CASCADE,
  access TEXT NOT NULL CHECK (access IN ('read','write')),
  created_at INTEGER NOT NULL,
  PRIMARY KEY (playlist_id, account_id)
);
CREATE INDEX playlist_share_account ON playlist_share(account_id);
