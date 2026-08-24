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
	"kapsel/internal/download"
	"kapsel/internal/jobs"
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

// Report summarizes a playlist CSV import.
type Report struct {
	Playlists int      `json:"playlists"`
	Linked    int      `json:"linked"`
	Missing   int      `json:"missing"`
	Enqueued  int      `json:"enqueued"`
	Skipped   int      `json:"skipped"`
	Errors    []string `json:"errors,omitempty"`
}

// ImportFile parses the playlist export at path, creates or updates the
// playlist named after the file base, and links every video already present
// in the archive. When downloadMissing is true, a direct-video download job is
// enqueued for each missing video so a later re-run can link it. It returns a
// report.
func ImportFile(ctx context.Context, db *sql.DB, store *jobs.Store, path string, downloadMissing bool) (Report, error) {
	file, err := os.Open(path)
	if err != nil {
		return Report{}, err
	}
	defer file.Close()

	entries, err := Parse(file)
	if err != nil {
		return Report{}, err
	}

	return ImportEntries(ctx, db, store, path, entries, downloadMissing)
}

// ImportEntries links parsed entries into a playlist named after the file base
// name of path. It is split from ImportFile so tests can drive it directly.
func ImportEntries(ctx context.Context, db *sql.DB, store *jobs.Store, path string, entries []Entry, downloadMissing bool) (Report, error) {
	if db == nil {
		return Report{}, errors.New("playlist import missing database")
	}
	report := Report{Playlists: 1}

	playlistID, err := upsertPlaylist(ctx, db, path)
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
	if downloadMissing {
		enqueued := 0
		for _, videoID := range missing {
			payload := download.Payload{URL: "https://www.youtube.com/watch?v=" + videoID}
			if _, err := download.EnqueueDownload(ctx, store, payload); err != nil {
				report.Errors = append(report.Errors, fmt.Sprintf("enqueue video %s: %v", videoID, err))
				continue
			}
			enqueued++
		}
		report.Enqueued = enqueued
	}

	return report, nil
}

// upsertPlaylist creates or refreshes the playlist for path, deriving a
// deterministic id and the display title from the file base name. It returns
// the playlist id.
func upsertPlaylist(ctx context.Context, db *sql.DB, path string) (string, error) {
	base := filepath.Base(path)
	title := strings.TrimSuffix(base, filepath.Ext(base))
	if strings.TrimSpace(title) == "" {
		title = base
	}
	playlistID := "csv-" + slugify(title)

	_, err := db.ExecContext(ctx, `
INSERT INTO playlists (id, external_id, title, updated_at)
VALUES (?, ?, ?, strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
ON CONFLICT(id) DO UPDATE SET
  title = excluded.title,
  updated_at = excluded.updated_at`, playlistID, title, title)
	if err != nil {
		return "", err
	}
	if err := denorm.SyncSearchDocument(ctx, db, "playlist", playlistID, "title", title); err != nil {
		return "", err
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
