// Package subsimport imports a list of channels from a Google Takeout
// subscriptions.csv file by enqueueing Kapsel's channel-first download flow
// for each subscribed channel.
package subsimport

import (
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"kapsel/internal/download"
	"kapsel/internal/jobs"
)

// Entry is a single parsed subscription row.
type Entry struct {
	ChannelID   string
	ChannelURL  string
	ChannelName string
}

// Parse reads Google Takeout subscriptions.csv from r and returns the channel
// entries it contains. Rows without a usable channel URL are skipped. A
// UTF-8 BOM and surrounding whitespace are tolerated.
func Parse(r io.Reader) ([]Entry, error) {
	reader := csv.NewReader(r)
	// Tolerate rows with a different field count (e.g. trailing blank lines)
	// while still validating column names from the header.
	reader.FieldsPerRecord = -1
	entries := []Entry{}
	record, err := reader.Read()
	if err != nil {
		if errors.Is(err, io.EOF) {
			return entries, nil
		}
		return nil, fmt.Errorf("read subscriptions header: %w", err)
	}
	record = trimFields(record)
	header := headerIndex(record)

	// Some Takeout exports use "Channel Id,Channel Url,Channel Title".
	urlCol, hasURL := header["channel url"]
	idCol, hasID := header["channel id"]
	nameCol, _ := header["channel title"]
	if !hasURL && !hasID {
		return nil, errors.New("subscriptions.csv is missing a channel URL or channel ID column")
	}

	for {
		row, err := reader.Read()
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return nil, fmt.Errorf("read subscriptions row: %w", err)
		}
		row = trimFields(row)
		entry := Entry{}
		if idCol >= 0 && idCol < len(row) {
			entry.ChannelID = row[idCol]
		}
		if urlCol >= 0 && urlCol < len(row) {
			entry.ChannelURL = row[urlCol]
		}
		if nameCol >= 0 && nameCol < len(row) {
			entry.ChannelName = row[nameCol]
		}
		if strings.TrimSpace(entry.ChannelURL) == "" && strings.TrimSpace(entry.ChannelID) == "" {
			continue
		}
		entries = append(entries, entry)
	}

	return entries, nil
}

// EnqueueChannels normalizes and enqueues a channel-first download job for
// each entry, mirroring the manual add-channel flow so channels are marked
// subscribed for automatic downloads. scanOnly skips the first-video download,
// adding the channel and its catalog to the library without fetching media.
// It returns the number successfully enqueued and the error for the first
// invalid entry encountered.
func EnqueueChannels(ctx context.Context, store *jobs.Store, entries []Entry, scanOnly bool) (int, error) {
	if store == nil {
		return 0, errors.New("subscriptions import missing job store")
	}
	enqueued := 0
	for _, entry := range entries {
		rawURL := strings.TrimSpace(entry.ChannelURL)
		if rawURL == "" {
			// Fall back to building a URL from the channel id if that is all we have.
			if id := strings.TrimSpace(entry.ChannelID); id != "" {
				rawURL = "https://www.youtube.com/channel/" + id
			} else {
				continue
			}
		}
		normalized, err := download.NormalizeChannelURL(rawURL)
		if err != nil {
			// Skip malformed rows rather than failing the whole import.
			continue
		}
		if _, err := download.EnqueueChannelFirst(ctx, store, download.Payload{URL: normalized, ScanOnly: scanOnly}); err != nil {
			return enqueued, fmt.Errorf("enqueue channel %q: %w", normalized, err)
		}
		enqueued++
	}

	return enqueued, nil
}

func trimFields(fields []string) []string {
	out := make([]string, len(fields))
	for i, field := range fields {
		out[i] = strings.TrimSpace(strings.TrimPrefix(field, "\uFEFF"))
	}
	return out
}

func headerIndex(fields []string) map[string]int {
	index := map[string]int{}
	for i, field := range fields {
		key := strings.ToLower(field)
		if _, ok := index[key]; !ok {
			index[key] = i
		}
	}
	return index
}

// Report summarizes a subscriptions.csv import.
type Report struct {
	Parsed   int      `json:"parsed"`
	Enqueued int      `json:"enqueued"`
	Skipped  int      `json:"skipped"`
	Errors   []string `json:"errors,omitempty"`
}

// ImportFile parses the subscriptions.csv at path and enqueues a
// channel-first download job for each valid channel. When scanOnly is true the
// channel and its catalog are added without downloading media. It returns a
// report.
func ImportFile(ctx context.Context, store *jobs.Store, path string, scanOnly bool) (Report, error) {
	file, err := os.Open(path)
	if err != nil {
		return Report{}, err
	}
	defer file.Close()

	entries, err := Parse(file)
	if err != nil {
		return Report{}, err
	}
	report := Report{Parsed: len(entries)}
	enqueued, err := EnqueueChannels(ctx, store, entries, scanOnly)
	if err != nil {
		report.Skipped = len(entries) - enqueued
		report.Errors = append(report.Errors, err.Error())
		return report, nil
	}
	report.Enqueued = enqueued
	report.Skipped = len(entries) - enqueued

	return report, nil
}

