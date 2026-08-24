package playlistimport

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"kapsel/internal/database"
	"kapsel/internal/jobs"
)

func openDB(t *testing.T) *sql.DB {
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

func TestParsePlaylistCSV(t *testing.T) {
	t.Parallel()

	csvData := "Video ID,Playlist Video Creation Timestamp\n" +
		"CtCgNRquauE,2026-06-23T12:03:34+00:00\n" +
		"Arj1LYD4ano,2026-07-04T19:33:26+00:00\n" +
		"\n" +
		"TIddhXxyUGY,2026-07-04T19:35:09+00:00\n"
	entries, err := Parse(strings.NewReader(csvData))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 3 {
		t.Fatalf("expected 3 entries, got %d", len(entries))
	}
	if entries[0].VideoID != "CtCgNRquauE" || entries[1].VideoID != "Arj1LYD4ano" || entries[2].VideoID != "TIddhXxyUGY" {
		t.Fatalf("unexpected entries: %#v", entries)
	}
}

func TestParseSkipsInvalidVideoIDs(t *testing.T) {
	t.Parallel()

	csvData := "Video ID,Playlist Video Creation Timestamp\n" +
		"CtCgNRquauE,2026-06-23T12:03:34+00:00\n" +
		",2026-06-24T00:00:00+00:00\n" + // empty video id
		"not-a-valid-id,2026-06-25T00:00:00+00:00\n" + // too short / invalid
		"CtCgNRquauX,2026-06-26T00:00:00+00:00\n"
	entries, err := Parse(strings.NewReader(csvData))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("expected 2 entries, got %d: %#v", len(entries), entries)
	}
	if entries[0].VideoID != "CtCgNRquauE" || entries[1].VideoID != "CtCgNRquauX" {
		t.Fatalf("unexpected entries: %#v", entries)
	}
}

func TestParseRequiresVideoIDColumn(t *testing.T) {
	t.Parallel()

	_, err := Parse(strings.NewReader("Playlist Title\nMy playlist\n"))
	if err == nil || !strings.Contains(err.Error(), "Video ID column") {
		t.Fatalf("expected missing Video ID column error, got %v", err)
	}
}

func TestImportLinksExistingVideos(t *testing.T) {
	t.Parallel()

	db := openDB(t)
	store := jobs.NewStore(db)
	if _, err := db.Exec(`
INSERT INTO channels (id, external_id, name) VALUES ('chan-1', 'chan-1', 'Channel');
INSERT INTO videos (id, source, external_id, channel_id, title, duration_seconds)
VALUES ('v1', 'youtube', 'CtCgNRquauE', 'chan-1', 'Video One', 60),
       ('v2', 'youtube', 'Arj1LYD4ano', 'chan-1', 'Video Two', 60);`); err != nil {
		t.Fatal(err)
	}

	path := filepath.Join(t.TempDir(), "DnB-videos.csv")
	if err := os.WriteFile(path, []byte("Video ID\nCtCgNRquauE\nArj1LYD4ano\nAAAAbbbbCCC\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	report, err := ImportFile(context.Background(), db, store, path, ModeLinkOnly)
	if err != nil {
		t.Fatal(err)
	}
	if report.Playlists != 1 || report.Linked != 2 || report.Missing != 1 || report.Enqueued != 0 {
		t.Fatalf("unexpected report: %#v", report)
	}

	var title string
	var entryCount int
	if err := db.QueryRow("SELECT title FROM playlists WHERE id = 'csv-dnb-videos'").Scan(&title); err != nil {
		t.Fatal(err)
	}
	if title != "DnB-videos" {
		t.Fatalf("expected playlist title DnB-videos, got %q", title)
	}
	if err := db.QueryRow("SELECT count(*) FROM playlist_entries WHERE playlist_id = 'csv-dnb-videos'").Scan(&entryCount); err != nil {
		t.Fatal(err)
	}
	if entryCount != 2 {
		t.Fatalf("expected 2 playlist entries, got %d", entryCount)
	}

	// Positions follow CSV row order.
	var positions string
	if err := db.QueryRow(`SELECT group_concat(position) FROM playlist_entries WHERE playlist_id = 'csv-dnb-videos' ORDER BY position`).Scan(&positions); err != nil {
		t.Fatal(err)
	}
	if positions != "0,1" {
		t.Fatalf("expected positions 0,1, got %q", positions)
	}
}

func TestImportIsIdempotentAndRefreshesEntries(t *testing.T) {
	t.Parallel()

	db := openDB(t)
	store := jobs.NewStore(db)
	if _, err := db.Exec(`
INSERT INTO channels (id, external_id, name) VALUES ('chan-1', 'chan-1', 'Channel');
INSERT INTO videos (id, source, external_id, channel_id, title, duration_seconds)
VALUES ('v1', 'youtube', 'CtCgNRquauE', 'chan-1', 'Video One', 60),
       ('v2', 'youtube', 'Arj1LYD4ano', 'chan-1', 'Video Two', 60);`); err != nil {
		t.Fatal(err)
	}

	path := filepath.Join(t.TempDir(), "playlist.csv")
	write := func(ids string) {
		t.Helper()
		if err := os.WriteFile(path, []byte("Video ID\n"+ids), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	write("CtCgNRquauE\nArj1LYD4ano\n")
	if _, err := ImportFile(context.Background(), db, store, path, ModeLinkOnly); err != nil {
		t.Fatal(err)
	}
	// Re-import with a different set: entries must be replaced, not added to.
	write("Arj1LYD4ano\n")
	if _, err := ImportFile(context.Background(), db, store, path, ModeLinkOnly); err != nil {
		t.Fatal(err)
	}

	var count int
	if err := db.QueryRow("SELECT count(*) FROM playlist_entries WHERE playlist_id = 'csv-playlist'").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("expected 1 entry after re-import, got %d", count)
	}
}

func TestImportEnqueuesDownloadsForMissingVideos(t *testing.T) {
	t.Parallel()

	db := openDB(t)
	store := jobs.NewStore(db)

	path := filepath.Join(t.TempDir(), "playlist.csv")
	if err := os.WriteFile(path, []byte("Video ID\nCtCgNRquauE\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	report, err := ImportFile(context.Background(), db, store, path, ModeDownload)
	if err != nil {
		t.Fatal(err)
	}
	if report.Linked != 0 || report.Missing != 1 || report.Enqueued != 1 {
		t.Fatalf("unexpected report: %#v", report)
	}

	var jobCount int
	if err := db.QueryRow("SELECT count(*) FROM jobs WHERE type = ?", "download").Scan(&jobCount); err != nil {
		t.Fatal(err)
	}
	if jobCount != 1 {
		t.Fatalf("expected 1 video download job, got %d", jobCount)
	}
}

func TestImportDefaultEnqueuesMetadataScansForMissingVideos(t *testing.T) {
	t.Parallel()

	db := openDB(t)
	store := jobs.NewStore(db)

	path := filepath.Join(t.TempDir(), "playlist.csv")
	if err := os.WriteFile(path, []byte("Video ID\nCtCgNRquauE\nAAAAbbbbCCC\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	report, err := ImportFile(context.Background(), db, store, path, "")
	if err != nil {
		t.Fatal(err)
	}
	if report.Linked != 0 || report.Missing != 2 || report.Enqueued != 2 {
		t.Fatalf("unexpected report: %#v", report)
	}

	var jobCount int
	if err := db.QueryRow("SELECT count(*) FROM jobs WHERE type = ?", "video_metadata_scan").Scan(&jobCount); err != nil {
		t.Fatal(err)
	}
	if jobCount != 2 {
		t.Fatalf("expected 2 video metadata scan jobs, got %d", jobCount)
	}
}

func TestImportMetadataScanIsIdempotentPerURL(t *testing.T) {
	t.Parallel()

	db := openDB(t)
	store := jobs.NewStore(db)

	path := filepath.Join(t.TempDir(), "playlist.csv")
	write := func() {
		t.Helper()
		if err := os.WriteFile(path, []byte("Video ID\nCtCgNRquauE\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	write()
	if _, err := ImportFile(context.Background(), db, store, path, ModeMetadataScan); err != nil {
		t.Fatal(err)
	}
	// Re-importing the same file must not enqueue a second scan job for the
	// same URL while the first is still queued/running.
	write()
	if _, err := ImportFile(context.Background(), db, store, path, ModeMetadataScan); err != nil {
		t.Fatal(err)
	}

	var jobCount int
	if err := db.QueryRow("SELECT count(*) FROM jobs WHERE type = ?", "video_metadata_scan").Scan(&jobCount); err != nil {
		t.Fatal(err)
	}
	if jobCount != 1 {
		t.Fatalf("expected 1 metadata scan job after re-import, got %d", jobCount)
	}
}

func TestImportLinkOnlyDoesNotEnqueueJobs(t *testing.T) {
	t.Parallel()

	db := openDB(t)
	store := jobs.NewStore(db)

	path := filepath.Join(t.TempDir(), "playlist.csv")
	if err := os.WriteFile(path, []byte("Video ID\nCtCgNRquauE\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	report, err := ImportFile(context.Background(), db, store, path, ModeLinkOnly)
	if err != nil {
		t.Fatal(err)
	}
	if report.Enqueued != 0 || report.Missing != 1 {
		t.Fatalf("unexpected report: %#v", report)
	}

	var jobCount int
	if err := db.QueryRow("SELECT count(*) FROM jobs").Scan(&jobCount); err != nil {
		t.Fatal(err)
	}
	if jobCount != 0 {
		t.Fatalf("expected no jobs enqueued, got %d", jobCount)
	}
}
