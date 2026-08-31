package download

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"kapsel/internal/jobs"
	"strings"
)

type Payload struct {
	URL      string `json:"url"`
	Origin   string `json:"origin,omitempty"`
	ScanOnly bool   `json:"scan_only,omitempty"`
}

func NormalizeDownloadPayload(payload Payload) (Payload, error) {
	url, err := NormalizeDirectVideoURL(payload.URL)
	if err != nil {
		return Payload{}, err
	}
	payload.URL = url
	if strings.TrimSpace(payload.Origin) == DownloadOriginChannelAuto {
		payload.Origin = DownloadOriginChannelAuto
	} else {
		payload.Origin = ""
	}

	return payload, nil
}

func EnqueueDownload(ctx context.Context, store *jobs.Store, payload Payload) (jobs.Job, error) {
	if store == nil {
		return jobs.Job{}, errors.New("download enqueue missing job store")
	}
	payload, payloadJSON, err := canonicalDownloadPayload(payload)
	if err != nil {
		return jobs.Job{}, err
	}
	job, _, err := store.FindOrEnqueue(ctx, jobs.EnqueueParams{Type: JobType, PayloadJSON: payloadJSON}, func(ctx context.Context, tx *sql.Tx) (jobs.Job, bool, error) {
		return activeDownloadJobForURL(ctx, store, tx, payload.URL, true)
	})

	return job, err
}

// EnqueueVideoMetadataScan enqueues a metadata-only job for a single video
// URL. The job fetches the video's catalog metadata (title, channel,
// thumbnail, duration) without downloading media, so a later playlist or
// channel import can link the catalog row. It is deduplicated per URL against
// queued/running metadata scans and full downloads.
func EnqueueVideoMetadataScan(ctx context.Context, store *jobs.Store, payload Payload) (jobs.Job, error) {
	if store == nil {
		return jobs.Job{}, errors.New("video metadata scan enqueue missing job store")
	}
	payload, payloadJSON, err := canonicalDownloadPayload(payload)
	if err != nil {
		return jobs.Job{}, err
	}
	job, _, err := store.FindOrEnqueue(ctx, jobs.EnqueueParams{Type: VideoMetadataScanJobType, PayloadJSON: payloadJSON}, func(ctx context.Context, tx *sql.Tx) (jobs.Job, bool, error) {
		return activeMetadataScanOrDownloadForURL(ctx, store, tx, payload.URL)
	})

	return job, err
}

func activeMetadataScanOrDownloadForURL(ctx context.Context, store *jobs.Store, tx *sql.Tx, url string) (jobs.Job, bool, error) {
	for _, jobType := range []string{VideoMetadataScanJobType, JobType} {
		if job, active, err := activeJobForURLType(ctx, store, tx, url, jobType); err != nil || active {
			return job, active, err
		}
	}

	return jobs.Job{}, false, nil
}

func activeJobForURLType(ctx context.Context, store *jobs.Store, tx *sql.Tx, url string, jobType string) (jobs.Job, bool, error) {
	if store == nil {
		return jobs.Job{}, false, nil
	}
	normalized, err := NormalizeDownloadURL(url)
	if err != nil {
		return jobs.Job{}, false, err
	}
	var activeJobs []jobs.Job
	if tx != nil {
		activeJobs, err = store.ActiveByTypeWithoutCancelRequestedTx(ctx, tx, jobType, jobs.MaxActiveLookupLimit)
	} else {
		activeJobs, err = store.ActiveByType(ctx, jobType, jobs.MaxActiveLookupLimit)
	}
	if err != nil {
		return jobs.Job{}, false, err
	}
	for _, job := range activeJobs {
		var existing Payload
		if err := json.Unmarshal([]byte(job.PayloadJSON), &existing); err != nil {
			continue
		}
		if existing.URL == normalized {
			return job, true, nil
		}
	}

	return jobs.Job{}, false, nil
}

func enqueueDownloadTx(ctx context.Context, store *jobs.Store, tx *sql.Tx, payload Payload, includeCancelRequested bool) (jobs.Job, bool, error) {
	if store == nil {
		return jobs.Job{}, false, errors.New("download enqueue missing job store")
	}
	payload, payloadJSON, err := canonicalDownloadPayload(payload)
	if err != nil {
		return jobs.Job{}, false, err
	}

	return store.FindOrEnqueueTx(ctx, tx, jobs.EnqueueParams{Type: JobType, PayloadJSON: payloadJSON}, func(ctx context.Context, tx *sql.Tx) (jobs.Job, bool, error) {
		return activeDownloadJobForURL(ctx, store, tx, payload.URL, includeCancelRequested)
	})
}

func canonicalDownloadPayload(payload Payload) (Payload, string, error) {
	payload, err := NormalizeDownloadPayload(payload)
	if err != nil {
		return Payload{}, "", err
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return Payload{}, "", err
	}

	return payload, string(body), nil
}

func ActiveJobForPayload(ctx context.Context, store *jobs.Store, payloadJSON string) (jobs.Job, bool, error) {
	if store == nil {
		return jobs.Job{}, false, nil
	}
	var target Payload
	if err := json.Unmarshal([]byte(payloadJSON), &target); err != nil {
		return jobs.Job{}, false, err
	}
	targetURL, err := NormalizeDownloadURL(target.URL)
	if err != nil {
		return jobs.Job{}, false, err
	}

	return activeDownloadJobForURL(ctx, store, nil, targetURL, true)
}

func activeDownloadJobForURL(ctx context.Context, store *jobs.Store, tx *sql.Tx, targetURL string, includeCancelRequested bool) (jobs.Job, bool, error) {
	if store == nil {
		return jobs.Job{}, false, nil
	}
	targetURL, err := NormalizeDownloadURL(targetURL)
	if err != nil {
		return jobs.Job{}, false, err
	}
	var activeJobs []jobs.Job
	if tx != nil {
		if includeCancelRequested {
			activeJobs, err = store.ActiveByTypeTx(ctx, tx, JobType, jobs.MaxActiveLookupLimit)
		} else {
			activeJobs, err = store.ActiveByTypeWithoutCancelRequestedTx(ctx, tx, JobType, jobs.MaxActiveLookupLimit)
		}
	} else {
		activeJobs, err = store.ActiveByType(ctx, JobType, jobs.MaxActiveLookupLimit)
	}
	if err != nil {
		return jobs.Job{}, false, err
	}
	for _, job := range activeJobs {
		var existing Payload
		if err := json.Unmarshal([]byte(job.PayloadJSON), &existing); err != nil {
			continue
		}
		existingURL, err := NormalizeDownloadURL(existing.URL)
		if err != nil {
			continue
		}
		if existingURL == targetURL {
			return job, true, nil
		}
	}

	return jobs.Job{}, false, nil
}

type ChannelScanPayload struct {
	URL       string `json:"url"`
	ChannelID string `json:"channel_id"`
}

func EnqueueChannelFirst(ctx context.Context, store *jobs.Store, payload Payload) (jobs.Job, error) {
	if store == nil {
		return jobs.Job{}, errors.New("channel enqueue missing job store")
	}
	payload.URL = strings.TrimSpace(payload.URL)
	channelURL, err := NormalizeChannelURL(payload.URL)
	if err != nil {
		return jobs.Job{}, err
	}
	payload.URL = channelURL
	payload.Origin = ""
	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		return jobs.Job{}, err
	}
	job, _, err := store.FindOrEnqueue(ctx, jobs.EnqueueParams{Type: ChannelJobType, PayloadJSON: string(payloadJSON)}, func(ctx context.Context, tx *sql.Tx) (jobs.Job, bool, error) {
		return store.ActiveByPayloadTx(ctx, tx, ChannelJobType, string(payloadJSON))
	})

	return job, err
}

func EnqueueChannelScan(ctx context.Context, store *jobs.Store, payload ChannelScanPayload) (jobs.Job, error) {
	if store == nil {
		return jobs.Job{}, errors.New("channel scan enqueue missing job store")
	}
	payload.ChannelID = strings.TrimSpace(payload.ChannelID)
	if payload.ChannelID == "" {
		return jobs.Job{}, errors.New("channel scan payload missing channel id")
	}
	channelURL, err := NormalizeChannelURL(payload.URL)
	if err != nil {
		return jobs.Job{}, err
	}
	payload.URL = channelURL
	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		return jobs.Job{}, err
	}
	job, _, err := store.FindOrEnqueue(ctx, jobs.EnqueueParams{Type: ChannelScanJobType, PayloadJSON: string(payloadJSON)}, func(ctx context.Context, tx *sql.Tx) (jobs.Job, bool, error) {
		return store.ActiveByPayloadTx(ctx, tx, ChannelScanJobType, string(payloadJSON))
	})

	return job, err
}

// PlaylistImportPayload is the payload for a playlist_import job: a YouTube
// playlist URL whose entries are fetched and imported into the archive.
type PlaylistImportPayload struct {
	URL string `json:"url"`
}

// playlistImportResult is stored on a completed playlist_import job and mirrors
// the CSV import report shape so the UI can summarize the outcome.
type playlistImportResult struct {
	PlaylistID string   `json:"playlist_id"`
	Title      string   `json:"title"`
	Linked     int      `json:"linked"`
	Missing    int      `json:"missing"`
	Enqueued   int      `json:"enqueued"`
	Skipped    int      `json:"skipped"`
	Errors     []string `json:"errors,omitempty"`
}

// EnqueuePlaylistImport enqueues a playlist_import job for a YouTube playlist
// URL. The URL is normalized first, and an active job for the same playlist is
// deduplicated so repeated submissions do not stack up.
func EnqueuePlaylistImport(ctx context.Context, store *jobs.Store, payload PlaylistImportPayload) (jobs.Job, error) {
	if store == nil {
		return jobs.Job{}, errors.New("playlist import enqueue missing job store")
	}
	playlistURL, _, err := NormalizePlaylistURL(payload.URL)
	if err != nil {
		return jobs.Job{}, err
	}
	payload.URL = playlistURL
	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		return jobs.Job{}, err
	}
	job, _, err := store.FindOrEnqueue(ctx, jobs.EnqueueParams{Type: PlaylistImportJobType, PayloadJSON: string(payloadJSON)}, func(ctx context.Context, tx *sql.Tx) (jobs.Job, bool, error) {
		return store.ActiveByPayloadTx(ctx, tx, PlaylistImportJobType, string(payloadJSON))
	})

	return job, err
}

type ChannelAutoDownloadPayload struct {
	URL       string `json:"url"`
	ChannelID string `json:"channel_id"`
}
