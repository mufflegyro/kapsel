package denorm

import (
	"context"
	"database/sql"
	"strings"
)

type Runner interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

func SyncSearchDocument(ctx context.Context, db Runner, ownerType string, ownerID string, field string, text string) error {
	if strings.TrimSpace(text) == "" {
		return DeleteSearchDocument(ctx, db, ownerType, ownerID, field)
	}

	_, err := db.ExecContext(ctx, `
INSERT INTO search_documents (owner_type, owner_id, field, text)
VALUES (?, ?, ?, ?)
ON CONFLICT(owner_type, owner_id, field) DO UPDATE SET
  text = excluded.text,
  updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')`, ownerType, ownerID, field, text)

	return err
}

func DeleteSearchDocument(ctx context.Context, db Runner, ownerType string, ownerID string, field string) error {
	_, err := db.ExecContext(ctx, "DELETE FROM search_documents WHERE owner_type = ? AND owner_id = ? AND field = ?", ownerType, ownerID, field)

	return err
}

func DeleteSearchDocumentsForOwner(ctx context.Context, db Runner, ownerType string, ownerID string) error {
	_, err := db.ExecContext(ctx, "DELETE FROM search_documents WHERE owner_type = ? AND owner_id = ?", ownerType, ownerID)

	return err
}

// SyncVideoChannelSearchDocument writes the per-video channel search doc:
// owner_type 'video', field 'channel', text "<channel name> <video title>".
// Combining both strings lets AND-semantics multiword queries that pair a
// channel with a topic ("adam stew island") match a single document. An empty
// channel name deletes the doc, mirroring SyncSearchDocument.
func SyncVideoChannelSearchDocument(ctx context.Context, db Runner, videoID string, channelName string, videoTitle string) error {
	if strings.TrimSpace(channelName) == "" {
		return DeleteSearchDocument(ctx, db, "video", videoID, "channel")
	}

	_, err := db.ExecContext(ctx, `
INSERT INTO search_documents (owner_type, owner_id, field, text)
VALUES ('video', ?, 'channel', ? || ' ' || ?)
ON CONFLICT(owner_type, owner_id, field) DO UPDATE SET text = excluded.text`, videoID, channelName, videoTitle)

	return err
}

// SyncChannelVideoSearchDocuments writes (or refreshes) the per-video channel
// search doc for every video belonging to a channel — the rename/backfill
// path for SyncVideoChannelSearchDocument. Videos whose doc already holds the
// expected text are left untouched, so calling this on every channel upsert
// stays cheap even during per-video import loops.
func SyncChannelVideoSearchDocuments(ctx context.Context, db Runner, channelID string, channelName string) error {
	if strings.TrimSpace(channelName) == "" {
		return nil
	}

	_, err := db.ExecContext(ctx, `
INSERT INTO search_documents (owner_type, owner_id, field, text)
SELECT 'video', v.id, 'channel', ? || ' ' || v.title
FROM videos v
WHERE v.channel_id = ?
  AND (
    SELECT d.text
    FROM search_documents d
    WHERE d.owner_type = 'video' AND d.owner_id = v.id AND d.field = 'channel'
  ) IS NOT ? || ' ' || v.title
ON CONFLICT(owner_type, owner_id, field) DO UPDATE SET text = excluded.text`, channelName, channelID, channelName)

	return err
}

func SyncMediaAsset(ctx context.Context, db Runner, ownerType string, ownerID string, kind string, path string) error {
	if strings.TrimSpace(path) == "" {
		return DeleteMediaAsset(ctx, db, ownerType, ownerID, kind)
	}

	_, err := db.ExecContext(ctx, `
INSERT INTO media_assets (owner_type, owner_id, kind, path)
VALUES (?, ?, ?, ?)
ON CONFLICT(owner_type, owner_id, kind) DO UPDATE SET path = excluded.path`, ownerType, ownerID, kind, path)

	return err
}

func DeleteMediaAsset(ctx context.Context, db Runner, ownerType string, ownerID string, kind string) error {
	_, err := db.ExecContext(ctx, "DELETE FROM media_assets WHERE owner_type = ? AND owner_id = ? AND kind = ?", ownerType, ownerID, kind)

	return err
}

func DeleteMediaAssetsForOwner(ctx context.Context, db Runner, ownerType string, ownerID string) error {
	_, err := db.ExecContext(ctx, "DELETE FROM media_assets WHERE owner_type = ? AND owner_id = ?", ownerType, ownerID)

	return err
}
