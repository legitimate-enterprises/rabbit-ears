-- The device Move here took a session to; set only on a session that ended
-- that way.
ALTER TABLE playback_session ADD COLUMN moved_to_device_id INTEGER REFERENCES device(id) ON DELETE SET NULL;

-- One profile may be both members of a Watch together, on two devices, so a
-- member is known by its device.
CREATE TABLE watch_together_member_new (
  watch_together_id INTEGER NOT NULL REFERENCES watch_together(id) ON DELETE CASCADE,
  account_id INTEGER NOT NULL REFERENCES account(id) ON DELETE CASCADE,
  device_id INTEGER NOT NULL REFERENCES device(id) ON DELETE CASCADE,
  role TEXT NOT NULL CHECK (role IN ('host','guest')),
  joined_at INTEGER NOT NULL,
  last_seen_at INTEGER NOT NULL,
  PRIMARY KEY (watch_together_id, device_id),
  UNIQUE (watch_together_id, role)
);
INSERT INTO watch_together_member_new (watch_together_id, account_id, device_id, role, joined_at, last_seen_at)
  SELECT watch_together_id, account_id, device_id, role, joined_at, last_seen_at FROM watch_together_member;
DROP TABLE watch_together_member;
ALTER TABLE watch_together_member_new RENAME TO watch_together_member;
CREATE INDEX watch_together_member_account ON watch_together_member(account_id);
