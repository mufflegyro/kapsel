package backup

import (
	"archive/zip"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"kapsel/internal/applock"
	"kapsel/internal/config"
	"kapsel/internal/database"
	"kapsel/internal/jobs"
)

const (
	formatVersion          = 1
	maxMetadataBytes       = 1 << 20
	maxBackupDatabaseBytes = 64 << 30
)

var (
	ErrActiveJobs         = errors.New("restore blocked by active jobs")
	ErrDatabaseInUse      = applock.ErrLocked
	ErrIncompatibleBackup = errors.New("backup is not compatible with this Kapsel version")
)

type Metadata struct {
	FormatVersion         int            `json:"format_version"`
	CreatedAt             string         `json:"created_at"`
	SchemaVersion         int            `json:"schema_version"`
	RestoredSchemaVersion int            `json:"restored_schema_version,omitempty"`
	Config                ConfigMetadata `json:"config"`
}

type ConfigMetadata struct {
	AuthMode               string `json:"auth_mode"`
	AuthUsername           string `json:"auth_username,omitempty"`
	AuthPasswordConfigured bool   `json:"auth_password_configured"`
	DataDir                string `json:"data_dir"`
	DBPath                 string `json:"db_path"`
	ImportRoot             string `json:"import_root"`
	MediaRoot              string `json:"media_root"`
	PreviewsEnabled        bool   `json:"previews_enabled"`
	FFMPEGPath             string `json:"ffmpeg_path"`
	YTDLPPath              string `json:"ytdlp_path"`
}

type RestoreOptions struct {
	Force bool
}

func Create(ctx context.Context, cfg config.Config, outputPath string) (Metadata, error) {
	if strings.TrimSpace(outputPath) == "" {
		return Metadata{}, errors.New("backup output path is required")
	}
	if backupTargetsDatabase(outputPath, cfg.DBPath) {
		return Metadata{}, errors.New("backup output path must not target the configured database or SQLite sidecar files")
	}
	db, err := database.Open(ctx, cfg.DBPath)
	if err != nil {
		return Metadata{}, err
	}
	defer db.Close()
	if err := database.Migrate(ctx, db); err != nil {
		return Metadata{}, err
	}
	version, err := database.SchemaVersion(ctx, db)
	if err != nil {
		return Metadata{}, err
	}

	tmpDir, err := os.MkdirTemp("", "kapsel-backup-*")
	if err != nil {
		return Metadata{}, err
	}
	defer os.RemoveAll(tmpDir)
	snapshotPath := filepath.Join(tmpDir, "kapsel.db")
	if _, err := db.ExecContext(ctx, "VACUUM main INTO "+sqliteString(snapshotPath)); err != nil {
		return Metadata{}, err
	}

	metadata := Metadata{FormatVersion: formatVersion, CreatedAt: time.Now().UTC().Format(time.RFC3339Nano), SchemaVersion: version, Config: configMetadata(cfg)}
	if err := os.MkdirAll(filepath.Dir(outputPath), 0o755); err != nil {
		return Metadata{}, err
	}
	tmpFile, err := safeTempFile(filepath.Dir(outputPath), filepath.Base(outputPath)+".*.tmp", cfg.DBPath)
	if err != nil {
		return Metadata{}, err
	}
	tmpOutput := tmpFile.Name()
	if err := writeBackupZip(tmpFile, metadata, snapshotPath); err != nil {
		_ = os.Remove(tmpOutput)
		return Metadata{}, err
	}
	if err := os.Rename(tmpOutput, outputPath); err != nil {
		_ = os.Remove(tmpOutput)
		return Metadata{}, err
	}
	if err := syncDir(filepath.Dir(outputPath)); err != nil {
		return Metadata{}, err
	}

	return metadata, nil
}

func Restore(ctx context.Context, cfg config.Config, backupPath string, options RestoreOptions) (Metadata, error) {
	if strings.TrimSpace(backupPath) == "" {
		return Metadata{}, errors.New("backup path is required")
	}
	lock, err := applock.Acquire(cfg.DBPath + ".lock")
	if err != nil {
		return Metadata{}, err
	}
	defer lock.Close()
	if !options.Force {
		active, err := hasActiveJobs(ctx, cfg.DBPath)
		if err != nil {
			return Metadata{}, err
		}
		if active {
			return Metadata{}, ErrActiveJobs
		}
	}

	tmpDir, err := os.MkdirTemp("", "kapsel-restore-*")
	if err != nil {
		return Metadata{}, err
	}
	defer os.RemoveAll(tmpDir)
	metadata, dbPath, err := extractBackup(backupPath, tmpDir)
	if err != nil {
		return Metadata{}, err
	}
	if err := validateMetadata(metadata); err != nil {
		return Metadata{}, err
	}
	restoredSchemaVersion, err := validateDatabase(ctx, dbPath)
	if err != nil {
		return Metadata{}, err
	}
	metadata.RestoredSchemaVersion = restoredSchemaVersion
	if err := os.MkdirAll(filepath.Dir(cfg.DBPath), 0o755); err != nil {
		return Metadata{}, err
	}
	if err := removePathIfExists(cfg.DBPath + ".restore-tmp"); err != nil {
		return Metadata{}, err
	}
	tmpFile, err := safeTempFile(filepath.Dir(cfg.DBPath), filepath.Base(cfg.DBPath)+".restore-*.tmp", cfg.DBPath)
	if err != nil {
		return Metadata{}, err
	}
	tmpTarget := tmpFile.Name()
	if err := copyFile(tmpFile, dbPath); err != nil {
		_ = os.Remove(tmpTarget)
		return Metadata{}, err
	}
	if err := os.Rename(tmpTarget, cfg.DBPath); err != nil {
		_ = os.Remove(tmpTarget)
		return Metadata{}, err
	}
	if err := removeSQLiteSidecars(cfg.DBPath); err != nil {
		return Metadata{}, err
	}
	if err := syncDir(filepath.Dir(cfg.DBPath)); err != nil {
		return Metadata{}, err
	}

	return metadata, nil
}

func validateMetadata(metadata Metadata) error {
	if metadata.FormatVersion != formatVersion || metadata.SchemaVersion <= 0 {
		return ErrIncompatibleBackup
	}
	supported, err := database.SupportedSchemaVersion()
	if err != nil {
		return err
	}
	if metadata.SchemaVersion > supported {
		return ErrIncompatibleBackup
	}

	return nil
}

func validateDatabase(ctx context.Context, path string) (int, error) {
	db, err := database.Open(ctx, path)
	if err != nil {
		return 0, err
	}
	defer db.Close()
	if err := database.Migrate(ctx, db); err != nil {
		return 0, err
	}
	if err := integrityCheck(ctx, db); err != nil {
		return 0, err
	}
	version, err := database.SchemaVersion(ctx, db)
	if err != nil {
		return 0, err
	}
	if _, err := db.ExecContext(ctx, "PRAGMA wal_checkpoint(TRUNCATE)"); err != nil {
		return 0, err
	}

	return version, nil
}

func integrityCheck(ctx context.Context, db *sql.DB) error {
	var result string
	if err := db.QueryRowContext(ctx, "PRAGMA integrity_check").Scan(&result); err != nil {
		return err
	}
	if result != "ok" {
		return fmt.Errorf("SQLite integrity check failed: %s", result)
	}
	rows, err := db.QueryContext(ctx, "PRAGMA foreign_key_check")
	if err != nil {
		return err
	}
	defer rows.Close()
	if rows.Next() {
		var table string
		var rowID sql.NullInt64
		var parent string
		var foreignKeyID int
		if err := rows.Scan(&table, &rowID, &parent, &foreignKeyID); err != nil {
			return err
		}

		return fmt.Errorf("SQLite foreign key check failed: table=%s rowid=%v parent=%s fkid=%d", table, rowID, parent, foreignKeyID)
	}

	return rows.Err()
}

func hasActiveJobs(ctx context.Context, dbPath string) (bool, error) {
	if _, err := os.Stat(dbPath); errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	db, err := database.Open(ctx, dbPath)
	if err != nil {
		return false, err
	}
	defer db.Close()
	var count int
	if err := db.QueryRowContext(ctx, "SELECT count(*) FROM jobs WHERE status IN (?, ?)", jobs.StatusQueued, jobs.StatusRunning).Scan(&count); err != nil {
		return false, err
	}

	return count > 0, nil
}

func extractBackup(path string, dir string) (Metadata, string, error) {
	reader, err := zip.OpenReader(path)
	if err != nil {
		return Metadata{}, "", err
	}
	defer reader.Close()

	var metadata Metadata
	var foundMetadata bool
	var dbPath string
	for _, file := range reader.File {
		switch file.Name {
		case "metadata.json":
			body, err := readZipFile(file, maxMetadataBytes)
			if err != nil {
				return Metadata{}, "", err
			}
			if err := json.Unmarshal(body, &metadata); err != nil {
				return Metadata{}, "", err
			}
			foundMetadata = true
		case "kapsel.db":
			dbPath = filepath.Join(dir, "kapsel.db")
			if err := extractZipFile(file, dbPath, maxBackupDatabaseBytes); err != nil {
				return Metadata{}, "", err
			}
		}
	}
	if !foundMetadata || dbPath == "" {
		return Metadata{}, "", ErrIncompatibleBackup
	}

	return metadata, dbPath, nil
}

func writeBackupZip(file *os.File, metadata Metadata, dbPath string) error {
	zipFile := zip.NewWriter(file)

	metadataBody, err := json.MarshalIndent(metadata, "", "  ")
	if err != nil {
		_ = file.Close()
		return err
	}
	entry, err := zipFile.Create("metadata.json")
	if err != nil {
		_ = zipFile.Close()
		_ = file.Close()
		return err
	}
	if _, err := entry.Write(metadataBody); err != nil {
		_ = zipFile.Close()
		_ = file.Close()
		return err
	}
	dbEntry, err := zipFile.Create("kapsel.db")
	if err != nil {
		_ = zipFile.Close()
		_ = file.Close()
		return err
	}
	db, err := os.Open(dbPath)
	if err != nil {
		_ = zipFile.Close()
		_ = file.Close()
		return err
	}
	_, copyErr := io.Copy(dbEntry, db)
	dbCloseErr := db.Close()
	if copyErr != nil {
		_ = zipFile.Close()
		_ = file.Close()
		return copyErr
	}
	if dbCloseErr != nil {
		_ = zipFile.Close()
		_ = file.Close()
		return dbCloseErr
	}
	if err := zipFile.Close(); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return err
	}

	return file.Close()
}

func readZipFile(file *zip.File, maxBytes int64) ([]byte, error) {
	reader, err := file.Open()
	if err != nil {
		return nil, err
	}
	defer reader.Close()

	return readLimited(reader, maxBytes)
}

func extractZipFile(file *zip.File, path string, maxBytes int64) error {
	if file.UncompressedSize64 > uint64(maxBytes) {
		return fmt.Errorf("backup database exceeds %d bytes", maxBytes)
	}
	reader, err := file.Open()
	if err != nil {
		return err
	}
	defer reader.Close()
	out, err := os.Create(path)
	if err != nil {
		return err
	}
	_, copyErr := copyLimited(out, reader, maxBytes)
	syncErr := out.Sync()
	closeErr := out.Close()
	if copyErr != nil {
		return copyErr
	}
	if syncErr != nil {
		return syncErr
	}

	return closeErr
}

func copyFile(out *os.File, source string) error {
	in, err := os.Open(source)
	if err != nil {
		_ = out.Close()
		return err
	}
	defer in.Close()
	_, copyErr := io.Copy(out, in)
	syncErr := out.Sync()
	closeErr := out.Close()
	if copyErr != nil {
		return copyErr
	}
	if syncErr != nil {
		return syncErr
	}

	return closeErr
}

func readLimited(reader io.Reader, maxBytes int64) ([]byte, error) {
	limited := &io.LimitedReader{R: reader, N: maxBytes + 1}
	body, err := io.ReadAll(limited)
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > maxBytes {
		return nil, fmt.Errorf("zip entry exceeds %d bytes", maxBytes)
	}

	return body, nil
}

func copyLimited(writer io.Writer, reader io.Reader, maxBytes int64) (int64, error) {
	limited := &io.LimitedReader{R: reader, N: maxBytes + 1}
	written, err := io.Copy(writer, limited)
	if err != nil {
		return written, err
	}
	if written > maxBytes {
		return written, fmt.Errorf("zip entry exceeds %d bytes", maxBytes)
	}

	return written, nil
}

func syncDir(path string) error {
	dir, err := os.Open(path)
	if err != nil {
		return err
	}
	defer dir.Close()
	if err := dir.Sync(); err != nil && !errors.Is(err, syscall.EINVAL) && !errors.Is(err, syscall.ENOTSUP) {
		return err
	}

	return nil
}

func removeSQLiteSidecars(path string) error {
	for _, suffix := range []string{"-wal", "-shm"} {
		if err := os.Remove(path + suffix); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}

	return nil
}

func removePathIfExists(path string) error {
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}

	return nil
}

func safeTempFile(dir string, pattern string, dbPath string) (*os.File, error) {
	for range 100 {
		file, err := os.CreateTemp(dir, pattern)
		if err != nil {
			return nil, err
		}
		if !backupTargetsDatabase(file.Name(), dbPath) {
			return file, nil
		}
		name := file.Name()
		_ = file.Close()
		_ = os.Remove(name)
	}

	return nil, errors.New("failed to allocate safe temporary backup path")
}

func backupTargetsDatabase(outputPath string, dbPath string) bool {
	output, err := normalizedPath(outputPath)
	if err != nil {
		return false
	}
	for _, reserved := range []string{dbPath, dbPath + "-wal", dbPath + "-shm", dbPath + ".lock", dbPath + ".restore-tmp"} {
		reservedPath, err := normalizedPath(reserved)
		if err == nil && output == reservedPath {
			return true
		}
	}

	return false
}

func normalizedPath(path string) (string, error) {
	absPath, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(absPath)
	if err == nil {
		return resolved, nil
	}
	resolvedParent, parentErr := filepath.EvalSymlinks(filepath.Dir(absPath))
	if parentErr == nil {
		return filepath.Join(resolvedParent, filepath.Base(absPath)), nil
	}

	return filepath.Clean(absPath), nil
}

func configMetadata(cfg config.Config) ConfigMetadata {
	return ConfigMetadata{AuthMode: cfg.AuthMode, AuthUsername: cfg.AuthUsername, AuthPasswordConfigured: strings.TrimSpace(cfg.AuthPasswordHash) != "", DataDir: cfg.DataDir, DBPath: cfg.DBPath, ImportRoot: cfg.ImportRoot, MediaRoot: cfg.MediaRoot, PreviewsEnabled: cfg.PreviewsEnabled, FFMPEGPath: cfg.FFMPEGPath, YTDLPPath: cfg.YTDLPPath}
}

func sqliteString(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "''") + "'"
}
