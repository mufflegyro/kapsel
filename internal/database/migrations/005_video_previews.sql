CREATE TABLE video_previews (
  video_id TEXT PRIMARY KEY REFERENCES videos(id) ON DELETE CASCADE,
  sprite_path TEXT NOT NULL UNIQUE,
  interval_seconds INTEGER NOT NULL CHECK (interval_seconds > 0),
  frame_width INTEGER NOT NULL CHECK (frame_width > 0),
  frame_height INTEGER NOT NULL CHECK (frame_height > 0),
  columns INTEGER NOT NULL CHECK (columns > 0),
  preview_count INTEGER NOT NULL CHECK (preview_count > 0),
  created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
  updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);

CREATE INDEX idx_video_previews_sprite_path ON video_previews(sprite_path);
