package search

import (
	"context"
	"database/sql"
	"path/filepath"
	"strconv"
	"testing"

	"kapsel/internal/database"
)

func TestSearchMatchesSeededDocuments(t *testing.T) {
	t.Parallel()

	db := openSearchTestDB(t)
	seedSearchDocuments(t, db)

	tests := []struct {
		name      string
		term      string
		ownerType string
		ownerID   string
	}{
		{name: "video title", term: "Kapsel", ownerType: "video", ownerID: "vid-1"},
		{name: "channel name", term: "Workshop", ownerType: "channel", ownerID: "chan-1"},
		{name: "subtitle text", term: "lunar", ownerType: "subtitle", ownerID: "sub-1"},
		{name: "comment text", term: "cabinet", ownerType: "comment", ownerID: "comment-1"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			results, err := Search(context.Background(), db, Query{Term: tt.term, Limit: 10})
			if err != nil {
				t.Fatal(err)
			}

			assertResult(t, results, tt.ownerType, tt.ownerID)
		})
	}
}

func TestSearchBoundsResultCount(t *testing.T) {
	t.Parallel()

	db := openSearchTestDB(t)
	for i := range 75 {
		_, err := db.Exec(
			"INSERT INTO search_documents (owner_type, owner_id, field, text) VALUES (?, ?, ?, ?)",
			"video",
			"vid-many-"+strconv.Itoa(i),
			"title",
			"kapsel archive result",
		)
		if err != nil {
			t.Fatal(err)
		}
	}

	results, err := Search(context.Background(), db, Query{Term: "kapsel", Limit: 500})
	if err != nil {
		t.Fatal(err)
	}

	if len(results) != MaxLimit {
		t.Fatalf("expected bounded result count %d, got %d", MaxLimit, len(results))
	}
}

func TestSearchHydratesArchiveRecords(t *testing.T) {
	t.Parallel()

	db := openSearchTestDB(t)
	seedHydratedSearchRecords(t, db)

	results, err := Search(context.Background(), db, Query{Term: "kapsel", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}

	video := findResult(t, results, "video", "vid-1")
	if video.Record.Title != "Kapsel walkthrough" || video.Record.Channel == nil || video.Record.Channel.Name != "Archive Workshop" || video.Record.DurationSeconds != 120 {
		t.Fatalf("expected hydrated video record, got %#v", video.Record)
	}
	channel := findResult(t, results, "channel", "chan-1")
	if channel.Record.Title != "Archive Workshop" || channel.Record.Description != "Local archiving channel" {
		t.Fatalf("expected hydrated channel record, got %#v", channel.Record)
	}
	playlist := findResult(t, results, "playlist", "playlist-1")
	if playlist.Record.Title != "Kapsel playlist" || playlist.Record.Description != "Curated archive videos" {
		t.Fatalf("expected hydrated playlist record, got %#v", playlist.Record)
	}
	subtitle := findResult(t, results, "subtitle", "vid-1")
	if subtitle.Record.Type != "video" || subtitle.Record.ID != "vid-1" || subtitle.Record.Title != "Kapsel walkthrough" {
		t.Fatalf("expected subtitle to hydrate owning video, got %#v", subtitle.Record)
	}
	comment := findResult(t, results, "comment", "comment-1")
	if comment.Record.Type != "video" || comment.Record.ID != "vid-1" || comment.Record.Title != "Kapsel walkthrough" {
		t.Fatalf("expected comment to hydrate owning video, got %#v", comment.Record)
	}
}

func TestHydrateResultsPreservesOrderAndSnippets(t *testing.T) {
	t.Parallel()

	db := openSearchTestDB(t)
	seedHydratedSearchRecords(t, db)
	if _, err := db.Exec(`
INSERT INTO videos (id, external_id, channel_id, title, description, duration_seconds) VALUES ('vid-2', 'vid-2', 'chan-1', 'Second Kapsel video', 'Another archive result', 90);
INSERT INTO comments (id, video_id, text) VALUES ('comment-2', 'vid-2', 'Second comment')`); err != nil {
		t.Fatal(err)
	}
	results := []Result{
		{OwnerType: "comment", OwnerID: "comment-2", Field: "text", Snippet: "second snippet", Rank: 0.1},
		{OwnerType: "video", OwnerID: "vid-1", Field: "title", Snippet: "first snippet", Rank: 0.2},
		{OwnerType: "video", OwnerID: "vid-2", Field: "title", Snippet: "video two snippet", Rank: 0.3},
		{OwnerType: "subtitle", OwnerID: "vid-1", Field: "text:en:downloaded", Snippet: "subtitle snippet", Rank: 0.4},
	}

	if err := hydrateResults(context.Background(), db, results); err != nil {
		t.Fatal(err)
	}

	if results[0].OwnerType != "comment" || results[0].OwnerID != "comment-2" || results[0].Snippet != "second snippet" || results[0].Record.ID != "vid-2" || results[0].Record.Title != "Second Kapsel video" {
		t.Fatalf("expected first comment result to keep order and hydrate owning video, got %#v", results[0])
	}
	if results[1].OwnerID != "vid-1" || results[1].Snippet != "first snippet" || results[1].Record.Title != "Kapsel walkthrough" {
		t.Fatalf("expected second video result to keep order and hydrate video one, got %#v", results[1])
	}
	if results[2].OwnerID != "vid-2" || results[2].Snippet != "video two snippet" || results[2].Record.Title != "Second Kapsel video" {
		t.Fatalf("expected third video result to keep order and hydrate video two, got %#v", results[2])
	}
	if results[3].OwnerType != "subtitle" || results[3].OwnerID != "vid-1" || results[3].Snippet != "subtitle snippet" || results[3].Record.Type != "video" || results[3].Record.Title != "Kapsel walkthrough" {
		t.Fatalf("expected subtitle result to keep order and hydrate video, got %#v", results[3])
	}
}

func TestSearchEscapesHighlightedSnippets(t *testing.T) {
	t.Parallel()

	db := openSearchTestDB(t)
	if _, err := db.Exec("INSERT INTO search_documents (owner_type, owner_id, field, text) VALUES ('video', 'vid-xss', 'description', '<img src=x onerror=alert(1)> kapsel')"); err != nil {
		t.Fatal(err)
	}

	results, err := Search(context.Background(), db, Query{Term: "kapsel", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	result := findResult(t, results, "video", "vid-xss")
	if result.Snippet != "&lt;img src=x onerror=alert(1)&gt; <mark>kapsel</mark>" {
		t.Fatalf("expected escaped highlighted snippet, got %q", result.Snippet)
	}
}

func openSearchTestDB(t *testing.T) *sql.DB {
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

func seedSearchDocuments(t *testing.T, db *sql.DB) {
	t.Helper()

	rows := []struct {
		ownerType string
		ownerID   string
		field     string
		text      string
	}{
		{ownerType: "video", ownerID: "vid-1", field: "title", text: "Kapsel walkthrough"},
		{ownerType: "channel", ownerID: "chan-1", field: "name", text: "Archive Workshop"},
		{ownerType: "subtitle", ownerID: "sub-1", field: "text", text: "A quiet lunar capsule floats past the archive"},
		{ownerType: "comment", ownerID: "comment-1", field: "text", text: "I loved the cabinet of preserved clips"},
	}

	for _, row := range rows {
		_, err := db.Exec(
			"INSERT INTO search_documents (owner_type, owner_id, field, text) VALUES (?, ?, ?, ?)",
			row.ownerType,
			row.ownerID,
			row.field,
			row.text,
		)
		if err != nil {
			t.Fatal(err)
		}
	}
}

func seedHydratedSearchRecords(t *testing.T, db *sql.DB) {
	t.Helper()

	if _, err := db.Exec(`
INSERT INTO channels (id, external_id, name, description) VALUES ('chan-1', 'chan-1', 'Archive Workshop', 'Local archiving channel');
INSERT INTO videos (id, external_id, channel_id, title, description, duration_seconds, thumbnail_url) VALUES ('vid-1', 'vid-1', 'chan-1', 'Kapsel walkthrough', 'A video about local archives', 120, 'https://i.ytimg.com/vi/vid-1/hqdefault.jpg');
INSERT INTO playlists (id, external_id, channel_id, title, description) VALUES ('playlist-1', 'playlist-1', 'chan-1', 'Kapsel playlist', 'Curated archive videos');
INSERT INTO comments (id, video_id, text) VALUES ('comment-1', 'vid-1', 'Great kapsel comment');
INSERT INTO search_documents (owner_type, owner_id, field, text) VALUES
  ('video', 'vid-1', 'title', 'Kapsel walkthrough'),
  ('channel', 'chan-1', 'name', 'Archive Workshop kapsel'),
  ('playlist', 'playlist-1', 'title', 'Kapsel playlist'),
  ('subtitle', 'vid-1', 'text:en:downloaded', 'Kapsel subtitle text'),
  ('comment', 'comment-1', 'text', 'Great kapsel comment')`); err != nil {
		t.Fatal(err)
	}
}

func assertResult(t *testing.T, results []Result, ownerType string, ownerID string) {
	t.Helper()

	for _, result := range results {
		if result.OwnerType == ownerType && result.OwnerID == ownerID {
			return
		}
	}

	t.Fatalf("expected result %s/%s in %#v", ownerType, ownerID, results)
}

func findResult(t *testing.T, results []Result, ownerType string, ownerID string) Result {
	t.Helper()

	for _, result := range results {
		if result.OwnerType == ownerType && result.OwnerID == ownerID {
			return result
		}
	}

	t.Fatalf("expected result %s/%s in %#v", ownerType, ownerID, results)
	return Result{}
}
