CREATE TABLE channels (
  id TEXT PRIMARY KEY,
  source TEXT NOT NULL DEFAULT 'youtube',
  external_id TEXT NOT NULL,
  name TEXT NOT NULL,
  handle TEXT NOT NULL DEFAULT '',
  description TEXT NOT NULL DEFAULT '',
  subscribed INTEGER NOT NULL DEFAULT 0 CHECK (subscribed IN (0, 1)),
  last_scanned_at TEXT,
  created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
  updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
  UNIQUE (source, external_id)
);

CREATE TABLE videos (
  id TEXT PRIMARY KEY,
  source TEXT NOT NULL DEFAULT 'youtube',
  external_id TEXT NOT NULL,
  channel_id TEXT REFERENCES channels(id) ON DELETE SET NULL,
  title TEXT NOT NULL,
  description TEXT NOT NULL DEFAULT '',
  published_at TEXT,
  duration_seconds INTEGER NOT NULL DEFAULT 0 CHECK (duration_seconds >= 0),
  media_path TEXT NOT NULL DEFAULT '',
  thumbnail_path TEXT NOT NULL DEFAULT '',
  watched INTEGER NOT NULL DEFAULT 0 CHECK (watched IN (0, 1)),
  archived_at TEXT,
  created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
  updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
  UNIQUE (source, external_id)
);

CREATE INDEX idx_videos_channel_id ON videos(channel_id);
CREATE INDEX idx_videos_published_at ON videos(published_at DESC, id);

CREATE TABLE playlists (
  id TEXT PRIMARY KEY,
  source TEXT NOT NULL DEFAULT 'youtube',
  external_id TEXT NOT NULL,
  channel_id TEXT REFERENCES channels(id) ON DELETE SET NULL,
  title TEXT NOT NULL,
  description TEXT NOT NULL DEFAULT '',
  subscribed INTEGER NOT NULL DEFAULT 0 CHECK (subscribed IN (0, 1)),
  created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
  updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
  UNIQUE (source, external_id)
);

CREATE TABLE playlist_entries (
  playlist_id TEXT NOT NULL REFERENCES playlists(id) ON DELETE CASCADE,
  video_id TEXT NOT NULL REFERENCES videos(id) ON DELETE CASCADE,
  position INTEGER NOT NULL CHECK (position >= 0),
  created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
  PRIMARY KEY (playlist_id, video_id),
  UNIQUE (playlist_id, position)
);

CREATE INDEX idx_playlist_entries_playlist_position ON playlist_entries(playlist_id, position);

CREATE TABLE downloads (
  id INTEGER PRIMARY KEY,
  video_id TEXT REFERENCES videos(id) ON DELETE SET NULL,
  source TEXT NOT NULL DEFAULT 'youtube',
  external_id TEXT NOT NULL DEFAULT '',
  url TEXT NOT NULL,
  status TEXT NOT NULL,
  priority INTEGER NOT NULL DEFAULT 0,
  error TEXT NOT NULL DEFAULT '',
  payload_json TEXT NOT NULL DEFAULT '{}',
  created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
  updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);

CREATE INDEX idx_downloads_status_priority ON downloads(status, priority DESC, created_at);

CREATE TABLE user_progress (
  video_id TEXT PRIMARY KEY REFERENCES videos(id) ON DELETE CASCADE,
  position_seconds INTEGER NOT NULL DEFAULT 0 CHECK (position_seconds >= 0),
  duration_seconds INTEGER NOT NULL DEFAULT 0 CHECK (duration_seconds >= 0),
  watched INTEGER NOT NULL DEFAULT 0 CHECK (watched IN (0, 1)),
  updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);

CREATE INDEX idx_user_progress_video_id ON user_progress(video_id);

CREATE TABLE jobs (
  id TEXT PRIMARY KEY,
  type TEXT NOT NULL,
  payload_json TEXT NOT NULL DEFAULT '{}',
  status TEXT NOT NULL,
  priority INTEGER NOT NULL DEFAULT 0,
  attempts INTEGER NOT NULL DEFAULT 0 CHECK (attempts >= 0),
  max_attempts INTEGER NOT NULL DEFAULT 3 CHECK (max_attempts >= 1),
  progress REAL NOT NULL DEFAULT 0 CHECK (progress >= 0 AND progress <= 1),
  error TEXT NOT NULL DEFAULT '',
  run_after TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
  locked_at TEXT,
  cancel_requested INTEGER NOT NULL DEFAULT 0 CHECK (cancel_requested IN (0, 1)),
  created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
  updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
  completed_at TEXT
);

CREATE INDEX idx_jobs_status_priority ON jobs(status, priority DESC, run_after, created_at);

CREATE TABLE settings (
  key TEXT PRIMARY KEY,
  value TEXT NOT NULL,
  updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);

CREATE TABLE media_assets (
  id INTEGER PRIMARY KEY,
  owner_type TEXT NOT NULL,
  owner_id TEXT NOT NULL,
  kind TEXT NOT NULL,
  path TEXT NOT NULL UNIQUE,
  mime_type TEXT NOT NULL DEFAULT '',
  byte_size INTEGER NOT NULL DEFAULT 0 CHECK (byte_size >= 0),
  width INTEGER NOT NULL DEFAULT 0 CHECK (width >= 0),
  height INTEGER NOT NULL DEFAULT 0 CHECK (height >= 0),
  created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
  UNIQUE (owner_type, owner_id, kind)
);

CREATE INDEX idx_media_assets_owner ON media_assets(owner_type, owner_id, kind);

CREATE TABLE subtitles (
  id INTEGER PRIMARY KEY,
  video_id TEXT NOT NULL REFERENCES videos(id) ON DELETE CASCADE,
  language TEXT NOT NULL,
  name TEXT NOT NULL DEFAULT '',
  source TEXT NOT NULL DEFAULT '',
  format TEXT NOT NULL DEFAULT '',
  path TEXT NOT NULL DEFAULT '',
  text TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
  UNIQUE (video_id, language, source)
);

CREATE INDEX idx_subtitles_video_language ON subtitles(video_id, language);

CREATE TABLE comments (
  id TEXT PRIMARY KEY,
  video_id TEXT NOT NULL REFERENCES videos(id) ON DELETE CASCADE,
  parent_id TEXT REFERENCES comments(id) ON DELETE CASCADE,
  author TEXT NOT NULL DEFAULT '',
  text TEXT NOT NULL DEFAULT '',
  published_at TEXT,
  like_count INTEGER NOT NULL DEFAULT 0 CHECK (like_count >= 0),
  created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);

CREATE INDEX idx_comments_video_parent ON comments(video_id, parent_id);

CREATE TABLE search_documents (
  owner_type TEXT NOT NULL,
  owner_id TEXT NOT NULL,
  field TEXT NOT NULL,
  text TEXT NOT NULL,
  updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
  PRIMARY KEY (owner_type, owner_id, field)
);

CREATE INDEX idx_search_documents_owner ON search_documents(owner_type, owner_id);
