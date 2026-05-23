package previews

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"kapsel/internal/assetpath"
	"kapsel/internal/database"
	"kapsel/internal/jobs"
)

func TestGenerateAndStorePreviewMetadata(t *testing.T) {
	t.Parallel()

	db := openPreviewDB(t)
	seedPreviewVideo(t, db)
	mediaRoot := t.TempDir()
	writePreviewFile(t, filepath.Join(mediaRoot, "videos", "vid-1.mp4"), "video")
	runner := &fakeRunner{}

	metadata, err := GenerateAndStore(context.Background(), db, Config{MediaRoot: mediaRoot, FFmpegPath: "ffmpeg", Runner: runner}, Video{
		ID:              "vid-1",
		MediaPath:       "videos/vid-1.mp4",
		DurationSeconds: 25,
	})
	if err != nil {
		t.Fatal(err)
	}

	if metadata.SpritePath != "derived/previews/vid-1/sprite.jpg" || metadata.Count != 3 || metadata.Columns != DefaultColumns {
		t.Fatalf("unexpected preview metadata: %#v", metadata)
	}
	if len(runner.commands) != 1 || runner.commands[0].Name != "ffmpeg" {
		t.Fatalf("expected ffmpeg command, got %#v", runner.commands)
	}
	if runner.commands[0].Dir != mediaRoot {
		t.Fatalf("expected preview command working directory %q, got %q", mediaRoot, runner.commands[0].Dir)
	}
	assertPreviewScalar(t, db, "SELECT sprite_path FROM video_previews WHERE video_id = ?", "derived/previews/vid-1/sprite.jpg", "vid-1")
	assertPreviewScalar(t, db, "SELECT preview_count FROM video_previews WHERE video_id = ?", int64(3), "vid-1")
	assertPreviewScalar(t, db, "SELECT path FROM media_assets WHERE owner_type = 'video' AND owner_id = ? AND kind = ?", "derived/previews/vid-1/sprite.jpg", "vid-1", SpriteAssetKind)
}

func TestGenerateSpecifiesOutputFormatForTemporarySpritePath(t *testing.T) {
	t.Parallel()

	mediaRoot := t.TempDir()
	writePreviewFile(t, filepath.Join(mediaRoot, "videos", "vid-1.mp4"), "video")
	runner := &fakeRunner{}

	_, err := Generate(context.Background(), Config{MediaRoot: mediaRoot, FFmpegPath: "ffmpeg", Runner: runner}, Video{ID: "vid-1", MediaPath: "videos/vid-1.mp4", DurationSeconds: 25})
	if err != nil {
		t.Fatal(err)
	}
	if len(runner.commands) != 1 {
		t.Fatalf("expected one preview command, got %#v", runner.commands)
	}
	args := runner.commands[0].Args
	if !argsContainSequence(args, "-f", "image2") {
		t.Fatalf("expected ffmpeg command to specify image2 output format for .tmp path, got %#v", args)
	}
}

func TestGenerateResolvesRelativeMediaRootForCommand(t *testing.T) {
	root := t.TempDir()
	t.Chdir(root)
	mediaRoot := filepath.Join("data", "media")
	wantMediaRoot := filepath.Join(root, mediaRoot)
	writePreviewFile(t, filepath.Join(wantMediaRoot, "videos", "vid-1.mp4"), "video")
	runner := &fakeRunner{}

	metadata, err := Generate(context.Background(), Config{MediaRoot: mediaRoot, FFmpegPath: "ffmpeg", Runner: runner}, Video{ID: "vid-1", MediaPath: "videos/vid-1.mp4", DurationSeconds: 25})
	if err != nil {
		t.Fatal(err)
	}
	if metadata.SpritePath != "derived/previews/vid-1/sprite.jpg" {
		t.Fatalf("unexpected metadata path %q", metadata.SpritePath)
	}
	if len(runner.commands) != 1 {
		t.Fatalf("expected one preview command, got %#v", runner.commands)
	}
	command := runner.commands[0]
	inputPath := filepath.Join(wantMediaRoot, "videos", "vid-1.mp4")
	outputDir := filepath.Join(wantMediaRoot, "derived", "previews", "vid-1")
	outputPath := filepath.Join(outputDir, "sprite.jpg.tmp")
	if command.Dir != wantMediaRoot {
		t.Fatalf("expected absolute command working directory %q, got %q", wantMediaRoot, command.Dir)
	}
	if !argsContainSequence(command.Args, "-i", inputPath) || command.Args[len(command.Args)-1] != outputPath {
		t.Fatalf("expected absolute ffmpeg input/output paths, got %#v", command.Args)
	}
	if len(command.Access.ReadOnly) != 1 || command.Access.ReadOnly[0].Path != inputPath {
		t.Fatalf("expected preview input read grant %q, got %#v", inputPath, command.Access.ReadOnly)
	}
	if len(command.Access.ReadWrite) != 1 || command.Access.ReadWrite[0].Path != outputDir {
		t.Fatalf("expected preview output write grant %q, got %#v", outputDir, command.Access.ReadWrite)
	}
}

func TestExecRunnerMinimizesEnvironmentAndSetsWorkdir(t *testing.T) {
	const helperArg = "preview-exec-runner-env-helper"
	if len(os.Args) >= 3 && os.Args[len(os.Args)-2] == helperArg {
		markerPath := os.Args[len(os.Args)-1]
		cwd, err := os.Getwd()
		if err != nil {
			_, _ = os.Stderr.WriteString(err.Error())
			os.Exit(1)
		}
		body := "kapsel=" + os.Getenv("KAPSEL_SESSION_SECRET") + "\n" +
			"custom=" + os.Getenv("SHOULD_NOT_LEAK") + "\n" +
			"tmp=" + os.Getenv("TMPDIR") + "\n" +
			"cwd=" + cwd + "\n"
		if err := os.WriteFile(markerPath, []byte(body), 0o644); err != nil {
			_, _ = os.Stderr.WriteString(err.Error())
			os.Exit(1)
		}
		os.Exit(0)
	}
	t.Setenv("KAPSEL_SESSION_SECRET", "top-secret")
	t.Setenv("SHOULD_NOT_LEAK", "top-secret")
	workdir := t.TempDir()
	markerPath := filepath.Join(t.TempDir(), "env.txt")

	if err := (ExecRunner{}).Run(context.Background(), Command{
		Name: os.Args[0],
		Args: []string{"-test.run=TestExecRunnerMinimizesEnvironmentAndSetsWorkdir", "--", helperArg, markerPath},
		Dir:  workdir,
	}); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(markerPath)
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	for _, unexpected := range []string{"top-secret", "kapsel=top-secret", "custom=top-secret"} {
		if strings.Contains(text, unexpected) {
			t.Fatalf("expected sanitized environment, got %q", text)
		}
	}
	resolvedWorkdir, err := filepath.EvalSymlinks(workdir)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(text, "tmp=") || !strings.Contains(text, "cwd="+resolvedWorkdir) {
		t.Fatalf("expected sandbox tmp and cwd in output, got %q", text)
	}
}

func TestGenerateRejectsUnsafePreviewPaths(t *testing.T) {
	t.Parallel()

	db := openPreviewDB(t)
	seedPreviewVideo(t, db)
	mediaRoot := t.TempDir()
	writePreviewFile(t, filepath.Join(mediaRoot, "videos", "vid-1.mp4"), "video")

	_, err := GenerateAndStore(context.Background(), db, Config{MediaRoot: mediaRoot, FFmpegPath: "ffmpeg", Runner: &fakeRunner{}}, Video{
		ID:              "../vid-1",
		MediaPath:       "videos/vid-1.mp4",
		DurationSeconds: 25,
	})
	if !errors.Is(err, assetpath.ErrInvalid) {
		t.Fatalf("expected unsafe preview path to be rejected, got %v", err)
	}
	assertPreviewScalar(t, db, "SELECT count(*) FROM video_previews", int64(0))
}

func TestGenerateRetryAfterFailureIsIdempotent(t *testing.T) {
	t.Parallel()

	db := openPreviewDB(t)
	seedPreviewVideo(t, db)
	mediaRoot := t.TempDir()
	writePreviewFile(t, filepath.Join(mediaRoot, "videos", "vid-1.mp4"), "video")
	runner := &fakeRunner{err: errors.New("ffmpeg failed")}
	video := Video{ID: "vid-1", MediaPath: "videos/vid-1.mp4", DurationSeconds: 25}

	if _, err := GenerateAndStore(context.Background(), db, Config{MediaRoot: mediaRoot, FFmpegPath: "ffmpeg", Runner: runner}, video); err == nil || !strings.Contains(err.Error(), "ffmpeg failed") {
		t.Fatalf("expected first generation to fail, got %v", err)
	}
	assertPreviewScalar(t, db, "SELECT count(*) FROM video_previews", int64(0))

	runner.err = nil
	if _, err := GenerateAndStore(context.Background(), db, Config{MediaRoot: mediaRoot, FFmpegPath: "ffmpeg", Runner: runner}, video); err != nil {
		t.Fatal(err)
	}
	if _, err := GenerateAndStore(context.Background(), db, Config{MediaRoot: mediaRoot, FFmpegPath: "ffmpeg", Runner: runner}, video); err != nil {
		t.Fatal(err)
	}
	assertPreviewScalar(t, db, "SELECT count(*) FROM video_previews WHERE video_id = ?", int64(1), "vid-1")
	assertPreviewScalar(t, db, "SELECT count(*) FROM media_assets WHERE owner_type = 'video' AND owner_id = ? AND kind = ?", int64(1), "vid-1", SpriteAssetKind)
}

func TestGenerateFailedRegenerationPreservesExistingSprite(t *testing.T) {
	t.Parallel()

	db := openPreviewDB(t)
	seedPreviewVideo(t, db)
	mediaRoot := t.TempDir()
	writePreviewFile(t, filepath.Join(mediaRoot, "videos", "vid-1.mp4"), "video")
	runner := &fakeRunner{body: "original"}
	video := Video{ID: "vid-1", MediaPath: "videos/vid-1.mp4", DurationSeconds: 25}
	if _, err := GenerateAndStore(context.Background(), db, Config{MediaRoot: mediaRoot, FFmpegPath: "ffmpeg", Runner: runner}, video); err != nil {
		t.Fatal(err)
	}

	runner.body = "corrupt"
	runner.err = errors.New("ffmpeg failed after writing")
	if _, err := GenerateAndStore(context.Background(), db, Config{MediaRoot: mediaRoot, FFmpegPath: "ffmpeg", Runner: runner}, video); err == nil {
		t.Fatal("expected failed regeneration")
	}
	body, err := os.ReadFile(filepath.Join(mediaRoot, "derived", "previews", "vid-1", "sprite.jpg"))
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "original" {
		t.Fatalf("expected failed regeneration to preserve existing sprite, got %q", string(body))
	}
}

func TestGenerateRejectsSymlinkedPreviewDirectory(t *testing.T) {
	t.Parallel()

	db := openPreviewDB(t)
	seedPreviewVideo(t, db)
	mediaRoot := t.TempDir()
	writePreviewFile(t, filepath.Join(mediaRoot, "videos", "vid-1.mp4"), "video")
	outside := t.TempDir()
	if err := os.MkdirAll(filepath.Join(mediaRoot, "derived"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(mediaRoot, "derived", "previews")); err != nil {
		t.Skipf("symlink not supported: %v", err)
	}

	_, err := GenerateAndStore(context.Background(), db, Config{MediaRoot: mediaRoot, FFmpegPath: "ffmpeg", Runner: &fakeRunner{}}, Video{ID: "vid-1", MediaPath: "videos/vid-1.mp4", DurationSeconds: 25})
	if !errors.Is(err, assetpath.ErrInvalid) {
		t.Fatalf("expected symlinked preview directory to be rejected, got %v", err)
	}
}

func TestJobHandlerGeneratesAndStoresPreview(t *testing.T) {
	t.Parallel()

	db := openPreviewDB(t)
	store := jobs.NewStore(db)
	seedPreviewVideo(t, db)
	mediaRoot := t.TempDir()
	writePreviewFile(t, filepath.Join(mediaRoot, "videos", "vid-1.mp4"), "video")
	runner := &fakeRunner{}
	job := enqueuePreviewJob(t, store, "vid-1")
	jobRunner := jobs.NewRunner(store, map[string]jobs.Handler{
		JobType: NewJobHandler(db, Config{MediaRoot: mediaRoot, FFmpegPath: "ffmpeg", Runner: runner, JobStore: store}).Handle,
	})

	if err := jobRunner.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	stored, err := store.Get(context.Background(), job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != jobs.StatusSucceeded {
		t.Fatalf("expected preview job to succeed, got %#v", stored)
	}
	assertPreviewScalar(t, db, "SELECT sprite_path FROM video_previews WHERE video_id = ?", "derived/previews/vid-1/sprite.jpg", "vid-1")
	assertPreviewScalar(t, db, "SELECT count(*) FROM media_assets WHERE owner_type = 'video' AND owner_id = ? AND kind = ?", int64(1), "vid-1", SpriteAssetKind)
	var result Result
	if err := json.Unmarshal([]byte(stored.ResultJSON), &result); err != nil {
		t.Fatal(err)
	}
	if result.VideoID != "vid-1" || result.SpritePath != "derived/previews/vid-1/sprite.jpg" || result.PreviewCount != 3 {
		t.Fatalf("unexpected preview job result: %#v", result)
	}
}

func TestJobHandlerRollsBackPreviewRowsWhenJobCompletionFails(t *testing.T) {
	t.Parallel()

	db := openPreviewDB(t)
	store := jobs.NewStore(db)
	seedPreviewVideo(t, db)
	mediaRoot := t.TempDir()
	writePreviewFile(t, filepath.Join(mediaRoot, "videos", "vid-1.mp4"), "video")
	handler := NewJobHandler(db, Config{MediaRoot: mediaRoot, FFmpegPath: "ffmpeg", Runner: &fakeRunner{}, JobStore: store})

	err := handler.Handle(context.Background(), jobs.Job{ID: "missing-job", PayloadJSON: `{"video_id":"vid-1"}`})
	if !errors.Is(err, jobs.ErrNotFound) {
		t.Fatalf("expected missing job completion error, got %v", err)
	}
	assertPreviewScalar(t, db, "SELECT count(*) FROM video_previews WHERE video_id = ?", int64(0), "vid-1")
	assertPreviewScalar(t, db, "SELECT count(*) FROM media_assets WHERE owner_type = 'video' AND owner_id = ? AND kind = ?", int64(0), "vid-1", SpriteAssetKind)
}

func TestJobHandlerMissingStoreFailsBeforeGeneratingPreview(t *testing.T) {
	t.Parallel()

	db := openPreviewDB(t)
	seedPreviewVideo(t, db)
	mediaRoot := t.TempDir()
	writePreviewFile(t, filepath.Join(mediaRoot, "videos", "vid-1.mp4"), "video")
	runner := &fakeRunner{}
	handler := NewJobHandler(db, Config{MediaRoot: mediaRoot, FFmpegPath: "ffmpeg", Runner: runner})

	err := handler.Handle(context.Background(), jobs.Job{ID: "missing-store", PayloadJSON: `{"video_id":"vid-1"}`})
	if err == nil || !strings.Contains(err.Error(), "missing job store") {
		t.Fatalf("expected missing job store error, got %v", err)
	}
	if len(runner.commands) != 0 {
		t.Fatalf("expected missing store to fail before ffmpeg, got %#v", runner.commands)
	}
	assertPreviewScalar(t, db, "SELECT count(*) FROM video_previews WHERE video_id = ?", int64(0), "vid-1")
}

func TestJobHandlerFailureDoesNotStorePreview(t *testing.T) {
	t.Parallel()

	db := openPreviewDB(t)
	store := jobs.NewStore(db)
	seedPreviewVideo(t, db)
	mediaRoot := t.TempDir()
	writePreviewFile(t, filepath.Join(mediaRoot, "videos", "vid-1.mp4"), "video")
	job := enqueuePreviewJob(t, store, "vid-1")
	jobRunner := jobs.NewRunner(store, map[string]jobs.Handler{
		JobType: NewJobHandler(db, Config{MediaRoot: mediaRoot, FFmpegPath: "ffmpeg", Runner: &fakeRunner{err: errors.New("ffmpeg failed")}, JobStore: store}).Handle,
	})

	if err := jobRunner.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	stored, err := store.Get(context.Background(), job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != jobs.StatusFailed || !strings.Contains(stored.Error, "ffmpeg failed") {
		t.Fatalf("expected failed preview job, got %#v", stored)
	}
	assertPreviewScalar(t, db, "SELECT count(*) FROM video_previews WHERE video_id = ?", int64(0), "vid-1")
}

type fakeRunner struct {
	commands []Command
	err      error
	body     string
}

func (r *fakeRunner) Run(_ context.Context, command Command) error {
	r.commands = append(r.commands, command)
	output := command.Args[len(command.Args)-1]
	if err := os.MkdirAll(filepath.Dir(output), 0o755); err != nil {
		return err
	}

	body := r.body
	if body == "" {
		body = "sprite"
	}
	if err := os.WriteFile(output, []byte(body), 0o644); err != nil {
		return err
	}

	return r.err
}

func argsContainSequence(args []string, sequence ...string) bool {
	for i := 0; i+len(sequence) <= len(args); i++ {
		matched := true
		for j, value := range sequence {
			if args[i+j] != value {
				matched = false
				break
			}
		}
		if matched {
			return true
		}
	}

	return false
}

func openPreviewDB(t *testing.T) *sql.DB {
	t.Helper()

	db, err := database.Open(context.Background(), filepath.Join(t.TempDir(), "kapsel.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := database.Migrate(context.Background(), db); err != nil {
		t.Fatal(err)
	}

	return db
}

func writePreviewFile(t *testing.T, path string, body string) {
	t.Helper()

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func seedPreviewVideo(t *testing.T, db *sql.DB) {
	t.Helper()

	_, err := db.Exec("INSERT INTO videos (id, external_id, title, media_path, duration_seconds) VALUES ('vid-1', 'vid-1', 'Video One', 'videos/vid-1.mp4', 25)")
	if err != nil {
		t.Fatal(err)
	}
}

func enqueuePreviewJob(t *testing.T, store *jobs.Store, videoID string) jobs.Job {
	t.Helper()

	payloadJSON, err := json.Marshal(Payload{VideoID: videoID})
	if err != nil {
		t.Fatal(err)
	}
	job, err := store.Enqueue(context.Background(), jobs.EnqueueParams{Type: JobType, PayloadJSON: string(payloadJSON), MaxAttempts: 1, RunAfter: time.Now().Add(-time.Second)})
	if err != nil {
		t.Fatal(err)
	}

	return job
}

func assertPreviewScalar[T comparable](t *testing.T, db *sql.DB, query string, expected T, args ...any) {
	t.Helper()

	var got T
	if err := db.QueryRow(query, args...).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != expected {
		t.Fatalf("expected %v, got %v", expected, got)
	}
}
