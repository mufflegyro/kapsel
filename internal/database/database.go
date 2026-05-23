package database

import (
	"cmp"
	"context"
	"database/sql"
	"embed"
	"fmt"
	"io/fs"
	"net/url"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	_ "modernc.org/sqlite"
)

//go:embed migrations/*.sql
var migrationFiles embed.FS

const (
	sqliteBusyTimeoutMS = 5000
	sqliteMaxOpenConns  = 4
)

func Open(ctx context.Context, path string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", sqliteDSN(path))
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(sqliteMaxOpenConns)
	db.SetMaxIdleConns(sqliteMaxOpenConns)

	if err := configure(ctx, db); err != nil {
		_ = db.Close()
		return nil, err
	}

	return db, nil
}

func Migrate(ctx context.Context, db *sql.DB) error {
	migrations, err := loadMigrations()
	if err != nil {
		return err
	}
	maxSupportedVersion := latestMigrationVersion(migrations)

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, `
CREATE TABLE IF NOT EXISTS schema_migrations (
  version INTEGER PRIMARY KEY,
  name TEXT NOT NULL,
  applied_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
)`); err != nil {
		return err
	}

	applied, err := appliedMigrations(ctx, tx)
	if err != nil {
		return err
	}
	if err := validateAppliedMigrations(applied, migrations, maxSupportedVersion); err != nil {
		return err
	}

	for _, migration := range migrations {
		if _, ok := applied[migration.version]; ok {
			continue
		}

		if _, err := tx.ExecContext(ctx, migration.sql); err != nil {
			return fmt.Errorf("apply migration %03d %s: %w", migration.version, migration.name, err)
		}

		if _, err := tx.ExecContext(
			ctx,
			"INSERT INTO schema_migrations (version, name) VALUES (?, ?)",
			migration.version,
			migration.name,
		); err != nil {
			return err
		}
	}

	return tx.Commit()
}

func validateAppliedMigrations(applied map[int]string, migrations []migration, maxSupportedVersion int) error {
	if maxAppliedVersion := latestAppliedMigrationVersion(applied); maxAppliedVersion > maxSupportedVersion {
		return fmt.Errorf(
			"database schema version %d is newer than this binary supports (%d); use a newer Kapsel binary or restore a compatible backup",
			maxAppliedVersion,
			maxSupportedVersion,
		)
	}

	supported := make(map[int]migration, len(migrations))
	for _, migration := range migrations {
		supported[migration.version] = migration
	}
	for version, name := range applied {
		migration, ok := supported[version]
		if !ok {
			return fmt.Errorf("database schema version %d is not recognized by this binary", version)
		}
		if name != migration.name {
			return fmt.Errorf("database schema version %d is recorded as %q, expected %q", version, name, migration.name)
		}
	}

	foundGap := false
	for _, migration := range migrations {
		if _, ok := applied[migration.version]; ok {
			if foundGap {
				return fmt.Errorf("database schema migration history is not contiguous before version %d", migration.version)
			}
			continue
		}
		foundGap = true
	}

	return nil
}

func sqliteDSN(path string) string {
	values := url.Values{}
	values.Add("_pragma", fmt.Sprintf("busy_timeout(%d)", sqliteBusyTimeoutMS))
	values.Add("_pragma", "foreign_keys(1)")
	values.Add("_pragma", "journal_mode(WAL)")
	values.Set("_txlock", "immediate")

	separator := "?"
	if strings.Contains(path, "?") {
		separator = "&"
	}

	return path + separator + values.Encode()
}

func configure(ctx context.Context, db *sql.DB) error {
	if _, err := db.ExecContext(ctx, "PRAGMA foreign_keys = ON"); err != nil {
		return err
	}
	if _, err := db.ExecContext(ctx, fmt.Sprintf("PRAGMA busy_timeout = %d", sqliteBusyTimeoutMS)); err != nil {
		return err
	}

	var journalMode string
	if err := db.QueryRowContext(ctx, "PRAGMA journal_mode = WAL").Scan(&journalMode); err != nil {
		return err
	}
	if !strings.EqualFold(journalMode, "wal") {
		return fmt.Errorf("expected SQLite journal_mode wal, got %q", journalMode)
	}

	return nil
}

type migration struct {
	version int
	name    string
	sql     string
}

func loadMigrations() ([]migration, error) {
	paths, err := fs.Glob(migrationFiles, "migrations/*.sql")
	if err != nil {
		return nil, err
	}

	migrations := make([]migration, 0, len(paths))
	for _, path := range paths {
		version, name, err := parseMigrationName(path)
		if err != nil {
			return nil, err
		}

		body, err := migrationFiles.ReadFile(path)
		if err != nil {
			return nil, err
		}

		migrations = append(migrations, migration{
			version: version,
			name:    name,
			sql:     string(body),
		})
	}
	slices.SortFunc(migrations, func(a, b migration) int {
		return cmp.Compare(a.version, b.version)
	})

	return migrations, nil
}

func latestMigrationVersion(migrations []migration) int {
	latest := 0
	for _, migration := range migrations {
		if migration.version > latest {
			latest = migration.version
		}
	}

	return latest
}

func SupportedSchemaVersion() (int, error) {
	migrations, err := loadMigrations()
	if err != nil {
		return 0, err
	}

	return latestMigrationVersion(migrations), nil
}

func SchemaVersion(ctx context.Context, db *sql.DB) (int, error) {
	var version int
	if err := db.QueryRowContext(ctx, "SELECT COALESCE(MAX(version), 0) FROM schema_migrations").Scan(&version); err != nil {
		return 0, err
	}

	return version, nil
}

func latestAppliedMigrationVersion(applied map[int]string) int {
	latest := 0
	for version := range applied {
		if version > latest {
			latest = version
		}
	}

	return latest
}

func parseMigrationName(path string) (int, string, error) {
	base := strings.TrimSuffix(filepath.Base(path), ".sql")
	versionText, name, ok := strings.Cut(base, "_")
	if !ok {
		return 0, "", fmt.Errorf("invalid migration name %q", path)
	}

	version, err := strconv.Atoi(versionText)
	if err != nil {
		return 0, "", fmt.Errorf("invalid migration version %q: %w", path, err)
	}

	return version, name, nil
}

func appliedMigrations(ctx context.Context, tx *sql.Tx) (map[int]string, error) {
	rows, err := tx.QueryContext(ctx, "SELECT version, name FROM schema_migrations")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	applied := map[int]string{}
	for rows.Next() {
		var version int
		var name string
		if err := rows.Scan(&version, &name); err != nil {
			return nil, err
		}
		applied[version] = name
	}

	return applied, rows.Err()
}
