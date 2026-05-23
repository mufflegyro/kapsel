ALTER TABLE videos ADD COLUMN view_count INTEGER NOT NULL DEFAULT 0 CHECK (view_count >= 0);

CREATE INDEX idx_videos_view_count ON videos(view_count DESC, id);
