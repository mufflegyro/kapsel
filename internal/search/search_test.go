package search

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

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

func TestMatchExpressionQuotesTokensAndJoinsWithAnd(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		term string
		want string
	}{
		{name: "single token", term: "island", want: `"island"`},
		{name: "multiword", term: "adam stew island", want: `"adam" AND "stew" AND "island"`},
		{name: "collapses whitespace", term: "  island   cabin  ", want: `"island" AND "cabin"`},
		{name: "doubles embedded quotes", term: `say "hi" now`, want: `"say" AND """hi""" AND "now"`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := matchExpression(tt.term); got != tt.want {
				t.Fatalf("matchExpression(%q) = %q, want %q", tt.term, got, tt.want)
			}
		})
	}
}

func TestSearchMultiwordQueryMatchesAllTokensAnywhere(t *testing.T) {
	t.Parallel()

	db := openSearchTestDB(t)
	if _, err := db.Exec(`
INSERT INTO search_documents (owner_type, owner_id, field, text) VALUES
  ('video', 'vid-1', 'title', 'I Bought a Cabin on a Remote Island'),
  ('video', 'vid-2', 'title', 'Island cabin build'),
  ('video', 'vid-3', 'title', 'Cabin fever vlog')`); err != nil {
		t.Fatal(err)
	}

	results, err := Search(context.Background(), db, Query{Term: "cabin island", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}

	assertResult(t, results, "video", "vid-1")
	assertResult(t, results, "video", "vid-2")
	for _, result := range results {
		if result.OwnerID == "vid-3" {
			t.Fatalf("expected AND semantics to exclude documents missing a token, got %#v", result)
		}
	}
}

func TestSearchStatsCountRowsAndDistinctOwners(t *testing.T) {
	t.Parallel()

	db := openSearchTestDB(t)
	if _, err := db.Exec(`
INSERT INTO channels (id, external_id, name) VALUES ('chan-1', 'chan-1', 'Island Workshop');
INSERT INTO videos (id, external_id, channel_id, title, description, duration_seconds) VALUES
  ('vid-1', 'vid-1', 'chan-1', 'Island walkthrough', 'An island hop diary', 120),
  ('vid-2', 'vid-2', 'chan-1', 'Second island video', '', 90),
  ('mem-1', 'mem-1', 'chan-1', 'Members only island', '', 60);
UPDATE videos SET members_only = 1 WHERE id = 'mem-1';
INSERT INTO comments (id, video_id, text) VALUES ('comment-1', 'vid-1', 'Island vibes comment');
INSERT INTO search_documents (owner_type, owner_id, field, text) VALUES
  ('video', 'vid-1', 'title', 'Island walkthrough'),
  ('video', 'vid-1', 'description', 'An island hop diary'),
  ('video', 'vid-1', 'channel', 'Island Workshop Island walkthrough'),
  ('subtitle', 'vid-1', 'text:en:downloaded', 'island chatter transcript'),
  ('comment', 'comment-1', 'text', 'Island vibes comment'),
  ('video', 'vid-2', 'title', 'Second island video'),
  ('video', 'mem-1', 'title', 'Members only island'),
  ('channel', 'chan-1', 'name', 'Island Workshop')`); err != nil {
		t.Fatal(err)
	}

	stats, err := Stats(context.Background(), db, "island")
	if err != nil {
		t.Fatal(err)
	}

	if stats.Total != 8 {
		t.Fatalf("expected 8 matching rows, got %d", stats.Total)
	}
	// vid-1 folds its title/description/subtitle/comment docs into one owner,
	// vid-2 and the channel add one each, and the members-only video is hidden.
	if stats.DistinctOwners != 3 {
		t.Fatalf("expected 3 distinct owners, got %d", stats.DistinctOwners)
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

	var err error
	results, err = hydrateResults(context.Background(), db, results)
	if err != nil {
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

func TestSearchExcludesMembersOnlyVideos(t *testing.T) {
	t.Parallel()

	db := openSearchTestDB(t)
	seedHydratedSearchRecords(t, db)
	if _, err := db.Exec(`
INSERT INTO channels (id, external_id, name) VALUES ('chan-mem', 'chan-mem', 'Members Channel');
INSERT INTO videos (id, external_id, channel_id, title, description, duration_seconds, members_only) VALUES ('mem-1', 'mem-1', 'chan-mem', 'Private members capsul', 'Members ONLY secret archive', 90, 1);
INSERT INTO search_documents (owner_type, owner_id, field, text) VALUES ('video', 'mem-1', 'title', 'Private members capsul secret')`); err != nil {
		t.Fatal(err)
	}

	results, err := Search(context.Background(), db, Query{Term: "secret", Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	for _, result := range results {
		if result.Record.Type == "video" && result.Record.ID == "mem-1" {
			t.Fatalf("expected members-only video to be excluded from search, got %#v", result)
		}
	}
}

func seedDatedVideo(t *testing.T, db *sql.DB, id string, field string, text string, publishedAt time.Time) {
	t.Helper()

	if _, err := db.Exec(
		"INSERT INTO videos (id, external_id, title, published_at) VALUES (?, ?, ?, ?)",
		id, id, text, publishedAt.UTC().Format(time.RFC3339),
	); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(
		"INSERT INTO search_documents (owner_type, owner_id, field, text) VALUES ('video', ?, ?, ?)",
		id, field, text,
	); err != nil {
		t.Fatal(err)
	}
}

func seedCommentDoc(t *testing.T, db *sql.DB, commentID string, videoID string, publishedAt time.Time) {
	t.Helper()

	if _, err := db.Exec(
		"INSERT INTO videos (id, external_id, title, published_at) VALUES (?, ?, ?, ?)",
		videoID, videoID, "Commented video", publishedAt.UTC().Format(time.RFC3339),
	); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(
		"INSERT INTO comments (id, video_id, text) VALUES (?, ?, 'Island')", commentID, videoID,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(
		"INSERT INTO search_documents (owner_type, owner_id, field, text) VALUES ('comment', ?, 'text', 'Island')", commentID,
	); err != nil {
		t.Fatal(err)
	}
}

// Identical title docs differing only in publish date: the re-rank must
// order the fresh upload first, where raw BM25 cannot separate them.
func TestSearchReranksFreshVideosAboveOldTitles(t *testing.T) {
	t.Parallel()

	db := openSearchTestDB(t)
	now := time.Now()
	seedDatedVideo(t, db, "vid-fresh", "title", "Island", now.Add(-48*time.Hour))
	seedDatedVideo(t, db, "vid-old", "title", "Island", now.Add(-8*365*24*time.Hour))

	results, err := Search(context.Background(), db, Query{Term: "island", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 || results[0].OwnerID != "vid-fresh" || results[1].OwnerID != "vid-old" {
		t.Fatalf("expected fresh video ranked above old video, got %#v", results)
	}
}

// Same doc text and age in different fields: the title weight must lift the
// title row above the comment row where BM25 alone cannot separate them.
func TestSearchWeightsTitleMatchesAboveTextMatches(t *testing.T) {
	t.Parallel()

	db := openSearchTestDB(t)
	seedDatedVideo(t, db, "vid-title", "title", "Island", time.Now().Add(-24*time.Hour))
	seedCommentDoc(t, db, "comment-1", "vid-comment", time.Now().Add(-24*time.Hour))

	results, err := Search(context.Background(), db, Query{Term: "island", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 || results[0].OwnerID != "vid-title" || results[0].Field != "title" {
		t.Fatalf("expected title match ranked above comment match, got %#v", results)
	}
}

// The proposal's editorial constraint: a strong title match from years ago
// must still beat a weak transcript/comment match from today.
func TestSearchOldTitleStillBeatsFreshComment(t *testing.T) {
	t.Parallel()

	db := openSearchTestDB(t)
	now := time.Now()
	seedDatedVideo(t, db, "vid-oldtitle", "title", "Island", now.Add(-7*365*24*time.Hour))
	seedCommentDoc(t, db, "comment-1", "vid-comment", now.Add(-24*time.Hour))

	results, err := Search(context.Background(), db, Query{Term: "island", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 || results[0].OwnerID != "vid-oldtitle" {
		t.Fatalf("expected 7-year-old title match ranked above fresh comment match, got %#v", results)
	}
}

// The candidate pool must extend past the returned page: a fresh long-title
// video sitting below 60 old short-title docs in raw BM25 order must still
// surface on the first page after re-ranking.
func TestSearchPoolSurfacesFreshVideoBeyondBM25Cutoff(t *testing.T) {
	t.Parallel()

	db := openSearchTestDB(t)
	now := time.Now()
	old := now.Add(-8 * 365 * 24 * time.Hour)
	for i := range 60 {
		seedDatedVideo(t, db, "vid-old-"+fmt.Sprintf("%02d", i), "title", "Island", old)
	}
	seedDatedVideo(t, db, "vid-fresh", "title", "A long walkthrough diary about an island adventure trip", now.Add(-24*time.Hour))

	results, err := Search(context.Background(), db, Query{Term: "island", Limit: MaxLimit})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != MaxLimit {
		t.Fatalf("expected a full page of %d results, got %d", MaxLimit, len(results))
	}
	for index, result := range results {
		if result.OwnerID == "vid-fresh" {
			if index >= MaxLimit {
				t.Fatalf("expected fresh video within the first page, got index %d", index)
			}
			return
		}
	}
	t.Fatal("expected fresh video to appear within the first page")
}

// Offsets page through the re-ranked list; doc lengths grow strictly, so
// raw BM25 order is deterministic and the windows are exact.
func TestSearchOffsetWindow(t *testing.T) {
	t.Parallel()

	db := openSearchTestDB(t)
	for i := range 10 {
		text := "Island" + strings.Repeat(" pad", i)
		if _, err := db.Exec(
			"INSERT INTO search_documents (owner_type, owner_id, field, text) VALUES ('video', ?, 'title', ?)",
			"vid-"+strconv.Itoa(i), text,
		); err != nil {
			t.Fatal(err)
		}
	}

	assertWindow := func(offset int, want []string) {
		t.Helper()
		results, err := Search(context.Background(), db, Query{Term: "island", Limit: 3, Offset: offset})
		if err != nil {
			t.Fatal(err)
		}
		if len(results) != len(want) {
			t.Fatalf("offset %d: expected %d results, got %#v", offset, len(want), results)
		}
		for index, id := range want {
			if results[index].OwnerID != id {
				t.Fatalf("offset %d: expected %s at position %d, got %#v", offset, id, index, results[index])
			}
		}
	}

	assertWindow(0, []string{"vid-0", "vid-1", "vid-2"})
	assertWindow(4, []string{"vid-4", "vid-5", "vid-6"})
	assertWindow(50, []string{})
}

func seedChannelDoc(t *testing.T, db *sql.DB, channelID string, field string, text string) {
	t.Helper()

	if _, err := db.Exec(
		"INSERT INTO channels (id, external_id, name) VALUES (?, ?, ?)", channelID, channelID, text,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(
		"INSERT INTO search_documents (owner_type, owner_id, field, text) VALUES ('channel', ?, ?, ?)",
		channelID, field, text,
	); err != nil {
		t.Fatal(err)
	}
}

// A channel description match ranked below the whole episode window must
// still be returned with the page — the channels & playlists block would
// otherwise render empty for any term that matches many videos.
func TestSearchSecondaryReturnedBeyondEpisodeWindow(t *testing.T) {
	t.Parallel()

	db := openSearchTestDB(t)
	now := time.Now()
	for i := range 60 {
		seedDatedVideo(t, db, "vid-music-"+fmt.Sprintf("%02d", i), "title", "Music", now.Add(-48*time.Hour))
	}
	seedChannelDoc(t, db, "chan-desc", "description", "A quiet music archive")

	results, err := Search(context.Background(), db, Query{Term: "music", Limit: MaxLimit})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != MaxLimit+1 {
		t.Fatalf("expected a full episode window plus the channel row, got %d rows", len(results))
	}
	secondary := results[MaxLimit:]
	if len(secondary) != 1 || secondary[0].OwnerType != "channel" || secondary[0].OwnerID != "chan-desc" {
		t.Fatalf("expected the channel description row appended after the window, got %#v", secondary)
	}
	for _, result := range results[:MaxLimit] {
		if result.Record.Type != "video" {
			t.Fatalf("expected the window to hold only episodes, got %#v", result)
		}
	}
}

// The secondary block is capped: with more matching channel docs than the
// quota, only the top-scored rows ride along.
func TestSearchSecondaryQuotaCap(t *testing.T) {
	t.Parallel()

	db := openSearchTestDB(t)
	for i := range 12 {
		seedChannelDoc(t, db, "chan-"+strconv.Itoa(i), "description", "Music channel "+strconv.Itoa(i))
	}

	results, err := Search(context.Background(), db, Query{Term: "music", Limit: MaxLimit})
	if err != nil {
		t.Fatal(err)
	}
	channels := 0
	for _, result := range results {
		if result.OwnerType == "channel" {
			channels++
		}
	}
	if channels != secondaryResultCap {
		t.Fatalf("expected secondary quota of %d channel rows, got %d", secondaryResultCap, channels)
	}
}

// The quota is window-independent: offset pages keep the same secondary
// rows, so paging never hides or duplicates the channels & playlists block.
func TestSearchSecondaryStableAcrossOffsetPages(t *testing.T) {
	t.Parallel()

	db := openSearchTestDB(t)
	now := time.Now()
	for i := range 60 {
		seedDatedVideo(t, db, "vid-music-"+fmt.Sprintf("%02d", i), "title", "Music "+strings.Repeat("x", i), now.Add(-48*time.Hour))
	}
	seedChannelDoc(t, db, "chan-name", "name", "Music")
	seedChannelDoc(t, db, "chan-desc", "description", "Music")

	collectSecondary := func(offset int) []Result {
		t.Helper()
		results, err := Search(context.Background(), db, Query{Term: "music", Limit: MaxLimit, Offset: offset})
		if err != nil {
			t.Fatal(err)
		}
		secondary := []Result{}
		for _, result := range results {
			if result.OwnerType == "channel" {
				secondary = append(secondary, result)
			}
		}
		return secondary
	}

	// Identical doc text: the name doc (×3) must outrank the description doc
	// (×1), and both must ride along on every page unchanged.
	firstPage := collectSecondary(0)
	secondPage := collectSecondary(50)
	if len(firstPage) != 2 || firstPage[0].OwnerID != "chan-name" || firstPage[1].OwnerID != "chan-desc" {
		t.Fatalf("expected name doc ranked above description doc in the secondary block, got %#v", firstPage)
	}
	if len(secondPage) != 2 || secondPage[0].OwnerID != "chan-name" || secondPage[1].OwnerID != "chan-desc" {
		t.Fatalf("expected the same secondary rows on offset pages, got %#v", secondPage)
	}
}

// The episode window holds exactly limit rows whether or not secondary
// matches exist — the quota never consumes episode slots.
func TestSearchEpisodeWindowUnchangedBySecondaryMatches(t *testing.T) {
	t.Parallel()

	db := openSearchTestDB(t)
	now := time.Now()
	for i := range 12 {
		seedDatedVideo(t, db, "vid-solo-"+strconv.Itoa(i), "title", "Music"+strings.Repeat(" pad", i), now.Add(-48*time.Hour))
	}

	solo, err := Search(context.Background(), db, Query{Term: "music", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(solo) != 10 {
		t.Fatalf("expected 10 episodes with no secondary matches, got %d", len(solo))
	}

	seedChannelDoc(t, db, "chan-desc", "description", "A music archive")
	withSecondary, err := Search(context.Background(), db, Query{Term: "music", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(withSecondary) != 11 {
		t.Fatalf("expected 10 episodes plus 1 secondary row, got %d", len(withSecondary))
	}
	for index, result := range withSecondary[:10] {
		if result.OwnerID != solo[index].OwnerID {
			t.Fatalf("expected unchanged episode order at %d, got %#v vs %#v", index, result, solo[index])
		}
	}
}
