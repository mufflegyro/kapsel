ALTER TABLE downloads ADD COLUMN origin TEXT NOT NULL DEFAULT 'manual' CHECK (origin IN ('manual', 'channel_auto'));
