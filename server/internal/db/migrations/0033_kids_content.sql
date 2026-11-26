-- kids_content is, for a movie or show, an adult's override of whether it
-- is kids' content, NULL leaving it to the rating; for a YouTube channel,
-- whether it is for kids, NULL being not. explicit is a song's advisory
-- tag, NULL until a probe has read it. hide_kids_content keeps an adult's
-- browsing free of kids' content.
ALTER TABLE item ADD COLUMN kids_content INTEGER CHECK (kids_content IN (0,1));
ALTER TABLE item ADD COLUMN explicit INTEGER CHECK (explicit IN (0,1));
ALTER TABLE account ADD COLUMN hide_kids_content INTEGER NOT NULL DEFAULT 0 CHECK (hide_kids_content IN (0,1));
