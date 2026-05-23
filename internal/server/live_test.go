package server

import (
	"bufio"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"kapsel/internal/jobs"
)

func TestLiveJobsWebSocketStreamsSnapshots(t *testing.T) {
	db := openServerTestDB(t)
	store := jobs.NewStore(db)
	job, err := store.Enqueue(t.Context(), jobs.EnqueueParams{Type: "download", PayloadJSON: `{"secret":"top-secret"}`})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(NewHandler(WithJobs(store)))
	t.Cleanup(server.Close)
	conn, reader := openTestWebSocket(t, server.URL, "/api/live")
	t.Cleanup(func() { _ = conn.Close() })

	initial, initialBody := readLiveJobsFrame(t, conn, reader)
	assertPublicJobBody(t, initialBody)
	assertLiveJobStatus(t, initial, job.ID, jobs.StatusQueued)
	claimed, ok, err := store.Claim(t.Context(), time.Now(), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if !ok || claimed.ID != job.ID {
		t.Fatalf("expected to claim job %s, ok=%v claimed=%#v", job.ID, ok, claimed)
	}

	running := readLiveJobsMessageUntil(t, conn, reader, func(message liveJobsMessage) bool {
		return liveMessageHasJobStatus(message, job.ID, jobs.StatusRunning)
	})
	assertLiveJobStatus(t, running, job.ID, jobs.StatusRunning)
}

func TestLiveJobsWebSocketIncludesActiveJobsOutsideRecentPage(t *testing.T) {
	db := openServerTestDB(t)
	store := jobs.NewStore(db)
	active, err := store.Enqueue(t.Context(), jobs.EnqueueParams{ID: "active-old", Type: "download"})
	if err != nil {
		t.Fatal(err)
	}
	claimed, ok, err := store.Claim(t.Context(), time.Now(), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if !ok || claimed.ID != active.ID {
		t.Fatalf("expected active job claim, ok=%v claimed=%#v", ok, claimed)
	}
	if _, err := db.Exec("UPDATE jobs SET updated_at = ?, locked_at = ? WHERE id = ?", "2026-05-01T00:00:00Z", "2026-05-01T00:00:00Z", active.ID); err != nil {
		t.Fatal(err)
	}
	for index := range jobs.MaxListPageSize {
		id := fmt.Sprintf("recent-%02d", index)
		if _, err := store.Enqueue(t.Context(), jobs.EnqueueParams{ID: id, Type: "download"}); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec("UPDATE jobs SET status = ?, updated_at = ? WHERE id = ?", jobs.StatusSucceeded, fmt.Sprintf("2026-05-04T12:%02d:00Z", index), id); err != nil {
			t.Fatal(err)
		}
	}

	message, err := recentJobsMessage(t.Context(), store)
	if err != nil {
		t.Fatal(err)
	}

	assertLiveJobStatus(t, message, active.ID, jobs.StatusRunning)
}

func TestLiveJobsWebSocketRequiresAuth(t *testing.T) {
	store := jobs.NewStore(openServerTestDB(t))
	manager := newServerAuthManager(t, time.Now())
	req := httptest.NewRequest(http.MethodGet, "/api/live", nil)
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Upgrade", "websocket")
	req.Header.Set("Sec-WebSocket-Version", "13")
	req.Header.Set("Sec-WebSocket-Key", base64.StdEncoding.EncodeToString([]byte("kapsel-live-test!")))
	rec := httptest.NewRecorder()

	NewHandler(WithJobs(store), WithAuth(manager)).ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected status %d, got %d body=%s", http.StatusUnauthorized, rec.Code, rec.Body.String())
	}
}

func TestLiveJobsWebSocketRejectsCrossOrigin(t *testing.T) {
	store := jobs.NewStore(openServerTestDB(t))
	req := httptest.NewRequest(http.MethodGet, "/api/live", nil)
	req.Host = "kapsel.local"
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Upgrade", "websocket")
	req.Header.Set("Sec-WebSocket-Version", "13")
	req.Header.Set("Sec-WebSocket-Key", base64.StdEncoding.EncodeToString([]byte("kapsel-live-test!")))
	req.Header.Set("Origin", "https://evil.example")
	rec := httptest.NewRecorder()

	NewHandler(WithJobs(store)).ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected status %d, got %d body=%s", http.StatusForbidden, rec.Code, rec.Body.String())
	}
}

func TestLiveJobsWebSocketRejectsMissingOrigin(t *testing.T) {
	store := jobs.NewStore(openServerTestDB(t))
	req := httptest.NewRequest(http.MethodGet, "/api/live", nil)
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Upgrade", "websocket")
	req.Header.Set("Sec-WebSocket-Version", "13")
	req.Header.Set("Sec-WebSocket-Key", base64.StdEncoding.EncodeToString([]byte("kapsel-live-test!")))
	rec := httptest.NewRecorder()

	NewHandler(WithJobs(store)).ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected status %d, got %d body=%s", http.StatusForbidden, rec.Code, rec.Body.String())
	}
}

func TestValidWebSocketOrigin(t *testing.T) {
	for _, test := range []struct {
		name    string
		host    string
		origins []string
		want    bool
	}{
		{name: "same origin", host: "kapsel.local", origins: []string{"https://kapsel.local"}, want: true},
		{name: "cross origin", host: "kapsel.local", origins: []string{"https://evil.example"}, want: false},
		{name: "malformed origin", host: "kapsel.local", origins: []string{"://bad-origin"}, want: false},
		{name: "missing origin", host: "kapsel.local", want: false},
		{name: "duplicate origins", host: "kapsel.local", origins: []string{"https://kapsel.local", "https://evil.example"}, want: false},
		{name: "origin with user info", host: "kapsel.local", origins: []string{"https://user@kapsel.local"}, want: false},
		{name: "origin with path", host: "kapsel.local", origins: []string{"https://kapsel.local/path"}, want: false},
		{name: "origin with query", host: "kapsel.local", origins: []string{"https://kapsel.local?x=1"}, want: false},
		{name: "origin with fragment", host: "kapsel.local", origins: []string{"https://kapsel.local#section"}, want: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/api/live", nil)
			req.Host = test.host
			for _, origin := range test.origins {
				req.Header.Add("Origin", origin)
			}

			if got := validWebSocketOrigin(req); got != test.want {
				t.Fatalf("expected validWebSocketOrigin=%v, got %v", test.want, got)
			}
		})
	}
}

func TestLiveJobsWebSocketRespondsToPing(t *testing.T) {
	store := jobs.NewStore(openServerTestDB(t))
	server := httptest.NewServer(NewHandler(WithJobs(store)))
	t.Cleanup(server.Close)
	conn, reader := openTestWebSocket(t, server.URL, "/api/live")
	t.Cleanup(func() { _ = conn.Close() })
	_ = readLiveJobsMessage(t, conn, reader)

	writeMaskedTestFrame(t, conn, 0x9, []byte("hi"))
	opcode, payload := readTestWebSocketFrame(t, conn, reader)
	if opcode != 0xA || string(payload) != "hi" {
		t.Fatalf("expected pong payload %q, got opcode=%d payload=%q", "hi", opcode, string(payload))
	}
}

func TestLiveJobsWebSocketRejectsUnmaskedClientFrames(t *testing.T) {
	store := jobs.NewStore(openServerTestDB(t))
	server := httptest.NewServer(NewHandler(WithJobs(store)))
	t.Cleanup(server.Close)
	conn, reader := openTestWebSocket(t, server.URL, "/api/live")
	t.Cleanup(func() { _ = conn.Close() })
	_ = readLiveJobsMessage(t, conn, reader)

	writeUnmaskedTestFrame(t, conn, 0x9, []byte("hi"))
	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	_, _, _, err := readWebSocketFrameHeader(reader)
	if err == nil {
		t.Fatal("expected websocket connection to close after an unmasked client frame")
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		t.Fatal("timed out waiting for websocket connection to close after an unmasked client frame")
	}
}

func TestLiveJobsWebSocketLimitsConnections(t *testing.T) {
	store := jobs.NewStore(openServerTestDB(t))
	server := httptest.NewServer(liveJobsWithLimiter(store, newLiveWebSocketLimiter(1)))
	t.Cleanup(server.Close)
	conn, _ := openTestWebSocket(t, server.URL, "/api/live")
	t.Cleanup(func() { _ = conn.Close() })

	_, _, status := attemptTestWebSocket(t, server.URL, "/api/live")
	if status != http.StatusTooManyRequests {
		t.Fatalf("expected excess websocket connection status %d, got %d", http.StatusTooManyRequests, status)
	}
}

func TestLiveJobsWebSocketLimitCleansUpAfterDisconnect(t *testing.T) {
	store := jobs.NewStore(openServerTestDB(t))
	server := httptest.NewServer(liveJobsWithLimiter(store, newLiveWebSocketLimiter(1)))
	t.Cleanup(server.Close)
	conn, _ := openTestWebSocket(t, server.URL, "/api/live")
	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		conn, _, status := attemptTestWebSocket(t, server.URL, "/api/live")
		if status == http.StatusSwitchingProtocols {
			_ = conn.Close()
			return
		}
		if status != http.StatusTooManyRequests {
			t.Fatalf("expected cleanup retry to see status %d or %d, got %d", http.StatusSwitchingProtocols, http.StatusTooManyRequests, status)
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("timed out waiting for websocket connection limit cleanup")
}

func TestReadWebSocketFramesTimesOutIdleClients(t *testing.T) {
	serverConn, clientConn := net.Pipe()
	t.Cleanup(func() { _ = serverConn.Close() })
	t.Cleanup(func() { _ = clientConn.Close() })
	errCh := make(chan error, 1)
	go func() {
		errCh <- readWebSocketFrames(serverConn, bufio.NewReader(serverConn), nil, 20*time.Millisecond)
	}()

	select {
	case err := <-errCh:
		if err == nil {
			t.Fatal("expected idle websocket reader to time out")
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for idle websocket reader to return")
	}
}

func openTestWebSocket(t *testing.T, serverURL string, requestPath string) (net.Conn, *bufio.Reader) {
	t.Helper()
	conn, reader, status := attemptTestWebSocket(t, serverURL, requestPath)
	if status != http.StatusSwitchingProtocols {
		if conn != nil {
			_ = conn.Close()
		}
		t.Fatalf("expected websocket upgrade, got status %d", status)
	}

	return conn, reader
}

func attemptTestWebSocket(t *testing.T, serverURL string, requestPath string) (net.Conn, *bufio.Reader, int) {
	t.Helper()
	addr := strings.TrimPrefix(serverURL, "http://")
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	key := base64.StdEncoding.EncodeToString([]byte("kapsel-live-test!"))
	request := fmt.Sprintf("GET %s HTTP/1.1\r\nHost: %s\r\nConnection: Upgrade\r\nUpgrade: websocket\r\nSec-WebSocket-Version: 13\r\nSec-WebSocket-Key: %s\r\nOrigin: %s\r\n\r\n", requestPath, addr, key, serverURL)
	if _, err := conn.Write([]byte(request)); err != nil {
		_ = conn.Close()
		t.Fatal(err)
	}
	reader := bufio.NewReader(conn)
	response, err := http.ReadResponse(reader, nil)
	if err != nil {
		_ = conn.Close()
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusSwitchingProtocols {
		_ = conn.Close()
		return nil, nil, response.StatusCode
	}
	if got := response.Header.Get("Sec-WebSocket-Accept"); got != webSocketAccept(key) {
		_ = conn.Close()
		t.Fatalf("unexpected websocket accept header %q", got)
	}

	return conn, reader, response.StatusCode
}

func readLiveJobsMessageUntil(t *testing.T, conn net.Conn, reader *bufio.Reader, matches func(liveJobsMessage) bool) liveJobsMessage {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		message := readLiveJobsMessage(t, conn, reader)
		if matches(message) {
			return message
		}
	}

	t.Fatal("timed out waiting for live jobs message")
	return liveJobsMessage{}
}

func readLiveJobsMessage(t *testing.T, conn net.Conn, reader *bufio.Reader) liveJobsMessage {
	t.Helper()
	message, _ := readLiveJobsFrame(t, conn, reader)
	return message
}

func readLiveJobsFrame(t *testing.T, conn net.Conn, reader *bufio.Reader) (liveJobsMessage, string) {
	t.Helper()
	opcode, body := readTestWebSocketFrame(t, conn, reader)
	if opcode != 0x1 {
		t.Fatalf("expected text websocket frame, got opcode %d", opcode)
	}
	var message liveJobsMessage
	if err := json.Unmarshal(body, &message); err != nil {
		t.Fatalf("failed to decode live jobs message %q: %v", string(body), err)
	}

	return message, string(body)
}

func readTestWebSocketFrame(t *testing.T, conn net.Conn, reader *bufio.Reader) (byte, []byte) {
	t.Helper()
	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	opcode, length, _, err := readWebSocketFrameHeader(reader)
	if err != nil {
		t.Fatal(err)
	}
	body := make([]byte, length)
	if _, err := io.ReadFull(reader, body); err != nil {
		t.Fatal(err)
	}

	return opcode, body
}

func writeMaskedTestFrame(t *testing.T, conn net.Conn, opcode byte, payload []byte) {
	t.Helper()
	mask := [4]byte{1, 2, 3, 4}
	header := []byte{0x80 | opcode, 0x80}
	length := len(payload)
	switch {
	case length <= 125:
		header[1] |= byte(length)
	case length <= 65535:
		header[1] |= 126
		header = append(header, byte(length>>8), byte(length))
	default:
		header[1] |= 127
		var extended [8]byte
		binary.BigEndian.PutUint64(extended[:], uint64(length))
		header = append(header, extended[:]...)
	}
	masked := append([]byte{}, payload...)
	for index := range masked {
		masked[index] ^= mask[index%len(mask)]
	}
	frame := append(header, mask[:]...)
	frame = append(frame, masked...)
	if _, err := conn.Write(frame); err != nil {
		t.Fatal(err)
	}
}

func writeUnmaskedTestFrame(t *testing.T, conn net.Conn, opcode byte, payload []byte) {
	t.Helper()
	header := []byte{0x80 | opcode, 0}
	length := len(payload)
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
	frame := append(header, payload...)
	if _, err := conn.Write(frame); err != nil {
		t.Fatal(err)
	}
}

func assertLiveJobStatus(t *testing.T, message liveJobsMessage, jobID string, status jobs.Status) {
	t.Helper()
	if !liveMessageHasJobStatus(message, jobID, status) {
		t.Fatalf("expected live message to include %s status %s, got %#v", jobID, status, message)
	}
}

func liveMessageHasJobStatus(message liveJobsMessage, jobID string, status jobs.Status) bool {
	for _, item := range message.Data {
		if item.ID == jobID && item.Status == status {
			return true
		}
	}

	return false
}
