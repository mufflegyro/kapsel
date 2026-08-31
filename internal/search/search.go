package search

import (
	"context"
	"database/sql"
	"html"
	"strings"
)

const (
	DefaultLimit = 20
	MaxLimit     = 50
	markStart    = "\x1f"
	markEnd      = "\x1e"
)

type Query struct {
	Term  string
	Limit int
}

// MatchStats carries match-set counts for a search term.
type MatchStats struct {
	Total          int
	DistinctOwners int
}

type Result struct {
	OwnerType string  `json:"owner_type"`
	OwnerID   string  `json:"owner_id"`
	Field     string  `json:"field"`
	Snippet   string  `json:"snippet"`
	Rank      float64 `json:"rank"`
	Record    Record  `json:"record"`
}

type Record struct {
	Type            string       `json:"type"`
	ID              string       `json:"id"`
	Title           string       `json:"title"`
	Description     string       `json:"description,omitempty"`
	ThumbnailURL    string       `json:"thumbnail_url,omitempty"`
	ThumbnailPath   string       `json:"-"`
	PublishedAt     string       `json:"published_at,omitempty"`
	DurationSeconds int          `json:"duration_seconds,omitempty"`
	KeepForever     bool         `json:"keep_forever"`
	Channel         *ChannelInfo `json:"channel,omitempty"`
}

type ChannelInfo struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

func Search(ctx context.Context, db *sql.DB, query Query) ([]Result, error) {
	term := strings.TrimSpace(query.Term)
	if term == "" {
		return []Result{}, nil
	}

	limit := query.Limit
	if limit <= 0 {
		limit = DefaultLimit
	}
	if limit > MaxLimit {
		limit = MaxLimit
	}

	rows, err := db.QueryContext(ctx, `
SELECT
  owner_type,
  owner_id,
  field,
  snippet(search_documents_fts, 3, char(31), char(30), '...', 12) AS snippet,
  bm25(search_documents_fts) AS rank
FROM search_documents_fts
WHERE search_documents_fts MATCH ?
ORDER BY rank
LIMIT ?`, matchExpression(term), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	results := []Result{}
	for rows.Next() {
		var result Result
		if err := rows.Scan(
			&result.OwnerType,
			&result.OwnerID,
			&result.Field,
			&result.Snippet,
			&result.Rank,
		); err != nil {
			return nil, err
		}
		result.Snippet = safeSnippet(result.Snippet)
		results = append(results, result)
	}

	if err := rows.Err(); err != nil {
		return nil, err
	}
	results, err = hydrateResults(ctx, db, results)
	if err != nil {
		return nil, err
	}

	return results, nil
}

func hydrateResults(ctx context.Context, db *sql.DB, results []Result) ([]Result, error) {
	videoIndexes := map[string][]int{}
	commentIndexes := map[string][]int{}
	channelIndexes := map[string][]int{}
	playlistIndexes := map[string][]int{}

	for index := range results {
		result := &results[index]
		result.Record = Record{Type: result.OwnerType, ID: result.OwnerID, Title: result.OwnerID}
		switch result.OwnerType {
		case "video":
			videoIndexes[result.OwnerID] = append(videoIndexes[result.OwnerID], index)
		case "subtitle":
			result.Record.Type = "video"
			result.Record.ID = result.OwnerID
			videoIndexes[result.OwnerID] = append(videoIndexes[result.OwnerID], index)
		case "comment":
			commentIndexes[result.OwnerID] = append(commentIndexes[result.OwnerID], index)
		case "channel":
			channelIndexes[result.OwnerID] = append(channelIndexes[result.OwnerID], index)
		case "playlist":
			playlistIndexes[result.OwnerID] = append(playlistIndexes[result.OwnerID], index)
		}
	}
	commentVideoIDs, err := loadCommentVideoIDs(ctx, db, mapKeys(commentIndexes))
	if err != nil {
		return nil, err
	}
	for commentID, videoID := range commentVideoIDs {
		for _, index := range commentIndexes[commentID] {
			results[index].Record.Type = "video"
			results[index].Record.ID = videoID
			videoIndexes[videoID] = append(videoIndexes[videoID], index)
		}
	}
	videoRecords, membersOnly, err := loadVideoRecords(ctx, db, mapKeys(videoIndexes))
	if err != nil {
		return nil, err
	}
	applyRecords(results, videoIndexes, videoRecords)
	channelRecords, err := loadChannelRecords(ctx, db, mapKeys(channelIndexes))
	if err != nil {
		return nil, err
	}
	applyRecords(results, channelIndexes, channelRecords)
	playlistRecords, err := loadPlaylistRecords(ctx, db, mapKeys(playlistIndexes))
	if err != nil {
		return nil, err
	}
	applyRecords(results, playlistIndexes, playlistRecords)

	if len(membersOnly) == 0 {
		return results, nil
	}
	filtered := results[:0]
	for _, result := range results {
		if result.Record.Type == "video" && membersOnly[result.Record.ID] {
			continue
		}
		filtered = append(filtered, result)
	}

	return filtered, nil
}

func loadCommentVideoIDs(ctx context.Context, db *sql.DB, ids []string) (map[string]string, error) {
	videoIDs := map[string]string{}
	if len(ids) == 0 {
		return videoIDs, nil
	}
	rows, err := db.QueryContext(ctx, "SELECT id, video_id FROM comments WHERE id IN ("+placeholders(len(ids))+")", stringArgs(ids)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var videoID string
		if err := rows.Scan(&id, &videoID); err != nil {
			return nil, err
		}
		videoIDs[id] = videoID
	}

	return videoIDs, rows.Err()
}

func loadVideoRecords(ctx context.Context, db *sql.DB, ids []string) (map[string]Record, map[string]bool, error) {
	records := map[string]Record{}
	membersOnly := map[string]bool{}
	if len(ids) == 0 {
		return records, membersOnly, nil
	}
	rows, err := db.QueryContext(ctx, `
SELECT
  v.id,
  v.title,
  v.description,
  COALESCE(v.published_at, ''),
	  v.duration_seconds,
	  v.keep_forever,
	  v.thumbnail_path,
  v.thumbnail_url,
  v.members_only,
  COALESCE(c.id, ''),
  COALESCE(c.name, '')
FROM videos v
LEFT JOIN channels c ON c.id = v.channel_id
WHERE v.id IN (`+placeholders(len(ids))+`)`, stringArgs(ids)...)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var channelID string
		var channelName string
		var keepForever int
		var membersOnlyFlag int
		record := Record{Type: "video"}
		if err := rows.Scan(
			&id,
			&record.Title,
			&record.Description,
			&record.PublishedAt,
			&record.DurationSeconds,
			&keepForever,
			&record.ThumbnailPath,
			&record.ThumbnailURL,
			&membersOnlyFlag,
			&channelID,
			&channelName,
		); err != nil {
			return nil, nil, err
		}
		record.ID = id
		record.KeepForever = keepForever == 1
		if membersOnlyFlag == 1 {
			membersOnly[id] = true
			continue
		}
		if channelID != "" || channelName != "" {
			record.Channel = &ChannelInfo{ID: channelID, Name: channelName}
		}
		records[id] = record
	}

	if rows.Err() != nil {
		return nil, nil, rows.Err()
	}

	return records, membersOnly, nil
}

func loadChannelRecords(ctx context.Context, db *sql.DB, ids []string) (map[string]Record, error) {
	records := map[string]Record{}
	if len(ids) == 0 {
		return records, nil
	}
	rows, err := db.QueryContext(ctx, "SELECT id, name, description FROM channels WHERE id IN ("+placeholders(len(ids))+")", stringArgs(ids)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		record := Record{Type: "channel"}
		if err := rows.Scan(&record.ID, &record.Title, &record.Description); err != nil {
			return nil, err
		}
		records[record.ID] = record
	}

	return records, rows.Err()
}

func loadPlaylistRecords(ctx context.Context, db *sql.DB, ids []string) (map[string]Record, error) {
	records := map[string]Record{}
	if len(ids) == 0 {
		return records, nil
	}
	rows, err := db.QueryContext(ctx, `
SELECT
  p.id,
  p.title,
  p.description,
  COALESCE(c.id, ''),
  COALESCE(c.name, '')
FROM playlists p
LEFT JOIN channels c ON c.id = p.channel_id
WHERE p.id IN (`+placeholders(len(ids))+`)`, stringArgs(ids)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var channelID string
		var channelName string
		record := Record{Type: "playlist"}
		if err := rows.Scan(&record.ID, &record.Title, &record.Description, &channelID, &channelName); err != nil {
			return nil, err
		}
		if channelID != "" || channelName != "" {
			record.Channel = &ChannelInfo{ID: channelID, Name: channelName}
		}
		records[record.ID] = record
	}

	return records, rows.Err()
}

func applyRecords(results []Result, indexes map[string][]int, records map[string]Record) {
	for id, record := range records {
		for _, index := range indexes[id] {
			results[index].Record = record
		}
	}
}

func mapKeys(values map[string][]int) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}

	return keys
}

func placeholders(count int) string {
	if count <= 0 {
		return ""
	}
	return strings.TrimRight(strings.Repeat("?,", count), ",")
}

func stringArgs(values []string) []any {
	args := make([]any, len(values))
	for index, value := range values {
		args[index] = value
	}

	return args
}

// matchExpression builds the FTS5 MATCH expression for a search term. Each
// whitespace-separated token is quoted (embedded quotes doubled) and the
// tokens are AND-joined, so natural multiword queries like "island cabin"
// match documents containing both tokens anywhere instead of only the exact
// phrase. Single-token terms keep the plain quoted form.
func matchExpression(term string) string {
	tokens := strings.Fields(term)
	quoted := make([]string, 0, len(tokens))
	for _, token := range tokens {
		quoted = append(quoted, `"`+strings.ReplaceAll(token, `"`, `""`)+`"`)
	}
	return strings.Join(quoted, " AND ")
}

// Stats summarizes the full match set for a term, independent of the page
// the results endpoint returns. Total counts every matching search document;
// DistinctOwners counts unique display owners — subtitle and comment docs
// fold into their parent video — excluding members-only videos, mirroring
// how hydrateResults shapes the returned page.
func Stats(ctx context.Context, db *sql.DB, term string) (MatchStats, error) {
	term = strings.TrimSpace(term)
	if term == "" {
		return MatchStats{}, nil
	}
	match := matchExpression(term)

	var total int
	if err := db.QueryRowContext(ctx, `
SELECT count(*)
FROM search_documents_fts
WHERE search_documents_fts MATCH ?`, match).Scan(&total); err != nil {
		return MatchStats{}, err
	}

	var distinctOwners int
	if err := db.QueryRowContext(ctx, `
SELECT count(DISTINCT display_key)
FROM (
  SELECT
    CASE fts.owner_type
      WHEN 'comment' THEN 'video:' || (SELECT c.video_id FROM comments c WHERE c.id = fts.owner_id)
      WHEN 'subtitle' THEN 'video:' || fts.owner_id
      ELSE fts.owner_type || ':' || fts.owner_id
    END AS display_key,
    fts.owner_type AS owner_type,
    CASE fts.owner_type
      WHEN 'comment' THEN (SELECT c.video_id FROM comments c WHERE c.id = fts.owner_id)
      ELSE fts.owner_id
    END AS resolved_id
  FROM search_documents_fts fts
  WHERE search_documents_fts MATCH ?
)
WHERE owner_type NOT IN ('video', 'subtitle', 'comment')
   OR NOT EXISTS (
     SELECT 1 FROM videos v WHERE v.id = resolved_id AND v.members_only = 1
   )`, match).Scan(&distinctOwners); err != nil {
		return MatchStats{}, err
	}

	return MatchStats{Total: total, DistinctOwners: distinctOwners}, nil
}

func safeSnippet(value string) string {
	escaped := html.EscapeString(value)
	escaped = strings.ReplaceAll(escaped, markStart, "<mark>")
	return strings.ReplaceAll(escaped, markEnd, "</mark>")
}
