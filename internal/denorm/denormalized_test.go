package denorm

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"kapsel/internal/database"
)

func openDenormTestDB(t *testing.T) *sql.DB {
	t.Helper()

	db, err := database.Open(context.Background(), filepath.Join(t.TempDir(), "kapsel.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Fatal(err)
		}
	})

	if err := database.Migrate(context.Background(), db); err != nil {
		t.Fatal(err)
	}

	return db
}

func seedDenormChannel(t *testing.T, db *sql.DB, id string, name string, videoTitles ...string) {
	t.Helper()

	if _, err := db.Exec("INSERT INTO channels (id, external_id, name) VALUES (?, ?, ?)", id, id, name); err != nil {
		t.Fatal(err)
	}
	for index, title := range videoTitles {
		videoID := id + "-vid-" + string(rune('a'+index))
		if _, err := db.Exec(
			"INSERT INTO videos (id, external_id, channel_id, title) VALUES (?, ?, ?, ?)",
			videoID, videoID, id, title,
		); err != nil {
			t.Fatal(err)
		}
	}
}

func searchDocText(t *testing.T, db *sql.DB, ownerType string, ownerID string, field string) (string, bool) {
	t.Helper()

	var text string
	err := db.QueryRow(
		"SELECT text FROM search_documents WHERE owner_type = ? AND owner_id = ? AND field = ?",
		ownerType, ownerID, field,
	).Scan(&text)
	if err == sql.ErrNoRows {
		return "", false
	}
	if err != nil {
		t.Fatal(err)
	}
	return text, true
}

func TestSyncSearchDocumentInsertsUpdatesAndDeletes(t *testing.T) {
	t.Parallel()

	db := openDenormTestDB(t)
	ctx := context.Background()

	if err := SyncSearchDocument(ctx, db, "video", "vid-1", "title", "Island walkthrough"); err != nil {
		t.Fatal(err)
	}
	if text, ok := searchDocText(t, db, "video", "vid-1", "title"); !ok || text != "Island walkthrough" {
		t.Fatalf("expected inserted doc, got %q (present=%v)", text, ok)
	}

	if err := SyncSearchDocument(ctx, db, "video", "vid-1", "title", "Renamed walkthrough"); err != nil {
		t.Fatal(err)
	}
	if text, _ := searchDocText(t, db, "video", "vid-1", "title"); text != "Renamed walkthrough" {
		t.Fatalf("expected rewritten doc, got %q", text)
	}

	if err := SyncSearchDocument(ctx, db, "video", "vid-1", "title", ""); err != nil {
		t.Fatal(err)
	}
	if _, ok := searchDocText(t, db, "video", "vid-1", "title"); ok {
		t.Fatal("expected empty sync to delete the doc")
	}
}

func TestSyncVideoChannelSearchDocument(t *testing.T) {
	t.Parallel()

	db := openDenormTestDB(t)
	ctx := context.Background()

	if err := SyncVideoChannelSearchDocument(ctx, db, "vid-1", "Adam Stew", "Camping on an Island"); err != nil {
		t.Fatal(err)
	}
	if text, ok := searchDocText(t, db, "video", "vid-1", "channel"); !ok || text != "Adam Stew Camping on an Island" {
		t.Fatalf("expected combined name+title doc, got %q (present=%v)", text, ok)
	}

	if err := SyncVideoChannelSearchDocument(ctx, db, "vid-1", "Adam Stew", "Renamed Island Trip"); err != nil {
		t.Fatal(err)
	}
	if text, _ := searchDocText(t, db, "video", "vid-1", "channel"); text != "Adam Stew Renamed Island Trip" {
		t.Fatalf("expected title change to rewrite the doc, got %q", text)
	}

	if err := SyncVideoChannelSearchDocument(ctx, db, "vid-1", "", "Camping on an Island"); err != nil {
		t.Fatal(err)
	}
	if _, ok := searchDocText(t, db, "video", "vid-1", "channel"); ok {
		t.Fatal("expected empty channel name to delete the doc")
	}
}

func TestSyncChannelVideoSearchDocumentsWritesStaleAndMatchingRows(t *testing.T) {
	t.Parallel()

	db := openDenormTestDB(t)
	ctx := context.Background()
	seedDenormChannel(t, db, "chan-1", "Adam Stew", "Island Trip", "Desert Hike")

	if _, err := db.Exec(`
INSERT INTO search_documents (owner_type, owner_id, field, text) VALUES
  ('video', 'chan-1-vid-a', 'channel', 'Adam Stew Island Trip'),
  ('video', 'chan-1-vid-b', 'channel', 'stale text from an interrupted write')`); err != nil {
		t.Fatal(err)
	}

	if err := SyncChannelVideoSearchDocuments(ctx, db, "chan-1", "Adam Stew"); err != nil {
		t.Fatal(err)
	}
	if text, _ := searchDocText(t, db, "video", "chan-1-vid-a", "channel"); text != "Adam Stew Island Trip" {
		t.Fatalf("expected matching doc to keep its text, got %q", text)
	}
	if text, _ := searchDocText(t, db, "video", "chan-1-vid-b", "channel"); text != "Adam Stew Desert Hike" {
		t.Fatalf("expected stale doc to be rewritten, got %q", text)
	}
}

func TestSyncChannelVideoSearchDocumentsRenameRewritesAllVideos(t *testing.T) {
	t.Parallel()

	db := openDenormTestDB(t)
	ctx := context.Background()
	seedDenormChannel(t, db, "chan-1", "Old Name", "Island Trip", "Desert Hike")

	if err := SyncChannelVideoSearchDocuments(ctx, db, "chan-1", "New Name"); err != nil {
		t.Fatal(err)
	}
	for videoID, want := range map[string]string{
		"chan-1-vid-a": "New Name Island Trip",
		"chan-1-vid-b": "New Name Desert Hike",
	} {
		text, ok := searchDocText(t, db, "video", videoID, "channel")
		if !ok {
			t.Fatalf("expected channel doc for %s", videoID)
		}
		if text != want {
			t.Fatalf("expected %s doc rewritten to %q, got %q", videoID, want, text)
		}
	}
}

func TestSyncChannelVideoSearchDocumentsEmptyNameAndMissingChannelAreNoOps(t *testing.T) {
	t.Parallel()

	db := openDenormTestDB(t)
	ctx := context.Background()
	seedDenormChannel(t, db, "chan-1", "Adam Stew", "Island Trip")

	if err := SyncChannelVideoSearchDocuments(ctx, db, "chan-1", "Adam Stew"); err != nil {
		t.Fatal(err)
	}
	// An empty name must not wipe the channel's existing docs.
	if err := SyncChannelVideoSearchDocuments(ctx, db, "chan-1", "  "); err != nil {
		t.Fatal(err)
	}
	if text, ok := searchDocText(t, db, "video", "chan-1-vid-a", "channel"); !ok || text != "Adam Stew Island Trip" {
		t.Fatalf("expected empty-name sync to leave docs untouched, got %q (present=%v)", text, ok)
	}

	// A channel with no videos must not error.
	if err := SyncChannelVideoSearchDocuments(ctx, db, "chan-unknown", "Some Name"); err != nil {
		t.Fatal(err)
	}
}

func TestSyncChannelVideoSearchDocumentsSkipsMatchingRowsWithoutRewrite(t *testing.T) {
	t.Parallel()

	db := openDenormTestDB(t)
	ctx := context.Background()
	seedDenormChannel(t, db, "chan-1", "Adam Stew", "Island Trip")

	if err := SyncChannelVideoSearchDocuments(ctx, db, "chan-1", "Adam Stew"); err != nil {
		t.Fatal(err)
	}
	// Stamp a sentinel updated_at: a rewrite would refresh it, a skip keeps it.
	if _, err := db.Exec("UPDATE search_documents SET updated_at = '2000-01-01T00:00:00.000Z' WHERE owner_type = 'video' AND owner_id = 'chan-1-vid-a' AND field = 'channel'"); err != nil {
		t.Fatal(err)
	}
	if err := SyncChannelVideoSearchDocuments(ctx, db, "chan-1", "Adam Stew"); err != nil {
		t.Fatal(err)
	}
	var updatedAt string
	if err := db.QueryRow("SELECT updated_at FROM search_documents WHERE owner_type = 'video' AND owner_id = 'chan-1-vid-a' AND field = 'channel'").Scan(&updatedAt); err != nil {
		t.Fatal(err)
	}
	if updatedAt != "2000-01-01T00:00:00.000Z" {
		t.Fatalf("expected matching doc to be skipped without rewrite, updated_at moved to %q", updatedAt)
	}
}

func TestChannelNameReturnsStoredOrEmpty(t *testing.T) {
	t.Parallel()

	db := openDenormTestDB(t)
	ctx := context.Background()

	name, err := ChannelName(ctx, db, "chan-unknown")
	if err != nil {
		t.Fatal(err)
	}
	if name != "" {
		t.Fatalf("expected empty name for missing channel, got %q", name)
	}

	seedDenormChannel(t, db, "chan-1", "Adam Stew")
	name, err = ChannelName(ctx, db, "chan-1")
	if err != nil {
		t.Fatal(err)
	}
	if name != "Adam Stew" {
		t.Fatalf("expected stored name, got %q", name)
	}
}
