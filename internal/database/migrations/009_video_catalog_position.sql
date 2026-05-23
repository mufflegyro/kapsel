ALTER TABLE videos ADD COLUMN catalog_position INTEGER NOT NULL DEFAULT -1 CHECK (catalog_position >= -1);
