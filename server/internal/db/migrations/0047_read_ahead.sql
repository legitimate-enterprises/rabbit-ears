-- read_ahead_seconds is how far past the newest segment a player fetched a
-- stream is converted before its encoder pauses, and the forward buffer
-- players are asked to hold.
ALTER TABLE server_info ADD COLUMN read_ahead_seconds INTEGER NOT NULL DEFAULT 300 CHECK (read_ahead_seconds BETWEEN 30 AND 600);
