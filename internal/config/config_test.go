package config

import (
	"os"
	"strings"
	"testing"
	"time"

	"kapsel/internal/diskspace"
	"kapsel/internal/download"
)

func TestLoadDefaults(t *testing.T) {
	t.Parallel()

	cfg := loadFromLookup(func(string) (string, bool) { return "", false }, missingExecutable)

	if cfg.Addr != ":8080" {
		t.Fatalf("expected default addr %q, got %q", ":8080", cfg.Addr)
	}
	if cfg.DataDir != "data" {
		t.Fatalf("expected default data dir %q, got %q", "data", cfg.DataDir)
	}
	if cfg.DBPath != "data/kapsel.db" {
		t.Fatalf("expected default db path %q, got %q", "data/kapsel.db", cfg.DBPath)
	}
	if cfg.ImportRoot != "data/imports" {
		t.Fatalf("expected default import root %q, got %q", "data/imports", cfg.ImportRoot)
	}
	if cfg.MediaRoot != "data/media" {
		t.Fatalf("expected default media root %q, got %q", "data/media", cfg.MediaRoot)
	}
	if cfg.MediaSigningSecret == "" {
		t.Fatal("expected default media signing secret")
	}
	if cfg.MediaSigningSecretConfigured {
		t.Fatal("expected default media signing secret to be marked ephemeral")
	}
	other := loadFromLookup(func(string) (string, bool) { return "", false }, missingExecutable)
	if cfg.MediaSigningSecret == other.MediaSigningSecret {
		t.Fatal("expected default media signing secret to be generated per load")
	}
	if cfg.MediaURLTTL != 24*time.Hour {
		t.Fatalf("expected default media URL TTL %s, got %s", 24*time.Hour, cfg.MediaURLTTL)
	}
	if cfg.YTDLPPath != "yt-dlp" {
		t.Fatalf("expected default yt-dlp path %q, got %q", "yt-dlp", cfg.YTDLPPath)
	}
	if cfg.YTDLPFormat != download.DefaultFormatSelector {
		t.Fatalf("expected default yt-dlp format selector %q, got %q", download.DefaultFormatSelector, cfg.YTDLPFormat)
	}
	if cfg.YTDLPCookiesFile != "" {
		t.Fatalf("expected no default yt-dlp cookies file, got %q", cfg.YTDLPCookiesFile)
	}
	if cfg.YTDLPSleepInterval != download.DefaultYTDLPSleepInterval {
		t.Fatalf("expected default yt-dlp sleep interval %s, got %s", download.DefaultYTDLPSleepInterval, cfg.YTDLPSleepInterval)
	}
	if cfg.YTDLPUpdateInterval != download.DefaultYTDLPUpdateInterval {
		t.Fatalf("expected default yt-dlp update interval %s, got %s", download.DefaultYTDLPUpdateInterval, cfg.YTDLPUpdateInterval)
	}
	if cfg.RetentionWatchedAfter != download.DefaultRetentionWatchedAfter {
		t.Fatalf("expected default retention watched interval %s, got %s", download.DefaultRetentionWatchedAfter, cfg.RetentionWatchedAfter)
	}
	if cfg.RetentionIncludeManual {
		t.Fatal("expected manual media retention opt-in to default to off")
	}
	if cfg.MinFreeSpaceBytes != diskspace.DefaultMinFreeBytes {
		t.Fatalf("expected default free-space headroom %d, got %d", diskspace.DefaultMinFreeBytes, cfg.MinFreeSpaceBytes)
	}
	if cfg.PreviewsEnabled {
		t.Fatal("expected timeline previews to be disabled when ffmpeg is missing")
	}
	if !cfg.SponsorBlockEnabled {
		t.Fatal("expected SponsorBlock integration to be enabled by default")
	}
	if cfg.FFMPEGPath != "ffmpeg" {
		t.Fatalf("expected default ffmpeg path %q, got %q", "ffmpeg", cfg.FFMPEGPath)
	}
	if cfg.AuthMode != "required" {
		t.Fatalf("expected auth to be required by default, got %q", cfg.AuthMode)
	}
	if cfg.AuthUsername != "" || cfg.AuthPasswordHash != "" {
		t.Fatalf("expected no default auth account, got username=%q hash=%q", cfg.AuthUsername, cfg.AuthPasswordHash)
	}
	if cfg.SessionSecret == "" || cfg.SessionSecretConfigured {
		t.Fatalf("expected generated ephemeral session secret, configured=%v", cfg.SessionSecretConfigured)
	}
	if cfg.SessionCookieSecure {
		t.Fatal("expected session cookies not to require HTTPS by default")
	}
	if cfg.SessionTTL != 7*24*time.Hour {
		t.Fatalf("expected default session TTL %s, got %s", 7*24*time.Hour, cfg.SessionTTL)
	}
}

func TestLoadDefaultsEnablesPreviewsWhenFFmpegExists(t *testing.T) {
	t.Parallel()

	cfg := loadFromLookup(func(string) (string, bool) { return "", false }, availableExecutable)

	if !cfg.PreviewsEnabled {
		t.Fatal("expected timeline previews to be enabled when ffmpeg is available")
	}
}

func TestLoadPreviewEnvironmentOverrideDisablesAutoDetection(t *testing.T) {
	t.Parallel()

	cfg := loadFromLookup(func(key string) (string, bool) {
		if key == EnvPreviewsEnabled {
			return "false", true
		}

		return "", false
	}, availableExecutable)

	if cfg.PreviewsEnabled {
		t.Fatal("expected explicit preview disable to override available ffmpeg")
	}
}

func TestLoadEnvironmentOverrides(t *testing.T) {
	t.Parallel()

	env := map[string]string{
		"KAPSEL_ADDR":                     "127.0.0.1:9000",
		"KAPSEL_AUTH_MODE":                "disabled",
		"KAPSEL_AUTH_PASSWORD_HASH":       "$argon2id$v=19$m=65536,t=3,p=1$salt$hash",
		"KAPSEL_AUTH_USERNAME":            "admin",
		"KAPSEL_DATA_DIR":                 "/srv/kapsel",
		"KAPSEL_DB_PATH":                  "/srv/db/app.db",
		"KAPSEL_IMPORT_ROOT":              "/srv/imports",
		"KAPSEL_MEDIA_ROOT":               "/srv/media",
		"KAPSEL_MEDIA_SIGNING_SECRET":     "secret",
		"KAPSEL_MEDIA_URL_TTL":            "15m",
		"KAPSEL_SESSION_SECRET":           "session-secret",
		"KAPSEL_SESSION_COOKIE_SECURE":    "true",
		"KAPSEL_SESSION_TTL":              "24h",
		"KAPSEL_YTDLP_FORMAT":             "best[height<=480]",
		"KAPSEL_YTDLP_PATH":               "/usr/local/bin/yt-dlp",
		"KAPSEL_YTDLP_COOKIES_FILE":       "/etc/kapsel/youtube.cookies.txt",
		"KAPSEL_YTDLP_SLEEP_INTERVAL":     "30s",
		"KAPSEL_YTDLP_UPDATE_INTERVAL":    "12h",
		"KAPSEL_MIN_FREE_SPACE":           "2GiB",
		"KAPSEL_PREVIEWS_ENABLED":         "true",
		"KAPSEL_SPONSORBLOCK_ENABLED":     "false",
		"KAPSEL_FFMPEG_PATH":              "/usr/local/bin/ffmpeg",
		"KAPSEL_RETENTION_WATCHED_AFTER":  "1h",
		"KAPSEL_RETENTION_INCLUDE_MANUAL": "true",
	}
	cfg := loadFromLookup(func(key string) (string, bool) {
		value, ok := env[key]
		return value, ok
	}, missingExecutable)

	if cfg.Addr != "127.0.0.1:9000" {
		t.Fatalf("unexpected addr %q", cfg.Addr)
	}
	if cfg.DataDir != "/srv/kapsel" {
		t.Fatalf("unexpected data dir %q", cfg.DataDir)
	}
	if cfg.DBPath != "/srv/db/app.db" {
		t.Fatalf("unexpected db path %q", cfg.DBPath)
	}
	if cfg.ImportRoot != "/srv/imports" {
		t.Fatalf("unexpected import root %q", cfg.ImportRoot)
	}
	if cfg.MediaRoot != "/srv/media" {
		t.Fatalf("unexpected media root %q", cfg.MediaRoot)
	}
	if cfg.MediaSigningSecret != "secret" {
		t.Fatalf("unexpected media signing secret %q", cfg.MediaSigningSecret)
	}
	if !cfg.MediaSigningSecretConfigured {
		t.Fatal("expected configured media signing secret to be marked stable")
	}
	if cfg.MediaURLTTL != 15*time.Minute {
		t.Fatalf("unexpected media URL TTL %s", cfg.MediaURLTTL)
	}
	if cfg.YTDLPPath != "/usr/local/bin/yt-dlp" {
		t.Fatalf("unexpected yt-dlp path %q", cfg.YTDLPPath)
	}
	if cfg.YTDLPFormat != "best[height<=480]" {
		t.Fatalf("unexpected yt-dlp format selector %q", cfg.YTDLPFormat)
	}
	if cfg.YTDLPCookiesFile != "/etc/kapsel/youtube.cookies.txt" {
		t.Fatalf("unexpected yt-dlp cookies file %q", cfg.YTDLPCookiesFile)
	}
	if cfg.YTDLPSleepInterval != 30*time.Second {
		t.Fatalf("unexpected yt-dlp sleep interval %s", cfg.YTDLPSleepInterval)
	}
	if cfg.YTDLPUpdateInterval != 12*time.Hour {
		t.Fatalf("unexpected yt-dlp update interval %s", cfg.YTDLPUpdateInterval)
	}
	if cfg.MinFreeSpaceBytes != 2<<30 {
		t.Fatalf("unexpected free-space headroom %d", cfg.MinFreeSpaceBytes)
	}
	if !cfg.PreviewsEnabled {
		t.Fatal("expected timeline previews to be enabled")
	}
	if cfg.SponsorBlockEnabled {
		t.Fatal("expected SponsorBlock integration to follow explicit disable")
	}
	if cfg.FFMPEGPath != "/usr/local/bin/ffmpeg" {
		t.Fatalf("unexpected ffmpeg path %q", cfg.FFMPEGPath)
	}
	if cfg.AuthMode != "disabled" || cfg.AuthUsername != "admin" || cfg.AuthPasswordHash != "$argon2id$v=19$m=65536,t=3,p=1$salt$hash" {
		t.Fatalf("unexpected auth configuration: mode=%q username=%q hash=%q", cfg.AuthMode, cfg.AuthUsername, cfg.AuthPasswordHash)
	}
	if cfg.SessionSecret != "session-secret" || !cfg.SessionSecretConfigured {
		t.Fatalf("unexpected session secret configuration: secret=%q configured=%v", cfg.SessionSecret, cfg.SessionSecretConfigured)
	}
	if !cfg.SessionCookieSecure {
		t.Fatal("expected secure session cookies to be enabled")
	}
	if cfg.SessionTTL != 24*time.Hour {
		t.Fatalf("unexpected session TTL %s", cfg.SessionTTL)
	}
	if cfg.RetentionWatchedAfter != time.Hour {
		t.Fatalf("unexpected retention watched interval %s", cfg.RetentionWatchedAfter)
	}
	if !cfg.RetentionIncludeManual {
		t.Fatal("expected manual media retention opt-in to be enabled")
	}
}

func TestLoadYTDLPSleepIntervalAllowsZero(t *testing.T) {
	t.Parallel()

	cfg := loadFromLookup(func(key string) (string, bool) {
		if key == EnvYTDLPSleepInterval {
			return "0s", true
		}

		return "", false
	}, missingExecutable)

	if cfg.YTDLPSleepInterval != 0 {
		t.Fatalf("expected zero yt-dlp sleep interval override, got %s", cfg.YTDLPSleepInterval)
	}
}

func TestLoadRetentionWatchedAfterAllowsZero(t *testing.T) {
	t.Parallel()

	cfg := loadFromLookup(func(key string) (string, bool) {
		if key == EnvRetentionWatchedAfter {
			return "0s", true
		}

		return "", false
	}, missingExecutable)

	if cfg.RetentionWatchedAfter != 0 {
		t.Fatalf("expected zero retention watched interval to disable watched cleanup, got %s", cfg.RetentionWatchedAfter)
	}
}

func TestDeploymentEnvExampleDocumentsSponsorBlockDisable(t *testing.T) {
	t.Parallel()

	body, err := os.ReadFile("../../deploy/kapsel.env.example")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "KAPSEL_SPONSORBLOCK_ENABLED=false") {
		t.Fatal("expected deployment env example to document disabling SponsorBlock")
	}
	if !strings.Contains(string(body), "KAPSEL_YTDLP_COOKIES_FILE=/etc/kapsel/youtube.cookies.txt") {
		t.Fatal("expected deployment env example to document yt-dlp cookies file")
	}
	if !strings.Contains(string(body), "KAPSEL_YTDLP_SLEEP_INTERVAL=10s") {
		t.Fatal("expected deployment env example to document yt-dlp sleep interval")
	}
}

func TestLoadDataDirDerivesPaths(t *testing.T) {
	t.Parallel()

	cfg := loadFromLookup(func(key string) (string, bool) {
		if key == "KAPSEL_DATA_DIR" {
			return "/tmp/kapsel-data", true
		}

		return "", false
	}, missingExecutable)

	if cfg.DBPath != "/tmp/kapsel-data/kapsel.db" {
		t.Fatalf("expected derived db path, got %q", cfg.DBPath)
	}
	if cfg.ImportRoot != "/tmp/kapsel-data/imports" {
		t.Fatalf("expected derived import root, got %q", cfg.ImportRoot)
	}
	if cfg.MediaRoot != "/tmp/kapsel-data/media" {
		t.Fatalf("expected derived media root, got %q", cfg.MediaRoot)
	}
}

func availableExecutable(path string) (string, error) {
	return path, nil
}

func missingExecutable(string) (string, error) {
	return "", errExecutableMissing{}
}

type errExecutableMissing struct{}

func (errExecutableMissing) Error() string { return "executable missing" }
