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
	"kapsel/internal/backup"
	"kapsel/internal/config"
	"kapsel/internal/database"
	"kapsel/internal/download"
	"kapsel/internal/jobs"
	"kapsel/internal/media"
	"kapsel/internal/previews"
	"kapsel/internal/server"
	"kapsel/internal/sponsorblock"
	"kapsel/internal/taimport"
	"kapsel/internal/updater"
	"kapsel/internal/version"
)

type App struct {
	Config  config.Config
	DB      *sql.DB
	Jobs    *jobs.Store
	Runner  *jobs.Runner
	Handler http.Handler
	Updater *updater.Updater
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
	downloader := download.NewDownloader(db, download.Config{YTDLPPath: cfg.YTDLPPath, YTDLPCookiesFile: cfg.YTDLPCookiesFile, YTDLPSleepInterval: cfg.YTDLPSleepInterval, DataRoot: cfg.DataDir, MediaRoot: cfg.MediaRoot, FormatSelector: cfg.YTDLPFormat, MinFreeSpaceBytes: cfg.MinFreeSpaceBytes, PreviewsEnabled: cfg.PreviewsEnabled, SubtitlesEnabled: cfg.SubtitlesEnabled, FFMPEGPath: cfg.FFMPEGPath, JobStore: jobStore, RetentionWatchedCleanupDisabled: cfg.RetentionWatchedAfter == 0, RetentionIncludeManual: cfg.RetentionIncludeManual}, nil)
	updaterService := updater.New(db, jobStore, updater.Config{
		Repo:           cfg.UpdateRepo,
		CurrentVersion: version.Version,
		DataDir:        cfg.DataDir,
		DBPath:         cfg.DBPath,
		CheckInterval:  cfg.UpdateCheckInterval,
		CreateBackup: func(ctx context.Context, outputPath string) (updater.BackupMetadata, error) {
			metadata, err := backup.Create(ctx, cfg, outputPath)
			if err != nil {
				return updater.BackupMetadata{}, err
			}

			return updater.BackupMetadata{SchemaVersion: metadata.SchemaVersion}, nil
		},
	})
	previewer := previews.NewJobHandler(db, previews.Config{MediaRoot: cfg.MediaRoot, FFmpegPath: cfg.FFMPEGPath, JobStore: jobStore})
	taImporter := taimport.NewJobHandler(db, jobStore, cfg.ImportRoot).WithDiskSpace(cfg.DataDir, cfg.MinFreeSpaceBytes, nil)
	runner := jobs.NewRunner(jobStore, map[string]jobs.Handler{
		download.JobType:                    downloader.Handle,
		download.VideoMetadataScanJobType:   downloader.HandleVideoMetadataScan,
		download.ChannelJobType:             downloader.HandleChannelFirst,
		download.ChannelScanJobType:         downloader.HandleChannelScan,
		download.ChannelAutoDownloadJobType: downloader.HandleChannelAutoDownload,
		download.PlaylistImportJobType:      downloader.HandlePlaylistImport,
		download.RetentionJobType:           downloader.HandleRetention,
		download.YTDLPUpdateJobType:         downloader.HandleYTDLPUpdate,
		updater.ReleaseCheckJobType:         updaterService.HandleReleaseCheck,
		updater.SelfUpdateJobType:           updaterService.HandleSelfUpdate,
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
	handlerOptions = append(handlerOptions, server.WithUpdater(updaterService))
	handler := server.NewHandler(handlerOptions...)

	return &App{
		Config:  cfg,
		DB:      db,
		Jobs:    jobStore,
		Runner:  runner,
		Handler: handler,
		Updater: updaterService,
		lock:    lock,
	}, nil
}

func (a *App) RunJobs(ctx context.Context) error {
	if a == nil || a.Runner == nil {
		return nil
	}
	// One fixed tick per scheduler family; whether a tick enqueues anything is
	// scheduling policy owned by the domain packages' Ensure* functions (see
	// docs/scheduler.md). All durable scheduled work is jobs, processed by the
	// runner below — the loops here never execute domain work inline.
	const schedulerTick = time.Hour
	if a.Config.ChannelAutoDownloadInterval > 0 {
		interval := a.Config.ChannelAutoDownloadInterval
		go a.runPeriodicScheduler(ctx, "channel auto-download", schedulerTick, func(ctx context.Context) error {
			_, err := download.EnsureChannelAutoDownloadJobs(ctx, a.DB, a.Jobs, download.ChannelAutoScheduleOptions{Interval: interval})
			return err
		})
	}
	go a.runPeriodicScheduler(ctx, "retention", schedulerTick, func(ctx context.Context) error {
		_, err := download.EnsureRetentionJobs(ctx, a.Jobs, download.RetentionScheduleOptions{})
		return err
	})
	go a.runPeriodicScheduler(ctx, "yt-dlp update", schedulerTick, func(ctx context.Context) error {
		_, err := download.EnsureYTDLPUpdateJobs(ctx, a.Jobs, download.YTDLPUpdateScheduleOptions{Interval: a.Config.YTDLPUpdateInterval})
		return err
	})
	if a.Updater != nil && a.Config.UpdateCheckInterval > 0 {
		go a.runPeriodicScheduler(ctx, "release check", schedulerTick, func(ctx context.Context) error {
			_, err := a.Updater.EnsureReleaseCheckJobs(ctx)
			return err
		})
	}

	return a.Runner.RunLoop(ctx, time.Second)
}

// runPeriodicScheduler owns only cadence and error reporting for one scheduler
// family: it invokes the given ensure function on every tick and logs failures.
// Whether a tick actually enqueues a job is scheduling policy inside the
// domain package (dedupe, throttling, backoff); this loop never queries the
// job table directly. Loop-level ticks are hourly regardless of the job
// cadence so that each policy call can cheaply decide "nothing due".
func (a *App) runPeriodicScheduler(ctx context.Context, name string, tick time.Duration, ensure func(context.Context) error) {
	if tick <= 0 {
		tick = time.Hour
	}
	ticker := time.NewTicker(tick)
	defer ticker.Stop()
	for {
		if err := ensure(ctx); err != nil && ctx.Err() == nil {
			slog.Error(name+" scheduler failed", "error", err)
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
