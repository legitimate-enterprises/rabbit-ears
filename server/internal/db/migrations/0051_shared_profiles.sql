-- The households a profile is in beyond account.household_id. A kid stands
-- equally in each; an adult is only present there and manages nothing.
CREATE TABLE household_member (
  household_id INTEGER NOT NULL REFERENCES household(id) ON DELETE CASCADE,
  account_id INTEGER NOT NULL REFERENCES account(id) ON DELETE CASCADE,
  created_at INTEGER NOT NULL,
  PRIMARY KEY (household_id, account_id)
);
CREATE INDEX household_member_account ON household_member(account_id);

-- Every household each profile is in.
CREATE VIEW membership AS
  SELECT id AS account_id, household_id FROM account WHERE household_id IS NOT NULL
  UNION
  SELECT account_id, household_id FROM household_member;

-- Two adults of different households whom an admin lets see each other, on
-- TVs only when tv is set. Stored once, lower id first.
CREATE TABLE adult_link (
  a_id INTEGER NOT NULL REFERENCES account(id) ON DELETE CASCADE,
  b_id INTEGER NOT NULL REFERENCES account(id) ON DELETE CASCADE,
  tv INTEGER NOT NULL DEFAULT 0 CHECK (tv IN (0,1)),
  created_at INTEGER NOT NULL,
  PRIMARY KEY (a_id, b_id),
  CHECK (a_id < b_id)
);
CREATE INDEX adult_link_b ON adult_link(b_id);
