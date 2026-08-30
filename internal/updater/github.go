package updater

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"runtime"
	"strings"
	"time"
)

const (
	githubAPIBaseURL     = "https://api.github.com"
	githubAPIContentType = "application/vnd.github+json"

	// maxReleaseJSONBytes bounds a GitHub releases API response payload.
	maxReleaseJSONBytes = 4 << 20
	// maxUpdateAssetBytes bounds a downloaded release binary. A kapsel
	// release binary is tens of megabytes; the cap only stops runaway
	// downloads from filling the data volume.
	maxUpdateAssetBytes = 512 << 20
	// maxChecksumAssetBytes bounds a checksums sidecar file.
	maxChecksumAssetBytes = 1 << 20
	// updateHTTPTimeout bounds a single GitHub API or asset request.
	updateHTTPTimeout = 5 * time.Minute
)

// ErrNoReleases reports that the update repository publishes no releases yet.
var ErrNoReleases = errors.New("update repository has no releases")

// ErrReleaseNotFound reports that a specific requested release (by tag) is
// absent — e.g. it was deleted after an offer was recorded.
var ErrReleaseNotFound = errors.New("release not found")

// ErrNoMatchingAsset reports that a release publishes no binary asset for
// this platform.
var ErrNoMatchingAsset = errors.New("release has no binary asset for this platform")

// ErrNoChecksumAsset reports that a release publishes no checksum sidecar.
var ErrNoChecksumAsset = errors.New("release has no checksum sidecar")

// ErrUnsupportedDownloadURL reports a release asset URL outside the GitHub
// download hosts. Release binaries are only installed from GitHub.
var ErrUnsupportedDownloadURL = errors.New("release asset URL is not a GitHub download URL")

type githubClient struct {
	baseURL string
	client  *http.Client
}

type githubRelease struct {
	TagName         string            `json:"tag_name"`
	Name            string            `json:"name"`
	HTMLURL         string            `json:"html_url"`
	PublishedAt     string            `json:"published_at"`
	Body            string            `json:"body"`
	Draft           bool              `json:"draft"`
	Prerelease      bool              `json:"prerelease"`
	Assets          []githubAssetList `json:"assets"`
	fetchedChecksum *downloadedFile
}

type githubAssetList struct {
	Name               string `json:"name"`
	Size               int64  `json:"size"`
	BrowserDownloadURL string `json:"browser_download_url"`
}

type downloadedFile struct {
	Name     string
	Body     []byte
	Checksum string
}

func newGitHubClient() *githubClient {
	return &githubClient{baseURL: githubAPIBaseURL, client: &http.Client{Timeout: updateHTTPTimeout}}
}

func (c *githubClient) do(ctx context.Context, requestURL string, maxBytes int64) ([]byte, error) {
	parsed, err := url.Parse(requestURL)
	if err != nil {
		return nil, fmt.Errorf("invalid request URL %q: %w", requestURL, err)
	}
	// Tests point baseURL at an httptest server, which serves plain http.
	if parsed.Scheme != "https" && !(parsed.Scheme == "http" && c.baseURL != githubAPIBaseURL) {
		return nil, fmt.Errorf("request URL must use https: %q", requestURL)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, parsed.String(), nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Accept", githubAPIContentType)
	request.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	request.Header.Set("User-Agent", "kapsel-self-update")

	response, err := c.client.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, maxBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > maxBytes {
		return nil, fmt.Errorf("response from %s exceeds %d bytes", parsed.Host, maxBytes)
	}
	if response.StatusCode == http.StatusNotFound {
		return nil, ErrNoReleases
	}
	if response.StatusCode == http.StatusTooManyRequests || response.StatusCode == http.StatusForbidden {
		return nil, fmt.Errorf("GitHub rate limit reached (status %d, resets %s)", response.StatusCode, response.Header.Get("X-RateLimit-Reset"))
	}
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GitHub request failed with status %d", response.StatusCode)
	}

	return body, nil
}

func (c *githubClient) release(ctx context.Context, repo string, tag string) (*githubRelease, error) {
	repo = strings.TrimSpace(repo)
	if repo == "" || strings.Count(repo, "/") != 1 {
		return nil, fmt.Errorf("update repository must be owner/name, got %q", repo)
	}
	endpoint := "/latest"
	if tag = strings.TrimSpace(tag); tag != "" {
		endpoint = "/tags/" + url.PathEscape(tag)
	}
	body, err := c.do(ctx, c.baseURL+"/repos/"+repo+"/releases"+endpoint, maxReleaseJSONBytes)
	if err != nil {
		// A 404 on the by-tag path means that specific release is gone (or
		// was never published); reporting "no releases" would mislead the
		// admin looking at a pending offer for that exact tag.
		if tag != "" && errors.Is(err, ErrNoReleases) {
			return nil, fmt.Errorf("%w: %s is not published in %s", ErrReleaseNotFound, tag, repo)
		}

		return nil, err
	}
	var release githubRelease
	if err := json.Unmarshal(body, &release); err != nil {
		return nil, fmt.Errorf("could not parse GitHub release response: %w", err)
	}
	if strings.TrimSpace(release.TagName) == "" {
		return nil, fmt.Errorf("GitHub release response has no tag")
	}

	return &release, nil
}

// selectAsset finds the plain-binary release asset for this platform. The
// release convention is kapsel_<os>_<arch> (dashes or underscores, optional
// v-prefixed version and platform segments); archives are ignored.
func (r *githubRelease) selectAsset(goos string, goarch string) (*githubAssetList, error) {
	platformTokens := []string{goos + string(assetSeparatorSet[0]) + goarch, goos + string(assetSeparatorSet[1]) + goarch}
	for index := range r.Assets {
		asset := &r.Assets[index]
		name := strings.ToLower(asset.Name)
		if isArchiveAssetName(name) {
			continue
		}
		for _, token := range platformTokens {
			if strings.Contains(name, token) {
				return asset, nil
			}
		}
	}

	return nil, fmt.Errorf("%w (%s/%s)", ErrNoMatchingAsset, goos, goarch)
}

var assetSeparatorSet = [2]rune{'-', '_'}

var archiveExtensions = []string{".zip", ".tar.gz", ".tgz", ".tar.xz", ".txz", ".tar.bz2", ".gz", ".xz", ".bz2", ".dmg", ".deb", ".rpm", ".apk"}

func isArchiveAssetName(name string) bool {
	for _, extension := range archiveExtensions {
		if strings.HasSuffix(name, extension) {
			return true
		}
	}

	return false
}

// selectChecksumAsset finds the release checksum sidecar for the given
// binary asset. Supported conventions: a shared checksums.txt or
// sha256sums.txt, or a per-asset <asset>.sha256(.sum) sidecar.
func (r *githubRelease) selectChecksumAsset(assetName string) (*githubAssetList, error) {
	var shared *githubAssetList
	var perAsset *githubAssetList
	assetLower := strings.ToLower(assetName)
	for index := range r.Assets {
		asset := &r.Assets[index]
		name := strings.ToLower(asset.Name)
		switch {
		case name == "checksums.txt" || name == "checksums" || name == "sha256sums.txt" || name == "sha256sums":
			if shared == nil {
				shared = asset
			}
		case name == assetLower+".sha256" || name == assetLower+".sha256sum" || name == assetLower+".sha256sums" || name == assetLower+".sha":
			if perAsset == nil {
				perAsset = asset
			}
		}
	}
	if shared != nil {
		return shared, nil
	}
	if perAsset != nil {
		return perAsset, nil
	}

	return nil, ErrNoChecksumAsset
}

// downloadURLAllowed verifies that an asset download URL points at GitHub's
// release hosts over HTTPS. Anything else is rejected.
func downloadURLAllowed(rawURL string) error {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return fmt.Errorf("%w: %v", ErrUnsupportedDownloadURL, err)
	}
	if parsed.Scheme != "https" {
		return fmt.Errorf("%w: scheme %q", ErrUnsupportedDownloadURL, parsed.Scheme)
	}
	host := strings.ToLower(parsed.Hostname())
	if host == "github.com" || host == "objects.githubusercontent.com" || host == "release-assets.githubusercontent.com" || strings.HasSuffix(host, ".githubusercontent.com") {
		return nil
	}

	return fmt.Errorf("%w: host %q", ErrUnsupportedDownloadURL, host)
}

// downloadURLAllowedFor gates asset downloads for one updater. Tests may
// relax the GitHub host allowlist to point at an httptest server.
func (u *Updater) downloadURLAllowedFor(rawURL string) error {
	if u.config.allowAnyDownloadURL {
		return nil
	}

	return downloadURLAllowed(rawURL)
}

// platform identifies the binary asset selector for this runtime.
func platform() (string, string) {
	return runtime.GOOS, runtime.GOARCH
}
