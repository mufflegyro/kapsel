package app

import (
	"context"
	"database/sql"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"kapsel/internal/applock"
	"kapsel/internal/auth"
	"kapsel/internal/config"
	"kapsel/internal/database"
	"kapsel/internal/download"
	"kapsel/internal/jobs"
	"kapsel/internal/media"
	"kapsel/internal/previews"
	"kapsel/internal/server"
	"kapsel/internal/sponsorblock"
	"kapsel/internal/taimport"
)

type App struct {
	Config  config.Config
	DB      *sql.DB
	Jobs    *jobs.Store
	Runner  *jobs.Runner
	Handler http.Handler
	lock    *applock.Lock
}

func New(ctx context.Context, cfg config.Config) (*App, error) {
	if err := ensureRuntimeDirs(cfg); err != nil {
		return nil, err
	}

	lock, err := applock.Acquire(cfg.DBPath + ".lock")
	if err != nil {
		return nil, err
	}
	db, err := database.Open(ctx, cfg.DBPath)
	if err != nil {
		_ = lock.Close()
		return nil, err
	}
	if err := database.Migrate(ctx, db); err != nil {
		_ = db.Close()
		_ = lock.Close()
		return nil, err
	}
	supportedSchemaVersion, err := database.SupportedSchemaVersion()
	if err != nil {
		_ = db.Close()
		_ = lock.Close()
		return nil, err
	}

	jobStore := jobs.NewStore(db)
	authManager := auth.NewManager(auth.Config{Enabled: cfg.AuthMode != "disabled", Username: cfg.AuthUsername, PasswordHash: cfg.AuthPasswordHash, SessionSecret: cfg.SessionSecret, SessionTTL: cfg.SessionTTL, CookieSecure: cfg.SessionCookieSecure})
	downloader := download.NewDownloader(db, download.Config{YTDLPPath: cfg.YTDLPPath, YTDLPCookiesFile: cfg.YTDLPCookiesFile, YTDLPSleepInterval: cfg.YTDLPSleepInterval, DataRoot: cfg.DataDir, MediaRoot: cfg.MediaRoot, FormatSelector: cfg.YTDLPFormat, MinFreeSpaceBytes: cfg.MinFreeSpaceBytes, PreviewsEnabled: cfg.PreviewsEnabled, FFMPEGPath: cfg.FFMPEGPath, JobStore: jobStore}, nil)
	previewer := previews.NewJobHandler(db, previews.Config{MediaRoot: cfg.MediaRoot, FFmpegPath: cfg.FFMPEGPath, JobStore: jobStore})
	taImporter := taimport.NewJobHandler(db, jobStore, cfg.ImportRoot).WithDiskSpace(cfg.DataDir, cfg.MinFreeSpaceBytes, nil)
	runner := jobs.NewRunner(jobStore, map[string]jobs.Handler{
		download.JobType:                    downloader.Handle,
		download.ChannelJobType:             downloader.HandleChannelFirst,
		download.ChannelScanJobType:         downloader.HandleChannelScan,
		download.ChannelAutoDownloadJobType: downloader.HandleChannelAutoDownload,
		download.RetentionJobType:           downloader.HandleRetention,
		download.YTDLPUpdateJobType:         downloader.HandleYTDLPUpdate,
		previews.JobType:                    previewer.Handle,
		taimport.JobType:                    taImporter.Handle,
	})
	handlerOptions := []server.Option{
		server.WithDatabase(db),
		server.WithJobs(jobStore),
		server.WithImportRoot(cfg.ImportRoot),
		server.WithAuth(authManager),
		server.WithMedia(cfg.MediaRoot, media.NewSigner(cfg.MediaSigningSecret)),
		server.WithMediaURLTTL(cfg.MediaURLTTL),
		server.WithSettingsDiagnostics(server.SettingsDiagnostics{
			Addr:                         cfg.Addr,
			AuthMode:                     cfg.AuthMode,
			DataDir:                      cfg.DataDir,
			DBPath:                       cfg.DBPath,
			ImportRoot:                   cfg.ImportRoot,
			MediaRoot:                    cfg.MediaRoot,
			MediaSigningSecretConfigured: cfg.MediaSigningSecretConfigured,
			AuthenticationConfigured:     authManager.Enabled() && authManager.Configured(),
			SessionSecretConfigured:      cfg.SessionSecretConfigured,
			MediaURLTTL:                  cfg.MediaURLTTL,
			MinFreeSpaceBytes:            cfg.MinFreeSpaceBytes,
			PreviewsEnabled:              cfg.PreviewsEnabled,
			SponsorBlockEnabled:          cfg.SponsorBlockEnabled,
			FFMPEGPath:                   cfg.FFMPEGPath,
			YTDLPPath:                    cfg.YTDLPPath,
		}),
		server.WithSupportedSchemaVersion(supportedSchemaVersion),
		server.WithYTDLPDiagnostics(cfg.YTDLPPath, nil),
		server.WithStorageDiagnostics(cfg.DataDir, cfg.MediaRoot, cfg.MinFreeSpaceBytes, nil),
	}
	if cfg.SponsorBlockEnabled {
		handlerOptions = append(handlerOptions, server.WithSponsorBlockClient(sponsorblock.NewClient()))
	}
	handler := server.NewHandler(handlerOptions...)

	return &App{
		Config:  cfg,
		DB:      db,
		Jobs:    jobStore,
		Runner:  runner,
		Handler: handler,
		lock:    lock,
	}, nil
}

func (a *App) RunJobs(ctx context.Context) error {
	if a == nil || a.Runner == nil {
		return nil
	}
	go a.runChannelAutoDownloadScheduler(ctx, time.Hour)
	go a.runRetentionScheduler(ctx, time.Hour)
	go a.runYTDLPUpdateScheduler(ctx, time.Hour)

	return a.Runner.RunLoop(ctx, time.Second)
}

func (a *App) runYTDLPUpdateScheduler(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = time.Hour
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		if _, err := download.EnsureYTDLPUpdateJobs(ctx, a.DB, a.Jobs, download.YTDLPUpdateScheduleOptions{Interval: a.Config.YTDLPUpdateInterval}); err != nil && ctx.Err() == nil {
			slog.Error("yt-dlp update scheduler failed", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (a *App) runChannelAutoDownloadScheduler(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = time.Hour
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		if _, err := download.EnsureChannelAutoDownloadJobs(ctx, a.DB, a.Jobs, download.ChannelAutoScheduleOptions{}); err != nil && ctx.Err() == nil {
			slog.Error("channel auto-download scheduler failed", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (a *App) runRetentionScheduler(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = time.Hour
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		if _, err := download.EnsureRetentionJobs(ctx, a.DB, a.Jobs, download.RetentionScheduleOptions{}); err != nil && ctx.Err() == nil {
			slog.Error("retention scheduler failed", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (a *App) Close() error {
	if a == nil || a.DB == nil {
		return nil
	}

	err := a.DB.Close()
	if lockErr := a.lock.Close(); err == nil {
		err = lockErr
	}
	return err
}

func ensureRuntimeDirs(cfg config.Config) error {
	if err := os.MkdirAll(filepath.Dir(cfg.DBPath), 0o755); err != nil {
		return err
	}
	if cfg.ImportRoot != "" {
		if err := os.MkdirAll(cfg.ImportRoot, 0o755); err != nil {
			return err
		}
	}
	if err := os.MkdirAll(cfg.MediaRoot, 0o755); err != nil {
		return err
	}

	return nil
}
