-- An adult's offer of a profile of their household, household_id, to an
-- adult linked with them, to_id, for to_id's own household. Only to_id
-- accepts or declines it; the adults of household_id see and withdraw it.
CREATE TABLE profile_offer (
  id INTEGER PRIMARY KEY,
  profile_id INTEGER NOT NULL REFERENCES account(id) ON DELETE CASCADE,
  household_id INTEGER NOT NULL REFERENCES household(id) ON DELETE CASCADE,
  from_id INTEGER NOT NULL REFERENCES account(id) ON DELETE CASCADE,
  to_id INTEGER NOT NULL REFERENCES account(id) ON DELETE CASCADE,
  created_at INTEGER NOT NULL,
  UNIQUE (profile_id, to_id)
);
CREATE INDEX profile_offer_household ON profile_offer(household_id);
CREATE INDEX profile_offer_from ON profile_offer(from_id);
CREATE INDEX profile_offer_to ON profile_offer(to_id);
