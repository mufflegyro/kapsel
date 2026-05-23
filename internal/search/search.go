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
LIMIT ?`, quoteMatch(term), limit)
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
	if err := hydrateResults(ctx, db, results); err != nil {
		return nil, err
	}

	return results, nil
}

func hydrateResults(ctx context.Context, db *sql.DB, results []Result) error {
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
		return err
	}
	for commentID, videoID := range commentVideoIDs {
		for _, index := range commentIndexes[commentID] {
			results[index].Record.Type = "video"
			results[index].Record.ID = videoID
			videoIndexes[videoID] = append(videoIndexes[videoID], index)
		}
	}
	videoRecords, err := loadVideoRecords(ctx, db, mapKeys(videoIndexes))
	if err != nil {
		return err
	}
	applyRecords(results, videoIndexes, videoRecords)
	channelRecords, err := loadChannelRecords(ctx, db, mapKeys(channelIndexes))
	if err != nil {
		return err
	}
	applyRecords(results, channelIndexes, channelRecords)
	playlistRecords, err := loadPlaylistRecords(ctx, db, mapKeys(playlistIndexes))
	if err != nil {
		return err
	}
	applyRecords(results, playlistIndexes, playlistRecords)

	return nil
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

func loadVideoRecords(ctx context.Context, db *sql.DB, ids []string) (map[string]Record, error) {
	records := map[string]Record{}
	if len(ids) == 0 {
		return records, nil
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
  COALESCE(c.id, ''),
  COALESCE(c.name, '')
FROM videos v
LEFT JOIN channels c ON c.id = v.channel_id
WHERE v.id IN (`+placeholders(len(ids))+`)`, stringArgs(ids)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var channelID string
		var channelName string
		var keepForever int
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
			&channelID,
			&channelName,
		); err != nil {
			return nil, err
		}
		record.ID = id
		record.KeepForever = keepForever == 1
		if channelID != "" || channelName != "" {
			record.Channel = &ChannelInfo{ID: channelID, Name: channelName}
		}
		records[id] = record
	}

	return records, rows.Err()
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

func quoteMatch(term string) string {
	return `"` + strings.ReplaceAll(term, `"`, `""`) + `"`
}

func safeSnippet(value string) string {
	escaped := html.EscapeString(value)
	escaped = strings.ReplaceAll(escaped, markStart, "<mark>")
	return strings.ReplaceAll(escaped, markEnd, "</mark>")
}
