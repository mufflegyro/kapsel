package assetpath

import (
	"errors"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
)

var (
	ErrInvalid = errors.New("invalid media path")
	ErrSymlink = errors.New("media path contains symlink")
	ErrChanged = errors.New("media path changed")
)

func Clean(raw string) (string, error) {
	value := strings.TrimSpace(strings.ReplaceAll(raw, "\\", "/"))
	if value == "" || strings.HasPrefix(value, "/") || hasParentSegment(value) {
		return "", ErrInvalid
	}

	cleaned := path.Clean(value)
	if cleaned == "." || !fs.ValidPath(cleaned) {
		return "", ErrInvalid
	}

	return cleaned, nil
}

func FromMediaRoot(mediaRoot string, raw string) (string, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return "", ErrInvalid
	}
	if hasParentSegment(strings.ReplaceAll(value, "\\", "/")) {
		return "", ErrInvalid
	}

	if filepath.IsAbs(value) {
		rootAbs, err := filepath.Abs(mediaRoot)
		if err != nil {
			return "", err
		}
		relative, err := filepath.Rel(rootAbs, filepath.Clean(value))
		if err != nil || outsideRoot(relative) {
			return "", ErrInvalid
		}

		return Clean(filepath.ToSlash(relative))
	}

	cleanedRoot := filepath.Clean(mediaRoot)
	cleanedValue := filepath.Clean(value)
	if cleanedRoot != "." && !filepath.IsAbs(cleanedRoot) {
		if relative, ok := relativeToRoot(cleanedRoot, cleanedValue); ok {
			return Clean(filepath.ToSlash(relative))
		}
	}

	return Clean(filepath.ToSlash(value))
}

func Lstat(root string, raw string) (string, fs.FileInfo, error) {
	rootPath, _, err := validateRoot(root)
	if err != nil {
		return "", nil, err
	}
	cleaned, _, info, err := lstatPath(rootPath, raw)

	return cleaned, info, err
}

func Open(root string, raw string) (string, *os.File, fs.FileInfo, error) {
	rootPath, rootInfo, err := validateRoot(root)
	if err != nil {
		return "", nil, nil, err
	}
	cleaned, _, info, err := lstatPath(rootPath, raw)
	if err != nil {
		return "", nil, nil, err
	}
	rootDir, err := os.OpenRoot(rootPath)
	if err != nil {
		return "", nil, nil, err
	}
	defer rootDir.Close()
	openedRootInfo, err := rootDir.Stat(".")
	if err != nil {
		return "", nil, nil, err
	}
	if !os.SameFile(rootInfo, openedRootInfo) {
		return "", nil, nil, ErrInvalid
	}
	file, err := rootDir.Open(cleaned)
	if err != nil {
		return "", nil, nil, err
	}
	openedInfo, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return "", nil, nil, err
	}
	if !os.SameFile(info, openedInfo) {
		_ = file.Close()
		return "", nil, nil, ErrInvalid
	}

	return cleaned, file, openedInfo, nil
}

func RemoveRegular(root string, raw string) (string, error) {
	return RemoveRegularMatching(root, raw, nil)
}

func RemoveRegularMatching(root string, raw string, expected fs.FileInfo) (string, error) {
	rootPath, rootInfo, err := validateRoot(root)
	if err != nil {
		return "", err
	}
	cleaned, _, info, err := lstatPath(rootPath, raw)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", ErrInvalid
	}
	if expected != nil && !os.SameFile(info, expected) {
		return "", ErrChanged
	}
	rootDir, err := os.OpenRoot(rootPath)
	if err != nil {
		return "", err
	}
	defer rootDir.Close()
	openedRootInfo, err := rootDir.Stat(".")
	if err != nil {
		return "", err
	}
	if !os.SameFile(rootInfo, openedRootInfo) {
		return "", ErrInvalid
	}
	openedInfo, err := rootDir.Lstat(cleaned)
	if err != nil {
		return "", err
	}
	if !os.SameFile(info, openedInfo) {
		return "", ErrChanged
	}
	if err := rootDir.Remove(cleaned); err != nil {
		return "", err
	}

	return cleaned, nil
}

// ValidateRoot returns the cleaned root path when root exists as a non-symlink directory.
func ValidateRoot(root string) (string, error) {
	rootPath, _, err := validateRoot(root)
	if err != nil {
		return "", err
	}

	return rootPath, nil
}

func validateRoot(root string) (string, fs.FileInfo, error) {
	if strings.TrimSpace(root) == "" {
		return "", nil, ErrInvalid
	}
	rootPath := filepath.Clean(root)
	info, err := os.Lstat(rootPath)
	if err != nil {
		return "", nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return "", nil, ErrSymlink
	}
	if !info.IsDir() {
		return "", nil, ErrInvalid
	}

	return rootPath, info, nil
}

func lstatPath(root string, raw string) (string, string, fs.FileInfo, error) {
	if strings.TrimSpace(root) == "" {
		return "", "", nil, ErrInvalid
	}
	cleaned, err := Clean(raw)
	if err != nil {
		return "", "", nil, err
	}
	current := root
	parts := strings.Split(cleaned, "/")
	for index, part := range parts {
		current = filepath.Join(current, filepath.FromSlash(part))
		info, err := os.Lstat(current)
		if err != nil {
			return "", "", nil, err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return "", "", nil, ErrSymlink
		}
		if index < len(parts)-1 && !info.IsDir() {
			return "", "", nil, ErrInvalid
		}
		if index == len(parts)-1 {
			return cleaned, current, info, nil
		}
	}

	return "", "", nil, os.ErrNotExist
}

func relativeToRoot(root string, value string) (string, bool) {
	relative, err := filepath.Rel(root, value)
	if err != nil || outsideRoot(relative) {
		return "", false
	}

	return relative, true
}

func outsideRoot(relative string) bool {
	return relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func hasParentSegment(value string) bool {
	for _, part := range strings.Split(value, "/") {
		if part == ".." {
			return true
		}
	}

	return false
}
