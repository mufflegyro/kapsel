package assetpath

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestFromMediaRootReturnsCanonicalRelativePath(t *testing.T) {
	t.Parallel()

	relative, err := FromMediaRoot(filepath.Join("data", "media"), filepath.Join("data", "media", "videos", "sample.mp4"))
	if err != nil {
		t.Fatal(err)
	}
	if relative != "videos/sample.mp4" {
		t.Fatalf("expected relative media path, got %q", relative)
	}

	root := filepath.Join(t.TempDir(), "media")
	absolute, err := FromMediaRoot(root, filepath.Join(root, "thumbs", "sample.jpg"))
	if err != nil {
		t.Fatal(err)
	}
	if absolute != "thumbs/sample.jpg" {
		t.Fatalf("expected absolute path under media root to become relative, got %q", absolute)
	}
}

func TestFromMediaRootRejectsUnsafePaths(t *testing.T) {
	t.Parallel()

	root := filepath.Join(t.TempDir(), "media")
	for _, raw := range []string{
		"",
		".",
		"../secret.mp4",
		"videos/../secret.mp4",
		"media/videos/../secret.mp4",
		filepath.Join(filepath.Dir(root), "secret.mp4"),
	} {
		if cleaned, err := FromMediaRoot(root, raw); err == nil {
			t.Fatalf("expected %q to be rejected, got %q", raw, cleaned)
		}
	}
}

func TestCleanRejectsNonCanonicalRelativePaths(t *testing.T) {
	t.Parallel()

	for _, raw := range []string{"", ".", "/videos/sample.mp4", "../sample.mp4", "videos/../sample.mp4", `videos\..\sample.mp4`} {
		if cleaned, err := Clean(raw); err == nil {
			t.Fatalf("expected %q to be rejected, got %q", raw, cleaned)
		}
	}
}

func TestValidateRootAcceptsDirectoryAndRejectsSymlink(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	cleaned, err := ValidateRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	if cleaned != filepath.Clean(root) {
		t.Fatalf("expected cleaned root %q, got %q", filepath.Clean(root), cleaned)
	}

	link := filepath.Join(t.TempDir(), "media-link")
	if err := os.Symlink(root, link); err != nil {
		t.Skipf("symlink not supported: %v", err)
	}
	if _, err := ValidateRoot(link); !errors.Is(err, ErrSymlink) {
		t.Fatalf("expected symlink root to be rejected, got %v", err)
	}
}

func TestRemoveRegularRemovesOnlyRegularRootedFiles(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	mediaPath := filepath.Join(root, "videos", "sample.mp4")
	if err := os.MkdirAll(filepath.Dir(mediaPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(mediaPath, []byte("video"), 0o644); err != nil {
		t.Fatal(err)
	}

	cleaned, err := RemoveRegular(root, "videos/sample.mp4")
	if err != nil {
		t.Fatal(err)
	}
	if cleaned != "videos/sample.mp4" {
		t.Fatalf("expected cleaned path, got %q", cleaned)
	}
	if _, err := os.Stat(mediaPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected file to be removed, got %v", err)
	}

	if err := os.MkdirAll(filepath.Join(root, "dirs", "sample"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := RemoveRegular(root, "dirs/sample"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("expected directory removal to be rejected, got %v", err)
	}

	outside := filepath.Join(t.TempDir(), "outside.mp4")
	if err := os.WriteFile(outside, []byte("secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "videos", "linked.mp4")); err != nil {
		t.Skipf("symlink not supported: %v", err)
	}
	if _, err := RemoveRegular(root, "videos/linked.mp4"); !errors.Is(err, ErrSymlink) {
		t.Fatalf("expected symlink removal to be rejected, got %v", err)
	}
	if _, err := os.Stat(outside); err != nil {
		t.Fatalf("expected outside file to remain, got %v", err)
	}
}

func TestRemoveRegularMatchingRejectsChangedFile(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	mediaPath := filepath.Join(root, "videos", "sample.mp4")
	if err := os.MkdirAll(filepath.Dir(mediaPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(mediaPath, []byte("original"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, info, err := Lstat(root, "videos/sample.mp4")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(mediaPath); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(mediaPath, []byte("replacement"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := RemoveRegularMatching(root, "videos/sample.mp4", info); !errors.Is(err, ErrChanged) {
		t.Fatalf("expected changed file to be rejected, got %v", err)
	}
	body, err := os.ReadFile(mediaPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "replacement" {
		t.Fatalf("expected replacement file to remain, got %q", string(body))
	}
}

func TestSameAssetDetectsInPlaceChanges(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	raw := "asset.bin"
	assetPath := filepath.Join(root, raw)
	if err := os.WriteFile(assetPath, []byte("0123456789"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, recorded, err := Lstat(root, raw)
	if err != nil {
		t.Fatal(err)
	}

	// An untouched file stays identical even though os.SameFile would say
	// so anyway.
	_, current, err := Lstat(root, raw)
	if err != nil {
		t.Fatal(err)
	}
	if !sameAsset(current, recorded) {
		t.Fatal("expected untouched file to match its recorded identity")
	}

	// Size change with the mtime preserved: os.SameFile would accept this
	// (same inode), sameAsset must not.
	file, err := os.OpenFile(assetPath, os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString("extra"); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(assetPath, recorded.ModTime(), recorded.ModTime()); err != nil {
		t.Fatal(err)
	}
	_, current, err = Lstat(root, raw)
	if err != nil {
		t.Fatal(err)
	}
	if sameAsset(current, recorded) {
		t.Fatal("expected a size change to break the identity match")
	}

	// Restored size but a new mtime must also break the match.
	if err := os.Truncate(assetPath, 10); err != nil {
		t.Fatal(err)
	}
	_, current, err = Lstat(root, raw)
	if err != nil {
		t.Fatal(err)
	}
	if sameAsset(current, recorded) {
		t.Fatal("expected an mtime change to break the identity match")
	}

	// Fully restored file matches again.
	if err := os.Chtimes(assetPath, recorded.ModTime(), recorded.ModTime()); err != nil {
		t.Fatal(err)
	}
	_, current, err = Lstat(root, raw)
	if err != nil {
		t.Fatal(err)
	}
	if !sameAsset(current, recorded) {
		t.Fatal("expected the restored file to match its recorded identity")
	}
}
