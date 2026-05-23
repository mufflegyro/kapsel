CREATE TABLE sponsorblock_cache (
  video_id TEXT PRIMARY KEY REFERENCES videos(id) ON DELETE CASCADE,
  source TEXT NOT NULL DEFAULT 'youtube',
  external_id TEXT NOT NULL DEFAULT '',
  fetched_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);

CREATE TABLE sponsorblock_segments (
  id INTEGER PRIMARY KEY,
  video_id TEXT NOT NULL REFERENCES videos(id) ON DELETE CASCADE,
  start_seconds REAL NOT NULL CHECK (start_seconds >= 0),
  end_seconds REAL NOT NULL CHECK (end_seconds > start_seconds),
  created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
  UNIQUE (video_id, start_seconds, end_seconds)
);

CREATE INDEX idx_sponsorblock_segments_video ON sponsorblock_segments(video_id, start_seconds);
