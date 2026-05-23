package storage

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"kapsel/internal/database"
)

func TestScanReportsUsageOrphansAndMissingReferences(t *testing.T) {
	t.Parallel()

	cfg, db := storageTestDB(t)
	writeStorageFile(t, cfg.MediaRoot, "videos/vid-1.mp4", "0123456789")
	writeStorageFile(t, cfg.MediaRoot, "thumbs/vid-1.jpg", "img")
	writeStorageFile(t, cfg.MediaRoot, "subtitles/vid-1.en.vtt", "WEBVTT")
	writeStorageFile(t, cfg.MediaRoot, "derived/previews/vid-1/sprite.jpg", "sprite!")
	writeStorageFile(t, cfg.MediaRoot, "orphan/leftover.bin", "orphan-data")
	seedStorageReferences(t, db)

	report, err := Scan(context.Background(), db, cfg)
	if err != nil {
		t.Fatal(err)
	}

	if got := usageBytes(report, CategoryMedia); got != 10 {
		t.Fatalf("expected media usage 10 bytes, got %d", got)
	}
	if got := usageBytes(report, CategoryThumbnail); got != 3 {
		t.Fatalf("expected thumbnail usage 3 bytes, got %d", got)
	}
	if got := usageBytes(report, CategorySubtitle); got != 6 {
		t.Fatalf("expected subtitle usage 6 bytes, got %d", got)
	}
	if got := usageBytes(report, CategoryDerived); got != 7 {
		t.Fatalf("expected derived usage 7 bytes, got %d", got)
	}
	if got := usageBytes(report, CategoryDatabase); got == 0 {
		t.Fatal("expected database usage to be reported")
	}
	if !hasOrphan(report, "orphan/leftover.bin") {
		t.Fatalf("expected orphan file in report: %#v", report.OrphanFiles)
	}
	for _, path := range []string{"videos/missing.mp4", "thumbs/missing.jpg", "subtitles/missing.vtt"} {
		if !hasMissingReference(report, path) {
			t.Fatalf("expected missing reference %s in %#v", path, report.MissingReferences)
		}
	}
	if report.Summary.OrphanFiles != 1 || report.Summary.MissingReferences != 3 {
		t.Fatalf("unexpected summary: %#v", report.Summary)
	}
}

func TestCleanupDryRunAndConfirmedDelete(t *testing.T) {
	t.Parallel()

	cfg, db := storageTestDB(t)
	writeStorageFile(t, cfg.MediaRoot, "orphan/delete-me.bin", "delete")
	outsidePath := filepath.Join(filepath.Dir(cfg.MediaRoot), "outside.bin")
	if err := os.WriteFile(outsidePath, []byte("outside"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO videos (id, external_id, title, media_path) VALUES ('bad-ref', 'bad-ref', 'Bad Reference', '../outside.bin')"); err != nil {
		t.Fatal(err)
	}

	dryRun, err := Cleanup(context.Background(), db, cfg, CleanupOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !dryRun.DryRun || len(dryRun.DeletedFiles) != 0 || !hasOrphan(dryRun.Report, "orphan/delete-me.bin") {
		t.Fatalf("unexpected dry-run cleanup report: %#v", dryRun)
	}
	assertStorageFileExists(t, cfg.MediaRoot, "orphan/delete-me.bin")

	_, err = Cleanup(context.Background(), db, cfg, CleanupOptions{Delete: true})
	if !errors.Is(err, ErrConfirmationRequired) {
		t.Fatalf("expected confirmation error, got %v", err)
	}
	assertStorageFileExists(t, cfg.MediaRoot, "orphan/delete-me.bin")

	deleted, err := Cleanup(context.Background(), db, cfg, CleanupOptions{Delete: true, Confirm: true})
	if err != nil {
		t.Fatal(err)
	}
	if deleted.DryRun || len(deleted.DeletedFiles) != 1 || deleted.DeletedFiles[0].Path != "orphan/delete-me.bin" {
		t.Fatalf("unexpected confirmed cleanup report: %#v", deleted)
	}
	assertStorageFileMissing(t, cfg.MediaRoot, "orphan/delete-me.bin")
	if _, err := os.Stat(outsidePath); err != nil {
		t.Fatalf("cleanup escaped media root: %v", err)
	}
}

func TestScanRejectsEmptyOrFilesystemRootMediaRoot(t *testing.T) {
	t.Parallel()

	cfg, db := storageTestDB(t)
	for _, mediaRoot := range []string{"", filepath.VolumeName(cfg.MediaRoot) + string(os.PathSeparator)} {
		cfg.MediaRoot = mediaRoot
		_, err := Scan(context.Background(), db, cfg)
		if !errors.Is(err, ErrUnsafePath) {
			t.Fatalf("expected unsafe media root %q to be rejected, got %v", mediaRoot, err)
		}
	}
}

func TestScanRejectsSymlinkMediaRoot(t *testing.T) {
	t.Parallel()

	cfg, db := storageTestDB(t)
	realRoot := filepath.Join(cfg.DataRoot, "real-media")
	symlinkRoot := filepath.Join(cfg.DataRoot, "media-link")
	if err := os.MkdirAll(realRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(realRoot, symlinkRoot); err != nil {
		t.Fatal(err)
	}
	cfg.MediaRoot = symlinkRoot

	_, err := Scan(context.Background(), db, cfg)
	if !errors.Is(err, ErrUnsafePath) {
		t.Fatalf("expected symlink media root to be rejected, got %v", err)
	}
}

func storageTestDB(t *testing.T) (Config, *sql.DB) {
	t.Helper()
	root := t.TempDir()
	cfg := Config{DataRoot: root, MediaRoot: filepath.Join(root, "media"), DBPath: filepath.Join(root, "kapsel.db")}
	if err := os.MkdirAll(cfg.MediaRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	db, err := database.Open(context.Background(), cfg.DBPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := database.Migrate(context.Background(), db); err != nil {
		t.Fatal(err)
	}

	return cfg, db
}

func seedStorageReferences(t *testing.T, db *sql.DB) {
	t.Helper()
	_, err := db.Exec(`
INSERT INTO videos (id, external_id, title, media_path, thumbnail_path) VALUES
  ('vid-1', 'vid-1', 'Video', 'videos/vid-1.mp4', 'thumbs/vid-1.jpg'),
  ('vid-missing', 'vid-missing', 'Missing', 'videos/missing.mp4', 'thumbs/missing.jpg');
INSERT INTO subtitles (video_id, language, source, format, path, text) VALUES
  ('vid-1', 'en', 'downloaded', 'vtt', 'subtitles/vid-1.en.vtt', 'caption'),
  ('vid-missing', 'en', 'downloaded', 'vtt', 'subtitles/missing.vtt', 'caption');
INSERT INTO video_previews (video_id, sprite_path, interval_seconds, frame_width, frame_height, columns, preview_count)
VALUES ('vid-1', 'derived/previews/vid-1/sprite.jpg', 10, 160, 90, 5, 3);`)
	if err != nil {
		t.Fatal(err)
	}
}

func writeStorageFile(t *testing.T, root string, path string, body string) {
	t.Helper()
	absPath := filepath.Join(root, filepath.FromSlash(path))
	if err := os.MkdirAll(filepath.Dir(absPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(absPath, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func usageBytes(report Report, category Category) int64 {
	for _, usage := range report.Usage {
		if usage.Category == category {
			return usage.Bytes
		}
	}

	return 0
}

func hasOrphan(report Report, path string) bool {
	for _, orphan := range report.OrphanFiles {
		if orphan.Path == path {
			return true
		}
	}

	return false
}

func hasMissingReference(report Report, path string) bool {
	for _, missing := range report.MissingReferences {
		if missing.Path == path {
			return true
		}
	}

	return false
}

func assertStorageFileExists(t *testing.T, root string, path string) {
	t.Helper()
	if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(path))); err != nil {
		t.Fatalf("expected %s to exist: %v", path, err)
	}
}

func assertStorageFileMissing(t *testing.T, root string, path string) {
	t.Helper()
	if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(path))); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected %s to be removed, got %v", path, err)
	}
}
