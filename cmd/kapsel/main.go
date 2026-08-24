package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"kapsel/internal/app"
	"kapsel/internal/applock"
	"kapsel/internal/auth"
	"kapsel/internal/backup"
	"kapsel/internal/config"
	"kapsel/internal/database"
	"kapsel/internal/diskspace"
	"kapsel/internal/playlistimport"
	"kapsel/internal/storage"
	"kapsel/internal/subsimport"
	"kapsel/internal/taimport"
)

const (
	serverReadHeaderTimeout = 5 * time.Second
	serverReadTimeout       = 30 * time.Second
	serverWriteTimeout      = 30 * time.Second
	serverIdleTimeout       = 2 * time.Minute
)

func main() {
	os.Exit(runWithConfig(context.Background(), config.Load(), os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

func runWithConfig(ctx context.Context, cfg config.Config, args []string, stdin io.Reader, stdout io.Writer, stderr io.Writer) int {
	if len(args) > 0 {
		switch args[0] {
		case "hash-password":
			return runHashPassword(args[1:], stdin, stdout, stderr)
		case "import-ta":
			return runImportTA(ctx, cfg, args[1:], stdout, stderr)
		case "import-subscriptions":
			return runImportSubscriptions(ctx, cfg, args[1:], stdout, stderr)
		case "import-playlists":
			return runImportPlaylists(ctx, cfg, args[1:], stdout, stderr)
		case "backup":
			return runBackup(ctx, cfg, args[1:], stdout, stderr)
		case "restore":
			return runRestore(ctx, cfg, args[1:], stdout, stderr)
		case "storage-report":
			return runStorageReport(ctx, cfg, args[1:], stdout, stderr)
		case "storage-cleanup":
			return runStorageCleanup(ctx, cfg, args[1:], stdout, stderr)
		default:
			fmt.Fprintf(stderr, "unknown command %q\n", args[0])
			return 2
		}
	}

	if err := runServer(ctx, cfg); err != nil {
		slog.Error("server failed", "error", err)
		return 1
	}

	return 0
}

func runBackup(ctx context.Context, cfg config.Config, args []string, stdout io.Writer, stderr io.Writer) int {
	if len(args) != 1 {
		fmt.Fprintln(stderr, "usage: kapsel backup <backup.zip>")
		return 2
	}
	metadata, err := backup.Create(ctx, cfg, args[0])
	if err != nil {
		fmt.Fprintf(stderr, "backup failed: %v\n", err)
		return 1
	}
	if err := json.NewEncoder(stdout).Encode(metadata); err != nil {
		fmt.Fprintf(stderr, "failed to write backup report: %v\n", err)
		return 1
	}

	return 0
}

func runRestore(ctx context.Context, cfg config.Config, args []string, stdout io.Writer, stderr io.Writer) int {
	force := false
	filtered := []string{}
	for _, arg := range args {
		if arg == "--force" {
			force = true
			continue
		}
		filtered = append(filtered, arg)
	}
	if len(filtered) != 1 {
		fmt.Fprintln(stderr, "usage: kapsel restore [--force] <backup.zip>")
		return 2
	}
	metadata, err := backup.Restore(ctx, cfg, filtered[0], backup.RestoreOptions{Force: force})
	if err != nil {
		fmt.Fprintf(stderr, "restore failed: %v\n", err)
		return 1
	}
	if err := json.NewEncoder(stdout).Encode(metadata); err != nil {
		fmt.Fprintf(stderr, "failed to write restore report: %v\n", err)
		return 1
	}

	return 0
}

func runStorageReport(ctx context.Context, cfg config.Config, args []string, stdout io.Writer, stderr io.Writer) int {
	if len(args) != 0 {
		fmt.Fprintln(stderr, "usage: kapsel storage-report")
		return 2
	}
	db, err := openMetadataDB(ctx, cfg)
	if err != nil {
		fmt.Fprintf(stderr, "storage report failed: %v\n", err)
		return 1
	}
	defer db.Close()
	report, err := storage.Scan(ctx, db, storageConfig(cfg))
	if err != nil {
		fmt.Fprintf(stderr, "storage report failed: %v\n", err)
		return 1
	}
	if err := json.NewEncoder(stdout).Encode(report); err != nil {
		fmt.Fprintf(stderr, "failed to write storage report: %v\n", err)
		return 1
	}

	return 0
}

func runStorageCleanup(ctx context.Context, cfg config.Config, args []string, stdout io.Writer, stderr io.Writer) int {
	options := storage.CleanupOptions{}
	dryRunExplicit := false
	for _, arg := range args {
		switch arg {
		case "--dry-run":
			dryRunExplicit = true
		case "--delete":
			options.Delete = true
		case "--confirm":
			options.Confirm = true
		default:
			fmt.Fprintln(stderr, "usage: kapsel storage-cleanup [--dry-run | --delete --confirm]")
			return 2
		}
	}
	if dryRunExplicit && options.Delete {
		fmt.Fprintln(stderr, "usage: kapsel storage-cleanup [--dry-run | --delete --confirm]")
		return 2
	}
	if options.Delete {
		lock, err := applock.Acquire(cfg.DBPath + ".lock")
		if err != nil {
			fmt.Fprintf(stderr, "storage cleanup failed: %v\n", err)
			return 1
		}
		defer lock.Close()
	}
	db, err := openMetadataDB(ctx, cfg)
	if err != nil {
		fmt.Fprintf(stderr, "storage cleanup failed: %v\n", err)
		return 1
	}
	defer db.Close()
	report, err := storage.Cleanup(ctx, db, storageConfig(cfg), options)
	if err != nil {
		fmt.Fprintf(stderr, "storage cleanup failed: %v\n", err)
		return 1
	}
	if err := json.NewEncoder(stdout).Encode(report); err != nil {
		fmt.Fprintf(stderr, "failed to write storage cleanup report: %v\n", err)
		return 1
	}

	return 0
}

func openMetadataDB(ctx context.Context, cfg config.Config) (*sql.DB, error) {
	db, err := database.Open(ctx, cfg.DBPath)
	if err != nil {
		return nil, err
	}
	if err := database.Migrate(ctx, db); err != nil {
		_ = db.Close()
		return nil, err
	}

	return db, nil
}

func storageConfig(cfg config.Config) storage.Config {
	return storage.Config{DataRoot: cfg.DataDir, MediaRoot: cfg.MediaRoot, DBPath: cfg.DBPath}
}

func runHashPassword(args []string, stdin io.Reader, stdout io.Writer, stderr io.Writer) int {
	if len(args) > 1 || (len(args) == 1 && args[0] != "--stdin") {
		fmt.Fprintln(stderr, "usage: kapsel hash-password [--stdin]")
		return 2
	}
	passwordBytes, err := io.ReadAll(io.LimitReader(stdin, 4096))
	if err != nil {
		fmt.Fprintf(stderr, "failed to read password: %v\n", err)
		return 1
	}
	password := strings.TrimRight(string(passwordBytes), "\r\n")
	hash, err := auth.HashPassword(password)
	if err != nil {
		fmt.Fprintf(stderr, "password hash failed: %v\n", err)
		return 1
	}
	fmt.Fprintln(stdout, hash)

	return 0
}

func runImportTA(ctx context.Context, cfg config.Config, args []string, stdout io.Writer, stderr io.Writer) int {
	if len(args) != 1 {
		fmt.Fprintln(stderr, "usage: kapsel import-ta <tubearchivist-root>")
		return 2
	}
	application, err := app.New(ctx, cfg)
	if err != nil {
		fmt.Fprintf(stderr, "application setup failed: %v\n", err)
		return 1
	}
	defer application.Close()
	if err := diskspace.NewChecker(cfg.MinFreeSpaceBytes, nil).Ensure(cfg.DataDir); err != nil {
		fmt.Fprintf(stderr, "TubeArchivist import failed: %v\n", err)
		return 1
	}
	report, err := taimport.Import(ctx, application.DB, args[0])
	if err != nil {
		fmt.Fprintf(stderr, "TubeArchivist import failed: %v\n", err)
		return 1
	}
	if err := json.NewEncoder(stdout).Encode(report); err != nil {
		fmt.Fprintf(stderr, "failed to write import report: %v\n", err)
		return 1
	}

	return 0
}

func runImportSubscriptions(ctx context.Context, cfg config.Config, args []string, stdout io.Writer, stderr io.Writer) int {
	scanOnly := false
	filtered := []string{}
	for _, arg := range args {
		switch arg {
		case "--scan-only":
			scanOnly = true
		default:
			filtered = append(filtered, arg)
		}
	}
	if len(filtered) != 1 {
		fmt.Fprintln(stderr, "usage: kapsel import-subscriptions [--scan-only] <subscriptions.csv>")
		return 2
	}
	application, err := app.New(ctx, cfg)
	if err != nil {
		fmt.Fprintf(stderr, "application setup failed: %v\n", err)
		return 1
	}
	defer application.Close()

	report, err := subsimport.ImportFile(ctx, application.Jobs, filtered[0], scanOnly)
	if err != nil {
		fmt.Fprintf(stderr, "subscriptions import failed: %v\n", err)
		return 1
	}
	if err := json.NewEncoder(stdout).Encode(report); err != nil {
		fmt.Fprintf(stderr, "failed to write import report: %v\n", err)
		return 1
	}

	return 0
}

func runImportPlaylists(ctx context.Context, cfg config.Config, args []string, stdout io.Writer, stderr io.Writer) int {
	mode := playlistimport.ModeMetadataScan
	filtered := []string{}
	for _, arg := range args {
		switch arg {
		case "--download":
			mode = playlistimport.ModeDownload
		case "--link-only":
			mode = playlistimport.ModeLinkOnly
		default:
			filtered = append(filtered, arg)
		}
	}
	if len(filtered) < 1 {
		fmt.Fprintln(stderr, "usage: kapsel import-playlists [--download|--link-only] <playlist.csv>...")
		return 2
	}
	application, err := app.New(ctx, cfg)
	if err != nil {
		fmt.Fprintf(stderr, "application setup failed: %v\n", err)
		return 1
	}
	defer application.Close()

	total := playlistimport.Report{Playlists: 0}
	for _, path := range filtered {
		report, err := playlistimport.ImportFile(ctx, application.DB, application.Jobs, path, mode)
		if err != nil {
			fmt.Fprintf(stderr, "playlist import %q failed: %v\n", path, err)
			return 1
		}
		total.Playlists += report.Playlists
		total.Linked += report.Linked
		total.Missing += report.Missing
		total.Enqueued += report.Enqueued
		total.Skipped += report.Skipped
		total.Errors = append(total.Errors, report.Errors...)
	}
	if err := json.NewEncoder(stdout).Encode(total); err != nil {
		fmt.Fprintf(stderr, "failed to write import report: %v\n", err)
		return 1
	}

	return 0
}

func runServer(ctx context.Context, cfg config.Config) error {
	application, err := app.New(ctx, cfg)
	if err != nil {
		return fmt.Errorf("application setup failed: %w", err)
	}
	defer application.Close()
	workerCtx, workerCancel := context.WithCancel(ctx)
	defer workerCancel()
	workerDone := make(chan struct{})
	go func() {
		defer close(workerDone)
		if err := application.RunJobs(workerCtx); err != nil && !errors.Is(err, context.Canceled) {
			slog.Error("job runner failed", "error", err)
		}
	}()

	srv := newHTTPServer(cfg.Addr, application.Handler)

	serverErr := make(chan error, 1)
	go func() {
		slog.Info("starting kapsel", "addr", cfg.Addr, "db", cfg.DBPath, "media", cfg.MediaRoot)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
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
	if err := srv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("server shutdown failed: %w", err)
	}
	workerCancel()
	select {
	case <-workerDone:
	case <-time.After(10 * time.Second):
		slog.Error("job runner did not shut down within 10 seconds")
	}

	return nil
}

func newHTTPServer(addr string, handler http.Handler) *http.Server {
	return &http.Server{
		Addr:              addr,
		Handler:           handler,
		ReadHeaderTimeout: serverReadHeaderTimeout,
		ReadTimeout:       serverReadTimeout,
		WriteTimeout:      serverWriteTimeout,
		IdleTimeout:       serverIdleTimeout,
	}
}
