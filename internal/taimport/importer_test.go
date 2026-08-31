package taimport

import (
	"archive/zip"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"kapsel/internal/database"
	"kapsel/internal/denorm"
	"kapsel/internal/diskspace"
	"kapsel/internal/jobs"
	"kapsel/internal/search"
)

func TestImportTubeArchivistBackup(t *testing.T) {
	t.Parallel()

	db := openImportDB(t)
	root := t.TempDir()
	writeBackupZip(t, root)

	report, err := Import(context.Background(), db, root)
	if err != nil {
		t.Fatal(err)
	}
	if report.Channels != 1 || report.Videos != 1 || report.Playlists != 1 || report.Comments != 2 {
		t.Fatalf("unexpected report counts: %#v", report)
	}
	if len(report.Skipped) != 1 {
		t.Fatalf("expected one skipped record, got %#v", report.Skipped)
	}

	assertScalar(t, db, "SELECT name FROM channels WHERE id = ?", "Archive Workshop", "chan-1")
	assertScalar(t, db, "SELECT title FROM videos WHERE id = ?", "Kapsel Demo", "vid-1")
	assertScalar(t, db, "SELECT view_count FROM videos WHERE id = ?", int64(4321), "vid-1")
	assertScalar(t, db, "SELECT archived_at FROM videos WHERE id = ?", sql.NullString{String: "2026-05-04T10:11:12Z", Valid: true}, "vid-1")
	assertScalar(t, db, "SELECT media_origin FROM videos WHERE id = ?", "imported", "vid-1")
	assertScalar(t, db, "SELECT media_downloaded_at FROM videos WHERE id = ?", "2026-05-04T10:11:12Z", "vid-1")
	assertScalar(t, db, "SELECT title FROM playlists WHERE id = ?", "Saved Clips", "playlist-1")
	assertScalar(t, db, "SELECT position_seconds FROM user_progress WHERE video_id = ?", int64(42), "vid-1")
	assertScalar(t, db, "SELECT path FROM media_assets WHERE owner_type = 'video' AND owner_id = ? AND kind = 'media'", "media/vid-1.mp4", "vid-1")
	assertScalar(t, db, "SELECT thumbnail_path FROM videos WHERE id = ?", "cache/videos/vid-1.jpg", "vid-1")
	assertScalar(t, db, "SELECT path FROM media_assets WHERE owner_type = 'video' AND owner_id = ? AND kind = 'thumbnail'", "cache/videos/vid-1.jpg", "vid-1")
	assertScalar(t, db, "SELECT path FROM media_assets WHERE owner_type = 'channel' AND owner_id = ? AND kind = 'thumbnail'", "cache/channels/chan-1_thumb.jpg", "chan-1")
	assertScalar(t, db, "SELECT text FROM subtitles WHERE video_id = ? AND language = ?", "A quiet lunar capsule floats past the archive.", "vid-1", "en")
	assertScalar(t, db, "SELECT path FROM subtitles WHERE video_id = ? AND language = ?", "subtitles/vid-1.ja.vtt", "vid-1", "ja")
	assertScalar(t, db, "SELECT count(*) FROM search_documents WHERE owner_type = 'subtitle' AND text LIKE '%lunar%'", int64(1))
	assertScalar(t, db, "SELECT text FROM search_documents WHERE owner_type = 'video' AND owner_id = ? AND field = 'channel'", "Archive Workshop Kapsel Demo", "vid-1")
	assertScalar(t, db, "SELECT text FROM comments WHERE id = ?", "A preserved cabinet of comments", "comment-1")
	assertScalar(t, db, "SELECT parent_id FROM comments WHERE id = ?", "comment-1", "comment-2")
	assertScalar(t, db, "SELECT count(*) FROM search_documents WHERE owner_type = 'comment' AND text LIKE '%cabinet%'", int64(1))
}

func TestUpsertChannelRewritesVideoChannelSearchDocs(t *testing.T) {
	t.Parallel()

	db := openImportDB(t)
	ctx := context.Background()
	if err := upsertChannel(ctx, db, "chan-1", "Old Name", "", "", false, true); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
INSERT INTO videos (id, external_id, channel_id, title) VALUES
  ('vid-1', 'vid-1', 'chan-1', 'Island hike'),
  ('vid-2', 'vid-2', 'chan-1', 'Cabin build')`); err != nil {
		t.Fatal(err)
	}
	for _, video := range []struct{ id, title string }{{"vid-1", "Island hike"}, {"vid-2", "Cabin build"}} {
		if err := denorm.SyncVideoChannelSearchDocument(ctx, db, video.id, "Old Name", video.title); err != nil {
			t.Fatal(err)
		}
	}

	if err := upsertChannel(ctx, db, "chan-1", "New Name", "", "", false, true); err != nil {
		t.Fatal(err)
	}

	assertScalar(t, db, "SELECT text FROM search_documents WHERE owner_type = 'video' AND owner_id = ? AND field = 'channel'", "New Name Island hike", "vid-1")
	assertScalar(t, db, "SELECT text FROM search_documents WHERE owner_type = 'video' AND owner_id = ? AND field = 'channel'", "New Name Cabin build", "vid-2")

	results, err := search.Search(ctx, db, search.Query{Term: "New Name island", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].OwnerType != "video" || results[0].OwnerID != "vid-1" {
		t.Fatalf("expected only vid-1 to match the renamed channel + topic query, got %#v", results)
	}
}

func TestImportJobStoresReport(t *testing.T) {
	t.Parallel()

	db := openImportDB(t)
	store := jobs.NewStore(db)
	root := t.TempDir()
	writeBackupZip(t, root)
	job, err := store.Enqueue(context.Background(), jobs.EnqueueParams{
		Type:        JobType,
		PayloadJSON: `{"root":"` + filepath.ToSlash(root) + `"}`,
		MaxAttempts: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	runner := jobs.NewRunner(store, map[string]jobs.Handler{
		JobType: NewJobHandler(db, store).Handle,
	})

	if err := runner.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	stored, err := store.Get(context.Background(), job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != jobs.StatusSucceeded {
		t.Fatalf("expected succeeded import job, got %#v", stored)
	}
	var report Report
	if err := json.Unmarshal([]byte(stored.ResultJSON), &report); err != nil {
		t.Fatal(err)
	}
	if report.Channels != 1 || report.Videos != 1 || report.Playlists != 1 || report.Comments != 2 || len(report.Skipped) != 1 {
		t.Fatalf("unexpected persisted import report: %#v", report)
	}
}

func TestImportJobCompletionFailureLeavesRetrySafeCommittedRows(t *testing.T) {
	t.Parallel()

	db := openImportDB(t)
	store := jobs.NewStore(db)
	root := t.TempDir()
	writeBackupZip(t, root)
	job, err := store.Enqueue(context.Background(), jobs.EnqueueParams{
		Type:        JobType,
		PayloadJSON: `{"root":"` + filepath.ToSlash(root) + `"}`,
		MaxAttempts: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	claimed, ok, err := store.Claim(context.Background(), time.Now().Add(time.Second), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if !ok || claimed.ID != job.ID {
		t.Fatalf("expected import job claim, ok=%v job=%#v", ok, claimed)
	}
	if _, err := db.Exec("UPDATE jobs SET status = ?, locked_at = NULL, completed_at = ?, updated_at = ? WHERE id = ?", jobs.StatusFailed, time.Now().UTC().Format(time.RFC3339Nano), time.Now().UTC().Format(time.RFC3339Nano), job.ID); err != nil {
		t.Fatal(err)
	}

	err = NewJobHandler(db, store).Handle(context.Background(), claimed)
	if !errors.Is(err, jobs.ErrInvalidTransition) {
		t.Fatalf("expected invalid transition completing non-running import job, got %v", err)
	}
	failed, err := store.Get(context.Background(), job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if failed.Status != jobs.StatusFailed || failed.ResultCommitted || failed.ResultJSON != "{}" {
		t.Fatalf("expected completion failure to leave job retry-safe without final result, got %#v", failed)
	}
	assertScalar(t, db, "SELECT count(*) FROM videos WHERE id = ?", int64(1), "vid-1")

	if err := store.Retry(context.Background(), job.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("UPDATE jobs SET run_after = ? WHERE id = ?", time.Now().Add(-time.Second).UTC().Format(time.RFC3339Nano), job.ID); err != nil {
		t.Fatal(err)
	}
	runner := jobs.NewRunner(store, map[string]jobs.Handler{
		JobType: NewJobHandler(db, store).Handle,
	})
	if err := runner.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	succeeded, err := store.Get(context.Background(), job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if succeeded.Status != jobs.StatusSucceeded || !succeeded.ResultCommitted || succeeded.ResultSummary == "" {
		t.Fatalf("expected retry to complete imported rows with final result, got %#v", succeeded)
	}
	assertScalar(t, db, "SELECT count(*) FROM videos WHERE id = ?", int64(1), "vid-1")
}

func TestDownloadedAtParsesTubeArchivistDateShapes(t *testing.T) {
	t.Parallel()

	fallback := "2026-05-04T12:00:00Z"
	for _, test := range []struct {
		name string
		raw  string
		want string
	}{
		{name: "unix-number", raw: `1777874400`, want: "2026-05-04T06:00:00Z"},
		{name: "unix-string", raw: `"1777874400"`, want: "2026-05-04T06:00:00Z"},
		{name: "rfc3339", raw: `"2026-05-04T10:11:12Z"`, want: "2026-05-04T10:11:12Z"},
		{name: "date", raw: `"2026-05-04"`, want: "2026-05-04T00:00:00Z"},
		{name: "empty", raw: `""`, want: fallback},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := downloadedAt(json.RawMessage(test.raw), fallback); got != test.want {
				t.Fatalf("expected %q, got %q", test.want, got)
			}
		})
	}
}

func TestImportJobFailsEarlyWhenDiskSpaceBelowThreshold(t *testing.T) {
	t.Parallel()

	db := openImportDB(t)
	store := jobs.NewStore(db)
	dataRoot := t.TempDir()
	root := t.TempDir()
	writeBackupZip(t, root)
	job, err := store.Enqueue(context.Background(), jobs.EnqueueParams{
		Type:        JobType,
		PayloadJSON: `{"root":"` + filepath.ToSlash(root) + `"}`,
		MaxAttempts: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	runner := jobs.NewRunner(store, map[string]jobs.Handler{
		JobType: NewJobHandler(db, store).WithDiskSpace(dataRoot, 1<<30, func(path string) (diskspace.Stats, error) {
			return diskspace.Stats{Path: path, AvailableBytes: 512 << 20}, nil
		}).Handle,
	})

	if err := runner.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	stored, err := store.Get(context.Background(), job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != jobs.StatusFailed || !strings.Contains(stored.Error, "low disk space") || !strings.Contains(stored.Error, dataRoot) {
		t.Fatalf("expected failed low-space import job, got %#v", stored)
	}
	assertScalar(t, db, "SELECT count(*) FROM videos", int64(0))
}

func TestImportJobStoresPartialReportOnFailure(t *testing.T) {
	t.Parallel()

	db := openImportDB(t)
	store := jobs.NewStore(db)
	root := t.TempDir()
	writeBackupZip(t, root)
	brokenPath := filepath.Join(root, "cache", "backup", "zz_broken.zip")
	if err := os.WriteFile(brokenPath, []byte("not a zip"), 0o644); err != nil {
		t.Fatal(err)
	}
	job, err := store.Enqueue(context.Background(), jobs.EnqueueParams{
		Type:        JobType,
		PayloadJSON: `{"root":"` + filepath.ToSlash(root) + `"}`,
		MaxAttempts: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	runner := jobs.NewRunner(store, map[string]jobs.Handler{
		JobType: NewJobHandler(db, store).Handle,
	})

	if err := runner.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	stored, err := store.Get(context.Background(), job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != jobs.StatusFailed || stored.Error == "" {
		t.Fatalf("expected failed import job with error, got %#v", stored)
	}
	var report Report
	if err := json.Unmarshal([]byte(stored.ResultJSON), &report); err != nil {
		t.Fatal(err)
	}
	if report.Channels != 1 || report.Videos != 1 || report.Playlists != 1 || len(report.Skipped) != 1 {
		t.Fatalf("unexpected partial import report: %#v", report)
	}
}

func TestImportJobStoresPartialReportOnCancelledContext(t *testing.T) {
	t.Parallel()

	db := openImportDB(t)
	store := jobs.NewStore(db)
	root := t.TempDir()
	writeBackupZip(t, root)
	job, err := store.Enqueue(context.Background(), jobs.EnqueueParams{
		Type:        JobType,
		PayloadJSON: `{"root":"` + filepath.ToSlash(root) + `"}`,
		MaxAttempts: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	claimed, ok, err := store.Claim(context.Background(), time.Now().Add(time.Second), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if !ok || claimed.ID != job.ID {
		t.Fatalf("expected import job claim, ok=%v job=%#v", ok, claimed)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if err := NewJobHandler(db, store).Handle(ctx, claimed); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected cancelled import handler, got %v", err)
	}
	stored, err := store.Get(context.Background(), job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.ResultJSON == "" || stored.ResultJSON == "{}" || stored.ResultCommitted {
		t.Fatalf("expected cancelled import to preserve diagnostic partial report, got %#v", stored)
	}
}

func TestImportRejectsOversizedBackupEntry(t *testing.T) {
	oldLimit := maxImportEntryBytes
	maxImportEntryBytes = 64
	t.Cleanup(func() { maxImportEntryBytes = oldLimit })

	db := openImportDB(t)
	root := t.TempDir()
	writeOversizedBackupZip(t, root)

	_, err := Import(context.Background(), db, root)
	if err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("expected oversized entry error, got %v", err)
	}
	assertScalar(t, db, "SELECT count(*) FROM videos", int64(0))
}

func TestImportStopsAfterProgressCancellation(t *testing.T) {
	t.Parallel()

	db := openImportDB(t)
	root := t.TempDir()
	writeBackupZip(t, root)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	_, err := importWithProgress(ctx, db, root, func(progress float64) error {
		if progress > 0 {
			cancel()
		}
		return nil
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected import cancellation, got %v", err)
	}
	assertScalar(t, db, "SELECT count(*) FROM channels", int64(1))
	assertScalar(t, db, "SELECT count(*) FROM videos", int64(0))
}

func TestReadImportEntryHonorsCancelledContext(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	writeBackupZip(t, root)
	backups, err := findBackups(root)
	if err != nil {
		t.Fatal(err)
	}
	reader, err := zip.OpenReader(backups[0])
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err = readImportEntry(ctx, reader.File[0])
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected cancelled context before reading import entry, got %v", err)
	}
}

func TestImportJobReportsProgress(t *testing.T) {
	t.Parallel()

	db := openImportDB(t)
	store := jobs.NewStore(db)
	root := t.TempDir()
	writeBackupZip(t, root)
	_, err := store.Enqueue(context.Background(), jobs.EnqueueParams{
		Type:        JobType,
		PayloadJSON: `{"root":"` + filepath.ToSlash(root) + `"}`,
		MaxAttempts: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	claimed, ok, err := store.Claim(context.Background(), time.Now(), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("expected queued import job to be claimed")
	}

	observedProgress := false
	report, err := importWithProgress(context.Background(), db, root, func(progress float64) error {
		if err := store.ReportProgress(context.Background(), claimed.ID, progress); err != nil {
			return err
		}
		if progress <= 0 || observedProgress {
			return nil
		}
		stored, err := store.Get(context.Background(), claimed.ID)
		if err != nil {
			return err
		}
		if stored.Progress <= 0 || stored.Progress >= 1 {
			return fmt.Errorf("expected in-flight progress below completion, got %#v", stored)
		}
		if stored.LockedAt != claimed.LockedAt {
			return fmt.Errorf("expected progress reporting not to renew lease, got locked_at %q", stored.LockedAt)
		}
		observedProgress = true
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !observedProgress {
		t.Fatal("expected import progress callback before completion")
	}
	resultJSON, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CompleteWithResult(context.Background(), claimed.ID, string(resultJSON)); err != nil {
		t.Fatal(err)
	}
	stored, err := store.Get(context.Background(), claimed.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != jobs.StatusSucceeded || stored.Progress != 1 || !stored.ResultCommitted || stored.ResultSummary == "" {
		t.Fatalf("expected import handler to complete with a committed result, got %#v", stored)
	}
}

func TestImportJobHandlerReportsProgress(t *testing.T) {
	t.Parallel()

	db := openImportDB(t)
	store := jobs.NewStore(db)
	root := t.TempDir()
	writeBackupZip(t, root)
	job, err := store.Enqueue(context.Background(), jobs.EnqueueParams{
		Type:        JobType,
		PayloadJSON: `{"root":"` + filepath.ToSlash(root) + `"}`,
		MaxAttempts: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	claimed, ok, err := store.Claim(context.Background(), time.Now().Add(time.Second), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if !ok || claimed.ID != job.ID {
		t.Fatalf("expected import job claim, ok=%v job=%#v", ok, claimed)
	}
	if _, err := db.Exec("CREATE TABLE observed_import_progress (progress REAL NOT NULL)"); err != nil {
		t.Fatal(err)
	}
	quotedID := strings.ReplaceAll(claimed.ID, "'", "''")
	if _, err := db.Exec(fmt.Sprintf(`
CREATE TRIGGER observe_import_progress
AFTER UPDATE OF progress ON jobs
WHEN NEW.id = '%s' AND OLD.status = 'running' AND NEW.status = 'running' AND NEW.progress > 0 AND NEW.progress < 1
BEGIN
  INSERT INTO observed_import_progress(progress) VALUES (NEW.progress);
END`, quotedID)); err != nil {
		t.Fatal(err)
	}

	if err := NewJobHandler(db, store).Handle(context.Background(), claimed); err != nil {
		t.Fatal(err)
	}
	assertScalar(t, db, "SELECT count(*) > 0 FROM observed_import_progress", int64(1))
}

func TestImportJobHandlerIgnoresProgressUpdateErrors(t *testing.T) {
	t.Parallel()

	db := openImportDB(t)
	store := jobs.NewStore(db)
	root := t.TempDir()
	writeBackupZip(t, root)
	job, err := store.Enqueue(context.Background(), jobs.EnqueueParams{
		Type:        JobType,
		PayloadJSON: `{"root":"` + filepath.ToSlash(root) + `"}`,
		MaxAttempts: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	claimed, ok, err := store.Claim(context.Background(), time.Now().Add(time.Second), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if !ok || claimed.ID != job.ID {
		t.Fatalf("expected import job claim, ok=%v job=%#v", ok, claimed)
	}
	quotedID := strings.ReplaceAll(claimed.ID, "'", "''")
	_, err = db.Exec(fmt.Sprintf(`
CREATE TRIGGER fail_import_progress_update
BEFORE UPDATE OF progress ON jobs
WHEN NEW.id = '%s' AND OLD.status = 'running' AND NEW.status = 'running' AND NEW.progress > 0 AND NEW.progress < 1
BEGIN
  SELECT RAISE(FAIL, 'progress update unavailable');
END`, quotedID))
	if err != nil {
		t.Fatal(err)
	}

	if err := NewJobHandler(db, store).Handle(context.Background(), claimed); err != nil {
		t.Fatal(err)
	}
	stored, err := store.Get(context.Background(), job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != jobs.StatusSucceeded || stored.Progress != 1 || !stored.ResultCommitted {
		t.Fatalf("expected import job to complete despite progress update errors, got %#v", stored)
	}
	assertScalar(t, db, "SELECT count(*) FROM videos WHERE id = ?", int64(1), "vid-1")
}

func TestImportJobRevalidatesImportRootAtExecution(t *testing.T) {
	t.Parallel()

	db := openImportDB(t)
	store := jobs.NewStore(db)
	allowedRoot := t.TempDir()
	importRoot := filepath.Join(allowedRoot, "tubearchivist")
	if err := os.MkdirAll(importRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	outsideRoot := t.TempDir()
	writeBackupZip(t, outsideRoot)
	job, err := store.Enqueue(context.Background(), jobs.EnqueueParams{
		Type:        JobType,
		PayloadJSON: `{"root":"` + filepath.ToSlash(importRoot) + `"}`,
		MaxAttempts: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(importRoot); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outsideRoot, importRoot); err != nil {
		t.Skipf("symlink not supported: %v", err)
	}
	runner := jobs.NewRunner(store, map[string]jobs.Handler{
		JobType: NewJobHandler(db, store, allowedRoot).Handle,
	})

	if err := runner.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	stored, err := store.Get(context.Background(), job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != jobs.StatusFailed || stored.Error == "" {
		t.Fatalf("expected failed import job after symlink swap, got %#v", stored)
	}
	var count int
	if err := db.QueryRow("SELECT count(*) FROM videos").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("expected symlink-swapped import not to import videos, got %d", count)
	}
}

func TestImportRejectsSymlinkedBackupOutsideRoot(t *testing.T) {
	t.Parallel()

	db := openImportDB(t)
	root := t.TempDir()
	outsideRoot := t.TempDir()
	writeBackupZip(t, outsideRoot)
	if err := os.Symlink(filepath.Join(outsideRoot, "cache"), filepath.Join(root, "cache")); err != nil {
		t.Skipf("symlink not supported: %v", err)
	}

	_, err := Import(context.Background(), db, root)
	if !errors.Is(err, ErrRootOutsideImportRoot) {
		t.Fatalf("expected symlinked backup root to be rejected, got %v", err)
	}
	var count int
	if err := db.QueryRow("SELECT count(*) FROM videos").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("expected symlinked backup not to import videos, got %d", count)
	}
}

func TestImportRejectsUnsafeThumbnailPath(t *testing.T) {
	t.Parallel()

	db := openImportDB(t)
	root := t.TempDir()
	writeUnsafeThumbnailBackupZip(t, root)

	report, err := Import(context.Background(), db, root)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Skipped) != 1 || !strings.Contains(report.Skipped[0].Reason, "invalid media path") {
		t.Fatalf("expected unsafe thumbnail record to be skipped, got %#v", report)
	}
	assertScalar(t, db, "SELECT count(*) FROM videos WHERE id = ?", int64(0), "vid-unsafe")
	assertScalar(t, db, "SELECT count(*) FROM media_assets WHERE path LIKE '%secret%'", int64(0))
}

func TestImportStoresRemoteThumbnailURLs(t *testing.T) {
	t.Parallel()

	db := openImportDB(t)
	root := t.TempDir()
	writeRemoteThumbnailBackupZip(t, root)

	report, err := Import(context.Background(), db, root)
	if err != nil {
		t.Fatal(err)
	}
	if report.Channels != 1 || report.Videos != 1 || len(report.Skipped) != 0 {
		t.Fatalf("unexpected remote thumbnail report: %#v", report)
	}
	assertScalar(t, db, "SELECT thumbnail_url FROM channels WHERE id = ?", "https://yt3.googleusercontent.com/archive-workshop.jpg", "chan-remote")
	assertScalar(t, db, "SELECT thumbnail_url FROM videos WHERE id = ?", "https://i.ytimg.com/vi/vid-remote/maxresdefault.jpg", "vid-remote")
	assertScalar(t, db, "SELECT media_origin FROM videos WHERE id = ?", "imported", "vid-remote")
	assertScalar(t, db, "SELECT media_downloaded_at <> '' FROM videos WHERE id = ?", int64(1), "vid-remote")
	assertScalar(t, db, "SELECT thumbnail_path FROM videos WHERE id = ?", "", "vid-remote")
	assertScalar(t, db, "SELECT count(*) FROM media_assets WHERE owner_type = 'channel' AND owner_id = ? AND kind = 'thumbnail'", int64(0), "chan-remote")
	assertScalar(t, db, "SELECT count(*) FROM media_assets WHERE owner_type = 'video' AND owner_id = ? AND kind = 'thumbnail'", int64(0), "vid-remote")
	assertScalar(t, db, "SELECT path FROM media_assets WHERE owner_type = 'video' AND owner_id = ? AND kind = 'media'", "media/vid-remote.mp4", "vid-remote")
}

func TestImportRejectsDisallowedRemoteThumbnailURLs(t *testing.T) {
	t.Parallel()

	db := openImportDB(t)
	for _, test := range []struct {
		name string
		url  string
	}{
		{name: "file", url: "file:///../../secret.jpg"},
		{name: "ftp", url: "ftp://i.ytimg.com/vi/vid/thumb.jpg"},
		{name: "host", url: "https://example.com/thumb.jpg"},
	} {
		t.Run(test.name, func(t *testing.T) {
			source, err := json.Marshal(map[string]any{
				"youtube_id":    "vid-disallowed-" + test.name,
				"title":         "Disallowed Thumbnail",
				"vid_thumb_url": test.url,
			})
			if err != nil {
				t.Fatal(err)
			}

			if err := importVideo(context.Background(), db, source); err == nil || !strings.Contains(err.Error(), "invalid media path") {
				t.Fatalf("expected invalid media path for %q, got %v", test.url, err)
			}
		})
	}
}

func TestImportStoresSchemeRelativeRemoteThumbnailURL(t *testing.T) {
	t.Parallel()

	db := openImportDB(t)
	source, err := json.Marshal(map[string]any{
		"youtube_id":    "vid-scheme-relative",
		"title":         "Scheme Relative Thumbnail",
		"vid_thumb_url": "//i.ytimg.com/vi/vid-scheme-relative/maxresdefault.jpg",
	})
	if err != nil {
		t.Fatal(err)
	}

	if err := importVideo(context.Background(), db, source); err != nil {
		t.Fatal(err)
	}

	assertScalar(t, db, "SELECT thumbnail_url FROM videos WHERE id = ?", "https://i.ytimg.com/vi/vid-scheme-relative/maxresdefault.jpg", "vid-scheme-relative")
	assertScalar(t, db, "SELECT count(*) FROM media_assets WHERE owner_type = 'video' AND owner_id = ? AND kind = 'thumbnail'", int64(0), "vid-scheme-relative")
}

func TestImportChannelClearsStaleRemoteThumbnailURL(t *testing.T) {
	t.Parallel()

	db := openImportDB(t)
	remote, err := json.Marshal(map[string]any{
		"channel_id":        "chan-stale-remote",
		"channel_name":      "Stale Remote Channel",
		"channel_thumb_url": "https://yt3.googleusercontent.com/stale.jpg",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := importChannel(context.Background(), db, remote); err != nil {
		t.Fatal(err)
	}
	assertScalar(t, db, "SELECT thumbnail_url FROM channels WHERE id = ?", "https://yt3.googleusercontent.com/stale.jpg", "chan-stale-remote")

	local, err := json.Marshal(map[string]any{
		"channel_id":        "chan-stale-remote",
		"channel_name":      "Stale Remote Channel",
		"channel_thumb_url": "/cache/channels/stale-local.jpg",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := importChannel(context.Background(), db, local); err != nil {
		t.Fatal(err)
	}

	assertScalar(t, db, "SELECT thumbnail_url FROM channels WHERE id = ?", "", "chan-stale-remote")
	assertScalar(t, db, "SELECT path FROM media_assets WHERE owner_type = 'channel' AND owner_id = ? AND kind = 'thumbnail'", "cache/channels/stale-local.jpg", "chan-stale-remote")
}

func TestImportVideoParsesNumericPublishedDate(t *testing.T) {
	t.Parallel()

	db := openImportDB(t)
	source, err := json.Marshal(map[string]any{
		"youtube_id": "vid-published-number",
		"title":      "Published Number",
		"published":  1777809600,
	})
	if err != nil {
		t.Fatal(err)
	}

	if err := importVideo(context.Background(), db, source); err != nil {
		t.Fatal(err)
	}

	assertScalar(t, db, "SELECT published_at FROM videos WHERE id = ?", sql.NullString{String: "2026-05-03T12:00:00Z", Valid: true}, "vid-published-number")
}

func TestImportVideoClassifiesReimportedMediaAsImported(t *testing.T) {
	t.Parallel()

	db := openImportDB(t)
	if _, err := db.Exec(`
INSERT INTO videos (id, external_id, title, media_path, media_origin, media_downloaded_at)
VALUES ('vid-reimport-origin', 'vid-reimport-origin', 'Auto-owned Video', 'videos/auto.mp4', 'channel_auto', '2026-05-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	source, err := json.Marshal(map[string]any{
		"youtube_id":      "vid-reimport-origin",
		"title":           "Imported Video",
		"media_url":       "videos/imported.mp4",
		"date_downloaded": "2026-05-04T10:11:12Z",
	})
	if err != nil {
		t.Fatal(err)
	}

	if err := importVideo(context.Background(), db, source); err != nil {
		t.Fatal(err)
	}

	assertScalar(t, db, "SELECT media_path FROM videos WHERE id = ?", "videos/imported.mp4", "vid-reimport-origin")
	assertScalar(t, db, "SELECT media_origin FROM videos WHERE id = ?", "imported", "vid-reimport-origin")
	assertScalar(t, db, "SELECT media_downloaded_at FROM videos WHERE id = ?", "2026-05-04T10:11:12Z", "vid-reimport-origin")
}

func TestImportPlaylistReimportRemovesStaleEntries(t *testing.T) {
	t.Parallel()

	db := openImportDB(t)
	root := t.TempDir()
	writePlaylistBackupZip(t, root, "playlist-stale", []playlistBackupEntry{
		{VideoID: "vid-1", Position: 0},
		{VideoID: "vid-2", Position: 1},
	})
	if _, err := Import(context.Background(), db, root); err != nil {
		t.Fatal(err)
	}
	assertScalar(t, db, "SELECT count(*) FROM playlist_entries WHERE playlist_id = ?", int64(2), "playlist-stale")

	writePlaylistBackupZip(t, root, "playlist-stale", []playlistBackupEntry{
		{VideoID: "vid-2", Position: 0},
	})
	report, err := Import(context.Background(), db, root)
	if err != nil {
		t.Fatal(err)
	}
	if report.Playlists != 1 || len(report.Skipped) != 0 {
		t.Fatalf("unexpected reimport report: %#v", report)
	}

	assertScalar(t, db, "SELECT count(*) FROM playlist_entries WHERE playlist_id = ?", int64(1), "playlist-stale")
	assertScalar(t, db, "SELECT count(*) FROM playlist_entries WHERE playlist_id = ? AND video_id = ?", int64(0), "playlist-stale", "vid-1")
	assertScalar(t, db, "SELECT position FROM playlist_entries WHERE playlist_id = ? AND video_id = ?", int64(0), "playlist-stale", "vid-2")
}

func TestImportPlaylistReordersEntriesAtomically(t *testing.T) {
	t.Parallel()

	db := openImportDB(t)
	root := t.TempDir()
	writePlaylistBackupZip(t, root, "playlist-reorder", []playlistBackupEntry{
		{VideoID: "vid-1", Position: 0},
		{VideoID: "vid-2", Position: 1},
	})
	if _, err := Import(context.Background(), db, root); err != nil {
		t.Fatal(err)
	}

	writePlaylistBackupZip(t, root, "playlist-reorder", []playlistBackupEntry{
		{VideoID: "vid-2", Position: 0},
		{VideoID: "vid-1", Position: 1},
	})
	report, err := Import(context.Background(), db, root)
	if err != nil {
		t.Fatal(err)
	}
	if report.Playlists != 1 || len(report.Skipped) != 0 {
		t.Fatalf("unexpected reorder report: %#v", report)
	}

	assertScalar(t, db, "SELECT position FROM playlist_entries WHERE playlist_id = ? AND video_id = ?", int64(0), "playlist-reorder", "vid-2")
	assertScalar(t, db, "SELECT position FROM playlist_entries WHERE playlist_id = ? AND video_id = ?", int64(1), "playlist-reorder", "vid-1")
}

func TestImportPlaylistRollsBackMetadataAndEntriesOnFailure(t *testing.T) {
	t.Parallel()

	db := openImportDB(t)
	root := t.TempDir()
	writePlaylistBackupZip(t, root, "playlist-atomic", []playlistBackupEntry{
		{VideoID: "vid-1", Position: 0},
	})
	if _, err := Import(context.Background(), db, root); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("UPDATE playlists SET title = 'Original Title' WHERE id = 'playlist-atomic'"); err != nil {
		t.Fatal(err)
	}

	writePlaylistBackupZip(t, root, "playlist-atomic", []playlistBackupEntry{
		{VideoID: "missing-video", Position: 0, SkipVideo: true},
	})
	report, err := Import(context.Background(), db, root)
	if err != nil {
		t.Fatal(err)
	}
	if report.Playlists != 0 || len(report.Skipped) != 1 {
		t.Fatalf("expected failed playlist import to be skipped, got %#v", report)
	}

	assertScalar(t, db, "SELECT title FROM playlists WHERE id = ?", "Original Title", "playlist-atomic")
	assertScalar(t, db, "SELECT count(*) FROM playlist_entries WHERE playlist_id = ?", int64(1), "playlist-atomic")
	assertScalar(t, db, "SELECT position FROM playlist_entries WHERE playlist_id = ? AND video_id = ?", int64(0), "playlist-atomic", "vid-1")
}

func TestImportVideoClearsDenormalizedRows(t *testing.T) {
	t.Parallel()

	db := openImportDB(t)
	first, err := json.Marshal(map[string]any{
		"youtube_id":    "vid-clear",
		"title":         "Clearable Video",
		"description":   "searchable description",
		"media_url":     "media/vid-clear.mp4",
		"vid_thumb_url": "cache/videos/vid-clear.jpg",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := importVideo(context.Background(), db, first); err != nil {
		t.Fatal(err)
	}
	assertScalar(t, db, "SELECT count(*) FROM media_assets WHERE owner_type = 'video' AND owner_id = ?", int64(2), "vid-clear")
	assertScalar(t, db, "SELECT count(*) FROM search_documents WHERE owner_type = 'video' AND owner_id = ? AND field = 'description'", int64(1), "vid-clear")

	cleared, err := json.Marshal(map[string]any{
		"youtube_id":  "vid-clear",
		"title":       "Clearable Video",
		"description": "",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := importVideo(context.Background(), db, cleared); err != nil {
		t.Fatal(err)
	}

	assertScalar(t, db, "SELECT media_path FROM videos WHERE id = ?", "", "vid-clear")
	assertScalar(t, db, "SELECT thumbnail_path FROM videos WHERE id = ?", "", "vid-clear")
	assertScalar(t, db, "SELECT count(*) FROM media_assets WHERE owner_type = 'video' AND owner_id = ?", int64(0), "vid-clear")
	assertScalar(t, db, "SELECT count(*) FROM search_documents WHERE owner_type = 'video' AND owner_id = ? AND field = 'description'", int64(0), "vid-clear")
	assertScalar(t, db, "SELECT count(*) FROM search_documents WHERE owner_type = 'video' AND owner_id = ? AND field = 'title'", int64(1), "vid-clear")
}

func TestImportVideoKeepsWatchedStateMonotonic(t *testing.T) {
	t.Parallel()

	db := openImportDB(t)
	watched, err := json.Marshal(map[string]any{
		"youtube_id": "vid-import-watched",
		"title":      "Imported Watched Video",
		"player": map[string]any{
			"watched": true,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := importVideo(context.Background(), db, watched); err != nil {
		t.Fatal(err)
	}
	assertScalar(t, db, "SELECT watched FROM videos WHERE id = ?", int64(1), "vid-import-watched")
	assertScalar(t, db, "SELECT watched FROM user_progress WHERE video_id = ?", int64(1), "vid-import-watched")

	unwatched, err := json.Marshal(map[string]any{
		"youtube_id": "vid-import-watched",
		"title":      "Imported Watched Video",
		"player": map[string]any{
			"watched": false,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := importVideo(context.Background(), db, unwatched); err != nil {
		t.Fatal(err)
	}
	assertScalar(t, db, "SELECT watched FROM videos WHERE id = ?", int64(1), "vid-import-watched")
	assertScalar(t, db, "SELECT watched FROM user_progress WHERE video_id = ?", int64(1), "vid-import-watched")

	local, err := json.Marshal(map[string]any{
		"youtube_id": "vid-local-watched",
		"title":      "Local Watched Video",
		"player": map[string]any{
			"watched": false,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := importVideo(context.Background(), db, local); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("UPDATE videos SET watched = 0 WHERE id = 'vid-local-watched'"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("UPDATE user_progress SET watched = 1 WHERE video_id = 'vid-local-watched'"); err != nil {
		t.Fatal(err)
	}
	if err := importVideo(context.Background(), db, local); err != nil {
		t.Fatal(err)
	}
	assertScalar(t, db, "SELECT watched FROM videos WHERE id = ?", int64(1), "vid-local-watched")
	assertScalar(t, db, "SELECT watched FROM user_progress WHERE video_id = ?", int64(1), "vid-local-watched")
}

func TestImportChannelClearsThumbnailAsset(t *testing.T) {
	t.Parallel()

	db := openImportDB(t)
	first, err := json.Marshal(map[string]any{
		"channel_id":        "chan-clear",
		"channel_name":      "Clearable Channel",
		"channel_thumb_url": "/cache/channels/chan-clear.jpg",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := importChannel(context.Background(), db, first); err != nil {
		t.Fatal(err)
	}
	assertScalar(t, db, "SELECT count(*) FROM media_assets WHERE owner_type = 'channel' AND owner_id = ? AND kind = 'thumbnail'", int64(1), "chan-clear")

	cleared, err := json.Marshal(map[string]any{
		"channel_id":   "chan-clear",
		"channel_name": "Clearable Channel",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := importChannel(context.Background(), db, cleared); err != nil {
		t.Fatal(err)
	}

	assertScalar(t, db, "SELECT count(*) FROM channels WHERE id = ?", int64(1), "chan-clear")
	assertScalar(t, db, "SELECT count(*) FROM media_assets WHERE owner_type = 'channel' AND owner_id = ? AND kind = 'thumbnail'", int64(0), "chan-clear")
}

func TestImportChannelRollsBackDenormalizedFailure(t *testing.T) {
	t.Parallel()

	db := openImportDB(t)
	first, err := json.Marshal(map[string]any{
		"channel_id":        "chan-rollback",
		"channel_name":      "Old Channel",
		"channel_thumb_url": "/cache/channels/old.jpg",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := importChannel(context.Background(), db, first); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO media_assets (owner_type, owner_id, kind, path) VALUES ('channel', 'other-channel', 'thumbnail', 'cache/channels/conflict.jpg')"); err != nil {
		t.Fatal(err)
	}

	conflicting, err := json.Marshal(map[string]any{
		"channel_id":        "chan-rollback",
		"channel_name":      "New Channel",
		"channel_thumb_url": "/cache/channels/conflict.jpg",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := importChannel(context.Background(), db, conflicting); err == nil {
		t.Fatal("expected conflicting channel thumbnail asset to fail")
	}

	assertScalar(t, db, "SELECT name FROM channels WHERE id = ?", "Old Channel", "chan-rollback")
	assertScalar(t, db, "SELECT text FROM search_documents WHERE owner_type = 'channel' AND owner_id = ? AND field = 'name'", "Old Channel", "chan-rollback")
	assertScalar(t, db, "SELECT path FROM media_assets WHERE owner_type = 'channel' AND owner_id = ? AND kind = 'thumbnail'", "cache/channels/old.jpg", "chan-rollback")
}

func TestImportVideoRollsBackDenormalizedFailure(t *testing.T) {
	t.Parallel()

	db := openImportDB(t)
	first, err := json.Marshal(map[string]any{
		"youtube_id":  "vid-rollback",
		"title":       "Rollback Video",
		"description": "old description",
		"media_url":   "media/old.mp4",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := importVideo(context.Background(), db, first); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO media_assets (owner_type, owner_id, kind, path) VALUES ('video', 'other-video', 'media', 'media/conflict.mp4')"); err != nil {
		t.Fatal(err)
	}

	conflicting, err := json.Marshal(map[string]any{
		"youtube_id":  "vid-rollback",
		"title":       "Rollback Video",
		"description": "new description",
		"media_url":   "media/conflict.mp4",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := importVideo(context.Background(), db, conflicting); err == nil {
		t.Fatal("expected conflicting media asset to fail")
	}

	assertScalar(t, db, "SELECT description FROM videos WHERE id = ?", "old description", "vid-rollback")
	assertScalar(t, db, "SELECT media_path FROM videos WHERE id = ?", "media/old.mp4", "vid-rollback")
	assertScalar(t, db, "SELECT path FROM media_assets WHERE owner_type = 'video' AND owner_id = ? AND kind = 'media'", "media/old.mp4", "vid-rollback")
	assertScalar(t, db, "SELECT text FROM search_documents WHERE owner_type = 'video' AND owner_id = ? AND field = 'description'", "old description", "vid-rollback")
}

func TestImportSubtitleEmptyTextClearsTranscriptSearch(t *testing.T) {
	t.Parallel()

	db := openImportDB(t)
	if _, err := db.Exec("INSERT INTO videos (id, external_id, title) VALUES ('vid-1', 'vid-1', 'Video One')"); err != nil {
		t.Fatal(err)
	}
	if err := importSubtitle(context.Background(), db, "vid-1", subtitleDoc{Language: "en", Source: "manual", Format: "vtt", Path: "subtitles/vid-1.en.vtt", Text: "lunar transcript"}); err != nil {
		t.Fatal(err)
	}
	assertScalar(t, db, "SELECT count(*) FROM search_documents WHERE owner_type = 'subtitle' AND owner_id = ?", int64(1), "vid-1")

	if err := importSubtitle(context.Background(), db, "vid-1", subtitleDoc{Language: "en", Source: "manual", Format: "vtt", Path: "subtitles/vid-1.en.vtt"}); err != nil {
		t.Fatal(err)
	}
	assertScalar(t, db, "SELECT text FROM subtitles WHERE video_id = ? AND language = ?", "", "vid-1", "en")
	assertScalar(t, db, "SELECT count(*) FROM search_documents WHERE owner_type = 'subtitle' AND owner_id = ?", int64(0), "vid-1")
}

func TestImportNestedTubeArchivistComments(t *testing.T) {
	t.Parallel()

	db := openImportDB(t)
	root := t.TempDir()
	writeNestedCommentBackupZip(t, root)

	report, err := Import(context.Background(), db, root)
	if err != nil {
		t.Fatal(err)
	}
	if report.Comments != 2 || len(report.Skipped) != 0 {
		t.Fatalf("unexpected nested comment report: %#v", report)
	}
	assertScalar(t, db, "SELECT parent_id FROM comments WHERE id = ?", sql.NullString{}, "comment-parent")
	assertScalar(t, db, "SELECT parent_id FROM comments WHERE id = ?", sql.NullString{String: "comment-parent", Valid: true}, "comment-reply")
	assertScalar(t, db, "SELECT published_at FROM comments WHERE id = ?", sql.NullString{String: "2026-05-03T12:00:00Z", Valid: true}, "comment-parent")
	assertScalar(t, db, "SELECT count(*) FROM search_documents WHERE owner_type = 'comment' AND text LIKE '%nested parent%'", int64(1))
}

func TestImportEmptyCommentDeletesExistingComment(t *testing.T) {
	t.Parallel()

	db := openImportDB(t)
	if _, err := db.Exec("INSERT INTO videos (id, external_id, title) VALUES ('vid-comments', 'vid-comments', 'Commented Video')"); err != nil {
		t.Fatal(err)
	}
	nonEmpty, err := json.Marshal(map[string]any{
		"comment_id":     "comment-empty",
		"youtube_id":     "vid-comments",
		"comment_text":   "visible comment",
		"comment_author": "Archivist",
	})
	if err != nil {
		t.Fatal(err)
	}
	if count, err := importComment(context.Background(), db, nonEmpty); err != nil || count != 1 {
		t.Fatalf("expected initial comment import, count=%d err=%v", count, err)
	}
	assertScalar(t, db, "SELECT text FROM comments WHERE id = ?", "visible comment", "comment-empty")
	assertScalar(t, db, "SELECT count(*) FROM search_documents WHERE owner_type = 'comment' AND owner_id = ?", int64(1), "comment-empty")

	empty, err := json.Marshal(map[string]any{
		"comment_id":     "comment-empty",
		"youtube_id":     "vid-comments",
		"comment_text":   "   ",
		"comment_author": "Archivist",
	})
	if err != nil {
		t.Fatal(err)
	}
	if count, err := importComment(context.Background(), db, empty); err != nil || count != 0 {
		t.Fatalf("expected empty comment reimport to delete only, count=%d err=%v", count, err)
	}

	assertScalar(t, db, "SELECT count(*) FROM comments WHERE id = ?", int64(0), "comment-empty")
	assertScalar(t, db, "SELECT count(*) FROM search_documents WHERE owner_type = 'comment' AND owner_id = ?", int64(0), "comment-empty")
}

func TestImportEmptyParentCommentCleansCascadedSearchDocuments(t *testing.T) {
	t.Parallel()

	db := openImportDB(t)
	if _, err := db.Exec("INSERT INTO videos (id, external_id, title) VALUES ('vid-comments', 'vid-comments', 'Commented Video')"); err != nil {
		t.Fatal(err)
	}
	nested, err := json.Marshal(map[string]any{
		"youtube_id": "vid-comments",
		"comment_comments": []map[string]any{
			{"comment_id": "comment-parent", "comment_parent": "root", "comment_text": "parent text"},
			{"comment_id": "comment-reply", "comment_parent": "comment-parent", "comment_text": "reply text"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if count, err := importComment(context.Background(), db, nested); err != nil || count != 2 {
		t.Fatalf("expected nested comment import, count=%d err=%v", count, err)
	}
	assertScalar(t, db, "SELECT count(*) FROM comments WHERE id IN ('comment-parent', 'comment-reply')", int64(2))
	assertScalar(t, db, "SELECT count(*) FROM search_documents WHERE owner_type = 'comment' AND owner_id IN ('comment-parent', 'comment-reply')", int64(2))

	emptyParent, err := json.Marshal(map[string]any{
		"comment_id":     "comment-parent",
		"youtube_id":     "vid-comments",
		"comment_text":   "",
		"comment_author": "Archivist",
	})
	if err != nil {
		t.Fatal(err)
	}
	if count, err := importComment(context.Background(), db, emptyParent); err != nil || count != 0 {
		t.Fatalf("expected empty parent reimport to delete only, count=%d err=%v", count, err)
	}

	assertScalar(t, db, "SELECT count(*) FROM comments WHERE id IN ('comment-parent', 'comment-reply')", int64(0))
	assertScalar(t, db, "SELECT count(*) FROM search_documents WHERE owner_type = 'comment' AND owner_id IN ('comment-parent', 'comment-reply')", int64(0))
}

func TestImportEmptyCommentDeleteRollsBackOnLaterFailure(t *testing.T) {
	t.Parallel()

	db := openImportDB(t)
	if _, err := db.Exec("INSERT INTO videos (id, external_id, title) VALUES ('vid-comments', 'vid-comments', 'Commented Video')"); err != nil {
		t.Fatal(err)
	}
	nonEmpty, err := json.Marshal(map[string]any{
		"comment_id":     "comment-keep",
		"youtube_id":     "vid-comments",
		"comment_text":   "keep comment",
		"comment_author": "Archivist",
	})
	if err != nil {
		t.Fatal(err)
	}
	if count, err := importComment(context.Background(), db, nonEmpty); err != nil || count != 1 {
		t.Fatalf("expected initial comment import, count=%d err=%v", count, err)
	}

	failing, err := json.Marshal(map[string]any{
		"youtube_id": "vid-comments",
		"comment_comments": []map[string]any{
			{"comment_id": "comment-keep", "comment_text": ""},
			{"comment_id": "comment-invalid", "comment_text": "invalid", "comment_likecount": -1},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if count, err := importComment(context.Background(), db, failing); err == nil || count != 0 {
		t.Fatalf("expected failed mixed comment import, count=%d err=%v", count, err)
	}

	assertScalar(t, db, "SELECT text FROM comments WHERE id = ?", "keep comment", "comment-keep")
	assertScalar(t, db, "SELECT count(*) FROM search_documents WHERE owner_type = 'comment' AND owner_id = ?", int64(1), "comment-keep")
}

func TestImportNestedCommentsRollsBackPartialSourceOnFailure(t *testing.T) {
	t.Parallel()

	db := openImportDB(t)
	if _, err := db.Exec("INSERT INTO videos (id, external_id, title) VALUES ('vid-comments', 'vid-comments', 'Commented Video')"); err != nil {
		t.Fatal(err)
	}
	source, err := json.Marshal(map[string]any{
		"youtube_id": "vid-comments",
		"comment_comments": []map[string]any{
			{
				"comment_id":     "comment-parent",
				"comment_parent": "root",
				"comment_text":   "partial parent",
			},
			{
				"comment_id":        "comment-reply",
				"comment_parent":    "comment-parent",
				"comment_text":      "invalid reply",
				"comment_likecount": -1,
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	count, err := importComment(context.Background(), db, source)
	if err == nil {
		t.Fatal("expected invalid nested comment to fail")
	}
	if count != 0 {
		t.Fatalf("expected no imported count after rollback, got %d", count)
	}
	assertScalar(t, db, "SELECT count(*) FROM comments WHERE id IN ('comment-parent', 'comment-reply')", int64(0))
	assertScalar(t, db, "SELECT count(*) FROM search_documents WHERE owner_type = 'comment' AND text LIKE '%partial parent%'", int64(0))
}

func TestEnqueueJobSuppressesConcurrentDuplicateImportRoots(t *testing.T) {
	t.Parallel()

	db := openImportDB(t)
	store := jobs.NewStore(db)
	root := t.TempDir()
	payloads := []Payload{{Root: root}, {Root: root + string(os.PathSeparator) + "."}}
	ids := make(chan string, len(payloads)*4)
	errs := make(chan error, len(payloads)*4)
	var wg sync.WaitGroup
	for i := range len(payloads) * 4 {
		wg.Add(1)
		go func(payload Payload) {
			defer wg.Done()
			job, err := EnqueueJob(context.Background(), store, payload)
			if err != nil {
				errs <- err
				return
			}
			ids <- job.ID
		}(payloads[i%len(payloads)])
	}
	wg.Wait()
	close(ids)
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}

	var first string
	for id := range ids {
		if first == "" {
			first = id
			continue
		}
		if id != first {
			t.Fatalf("expected duplicate enqueue to return %q, got %q", first, id)
		}
	}
	assertScalar(t, db, "SELECT count(*) FROM jobs WHERE type = ?", int64(1), JobType)
}

func openImportDB(t *testing.T) *sql.DB {
	t.Helper()

	db, err := database.Open(context.Background(), filepath.Join(t.TempDir(), "kapsel.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := database.Migrate(context.Background(), db); err != nil {
		t.Fatal(err)
	}

	return db
}

type playlistBackupEntry struct {
	VideoID   string
	Position  int
	SkipVideo bool
}

func writePlaylistBackupZip(t *testing.T, root string, playlistID string, entries []playlistBackupEntry) {
	t.Helper()

	backupPath := filepath.Join(root, "cache", "backup", "ta_backup-20260503-playlist.zip")
	if err := os.MkdirAll(filepath.Dir(backupPath), 0o755); err != nil {
		t.Fatal(err)
	}
	file, err := os.Create(backupPath)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	zipFile := zip.NewWriter(file)
	defer zipFile.Close()

	videoDocs := make([]string, 0, len(entries))
	playlistEntries := make([]map[string]any, 0, len(entries))
	seenVideos := map[string]bool{}
	for _, entry := range entries {
		if !entry.SkipVideo && !seenVideos[entry.VideoID] {
			videoDocs = append(videoDocs, bulkDocument(entry.VideoID, map[string]any{
				"youtube_id": entry.VideoID,
				"title":      "Video " + entry.VideoID,
			}))
			seenVideos[entry.VideoID] = true
		}
		playlistEntries = append(playlistEntries, map[string]any{"youtube_id": entry.VideoID, "idx": entry.Position, "downloaded": true})
	}
	if len(videoDocs) > 0 {
		writeZipFile(t, zipFile, "es_video-20260503-playlist.json", strings.Join(videoDocs, "\n"))
	}
	writeZipFile(t, zipFile, "es_playlist-20260503-playlist.json", bulkDocument(playlistID, map[string]any{
		"playlist_id":      playlistID,
		"playlist_name":    "Playlist " + playlistID,
		"playlist_entries": playlistEntries,
	}))
}

func writeUnsafeThumbnailBackupZip(t *testing.T, root string) {
	t.Helper()

	backupPath := filepath.Join(root, "cache", "backup", "ta_backup-20260503-unsafe.zip")
	if err := os.MkdirAll(filepath.Dir(backupPath), 0o755); err != nil {
		t.Fatal(err)
	}
	file, err := os.Create(backupPath)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	zipFile := zip.NewWriter(file)
	defer zipFile.Close()

	writeZipFile(t, zipFile, "es_video-20260503-0.json", bulkDocument("vid-unsafe", map[string]any{
		"youtube_id":    "vid-unsafe",
		"title":         "Unsafe Thumbnail",
		"media_url":     "media/vid-unsafe.mp4",
		"vid_thumb_url": "/../../secret.jpg",
	}))
}

func writeRemoteThumbnailBackupZip(t *testing.T, root string) {
	t.Helper()

	backupPath := filepath.Join(root, "cache", "backup", "ta_backup-20260503-remote-thumbnails.zip")
	if err := os.MkdirAll(filepath.Dir(backupPath), 0o755); err != nil {
		t.Fatal(err)
	}
	file, err := os.Create(backupPath)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	zipFile := zip.NewWriter(file)
	defer zipFile.Close()

	writeZipFile(t, zipFile, "es_channel-20260503-remote.json", bulkDocument("chan-remote", map[string]any{
		"channel_id":        "chan-remote",
		"channel_name":      "Remote Thumbnail Channel",
		"channel_thumb_url": "https://yt3.googleusercontent.com/archive-workshop.jpg",
	}))
	writeZipFile(t, zipFile, "es_video-20260503-remote.json", bulkDocument("vid-remote", map[string]any{
		"youtube_id":    "vid-remote",
		"title":         "Remote Thumbnail Video",
		"media_url":     "media/vid-remote.mp4",
		"vid_thumb_url": "https://i.ytimg.com/vi/vid-remote/maxresdefault.jpg",
		"channel": map[string]any{
			"channel_id":   "chan-remote",
			"channel_name": "Remote Thumbnail Channel",
		},
	}))
}

func writeNestedCommentBackupZip(t *testing.T, root string) {
	t.Helper()

	backupPath := filepath.Join(root, "cache", "backup", "ta_backup-20260503-comments.zip")
	if err := os.MkdirAll(filepath.Dir(backupPath), 0o755); err != nil {
		t.Fatal(err)
	}
	file, err := os.Create(backupPath)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	zipFile := zip.NewWriter(file)
	defer zipFile.Close()

	writeZipFile(t, zipFile, "es_video-20260503-0.json", bulkDocument("vid-comments", map[string]any{
		"youtube_id": "vid-comments",
		"title":      "Commented Video",
	}))
	writeZipFile(t, zipFile, "es_comment-20260503-0.json", bulkDocument("vid-comments", map[string]any{
		"youtube_id": "vid-comments",
		"comment_comments": []map[string]any{
			{
				"comment_id":     "comment-reply",
				"comment_parent": "comment-parent",
				"comment_author": "Viewer",
				"comment_text":   "nested reply",
			},
			{
				"comment_id":        "comment-parent",
				"comment_parent":    "root",
				"comment_author":    "Archivist",
				"comment_text":      "nested parent",
				"comment_timestamp": 1777809600,
			},
		},
	}))
}

func writeOversizedBackupZip(t *testing.T, root string) {
	t.Helper()

	backupPath := filepath.Join(root, "cache", "backup", "ta_backup-20260503-oversized.zip")
	if err := os.MkdirAll(filepath.Dir(backupPath), 0o755); err != nil {
		t.Fatal(err)
	}
	file, err := os.Create(backupPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = file.Close() })
	zipFile := zip.NewWriter(file)
	defer zipFile.Close()

	writeZipFile(t, zipFile, "es_video-20260503-oversized.json", strings.Repeat("x", int(maxImportEntryBytes)+1))
}

func writeBackupZip(t *testing.T, root string) {
	t.Helper()

	backupPath := filepath.Join(root, "cache", "backup", "ta_backup-20260503-test.zip")
	if err := os.MkdirAll(filepath.Dir(backupPath), 0o755); err != nil {
		t.Fatal(err)
	}
	file, err := os.Create(backupPath)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	zipFile := zip.NewWriter(file)
	defer zipFile.Close()

	writeZipFile(t, zipFile, "es_channel-20260503-0.json", bulkDocument("chan-1", map[string]any{
		"channel_id":          "chan-1",
		"channel_name":        "Archive Workshop",
		"channel_description": "A channel about archives",
		"channel_subscribed":  true,
		"channel_thumb_url":   "/cache/channels/chan-1_thumb.jpg",
	}))
	writeZipFile(t, zipFile, "es_video-20260503-0.json", bulkDocument("vid-1", map[string]any{
		"youtube_id":      "vid-1",
		"title":           "Kapsel Demo",
		"description":     "A demo video",
		"published":       "2026-05-03",
		"date_downloaded": "2026-05-04T10:11:12Z",
		"media_url":       "media/vid-1.mp4",
		"vid_thumb_url":   "/cache/videos/vid-1.jpg",
		"channel": map[string]any{
			"channel_id":   "chan-1",
			"channel_name": "Archive Workshop",
		},
		"player": map[string]any{
			"duration": 120,
			"position": 42,
			"watched":  false,
		},
		"stats": map[string]any{
			"view_count": 4321,
		},
		"subtitles": []map[string]any{
			{"lang": "en", "name": "English", "source": "manual", "format": "vtt", "path": "subtitles/vid-1.en.vtt", "text": "A quiet lunar capsule floats past the archive."},
			{"lang": "ja", "name": "Japanese", "source": "manual", "format": "vtt", "path": "subtitles/vid-1.ja.vtt"},
		},
	})+"\n"+`{"index":{"_index":"ta_video","_id":"bad"}}`+"\n"+`{"broken":`)
	writeZipFile(t, zipFile, "es_playlist-20260503-0.json", bulkDocument("playlist-1", map[string]any{
		"playlist_id":          "playlist-1",
		"playlist_name":        "Saved Clips",
		"playlist_description": "Useful videos",
		"playlist_subscribed":  true,
		"playlist_channel_id":  "chan-1",
		"playlist_entries": []map[string]any{
			{"youtube_id": "vid-1", "idx": 0, "downloaded": true},
		},
	}))
	writeZipFile(t, zipFile, "es_comment-20260503-0.json", bulkDocument("comment-1", map[string]any{
		"comment_id":        "comment-1",
		"youtube_id":        "vid-1",
		"comment_author":    "Archivist",
		"comment_text":      "A preserved cabinet of comments",
		"comment_published": "2026-05-03T12:00:00Z",
		"comment_likecount": 7,
	})+"\n"+bulkDocument("comment-2", map[string]any{
		"comment_id":     "comment-2",
		"youtube_id":     "vid-1",
		"comment_parent": "comment-1",
		"comment_author": "Viewer",
		"comment_text":   "Reply from the archive",
	}))
}

func writeZipFile(t *testing.T, zipFile *zip.Writer, name string, body string) {
	t.Helper()

	writer, err := zipFile.Create(name)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Write([]byte(body)); err != nil {
		t.Fatal(err)
	}
}

func bulkDocument(id string, source map[string]any) string {
	action, _ := json.Marshal(map[string]any{"index": map[string]any{"_index": "ta_backup", "_id": id}})
	body, _ := json.Marshal(source)

	return string(action) + "\n" + string(body)
}

func assertScalar[T comparable](t *testing.T, db *sql.DB, query string, expected T, args ...any) {
	t.Helper()

	var got T
	if err := db.QueryRow(query, args...).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != expected {
		t.Fatalf("expected %v, got %v", expected, got)
	}
}
