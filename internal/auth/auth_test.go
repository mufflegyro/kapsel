package auth

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestPasswordHashVerifiesAndRejectsWrongPassword(t *testing.T) {
	t.Parallel()

	hash, err := HashPassword("correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(hash, "$argon2id$") {
		t.Fatalf("expected argon2id password hash, got %q", hash)
	}
	if !VerifyPassword("correct horse battery staple", hash) {
		t.Fatal("expected password to verify")
	}
	if VerifyPassword("wrong password", hash) {
		t.Fatal("expected wrong password to fail verification")
	}
}

func TestManagerAuthenticatesAndSignsSessionCookie(t *testing.T) {
	t.Parallel()

	hash, err := HashPassword("open sesame")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 5, 3, 12, 0, 0, 0, time.UTC)
	manager := NewManager(Config{Enabled: true, Username: "admin", PasswordHash: hash, SessionSecret: "session-secret", SessionTTL: time.Hour, Now: func() time.Time { return now }})

	if !manager.VerifyLogin("admin", "open sesame") {
		t.Fatal("expected login credentials to verify")
	}
	if manager.VerifyLogin("admin", "wrong") || manager.VerifyLogin("other", "open sesame") {
		t.Fatal("expected invalid login credentials to fail")
	}
	cookie := manager.SessionCookie("admin")
	if cookie.Name != SessionCookieName || cookie.Value == "" {
		t.Fatalf("unexpected session cookie before security assertions: %#v", cookie)
	}
	if !cookie.HttpOnly || cookie.SameSite != http.SameSiteLaxMode || cookie.Path != "/" {
		t.Fatalf("expected secure session cookie attributes, got %#v", cookie)
	}

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(cookie)
	username, ok := manager.AuthenticatedUser(req)
	if !ok || username != "admin" {
		t.Fatalf("expected authenticated user admin, got %q ok=%v", username, ok)
	}
}

func TestManagerCanMarkSessionCookieSecure(t *testing.T) {
	t.Parallel()

	hash, err := HashPassword("open sesame")
	if err != nil {
		t.Fatal(err)
	}
	manager := NewManager(Config{Enabled: true, Username: "admin", PasswordHash: hash, SessionSecret: "session-secret", CookieSecure: true})

	cookie := manager.SessionCookie("admin")
	if !cookie.Secure {
		t.Fatalf("expected secure session cookie, got %#v", cookie)
	}
	clearCookie := manager.ClearSessionCookie()
	if !clearCookie.Secure {
		t.Fatalf("expected secure clear-session cookie, got %#v", clearCookie)
	}
}

func TestManagerRejectsExpiredSessionCookie(t *testing.T) {
	t.Parallel()

	hash, err := HashPassword("open sesame")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 5, 3, 12, 0, 0, 0, time.UTC)
	manager := NewManager(Config{Enabled: true, Username: "admin", PasswordHash: hash, SessionSecret: "session-secret", SessionTTL: time.Minute, Now: func() time.Time { return now }})
	cookie := manager.SessionCookie("admin")

	expired := NewManager(Config{Enabled: true, Username: "admin", PasswordHash: hash, SessionSecret: "session-secret", SessionTTL: time.Minute, Now: func() time.Time { return now.Add(2 * time.Minute) }})
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(cookie)
	if username, ok := expired.AuthenticatedUser(req); ok || username != "" {
		t.Fatalf("expected expired session to be rejected, got %q ok=%v", username, ok)
	}
}
