package download

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"kapsel/internal/database"
	"kapsel/internal/diskspace"
	"kapsel/internal/jobs"
	"kapsel/internal/previews"
	"kapsel/internal/search"
)

func TestBuildCommand(t *testing.T) {
	t.Parallel()

	downloader := NewDownloader(nil, Config{YTDLPPath: "yt-dlp", MediaRoot: "/archive/media", SubtitlesEnabled: true}, nil)
	command, err := downloader.BuildCommand("https://www.youtube.com/watch?v=abc123DEF45")
	if err != nil {
		t.Fatal(err)
	}

	if command.Name != "yt-dlp" {
		t.Fatalf("expected command name %q, got %q", "yt-dlp", command.Name)
	}
	if command.Dir != "/archive/media" {
		t.Fatalf("expected command working directory %q, got %q", "/archive/media", command.Dir)
	}
	for _, arg := range []string{"--no-playlist", "--no-simulate", "--progress", "--check-formats", "--dump-single-json", "--write-info-json", "--write-thumbnail", "--paths", "/archive/media", "https://www.youtube.com/watch?v=abc123DEF45"} {
		if !slices.Contains(command.Args, arg) {
			t.Fatalf("expected args to contain %q: %#v", arg, command.Args)
		}
	}
	for _, flag := range []string{"--write-subs", "--sub-langs", "--convert-subs", "vtt"} {
		if !slices.Contains(command.Args, flag) {
			t.Fatalf("expected subtitle arg %q in %#v", flag, command.Args)
		}
	}
	if slices.Contains(command.Args, "--write-auto-subs") {
		t.Fatalf("expected subtitle downloads to skip auto-translated captions: %#v", command.Args)
	}
	assertArgValue(t, command.Args, "--sub-langs", "all")
	if command.MaxStdoutBytes != 0 {
		t.Fatalf("expected download metadata stdout to be uncapped, got %d", command.MaxStdoutBytes)
	}
	assertArgValue(t, command.Args, "--format", DefaultFormatSelector)
	assertArgValue(t, command.Args, "--merge-output-format", "mp4")
}

func TestBuildOriginalAutomaticSubtitleCommand(t *testing.T) {
	t.Parallel()

	downloader := NewDownloader(nil, Config{YTDLPPath: "yt-dlp", MediaRoot: "/archive/media"}, nil)
	command, err := downloader.BuildOriginalAutomaticSubtitleCommand("https://www.youtube.com/watch?v=abc123DEF45")
	if err != nil {
		t.Fatal(err)
	}

	for _, arg := range []string{"--no-playlist", "--no-simulate", "--skip-download", "--dump-single-json", "--write-auto-subs", "--sub-langs", "--convert-subs", "vtt", "--paths", "/archive/media", "https://www.youtube.com/watch?v=abc123DEF45"} {
		if !slices.Contains(command.Args, arg) {
			t.Fatalf("expected args to contain %q: %#v", arg, command.Args)
		}
	}
	if slices.Contains(command.Args, "--write-subs") {
		t.Fatalf("expected automatic subtitle command to skip manual subtitle requests: %#v", command.Args)
	}
	assertArgValue(t, command.Args, "--sub-langs", ".*-orig")
	assertArgValue(t, command.Args, "--output", "%(id)s.%(ext)s")
}

func TestBuildCommandsUseConfiguredCookiesFile(t *testing.T) {
	t.Parallel()

	downloader := NewDownloader(nil, Config{YTDLPPath: "yt-dlp", MediaRoot: "/archive/media", YTDLPCookiesFile: "/etc/kapsel/youtube.cookies.txt", SubtitlesEnabled: true}, nil)
	commands := []Command{}
	video, err := downloader.BuildCommand("https://www.youtube.com/watch?v=abc123DEF45")
	if err != nil {
		t.Fatal(err)
	}
	commands = append(commands, video)
	subtitles, err := downloader.BuildOriginalAutomaticSubtitleCommand("https://www.youtube.com/watch?v=abc123DEF45")
	if err != nil {
		t.Fatal(err)
	}
	commands = append(commands, subtitles)
	channel, err := downloader.BuildChannelCatalogPageCommand("https://www.youtube.com/@archive", 1, 30)
	if err != nil {
		t.Fatal(err)
	}
	commands = append(commands, channel)

	for _, command := range commands {
		if !slices.Contains(command.Args, "--ignore-config") {
			t.Fatalf("expected yt-dlp command to disable implicit config loading: %#v", command.Args)
		}
		assertArgValue(t, command.Args, "--cookies", "/etc/kapsel/youtube.cookies.txt")
	}
}

func TestBuildCommandsResolveRelativeSandboxPaths(t *testing.T) {
	root := t.TempDir()
	t.Chdir(root)
	mediaRoot := filepath.Join("data", "media")
	cookiesFile := filepath.Join("secrets", "youtube.cookies.txt")
	wantMediaRoot := filepath.Join(root, mediaRoot)
	wantCookiesFile := filepath.Join(root, cookiesFile)
	downloader := NewDownloader(nil, Config{YTDLPPath: "yt-dlp", MediaRoot: mediaRoot, YTDLPCookiesFile: cookiesFile, SubtitlesEnabled: true}, nil)

	video, err := downloader.BuildCommand("https://www.youtube.com/watch?v=abc123DEF45")
	if err != nil {
		t.Fatal(err)
	}
	subtitles, err := downloader.BuildOriginalAutomaticSubtitleCommand("https://www.youtube.com/watch?v=abc123DEF45")
	if err != nil {
		t.Fatal(err)
	}
	channel, err := downloader.BuildChannelCatalogPageCommand("https://www.youtube.com/@archive", 1, 30)
	if err != nil {
		t.Fatal(err)
	}

	for _, command := range []Command{video, subtitles, channel} {
		if command.Dir != wantMediaRoot {
			t.Fatalf("expected absolute command working directory %q, got %q", wantMediaRoot, command.Dir)
		}
		assertArgValue(t, command.Args, "--cookies", wantCookiesFile)
		if len(command.Access.ReadOnly) != 1 || command.Access.ReadOnly[0].Path != wantCookiesFile {
			t.Fatalf("expected cookies read grant %q, got %#v", wantCookiesFile, command.Access.ReadOnly)
		}
	}
	for _, command := range []Command{video, subtitles} {
		assertArgValue(t, command.Args, "--paths", wantMediaRoot)
		if len(command.Access.ReadWrite) != 1 || command.Access.ReadWrite[0].Path != wantMediaRoot {
			t.Fatalf("expected media write grant %q, got %#v", wantMediaRoot, command.Access.ReadWrite)
		}
	}
	if len(channel.Access.ReadWrite) != 0 {
		t.Fatalf("expected channel catalog command to skip media write grant, got %#v", channel.Access.ReadWrite)
	}
}

func TestBuildVideoMetadataScanCommand(t *testing.T) {
	t.Parallel()

	downloader := NewDownloader(nil, Config{YTDLPPath: "yt-dlp", MediaRoot: "/archive/media"}, nil)
	command, err := downloader.BuildVideoMetadataScanCommand("https://www.youtube.com/watch?v=abc123DEF45")
	if err != nil {
		t.Fatal(err)
	}

	for _, arg := range []string{"--skip-download", "--dump-single-json", "https://www.youtube.com/watch?v=abc123DEF45"} {
		if !slices.Contains(command.Args, arg) {
			t.Fatalf("expected metadata scan arg %q in %#v", arg, command.Args)
		}
	}
	for _, forbidden := range []string{"--write-subs", "--write-auto-subs", "--write-thumbnail", "--write-info-json"} {
		if slices.Contains(command.Args, forbidden) {
			t.Fatalf("metadata scan must not %q: %#v", forbidden, command.Args)
		}
	}
}

func TestHandleVideoMetadataScanUpsertsCatalogRow(t *testing.T) {
	t.Parallel()

	db := openDownloadDB(t)
	store := jobs.NewStore(db)
	downloader := newTestDownloader(db, store, Config{YTDLPPath: "yt-dlp", MediaRoot: t.TempDir()}, fakeRunner{stdout: metadataScanFixture})

	if _, err := store.Enqueue(context.Background(), jobs.EnqueueParams{
		Type:        VideoMetadataScanJobType,
		PayloadJSON: `{"url":"https://www.youtube.com/watch?v=abc123DEF45"}`,
		MaxAttempts: 1,
		RunAfter:    time.Now().Add(-time.Second),
	}); err != nil {
		t.Fatal(err)
	}
	runner := jobs.NewRunner(store, map[string]jobs.Handler{
		VideoMetadataScanJobType: downloader.HandleVideoMetadataScan,
	})
	if err := runner.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}

	var id, title, channelID string
	var mediaPath any
	if err := db.QueryRow("SELECT id, title, channel_id, media_path FROM videos WHERE external_id = 'abc123DEF45'").Scan(&id, &title, &channelID, &mediaPath); err != nil {
		t.Fatal(err)
	}
	if title != "Kapsel Demo" || channelID != "chan-1" {
		t.Fatalf("unexpected catalog row: id=%s title=%q channel=%q", id, title, channelID)
	}
	if mediaPath != nil && mediaPath != "" {
		t.Fatalf("expected metadata scan to leave media_path empty, got %v", mediaPath)
	}
}

func TestBuildCommandsIgnoreImplicitYTDLPConfig(t *testing.T) {
	t.Parallel()

	downloader := NewDownloader(nil, Config{YTDLPPath: "yt-dlp", MediaRoot: "/archive/media"}, nil)
	commands := []Command{}
	video, err := downloader.BuildCommand("https://www.youtube.com/watch?v=abc123DEF45")
	if err != nil {
		t.Fatal(err)
	}
	commands = append(commands, video)
	subtitles, err := downloader.BuildOriginalAutomaticSubtitleCommand("https://www.youtube.com/watch?v=abc123DEF45")
	if err != nil {
		t.Fatal(err)
	}
	commands = append(commands, subtitles)
	channel, err := downloader.BuildChannelCatalogPageCommand("https://www.youtube.com/@archive", 1, 30)
	if err != nil {
		t.Fatal(err)
	}
	commands = append(commands, channel)

	for _, command := range commands {
		if !slices.Contains(command.Args, "--ignore-config") {
			t.Fatalf("expected yt-dlp command to disable implicit config loading: %#v", command.Args)
		}
	}
}

func TestYTDLPPacingSleepsBetweenConsecutiveCommands(t *testing.T) {
	t.Parallel()

	sleeps := []time.Duration{}
	runner := &sequenceRunner{stdout: [][]byte{[]byte("{}"), []byte("{}")}}
	downloader := NewDownloader(nil, Config{
		YTDLPPath:          "yt-dlp",
		YTDLPSleepInterval: 10 * time.Second,
		ytdlpSleepJitter:   func(time.Duration) time.Duration { return 12 * time.Second },
		ytdlpSleep:         func(_ context.Context, delay time.Duration) error { sleeps = append(sleeps, delay); return nil },
		ytdlpNow:           func() time.Time { return time.Unix(0, 0) },
	}, runner)

	if _, err := downloader.runYTDLP(context.Background(), Command{Name: "yt-dlp"}); err != nil {
		t.Fatal(err)
	}
	if _, err := downloader.runYTDLP(context.Background(), Command{Name: "yt-dlp"}); err != nil {
		t.Fatal(err)
	}

	if !slices.Equal(sleeps, []time.Duration{12 * time.Second}) {
		t.Fatalf("expected one randomized pacing sleep before the second command, got %#v", sleeps)
	}
	if len(runner.commands) != 2 {
		t.Fatalf("expected both yt-dlp commands to run, got %d", len(runner.commands))
	}
}

func TestYTDLPRetryDelayClassifiesAuthChallenges(t *testing.T) {
	t.Parallel()

	if got := ytdlpRetryDelay([]byte("ERROR: network reset"), errors.New("exit status 1")); got != DefaultYTDLPRetryDelay {
		t.Fatalf("expected default yt-dlp retry delay %s, got %s", DefaultYTDLPRetryDelay, got)
	}
	if got := ytdlpRetryDelay([]byte("ERROR: Sign in to confirm your age"), errors.New("exit status 1")); got != DefaultYTDLPAuthRetryDelay {
		t.Fatalf("expected auth challenge retry delay %s, got %s", DefaultYTDLPAuthRetryDelay, got)
	}
}

func TestParseYTDLPDownloadProgress(t *testing.T) {
	t.Parallel()

	tests := []struct {
		line string
		want float64
		ok   bool
	}{
		{line: "[download]   0.0% of 10.00MiB at 1.00MiB/s ETA 00:10", want: 0, ok: true},
		{line: "[download]  42.5% of 10.00MiB at 1.00MiB/s ETA 00:05", want: 0.425, ok: true},
		{line: "[download] 100% of 10.00MiB in 00:01", want: 1, ok: true},
		{line: "[download] Destination: My 100% Video.mp4", ok: false},
		{line: "[info] Writing video metadata as JSON", ok: false},
	}

	for _, test := range tests {
		got, ok := parseYTDLPDownloadProgress(test.line)
		if ok != test.ok || got != test.want {
			t.Fatalf("parseYTDLPDownloadProgress(%q) = %v, %v; want %v, %v", test.line, got, ok, test.want, test.ok)
		}
	}
}

func TestProgressWriterCapsPerStreamDownloadProgress(t *testing.T) {
	t.Parallel()

	var progress []float64
	writer := progressWriter{progress: func(value float64) error {
		progress = append(progress, value)
		return nil
	}}
	_, err := writer.Write([]byte("[download]  80.0% of 10.00MiB\n[download] 100.0% of 10.00MiB\n[download]   5.0% of 1.00MiB\n[download]  20.0% of 1.00MiB\n"))
	if err != nil {
		t.Fatal(err)
	}

	if !slices.Equal(progress, []float64{0.8, maxInFlightDownloadProgress}) {
		t.Fatalf("expected progress to cap before stream reset, got %#v", progress)
	}
}

func TestProgressWriterFlushesPendingProgressLine(t *testing.T) {
	t.Parallel()

	var progress []float64
	writer := progressWriter{progress: func(value float64) error {
		progress = append(progress, value)
		return nil
	}}
	if _, err := writer.Write([]byte("[download]  42.0% of 10.00MiB")); err != nil {
		t.Fatal(err)
	}
	writer.Flush()

	if !slices.Equal(progress, []float64{0.42}) {
		t.Fatalf("expected pending progress line to flush, got %#v", progress)
	}
}

func TestStdoutProgressWriterFiltersProgressAndPreservesJSON(t *testing.T) {
	t.Parallel()

	stdout := limitedBuffer{}
	var progress []float64
	tracker := progressWriter{progress: func(value float64) error {
		progress = append(progress, value)
		return nil
	}}
	writer := stdoutProgressWriter{buffer: &stdout, progress: &tracker}

	if _, err := writer.Write([]byte("[download]  42.0% of 10.00MiB at 1.00MiB/s ETA 00:05\n")); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Write([]byte("{\"id\":\"vid-1\"}")); err != nil {
		t.Fatal(err)
	}
	writer.Flush()

	if stdout.String() != `{"id":"vid-1"}` {
		t.Fatalf("expected clean JSON stdout, got %q", stdout.String())
	}
	if !slices.Equal(progress, []float64{0.42}) {
		t.Fatalf("expected stdout progress to be recorded, got %#v", progress)
	}
}

func TestStdoutProgressWriterHandlesChunkedProgressLine(t *testing.T) {
	t.Parallel()

	stdout := limitedBuffer{}
	var progress []float64
	tracker := progressWriter{progress: func(value float64) error {
		progress = append(progress, value)
		return nil
	}}
	writer := stdoutProgressWriter{buffer: &stdout, progress: &tracker}

	if _, err := writer.Write([]byte("[download]  4")); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Write([]byte("2.0% of 10.00MiB\r{\"id\":\"vid-1\"}\n")); err != nil {
		t.Fatal(err)
	}
	writer.Flush()

	if stdout.String() != "{\"id\":\"vid-1\"}\n" {
		t.Fatalf("expected clean JSON stdout, got %q", stdout.String())
	}
	if !slices.Equal(progress, []float64{0.42}) {
		t.Fatalf("expected chunked progress to be recorded, got %#v", progress)
	}
}

func TestStdoutProgressWriterFiltersProgressWithoutCallback(t *testing.T) {
	t.Parallel()

	stdout := limitedBuffer{}
	tracker := progressWriter{}
	writer := stdoutProgressWriter{buffer: &stdout, progress: &tracker}

	if _, err := writer.Write([]byte("[download]  42.0% of 10.00MiB\n{\"id\":\"vid-1\"}")); err != nil {
		t.Fatal(err)
	}
	writer.Flush()

	if stdout.String() != `{"id":"vid-1"}` {
		t.Fatalf("expected clean JSON stdout without progress callback, got %q", stdout.String())
	}
}

func TestStdoutProgressWriterFiltersYTDLPStatusLines(t *testing.T) {
	t.Parallel()

	stdout := limitedBuffer{}
	var progress []float64
	tracker := progressWriter{progress: func(value float64) error {
		progress = append(progress, value)
		return nil
	}}
	writer := stdoutProgressWriter{buffer: &stdout, progress: &tracker}

	if _, err := writer.Write([]byte("[download] Destination: vid-1.f137.mp4\n[info] Writing video metadata as JSON\n[Merger] Merging formats into vid-1.mp4\n[MoveFiles] Moving file to vid-1.mp4\n[download]  42.0% of 10.00MiB\n{\"id\":\"vid-1\"}")); err != nil {
		t.Fatal(err)
	}
	writer.Flush()

	if stdout.String() != `{"id":"vid-1"}` {
		t.Fatalf("expected clean JSON stdout after status lines, got %q", stdout.String())
	}
	if !slices.Equal(progress, []float64{0.42}) {
		t.Fatalf("expected only percentage status to record progress, got %#v", progress)
	}
}

func TestExecRunnerKeepsUncappedOutputOnError(t *testing.T) {
	const helperArg = "exec-runner-uncapped-stdout-helper"
	const stdoutTailMarker = "uncapped-stdout-tail-marker"
	const stderrTailMarker = "uncapped-stderr-tail-marker"
	if len(os.Args) > 0 && os.Args[len(os.Args)-1] == helperArg {
		_, _ = os.Stdout.Write([]byte(`{"id":"vid-1","title":"` + strings.Repeat("x", maxErrorOutput) + stdoutTailMarker + `","requested_downloads":[{"filepath":"vid-1.mp4"}]}`))
		_, _ = os.Stderr.Write([]byte(strings.Repeat("y", maxErrorOutput) + stderrTailMarker))
		os.Exit(1)
	}
	t.Parallel()

	output, err := ExecRunner{}.Run(context.Background(), Command{
		Name: os.Args[0],
		Args: []string{"-test.run=TestExecRunnerKeepsUncappedOutputOnError", "--", helperArg},
	})

	if err == nil {
		t.Fatal("expected helper command to fail")
	}
	text := string(output)
	if !strings.Contains(text, stdoutTailMarker) || !strings.Contains(text, stderrTailMarker) {
		t.Fatalf("expected uncapped stdout and stderr to include tail markers, got %d bytes", len(output))
	}
}

func TestExecRunnerMinimizesEnvironmentAndSetsWorkdir(t *testing.T) {
	const helperArg = "exec-runner-env-helper"
	if len(os.Args) > 0 && os.Args[len(os.Args)-1] == helperArg {
		cwd, err := os.Getwd()
		if err != nil {
			_, _ = os.Stderr.WriteString(err.Error())
			os.Exit(1)
		}
		_, _ = os.Stdout.WriteString("kapsel=" + os.Getenv("KAPSEL_SESSION_SECRET") + "\n")
		_, _ = os.Stdout.WriteString("custom=" + os.Getenv("SHOULD_NOT_LEAK") + "\n")
		_, _ = os.Stdout.WriteString("tmp=" + os.Getenv("TMPDIR") + "\n")
		_, _ = os.Stdout.WriteString("cwd=" + cwd + "\n")
		os.Exit(0)
	}
	t.Setenv("KAPSEL_SESSION_SECRET", "top-secret")
	t.Setenv("SHOULD_NOT_LEAK", "top-secret")
	workdir := t.TempDir()

	output, err := ExecRunner{}.Run(context.Background(), Command{
		Name: os.Args[0],
		Args: []string{"-test.run=TestExecRunnerMinimizesEnvironmentAndSetsWorkdir", "--", helperArg},
		Dir:  workdir,
	})
	if err != nil {
		t.Fatal(err)
	}
	text := string(output)
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

func TestBuildCommandUsesConfiguredFormatSelector(t *testing.T) {
	t.Parallel()

	downloader := NewDownloader(nil, Config{YTDLPPath: "yt-dlp", MediaRoot: "/archive/media", FormatSelector: "best[height<=480]"}, nil)
	command, err := downloader.BuildCommand("https://www.youtube.com/watch?v=abc123DEF45")
	if err != nil {
		t.Fatal(err)
	}

	assertArgValue(t, command.Args, "--format", "best[height<=480]")
}

func TestBuildCommandRejectsUnsupportedURLScheme(t *testing.T) {
	t.Parallel()

	downloader := NewDownloader(nil, Config{YTDLPPath: "yt-dlp", MediaRoot: "/archive/media"}, nil)
	if _, err := downloader.BuildCommand("file:///etc/passwd"); !errors.Is(err, ErrUnsupportedURLScheme) {
		t.Fatalf("expected unsupported scheme error, got %v", err)
	}
}

func TestBuildCommandRejectsNonYouTubeURL(t *testing.T) {
	t.Parallel()

	downloader := NewDownloader(nil, Config{YTDLPPath: "yt-dlp", MediaRoot: "/archive/media"}, nil)
	if _, err := downloader.BuildCommand("https://example.com/watch?v=abc"); !errors.Is(err, ErrUnsupportedVideoURL) {
		t.Fatalf("expected unsupported video URL error, got %v", err)
	}
}

func TestNormalizeDirectVideoURLNormalizesYouTubeVideoVariants(t *testing.T) {
	t.Parallel()

	for _, rawURL := range []string{
		" https://www.youtube.com/shorts/abc123DEF45?feature=share ",
		"https://www.youtube.com/live/abc123DEF45",
		"https://www.youtube.com/v/abc123DEF45",
		"https://youtu.be/abc123DEF45?si=share",
	} {
		videoURL, err := NormalizeDirectVideoURL(rawURL)
		if err != nil {
			t.Fatalf("expected %q to normalize: %v", rawURL, err)
		}
		if videoURL != "https://www.youtube.com/watch?v=abc123DEF45" {
			t.Fatalf("expected normalized watch URL for %q, got %q", rawURL, videoURL)
		}
	}
}

func TestNormalizeDirectVideoURLRejectsYouTubeNonVideoURLs(t *testing.T) {
	t.Parallel()

	for _, rawURL := range []string{
		"https://www.youtube.com/@archive",
		"https://www.youtube.com/playlist?list=PLabc",
		"https://www.youtube.com/feed/subscriptions",
		"https://youtu.be/playlist?list=PLabc",
		"https://youtu.be/feed",
	} {
		if _, err := NormalizeDirectVideoURL(rawURL); !errors.Is(err, ErrUnsupportedVideoURL) {
			t.Fatalf("expected unsupported video URL error for %q, got %v", rawURL, err)
		}
	}
}

func TestNormalizeDirectVideoURLRejectsNonYouTubeHTTPURLs(t *testing.T) {
	t.Parallel()

	for _, rawURL := range []string{
		"https://example.com/watch?v=abc",
		"http://127.0.0.1:8080/video.mp4",
		"https://metadata.google.internal/computeMetadata/v1/",
	} {
		if _, err := NormalizeDirectVideoURL(rawURL); !errors.Is(err, ErrUnsupportedVideoURL) {
			t.Fatalf("expected unsupported video URL error for %q, got %v", rawURL, err)
		}
	}
}

func TestNormalizeDownloadURLPreservesRequiredURLError(t *testing.T) {
	t.Parallel()

	if _, err := NormalizeDownloadURL("   "); !errors.Is(err, ErrDownloadURLRequired) {
		t.Fatalf("expected required URL error, got %v", err)
	}
}

func TestNormalizeChannelURLRejectsNonChannelURLs(t *testing.T) {
	t.Parallel()

	for _, rawURL := range []string{
		"https://example.com/@archive",
		"https://www.youtube.com/watch?v=abc",
		"https://www.youtube.com/@",
		"https://youtu.be/abc",
	} {
		if _, err := NormalizeChannelURL(rawURL); !errors.Is(err, ErrUnsupportedChannelURL) {
			t.Fatalf("expected unsupported channel URL error for %q, got %v", rawURL, err)
		}
	}
}

func TestNormalizeChannelURLCanonicalizesEquivalentURLs(t *testing.T) {
	t.Parallel()

	for _, rawURL := range []string{
		"https://www.youtube.com/channel/chan-1",
		"https://www.youtube.com/channel/chan-1/",
		"https://www.youtube.com/channel/chan-1/videos",
		"https://WWW.YOUTUBE.COM/channel/chan-1?view=videos#featured",
		"http://youtube.com/channel/chan-1",
	} {
		got, err := NormalizeChannelURL(rawURL)
		if err != nil {
			t.Fatalf("NormalizeChannelURL(%q): %v", rawURL, err)
		}
		if got != "https://www.youtube.com/channel/chan-1" {
			t.Fatalf("expected canonical channel URL, got %q", got)
		}
	}

	got, err := NormalizeChannelURL("https://www.youtube.com/@archive/videos")
	if err != nil {
		t.Fatal(err)
	}
	if got != "https://www.youtube.com/@archive" {
		t.Fatalf("expected canonical handle URL, got %q", got)
	}
}

func TestFirstChannelVideoURLRejectsNonYouTubeResolvedURL(t *testing.T) {
	t.Parallel()

	_, err := firstChannelVideoURL([]byte(`{"entries":[{"webpage_url":"https://example.com/watch?v=abc"}]}`))
	if !errors.Is(err, ErrUnsupportedChannelURL) {
		t.Fatalf("expected unsupported channel URL error, got %v", err)
	}
}

func TestFirstChannelVideoURLNormalizesYouTubeShortURL(t *testing.T) {
	t.Parallel()

	videoURL, err := firstChannelVideoURL([]byte(`{"entries":[{"url":"https://youtu.be/abc"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if videoURL != "https://www.youtube.com/watch?v=abc" {
		t.Fatalf("expected normalized watch URL, got %q", videoURL)
	}
}

func TestFirstChannelVideoURLFindsNestedTabVideo(t *testing.T) {
	t.Parallel()

	videoURL, err := firstChannelVideoURL([]byte(`{"entries":[{"id":"UCRGEkGQVIwucHEsxGS0koyw","_type":"playlist","webpage_url":"https://www.youtube.com/@Nyanners_Clips/videos","entries":[{"id":"-DO4NxnKoLM","_type":"url","url":"https://www.youtube.com/watch?v=-DO4NxnKoLM"}]}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if videoURL != "https://www.youtube.com/watch?v=-DO4NxnKoLM" {
		t.Fatalf("expected nested video URL, got %q", videoURL)
	}
}

func TestBuildChannelFirstCommand(t *testing.T) {
	t.Parallel()

	downloader := NewDownloader(nil, Config{YTDLPPath: "yt-dlp", MediaRoot: "/archive/media"}, nil)
	command, err := downloader.BuildChannelFirstCommand("https://www.youtube.com/@archive")
	if err != nil {
		t.Fatal(err)
	}

	if command.Name != "yt-dlp" {
		t.Fatalf("expected command name %q, got %q", "yt-dlp", command.Name)
	}
	for _, arg := range []string{"--flat-playlist", "--playlist-end", "500", "--dump-single-json", "https://www.youtube.com/@archive/videos"} {
		if !slices.Contains(command.Args, arg) {
			t.Fatalf("expected args to contain %q: %#v", arg, command.Args)
		}
	}
	assertArgValue(t, command.Args, "--extractor-args", "youtubetab:approximate_date")
	if command.MaxStdoutBytes <= 0 {
		t.Fatalf("expected channel metadata stdout to be bounded, got %d", command.MaxStdoutBytes)
	}
}

func TestBuildChannelCatalogPageCommandBoundsPageRange(t *testing.T) {
	t.Parallel()

	downloader := NewDownloader(nil, Config{YTDLPPath: "yt-dlp", MediaRoot: "/archive/media"}, nil)
	command, err := downloader.BuildChannelCatalogPageCommand("https://www.youtube.com/@archive", 31, 60)
	if err != nil {
		t.Fatal(err)
	}

	assertArgValue(t, command.Args, "--playlist-start", "31")
	assertArgValue(t, command.Args, "--playlist-end", "60")
	if !slices.Contains(command.Args, "https://www.youtube.com/@archive/videos") {
		t.Fatalf("expected command to target channel videos tab, got %#v", command.Args)
	}
}

func TestCatalogPublishedAtUsesApproximateTimestamp(t *testing.T) {
	t.Parallel()

	timestamp := time.Date(2026, 5, 8, 15, 30, 0, 0, time.UTC).Unix()
	if got := catalogPublishedAt(channelEntry{Timestamp: timestamp}); got != "2026-05-08" {
		t.Fatalf("expected approximate timestamp date, got %q", got)
	}
}

func TestBuildChannelFirstCommandTargetsVideosTab(t *testing.T) {
	t.Parallel()

	downloader := NewDownloader(nil, Config{YTDLPPath: "yt-dlp", MediaRoot: "/archive/media"}, nil)
	for _, test := range []struct {
		rawURL string
		want   string
	}{
		{rawURL: "https://www.youtube.com/@Nyanners_Clips", want: "https://www.youtube.com/@Nyanners_Clips/videos"},
		{rawURL: "https://www.youtube.com/@Nyanners_Clips/videos", want: "https://www.youtube.com/@Nyanners_Clips/videos"},
		{rawURL: "https://www.youtube.com/channel/UCRGEkGQVIwucHEsxGS0koyw", want: "https://www.youtube.com/channel/UCRGEkGQVIwucHEsxGS0koyw/videos"},
	} {
		command, err := downloader.BuildChannelFirstCommand(test.rawURL)
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Contains(command.Args, test.want) {
			t.Fatalf("expected %q command to target %q, got %#v", test.rawURL, test.want, command.Args)
		}
	}
}

func TestCheckYTDLPValidVersionOutput(t *testing.T) {
	t.Parallel()

	runner := &sequenceRunner{stdout: [][]byte{[]byte("2026.03.17\n")}}
	status := CheckYTDLP(context.Background(), "/opt/bin/yt-dlp", runner)

	if !status.Available {
		t.Fatalf("expected yt-dlp to be available, got %#v", status)
	}
	if status.Path != "/opt/bin/yt-dlp" || status.Version != "2026.03.17" || status.Error != "" {
		t.Fatalf("unexpected yt-dlp status: %#v", status)
	}
	if len(runner.commands) != 1 || runner.commands[0].Name != "/opt/bin/yt-dlp" || !slices.Equal(runner.commands[0].Args, []string{"--version"}) || runner.commands[0].MaxStdoutBytes == 0 {
		t.Fatalf("expected bounded version command, got %#v", runner.commands)
	}
}

func TestCheckYTDLPMissingExecutable(t *testing.T) {
	t.Parallel()

	status := CheckYTDLP(context.Background(), "/missing/yt-dlp", fakeRunner{err: exec.ErrNotFound})

	if status.Available {
		t.Fatalf("expected missing yt-dlp to be unavailable, got %#v", status)
	}
	if !strings.Contains(status.Error, "yt-dlp unavailable") || !strings.Contains(status.Error, "/missing/yt-dlp") {
		t.Fatalf("expected missing executable diagnostic, got %#v", status)
	}
}

func TestCheckYTDLPFailingExecutable(t *testing.T) {
	t.Parallel()

	status := CheckYTDLP(context.Background(), "yt-dlp", fakeRunner{
		stdout: []byte("ERROR: failed for https://example.com/watch?v=abc&token=secret\nAuthorization: Bearer supersecret\ntoken=plain-secret api-key: header-secret\n\"password\": \"json-secret\"\nsecret = spaced-secret\ntoken: Bearer very-secret-value\npassword: correct horse battery staple\n" + strings.Repeat("x", maxDiagnosticLength+100)),
		err:    errors.New("exit status 1"),
	})

	if status.Available {
		t.Fatalf("expected failing yt-dlp to be unavailable, got %#v", status)
	}
	if !strings.Contains(status.Error, "yt-dlp command failed") || !strings.Contains(status.Error, "https://example.com/watch") {
		t.Fatalf("expected failing executable diagnostic, got %#v", status)
	}
	if strings.Contains(status.Error, "token=secret") {
		t.Fatalf("expected secret query values to be redacted, got %#v", status)
	}
	if !strings.Contains(status.Error, "[truncated]") {
		t.Fatalf("expected public yt-dlp status error to be truncated, got %#v", status)
	}
	for _, secret := range []string{"supersecret", "plain-secret", "header-secret", "json-secret", "spaced-secret", "very-secret-value", "horse battery"} {
		if strings.Contains(status.Error, secret) {
			t.Fatalf("expected common secret patterns to be redacted, got %#v", status)
		}
	}
}

func TestCheckYTDLPTimeout(t *testing.T) {
	t.Parallel()

	status := checkYTDLP(context.Background(), "yt-dlp", timeoutRunner{}, time.Millisecond)

	if status.Available {
		t.Fatalf("expected timed out yt-dlp to be unavailable, got %#v", status)
	}
	if !strings.Contains(status.Error, "timed out") {
		t.Fatalf("expected timeout diagnostic, got %#v", status)
	}
}

func TestYTDLPCommandErrorPreservesFullMetadataJSON(t *testing.T) {
	t.Parallel()

	tailMarker := "full-diagnostic-tail-marker"
	output := []byte(`{"id":"vid-1","title":"Kapsel Demo","formats":[{"format_id":"sb3"}],"requested_downloads":[{"filepath":"vid-1.mp4"}],"description":"` + strings.Repeat("x", maxDiagnosticLength+100) + tailMarker + `"}` + "\nERROR: subtitle post-processing failed\ntoken=secret")
	err := ytdlpCommandError(Command{Name: "yt-dlp"}, output, errors.New("exit status 1"))

	message := err.Error()
	for _, expected := range []string{"vid-1", "formats", "requested_downloads", "subtitle post-processing failed", tailMarker, "token=[redacted]"} {
		if !strings.Contains(message, expected) {
			t.Fatalf("expected full diagnostic to contain %q, got %q", expected, message)
		}
	}
	if strings.Contains(message, "token=secret") || strings.Contains(message, "[truncated]") {
		t.Fatalf("expected full redacted diagnostic without truncation, got %q", message)
	}
}

func TestYTDLPCommandErrorPreservesMetadataJSONPastLargePrefix(t *testing.T) {
	t.Parallel()

	tailMarker := "large-prefix-tail-marker"
	output := []byte(`{"id":"vid-1","title":"` + strings.Repeat("x", 9000) + `","requested_downloads":[{"filepath":"vid-1.mp4"}],"description":"` + tailMarker + `"}`)
	err := ytdlpCommandError(Command{Name: "yt-dlp"}, output, errors.New("exit status 1"))

	message := err.Error()
	if !strings.Contains(message, "requested_downloads") || !strings.Contains(message, tailMarker) || strings.Contains(message, "[truncated]") {
		t.Fatalf("expected full large metadata JSON diagnostic, got %q", message)
	}
}

func TestYTDLPCommandErrorPreservesIncompleteMetadataLikeJSON(t *testing.T) {
	t.Parallel()

	tailMarker := "incomplete-json-tail-marker"
	output := []byte(`{"id":"vid-1","formats":[` + strings.Repeat("x", maxDiagnosticLength+100) + tailMarker)
	err := ytdlpCommandError(Command{Name: "yt-dlp"}, output, errors.New("exit status 1"))

	message := err.Error()
	if !strings.Contains(message, "formats") || !strings.Contains(message, tailMarker) || strings.Contains(message, "[truncated]") {
		t.Fatalf("expected full incomplete metadata-like JSON diagnostic, got %q", message)
	}
}

func TestDownloadHandlerIngestsFixtureMetadata(t *testing.T) {
	t.Parallel()

	db := openDownloadDB(t)
	mediaRoot := writeDownloadFiles(t, "vid-1.mp4", "vid-1.jpg")
	store := jobs.NewStore(db)
	job, err := store.Enqueue(context.Background(), jobs.EnqueueParams{
		Type:        JobType,
		PayloadJSON: `{"url":"https://www.youtube.com/watch?v=abc123DEF45"}`,
		MaxAttempts: 1,
		RunAfter:    time.Now().Add(-time.Second),
	})
	if err != nil {
		t.Fatal(err)
	}

	runner := jobs.NewRunner(store, map[string]jobs.Handler{
		JobType: newTestDownloader(db, store, Config{YTDLPPath: "yt-dlp", MediaRoot: mediaRoot}, fakeRunner{stdout: fixtureMetadata}).Handle,
	})
	if err := runner.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}

	stored, err := store.Get(context.Background(), job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != jobs.StatusSucceeded {
		t.Fatalf("expected succeeded job, got %#v", stored)
	}

	assertScalar(t, db, "SELECT name FROM channels WHERE id = ?", "Archive Workshop", "chan-1")
	assertScalar(t, db, "SELECT title FROM videos WHERE id = ?", "Kapsel Demo", "vid-1")
	assertScalar(t, db, "SELECT duration_seconds FROM videos WHERE id = ?", int64(120), "vid-1")
	assertScalar(t, db, "SELECT view_count FROM videos WHERE id = ?", int64(1234), "vid-1")
	assertScalar(t, db, "SELECT media_path FROM videos WHERE id = ?", "vid-1.mp4", "vid-1")
	assertScalar(t, db, "SELECT path FROM media_assets WHERE owner_type = 'video' AND owner_id = ? AND kind = 'media'", "vid-1.mp4", "vid-1")
	assertScalar(t, db, "SELECT path FROM media_assets WHERE owner_type = 'video' AND owner_id = ? AND kind = 'thumbnail'", "vid-1.jpg", "vid-1")
	assertScalar(t, db, "SELECT status FROM downloads WHERE video_id = ?", "succeeded", "vid-1")
	assertScalar(t, db, "SELECT origin FROM downloads WHERE video_id = ?", DownloadOriginManual, "vid-1")
	assertScalar(t, db, "SELECT media_origin FROM videos WHERE id = ?", DownloadOriginManual, "vid-1")
	assertScalar(t, db, "SELECT media_downloaded_at <> '' FROM videos WHERE id = ?", int64(1), "vid-1")
}

func TestDownloadHandlerIngestsMetadataWhenYTDLPExitsAfterDownload(t *testing.T) {
	t.Parallel()

	db := openDownloadDB(t)
	mediaRoot := writeDownloadFiles(t, "vid-1.mp4", "vid-1.jpg")
	store := jobs.NewStore(db)
	job := enqueueDownloadJob(t, store)
	output := append([]byte{}, fixtureMetadata...)
	output = append(output, []byte("\nERROR: subtitle post-processing failed")...)
	runnerCalls := &sequenceRunner{
		stdout: [][]byte{output},
		errs:   []error{errors.New("exit status 1")},
	}
	runner := jobs.NewRunner(store, map[string]jobs.Handler{
		JobType: newTestDownloader(db, store, Config{YTDLPPath: "yt-dlp", MediaRoot: mediaRoot}, runnerCalls).Handle,
	})
	if err := runner.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}

	stored, err := store.Get(context.Background(), job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != jobs.StatusSucceeded {
		t.Fatalf("expected post-download yt-dlp exit to keep completed media, got %#v", stored)
	}
	assertScalar(t, db, "SELECT media_path FROM videos WHERE id = ?", "vid-1.mp4", "vid-1")
	assertScalar(t, db, "SELECT status FROM downloads WHERE video_id = ?", "succeeded", "vid-1")
}

func TestDownloadHandlerRejectsTrailingOutputAfterSuccessfulYTDLP(t *testing.T) {
	t.Parallel()

	db := openDownloadDB(t)
	mediaRoot := writeDownloadFiles(t, "vid-1.mp4", "vid-1.jpg")
	store := jobs.NewStore(db)
	job := enqueueDownloadJob(t, store)
	output := append([]byte{}, fixtureMetadata...)
	output = append(output, []byte("\nERROR: unexpected trailing stdout")...)
	runner := jobs.NewRunner(store, map[string]jobs.Handler{
		JobType: newTestDownloader(db, store, Config{YTDLPPath: "yt-dlp", MediaRoot: mediaRoot}, fakeRunner{stdout: output}).Handle,
	})
	if err := runner.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}

	stored, err := store.Get(context.Background(), job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != jobs.StatusFailed || !strings.Contains(stored.Error, "invalid character") {
		t.Fatalf("expected successful yt-dlp output with trailing junk to fail strict parsing, got %#v", stored)
	}
}

func TestDownloadHandlerDelaysBotDetectionRetry(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 5, 10, 12, 0, 0, 0, time.UTC)
	db := openDownloadDB(t)
	store := jobs.NewStore(db)
	job, err := store.Enqueue(context.Background(), jobs.EnqueueParams{
		Type:        JobType,
		PayloadJSON: `{"url":"https://www.youtube.com/watch?v=abc123DEF45"}`,
		MaxAttempts: 3,
		RunAfter:    now.Add(-time.Second),
	})
	if err != nil {
		t.Fatal(err)
	}
	runner := jobs.NewRunner(store, map[string]jobs.Handler{
		JobType: newTestDownloader(db, store, Config{YTDLPPath: "yt-dlp", MediaRoot: t.TempDir()}, fakeRunner{
			stdout: []byte("ERROR: [youtube] abc123DEF45: Sign in to confirm you're not a bot"),
			err:    errors.New("exit status 1"),
		}).Handle,
	})
	runner.Now = func() time.Time { return now }

	if err := runner.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}

	stored, err := store.Get(context.Background(), job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != jobs.StatusQueued || stored.Attempts != 1 {
		t.Fatalf("expected bot failure to be queued for delayed retry, got %#v", stored)
	}
	runAfter, err := time.Parse(time.RFC3339Nano, stored.RunAfter)
	if err != nil {
		t.Fatal(err)
	}
	if !runAfter.Equal(now.Add(DefaultYTDLPAuthRetryDelay)) {
		t.Fatalf("expected bot retry at %s, got %s", now.Add(DefaultYTDLPAuthRetryDelay), runAfter)
	}
}

func TestManualDownloadOriginStaysStickyAfterAutoDownload(t *testing.T) {
	t.Parallel()

	db := openDownloadDB(t)
	mediaRoot := writeDownloadFiles(t, "vid-1.mp4", "vid-1.jpg")
	store := jobs.NewStore(db)
	manual := runDownloadJobWithOutput(t, db, store, mediaRoot, fixtureMetadata)
	if manual.Status != jobs.StatusSucceeded {
		t.Fatalf("expected manual download to succeed, got %#v", manual)
	}
	job, err := store.Enqueue(context.Background(), jobs.EnqueueParams{
		Type:        JobType,
		PayloadJSON: `{"url":"https://www.youtube.com/watch?v=abc123DEF45","origin":"channel_auto"}`,
		MaxAttempts: 1,
		RunAfter:    time.Now().Add(-time.Second),
	})
	if err != nil {
		t.Fatal(err)
	}
	runner := jobs.NewRunner(store, map[string]jobs.Handler{
		JobType: newTestDownloader(db, store, Config{YTDLPPath: "yt-dlp", MediaRoot: mediaRoot}, fakeRunner{stdout: fixtureMetadata}).Handle,
	})
	if err := runner.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	stored, err := store.Get(context.Background(), job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != jobs.StatusSucceeded {
		t.Fatalf("expected auto download to succeed, got %#v", stored)
	}
	assertScalar(t, db, "SELECT origin FROM downloads WHERE video_id = ?", DownloadOriginManual, "vid-1")
	assertScalar(t, db, "SELECT media_origin FROM videos WHERE id = ?", DownloadOriginManual, "vid-1")
}

func TestAutoDownloadOriginMarksMedia(t *testing.T) {
	t.Parallel()

	db := openDownloadDB(t)
	mediaRoot := writeDownloadFiles(t, "vid-1.mp4", "vid-1.jpg")
	store := jobs.NewStore(db)
	job, err := store.Enqueue(context.Background(), jobs.EnqueueParams{
		Type:        JobType,
		PayloadJSON: `{"url":"https://www.youtube.com/watch?v=abc123DEF45","origin":"channel_auto"}`,
		MaxAttempts: 1,
		RunAfter:    time.Now().Add(-time.Second),
	})
	if err != nil {
		t.Fatal(err)
	}
	runner := jobs.NewRunner(store, map[string]jobs.Handler{
		JobType: newTestDownloader(db, store, Config{YTDLPPath: "yt-dlp", MediaRoot: mediaRoot}, fakeRunner{stdout: fixtureMetadata}).Handle,
	})
	if err := runner.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	stored, err := store.Get(context.Background(), job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != jobs.StatusSucceeded {
		t.Fatalf("expected auto download to succeed, got %#v", stored)
	}

	assertScalar(t, db, "SELECT origin FROM downloads WHERE video_id = ?", DownloadOriginChannelAuto, "vid-1")
	assertScalar(t, db, "SELECT media_origin FROM videos WHERE id = ?", DownloadOriginChannelAuto, "vid-1")
	assertScalar(t, db, "SELECT media_downloaded_at <> '' FROM videos WHERE id = ?", int64(1), "vid-1")
}

func TestDownloadHandlerQueuesTimelinePreviewWhenEnabled(t *testing.T) {
	t.Parallel()

	db := openDownloadDB(t)
	mediaRoot := writeDownloadFiles(t, "vid-1.mp4", "vid-1.jpg")
	store := jobs.NewStore(db)
	previewRunner := &fakePreviewRunner{err: errors.New("ffmpeg failed")}
	job, err := store.Enqueue(context.Background(), jobs.EnqueueParams{
		Type:        JobType,
		PayloadJSON: `{"url":"https://www.youtube.com/watch?v=abc123DEF45"}`,
		MaxAttempts: 1,
		RunAfter:    time.Now().Add(-time.Second),
	})
	if err != nil {
		t.Fatal(err)
	}
	runner := jobs.NewRunner(store, map[string]jobs.Handler{
		JobType: newTestDownloader(db, store, Config{YTDLPPath: "yt-dlp", MediaRoot: mediaRoot, PreviewsEnabled: true, FFMPEGPath: "ffmpeg", PreviewRunner: previewRunner}, fakeRunner{stdout: fixtureMetadata}).Handle,
	})

	if err := runner.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	stored, err := store.Get(context.Background(), job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != jobs.StatusSucceeded {
		t.Fatalf("expected succeeded job, got %#v", stored)
	}
	if len(previewRunner.commands) != 0 {
		t.Fatalf("expected preview generation to be deferred, got %#v", previewRunner.commands)
	}
	assertScalar(t, db, "SELECT count(*) FROM video_previews WHERE video_id = ?", int64(0), "vid-1")
	assertScalar(t, db, "SELECT count(*) FROM media_assets WHERE owner_type = 'video' AND owner_id = ? AND kind = ?", int64(0), "vid-1", previews.SpriteAssetKind)
	assertScalar(t, db, "SELECT status FROM jobs WHERE type = ?", jobs.StatusQueued, previews.JobType)
	assertPreviewJobPayload(t, db, "vid-1")
}

func TestDownloadHandlerIngestsSubtitlesAndIndexesTranscript(t *testing.T) {
	t.Parallel()

	db := openDownloadDB(t)
	mediaRoot := writeDownloadFiles(t, "vid-1.mp4", "vid-1.jpg")
	writeDownloadFile(t, mediaRoot, "vid-1.en.vtt", "WEBVTT\n\n00:00:00.000 --> 00:00:02.000\nA quiet lunar capsule floats past the archive.\n")
	store := jobs.NewStore(db)
	stored := runDownloadJobWithOutput(t, db, store, mediaRoot, fixtureMetadataWithSubtitle(t))
	if stored.Status != jobs.StatusSucceeded {
		t.Fatalf("expected succeeded job, got %#v", stored)
	}

	assertScalar(t, db, "SELECT language FROM subtitles WHERE video_id = ?", "en", "vid-1")
	assertScalar(t, db, "SELECT format FROM subtitles WHERE video_id = ?", "vtt", "vid-1")
	assertScalar(t, db, "SELECT path FROM subtitles WHERE video_id = ?", "vid-1.en.vtt", "vid-1")
	results, err := search.Search(context.Background(), db, search.Query{Term: "lunar", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) == 0 || results[0].OwnerType != "subtitle" || results[0].OwnerID != "vid-1" || !strings.Contains(results[0].Snippet, "<mark>lunar</mark>") {
		t.Fatalf("expected highlighted subtitle search result, got %#v", results)
	}
}

func TestDownloadHandlerFetchesOriginalAutomaticSubtitles(t *testing.T) {
	t.Parallel()

	db := openDownloadDB(t)
	mediaRoot := writeDownloadFiles(t, "vid-1.mp4", "vid-1.jpg")
	writeDownloadFile(t, mediaRoot, "vid-1.en-CA.vtt", "WEBVTT\n\n00:00:00.000 --> 00:00:02.000\nManual English captions.\n")
	writeDownloadFile(t, mediaRoot, "vid-1.en-orig.vtt", "WEBVTT\n\n00:00:00.000 --> 00:00:02.000\nOriginal automatic captions.\n")
	store := jobs.NewStore(db)
	job := enqueueDownloadJob(t, store)
	runnerCalls := &sequenceRunner{stdout: [][]byte{
		fixtureMetadataWithSubtitleAndAutomaticOriginal(t),
		fixtureMetadataWithAutomaticOriginalSubtitle(t),
	}}
	runner := jobs.NewRunner(store, map[string]jobs.Handler{
		JobType: newTestDownloader(db, store, Config{YTDLPPath: "yt-dlp", MediaRoot: mediaRoot, SubtitlesEnabled: true}, runnerCalls).Handle,
	})
	if err := runner.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	stored, err := store.Get(context.Background(), job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != jobs.StatusSucceeded {
		t.Fatalf("expected succeeded job, got %#v", stored)
	}
	if len(runnerCalls.commands) != 2 {
		t.Fatalf("expected media and automatic subtitle commands, got %#v", runnerCalls.commands)
	}
	if slices.Contains(runnerCalls.commands[0].Args, "--write-auto-subs") {
		t.Fatalf("expected media command to avoid automatic translations: %#v", runnerCalls.commands[0])
	}
	assertArgValue(t, runnerCalls.commands[1].Args, "--sub-langs", ".*-orig")

	assertScalar(t, db, "SELECT count(*) FROM subtitles WHERE video_id = ?", int64(2), "vid-1")
	assertScalar(t, db, "SELECT path FROM subtitles WHERE video_id = ? AND language = ?", "vid-1.en-CA.vtt", "vid-1", "en-ca")
	assertScalar(t, db, "SELECT path FROM subtitles WHERE video_id = ? AND language = ?", "vid-1.en-orig.vtt", "vid-1", "en-orig")
}

func TestDownloadHandlerKeepsMediaWhenOriginalAutomaticSubtitlesFail(t *testing.T) {
	t.Parallel()

	db := openDownloadDB(t)
	mediaRoot := writeDownloadFiles(t, "vid-1.mp4", "vid-1.jpg")
	writeDownloadFile(t, mediaRoot, "vid-1.en-CA.vtt", "WEBVTT\n\n00:00:00.000 --> 00:00:02.000\nManual English captions.\n")
	store := jobs.NewStore(db)
	job := enqueueDownloadJob(t, store)
	runnerCalls := &sequenceRunner{
		stdout: [][]byte{
			fixtureMetadataWithSubtitleAndAutomaticOriginal(t),
			[]byte("ERROR: Unable to download video subtitles for 'en-orig': HTTP Error 429: Too Many Requests"),
		},
		errs: []error{nil, errors.New("exit status 1")},
	}
	runner := jobs.NewRunner(store, map[string]jobs.Handler{
		JobType: newTestDownloader(db, store, Config{YTDLPPath: "yt-dlp", MediaRoot: mediaRoot, SubtitlesEnabled: true}, runnerCalls).Handle,
	})
	if err := runner.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	stored, err := store.Get(context.Background(), job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != jobs.StatusSucceeded {
		t.Fatalf("expected media ingest to succeed without original auto captions, got %#v", stored)
	}
	if len(runnerCalls.commands) != 2 {
		t.Fatalf("expected attempted original automatic subtitle command, got %#v", runnerCalls.commands)
	}
	assertScalar(t, db, "SELECT status FROM downloads WHERE video_id = ?", "succeeded", "vid-1")
	assertScalar(t, db, "SELECT count(*) FROM subtitles WHERE video_id = ?", int64(1), "vid-1")
	assertScalar(t, db, "SELECT path FROM subtitles WHERE video_id = ? AND language = ?", "vid-1.en-CA.vtt", "vid-1", "en-ca")
}

func TestDownloadHandlerPropagatesOriginalAutomaticSubtitleCancellation(t *testing.T) {
	t.Parallel()

	db := openDownloadDB(t)
	mediaRoot := writeDownloadFiles(t, "vid-1.mp4", "vid-1.jpg")
	writeDownloadFile(t, mediaRoot, "vid-1.en-CA.vtt", "WEBVTT\n\n00:00:00.000 --> 00:00:02.000\nManual English captions.\n")
	store := jobs.NewStore(db)
	job := enqueueDownloadJob(t, store)
	runnerCalls := &sequenceRunner{
		stdout: [][]byte{fixtureMetadataWithSubtitleAndAutomaticOriginal(t), []byte("cancelled")},
		errs:   []error{nil, context.Canceled},
	}
	runner := jobs.NewRunner(store, map[string]jobs.Handler{
		JobType: newTestDownloader(db, store, Config{YTDLPPath: "yt-dlp", MediaRoot: mediaRoot, SubtitlesEnabled: true}, runnerCalls).Handle,
	})
	if err := runner.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	stored, err := store.Get(context.Background(), job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != jobs.StatusFailed {
		t.Fatalf("expected cancelled original auto-caption fetch to fail the job, got %#v", stored)
	}
	assertNoDownloadedVideoRows(t, db)
}

func TestDownloadHandlerSkipsAutomaticTranslationOnlySubtitles(t *testing.T) {
	t.Parallel()

	db := openDownloadDB(t)
	mediaRoot := writeDownloadFiles(t, "vid-1.mp4", "vid-1.jpg")
	store := jobs.NewStore(db)
	job := enqueueDownloadJob(t, store)
	runnerCalls := &sequenceRunner{stdout: [][]byte{fixtureMetadataWithAutomaticTranslationOnly(t)}}
	runner := jobs.NewRunner(store, map[string]jobs.Handler{
		JobType: newTestDownloader(db, store, Config{YTDLPPath: "yt-dlp", MediaRoot: mediaRoot}, runnerCalls).Handle,
	})
	if err := runner.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	stored, err := store.Get(context.Background(), job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != jobs.StatusSucceeded {
		t.Fatalf("expected succeeded job, got %#v", stored)
	}
	if len(runnerCalls.commands) != 1 {
		t.Fatalf("expected no automatic subtitle command for translations only, got %#v", runnerCalls.commands)
	}
	assertScalar(t, db, "SELECT count(*) FROM subtitles WHERE video_id = ?", int64(0), "vid-1")
}

func TestDownloadHandlerRemovesStaleDownloadedSubtitles(t *testing.T) {
	t.Parallel()

	db := openDownloadDB(t)
	mediaRoot := writeDownloadFiles(t, "vid-1.mp4", "vid-1.jpg")
	writeDownloadFile(t, mediaRoot, "vid-1.en.vtt", "WEBVTT\n\n00:00:00.000 --> 00:00:02.000\nA quiet lunar capsule floats past the archive.\n")
	store := jobs.NewStore(db)
	first := runDownloadJobWithOutput(t, db, store, mediaRoot, fixtureMetadataWithSubtitle(t))
	if first.Status != jobs.StatusSucceeded {
		t.Fatalf("expected first job to succeed, got %#v", first)
	}
	assertScalar(t, db, "SELECT count(*) FROM subtitles WHERE video_id = ?", int64(1), "vid-1")
	if _, err := db.Exec("INSERT INTO search_documents (owner_type, owner_id, field, text) VALUES ('subtitle', 'vid-1', 'text:ja:manual', 'manual transcript')"); err != nil {
		t.Fatal(err)
	}

	second := runDownloadJobWithOutput(t, db, store, mediaRoot, fixtureMetadata)
	if second.Status != jobs.StatusSucceeded {
		t.Fatalf("expected second job to succeed, got %#v", second)
	}
	assertScalar(t, db, "SELECT count(*) FROM subtitles WHERE video_id = ? AND source = 'downloaded'", int64(0), "vid-1")
	assertScalar(t, db, "SELECT count(*) FROM search_documents WHERE owner_type = 'subtitle' AND owner_id = ? AND field LIKE '%:downloaded'", int64(0), "vid-1")
	assertScalar(t, db, "SELECT count(*) FROM search_documents WHERE owner_type = 'subtitle' AND owner_id = ? AND field = 'text:ja:manual'", int64(1), "vid-1")
}

func TestDownloadJobFailsEarlyWhenDiskSpaceBelowThreshold(t *testing.T) {
	t.Parallel()

	db := openDownloadDB(t)
	dataRoot := t.TempDir()
	mediaRoot := t.TempDir()
	store := jobs.NewStore(db)
	job, err := store.Enqueue(context.Background(), jobs.EnqueueParams{
		Type:        JobType,
		PayloadJSON: `{"url":"https://www.youtube.com/watch?v=abc123DEF45"}`,
		MaxAttempts: 1,
		RunAfter:    time.Now().Add(-time.Second),
	})
	if err != nil {
		t.Fatal(err)
	}
	runnerCalls := &recordingRunner{}
	runner := jobs.NewRunner(store, map[string]jobs.Handler{
		JobType: newTestDownloader(db, store, Config{
			YTDLPPath:         "yt-dlp",
			DataRoot:          dataRoot,
			MediaRoot:         mediaRoot,
			MinFreeSpaceBytes: 1 << 30,
			Stat: func(path string) (diskspace.Stats, error) {
				available := uint64(2 << 30)
				if path == mediaRoot {
					available = 512 << 20
				}
				return diskspace.Stats{Path: path, AvailableBytes: available}, nil
			},
		}, runnerCalls).Handle,
	})

	if err := runner.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	stored, err := store.Get(context.Background(), job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != jobs.StatusFailed || !strings.Contains(stored.Error, "low disk space") || !strings.Contains(stored.Error, mediaRoot) {
		t.Fatalf("expected failed low-space job, got %#v", stored)
	}
	if runnerCalls.called {
		t.Fatal("expected low-space guard to fail before starting yt-dlp")
	}
}

func TestDownloadJobRunsWhenDiskSpaceIsSufficient(t *testing.T) {
	t.Parallel()

	db := openDownloadDB(t)
	dataRoot := t.TempDir()
	mediaRoot := writeDownloadFiles(t, "vid-1.mp4", "vid-1.jpg")
	store := jobs.NewStore(db)
	job, err := store.Enqueue(context.Background(), jobs.EnqueueParams{
		Type:        JobType,
		PayloadJSON: `{"url":"https://www.youtube.com/watch?v=abc123DEF45"}`,
		MaxAttempts: 1,
		RunAfter:    time.Now().Add(-time.Second),
	})
	if err != nil {
		t.Fatal(err)
	}
	runnerCalls := &sequenceRunner{stdout: [][]byte{fixtureMetadata}}
	runner := jobs.NewRunner(store, map[string]jobs.Handler{
		JobType: newTestDownloader(db, store, Config{
			YTDLPPath:         "yt-dlp",
			DataRoot:          dataRoot,
			MediaRoot:         mediaRoot,
			MinFreeSpaceBytes: 1 << 30,
			Stat: func(path string) (diskspace.Stats, error) {
				return diskspace.Stats{Path: path, AvailableBytes: 2 << 30}, nil
			},
		}, runnerCalls).Handle,
	})

	if err := runner.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	stored, err := store.Get(context.Background(), job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != jobs.StatusSucceeded {
		t.Fatalf("expected succeeded job, got %#v", stored)
	}
	if len(runnerCalls.commands) != 1 {
		t.Fatalf("expected yt-dlp to run once, got %#v", runnerCalls.commands)
	}
}

func TestChannelFirstJobFailsEarlyWhenDiskSpaceBelowThreshold(t *testing.T) {
	t.Parallel()

	db := openDownloadDB(t)
	dataRoot := t.TempDir()
	mediaRoot := writeDownloadFiles(t, "vid-1.mp4", "vid-1.jpg")
	store := jobs.NewStore(db)
	job, err := store.Enqueue(context.Background(), jobs.EnqueueParams{
		Type:        ChannelJobType,
		PayloadJSON: `{"url":"https://www.youtube.com/@archive"}`,
		MaxAttempts: 1,
		RunAfter:    time.Now().Add(-time.Second),
	})
	if err != nil {
		t.Fatal(err)
	}
	runnerCalls := &recordingRunner{}
	runner := jobs.NewRunner(store, map[string]jobs.Handler{
		ChannelJobType: newTestDownloader(db, store, Config{
			YTDLPPath:         "yt-dlp",
			DataRoot:          dataRoot,
			MediaRoot:         mediaRoot,
			MinFreeSpaceBytes: 1 << 30,
			Stat: func(path string) (diskspace.Stats, error) {
				available := uint64(2 << 30)
				if path == mediaRoot {
					available = 512 << 20
				}
				return diskspace.Stats{Path: path, AvailableBytes: available}, nil
			},
		}, runnerCalls).HandleChannelFirst,
	})

	if err := runner.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	stored, err := store.Get(context.Background(), job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != jobs.StatusFailed || !strings.Contains(stored.Error, "low disk space") || !strings.Contains(stored.Error, mediaRoot) {
		t.Fatalf("expected failed low-space channel job, got %#v", stored)
	}
	if runnerCalls.called {
		t.Fatal("expected low-space guard to fail before resolving the channel")
	}
}

func TestDownloadHandlerIgnoresMissingOptionalThumbnail(t *testing.T) {
	t.Parallel()

	db := openDownloadDB(t)
	mediaRoot := writeDownloadFiles(t, "vid-1.mp4")
	store := jobs.NewStore(db)
	job, err := store.Enqueue(context.Background(), jobs.EnqueueParams{
		Type:        JobType,
		PayloadJSON: `{"url":"https://www.youtube.com/watch?v=abc123DEF45"}`,
		MaxAttempts: 1,
		RunAfter:    time.Now().Add(-time.Second),
	})
	if err != nil {
		t.Fatal(err)
	}

	runner := jobs.NewRunner(store, map[string]jobs.Handler{
		JobType: newTestDownloader(db, store, Config{YTDLPPath: "yt-dlp", MediaRoot: mediaRoot}, fakeRunner{stdout: fixtureMetadataWithPaths(t, "vid-1.mp4", "missing.jpg")}).Handle,
	})
	if err := runner.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}

	stored, err := store.Get(context.Background(), job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != jobs.StatusSucceeded {
		t.Fatalf("expected succeeded job, got %#v", stored)
	}
	assertScalar(t, db, "SELECT thumbnail_path FROM videos WHERE id = ?", "", "vid-1")
	assertScalar(t, db, "SELECT count(*) FROM media_assets WHERE owner_type = 'video' AND owner_id = ? AND kind = 'thumbnail'", int64(0), "vid-1")
}

func TestDownloadHandlerDiscoversDownloadedThumbnailByVideoID(t *testing.T) {
	t.Parallel()

	db := openDownloadDB(t)
	mediaRoot := writeDownloadFiles(t, "vid-1.mp4", "vid-1.webp")
	store := jobs.NewStore(db)
	stored := runDownloadJobWithOutput(t, db, store, mediaRoot, fixtureMetadataWithPaths(t, "vid-1.mp4", ""))
	if stored.Status != jobs.StatusSucceeded {
		t.Fatalf("expected succeeded job, got %#v", stored)
	}

	assertScalar(t, db, "SELECT thumbnail_path FROM videos WHERE id = ?", "vid-1.webp", "vid-1")
	assertScalar(t, db, "SELECT path FROM media_assets WHERE owner_type = 'video' AND owner_id = ? AND kind = 'thumbnail'", "vid-1.webp", "vid-1")
}

func TestDownloadHandlerRemovesStaleThumbnailAssetWhenMissing(t *testing.T) {
	t.Parallel()

	db := openDownloadDB(t)
	mediaRoot := writeDownloadFiles(t, "vid-1.mp4", "vid-1.jpg")
	if err := NewDownloader(db, Config{YTDLPPath: "yt-dlp", MediaRoot: mediaRoot}, fakeRunner{stdout: fixtureMetadata}).Handle(context.Background(), jobs.Job{
		PayloadJSON: `{"url":"https://www.youtube.com/watch?v=abc123DEF45"}`,
	}); err != nil {
		t.Fatal(err)
	}
	assertScalar(t, db, "SELECT count(*) FROM media_assets WHERE owner_type = 'video' AND owner_id = ? AND kind = 'thumbnail'", int64(1), "vid-1")

	if err := NewDownloader(db, Config{YTDLPPath: "yt-dlp", MediaRoot: mediaRoot}, fakeRunner{stdout: fixtureMetadataWithPaths(t, "vid-1.mp4", "missing.jpg")}).Handle(context.Background(), jobs.Job{
		PayloadJSON: `{"url":"https://www.youtube.com/watch?v=abc123DEF45"}`,
	}); err != nil {
		t.Fatal(err)
	}

	assertScalar(t, db, "SELECT thumbnail_path FROM videos WHERE id = ?", "", "vid-1")
	assertScalar(t, db, "SELECT count(*) FROM media_assets WHERE owner_type = 'video' AND owner_id = ? AND kind = 'thumbnail'", int64(0), "vid-1")
}

func TestDownloadIngestionRollsBackOnLateWriteFailure(t *testing.T) {
	t.Parallel()

	db := openDownloadDB(t)
	mediaRoot := writeDownloadFiles(t, "vid-1.mp4", "vid-1.jpg")
	if _, err := db.Exec("INSERT INTO media_assets (owner_type, owner_id, kind, path) VALUES ('video', 'other-video', 'media', 'vid-1.mp4')"); err != nil {
		t.Fatal(err)
	}
	store := jobs.NewStore(db)
	stored := runDownloadJobWithOutput(t, db, store, mediaRoot, fixtureMetadata)

	if stored.Status != jobs.StatusFailed {
		t.Fatalf("expected failed job, got %#v", stored)
	}
	assertScalar(t, db, "SELECT count(*) FROM videos WHERE id = ?", int64(0), "vid-1")
	assertScalar(t, db, "SELECT count(*) FROM videos WHERE media_path <> ''", int64(0))
	assertScalar(t, db, "SELECT count(*) FROM downloads WHERE external_id = ?", int64(0), "vid-1")
	assertScalar(t, db, "SELECT count(*) FROM search_documents WHERE owner_type = 'video' AND owner_id = ?", int64(0), "vid-1")
	assertScalar(t, db, "SELECT count(*) FROM channels WHERE id = ?", int64(0), "chan-1")
}

func TestDownloadIngestionRollsBackWhenJobCompletionFails(t *testing.T) {
	t.Parallel()

	db := openDownloadDB(t)
	store := jobs.NewStore(db)
	mediaRoot := writeDownloadFiles(t, "vid-1.mp4", "vid-1.jpg")
	handler := newTestDownloader(db, store, Config{YTDLPPath: "yt-dlp", MediaRoot: mediaRoot}, fakeRunner{stdout: fixtureMetadata})
	err := handler.Handle(context.Background(), jobs.Job{ID: "missing-job", PayloadJSON: `{"url":"https://www.youtube.com/watch?v=abc123DEF45"}`})
	if !errors.Is(err, jobs.ErrNotFound) {
		t.Fatalf("expected missing job completion error, got %v", err)
	}

	assertScalar(t, db, "SELECT count(*) FROM videos WHERE id = ?", int64(0), "vid-1")
	assertScalar(t, db, "SELECT count(*) FROM downloads WHERE external_id = ?", int64(0), "vid-1")
	assertScalar(t, db, "SELECT count(*) FROM media_assets WHERE owner_id = ?", int64(0), "vid-1")
	assertScalar(t, db, "SELECT count(*) FROM search_documents WHERE owner_type = 'video' AND owner_id = ?", int64(0), "vid-1")
	assertScalar(t, db, "SELECT count(*) FROM channels WHERE id = ?", int64(0), "chan-1")
}

func TestDownloadRetryAfterFailedIngestionSucceeds(t *testing.T) {
	t.Parallel()

	db := openDownloadDB(t)
	mediaRoot := writeDownloadFiles(t, "vid-1.mp4", "vid-1.jpg")
	if _, err := db.Exec("INSERT INTO media_assets (owner_type, owner_id, kind, path) VALUES ('video', 'other-video', 'media', 'vid-1.mp4')"); err != nil {
		t.Fatal(err)
	}
	store := jobs.NewStore(db)
	failed := runDownloadJobWithOutput(t, db, store, mediaRoot, fixtureMetadata)
	if failed.Status != jobs.StatusFailed {
		t.Fatalf("expected failed job, got %#v", failed)
	}
	if _, err := db.Exec("DELETE FROM media_assets WHERE owner_type = 'video' AND owner_id = 'other-video'"); err != nil {
		t.Fatal(err)
	}

	retry := runDownloadJobWithOutput(t, db, store, mediaRoot, fixtureMetadata)
	if retry.Status != jobs.StatusSucceeded {
		t.Fatalf("expected retry to succeed, got %#v", retry)
	}
	assertJobResultAction(t, store, retry.ID, "created")
	assertScalar(t, db, "SELECT count(*) FROM videos WHERE id = ?", int64(1), "vid-1")
	assertScalar(t, db, "SELECT count(*) FROM downloads WHERE external_id = ?", int64(1), "vid-1")
}

func TestDownloadDurableRetrySameJobAfterFailedIngestionSucceeds(t *testing.T) {
	t.Parallel()

	db := openDownloadDB(t)
	mediaRoot := writeDownloadFiles(t, "vid-1.mp4", "vid-1.jpg")
	if _, err := db.Exec("INSERT INTO media_assets (owner_type, owner_id, kind, path) VALUES ('video', 'other-video', 'media', 'vid-1.mp4')"); err != nil {
		t.Fatal(err)
	}
	store := jobs.NewStore(db)
	job, err := store.Enqueue(context.Background(), jobs.EnqueueParams{
		Type:        JobType,
		PayloadJSON: `{"url":"https://www.youtube.com/watch?v=abc123DEF45"}`,
		MaxAttempts: 2,
		RunAfter:    time.Now().Add(-time.Second),
	})
	if err != nil {
		t.Fatal(err)
	}
	runner := jobs.NewRunner(store, map[string]jobs.Handler{
		JobType: newTestDownloader(db, store, Config{YTDLPPath: "yt-dlp", MediaRoot: mediaRoot}, fakeRunner{stdout: fixtureMetadata}).Handle,
	})
	if err := runner.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	queued, err := store.Get(context.Background(), job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if queued.Status != jobs.StatusQueued || queued.Attempts != 1 {
		t.Fatalf("expected retryable queued job after first failure, got %#v", queued)
	}
	assertScalar(t, db, "SELECT count(*) FROM videos WHERE id = ?", int64(0), "vid-1")
	if _, err := db.Exec("DELETE FROM media_assets WHERE owner_type = 'video' AND owner_id = 'other-video'"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("UPDATE jobs SET run_after = ? WHERE id = ?", time.Now().Add(-time.Second).UTC().Format(time.RFC3339Nano), job.ID); err != nil {
		t.Fatal(err)
	}

	if err := runner.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	succeeded, err := store.Get(context.Background(), job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if succeeded.Status != jobs.StatusSucceeded || succeeded.Attempts != 2 {
		t.Fatalf("expected same job retry to succeed, got %#v", succeeded)
	}
	assertJobResultAction(t, store, job.ID, "created")
	assertScalar(t, db, "SELECT count(*) FROM videos WHERE id = ?", int64(1), "vid-1")
	assertScalar(t, db, "SELECT count(*) FROM downloads WHERE external_id = ?", int64(1), "vid-1")
}

func TestDuplicateDownloadsUpdateCanonicalRowsAndJobResults(t *testing.T) {
	t.Parallel()

	db := openDownloadDB(t)
	mediaRoot := writeDownloadFiles(t, "vid-1.mp4", "vid-1.jpg")
	store := jobs.NewStore(db)
	first := runDownloadJobWithOutput(t, db, store, mediaRoot, fixtureMetadata)
	if first.Status != jobs.StatusSucceeded {
		t.Fatalf("expected first download to succeed, got %#v", first)
	}
	assertJobResultAction(t, store, first.ID, "created")

	updatedMetadata := fixtureMetadataWithIDAndTitle(t, "vid-1", "Updated Kapsel Demo", "vid-1.mp4", "vid-1.jpg")
	second := runDownloadJobWithOutput(t, db, store, mediaRoot, updatedMetadata)
	if second.Status != jobs.StatusSucceeded {
		t.Fatalf("expected second download to succeed, got %#v", second)
	}
	assertJobResultAction(t, store, second.ID, "updated")
	assertScalar(t, db, "SELECT count(*) FROM videos WHERE id = ?", int64(1), "vid-1")
	assertScalar(t, db, "SELECT title FROM videos WHERE id = ?", "Updated Kapsel Demo", "vid-1")
	assertScalar(t, db, "SELECT view_count FROM videos WHERE id = ?", int64(1234), "vid-1")
	assertScalar(t, db, "SELECT count(*) FROM downloads WHERE external_id = ?", int64(1), "vid-1")
	assertScalar(t, db, "SELECT count(*) FROM media_assets WHERE owner_type = 'video' AND owner_id = ? AND kind = 'media'", int64(1), "vid-1")
}

func TestDownloadUpdatesExistingCatalogRowByExternalID(t *testing.T) {
	t.Parallel()

	db := openDownloadDB(t)
	mediaRoot := writeDownloadFiles(t, "vid-1.mp4", "vid-1.jpg")
	if _, err := db.Exec("INSERT INTO channels (id, external_id, name) VALUES ('chan-1', 'chan-1', 'Channel One')"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO videos (id, external_id, source, channel_id, title, duration_seconds, media_path) VALUES ('catalog-row', 'vid-1', 'youtube', 'chan-1', 'Catalog Row', 120, '')"); err != nil {
		t.Fatal(err)
	}
	store := jobs.NewStore(db)

	stored := runDownloadJobWithOutput(t, db, store, mediaRoot, fixtureMetadata)
	if stored.Status != jobs.StatusSucceeded {
		t.Fatalf("expected catalog row download to succeed, got %#v", stored)
	}
	assertJobResultActionForVideo(t, store, stored.ID, "catalog-row", "updated")
	assertScalar(t, db, "SELECT count(*) FROM videos WHERE id = ?", int64(1), "catalog-row")
	assertScalar(t, db, "SELECT count(*) FROM videos WHERE id = ?", int64(0), "vid-1")
	assertScalar(t, db, "SELECT media_path FROM videos WHERE id = ?", "vid-1.mp4", "catalog-row")
	assertScalar(t, db, "SELECT count(*) FROM media_assets WHERE owner_type = 'video' AND owner_id = ? AND kind = 'media'", int64(1), "catalog-row")
	assertScalar(t, db, "SELECT video_id FROM downloads WHERE external_id = ?", "catalog-row", "vid-1")
}

func TestDownloadCatalogRowPreviewUsesCanonicalVideoID(t *testing.T) {
	t.Parallel()

	db := openDownloadDB(t)
	mediaRoot := writeDownloadFiles(t, "vid-1.mp4", "vid-1.jpg")
	if _, err := db.Exec("INSERT INTO channels (id, external_id, name) VALUES ('chan-1', 'chan-1', 'Channel One')"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO videos (id, external_id, source, channel_id, title, duration_seconds, media_path) VALUES ('catalog-row', 'vid-1', 'youtube', 'chan-1', 'Catalog Row', 120, '')"); err != nil {
		t.Fatal(err)
	}
	store := jobs.NewStore(db)
	previewRunner := &fakePreviewRunner{}
	job := enqueueDownloadJob(t, store)
	runner := jobs.NewRunner(store, map[string]jobs.Handler{
		JobType: newTestDownloader(db, store, Config{YTDLPPath: "yt-dlp", MediaRoot: mediaRoot, PreviewsEnabled: true, FFMPEGPath: "ffmpeg", PreviewRunner: previewRunner}, fakeRunner{stdout: fixtureMetadata}).Handle,
	})

	if err := runner.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	stored, err := store.Get(context.Background(), job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != jobs.StatusSucceeded {
		t.Fatalf("expected catalog row download to succeed, got %#v", stored)
	}
	if len(previewRunner.commands) != 0 {
		t.Fatalf("expected preview generation to be deferred, got %#v", previewRunner.commands)
	}
	assertPreviewJobPayload(t, db, "catalog-row")
	assertScalar(t, db, "SELECT count(*) FROM video_previews WHERE video_id = ?", int64(0), "catalog-row")
	assertScalar(t, db, "SELECT count(*) FROM video_previews WHERE video_id = ?", int64(0), "vid-1")
	assertScalar(t, db, "SELECT count(*) FROM media_assets WHERE owner_type = 'video' AND owner_id = ? AND kind = ?", int64(0), "catalog-row", previews.SpriteAssetKind)
}

func TestUpsertVideoHandlesExternalIDConflict(t *testing.T) {
	t.Parallel()

	db := openDownloadDB(t)
	if _, err := db.Exec("INSERT INTO channels (id, external_id, name) VALUES ('chan-1', 'chan-1', 'Channel One')"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO videos (id, external_id, source, channel_id, title, duration_seconds, media_path) VALUES ('catalog-row', 'vid-1', 'youtube', 'chan-1', 'Catalog Row', 120, '')"); err != nil {
		t.Fatal(err)
	}
	downloader := NewDownloader(db, Config{}, nil)

	err := downloader.upsertVideo(context.Background(), db, "vid-1", metadata{ID: "vid-1", Title: "Downloaded Title", Description: "Downloaded description", Duration: 240}, "chan-1", "vid-1.mp4", "vid-1.jpg", DownloadOriginManual)
	if err != nil {
		t.Fatal(err)
	}

	assertScalar(t, db, "SELECT count(*) FROM videos WHERE id = ?", int64(1), "catalog-row")
	assertScalar(t, db, "SELECT count(*) FROM videos WHERE id = ?", int64(0), "vid-1")
	assertScalar(t, db, "SELECT title FROM videos WHERE id = ?", "Downloaded Title", "catalog-row")
	assertScalar(t, db, "SELECT media_path FROM videos WHERE id = ?", "vid-1.mp4", "catalog-row")
}

func TestDuplicateDownloadRemovesClearedDescriptionSearchDocument(t *testing.T) {
	t.Parallel()

	db := openDownloadDB(t)
	mediaRoot := writeDownloadFiles(t, "vid-1.mp4", "vid-1.jpg")
	store := jobs.NewStore(db)
	first := runDownloadJobWithOutput(t, db, store, mediaRoot, fixtureMetadataWithDescription(t, "vid-1", "Kapsel Demo", "A downloaded demo", "vid-1.mp4", "vid-1.jpg"))
	if first.Status != jobs.StatusSucceeded {
		t.Fatalf("expected first download to succeed, got %#v", first)
	}
	assertScalar(t, db, "SELECT count(*) FROM search_documents WHERE owner_type = 'video' AND owner_id = ? AND field = 'description'", int64(1), "vid-1")

	second := runDownloadJobWithOutput(t, db, store, mediaRoot, fixtureMetadataWithDescription(t, "vid-1", "Kapsel Demo", "", "vid-1.mp4", "vid-1.jpg"))
	if second.Status != jobs.StatusSucceeded {
		t.Fatalf("expected second download to succeed, got %#v", second)
	}

	assertScalar(t, db, "SELECT description FROM videos WHERE id = ?", "", "vid-1")
	assertScalar(t, db, "SELECT count(*) FROM search_documents WHERE owner_type = 'video' AND owner_id = ? AND field = 'description'", int64(0), "vid-1")
}

func TestRedownloadPreservesArchivedAtAndUpdatesMetadata(t *testing.T) {
	t.Parallel()

	db := openDownloadDB(t)
	mediaRoot := writeDownloadFiles(t, "vid-1.mp4", "vid-1.jpg")
	store := jobs.NewStore(db)
	first := runDownloadJobWithOutput(t, db, store, mediaRoot, fixtureMetadata)
	if first.Status != jobs.StatusSucceeded {
		t.Fatalf("expected first download to succeed, got %#v", first)
	}
	const archivedAt = "2026-01-02T03:04:05Z"
	const oldUpdatedAt = "2026-01-03T04:05:06Z"
	if _, err := db.Exec("UPDATE videos SET archived_at = ?, updated_at = ? WHERE id = ?", archivedAt, oldUpdatedAt, "vid-1"); err != nil {
		t.Fatal(err)
	}

	second := runDownloadJobWithOutput(t, db, store, mediaRoot, fixtureMetadataWithIDAndTitle(t, "vid-1", "Updated Kapsel Demo", "vid-1.mp4", "vid-1.jpg"))
	if second.Status != jobs.StatusSucceeded {
		t.Fatalf("expected redownload to succeed, got %#v", second)
	}

	assertScalar(t, db, "SELECT title FROM videos WHERE id = ?", "Updated Kapsel Demo", "vid-1")
	assertScalar(t, db, "SELECT archived_at FROM videos WHERE id = ?", archivedAt, "vid-1")
	var updatedAt string
	if err := db.QueryRow("SELECT updated_at FROM videos WHERE id = ?", "vid-1").Scan(&updatedAt); err != nil {
		t.Fatal(err)
	}
	if updatedAt == oldUpdatedAt || updatedAt == "" {
		t.Fatalf("expected updated_at to reflect redownload, got %q", updatedAt)
	}
}

func TestRedownloadFillsMissingArchivedAt(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name  string
		value any
	}{
		{name: "null", value: nil},
		{name: "empty", value: ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			db := openDownloadDB(t)
			mediaRoot := writeDownloadFiles(t, "vid-1.mp4", "vid-1.jpg")
			store := jobs.NewStore(db)
			first := runDownloadJobWithOutput(t, db, store, mediaRoot, fixtureMetadata)
			if first.Status != jobs.StatusSucceeded {
				t.Fatalf("expected first download to succeed, got %#v", first)
			}
			if _, err := db.Exec("UPDATE videos SET archived_at = ? WHERE id = ?", test.value, "vid-1"); err != nil {
				t.Fatal(err)
			}

			second := runDownloadJobWithOutput(t, db, store, mediaRoot, fixtureMetadataWithIDAndTitle(t, "vid-1", "Updated Kapsel Demo", "vid-1.mp4", "vid-1.jpg"))
			if second.Status != jobs.StatusSucceeded {
				t.Fatalf("expected redownload to succeed, got %#v", second)
			}

			var archivedAt sql.NullString
			if err := db.QueryRow("SELECT archived_at FROM videos WHERE id = ?", "vid-1").Scan(&archivedAt); err != nil {
				t.Fatal(err)
			}
			if !archivedAt.Valid || archivedAt.String == "" {
				t.Fatalf("expected redownload to fill missing archived_at, got %#v", archivedAt)
			}
		})
	}
}

func TestDownloadValidationRejectsPathTraversal(t *testing.T) {
	t.Parallel()

	stored, db := runFailedValidationJob(t, fixtureMetadataWithPaths(t, "videos/../secret.mp4", ""), writeDownloadFiles(t))
	if !strings.Contains(stored.Error, "invalid download media path") {
		t.Fatalf("expected path validation error, got %#v", stored)
	}
	assertNoDownloadedVideoRows(t, db)
}

func TestDownloadValidationRejectsAbsolutePathOutsideMediaRoot(t *testing.T) {
	t.Parallel()

	mediaRoot := writeDownloadFiles(t)
	outsidePath := filepath.Join(filepath.Dir(mediaRoot), "outside.mp4")
	stored, db := runFailedValidationJob(t, fixtureMetadataWithPaths(t, outsidePath, ""), mediaRoot)
	if !strings.Contains(stored.Error, "invalid download media path") {
		t.Fatalf("expected absolute path validation error, got %#v", stored)
	}
	assertNoDownloadedVideoRows(t, db)
}

func TestDownloadValidationRejectsMissingMediaFile(t *testing.T) {
	t.Parallel()

	stored, db := runFailedValidationJob(t, fixtureMetadataWithPaths(t, "missing.mp4", ""), writeDownloadFiles(t))
	if !strings.Contains(stored.Error, "download media file missing") {
		t.Fatalf("expected missing media file error, got %#v", stored)
	}
	assertNoDownloadedVideoRows(t, db)
}

func TestDownloadValidationRejectsNonRegularMediaFile(t *testing.T) {
	t.Parallel()

	mediaRoot := writeDownloadFiles(t)
	if err := os.MkdirAll(filepath.Join(mediaRoot, "vid-1.mp4"), 0o755); err != nil {
		t.Fatal(err)
	}
	stored, db := runFailedValidationJob(t, fixtureMetadataWithPaths(t, "vid-1.mp4", ""), mediaRoot)
	if !strings.Contains(stored.Error, "not a regular file") {
		t.Fatalf("expected non-regular media file error, got %#v", stored)
	}
	assertNoDownloadedVideoRows(t, db)
}

func TestDownloadValidationRejectsSymlinkMediaFile(t *testing.T) {
	t.Parallel()

	mediaRoot := writeDownloadFiles(t)
	outsideRoot := t.TempDir()
	outsidePath := filepath.Join(outsideRoot, "outside.mp4")
	if err := os.WriteFile(outsidePath, []byte("media"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outsidePath, filepath.Join(mediaRoot, "vid-1.mp4")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}

	stored, db := runFailedValidationJob(t, fixtureMetadataWithPaths(t, "vid-1.mp4", ""), mediaRoot)
	if !strings.Contains(stored.Error, "symlink") {
		t.Fatalf("expected symlink media file error, got %#v", stored)
	}
	assertNoDownloadedVideoRows(t, db)
}

func TestDownloadValidationRejectsSymlinkParentDirectory(t *testing.T) {
	t.Parallel()

	mediaRoot := writeDownloadFiles(t)
	outsideRoot := t.TempDir()
	outsidePath := filepath.Join(outsideRoot, "vid-1.mp4")
	if err := os.WriteFile(outsidePath, []byte("media"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outsideRoot, filepath.Join(mediaRoot, "videos")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}

	stored, db := runFailedValidationJob(t, fixtureMetadataWithPaths(t, "videos/vid-1.mp4", ""), mediaRoot)
	if !strings.Contains(stored.Error, "symlink") {
		t.Fatalf("expected symlink parent directory error, got %#v", stored)
	}
	assertNoDownloadedVideoRows(t, db)
}

func TestDownloadValidationRejectsMalformedMetadata(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name     string
		metadata []byte
		message  string
	}{
		{name: "missing id", metadata: fixtureMetadataWithIDAndTitle(t, "", "Kapsel Demo", "vid-1.mp4"), message: "missing video id"},
		{name: "unsafe id slash", metadata: fixtureMetadataWithIDAndTitle(t, "vid/1", "Kapsel Demo", "vid-1.mp4"), message: "invalid video id"},
		{name: "unsafe id backslash", metadata: fixtureMetadataWithIDAndTitle(t, `vid\1`, "Kapsel Demo", "vid-1.mp4"), message: "invalid video id"},
		{name: "blank title", metadata: fixtureMetadataWithIDAndTitle(t, "vid-1", "  ", "vid-1.mp4"), message: "missing title"},
		{name: "unsafe title control", metadata: fixtureMetadataWithIDAndTitle(t, "vid-1", "bad\x00title", "vid-1.mp4"), message: "invalid title"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			stored, db := runFailedValidationJob(t, tc.metadata, writeDownloadFiles(t, "vid-1.mp4"))
			if !strings.Contains(stored.Error, tc.message) {
				t.Fatalf("expected metadata validation error %q, got %#v", tc.message, stored)
			}
			assertNoDownloadedVideoRows(t, db)
		})
	}
}

func TestDownloadHandlerRejectsUnsupportedURLSchemeBeforeRunner(t *testing.T) {
	t.Parallel()

	db := openDownloadDB(t)
	runner := &recordingRunner{}
	err := NewDownloader(db, Config{YTDLPPath: "yt-dlp", MediaRoot: "media"}, runner).Handle(context.Background(), jobs.Job{
		PayloadJSON: `{"url":"file:///etc/passwd"}`,
	})
	if !errors.Is(err, ErrUnsupportedURLScheme) {
		t.Fatalf("expected unsupported scheme error, got %v", err)
	}
	if runner.called {
		t.Fatal("expected invalid URL to be rejected before running yt-dlp")
	}
}

func TestDownloadHandlerRejectsNonYouTubeURLBeforeRunner(t *testing.T) {
	t.Parallel()

	db := openDownloadDB(t)
	runner := &recordingRunner{}
	err := NewDownloader(db, Config{YTDLPPath: "yt-dlp", MediaRoot: "media"}, runner).Handle(context.Background(), jobs.Job{
		PayloadJSON: `{"url":"https://example.com/watch?v=abc"}`,
	})
	if !errors.Is(err, ErrUnsupportedVideoURL) {
		t.Fatalf("expected unsupported video URL error, got %v", err)
	}
	if runner.called {
		t.Fatal("expected non-YouTube URL to be rejected before running yt-dlp")
	}
}

func TestDownloadHandlerMissingStoreFailsBeforeRunner(t *testing.T) {
	t.Parallel()

	db := openDownloadDB(t)
	runner := &recordingRunner{}
	err := NewDownloader(db, Config{YTDLPPath: "yt-dlp", MediaRoot: writeDownloadFiles(t, "vid-1.mp4", "vid-1.jpg")}, runner).Handle(context.Background(), jobs.Job{
		ID:          "missing-store",
		PayloadJSON: `{"url":"https://www.youtube.com/watch?v=abc123DEF45"}`,
	})
	if err == nil || !strings.Contains(err.Error(), "missing job store") {
		t.Fatalf("expected missing job store error, got %v", err)
	}
	if runner.called {
		t.Fatal("expected missing store to fail before running yt-dlp")
	}
	assertNoDownloadedVideoRows(t, db)
}

func TestDownloadFailureMarksJobFailed(t *testing.T) {
	t.Parallel()

	db := openDownloadDB(t)
	store := jobs.NewStore(db)
	job, err := store.Enqueue(context.Background(), jobs.EnqueueParams{
		Type:        JobType,
		PayloadJSON: `{"url":"https://www.youtube.com/watch?v=abc123DEF45"}`,
		MaxAttempts: 1,
	})
	if err != nil {
		t.Fatal(err)
	}

	runner := jobs.NewRunner(store, map[string]jobs.Handler{
		JobType: newTestDownloader(db, store, Config{YTDLPPath: "yt-dlp", MediaRoot: "media"}, fakeRunner{
			stdout: []byte("ERROR: failed for https://example.com/watch?v=abc&token=secret"),
			err:    errors.New("exit status 1"),
		}).Handle,
	})
	if err := runner.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}

	stored, err := store.Get(context.Background(), job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != jobs.StatusFailed || !strings.Contains(stored.Error, "yt-dlp command failed") || !strings.Contains(stored.Error, "https://example.com/watch") {
		t.Fatalf("expected failed job, got %#v", stored)
	}
	if strings.Contains(stored.Error, "token=secret") {
		t.Fatalf("expected failed job error to redact query secrets, got %#v", stored)
	}
}

func TestDownloadFailureStoresFullRedactedYTDLPError(t *testing.T) {
	t.Parallel()

	tailMarker := "stored-full-error-tail-marker"
	db := openDownloadDB(t)
	store := jobs.NewStore(db)
	job, err := store.Enqueue(context.Background(), jobs.EnqueueParams{
		Type:        JobType,
		PayloadJSON: `{"url":"https://www.youtube.com/watch?v=abc123DEF45"}`,
		MaxAttempts: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	output := []byte(`{"id":"vid-1","title":"Kapsel Demo","formats":[{"format_id":"sb3"}],"description":"` + strings.Repeat("x", maxDiagnosticLength+100) + tailMarker + `"}` + "\ntoken=secret\nERROR: failed after secret")
	runner := jobs.NewRunner(store, map[string]jobs.Handler{
		JobType: newTestDownloader(db, store, Config{YTDLPPath: "yt-dlp", MediaRoot: "media"}, fakeRunner{
			stdout: output,
			err:    errors.New("exit status 1"),
		}).Handle,
	})
	if err := runner.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}

	stored, err := store.Get(context.Background(), job.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{"formats", tailMarker, "token=[redacted]", "ERROR: failed after secret", "exit status 1"} {
		if !strings.Contains(stored.Error, expected) {
			t.Fatalf("expected stored error to contain %q, got %#v", expected, stored)
		}
	}
	if strings.Contains(stored.Error, "token=secret") || strings.Contains(stored.Error, "[truncated]") {
		t.Fatalf("expected stored error to be fully retained but redacted, got %#v", stored)
	}
}

func TestDownloadFailurePreservesYTDLPErrorWhenRecoveryMediaMissing(t *testing.T) {
	t.Parallel()

	tailMarker := "missing-media-ytdlp-tail-marker"
	db := openDownloadDB(t)
	store := jobs.NewStore(db)
	job := enqueueDownloadJob(t, store)
	output := fixtureMetadataWithDescription(t, "vid-1", "Kapsel Demo", strings.Repeat("x", maxDiagnosticLength+100)+tailMarker, "missing.mp4")
	output = append(output, []byte("\nERROR: post-download failure")...)
	runnerCalls := &sequenceRunner{
		stdout: [][]byte{output},
		errs:   []error{errors.New("exit status 1")},
	}
	runner := jobs.NewRunner(store, map[string]jobs.Handler{
		JobType: newTestDownloader(db, store, Config{YTDLPPath: "yt-dlp", MediaRoot: writeDownloadFiles(t)}, runnerCalls).Handle,
	})
	if err := runner.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}

	stored, err := store.Get(context.Background(), job.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{"yt-dlp command failed", tailMarker, "ERROR: post-download failure", "exit status 1", "download media file missing"} {
		if !strings.Contains(stored.Error, expected) {
			t.Fatalf("expected stored error to preserve %q, got %#v", expected, stored)
		}
	}
	if strings.Contains(stored.Error, "[truncated]") {
		t.Fatalf("expected stored recovery failure to keep full yt-dlp diagnostic, got %#v", stored)
	}
}

func TestDownloadJobHeartbeatsYTDLPProgress(t *testing.T) {
	t.Parallel()

	db := openDownloadDB(t)
	store := jobs.NewStore(db)
	job, err := store.Enqueue(context.Background(), jobs.EnqueueParams{
		Type:        JobType,
		PayloadJSON: `{"url":"https://www.youtube.com/watch?v=abc123DEF45"}`,
		MaxAttempts: 1,
	})
	if err != nil {
		t.Fatal(err)
	}

	runner := jobs.NewRunner(store, map[string]jobs.Handler{
		JobType: newTestDownloader(db, store, Config{YTDLPPath: "yt-dlp", MediaRoot: "media"}, progressRunner{progress: []float64{0.42}, err: errors.New("exit status 1")}).Handle,
	})
	if err := runner.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}

	stored, err := store.Get(context.Background(), job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != jobs.StatusFailed || stored.Progress != 0.42 {
		t.Fatalf("expected failed job to retain download progress, got %#v", stored)
	}
}

func TestDownloadJobIgnoresProgressUpdateErrors(t *testing.T) {
	t.Parallel()

	db := openDownloadDB(t)
	mediaRoot := writeDownloadFiles(t, "vid-1.mp4", "vid-1.jpg")
	store := jobs.NewStore(db)
	job := enqueueDownloadJob(t, store)
	quotedID := strings.ReplaceAll(job.ID, "'", "''")
	if _, err := db.Exec(fmt.Sprintf(`
CREATE TRIGGER fail_download_progress_update
BEFORE UPDATE OF progress ON jobs
WHEN NEW.id = '%s' AND OLD.status = 'running' AND NEW.status = 'running' AND NEW.progress > 0 AND NEW.progress < 1
BEGIN
  SELECT RAISE(FAIL, 'progress update unavailable');
END`, quotedID)); err != nil {
		t.Fatal(err)
	}

	runner := jobs.NewRunner(store, map[string]jobs.Handler{
		JobType: newTestDownloader(db, store, Config{YTDLPPath: "yt-dlp", MediaRoot: mediaRoot}, progressMetadataRunner{progress: []float64{0.42}, stdout: fixtureMetadata}).Handle,
	})
	if err := runner.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	stored, err := store.Get(context.Background(), job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != jobs.StatusSucceeded || stored.Progress != 1 || !stored.ResultCommitted {
		t.Fatalf("expected download job to complete despite progress update errors, got %#v", stored)
	}
	assertScalar(t, db, "SELECT title FROM videos WHERE id = ?", "Kapsel Demo", "vid-1")
}

func TestDownloadFailureMarksUnavailableYTDLP(t *testing.T) {
	t.Parallel()

	db := openDownloadDB(t)
	store := jobs.NewStore(db)
	job, err := store.Enqueue(context.Background(), jobs.EnqueueParams{
		Type:        JobType,
		PayloadJSON: `{"url":"https://www.youtube.com/watch?v=abc123DEF45"}`,
		MaxAttempts: 1,
	})
	if err != nil {
		t.Fatal(err)
	}

	runner := jobs.NewRunner(store, map[string]jobs.Handler{
		JobType: newTestDownloader(db, store, Config{YTDLPPath: "/missing/yt-dlp", MediaRoot: "media"}, fakeRunner{err: exec.ErrNotFound}).Handle,
	})
	if err := runner.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}

	stored, err := store.Get(context.Background(), job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != jobs.StatusFailed || !strings.Contains(stored.Error, "yt-dlp unavailable") || !strings.Contains(stored.Error, "/missing/yt-dlp") {
		t.Fatalf("expected unavailable yt-dlp job error, got %#v", stored)
	}
}

func TestChannelFirstDownloadHandlerQueuesFirstVideoDownloadJob(t *testing.T) {
	t.Parallel()

	db := openDownloadDB(t)
	mediaRoot := writeDownloadFiles(t, "vid-1.mp4", "vid-1.jpg")
	store := jobs.NewStore(db)
	job, err := store.Enqueue(context.Background(), jobs.EnqueueParams{
		Type:        ChannelJobType,
		PayloadJSON: `{"url":"https://www.youtube.com/@archive"}`,
		MaxAttempts: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	runnerCalls := &sequenceRunner{stdout: [][]byte{channelFixtureMetadata}}
	runner := jobs.NewRunner(store, map[string]jobs.Handler{
		ChannelJobType: newTestDownloader(db, store, Config{YTDLPPath: "yt-dlp", MediaRoot: mediaRoot}, runnerCalls).HandleChannelFirst,
	})

	if err := runner.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	stored, err := store.Get(context.Background(), job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != jobs.StatusSucceeded {
		t.Fatalf("expected succeeded channel job, got %#v", stored)
	}
	var result struct {
		ChannelID     string               `json:"channel_id"`
		Videos        int                  `json:"videos"`
		Catalog       channelCatalogResult `json:"catalog"`
		FirstVideoURL string               `json:"first_video_url"`
		DownloadJobID string               `json:"download_job_id"`
	}
	if err := json.Unmarshal([]byte(stored.ResultJSON), &result); err != nil {
		t.Fatal(err)
	}
	if result.ChannelID != "archive-channel" || result.Videos != 2 || result.Catalog.ChannelID != "archive-channel" || result.Catalog.Videos != 2 {
		t.Fatalf("expected channel-first result to preserve catalog sync details, got %#v from %q", result, stored.ResultJSON)
	}
	if result.FirstVideoURL != "https://www.youtube.com/watch?v=abc123DEF45" || result.DownloadJobID == "" {
		t.Fatalf("expected channel-first result to reference queued first-video download, got %#v from %q", result, stored.ResultJSON)
	}
	if strings.Contains(stored.ResultJSON, `"video_id"`) || strings.Contains(stored.ResultJSON, `"download"`) {
		t.Fatalf("expected parent result not to claim child download completion, got %q", stored.ResultJSON)
	}
	if len(runnerCalls.commands) != 1 {
		t.Fatalf("expected only channel resolve command, got %#v", runnerCalls.commands)
	}
	if !slices.Contains(runnerCalls.commands[0].Args, "--flat-playlist") {
		t.Fatalf("expected first command to resolve flat playlist, got %#v", runnerCalls.commands[0])
	}

	var downloadJobID, payloadJSON string
	var downloadStatus jobs.Status
	if err := db.QueryRow("SELECT id, payload_json, status FROM jobs WHERE type = ?", JobType).Scan(&downloadJobID, &payloadJSON, &downloadStatus); err != nil {
		t.Fatal(err)
	}
	if downloadJobID != result.DownloadJobID || downloadStatus != jobs.StatusQueued {
		t.Fatalf("expected queued child download job %q, got id=%q status=%s", result.DownloadJobID, downloadJobID, downloadStatus)
	}
	var childPayload Payload
	if err := json.Unmarshal([]byte(payloadJSON), &childPayload); err != nil {
		t.Fatal(err)
	}
	if childPayload.URL != "https://www.youtube.com/watch?v=abc123DEF45" || childPayload.Origin != "" {
		t.Fatalf("expected normal first-video download payload, got %#v from %q", childPayload, payloadJSON)
	}
	assertScalar(t, db, "SELECT media_path FROM videos WHERE id = ?", "", "abc123DEF45")
	assertScalar(t, db, "SELECT count(*) FROM downloads", int64(0))
	assertScalar(t, db, "SELECT subscribed FROM channels WHERE id = ?", int64(1), "archive-channel")

	childRunnerCalls := &sequenceRunner{stdout: [][]byte{fixtureMetadataWithIDAndTitle(t, "abc123DEF45", "Kapsel Demo", "vid-1.mp4", "vid-1.jpg")}}
	childRunner := jobs.NewRunner(store, map[string]jobs.Handler{
		JobType: newTestDownloader(db, store, Config{YTDLPPath: "yt-dlp", MediaRoot: mediaRoot}, childRunnerCalls).Handle,
	})
	if err := childRunner.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	child, err := store.Get(context.Background(), downloadJobID)
	if err != nil {
		t.Fatal(err)
	}
	if child.Status != jobs.StatusSucceeded {
		t.Fatalf("expected queued first-video download to succeed as a normal job, got %#v", child)
	}
	if len(childRunnerCalls.commands) != 1 || !slices.Contains(childRunnerCalls.commands[0].Args, "https://www.youtube.com/watch?v=abc123DEF45") {
		t.Fatalf("expected child job to download first entry, got %#v", childRunnerCalls.commands)
	}
	assertArgValue(t, childRunnerCalls.commands[0].Args, "--format", DefaultFormatSelector)
	assertJobResultActionForVideo(t, store, downloadJobID, "abc123DEF45", "updated")
	assertScalar(t, db, "SELECT title FROM videos WHERE id = ?", "Kapsel Demo", "abc123DEF45")
	assertScalar(t, db, "SELECT status FROM downloads WHERE video_id = ?", "succeeded", "abc123DEF45")
}

func TestChannelFirstDownloadErrorDoesNotFailCatalogJob(t *testing.T) {
	t.Parallel()

	db := openDownloadDB(t)
	store := jobs.NewStore(db)
	job, err := store.Enqueue(context.Background(), jobs.EnqueueParams{
		Type:        ChannelJobType,
		PayloadJSON: `{"url":"https://www.youtube.com/@archive"}`,
		MaxAttempts: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	runner := jobs.NewRunner(store, map[string]jobs.Handler{
		ChannelJobType: newTestDownloader(db, store, Config{YTDLPPath: "yt-dlp", MediaRoot: t.TempDir()}, &sequenceRunner{stdout: [][]byte{channelFixtureMetadata}}).HandleChannelFirst,
	})
	if err := runner.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	parent, err := store.Get(context.Background(), job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if parent.Status != jobs.StatusSucceeded {
		t.Fatalf("expected channel catalog job to succeed before child download runs, got %#v", parent)
	}

	var childID string
	if err := db.QueryRow("SELECT id FROM jobs WHERE type = ?", JobType).Scan(&childID); err != nil {
		t.Fatal(err)
	}
	childRunner := jobs.NewRunner(store, map[string]jobs.Handler{
		JobType: newTestDownloader(db, store, Config{YTDLPPath: "yt-dlp", MediaRoot: t.TempDir()}, fakeRunner{stdout: []byte("ERROR: first video failed"), err: errors.New("exit status 1")}).Handle,
	})
	if err := childRunner.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}

	child, err := store.Get(context.Background(), childID)
	if err != nil {
		t.Fatal(err)
	}
	if child.Status != jobs.StatusQueued || child.Attempts != 1 || !strings.Contains(child.Error, "first video failed") {
		t.Fatalf("expected first-video download retry state to be independent, got %#v", child)
	}
	parent, err = store.Get(context.Background(), job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if parent.Status != jobs.StatusSucceeded {
		t.Fatalf("expected channel catalog job to remain succeeded, got %#v", parent)
	}
	assertScalar(t, db, "SELECT count(*) FROM videos WHERE channel_id = ?", int64(2), "archive-channel")
	assertScalar(t, db, "SELECT count(*) FROM downloads", int64(0))
	assertScalar(t, db, "SELECT subscribed FROM channels WHERE id = ?", int64(1), "archive-channel")
}

func TestChannelFirstQueuedDownloadCanBeCancelledIndependently(t *testing.T) {
	t.Parallel()

	db := openDownloadDB(t)
	store := jobs.NewStore(db)
	job, err := store.Enqueue(context.Background(), jobs.EnqueueParams{
		Type:        ChannelJobType,
		PayloadJSON: `{"url":"https://www.youtube.com/@archive"}`,
		MaxAttempts: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	runner := jobs.NewRunner(store, map[string]jobs.Handler{
		ChannelJobType: newTestDownloader(db, store, Config{YTDLPPath: "yt-dlp", MediaRoot: t.TempDir()}, &sequenceRunner{stdout: [][]byte{channelFixtureMetadata}}).HandleChannelFirst,
	})
	if err := runner.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	var childID string
	if err := db.QueryRow("SELECT id FROM jobs WHERE type = ?", JobType).Scan(&childID); err != nil {
		t.Fatal(err)
	}
	if err := store.Cancel(context.Background(), childID); err != nil {
		t.Fatal(err)
	}

	parent, err := store.Get(context.Background(), job.ID)
	if err != nil {
		t.Fatal(err)
	}
	child, err := store.Get(context.Background(), childID)
	if err != nil {
		t.Fatal(err)
	}
	if parent.Status != jobs.StatusSucceeded {
		t.Fatalf("expected cancelling first-video download not to cancel channel job, got %#v", parent)
	}
	if child.Status != jobs.StatusCancelled || !child.CancelRequested {
		t.Fatalf("expected first-video download to cancel as a normal queued job, got %#v", child)
	}
	assertScalar(t, db, "SELECT count(*) FROM downloads", int64(0))
}

func TestChannelFirstDownloadHandlerSyncsCatalogMetadata(t *testing.T) {
	t.Parallel()

	db := openDownloadDB(t)
	if _, err := db.Exec("INSERT INTO channels (id, external_id, name) VALUES ('chan-1', 'chan-1', 'Archive Workshop')"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
INSERT INTO videos (id, external_id, channel_id, title, description, published_at, duration_seconds)
VALUES ('vid-2', 'vid-2', 'chan-1', 'Old Catalog Title', 'Old catalog description', '2020-01-01', 10)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
INSERT INTO videos (id, external_id, channel_id, title, description, published_at, duration_seconds, media_path, thumbnail_path, thumbnail_url, archived_at)
VALUES ('vid-3', 'vid-3', 'chan-1', 'Old Downloaded Title', 'Old downloaded description', '2020-02-02', 20, 'videos/downloaded.mp4', 'thumbs/downloaded.jpg', 'https://img.example/old.jpg', '2026-01-02T03:04:05Z')`); err != nil {
		t.Fatal(err)
	}

	store := jobs.NewStore(db)
	job, err := store.Enqueue(context.Background(), jobs.EnqueueParams{
		Type:        ChannelJobType,
		PayloadJSON: `{"url":"https://www.youtube.com/@archive"}`,
		MaxAttempts: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	mediaRoot := t.TempDir()
	runnerCalls := &sequenceRunner{stdout: [][]byte{catalogSyncFixtureMetadata}}
	runner := jobs.NewRunner(store, map[string]jobs.Handler{
		ChannelJobType: newTestDownloader(db, store, Config{YTDLPPath: "yt-dlp", MediaRoot: mediaRoot}, runnerCalls).HandleChannelFirst,
	})

	if err := runner.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	stored, err := store.Get(context.Background(), job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != jobs.StatusSucceeded {
		t.Fatalf("expected succeeded channel job, got %#v", stored)
	}

	assertScalar(t, db, "SELECT count(*) FROM videos", int64(4))
	assertScalar(t, db, "SELECT description FROM channels WHERE id = ?", "A channel about local archives\nUpdated weekly", "chan-1")
	assertScalar(t, db, "SELECT thumbnail_url FROM channels WHERE id = ?", "https://yt3.ggpht.com/archive-workshop.jpg", "chan-1")
	assertScalar(t, db, "SELECT catalog_position FROM videos WHERE id = ?", 1, "vid-2")
	assertScalar(t, db, "SELECT catalog_position FROM videos WHERE id = ?", 2, "vid-3")
	assertScalar(t, db, "SELECT count(*) FROM videos WHERE id = 'vid-2'", int64(1))
	assertScalar(t, db, "SELECT title FROM videos WHERE id = ?", "Updated Catalog Title", "vid-2")
	assertScalar(t, db, "SELECT description FROM videos WHERE id = ?", "Updated catalog description", "vid-2")
	assertScalar(t, db, "SELECT published_at FROM videos WHERE id = ?", "2026-04-01", "vid-2")
	assertScalar(t, db, "SELECT duration_seconds FROM videos WHERE id = ?", 95, "vid-2")
	assertScalar(t, db, "SELECT media_path FROM videos WHERE id = ?", "", "vid-2")
	assertScalar(t, db, "SELECT thumbnail_url FROM videos WHERE id = ?", "https://i.ytimg.com/vi/vid-2/hqdefault.jpg", "vid-2")
	assertScalar(t, db, "SELECT title FROM videos WHERE id = ?", "Updated Downloaded Title", "vid-3")
	assertScalar(t, db, "SELECT media_path FROM videos WHERE id = ?", "videos/downloaded.mp4", "vid-3")
	assertScalar(t, db, "SELECT thumbnail_path FROM videos WHERE id = ?", "thumbs/downloaded.jpg", "vid-3")
	assertScalar(t, db, "SELECT archived_at FROM videos WHERE id = ?", "2026-01-02T03:04:05Z", "vid-3")
	assertScalar(t, db, "SELECT thumbnail_url FROM videos WHERE id = ?", "https://i.ytimg.com/vi/vid-3/hqdefault.jpg", "vid-3")
	assertScalar(t, db, "SELECT thumbnail_url FROM videos WHERE id = ?", "", "vid-4")
}

func TestChannelCatalogAcceptsFractionalNestedDurations(t *testing.T) {
	t.Parallel()

	db := openDownloadDB(t)
	metadata := []byte(`{
  "id": "chan-1",
  "channel": "Fractional Duration Channel",
  "entries": [
    {
      "id": "playlist-1",
      "_type": "playlist",
      "entries": [
        {"id": "fracdur0001", "url": "https://www.youtube.com/watch?v=fracdur0001", "title": "Fractional Duration", "duration": 379.0, "upload_date": "20260503"}
      ]
    }
  ]
}`)

	result, err := NewDownloader(db, Config{MediaRoot: t.TempDir()}, nil).syncChannelCatalog(context.Background(), metadata, "")
	if err != nil {
		t.Fatal(err)
	}
	if result.ChannelID != "chan-1" || result.Videos != 1 {
		t.Fatalf("unexpected catalog result: %#v", result)
	}
	assertScalar(t, db, "SELECT duration_seconds FROM videos WHERE id = ?", 379, "fracdur0001")
}

func TestChannelCatalogStoresApproximateDateWithoutReplacingDownloadedDate(t *testing.T) {
	t.Parallel()

	db := openDownloadDB(t)
	if _, err := db.Exec("INSERT INTO channels (id, external_id, name) VALUES ('chan-1', 'chan-1', 'Archive Workshop')"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
INSERT INTO videos (id, external_id, channel_id, title, description, published_at, duration_seconds, media_path)
VALUES ('exact-date1', 'exact-date1', 'chan-1', 'Downloaded Exact Title', '', '2026-01-02', 120, 'videos/exact-date1.mp4')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
INSERT INTO videos (id, external_id, channel_id, title, description, duration_seconds)
VALUES ('missingdate', 'missingdate', 'chan-1', 'Missing Date', '', 0)`); err != nil {
		t.Fatal(err)
	}
	timestamp := time.Date(2026, 5, 8, 15, 30, 0, 0, time.UTC).Unix()
	metadata := []byte(fmt.Sprintf(`{
  "id": "chan-1",
  "channel_id": "chan-1",
  "channel": "Archive Workshop",
  "entries": [
    {"id": "approxdate1", "url": "https://www.youtube.com/watch?v=approxdate1", "title": "Approximate Date", "timestamp": %d},
    {"id": "exact-date1", "url": "https://www.youtube.com/watch?v=exact-date1", "title": "Sparse Catalog Title", "timestamp": %d},
    {"id": "missingdate", "url": "https://www.youtube.com/watch?v=missingdate", "title": "Catalog Date Fill", "timestamp": %d}
  ]
}`, timestamp, timestamp, timestamp))

	result, err := NewDownloader(db, Config{MediaRoot: t.TempDir()}, nil).syncChannelCatalog(context.Background(), metadata, "")
	if err != nil {
		t.Fatal(err)
	}
	if result.ChannelID != "chan-1" || result.Videos != 3 {
		t.Fatalf("unexpected catalog result: %#v", result)
	}
	assertScalar(t, db, "SELECT published_at FROM videos WHERE id = ?", "2026-05-08", "approxdate1")
	assertScalar(t, db, "SELECT published_at FROM videos WHERE id = ?", "2026-01-02", "exact-date1")
	assertScalar(t, db, "SELECT title FROM videos WHERE id = ?", "Downloaded Exact Title", "exact-date1")
	assertScalar(t, db, "SELECT published_at FROM videos WHERE id = ?", "2026-05-08", "missingdate")
}

func TestChannelScanHandlerSyncsCatalogWithoutDownloading(t *testing.T) {
	t.Parallel()

	db := openDownloadDB(t)
	store := jobs.NewStore(db)
	job, err := store.Enqueue(context.Background(), jobs.EnqueueParams{
		Type:        ChannelScanJobType,
		PayloadJSON: `{"url":"https://www.youtube.com/channel/chan-1","channel_id":"chan-1"}`,
		MaxAttempts: 1,
		RunAfter:    time.Now().Add(-time.Second),
	})
	if err != nil {
		t.Fatal(err)
	}
	runnerCalls := &sequenceRunner{stdout: [][]byte{catalogSyncFixtureMetadata}}
	runner := jobs.NewRunner(store, map[string]jobs.Handler{
		ChannelScanJobType: newTestDownloader(db, store, Config{YTDLPPath: "yt-dlp", MediaRoot: t.TempDir()}, runnerCalls).HandleChannelScan,
	})

	if err := runner.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	stored, err := store.Get(context.Background(), job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != jobs.StatusSucceeded {
		t.Fatalf("expected succeeded scan job, got %#v", stored)
	}
	if len(runnerCalls.commands) != 1 || !slices.Contains(runnerCalls.commands[0].Args, "--flat-playlist") {
		t.Fatalf("expected one bounded scan command, got %#v", runnerCalls.commands)
	}
	assertScalar(t, db, "SELECT count(*) FROM downloads", int64(0))
	assertScalar(t, db, "SELECT count(*) FROM videos", int64(4))
	assertScalar(t, db, "SELECT count(*) FROM videos WHERE id = 'vid-2'", int64(1))
	var result struct {
		ChannelID string `json:"channel_id"`
		Videos    int    `json:"videos"`
	}
	if err := json.Unmarshal([]byte(stored.ResultJSON), &result); err != nil {
		t.Fatal(err)
	}
	if result.ChannelID != "chan-1" || result.Videos != 4 {
		t.Fatalf("expected scan result to link channel and synced count, got %#v from %q", result, stored.ResultJSON)
	}
}

func TestChannelScanHandlerUsesRequestedChannelID(t *testing.T) {
	t.Parallel()

	db := openDownloadDB(t)
	store := jobs.NewStore(db)
	job, err := store.Enqueue(context.Background(), jobs.EnqueueParams{
		Type:        ChannelScanJobType,
		PayloadJSON: `{"url":"https://www.youtube.com/channel/resolved-chan","channel_id":"internal-chan"}`,
		MaxAttempts: 1,
		RunAfter:    time.Now().Add(-time.Second),
	})
	if err != nil {
		t.Fatal(err)
	}
	runner := jobs.NewRunner(store, map[string]jobs.Handler{
		ChannelScanJobType: newTestDownloader(db, store, Config{YTDLPPath: "yt-dlp", MediaRoot: t.TempDir()}, &sequenceRunner{stdout: [][]byte{catalogSyncFixtureMetadata}}).HandleChannelScan,
	})

	if err := runner.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	stored, err := store.Get(context.Background(), job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != jobs.StatusSucceeded {
		t.Fatalf("expected succeeded scan job, got %#v", stored)
	}
	assertScalar(t, db, "SELECT count(*) FROM channels WHERE id = ?", int64(1), "internal-chan")
	assertScalar(t, db, "SELECT count(*) FROM channels WHERE id = ?", int64(0), "chan-1")
	assertScalar(t, db, "SELECT count(*) FROM videos WHERE channel_id = ?", int64(4), "internal-chan")
	var result struct {
		ChannelID string `json:"channel_id"`
	}
	if err := json.Unmarshal([]byte(stored.ResultJSON), &result); err != nil {
		t.Fatal(err)
	}
	if result.ChannelID != "internal-chan" {
		t.Fatalf("expected result to link requested channel, got %#v", result)
	}
}

func TestChannelAutoDownloadSchedulerEnqueuesJitteredSubscribedJobs(t *testing.T) {
	t.Parallel()

	db := openDownloadDB(t)
	store := jobs.NewStore(db)
	if _, err := db.Exec(`
INSERT INTO channels (id, external_id, name, subscribed, last_scanned_at)
VALUES
  ('chan-auto', 'chan-auto', 'Auto Channel', 1, ''),
  ('chan-manual', 'chan-manual', 'Manual Channel', 0, '')`); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 5, 7, 9, 0, 0, 0, time.UTC)
	options := ChannelAutoScheduleOptions{
		Now:      func() time.Time { return now },
		Interval: 24 * time.Hour,
		Jitter:   func(time.Duration) time.Duration { return 6 * time.Hour },
	}

	created, err := EnsureChannelAutoDownloadJobs(context.Background(), db, store, options)
	if err != nil {
		t.Fatal(err)
	}
	if created != 1 {
		t.Fatalf("expected one auto job, got %d", created)
	}
	created, err = EnsureChannelAutoDownloadJobs(context.Background(), db, store, options)
	if err != nil {
		t.Fatal(err)
	}
	if created != 0 {
		t.Fatalf("expected active auto job to suppress duplicates, got %d", created)
	}

	items, err := store.List(context.Background(), jobs.ListOptions{Statuses: []jobs.Status{jobs.StatusQueued}, PageSize: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(items.Jobs) != 1 || items.Jobs[0].Type != ChannelAutoDownloadJobType {
		t.Fatalf("expected one queued auto job, got %#v", items.Jobs)
	}
	job, err := store.Get(context.Background(), items.Jobs[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	var payload ChannelAutoDownloadPayload
	if err := json.Unmarshal([]byte(job.PayloadJSON), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.ChannelID != "chan-auto" || payload.URL != "https://www.youtube.com/channel/chan-auto" {
		t.Fatalf("unexpected auto payload: %#v", payload)
	}
	if job.RunAfter != now.Add(6*time.Hour).Format(time.RFC3339Nano) {
		t.Fatalf("expected jittered run_after, got %q", job.RunAfter)
	}
}

func TestNextChannelAutoRunUsesNextDailyWindow(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 5, 7, 10, 0, 0, 0, time.UTC)
	lastScannedAt := time.Date(2026, 5, 7, 9, 0, 0, 0, time.UTC).Format(time.RFC3339Nano)
	runAfter := nextChannelAutoRun(now, lastScannedAt, 24*time.Hour, func(time.Duration) time.Duration {
		return 6 * time.Hour
	})
	want := time.Date(2026, 5, 8, 6, 0, 0, 0, time.UTC)
	if !runAfter.Equal(want) {
		t.Fatalf("expected next daily jittered run %s, got %s", want, runAfter)
	}
}

func TestChannelAutoDownloadSchedulerCreatesNextJobAfterCompletedScan(t *testing.T) {
	t.Parallel()

	db := openDownloadDB(t)
	store := jobs.NewStore(db)
	lastScannedAt := time.Date(2026, 5, 7, 9, 0, 0, 0, time.UTC).Format(time.RFC3339Nano)
	if _, err := db.Exec("INSERT INTO channels (id, external_id, name, subscribed, last_scanned_at) VALUES ('chan-auto', 'chan-auto', 'Auto Channel', 1, ?)", lastScannedAt); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 5, 7, 10, 0, 0, 0, time.UTC)
	created, err := EnsureChannelAutoDownloadJobs(context.Background(), db, store, ChannelAutoScheduleOptions{
		Now:      func() time.Time { return now },
		Interval: 24 * time.Hour,
		Jitter:   func(time.Duration) time.Duration { return 6 * time.Hour },
	})
	if err != nil {
		t.Fatal(err)
	}
	if created != 1 {
		t.Fatalf("expected next auto job after completed scan, got %d", created)
	}
	items, err := store.List(context.Background(), jobs.ListOptions{Statuses: []jobs.Status{jobs.StatusQueued}, PageSize: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(items.Jobs) != 1 {
		t.Fatalf("expected one queued auto job, got %#v", items.Jobs)
	}
	if items.Jobs[0].RunAfter != time.Date(2026, 5, 8, 6, 0, 0, 0, time.UTC).Format(time.RFC3339Nano) {
		t.Fatalf("expected next daily run_after, got %q", items.Jobs[0].RunAfter)
	}
}

func TestChannelAutoDownloadSchedulerPreservesHandleChannelURL(t *testing.T) {
	t.Parallel()

	db := openDownloadDB(t)
	store := jobs.NewStore(db)
	if _, err := db.Exec("INSERT INTO channels (id, external_id, name, subscribed) VALUES ('chan-handle', '@archive', 'Archive Channel', 1)"); err != nil {
		t.Fatal(err)
	}
	created, err := EnsureChannelAutoDownloadJobs(context.Background(), db, store, ChannelAutoScheduleOptions{
		Now:    func() time.Time { return time.Date(2026, 5, 7, 10, 0, 0, 0, time.UTC) },
		Jitter: func(time.Duration) time.Duration { return time.Hour },
	})
	if err != nil {
		t.Fatal(err)
	}
	if created != 1 {
		t.Fatalf("expected handle auto job, got %d", created)
	}
	items, err := store.List(context.Background(), jobs.ListOptions{Statuses: []jobs.Status{jobs.StatusQueued}, PageSize: 10})
	if err != nil {
		t.Fatal(err)
	}
	job, err := store.Get(context.Background(), items.Jobs[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	var payload ChannelAutoDownloadPayload
	if err := json.Unmarshal([]byte(job.PayloadJSON), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.URL != "https://www.youtube.com/@archive" {
		t.Fatalf("expected handle channel URL, got %#v", payload)
	}
}

func TestChannelAutoDownloadSchedulerIgnoresStaleFutureJob(t *testing.T) {
	t.Parallel()

	db := openDownloadDB(t)
	store := jobs.NewStore(db)
	if _, err := db.Exec("INSERT INTO channels (id, external_id, name, subscribed, last_scanned_at) VALUES ('chan-auto', 'chan-auto', 'Auto Channel', 1, '')"); err != nil {
		t.Fatal(err)
	}
	oldJob, err := store.Enqueue(context.Background(), jobs.EnqueueParams{
		Type:        ChannelAutoDownloadJobType,
		PayloadJSON: `{"url":"https://www.youtube.com/channel/chan-auto","channel_id":"chan-auto"}`,
		MaxAttempts: 1,
		RunAfter:    time.Date(2026, 5, 9, 0, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatal(err)
	}
	oldJobCreatedAt, err := time.Parse(time.RFC3339Nano, oldJob.CreatedAt)
	if err != nil {
		t.Fatal(err)
	}
	lastScannedAt := oldJobCreatedAt.Add(time.Hour).Format(time.RFC3339Nano)
	if _, err := db.Exec("UPDATE channels SET last_scanned_at = ? WHERE id = 'chan-auto'", lastScannedAt); err != nil {
		t.Fatal(err)
	}
	created, err := EnsureChannelAutoDownloadJobs(context.Background(), db, store, ChannelAutoScheduleOptions{
		Now:      func() time.Time { return oldJobCreatedAt.Add(2 * time.Hour) },
		Interval: 24 * time.Hour,
		Jitter:   func(time.Duration) time.Duration { return 6 * time.Hour },
	})
	if err != nil {
		t.Fatal(err)
	}
	if created != 1 {
		t.Fatalf("expected replacement auto job for stale future job, got %d", created)
	}
	items, err := store.List(context.Background(), jobs.ListOptions{Statuses: []jobs.Status{jobs.StatusQueued}, PageSize: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(items.Jobs) != 2 {
		t.Fatalf("expected stale and replacement queued jobs, got %#v", items.Jobs)
	}
}

func TestChannelAutoDownloadSchedulerIgnoresCancelRequestedActiveJob(t *testing.T) {
	t.Parallel()

	db := openDownloadDB(t)
	store := jobs.NewStore(db)
	if _, err := db.Exec("INSERT INTO channels (id, external_id, name, subscribed, last_scanned_at) VALUES ('chan-auto', 'chan-auto', 'Auto Channel', 1, '')"); err != nil {
		t.Fatal(err)
	}
	active, err := store.Enqueue(context.Background(), jobs.EnqueueParams{
		Type:        ChannelAutoDownloadJobType,
		PayloadJSON: `{"url":"https://www.youtube.com/channel/chan-auto","channel_id":"chan-auto"}`,
		MaxAttempts: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	claimed, ok, err := store.Claim(context.Background(), time.Now().Add(time.Second), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if !ok || claimed.ID != active.ID {
		t.Fatalf("expected active auto job claim, ok=%v job=%#v", ok, claimed)
	}
	if err := store.Cancel(context.Background(), active.ID); err != nil {
		t.Fatal(err)
	}

	created, err := EnsureChannelAutoDownloadJobs(context.Background(), db, store, ChannelAutoScheduleOptions{
		Now:    func() time.Time { return time.Date(2026, 5, 7, 10, 0, 0, 0, time.UTC) },
		Jitter: func(time.Duration) time.Duration { return 0 },
	})
	if err != nil {
		t.Fatal(err)
	}
	if created != 1 {
		t.Fatalf("expected cancel-requested active job not to suppress replacement, got %d", created)
	}
	assertScalar(t, db, "SELECT count(*) FROM jobs WHERE type = ?", int64(2), ChannelAutoDownloadJobType)
}

func TestChannelAutoDownloadHandlerStopsAfterNewestPageOverlap(t *testing.T) {
	t.Parallel()

	db := openDownloadDB(t)
	store := jobs.NewStore(db)
	if _, err := db.Exec("INSERT INTO channels (id, external_id, name, subscribed) VALUES ('chan-1', 'chan-1', 'Archive Workshop', 1)"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
INSERT INTO videos (id, external_id, channel_id, title, description, media_path)
VALUES ('oldvideo001', 'oldvideo001', 'chan-1', 'Already Downloaded', '', 'oldvideo001.mp4')`); err != nil {
		t.Fatal(err)
	}
	job, err := store.Enqueue(context.Background(), jobs.EnqueueParams{
		Type:        ChannelAutoDownloadJobType,
		PayloadJSON: `{"url":"https://www.youtube.com/channel/chan-1","channel_id":"chan-1"}`,
		MaxAttempts: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	runnerCalls := &sequenceRunner{stdout: [][]byte{channelCatalogPageFixture(t, "chan-1", "newvideo001", "oldvideo001")}}
	runner := jobs.NewRunner(store, map[string]jobs.Handler{
		ChannelAutoDownloadJobType: newTestDownloader(db, store, Config{YTDLPPath: "yt-dlp", MediaRoot: t.TempDir()}, runnerCalls).HandleChannelAutoDownload,
	})

	if err := runner.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	stored, err := store.Get(context.Background(), job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != jobs.StatusSucceeded {
		t.Fatalf("expected succeeded auto job, got %#v", stored)
	}
	if len(runnerCalls.commands) != 1 {
		t.Fatalf("expected first page only when newest page overlaps local downloads, got %#v", runnerCalls.commands)
	}
	assertArgValue(t, runnerCalls.commands[0].Args, "--playlist-end", "30")
	assertScalar(t, db, "SELECT count(*) FROM jobs WHERE type = ?", int64(1), JobType)
	var payload Payload
	assertScalar(t, db, "SELECT payload_json FROM jobs WHERE type = ?", `{"url":"https://www.youtube.com/watch?v=newvideo001","origin":"channel_auto"}`, JobType)
	if err := json.Unmarshal([]byte(`{"url":"https://www.youtube.com/watch?v=newvideo001","origin":"channel_auto"}`), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Origin != DownloadOriginChannelAuto {
		t.Fatalf("expected auto queued download origin, got %#v", payload)
	}
	assertScalar(t, db, "SELECT last_scanned_at <> '' FROM channels WHERE id = ?", int64(1), "chan-1")
	var result channelAutoDownloadResult
	if err := json.Unmarshal([]byte(stored.ResultJSON), &result); err != nil {
		t.Fatal(err)
	}
	if result.ChannelID != "chan-1" || result.Pages != 1 || result.DownloadsQueued != 1 {
		t.Fatalf("unexpected auto result: %#v", result)
	}
}

func TestActiveDownloadJobForPayloadFindsNormalizedMatchBeyondListPage(t *testing.T) {
	t.Parallel()

	store := jobs.NewStore(openDownloadDB(t))
	for index := range jobs.MaxListPageSize + 1 {
		url := fmt.Sprintf("https://www.youtube.com/watch?v=active%05d", index)
		if index == jobs.MaxListPageSize {
			url = "https://www.youtube.com/watch?v=abc123DEF45"
		}
		payloadJSON, err := json.Marshal(Payload{URL: url})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := store.Enqueue(context.Background(), jobs.EnqueueParams{Type: JobType, PayloadJSON: string(payloadJSON)}); err != nil {
			t.Fatal(err)
		}
	}
	target, err := json.Marshal(Payload{URL: "https://youtu.be/abc123DEF45"})
	if err != nil {
		t.Fatal(err)
	}

	found, ok, err := ActiveJobForPayload(context.Background(), store, string(target))
	if err != nil {
		t.Fatal(err)
	}
	if !ok || found.PayloadJSON != `{"url":"https://www.youtube.com/watch?v=abc123DEF45"}` {
		t.Fatalf("expected normalized match beyond first list page, ok=%v job=%#v", ok, found)
	}
}

func TestEnqueueDownloadSuppressesConcurrentEquivalentURLs(t *testing.T) {
	t.Parallel()

	db := openDownloadDB(t)
	store := jobs.NewStore(db)
	urls := []string{
		"https://www.youtube.com/watch?v=abc123DEF45",
		"https://youtu.be/abc123DEF45",
		"https://www.youtube.com/shorts/abc123DEF45?feature=share",
		" https://www.youtube.com/embed/abc123DEF45 ",
	}
	ids := make(chan string, len(urls)*4)
	errs := make(chan error, len(urls)*4)
	var wg sync.WaitGroup
	for i := range len(urls) * 4 {
		wg.Add(1)
		go func(rawURL string) {
			defer wg.Done()
			job, err := EnqueueDownload(context.Background(), store, Payload{URL: rawURL})
			if err != nil {
				errs <- err
				return
			}
			ids <- job.ID
		}(urls[i%len(urls)])
	}
	wg.Wait()
	close(ids)
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}

	var first string
	for id := range ids {
		if first == "" {
			first = id
			continue
		}
		if id != first {
			t.Fatalf("expected duplicate enqueue to return %q, got %q", first, id)
		}
	}
	assertScalar(t, db, "SELECT count(*) FROM jobs WHERE type = ?", int64(1), JobType)
	assertScalar(t, db, "SELECT payload_json FROM jobs WHERE type = ?", `{"url":"https://www.youtube.com/watch?v=abc123DEF45"}`, JobType)
}

func TestEnqueueChannelScanSuppressesConcurrentDuplicate(t *testing.T) {
	t.Parallel()

	db := openDownloadDB(t)
	store := jobs.NewStore(db)
	payloads := []ChannelScanPayload{
		{URL: "https://www.youtube.com/channel/chan-1", ChannelID: "chan-1"},
		{URL: " https://WWW.YOUTUBE.COM/channel/chan-1/?view=videos#featured ", ChannelID: "chan-1"},
	}
	ids := make(chan string, len(payloads)*4)
	errs := make(chan error, len(payloads)*4)
	var wg sync.WaitGroup
	for i := range len(payloads) * 4 {
		wg.Add(1)
		go func(payload ChannelScanPayload) {
			defer wg.Done()
			job, err := EnqueueChannelScan(context.Background(), store, payload)
			if err != nil {
				errs <- err
				return
			}
			ids <- job.ID
		}(payloads[i%len(payloads)])
	}
	wg.Wait()
	close(ids)
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}

	var first string
	for id := range ids {
		if first == "" {
			first = id
			continue
		}
		if id != first {
			t.Fatalf("expected duplicate enqueue to return %q, got %q", first, id)
		}
	}
	assertScalar(t, db, "SELECT count(*) FROM jobs WHERE type = ?", int64(1), ChannelScanJobType)
}

func TestChannelAutoDownloadQueuesOnlyNewestTwoCatalogVideos(t *testing.T) {
	t.Parallel()

	db := openDownloadDB(t)
	store := jobs.NewStore(db)
	if _, err := db.Exec("INSERT INTO channels (id, external_id, name, subscribed) VALUES ('chan-1', 'chan-1', 'Archive Workshop', 1)"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
INSERT INTO videos (id, external_id, channel_id, title, description, media_path)
VALUES ('newest00001', 'newest00001', 'chan-1', 'Already Downloaded', '', 'newest00001.mp4')`); err != nil {
		t.Fatal(err)
	}
	job, err := store.Enqueue(context.Background(), jobs.EnqueueParams{
		Type:        ChannelAutoDownloadJobType,
		PayloadJSON: `{"url":"https://www.youtube.com/channel/chan-1","channel_id":"chan-1"}`,
		MaxAttempts: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	runnerCalls := &sequenceRunner{stdout: [][]byte{channelCatalogPageFixture(t, "chan-1", "newest00001", "newest00002", "older000003")}}
	runner := jobs.NewRunner(store, map[string]jobs.Handler{
		ChannelAutoDownloadJobType: newTestDownloader(db, store, Config{YTDLPPath: "yt-dlp", MediaRoot: t.TempDir()}, runnerCalls).HandleChannelAutoDownload,
	})

	if err := runner.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	stored, err := store.Get(context.Background(), job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != jobs.StatusSucceeded {
		t.Fatalf("expected succeeded auto job, got %#v", stored)
	}
	assertScalar(t, db, "SELECT count(*) FROM jobs WHERE type = ?", int64(1), JobType)
	assertScalar(t, db, "SELECT payload_json FROM jobs WHERE type = ?", `{"url":"https://www.youtube.com/watch?v=newest00002","origin":"channel_auto"}`, JobType)
	var result channelAutoDownloadResult
	if err := json.Unmarshal([]byte(stored.ResultJSON), &result); err != nil {
		t.Fatal(err)
	}
	if result.DownloadsQueued != 1 {
		t.Fatalf("expected only the missing newest-two video to queue, got %#v", result)
	}
}

func TestChannelAutoDownloadDoesNotBackfillPastActiveNewestTwo(t *testing.T) {
	t.Parallel()

	db := openDownloadDB(t)
	store := jobs.NewStore(db)
	if _, err := db.Exec("INSERT INTO channels (id, external_id, name, subscribed) VALUES ('chan-1', 'chan-1', 'Archive Workshop', 1)"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Enqueue(context.Background(), jobs.EnqueueParams{
		Type:        JobType,
		PayloadJSON: `{"url":"https://www.youtube.com/watch?v=newest00001"}`,
		MaxAttempts: 1,
		RunAfter:    time.Now().Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	job, err := store.Enqueue(context.Background(), jobs.EnqueueParams{
		Type:        ChannelAutoDownloadJobType,
		PayloadJSON: `{"url":"https://www.youtube.com/channel/chan-1","channel_id":"chan-1"}`,
		MaxAttempts: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	runnerCalls := &sequenceRunner{stdout: [][]byte{channelCatalogPageFixture(t, "chan-1", "newest00001", "newest00002", "older000003")}}
	runner := jobs.NewRunner(store, map[string]jobs.Handler{
		ChannelAutoDownloadJobType: newTestDownloader(db, store, Config{YTDLPPath: "yt-dlp", MediaRoot: t.TempDir()}, runnerCalls).HandleChannelAutoDownload,
	})

	if err := runner.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	stored, err := store.Get(context.Background(), job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != jobs.StatusSucceeded {
		t.Fatalf("expected succeeded auto job, got %#v", stored)
	}
	assertScalar(t, db, "SELECT count(*) FROM jobs WHERE type = ?", int64(2), JobType)
	assertScalar(t, db, "SELECT count(*) FROM jobs WHERE type = ? AND payload_json = ?", int64(1), JobType, `{"url":"https://www.youtube.com/watch?v=newest00002","origin":"channel_auto"}`)
	assertScalar(t, db, "SELECT count(*) FROM jobs WHERE type = ? AND payload_json LIKE ?", int64(0), JobType, `%older000003%`)
	var result channelAutoDownloadResult
	if err := json.Unmarshal([]byte(stored.ResultJSON), &result); err != nil {
		t.Fatal(err)
	}
	if result.DownloadsQueued != 1 {
		t.Fatalf("expected active newest video to suppress backfill, got %#v", result)
	}
}

func TestChannelAutoDownloadDoesNotBackfillPastDownloadedSecondVideo(t *testing.T) {
	t.Parallel()

	db := openDownloadDB(t)
	store := jobs.NewStore(db)
	if _, err := db.Exec("INSERT INTO channels (id, external_id, name, subscribed) VALUES ('chan-1', 'chan-1', 'Archive Workshop', 1)"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
INSERT INTO videos (id, external_id, channel_id, title, description, media_path)
VALUES ('newest00002', 'newest00002', 'chan-1', 'Already Downloaded', '', 'newest00002.mp4')`); err != nil {
		t.Fatal(err)
	}
	job, err := store.Enqueue(context.Background(), jobs.EnqueueParams{
		Type:        ChannelAutoDownloadJobType,
		PayloadJSON: `{"url":"https://www.youtube.com/channel/chan-1","channel_id":"chan-1"}`,
		MaxAttempts: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	runnerCalls := &sequenceRunner{stdout: [][]byte{channelCatalogPageFixture(t, "chan-1", "newest00001", "newest00002", "older000003")}}
	runner := jobs.NewRunner(store, map[string]jobs.Handler{
		ChannelAutoDownloadJobType: newTestDownloader(db, store, Config{YTDLPPath: "yt-dlp", MediaRoot: t.TempDir()}, runnerCalls).HandleChannelAutoDownload,
	})

	if err := runner.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	stored, err := store.Get(context.Background(), job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != jobs.StatusSucceeded {
		t.Fatalf("expected succeeded auto job, got %#v", stored)
	}
	assertScalar(t, db, "SELECT count(*) FROM jobs WHERE type = ?", int64(1), JobType)
	assertScalar(t, db, "SELECT payload_json FROM jobs WHERE type = ?", `{"url":"https://www.youtube.com/watch?v=newest00001","origin":"channel_auto"}`, JobType)
	assertScalar(t, db, "SELECT count(*) FROM jobs WHERE type = ? AND payload_json LIKE ?", int64(0), JobType, `%older000003%`)
	var result channelAutoDownloadResult
	if err := json.Unmarshal([]byte(stored.ResultJSON), &result); err != nil {
		t.Fatal(err)
	}
	if result.DownloadsQueued != 1 {
		t.Fatalf("expected only the missing newest video to queue, got %#v", result)
	}
}

func TestChannelAutoDownloadQueuesNothingWhenNewestTwoAreHandled(t *testing.T) {
	t.Parallel()

	db := openDownloadDB(t)
	store := jobs.NewStore(db)
	if _, err := db.Exec("INSERT INTO channels (id, external_id, name, subscribed) VALUES ('chan-1', 'chan-1', 'Archive Workshop', 1)"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
INSERT INTO videos (id, external_id, channel_id, title, description, media_path)
VALUES ('newest00002', 'newest00002', 'chan-1', 'Already Downloaded', '', 'newest00002.mp4')`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Enqueue(context.Background(), jobs.EnqueueParams{
		Type:        JobType,
		PayloadJSON: `{"url":"https://www.youtube.com/watch?v=newest00001"}`,
		MaxAttempts: 1,
		RunAfter:    time.Now().Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	job, err := store.Enqueue(context.Background(), jobs.EnqueueParams{
		Type:        ChannelAutoDownloadJobType,
		PayloadJSON: `{"url":"https://www.youtube.com/channel/chan-1","channel_id":"chan-1"}`,
		MaxAttempts: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	runnerCalls := &sequenceRunner{stdout: [][]byte{channelCatalogPageFixture(t, "chan-1", "newest00001", "newest00002", "older000003")}}
	runner := jobs.NewRunner(store, map[string]jobs.Handler{
		ChannelAutoDownloadJobType: newTestDownloader(db, store, Config{YTDLPPath: "yt-dlp", MediaRoot: t.TempDir()}, runnerCalls).HandleChannelAutoDownload,
	})

	if err := runner.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	stored, err := store.Get(context.Background(), job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != jobs.StatusSucceeded {
		t.Fatalf("expected succeeded auto job, got %#v", stored)
	}
	assertScalar(t, db, "SELECT count(*) FROM jobs WHERE type = ?", int64(1), JobType)
	assertScalar(t, db, "SELECT count(*) FROM jobs WHERE type = ? AND payload_json LIKE ?", int64(0), JobType, `%older000003%`)
	var result channelAutoDownloadResult
	if err := json.Unmarshal([]byte(stored.ResultJSON), &result); err != nil {
		t.Fatal(err)
	}
	if result.DownloadsQueued != 0 {
		t.Fatalf("expected no backfill when newest two are handled, got %#v", result)
	}
}

func TestCatalogDownloadNeededDeduplicatesManualAndAutoPayloads(t *testing.T) {
	t.Parallel()

	db := openDownloadDB(t)
	store := jobs.NewStore(db)
	if _, err := store.Enqueue(context.Background(), jobs.EnqueueParams{
		Type:        JobType,
		PayloadJSON: `{"url":"https://www.youtube.com/watch?v=newvideo001"}`,
		MaxAttempts: 1,
	}); err != nil {
		t.Fatal(err)
	}
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	queued, err := newTestDownloader(db, store, Config{MediaRoot: t.TempDir()}, nil).enqueueMissingCatalogDownloads(context.Background(), tx, []catalogVideo{{ID: "newvideo001"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if queued != 0 {
		t.Fatal("expected active manual download to suppress matching auto download")
	}
	assertScalar(t, db, "SELECT count(*) FROM jobs WHERE type = ?", int64(1), JobType)
}

func TestAutoCatalogDownloadIgnoresCancelRequestedActiveDownload(t *testing.T) {
	t.Parallel()

	db := openDownloadDB(t)
	store := jobs.NewStore(db)
	active, err := store.Enqueue(context.Background(), jobs.EnqueueParams{
		Type:        JobType,
		PayloadJSON: `{"url":"https://www.youtube.com/watch?v=newvideo001"}`,
		MaxAttempts: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	claimed, ok, err := store.Claim(context.Background(), time.Now().Add(time.Second), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if !ok || claimed.ID != active.ID {
		t.Fatalf("expected active download claim, ok=%v job=%#v", ok, claimed)
	}
	if err := store.Cancel(context.Background(), active.ID); err != nil {
		t.Fatal(err)
	}
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	queued, err := newTestDownloader(db, store, Config{MediaRoot: t.TempDir()}, nil).enqueueMissingCatalogDownloads(context.Background(), tx, []catalogVideo{{ID: "newvideo001"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if queued != 1 {
		t.Fatalf("expected cancel-requested active download not to suppress replacement, got %d", queued)
	}
	assertScalar(t, db, "SELECT count(*) FROM jobs WHERE type = ?", int64(2), JobType)
}

func TestChannelAutoDownloadPreservesDownloadedMetadataFromSparseCatalog(t *testing.T) {
	t.Parallel()

	db := openDownloadDB(t)
	store := jobs.NewStore(db)
	if _, err := db.Exec("INSERT INTO channels (id, external_id, name, subscribed) VALUES ('chan-1', 'chan-1', 'Archive Workshop', 1)"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
INSERT INTO videos (id, external_id, channel_id, title, description, published_at, duration_seconds, media_path)
VALUES ('oldvideo001', 'oldvideo001', 'chan-1', 'Rich Downloaded Title', 'Rich downloaded description', '2026-01-02', 321, 'oldvideo001.mp4')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
INSERT INTO search_documents (owner_type, owner_id, field, text)
VALUES
  ('video', 'oldvideo001', 'title', 'Rich Downloaded Title'),
  ('video', 'oldvideo001', 'description', 'Rich downloaded description')`); err != nil {
		t.Fatal(err)
	}
	job, err := store.Enqueue(context.Background(), jobs.EnqueueParams{
		Type:        ChannelAutoDownloadJobType,
		PayloadJSON: `{"url":"https://www.youtube.com/channel/chan-1","channel_id":"chan-1"}`,
		MaxAttempts: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	runnerCalls := &sequenceRunner{stdout: [][]byte{channelCatalogPageFixture(t, "chan-1", "oldvideo001")}}
	runner := jobs.NewRunner(store, map[string]jobs.Handler{
		ChannelAutoDownloadJobType: newTestDownloader(db, store, Config{YTDLPPath: "yt-dlp", MediaRoot: t.TempDir()}, runnerCalls).HandleChannelAutoDownload,
	})

	if err := runner.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	stored, err := store.Get(context.Background(), job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != jobs.StatusSucceeded {
		t.Fatalf("expected succeeded auto job, got %#v", stored)
	}
	assertScalar(t, db, "SELECT title FROM videos WHERE id = ?", "Rich Downloaded Title", "oldvideo001")
	assertScalar(t, db, "SELECT description FROM videos WHERE id = ?", "Rich downloaded description", "oldvideo001")
	assertScalar(t, db, "SELECT published_at FROM videos WHERE id = ?", "2026-01-02", "oldvideo001")
	assertScalar(t, db, "SELECT duration_seconds FROM videos WHERE id = ?", 321, "oldvideo001")
	assertScalar(t, db, "SELECT text FROM search_documents WHERE owner_type = 'video' AND owner_id = ? AND field = 'title'", "Rich Downloaded Title", "oldvideo001")
	assertScalar(t, db, "SELECT text FROM search_documents WHERE owner_type = 'video' AND owner_id = ? AND field = 'description'", "Rich downloaded description", "oldvideo001")
}

func TestChannelAutoDownloadSkipsStaleQueuedJobAfterManualScan(t *testing.T) {
	t.Parallel()

	db := openDownloadDB(t)
	store := jobs.NewStore(db)
	if _, err := db.Exec("INSERT INTO channels (id, external_id, name, subscribed, last_scanned_at) VALUES ('chan-1', 'chan-1', 'Archive Workshop', 1, '')"); err != nil {
		t.Fatal(err)
	}
	job, err := store.Enqueue(context.Background(), jobs.EnqueueParams{
		Type:        ChannelAutoDownloadJobType,
		PayloadJSON: `{"url":"https://www.youtube.com/channel/chan-1","channel_id":"chan-1"}`,
		MaxAttempts: 1,
		RunAfter:    time.Now().Add(-time.Second),
	})
	if err != nil {
		t.Fatal(err)
	}
	jobCreatedAt, err := time.Parse(time.RFC3339Nano, job.CreatedAt)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("UPDATE channels SET last_scanned_at = ? WHERE id = 'chan-1'", jobCreatedAt.Add(time.Hour).Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	runnerCalls := &recordingRunner{}
	runner := jobs.NewRunner(store, map[string]jobs.Handler{
		ChannelAutoDownloadJobType: newTestDownloader(db, store, Config{YTDLPPath: "yt-dlp", MediaRoot: t.TempDir()}, runnerCalls).HandleChannelAutoDownload,
	})

	if err := runner.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	stored, err := store.Get(context.Background(), job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != jobs.StatusSucceeded {
		t.Fatalf("expected stale auto job to complete as skipped, got %#v", stored)
	}
	if runnerCalls.called {
		t.Fatalf("expected stale auto job not to call yt-dlp")
	}
	var result channelAutoDownloadResult
	if err := json.Unmarshal([]byte(stored.ResultJSON), &result); err != nil {
		t.Fatal(err)
	}
	if !result.Skipped {
		t.Fatalf("expected skipped result, got %#v", result)
	}
}

func TestChannelAutoDownloadSkipsDeletedQueuedChannel(t *testing.T) {
	t.Parallel()

	db := openDownloadDB(t)
	store := jobs.NewStore(db)
	if _, err := db.Exec("INSERT INTO channels (id, external_id, name, subscribed) VALUES ('chan-1', 'chan-1', 'Archive Workshop', 1)"); err != nil {
		t.Fatal(err)
	}
	job, err := store.Enqueue(context.Background(), jobs.EnqueueParams{
		Type:        ChannelAutoDownloadJobType,
		PayloadJSON: `{"url":"https://www.youtube.com/channel/chan-1","channel_id":"chan-1"}`,
		MaxAttempts: 1,
		RunAfter:    time.Now().Add(-time.Second),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("DELETE FROM channels WHERE id = 'chan-1'"); err != nil {
		t.Fatal(err)
	}
	runnerCalls := &recordingRunner{}
	runner := jobs.NewRunner(store, map[string]jobs.Handler{
		ChannelAutoDownloadJobType: newTestDownloader(db, store, Config{YTDLPPath: "yt-dlp", MediaRoot: t.TempDir()}, runnerCalls).HandleChannelAutoDownload,
	})

	if err := runner.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	stored, err := store.Get(context.Background(), job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != jobs.StatusSucceeded || runnerCalls.called {
		t.Fatalf("expected deleted auto job to skip without yt-dlp, got job=%#v called=%v", stored, runnerCalls.called)
	}
	assertScalar(t, db, "SELECT count(*) FROM channels WHERE id = ?", int64(0), "chan-1")
}

func TestChannelAutoDownloadStopsBeforeQueueWhenUnsubscribedDuringRun(t *testing.T) {
	t.Parallel()

	db := openDownloadDB(t)
	store := jobs.NewStore(db)
	if _, err := db.Exec("INSERT INTO channels (id, external_id, name, subscribed, last_scanned_at) VALUES ('chan-1', 'chan-1', 'Archive Workshop', 1, '')"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
INSERT INTO videos (id, external_id, channel_id, title, description, media_path)
VALUES ('oldvideo001', 'oldvideo001', 'chan-1', 'Already Downloaded', '', 'oldvideo001.mp4')`); err != nil {
		t.Fatal(err)
	}
	job, err := store.Enqueue(context.Background(), jobs.EnqueueParams{
		Type:        ChannelAutoDownloadJobType,
		PayloadJSON: `{"url":"https://www.youtube.com/channel/chan-1","channel_id":"chan-1"}`,
		MaxAttempts: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	runnerCalls := &hookRunner{
		stdout: [][]byte{channelCatalogPageFixture(t, "chan-1", "newvideo001", "oldvideo001")},
		afterRun: func() {
			_, err := db.Exec("UPDATE channels SET subscribed = 0 WHERE id = 'chan-1'")
			if err != nil {
				t.Errorf("unsubscribe channel: %v", err)
			}
		},
	}
	runner := jobs.NewRunner(store, map[string]jobs.Handler{
		ChannelAutoDownloadJobType: newTestDownloader(db, store, Config{YTDLPPath: "yt-dlp", MediaRoot: t.TempDir()}, runnerCalls).HandleChannelAutoDownload,
	})

	if err := runner.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	stored, err := store.Get(context.Background(), job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != jobs.StatusSucceeded {
		t.Fatalf("expected unsubscribed auto job to complete as skipped, got %#v", stored)
	}
	assertScalar(t, db, "SELECT count(*) FROM jobs WHERE type = ?", int64(0), JobType)
	assertScalar(t, db, "SELECT last_scanned_at FROM channels WHERE id = ?", "", "chan-1")
	var result channelAutoDownloadResult
	if err := json.Unmarshal([]byte(stored.ResultJSON), &result); err != nil {
		t.Fatal(err)
	}
	if !result.Skipped {
		t.Fatalf("expected skipped result, got %#v", result)
	}
}

func TestChannelAutoDownloadHandlerFetchesNextPageUntilOverlap(t *testing.T) {
	t.Parallel()

	db := openDownloadDB(t)
	store := jobs.NewStore(db)
	if _, err := db.Exec("INSERT INTO channels (id, external_id, name, subscribed) VALUES ('chan-1', 'chan-1', 'Archive Workshop', 1)"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
INSERT INTO videos (id, external_id, channel_id, title, description, media_path)
VALUES ('oldvideo001', 'oldvideo001', 'chan-1', 'Already Downloaded', '', 'oldvideo001.mp4')`); err != nil {
		t.Fatal(err)
	}
	job, err := store.Enqueue(context.Background(), jobs.EnqueueParams{
		Type:        ChannelAutoDownloadJobType,
		PayloadJSON: `{"url":"https://www.youtube.com/channel/chan-1","channel_id":"chan-1"}`,
		MaxAttempts: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	firstPageIDs := numberedVideoIDs("new", 1, DefaultChannelCatalogPageSize)
	secondPageIDs := append(numberedVideoIDs("new", DefaultChannelCatalogPageSize+1, 1), "oldvideo001")
	runnerCalls := &sequenceRunner{stdout: [][]byte{
		channelCatalogPageFixture(t, "chan-1", firstPageIDs...),
		channelCatalogPageFixture(t, "chan-1", secondPageIDs...),
	}}
	runner := jobs.NewRunner(store, map[string]jobs.Handler{
		ChannelAutoDownloadJobType: newTestDownloader(db, store, Config{YTDLPPath: "yt-dlp", MediaRoot: t.TempDir()}, runnerCalls).HandleChannelAutoDownload,
	})

	if err := runner.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	stored, err := store.Get(context.Background(), job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != jobs.StatusSucceeded {
		t.Fatalf("expected succeeded auto job, got %#v", stored)
	}
	if len(runnerCalls.commands) != 2 {
		t.Fatalf("expected second page when newest page has no local overlap, got %#v", runnerCalls.commands)
	}
	assertArgValue(t, runnerCalls.commands[1].Args, "--playlist-start", "31")
	assertArgValue(t, runnerCalls.commands[1].Args, "--playlist-end", "60")
	assertScalar(t, db, "SELECT count(*) FROM jobs WHERE type = ?", int64(DefaultChannelAutoDownloadLimit), JobType)
	var result channelAutoDownloadResult
	if err := json.Unmarshal([]byte(stored.ResultJSON), &result); err != nil {
		t.Fatal(err)
	}
	if result.ChannelID != "chan-1" || result.Pages != 2 || result.DownloadsQueued != DefaultChannelAutoDownloadLimit {
		t.Fatalf("unexpected auto result: %#v", result)
	}
}

func TestChannelAutoDownloadUsesRawEntryCountForShortPageDetection(t *testing.T) {
	t.Parallel()

	db := openDownloadDB(t)
	store := jobs.NewStore(db)
	if _, err := db.Exec("INSERT INTO channels (id, external_id, name, subscribed) VALUES ('chan-1', 'chan-1', 'Archive Workshop', 1)"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
INSERT INTO videos (id, external_id, channel_id, title, description, media_path)
VALUES ('oldvideo001', 'oldvideo001', 'chan-1', 'Already Downloaded', '', 'oldvideo001.mp4')`); err != nil {
		t.Fatal(err)
	}
	job, err := store.Enqueue(context.Background(), jobs.EnqueueParams{
		Type:        ChannelAutoDownloadJobType,
		PayloadJSON: `{"url":"https://www.youtube.com/channel/chan-1","channel_id":"chan-1"}`,
		MaxAttempts: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	entries := channelCatalogEntries("chan-1", numberedVideoIDs("raw", 1, DefaultChannelCatalogPageSize-1)...)
	entries = append(entries, map[string]any{"id": "invalid", "url": "https://www.youtube.com/watch?v=invalid", "title": "Invalid"})
	runnerCalls := &sequenceRunner{stdout: [][]byte{
		channelCatalogEntriesFixture(t, "chan-1", entries),
		channelCatalogPageFixture(t, "chan-1", "oldvideo001"),
	}}
	runner := jobs.NewRunner(store, map[string]jobs.Handler{
		ChannelAutoDownloadJobType: newTestDownloader(db, store, Config{YTDLPPath: "yt-dlp", MediaRoot: t.TempDir()}, runnerCalls).HandleChannelAutoDownload,
	})

	if err := runner.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	stored, err := store.Get(context.Background(), job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != jobs.StatusSucceeded {
		t.Fatalf("expected succeeded auto job, got %#v", stored)
	}
	if len(runnerCalls.commands) != 2 {
		t.Fatalf("expected raw full page to fetch second page despite one filtered entry, got %#v", runnerCalls.commands)
	}
}

func TestChannelAutoDownloadDoesNotAdvanceScanTimeBeforeDownloadsQueued(t *testing.T) {
	t.Parallel()

	db := openDownloadDB(t)
	store := jobs.NewStore(db)
	if _, err := db.Exec("INSERT INTO channels (id, external_id, name, subscribed, last_scanned_at) VALUES ('chan-1', 'chan-1', 'Archive Workshop', 1, '')"); err != nil {
		t.Fatal(err)
	}
	job, err := store.Enqueue(context.Background(), jobs.EnqueueParams{
		Type:        ChannelAutoDownloadJobType,
		PayloadJSON: `{"url":"https://www.youtube.com/channel/chan-1","channel_id":"chan-1"}`,
		MaxAttempts: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	runnerCalls := &sequenceRunner{stdout: [][]byte{
		channelCatalogPageFixture(t, "chan-1", numberedVideoIDs("new", 1, DefaultChannelCatalogPageSize)...),
	}}
	runner := jobs.NewRunner(store, map[string]jobs.Handler{
		ChannelAutoDownloadJobType: newTestDownloader(db, store, Config{YTDLPPath: "yt-dlp", MediaRoot: t.TempDir()}, runnerCalls).HandleChannelAutoDownload,
	})

	if err := runner.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	stored, err := store.Get(context.Background(), job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != jobs.StatusFailed || !strings.Contains(stored.Error, "unexpected command") {
		t.Fatalf("expected failed auto job after second page command, got %#v", stored)
	}
	assertScalar(t, db, "SELECT last_scanned_at FROM channels WHERE id = ?", "", "chan-1")
	assertScalar(t, db, "SELECT count(*) FROM jobs WHERE type = ?", int64(0), JobType)
}

func TestChannelAutoDownloadDoesNotCompleteWhenPageLimitHasNoOverlap(t *testing.T) {
	t.Parallel()

	db := openDownloadDB(t)
	store := jobs.NewStore(db)
	if _, err := db.Exec("INSERT INTO channels (id, external_id, name, subscribed, last_scanned_at) VALUES ('chan-1', 'chan-1', 'Archive Workshop', 1, '')"); err != nil {
		t.Fatal(err)
	}
	job, err := store.Enqueue(context.Background(), jobs.EnqueueParams{
		Type:        ChannelAutoDownloadJobType,
		PayloadJSON: `{"url":"https://www.youtube.com/channel/chan-1","channel_id":"chan-1"}`,
		MaxAttempts: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	maxPages := (DefaultChannelCatalogLimit + DefaultChannelCatalogPageSize - 1) / DefaultChannelCatalogPageSize
	stdout := make([][]byte, 0, maxPages)
	nextID := 0
	for page := 0; page < maxPages; page++ {
		remaining := DefaultChannelCatalogLimit - page*DefaultChannelCatalogPageSize
		count := DefaultChannelCatalogPageSize
		if remaining < count {
			count = remaining
		}
		ids := make([]string, 0, count)
		for index := 0; index < count; index++ {
			nextID++
			ids = append(ids, fmt.Sprintf("video%06d", nextID))
		}
		stdout = append(stdout, channelCatalogPageFixture(t, "chan-1", ids...))
	}
	runnerCalls := &sequenceRunner{stdout: stdout}
	runner := jobs.NewRunner(store, map[string]jobs.Handler{
		ChannelAutoDownloadJobType: newTestDownloader(db, store, Config{YTDLPPath: "yt-dlp", MediaRoot: t.TempDir()}, runnerCalls).HandleChannelAutoDownload,
	})

	if err := runner.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	stored, err := store.Get(context.Background(), job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != jobs.StatusSucceeded {
		t.Fatalf("expected capped auto job to succeed with incomplete result, got %#v", stored)
	}
	assertScalar(t, db, "SELECT last_scanned_at FROM channels WHERE id = ?", "", "chan-1")
	assertScalar(t, db, "SELECT count(*) FROM jobs WHERE type = ?", int64(DefaultChannelAutoDownloadLimit), JobType)
	var result channelAutoDownloadResult
	if err := json.Unmarshal([]byte(stored.ResultJSON), &result); err != nil {
		t.Fatal(err)
	}
	if !result.Incomplete || result.DownloadsQueued != DefaultChannelAutoDownloadLimit {
		t.Fatalf("expected incomplete capped result with queued downloads, got %#v", result)
	}
}

func TestChannelAutoDownloadCompletesShortFinalLimitPage(t *testing.T) {
	t.Parallel()

	db := openDownloadDB(t)
	store := jobs.NewStore(db)
	if _, err := db.Exec("INSERT INTO channels (id, external_id, name, subscribed, last_scanned_at) VALUES ('chan-1', 'chan-1', 'Archive Workshop', 1, '')"); err != nil {
		t.Fatal(err)
	}
	job, err := store.Enqueue(context.Background(), jobs.EnqueueParams{
		Type:        ChannelAutoDownloadJobType,
		PayloadJSON: `{"url":"https://www.youtube.com/channel/chan-1","channel_id":"chan-1"}`,
		MaxAttempts: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	maxPages := (DefaultChannelCatalogLimit + DefaultChannelCatalogPageSize - 1) / DefaultChannelCatalogPageSize
	stdout := make([][]byte, 0, maxPages)
	nextID := 0
	for page := 0; page < maxPages; page++ {
		remaining := DefaultChannelCatalogLimit - 1 - page*DefaultChannelCatalogPageSize
		count := DefaultChannelCatalogPageSize
		if remaining < count {
			count = remaining
		}
		ids := make([]string, 0, count)
		for index := 0; index < count; index++ {
			nextID++
			ids = append(ids, fmt.Sprintf("short%06d", nextID))
		}
		stdout = append(stdout, channelCatalogPageFixture(t, "chan-1", ids...))
	}
	runnerCalls := &sequenceRunner{stdout: stdout}
	runner := jobs.NewRunner(store, map[string]jobs.Handler{
		ChannelAutoDownloadJobType: newTestDownloader(db, store, Config{YTDLPPath: "yt-dlp", MediaRoot: t.TempDir()}, runnerCalls).HandleChannelAutoDownload,
	})

	if err := runner.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	stored, err := store.Get(context.Background(), job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != jobs.StatusSucceeded {
		t.Fatalf("expected succeeded auto job, got %#v", stored)
	}
	assertScalar(t, db, "SELECT last_scanned_at <> '' FROM channels WHERE id = ?", int64(1), "chan-1")
	var result channelAutoDownloadResult
	if err := json.Unmarshal([]byte(stored.ResultJSON), &result); err != nil {
		t.Fatal(err)
	}
	if result.Incomplete || result.DownloadsQueued != DefaultChannelAutoDownloadLimit {
		t.Fatalf("expected complete short final page result, got %#v", result)
	}
}

func TestAutoDownloadRetentionRemovesOnlyStaleUnstartedAutoMedia(t *testing.T) {
	t.Parallel()

	db := openDownloadDB(t)
	mediaRoot := writeDownloadFiles(t,
		"videos/latest-one.mp4",
		"videos/latest-two.mp4",
		"videos/stale-auto.mp4",
		"videos/keep-forever.mp4",
		"videos/started-auto.mp4",
		"videos/watched-auto.mp4",
		"videos/progress-watched-auto.mp4",
		"videos/manual-old.mp4",
		"videos/imported-old.mp4",
	)
	if _, err := db.Exec("INSERT INTO channels (id, external_id, name, subscribed) VALUES ('chan-1', 'chan-1', 'Archive Workshop', 1)"); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 5, 21, 12, 0, 0, 0, time.UTC)
	old := now.Add(-15 * 24 * time.Hour).Format(time.RFC3339Nano)
	recentWatchedAt := now.Add(-23 * time.Hour).Format(time.RFC3339Nano)
	seedRetentionVideo(t, db, "latestone01", "2026-05-05", "videos/latest-one.mp4", DownloadOriginChannelAuto, old)
	seedRetentionVideo(t, db, "latesttwo02", "2026-05-04", "videos/latest-two.mp4", DownloadOriginChannelAuto, old)
	seedRetentionVideo(t, db, "staleauto03", "2026-05-03", "videos/stale-auto.mp4", DownloadOriginChannelAuto, old)
	seedRetentionVideo(t, db, "keepauto04", "2026-05-02", "videos/keep-forever.mp4", DownloadOriginChannelAuto, old)
	seedRetentionVideo(t, db, "startauto05", "2026-05-01", "videos/started-auto.mp4", DownloadOriginChannelAuto, old)
	seedRetentionVideo(t, db, "watchauto7", "2026-04-30", "videos/watched-auto.mp4", DownloadOriginChannelAuto, old)
	seedRetentionVideo(t, db, "doneprog08", "2026-04-29", "videos/progress-watched-auto.mp4", DownloadOriginChannelAuto, old)
	seedRetentionVideo(t, db, "manualold06", "2026-04-30", "videos/manual-old.mp4", DownloadOriginManual, old)
	seedImportedRetentionVideo(t, db, "importold7", "2026-04-29", "videos/imported-old.mp4", old)
	if _, err := db.Exec(`
INSERT INTO downloads (video_id, source, external_id, url, status, origin, payload_json, created_at, updated_at)
VALUES ('importold7', 'youtube', 'importold7', ?, 'succeeded', ?, '{}', ?, ?)`, youtubeWatchURL("importold7"), DownloadOriginChannelAuto, old, old); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("UPDATE videos SET keep_forever = 1 WHERE id = 'keepauto04'"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO user_progress (video_id, position_seconds, duration_seconds, watched, updated_at) VALUES ('startauto05', 12, 120, 0, ?)", old); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("UPDATE videos SET watched = 1, updated_at = ? WHERE id = 'watchauto7'", recentWatchedAt); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO user_progress (video_id, position_seconds, duration_seconds, watched, updated_at) VALUES ('doneprog08', 120, 120, 1, ?)", recentWatchedAt); err != nil {
		t.Fatal(err)
	}

	result, err := NewDownloader(db, Config{MediaRoot: mediaRoot}, nil).ApplyAutoDownloadRetention(context.Background(), RetentionOptions{
		Now:        func() time.Time { return now },
		StaleAfter: 14 * 24 * time.Hour,
		Limit:      20,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Removed != 1 || result.Checked != 1 {
		t.Fatalf("expected one stale auto removal, got %#v", result)
	}

	assertScalar(t, db, "SELECT count(*) FROM videos WHERE id = ?", int64(1), "staleauto03")
	assertScalar(t, db, "SELECT title FROM videos WHERE id = ?", "Video staleauto03", "staleauto03")
	assertScalar(t, db, "SELECT media_path FROM videos WHERE id = ?", "", "staleauto03")
	assertScalar(t, db, "SELECT media_origin FROM videos WHERE id = ?", MediaOriginImported, "staleauto03")
	assertScalar(t, db, "SELECT media_downloaded_at FROM videos WHERE id = ?", "", "staleauto03")
	assertScalar(t, db, "SELECT count(*) FROM media_assets WHERE owner_type = 'video' AND owner_id = ? AND kind = 'media'", int64(0), "staleauto03")
	if _, err := os.Stat(filepath.Join(mediaRoot, "videos", "stale-auto.mp4")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected stale media file to be removed, got %v", err)
	}
	needed, err := NewDownloader(db, Config{MediaRoot: mediaRoot}, nil).catalogDownloadNeeded(context.Background(), db, "staleauto03", Payload{URL: "https://www.youtube.com/watch?v=staleauto03", Origin: DownloadOriginChannelAuto})
	if err != nil {
		t.Fatal(err)
	}
	if needed {
		t.Fatal("expected retained-out auto download not to be queued again by channel auto sync")
	}
	for _, id := range []string{"latestone01", "latesttwo02", "keepauto04", "startauto05", "watchauto7", "doneprog08", "manualold06", "importold7"} {
		assertScalar(t, db, "SELECT media_path <> '' FROM videos WHERE id = ?", int64(1), id)
	}
	if _, err := os.Stat(filepath.Join(mediaRoot, "videos", "keep-forever.mp4")); err != nil {
		t.Fatalf("expected keep-forever media file to remain, got %v", err)
	}
}

func TestAutoDownloadRetentionRemovesWatchedAutoMediaAfterOneDay(t *testing.T) {
	t.Parallel()

	db := openDownloadDB(t)
	mediaRoot := writeDownloadFiles(t,
		"videos/recent-watched.mp4",
		"videos/old-video-watched.mp4",
		"videos/old-progress-watched.mp4",
		"videos/old-keep-watched.mp4",
		"videos/manual-watched.mp4",
		"videos/imported-watched.mp4",
	)
	if _, err := db.Exec("INSERT INTO channels (id, external_id, name, subscribed) VALUES ('chan-1', 'chan-1', 'Archive Workshop', 1)"); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 5, 21, 12, 0, 0, 0, time.UTC)
	downloadedAt := now.Add(-48 * time.Hour).Format(time.RFC3339Nano)
	recentWatchedAt := now.Add(-23 * time.Hour).Format(time.RFC3339Nano)
	oldWatchedAt := now.Add(-25 * time.Hour).Format(time.RFC3339Nano)
	seedRetentionVideo(t, db, "recentwatch", "2026-05-05", "videos/recent-watched.mp4", DownloadOriginChannelAuto, downloadedAt)
	seedRetentionVideo(t, db, "oldvwatch1", "2026-05-04", "videos/old-video-watched.mp4", DownloadOriginChannelAuto, downloadedAt)
	seedRetentionVideo(t, db, "oldpwatch2", "2026-05-03", "videos/old-progress-watched.mp4", DownloadOriginChannelAuto, downloadedAt)
	seedRetentionVideo(t, db, "keepwatch3", "2026-05-02", "videos/old-keep-watched.mp4", DownloadOriginChannelAuto, downloadedAt)
	seedRetentionVideo(t, db, "manualwat4", "2026-05-01", "videos/manual-watched.mp4", DownloadOriginManual, downloadedAt)
	seedImportedRetentionVideo(t, db, "importwat5", "2026-04-30", "videos/imported-watched.mp4", downloadedAt)
	if _, err := db.Exec("UPDATE videos SET watched = 1, updated_at = ? WHERE id IN ('recentwatch', 'oldvwatch1', 'keepwatch3', 'manualwat4', 'importwat5')", oldWatchedAt); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("UPDATE videos SET updated_at = ? WHERE id = 'recentwatch'", recentWatchedAt); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO user_progress (video_id, position_seconds, duration_seconds, watched, updated_at) VALUES ('oldpwatch2', 120, 120, 1, ?)", oldWatchedAt); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("UPDATE videos SET keep_forever = 1 WHERE id = 'keepwatch3'"); err != nil {
		t.Fatal(err)
	}

	result, err := NewDownloader(db, Config{MediaRoot: mediaRoot}, nil).ApplyAutoDownloadRetention(context.Background(), RetentionOptions{Now: func() time.Time { return now }, Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	if result.Removed != 2 || result.Checked != 2 {
		t.Fatalf("expected two old watched auto removals, got %#v", result)
	}
	for _, id := range []string{"oldvwatch1", "oldpwatch2"} {
		assertScalar(t, db, "SELECT media_path FROM videos WHERE id = ?", "", id)
		assertScalar(t, db, "SELECT media_origin FROM videos WHERE id = ?", MediaOriginImported, id)
	}
	for _, path := range []string{"old-video-watched.mp4", "old-progress-watched.mp4"} {
		if _, err := os.Stat(filepath.Join(mediaRoot, "videos", path)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("expected watched media file %s to be removed, got %v", path, err)
		}
	}
	for _, id := range []string{"recentwatch", "keepwatch3", "manualwat4", "importwat5"} {
		assertScalar(t, db, "SELECT media_path <> '' FROM videos WHERE id = ?", int64(1), id)
	}
}

func TestAutoDownloadRetentionRechecksWatchedCutoffBeforeDelete(t *testing.T) {
	t.Parallel()

	db := openDownloadDB(t)
	mediaRoot := writeDownloadFiles(t, "videos/rewatched.mp4")
	if _, err := db.Exec("INSERT INTO channels (id, external_id, name, subscribed) VALUES ('chan-1', 'chan-1', 'Archive Workshop', 1)"); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 5, 21, 12, 0, 0, 0, time.UTC)
	downloadedAt := now.Add(-48 * time.Hour).Format(time.RFC3339Nano)
	oldWatchedAt := now.Add(-25 * time.Hour).Format(time.RFC3339Nano)
	recentWatchedAt := now.Add(-time.Hour).Format(time.RFC3339Nano)
	seedRetentionVideo(t, db, "rewatched1", "2026-05-05", "videos/rewatched.mp4", DownloadOriginChannelAuto, downloadedAt)
	if _, err := db.Exec("INSERT INTO user_progress (video_id, position_seconds, duration_seconds, watched, updated_at) VALUES ('rewatched1', 120, 120, 1, ?)", oldWatchedAt); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("UPDATE user_progress SET updated_at = ? WHERE video_id = 'rewatched1'", recentWatchedAt); err != nil {
		t.Fatal(err)
	}

	removed, err := NewRetentionCleaner(db, mediaRoot).removeRetainedVideoMedia(context.Background(), retentionCandidate{
		VideoID:       "rewatched1",
		MediaPath:     "videos/rewatched.mp4",
		DownloadedAt:  downloadedAt,
		StaleCutoff:   now.Add(-DefaultRetentionStaleAfter).Format(time.RFC3339Nano),
		WatchedCutoff: now.Add(-DefaultRetentionWatchedAfter).Format(time.RFC3339Nano),
	})
	if err != nil {
		t.Fatal(err)
	}
	if removed {
		t.Fatal("expected recently rewatched media to be skipped")
	}
	assertScalar(t, db, "SELECT media_path FROM videos WHERE id = ?", "videos/rewatched.mp4", "rewatched1")
	assertScalar(t, db, "SELECT media_origin FROM videos WHERE id = ?", DownloadOriginChannelAuto, "rewatched1")
	if _, err := os.Stat(filepath.Join(mediaRoot, "videos", "rewatched.mp4")); err != nil {
		t.Fatalf("expected rewatched media file to remain, got %v", err)
	}
}

func TestAutoDownloadRetentionSkipsCandidateMarkedKeepForeverBeforeDelete(t *testing.T) {
	t.Parallel()

	db := openDownloadDB(t)
	mediaRoot := writeDownloadFiles(t, "videos/protected.mp4")
	if _, err := db.Exec("INSERT INTO channels (id, external_id, name, subscribed) VALUES ('chan-1', 'chan-1', 'Archive Workshop', 1)"); err != nil {
		t.Fatal(err)
	}
	old := time.Now().UTC().Add(-30 * 24 * time.Hour).Format(time.RFC3339Nano)
	seedRetentionVideo(t, db, "protected01", "2026-05-01", "videos/protected.mp4", DownloadOriginChannelAuto, old)
	if _, err := db.Exec("UPDATE videos SET keep_forever = 1 WHERE id = 'protected01'"); err != nil {
		t.Fatal(err)
	}

	removed, err := NewRetentionCleaner(db, mediaRoot).removeRetainedVideoMedia(context.Background(), retentionCandidate{VideoID: "protected01", MediaPath: "videos/protected.mp4", DownloadedAt: old})
	if err != nil {
		t.Fatal(err)
	}
	if removed {
		t.Fatal("expected keep-forever candidate to be skipped")
	}
	assertScalar(t, db, "SELECT media_path FROM videos WHERE id = ?", "videos/protected.mp4", "protected01")
	assertScalar(t, db, "SELECT media_origin FROM videos WHERE id = ?", DownloadOriginChannelAuto, "protected01")
	if _, err := os.Stat(filepath.Join(mediaRoot, "videos", "protected.mp4")); err != nil {
		t.Fatalf("expected protected media file to remain, got %v", err)
	}
}

func TestAutoDownloadRetentionRechecksMediaOwnershipBeforeDelete(t *testing.T) {
	t.Parallel()

	db := openDownloadDB(t)
	mediaRoot := writeDownloadFiles(t, "videos/manual-now.mp4")
	if _, err := db.Exec("INSERT INTO channels (id, external_id, name, subscribed) VALUES ('chan-1', 'chan-1', 'Archive Workshop', 1)"); err != nil {
		t.Fatal(err)
	}
	old := time.Now().UTC().Add(-30 * 24 * time.Hour).Format(time.RFC3339Nano)
	seedRetentionVideo(t, db, "manualnow1", "2026-05-01", "videos/manual-now.mp4", DownloadOriginChannelAuto, old)
	if _, err := db.Exec("UPDATE videos SET media_origin = ?, media_downloaded_at = ? WHERE id = ?", DownloadOriginManual, time.Now().UTC().Format(time.RFC3339Nano), "manualnow1"); err != nil {
		t.Fatal(err)
	}

	removed, err := NewRetentionCleaner(db, mediaRoot).removeRetainedVideoMedia(context.Background(), retentionCandidate{VideoID: "manualnow1", MediaPath: "videos/manual-now.mp4", DownloadedAt: old})
	if err != nil {
		t.Fatal(err)
	}
	if removed {
		t.Fatal("expected changed media ownership to skip retention removal")
	}
	assertScalar(t, db, "SELECT media_path FROM videos WHERE id = ?", "videos/manual-now.mp4", "manualnow1")
	assertScalar(t, db, "SELECT media_origin FROM videos WHERE id = ?", DownloadOriginManual, "manualnow1")
	if _, err := os.Stat(filepath.Join(mediaRoot, "videos", "manual-now.mp4")); err != nil {
		t.Fatalf("expected manual media file to remain, got %v", err)
	}
}

func TestRetentionSchedulerEnqueuesObservableCleanupJob(t *testing.T) {
	t.Parallel()

	db := openDownloadDB(t)
	store := jobs.NewStore(db)
	now := time.Date(2026, 5, 21, 12, 0, 0, 0, time.UTC)
	created, err := EnsureRetentionJobs(context.Background(), db, store, RetentionScheduleOptions{Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	if created != 1 {
		t.Fatalf("expected one retention job, got %d", created)
	}
	created, err = EnsureRetentionJobs(context.Background(), db, store, RetentionScheduleOptions{Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	if created != 0 {
		t.Fatalf("expected active retention job to suppress duplicates, got %d", created)
	}
	items, err := store.List(context.Background(), jobs.ListOptions{Statuses: []jobs.Status{jobs.StatusQueued}, PageSize: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(items.Jobs) != 1 || items.Jobs[0].Type != RetentionJobType {
		t.Fatalf("expected observable queued retention job, got %#v", items.Jobs)
	}
}

func TestRetentionSchedulerIgnoresCancelRequestedActiveJobAfterInterval(t *testing.T) {
	t.Parallel()

	db := openDownloadDB(t)
	store := jobs.NewStore(db)
	active, err := store.Enqueue(context.Background(), jobs.EnqueueParams{Type: RetentionJobType, PayloadJSON: `{}`, MaxAttempts: 1})
	if err != nil {
		t.Fatal(err)
	}
	claimed, ok, err := store.Claim(context.Background(), time.Now().Add(time.Second), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if !ok || claimed.ID != active.ID {
		t.Fatalf("expected active retention claim, ok=%v job=%#v", ok, claimed)
	}
	if err := store.Cancel(context.Background(), active.ID); err != nil {
		t.Fatal(err)
	}
	createdAt, err := time.Parse(time.RFC3339Nano, active.CreatedAt)
	if err != nil {
		t.Fatal(err)
	}

	created, err := EnsureRetentionJobs(context.Background(), db, store, RetentionScheduleOptions{
		Now: func() time.Time { return createdAt.Add(DefaultRetentionInterval + time.Hour) },
	})
	if err != nil {
		t.Fatal(err)
	}
	if created != 1 {
		t.Fatalf("expected cancel-requested active job not to suppress replacement after interval, got %d", created)
	}
	assertScalar(t, db, "SELECT count(*) FROM jobs WHERE type = ?", int64(2), RetentionJobType)
}

func TestRetentionJobRecordsCleanupResult(t *testing.T) {
	t.Parallel()

	db := openDownloadDB(t)
	store := jobs.NewStore(db)
	mediaRoot := writeDownloadFiles(t, "videos/keep-one.mp4", "videos/keep-two.mp4", "videos/remove-me.mp4")
	if _, err := db.Exec("INSERT INTO channels (id, external_id, name, subscribed) VALUES ('chan-1', 'chan-1', 'Archive Workshop', 1)"); err != nil {
		t.Fatal(err)
	}
	old := time.Now().UTC().Add(-30 * 24 * time.Hour).Format(time.RFC3339Nano)
	seedRetentionVideo(t, db, "keep-one", "2026-05-05", "videos/keep-one.mp4", DownloadOriginChannelAuto, old)
	seedRetentionVideo(t, db, "keep-two", "2026-05-04", "videos/keep-two.mp4", DownloadOriginChannelAuto, old)
	seedRetentionVideo(t, db, "remove-me", "2026-05-03", "videos/remove-me.mp4", DownloadOriginChannelAuto, old)
	job, err := store.Enqueue(context.Background(), jobs.EnqueueParams{Type: RetentionJobType, PayloadJSON: `{}`, MaxAttempts: 1, RunAfter: time.Now().Add(-time.Second)})
	if err != nil {
		t.Fatal(err)
	}
	runner := jobs.NewRunner(store, map[string]jobs.Handler{
		RetentionJobType: newTestDownloader(db, store, Config{MediaRoot: mediaRoot}, nil).HandleRetention,
	})

	if err := runner.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	stored, err := store.Get(context.Background(), job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != jobs.StatusSucceeded {
		t.Fatalf("expected succeeded retention job, got %#v", stored)
	}
	var result RetentionResult
	if err := json.Unmarshal([]byte(stored.ResultJSON), &result); err != nil {
		t.Fatal(err)
	}
	if result.Removed != 1 || !slices.Contains(result.RemovedVideoIDs, "remove-me") {
		t.Fatalf("expected observable retention result for removed media, got %#v", result)
	}
}

func assertArgValue(t *testing.T, args []string, flag string, expected string) {
	t.Helper()

	for index, arg := range args {
		if arg != flag {
			continue
		}
		if index+1 >= len(args) {
			t.Fatalf("expected value after %s in %#v", flag, args)
		}
		if args[index+1] != expected {
			t.Fatalf("expected %s %q, got %q in %#v", flag, expected, args[index+1], args)
		}
		return
	}

	t.Fatalf("expected args to include %s in %#v", flag, args)
}

type fakeRunner struct {
	stdout []byte
	err    error
}

func (f fakeRunner) Run(context.Context, Command) ([]byte, error) {
	return f.stdout, f.err
}

type progressRunner struct {
	progress []float64
	err      error
}

func (r progressRunner) Run(_ context.Context, command Command) ([]byte, error) {
	if command.Progress == nil {
		return nil, errors.New("missing progress callback")
	}
	for _, progress := range r.progress {
		if err := command.Progress(progress); err != nil {
			return nil, err
		}
	}

	return []byte("ERROR: download failed"), r.err
}

type progressMetadataRunner struct {
	progress []float64
	stdout   []byte
}

func (r progressMetadataRunner) Run(_ context.Context, command Command) ([]byte, error) {
	if command.Progress == nil {
		return nil, errors.New("missing progress callback")
	}
	for _, progress := range r.progress {
		if err := command.Progress(progress); err != nil {
			return nil, err
		}
	}

	return r.stdout, nil
}

type recordingRunner struct {
	called bool
}

func (r *recordingRunner) Run(context.Context, Command) ([]byte, error) {
	r.called = true
	return nil, errors.New("unexpected call")
}

type sequenceRunner struct {
	stdout   [][]byte
	errs     []error
	err      error
	commands []Command
}

type hookRunner struct {
	stdout   [][]byte
	afterRun func()
	commands []Command
}

type fakePreviewRunner struct {
	commands []previews.Command
	err      error
}

func (r *fakePreviewRunner) Run(_ context.Context, command previews.Command) error {
	r.commands = append(r.commands, command)
	if r.err != nil {
		return r.err
	}
	output := command.Args[len(command.Args)-1]
	if err := os.MkdirAll(filepath.Dir(output), 0o755); err != nil {
		return err
	}

	return os.WriteFile(output, []byte("sprite"), 0o644)
}

type timeoutRunner struct{}

func (timeoutRunner) Run(ctx context.Context, _ Command) ([]byte, error) {
	<-ctx.Done()

	return nil, errors.New("signal: killed")
}

func (r *sequenceRunner) Run(_ context.Context, command Command) ([]byte, error) {
	r.commands = append(r.commands, command)
	if r.err != nil {
		return nil, r.err
	}
	if len(r.stdout) == 0 {
		return nil, errors.New("unexpected command")
	}
	output := r.stdout[0]
	r.stdout = r.stdout[1:]
	if len(r.errs) > 0 {
		err := r.errs[0]
		r.errs = r.errs[1:]
		if err != nil {
			return output, err
		}
	}

	return output, nil
}

func (r *hookRunner) Run(_ context.Context, command Command) ([]byte, error) {
	r.commands = append(r.commands, command)
	if len(r.stdout) == 0 {
		return nil, errors.New("unexpected command")
	}
	output := r.stdout[0]
	r.stdout = r.stdout[1:]
	if r.afterRun != nil {
		r.afterRun()
	}

	return output, nil
}

func openDownloadDB(t *testing.T) *sql.DB {
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

func newTestDownloader(db *sql.DB, store *jobs.Store, config Config, runner Runner) *Downloader {
	config.JobStore = store

	return NewDownloader(db, config, runner)
}

func assertScalar[T comparable](t *testing.T, db *sql.DB, query string, expected T, args ...any) {
	t.Helper()

	var got T
	if err := db.QueryRow(query, args...).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != expected {
		t.Fatalf("expected %v, got %v", expected, got)
	}
}

func assertNoDownloadedVideoRows(t *testing.T, db *sql.DB) {
	t.Helper()

	assertScalar(t, db, "SELECT count(*) FROM videos", int64(0))
	assertScalar(t, db, "SELECT count(*) FROM downloads", int64(0))
	assertScalar(t, db, "SELECT count(*) FROM media_assets", int64(0))
}

func runDownloadJobWithOutput(t *testing.T, db *sql.DB, store *jobs.Store, mediaRoot string, output []byte) jobs.Job {
	t.Helper()

	job := enqueueDownloadJob(t, store)
	runner := jobs.NewRunner(store, map[string]jobs.Handler{
		JobType: newTestDownloader(db, store, Config{YTDLPPath: "yt-dlp", MediaRoot: mediaRoot}, fakeRunner{stdout: output}).Handle,
	})
	if err := runner.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}

	stored, err := store.Get(context.Background(), job.ID)
	if err != nil {
		t.Fatal(err)
	}

	return stored
}

func enqueueDownloadJob(t *testing.T, store *jobs.Store) jobs.Job {
	t.Helper()

	job, err := store.Enqueue(context.Background(), jobs.EnqueueParams{
		Type:        JobType,
		PayloadJSON: `{"url":"https://www.youtube.com/watch?v=abc123DEF45"}`,
		MaxAttempts: 1,
		RunAfter:    time.Now().Add(-time.Second),
	})
	if err != nil {
		t.Fatal(err)
	}

	return job
}

func assertJobResultAction(t *testing.T, store *jobs.Store, jobID string, expected string) {
	t.Helper()

	assertJobResultActionForVideo(t, store, jobID, "vid-1", expected)
}

func assertJobResultActionForVideo(t *testing.T, store *jobs.Store, jobID string, expectedVideoID string, expected string) {
	t.Helper()

	job, err := store.Get(context.Background(), jobID)
	if err != nil {
		t.Fatal(err)
	}
	var result struct {
		VideoID string `json:"video_id"`
		Action  string `json:"action"`
	}
	if err := json.Unmarshal([]byte(job.ResultJSON), &result); err != nil {
		t.Fatal(err)
	}
	if result.VideoID != expectedVideoID || result.Action != expected {
		t.Fatalf("expected job result action %q for %s, got %#v from %q", expected, expectedVideoID, result, job.ResultJSON)
	}
}

func assertPreviewJobPayload(t *testing.T, db *sql.DB, expectedVideoID string) {
	t.Helper()

	var payloadJSON string
	if err := db.QueryRow("SELECT payload_json FROM jobs WHERE type = ?", previews.JobType).Scan(&payloadJSON); err != nil {
		t.Fatal(err)
	}
	var payload previews.Payload
	if err := json.Unmarshal([]byte(payloadJSON), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.VideoID != expectedVideoID {
		t.Fatalf("expected preview job for %q, got %#v from %q", expectedVideoID, payload, payloadJSON)
	}
}

func runFailedValidationJob(t *testing.T, metadata []byte, mediaRoot string) (jobs.Job, *sql.DB) {
	t.Helper()

	db := openDownloadDB(t)
	store := jobs.NewStore(db)
	job, err := store.Enqueue(context.Background(), jobs.EnqueueParams{
		Type:        JobType,
		PayloadJSON: `{"url":"https://www.youtube.com/watch?v=abc123DEF45"}`,
		MaxAttempts: 1,
		RunAfter:    time.Now().Add(-time.Second),
	})
	if err != nil {
		t.Fatal(err)
	}
	runner := jobs.NewRunner(store, map[string]jobs.Handler{
		JobType: newTestDownloader(db, store, Config{YTDLPPath: "yt-dlp", MediaRoot: mediaRoot}, fakeRunner{stdout: metadata}).Handle,
	})
	if err := runner.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}

	stored, err := store.Get(context.Background(), job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != jobs.StatusFailed {
		t.Fatalf("expected failed validation job, got %#v", stored)
	}

	return stored, db
}

func writeDownloadFiles(t *testing.T, paths ...string) string {
	t.Helper()

	mediaRoot := t.TempDir()
	for _, path := range paths {
		target := filepath.Join(mediaRoot, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(target, []byte("media"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	return mediaRoot
}

func seedRetentionVideo(t *testing.T, db *sql.DB, id string, publishedAt string, mediaPath string, origin string, downloadedAt string) {
	t.Helper()
	if _, err := db.Exec(`
	INSERT INTO videos (id, external_id, channel_id, title, published_at, duration_seconds, media_path, media_origin, media_downloaded_at, archived_at)
VALUES (?, ?, 'chan-1', ?, ?, 120, ?, ?, ?, ?)`, id, id, "Video "+id, publishedAt, mediaPath, origin, downloadedAt, downloadedAt); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
INSERT INTO media_assets (owner_type, owner_id, kind, path)
VALUES ('video', ?, 'media', ?)`, id, mediaPath); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
INSERT INTO downloads (video_id, source, external_id, url, status, origin, payload_json, created_at, updated_at)
VALUES (?, 'youtube', ?, ?, 'succeeded', ?, '{}', ?, ?)`, id, id, youtubeWatchURL(id), origin, downloadedAt, downloadedAt); err != nil {
		t.Fatal(err)
	}
}

func seedImportedRetentionVideo(t *testing.T, db *sql.DB, id string, publishedAt string, mediaPath string, importedAt string) {
	t.Helper()
	if _, err := db.Exec(`
INSERT INTO videos (id, external_id, channel_id, title, published_at, duration_seconds, media_path, media_origin, media_downloaded_at, archived_at)
VALUES (?, ?, 'chan-1', ?, ?, 120, ?, ?, ?, ?)`, id, id, "Video "+id, publishedAt, mediaPath, MediaOriginImported, importedAt, importedAt); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
INSERT INTO media_assets (owner_type, owner_id, kind, path)
VALUES ('video', ?, 'media', ?)`, id, mediaPath); err != nil {
		t.Fatal(err)
	}
}

func fixtureMetadataWithPaths(t *testing.T, mediaPath string, thumbnailPath string) []byte {
	t.Helper()

	return fixtureMetadataWithIDAndTitle(t, "vid-1", "Kapsel Demo", mediaPath, thumbnailPath)
}

func fixtureMetadataWithIDAndTitle(t *testing.T, id string, title string, mediaPath string, thumbnailPaths ...string) []byte {
	t.Helper()

	return fixtureMetadataWithDescription(t, id, title, "A downloaded demo", mediaPath, thumbnailPaths...)
}

func fixtureMetadataWithDescription(t *testing.T, id string, title string, description string, mediaPath string, thumbnailPaths ...string) []byte {
	t.Helper()

	thumbnailPath := ""
	if len(thumbnailPaths) > 0 {
		thumbnailPath = thumbnailPaths[0]
	}
	body, err := json.Marshal(map[string]any{
		"id":             id,
		"title":          title,
		"description":    description,
		"duration":       120,
		"upload_date":    "20260503",
		"channel_id":     "chan-1",
		"channel":        "Archive Workshop",
		"webpage_url":    "https://example.com/watch?v=abc",
		"filepath":       mediaPath,
		"thumbnail_path": thumbnailPath,
		"requested_downloads": []map[string]string{
			{"filepath": mediaPath},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	return body
}

func fixtureMetadataWithSubtitle(t *testing.T) []byte {
	t.Helper()

	var body map[string]any
	if err := json.Unmarshal(fixtureMetadata, &body); err != nil {
		t.Fatal(err)
	}
	body["requested_subtitles"] = map[string]any{
		"en": map[string]any{"filepath": "vid-1.en.vtt", "ext": "vtt", "name": "English"},
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}

	return encoded
}

func fixtureMetadataWithSubtitleAndAutomaticOriginal(t *testing.T) []byte {
	t.Helper()

	var body map[string]any
	if err := json.Unmarshal(fixtureMetadata, &body); err != nil {
		t.Fatal(err)
	}
	body["requested_subtitles"] = map[string]any{
		"en-CA": map[string]any{"filepath": "vid-1.en-CA.vtt", "ext": "vtt", "name": "English (Canada)"},
	}
	body["automatic_captions"] = map[string]any{
		"en-orig":    []map[string]any{{"ext": "vtt", "name": "English (Original)"}},
		"en-zh-Hant": []map[string]any{{"ext": "vtt", "name": "English from Chinese (Traditional)"}},
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}

	return encoded
}

func fixtureMetadataWithAutomaticOriginalSubtitle(t *testing.T) []byte {
	t.Helper()

	var body map[string]any
	if err := json.Unmarshal(fixtureMetadata, &body); err != nil {
		t.Fatal(err)
	}
	body["requested_subtitles"] = map[string]any{
		"en-orig": map[string]any{"filepath": "vid-1.en-orig.vtt", "ext": "vtt", "name": "English (Original)"},
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}

	return encoded
}

func fixtureMetadataWithAutomaticTranslationOnly(t *testing.T) []byte {
	t.Helper()

	var body map[string]any
	if err := json.Unmarshal(fixtureMetadata, &body); err != nil {
		t.Fatal(err)
	}
	body["automatic_captions"] = map[string]any{
		"en-zh-Hant": []map[string]any{{"ext": "vtt", "name": "English from Chinese (Traditional)"}},
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}

	return encoded
}

func channelCatalogPageFixture(t *testing.T, channelID string, ids ...string) []byte {
	t.Helper()

	return channelCatalogEntriesFixture(t, channelID, channelCatalogEntries(channelID, ids...))
}

func channelCatalogEntries(channelID string, ids ...string) []map[string]any {
	entries := make([]map[string]any, 0, len(ids))
	for _, id := range ids {
		entries = append(entries, map[string]any{
			"id":         id,
			"url":        "https://www.youtube.com/watch?v=" + id,
			"title":      "Video " + id,
			"channel_id": channelID,
			"channel":    "Archive Workshop",
		})
	}

	return entries
}

func channelCatalogEntriesFixture(t *testing.T, channelID string, entries []map[string]any) []byte {
	t.Helper()

	body, err := json.Marshal(map[string]any{
		"id":         channelID,
		"channel_id": channelID,
		"channel":    "Archive Workshop",
		"entries":    entries,
	})
	if err != nil {
		t.Fatal(err)
	}

	return body
}

func numberedVideoIDs(prefix string, start int, count int) []string {
	ids := make([]string, 0, count)
	for index := 0; index < count; index++ {
		ids = append(ids, fmt.Sprintf("%s%08d", prefix, start+index))
	}

	return ids
}

func writeDownloadFile(t *testing.T, mediaRoot string, name string, body string) {
	t.Helper()

	path := filepath.Join(mediaRoot, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

var metadataScanFixture = []byte(`{
  "id": "abc123DEF45",
  "title": "Kapsel Demo",
  "description": "A downloaded demo",
  "duration": 120,
  "view_count": 1234,
  "upload_date": "20260503",
  "channel_id": "chan-1",
  "channel": "Archive Workshop",
  "webpage_url": "https://www.youtube.com/watch?v=abc123DEF45",
  "thumbnail": "https://example.com/thumb.jpg"
}`)

var fixtureMetadata = []byte(`{
  "id": "vid-1",
  "title": "Kapsel Demo",
  "description": "A downloaded demo",
  "duration": 120,
  "view_count": 1234,
  "upload_date": "20260503",
  "channel_id": "chan-1",
  "channel": "Archive Workshop",
  "webpage_url": "https://example.com/watch?v=abc",
  "filepath": "vid-1.mp4",
  "thumbnail_path": "vid-1.jpg",
  "requested_downloads": [
    {"filepath": "vid-1.mp4"}
  ]
}`)

var channelFixtureMetadata = []byte(`{
  "id": "archive-channel",
  "title": "Archive Workshop",
  "entries": [
    {"id": "abc123DEF45", "url": "https://www.youtube.com/watch?v=abc123DEF45", "title": "Kapsel Demo"},
    {"id": "vid-2", "url": "https://www.youtube.com/watch?v=vid-2", "title": "Second Video"}
  ]
}`)

var catalogSyncFixtureMetadata = []byte(`{
  "id": "chan-1",
  "channel_id": "chan-1",
  "channel": "Archive Workshop",
  "description": "A channel about local archives\nUpdated weekly",
  "thumbnail": "https://yt3.ggpht.com/archive-workshop.jpg",
  "entries": [
    {"id": "abc123DEF45", "url": "https://www.youtube.com/watch?v=abc123DEF45", "title": "Kapsel Demo", "description": "First catalog description", "duration": 120, "upload_date": "20260503", "thumbnail": "https://i.ytimg.com/vi/vid-1/hqdefault.jpg"},
    {"id": "vid-2", "url": "https://www.youtube.com/watch?v=vid-2", "title": "Updated Catalog Title", "description": "Updated catalog description", "duration": 95, "upload_date": "20260401", "thumbnail": "https://i.ytimg.com/vi/vid-2/hqdefault.jpg"},
    {"id": "vid-2", "url": "https://www.youtube.com/watch?v=vid-2", "title": "Duplicate Catalog Title", "description": "Duplicate catalog description", "duration": 96, "upload_date": "20260402", "thumbnail": "https://i.ytimg.com/vi/duplicate/hqdefault.jpg"},
    {"id": "vid-3", "url": "https://www.youtube.com/watch?v=vid-3", "title": "Updated Downloaded Title", "description": "Updated downloaded description", "duration": 135, "upload_date": "20260403", "thumbnail": "https://i.ytimg.com/vi/vid-3/hqdefault.jpg"},
    {"id": "vid-4", "url": "https://www.youtube.com/watch?v=vid-4", "title": "Unsafe Thumbnail", "description": "Catalog description", "duration": 60, "upload_date": "20260404", "thumbnail": "https://img.example/unsafe.jpg"}
  ]
}`)

func TestEnsureYTDLPUpdateJobsEnqueuesObservableJob(t *testing.T) {
	t.Parallel()

	db := openDownloadDB(t)
	store := jobs.NewStore(db)
	now := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)

	created, err := EnsureYTDLPUpdateJobs(context.Background(), db, store, YTDLPUpdateScheduleOptions{Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	if created != 1 {
		t.Fatalf("expected one yt-dlp update job, got %d", created)
	}

	// An active update job suppresses duplicates.
	created, err = EnsureYTDLPUpdateJobs(context.Background(), db, store, YTDLPUpdateScheduleOptions{Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	if created != 0 {
		t.Fatalf("expected active update job to suppress duplicates, got %d", created)
	}
}

func TestEnsureYTDLPUpdateJobsWrapsAfterInterval(t *testing.T) {
	t.Parallel()

	db := openDownloadDB(t)
	store := jobs.NewStore(db)
	now := time.Now().UTC()

	created, err := EnsureYTDLPUpdateJobs(context.Background(), db, store, YTDLPUpdateScheduleOptions{Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	if created != 1 {
		t.Fatalf("expected one update job, got %d", created)
	}

	// Run the first update job to completion.
	runner := jobs.NewRunner(store, map[string]jobs.Handler{
		YTDLPUpdateJobType: newTestDownloader(db, store, Config{YTDLPPath: "yt-dlp", MediaRoot: "/archive/media", JobStore: store}, &sequenceRunner{stdout: [][]byte{[]byte("update ok\n"), []byte("2026.08.24.1\n")}}).HandleYTDLPUpdate,
	})
	if err := runner.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}

	// A recent successful update suppresses a new job until the interval elapses.
	created, err = EnsureYTDLPUpdateJobs(context.Background(), db, store, YTDLPUpdateScheduleOptions{Now: func() time.Time { return now.Add(time.Minute) }})
	if err != nil {
		t.Fatal(err)
	}
	if created != 0 {
		t.Fatalf("expected no update job within interval, got %d", created)
	}

	// Backdate the latest completed job to before the interval, then a new one is enqueued.
	if _, err := db.Exec("UPDATE jobs SET created_at = ?", now.Add(-DefaultYTDLPUpdateInterval*2).UTC().Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	created, err = EnsureYTDLPUpdateJobs(context.Background(), db, store, YTDLPUpdateScheduleOptions{Now: func() time.Time { return now.Add(time.Hour) }})
	if err != nil {
		t.Fatal(err)
	}
	if created != 1 {
		t.Fatalf("expected a new update job after interval, got %d", created)
	}
}

func TestHandleYTDLPUpdateRunsWhenNoDownloadActive(t *testing.T) {
	t.Parallel()

	db := openDownloadDB(t)
	store := jobs.NewStore(db)
	runnerCalls := &sequenceRunner{stdout: [][]byte{[]byte("update ok\n"), []byte("2026.08.24.1\n")}}
	handler := newTestDownloader(db, store, Config{YTDLPPath: "yt-dlp", MediaRoot: "/archive/media", JobStore: store}, runnerCalls).HandleYTDLPUpdate
	runner := jobs.NewRunner(store, map[string]jobs.Handler{YTDLPUpdateJobType: handler})

	job, err := store.Enqueue(context.Background(), jobs.EnqueueParams{Type: YTDLPUpdateJobType, PayloadJSON: `{}`, MaxAttempts: 1, RunAfter: time.Now().Add(-time.Second)})
	if err != nil {
		t.Fatal(err)
	}

	if err := runner.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	stored, err := store.Get(context.Background(), job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != jobs.StatusSucceeded {
		t.Fatalf("expected succeeded update job, got %#v", stored)
	}

	args := []string{}
	for _, c := range runnerCalls.commands {
		for _, a := range c.Args {
			args = append(args, a)
		}
	}
	if !slices.Contains(args, "--update-to") || !slices.Contains(args, "nightly") {
		t.Fatalf("expected update command to pass --update-to nightly, got %#v", args)
	}
}

func TestUpdateYTDLPSkipsWhileDownloadActive(t *testing.T) {
	t.Parallel()

	db := openDownloadDB(t)
	store := jobs.NewStore(db)
	runnerCalls := &sequenceRunner{stdout: [][]byte{[]byte("should not run\n")}}
	downloader := newTestDownloader(db, store, Config{YTDLPPath: "yt-dlp", MediaRoot: "/archive/media", JobStore: store}, runnerCalls)

	// Enqueue and claim an active download so the guard sees a running download.
	active, err := store.Enqueue(context.Background(), jobs.EnqueueParams{Type: JobType, PayloadJSON: `{}`, MaxAttempts: 1})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok, err := store.Claim(context.Background(), time.Now().Add(time.Second), time.Hour); err != nil || !ok {
		t.Fatalf("expected to claim active download, ok=%v err=%v", ok, err)
	}

	result, err := downloader.UpdateYTDLP(context.Background())
	if err != nil {
		t.Fatalf("expected no error when skipping, got %v", err)
	}
	if !result.Skipped {
		t.Fatalf("expected update to be skipped while a download is active, got %#v", result)
	}
	if len(runnerCalls.commands) != 0 {
		t.Fatalf("expected no yt-dlp command to run while a download is active, got %d", len(runnerCalls.commands))
	}
	_ = store.Cancel(context.Background(), active.ID)
}

func TestChannelFirstScanOnlyDoesNotEnqueueDownload(t *testing.T) {
	t.Parallel()

	db := openDownloadDB(t)
	store := jobs.NewStore(db)
	job, err := store.Enqueue(context.Background(), jobs.EnqueueParams{
		Type:        ChannelJobType,
		PayloadJSON: `{"url":"https://www.youtube.com/@archive","scan_only":true}`,
		MaxAttempts: 1,
	})
	if err != nil {
		t.Fatal(err)
	}

	runner := jobs.NewRunner(store, map[string]jobs.Handler{
		ChannelJobType: newTestDownloader(db, store, Config{YTDLPPath: "yt-dlp", MediaRoot: t.TempDir()}, &sequenceRunner{stdout: [][]byte{catalogSyncFixtureMetadata}}).HandleChannelFirst,
	})
	if err := runner.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	stored, err := store.Get(context.Background(), job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != jobs.StatusSucceeded {
		t.Fatalf("expected succeeded channel job, got %#v", stored)
	}

	// Channel is marked subscribed, but no download job is enqueued.
	assertScalar(t, db, "SELECT count(*) FROM downloads", int64(0))
	assertScalar(t, db, "SELECT count(*) FROM jobs WHERE type = ?", int64(0), JobType)
	assertScalar(t, db, "SELECT subscribed FROM channels WHERE id = ?", int64(1), "chan-1")
	assertScalar(t, db, "SELECT count(*) FROM videos", int64(4))
}

func TestDownloadHandlerMarksMembersOnlyVideoAndDoesNotRetry(t *testing.T) {
	t.Parallel()

	db := openDownloadDB(t)
	store := jobs.NewStore(db)
	if _, err := db.Exec("INSERT INTO channels (id, external_id, name) VALUES ('chan-1', 'chan-1', 'Channel')"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
INSERT INTO videos (id, external_id, source, channel_id, title, duration_seconds, published_at, archived_at)
VALUES ('mem-vid', 'hD37el3bCw4', 'youtube', 'chan-1', 'Members Only Video', 120, '2026-01-01', strftime('%Y-%m-%dT%H:%M:%fZ','now'))`); err != nil {
		t.Fatal(err)
	}

	job, err := store.Enqueue(context.Background(), jobs.EnqueueParams{
		Type:        JobType,
		PayloadJSON: `{"url":"https://www.youtube.com/watch?v=hD37el3bCw4"}`,
		MaxAttempts: 3,
		RunAfter:    time.Now().Add(-time.Second),
	})
	if err != nil {
		t.Fatal(err)
	}
	runner := jobs.NewRunner(store, map[string]jobs.Handler{
		JobType: newTestDownloader(db, store, Config{YTDLPPath: "yt-dlp", MediaRoot: t.TempDir()}, fakeRunner{
			stdout: []byte("ERROR: [youtube] hD37el3bCw4: This video is available to this channel's members on level: Friends of the Pod (or any higher level). Join this channel to get access to members-only content and other exclusive perks."),
			err:    errors.New("exit status 1"),
		}).Handle,
	})

	if err := runner.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}

	stored, err := store.Get(context.Background(), job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != jobs.StatusSucceeded {
		t.Fatalf("expected members-only failure to complete without retry, got %#v", stored)
	}
	assertScalar(t, db, "SELECT members_only FROM videos WHERE id = ?", int64(1), "mem-vid")
}

func TestDownloadHandlerRetriesNonMembersOnlyFailure(t *testing.T) {
	t.Parallel()

	db := openDownloadDB(t)
	store := jobs.NewStore(db)
	job, err := store.Enqueue(context.Background(), jobs.EnqueueParams{
		Type:        JobType,
		PayloadJSON: `{"url":"https://www.youtube.com/watch?v=abc123DEF45"}`,
		MaxAttempts: 3,
		RunAfter:    time.Now().Add(-time.Second),
	})
	if err != nil {
		t.Fatal(err)
	}
	runner := jobs.NewRunner(store, map[string]jobs.Handler{
		JobType: newTestDownloader(db, store, Config{YTDLPPath: "yt-dlp", MediaRoot: t.TempDir()}, fakeRunner{
			stdout: []byte("ERROR: [youtube] abc123DEF45: Some other failure"),
			err:    errors.New("exit status 1"),
		}).Handle,
	})

	if err := runner.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}

	stored, err := store.Get(context.Background(), job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != jobs.StatusQueued || stored.Attempts != 1 {
		t.Fatalf("expected non-members-only failure to queue for retry, got %#v", stored)
	}
}

func TestNormalizePlaylistURL(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name    string
		raw     string
		wantURL string
		wantID  string
	}{
		{name: "playlist path", raw: "https://www.youtube.com/playlist?list=PLtestListID1234567890", wantURL: "https://www.youtube.com/playlist?list=PLtestListID1234567890", wantID: "PLtestListID1234567890"},
		{name: "watch with list", raw: "https://www.youtube.com/watch?v=CtCgNRquauE&list=RDMM&index=2", wantURL: "https://www.youtube.com/playlist?list=RDMM", wantID: "RDMM"},
		{name: "youtu.be with list", raw: "https://youtu.be/CtCgNRquauE?list=PLabc", wantURL: "https://www.youtube.com/playlist?list=PLabc", wantID: "PLabc"},
		{name: "whitespace trimmed", raw: "  https://www.youtube.com/playlist?list=PLabc  ", wantURL: "https://www.youtube.com/playlist?list=PLabc", wantID: "PLabc"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			url, listID, err := NormalizePlaylistURL(tc.raw)
			if err != nil {
				t.Fatal(err)
			}
			if url != tc.wantURL || listID != tc.wantID {
				t.Fatalf("got url=%q id=%q, want url=%q id=%q", url, listID, tc.wantURL, tc.wantID)
			}
		})
	}

	for _, tc := range []struct {
		name string
		raw  string
	}{
		{name: "empty", raw: ""},
		{name: "not a url", raw: "not a url"},
		{name: "non-YouTube host", raw: "https://example.com/playlist?list=PLabc"},
		{name: "missing list param", raw: "https://www.youtube.com/playlist"},
		{name: "empty list param", raw: "https://www.youtube.com/playlist?list="},
		{name: "invalid list chars", raw: "https://www.youtube.com/playlist?list=PL bad"},
		{name: "ftp scheme", raw: "ftp://www.youtube.com/playlist?list=PLabc"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if _, _, err := NormalizePlaylistURL(tc.raw); err == nil {
				t.Fatalf("expected %q to be rejected", tc.raw)
			}
		})
	}
}

func TestBuildPlaylistImportCommand(t *testing.T) {
	t.Parallel()

	downloader := NewDownloader(nil, Config{YTDLPPath: "yt-dlp", MediaRoot: "/archive/media", YTDLPCookiesFile: "/etc/kapsel/youtube.cookies.txt"}, nil)
	command, err := downloader.BuildPlaylistImportCommand("https://www.youtube.com/playlist?list=PLtestListID1234567890")
	if err != nil {
		t.Fatal(err)
	}

	if command.Name != "yt-dlp" {
		t.Fatalf("expected command name %q, got %q", "yt-dlp", command.Name)
	}
	if command.Dir != "/archive/media" {
		t.Fatalf("expected working directory %q, got %q", "/archive/media", command.Dir)
	}
	for _, arg := range []string{"--ignore-config", "--cookies", "/etc/kapsel/youtube.cookies.txt", "--flat-playlist", "--dump-single-json", "https://www.youtube.com/playlist?list=PLtestListID1234567890"} {
		if !slices.Contains(command.Args, arg) {
			t.Fatalf("expected args to contain %q: %#v", arg, command.Args)
		}
	}
	for _, forbidden := range []string{"--no-playlist", "--no-simulate", "--write-thumbnail", "--format"} {
		if slices.Contains(command.Args, forbidden) {
			t.Fatalf("expected args not to contain %q: %#v", forbidden, command.Args)
		}
	}
	if command.MaxStdoutBytes != maxChannelCatalogOutputBytes {
		t.Fatalf("expected stdout cap %d, got %d", maxChannelCatalogOutputBytes, command.MaxStdoutBytes)
	}
}

func TestEnqueuePlaylistImportNormalizesAndDeduplicates(t *testing.T) {
	t.Parallel()

	db := openDownloadDB(t)
	store := jobs.NewStore(db)

	first, err := EnqueuePlaylistImport(context.Background(), store, PlaylistImportPayload{URL: "https://www.youtube.com/watch?v=CtCgNRquauE&list=PLtestListID1234567890"})
	if err != nil {
		t.Fatal(err)
	}
	// A second enqueue of the same playlist (different URL spelling) must
	// resolve to the same active job, not a duplicate.
	second, err := EnqueuePlaylistImport(context.Background(), store, PlaylistImportPayload{URL: "https://www.youtube.com/playlist?list=PLtestListID1234567890"})
	if err != nil {
		t.Fatal(err)
	}
	if first.ID != second.ID {
		t.Fatalf("expected deduplicated job, got %q and %q", first.ID, second.ID)
	}

	var jobCount int
	if err := db.QueryRow("SELECT count(*) FROM jobs WHERE type = ?", "playlist_import").Scan(&jobCount); err != nil {
		t.Fatal(err)
	}
	if jobCount != 1 {
		t.Fatalf("expected 1 playlist_import job, got %d", jobCount)
	}

	if _, err := EnqueuePlaylistImport(context.Background(), store, PlaylistImportPayload{URL: "https://example.com/playlist?list=PLabc"}); err == nil {
		t.Fatal("expected non-YouTube URL to be rejected")
	}
}

const playlistImportFixture = `{
  "id": "PLfixtureListID1234567890",
  "title": "DnB Mix 2026",
  "channel_id": "UCfixtureChannelID1",
  "channel": "Fixture Channel",
  "entries": [
    {"id": "CtCgNRquauE", "title": "Track One"},
    {"id": "Arj1LYD4ano", "title": "Track Two"},
    {"id": "AAAAbbbbCCC", "title": "Track Three"},
    {"id": "Arj1LYD4ano", "title": "Track Two (duplicate)"}
  ]
}`

func TestHandlePlaylistImportHydratesAndLinksAllEntries(t *testing.T) {
	t.Parallel()

	db := openDownloadDB(t)
	store := jobs.NewStore(db)
	// v1 already belongs to a channel catalog at position 7: importing a
	// playlist must not reorder it. The uploader channel is deliberately not
	// pre-seeded — the handler should create it from the flat dump.
	if _, err := db.Exec(`
INSERT INTO videos (id, source, external_id, title, duration_seconds, catalog_position)
VALUES ('CtCgNRquauE', 'youtube', 'CtCgNRquauE', 'Track One', 60, 7),
       ('Arj1LYD4ano', 'youtube', 'Arj1LYD4ano', 'Track Two', 60, -1);`); err != nil {
		t.Fatal(err)
	}

	job, err := store.Enqueue(context.Background(), jobs.EnqueueParams{
		Type:        PlaylistImportJobType,
		PayloadJSON: `{"url":"https://www.youtube.com/playlist?list=PLfixtureListID1234567890"}`,
		MaxAttempts: 1,
		RunAfter:    time.Now().Add(-time.Second),
	})
	if err != nil {
		t.Fatal(err)
	}
	runner := jobs.NewRunner(store, map[string]jobs.Handler{
		PlaylistImportJobType: newTestDownloader(db, store, Config{YTDLPPath: "yt-dlp", MediaRoot: t.TempDir()}, fakeRunner{stdout: []byte(playlistImportFixture)}).HandlePlaylistImport,
	})
	if err := runner.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}

	stored, err := store.Get(context.Background(), job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != jobs.StatusSucceeded {
		t.Fatalf("expected succeeded job, got %#v", stored)
	}
	var result playlistImportResult
	if err := json.Unmarshal([]byte(stored.ResultJSON), &result); err != nil {
		t.Fatal(err)
	}
	if result.PlaylistID != "yt-PLfixtureListID1234567890" || result.Title != "DnB Mix 2026" {
		t.Fatalf("unexpected result: %#v", result)
	}
	// The duplicate entry collapses: all three unique videos are linked in one
	// pass, so nothing is missing and no metadata scans are enqueued.
	if result.Linked != 3 || result.Missing != 0 || result.Enqueued != 0 || result.Skipped != 0 {
		t.Fatalf("unexpected result counts: %#v", result)
	}

	var title string
	var channelID string
	if err := db.QueryRow("SELECT title, channel_id FROM playlists WHERE id = 'yt-PLfixtureListID1234567890'").Scan(&title, &channelID); err != nil {
		t.Fatal(err)
	}
	if title != "DnB Mix 2026" || channelID != "UCfixtureChannelID1" {
		t.Fatalf("unexpected playlist row: title=%q channel=%q", title, channelID)
	}
	// The uploader channel row was created by the import.
	var channelName string
	if err := db.QueryRow("SELECT name FROM channels WHERE id = 'UCfixtureChannelID1'").Scan(&channelName); err != nil {
		t.Fatal(err)
	}
	if channelName != "Fixture Channel" {
		t.Fatalf("expected created channel name, got %q", channelName)
	}
	var entryCount int
	if err := db.QueryRow("SELECT count(*) FROM playlist_entries WHERE playlist_id = 'yt-PLfixtureListID1234567890'").Scan(&entryCount); err != nil {
		t.Fatal(err)
	}
	if entryCount != 3 {
		t.Fatalf("expected 3 playlist entries, got %d", entryCount)
	}
	// The missing video became a catalog row from the flat dump (position -1,
	// not a channel-catalog member) and the existing video kept its channel
	// position: playlist imports must not reorder channel catalogs.
	var newPosition int
	if err := db.QueryRow("SELECT catalog_position FROM videos WHERE external_id = 'AAAAbbbbCCC'").Scan(&newPosition); err != nil {
		t.Fatal(err)
	}
	if newPosition != -1 {
		t.Fatalf("expected new catalog row at position -1, got %d", newPosition)
	}
	assertScalar(t, db, "SELECT catalog_position FROM videos WHERE id = 'CtCgNRquauE'", 7)
	// No metadata scans: the flat dump already populated the catalog.
	var jobCount int
	if err := db.QueryRow("SELECT count(*) FROM jobs WHERE type = ?", "video_metadata_scan").Scan(&jobCount); err != nil {
		t.Fatal(err)
	}
	if jobCount != 0 {
		t.Fatalf("expected no video metadata scan jobs, got %d", jobCount)
	}
}

func TestHandlePlaylistImportRejectsEmptyPlaylist(t *testing.T) {
	t.Parallel()

	db := openDownloadDB(t)
	store := jobs.NewStore(db)
	job, err := store.Enqueue(context.Background(), jobs.EnqueueParams{
		Type:        PlaylistImportJobType,
		PayloadJSON: `{"url":"https://www.youtube.com/playlist?list=PLfixtureEmpty0000"}`,
		MaxAttempts: 1,
		RunAfter:    time.Now().Add(-time.Second),
	})
	if err != nil {
		t.Fatal(err)
	}
	runner := jobs.NewRunner(store, map[string]jobs.Handler{
		PlaylistImportJobType: newTestDownloader(db, store, Config{YTDLPPath: "yt-dlp", MediaRoot: t.TempDir()}, fakeRunner{
			stdout: []byte(`{"id": "PLfixtureEmpty0000", "title": "Empty", "entries": []}`),
		}).HandlePlaylistImport,
	})
	if err := runner.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}

	stored, err := store.Get(context.Background(), job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != jobs.StatusFailed || !strings.Contains(stored.Error, "contains no videos") {
		t.Fatalf("expected failed empty-playlist job, got %#v", stored)
	}
	var playlistCount int
	if err := db.QueryRow("SELECT count(*) FROM playlists").Scan(&playlistCount); err != nil {
		t.Fatal(err)
	}
	if playlistCount != 0 {
		t.Fatalf("expected no playlists created for empty playlist, got %d", playlistCount)
	}
}
