package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"kapsel/internal/assetpath"
)

var (
	ErrConfirmationRequired = errors.New("storage cleanup requires --confirm with --delete")
	ErrUnsafePath           = errors.New("storage path is unsafe")
)

type Category string

const (
	CategoryMedia     Category = "media"
	CategoryThumbnail Category = "thumbnail"
	CategorySubtitle  Category = "subtitle"
	CategoryDatabase  Category = "database"
	CategoryDerived   Category = "derived"
)

type Config struct {
	DataRoot  string
	MediaRoot string
	DBPath    string
}

type Usage struct {
	Category Category `json:"category"`
	Files    int      `json:"files"`
	Bytes    int64    `json:"bytes"`
}

type FileIssue struct {
	Path  string `json:"path"`
	Bytes int64  `json:"bytes"`
}

type MissingReference struct {
	Path      string `json:"path"`
	Table     string `json:"table"`
	Column    string `json:"column"`
	OwnerType string `json:"owner_type"`
	OwnerID   string `json:"owner_id"`
	Reason    string `json:"reason"`
}

type Summary struct {
	Usage             []Usage `json:"usage"`
	OrphanFiles       int     `json:"orphan_files"`
	OrphanBytes       int64   `json:"orphan_bytes"`
	MissingReferences int     `json:"missing_references"`
}

type Report struct {
	Usage             []Usage            `json:"usage"`
	OrphanFiles       []FileIssue        `json:"orphan_files"`
	MissingReferences []MissingReference `json:"missing_references"`
	Summary           Summary            `json:"summary"`
}

type CleanupOptions struct {
	Delete  bool
	Confirm bool
}

type CleanupReport struct {
	DryRun       bool        `json:"dry_run"`
	DeletedFiles []FileIssue `json:"deleted_files"`
	Report       Report      `json:"report"`
}

type pathReference struct {
	category  Category
	table     string
	column    string
	ownerType string
	ownerID   string
	path      string
}

func Scan(ctx context.Context, db *sql.DB, cfg Config) (Report, error) {
	if db == nil {
		return Report{}, errors.New("storage scan requires database")
	}
	if err := validateMediaRoot(cfg.MediaRoot); err != nil {
		return Report{}, err
	}
	refs, err := referencedPaths(ctx, db)
	if err != nil {
		return Report{}, err
	}
	referenced := map[string][]pathReference{}
	missing := []MissingReference{}
	for _, ref := range refs {
		cleaned, err := assetpath.Clean(ref.path)
		if err != nil {
			missing = append(missing, missingReference(ref, ref.path, "invalid_path"))
			continue
		}
		ref.path = cleaned
		referenced[cleaned] = append(referenced[cleaned], ref)
	}

	usage := usageMap()
	for path, pathRefs := range referenced {
		info, err := lstatMediaPath(cfg.MediaRoot, path)
		if errors.Is(err, os.ErrNotExist) {
			for _, ref := range pathRefs {
				missing = append(missing, missingReference(ref, path, "missing"))
			}
			continue
		}
		if errors.Is(err, ErrUnsafePath) {
			for _, ref := range pathRefs {
				missing = append(missing, missingReference(ref, path, "unsafe_path"))
			}
			continue
		}
		if err != nil {
			return Report{}, err
		}
		if !info.Mode().IsRegular() {
			for _, ref := range pathRefs {
				missing = append(missing, missingReference(ref, path, "not_regular"))
			}
			continue
		}
		addUsage(usage, pathRefs[0].category, info.Size())
	}

	if err := addDatabaseUsage(usage, cfg.DBPath); err != nil {
		return Report{}, err
	}
	orphans, err := orphanFiles(cfg.MediaRoot, referenced)
	if err != nil {
		return Report{}, err
	}
	report := Report{Usage: orderedUsage(usage), OrphanFiles: orphans, MissingReferences: missing}
	sortMissing(report.MissingReferences)
	report.Summary = summary(report)

	return report, nil
}

func Cleanup(ctx context.Context, db *sql.DB, cfg Config, options CleanupOptions) (CleanupReport, error) {
	report, err := Scan(ctx, db, cfg)
	if err != nil {
		return CleanupReport{}, err
	}
	cleanup := CleanupReport{DryRun: !options.Delete, Report: report}
	if !options.Delete {
		return cleanup, nil
	}
	if !options.Confirm {
		return cleanup, ErrConfirmationRequired
	}
	cleanup.DryRun = false
	for _, orphan := range report.OrphanFiles {
		absPath, err := mediaPath(cfg.MediaRoot, orphan.Path)
		if err != nil {
			return cleanup, err
		}
		info, err := lstatMediaPath(cfg.MediaRoot, orphan.Path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return cleanup, err
		}
		if !info.Mode().IsRegular() {
			return cleanup, fmt.Errorf("refusing to delete non-regular orphan file %s", orphan.Path)
		}
		if err := os.Remove(absPath); err != nil {
			return cleanup, err
		}
		cleanup.DeletedFiles = append(cleanup.DeletedFiles, orphan)
	}

	return cleanup, nil
}

func referencedPaths(ctx context.Context, db *sql.DB) ([]pathReference, error) {
	refs := []pathReference{}
	queries := []struct {
		query    string
		category Category
		table    string
		column   string
	}{
		{query: "SELECT 'video', id, media_path FROM videos WHERE media_path <> ''", category: CategoryMedia, table: "videos", column: "media_path"},
		{query: "SELECT 'video', id, thumbnail_path FROM videos WHERE thumbnail_path <> ''", category: CategoryThumbnail, table: "videos", column: "thumbnail_path"},
		{query: "SELECT 'video', video_id, path FROM subtitles WHERE path <> ''", category: CategorySubtitle, table: "subtitles", column: "path"},
		{query: "SELECT 'video', video_id, sprite_path FROM video_previews WHERE sprite_path <> ''", category: CategoryDerived, table: "video_previews", column: "sprite_path"},
	}
	for _, query := range queries {
		if err := appendSimpleRefs(ctx, db, &refs, query.query, query.category, query.table, query.column); err != nil {
			return nil, err
		}
	}
	if err := appendMediaAssetRefs(ctx, db, &refs); err != nil {
		return nil, err
	}

	return refs, nil
}

func appendSimpleRefs(ctx context.Context, db *sql.DB, refs *[]pathReference, query string, category Category, table string, column string) error {
	rows, err := db.QueryContext(ctx, query)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var ownerType string
		var ownerID string
		var path string
		if err := rows.Scan(&ownerType, &ownerID, &path); err != nil {
			return err
		}
		*refs = append(*refs, pathReference{category: category, table: table, column: column, ownerType: ownerType, ownerID: ownerID, path: path})
	}

	return rows.Err()
}

func appendMediaAssetRefs(ctx context.Context, db *sql.DB, refs *[]pathReference) error {
	rows, err := db.QueryContext(ctx, "SELECT owner_type, owner_id, kind, path FROM media_assets WHERE path <> ''")
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var ownerType string
		var ownerID string
		var kind string
		var path string
		if err := rows.Scan(&ownerType, &ownerID, &kind, &path); err != nil {
			return err
		}
		*refs = append(*refs, pathReference{category: categoryForAssetKind(kind), table: "media_assets", column: "path", ownerType: ownerType, ownerID: ownerID, path: path})
	}

	return rows.Err()
}

func categoryForAssetKind(kind string) Category {
	switch kind {
	case "media":
		return CategoryMedia
	case "thumbnail":
		return CategoryThumbnail
	case "subtitle":
		return CategorySubtitle
	default:
		return CategoryDerived
	}
}

func missingReference(ref pathReference, path string, reason string) MissingReference {
	return MissingReference{Path: path, Table: ref.table, Column: ref.column, OwnerType: ref.ownerType, OwnerID: ref.ownerID, Reason: reason}
}

func lstatMediaPath(root string, path string) (fs.FileInfo, error) {
	_, info, err := assetpath.Lstat(root, path)
	if errors.Is(err, assetpath.ErrInvalid) || errors.Is(err, assetpath.ErrSymlink) {
		return nil, ErrUnsafePath
	}

	return info, err
}

func mediaPath(root string, path string) (string, error) {
	cleaned, err := assetpath.Clean(path)
	if err != nil {
		return "", ErrUnsafePath
	}
	root = filepath.Clean(root)
	absPath := filepath.Join(root, filepath.FromSlash(cleaned))
	relative, err := filepath.Rel(root, absPath)
	if err != nil || relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", ErrUnsafePath
	}

	return absPath, nil
}

func validateMediaRoot(root string) error {
	if strings.TrimSpace(root) == "" {
		return ErrUnsafePath
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return err
	}
	cleaned := filepath.Clean(absRoot)
	filesystemRoot := filepath.VolumeName(cleaned) + string(os.PathSeparator)
	if cleaned == filesystemRoot {
		return ErrUnsafePath
	}
	_, err = assetpath.ValidateRoot(cleaned)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		if errors.Is(err, assetpath.ErrInvalid) || errors.Is(err, assetpath.ErrSymlink) {
			return ErrUnsafePath
		}
		return err
	}

	return nil
}

func orphanFiles(mediaRoot string, referenced map[string][]pathReference) ([]FileIssue, error) {
	orphans := []FileIssue{}
	err := filepath.WalkDir(mediaRoot, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		relative, err := filepath.Rel(mediaRoot, path)
		if err != nil {
			return err
		}
		cleaned, err := assetpath.Clean(filepath.ToSlash(relative))
		if err != nil {
			return nil
		}
		if _, ok := referenced[cleaned]; !ok {
			orphans = append(orphans, FileIssue{Path: cleaned, Bytes: info.Size()})
		}

		return nil
	})
	if errors.Is(err, os.ErrNotExist) {
		return orphans, nil
	}
	if err != nil {
		return nil, err
	}
	sort.Slice(orphans, func(i, j int) bool { return orphans[i].Path < orphans[j].Path })

	return orphans, nil
}

func addDatabaseUsage(usage map[Category]Usage, dbPath string) error {
	if strings.TrimSpace(dbPath) == "" {
		return nil
	}
	for _, path := range []string{dbPath, dbPath + "-wal", dbPath + "-shm"} {
		info, err := os.Stat(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		if info.Mode().IsRegular() {
			addUsage(usage, CategoryDatabase, info.Size())
		}
	}

	return nil
}

func usageMap() map[Category]Usage {
	usage := map[Category]Usage{}
	for _, category := range categoryOrder() {
		usage[category] = Usage{Category: category}
	}

	return usage
}

func addUsage(usage map[Category]Usage, category Category, bytes int64) {
	item := usage[category]
	item.Category = category
	item.Files++
	item.Bytes += bytes
	usage[category] = item
}

func orderedUsage(usage map[Category]Usage) []Usage {
	ordered := []Usage{}
	for _, category := range categoryOrder() {
		ordered = append(ordered, usage[category])
	}

	return ordered
}

func categoryOrder() []Category {
	return []Category{CategoryMedia, CategoryThumbnail, CategorySubtitle, CategoryDerived, CategoryDatabase}
}

func summary(report Report) Summary {
	var orphanBytes int64
	for _, orphan := range report.OrphanFiles {
		orphanBytes += orphan.Bytes
	}

	return Summary{Usage: report.Usage, OrphanFiles: len(report.OrphanFiles), OrphanBytes: orphanBytes, MissingReferences: len(report.MissingReferences)}
}

func sortMissing(missing []MissingReference) {
	sort.Slice(missing, func(i, j int) bool {
		if missing[i].Path != missing[j].Path {
			return missing[i].Path < missing[j].Path
		}
		if missing[i].Table != missing[j].Table {
			return missing[i].Table < missing[j].Table
		}
		return missing[i].OwnerID < missing[j].OwnerID
	})
}
