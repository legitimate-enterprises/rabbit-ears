-- How an occasion's three colors are washed behind its screens, each in
-- percent: the glows' strength, how far each color is brightened toward
-- white, how much of each glow's radius is at full strength, and the
-- glows' radius. The defaults here are the only ones; a create without
-- them takes these.

ALTER TABLE occasion ADD COLUMN wash_strength INTEGER NOT NULL DEFAULT 200 CHECK (wash_strength BETWEEN 100 AND 400);
ALTER TABLE occasion ADD COLUMN wash_brightness INTEGER NOT NULL DEFAULT 15 CHECK (wash_brightness BETWEEN 0 AND 60);
ALTER TABLE occasion ADD COLUMN wash_sharpness INTEGER NOT NULL DEFAULT 0 CHECK (wash_sharpness BETWEEN 0 AND 70);
ALTER TABLE occasion ADD COLUMN wash_reach INTEGER NOT NULL DEFAULT 100 CHECK (wash_reach BETWEEN 60 AND 160);
