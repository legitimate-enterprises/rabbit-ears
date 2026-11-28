-- What a client app adds to the log_entry it reported, whose source is
-- 'client'. The entry outlives the device and profile it came from, which
-- are then no longer named.
CREATE TABLE client_log_entry (
  log_entry_id BLOB NOT NULL PRIMARY KEY REFERENCES log_entry(id) ON DELETE CASCADE,
  platform TEXT NOT NULL,
  app_version TEXT NOT NULL,
  os_version TEXT NOT NULL,
  context TEXT NOT NULL,
  detail TEXT,
  device_id BLOB REFERENCES device(id) ON DELETE SET NULL,
  profile_id BLOB REFERENCES account(id) ON DELETE SET NULL
);
CREATE INDEX client_log_entry_device ON client_log_entry(device_id);
CREATE INDEX client_log_entry_profile ON client_log_entry(profile_id);
