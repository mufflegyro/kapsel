package fixture

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"kapsel/internal/config"
)

func Config(addr string, dataDir string) config.Config {
	return config.Config{
		Addr:                         addr,
		AuthMode:                     "disabled",
		DataDir:                      dataDir,
		DBPath:                       filepath.Join(dataDir, "kapsel.db"),
		ImportRoot:                   filepath.Join(dataDir, "imports"),
		MediaRoot:                    filepath.Join(dataDir, "media"),
		MediaSigningSecret:           "kapsel-e2e-media-secret",
		MediaSigningSecretConfigured: true,
		MediaURLTTL:                  config.DefaultMediaURLTTL,
		MinFreeSpaceBytes:            0,
		PreviewsEnabled:              false,
		SponsorBlockEnabled:          false,
		SessionSecret:                "kapsel-e2e-session-secret",
		SessionSecretConfigured:      true,
		SessionTTL:                   time.Hour,
		FFMPEGPath:                   "ffmpeg",
		YTDLPFormat:                  "best",
		YTDLPPath:                    "yt-dlp",
	}
}

func Seed(ctx context.Context, db *sql.DB, mediaRoot string) error {
	mediaPath := filepath.Join(mediaRoot, "videos", "e2e-video.mp4")
	if err := os.MkdirAll(filepath.Dir(mediaPath), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(mediaPath, []byte("e2e media placeholder"), 0o644); err != nil {
		return err
	}
	spritePath := filepath.Join(mediaRoot, "derived", "previews", "e2e-video", "sprite.jpg")
	if err := os.MkdirAll(filepath.Dir(spritePath), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(spritePath, []byte("e2e preview sprite"), 0o644); err != nil {
		return err
	}
	subtitleDir := filepath.Join(mediaRoot, "subtitles")
	if err := os.MkdirAll(subtitleDir, 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(subtitleDir, "e2e-video.en.vtt"), []byte("WEBVTT\n\n00:00:00.000 --> 00:00:02.000 align:start position:0% size:50%\nEnglish caption text\n"), 0o644); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(subtitleDir, "e2e-video.en-orig.vtt"), []byte("WEBVTT\n\n00:00:00.000 --> 00:00:02.000 align:start position:0% size:50%\nOriginal caption text\n"), 0o644); err != nil {
		return err
	}
	channelThumbnailPath := filepath.Join(mediaRoot, "channels", "e2e-channel.jpg")
	if err := os.MkdirAll(filepath.Dir(channelThumbnailPath), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(channelThumbnailPath, []byte("e2e channel thumbnail"), 0o644); err != nil {
		return err
	}

	statements := []string{
		`INSERT INTO channels (id, external_id, name, handle, description, subscribed)
VALUES ('e2e-channel', 'e2e-channel', 'E2E Test Channel', '@e2e', 'Deterministic channel for browser smoke tests. See https://example.com/intro.
This deliberately long description verifies that channel pages keep their headers compact when imported metadata runs long.
It should collapse before taking over the screen, then expand on request.
### Links
Visit [project notes](https://example.com/kapsel) and https://example.org/archive.
Wrapped (https://example.net/wrapped). Contact smoke@example.com. Unsafe [bad](javascript:alert(1)) <script>alert(1)</script>.
### More context
- Line one describes deterministic fixtures.
- Line two keeps enough height for the collapse threshold.
- Line three checks list rendering.
- Line four checks that the bottom of the text can be hidden.
- Line five keeps the fixture longer than a compact header should be.
Final [visible-after-expand marker](https://example.com/after-expand).', 1)
ON CONFLICT(id) DO UPDATE SET name = excluded.name, handle = excluded.handle, description = excluded.description, subscribed = excluded.subscribed`,
		`INSERT INTO videos (id, external_id, channel_id, title, description, published_at, duration_seconds, media_path, watched)
VALUES ('e2e-video', 'e2e-video', 'e2e-channel', 'E2E Lunar Archive Smoke', '00:00 - E2E opening
02:00 - E2E closing
A deterministic browser smoke fixture about lunar archives. Visit https://example.com/watch-details.
Jump to 0:42 or 1:05. Unsafe <script>alert(1)</script>.', '2026-05-03T12:00:00Z', 125, 'videos/e2e-video.mp4', 0)
ON CONFLICT(id) DO UPDATE SET channel_id = excluded.channel_id, title = excluded.title, description = excluded.description, published_at = excluded.published_at, duration_seconds = excluded.duration_seconds, media_path = excluded.media_path, watched = excluded.watched`,
		`INSERT INTO media_assets (owner_type, owner_id, kind, path)
VALUES ('channel', 'e2e-channel', 'thumbnail', 'channels/e2e-channel.jpg')
ON CONFLICT(owner_type, owner_id, kind) DO UPDATE SET path = excluded.path`,
		`INSERT INTO video_previews (video_id, sprite_path, interval_seconds, frame_width, frame_height, columns, preview_count)
VALUES ('e2e-video', 'derived/previews/e2e-video/sprite.jpg', 10, 160, 90, 5, 3)
ON CONFLICT(video_id) DO UPDATE SET sprite_path = excluded.sprite_path, interval_seconds = excluded.interval_seconds, frame_width = excluded.frame_width, frame_height = excluded.frame_height, columns = excluded.columns, preview_count = excluded.preview_count`,
		`INSERT INTO media_assets (owner_type, owner_id, kind, path)
VALUES ('video', 'e2e-video', 'timeline_preview_sprite', 'derived/previews/e2e-video/sprite.jpg')
ON CONFLICT(owner_type, owner_id, kind) DO UPDATE SET path = excluded.path`,
		`INSERT INTO subtitles (video_id, language, name, source, format, path, text)
VALUES ('e2e-video', 'en', 'English', 'downloaded', 'vtt', 'subtitles/e2e-video.en.vtt', 'English caption text')
ON CONFLICT(video_id, language, source) DO UPDATE SET name = excluded.name, format = excluded.format, path = excluded.path, text = excluded.text`,
		`INSERT INTO subtitles (video_id, language, name, source, format, path, text)
VALUES ('e2e-video', 'en-orig', 'Original', 'downloaded', 'vtt', 'subtitles/e2e-video.en-orig.vtt', 'Original caption text')
ON CONFLICT(video_id, language, source) DO UPDATE SET name = excluded.name, format = excluded.format, path = excluded.path, text = excluded.text`,
		`INSERT INTO videos (id, external_id, channel_id, title, description, published_at, duration_seconds)
VALUES ('e2e-catalog-video', 'MoonBrief01', 'e2e-channel', 'E2E Catalog Moon Brief', 'A catalog-only browser smoke fixture.', '2026-05-04', 95)
ON CONFLICT(id) DO UPDATE SET channel_id = excluded.channel_id, title = excluded.title, description = excluded.description, published_at = excluded.published_at, duration_seconds = excluded.duration_seconds`,
		`INSERT INTO videos (id, external_id, channel_id, title, description, published_at, duration_seconds)
VALUES ('e2e-catalog-video-2', 'OrbitBrief1', 'e2e-channel', 'E2E Catalog Orbit Brief', 'A second catalog-only browser smoke fixture.', '2026-05-05', 105)
ON CONFLICT(id) DO UPDATE SET channel_id = excluded.channel_id, title = excluded.title, description = excluded.description, published_at = excluded.published_at, duration_seconds = excluded.duration_seconds`,
		`INSERT INTO comments (id, video_id, author, text, published_at, like_count)
VALUES ('e2e-comment', 'e2e-video', 'Smoke Tester', 'A deterministic imported comment.', '2026-05-03T12:10:00Z', 3)
ON CONFLICT(id) DO UPDATE SET video_id = excluded.video_id, author = excluded.author, text = excluded.text, published_at = excluded.published_at, like_count = excluded.like_count`,
		`INSERT INTO user_progress (video_id, position_seconds, duration_seconds, watched)
VALUES ('e2e-video', 42, 125, 0)
ON CONFLICT(video_id) DO UPDATE SET position_seconds = excluded.position_seconds, duration_seconds = excluded.duration_seconds, watched = excluded.watched`,
		`INSERT INTO search_documents (owner_type, owner_id, field, text)
VALUES ('video', 'e2e-video', 'title', 'E2E Lunar Archive Smoke lunar smoke')
ON CONFLICT(owner_type, owner_id, field) DO UPDATE SET text = excluded.text`,
		`INSERT INTO search_documents (owner_type, owner_id, field, text)
VALUES ('video', 'e2e-video', 'description', 'Deterministic archive fixture about moondust and lunar smoke')
ON CONFLICT(owner_type, owner_id, field) DO UPDATE SET text = excluded.text`,
		`INSERT INTO search_documents (owner_type, owner_id, field, text)
VALUES ('video', 'e2e-catalog-video', 'title', 'E2E Catalog Moon Brief')
ON CONFLICT(owner_type, owner_id, field) DO UPDATE SET text = excluded.text`,
		`INSERT INTO search_documents (owner_type, owner_id, field, text)
VALUES ('video', 'e2e-catalog-video-2', 'title', 'E2E Catalog Orbit Brief')
ON CONFLICT(owner_type, owner_id, field) DO UPDATE SET text = excluded.text`,
		`INSERT INTO search_documents (owner_type, owner_id, field, text)
VALUES ('channel', 'e2e-channel', 'name', 'E2E Test Channel')
ON CONFLICT(owner_type, owner_id, field) DO UPDATE SET text = excluded.text`,
		`INSERT INTO search_documents (owner_type, owner_id, field, text)
VALUES ('channel', 'e2e-channel', 'description', 'Deterministic channel for browser smoke tests')
ON CONFLICT(owner_type, owner_id, field) DO UPDATE SET text = excluded.text`,
		`INSERT INTO search_documents (owner_type, owner_id, field, text)
VALUES ('video', 'e2e-video', 'channel', 'E2E Test Channel E2E Lunar Archive Smoke')
ON CONFLICT(owner_type, owner_id, field) DO UPDATE SET text = excluded.text`,
	}
	// A term matching many episodes plus a channel description doc exercises
	// the secondary-block quota: without it, every window slot goes to
	// recency-boosted episodes and the channels & playlists block renders
	// empty (see restore-secondary-search-matches.md).
	for i := range 55 {
		id := fmt.Sprintf("e2e-filler-%02d", i)
		statements = append(statements,
			fmt.Sprintf(`INSERT INTO videos (id, external_id, title, published_at, duration_seconds) VALUES ('%[1]s', '%[1]s', 'Filler Episode %[2]02d', '2010-01-01T00:00:00Z', 60) ON CONFLICT(id) DO UPDATE SET title = excluded.title, published_at = excluded.published_at, duration_seconds = excluded.duration_seconds`, id, i),
			fmt.Sprintf(`INSERT INTO search_documents (owner_type, owner_id, field, text) VALUES ('video', '%[1]s', 'title', 'Filler Episode %[2]02d') ON CONFLICT(owner_type, owner_id, field) DO UPDATE SET text = excluded.text`, id, i),
		)
	}
	statements = append(statements,
		`INSERT INTO search_documents (owner_type, owner_id, field, text)
VALUES ('channel', 'e2e-channel', 'description', 'Deterministic channel for browser smoke tests with filler episodes')
ON CONFLICT(owner_type, owner_id, field) DO UPDATE SET text = excluded.text`)
	for _, statement := range statements {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("seed e2e data: %w", err)
		}
	}

	return nil
}

func ResetProgress(ctx context.Context, db *sql.DB) error {
	if _, err := db.ExecContext(ctx, `UPDATE videos SET watched = 0, keep_forever = 0 WHERE id = 'e2e-video'`); err != nil {
		return fmt.Errorf("reset e2e video watched flag: %w", err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO user_progress (video_id, position_seconds, duration_seconds, watched)
VALUES ('e2e-video', 42, 125, 0)
ON CONFLICT(video_id) DO UPDATE SET position_seconds = excluded.position_seconds, duration_seconds = excluded.duration_seconds, watched = excluded.watched`); err != nil {
		return fmt.Errorf("reset e2e progress: %w", err)
	}

	return nil
}
