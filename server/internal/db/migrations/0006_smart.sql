-- Smart lists: a playlist or custom collection whose titles its rules
-- choose. The rules match `match` of their groups, a group `match` of its
-- conditions. The server writes the matching titles into playlist_entry or
-- collection_movie, so everything that reads those reads a smart list as it
-- reads any other. smart_list is synced; the rules are not.

CREATE TABLE smart_list (
  id BLOB NOT NULL PRIMARY KEY,
  playlist_id BLOB UNIQUE REFERENCES playlist(id) ON DELETE CASCADE,
  collection_id BLOB UNIQUE REFERENCES collection(id) ON DELETE CASCADE,
  match TEXT NOT NULL CHECK (match IN ('all','any')),
  created_at INTEGER NOT NULL,
  modified_at INTEGER NOT NULL,
  created_by BLOB REFERENCES account(id) ON DELETE SET NULL,
  modified_by BLOB REFERENCES account(id) ON DELETE SET NULL,
  sync_version INTEGER NOT NULL DEFAULT 0,
  CHECK ((playlist_id IS NULL) != (collection_id IS NULL))
);
CREATE INDEX smart_list_sync ON smart_list(sync_version);

CREATE TABLE smart_group (
  id BLOB NOT NULL PRIMARY KEY,
  list_id BLOB NOT NULL REFERENCES smart_list(id) ON DELETE CASCADE,
  position INTEGER NOT NULL CHECK (position >= 0),
  match TEXT NOT NULL CHECK (match IN ('all','any')),
  created_at INTEGER NOT NULL,
  modified_at INTEGER NOT NULL,
  UNIQUE (list_id, position)
);

-- field is checked against the server's catalog of fields, so a new field
-- needs no rebuild of the table.
CREATE TABLE smart_condition (
  id BLOB NOT NULL PRIMARY KEY,
  group_id BLOB NOT NULL REFERENCES smart_group(id) ON DELETE CASCADE,
  position INTEGER NOT NULL CHECK (position >= 0),
  field TEXT NOT NULL,
  operator TEXT NOT NULL CHECK (operator IN ('is','isNot','contains','notContains','startsWith','endsWith',
    'lt','le','gt','ge','isTrue','isFalse')),
  text_value TEXT,
  number_value INTEGER,
  created_at INTEGER NOT NULL,
  modified_at INTEGER NOT NULL,
  CHECK (CASE WHEN operator IN ('isTrue','isFalse') THEN text_value IS NULL AND number_value IS NULL
    WHEN operator IN ('contains','notContains','startsWith','endsWith') THEN text_value IS NOT NULL AND number_value IS NULL
    WHEN operator IN ('lt','le','gt','ge') THEN number_value IS NOT NULL AND text_value IS NULL
    ELSE (text_value IS NULL) != (number_value IS NULL) END),
  UNIQUE (group_id, position)
);

-- A write to anything a rule reads makes the Smart lists task due, at the
-- epoch so it is due under any clock; the task applies every list's rules
-- again. The WHEN clauses skip writes that change nothing a rule reads,
-- such as a scan rewriting an item as it was, or a sync_version stamp.
CREATE TRIGGER smart_item_insert AFTER INSERT ON item
BEGIN UPDATE task SET next_run_at = 0 WHERE kind = 'smart' AND next_run_at IS NOT 0; END;
CREATE TRIGGER smart_item_delete AFTER DELETE ON item
BEGIN UPDATE task SET next_run_at = 0 WHERE kind = 'smart' AND next_run_at IS NOT 0; END;
CREATE TRIGGER smart_item_update AFTER UPDATE ON item
WHEN OLD.parent_id IS NOT NEW.parent_id OR OLD.kind IS NOT NEW.kind OR OLD.parsed_title IS NOT NEW.parsed_title
  OR OLD.parsed_year IS NOT NEW.parsed_year OR OLD.season_number IS NOT NEW.season_number
  OR OLD.episode_number IS NOT NEW.episode_number OR OLD.disc_number IS NOT NEW.disc_number
  OR OLD.track_number IS NOT NEW.track_number OR OLD.artist_id IS NOT NEW.artist_id OR OLD.sort_title IS NOT NEW.sort_title
  OR OLD.explicit IS NOT NEW.explicit
BEGIN UPDATE task SET next_run_at = 0 WHERE kind = 'smart' AND next_run_at IS NOT 0; END;

CREATE TRIGGER smart_metadata_insert AFTER INSERT ON item_metadata
BEGIN UPDATE task SET next_run_at = 0 WHERE kind = 'smart' AND next_run_at IS NOT 0; END;
CREATE TRIGGER smart_metadata_delete AFTER DELETE ON item_metadata
BEGIN UPDATE task SET next_run_at = 0 WHERE kind = 'smart' AND next_run_at IS NOT 0; END;
CREATE TRIGGER smart_metadata_update AFTER UPDATE ON item_metadata
WHEN OLD.title IS NOT NEW.title OR OLD.release_date IS NOT NEW.release_date OR OLD.runtime_minutes IS NOT NEW.runtime_minutes
  OR OLD.content_rating IS NOT NEW.content_rating
BEGIN UPDATE task SET next_run_at = 0 WHERE kind = 'smart' AND next_run_at IS NOT 0; END;

CREATE TRIGGER smart_item_genre_insert AFTER INSERT ON item_genre
BEGIN UPDATE task SET next_run_at = 0 WHERE kind = 'smart' AND next_run_at IS NOT 0; END;
CREATE TRIGGER smart_item_genre_delete AFTER DELETE ON item_genre
BEGIN UPDATE task SET next_run_at = 0 WHERE kind = 'smart' AND next_run_at IS NOT 0; END;
CREATE TRIGGER smart_item_genre_update AFTER UPDATE ON item_genre
WHEN OLD.item_id IS NOT NEW.item_id OR OLD.genre_id IS NOT NEW.genre_id OR OLD.source IS NOT NEW.source
BEGIN UPDATE task SET next_run_at = 0 WHERE kind = 'smart' AND next_run_at IS NOT 0; END;
CREATE TRIGGER smart_genre_update AFTER UPDATE ON genre WHEN OLD.name IS NOT NEW.name
BEGIN UPDATE task SET next_run_at = 0 WHERE kind = 'smart' AND next_run_at IS NOT 0; END;

CREATE TRIGGER smart_credit_insert AFTER INSERT ON credit
BEGIN UPDATE task SET next_run_at = 0 WHERE kind = 'smart' AND next_run_at IS NOT 0; END;
CREATE TRIGGER smart_credit_delete AFTER DELETE ON credit
BEGIN UPDATE task SET next_run_at = 0 WHERE kind = 'smart' AND next_run_at IS NOT 0; END;
CREATE TRIGGER smart_credit_update AFTER UPDATE ON credit
WHEN OLD.item_id IS NOT NEW.item_id OR OLD.person_id IS NOT NEW.person_id OR OLD.kind IS NOT NEW.kind OR OLD.job IS NOT NEW.job
BEGIN UPDATE task SET next_run_at = 0 WHERE kind = 'smart' AND next_run_at IS NOT 0; END;
CREATE TRIGGER smart_person_update AFTER UPDATE ON person WHEN OLD.name IS NOT NEW.name
BEGIN UPDATE task SET next_run_at = 0 WHERE kind = 'smart' AND next_run_at IS NOT 0; END;

CREATE TRIGGER smart_edition_insert AFTER INSERT ON edition
BEGIN UPDATE task SET next_run_at = 0 WHERE kind = 'smart' AND next_run_at IS NOT 0; END;
CREATE TRIGGER smart_edition_delete AFTER DELETE ON edition
BEGIN UPDATE task SET next_run_at = 0 WHERE kind = 'smart' AND next_run_at IS NOT 0; END;
CREATE TRIGGER smart_edition_update AFTER UPDATE ON edition
WHEN OLD.item_id IS NOT NEW.item_id OR OLD.runtime_ms IS NOT NEW.runtime_ms
BEGIN UPDATE task SET next_run_at = 0 WHERE kind = 'smart' AND next_run_at IS NOT 0; END;
CREATE TRIGGER smart_edition_file_insert AFTER INSERT ON edition_file
BEGIN UPDATE task SET next_run_at = 0 WHERE kind = 'smart' AND next_run_at IS NOT 0; END;
CREATE TRIGGER smart_edition_file_delete AFTER DELETE ON edition_file
BEGIN UPDATE task SET next_run_at = 0 WHERE kind = 'smart' AND next_run_at IS NOT 0; END;
CREATE TRIGGER smart_media_file_update AFTER UPDATE ON media_file
WHEN (OLD.missing_at IS NULL) IS NOT (NEW.missing_at IS NULL) OR OLD.duration_ms IS NOT NEW.duration_ms
BEGIN UPDATE task SET next_run_at = 0 WHERE kind = 'smart' AND next_run_at IS NOT 0; END;
CREATE TRIGGER smart_track_insert AFTER INSERT ON track
BEGIN UPDATE task SET next_run_at = 0 WHERE kind = 'smart' AND next_run_at IS NOT 0; END;
CREATE TRIGGER smart_track_delete AFTER DELETE ON track
BEGIN UPDATE task SET next_run_at = 0 WHERE kind = 'smart' AND next_run_at IS NOT 0; END;
CREATE TRIGGER smart_track_update AFTER UPDATE ON track
WHEN OLD.kind IS NOT NEW.kind OR OLD.codec IS NOT NEW.codec OR OLD.width IS NOT NEW.width OR OLD.height IS NOT NEW.height
  OR OLD.hdr_format IS NOT NEW.hdr_format
BEGIN UPDATE task SET next_run_at = 0 WHERE kind = 'smart' AND next_run_at IS NOT 0; END;

CREATE TRIGGER smart_server_info_update AFTER UPDATE ON server_info
WHEN OLD.metadata_source IS NOT NEW.metadata_source OR OLD.metadata_mode IS NOT NEW.metadata_mode
BEGIN UPDATE task SET next_run_at = 0 WHERE kind = 'smart' AND next_run_at IS NOT 0; END;

CREATE TRIGGER smart_watch_insert AFTER INSERT ON watch_state
BEGIN UPDATE task SET next_run_at = 0 WHERE kind = 'smart' AND next_run_at IS NOT 0; END;
CREATE TRIGGER smart_watch_delete AFTER DELETE ON watch_state
BEGIN UPDATE task SET next_run_at = 0 WHERE kind = 'smart' AND next_run_at IS NOT 0; END;
CREATE TRIGGER smart_watch_update AFTER UPDATE ON watch_state
WHEN OLD.watched IS NOT NEW.watched OR OLD.play_count IS NOT NEW.play_count
BEGIN UPDATE task SET next_run_at = 0 WHERE kind = 'smart' AND next_run_at IS NOT 0; END;
CREATE TRIGGER smart_favorite_insert AFTER INSERT ON favorite
BEGIN UPDATE task SET next_run_at = 0 WHERE kind = 'smart' AND next_run_at IS NOT 0; END;
CREATE TRIGGER smart_favorite_delete AFTER DELETE ON favorite
BEGIN UPDATE task SET next_run_at = 0 WHERE kind = 'smart' AND next_run_at IS NOT 0; END;
CREATE TRIGGER smart_playlist_owner AFTER UPDATE ON playlist WHEN OLD.account_id IS NOT NEW.account_id
BEGIN UPDATE task SET next_run_at = 0 WHERE kind = 'smart' AND next_run_at IS NOT 0; END;
