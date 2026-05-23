package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"kapsel/internal/download"
	"kapsel/internal/jobs"
)

func getJob(store *jobs.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		job, err := store.Get(r.Context(), r.PathValue("id"))
		if errors.Is(err, jobs.ErrNotFound) {
			http.NotFound(w, r)
			return
		}
		if err != nil {
			http.Error(w, "failed to load job", http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(publicJobResponse(job))
	}
}

func cancelJob(store *jobs.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !requireNoBody(w, r) {
			return
		}
		writeJobActionResponse(w, r, store, store.Cancel(r.Context(), r.PathValue("id")))
	}
}

func retryJob(store *jobs.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !requireNoBody(w, r) {
			return
		}
		writeJobActionResponse(w, r, store, store.Retry(r.Context(), r.PathValue("id")))
	}
}

func writeJobActionResponse(w http.ResponseWriter, r *http.Request, store *jobs.Store, err error) {
	if errors.Is(err, jobs.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if errors.Is(err, jobs.ErrInvalidTransition) || errors.Is(err, jobs.ErrUnsafeRetry) {
		writeJSONError(w, err.Error(), http.StatusConflict)
		return
	}
	if err != nil {
		http.Error(w, "failed to update job", http.StatusInternalServerError)
		return
	}
	job, err := store.Get(r.Context(), r.PathValue("id"))
	if errors.Is(err, jobs.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		http.Error(w, "failed to load job", http.StatusInternalServerError)
		return
	}

	writeJSON(w, http.StatusOK, publicJobResponse(job))
}

type jobListResponse struct {
	Data       []jobResponse `json:"data"`
	Pagination pagination    `json:"pagination"`
}

type diagnosticErrorsResponse struct {
	Limit int               `json:"limit"`
	Data  []diagnosticError `json:"data"`
}

type diagnosticError struct {
	JobID     string `json:"job_id"`
	Type      string `json:"type"`
	Status    string `json:"status"`
	Error     string `json:"error"`
	UpdatedAt string `json:"updated_at"`
}

func listJobs(store *jobs.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		page := boundedInt(r.URL.Query().Get("page"), 1, 1, 1000000)
		pageSize := boundedInt(r.URL.Query().Get("page_size"), jobs.DefaultListPageSize, 1, jobs.MaxListPageSize)
		statuses, err := jobStatusFilters(r)
		if err != nil {
			writeJSONError(w, err.Error(), http.StatusBadRequest)
			return
		}
		result, err := store.List(r.Context(), jobs.ListOptions{Statuses: statuses, Page: page, PageSize: pageSize})
		if err != nil {
			http.Error(w, "failed to list jobs", http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(jobListResponse{
			Data: publicJobList(result.Jobs),
			Pagination: pagination{
				Page:     result.Page,
				PageSize: result.PageSize,
				Total:    result.Total,
			},
		})
	}
}

func diagnosticErrors(store *jobs.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		requestedLimit := boundedInt(r.URL.Query().Get("limit"), jobs.DefaultDiagnosticLimit, 1, jobs.MaxDiagnosticLimit)
		items, limit, err := store.ListFailedDiagnostics(r.Context(), requestedLimit)
		if err != nil {
			http.Error(w, "failed to list diagnostic errors", http.StatusInternalServerError)
			return
		}
		response := diagnosticErrorsResponse{Limit: limit, Data: []diagnosticError{}}
		for _, job := range items {
			response.Data = append(response.Data, diagnosticError{
				JobID:     job.ID,
				Type:      job.Type,
				Status:    string(job.Status),
				Error:     download.SanitizeDiagnosticText(job.Error),
				UpdatedAt: job.UpdatedAt,
			})
		}

		writeJSON(w, http.StatusOK, response)
	}
}

func jobStatusFilters(r *http.Request) ([]jobs.Status, error) {
	var statuses []jobs.Status
	seen := map[jobs.Status]bool{}
	for _, raw := range r.URL.Query()["status"] {
		for _, part := range strings.Split(raw, ",") {
			part = strings.TrimSpace(part)
			if part == "" {
				continue
			}
			status := jobs.Status(part)
			if !validJobStatus(status) {
				return nil, fmt.Errorf("invalid job status %q", part)
			}
			if seen[status] {
				continue
			}
			seen[status] = true
			statuses = append(statuses, status)
		}
	}

	return statuses, nil
}

func validJobStatus(status jobs.Status) bool {
	switch status {
	case jobs.StatusQueued, jobs.StatusRunning, jobs.StatusSucceeded, jobs.StatusFailed, jobs.StatusCancelled:
		return true
	default:
		return false
	}
}
