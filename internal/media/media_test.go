package media

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestSignedMediaRangeRequest(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	writeFile(t, filepath.Join(root, "videos", "sample.mp4"), "0123456789")

	signer := NewSigner("secret")
	req := httptest.NewRequest(http.MethodGet, "/media/videos/sample.mp4?"+signer.Query("videos/sample.mp4", time.Now().Add(time.Hour)).Encode(), nil)
	req.Header.Set("Range", "bytes=2-5")
	rec := httptest.NewRecorder()

	NewHandler(root, signer).ServeHTTP(rec, req)

	if rec.Code != http.StatusPartialContent {
		t.Fatalf("expected status %d, got %d", http.StatusPartialContent, rec.Code)
	}
	if body := rec.Body.String(); body != "2345" {
		t.Fatalf("expected range body %q, got %q", "2345", body)
	}
	if got := rec.Header().Get("Content-Range"); got != "bytes 2-5/10" {
		t.Fatalf("unexpected content range %q", got)
	}
}

func TestSignedThumbnailCacheHeaders(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	writeFile(t, filepath.Join(root, "thumbs", "sample.jpg"), "jpg")

	signer := NewSigner("secret")
	req := httptest.NewRequest(http.MethodGet, "/media/thumbs/sample.jpg?"+signer.Query("thumbs/sample.jpg", time.Now().Add(time.Hour)).Encode(), nil)
	rec := httptest.NewRecorder()

	NewHandler(root, signer).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, rec.Code)
	}
	if got := rec.Header().Get("Cache-Control"); got != "private, max-age=86400" {
		t.Fatalf("unexpected cache control %q", got)
	}
}

func TestSignedMediaRequestSetsFiniteWriteDeadline(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	writeFile(t, filepath.Join(root, "videos", "sample.mp4"), "video")

	signer := NewSigner("secret")
	req := httptest.NewRequest(http.MethodGet, "/media/videos/sample.mp4?"+signer.Query("videos/sample.mp4", time.Now().Add(time.Hour)).Encode(), nil)
	rec := &deadlineRecorder{ResponseRecorder: httptest.NewRecorder()}
	before := time.Now()

	NewHandler(root, signer).ServeHTTP(rec, req)
	after := time.Now()

	if !rec.deadlineSet {
		t.Fatal("expected media handler to set write deadline")
	}
	if rec.deadline.IsZero() {
		t.Fatal("expected finite write deadline, got zero deadline")
	}
	if rec.deadline.Before(before.Add(mediaWriteTimeout)) || rec.deadline.After(after.Add(mediaWriteTimeout)) {
		t.Fatalf("expected media write deadline near %s, got %s", mediaWriteTimeout, rec.deadline)
	}
}

func TestUnsignedMediaRequestIsRejected(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	writeFile(t, filepath.Join(root, "videos", "sample.mp4"), "0123456789")

	req := httptest.NewRequest(http.MethodGet, "/media/videos/sample.mp4", nil)
	rec := httptest.NewRecorder()

	NewHandler(root, NewSigner("secret")).ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected status %d, got %d", http.StatusUnauthorized, rec.Code)
	}
}

func TestExpiredMediaSignatureIsRejected(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	writeFile(t, filepath.Join(root, "videos", "sample.mp4"), "0123456789")

	signer := NewSigner("secret")
	query := signer.Query("videos/sample.mp4", time.Now().Add(-time.Minute))
	req := httptest.NewRequest(http.MethodGet, "/media/videos/sample.mp4?"+query.Encode(), nil)
	rec := httptest.NewRecorder()

	NewHandler(root, signer).ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected status %d, got %d", http.StatusUnauthorized, rec.Code)
	}
}

func TestSignatureDependsOnPath(t *testing.T) {
	t.Parallel()

	signer := NewSigner("secret")
	query := signer.Query("videos/sample.mp4", time.Now().Add(time.Hour))
	if signer.Verify("videos/other.mp4", query, time.Now()) {
		t.Fatal("expected signature for different path to fail")
	}
}

func TestSignerUsesCanonicalRelativePath(t *testing.T) {
	t.Parallel()

	signer := NewSigner("secret")
	query := signer.Query("./videos/sample.mp4", time.Now().Add(time.Hour))
	if !signer.Verify("videos/sample.mp4", query, time.Now()) {
		t.Fatal("expected canonical media path to verify")
	}
}

func TestTraversalMediaRequestIsRejected(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	writeFile(t, filepath.Join(root, "sample.mp4"), "0123456789")

	signer := NewSigner("secret")
	req := httptest.NewRequest(http.MethodGet, "/media/videos/../sample.mp4?"+signer.Query("sample.mp4", time.Now().Add(time.Hour)).Encode(), nil)
	rec := httptest.NewRecorder()

	NewHandler(root, signer).ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected status %d, got %d", http.StatusNotFound, rec.Code)
	}
}

func TestSignedSymlinkMediaRequestIsRejected(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside.txt")
	writeFile(t, outside, "secret")
	if err := os.MkdirAll(filepath.Join(root, "videos"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "videos", "sample.mp4")); err != nil {
		t.Fatal(err)
	}

	signer := NewSigner("secret")
	req := httptest.NewRequest(http.MethodGet, "/media/videos/sample.mp4?"+signer.Query("videos/sample.mp4", time.Now().Add(time.Hour)).Encode(), nil)
	rec := httptest.NewRecorder()

	NewHandler(root, signer).ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected status %d, got %d", http.StatusNotFound, rec.Code)
	}
}

func TestSignedSymlinkParentMediaRequestIsRejected(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	outsideRoot := t.TempDir()
	writeFile(t, filepath.Join(outsideRoot, "sample.mp4"), "secret")
	if err := os.Symlink(outsideRoot, filepath.Join(root, "videos")); err != nil {
		t.Fatal(err)
	}

	signer := NewSigner("secret")
	req := httptest.NewRequest(http.MethodGet, "/media/videos/sample.mp4?"+signer.Query("videos/sample.mp4", time.Now().Add(time.Hour)).Encode(), nil)
	rec := httptest.NewRecorder()

	NewHandler(root, signer).ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected status %d, got %d", http.StatusNotFound, rec.Code)
	}
}

func TestSignedSymlinkRootMediaRequestIsRejected(t *testing.T) {
	t.Parallel()

	actualRoot := t.TempDir()
	writeFile(t, filepath.Join(actualRoot, "videos", "sample.mp4"), "secret")
	root := filepath.Join(t.TempDir(), "media")
	if err := os.Symlink(actualRoot, root); err != nil {
		t.Fatal(err)
	}

	signer := NewSigner("secret")
	req := httptest.NewRequest(http.MethodGet, "/media/videos/sample.mp4?"+signer.Query("videos/sample.mp4", time.Now().Add(time.Hour)).Encode(), nil)
	rec := httptest.NewRecorder()

	NewHandler(root, signer).ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected status %d, got %d", http.StatusNotFound, rec.Code)
	}
}

func writeFile(t *testing.T, path string, body string) {
	t.Helper()

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

type deadlineRecorder struct {
	*httptest.ResponseRecorder
	deadlineSet bool
	deadline    time.Time
}

func (r *deadlineRecorder) SetWriteDeadline(deadline time.Time) error {
	r.deadlineSet = true
	r.deadline = deadline
	return nil
}

func TestSignerQueryUsesURLValues(t *testing.T) {
	t.Parallel()

	values := NewSigner("secret").Query("videos/sample.mp4", time.Now().Add(time.Hour))
	if _, err := url.ParseQuery(values.Encode()); err != nil {
		t.Fatal(err)
	}
}
