-- A song's sessions are its plays: Popular counts them across profiles, and
-- Recently played and an album's last play read one profile's.
CREATE INDEX playback_session_item ON playback_session(item_id, account_id, started_at);
