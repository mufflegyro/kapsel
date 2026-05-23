package sponsorblock

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestClientFetchesOnlySponsorSkipSegments(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/skipSegments" {
			t.Fatalf("unexpected path %q", r.URL.Path)
		}
		if got := r.URL.Query().Get("videoID"); got != "vid-1" {
			t.Fatalf("unexpected videoID %q", got)
		}
		if got := r.URL.Query().Get("categories"); got != `["sponsor"]` {
			t.Fatalf("expected sponsor-only categories query, got %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[
			{"category":"sponsor","actionType":"skip","segment":[10.5,15]},
			{"category":"intro","actionType":"skip","segment":[0,3]},
			{"category":"sponsor","actionType":"mute","segment":[20,22]}
		]`))
	}))
	t.Cleanup(server.Close)

	client := &Client{BaseURL: server.URL + "/api", HTTPClient: server.Client()}
	segments, err := client.SponsorSegments(context.Background(), "vid-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(segments) != 1 || segments[0].StartSeconds != 10.5 || segments[0].EndSeconds != 15 {
		t.Fatalf("expected only sponsor skip segment, got %#v", segments)
	}
}

func TestClientTreatsMissingSegmentsAsEmpty(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.NotFoundHandler())
	t.Cleanup(server.Close)

	client := &Client{BaseURL: server.URL, HTTPClient: server.Client()}
	segments, err := client.SponsorSegments(context.Background(), "vid-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(segments) != 0 {
		t.Fatalf("expected missing SponsorBlock segments to be empty, got %#v", segments)
	}
}
