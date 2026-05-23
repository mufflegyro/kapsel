package media

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"

	"kapsel/internal/assetpath"
)

const (
	queryExpires   = "expires"
	querySignature = "signature"
	// Media streams may run far longer than API responses, but should not stay open forever when a client stalls.
	mediaWriteTimeout = 6 * time.Hour
)

type Signer struct {
	secret []byte
}

func NewSigner(secret string) Signer {
	return Signer{secret: []byte(secret)}
}

func (s Signer) Query(mediaPath string, expires time.Time) url.Values {
	mediaPath, err := assetpath.Clean(mediaPath)
	if err != nil {
		return url.Values{}
	}

	expiresUnix := strconv.FormatInt(expires.Unix(), 10)
	values := url.Values{}
	values.Set(queryExpires, expiresUnix)
	values.Set(querySignature, s.sign(mediaPath, expiresUnix))

	return values
}

func (s Signer) Verify(mediaPath string, values url.Values, now time.Time) bool {
	mediaPath, err := assetpath.Clean(mediaPath)
	if err != nil {
		return false
	}

	expiresUnix := values.Get(queryExpires)
	signature := values.Get(querySignature)
	if expiresUnix == "" || signature == "" {
		return false
	}

	expires, err := strconv.ParseInt(expiresUnix, 10, 64)
	if err != nil || now.Unix() > expires {
		return false
	}

	expected := s.sign(mediaPath, expiresUnix)
	return hmac.Equal([]byte(signature), []byte(expected))
}

func (s Signer) sign(mediaPath string, expiresUnix string) string {
	mac := hmac.New(sha256.New, s.secret)
	mac.Write([]byte(mediaPath))
	mac.Write([]byte("\n"))
	mac.Write([]byte(expiresUnix))

	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

type Handler struct {
	signer Signer
	root   string
}

func NewHandler(root string, signer Signer) http.Handler {
	return Handler{
		signer: signer,
		root:   root,
	}
}

func (h Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	mediaPath, ok := requestPath(r)
	if !ok {
		http.NotFound(w, r)
		return
	}

	if !h.signer.Verify(mediaPath, r.URL.Query(), time.Now()) {
		http.Error(w, "unauthorized media URL", http.StatusUnauthorized)
		return
	}

	_, file, info, err := assetpath.Open(h.root, mediaPath)
	if err != nil || info.IsDir() || !info.Mode().IsRegular() {
		http.NotFound(w, r)
		return
	}
	defer file.Close()

	// Media responses can legitimately outlive the server's API write timeout.
	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(mediaWriteTimeout))
	w.Header().Set("Cache-Control", "private, max-age=86400")
	http.ServeContent(w, r, path.Base(mediaPath), info.ModTime(), file)
}

func requestPath(r *http.Request) (string, bool) {
	mediaPath := r.PathValue("path")
	if mediaPath == "" {
		mediaPath = strings.TrimPrefix(r.URL.Path, "/media/")
	}

	mediaPath, err := assetpath.Clean(mediaPath)
	if err != nil {
		return "", false
	}

	return mediaPath, true
}
