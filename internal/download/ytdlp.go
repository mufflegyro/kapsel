package download

import (
	"bytes"
	"context"
	crand "crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"kapsel/internal/jobs"
	"kapsel/internal/sandbox"
	"math/big"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const DefaultFormatSelector = "bv[height<=1080][ext=mp4][vcodec^=avc1][acodec=none]+ba[ext=m4a][acodec^=mp4a]/b[height<=1080][ext=mp4][vcodec^=avc1][acodec^=mp4a]/b[height<=1080][ext=mp4]/best[height<=1080]"

const DefaultYTDLPSleepInterval = 10 * time.Second

const DefaultYTDLPRetryDelay = 10 * time.Minute

const DefaultYTDLPAuthRetryDelay = time.Hour

// DefaultPremiereBuffer is added to a parsed time-until-premiere so the retry
// lands after the video has actually been published and is downloadable.
const DefaultPremiereBuffer = 30 * time.Minute

const DefaultSubtitleLanguages = "all"

const DefaultAutomaticSubtitleLanguages = ".*-orig"

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

type ExecRunner struct {
	Backend sandbox.Backend
}

type stdoutProgressWriter struct {
	buffer   *limitedBuffer
	progress *progressWriter
	pending  string
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
	if delay, ok := parsePremiereDelay(text); ok {
		return delay
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

// premiereDelayPattern matches yt-dlp's message for a scheduled premiere that
// has not started yet (e.g. "ERROR: [youtube] fEDRRQgykd8: Premieres in 26
// hours"). The capture group holds the consecutive duration components that
// follow, so "1 hour 30 minutes" is captured as one phrase.
var premiereDelayPattern = regexp.MustCompile(`(?i)premieres?\s+in\s+((?:\d+\s*(?:hours?|minutes?|seconds?|days?)[\s,]*)+)`)

// premiereDurationComponentPattern extracts one number/unit pair such as
// "26 hours" from a premiere duration phrase.
var premiereDurationComponentPattern = regexp.MustCompile(`(?i)(\d+)\s*(hours?|minutes?|seconds?|days?)`)

// parsePremiereDelay extracts the time until a scheduled premiere from yt-dlp
// error output and returns it with DefaultPremiereBuffer added so the retry
// lands just after the video is published. It reports false when the text does
// not contain a premiere message or the stated duration cannot be parsed.
func parsePremiereDelay(text string) (time.Duration, bool) {
	match := premiereDelayPattern.FindStringSubmatch(text)
	if match == nil {
		return 0, false
	}

	delay := time.Duration(0)
	for _, component := range premiereDurationComponentPattern.FindAllStringSubmatch(match[1], -1) {
		value, err := strconv.Atoi(component[1])
		if err != nil || value < 0 {
			continue
		}
		switch {
		case strings.HasPrefix(strings.ToLower(component[2]), "day"):
			delay += time.Duration(value) * 24 * time.Hour
		case strings.HasPrefix(strings.ToLower(component[2]), "hour"):
			delay += time.Duration(value) * time.Hour
		case strings.HasPrefix(strings.ToLower(component[2]), "minute"):
			delay += time.Duration(value) * time.Minute
		case strings.HasPrefix(strings.ToLower(component[2]), "second"):
			delay += time.Duration(value) * time.Second
		}
	}
	if delay <= 0 {
		return 0, false
	}

	return delay + DefaultPremiereBuffer, true
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

	args := d.ytdlpArgs(cookiesFile,
		"--no-playlist",
		"--no-simulate",
		"--newline",
		"--progress",
		"--check-formats",
		"--dump-single-json",
		"--write-info-json",
		"--write-thumbnail",
		"--format", d.config.FormatSelector,
		"--merge-output-format", "mp4",
		"--paths", mediaRoot,
		"--output", "%(id)s.%(ext)s",
		downloadURL,
	)
	if d.config.SubtitlesEnabled {
		args = append(args,
			"--write-subs",
			"--sub-langs", DefaultSubtitleLanguages,
			"--convert-subs", "vtt",
		)
	}

	return Command{
		Name:    d.config.YTDLPPath,
		Dir:     mediaRoot,
		Kind:    sandbox.KindYTDLP,
		Access:  d.ytdlpAccess(mediaRoot, cookiesFile, true),
		Network: sandbox.NetworkAllow,
		Args:    args,
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

// BuildVideoMetadataScanCommand builds a metadata-only yt-dlp command for a
// single video: it fetches the catalog metadata (title, channel, thumbnail,
// duration, view count) without downloading or writing any media. This is the
// single-video equivalent of the channel scan path and is used by the
// video_metadata_scan job so playlist imports can populate the catalog without
// transferring media.
func (d *Downloader) BuildVideoMetadataScanCommand(rawURL string) (Command, error) {
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
		Access:  d.ytdlpAccess(mediaRoot, cookiesFile, false),
		Network: sandbox.NetworkAllow,
		Args: d.ytdlpArgs(cookiesFile,
			"--no-playlist",
			"--no-simulate",
			"--skip-download",
			"--dump-single-json",
			"--no-write-info-json",
			"--no-write-thumbnail",
			"--no-write-subs",
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

// BuildPlaylistImportCommand builds a metadata-only yt-dlp flat dump for a
// YouTube playlist URL. Flat entries carry the video ids (plus light metadata)
// without downloading media, matching the channel-catalog scan shape; the same
// bounded stdout cap applies since flat entries are small.
func (d *Downloader) BuildPlaylistImportCommand(playlistURL string) (Command, error) {
	mediaRoot, cookiesFile, err := d.ytdlpSandboxPaths()
	if err != nil {
		return Command{}, err
	}
	args := d.ytdlpArgs(cookiesFile, "--flat-playlist", "--dump-single-json", playlistURL)

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
	Updated    bool   `json:"updated"`
	Version    string `json:"version,omitempty"`
	Skipped    bool   `json:"skipped,omitempty"`
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

// membersOnlyFailurePatterns match yt-dlp's message when a video is restricted
// to a channel's paying members. Such a video can never be downloaded without
// membership, so Kapsel should mark it and skip further retries.
var membersOnlyFailurePatterns = []string{
	"available to this channel's members",
	"members-only content",
	"join this channel to get access",
	"member-only",
}

func isMembersOnlyYTDLPFailure(output []byte, err error) bool {
	if err == nil {
		return false
	}
	text := strings.ToLower(string(output) + "\n" + err.Error())
	for _, pattern := range membersOnlyFailurePatterns {
		if strings.Contains(text, pattern) {
			return true
		}
	}

	return false
}

func (d *Downloader) markVideoMembersOnly(ctx context.Context, videoID string) error {
	if d.db == nil || strings.TrimSpace(videoID) == "" {
		return nil
	}
	_, err := d.db.ExecContext(ctx, "UPDATE videos SET members_only = 1, updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now') WHERE external_id = ? AND source = 'youtube'", videoID)

	return err
}
