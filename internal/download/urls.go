package download

import (
	"errors"
	"net/url"
	"strings"
)

func ChannelScanPayloadForExternalID(channelID string, externalID string) (ChannelScanPayload, error) {
	channelID = strings.TrimSpace(channelID)
	if channelID == "" {
		return ChannelScanPayload{}, errors.New("channel scan payload missing channel id")
	}
	channelURL, err := channelURLFromExternalID(externalID)
	if err != nil {
		return ChannelScanPayload{}, err
	}

	return ChannelScanPayload{URL: channelURL, ChannelID: channelID}, nil
}

// NormalizePlaylistURL validates a YouTube playlist link and returns the
// canonical playlist URL plus its list id. Accepted forms:
//
//	https://www.youtube.com/playlist?list=<id>
//	https://www.youtube.com/watch?v=<video>&list=<id>
//	https://youtu.be/<video>?list=<id>
//
// A list query parameter is required; non-YouTube hosts and empty/invalid
// list ids are rejected.
func NormalizePlaylistURL(rawURL string) (string, string, error) {
	value := strings.TrimSpace(rawURL)
	if value == "" {
		return "", "", ErrDownloadURLRequired
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return "", "", ErrUnsupportedURLScheme
	}
	parsed.Scheme = strings.ToLower(parsed.Scheme)
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", "", ErrUnsupportedURLScheme
	}
	host := strings.ToLower(parsed.Hostname())
	if !isYouTubeHost(host) && host != "youtu.be" {
		return "", "", ErrUnsupportedPlaylistURL
	}
	listID := strings.TrimSpace(parsed.Query().Get("list"))
	if !isLikelyYouTubeListID(listID) {
		return "", "", ErrUnsupportedPlaylistURL
	}
	canonical := "https://www.youtube.com/playlist?list=" + url.QueryEscape(listID)

	return canonical, listID, nil
}

func isLikelyYouTubeListID(value string) bool {
	if value == "" || len(value) > 64 {
		return false
	}
	for _, char := range value {
		if char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' || char >= '0' && char <= '9' || char == '_' || char == '-' {
			continue
		}
		return false
	}

	return true
}

func channelURLFromExternalID(externalID string) (string, error) {
	externalID = strings.TrimSpace(externalID)
	if strings.HasPrefix(externalID, "@") {
		return NormalizeChannelURL("https://www.youtube.com/" + externalID)
	}

	return NormalizeChannelURL("https://www.youtube.com/channel/" + url.PathEscape(externalID))
}

func channelVideosURL(rawURL string) (string, error) {
	channelURL, err := NormalizeChannelURL(rawURL)
	if err != nil {
		return "", err
	}
	parsed, err := url.Parse(channelURL)
	if err != nil {
		return "", ErrUnsupportedChannelURL
	}
	path := strings.Trim(parsed.Path, "/")
	switch {
	case strings.HasPrefix(path, "@"):
		handle := firstPathSegment(strings.TrimPrefix(path, "@"))
		parsed.Path = "/@" + handle + "/videos"
	case hasPathPrefixValue(path, "channel/"):
		parsed.Path = "/channel/" + firstPathSegment(strings.TrimPrefix(path, "channel/")) + "/videos"
	case hasPathPrefixValue(path, "c/"):
		parsed.Path = "/c/" + firstPathSegment(strings.TrimPrefix(path, "c/")) + "/videos"
	case hasPathPrefixValue(path, "user/"):
		parsed.Path = "/user/" + firstPathSegment(strings.TrimPrefix(path, "user/")) + "/videos"
	}
	parsed.RawQuery = ""
	parsed.Fragment = ""

	return parsed.String(), nil
}

func normalizeDirectYouTubeVideoURL(parsed *url.URL) (string, error) {
	host := strings.ToLower(parsed.Hostname())
	path := strings.Trim(parsed.EscapedPath(), "/")
	if host == "youtu.be" {
		return youtubeDirectWatchURL(firstPathSegment(path))
	}
	if !isYouTubeHost(host) {
		return "", ErrUnsupportedVideoURL
	}
	if path == "watch" {
		return youtubeDirectWatchURL(parsed.Query().Get("v"))
	}
	for _, prefix := range []string{"shorts/", "embed/", "live/", "v/"} {
		if hasPathPrefixValue(path, prefix) {
			return youtubeDirectWatchURL(firstPathSegment(strings.TrimPrefix(path, prefix)))
		}
	}

	return "", ErrUnsupportedVideoURL
}

func youtubeDirectWatchURL(videoID string) (string, error) {
	if !isLikelyYouTubeVideoID(videoID) {
		return "", ErrUnsupportedVideoURL
	}

	return youtubeWatchURL(videoID), nil
}

func NormalizeChannelURL(rawURL string) (string, error) {
	channelURL, err := NormalizeURL(rawURL)
	if err != nil {
		return "", err
	}
	parsed, err := url.Parse(channelURL)
	if err != nil {
		return "", ErrUnsupportedChannelURL
	}
	host := strings.ToLower(parsed.Hostname())
	if !isYouTubeHost(host) {
		return "", ErrUnsupportedChannelURL
	}
	path := strings.Trim(parsed.Path, "/")
	if strings.HasPrefix(path, "@") {
		handle := firstPathSegment(strings.TrimPrefix(path, "@"))
		if handle != "" {
			return canonicalYouTubeChannelURL("@" + handle), nil
		}
	}
	for _, prefix := range []string{"channel/", "c/", "user/"} {
		if hasPathPrefixValue(path, prefix) {
			return canonicalYouTubeChannelURL(strings.TrimSuffix(prefix, "/") + "/" + firstPathSegment(strings.TrimPrefix(path, prefix))), nil
		}
	}

	return "", ErrUnsupportedChannelURL
}

func canonicalYouTubeChannelURL(path string) string {
	return (&url.URL{Scheme: "https", Host: "www.youtube.com", Path: "/" + strings.Trim(path, "/")}).String()
}

func videoIDFromWatchURL(rawURL string) string {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}

	return strings.TrimSpace(parsed.Query().Get("v"))
}

func NormalizeYouTubeVideoURL(rawURL string) (string, error) {
	videoURL, err := NormalizeURL(rawURL)
	if err != nil {
		return "", err
	}
	parsed, err := url.Parse(videoURL)
	if err != nil {
		return "", ErrUnsupportedChannelURL
	}
	host := strings.ToLower(parsed.Hostname())
	path := strings.Trim(parsed.EscapedPath(), "/")
	if host == "youtu.be" {
		if videoID := firstPathSegment(path); videoID != "" {
			return "https://www.youtube.com/watch?v=" + url.QueryEscape(videoID), nil
		}
		return "", ErrUnsupportedChannelURL
	}
	if !isYouTubeHost(host) {
		return "", ErrUnsupportedChannelURL
	}
	if path == "watch" {
		if videoID := parsed.Query().Get("v"); videoID != "" {
			return "https://www.youtube.com/watch?v=" + url.QueryEscape(videoID), nil
		}
	}
	if hasPathPrefixValue(path, "shorts/") {
		return "https://www.youtube.com/watch?v=" + url.QueryEscape(firstPathSegment(strings.TrimPrefix(path, "shorts/"))), nil
	}
	if hasPathPrefixValue(path, "embed/") {
		return "https://www.youtube.com/watch?v=" + url.QueryEscape(firstPathSegment(strings.TrimPrefix(path, "embed/"))), nil
	}

	return "", ErrUnsupportedChannelURL
}

func isYouTubeHost(host string) bool {
	host = strings.ToLower(host)
	return host == "youtube.com" || strings.HasSuffix(host, ".youtube.com")
}

func hasPathPrefixValue(path string, prefix string) bool {
	return strings.HasPrefix(path, prefix) && firstPathSegment(strings.TrimPrefix(path, prefix)) != ""
}

func firstPathSegment(path string) string {
	return strings.Split(path, "/")[0]
}

func isLikelyYouTubeVideoID(value string) bool {
	if len(value) != 11 {
		return false
	}
	for _, char := range value {
		if char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' || char >= '0' && char <= '9' || char == '_' || char == '-' {
			continue
		}
		return false
	}

	return true
}

func youtubeWatchURL(videoID string) string {
	return "https://www.youtube.com/watch?v=" + url.QueryEscape(videoID)
}
