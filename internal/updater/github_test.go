package updater

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func testReleaseBody(tag string, assets []githubAssetList) []byte {
	body, err := json.Marshal(githubRelease{
		TagName:     tag,
		HTMLURL:     "https://github.com/mufflegyro/yummle/releases/tag/" + tag,
		PublishedAt: "2026-08-01T12:00:00Z",
		Body:        "Release " + tag,
		Assets:      assets,
	})
	if err != nil {
		panic(err)
	}

	return body
}

func newTestGitHubServer(t *testing.T, handler http.HandlerFunc) *githubClient {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	client := newGitHubClient()
	client.baseURL = server.URL

	return client
}

func TestGitHubClientFetchesLatestRelease(t *testing.T) {
	t.Parallel()

	var gotPath string
	client := newTestGitHubServer(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(testReleaseBody("v1.4.0", []githubAssetList{
			{Name: "checksums.txt", Size: 120, BrowserDownloadURL: "https://github.com/mufflegyro/yummle/releases/download/v1.4.0/checksums.txt"},
		}))
	})
	release, err := client.release(context.Background(), "mufflegyro/yummle", "")
	if err != nil {
		t.Fatal(err)
	}
	if release.TagName != "v1.4.0" {
		t.Errorf("TagName = %q, want v1.4.0", release.TagName)
	}
	if gotPath != "/repos/mufflegyro/yummle/releases/latest" {
		t.Errorf("request path = %q", gotPath)
	}
}

func TestGitHubClientFetchesReleaseByTag(t *testing.T) {
	t.Parallel()

	var gotPath string
	client := newTestGitHubServer(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_, _ = w.Write(testReleaseBody("v1.2.3", nil))
	})
	release, err := client.release(context.Background(), "mufflegyro/yummle", "v1.2.3")
	if err != nil {
		t.Fatal(err)
	}
	if release.TagName != "v1.2.3" {
		t.Errorf("TagName = %q", release.TagName)
	}
	if gotPath != "/repos/mufflegyro/yummle/releases/tags/v1.2.3" {
		t.Errorf("request path = %q", gotPath)
	}
}

func TestGitHubClientByTag404MapsToReleaseNotFound(t *testing.T) {
	t.Parallel()

	client := newTestGitHubServer(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/releases/latest") {
			_, _ = w.Write(testReleaseBody("v1.0.0", nil))

			return
		}
		http.NotFound(w, nil)
	})
	// The latest endpoint still resolves.
	if _, err := client.release(context.Background(), "mufflegyro/yummle", ""); err != nil {
		t.Fatal(err)
	}
	// A 404 on the by-tag endpoint means that release is gone — not that
	// the repository has no releases.
	_, err := client.release(context.Background(), "mufflegyro/yummle", "v9.9.9")
	if !errors.Is(err, ErrReleaseNotFound) {
		t.Fatalf("err = %v, want ErrReleaseNotFound", err)
	}
	if !strings.Contains(err.Error(), "v9.9.9") {
		t.Errorf("error should name the missing tag, got %q", err.Error())
	}
}

func TestGitHubClientEscapesTagPath(t *testing.T) {
	t.Parallel()

	var gotPath string
	client := newTestGitHubServer(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_, _ = w.Write(testReleaseBody("v1.2.3-rc.1", nil))
	})
	if _, err := client.release(context.Background(), "mufflegyro/yummle", "v1.2.3+build.5"); err != nil {
		t.Fatal(err)
	}
	// URL path escaping keeps traversal and spaces out of the endpoint.
	if strings.Contains(gotPath, " ") || strings.Contains(gotPath, "..") {
		t.Errorf("request path = %q, expected escaped", gotPath)
	}
}

func TestGitHubClientMapsNotFoundToNoReleases(t *testing.T) {
	t.Parallel()

	client := newTestGitHubServer(t, func(w http.ResponseWriter, _ *http.Request) {
		http.NotFound(w, nil)
	})
	if _, err := client.release(context.Background(), "mufflegyro/yummle", ""); !errors.Is(err, ErrNoReleases) {
		t.Fatalf("err = %v, want ErrNoReleases", err)
	}
}

func TestGitHubClientSurfacesRateLimit(t *testing.T) {
	t.Parallel()

	client := newTestGitHubServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("X-RateLimit-Reset", "1800")
		w.WriteHeader(http.StatusForbidden)
	})
	_, err := client.release(context.Background(), "mufflegyro/yummle", "")
	if err == nil || !strings.Contains(err.Error(), "rate limit") {
		t.Fatalf("err = %v, want rate limit message", err)
	}
}

func TestGitHubClientRejectsBadRepo(t *testing.T) {
	t.Parallel()

	client := newTestGitHubServer(t, func(w http.ResponseWriter, _ *http.Request) {
		t.Error("request should not be sent for a bad repo")
	})
	if _, err := client.release(context.Background(), "justaname", ""); err == nil {
		t.Fatal("expected error for repo without owner/name")
	}
	if _, err := client.release(context.Background(), "", ""); err == nil {
		t.Fatal("expected error for empty repo")
	}
}

func TestSelectAssetPrefersPlatformBinary(t *testing.T) {
	t.Parallel()

	release := githubRelease{Assets: []githubAssetList{
		{Name: "kapsel_linux_amd64.tar.gz"},
		{Name: "kapsel_linux_amd64"},
		{Name: "kapsel_darwin_arm64"},
	}}
	asset, err := release.selectAsset("linux", "amd64")
	if err != nil {
		t.Fatal(err)
	}
	if asset.Name != "kapsel_linux_amd64" {
		t.Errorf("selected %q, want the plain binary", asset.Name)
	}
	if _, err := release.selectAsset("windows", "386"); !errors.Is(err, ErrNoMatchingAsset) {
		t.Fatalf("err = %v, want ErrNoMatchingAsset", err)
	}
}

func TestSelectChecksumAsset(t *testing.T) {
	t.Parallel()

	release := githubRelease{Assets: []githubAssetList{
		{Name: "kapsel_linux_amd64"},
		{Name: "kapsel_linux_amd64.sha256"},
		{Name: "checksums.txt"},
	}}
	assetName := "kapsel_linux_amd64"

	checksum, err := release.selectChecksumAsset(assetName)
	if err != nil {
		t.Fatal(err)
	}
	if checksum.Name != "checksums.txt" {
		t.Errorf("selected %q, want shared checksums.txt", checksum.Name)
	}

	shared := githubRelease{Assets: []githubAssetList{
		{Name: "kapsel_linux_amd64"},
		{Name: "kapsel_linux_amd64.sha256"},
	}}
	checksum, err = shared.selectChecksumAsset(assetName)
	if err != nil {
		t.Fatal(err)
	}
	if checksum.Name != "kapsel_linux_amd64.sha256" {
		t.Errorf("selected %q, want the per-asset sidecar", checksum.Name)
	}

	if _, err := release.selectChecksumAsset("kapsel_linux_amd64"); err != nil {
		t.Fatalf("shared checksums.txt should cover any asset: %v", err)
	}

	bare := githubRelease{Assets: []githubAssetList{
		{Name: "kapsel_linux_amd64"},
	}}
	if _, err := bare.selectChecksumAsset("kapsel_linux_amd64"); !errors.Is(err, ErrNoChecksumAsset) {
		t.Fatalf("err = %v, want ErrNoChecksumAsset", err)
	}
}

func TestExpectedChecksumParsesSidecarFormats(t *testing.T) {
	t.Parallel()

	shared := "a665a45920422f9d417e4867efdc4fb8a04a1f3fff1fa07e998e86f7f7a27ae3  kapsel_linux_amd64\n# comment\nabc123  other\n"
	digest, err := expectedChecksum(shared, "kapsel_linux_amd64", "checksums.txt")
	if err != nil {
		t.Fatal(err)
	}
	if digest != "a665a45920422f9d417e4867efdc4fb8a04a1f3fff1fa07e998e86f7f7a27ae3" {
		t.Errorf("digest = %q", digest)
	}

	// Binary-mode marker (uppercase, asterisk prefix) and per-asset bare digest.
	bare := "ABCDEF0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF0123456789"
	digest, err = expectedChecksum(bare+"\n", "kapsel_linux_amd64", "kapsel_linux_amd64.sha256")
	if err != nil {
		t.Fatal(err)
	}
	if digest != strings.ToLower(bare) {
		t.Errorf("digest = %q, want %q", digest, strings.ToLower(bare))
	}

	if _, err := expectedChecksum("", "kapsel_linux_amd64", "checksums.txt"); err == nil {
		t.Fatal("expected error when sidecar lacks the asset")
	}
}

func TestDownloadURLAllowed(t *testing.T) {
	t.Parallel()

	allowed := []string{
		"https://github.com/mufflegyro/yummle/releases/download/v1.2.3/kapsel_linux_amd64",
		"https://objects.githubusercontent.com/some/object",
		"https://release-assets.githubusercontent.com/x",
		"https://something.githubusercontent.com/file",
	}
	for _, rawURL := range allowed {
		if err := downloadURLAllowed(rawURL); err != nil {
			t.Errorf("downloadURLAllowed(%q) = %v, want nil", rawURL, err)
		}
	}

	rejected := []string{
		"http://github.com/download",
		"https://evil.example.com/kapsel",
		"https://github.com.evil.example.com/kapsel",
		"not a url",
	}
	for _, rawURL := range rejected {
		if err := downloadURLAllowed(rawURL); err == nil {
			t.Errorf("downloadURLAllowed(%q) = nil, want error", rawURL)
		}
	}
}
