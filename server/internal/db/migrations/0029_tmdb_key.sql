-- The TMDB API key saved in Settings; NULL falls back to the environment.
ALTER TABLE server_info ADD COLUMN tmdb_key TEXT;
