package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/argon2"
)

const SessionCookieName = "kapsel_session"

const (
	argonVersion      = argon2.Version
	argonMemoryKiB    = 64 * 1024
	argonIterations   = 3
	argonParallelism  = 1
	argonSaltBytes    = 16
	argonKeyBytes     = 32
	defaultSessionTTL = 7 * 24 * time.Hour
)

type Config struct {
	Enabled       bool
	Username      string
	PasswordHash  string
	SessionSecret string
	SessionTTL    time.Duration
	CookieSecure  bool
	Now           func() time.Time
}

type Manager struct {
	enabled       bool
	username      string
	passwordHash  string
	sessionSecret []byte
	sessionTTL    time.Duration
	cookieSecure  bool
	now           func() time.Time
}

type sessionPayload struct {
	Username string `json:"u"`
	Expires  int64  `json:"e"`
}

func NewManager(config Config) *Manager {
	if config.SessionTTL <= 0 {
		config.SessionTTL = defaultSessionTTL
	}
	if config.Now == nil {
		config.Now = time.Now
	}

	return &Manager{
		enabled:       config.Enabled,
		username:      strings.TrimSpace(config.Username),
		passwordHash:  strings.TrimSpace(config.PasswordHash),
		sessionSecret: []byte(config.SessionSecret),
		sessionTTL:    config.SessionTTL,
		cookieSecure:  config.CookieSecure,
		now:           config.Now,
	}
}

func (m *Manager) Enabled() bool {
	return m != nil && m.enabled
}

func (m *Manager) Configured() bool {
	if m == nil || !m.enabled {
		return true
	}

	return m.username != "" && m.passwordHash != "" && len(m.sessionSecret) > 0
}

func (m *Manager) VerifyLogin(username string, password string) bool {
	if m == nil || !m.enabled || !m.Configured() {
		return false
	}
	if subtle.ConstantTimeCompare([]byte(strings.TrimSpace(username)), []byte(m.username)) != 1 {
		return false
	}

	return VerifyPassword(password, m.passwordHash)
}

func (m *Manager) SessionCookie(username string) *http.Cookie {
	expires := m.now().Add(m.sessionTTL)
	payload := sessionPayload{Username: username, Expires: expires.Unix()}
	payloadJSON, _ := json.Marshal(payload)
	payloadText := base64.RawURLEncoding.EncodeToString(payloadJSON)

	return &http.Cookie{
		Name:     SessionCookieName,
		Value:    payloadText + "." + m.sign(payloadText),
		Path:     "/",
		Expires:  expires,
		MaxAge:   int(m.sessionTTL.Seconds()),
		HttpOnly: true,
		Secure:   m.cookieSecure,
		SameSite: http.SameSiteLaxMode,
	}
}

func (m *Manager) ClearSessionCookie() *http.Cookie {
	return &http.Cookie{
		Name:     SessionCookieName,
		Value:    "",
		Path:     "/",
		Expires:  time.Unix(0, 0),
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   m.cookieSecure,
		SameSite: http.SameSiteLaxMode,
	}
}

func (m *Manager) AuthenticatedUser(r *http.Request) (string, bool) {
	if m == nil || !m.enabled {
		return "development", true
	}
	if !m.Configured() {
		return "", false
	}
	cookie, err := r.Cookie(SessionCookieName)
	if err != nil || cookie.Value == "" {
		return "", false
	}
	parts := strings.Split(cookie.Value, ".")
	if len(parts) != 2 || !m.validSignature(parts[0], parts[1]) {
		return "", false
	}
	payloadJSON, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return "", false
	}
	var payload sessionPayload
	if err := json.Unmarshal(payloadJSON, &payload); err != nil {
		return "", false
	}
	if payload.Username == "" || payload.Username != m.username || m.now().Unix() > payload.Expires {
		return "", false
	}

	return payload.Username, true
}

func (m *Manager) sign(payload string) string {
	mac := hmac.New(sha256.New, m.sessionSecret)
	mac.Write([]byte(payload))

	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func (m *Manager) validSignature(payload string, signature string) bool {
	expected := m.sign(payload)

	return hmac.Equal([]byte(signature), []byte(expected))
}

func HashPassword(password string) (string, error) {
	if password == "" {
		return "", errors.New("password is required")
	}
	salt := make([]byte, argonSaltBytes)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	hash := argon2.IDKey([]byte(password), salt, argonIterations, argonMemoryKiB, argonParallelism, argonKeyBytes)

	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argonVersion,
		argonMemoryKiB,
		argonIterations,
		argonParallelism,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(hash),
	), nil
}

func VerifyPassword(password string, encodedHash string) bool {
	params, salt, hash, err := parsePasswordHash(encodedHash)
	if err != nil {
		return false
	}
	candidate := argon2.IDKey([]byte(password), salt, params.iterations, params.memory, params.parallelism, uint32(len(hash)))

	return subtle.ConstantTimeCompare(candidate, hash) == 1
}

type passwordHashParams struct {
	memory      uint32
	iterations  uint32
	parallelism uint8
}

func parsePasswordHash(encodedHash string) (passwordHashParams, []byte, []byte, error) {
	var params passwordHashParams
	parts := strings.Split(encodedHash, "$")
	if len(parts) != 6 || parts[1] != "argon2id" || parts[2] != fmt.Sprintf("v=%d", argonVersion) {
		return params, nil, nil, errors.New("unsupported password hash")
	}
	for _, part := range strings.Split(parts[3], ",") {
		key, value, ok := strings.Cut(part, "=")
		if !ok {
			return params, nil, nil, errors.New("invalid password hash parameters")
		}
		parsed, err := strconv.ParseUint(value, 10, 32)
		if err != nil {
			return params, nil, nil, errors.New("invalid password hash parameter")
		}
		switch key {
		case "m":
			params.memory = uint32(parsed)
		case "t":
			params.iterations = uint32(parsed)
		case "p":
			if parsed > 255 {
				return params, nil, nil, errors.New("invalid password hash parallelism")
			}
			params.parallelism = uint8(parsed)
		}
	}
	if params.memory == 0 || params.iterations == 0 || params.parallelism == 0 {
		return params, nil, nil, errors.New("incomplete password hash parameters")
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return params, nil, nil, err
	}
	hash, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil {
		return params, nil, nil, err
	}
	if len(salt) == 0 || len(hash) == 0 {
		return params, nil, nil, errors.New("empty password hash material")
	}

	return params, salt, hash, nil
}
