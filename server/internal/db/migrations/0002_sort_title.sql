-- The sort title of a movie or show is the title it shows (item_shown's,
-- else the parsed title) less one leading English article, "the", "a" or
-- "an" in any case, followed by whitespace and more text. item_sort is the
-- one definition; the triggers keep item.sort_title equal to it through
-- every change to what it reads. Music sort titles are the scanner's.

CREATE VIEW IF NOT EXISTS item_sort AS
  SELECT item_id, CASE
      WHEN lower(substr(t, 1, 3)) = 'the' AND length(the) BETWEEN 1 AND length(t) - 4 THEN the
      WHEN lower(substr(t, 1, 2)) = 'an' AND length(an) BETWEEN 1 AND length(t) - 3 THEN an
      WHEN lower(substr(t, 1, 1)) = 'a' AND length(a) BETWEEN 1 AND length(t) - 2 THEN a
      ELSE t END AS sort_title
  FROM (SELECT item_id, t, ltrim(substr(t, 4), char(32, 9, 10, 11, 12, 13)) AS the,
      ltrim(substr(t, 3), char(32, 9, 10, 11, 12, 13)) AS an, ltrim(substr(t, 2), char(32, 9, 10, 11, 12, 13)) AS a
    FROM (SELECT i.id AS item_id, COALESCE(NULLIF((SELECT s.title FROM item_shown s WHERE s.item_id = i.id), ''), i.parsed_title) AS t
      FROM item i WHERE i.kind IN ('movie','show')));

UPDATE item SET sort_title = (SELECT s.sort_title FROM item_sort s WHERE s.item_id = item.id)
WHERE kind IN ('movie','show') AND sort_title IS NOT (SELECT s.sort_title FROM item_sort s WHERE s.item_id = item.id);

CREATE TRIGGER IF NOT EXISTS item_sort_insert AFTER INSERT ON item WHEN NEW.kind IN ('movie','show')
BEGIN
  UPDATE item SET sort_title = (SELECT s.sort_title FROM item_sort s WHERE s.item_id = NEW.id) WHERE id = NEW.id;
END;

CREATE TRIGGER IF NOT EXISTS item_sort_update AFTER UPDATE OF parsed_title, sort_title ON item WHEN NEW.kind IN ('movie','show')
BEGIN
  UPDATE item SET sort_title = (SELECT s.sort_title FROM item_sort s WHERE s.item_id = NEW.id)
  WHERE id = NEW.id AND sort_title IS NOT (SELECT s.sort_title FROM item_sort s WHERE s.item_id = NEW.id);
END;

CREATE TRIGGER IF NOT EXISTS item_sort_metadata_insert AFTER INSERT ON item_metadata
BEGIN
  UPDATE item SET sort_title = (SELECT s.sort_title FROM item_sort s WHERE s.item_id = NEW.item_id)
  WHERE id = NEW.item_id AND kind IN ('movie','show') AND sort_title IS NOT (SELECT s.sort_title FROM item_sort s WHERE s.item_id = NEW.item_id);
END;

CREATE TRIGGER IF NOT EXISTS item_sort_metadata_update AFTER UPDATE OF title ON item_metadata
BEGIN
  UPDATE item SET sort_title = (SELECT s.sort_title FROM item_sort s WHERE s.item_id = NEW.item_id)
  WHERE id = NEW.item_id AND kind IN ('movie','show') AND sort_title IS NOT (SELECT s.sort_title FROM item_sort s WHERE s.item_id = NEW.item_id);
END;

CREATE TRIGGER IF NOT EXISTS item_sort_metadata_delete AFTER DELETE ON item_metadata
BEGIN
  UPDATE item SET sort_title = (SELECT s.sort_title FROM item_sort s WHERE s.item_id = OLD.item_id)
  WHERE id = OLD.item_id AND kind IN ('movie','show') AND sort_title IS NOT (SELECT s.sort_title FROM item_sort s WHERE s.item_id = OLD.item_id);
END;

CREATE TRIGGER IF NOT EXISTS item_sort_setting_update AFTER UPDATE OF metadata_source, metadata_mode ON server_info
WHEN NEW.metadata_source IS NOT OLD.metadata_source OR NEW.metadata_mode IS NOT OLD.metadata_mode
BEGIN
  UPDATE item SET sort_title = (SELECT s.sort_title FROM item_sort s WHERE s.item_id = item.id)
  WHERE kind IN ('movie','show') AND sort_title IS NOT (SELECT s.sort_title FROM item_sort s WHERE s.item_id = item.id);
END;
