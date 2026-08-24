package subsimport

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

func TestParseCommaSeparatedSubscriptions(t *testing.T) {
	t.Parallel()

	csvData := "Channel Id,Channel Url,Channel Title\n" +
		"UCAAAA,https://www.youtube.com/channel/UCAAAA,Channel A\n" +
		",https://www.youtube.com/@handleb,Channel B\n" +
		"UCBBBB,,Channel C (id only)\n"
	entries, err := Parse(strings.NewReader(csvData))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 3 {
		t.Fatalf("expected 3 entries, got %d", len(entries))
	}
	if entries[0].ChannelID != "UCAAAA" || entries[0].ChannelURL != "https://www.youtube.com/channel/UCAAAA" {
		t.Fatalf("unexpected first entry: %#v", entries[0])
	}
	if entries[1].ChannelURL != "https://www.youtube.com/@handleb" {
		t.Fatalf("unexpected second entry URL: %q", entries[1].ChannelURL)
	}
	if entries[2].ChannelID != "UCBBBB" || entries[2].ChannelURL != "" {
		t.Fatalf("unexpected third entry: %#v", entries[2])
	}
}

func TestParseSkipsMalformedOrEmptyRows(t *testing.T) {
	t.Parallel()

	csvData := "Channel Id,Channel Url,Channel Title\n" +
		"UCAAAA,https://www.youtube.com/channel/UCAAAA,Channel A\n" +
		",,, \n" + // empty row
		" , , \n" // whitespace-only row
	entries, err := Parse(strings.NewReader(csvData))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}
}

func TestParseToleratesBOMInHeader(t *testing.T) {
	t.Parallel()

	csvData := "\uFEFFChannel Id,Channel Url,Channel Title\n" +
		"UCAAAA,https://www.youtube.com/channel/UCAAAA,Channel A\n"
	entries, err := Parse(strings.NewReader(csvData))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].ChannelID != "UCAAAA" {
		t.Fatalf("expected BOM header to be handled, got %#v", entries)
	}
}

func TestParseEmptyInputReturnsEmpty(t *testing.T) {
	t.Parallel()

	entries, err := Parse(strings.NewReader(""))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("expected no entries for empty input, got %d", len(entries))
	}
}

func TestParseMissingRequiredColumnsErrors(t *testing.T) {
	t.Parallel()

	_, err := Parse(strings.NewReader("Foo,Bar\n1,2\n"))
	if err == nil {
		t.Fatal("expected an error when channel columns are missing")
	}
}

func TestEnqueueChannelsEnqueuesChannelJobs(t *testing.T) {
	t.Parallel()

	db := openDB(t)
	store := jobs.NewStore(db)
	entries := []Entry{
		{ChannelID: "UCAAAA", ChannelURL: "https://www.youtube.com/channel/UCAAAA"},
		{ChannelID: "", ChannelURL: "https://www.youtube.com/@handleb"},
	}
	enqueued, err := EnqueueChannels(context.Background(), store, entries, false)
	if err != nil {
		t.Fatal(err)
	}
	if enqueued != 2 {
		t.Fatalf("expected 2 enqueued channels, got %d", enqueued)
	}

	items, err := store.List(context.Background(), jobs.ListOptions{PageSize: 10})
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, item := range items.Jobs {
		if item.Type == "channel_first_download" {
			count++
		}
	}
	if count != 2 {
		t.Fatalf("expected 2 channel_first_download jobs, got %d", count)
	}
}

func TestEnqueueChannelsSkipsMalformedURLs(t *testing.T) {
	t.Parallel()

	db := openDB(t)
	store := jobs.NewStore(db)
	entries := []Entry{
		{ChannelID: "UCAAAA", ChannelURL: "https://www.youtube.com/channel/UCAAAA"},
		{ChannelID: "", ChannelURL: "not-a-valid-url"},
	}
	enqueued, err := EnqueueChannels(context.Background(), store, entries, false)
	if err != nil {
		t.Fatal(err)
	}
	if enqueued != 1 {
		t.Fatalf("expected 1 enqueued channel (malformed skipped), got %d", enqueued)
	}
}

func TestImportFileProducesReport(t *testing.T) {
	t.Parallel()

	db := openDB(t)
	store := jobs.NewStore(db)
	csvPath := filepath.Join(t.TempDir(), "subscriptions.csv")
	csvData := "Channel Id,Channel Url,Channel Title\n" +
		"UCAAAA,https://www.youtube.com/channel/UCAAAA,Channel A\n" +
		"UCBBBB,https://www.youtube.com/channel/UCBBBB,Channel B\n"
	if err := os.WriteFile(csvPath, []byte(csvData), 0o644); err != nil {
		t.Fatal(err)
	}

	report, err := ImportFile(context.Background(), store, csvPath, false)
	if err != nil {
		t.Fatal(err)
	}
	if report.Parsed != 2 {
		t.Fatalf("expected parsed=2, got %d", report.Parsed)
	}
	if report.Enqueued != 2 {
		t.Fatalf("expected enqueued=2, got %d", report.Enqueued)
	}
	if report.Skipped != 0 {
		t.Fatalf("expected skipped=0, got %d", report.Skipped)
	}
}

func TestImportFileMissingFileErrors(t *testing.T) {
	t.Parallel()

	db := openDB(t)
	store := jobs.NewStore(db)
	if _, err := ImportFile(context.Background(), store, "/nonexistent/subscriptions.csv", false); err == nil {
		t.Fatal("expected an error for a missing file")
	}
}

func TestEnqueueChannelsScanOnlyMarksJobPayload(t *testing.T) {
	t.Parallel()

	db := openDB(t)
	store := jobs.NewStore(db)
	entries := []Entry{
		{ChannelID: "UCAAAA", ChannelURL: "https://www.youtube.com/channel/UCAAAA"},
	}
	enqueued, err := EnqueueChannels(context.Background(), store, entries, true)
	if err != nil {
		t.Fatal(err)
	}
	if enqueued != 1 {
		t.Fatalf("expected 1 enqueued channel, got %d", enqueued)
	}

	var payload string
	if err := db.QueryRow("SELECT payload_json FROM jobs WHERE type = ?", "channel_first_download").Scan(&payload); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(payload, `"scan_only":true`) {
		t.Fatalf("expected scan_only to be set on the job payload, got %q", payload)
	}
}

func TestEnqueueChannelsDownloadModeHasNoScanOnly(t *testing.T) {
	t.Parallel()

	db := openDB(t)
	store := jobs.NewStore(db)
	entries := []Entry{
		{ChannelID: "UCAAAA", ChannelURL: "https://www.youtube.com/channel/UCAAAA"},
	}
	enqueued, err := EnqueueChannels(context.Background(), store, entries, false)
	if err != nil {
		t.Fatal(err)
	}
	if enqueued != 1 {
		t.Fatalf("expected 1 enqueued channel, got %d", enqueued)
	}

	var payload string
	if err := db.QueryRow("SELECT payload_json FROM jobs WHERE type = ?", "channel_first_download").Scan(&payload); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(payload, `"scan_only"`) {
		t.Fatalf("expected no scan_only in download-mode payload, got %q", payload)
	}
}
