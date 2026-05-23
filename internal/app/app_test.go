package app

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"kapsel/internal/applock"
	"kapsel/internal/config"
	"kapsel/internal/jobs"
	"kapsel/internal/media"
)

func TestNewCreatesRuntimeAndRunsMigrations(t *testing.T) {
	t.Parallel()

	dataDir := t.TempDir()
	cfg := config.Config{
		Addr:               "127.0.0.1:0",
		AuthMode:           "disabled",
		DataDir:            dataDir,
		DBPath:             filepath.Join(dataDir, "db", "kapsel.db"),
		MediaRoot:          filepath.Join(dataDir, "media"),
		MediaSigningSecret: "secret",
		YTDLPPath:          "yt-dlp",
	}

	application, err := New(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = application.Close() })

	if _, err := os.Stat(cfg.DBPath); err != nil {
		t.Fatalf("expected database file to exist: %v", err)
	}
	if info, err := os.Stat(cfg.MediaRoot); err != nil || !info.IsDir() {
		t.Fatalf("expected media root directory: info=%v err=%v", info, err)
	}

	var migrationCount int
	if err := application.DB.QueryRow("SELECT count(*) FROM schema_migrations").Scan(&migrationCount); err != nil {
		t.Fatal(err)
	}
	if migrationCount == 0 {
		t.Fatal("expected migrations to be applied")
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/health", nil)
	application.Handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected health status %d, got %d", http.StatusOK, rec.Code)
	}
}

func TestNewRequiresExclusiveDatabaseLock(t *testing.T) {
	t.Parallel()

	dataDir := t.TempDir()
	cfg := config.Config{
		Addr:               "127.0.0.1:0",
		AuthMode:           "disabled",
		DataDir:            dataDir,
		DBPath:             filepath.Join(dataDir, "kapsel.db"),
		MediaRoot:          filepath.Join(dataDir, "media"),
		MediaSigningSecret: "secret",
		YTDLPPath:          "yt-dlp",
	}
	application, err := New(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}

	second, err := New(context.Background(), cfg)
	if !errors.Is(err, applock.ErrLocked) {
		if second != nil {
			_ = second.Close()
		}
		t.Fatalf("expected lock error for second app, got %v", err)
	}
	if err := application.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := New(context.Background(), cfg)
	if err != nil {
		t.Fatalf("expected lock to be released after close: %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
}

func TestNewWiresJobsAndMedia(t *testing.T) {
	t.Parallel()

	dataDir := t.TempDir()
	cfg := config.Config{
		Addr:               "127.0.0.1:0",
		AuthMode:           "disabled",
		DataDir:            dataDir,
		DBPath:             filepath.Join(dataDir, "kapsel.db"),
		MediaRoot:          filepath.Join(dataDir, "media"),
		MediaSigningSecret: "secret",
		YTDLPPath:          "yt-dlp",
	}
	application, err := New(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = application.Close() })

	job, err := application.Jobs.Enqueue(context.Background(), jobs.EnqueueParams{Type: "download"})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/jobs/"+job.ID, nil)
	rec := httptest.NewRecorder()
	application.Handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected job status %d, got %d", http.StatusOK, rec.Code)
	}

	mediaPath := filepath.Join(cfg.MediaRoot, "sample.mp4")
	if err := os.WriteFile(mediaPath, []byte("0123456789"), 0o644); err != nil {
		t.Fatal(err)
	}
	signer := media.NewSigner(cfg.MediaSigningSecret)
	req = httptest.NewRequest(http.MethodGet, "/media/sample.mp4?"+signer.Query("sample.mp4", time.Now().Add(time.Hour)).Encode(), nil)
	req.Header.Set("Range", "bytes=0-3")
	rec = httptest.NewRecorder()

	application.Handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusPartialContent {
		t.Fatalf("expected media status %d, got %d", http.StatusPartialContent, rec.Code)
	}
}

func TestNewWiresDownloadRunner(t *testing.T) {
	t.Parallel()

	dataDir := t.TempDir()
	application, err := New(context.Background(), config.Config{
		Addr:               "127.0.0.1:0",
		AuthMode:           "disabled",
		DataDir:            dataDir,
		DBPath:             filepath.Join(dataDir, "kapsel.db"),
		MediaRoot:          filepath.Join(dataDir, "media"),
		MediaSigningSecret: "secret",
		YTDLPPath:          "yt-dlp",
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = application.Close() })

	if application.Runner == nil {
		t.Fatal("expected application job runner")
	}
}

func TestNewExposesSettingsWithoutRawSigningSecret(t *testing.T) {
	t.Parallel()

	dataDir := t.TempDir()
	application, err := New(context.Background(), config.Config{
		Addr:                         "127.0.0.1:0",
		AuthMode:                     "disabled",
		DataDir:                      dataDir,
		DBPath:                       filepath.Join(dataDir, "kapsel.db"),
		ImportRoot:                   filepath.Join(dataDir, "imports"),
		MediaRoot:                    filepath.Join(dataDir, "media"),
		MediaSigningSecret:           "supersecret",
		MediaSigningSecretConfigured: true,
		YTDLPPath:                    filepath.Join(dataDir, "missing-yt-dlp"),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = application.Close() })

	req := httptest.NewRequest(http.MethodGet, "/api/settings", nil)
	rec := httptest.NewRecorder()
	application.Handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected settings status %d, got %d", http.StatusOK, rec.Code)
	}
	body := rec.Body.String()
	if strings.Contains(body, "supersecret") || strings.Contains(body, "media_signing_secret") {
		t.Fatalf("expected settings response to avoid raw signing secret, got %s", body)
	}
	if !strings.Contains(body, `"media_signing_configured":true`) {
		t.Fatalf("expected settings response to show configured signing status, got %s", body)
	}
}

func TestNewProtectsMutatingEndpointsByDefault(t *testing.T) {
	t.Parallel()

	dataDir := t.TempDir()
	application, err := New(context.Background(), config.Config{
		Addr:               "127.0.0.1:0",
		AuthMode:           "required",
		DataDir:            dataDir,
		DBPath:             filepath.Join(dataDir, "kapsel.db"),
		ImportRoot:         filepath.Join(dataDir, "imports"),
		MediaRoot:          filepath.Join(dataDir, "media"),
		MediaSigningSecret: "secret",
		SessionSecret:      "session-secret",
		YTDLPPath:          "yt-dlp",
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = application.Close() })

	req := httptest.NewRequest(http.MethodPost, "/api/downloads", strings.NewReader(`{"url":"https://example.com/watch?v=abc"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	application.Handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected default auth protection status %d, got %d", http.StatusUnauthorized, rec.Code)
	}
}
