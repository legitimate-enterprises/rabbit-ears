-- Clients fetch each image at an address that names it, which the data
-- they sync carries, so no library-wide generation is kept.

DROP TRIGGER IF EXISTS collection_art_insert;
DROP TRIGGER IF EXISTS collection_art_update;
DROP TRIGGER IF EXISTS item_metadata_art_delete;
DROP TRIGGER IF EXISTS item_metadata_art_insert;
DROP TRIGGER IF EXISTS item_metadata_art_update;
DROP TRIGGER IF EXISTS metadata_setting_art_update;
DROP TRIGGER IF EXISTS person_art_insert;
DROP TRIGGER IF EXISTS person_art_update;
DROP TABLE IF EXISTS artwork_generation;
