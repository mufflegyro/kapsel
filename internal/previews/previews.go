package previews

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"kapsel/internal/assetpath"
	"kapsel/internal/jobs"
	"kapsel/internal/sandbox"
)

const (
	DefaultIntervalSeconds  = 10
	DefaultFrameWidth       = 160
	DefaultFrameHeight      = 90
	DefaultColumns          = 5
	DefaultFFMPEGPath       = "ffmpeg"
	JobType                 = "timeline_preview"
	SpriteAssetKind         = "timeline_preview_sprite"
	maxPreviewCommandOutput = 64 * 1024
)

type Payload struct {
	VideoID string `json:"video_id"`
}

func EnqueueJob(ctx context.Context, store *jobs.Store, videoID string) (jobs.Job, error) {
	if store == nil {
		return jobs.Job{}, errors.New("preview enqueue missing job store")
	}
	payloadJSON, err := canonicalPayloadJSON(videoID)
	if err != nil {
		return jobs.Job{}, err
	}
	job, _, err := store.FindOrEnqueue(ctx, jobs.EnqueueParams{Type: JobType, PayloadJSON: payloadJSON, MaxAttempts: 1}, func(ctx context.Context, tx *sql.Tx) (jobs.Job, bool, error) {
		return store.ActiveByPayloadTx(ctx, tx, JobType, payloadJSON)
	})

	return job, err
}

func EnqueueJobTx(ctx context.Context, store *jobs.Store, tx *sql.Tx, videoID string) (jobs.Job, bool, error) {
	if store == nil {
		return jobs.Job{}, false, errors.New("preview enqueue missing job store")
	}
	payloadJSON, err := canonicalPayloadJSON(videoID)
	if err != nil {
		return jobs.Job{}, false, err
	}

	return store.FindOrEnqueueTx(ctx, tx, jobs.EnqueueParams{Type: JobType, PayloadJSON: payloadJSON, MaxAttempts: 1}, func(ctx context.Context, tx *sql.Tx) (jobs.Job, bool, error) {
		return store.ActiveByPayloadTx(ctx, tx, JobType, payloadJSON)
	})
}

func ActiveJobForVideo(ctx context.Context, store *jobs.Store, videoID string) (jobs.Job, bool, error) {
	if store == nil {
		return jobs.Job{}, false, nil
	}
	payloadJSON, err := canonicalPayloadJSON(videoID)
	if err != nil {
		return jobs.Job{}, false, err
	}

	return store.ActiveByPayload(ctx, JobType, payloadJSON)
}

func canonicalPayloadJSON(videoID string) (string, error) {
	videoID = strings.TrimSpace(videoID)
	if videoID == "" {
		return "", errors.New("preview payload missing video id")
	}
	body, err := json.Marshal(Payload{VideoID: videoID})
	if err != nil {
		return "", err
	}

	return string(body), nil
}

type Result struct {
	VideoID      string `json:"video_id"`
	SpritePath   string `json:"sprite_path"`
	PreviewCount int    `json:"preview_count"`
}

type Config struct {
	MediaRoot       string
	FFmpegPath      string
	IntervalSeconds int
	FrameWidth      int
	FrameHeight     int
	Columns         int
	Runner          Runner
	JobStore        *jobs.Store
}

type Video struct {
	ID              string
	MediaPath       string
	DurationSeconds int
}

type Metadata struct {
	VideoID         string
	SpritePath      string
	IntervalSeconds int
	FrameWidth      int
	FrameHeight     int
	Columns         int
	Count           int
}

type Command struct {
	Name    string
	Args    []string
	Dir     string
	Kind    sandbox.Kind
	Access  sandbox.Access
	Network sandbox.NetworkPolicy
}

type Runner interface {
	Run(context.Context, Command) error
}

type ExecRunner struct {
	Backend sandbox.Backend
}

type JobHandler struct {
	db     *sql.DB
	store  *jobs.Store
	config Config
}

type sqlExecutor interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

func (r ExecRunner) Run(ctx context.Context, command Command) error {
	backend := r.Backend
	if backend == nil {
		backend = sandbox.BasicBackend{}
	}
	kind := command.Kind
	if kind == "" {
		kind = sandbox.KindFFMPEG
	}
	stdout := previewOutputBuffer{max: maxPreviewCommandOutput}
	stderr := previewOutputBuffer{max: maxPreviewCommandOutput}
	err := backend.Run(ctx, sandbox.Spec{
		Name:    command.Name,
		Args:    command.Args,
		Kind:    kind,
		Dir:     command.Dir,
		Access:  command.Access,
		Network: command.Network,
	}, sandbox.IO{Stdout: &stdout, Stderr: &stderr})
	if err != nil {
		return fmt.Errorf("preview command failed at %q: %s: %w", command.Name, strings.TrimSpace(string(combinedPreviewOutput(stdout.Bytes(), stderr.Bytes()))), err)
	}

	return nil
}

type previewOutputBuffer struct {
	bytes.Buffer
	max int
}

func (b *previewOutputBuffer) Write(p []byte) (int, error) {
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

func combinedPreviewOutput(stdout []byte, stderr []byte) []byte {
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

func NewJobHandler(db *sql.DB, config Config) *JobHandler {
	return &JobHandler{db: db, store: config.JobStore, config: config}
}

func (h *JobHandler) Handle(ctx context.Context, job jobs.Job) error {
	if h.db == nil {
		return errors.New("preview handler missing database")
	}
	if h.store == nil {
		return errors.New("preview handler missing job store")
	}
	var payload Payload
	if err := json.Unmarshal([]byte(job.PayloadJSON), &payload); err != nil {
		return err
	}
	video, err := h.loadVideo(ctx, payload.VideoID)
	if err != nil {
		return err
	}
	metadata, err := Generate(ctx, h.config, video)
	if err != nil {
		return err
	}
	resultJSON, err := json.Marshal(Result{VideoID: metadata.VideoID, SpritePath: metadata.SpritePath, PreviewCount: metadata.Count})
	if err != nil {
		return err
	}
	tx, err := h.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := Upsert(ctx, tx, metadata); err != nil {
		return err
	}
	if err := h.store.CompleteWithResultTx(ctx, tx, job.ID, string(resultJSON)); err != nil {
		return err
	}

	return tx.Commit()
}

func (h *JobHandler) loadVideo(ctx context.Context, id string) (Video, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return Video{}, errors.New("preview payload missing video id")
	}
	var video Video
	err := h.db.QueryRowContext(ctx, "SELECT id, media_path, duration_seconds FROM videos WHERE id = ?", id).Scan(&video.ID, &video.MediaPath, &video.DurationSeconds)
	if errors.Is(err, sql.ErrNoRows) {
		return Video{}, fmt.Errorf("preview video %q not found", id)
	}
	if err != nil {
		return Video{}, err
	}
	if strings.TrimSpace(video.MediaPath) == "" {
		return Video{}, fmt.Errorf("preview video %q has no media path", id)
	}

	return video, nil
}

func GenerateAndStore(ctx context.Context, db *sql.DB, config Config, video Video) (Metadata, error) {
	metadata, err := Generate(ctx, config, video)
	if err != nil {
		return Metadata{}, err
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return Metadata{}, err
	}
	defer tx.Rollback()
	if err := Upsert(ctx, tx, metadata); err != nil {
		return Metadata{}, err
	}
	if err := tx.Commit(); err != nil {
		return Metadata{}, err
	}

	return metadata, nil
}

func Generate(ctx context.Context, config Config, video Video) (Metadata, error) {
	config = withDefaults(config)
	mediaPath, err := assetpath.Clean(video.MediaPath)
	if err != nil {
		return Metadata{}, err
	}
	if strings.TrimSpace(video.ID) == "" || strings.ContainsAny(video.ID, `/\`) || strings.Contains(video.ID, "..") {
		return Metadata{}, assetpath.ErrInvalid
	}
	mediaRoot, err := filepath.Abs(config.MediaRoot)
	if err != nil {
		return Metadata{}, err
	}
	spritePath, err := assetpath.Clean("derived/previews/" + video.ID + "/sprite.jpg")
	if err != nil {
		return Metadata{}, err
	}
	inputPath := filepath.Join(mediaRoot, filepath.FromSlash(mediaPath))
	if err := requireRegularFile(inputPath); err != nil {
		return Metadata{}, err
	}
	outputDir, err := safeOutputDir(mediaRoot, filepath.Dir(spritePath))
	if err != nil {
		return Metadata{}, err
	}
	outputPath := filepath.Join(outputDir, filepath.Base(spritePath))
	tempPath := outputPath + ".tmp"
	defer os.Remove(tempPath)

	count := previewCount(video.DurationSeconds, config.IntervalSeconds)
	rows := (count + config.Columns - 1) / config.Columns
	filter := fmt.Sprintf("fps=1/%d,scale=%d:%d:force_original_aspect_ratio=decrease,pad=%d:%d:(ow-iw)/2:(oh-ih)/2,tile=%dx%d", config.IntervalSeconds, config.FrameWidth, config.FrameHeight, config.FrameWidth, config.FrameHeight, config.Columns, rows)
	command := Command{
		Name:    config.FFmpegPath,
		Args:    []string{"-y", "-hide_banner", "-loglevel", "error", "-nostdin", "-threads", "1", "-i", inputPath, "-map", "0:v:0", "-an", "-sn", "-dn", "-vf", filter, "-frames:v", "1", "-f", "image2", tempPath},
		Dir:     mediaRoot,
		Kind:    sandbox.KindFFMPEG,
		Access:  previewAccess(inputPath, outputDir),
		Network: sandbox.NetworkDeny,
	}
	if err := config.Runner.Run(ctx, command); err != nil {
		return Metadata{}, err
	}
	if err := requireRegularFile(tempPath); err != nil {
		return Metadata{}, err
	}
	if err := os.Rename(tempPath, outputPath); err != nil {
		return Metadata{}, err
	}

	return Metadata{
		VideoID:         video.ID,
		SpritePath:      spritePath,
		IntervalSeconds: config.IntervalSeconds,
		FrameWidth:      config.FrameWidth,
		FrameHeight:     config.FrameHeight,
		Columns:         config.Columns,
		Count:           count,
	}, nil
}

func previewAccess(inputPath string, outputDir string) sandbox.Access {
	access := sandbox.Access{}
	if strings.TrimSpace(inputPath) != "" {
		access.ReadOnly = append(access.ReadOnly, sandbox.PathGrant{Path: filepath.Clean(inputPath), Kind: sandbox.PathLiteral})
	}
	if strings.TrimSpace(outputDir) != "" {
		access.ReadWrite = append(access.ReadWrite, sandbox.PathGrant{Path: filepath.Clean(outputDir), Kind: sandbox.PathSubtree})
	}

	return access
}

func Upsert(ctx context.Context, exec sqlExecutor, metadata Metadata) error {
	_, err := exec.ExecContext(ctx, `
INSERT INTO video_previews (video_id, sprite_path, interval_seconds, frame_width, frame_height, columns, preview_count, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
ON CONFLICT(video_id) DO UPDATE SET
  sprite_path = excluded.sprite_path,
  interval_seconds = excluded.interval_seconds,
  frame_width = excluded.frame_width,
  frame_height = excluded.frame_height,
  columns = excluded.columns,
  preview_count = excluded.preview_count,
  updated_at = excluded.updated_at`, metadata.VideoID, metadata.SpritePath, metadata.IntervalSeconds, metadata.FrameWidth, metadata.FrameHeight, metadata.Columns, metadata.Count)
	if err != nil {
		return err
	}
	_, err = exec.ExecContext(ctx, `
INSERT INTO media_assets (owner_type, owner_id, kind, path)
VALUES ('video', ?, ?, ?)
ON CONFLICT(owner_type, owner_id, kind) DO UPDATE SET path = excluded.path`, metadata.VideoID, SpriteAssetKind, metadata.SpritePath)

	return err
}

func withDefaults(config Config) Config {
	if config.FFmpegPath == "" {
		config.FFmpegPath = DefaultFFMPEGPath
	}
	if config.IntervalSeconds <= 0 {
		config.IntervalSeconds = DefaultIntervalSeconds
	}
	if config.FrameWidth <= 0 {
		config.FrameWidth = DefaultFrameWidth
	}
	if config.FrameHeight <= 0 {
		config.FrameHeight = DefaultFrameHeight
	}
	if config.Columns <= 0 {
		config.Columns = DefaultColumns
	}
	if config.Runner == nil {
		config.Runner = ExecRunner{}
	}

	return config
}

func previewCount(durationSeconds int, intervalSeconds int) int {
	if durationSeconds <= 0 {
		return 1
	}
	count := (durationSeconds + intervalSeconds - 1) / intervalSeconds
	if count < 1 {
		return 1
	}

	return count
}

func requireRegularFile(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("preview path is not a regular file")
	}

	return nil
}

func safeOutputDir(mediaRoot string, relativeDir string) (string, error) {
	rootAbs, err := filepath.Abs(mediaRoot)
	if err != nil {
		return "", err
	}
	rootReal, err := filepath.EvalSymlinks(rootAbs)
	if err != nil {
		return "", err
	}
	current := rootAbs
	for _, segment := range strings.Split(filepath.Clean(filepath.FromSlash(relativeDir)), string(filepath.Separator)) {
		if segment == "." || segment == "" {
			continue
		}
		current = filepath.Join(current, segment)
		info, err := os.Lstat(current)
		if os.IsNotExist(err) {
			if err := os.Mkdir(current, 0o755); err != nil {
				return "", err
			}
			continue
		}
		if err != nil {
			return "", err
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return "", assetpath.ErrInvalid
		}
	}
	dirReal, err := filepath.EvalSymlinks(current)
	if err != nil {
		return "", err
	}
	if outsideRoot(rootReal, dirReal) {
		return "", assetpath.ErrInvalid
	}

	return current, nil
}

func outsideRoot(root string, path string) bool {
	relative, err := filepath.Rel(root, path)
	if err != nil {
		return true
	}

	return relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator))
}
