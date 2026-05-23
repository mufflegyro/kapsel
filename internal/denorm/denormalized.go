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
