-- Backfill per-video channel search docs: owner_type 'video', field 'channel',
-- text "<channel name> <video title>". The importer and downloader write these
-- going forward; this migration covers existing archives. Videos without a
-- resolvable channel are skipped, matching the sync helpers.
INSERT INTO search_documents (owner_type, owner_id, field, text)
SELECT 'video', v.id, 'channel', c.name || ' ' || v.title
FROM videos v
JOIN channels c ON c.id = v.channel_id
WHERE TRIM(c.name) <> ''
ON CONFLICT(owner_type, owner_id, field) DO UPDATE SET text = excluded.text
