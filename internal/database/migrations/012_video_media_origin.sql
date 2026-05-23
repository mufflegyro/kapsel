ALTER TABLE videos ADD COLUMN media_origin TEXT NOT NULL DEFAULT 'imported' CHECK (media_origin IN ('imported', 'manual', 'channel_auto'));

ALTER TABLE videos ADD COLUMN media_downloaded_at TEXT NOT NULL DEFAULT '';

UPDATE videos
SET media_origin = CASE
  WHEN EXISTS (SELECT 1 FROM downloads d WHERE d.video_id = videos.id AND d.status = 'succeeded' AND d.origin = 'manual') THEN 'manual'
  WHEN EXISTS (SELECT 1 FROM downloads d WHERE d.video_id = videos.id AND d.status = 'succeeded' AND d.origin = 'channel_auto') THEN 'channel_auto'
  ELSE 'imported'
END
WHERE media_path <> '';

UPDATE videos
SET media_downloaded_at = COALESCE(
  (
    SELECT d.updated_at
    FROM downloads d
    WHERE d.video_id = videos.id
      AND d.status = 'succeeded'
    ORDER BY d.updated_at DESC, d.id DESC
    LIMIT 1
  ),
  NULLIF(archived_at, ''),
  NULLIF(updated_at, ''),
  NULLIF(created_at, ''),
  strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
)
WHERE media_path <> '';
