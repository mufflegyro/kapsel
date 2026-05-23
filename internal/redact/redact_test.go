package redact

import (
	"strings"
	"testing"
)

func TestTextRedactsURLsAndCommonSecrets(t *testing.T) {
	t.Parallel()

	output := Text("failed https://user:pass@example.com/watch?v=abc&token=secret#frag Authorization: Bearer supersecret\ntoken=plain-secret\napi-key: header-secret\n\"password\": \"json-secret\"\nsecret = spaced-secret", 1200)

	for _, expected := range []string{"https://example.com/watch", "Authorization: [redacted]", "token=[redacted]", "api-key: [redacted]", "\"password\": [redacted]", "secret = [redacted]"} {
		if !strings.Contains(output, expected) {
			t.Fatalf("expected output to contain %q, got %q", expected, output)
		}
	}
	for _, secret := range []string{"user:pass", "token=secret", "frag", "supersecret", "plain-secret", "header-secret", "json-secret", "spaced-secret"} {
		if strings.Contains(output, secret) {
			t.Fatalf("expected output to redact %q, got %q", secret, output)
		}
	}
}

func TestTextStripsUnreadableControlCharsAndTruncates(t *testing.T) {
	t.Parallel()

	output := Text(" \x00abc\tdef\n"+strings.Repeat("x", 20), 10)

	if output != "abc\tdef\nxx ... [truncated]" {
		t.Fatalf("unexpected sanitized text %q", output)
	}
}

func TestTextDoesNotTruncateWhenMaxLengthDisabled(t *testing.T) {
	t.Parallel()

	output := Text("token=secret\n"+strings.Repeat("x", 2000)+"tail", 0)

	if !strings.Contains(output, "tail") || strings.Contains(output, "[truncated]") {
		t.Fatalf("expected unbounded sanitized text, got %q", output)
	}
	if strings.Contains(output, "token=secret") || !strings.Contains(output, "token=[redacted]") {
		t.Fatalf("expected unbounded text to still redact secrets, got %q", output)
	}
}
