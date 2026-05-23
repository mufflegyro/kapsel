package backup

import (
	"archive/zip"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"kapsel/internal/applock"
	"kapsel/internal/config"
	"kapsel/internal/database"
	"kapsel/internal/jobs"
)

func TestCreateBackupIncludesDatabaseAndMetadata(t *testing.T) {
	t.Parallel()

	cfg := backupTestConfig(t)
	db := openBackupDB(t, cfg.DBPath)
	seedBackupChannel(t, db, "chan-1")
	backupPath := filepath.Join(t.TempDir(), "kapsel-backup.zip")

	metadata, err := Create(context.Background(), cfg, backupPath)
	if err != nil {
		t.Fatal(err)
	}

	if metadata.FormatVersion != 1 || metadata.SchemaVersion == 0 || metadata.Config.DBPath != cfg.DBPath || metadata.Config.MediaRoot != cfg.MediaRoot {
		t.Fatalf("unexpected backup metadata: %#v", metadata)
	}
	entries := readBackupEntries(t, backupPath)
	if entries["metadata.json"] == nil || entries["kapsel.db"] == nil {
		t.Fatalf("expected metadata.json and kapsel.db entries, got %#v", entries)
	}
	var stored Metadata
	if err := json.Unmarshal(entries["metadata.json"], &stored); err != nil {
		t.Fatal(err)
	}
	if stored.SchemaVersion != metadata.SchemaVersion || stored.Config.YTDLPPath != cfg.YTDLPPath {
		t.Fatalf("unexpected stored metadata: %#v", stored)
	}
	if strings.Contains(string(entries["metadata.json"]), "hash") || strings.Contains(string(entries["metadata.json"]), "auth_password_hash") {
		t.Fatalf("expected metadata to omit auth password hash, got %s", string(entries["metadata.json"]))
	}
}

func TestCreateRejectsDatabaseOutputPath(t *testing.T) {
	t.Parallel()

	cfg := backupTestConfig(t)
	db := openBackupDB(t, cfg.DBPath)
	seedBackupChannel(t, db, "chan-current")
	_ = db.Close()

	for _, outputPath := range []string{cfg.DBPath, cfg.DBPath + "-wal", cfg.DBPath + "-shm", cfg.DBPath + ".lock"} {
		if _, err := Create(context.Background(), cfg, outputPath); err == nil {
			t.Fatalf("expected backup output %s to be rejected", outputPath)
		}
	}
	current := openBackupDB(t, cfg.DBPath)
	assertBackupScalar(t, current, "SELECT count(*) FROM channels WHERE id = ?", int64(1), "chan-current")
}

func TestCreateUsesSafeTempPath(t *testing.T) {
	t.Parallel()

	cfg := backupTestConfig(t)
	cfg.DBPath = filepath.Join(cfg.DataDir, "kapsel-backup.zip.tmp")
	db := openBackupDB(t, cfg.DBPath)
	seedBackupChannel(t, db, "chan-current")
	_ = db.Close()
	backupPath := strings.TrimSuffix(cfg.DBPath, ".tmp")
	if _, err := Create(context.Background(), cfg, backupPath); err != nil {
		t.Fatal(err)
	}
	current := openBackupDB(t, cfg.DBPath)
	assertBackupScalar(t, current, "SELECT count(*) FROM channels WHERE id = ?", int64(1), "chan-current")
}

func TestCreateDoesNotFollowPredictableTempSymlink(t *testing.T) {
	t.Parallel()

	cfg := backupTestConfig(t)
	db := openBackupDB(t, cfg.DBPath)
	seedBackupChannel(t, db, "chan-current")
	_ = db.Close()
	backupPath := filepath.Join(cfg.DataDir, "kapsel-backup.zip")
	if err := os.Symlink(cfg.DBPath, backupPath+".tmp"); err != nil {
		t.Fatal(err)
	}
	if _, err := Create(context.Background(), cfg, backupPath); err != nil {
		t.Fatal(err)
	}
	current := openBackupDB(t, cfg.DBPath)
	assertBackupScalar(t, current, "SELECT count(*) FROM channels WHERE id = ?", int64(1), "chan-current")
}

func TestRestoreRequiresExclusiveDatabaseLock(t *testing.T) {
	t.Parallel()

	sourceCfg := backupTestConfig(t)
	sourceDB := openBackupDB(t, sourceCfg.DBPath)
	seedBackupChannel(t, sourceDB, "chan-restored")
	backupPath := filepath.Join(t.TempDir(), "restore.zip")
	if _, err := Create(context.Background(), sourceCfg, backupPath); err != nil {
		t.Fatal(err)
	}

	targetCfg := backupTestConfig(t)
	targetDB := openBackupDB(t, targetCfg.DBPath)
	seedBackupChannel(t, targetDB, "chan-current")
	_ = targetDB.Close()
	lock, err := applock.Acquire(targetCfg.DBPath + ".lock")
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()

	_, err = Restore(context.Background(), targetCfg, backupPath, RestoreOptions{Force: true})
	if !errors.Is(err, ErrDatabaseInUse) {
		t.Fatalf("expected database-in-use error, got %v", err)
	}
	current := openBackupDB(t, targetCfg.DBPath)
	assertBackupScalar(t, current, "SELECT count(*) FROM channels WHERE id = ?", int64(1), "chan-current")
}

func TestRestoreValidatesAndReplacesDatabase(t *testing.T) {
	t.Parallel()

	sourceCfg := backupTestConfig(t)
	sourceDB := openBackupDB(t, sourceCfg.DBPath)
	seedBackupChannel(t, sourceDB, "chan-restored")
	backupPath := filepath.Join(t.TempDir(), "restore.zip")
	if _, err := Create(context.Background(), sourceCfg, backupPath); err != nil {
		t.Fatal(err)
	}

	targetCfg := backupTestConfig(t)
	targetDB := openBackupDB(t, targetCfg.DBPath)
	seedBackupChannel(t, targetDB, "chan-old")
	_ = targetDB.Close()

	if _, err := Restore(context.Background(), targetCfg, backupPath, RestoreOptions{}); err != nil {
		t.Fatal(err)
	}
	restored := openBackupDB(t, targetCfg.DBPath)
	assertBackupScalar(t, restored, "SELECT count(*) FROM channels WHERE id = ?", int64(1), "chan-restored")
	assertBackupScalar(t, restored, "SELECT count(*) FROM channels WHERE id = ?", int64(0), "chan-old")
}

func TestRestoreRemovesStaleWALSidecars(t *testing.T) {
	t.Parallel()

	sourceCfg := backupTestConfig(t)
	sourceDB := openBackupDB(t, sourceCfg.DBPath)
	seedBackupChannel(t, sourceDB, "chan-restored")
	backupPath := filepath.Join(t.TempDir(), "restore.zip")
	if _, err := Create(context.Background(), sourceCfg, backupPath); err != nil {
		t.Fatal(err)
	}

	targetCfg := backupTestConfig(t)
	targetDB := openBackupDB(t, targetCfg.DBPath)
	seedBackupChannel(t, targetDB, "chan-old")
	_ = targetDB.Close()
	for _, suffix := range []string{"-wal", "-shm"} {
		if err := os.WriteFile(targetCfg.DBPath+suffix, []byte("stale"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	if _, err := Restore(context.Background(), targetCfg, backupPath, RestoreOptions{Force: true}); err != nil {
		t.Fatal(err)
	}
	for _, suffix := range []string{"-wal", "-shm"} {
		if _, err := os.Stat(targetCfg.DBPath + suffix); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("expected restored database sidecar %s to be removed, got %v", suffix, err)
		}
	}
}

func TestRestoreDoesNotFollowPredictableTempSymlink(t *testing.T) {
	t.Parallel()

	sourceCfg := backupTestConfig(t)
	sourceDB := openBackupDB(t, sourceCfg.DBPath)
	seedBackupChannel(t, sourceDB, "chan-restored")
	backupPath := filepath.Join(t.TempDir(), "restore.zip")
	if _, err := Create(context.Background(), sourceCfg, backupPath); err != nil {
		t.Fatal(err)
	}

	targetCfg := backupTestConfig(t)
	targetDB := openBackupDB(t, targetCfg.DBPath)
	seedBackupChannel(t, targetDB, "chan-current")
	_ = targetDB.Close()
	if err := os.Symlink(targetCfg.DBPath, targetCfg.DBPath+".restore-tmp"); err != nil {
		t.Fatal(err)
	}

	if _, err := Restore(context.Background(), targetCfg, backupPath, RestoreOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(targetCfg.DBPath + ".restore-tmp"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected stale predictable restore temp path to be removed, got %v", err)
	}
	restored := openBackupDB(t, targetCfg.DBPath)
	assertBackupScalar(t, restored, "SELECT count(*) FROM channels WHERE id = ?", int64(1), "chan-restored")
	assertBackupScalar(t, restored, "SELECT count(*) FROM channels WHERE id = ?", int64(0), "chan-current")
}

func TestRestoreRejectsIncompatibleBackupWithoutReplacingDatabase(t *testing.T) {
	t.Parallel()

	targetCfg := backupTestConfig(t)
	targetDB := openBackupDB(t, targetCfg.DBPath)
	seedBackupChannel(t, targetDB, "chan-existing")
	_ = targetDB.Close()
	backupPath := filepath.Join(t.TempDir(), "future.zip")
	writeIncompatibleBackup(t, backupPath)

	_, err := Restore(context.Background(), targetCfg, backupPath, RestoreOptions{})
	if !errors.Is(err, ErrIncompatibleBackup) {
		t.Fatalf("expected incompatible backup error, got %v", err)
	}
	current := openBackupDB(t, targetCfg.DBPath)
	assertBackupScalar(t, current, "SELECT count(*) FROM channels WHERE id = ?", int64(1), "chan-existing")
}

func TestRestoreRejectsCorruptDatabaseWithoutReplacingDatabase(t *testing.T) {
	t.Parallel()

	targetCfg := backupTestConfig(t)
	targetDB := openBackupDB(t, targetCfg.DBPath)
	seedBackupChannel(t, targetDB, "chan-existing")
	_ = targetDB.Close()
	backupPath := filepath.Join(t.TempDir(), "corrupt.zip")
	writeBackupWithCorruptDatabase(t, backupPath)

	_, err := Restore(context.Background(), targetCfg, backupPath, RestoreOptions{})
	if err == nil {
		t.Fatal("expected corrupt database restore to fail")
	}
	current := openBackupDB(t, targetCfg.DBPath)
	assertBackupScalar(t, current, "SELECT count(*) FROM channels WHERE id = ?", int64(1), "chan-existing")
}

func TestRestoreBlocksActiveJobsUnlessForced(t *testing.T) {
	t.Parallel()

	sourceCfg := backupTestConfig(t)
	sourceDB := openBackupDB(t, sourceCfg.DBPath)
	seedBackupChannel(t, sourceDB, "chan-restored")
	backupPath := filepath.Join(t.TempDir(), "restore.zip")
	if _, err := Create(context.Background(), sourceCfg, backupPath); err != nil {
		t.Fatal(err)
	}

	targetCfg := backupTestConfig(t)
	targetDB := openBackupDB(t, targetCfg.DBPath)
	seedBackupChannel(t, targetDB, "chan-active")
	store := jobs.NewStore(targetDB)
	if _, err := store.Enqueue(context.Background(), jobs.EnqueueParams{Type: "download"}); err != nil {
		t.Fatal(err)
	}
	_ = targetDB.Close()

	_, err := Restore(context.Background(), targetCfg, backupPath, RestoreOptions{})
	if !errors.Is(err, ErrActiveJobs) {
		t.Fatalf("expected active jobs error, got %v", err)
	}
	current := openBackupDB(t, targetCfg.DBPath)
	assertBackupScalar(t, current, "SELECT count(*) FROM channels WHERE id = ?", int64(1), "chan-active")
	_ = current.Close()
	if _, err := Restore(context.Background(), targetCfg, backupPath, RestoreOptions{Force: true}); err != nil {
		t.Fatal(err)
	}
	restored := openBackupDB(t, targetCfg.DBPath)
	assertBackupScalar(t, restored, "SELECT count(*) FROM channels WHERE id = ?", int64(1), "chan-restored")
}

func backupTestConfig(t *testing.T) config.Config {
	t.Helper()
	root := t.TempDir()
	return config.Config{AuthMode: "required", AuthUsername: "kapsel", AuthPasswordHash: "hash", DataDir: root, DBPath: filepath.Join(root, "kapsel.db"), ImportRoot: filepath.Join(root, "imports"), MediaRoot: filepath.Join(root, "media"), FFMPEGPath: "ffmpeg", YTDLPPath: "yt-dlp"}
}

func openBackupDB(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := database.Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if err := database.Migrate(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	return db
}

func seedBackupChannel(t *testing.T, db *sql.DB, id string) {
	t.Helper()
	if _, err := db.Exec("INSERT INTO channels (id, external_id, name) VALUES (?, ?, ?)", id, id, id); err != nil {
		t.Fatal(err)
	}
}

func assertBackupScalar[T comparable](t *testing.T, db *sql.DB, query string, expected T, args ...any) {
	t.Helper()
	var got T
	if err := db.QueryRow(query, args...).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != expected {
		t.Fatalf("expected %v, got %v", expected, got)
	}
}

func readBackupEntries(t *testing.T, path string) map[string][]byte {
	t.Helper()
	reader, err := zip.OpenReader(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	entries := map[string][]byte{}
	for _, file := range reader.File {
		opened, err := file.Open()
		if err != nil {
			t.Fatal(err)
		}
		data, err := io.ReadAll(opened)
		_ = opened.Close()
		if err != nil {
			t.Fatal(err)
		}
		entries[file.Name] = data
	}
	return entries
}

func writeIncompatibleBackup(t *testing.T, path string) {
	t.Helper()
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	zipFile := zip.NewWriter(file)
	metadata, err := zipFile.Create("metadata.json")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := metadata.Write([]byte(`{"format_version":1,"schema_version":999999}`)); err != nil {
		t.Fatal(err)
	}
	dbEntry, err := zipFile.Create("kapsel.db")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := dbEntry.Write([]byte("not sqlite")); err != nil {
		t.Fatal(err)
	}
	if err := zipFile.Close(); err != nil {
		t.Fatal(err)
	}
}

func writeBackupWithCorruptDatabase(t *testing.T, path string) {
	t.Helper()
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	zipFile := zip.NewWriter(file)
	metadata, err := zipFile.Create("metadata.json")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := metadata.Write([]byte(`{"format_version":1,"schema_version":1}`)); err != nil {
		t.Fatal(err)
	}
	dbEntry, err := zipFile.Create("kapsel.db")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := dbEntry.Write([]byte("not sqlite")); err != nil {
		t.Fatal(err)
	}
	if err := zipFile.Close(); err != nil {
		t.Fatal(err)
	}
}
