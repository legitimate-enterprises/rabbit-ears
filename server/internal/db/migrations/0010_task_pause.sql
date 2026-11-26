-- Pause all on the Tasks page: while set, scheduled and file-change runs wait.
ALTER TABLE server_info ADD COLUMN tasks_paused INTEGER NOT NULL DEFAULT 0 CHECK (tasks_paused IN (0,1));

-- Run now: an explicit request, honored even while tasks are paused.
ALTER TABLE task ADD COLUMN run_requested_at INTEGER;
