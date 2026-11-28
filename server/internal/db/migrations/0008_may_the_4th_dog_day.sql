-- May the 4th and National Dog Day join the built-in occasions, with fixed
-- ids after Christmas's and Halloween's. A server whose admin already named
-- an occasion either way keeps that one and gets no built-in of the name.

INSERT OR IGNORE INTO occasion (id, name, holiday, days_before, days_after, created_at, modified_at) VALUES
  (x'00000000000070009000000000000003', 'May the 4th', 'may-the-4th', 14, 1, 0, 0),
  (x'00000000000070009000000000000004', 'National Dog Day', 'national-dog-day', 14, 1, 0, 0);
INSERT INTO occasion_keyword (occasion_id, keyword_id)
  SELECT id, 371623 FROM occasion WHERE id = x'00000000000070009000000000000003' AND holiday = 'may-the-4th'
  UNION ALL SELECT id, 15162 FROM occasion WHERE id = x'00000000000070009000000000000004' AND holiday = 'national-dog-day';
INSERT INTO occasion_seed (occasion_id)
  SELECT id FROM occasion WHERE id = x'00000000000070009000000000000003' AND holiday = 'may-the-4th'
  UNION ALL SELECT id FROM occasion WHERE id = x'00000000000070009000000000000004' AND holiday = 'national-dog-day';
