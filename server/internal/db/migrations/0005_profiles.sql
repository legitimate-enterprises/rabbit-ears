-- One row, written by first run.
CREATE TABLE server_info (
  id INTEGER PRIMARY KEY CHECK (id = 1),
  name TEXT NOT NULL,
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL
);

-- The profile a device currently acts as; NULL means its adult.
ALTER TABLE device ADD COLUMN profile_id INTEGER REFERENCES account(id) ON DELETE SET NULL;
