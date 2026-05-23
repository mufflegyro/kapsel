package main

import (
	"archive/zip"
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"kapsel/internal/auth"
	"kapsel/internal/config"
	"kapsel/internal/database"
	"kapsel/internal/storage"
	"kapsel/internal/taimport"
)

func TestRunImportTACommand(t *testing.T) {
	t.Parallel()

	dataDir := t.TempDir()
	importRoot := t.TempDir()
	writeCommandBackupZip(t, importRoot)
	cfg := config.Config{
		Addr:               "127.0.0.1:0",
		DataDir:            dataDir,
		DBPath:             filepath.Join(dataDir, "kapsel.db"),
		MediaRoot:          filepath.Join(dataDir, "media"),
		MediaSigningSecret: "secret",
		YTDLPPath:          "yt-dlp",
	}
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	code := runWithConfig(context.Background(), cfg, []string{"import-ta", importRoot}, strings.NewReader(""), &stdout, &stderr)

	if code != 0 {
		t.Fatalf("expected exit code 0, got %d stderr=%q", code, stderr.String())
	}
	var report taimport.Report
	if err := json.NewDecoder(&stdout).Decode(&report); err != nil {
		t.Fatal(err)
	}
	if report.Channels != 1 || report.Videos != 1 || report.Playlists != 1 || len(report.Skipped) != 1 {
		t.Fatalf("unexpected import report: %#v", report)
	}

	db := openCommandDB(t, cfg.DBPath)
	assertCommandScalar(t, db, "SELECT title FROM videos WHERE id = ?", "Kapsel Demo", "vid-1")
	assertCommandScalar(t, db, "SELECT position_seconds FROM user_progress WHERE video_id = ?", int64(42), "vid-1")
}

func TestRunHashPasswordCommand(t *testing.T) {
	t.Parallel()

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := runWithConfig(context.Background(), config.Config{}, []string{"hash-password"}, strings.NewReader("open sesame\n"), &stdout, &stderr)

	if code != 0 {
		t.Fatalf("expected exit code 0, got %d stderr=%q", code, stderr.String())
	}
	hash := strings.TrimSpace(stdout.String())
	if !strings.HasPrefix(hash, "$argon2id$") || !auth.VerifyPassword("open sesame", hash) {
		t.Fatalf("expected verifiable argon2id hash, got %q", hash)
	}
}

func TestProductionHTTPServerConfiguresTimeouts(t *testing.T) {
	t.Parallel()

	server := newHTTPServer("127.0.0.1:0", http.NewServeMux())

	if server.ReadHeaderTimeout != serverReadHeaderTimeout {
		t.Fatalf("expected read header timeout %s, got %s", serverReadHeaderTimeout, server.ReadHeaderTimeout)
	}
	if server.ReadTimeout != serverReadTimeout {
		t.Fatalf("expected read timeout %s, got %s", serverReadTimeout, server.ReadTimeout)
	}
	if server.WriteTimeout != serverWriteTimeout {
		t.Fatalf("expected write timeout %s, got %s", serverWriteTimeout, server.WriteTimeout)
	}
	if server.IdleTimeout != serverIdleTimeout {
		t.Fatalf("expected idle timeout %s, got %s", serverIdleTimeout, server.IdleTimeout)
	}
}

func TestRunBackupAndRestoreCommands(t *testing.T) {
	t.Parallel()

	dataDir := t.TempDir()
	cfg := config.Config{AuthMode: "required", AuthUsername: "kapsel", AuthPasswordHash: "supersecret-hash", DataDir: dataDir, DBPath: filepath.Join(dataDir, "kapsel.db"), MediaRoot: filepath.Join(dataDir, "media"), ImportRoot: filepath.Join(dataDir, "imports"), YTDLPPath: "yt-dlp"}
	db := openCommandDB(t, cfg.DBPath)
	if err := database.Migrate(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO channels (id, external_id, name) VALUES ('chan-backup', 'chan-backup', 'Backup Channel')"); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	backupPath := filepath.Join(t.TempDir(), "kapsel.zip")
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	code := runWithConfig(context.Background(), cfg, []string{"backup", backupPath}, strings.NewReader(""), &stdout, &stderr)
	if code != 0 {
		t.Fatalf("expected backup exit code 0, got %d stderr=%q", code, stderr.String())
	}
	if strings.Contains(stdout.String(), "supersecret-hash") || strings.Contains(stdout.String(), "auth_password_hash") {
		t.Fatalf("expected backup report to omit password hash, got %s", stdout.String())
	}
	if err := os.Remove(cfg.DBPath); err != nil {
		t.Fatal(err)
	}
	db = openCommandDB(t, cfg.DBPath)
	if err := database.Migrate(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO channels (id, external_id, name) VALUES ('chan-current', 'chan-current', 'Current Channel')"); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	stdout.Reset()
	stderr.Reset()

	code = runWithConfig(context.Background(), cfg, []string{"restore", backupPath}, strings.NewReader(""), &stdout, &stderr)
	if code != 0 {
		t.Fatalf("expected restore exit code 0, got %d stderr=%q", code, stderr.String())
	}
	if strings.Contains(stdout.String(), "supersecret-hash") || strings.Contains(stdout.String(), "auth_password_hash") {
		t.Fatalf("expected restore report to omit password hash, got %s", stdout.String())
	}
	restored := openCommandDB(t, cfg.DBPath)
	assertCommandScalar(t, restored, "SELECT count(*) FROM channels WHERE id = ?", int64(1), "chan-backup")
	assertCommandScalar(t, restored, "SELECT count(*) FROM channels WHERE id = ?", int64(0), "chan-current")
}

func TestRunStorageCleanupCommandDryRunAndConfirmedDelete(t *testing.T) {
	t.Parallel()

	dataDir := t.TempDir()
	cfg := config.Config{DataDir: dataDir, DBPath: filepath.Join(dataDir, "kapsel.db"), MediaRoot: filepath.Join(dataDir, "media"), ImportRoot: filepath.Join(dataDir, "imports"), YTDLPPath: "yt-dlp"}
	db := openCommandDB(t, cfg.DBPath)
	if err := database.Migrate(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	orphanPath := filepath.Join(cfg.MediaRoot, "orphan.bin")
	if err := os.MkdirAll(cfg.MediaRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(orphanPath, []byte("orphan"), 0o644); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	code := runWithConfig(context.Background(), cfg, []string{"storage-cleanup"}, strings.NewReader(""), &stdout, &stderr)
	if code != 0 {
		t.Fatalf("expected dry-run exit code 0, got %d stderr=%q", code, stderr.String())
	}
	var dryRun storage.CleanupReport
	if err := json.NewDecoder(&stdout).Decode(&dryRun); err != nil {
		t.Fatal(err)
	}
	if !dryRun.DryRun || dryRun.Report.Summary.OrphanFiles != 1 || len(dryRun.DeletedFiles) != 0 {
		t.Fatalf("unexpected dry-run report: %#v", dryRun)
	}
	if _, err := os.Stat(orphanPath); err != nil {
		t.Fatalf("expected dry-run to keep orphan: %v", err)
	}
	stdout.Reset()
	stderr.Reset()

	code = runWithConfig(context.Background(), cfg, []string{"storage-cleanup", "--delete"}, strings.NewReader(""), &stdout, &stderr)
	if code == 0 {
		t.Fatalf("expected delete without confirm to fail")
	}
	if _, err := os.Stat(orphanPath); err != nil {
		t.Fatalf("expected unconfirmed delete to keep orphan: %v", err)
	}
	stdout.Reset()
	stderr.Reset()

	code = runWithConfig(context.Background(), cfg, []string{"storage-cleanup", "--delete", "--confirm"}, strings.NewReader(""), &stdout, &stderr)
	if code != 0 {
		t.Fatalf("expected confirmed delete exit code 0, got %d stderr=%q", code, stderr.String())
	}
	var deleted storage.CleanupReport
	if err := json.NewDecoder(&stdout).Decode(&deleted); err != nil {
		t.Fatal(err)
	}
	if deleted.DryRun || len(deleted.DeletedFiles) != 1 || deleted.DeletedFiles[0].Path != "orphan.bin" {
		t.Fatalf("unexpected confirmed cleanup report: %#v", deleted)
	}
	if _, err := os.Stat(orphanPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected orphan to be deleted, got %v", err)
	}
}

func TestRunStorageCleanupRejectsDryRunDeleteCombination(t *testing.T) {
	t.Parallel()

	dataDir := t.TempDir()
	cfg := config.Config{DataDir: dataDir, DBPath: filepath.Join(dataDir, "kapsel.db"), MediaRoot: filepath.Join(dataDir, "media"), ImportRoot: filepath.Join(dataDir, "imports"), YTDLPPath: "yt-dlp"}
	db := openCommandDB(t, cfg.DBPath)
	if err := database.Migrate(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	orphanPath := filepath.Join(cfg.MediaRoot, "orphan.bin")
	if err := os.MkdirAll(cfg.MediaRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(orphanPath, []byte("orphan"), 0o644); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	code := runWithConfig(context.Background(), cfg, []string{"storage-cleanup", "--dry-run", "--delete", "--confirm"}, strings.NewReader(""), &stdout, &stderr)
	if code != 2 {
		t.Fatalf("expected conflicting cleanup flags to return usage code 2, got %d stderr=%q", code, stderr.String())
	}
	if _, err := os.Stat(orphanPath); err != nil {
		t.Fatalf("expected conflicting dry-run delete to keep orphan: %v", err)
	}
}

func TestReleaseBinaryServesHealthAndFrontend(t *testing.T) {
	binaryPath := filepath.Join(t.TempDir(), "kapsel")
	build := exec.Command("go", "build", "-trimpath", "-o", binaryPath, ".")
	build.Env = os.Environ()
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("release binary build failed: %v\n%s", err, string(output))
	}

	addr := freeLocalAddr(t)
	runtimeRoot := t.TempDir()
	server := exec.Command(binaryPath)
	server.Env = append(os.Environ(),
		"KAPSEL_ADDR="+addr,
		"KAPSEL_AUTH_MODE=disabled",
		"KAPSEL_DATA_DIR="+filepath.Join(runtimeRoot, "data"),
		"KAPSEL_DB_PATH="+filepath.Join(runtimeRoot, "data", "kapsel.db"),
		"KAPSEL_IMPORT_ROOT="+filepath.Join(runtimeRoot, "imports"),
		"KAPSEL_MEDIA_ROOT="+filepath.Join(runtimeRoot, "media"),
		"KAPSEL_MEDIA_SIGNING_SECRET=smoke-media-secret",
		"KAPSEL_SESSION_SECRET=smoke-session-secret",
		"KAPSEL_PREVIEWS_ENABLED=false",
	)
	var serverOutput bytes.Buffer
	server.Stdout = &serverOutput
	server.Stderr = &serverOutput
	if err := server.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- server.Wait() }()
	t.Cleanup(func() {
		if server.Process != nil {
			_ = server.Process.Kill()
		}
		select {
		case <-done:
		case <-time.After(time.Second):
		}
	})

	health := waitForHTTPBody(t, "http://"+addr+"/api/health", done, &serverOutput)
	if string(health) != "OK\n" {
		t.Fatalf("expected health response %q, got %q", "OK\n", string(health))
	}
	frontend := waitForHTTPBody(t, "http://"+addr+"/", done, &serverOutput)
	if !strings.Contains(string(frontend), "Kapsel") {
		t.Fatalf("expected embedded frontend shell to contain Kapsel, got %q", string(frontend))
	}
}

func openCommandDB(t *testing.T, path string) *sql.DB {
	t.Helper()

	db, err := database.Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })

	return db
}

func freeLocalAddr(t *testing.T) string {
	t.Helper()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()

	return listener.Addr().String()
}

func waitForHTTPBody(t *testing.T, rawURL string, processDone <-chan error, serverOutput *bytes.Buffer) []byte {
	t.Helper()

	client := http.Client{Timeout: 250 * time.Millisecond}
	deadline := time.Now().Add(15 * time.Second)
	var lastErr error
	for time.Now().Before(deadline) {
		select {
		case err := <-processDone:
			t.Fatalf("packaged server exited before serving %s: %v\n%s", rawURL, err, serverOutput.String())
		default:
		}
		response, err := client.Get(rawURL)
		if err == nil {
			body, readErr := io.ReadAll(response.Body)
			_ = response.Body.Close()
			if readErr != nil {
				t.Fatal(readErr)
			}
			if response.StatusCode == http.StatusOK {
				return body
			}
			lastErr = fmt.Errorf("status %d: %s", response.StatusCode, strings.TrimSpace(string(body)))
		} else {
			lastErr = err
		}
		time.Sleep(50 * time.Millisecond)
	}

	t.Fatalf("timed out waiting for %s: %v\n%s", rawURL, lastErr, serverOutput.String())
	return nil
}

func writeCommandBackupZip(t *testing.T, root string) {
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

	writeCommandZipFile(t, zipFile, "es_channel-20260503-0.json", commandBulkDocument("chan-1", map[string]any{
		"channel_id":   "chan-1",
		"channel_name": "Archive Workshop",
	}))
	writeCommandZipFile(t, zipFile, "es_video-20260503-0.json", commandBulkDocument("vid-1", map[string]any{
		"youtube_id":  "vid-1",
		"title":       "Kapsel Demo",
		"published":   "2026-05-03",
		"media_url":   "media/vid-1.mp4",
		"channel":     map[string]any{"channel_id": "chan-1", "channel_name": "Archive Workshop"},
		"player":      map[string]any{"duration": 120, "position": 42, "watched": false},
		"description": "A demo video",
	})+"\n"+`{"index":{"_index":"ta_video","_id":"bad"}}`+"\n"+`{"broken":`)
	writeCommandZipFile(t, zipFile, "es_playlist-20260503-0.json", commandBulkDocument("playlist-1", map[string]any{
		"playlist_id":         "playlist-1",
		"playlist_name":       "Saved Clips",
		"playlist_channel_id": "chan-1",
		"playlist_entries":    []map[string]any{{"youtube_id": "vid-1", "idx": 0, "downloaded": true}},
	}))
}

func writeCommandZipFile(t *testing.T, zipFile *zip.Writer, name string, body string) {
	t.Helper()

	writer, err := zipFile.Create(name)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Write([]byte(body)); err != nil {
		t.Fatal(err)
	}
}

func commandBulkDocument(id string, source map[string]any) string {
	action, _ := json.Marshal(map[string]any{"index": map[string]any{"_index": "ta_backup", "_id": id}})
	body, _ := json.Marshal(source)

	return string(action) + "\n" + string(body)
}

func assertCommandScalar[T comparable](t *testing.T, db *sql.DB, query string, expected T, args ...any) {
	t.Helper()

	var got T
	if err := db.QueryRow(query, args...).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != expected {
		t.Fatalf("expected %v, got %v", expected, got)
	}
}
