-- Two adults of one household watching one title in sync. The host waits
-- until join_by for a guest. The shared position is position_ms as of
-- position_at, advancing from then while playing.
CREATE TABLE watch_together (
  id INTEGER PRIMARY KEY,
  household_id INTEGER NOT NULL REFERENCES household(id) ON DELETE CASCADE,
  item_id INTEGER NOT NULL REFERENCES item(id) ON DELETE CASCADE,
  playing INTEGER NOT NULL DEFAULT 0 CHECK (playing IN (0,1)),
  position_ms INTEGER NOT NULL DEFAULT 0 CHECK (position_ms >= 0),
  position_at INTEGER NOT NULL,
  paused_by INTEGER REFERENCES account(id) ON DELETE SET NULL,
  version INTEGER NOT NULL DEFAULT 1,
  opened_at INTEGER NOT NULL,
  join_by INTEGER NOT NULL,
  ended_at INTEGER,
  updated_at INTEGER NOT NULL
);
CREATE INDEX watch_together_open ON watch_together(household_id, item_id) WHERE ended_at IS NULL;

-- One host and at most one guest; last_seen_at is the member's latest poll.
CREATE TABLE watch_together_member (
  watch_together_id INTEGER NOT NULL REFERENCES watch_together(id) ON DELETE CASCADE,
  account_id INTEGER NOT NULL REFERENCES account(id) ON DELETE CASCADE,
  device_id INTEGER NOT NULL REFERENCES device(id) ON DELETE CASCADE,
  role TEXT NOT NULL CHECK (role IN ('host','guest')),
  joined_at INTEGER NOT NULL,
  last_seen_at INTEGER NOT NULL,
  PRIMARY KEY (watch_together_id, account_id),
  UNIQUE (watch_together_id, role)
);
CREATE INDEX watch_together_member_account ON watch_together_member(account_id);
