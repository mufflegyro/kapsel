package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"kapsel/internal/app"
	"kapsel/internal/e2e/fixture"
)

func main() {
	if err := run(context.Background()); err != nil {
		slog.Error("e2e test server failed", "error", err)
		os.Exit(1)
	}
}

func run(ctx context.Context) error {
	addr := valueOrDefault("KAPSEL_E2E_ADDR", "127.0.0.1:18080")
	dataDir := os.Getenv("KAPSEL_E2E_DATA_DIR")
	cleanup := func() {}
	if dataDir == "" {
		tempDir, err := os.MkdirTemp("", "kapsel-e2e-*")
		if err != nil {
			return err
		}
		dataDir = tempDir
		cleanup = func() { _ = os.RemoveAll(tempDir) }
	}
	defer cleanup()

	cfg := fixture.Config(addr, dataDir)
	application, err := app.New(ctx, cfg)
	if err != nil {
		return err
	}
	defer application.Close()
	if err := fixture.Seed(ctx, application.DB, cfg.MediaRoot); err != nil {
		return err
	}

	handler := http.NewServeMux()
	handler.HandleFunc("/__e2e/reset-progress", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if err := fixture.ResetProgress(r.Context(), application.DB); err != nil {
			http.Error(w, "failed to reset progress", http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	handler.Handle("/", application.Handler)

	server := &http.Server{Addr: cfg.Addr, Handler: handler, ReadHeaderTimeout: 5 * time.Second}
	serverErr := make(chan error, 1)
	go func() {
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serverErr <- err
		}
	}()

	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()
	select {
	case err := <-serverErr:
		return err
	case <-ctx.Done():
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return server.Shutdown(shutdownCtx)
}

func valueOrDefault(key string, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}

	return fallback
}
