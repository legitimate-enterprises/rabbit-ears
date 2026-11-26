-- Paths the server made under its data folder become relative to it. Each
-- is known by the folder the server makes it in, whatever the data folder
-- above that was called; any other path, a provider's or a file in a
-- library, keeps its own. rtrim(p, replace(p, '/', '')) is p's folder.
UPDATE seek_preview SET dir = 'previews/' || media_file_id
  WHERE dir LIKE '/%' AND substr(dir, -length('/previews/' || media_file_id)) = '/previews/' || media_file_id;

UPDATE chapter SET thumbnail_path = 'thumbnails/' || media_file_id || '/' ||
    substr(thumbnail_path, length(rtrim(thumbnail_path, replace(thumbnail_path, '/', ''))) + 1)
  WHERE thumbnail_path LIKE '/%' AND substr(rtrim(thumbnail_path, replace(thumbnail_path, '/', '')),
    -length('/thumbnails/' || media_file_id || '/')) = '/thumbnails/' || media_file_id || '/';

WITH made(dir) AS (VALUES ('thumbnails/local/'), ('thumbnails/music/'), ('images/caa/'), ('cache/youtube/'))
UPDATE item_metadata SET poster_path = made.dir || substr(poster_path, length(rtrim(poster_path, replace(poster_path, '/', ''))) + 1)
  FROM made
  WHERE poster_path LIKE '/%' AND substr(rtrim(poster_path, replace(poster_path, '/', '')), -length(made.dir) - 1) = '/' || made.dir;

WITH made(dir) AS (VALUES ('thumbnails/local/'), ('thumbnails/music/'), ('images/caa/'), ('cache/youtube/'))
UPDATE item_metadata SET backdrop_path = made.dir || substr(backdrop_path, length(rtrim(backdrop_path, replace(backdrop_path, '/', ''))) + 1)
  FROM made
  WHERE backdrop_path LIKE '/%' AND substr(rtrim(backdrop_path, replace(backdrop_path, '/', '')), -length(made.dir) - 1) = '/' || made.dir;
