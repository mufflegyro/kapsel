package redact

import (
	"net/url"
	"regexp"
	"strings"
)

var urlPattern = regexp.MustCompile(`https?://[^\s"']+`)

var secretPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)\b(authorization\s*:\s*)[^\r\n]+`),
	regexp.MustCompile(`(?i)\b(cookie\s*:\s*)[^\r\n]+`),
	regexp.MustCompile(`(?i)(["']?\b(?:token|api[_-]?key|password|passwd|secret)["']?\s*[:=]\s*)(["'][^"']*["'])`),
	regexp.MustCompile(`(?i)(\b(?:token|api[_-]?key|password|passwd|secret)\s*[:=]\s*)[^\r\n]+`),
}

func Text(text string, maxLength int) string {
	text = urlPattern.ReplaceAllStringFunc(text, redactURL)
	for _, pattern := range secretPatterns {
		text = pattern.ReplaceAllString(text, "$1[redacted]")
	}
	text = strings.Map(func(r rune) rune {
		if r < 0x20 && r != '\n' && r != '\t' {
			return -1
		}
		return r
	}, text)
	text = strings.TrimSpace(text)
	if maxLength <= 0 || len(text) <= maxLength {
		return text
	}

	return strings.TrimSpace(text[:maxLength]) + " ... [truncated]"
}

func redactURL(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return raw
	}
	parsed.User = nil
	parsed.RawQuery = ""
	parsed.Fragment = ""

	return parsed.String()
}
