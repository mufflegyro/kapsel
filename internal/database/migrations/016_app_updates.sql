CREATE TABLE app_updates (
  id INTEGER PRIMARY KEY,
  version TEXT NOT NULL UNIQUE,
  release_url TEXT NOT NULL DEFAULT '',
  release_notes TEXT NOT NULL DEFAULT '',
  published_at TEXT NOT NULL DEFAULT '',
  discovered_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
  updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
  status TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'approved', 'applied', 'dismissed', 'failed')),
  approved_by TEXT NOT NULL DEFAULT '',
  approved_at TEXT,
  applied_at TEXT,
  error TEXT NOT NULL DEFAULT ''
);

CREATE INDEX idx_app_updates_status ON app_updates(status, discovered_at DESC);
