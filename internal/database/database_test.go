package database

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"kapsel/internal/jobs"
	"kapsel/internal/server"
)

func TestOpenEnablesWAL(t *testing.T) {
	t.Parallel()

	db := openTestDB(t)

	var journalMode string
	if err := db.QueryRow("PRAGMA journal_mode").Scan(&journalMode); err != nil {
		t.Fatal(err)
	}

	if journalMode != "wal" {
		t.Fatalf("expected journal_mode %q, got %q", "wal", journalMode)
	}
}

func TestOpenConfiguresPooledConnections(t *testing.T) {
	t.Parallel()

	db := openTestDB(t)
	maxOpen := db.Stats().MaxOpenConnections
	if maxOpen < 2 {
		t.Fatalf("expected a bounded pool with concurrent connections, got max open %d", maxOpen)
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	conns := make([]*sql.Conn, 0, maxOpen)
	for range maxOpen {
		conn, err := db.Conn(ctx)
		if err != nil {
			t.Fatal(err)
		}
		conns = append(conns, conn)
		defer conn.Close()
	}

	for _, conn := range conns {
		var foreignKeys int
		if err := conn.QueryRowContext(ctx, "PRAGMA foreign_keys").Scan(&foreignKeys); err != nil {
			t.Fatal(err)
		}
		if foreignKeys != 1 {
			t.Fatalf("expected foreign_keys enabled on every connection, got %d", foreignKeys)
		}

		var busyTimeout int
		if err := conn.QueryRowContext(ctx, "PRAGMA busy_timeout").Scan(&busyTimeout); err != nil {
			t.Fatal(err)
		}
		if busyTimeout != 5000 {
			t.Fatalf("expected busy_timeout 5000 on every connection, got %d", busyTimeout)
		}

		var journalMode string
		if err := conn.QueryRowContext(ctx, "PRAGMA journal_mode").Scan(&journalMode); err != nil {
			t.Fatal(err)
		}
		if journalMode != "wal" {
			t.Fatalf("expected journal_mode %q on every connection, got %q", "wal", journalMode)
		}
	}
}

func TestMigrateCreatesInitialSchema(t *testing.T) {
	t.Parallel()

	db := openTestDB(t)
	migrations := loadMigrationsForTest(t)
	if err := Migrate(context.Background(), db); err != nil {
		t.Fatal(err)
	}

	for _, table := range []string{
		"schema_migrations",
		"channels",
		"videos",
		"playlists",
		"playlist_entries",
		"downloads",
		"user_progress",
		"jobs",
		"settings",
		"media_assets",
		"video_previews",
		"subtitles",
		"comments",
		"sponsorblock_cache",
		"sponsorblock_segments",
		"search_documents",
		"search_documents_fts",
	} {
		assertTableExists(t, db, table)
	}

	for _, index := range []string{
		"idx_videos_channel_id",
		"idx_videos_published_at",
		"idx_videos_view_count",
		"idx_playlist_entries_playlist_position",
		"idx_downloads_status_priority",
		"idx_downloads_source_external_id",
		"idx_user_progress_video_id",
		"idx_jobs_status_priority",
		"idx_media_assets_owner",
		"idx_video_previews_sprite_path",
		"idx_subtitles_video_language",
		"idx_comments_video_parent",
		"idx_sponsorblock_segments_video",
		"idx_search_documents_owner",
	} {
		assertIndexExists(t, db, index)
	}

	var version int
	if err := db.QueryRow("SELECT version FROM schema_migrations ORDER BY version DESC LIMIT 1").Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != migrations[len(migrations)-1].version {
		t.Fatalf("expected schema version %d, got %d", migrations[len(migrations)-1].version, version)
	}
}

func TestMigrateBackfillsVideoChannelSearchDocs(t *testing.T) {
	t.Parallel()

	db := openTestDB(t)
	migrations := loadMigrationsForTest(t)
	for _, migration := range migrations {
		if migration.version >= 17 {
			break
		}
		applyMigrationForTest(t, db, migration)
	}
	if _, err := db.Exec(`
INSERT INTO channels (id, external_id, name) VALUES ('chan-1', 'chan-1', 'Island Workshop');
INSERT INTO videos (id, external_id, channel_id, title) VALUES
  ('vid-1', 'vid-1', 'chan-1', 'Remote island hike'),
  ('vid-2', 'vid-2', 'chan-1', 'Cabin build'),
  ('vid-3', 'vid-3', NULL, 'Orphan video');
INSERT INTO search_documents (owner_type, owner_id, field, text) VALUES
  ('video', 'vid-1', 'title', 'Remote island hike'),
  ('video', 'vid-2', 'channel', 'Stale Channel Name Cabin build')`); err != nil {
		t.Fatal(err)
	}

	if err := Migrate(context.Background(), db); err != nil {
		t.Fatal(err)
	}

	assertScalar(t, db, "SELECT text FROM search_documents WHERE owner_type = 'video' AND owner_id = ? AND field = 'channel'", "Island Workshop Remote island hike", "vid-1")
	assertScalar(t, db, "SELECT text FROM search_documents WHERE owner_type = 'video' AND owner_id = ? AND field = 'channel'", "Island Workshop Cabin build", "vid-2")
	assertScalar(t, db, "SELECT count(*) FROM search_documents WHERE owner_type = 'video' AND owner_id = ? AND field = 'channel'", int64(0), "vid-3")
}

func TestMigrateDeduplicatesDownloadsBeforeUniqueIndex(t *testing.T) {
	t.Parallel()

	db := openTestDB(t)
	migrations := loadMigrationsForTest(t)
	for _, migration := range migrations {
		if migration.version >= 4 {
			break
		}
		applyMigrationForTest(t, db, migration)
	}
	if _, err := db.Exec("INSERT INTO downloads (external_id, url, status) VALUES (?, ?, ?), (?, ?, ?)", "vid-1", "https://example.com/old", "succeeded", "vid-1", "https://example.com/new", "succeeded"); err != nil {
		t.Fatal(err)
	}

	if err := Migrate(context.Background(), db); err != nil {
		t.Fatal(err)
	}

	var count int
	if err := db.QueryRow("SELECT count(*) FROM downloads WHERE source = 'youtube' AND external_id = 'vid-1'").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("expected duplicate downloads to be deduplicated, got %d", count)
	}
	assertScalar(t, db, "SELECT url FROM downloads WHERE source = 'youtube' AND external_id = 'vid-1'", "https://example.com/new")
	assertIndexExists(t, db, "idx_downloads_source_external_id")
	if _, err := db.Exec("INSERT INTO downloads (external_id, url, status) VALUES (?, ?, ?)", "vid-1", "https://example.com/duplicate", "succeeded"); err == nil {
		t.Fatal("expected unique download source/external_id constraint")
	}
}

func TestMigrateDeduplicatesDownloadsPrefersSucceededRows(t *testing.T) {
	t.Parallel()

	db := openTestDB(t)
	migrations := loadMigrationsForTest(t)
	for _, migration := range migrations {
		if migration.version >= 4 {
			break
		}
		applyMigrationForTest(t, db, migration)
	}
	if _, err := db.Exec(`
INSERT INTO downloads (external_id, url, status, error, updated_at) VALUES
  ('vid-best', 'https://example.com/succeeded', 'succeeded', '', '2026-05-01T00:00:00Z'),
  ('vid-best', 'https://example.com/failed-newer', 'failed', 'newer failed retry', '2026-05-02T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}

	if err := Migrate(context.Background(), db); err != nil {
		t.Fatal(err)
	}

	assertScalar(t, db, "SELECT count(*) FROM downloads WHERE source = 'youtube' AND external_id = 'vid-best'", int64(1))
	assertScalar(t, db, "SELECT status FROM downloads WHERE source = 'youtube' AND external_id = 'vid-best'", "succeeded")
	assertScalar(t, db, "SELECT url FROM downloads WHERE source = 'youtube' AND external_id = 'vid-best'", "https://example.com/succeeded")
}

func TestMigrateBackfillsVideoMediaOrigin(t *testing.T) {
	t.Parallel()

	db := openTestDB(t)
	migrations := loadMigrationsForTest(t)
	for _, migration := range migrations {
		if migration.version >= 12 {
			break
		}
		applyMigrationForTest(t, db, migration)
	}
	if _, err := db.Exec(`
INSERT INTO videos (id, external_id, title, media_path, archived_at, updated_at) VALUES
  ('manual-video', 'manual-video', 'Manual Video', 'videos/manual.mp4', '2026-05-01T00:00:00Z', '2026-05-01T00:00:00Z'),
  ('auto-video', 'auto-video', 'Auto Video', 'videos/auto.mp4', '2026-05-02T00:00:00Z', '2026-05-02T00:00:00Z'),
  ('imported-video', 'imported-video', 'Imported Video', 'videos/imported.mp4', '2026-05-03T00:00:00Z', '2026-05-03T00:00:00Z'),
  ('catalog-video', 'catalog-video', 'Catalog Video', '', NULL, '2026-05-04T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
INSERT INTO downloads (video_id, source, external_id, url, status, origin, updated_at) VALUES
  ('manual-video', 'youtube', 'manual-video', 'https://example.com/manual', 'succeeded', 'manual', '2026-05-11T00:00:00Z'),
  ('auto-video', 'youtube', 'auto-video', 'https://example.com/auto', 'succeeded', 'channel_auto', '2026-05-12T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}

	if err := Migrate(context.Background(), db); err != nil {
		t.Fatal(err)
	}

	assertScalar(t, db, "SELECT media_origin FROM videos WHERE id = ?", "manual", "manual-video")
	assertScalar(t, db, "SELECT media_downloaded_at FROM videos WHERE id = ?", "2026-05-11T00:00:00Z", "manual-video")
	assertScalar(t, db, "SELECT media_origin FROM videos WHERE id = ?", "channel_auto", "auto-video")
	assertScalar(t, db, "SELECT media_downloaded_at FROM videos WHERE id = ?", "2026-05-12T00:00:00Z", "auto-video")
	assertScalar(t, db, "SELECT media_origin FROM videos WHERE id = ?", "imported", "imported-video")
	assertScalar(t, db, "SELECT media_downloaded_at FROM videos WHERE id = ?", "2026-05-03T00:00:00Z", "imported-video")
	assertScalar(t, db, "SELECT media_origin FROM videos WHERE id = ?", "imported", "catalog-video")
	assertScalar(t, db, "SELECT media_downloaded_at FROM videos WHERE id = ?", "", "catalog-video")
}

func TestMigrateAddsJobResultCommittedMarker(t *testing.T) {
	t.Parallel()

	db := openTestDB(t)
	migrations := loadMigrationsForTest(t)
	for _, migration := range migrations {
		if migration.version >= 14 {
			break
		}
		applyMigrationForTest(t, db, migration)
	}
	if _, err := db.Exec(`
INSERT INTO jobs (id, type, payload_json, status, result_json, created_at, updated_at) VALUES
  ('succeeded-result', 'scan', '{}', 'succeeded', '{"video_id":"done"}', '2026-05-01T00:00:00Z', '2026-05-01T00:00:00Z'),
  ('failed-result', 'scan', '{}', 'failed', '{"video_id":"unsafe"}', '2026-05-01T00:00:00Z', '2026-05-01T00:00:00Z'),
  ('running-result', 'scan', '{}', 'running', '{"partial":true}', '2026-05-01T00:00:00Z', '2026-05-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}

	if err := Migrate(context.Background(), db); err != nil {
		t.Fatal(err)
	}

	assertScalar(t, db, "SELECT result_committed FROM jobs WHERE id = ?", int64(1), "succeeded-result")
	assertScalar(t, db, "SELECT result_committed FROM jobs WHERE id = ?", int64(1), "failed-result")
	assertScalar(t, db, "SELECT result_committed FROM jobs WHERE id = ?", int64(0), "running-result")
}

func TestMigrateUpgradesOlderSchemaVersion(t *testing.T) {
	t.Parallel()

	db := openTestDB(t)
	migrations := loadMigrationsForTest(t)
	applyMigrationForTest(t, db, migrations[0])

	if err := Migrate(context.Background(), db); err != nil {
		t.Fatal(err)
	}

	var version int
	if err := db.QueryRow("SELECT version FROM schema_migrations ORDER BY version DESC LIMIT 1").Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != migrations[len(migrations)-1].version {
		t.Fatalf("expected upgraded schema version %d, got %d", migrations[len(migrations)-1].version, version)
	}

	var count int
	if err := db.QueryRow("SELECT count(*) FROM schema_migrations").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != len(migrations) {
		t.Fatalf("expected %d migration records, got %d", len(migrations), count)
	}
}

func TestMigrateRejectsNewerSchemaVersion(t *testing.T) {
	t.Parallel()

	db := openTestDB(t)
	migrations := loadMigrationsForTest(t)
	if err := Migrate(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	futureVersion := migrations[len(migrations)-1].version + 1
	if _, err := db.Exec("INSERT INTO schema_migrations (version, name) VALUES (?, ?)", futureVersion, "future"); err != nil {
		t.Fatal(err)
	}

	err := Migrate(context.Background(), db)
	if err == nil {
		t.Fatalf("expected schema version %d to be rejected", futureVersion)
	}
	if !strings.Contains(err.Error(), "newer than this binary") {
		t.Fatalf("expected newer schema error, got %v", err)
	}
}

func TestMigrateRejectsNonContiguousSchemaVersion(t *testing.T) {
	t.Parallel()

	db := openTestDB(t)
	migrations := loadMigrationsForTest(t)
	if len(migrations) < 3 {
		t.Fatalf("expected at least three migrations for gap test, got %d", len(migrations))
	}
	applyMigrationForTest(t, db, migrations[0])
	if _, err := db.Exec("INSERT INTO schema_migrations (version, name) VALUES (?, ?)", migrations[2].version, migrations[2].name); err != nil {
		t.Fatal(err)
	}

	err := Migrate(context.Background(), db)
	if err == nil {
		t.Fatal("expected non-contiguous migration history to be rejected")
	}
	if !strings.Contains(err.Error(), "not contiguous") {
		t.Fatalf("expected non-contiguous schema error, got %v", err)
	}
}

func TestMigrateRejectsMismatchedSchemaMigrationName(t *testing.T) {
	t.Parallel()

	db := openTestDB(t)
	migrations := loadMigrationsForTest(t)
	applyMigrationForTest(t, db, migrations[0])
	if _, err := db.Exec("UPDATE schema_migrations SET name = ? WHERE version = ?", "renamed", migrations[0].version); err != nil {
		t.Fatal(err)
	}

	err := Migrate(context.Background(), db)
	if err == nil {
		t.Fatal("expected mismatched migration name to be rejected")
	}
	if !strings.Contains(err.Error(), "expected") {
		t.Fatalf("expected migration name mismatch error, got %v", err)
	}
}

func TestValidateAppliedMigrationsRejectsUnknownSupportedVersion(t *testing.T) {
	t.Parallel()

	err := validateAppliedMigrations(
		map[int]string{1: "one", 2: "removed"},
		[]migration{{version: 1, name: "one"}, {version: 3, name: "three"}},
		3,
	)
	if err == nil {
		t.Fatal("expected unknown migration version to be rejected")
	}
	if !strings.Contains(err.Error(), "not recognized") {
		t.Fatalf("expected unknown migration version error, got %v", err)
	}
}

func TestMigrateIsIdempotent(t *testing.T) {
	t.Parallel()

	db := openTestDB(t)
	migrations := loadMigrationsForTest(t)
	for range 2 {
		if err := Migrate(context.Background(), db); err != nil {
			t.Fatal(err)
		}
	}

	var count int
	if err := db.QueryRow("SELECT count(*) FROM schema_migrations").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != len(migrations) {
		t.Fatalf("expected %d migration records, got %d", len(migrations), count)
	}
}

func TestConcurrentJobAndHTTPAccessDoesNotLock(t *testing.T) {
	t.Parallel()

	db := openTestDB(t)
	if err := Migrate(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	seedVideoForConcurrencyTest(t, db)

	store := jobs.NewStore(db)
	handler := server.NewHandler(server.WithDatabase(db), server.WithJobs(store))
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	const workers = 8
	const iterations = 30
	errs := make(chan error, workers*iterations*2)
	claimedIDs := map[string]bool{}
	var claimedMu sync.Mutex
	var wg sync.WaitGroup
	recordClaim := func(id string) error {
		claimedMu.Lock()
		defer claimedMu.Unlock()
		if claimedIDs[id] {
			return fmt.Errorf("job %s claimed more than once", id)
		}
		claimedIDs[id] = true

		return nil
	}

	for worker := range workers {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			for iteration := range iterations {
				_, err := store.Enqueue(ctx, jobs.EnqueueParams{Type: "stress", PayloadJSON: fmt.Sprintf(`{"worker":%d,"iteration":%d}`, worker, iteration)})
				if err != nil {
					errs <- err
					continue
				}

				claimed, ok, err := store.Claim(ctx, time.Now(), time.Minute)
				if err != nil {
					errs <- err
					continue
				}
				if !ok {
					continue
				}
				if err := recordClaim(claimed.ID); err != nil {
					errs <- err
					continue
				}
				if err := store.Heartbeat(ctx, claimed.ID, 0.5); err != nil {
					errs <- err
					continue
				}
				if err := store.Complete(ctx, claimed.ID); err != nil {
					errs <- err
				}
			}
		}(worker)
	}

	for worker := range workers {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			for iteration := range iterations {
				req := httptest.NewRequest(http.MethodGet, "/api/videos?page_size=5", nil).WithContext(ctx)
				rec := httptest.NewRecorder()
				handler.ServeHTTP(rec, req)
				if rec.Code != http.StatusOK {
					errs <- fmt.Errorf("GET /api/videos returned %d: %s", rec.Code, rec.Body.String())
					continue
				}

				req = httptest.NewRequest(http.MethodPost, "/api/downloads", strings.NewReader(fmt.Sprintf(`{"url":"https://www.youtube.com/watch?v=%011d"}`, worker*iterations+iteration))).WithContext(ctx)
				rec = httptest.NewRecorder()
				handler.ServeHTTP(rec, req)
				if rec.Code != http.StatusAccepted {
					errs <- fmt.Errorf("POST /api/downloads returned %d: %s", rec.Code, rec.Body.String())
				}
			}
		}(worker)
	}

	wg.Wait()
	close(errs)
	for err := range errs {
		if strings.Contains(err.Error(), "database is locked") {
			t.Fatalf("concurrent access hit SQLite lock error: %v", err)
		}
		t.Fatal(err)
	}
}

func openTestDB(t *testing.T) *sql.DB {
	t.Helper()

	db, err := Open(context.Background(), filepath.Join(t.TempDir(), "kapsel.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Fatal(err)
		}
	})

	return db
}

func loadMigrationsForTest(t *testing.T) []migration {
	t.Helper()

	migrations, err := loadMigrations()
	if err != nil {
		t.Fatal(err)
	}
	if len(migrations) == 0 {
		t.Fatal("expected embedded migrations")
	}

	return migrations
}

func applyMigrationForTest(t *testing.T, db *sql.DB, migration migration) {
	t.Helper()

	if _, err := db.Exec(`
CREATE TABLE IF NOT EXISTS schema_migrations (
  version INTEGER PRIMARY KEY,
  name TEXT NOT NULL,
  applied_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(migration.sql); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO schema_migrations (version, name) VALUES (?, ?)", migration.version, migration.name); err != nil {
		t.Fatal(err)
	}
}

func seedVideoForConcurrencyTest(t *testing.T, db *sql.DB) {
	t.Helper()

	if _, err := db.Exec("INSERT INTO channels (id, external_id, name) VALUES (?, ?, ?)", "channel-1", "external-channel-1", "Channel One"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO videos (id, external_id, channel_id, title, published_at) VALUES (?, ?, ?, ?, ?)", "video-1", "external-video-1", "channel-1", "Concurrent Video", "2026-05-03T00:00:00.000Z"); err != nil {
		t.Fatal(err)
	}
}

func assertTableExists(t *testing.T, db *sql.DB, table string) {
	t.Helper()

	var exists bool
	if err := db.QueryRow(
		"SELECT EXISTS(SELECT 1 FROM sqlite_schema WHERE type = 'table' AND name = ?)",
		table,
	).Scan(&exists); err != nil {
		t.Fatal(err)
	}
	if !exists {
		t.Fatalf("expected table %q to exist", table)
	}
}

func assertIndexExists(t *testing.T, db *sql.DB, index string) {
	t.Helper()

	var exists bool
	if err := db.QueryRow(
		"SELECT EXISTS(SELECT 1 FROM sqlite_schema WHERE type = 'index' AND name = ?)",
		index,
	).Scan(&exists); err != nil {
		t.Fatal(err)
	}
	if !exists {
		t.Fatalf("expected index %q to exist", index)
	}
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
