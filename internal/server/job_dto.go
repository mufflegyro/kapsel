package server

import "kapsel/internal/jobs"

type jobResponse struct {
	ID              string      `json:"id"`
	Type            string      `json:"type"`
	Status          jobs.Status `json:"status"`
	Priority        int         `json:"priority"`
	Attempts        int         `json:"attempts"`
	MaxAttempts     int         `json:"max_attempts"`
	Progress        float64     `json:"progress"`
	Error           string      `json:"error"`
	RunAfter        string      `json:"run_after"`
	LockedAt        string      `json:"locked_at,omitempty"`
	CancelRequested bool        `json:"cancel_requested"`
	CreatedAt       string      `json:"created_at"`
	UpdatedAt       string      `json:"updated_at"`
	CompletedAt     string      `json:"completed_at,omitempty"`
	ResultSummary   string      `json:"result_summary,omitempty"`
}

func publicJobResponse(job jobs.Job) jobResponse {
	return jobResponse{
		ID:              job.ID,
		Type:            job.Type,
		Status:          job.Status,
		Priority:        job.Priority,
		Attempts:        job.Attempts,
		MaxAttempts:     job.MaxAttempts,
		Progress:        job.Progress,
		Error:           job.Error,
		RunAfter:        job.RunAfter,
		LockedAt:        job.LockedAt,
		CancelRequested: job.CancelRequested,
		CreatedAt:       job.CreatedAt,
		UpdatedAt:       job.UpdatedAt,
		CompletedAt:     job.CompletedAt,
		ResultSummary:   job.ResultSummary,
	}
}

func publicJobListItem(item jobs.ListItem) jobResponse {
	return jobResponse{
		ID:              item.ID,
		Type:            item.Type,
		Status:          item.Status,
		Priority:        item.Priority,
		Attempts:        item.Attempts,
		MaxAttempts:     item.MaxAttempts,
		Progress:        item.Progress,
		Error:           item.Error,
		RunAfter:        item.RunAfter,
		LockedAt:        item.LockedAt,
		CancelRequested: item.CancelRequested,
		CreatedAt:       item.CreatedAt,
		UpdatedAt:       item.UpdatedAt,
		CompletedAt:     item.CompletedAt,
		ResultSummary:   item.ResultSummary,
	}
}

func publicJobList(items []jobs.ListItem) []jobResponse {
	if items == nil {
		return nil
	}
	responses := make([]jobResponse, len(items))
	for index, item := range items {
		responses[index] = publicJobListItem(item)
	}

	return responses
}
