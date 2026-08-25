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

// testEnqueuer writes missing-video jobs through the job store with the same
// payload shape and active-job dedupe the download helpers use, so tests can
// exercise the real enqueue semantics without importing download.
type testEnqueuer struct {
	store *jobs.Store
}

func newTestEnqueuer(store *jobs.Store) testEnqueuer {
	return testEnqueuer{store: store}
}

func (e testEnqueuer) EnqueuePlaylistVideo(ctx context.Context, videoID string, mode Mode) error {
	jobType := "video_metadata_scan"
	if mode == ModeDownload {
		jobType = "download"
	}
	payload := `{"url":"https://www.youtube.com/watch?v=` + videoID + `"}`
	_, _, err := e.store.FindOrEnqueue(ctx, jobs.EnqueueParams{Type: jobType, PayloadJSON: payload}, func(ctx context.Context, tx *sql.Tx) (jobs.Job, bool, error) {
		return e.store.ActiveByPayloadTx(ctx, tx, jobType, payload)
	})

	return err
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

func TestPlaylistIdentityFromPathDerivesStableIDAndTitle(t *testing.T) {
	t.Parallel()

	identity := PlaylistIdentityFromPath("/some/dir/DnB-videos.csv")
	if identity.ID != "csv-dnb-videos" || identity.Title != "DnB-videos" || identity.ExternalID != "DnB-videos" || identity.ChannelID != "" {
		t.Fatalf("unexpected identity: %#v", identity)
	}
	identity2 := PlaylistIdentityFromPath("/some/dir/My  Cool! Playlist.CSV")
	if identity2.ID != "csv-my-cool-playlist" || identity2.Title != "My  Cool! Playlist" {
		t.Fatalf("unexpected identity: id=%q title=%q", identity2.ID, identity2.Title)
	}
	identity3 := PlaylistIdentityFromPath("/some/dir/.hidden")
	if identity3.ID != "csv-hidden" || identity3.Title != ".hidden" {
		t.Fatalf("unexpected identity: id=%q title=%q", identity3.ID, identity3.Title)
	}
}

func TestImportLinksExistingVideos(t *testing.T) {
	t.Parallel()

	db := openDB(t)
	enqueuer := newTestEnqueuer(jobs.NewStore(db))
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

	report, err := ImportFile(context.Background(), db, enqueuer, path, ModeLinkOnly)
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
	enqueuer := newTestEnqueuer(jobs.NewStore(db))
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
	if _, err := ImportFile(context.Background(), db, enqueuer, path, ModeLinkOnly); err != nil {
		t.Fatal(err)
	}
	// Re-import with a different set: entries must be replaced, not added to.
	write("Arj1LYD4ano\n")
	if _, err := ImportFile(context.Background(), db, enqueuer, path, ModeLinkOnly); err != nil {
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

func TestImportIntoUsesExplicitURLIdentity(t *testing.T) {
	t.Parallel()

	db := openDB(t)
	enqueuer := newTestEnqueuer(jobs.NewStore(db))
	if _, err := db.Exec(`
INSERT INTO channels (id, external_id, name) VALUES ('UCchannel1', 'UCchannel1', 'Channel');
INSERT INTO videos (id, source, external_id, channel_id, title, duration_seconds)
VALUES ('v1', 'youtube', 'CtCgNRquauE', 'UCchannel1', 'Video One', 60),
       ('v2', 'youtube', 'Arj1LYD4ano', 'UCchannel1', 'Video Two', 60);`); err != nil {
		t.Fatal(err)
	}

	identity := PlaylistIdentity{
		ID:         "yt-PLtestListID1234567890",
		ExternalID: "PLtestListID1234567890",
		Title:      "Best of DnB",
		ChannelID:  "UCchannel1",
	}
	entries := []Entry{{VideoID: "CtCgNRquauE"}, {VideoID: "Arj1LYD4ano"}, {VideoID: "AAAAbbbbCCC"}}
	report, err := ImportInto(context.Background(), db, enqueuer, identity, entries, ModeMetadataScan)
	if err != nil {
		t.Fatal(err)
	}
	if report.Linked != 2 || report.Missing != 1 || report.Enqueued != 1 {
		t.Fatalf("unexpected report: %#v", report)
	}

	var externalID string
	var channelID sql.NullString
	if err := db.QueryRow("SELECT external_id, channel_id FROM playlists WHERE id = 'yt-PLtestListID1234567890'").Scan(&externalID, &channelID); err != nil {
		t.Fatal(err)
	}
	if externalID != "PLtestListID1234567890" {
		t.Fatalf("expected external_id PLtestListID1234567890, got %q", externalID)
	}
	if !channelID.Valid || channelID.String != "UCchannel1" {
		t.Fatalf("expected linked channel UCchannel1, got %#v", channelID)
	}
}

func TestImportIntoLinksChannelOnlyWhenItExists(t *testing.T) {
	t.Parallel()

	db := openDB(t)
	enqueuer := newTestEnqueuer(jobs.NewStore(db))

	identity := PlaylistIdentity{
		ID:         "yt-PLmissingChannel000",
		ExternalID: "PLmissingChannel000",
		Title:      "Unknown channel playlist",
		ChannelID:  "UCnotarchived",
	}
	_, err := ImportInto(context.Background(), db, enqueuer, identity, []Entry{{VideoID: "CtCgNRquauE"}}, ModeLinkOnly)
	if err != nil {
		t.Fatal(err)
	}

	var channelID sql.NullString
	if err := db.QueryRow("SELECT channel_id FROM playlists WHERE id = 'yt-PLmissingChannel000'").Scan(&channelID); err != nil {
		t.Fatal(err)
	}
	if channelID.Valid {
		t.Fatalf("expected channel link dropped when channel is not archived, got %#v", channelID)
	}
}

func TestImportIntoRefreshesSamePlaylistByID(t *testing.T) {
	t.Parallel()

	db := openDB(t)
	enqueuer := newTestEnqueuer(jobs.NewStore(db))
	if _, err := db.Exec(`
INSERT INTO videos (id, source, external_id, title, duration_seconds)
VALUES ('v1', 'youtube', 'CtCgNRquauE', 'Video One', 60),
       ('v2', 'youtube', 'Arj1LYD4ano', 'Video Two', 60);`); err != nil {
		t.Fatal(err)
	}

	identity := PlaylistIdentity{ID: "yt-PLsameListID", ExternalID: "PLsameListID", Title: "First title"}
	if _, err := ImportInto(context.Background(), db, enqueuer, identity, []Entry{{VideoID: "CtCgNRquauE"}, {VideoID: "Arj1LYD4ano"}}, ModeLinkOnly); err != nil {
		t.Fatal(err)
	}
	// Re-importing the same list id refreshes entries and the title.
	identity.Title = "Second title"
	if _, err := ImportInto(context.Background(), db, enqueuer, identity, []Entry{{VideoID: "Arj1LYD4ano"}}, ModeLinkOnly); err != nil {
		t.Fatal(err)
	}

	var title string
	var count int
	if err := db.QueryRow("SELECT title FROM playlists WHERE id = 'yt-PLsameListID'").Scan(&title); err != nil {
		t.Fatal(err)
	}
	if title != "Second title" {
		t.Fatalf("expected refreshed title, got %q", title)
	}
	if err := db.QueryRow("SELECT count(*) FROM playlist_entries WHERE playlist_id = 'yt-PLsameListID'").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("expected 1 entry after re-import, got %d", count)
	}
}

func TestImportEnqueuesDownloadsForMissingVideos(t *testing.T) {
	t.Parallel()

	db := openDB(t)
	enqueuer := newTestEnqueuer(jobs.NewStore(db))

	path := filepath.Join(t.TempDir(), "playlist.csv")
	if err := os.WriteFile(path, []byte("Video ID\nCtCgNRquauE\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	report, err := ImportFile(context.Background(), db, enqueuer, path, ModeDownload)
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
	enqueuer := newTestEnqueuer(jobs.NewStore(db))

	path := filepath.Join(t.TempDir(), "playlist.csv")
	if err := os.WriteFile(path, []byte("Video ID\nCtCgNRquauE\nAAAAbbbbCCC\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	report, err := ImportFile(context.Background(), db, enqueuer, path, "")
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
	enqueuer := newTestEnqueuer(jobs.NewStore(db))

	path := filepath.Join(t.TempDir(), "playlist.csv")
	write := func() {
		t.Helper()
		if err := os.WriteFile(path, []byte("Video ID\nCtCgNRquauE\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	write()
	if _, err := ImportFile(context.Background(), db, enqueuer, path, ModeMetadataScan); err != nil {
		t.Fatal(err)
	}
	// Re-importing the same file must not enqueue a second scan job for the
	// same URL while the first is still queued/running.
	write()
	if _, err := ImportFile(context.Background(), db, enqueuer, path, ModeMetadataScan); err != nil {
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
	enqueuer := newTestEnqueuer(jobs.NewStore(db))

	path := filepath.Join(t.TempDir(), "playlist.csv")
	if err := os.WriteFile(path, []byte("Video ID\nCtCgNRquauE\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	report, err := ImportFile(context.Background(), db, enqueuer, path, ModeLinkOnly)
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
