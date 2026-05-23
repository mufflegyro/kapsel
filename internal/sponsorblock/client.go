package sponsorblock

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

const (
	DefaultBaseURL  = "https://sponsor.ajay.app/api"
	defaultTimeout  = 5 * time.Second
	maxResponseSize = 1 << 20
)

type Segment struct {
	StartSeconds float64 `json:"start_seconds"`
	EndSeconds   float64 `json:"end_seconds"`
}

type Client struct {
	BaseURL    string
	HTTPClient *http.Client
}

func NewClient() *Client {
	return &Client{BaseURL: DefaultBaseURL, HTTPClient: &http.Client{Timeout: defaultTimeout}}
}

func (c *Client) SponsorSegments(ctx context.Context, externalID string) ([]Segment, error) {
	externalID = strings.TrimSpace(externalID)
	if externalID == "" {
		return nil, nil
	}
	endpoint, err := c.skipSegmentsURL(externalID)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")

	client := c.HTTPClient
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil, nil
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("sponsorblock status %d", resp.StatusCode)
	}

	var rows []apiSegment
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxResponseSize)).Decode(&rows); err != nil {
		return nil, err
	}
	return Normalize(apiSegments(rows).rawSegments()), nil
}

func (c *Client) skipSegmentsURL(externalID string) (string, error) {
	base := strings.TrimRight(c.BaseURL, "/")
	if base == "" {
		base = DefaultBaseURL
	}
	endpoint, err := url.Parse(base + "/skipSegments")
	if err != nil {
		return "", err
	}
	values := endpoint.Query()
	values.Set("videoID", externalID)
	values.Set("categories", `["sponsor"]`)
	endpoint.RawQuery = values.Encode()

	return endpoint.String(), nil
}

type apiSegment struct {
	Category   string     `json:"category"`
	ActionType string     `json:"actionType"`
	Segment    [2]float64 `json:"segment"`
}

type apiSegments []apiSegment

func (segments apiSegments) rawSegments() []Segment {
	items := make([]Segment, 0, len(segments))
	for _, row := range segments {
		if row.Category != "sponsor" || row.ActionType != "skip" {
			continue
		}
		items = append(items, Segment{StartSeconds: row.Segment[0], EndSeconds: row.Segment[1]})
	}

	return items
}

func Normalize(segments []Segment) []Segment {
	items := make([]Segment, 0, len(segments))
	for _, segment := range segments {
		if !finite(segment.StartSeconds) || !finite(segment.EndSeconds) || segment.StartSeconds < 0 || segment.EndSeconds <= segment.StartSeconds {
			continue
		}
		items = append(items, segment)
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].StartSeconds == items[j].StartSeconds {
			return items[i].EndSeconds < items[j].EndSeconds
		}
		return items[i].StartSeconds < items[j].StartSeconds
	})

	return items
}

func finite(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0)
}
