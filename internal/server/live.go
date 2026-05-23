package server

import (
	"bufio"
	"context"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"kapsel/internal/jobs"
)

const (
	webSocketGUID      = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"
	liveJobsInterval   = time.Second
	webSocketWriteWait = 5 * time.Second
	webSocketReadWait  = 60 * time.Second
	webSocketPingWait  = 30 * time.Second
	maxWebSocketInput  = 1024
	// Per-handler cap for live job streams. This fits a household deployment while bounding polling/database load.
	maxLiveWebSocketConnections = 32
)

type liveJobsMessage struct {
	Type       string        `json:"type"`
	Data       []jobResponse `json:"data,omitempty"`
	Pagination pagination    `json:"pagination,omitempty"`
	Error      string        `json:"error,omitempty"`
}

func liveJobs(store *jobs.Store) http.HandlerFunc {
	return liveJobsWithLimiter(store, newLiveWebSocketLimiter(maxLiveWebSocketConnections))
}

func liveJobsWithLimiter(store *jobs.Store, limiter *liveWebSocketLimiter) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if limiter != nil {
			if !limiter.acquire() {
				http.Error(w, "too many live websocket connections", http.StatusTooManyRequests)
				return
			}
			defer limiter.release()
		}

		conn, reader, ok := upgradeWebSocket(w, r)
		if !ok {
			return
		}
		defer conn.Close()
		writer := &webSocketWriter{conn: conn}

		done := make(chan struct{})
		go func() {
			defer close(done)
			_ = readWebSocketFrames(conn, reader, writer, webSocketReadWait)
		}()

		lastKey := ""
		if ok := writeLatestJobSnapshot(r.Context(), writer, store, &lastKey, true); !ok {
			return
		}

		ticker := time.NewTicker(liveJobsInterval)
		defer ticker.Stop()
		pingTicker := time.NewTicker(webSocketPingWait)
		defer pingTicker.Stop()
		warmupSnapshots := 3
		for {
			select {
			case <-r.Context().Done():
				return
			case <-done:
				return
			case <-pingTicker.C:
				if err := writer.writeFrame(0x9, nil); err != nil {
					return
				}
			case <-ticker.C:
				force := warmupSnapshots > 0
				if warmupSnapshots > 0 {
					warmupSnapshots--
				}
				if ok := writeLatestJobSnapshot(r.Context(), writer, store, &lastKey, force); !ok {
					return
				}
			}
		}
	}
}

type liveWebSocketLimiter struct {
	mu     sync.Mutex
	max    int
	active int
}

func newLiveWebSocketLimiter(max int) *liveWebSocketLimiter {
	return &liveWebSocketLimiter{max: max}
}

func (l *liveWebSocketLimiter) acquire() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.max <= 0 || l.active >= l.max {
		return false
	}
	l.active++

	return true
}

func (l *liveWebSocketLimiter) release() {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.active > 0 {
		l.active--
	}
}

func writeLatestJobSnapshot(ctx context.Context, writer *webSocketWriter, store *jobs.Store, lastKey *string, force bool) bool {
	message, err := recentJobsMessage(ctx, store)
	if err != nil {
		return false
	}
	key := liveJobsMessageKey(message)
	if !force && key == *lastKey {
		return true
	}
	*lastKey = key

	return writer.writeJSON(message) == nil
}

func recentJobsMessage(ctx context.Context, store *jobs.Store) (liveJobsMessage, error) {
	result, err := store.List(ctx, jobs.ListOptions{Page: 1, PageSize: jobs.MaxListPageSize})
	if err != nil {
		return liveJobsMessage{}, err
	}
	items := append([]jobs.ListItem{}, result.Jobs...)
	// Include active jobs outside the first page so active controls keep moving even when recent completed jobs push them down.
	active, err := store.List(ctx, jobs.ListOptions{Statuses: []jobs.Status{jobs.StatusQueued, jobs.StatusRunning}, Page: 1, PageSize: jobs.MaxListPageSize})
	if err != nil {
		return liveJobsMessage{}, err
	}
	seen := map[string]bool{}
	for _, item := range items {
		seen[item.ID] = true
	}
	for _, item := range active.Jobs {
		if seen[item.ID] {
			continue
		}
		seen[item.ID] = true
		items = append(items, item)
	}

	return liveJobsMessage{
		Type: "jobs_snapshot",
		Data: publicJobList(items),
		Pagination: pagination{
			Page:     result.Page,
			PageSize: result.PageSize,
			Total:    result.Total,
		},
	}, nil
}

func liveJobsMessageKey(message liveJobsMessage) string {
	var builder strings.Builder
	builder.WriteString(message.Type)
	builder.WriteString("|")
	builder.WriteString(message.Error)
	builder.WriteString("|")
	builder.WriteString(strconv.Itoa(message.Pagination.Total))
	for _, job := range message.Data {
		builder.WriteString("|")
		builder.WriteString(job.ID)
		builder.WriteString("|")
		builder.WriteString(string(job.Status))
		builder.WriteString("|")
		builder.WriteString(strconv.FormatFloat(job.Progress, 'f', 4, 64))
		builder.WriteString("|")
		builder.WriteString(job.Error)
		builder.WriteString("|")
		builder.WriteString(job.UpdatedAt)
		builder.WriteString("|")
		builder.WriteString(job.CompletedAt)
		builder.WriteString("|")
		builder.WriteString(job.ResultSummary)
		builder.WriteString("|")
		if job.CancelRequested {
			builder.WriteString("cancel")
		}
	}

	return builder.String()
}

func upgradeWebSocket(w http.ResponseWriter, r *http.Request) (net.Conn, *bufio.Reader, bool) {
	if !headerHasToken(r.Header, "Connection", "upgrade") || !strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
		http.Error(w, "websocket upgrade required", http.StatusBadRequest)
		return nil, nil, false
	}
	if r.Header.Get("Sec-WebSocket-Version") != "13" {
		http.Error(w, "unsupported websocket version", http.StatusBadRequest)
		return nil, nil, false
	}
	key := strings.TrimSpace(r.Header.Get("Sec-WebSocket-Key"))
	if key == "" {
		http.Error(w, "missing websocket key", http.StatusBadRequest)
		return nil, nil, false
	}
	if !validWebSocketOrigin(r) {
		http.Error(w, "websocket origin not allowed", http.StatusForbidden)
		return nil, nil, false
	}
	hijacker, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "websocket upgrade unavailable", http.StatusInternalServerError)
		return nil, nil, false
	}

	conn, rw, err := hijacker.Hijack()
	if err != nil {
		return nil, nil, false
	}
	response := "HTTP/1.1 101 Switching Protocols\r\n" +
		"Upgrade: websocket\r\n" +
		"Connection: Upgrade\r\n" +
		"Sec-WebSocket-Accept: " + webSocketAccept(key) + "\r\n\r\n"
	if _, err := rw.WriteString(response); err != nil {
		_ = conn.Close()
		return nil, nil, false
	}
	if err := rw.Flush(); err != nil {
		_ = conn.Close()
		return nil, nil, false
	}

	return conn, rw.Reader, true
}

func validWebSocketOrigin(r *http.Request) bool {
	origins := r.Header.Values("Origin")
	if len(origins) != 1 {
		return false
	}
	origin := strings.TrimSpace(origins[0])
	if origin == "" {
		return false
	}
	parsed, err := url.Parse(origin)
	if err != nil || parsed.Host == "" || parsed.User != nil || parsed.Opaque != "" ||
		parsed.Path != "" || parsed.RawPath != "" || parsed.ForceQuery || parsed.RawQuery != "" ||
		parsed.Fragment != "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return false
	}

	return strings.EqualFold(parsed.Host, r.Host)
}

func headerHasToken(header http.Header, key string, token string) bool {
	for _, value := range header.Values(key) {
		for _, part := range strings.Split(value, ",") {
			if strings.EqualFold(strings.TrimSpace(part), token) {
				return true
			}
		}
	}

	return false
}

func webSocketAccept(key string) string {
	sum := sha1.Sum([]byte(key + webSocketGUID))

	return base64.StdEncoding.EncodeToString(sum[:])
}

type webSocketWriter struct {
	conn net.Conn
	mu   sync.Mutex
}

func (w *webSocketWriter) writeJSON(value any) error {
	body, err := json.Marshal(value)
	if err != nil {
		return err
	}

	return w.writeFrame(0x1, body)
}

func (w *webSocketWriter) writeFrame(opcode byte, body []byte) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	_ = w.conn.SetWriteDeadline(time.Now().Add(webSocketWriteWait))

	header := []byte{0x80 | opcode, 0}
	length := len(body)
	switch {
	case length <= 125:
		header[1] = byte(length)
	case length <= 65535:
		header[1] = 126
		header = append(header, byte(length>>8), byte(length))
	default:
		header[1] = 127
		var extended [8]byte
		binary.BigEndian.PutUint64(extended[:], uint64(length))
		header = append(header, extended[:]...)
	}
	if _, err := w.conn.Write(header); err != nil {
		return err
	}
	_, err := w.conn.Write(body)

	return err
}

func readWebSocketFrames(conn net.Conn, reader *bufio.Reader, writer *webSocketWriter, readWait time.Duration) error {
	for {
		if readWait > 0 {
			_ = conn.SetReadDeadline(time.Now().Add(readWait))
		}
		opcode, length, masked, err := readWebSocketFrameHeader(reader)
		if err != nil {
			return err
		}
		if !masked {
			return errors.New("websocket client frame must be masked")
		}
		if length > maxWebSocketInput {
			return fmt.Errorf("websocket input frame too large")
		}
		var mask [4]byte
		if masked {
			if _, err := io.ReadFull(reader, mask[:]); err != nil {
				return err
			}
		}
		payload := make([]byte, length)
		if length > 0 {
			if _, err := io.ReadFull(reader, payload); err != nil {
				return err
			}
			if masked {
				for index := range payload {
					payload[index] ^= mask[index%len(mask)]
				}
			}
		}
		if opcode == 0x8 {
			return nil
		}
		if opcode == 0x9 && writer != nil {
			if err := writer.writeFrame(0xA, payload); err != nil {
				return err
			}
		}
	}
}

func readWebSocketFrameHeader(reader *bufio.Reader) (byte, int64, bool, error) {
	first, err := reader.ReadByte()
	if err != nil {
		return 0, 0, false, err
	}
	second, err := reader.ReadByte()
	if err != nil {
		return 0, 0, false, err
	}
	opcode := first & 0x0f
	masked := second&0x80 != 0
	length := int64(second & 0x7f)
	switch length {
	case 126:
		var extended [2]byte
		if _, err := io.ReadFull(reader, extended[:]); err != nil {
			return 0, 0, false, err
		}
		length = int64(binary.BigEndian.Uint16(extended[:]))
	case 127:
		var extended [8]byte
		if _, err := io.ReadFull(reader, extended[:]); err != nil {
			return 0, 0, false, err
		}
		value := binary.BigEndian.Uint64(extended[:])
		if value > maxWebSocketInput {
			return 0, 0, false, errors.New("websocket input frame too large")
		}
		length = int64(value)
	}

	return opcode, length, masked, nil
}
