package config

import (
	"crypto/rand"
	"encoding/base64"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"kapsel/internal/diskspace"
	"kapsel/internal/download"
)

const (
	EnvAddr                        = "KAPSEL_ADDR"
	EnvAuthMode                    = "KAPSEL_AUTH_MODE"
	EnvAuthPasswordHash            = "KAPSEL_AUTH_PASSWORD_HASH"
	EnvAuthUsername                = "KAPSEL_AUTH_USERNAME"
	EnvDataDir                     = "KAPSEL_DATA_DIR"
	EnvDBPath                      = "KAPSEL_DB_PATH"
	EnvImportRoot                  = "KAPSEL_IMPORT_ROOT"
	EnvMediaRoot                   = "KAPSEL_MEDIA_ROOT"
	EnvMediaSigningSecret          = "KAPSEL_MEDIA_SIGNING_SECRET"
	EnvMediaURLTTL                 = "KAPSEL_MEDIA_URL_TTL"
	EnvMinFreeSpace                = "KAPSEL_MIN_FREE_SPACE"
	EnvPreviewsEnabled             = "KAPSEL_PREVIEWS_ENABLED"
	EnvSponsorBlockEnabled         = "KAPSEL_SPONSORBLOCK_ENABLED"
	EnvSessionCookieSecure         = "KAPSEL_SESSION_COOKIE_SECURE"
	EnvSessionSecret               = "KAPSEL_SESSION_SECRET"
	EnvSessionTTL                  = "KAPSEL_SESSION_TTL"
	EnvFFMPEGPath                  = "KAPSEL_FFMPEG_PATH"
	EnvYTDLPCookiesFile            = "KAPSEL_YTDLP_COOKIES_FILE"
	EnvYTDLPFormat                 = "KAPSEL_YTDLP_FORMAT"
	EnvYTDLPPath                   = "KAPSEL_YTDLP_PATH"
	EnvYTDLPSleepInterval          = "KAPSEL_YTDLP_SLEEP_INTERVAL"
	EnvYTDLPUpdateInterval         = "KAPSEL_YTDLP_UPDATE_INTERVAL"
	EnvUpdateRepo                  = "KAPSEL_UPDATE_REPO"
	EnvUpdateCheckInterval         = "KAPSEL_UPDATE_CHECK_INTERVAL"
	EnvSubtitlesEnabled            = "KAPSEL_SUBTITLES_ENABLED"
	EnvChannelAutoDownloadInterval = "KAPSEL_CHANNEL_AUTO_DOWNLOAD_INTERVAL"
	EnvRetentionWatchedAfter       = "KAPSEL_RETENTION_WATCHED_AFTER"
)

const defaultYTDLPFormat = "bv[height<=1080][ext=mp4][vcodec^=avc1][acodec=none]+ba[ext=m4a][acodec^=mp4a]/b[height<=1080][ext=mp4][vcodec^=avc1][acodec^=mp4a]/b[height<=1080][ext=mp4]/best[height<=1080]"

const (
	// DefaultUpdateRepo is the GitHub owner/name repository checked for
	// application updates.
	DefaultUpdateRepo = "mufflegyro/yummle"
	// DefaultUpdateCheckInterval paces background GitHub release checks.
	DefaultUpdateCheckInterval = 24 * time.Hour
)

const DefaultMediaURLTTL = 24 * time.Hour

type Config struct {
	Addr                         string
	AuthMode                     string
	AuthUsername                 string
	AuthPasswordHash             string
	DataDir                      string
	DBPath                       string
	ImportRoot                   string
	MediaRoot                    string
	MediaSigningSecret           string
	MediaSigningSecretConfigured bool
	MediaURLTTL                  time.Duration
	MinFreeSpaceBytes            uint64
	PreviewsEnabled              bool
	SponsorBlockEnabled          bool
	SessionCookieSecure          bool
	SessionSecret                string
	SessionSecretConfigured      bool
	SessionTTL                   time.Duration
	FFMPEGPath                   string
	YTDLPCookiesFile             string
	YTDLPFormat                  string
	YTDLPPath                    string
	YTDLPSleepInterval           time.Duration
	YTDLPUpdateInterval          time.Duration
	SubtitlesEnabled             bool
	ChannelAutoDownloadInterval  time.Duration
	RetentionWatchedAfter        time.Duration
	UpdateRepo                   string
	UpdateCheckInterval          time.Duration
}

func Load() Config {
	return LoadFromLookup(os.LookupEnv)
}

func LoadFromLookup(lookup func(string) (string, bool)) Config {
	return loadFromLookup(lookup, exec.LookPath)
}

func loadFromLookup(lookup func(string) (string, bool), lookPath func(string) (string, error)) Config {
	dataDir := valueOrDefault(lookup, EnvDataDir, "data")
	mediaSigningSecret, mediaSigningSecretConfigured := mediaSigningSecret(lookup)
	sessionSecret, sessionSecretConfigured := sessionSecret(lookup)
	ffmpegPath := valueOrDefault(lookup, EnvFFMPEGPath, "ffmpeg")

	return Config{
		Addr:                         valueOrDefault(lookup, EnvAddr, ":8080"),
		AuthMode:                     authMode(lookup),
		AuthUsername:                 strings.TrimSpace(valueOrDefault(lookup, EnvAuthUsername, "")),
		AuthPasswordHash:             strings.TrimSpace(valueOrDefault(lookup, EnvAuthPasswordHash, "")),
		DataDir:                      dataDir,
		DBPath:                       valueOrDefault(lookup, EnvDBPath, filepath.Join(dataDir, "kapsel.db")),
		ImportRoot:                   valueOrDefault(lookup, EnvImportRoot, filepath.Join(dataDir, "imports")),
		MediaRoot:                    valueOrDefault(lookup, EnvMediaRoot, filepath.Join(dataDir, "media")),
		MediaSigningSecret:           mediaSigningSecret,
		MediaSigningSecretConfigured: mediaSigningSecretConfigured,
		MediaURLTTL:                  durationOrDefault(lookup, EnvMediaURLTTL, DefaultMediaURLTTL),
		MinFreeSpaceBytes:            bytesOrDefault(lookup, EnvMinFreeSpace, diskspace.DefaultMinFreeBytes),
		PreviewsEnabled:              previewsEnabledOrDefault(lookup, ffmpegPath, lookPath),
		SponsorBlockEnabled:          boolOrDefault(lookup, EnvSponsorBlockEnabled, true),
		SessionCookieSecure:          boolOrDefault(lookup, EnvSessionCookieSecure, false),
		SessionSecret:                sessionSecret,
		SessionSecretConfigured:      sessionSecretConfigured,
		SessionTTL:                   durationOrDefault(lookup, EnvSessionTTL, 7*24*time.Hour),
		FFMPEGPath:                   ffmpegPath,
		SubtitlesEnabled:             boolOrDefault(lookup, EnvSubtitlesEnabled, true),
		ChannelAutoDownloadInterval:  nonNegativeDurationOrDefault(lookup, EnvChannelAutoDownloadInterval, download.DefaultChannelAutoSyncInterval),
		RetentionWatchedAfter:        nonNegativeDurationOrDefault(lookup, EnvRetentionWatchedAfter, download.DefaultRetentionWatchedAfter),
		YTDLPCookiesFile:             strings.TrimSpace(valueOrDefault(lookup, EnvYTDLPCookiesFile, "")),
		YTDLPFormat:                  valueOrDefault(lookup, EnvYTDLPFormat, defaultYTDLPFormat),
		YTDLPPath:                    valueOrDefault(lookup, EnvYTDLPPath, "yt-dlp"),
		YTDLPSleepInterval:           nonNegativeDurationOrDefault(lookup, EnvYTDLPSleepInterval, download.DefaultYTDLPSleepInterval),
		YTDLPUpdateInterval:          nonNegativeDurationOrDefault(lookup, EnvYTDLPUpdateInterval, download.DefaultYTDLPUpdateInterval),
		UpdateRepo:                   updateRepo(lookup),
		UpdateCheckInterval:          nonNegativeDurationOrDefault(lookup, EnvUpdateCheckInterval, DefaultUpdateCheckInterval),
	}
}

func updateRepo(lookup func(string) (string, bool)) string {
	return strings.TrimSpace(valueOrDefault(lookup, EnvUpdateRepo, DefaultUpdateRepo))
}

func previewsEnabledOrDefault(lookup func(string) (string, bool), ffmpegPath string, lookPath func(string) (string, error)) bool {
	return boolOrDefault(lookup, EnvPreviewsEnabled, executableAvailable(ffmpegPath, lookPath))
}

func executableAvailable(path string, lookPath func(string) (string, error)) bool {
	path = strings.TrimSpace(path)
	if path == "" {
		return false
	}
	if lookPath == nil {
		lookPath = exec.LookPath
	}
	_, err := lookPath(path)

	return err == nil
}

func authMode(lookup func(string) (string, bool)) string {
	mode := strings.ToLower(strings.TrimSpace(valueOrDefault(lookup, EnvAuthMode, "required")))
	switch mode {
	case "disabled", "required":
		return mode
	default:
		return "required"
	}
}

func mediaSigningSecret(lookup func(string) (string, bool)) (string, bool) {
	if value, ok := lookup(EnvMediaSigningSecret); ok && value != "" {
		return value, true
	}

	return randomSigningSecret(), false
}

func sessionSecret(lookup func(string) (string, bool)) (string, bool) {
	if value, ok := lookup(EnvSessionSecret); ok && value != "" {
		return value, true
	}

	return randomSigningSecret(), false
}

func randomSigningSecret() string {
	var secret [32]byte
	if _, err := rand.Read(secret[:]); err != nil {
		panic(err)
	}

	return base64.RawURLEncoding.EncodeToString(secret[:])
}

func valueOrDefault(lookup func(string) (string, bool), key string, fallback string) string {
	value, ok := lookup(key)
	if !ok || value == "" {
		return fallback
	}

	return value
}

func durationOrDefault(lookup func(string) (string, bool), key string, fallback time.Duration) time.Duration {
	value, ok := lookup(key)
	if !ok || value == "" {
		return fallback
	}
	duration, err := time.ParseDuration(value)
	if err != nil || duration <= 0 {
		return fallback
	}

	return duration
}

func nonNegativeDurationOrDefault(lookup func(string) (string, bool), key string, fallback time.Duration) time.Duration {
	value, ok := lookup(key)
	if !ok || value == "" {
		return fallback
	}
	duration, err := time.ParseDuration(value)
	if err != nil || duration < 0 {
		return fallback
	}

	return duration
}

func bytesOrDefault(lookup func(string) (string, bool), key string, fallback uint64) uint64 {
	value, ok := lookup(key)
	if !ok || value == "" {
		return fallback
	}
	bytes, err := diskspace.ParseBytes(value)
	if err != nil {
		return fallback
	}

	return bytes
}

func boolOrDefault(lookup func(string) (string, bool), key string, fallback bool) bool {
	value, ok := lookup(key)
	if !ok || value == "" {
		return fallback
	}
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	default:
		return fallback
	}
}
