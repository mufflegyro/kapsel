// Package playlistimport imports YouTube playlists from per-playlist CSV
// exports by linking videos already in the archive and optionally enqueueing
// direct-video downloads for the ones that are missing.
package playlistimport

import (
	"context"
	"database/sql"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"kapsel/internal/denorm"
)

// Entry is a single parsed playlist row.
type Entry struct {
	VideoID string
}

// Parse reads a per-playlist CSV export from r and returns the video entries
// it contains, in row order. The header must include a "Video ID" column;
// extra columns (such as "Playlist Video Creation Timestamp") are tolerated
// and ignored. Rows without a usable video ID are skipped. A UTF-8 BOM and
// surrounding whitespace are tolerated.
func Parse(r io.Reader) ([]Entry, error) {
	reader := csv.NewReader(r)
	// Tolerate rows with a different field count (e.g. trailing blank lines)
	// while still validating column names from the header.
	reader.FieldsPerRecord = -1
	entries := []Entry{}
	record, err := reader.Read()
	if err != nil {
		if errors.Is(err, io.EOF) {
			return entries, nil
		}
		return nil, fmt.Errorf("read playlist header: %w", err)
	}
	record = trimFields(record)
	header := headerIndex(record)

	videoCol, ok := header["video id"]
	if !ok {
		return nil, errors.New("playlist export is missing a Video ID column")
	}

	for {
		row, err := reader.Read()
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return nil, fmt.Errorf("read playlist row: %w", err)
		}
		row = trimFields(row)
		videoID := ""
		if videoCol >= 0 && videoCol < len(row) {
			videoID = row[videoCol]
		}
		if !isLikelyYouTubeVideoID(videoID) {
			continue
		}
		entries = append(entries, Entry{VideoID: videoID})
	}

	return entries, nil
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

func trimFields(fields []string) []string {
	out := make([]string, len(fields))
	for i, field := range fields {
		out[i] = strings.TrimSpace(strings.TrimPrefix(field, "\uFEFF"))
	}
	return out
}

func headerIndex(fields []string) map[string]int {
	index := map[string]int{}
	for i, field := range fields {
		key := strings.ToLower(field)
		if _, ok := index[key]; !ok {
			index[key] = i
		}
	}
	return index
}

// Mode controls what happens to playlist videos that are missing from the
// archive after linking the ones already present.
type Mode string

const (
	// ModeMetadataScan (default) enqueues a metadata-only job for each missing
	// video so it becomes a catalog row that a later re-run can link. No media
	// is downloaded.
	ModeMetadataScan Mode = "metadata-scan"
	// ModeLinkOnly links existing videos and reports the rest as missing
	// without enqueuing anything.
	ModeLinkOnly Mode = "link-only"
	// ModeDownload enqueues a full media download for each missing video.
	ModeDownload Mode = "download"
)

// Report summarizes a playlist CSV import.
type Report struct {
	Playlists int      `json:"playlists"`
	Linked    int      `json:"linked"`
	Missing   int      `json:"missing"`
	Enqueued  int      `json:"enqueued"`
	Skipped   int      `json:"skipped"`
	Errors    []string `json:"errors,omitempty"`
}

// Enqueuer handles a playlist video that is missing from the archive. The CSV
// and CLI paths use the download helpers via download.NewPlaylistImportEnqueuer;
// the URL-import job handler provides the same adapter. Defining the interface
// here keeps the link logic in one place without a download import cycle.
type Enqueuer interface {
	EnqueuePlaylistVideo(ctx context.Context, videoID string, mode Mode) error
}

// PlaylistIdentity is the deterministic identity a playlist is upserted under:
// a stable local id, the YouTube external id (list id for URL imports, the
// title for CSV-derived playlists), the display title, an optional description,
// and an optional channel id that is linked only when that channel already
// exists in the archive.
type PlaylistIdentity struct {
	ID          string
	ExternalID  string
	Title       string
	Description string
	ChannelID   string
}

// ImportFile parses the playlist export at path, creates or updates the
// playlist named after the file base, and links every video already present
// in the archive. Videos missing from the archive are handled according to
// mode (see Mode). It returns a report.
func ImportFile(ctx context.Context, db *sql.DB, enqueuer Enqueuer, path string, mode Mode) (Report, error) {
	file, err := os.Open(path)
	if err != nil {
		return Report{}, err
	}
	defer file.Close()

	entries, err := Parse(file)
	if err != nil {
		return Report{}, err
	}

	return ImportEntries(ctx, db, enqueuer, path, entries, mode)
}

// ImportEntries links parsed entries into a playlist named after the file base
// name of path. It is split from ImportFile so tests can drive it directly.
func ImportEntries(ctx context.Context, db *sql.DB, enqueuer Enqueuer, path string, entries []Entry, mode Mode) (Report, error) {
	identity := PlaylistIdentityFromPath(path)

	return ImportInto(ctx, db, enqueuer, identity, entries, mode)
}

// ImportInto links entries into the playlist described by identity. It is the
// shared core behind ImportEntries (CSV) and the playlist_import URL job: it
// upserts the playlist, replaces its entries with the ones already in the
// archive, and enqueues missing videos according to mode.
func ImportInto(ctx context.Context, db *sql.DB, enqueuer Enqueuer, identity PlaylistIdentity, entries []Entry, mode Mode) (Report, error) {
	if db == nil {
		return Report{}, errors.New("playlist import missing database")
	}
	if enqueuer == nil {
		return Report{}, errors.New("playlist import missing enqueuer")
	}
	if mode == "" {
		mode = ModeMetadataScan
	}
	report := Report{Playlists: 1}

	playlistID, err := UpsertPlaylist(ctx, db, identity)
	if err != nil {
		return Report{}, err
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return Report{}, err
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, "DELETE FROM playlist_entries WHERE playlist_id = ?", playlistID); err != nil {
		return Report{}, err
	}

	missing := []string{}
	position := 0
	for _, entry := range entries {
		var videoID string
		err := tx.QueryRowContext(ctx,
			"SELECT id FROM videos WHERE source = 'youtube' AND external_id = ?", entry.VideoID,
		).Scan(&videoID)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				missing = append(missing, entry.VideoID)
				continue
			}
			return Report{}, err
		}
		if _, err := tx.ExecContext(ctx, `
INSERT INTO playlist_entries (playlist_id, video_id, position)
VALUES (?, ?, ?)`, playlistID, videoID, position); err != nil {
			return Report{}, err
		}
		position++
		report.Linked++
	}

	if err := tx.Commit(); err != nil {
		return Report{}, err
	}

	report.Missing = len(missing)
	report.Skipped = len(entries) - report.Linked - report.Missing
	if len(missing) == 0 || mode == ModeLinkOnly {
		return report, nil
	}

	enqueued := 0
	for _, videoID := range missing {
		if err := enqueuer.EnqueuePlaylistVideo(ctx, videoID, mode); err != nil {
			report.Errors = append(report.Errors, fmt.Sprintf("enqueue video %s: %v", videoID, err))
			continue
		}
		enqueued++
	}
	report.Enqueued = enqueued

	return report, nil
}

// PlaylistIdentityFromPath derives the deterministic playlist identity for a
// playlist export file path. Both the CLI and the HTTP upload path use this so
// that re-importing the same file name refreshes the same playlist.
func PlaylistIdentityFromPath(path string) PlaylistIdentity {
	base := filepath.Base(path)
	title := strings.TrimSuffix(base, filepath.Ext(base))
	if strings.TrimSpace(title) == "" {
		title = base
	}

	return PlaylistIdentity{
		ID:         "csv-" + slugify(title),
		ExternalID: title,
		Title:      title,
	}
}

// playlistDB is the subset of *sql.DB and *sql.Tx the playlist writer needs,
// so the CSV/CLI path can run on the database and the URL-import job can run
// inside its transaction.
type playlistDB interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

// UpsertPlaylist creates or refreshes the playlist described by identity,
// including its search document. A channel id is linked only when the channel
// already exists in the archive (the playlist_entries foreign key would reject
// an unknown channel). exec may be a *sql.DB or a transaction. It returns the
// playlist id.
func UpsertPlaylist(ctx context.Context, exec playlistDB, identity PlaylistIdentity) (string, error) {
	playlistID := identity.ID
	if strings.TrimSpace(playlistID) == "" {
		return "", errors.New("playlist import missing playlist id")
	}
	externalID := strings.TrimSpace(identity.ExternalID)
	if externalID == "" {
		externalID = playlistID
	}
	title := strings.TrimSpace(identity.Title)
	if title == "" {
		title = playlistID
	}
	description := strings.TrimSpace(identity.Description)
	channelID := sql.NullString{String: identity.ChannelID, Valid: strings.TrimSpace(identity.ChannelID) != ""}
	if channelID.Valid {
		var exists int
		if err := exec.QueryRowContext(ctx, "SELECT 1 FROM channels WHERE id = ?", channelID.String).Scan(&exists); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				channelID = sql.NullString{}
			} else {
				return "", err
			}
		}
	}

	_, err := exec.ExecContext(ctx, `
INSERT INTO playlists (id, external_id, channel_id, title, description, updated_at)
VALUES (?, ?, ?, ?, ?, strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
ON CONFLICT(id) DO UPDATE SET
  external_id = excluded.external_id,
  channel_id = excluded.channel_id,
  title = excluded.title,
  description = CASE WHEN excluded.description <> '' THEN excluded.description ELSE playlists.description END,
  updated_at = excluded.updated_at`, playlistID, externalID, channelID, title, description)
	if err != nil {
		return "", err
	}
	if err := denorm.SyncSearchDocument(ctx, exec, "playlist", playlistID, "title", title); err != nil {
		return "", err
	}
	if description != "" {
		if err := denorm.SyncSearchDocument(ctx, exec, "playlist", playlistID, "description", description); err != nil {
			return "", err
		}
	}

	return playlistID, nil
}

func slugify(value string) string {
	var builder strings.Builder
	for _, char := range strings.ToLower(value) {
		if char >= 'a' && char <= 'z' || char >= '0' && char <= '9' {
			builder.WriteRune(char)
		} else {
			builder.WriteByte('-')
		}
	}
	slug := builder.String()
	for strings.Contains(slug, "--") {
		slug = strings.ReplaceAll(slug, "--", "-")
	}

	return strings.Trim(slug, "-")
}
