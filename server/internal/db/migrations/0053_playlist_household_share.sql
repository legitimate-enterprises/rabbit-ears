-- A playlist its owner shares with a household they are in: it reaches
-- whoever is in that household at the time, and a kid only reads.
CREATE TABLE playlist_household_share (
  playlist_id INTEGER NOT NULL REFERENCES playlist(id) ON DELETE CASCADE,
  household_id INTEGER NOT NULL REFERENCES household(id) ON DELETE CASCADE,
  access TEXT NOT NULL CHECK (access IN ('read','write')),
  created_at INTEGER NOT NULL,
  PRIMARY KEY (playlist_id, household_id)
);
CREATE INDEX playlist_household_share_household ON playlist_household_share(household_id);

-- Every share reaching a profile, directly or through a household it is
-- in; a profile reached more than one way has a row for each.
CREATE VIEW playlist_reach AS
  SELECT playlist_id, account_id, access FROM playlist_share
  UNION ALL
  SELECT s.playlist_id, m.account_id, s.access FROM playlist_household_share s JOIN membership m ON m.household_id = s.household_id;
