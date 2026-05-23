package fixture_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"kapsel/internal/app"
	"kapsel/internal/e2e/fixture"
)

func TestSeedAndResetProgress(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	cfg := fixture.Config("127.0.0.1:0", t.TempDir())
	application, err := app.New(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer application.Close()

	if err := fixture.Seed(ctx, application.DB, cfg.MediaRoot); err != nil {
		t.Fatal(err)
	}
	assertScalar(t, application.DB.QueryRowContext(ctx, "SELECT title FROM videos WHERE id = 'e2e-video'"), "E2E Lunar Archive Smoke")
	assertFileExists(t, filepath.Join(cfg.MediaRoot, "videos", "e2e-video.mp4"))
	assertScalar(t, application.DB.QueryRowContext(ctx, "SELECT position_seconds FROM user_progress WHERE video_id = 'e2e-video'"), int64(42))

	if _, err := application.DB.ExecContext(ctx, "UPDATE videos SET watched = 1, keep_forever = 1 WHERE id = 'e2e-video'"); err != nil {
		t.Fatal(err)
	}
	if _, err := application.DB.ExecContext(ctx, "UPDATE user_progress SET position_seconds = 125, duration_seconds = 125, watched = 1 WHERE video_id = 'e2e-video'"); err != nil {
		t.Fatal(err)
	}
	if err := fixture.ResetProgress(ctx, application.DB); err != nil {
		t.Fatal(err)
	}
	assertScalar(t, application.DB.QueryRowContext(ctx, "SELECT watched FROM videos WHERE id = 'e2e-video'"), int64(0))
	assertScalar(t, application.DB.QueryRowContext(ctx, "SELECT keep_forever FROM videos WHERE id = 'e2e-video'"), int64(0))
	assertScalar(t, application.DB.QueryRowContext(ctx, "SELECT position_seconds FROM user_progress WHERE video_id = 'e2e-video'"), int64(42))
	assertScalar(t, application.DB.QueryRowContext(ctx, "SELECT watched FROM user_progress WHERE video_id = 'e2e-video'"), int64(0))
}

type scanner interface {
	Scan(dest ...any) error
}

func assertScalar[T comparable](t *testing.T, row scanner, want T) {
	t.Helper()

	var got T
	if err := row.Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("expected %v, got %v", want, got)
	}
}

func assertFileExists(t *testing.T, path string) {
	t.Helper()

	if _, err := os.Stat(path); err != nil {
		t.Fatal(err)
	}
}
