package download

import (
	"bytes"
	"context"
	crand "crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/big"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"kapsel/internal/assetpath"
	"kapsel/internal/denorm"
	"kapsel/internal/diskspace"
	"kapsel/internal/jobs"
	"kapsel/internal/previews"
	"kapsel/internal/sandbox"
)

const (
	defaultYTDLPPath                = "yt-dlp"
	JobType                         = "download"
	ChannelJobType                  = "channel_first_download"
	ChannelScanJobType              = "channel_scan"
	ChannelAutoDownloadJobType      = "channel_auto_download"
	RetentionJobType                = "retention_cleanup"
	YTDLPUpdateJobType              = "ytdlp_update"
	DownloadOriginManual            = "manual"
	DownloadOriginChannelAuto       = "channel_auto"
	MediaOriginImported             = "imported"
	DefaultChannelCatalogLimit      = 500
	DefaultChannelCatalogPageSize   = 30
	DefaultChannelAutoDownloadLimit = 2
	DefaultRetentionLimit           = 100
	maxChannelCatalogOutputBytes    = 4 * 1024 * 1024
	maxCatalogThumbnailURLLength    = 2048
	maxErrorOutput                  = 64 * 1024
	maxInFlightDownloadProgress     = 0.85
)

const DefaultChannelAutoSyncInterval = 24 * time.Hour

const DefaultRetentionInterval = 24 * time.Hour

const DefaultRetentionStaleAfter = 14 * 24 * time.Hour

const DefaultRetentionWatchedAfter = 24 * time.Hour

const DefaultYTDLPUpdateInterval = 24 * time.Hour

const DefaultFormatSelector = "bv[height<=1080][ext=mp4][vcodec^=avc1][acodec=none]+ba[ext=m4a][acodec^=mp4a]/b[height<=1080][ext=mp4][vcodec^=avc1][acodec^=mp4a]/b[height<=1080][ext=mp4]/best[height<=1080]"

const DefaultYTDLPSleepInterval = 10 * time.Second

const DefaultYTDLPRetryDelay = 10 * time.Minute

const DefaultYTDLPAuthRetryDelay = time.Hour

const DefaultSubtitleLanguages = "all"

const DefaultAutomaticSubtitleLanguages = ".*-orig"

var thumbnailFileExtensions = []string{".jpg", ".jpeg", ".webp", ".png", ".avif"}
var ytdlpDownloadProgressPattern = regexp.MustCompile(`^\[download\]\s+([0-9]+(?:\.[0-9]+)?)%`)

var ytdlpStdoutStatusPrefixes = []string{
	"[download]",
	"[info]",
	"[youtube]",
	"[Merger]",
	"[MoveFiles]",
	"[ExtractAudio]",
	"[VideoRemuxer]",
	"[Metadata]",
	"[EmbedSubtitle]",
	"[SubtitlesConvertor]",
	"[ThumbnailsConvertor]",
	"[Fixup",
	"[ffmpeg]",
}

var (
	ErrDownloadURLRequired   = errors.New("download URL is required")
	ErrUnsupportedURLScheme  = errors.New("download URL must use http or https")
	ErrUnsupportedChannelURL = errors.New("channel URL must be a supported YouTube channel URL")
	ErrUnsupportedVideoURL   = errors.New("download URL must be a supported single video URL")
)

type Config struct {
	YTDLPPath          string
	YTDLPSleepInterval time.Duration
	DataRoot           string
	MediaRoot          string
	FormatSelector     string
	YTDLPCookiesFile   string
	MinFreeSpaceBytes  uint64
	Stat               diskspace.StatFunc
	PreviewsEnabled    bool
	FFMPEGPath         string
	PreviewRunner      previews.Runner
	JobStore           *jobs.Store
	ytdlpSleep         func(context.Context, time.Duration) error
	ytdlpSleepJitter   func(time.Duration) time.Duration
	ytdlpNow           func() time.Time
}

type Command struct {
	Name           string
	Args           []string
	Dir            string
	Kind           sandbox.Kind
	Access         sandbox.Access
	Network        sandbox.NetworkPolicy
	MaxStdoutBytes int
	Progress       func(float64) error
}

type Runner interface {
	Run(context.Context, Command) ([]byte, error)
}

type sqlExecutor interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

type ExecRunner struct {
	Backend sandbox.Backend
}

func (r ExecRunner) Run(ctx context.Context, command Command) ([]byte, error) {
	stdout := limitedBuffer{max: command.MaxStdoutBytes}
	stderrMax := maxErrorOutput
	if command.MaxStdoutBytes <= 0 {
		stderrMax = 0
	}
	stderr := limitedBuffer{max: stderrMax}
	progress := progressWriter{buffer: &stderr, progress: command.Progress}
	stdoutProgress := stdoutProgressWriter{buffer: &stdout, progress: &progress}
	backend := r.Backend
	if backend == nil {
		backend = sandbox.BasicBackend{}
	}
	kind := command.Kind
	if kind == "" {
		kind = sandbox.KindYTDLP
	}
	err := backend.Run(ctx, sandbox.Spec{
		Name:    command.Name,
		Args:    command.Args,
		Kind:    kind,
		Dir:     command.Dir,
		Access:  command.Access,
		Network: command.Network,
	}, sandbox.IO{Stdout: &stdoutProgress, Stderr: &progress})
	stdoutProgress.Flush()
	progress.Flush()
	if err != nil {
		stdoutBytes := stdout.CappedBytes(maxErrorOutput)
		if command.MaxStdoutBytes <= 0 {
			stdoutBytes = stdout.Bytes()
		}
		return combinedOutput(stdoutBytes, stderr.Bytes()), err
	}
	if progress.err != nil {
		return combinedOutput(stdout.CappedBytes(maxErrorOutput), stderr.Bytes()), progress.err
	}

	return stdout.Bytes(), nil
}

type stdoutProgressWriter struct {
	buffer   *limitedBuffer
	progress *progressWriter
	pending  string
}

func (w *stdoutProgressWriter) Write(p []byte) (int, error) {
	accepted := len(p)
	text := strings.ReplaceAll(string(p), "\r", "\n")
	parts := strings.Split(w.pending+text, "\n")
	w.pending = parts[len(parts)-1]
	for _, line := range parts[:len(parts)-1] {
		w.recordLine(line, true)
	}

	return accepted, nil
}

func (w *stdoutProgressWriter) Flush() {
	if w.pending == "" {
		return
	}
	w.recordLine(w.pending, false)
	w.pending = ""
}

func (w *stdoutProgressWriter) recordLine(line string, trailingNewline bool) {
	if _, ok := parseYTDLPDownloadProgress(line); ok {
		if w.progress != nil {
			w.progress.recordLine(line)
		}
		return
	}
	if isYTDLPStdoutStatusLine(line) {
		return
	}
	if w.buffer == nil {
		return
	}

	_, _ = w.buffer.Write([]byte(line))
	if trailingNewline {
		_, _ = w.buffer.Write([]byte("\n"))
	}
}

func isYTDLPStdoutStatusLine(line string) bool {
	line = strings.TrimLeft(line, " \t")
	for _, prefix := range ytdlpStdoutStatusPrefixes {
		if strings.HasPrefix(line, prefix) {
			return true
		}
	}

	return false
}

type progressWriter struct {
	buffer       *limitedBuffer
	progress     func(float64) error
	pending      string
	lastProgress float64
	err          error
}

func (w *progressWriter) Write(p []byte) (int, error) {
	accepted := len(p)
	if w.buffer != nil {
		_, _ = w.buffer.Write(p)
	}
	if w.progress == nil || w.err != nil {
		return accepted, nil
	}

	text := strings.ReplaceAll(string(p), "\r", "\n")
	parts := strings.Split(w.pending+text, "\n")
	w.pending = parts[len(parts)-1]
	for _, line := range parts[:len(parts)-1] {
		w.recordLine(line)
	}

	return accepted, nil
}

func (w *progressWriter) Flush() {
	if w.pending == "" || w.progress == nil || w.err != nil {
		w.pending = ""
		return
	}
	w.recordLine(w.pending)
	w.pending = ""
}

func (w *progressWriter) recordLine(line string) {
	if w.progress == nil || w.err != nil {
		return
	}
	progress, ok := parseYTDLPDownloadProgress(line)
	if progress > maxInFlightDownloadProgress {
		progress = maxInFlightDownloadProgress
	}
	if !ok || progress < w.lastProgress+0.005 {
		return
	}
	if err := w.progress(progress); err != nil {
		w.err = err
		return
	}
	w.lastProgress = progress
}

func parseYTDLPDownloadProgress(line string) (float64, bool) {
	match := ytdlpDownloadProgressPattern.FindStringSubmatch(line)
	if match == nil {
		return 0, false
	}
	value, err := strconv.ParseFloat(match[1], 64)
	if err != nil || value < 0 || value > 100 {
		return 0, false
	}

	return value / 100, true
}

type limitedBuffer struct {
	bytes.Buffer
	max int
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	if b.max <= 0 {
		return b.Buffer.Write(p)
	}

	accepted := len(p)
	remaining := b.max - b.Len()
	if remaining > 0 {
		if len(p) > remaining {
			p = p[:remaining]
		}
		_, _ = b.Buffer.Write(p)
	}

	return accepted, nil
}

func (b *limitedBuffer) CappedBytes(max int) []byte {
	if max <= 0 || b.Len() <= max {
		return b.Bytes()
	}

	return b.Bytes()[:max]
}

func combinedOutput(stdout []byte, stderr []byte) []byte {
	if len(stdout) == 0 {
		return stderr
	}
	if len(stderr) == 0 {
		return stdout
	}

	output := make([]byte, 0, len(stdout)+1+len(stderr))
	output = append(output, stdout...)
	output = append(output, '\n')
	output = append(output, stderr...)

	return output
}

type Downloader struct {
	db                 *sql.DB
	store              *jobs.Store
	config             Config
	runner             Runner
	ytdlpMu            sync.Mutex
	lastYTDLPStartedAt time.Time
}

type Payload struct {
	URL    string `json:"url"`
	Origin string `json:"origin,omitempty"`
}

func NormalizeDownloadPayload(payload Payload) (Payload, error) {
	url, err := NormalizeDirectVideoURL(payload.URL)
	if err != nil {
		return Payload{}, err
	}
	payload.URL = url
	if strings.TrimSpace(payload.Origin) == DownloadOriginChannelAuto {
		payload.Origin = DownloadOriginChannelAuto
	} else {
		payload.Origin = ""
	}

	return payload, nil
}

func EnqueueDownload(ctx context.Context, store *jobs.Store, payload Payload) (jobs.Job, error) {
	if store == nil {
		return jobs.Job{}, errors.New("download enqueue missing job store")
	}
	payload, payloadJSON, err := canonicalDownloadPayload(payload)
	if err != nil {
		return jobs.Job{}, err
	}
	job, _, err := store.FindOrEnqueue(ctx, jobs.EnqueueParams{Type: JobType, PayloadJSON: payloadJSON}, func(ctx context.Context, tx *sql.Tx) (jobs.Job, bool, error) {
		return activeDownloadJobForURL(ctx, store, tx, payload.URL, true)
	})

	return job, err
}

func enqueueDownloadTx(ctx context.Context, store *jobs.Store, tx *sql.Tx, payload Payload, includeCancelRequested bool) (jobs.Job, bool, error) {
	if store == nil {
		return jobs.Job{}, false, errors.New("download enqueue missing job store")
	}
	payload, payloadJSON, err := canonicalDownloadPayload(payload)
	if err != nil {
		return jobs.Job{}, false, err
	}

	return store.FindOrEnqueueTx(ctx, tx, jobs.EnqueueParams{Type: JobType, PayloadJSON: payloadJSON}, func(ctx context.Context, tx *sql.Tx) (jobs.Job, bool, error) {
		return activeDownloadJobForURL(ctx, store, tx, payload.URL, includeCancelRequested)
	})
}

func canonicalDownloadPayload(payload Payload) (Payload, string, error) {
	payload, err := NormalizeDownloadPayload(payload)
	if err != nil {
		return Payload{}, "", err
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return Payload{}, "", err
	}

	return payload, string(body), nil
}

func ActiveJobForPayload(ctx context.Context, store *jobs.Store, payloadJSON string) (jobs.Job, bool, error) {
	if store == nil {
		return jobs.Job{}, false, nil
	}
	var target Payload
	if err := json.Unmarshal([]byte(payloadJSON), &target); err != nil {
		return jobs.Job{}, false, err
	}
	targetURL, err := NormalizeDownloadURL(target.URL)
	if err != nil {
		return jobs.Job{}, false, err
	}

	return activeDownloadJobForURL(ctx, store, nil, targetURL, true)
}

func activeDownloadJobForURL(ctx context.Context, store *jobs.Store, tx *sql.Tx, targetURL string, includeCancelRequested bool) (jobs.Job, bool, error) {
	if store == nil {
		return jobs.Job{}, false, nil
	}
	targetURL, err := NormalizeDownloadURL(targetURL)
	if err != nil {
		return jobs.Job{}, false, err
	}
	var activeJobs []jobs.Job
	if tx != nil {
		if includeCancelRequested {
			activeJobs, err = store.ActiveByTypeTx(ctx, tx, JobType, jobs.MaxActiveLookupLimit)
		} else {
			activeJobs, err = store.ActiveByTypeWithoutCancelRequestedTx(ctx, tx, JobType, jobs.MaxActiveLookupLimit)
		}
	} else {
		activeJobs, err = store.ActiveByType(ctx, JobType, jobs.MaxActiveLookupLimit)
	}
	if err != nil {
		return jobs.Job{}, false, err
	}
	for _, job := range activeJobs {
		var existing Payload
		if err := json.Unmarshal([]byte(job.PayloadJSON), &existing); err != nil {
			continue
		}
		existingURL, err := NormalizeDownloadURL(existing.URL)
		if err != nil {
			continue
		}
		if existingURL == targetURL {
			return job, true, nil
		}
	}

	return jobs.Job{}, false, nil
}

type ChannelScanPayload struct {
	URL       string `json:"url"`
	ChannelID string `json:"channel_id"`
}

func EnqueueChannelFirst(ctx context.Context, store *jobs.Store, payload Payload) (jobs.Job, error) {
	if store == nil {
		return jobs.Job{}, errors.New("channel enqueue missing job store")
	}
	payload.URL = strings.TrimSpace(payload.URL)
	channelURL, err := NormalizeChannelURL(payload.URL)
	if err != nil {
		return jobs.Job{}, err
	}
	payload.URL = channelURL
	payload.Origin = ""
	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		return jobs.Job{}, err
	}
	job, _, err := store.FindOrEnqueue(ctx, jobs.EnqueueParams{Type: ChannelJobType, PayloadJSON: string(payloadJSON)}, func(ctx context.Context, tx *sql.Tx) (jobs.Job, bool, error) {
		return store.ActiveByPayloadTx(ctx, tx, ChannelJobType, string(payloadJSON))
	})

	return job, err
}

func ChannelScanPayloadForExternalID(channelID string, externalID string) (ChannelScanPayload, error) {
	channelID = strings.TrimSpace(channelID)
	if channelID == "" {
		return ChannelScanPayload{}, errors.New("channel scan payload missing channel id")
	}
	channelURL, err := channelURLFromExternalID(externalID)
	if err != nil {
		return ChannelScanPayload{}, err
	}

	return ChannelScanPayload{URL: channelURL, ChannelID: channelID}, nil
}

func EnqueueChannelScan(ctx context.Context, store *jobs.Store, payload ChannelScanPayload) (jobs.Job, error) {
	if store == nil {
		return jobs.Job{}, errors.New("channel scan enqueue missing job store")
	}
	payload.ChannelID = strings.TrimSpace(payload.ChannelID)
	if payload.ChannelID == "" {
		return jobs.Job{}, errors.New("channel scan payload missing channel id")
	}
	channelURL, err := NormalizeChannelURL(payload.URL)
	if err != nil {
		return jobs.Job{}, err
	}
	payload.URL = channelURL
	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		return jobs.Job{}, err
	}
	job, _, err := store.FindOrEnqueue(ctx, jobs.EnqueueParams{Type: ChannelScanJobType, PayloadJSON: string(payloadJSON)}, func(ctx context.Context, tx *sql.Tx) (jobs.Job, bool, error) {
		return store.ActiveByPayloadTx(ctx, tx, ChannelScanJobType, string(payloadJSON))
	})

	return job, err
}

type ChannelAutoDownloadPayload struct {
	URL       string `json:"url"`
	ChannelID string `json:"channel_id"`
}

func EnqueueChannelAutoDownload(ctx context.Context, store *jobs.Store, payload ChannelAutoDownloadPayload, lastScannedAt string, runAfter time.Time) (jobs.Job, bool, error) {
	if store == nil {
		return jobs.Job{}, false, errors.New("channel auto scheduler missing job store")
	}
	payload.ChannelID = strings.TrimSpace(payload.ChannelID)
	if payload.ChannelID == "" {
		return jobs.Job{}, false, errors.New("channel auto-download payload missing channel id")
	}
	channelURL, err := NormalizeChannelURL(payload.URL)
	if err != nil {
		return jobs.Job{}, false, err
	}
	payload.URL = channelURL
	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		return jobs.Job{}, false, err
	}

	return store.FindOrEnqueue(ctx, jobs.EnqueueParams{Type: ChannelAutoDownloadJobType, PayloadJSON: string(payloadJSON), MaxAttempts: 1, RunAfter: runAfter}, func(ctx context.Context, tx *sql.Tx) (jobs.Job, bool, error) {
		return activeChannelAutoDownloadJob(ctx, store, tx, payload.ChannelID, lastScannedAt)
	})
}

func EnqueueRetentionCleanup(ctx context.Context, store *jobs.Store, runAfter time.Time) (jobs.Job, bool, error) {
	if store == nil {
		return jobs.Job{}, false, errors.New("retention scheduler missing job store")
	}

	return store.FindOrEnqueue(ctx, jobs.EnqueueParams{Type: RetentionJobType, PayloadJSON: `{}`, MaxAttempts: 1, RunAfter: runAfter}, func(ctx context.Context, tx *sql.Tx) (jobs.Job, bool, error) {
		return store.ActiveByPayloadWithoutCancelRequestedTx(ctx, tx, RetentionJobType, `{}`)
	})
}

func EnqueueYTDLPUpdate(ctx context.Context, store *jobs.Store, runAfter time.Time) (jobs.Job, bool, error) {
	if store == nil {
		return jobs.Job{}, false, errors.New("yt-dlp update scheduler missing job store")
	}

	return store.FindOrEnqueue(ctx, jobs.EnqueueParams{Type: YTDLPUpdateJobType, PayloadJSON: `{}`, MaxAttempts: 1, RunAfter: runAfter}, func(ctx context.Context, tx *sql.Tx) (jobs.Job, bool, error) {
		return store.ActiveByPayloadWithoutCancelRequestedTx(ctx, tx, YTDLPUpdateJobType, `{}`)
	})
}

type ChannelAutoScheduleOptions struct {
	Now      func() time.Time
	Interval time.Duration
	Jitter   func(time.Duration) time.Duration
}

type RetentionScheduleOptions struct {
	Now      func() time.Time
	Interval time.Duration
}

type YTDLPUpdateScheduleOptions struct {
	Now      func() time.Time
	Interval time.Duration
}

type RetentionOptions struct {
	Now          func() time.Time
	StaleAfter   time.Duration
	WatchedAfter time.Duration
	Limit        int
}

type RetentionResult struct {
	Checked         int      `json:"checked"`
	Removed         int      `json:"removed"`
	RemovedVideoIDs []string `json:"removed_video_ids,omitempty"`
}

func NewDownloader(db *sql.DB, config Config, runner Runner) *Downloader {
	if config.YTDLPPath == "" {
		config.YTDLPPath = defaultYTDLPPath
	}
	if config.FormatSelector == "" {
		config.FormatSelector = DefaultFormatSelector
	}
	if config.FFMPEGPath == "" {
		config.FFMPEGPath = previews.DefaultFFMPEGPath
	}
	if runner == nil {
		runner = ExecRunner{}
	}
	return &Downloader{db: db, store: config.JobStore, config: config, runner: runner}
}

func EnsureChannelAutoDownloadJobs(ctx context.Context, db *sql.DB, store *jobs.Store, options ChannelAutoScheduleOptions) (int, error) {
	if db == nil {
		return 0, errors.New("channel auto scheduler missing database")
	}
	if store == nil {
		return 0, errors.New("channel auto scheduler missing job store")
	}
	nowFunc := options.Now
	if nowFunc == nil {
		nowFunc = func() time.Time { return time.Now().UTC() }
	}
	interval := options.Interval
	if interval <= 0 {
		interval = DefaultChannelAutoSyncInterval
	}
	jitter := options.Jitter
	if jitter == nil {
		jitter = randomDelay
	}

	channels, err := subscribedChannels(ctx, db)
	if err != nil {
		return 0, err
	}
	created := 0
	now := nowFunc().UTC()
	for _, channel := range channels {
		channelURL, err := channelURLFromExternalID(channel.ExternalID)
		if err != nil {
			continue
		}
		_, wasCreated, err := EnqueueChannelAutoDownload(ctx, store, ChannelAutoDownloadPayload{URL: channelURL, ChannelID: channel.ID}, channel.LastScannedAt, nextChannelAutoRun(now, channel.LastScannedAt, interval, jitter))
		if err != nil {
			return created, err
		}
		if wasCreated {
			created++
		}
	}

	return created, nil
}

func EnsureRetentionJobs(ctx context.Context, db *sql.DB, store *jobs.Store, options RetentionScheduleOptions) (int, error) {
	if db == nil {
		return 0, errors.New("retention scheduler missing database")
	}
	if store == nil {
		return 0, errors.New("retention scheduler missing job store")
	}
	nowFunc := options.Now
	if nowFunc == nil {
		nowFunc = func() time.Time { return time.Now().UTC() }
	}
	interval := options.Interval
	if interval <= 0 {
		interval = DefaultRetentionInterval
	}
	now := nowFunc().UTC()

	var active bool
	if err := db.QueryRowContext(ctx, `
SELECT EXISTS(
  SELECT 1
  FROM jobs
  WHERE type = ?
    AND status IN (?, ?)
    AND cancel_requested = 0
)`, RetentionJobType, jobs.StatusQueued, jobs.StatusRunning).Scan(&active); err != nil {
		return 0, err
	}
	if active {
		return 0, nil
	}

	var latestCreatedAt string
	var latestStatus jobs.Status
	err := db.QueryRowContext(ctx, "SELECT created_at, status FROM jobs WHERE type = ? ORDER BY created_at DESC LIMIT 1", RetentionJobType).Scan(&latestCreatedAt, &latestStatus)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return 0, err
	}
	if err == nil && latestStatus != jobs.StatusFailed {
		latest, parseErr := time.Parse(time.RFC3339Nano, strings.TrimSpace(latestCreatedAt))
		if parseErr == nil && now.Sub(latest.UTC()) < interval {
			return 0, nil
		}
	}

	_, created, err := EnqueueRetentionCleanup(ctx, store, now)
	if err != nil {
		return 0, err
	}
	if !created {
		return 0, nil
	}

	return 1, nil
}

func EnsureYTDLPUpdateJobs(ctx context.Context, db *sql.DB, store *jobs.Store, options YTDLPUpdateScheduleOptions) (int, error) {
	if db == nil {
		return 0, errors.New("yt-dlp update scheduler missing database")
	}
	if store == nil {
		return 0, errors.New("yt-dlp update scheduler missing job store")
	}
	nowFunc := options.Now
	if nowFunc == nil {
		nowFunc = func() time.Time { return time.Now().UTC() }
	}
	interval := options.Interval
	if interval <= 0 {
		interval = DefaultYTDLPUpdateInterval
	}
	now := nowFunc().UTC()

	var active bool
	if err := db.QueryRowContext(ctx, `
SELECT EXISTS(
  SELECT 1
  FROM jobs
  WHERE type = ?
    AND status IN (?, ?)
    AND cancel_requested = 0
)`, YTDLPUpdateJobType, jobs.StatusQueued, jobs.StatusRunning).Scan(&active); err != nil {
		return 0, err
	}
	if active {
		return 0, nil
	}

	var latestCreatedAt string
	var latestStatus jobs.Status
	err := db.QueryRowContext(ctx, "SELECT created_at, status FROM jobs WHERE type = ? ORDER BY created_at DESC LIMIT 1", YTDLPUpdateJobType).Scan(&latestCreatedAt, &latestStatus)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return 0, err
	}
	if err == nil && latestStatus != jobs.StatusFailed {
		latest, parseErr := time.Parse(time.RFC3339Nano, strings.TrimSpace(latestCreatedAt))
		if parseErr == nil && now.Sub(latest.UTC()) < interval {
			return 0, nil
		}
	}

	_, created, err := EnqueueYTDLPUpdate(ctx, store, now)
	if err != nil {
		return 0, err
	}
	if !created {
		return 0, nil
	}

	return 1, nil
}

type autoDownloadChannel struct {
	ID            string
	ExternalID    string
	LastScannedAt string
}

func subscribedChannels(ctx context.Context, db *sql.DB) ([]autoDownloadChannel, error) {
	rows, err := db.QueryContext(ctx, "SELECT id, external_id, COALESCE(last_scanned_at, '') FROM channels WHERE subscribed = 1 ORDER BY id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	channels := []autoDownloadChannel{}
	for rows.Next() {
		var channel autoDownloadChannel
		if err := rows.Scan(&channel.ID, &channel.ExternalID, &channel.LastScannedAt); err != nil {
			return nil, err
		}
		channels = append(channels, channel)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	return channels, nil
}

type activeChannelAutoJob struct {
	CreatedAt string
}

func hasCurrentChannelAutoJob(active []activeChannelAutoJob, lastScannedAt string) bool {
	if len(active) == 0 {
		return false
	}
	lastScanned, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(lastScannedAt))
	if err != nil {
		return true
	}
	for _, job := range active {
		createdAt, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(job.CreatedAt))
		if err != nil || !lastScanned.After(createdAt) {
			return true
		}
	}

	return false
}

func activeChannelAutoDownloadJob(ctx context.Context, store *jobs.Store, tx *sql.Tx, channelID string, lastScannedAt string) (jobs.Job, bool, error) {
	activeJobs, err := store.ActiveByTypeWithoutCancelRequestedTx(ctx, tx, ChannelAutoDownloadJobType, jobs.MaxActiveLookupLimit)
	if err != nil {
		return jobs.Job{}, false, err
	}
	for _, job := range activeJobs {
		var payload ChannelAutoDownloadPayload
		if err := json.Unmarshal([]byte(job.PayloadJSON), &payload); err != nil {
			continue
		}
		if strings.TrimSpace(payload.ChannelID) != channelID {
			continue
		}
		if hasCurrentChannelAutoJob([]activeChannelAutoJob{{CreatedAt: job.CreatedAt}}, lastScannedAt) {
			return job, true, nil
		}
	}

	return jobs.Job{}, false, nil
}

func channelURLFromExternalID(externalID string) (string, error) {
	externalID = strings.TrimSpace(externalID)
	if strings.HasPrefix(externalID, "@") {
		return NormalizeChannelURL("https://www.youtube.com/" + externalID)
	}

	return NormalizeChannelURL("https://www.youtube.com/channel/" + url.PathEscape(externalID))
}

func nextChannelAutoRun(now time.Time, lastScannedAt string, interval time.Duration, jitter func(time.Duration) time.Duration) time.Time {
	if interval <= 0 {
		interval = DefaultChannelAutoSyncInterval
	}
	if jitter == nil {
		jitter = randomDelay
	}
	now = now.UTC()
	if scannedAt, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(lastScannedAt)); err == nil {
		runAfter := scannedAt.UTC().Truncate(interval).Add(interval).Add(jitter(interval)).UTC()
		for !runAfter.After(now) {
			runAfter = runAfter.Add(interval)
		}
		return runAfter
	}

	return now.Add(jitter(interval)).UTC()
}

func randomDelay(max time.Duration) time.Duration {
	if max <= 0 {
		return 0
	}
	value, err := crand.Int(crand.Reader, big.NewInt(int64(max)))
	if err != nil {
		return time.Duration(time.Now().UnixNano() % int64(max))
	}

	return time.Duration(value.Int64())
}

func randomizedYTDLPSleepInterval(interval time.Duration) time.Duration {
	if interval <= 0 {
		return 0
	}

	return interval/2 + randomDelay(interval)
}

func (d *Downloader) runYTDLP(ctx context.Context, command Command) ([]byte, error) {
	if err := d.waitBeforeYTDLP(ctx); err != nil {
		return nil, err
	}

	return d.runner.Run(ctx, command)
}

func (d *Downloader) waitBeforeYTDLP(ctx context.Context) error {
	interval := d.config.YTDLPSleepInterval
	if interval <= 0 {
		return nil
	}
	now := d.ytdlpNow()
	jitter := d.config.ytdlpSleepJitter
	if jitter == nil {
		jitter = randomizedYTDLPSleepInterval
	}

	d.ytdlpMu.Lock()
	nextRun := now
	if !d.lastYTDLPStartedAt.IsZero() {
		candidate := d.lastYTDLPStartedAt.Add(jitter(interval))
		if candidate.After(nextRun) {
			nextRun = candidate
		}
	}
	d.lastYTDLPStartedAt = nextRun
	d.ytdlpMu.Unlock()

	return d.ytdlpSleep(ctx, nextRun.Sub(now))
}

func (d *Downloader) ytdlpNow() time.Time {
	if d.config.ytdlpNow != nil {
		return d.config.ytdlpNow().UTC()
	}

	return time.Now().UTC()
}

func (d *Downloader) ytdlpSleep(ctx context.Context, delay time.Duration) error {
	if delay <= 0 {
		return nil
	}
	if d.config.ytdlpSleep != nil {
		return d.config.ytdlpSleep(ctx, delay)
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

type ytdlpRetryError struct {
	err   error
	delay time.Duration
}

func (e ytdlpRetryError) Error() string {
	return e.err.Error()
}

func (e ytdlpRetryError) Unwrap() error {
	return e.err
}

func (e ytdlpRetryError) RetryDelay() time.Duration {
	return e.delay
}

func ytdlpJobError(command Command, output []byte, err error) error {
	return ytdlpRetryError{err: ytdlpCommandError(command, output, err), delay: ytdlpRetryDelay(output, err)}
}

func ytdlpRetryDelay(output []byte, err error) time.Duration {
	text := string(output)
	if err != nil {
		text += "\n" + err.Error()
	}
	if isYTDLPAuthChallenge(text) {
		return DefaultYTDLPAuthRetryDelay
	}

	return DefaultYTDLPRetryDelay
}

func isYTDLPAuthChallenge(text string) bool {
	text = strings.ToLower(text)
	for _, phrase := range []string{
		"sign in to confirm you're not a bot",
		"confirm you're not a bot",
		"sign in to confirm your age",
		"confirm your age",
	} {
		if strings.Contains(text, phrase) {
			return true
		}
	}

	return false
}

func (d *Downloader) BuildCommand(rawURL string) (Command, error) {
	downloadURL, err := NormalizeDownloadURL(rawURL)
	if err != nil {
		return Command{}, err
	}
	mediaRoot, cookiesFile, err := d.ytdlpSandboxPaths()
	if err != nil {
		return Command{}, err
	}

	return Command{
		Name:    d.config.YTDLPPath,
		Dir:     mediaRoot,
		Kind:    sandbox.KindYTDLP,
		Access:  d.ytdlpAccess(mediaRoot, cookiesFile, true),
		Network: sandbox.NetworkAllow,
		Args: d.ytdlpArgs(cookiesFile,
			"--no-playlist",
			"--no-simulate",
			"--newline",
			"--progress",
			"--check-formats",
			"--dump-single-json",
			"--write-info-json",
			"--write-thumbnail",
			"--write-subs",
			"--sub-langs", DefaultSubtitleLanguages,
			"--convert-subs", "vtt",
			"--format", d.config.FormatSelector,
			"--merge-output-format", "mp4",
			"--paths", mediaRoot,
			"--output", "%(id)s.%(ext)s",
			downloadURL,
		),
	}, nil
}

func (d *Downloader) BuildOriginalAutomaticSubtitleCommand(rawURL string) (Command, error) {
	downloadURL, err := NormalizeDownloadURL(rawURL)
	if err != nil {
		return Command{}, err
	}
	mediaRoot, cookiesFile, err := d.ytdlpSandboxPaths()
	if err != nil {
		return Command{}, err
	}

	return Command{
		Name:    d.config.YTDLPPath,
		Dir:     mediaRoot,
		Kind:    sandbox.KindYTDLP,
		Access:  d.ytdlpAccess(mediaRoot, cookiesFile, true),
		Network: sandbox.NetworkAllow,
		Args: d.ytdlpArgs(cookiesFile,
			"--no-playlist",
			"--no-simulate",
			"--skip-download",
			"--dump-single-json",
			"--write-auto-subs",
			"--sub-langs", DefaultAutomaticSubtitleLanguages,
			"--convert-subs", "vtt",
			"--paths", mediaRoot,
			"--output", "%(id)s.%(ext)s",
			downloadURL,
		),
	}, nil
}

func (d *Downloader) BuildChannelFirstCommand(rawURL string) (Command, error) {
	return d.BuildChannelCatalogPageCommand(rawURL, 1, DefaultChannelCatalogLimit)
}

func (d *Downloader) BuildChannelCatalogPageCommand(rawURL string, start int, end int) (Command, error) {
	channelURL, err := channelVideosURL(rawURL)
	if err != nil {
		return Command{}, err
	}
	mediaRoot, cookiesFile, err := d.ytdlpSandboxPaths()
	if err != nil {
		return Command{}, err
	}
	if start <= 0 {
		start = 1
	}
	if end < start {
		end = start
	}
	if end > DefaultChannelCatalogLimit {
		end = DefaultChannelCatalogLimit
	}
	if start > end {
		start = end
	}
	args := d.ytdlpArgs(cookiesFile, "--flat-playlist", "--extractor-args", "youtubetab:approximate_date")
	if start > 1 {
		args = append(args, "--playlist-start", fmt.Sprint(start))
	}
	args = append(args,
		"--playlist-end", fmt.Sprint(end),
		"--dump-single-json",
		channelURL,
	)

	return Command{
		Name:           d.config.YTDLPPath,
		Args:           args,
		Dir:            mediaRoot,
		Kind:           sandbox.KindYTDLP,
		Access:         d.ytdlpAccess(mediaRoot, cookiesFile, false),
		Network:        sandbox.NetworkAllow,
		MaxStdoutBytes: maxChannelCatalogOutputBytes,
	}, nil
}

func (d *Downloader) ytdlpSandboxPaths() (string, string, error) {
	mediaRoot, err := commandPath(d.config.MediaRoot)
	if err != nil {
		return "", "", err
	}
	cookiesFile, err := commandPath(d.config.YTDLPCookiesFile)
	if err != nil {
		return "", "", err
	}

	return mediaRoot, cookiesFile, nil
}

func commandPath(path string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", nil
	}
	if filepath.IsAbs(path) {
		return filepath.Clean(path), nil
	}

	return filepath.Abs(path)
}

func (d *Downloader) ytdlpAccess(mediaRoot string, cookiesFile string, writeMedia bool) sandbox.Access {
	var access sandbox.Access
	if cookiesFile != "" {
		access.ReadOnly = append(access.ReadOnly, sandbox.PathGrant{Path: cookiesFile, Kind: sandbox.PathLiteral})
	}
	if writeMedia && mediaRoot != "" {
		access.ReadWrite = append(access.ReadWrite, sandbox.PathGrant{Path: mediaRoot, Kind: sandbox.PathSubtree})
	}

	return access
}

func (d *Downloader) ytdlpArgs(cookiesFile string, args ...string) []string {
	base := make([]string, 0, len(args)+3)
	base = append(base, "--ignore-config")
	if cookiesFile == "" {
		return append(base, args...)
	}
	withCookies := append(base, "--cookies", cookiesFile)
	withCookies = append(withCookies, args...)

	return withCookies
}

func channelVideosURL(rawURL string) (string, error) {
	channelURL, err := NormalizeChannelURL(rawURL)
	if err != nil {
		return "", err
	}
	parsed, err := url.Parse(channelURL)
	if err != nil {
		return "", ErrUnsupportedChannelURL
	}
	path := strings.Trim(parsed.Path, "/")
	switch {
	case strings.HasPrefix(path, "@"):
		handle := firstPathSegment(strings.TrimPrefix(path, "@"))
		parsed.Path = "/@" + handle + "/videos"
	case hasPathPrefixValue(path, "channel/"):
		parsed.Path = "/channel/" + firstPathSegment(strings.TrimPrefix(path, "channel/")) + "/videos"
	case hasPathPrefixValue(path, "c/"):
		parsed.Path = "/c/" + firstPathSegment(strings.TrimPrefix(path, "c/")) + "/videos"
	case hasPathPrefixValue(path, "user/"):
		parsed.Path = "/user/" + firstPathSegment(strings.TrimPrefix(path, "user/")) + "/videos"
	}
	parsed.RawQuery = ""
	parsed.Fragment = ""

	return parsed.String(), nil
}

func (d *Downloader) Handle(ctx context.Context, job jobs.Job) error {
	if err := d.requireJobStoreForJob(job); err != nil {
		return err
	}
	_, err := d.handlePayload(ctx, job.ID, job.PayloadJSON)

	return err
}

func (d *Downloader) HandleRetention(ctx context.Context, job jobs.Job) error {
	if d.db == nil {
		return errors.New("retention handler missing database")
	}
	if err := d.requireJobStoreForJob(job); err != nil {
		return err
	}
	result, err := d.ApplyAutoDownloadRetention(ctx, RetentionOptions{})
	if err != nil {
		if result.Checked > 0 || result.Removed > 0 {
			_ = d.setPartialJobResult(ctx, job.ID, result)
		}
		return err
	}

	return d.setJobResult(ctx, job.ID, result)
}

func (d *Downloader) HandleYTDLPUpdate(ctx context.Context, job jobs.Job) error {
	if err := d.requireJobStoreForJob(job); err != nil {
		return err
	}
	result, err := d.UpdateYTDLP(ctx)
	if err != nil {
		_ = d.setPartialJobResult(ctx, job.ID, result)
		return err
	}

	return d.setJobResult(ctx, job.ID, result)
}

// YTDLPUpdateResult reports the outcome of an automatic yt-dlp update run.
type YTDLPUpdateResult struct {
	Updated   bool   `json:"updated"`
	Version   string `json:"version,omitempty"`
	Skipped   bool   `json:"skipped,omitempty"`
	SkipReason string `json:"skip_reason,omitempty"`
}

// UpdateYTDLP runs yt-dlp --update-to nightly to keep the bundled binary
// current against YouTube changes. It skips the run when a download is active
// so an in-flight transfer is never disturbed. The update is a network call
// that writes the yt-dlp binary in place, so it bypasses the download pacing
// used for media downloads.
func (d *Downloader) UpdateYTDLP(ctx context.Context) (YTDLPUpdateResult, error) {
	path := d.config.YTDLPPath
	if path == "" {
		path = defaultYTDLPPath
	}
	if d.store != nil {
		active, err := d.store.ActiveByType(ctx, JobType, jobs.MaxActiveLookupLimit)
		if err != nil {
			return YTDLPUpdateResult{SkipReason: "could not check active downloads"}, err
		}
		if len(active) > 0 {
			return YTDLPUpdateResult{Skipped: true, SkipReason: "download in progress"}, nil
		}
	}

	binaryDir, err := commandPath(filepath.Dir(path))
	if err != nil {
		return YTDLPUpdateResult{SkipReason: "could not resolve yt-dlp directory"}, err
	}
	command := Command{
		Name:           path,
		Dir:            binaryDir,
		Kind:           sandbox.KindYTDLP,
		Access:         sandbox.Access{ReadWrite: []sandbox.PathGrant{{Path: binaryDir, Kind: sandbox.PathSubtree}}},
		Network:        sandbox.NetworkAllow,
		MaxStdoutBytes: maxErrorOutput,
		Args:           []string{"--update-to", "nightly"},
	}
	output, err := d.runner.Run(ctx, command)
	if err != nil {
		return YTDLPUpdateResult{SkipReason: "update command failed"}, ytdlpCommandError(command, output, err)
	}

	status := CheckYTDLP(ctx, path, d.runner)
	return YTDLPUpdateResult{Updated: status.Available, Version: status.Version}, nil
}

func (d *Downloader) handlePayload(ctx context.Context, jobID string, payloadJSON string) (ingestResult, error) {
	if d.db == nil {
		return ingestResult{}, errors.New("download handler missing database")
	}

	var payload Payload
	if err := json.Unmarshal([]byte(payloadJSON), &payload); err != nil {
		return ingestResult{}, err
	}
	downloadURL, err := NormalizeDownloadURL(payload.URL)
	if err != nil {
		return ingestResult{}, err
	}
	if err := d.ensureDiskSpace(); err != nil {
		return ingestResult{}, err
	}

	command, err := d.BuildCommand(downloadURL)
	if err != nil {
		return ingestResult{}, err
	}
	if jobID != "" {
		store, err := d.jobStore()
		if err != nil {
			return ingestResult{}, err
		}
		command.Progress = func(progress float64) error {
			// Download progress is best-effort UI state; lease renewal is runner-owned.
			_ = store.ReportProgress(ctx, jobID, progress)
			return nil
		}
	}
	output, runErr := d.runYTDLP(ctx, command)
	downloadedMetadata, err := parseDownloadMetadataOutput(output, runErr != nil)
	if err != nil {
		if runErr != nil {
			return ingestResult{}, ytdlpJobError(command, output, runErr)
		}
		return ingestResult{}, err
	}
	if runErr != nil {
		if !isRecoverableYTDLPDownloadExit(runErr, downloadedMetadata) {
			return ingestResult{}, ytdlpJobError(command, output, runErr)
		}
		if _, err := d.validateMetadata(downloadedMetadata); err != nil {
			return ingestResult{}, fmt.Errorf("%w; downloaded media validation failed: %v", ytdlpJobError(command, output, runErr), err)
		}
		slog.Warn("yt-dlp exited after producing downloaded media metadata; continuing ingest", "video_id", downloadedMetadata.ID, "job_id", jobID, "error", SanitizeDiagnosticText(ytdlpCommandError(command, output, runErr).Error()))
	}
	if hasOriginalAutomaticSubtitles(downloadedMetadata) {
		command, err := d.BuildOriginalAutomaticSubtitleCommand(downloadURL)
		if err != nil {
			return ingestResult{}, err
		}
		output, err := d.runYTDLP(ctx, command)
		if err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return ingestResult{}, ytdlpJobError(command, output, err)
			}
			slog.Warn("original automatic subtitle download failed", "video_id", downloadedMetadata.ID, "job_id", jobID, "error", SanitizeDiagnosticText(ytdlpCommandError(command, output, err).Error()))
			return d.ingest(ctx, downloadedMetadata, downloadURL, payloadJSON, jobID, downloadOrigin(payload.Origin))
		}
		automaticMetadata, err := parseDownloadMetadataOutput(output, false)
		if err != nil {
			return ingestResult{}, err
		}
		mergeRequestedSubtitles(&downloadedMetadata, automaticMetadata.RequestedSubtitles)
	}

	return d.ingest(ctx, downloadedMetadata, downloadURL, payloadJSON, jobID, downloadOrigin(payload.Origin))
}

func parseDownloadMetadataOutput(output []byte, allowTrailingOutput bool) (metadata, error) {
	body := bytes.TrimSpace(output)
	if allowTrailingOutput {
		if end := firstJSONObjectEnd(body); end > 0 {
			body = body[:end]
		}
	}

	var value metadata
	if err := json.Unmarshal(body, &value); err != nil {
		return metadata{}, err
	}

	return value, nil
}

func isRecoverableYTDLPDownloadExit(err error, value metadata) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, exec.ErrNotFound) {
		return false
	}

	return strings.TrimSpace(value.ID) != "" && strings.TrimSpace(value.Title) != "" && strings.TrimSpace(value.mediaPath()) != ""
}

func (d *Downloader) HandleChannelFirst(ctx context.Context, job jobs.Job) error {
	if d.db == nil {
		return errors.New("download handler missing database")
	}
	if err := d.requireJobStoreForJob(job); err != nil {
		return err
	}

	var payload Payload
	if err := json.Unmarshal([]byte(job.PayloadJSON), &payload); err != nil {
		return err
	}
	channelURL, err := NormalizeChannelURL(payload.URL)
	if err != nil {
		return err
	}
	if err := d.ensureDiskSpace(); err != nil {
		return err
	}
	command, err := d.BuildChannelFirstCommand(channelURL)
	if err != nil {
		return err
	}
	output, err := d.runYTDLP(ctx, command)
	if err != nil {
		return ytdlpJobError(command, output, err)
	}
	catalogResult, err := d.syncChannelCatalog(ctx, output, "")
	if err != nil {
		return err
	}
	videoURL, err := firstChannelVideoURL(output)
	if err != nil {
		return err
	}

	return d.finishChannelFirstJob(ctx, job.ID, catalogResult, videoURL)
}

func (d *Downloader) HandleChannelScan(ctx context.Context, job jobs.Job) error {
	if d.db == nil {
		return errors.New("channel scan handler missing database")
	}
	if err := d.requireJobStoreForJob(job); err != nil {
		return err
	}

	var payload ChannelScanPayload
	if err := json.Unmarshal([]byte(job.PayloadJSON), &payload); err != nil {
		return err
	}
	channelURL, err := NormalizeChannelURL(payload.URL)
	if err != nil {
		return err
	}
	if err := d.ensureDiskSpace(); err != nil {
		return err
	}
	command, err := d.BuildChannelFirstCommand(channelURL)
	if err != nil {
		return err
	}
	output, err := d.runYTDLP(ctx, command)
	if err != nil {
		return ytdlpJobError(command, output, err)
	}
	result, err := d.syncChannelCatalog(ctx, output, payload.ChannelID)
	if err != nil {
		return err
	}
	if result.ChannelID == "" {
		result.ChannelID = payload.ChannelID
	}

	return d.finishChannelJob(ctx, job.ID, result.ChannelID, false, result)
}

func (d *Downloader) HandleChannelAutoDownload(ctx context.Context, job jobs.Job) error {
	if d.db == nil {
		return errors.New("channel auto-download handler missing database")
	}
	if err := d.requireJobStoreForJob(job); err != nil {
		return err
	}

	var payload ChannelAutoDownloadPayload
	if err := json.Unmarshal([]byte(job.PayloadJSON), &payload); err != nil {
		return err
	}
	channelURL, err := NormalizeChannelURL(payload.URL)
	if err != nil {
		return err
	}
	if strings.TrimSpace(payload.ChannelID) == "" {
		return errors.New("channel auto-download payload missing channel id")
	}
	stale, err := d.channelAutoJobStale(ctx, payload.ChannelID, job.CreatedAt)
	if err != nil {
		return err
	}
	if stale {
		return d.setJobResult(ctx, job.ID, channelAutoDownloadResult{ChannelID: payload.ChannelID, Skipped: true})
	}
	if err := d.ensureDiskSpace(); err != nil {
		return err
	}

	result, err := d.syncChannelAutoDownloads(ctx, ChannelAutoDownloadPayload{URL: channelURL, ChannelID: payload.ChannelID})
	if err != nil {
		return err
	}
	if result.Incomplete || result.Skipped {
		return d.setJobResult(ctx, job.ID, result)
	}

	return d.finishChannelJob(ctx, job.ID, result.ChannelID, false, result)
}

func (d *Downloader) channelAutoJobStale(ctx context.Context, channelID string, createdAt string) (bool, error) {
	jobCreatedAt, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(createdAt))
	if err != nil {
		return false, nil
	}
	subscribed, lastScannedAt, err := d.channelAutoSubscriptionState(ctx, channelID)
	if errors.Is(err, sql.ErrNoRows) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	if subscribed != 1 {
		return true, nil
	}
	lastScanned, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(lastScannedAt))
	if err != nil {
		return false, nil
	}

	return lastScanned.After(jobCreatedAt), nil
}

func (d *Downloader) channelAutoSubscriptionState(ctx context.Context, channelID string) (int, string, error) {
	var subscribed int
	var lastScannedAt string
	err := d.db.QueryRowContext(ctx, "SELECT subscribed, COALESCE(last_scanned_at, '') FROM channels WHERE id = ?", channelID).Scan(&subscribed, &lastScannedAt)

	return subscribed, lastScannedAt, err
}

func (d *Downloader) ensureDiskSpace() error {
	if d.config.MinFreeSpaceBytes == 0 {
		return nil
	}

	return diskspace.NewChecker(d.config.MinFreeSpaceBytes, d.config.Stat).Ensure(d.config.DataRoot, d.config.MediaRoot)
}

func NormalizeURL(rawURL string) (string, error) {
	value := strings.TrimSpace(rawURL)
	if value == "" {
		return "", ErrDownloadURLRequired
	}

	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return "", ErrUnsupportedURLScheme
	}
	parsed.Scheme = strings.ToLower(parsed.Scheme)
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", ErrUnsupportedURLScheme
	}

	return parsed.String(), nil
}

func NormalizeDirectVideoURL(rawURL string) (string, error) {
	downloadURL, err := NormalizeURL(rawURL)
	if err != nil {
		return "", err
	}
	parsed, err := url.Parse(downloadURL)
	if err != nil {
		return "", ErrUnsupportedVideoURL
	}
	if isYouTubeHost(parsed.Hostname()) || strings.EqualFold(parsed.Hostname(), "youtu.be") {
		videoURL, err := normalizeDirectYouTubeVideoURL(parsed)
		if err != nil {
			return "", ErrUnsupportedVideoURL
		}

		return videoURL, nil
	}

	return "", ErrUnsupportedVideoURL
}

func NormalizeDownloadURL(rawURL string) (string, error) {
	return NormalizeDirectVideoURL(rawURL)
}

func normalizeDirectYouTubeVideoURL(parsed *url.URL) (string, error) {
	host := strings.ToLower(parsed.Hostname())
	path := strings.Trim(parsed.EscapedPath(), "/")
	if host == "youtu.be" {
		return youtubeDirectWatchURL(firstPathSegment(path))
	}
	if !isYouTubeHost(host) {
		return "", ErrUnsupportedVideoURL
	}
	if path == "watch" {
		return youtubeDirectWatchURL(parsed.Query().Get("v"))
	}
	for _, prefix := range []string{"shorts/", "embed/", "live/", "v/"} {
		if hasPathPrefixValue(path, prefix) {
			return youtubeDirectWatchURL(firstPathSegment(strings.TrimPrefix(path, prefix)))
		}
	}

	return "", ErrUnsupportedVideoURL
}

func youtubeDirectWatchURL(videoID string) (string, error) {
	if !isLikelyYouTubeVideoID(videoID) {
		return "", ErrUnsupportedVideoURL
	}

	return youtubeWatchURL(videoID), nil
}

func NormalizeChannelURL(rawURL string) (string, error) {
	channelURL, err := NormalizeURL(rawURL)
	if err != nil {
		return "", err
	}
	parsed, err := url.Parse(channelURL)
	if err != nil {
		return "", ErrUnsupportedChannelURL
	}
	host := strings.ToLower(parsed.Hostname())
	if !isYouTubeHost(host) {
		return "", ErrUnsupportedChannelURL
	}
	path := strings.Trim(parsed.Path, "/")
	if strings.HasPrefix(path, "@") {
		handle := firstPathSegment(strings.TrimPrefix(path, "@"))
		if handle != "" {
			return canonicalYouTubeChannelURL("@" + handle), nil
		}
	}
	for _, prefix := range []string{"channel/", "c/", "user/"} {
		if hasPathPrefixValue(path, prefix) {
			return canonicalYouTubeChannelURL(strings.TrimSuffix(prefix, "/") + "/" + firstPathSegment(strings.TrimPrefix(path, prefix))), nil
		}
	}

	return "", ErrUnsupportedChannelURL
}

func canonicalYouTubeChannelURL(path string) string {
	return (&url.URL{Scheme: "https", Host: "www.youtube.com", Path: "/" + strings.Trim(path, "/")}).String()
}

type metadata struct {
	ID                 string                        `json:"id"`
	Title              string                        `json:"title"`
	Description        string                        `json:"description"`
	Duration           float64                       `json:"duration"`
	ViewCount          *int                          `json:"view_count"`
	UploadDate         string                        `json:"upload_date"`
	Thumbnail          string                        `json:"thumbnail"`
	Thumbnails         []thumbnailMetadata           `json:"thumbnails"`
	ChannelID          string                        `json:"channel_id"`
	Channel            string                        `json:"channel"`
	UploaderID         string                        `json:"uploader_id"`
	Uploader           string                        `json:"uploader"`
	WebpageURL         string                        `json:"webpage_url"`
	Filepath           string                        `json:"filepath"`
	ThumbnailPath      string                        `json:"thumbnail_path"`
	ThumbnailFilepath  string                        `json:"__thumbnail_filepath"`
	RequestedSubtitles map[string]subtitleMetadata   `json:"requested_subtitles"`
	AutomaticCaptions  map[string][]subtitleMetadata `json:"automatic_captions"`
	RequestedDownloads []struct {
		Filepath string `json:"filepath"`
	} `json:"requested_downloads"`
}

type subtitleMetadata struct {
	Filepath string `json:"filepath"`
	Path     string `json:"path"`
	Ext      string `json:"ext"`
	Name     string `json:"name"`
	Language string `json:"language"`
}

type thumbnailMetadata struct {
	URL string `json:"url"`
}

type validatedMetadata struct {
	metadata      metadata
	mediaPath     string
	thumbnailPath string
	subtitles     []validatedSubtitle
}

type validatedSubtitle struct {
	Language string
	Name     string
	Source   string
	Format   string
	Path     string
	Text     string
}

type ingestResult struct {
	VideoID string `json:"video_id"`
	Action  string `json:"action"`
}

func hasOriginalAutomaticSubtitles(value metadata) bool {
	for language := range value.AutomaticCaptions {
		if strings.HasSuffix(language, "-orig") {
			return true
		}
	}

	return false
}

func mergeRequestedSubtitles(target *metadata, subtitles map[string]subtitleMetadata) {
	if len(subtitles) == 0 {
		return
	}
	if target.RequestedSubtitles == nil {
		target.RequestedSubtitles = map[string]subtitleMetadata{}
	}
	for language, subtitle := range subtitles {
		target.RequestedSubtitles[language] = subtitle
	}
}

type channelMetadata struct {
	ID          string              `json:"id"`
	Title       string              `json:"title"`
	Description string              `json:"description"`
	Thumbnail   string              `json:"thumbnail"`
	Thumbnails  []thumbnailMetadata `json:"thumbnails"`
	ChannelID   string              `json:"channel_id"`
	Channel     string              `json:"channel"`
	UploaderID  string              `json:"uploader_id"`
	Uploader    string              `json:"uploader"`
	Entries     []channelEntry      `json:"entries"`
}

type channelEntry struct {
	ID          string              `json:"id"`
	URL         string              `json:"url"`
	WebpageURL  string              `json:"webpage_url"`
	Title       string              `json:"title"`
	Description string              `json:"description"`
	Duration    float64             `json:"duration"`
	ViewCount   *int                `json:"view_count"`
	UploadDate  string              `json:"upload_date"`
	Timestamp   int64               `json:"timestamp"`
	Thumbnail   string              `json:"thumbnail"`
	Thumbnails  []thumbnailMetadata `json:"thumbnails"`
	ChannelID   string              `json:"channel_id"`
	Channel     string              `json:"channel"`
	UploaderID  string              `json:"uploader_id"`
	Uploader    string              `json:"uploader"`
	Entries     []channelEntry      `json:"entries"`
}

type catalogVideo struct {
	ID              string
	Title           string
	Description     string
	PublishedAt     string
	CatalogPosition int
	DurationSeconds int
	ViewCount       int
	HasViewCount    bool
	ThumbnailURL    string
	ChannelID       string
	ChannelName     string
}

type channelCatalogResult struct {
	ChannelID string `json:"channel_id"`
	Videos    int    `json:"videos"`
}

type channelCatalogPageResult struct {
	ChannelID  string
	Videos     []catalogVideo
	RawEntries int
}

type channelAutoDownloadResult struct {
	ChannelID       string `json:"channel_id"`
	Videos          int    `json:"videos"`
	Pages           int    `json:"pages"`
	DownloadsQueued int    `json:"downloads_queued"`
	Skipped         bool   `json:"skipped,omitempty"`
	Incomplete      bool   `json:"incomplete,omitempty"`
}

type channelFirstResult struct {
	ChannelID     string               `json:"channel_id"`
	Videos        int                  `json:"videos"`
	FirstVideoURL string               `json:"first_video_url"`
	DownloadJobID string               `json:"download_job_id"`
	Catalog       channelCatalogResult `json:"catalog"`
}

func firstChannelVideoURL(output []byte) (string, error) {
	var metadata channelMetadata
	if err := json.Unmarshal(output, &metadata); err != nil {
		return "", err
	}
	if len(metadata.Entries) == 0 {
		return "", errors.New("channel contains no videos")
	}
	return firstChannelVideoURLFromEntries(metadata.Entries)
}

func firstChannelVideoURLFromEntries(entries []channelEntry) (string, error) {
	var unsupportedErr error
	for _, entry := range entries {
		videoURL, err := firstChannelVideoURLFromEntry(entry)
		if err == nil {
			return videoURL, nil
		}
		if errors.Is(err, ErrUnsupportedChannelURL) && unsupportedErr == nil {
			unsupportedErr = err
		}
	}
	if unsupportedErr != nil {
		return "", unsupportedErr
	}

	return "", errors.New("channel first video is missing url")
}

func firstChannelVideoURLFromEntry(entry channelEntry) (string, error) {
	var unsupportedErr error
	for _, candidate := range []string{entry.WebpageURL, entry.URL} {
		if candidate == "" {
			continue
		}
		if normalized, err := NormalizeYouTubeVideoURL(candidate); err == nil {
			return normalized, nil
		} else if errors.Is(err, ErrUnsupportedChannelURL) {
			unsupportedErr = err
		}
		if isLikelyYouTubeVideoID(candidate) {
			return youtubeWatchURL(candidate), nil
		}
	}
	if isLikelyYouTubeVideoID(entry.ID) {
		return youtubeWatchURL(entry.ID), nil
	}

	videoURL, err := firstChannelVideoURLFromEntries(entry.Entries)
	if err == nil {
		return videoURL, nil
	}
	if unsupportedErr != nil {
		return "", unsupportedErr
	}

	return "", err
}

func (d *Downloader) syncChannelCatalog(ctx context.Context, output []byte, requestedChannelID string) (channelCatalogResult, error) {
	page, err := d.syncChannelCatalogPage(ctx, output, requestedChannelID, 0)
	if err != nil {
		return channelCatalogResult{}, err
	}

	return channelCatalogResult{ChannelID: page.ChannelID, Videos: len(page.Videos)}, nil
}

func (d *Downloader) syncChannelCatalogPage(ctx context.Context, output []byte, requestedChannelID string, positionOffset int) (channelCatalogPageResult, error) {
	var metadata channelMetadata
	if err := json.Unmarshal(output, &metadata); err != nil {
		return channelCatalogPageResult{}, err
	}

	channelID := firstNonEmpty(requestedChannelID, metadata.ChannelID, metadata.UploaderID, metadata.ID)
	channelName := firstNonEmpty(metadata.Channel, metadata.Uploader, metadata.Title, channelID)
	channelDescription := strings.TrimSpace(metadata.Description)
	if !isSafeMetadataValue(channelName) || !isSafeMetadataText(channelDescription) {
		return channelCatalogPageResult{}, errors.New("channel metadata contains unsafe text")
	}
	channelThumbnailURL := catalogThumbnailURL(metadata.Thumbnail, metadata.Thumbnails)
	videos := catalogVideosFromEntries(metadata.Entries, channelID, channelName, requestedChannelID != "")
	for index := range videos {
		videos[index].CatalogPosition += positionOffset
	}
	result := channelCatalogPageResult{ChannelID: channelID, Videos: videos, RawEntries: len(metadata.Entries)}
	if channelID == "" && len(videos) == 0 {
		return result, nil
	}

	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return channelCatalogPageResult{}, err
	}
	defer tx.Rollback()

	if channelID != "" {
		if err := d.upsertChannel(ctx, tx, channelID, channelName, channelDescription, channelThumbnailURL); err != nil {
			return channelCatalogPageResult{}, err
		}
	}
	for _, video := range videos {
		if video.ChannelID != "" {
			if err := d.upsertChannel(ctx, tx, video.ChannelID, video.ChannelName, "", ""); err != nil {
				return channelCatalogPageResult{}, err
			}
		}
		if err := d.upsertCatalogVideo(ctx, tx, video); err != nil {
			return channelCatalogPageResult{}, err
		}
		videoTitle, videoDescription, err := d.videoSearchText(ctx, tx, video.ID)
		if err != nil {
			return channelCatalogPageResult{}, err
		}
		if err := d.upsertSearch(ctx, tx, "video", video.ID, "title", videoTitle); err != nil {
			return channelCatalogPageResult{}, err
		}
		if err := d.upsertSearch(ctx, tx, "video", video.ID, "description", videoDescription); err != nil {
			return channelCatalogPageResult{}, err
		}
	}

	return result, tx.Commit()
}

func (d *Downloader) videoSearchText(ctx context.Context, exec sqlExecutor, id string) (string, string, error) {
	var title string
	var description string
	if err := exec.QueryRowContext(ctx, "SELECT title, description FROM videos WHERE id = ?", id).Scan(&title, &description); err != nil {
		return "", "", err
	}

	return title, description, nil
}

func (d *Downloader) syncChannelAutoDownloads(ctx context.Context, payload ChannelAutoDownloadPayload) (channelAutoDownloadResult, error) {
	result := channelAutoDownloadResult{ChannelID: payload.ChannelID}
	candidates := []catalogVideo{}
	pageSize := DefaultChannelCatalogPageSize
	maxPages := (DefaultChannelCatalogLimit + pageSize - 1) / pageSize
	complete := false
	for page := 0; page < maxPages; page++ {
		start := page*pageSize + 1
		end := start + pageSize - 1
		if end > DefaultChannelCatalogLimit {
			end = DefaultChannelCatalogLimit
		}
		requestedCount := end - start + 1
		hitLimitPage := end >= DefaultChannelCatalogLimit
		command, err := d.BuildChannelCatalogPageCommand(payload.URL, start, end)
		if err != nil {
			return channelAutoDownloadResult{}, err
		}
		output, err := d.runYTDLP(ctx, command)
		if err != nil {
			return channelAutoDownloadResult{}, ytdlpJobError(command, output, err)
		}
		catalogPage, err := d.syncChannelCatalogPage(ctx, output, payload.ChannelID, page*pageSize)
		if err != nil {
			return channelAutoDownloadResult{}, err
		}
		if result.ChannelID == "" {
			result.ChannelID = catalogPage.ChannelID
		}
		result.Pages++
		result.Videos += len(catalogPage.Videos)
		candidates = append(candidates, catalogPage.Videos...)
		if catalogPage.RawEntries == 0 {
			complete = true
			break
		}
		overlaps, err := d.catalogPageHasDownloadedOverlap(ctx, catalogPage.Videos)
		if err != nil {
			return channelAutoDownloadResult{}, err
		}
		if overlaps {
			complete = true
			break
		}
		if catalogPage.RawEntries < requestedCount {
			complete = true
			break
		}
		if hitLimitPage {
			break
		}
	}

	queued, subscribed, err := d.enqueueMissingCatalogDownloadsIfSubscribed(ctx, payload.ChannelID, candidates)
	if err != nil {
		return channelAutoDownloadResult{}, err
	}
	if !subscribed {
		result.Skipped = true
		return result, nil
	}
	result.DownloadsQueued = queued
	result.Incomplete = !complete

	return result, nil
}

func (d *Downloader) catalogPageHasDownloadedOverlap(ctx context.Context, videos []catalogVideo) (bool, error) {
	for _, video := range videos {
		var mediaPath string
		err := d.db.QueryRowContext(ctx, "SELECT media_path FROM videos WHERE id = ?", video.ID).Scan(&mediaPath)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return false, err
		}
		if strings.TrimSpace(mediaPath) != "" {
			return true, nil
		}
	}

	return false, nil
}

func (d *Downloader) enqueueMissingCatalogDownloadsIfSubscribed(ctx context.Context, channelID string, videos []catalogVideo) (int, bool, error) {
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, false, err
	}
	defer tx.Rollback()

	var subscribed int
	if err := tx.QueryRowContext(ctx, "SELECT subscribed FROM channels WHERE id = ?", channelID).Scan(&subscribed); errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	} else if err != nil {
		return 0, false, err
	}
	if subscribed != 1 {
		return 0, false, nil
	}
	queued, err := d.enqueueMissingCatalogDownloads(ctx, tx, videos)
	if err != nil {
		return 0, false, err
	}

	return queued, true, tx.Commit()
}

func (d *Downloader) enqueueMissingCatalogDownloads(ctx context.Context, tx *sql.Tx, videos []catalogVideo) (int, error) {
	store, err := d.jobStore()
	if err != nil {
		return 0, err
	}
	queued := 0
	considered := 0
	seen := map[string]struct{}{}
	for _, video := range videos {
		if _, ok := seen[video.ID]; ok {
			continue
		}
		seen[video.ID] = struct{}{}
		if considered >= DefaultChannelAutoDownloadLimit {
			break
		}
		considered++
		payload := Payload{URL: youtubeWatchURL(video.ID), Origin: DownloadOriginChannelAuto}
		needed, err := d.catalogDownloadNeeded(ctx, tx, video.ID, payload)
		if err != nil {
			return queued, err
		}
		if !needed {
			continue
		}
		_, created, err := enqueueDownloadTx(ctx, store, tx, payload, false)
		if err != nil {
			return queued, err
		}
		if created {
			queued++
		}
	}

	return queued, nil
}

func (d *Downloader) catalogDownloadNeeded(ctx context.Context, exec sqlExecutor, videoID string, payload Payload) (bool, error) {
	var mediaPath string
	err := exec.QueryRowContext(ctx, "SELECT media_path FROM videos WHERE id = ?", videoID).Scan(&mediaPath)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return false, err
	}
	if strings.TrimSpace(mediaPath) != "" {
		return false, nil
	}
	if _, err := NormalizeDownloadURL(payload.URL); err != nil {
		return false, nil
	}
	if downloadOrigin(payload.Origin) == DownloadOriginChannelAuto {
		alreadyAutoDownloaded, err := succeededAutoDownloadExists(ctx, exec, videoID)
		if err != nil {
			return false, err
		}
		if alreadyAutoDownloaded {
			return false, nil
		}
	}

	return true, nil
}

func succeededAutoDownloadExists(ctx context.Context, exec sqlExecutor, videoID string) (bool, error) {
	var exists bool
	if err := exec.QueryRowContext(ctx, `
SELECT EXISTS(
  SELECT 1
  FROM downloads
  WHERE video_id = ?
    AND status = 'succeeded'
    AND origin = ?
)`, videoID, DownloadOriginChannelAuto).Scan(&exists); err != nil {
		return false, err
	}

	return exists, nil
}

func (d *Downloader) ApplyAutoDownloadRetention(ctx context.Context, options RetentionOptions) (RetentionResult, error) {
	return NewRetentionCleaner(d.db, d.config.MediaRoot).Apply(ctx, options)
}

func downloadOrigin(value string) string {
	switch strings.TrimSpace(value) {
	case DownloadOriginChannelAuto:
		return DownloadOriginChannelAuto
	default:
		return DownloadOriginManual
	}
}

func catalogVideosFromEntries(entries []channelEntry, channelID string, channelName string, forceChannel bool) []catalogVideo {
	seen := map[string]struct{}{}
	videos := []catalogVideo{}
	position := 0
	appendCatalogVideos(&videos, seen, entries, channelID, channelName, forceChannel, &position)

	return videos
}

func appendCatalogVideos(videos *[]catalogVideo, seen map[string]struct{}, entries []channelEntry, channelID string, channelName string, forceChannel bool, position *int) {
	for _, entry := range entries {
		entryChannelID := channelID
		entryChannelName := channelName
		if !forceChannel {
			entryChannelID = firstNonEmpty(entry.ChannelID, entry.UploaderID, channelID)
			entryChannelName = firstNonEmpty(entry.Channel, entry.Uploader, channelName, entryChannelID)
		}
		if video, ok := catalogVideoFromEntry(entry, entryChannelID, entryChannelName); ok {
			if _, exists := seen[video.ID]; !exists {
				seen[video.ID] = struct{}{}
				video.CatalogPosition = *position
				*position += 1
				*videos = append(*videos, video)
			}
		}
		if len(entry.Entries) > 0 {
			appendCatalogVideos(videos, seen, entry.Entries, entryChannelID, entryChannelName, forceChannel, position)
		}
	}
}

func catalogVideoFromEntry(entry channelEntry, channelID string, channelName string) (catalogVideo, bool) {
	videoID := channelEntryVideoID(entry)
	if videoID == "" {
		return catalogVideo{}, false
	}
	title := strings.TrimSpace(firstNonEmpty(entry.Title, videoID))
	description := strings.TrimSpace(entry.Description)
	if !isSafeMetadataValue(title) || !isSafeMetadataText(description) {
		return catalogVideo{}, false
	}
	duration := durationSeconds(entry.Duration)
	viewCount, hasViewCount := viewCountValue(entry.ViewCount)

	return catalogVideo{
		ID:              videoID,
		Title:           title,
		Description:     description,
		PublishedAt:     catalogPublishedAt(entry),
		DurationSeconds: duration,
		ViewCount:       viewCount,
		HasViewCount:    hasViewCount,
		ThumbnailURL:    catalogThumbnailURL(entry.Thumbnail, entry.Thumbnails),
		ChannelID:       channelID,
		ChannelName:     channelName,
	}, true
}

func channelEntryVideoID(entry channelEntry) string {
	if isLikelyYouTubeVideoID(entry.ID) {
		return entry.ID
	}
	for _, candidate := range []string{entry.WebpageURL, entry.URL} {
		if normalized, err := NormalizeYouTubeVideoURL(candidate); err == nil {
			return videoIDFromWatchURL(normalized)
		}
		if isLikelyYouTubeVideoID(candidate) {
			return candidate
		}
	}

	return ""
}

func videoIDFromWatchURL(rawURL string) string {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}

	return strings.TrimSpace(parsed.Query().Get("v"))
}

func catalogPublishedAt(entry channelEntry) string {
	if len(entry.UploadDate) == 8 {
		return entry.UploadDate[0:4] + "-" + entry.UploadDate[4:6] + "-" + entry.UploadDate[6:8]
	}
	if entry.Timestamp > 0 {
		return time.Unix(entry.Timestamp, 0).UTC().Format("2006-01-02")
	}

	return ""
}

func catalogThumbnailURL(raw string, thumbnails []thumbnailMetadata) string {
	if value := validCatalogThumbnailURL(raw); value != "" {
		return value
	}
	for _, thumbnail := range thumbnails {
		if value := validCatalogThumbnailURL(thumbnail.URL); value != "" {
			return value
		}
	}

	return ""
}

func validCatalogThumbnailURL(raw string) string {
	value := strings.TrimSpace(raw)
	if value == "" || len(value) > maxCatalogThumbnailURLLength || !isSafeMetadataValue(value) {
		return ""
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.Host == "" {
		return ""
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return ""
	}
	if !isAllowedCatalogThumbnailHost(parsed.Hostname()) {
		return ""
	}

	return parsed.String()
}

func isAllowedCatalogThumbnailHost(host string) bool {
	host = strings.ToLower(strings.TrimSpace(host))
	return host == "img.youtube.com" || host == "ytimg.com" || strings.HasSuffix(host, ".ytimg.com") || host == "yt3.ggpht.com" || host == "yt3.googleusercontent.com"
}

func NormalizeYouTubeVideoURL(rawURL string) (string, error) {
	videoURL, err := NormalizeURL(rawURL)
	if err != nil {
		return "", err
	}
	parsed, err := url.Parse(videoURL)
	if err != nil {
		return "", ErrUnsupportedChannelURL
	}
	host := strings.ToLower(parsed.Hostname())
	path := strings.Trim(parsed.EscapedPath(), "/")
	if host == "youtu.be" {
		if videoID := firstPathSegment(path); videoID != "" {
			return "https://www.youtube.com/watch?v=" + url.QueryEscape(videoID), nil
		}
		return "", ErrUnsupportedChannelURL
	}
	if !isYouTubeHost(host) {
		return "", ErrUnsupportedChannelURL
	}
	if path == "watch" {
		if videoID := parsed.Query().Get("v"); videoID != "" {
			return "https://www.youtube.com/watch?v=" + url.QueryEscape(videoID), nil
		}
	}
	if hasPathPrefixValue(path, "shorts/") {
		return "https://www.youtube.com/watch?v=" + url.QueryEscape(firstPathSegment(strings.TrimPrefix(path, "shorts/"))), nil
	}
	if hasPathPrefixValue(path, "embed/") {
		return "https://www.youtube.com/watch?v=" + url.QueryEscape(firstPathSegment(strings.TrimPrefix(path, "embed/"))), nil
	}

	return "", ErrUnsupportedChannelURL
}

func isYouTubeHost(host string) bool {
	host = strings.ToLower(host)
	return host == "youtube.com" || strings.HasSuffix(host, ".youtube.com")
}

func hasPathPrefixValue(path string, prefix string) bool {
	return strings.HasPrefix(path, prefix) && firstPathSegment(strings.TrimPrefix(path, prefix)) != ""
}

func firstPathSegment(path string) string {
	return strings.Split(path, "/")[0]
}

func isLikelyYouTubeVideoID(value string) bool {
	if len(value) != 11 {
		return false
	}
	for _, char := range value {
		if char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' || char >= '0' && char <= '9' || char == '_' || char == '-' {
			continue
		}
		return false
	}

	return true
}

func youtubeWatchURL(videoID string) string {
	return "https://www.youtube.com/watch?v=" + url.QueryEscape(videoID)
}

func (d *Downloader) ingest(ctx context.Context, metadata metadata, rawURL string, payloadJSON string, jobID string, origin string) (ingestResult, error) {
	validated, err := d.validateMetadata(metadata)
	if err != nil {
		return ingestResult{}, err
	}
	metadata = validated.metadata
	mediaPath := validated.mediaPath
	thumbnailPath := validated.thumbnailPath
	subtitles := validated.subtitles
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return ingestResult{}, err
	}
	defer tx.Rollback()

	videoID, err := d.canonicalVideoID(ctx, tx, metadata.ID)
	if err != nil {
		return ingestResult{}, err
	}
	exists, err := d.videoExists(ctx, tx, videoID)
	if err != nil {
		return ingestResult{}, err
	}
	result := ingestResult{VideoID: videoID, Action: "created"}
	if exists {
		result.Action = "updated"
	}

	channelID := firstNonEmpty(metadata.ChannelID, metadata.UploaderID)
	channelName := firstNonEmpty(metadata.Channel, metadata.Uploader, channelID)
	if channelID != "" {
		if err := d.upsertChannel(ctx, tx, channelID, channelName, "", ""); err != nil {
			return ingestResult{}, err
		}
	}

	if err := d.upsertVideo(ctx, tx, videoID, metadata, channelID, mediaPath, thumbnailPath, origin); err != nil {
		return ingestResult{}, err
	}
	actualVideoID, err := d.canonicalVideoID(ctx, tx, metadata.ID)
	if err != nil {
		return ingestResult{}, err
	}
	if actualVideoID != videoID {
		videoID = actualVideoID
		result.VideoID = actualVideoID
		result.Action = "updated"
	}

	if err := d.upsertAsset(ctx, tx, "video", videoID, "media", mediaPath); err != nil {
		return ingestResult{}, err
	}
	if err := d.syncOptionalAsset(ctx, tx, "video", videoID, "thumbnail", thumbnailPath); err != nil {
		return ingestResult{}, err
	}
	if err := d.replaceDownloadedSubtitles(ctx, tx, videoID, subtitles); err != nil {
		return ingestResult{}, err
	}
	if err := d.upsertSearch(ctx, tx, "video", videoID, "title", firstNonEmpty(metadata.Title, metadata.ID)); err != nil {
		return ingestResult{}, err
	}
	if err := d.upsertSearch(ctx, tx, "video", videoID, "description", metadata.Description); err != nil {
		return ingestResult{}, err
	}
	if err := d.upsertDownload(ctx, tx, videoID, metadata, rawURL, payloadJSON, origin); err != nil {
		return ingestResult{}, err
	}
	if d.config.PreviewsEnabled {
		if err := d.enqueuePreviewJob(ctx, tx, videoID); err != nil {
			return ingestResult{}, err
		}
	}
	if err := d.setJobResultTx(ctx, tx, jobID, result); err != nil {
		return ingestResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return ingestResult{}, err
	}

	return result, nil
}

func (d *Downloader) enqueuePreviewJob(ctx context.Context, tx *sql.Tx, videoID string) error {
	// Preview generation is best-effort; do not repeatedly restart ffmpeg after a resource-related failure.
	store, err := d.jobStore()
	if err != nil {
		return err
	}
	_, _, err = previews.EnqueueJobTx(ctx, store, tx, videoID)

	return err
}

func (d *Downloader) canonicalVideoID(ctx context.Context, exec sqlExecutor, externalID string) (string, error) {
	var id string
	err := exec.QueryRowContext(ctx, "SELECT id FROM videos WHERE source = 'youtube' AND external_id = ?", externalID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return externalID, nil
	}
	if err != nil {
		return "", err
	}

	return id, nil
}

func (d *Downloader) validateMetadata(metadata metadata) (validatedMetadata, error) {
	metadata.ID = strings.TrimSpace(metadata.ID)
	metadata.Title = strings.TrimSpace(metadata.Title)
	if metadata.ID == "" {
		return validatedMetadata{}, errors.New("download metadata missing video id")
	}
	if !isSafeMetadataValue(metadata.ID) || strings.ContainsAny(metadata.ID, `/\`) {
		return validatedMetadata{}, errors.New("download metadata invalid video id")
	}
	if metadata.Title == "" {
		return validatedMetadata{}, errors.New("download metadata missing title")
	}
	if !isSafeMetadataValue(metadata.Title) {
		return validatedMetadata{}, errors.New("download metadata invalid title")
	}

	mediaPath, err := d.validatedAssetPath("media", metadata.mediaPath(), true)
	if err != nil {
		return validatedMetadata{}, err
	}
	thumbnailPath, err := d.validatedThumbnailPath(metadata)
	if err != nil {
		return validatedMetadata{}, err
	}
	subtitles, err := d.validatedSubtitles(metadata)
	if err != nil {
		return validatedMetadata{}, err
	}

	return validatedMetadata{metadata: metadata, mediaPath: mediaPath, thumbnailPath: thumbnailPath, subtitles: subtitles}, nil
}

func (d *Downloader) validatedThumbnailPath(metadata metadata) (string, error) {
	rawThumbnailPath := firstNonEmpty(metadata.ThumbnailPath, metadata.ThumbnailFilepath)
	thumbnailPath, err := d.validatedAssetPath("thumbnail", rawThumbnailPath, false)
	if err != nil || thumbnailPath != "" {
		return thumbnailPath, err
	}
	if strings.TrimSpace(rawThumbnailPath) != "" {
		return "", nil
	}

	return d.discoverDownloadedThumbnail(metadata.ID)
}

func (d *Downloader) discoverDownloadedThumbnail(videoID string) (string, error) {
	for _, extension := range thumbnailFileExtensions {
		thumbnailPath, err := d.validatedAssetPath("thumbnail", videoID+extension, false)
		if err != nil || thumbnailPath != "" {
			return thumbnailPath, err
		}
	}

	return "", nil
}

func (d *Downloader) validatedSubtitles(metadata metadata) ([]validatedSubtitle, error) {
	if len(metadata.RequestedSubtitles) == 0 {
		return nil, nil
	}
	languages := make([]string, 0, len(metadata.RequestedSubtitles))
	for language := range metadata.RequestedSubtitles {
		languages = append(languages, language)
	}
	sort.Strings(languages)
	subtitles := make([]validatedSubtitle, 0, len(languages))
	for _, key := range languages {
		subtitle := metadata.RequestedSubtitles[key]
		path, err := d.validatedAssetPath("subtitle", firstNonEmpty(subtitle.Filepath, subtitle.Path), false)
		if err != nil {
			return nil, err
		}
		if path == "" {
			continue
		}
		language := cleanSubtitleLanguage(firstNonEmpty(subtitle.Language, key))
		if language == "" {
			continue
		}
		format := cleanSubtitleFormat(firstNonEmpty(subtitle.Ext, filepath.Ext(path)))
		if format == "" {
			continue
		}
		text, err := d.readSubtitleText(path)
		if err != nil {
			return nil, err
		}
		subtitles = append(subtitles, validatedSubtitle{Language: language, Name: subtitle.Name, Source: "downloaded", Format: format, Path: path, Text: text})
	}

	return subtitles, nil
}

func (d *Downloader) videoExists(ctx context.Context, exec sqlExecutor, id string) (bool, error) {
	var exists bool
	if err := exec.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM videos WHERE id = ?)", id).Scan(&exists); err != nil {
		return false, err
	}

	return exists, nil
}

func (d *Downloader) upsertDownload(ctx context.Context, exec sqlExecutor, videoID string, metadata metadata, rawURL string, payloadJSON string, origin string) error {
	downloadURL := firstNonEmpty(metadata.WebpageURL, rawURL)
	_, err := exec.ExecContext(ctx, `
INSERT INTO downloads (video_id, source, external_id, url, status, origin, error, payload_json, updated_at)
VALUES (?, 'youtube', ?, ?, 'succeeded', ?, '', ?, strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
ON CONFLICT(source, external_id) WHERE external_id <> '' DO UPDATE SET
  video_id = excluded.video_id,
  url = excluded.url,
  status = excluded.status,
	origin = CASE WHEN downloads.origin = 'manual' OR excluded.origin = 'manual' THEN 'manual' ELSE excluded.origin END,
  error = '',
  payload_json = excluded.payload_json,
	updated_at = excluded.updated_at`, videoID, metadata.ID, downloadURL, downloadOrigin(origin), payloadJSON)

	return err
}

func (d *Downloader) jobStore() (*jobs.Store, error) {
	if d.store == nil {
		return nil, errors.New("download handler missing job store")
	}

	return d.store, nil
}

func (d *Downloader) requireJobStoreForJob(job jobs.Job) error {
	if job.ID == "" {
		return nil
	}
	_, err := d.jobStore()

	return err
}

func (d *Downloader) setJobResult(ctx context.Context, jobID string, result any) error {
	if jobID == "" {
		return nil
	}
	body, err := json.Marshal(result)
	if err != nil {
		return err
	}
	store, err := d.jobStore()
	if err != nil {
		return err
	}

	return store.CompleteWithResult(ctx, jobID, string(body))
}

func (d *Downloader) setPartialJobResult(ctx context.Context, jobID string, result any) error {
	if jobID == "" {
		return nil
	}
	body, err := json.Marshal(result)
	if err != nil {
		return err
	}
	store, err := d.jobStore()
	if err != nil {
		return err
	}

	return store.SetPartialResult(ctx, jobID, string(body))
}

func (d *Downloader) setJobResultTx(ctx context.Context, tx *sql.Tx, jobID string, result any) error {
	if jobID == "" {
		return nil
	}
	body, err := json.Marshal(result)
	if err != nil {
		return err
	}
	store, err := d.jobStore()
	if err != nil {
		return err
	}

	return store.CompleteWithResultTx(ctx, tx, jobID, string(body))
}

func (d *Downloader) finishChannelJob(ctx context.Context, jobID string, channelID string, subscribe bool, result any) error {
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if channelID != "" {
		if subscribe {
			if err := d.markChannelSubscribed(ctx, tx, channelID); err != nil {
				return err
			}
		}
		if err := d.markChannelScanned(ctx, tx, channelID); err != nil {
			return err
		}
	}
	if err := d.setJobResultTx(ctx, tx, jobID, result); err != nil {
		return err
	}

	return tx.Commit()
}

func (d *Downloader) finishChannelFirstJob(ctx context.Context, jobID string, catalogResult channelCatalogResult, firstVideoURL string) error {
	store, err := d.jobStore()
	if err != nil {
		return err
	}
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if catalogResult.ChannelID != "" {
		if err := d.markChannelSubscribed(ctx, tx, catalogResult.ChannelID); err != nil {
			return err
		}
		if err := d.markChannelScanned(ctx, tx, catalogResult.ChannelID); err != nil {
			return err
		}
	}
	downloadJob, _, err := enqueueDownloadTx(ctx, store, tx, Payload{URL: firstVideoURL}, true)
	if err != nil {
		return err
	}
	result := channelFirstResult{
		ChannelID:     catalogResult.ChannelID,
		Videos:        catalogResult.Videos,
		FirstVideoURL: firstVideoURL,
		DownloadJobID: downloadJob.ID,
		Catalog:       catalogResult,
	}
	if err := d.setJobResultTx(ctx, tx, jobID, result); err != nil {
		return err
	}

	return tx.Commit()
}

func (d *Downloader) replaceDownloadedSubtitles(ctx context.Context, exec sqlExecutor, videoID string, subtitles []validatedSubtitle) error {
	if _, err := exec.ExecContext(ctx, "DELETE FROM subtitles WHERE video_id = ? AND source = 'downloaded'", videoID); err != nil {
		return err
	}
	if _, err := exec.ExecContext(ctx, "DELETE FROM search_documents WHERE owner_type = 'subtitle' AND owner_id = ? AND field LIKE 'text:%:downloaded'", videoID); err != nil {
		return err
	}
	for _, subtitle := range subtitles {
		if err := d.upsertSubtitle(ctx, exec, videoID, subtitle); err != nil {
			return err
		}
	}

	return nil
}

func (d *Downloader) upsertSubtitle(ctx context.Context, exec sqlExecutor, videoID string, subtitle validatedSubtitle) error {
	_, err := exec.ExecContext(ctx, `
INSERT INTO subtitles (video_id, language, name, source, format, path, text)
VALUES (?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(video_id, language, source) DO UPDATE SET
  name = excluded.name,
  format = excluded.format,
  path = excluded.path,
  text = excluded.text`, videoID, subtitle.Language, subtitle.Name, subtitle.Source, subtitle.Format, subtitle.Path, subtitle.Text)
	if err != nil {
		return err
	}

	return d.upsertSearch(ctx, exec, "subtitle", videoID, subtitleSearchField(subtitle), subtitle.Text)
}

func subtitleSearchField(subtitle validatedSubtitle) string {
	return "text:" + subtitle.Language + ":" + subtitle.Source
}

func (d *Downloader) readSubtitleText(path string) (string, error) {
	body, err := os.ReadFile(filepath.Join(d.config.MediaRoot, filepath.FromSlash(path)))
	if err != nil {
		return "", err
	}

	return string(body), nil
}

func cleanSubtitleLanguage(value string) string {
	value = strings.ToLower(strings.ReplaceAll(strings.TrimSpace(value), "_", "-"))
	if value == "" {
		return ""
	}
	for _, char := range value {
		if char >= 'a' && char <= 'z' || char >= '0' && char <= '9' || char == '-' {
			continue
		}
		return ""
	}

	return value
}

func cleanSubtitleFormat(value string) string {
	value = strings.TrimPrefix(strings.ToLower(strings.TrimSpace(value)), ".")
	switch value {
	case "vtt", "srt":
		return value
	default:
		return ""
	}
}

func isSafeMetadataValue(value string) bool {
	for _, char := range value {
		if char < 0x20 {
			return false
		}
	}

	return true
}

func isSafeMetadataText(value string) bool {
	for _, char := range value {
		if char == '\n' || char == '\r' || char == '\t' {
			continue
		}
		if char < 0x20 {
			return false
		}
	}

	return true
}

func (d *Downloader) validatedAssetPath(kind string, raw string, required bool) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		if required {
			return "", fmt.Errorf("download %s path missing", kind)
		}
		return "", nil
	}

	path, err := d.assetPath(raw)
	if err != nil {
		return "", fmt.Errorf("invalid download %s path: %w", kind, err)
	}
	exists, err := d.assetExists(path, kind)
	if err != nil {
		return "", err
	}
	if !exists {
		if required {
			return "", fmt.Errorf("download %s file missing: %s", kind, path)
		}
		return "", nil
	}

	return path, nil
}

func (d *Downloader) assetExists(path string, kind string) (bool, error) {
	info, err := d.lstatAsset(path, kind)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("download %s file check failed: %w", kind, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return false, fmt.Errorf("download %s file is a symlink: %s", kind, path)
	}
	if !info.Mode().IsRegular() {
		return false, fmt.Errorf("download %s file is not a regular file: %s", kind, path)
	}

	return true, nil
}

func (d *Downloader) lstatAsset(path string, kind string) (os.FileInfo, error) {
	_, info, err := assetpath.Lstat(d.config.MediaRoot, path)
	if errors.Is(err, assetpath.ErrSymlink) {
		return nil, fmt.Errorf("download %s file path contains symlink: %s", kind, path)
	}
	if errors.Is(err, assetpath.ErrInvalid) {
		return nil, fmt.Errorf("download %s file path is unsafe: %s", kind, path)
	}

	return info, err
}

func (d *Downloader) syncOptionalAsset(ctx context.Context, exec sqlExecutor, ownerType string, ownerID string, kind string, path string) error {
	return denorm.SyncMediaAsset(ctx, exec, ownerType, ownerID, kind, path)
}

func (d *Downloader) assetPath(raw string) (string, error) {
	if strings.TrimSpace(raw) == "" {
		return "", nil
	}

	return assetpath.FromMediaRoot(d.config.MediaRoot, raw)
}

func (d *Downloader) upsertChannel(ctx context.Context, exec sqlExecutor, id string, name string, description string, thumbnailURL string) error {
	_, err := exec.ExecContext(ctx, `
INSERT INTO channels (id, external_id, name, description, thumbnail_url, updated_at)
VALUES (?, ?, ?, ?, ?, strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
ON CONFLICT(id) DO UPDATE SET
  name = excluded.name,
	description = CASE WHEN excluded.description <> '' THEN excluded.description ELSE channels.description END,
	thumbnail_url = CASE WHEN excluded.thumbnail_url <> '' THEN excluded.thumbnail_url ELSE channels.thumbnail_url END,
  updated_at = excluded.updated_at`, id, id, name, strings.TrimSpace(description), thumbnailURL)
	if err != nil {
		return err
	}
	if err := d.upsertSearch(ctx, exec, "channel", id, "name", name); err != nil {
		return err
	}
	if strings.TrimSpace(description) != "" {
		return d.upsertSearch(ctx, exec, "channel", id, "description", description)
	}

	return nil
}

func (d *Downloader) markChannelScanned(ctx context.Context, exec sqlExecutor, id string) error {
	_, err := exec.ExecContext(ctx, "UPDATE channels SET last_scanned_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now'), updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now') WHERE id = ?", id)

	return err
}

func (d *Downloader) markChannelSubscribed(ctx context.Context, exec sqlExecutor, id string) error {
	_, err := exec.ExecContext(ctx, "UPDATE channels SET subscribed = 1, updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now') WHERE id = ?", id)

	return err
}

func (d *Downloader) upsertVideo(ctx context.Context, exec sqlExecutor, videoID string, metadata metadata, channelID string, mediaPath string, thumbnailPath string, origin string) error {
	viewCount, hasViewCount := viewCountValue(metadata.ViewCount)
	now := time.Now().UTC().Format(time.RFC3339Nano)
	mediaOrigin := downloadOrigin(origin)
	_, err := exec.ExecContext(ctx, `
INSERT INTO videos (
	  id, external_id, channel_id, title, description, published_at, duration_seconds, view_count, media_path, thumbnail_path, thumbnail_url, media_origin, media_downloaded_at, archived_at, updated_at
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(id) DO UPDATE SET
	  channel_id = excluded.channel_id,
	  title = excluded.title,
	  description = excluded.description,
	  published_at = excluded.published_at,
	  duration_seconds = excluded.duration_seconds,
	  view_count = CASE WHEN ? THEN excluded.view_count ELSE videos.view_count END,
	  media_path = excluded.media_path,
	  thumbnail_path = excluded.thumbnail_path,
	  thumbnail_url = excluded.thumbnail_url,
	  media_origin = CASE WHEN videos.media_origin = 'manual' OR excluded.media_origin = 'manual' THEN 'manual' ELSE excluded.media_origin END,
	  media_downloaded_at = excluded.media_downloaded_at,
	  archived_at = CASE WHEN videos.archived_at IS NOT NULL AND videos.archived_at <> '' THEN videos.archived_at ELSE excluded.archived_at END,
	  updated_at = excluded.updated_at
ON CONFLICT(source, external_id) DO UPDATE SET
	  channel_id = excluded.channel_id,
	  title = excluded.title,
	  description = excluded.description,
	  published_at = excluded.published_at,
	  duration_seconds = excluded.duration_seconds,
	  view_count = CASE WHEN ? THEN excluded.view_count ELSE videos.view_count END,
	  media_path = excluded.media_path,
	  thumbnail_path = excluded.thumbnail_path,
	  thumbnail_url = excluded.thumbnail_url,
	  media_origin = CASE WHEN videos.media_origin = 'manual' OR excluded.media_origin = 'manual' THEN 'manual' ELSE excluded.media_origin END,
	  media_downloaded_at = excluded.media_downloaded_at,
	  archived_at = CASE WHEN videos.archived_at IS NOT NULL AND videos.archived_at <> '' THEN videos.archived_at ELSE excluded.archived_at END,
	  updated_at = excluded.updated_at`,
		videoID,
		metadata.ID,
		nullEmpty(channelID),
		firstNonEmpty(metadata.Title, metadata.ID),
		metadata.Description,
		dateFromUploadDate(metadata.UploadDate),
		durationSeconds(metadata.Duration),
		viewCount,
		mediaPath,
		thumbnailPath,
		catalogThumbnailURL(metadata.Thumbnail, metadata.Thumbnails),
		mediaOrigin,
		now,
		now,
		now,
		hasViewCount,
		hasViewCount,
	)

	return err
}

func (d *Downloader) upsertCatalogVideo(ctx context.Context, exec sqlExecutor, video catalogVideo) error {
	_, err := exec.ExecContext(ctx, `
INSERT INTO videos (
	  id, external_id, channel_id, title, description, published_at, catalog_position, duration_seconds, view_count, thumbnail_url, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
ON CONFLICT(id) DO UPDATE SET
	  channel_id = excluded.channel_id,
	  title = CASE
	    WHEN videos.media_path <> '' AND excluded.description = '' AND excluded.duration_seconds = 0 THEN videos.title
	    ELSE excluded.title
	  END,
	  description = CASE WHEN excluded.description <> '' THEN excluded.description ELSE videos.description END,
	  published_at = CASE
	    WHEN excluded.published_at IS NULL THEN videos.published_at
	    WHEN videos.media_path <> '' AND videos.published_at IS NOT NULL AND videos.published_at <> '' THEN videos.published_at
	    ELSE excluded.published_at
	  END,
	  catalog_position = excluded.catalog_position,
	  duration_seconds = CASE WHEN excluded.duration_seconds > 0 THEN excluded.duration_seconds ELSE videos.duration_seconds END,
	  view_count = CASE WHEN ? THEN excluded.view_count ELSE videos.view_count END,
	  thumbnail_url = CASE WHEN excluded.thumbnail_url <> '' THEN excluded.thumbnail_url ELSE videos.thumbnail_url END,
  updated_at = excluded.updated_at`,
		video.ID,
		video.ID,
		nullEmpty(video.ChannelID),
		firstNonEmpty(video.Title, video.ID),
		video.Description,
		nullEmpty(video.PublishedAt),
		video.CatalogPosition,
		video.DurationSeconds,
		video.ViewCount,
		video.ThumbnailURL,
		video.HasViewCount,
	)

	return err
}

func (d *Downloader) upsertAsset(ctx context.Context, exec sqlExecutor, ownerType string, ownerID string, kind string, path string) error {
	return denorm.SyncMediaAsset(ctx, exec, ownerType, ownerID, kind, path)
}

func (d *Downloader) upsertSearch(ctx context.Context, exec sqlExecutor, ownerType string, ownerID string, field string, text string) error {
	return denorm.SyncSearchDocument(ctx, exec, ownerType, ownerID, field, text)
}

func (m metadata) mediaPath() string {
	if len(m.RequestedDownloads) > 0 && m.RequestedDownloads[0].Filepath != "" {
		return m.RequestedDownloads[0].Filepath
	}

	return m.Filepath
}

func dateFromUploadDate(value string) any {
	if len(value) != 8 {
		return nullEmpty(value)
	}

	return value[0:4] + "-" + value[4:6] + "-" + value[6:8]
}

func durationSeconds(value float64) int {
	if value <= 0 {
		return 0
	}

	return int(value)
}

func viewCountValue(value *int) (int, bool) {
	if value == nil || *value < 0 {
		return 0, false
	}

	return *value, true
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}

	return ""
}

func nullEmpty(value string) any {
	if value == "" {
		return nil
	}

	return value
}
