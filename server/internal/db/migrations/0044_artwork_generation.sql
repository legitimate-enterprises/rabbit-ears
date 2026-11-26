-- Clients keep an image for as long as the generation they fetched it under
-- is the server's. It advances when any title, collection or person gains,
-- changes or loses an image, or the metadata settings change which image
-- shows, so a trigger on each table catches every writer.
-- Clients are told `settled`, which follows `generation` only once changes
-- have stopped for a while, so a scan does not move them at every title.
CREATE TABLE artwork_generation (
  id INTEGER PRIMARY KEY CHECK (id = 1),
  generation INTEGER NOT NULL,
  settled INTEGER NOT NULL,
  changed_at INTEGER NOT NULL
);
INSERT INTO artwork_generation (id, generation, settled, changed_at) VALUES (1, 1, 1, 0);

CREATE TRIGGER item_metadata_art_insert AFTER INSERT ON item_metadata
WHEN COALESCE(NEW.poster_path, '') <> '' OR COALESCE(NEW.backdrop_path, '') <> ''
BEGIN
  UPDATE artwork_generation SET generation = generation + 1, changed_at = CAST(strftime('%s', 'now') AS INTEGER) * 1000;
END;

CREATE TRIGGER item_metadata_art_update AFTER UPDATE OF poster_path, backdrop_path ON item_metadata
WHEN COALESCE(NEW.poster_path, '') <> COALESCE(OLD.poster_path, '') OR COALESCE(NEW.backdrop_path, '') <> COALESCE(OLD.backdrop_path, '')
BEGIN
  UPDATE artwork_generation SET generation = generation + 1, changed_at = CAST(strftime('%s', 'now') AS INTEGER) * 1000;
END;

CREATE TRIGGER item_metadata_art_delete AFTER DELETE ON item_metadata
WHEN COALESCE(OLD.poster_path, '') <> '' OR COALESCE(OLD.backdrop_path, '') <> ''
BEGIN
  UPDATE artwork_generation SET generation = generation + 1, changed_at = CAST(strftime('%s', 'now') AS INTEGER) * 1000;
END;

CREATE TRIGGER collection_art_insert AFTER INSERT ON collection
WHEN COALESCE(NEW.poster_path, '') <> '' OR COALESCE(NEW.backdrop_path, '') <> ''
BEGIN
  UPDATE artwork_generation SET generation = generation + 1, changed_at = CAST(strftime('%s', 'now') AS INTEGER) * 1000;
END;

CREATE TRIGGER collection_art_update AFTER UPDATE OF poster_path, backdrop_path ON collection
WHEN COALESCE(NEW.poster_path, '') <> COALESCE(OLD.poster_path, '') OR COALESCE(NEW.backdrop_path, '') <> COALESCE(OLD.backdrop_path, '')
BEGIN
  UPDATE artwork_generation SET generation = generation + 1, changed_at = CAST(strftime('%s', 'now') AS INTEGER) * 1000;
END;

CREATE TRIGGER person_art_insert AFTER INSERT ON person
WHEN COALESCE(NEW.profile_path, '') <> ''
BEGIN
  UPDATE artwork_generation SET generation = generation + 1, changed_at = CAST(strftime('%s', 'now') AS INTEGER) * 1000;
END;

CREATE TRIGGER person_art_update AFTER UPDATE OF profile_path ON person
WHEN COALESCE(NEW.profile_path, '') <> COALESCE(OLD.profile_path, '')
BEGIN
  UPDATE artwork_generation SET generation = generation + 1, changed_at = CAST(strftime('%s', 'now') AS INTEGER) * 1000;
END;

CREATE TRIGGER metadata_setting_art_update AFTER UPDATE OF metadata_source, metadata_mode ON server_info
WHEN NEW.metadata_source IS NOT OLD.metadata_source OR NEW.metadata_mode IS NOT OLD.metadata_mode
BEGIN
  UPDATE artwork_generation SET generation = generation + 1, changed_at = CAST(strftime('%s', 'now') AS INTEGER) * 1000;
END;
