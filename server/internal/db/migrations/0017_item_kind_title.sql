-- Show progress and title listings page one kind by title.
CREATE INDEX item_kind_title ON item(kind, parsed_title COLLATE NOCASE, id);
