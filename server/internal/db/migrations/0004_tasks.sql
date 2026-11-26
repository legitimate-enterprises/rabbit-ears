CREATE TABLE task (
  id INTEGER PRIMARY KEY,
  name TEXT NOT NULL UNIQUE,
  kind TEXT NOT NULL,
  interval_seconds INTEGER CHECK (interval_seconds IS NULL OR interval_seconds > 0),
  enabled INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0,1)),
  next_run_at INTEGER,
  created_at INTEGER NOT NULL
);

CREATE TABLE task_run (
  id INTEGER PRIMARY KEY,
  task_id INTEGER NOT NULL REFERENCES task(id) ON DELETE CASCADE,
  status TEXT NOT NULL CHECK (status IN ('running','succeeded','failed')),
  progress REAL NOT NULL DEFAULT 0 CHECK (progress BETWEEN 0 AND 1),
  error TEXT,
  started_at INTEGER NOT NULL,
  finished_at INTEGER
);
CREATE INDEX task_run_task ON task_run(task_id, started_at);
CREATE INDEX task_run_status ON task_run(status);
