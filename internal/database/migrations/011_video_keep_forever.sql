ALTER TABLE videos ADD COLUMN keep_forever INTEGER NOT NULL DEFAULT 0 CHECK (keep_forever IN (0, 1));
