package jobs

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
)

type Status string

const (
	StatusQueued    Status = "queued"
	StatusRunning   Status = "running"
	StatusSucceeded Status = "succeeded"
	StatusFailed    Status = "failed"
	StatusCancelled Status = "cancelled"
)

var ErrNotFound = errors.New("job not found")
var ErrInvalidTransition = errors.New("invalid job state transition")
var ErrUnsafeRetry = errors.New("job retry is unsafe after recorded result")

const (
	DefaultListPageSize      = 20
	MaxListPageSize          = 50
	MaxActiveLookupLimit     = 1000
	DefaultDiagnosticLimit   = 20
	MaxDiagnosticLimit       = 50
	MaxDiagnosticErrorLength = 1200
	maxResultSummaryLength   = 512
	staleMaxAttemptsError    = "stale job exceeded max attempts"
)

type Job struct {
	ID          string `json:"id"`
	Type        string `json:"type"`
	PayloadJSON string `json:"payload_json"`
	// ResultJSON is the user-facing final result when ResultCommitted is true.
	// When ResultCommitted is false, it may hold a partial diagnostic report for
	// failed/cancelled jobs but must not be used as proof that durable work finished.
	ResultJSON      string  `json:"result_json"`
	ResultCommitted bool    `json:"-"`
	ResultSummary   string  `json:"result_summary,omitempty"`
	Status          Status  `json:"status"`
	Priority        int     `json:"priority"`
	Attempts        int     `json:"attempts"`
	MaxAttempts     int     `json:"max_attempts"`
	Progress        float64 `json:"progress"`
	Error           string  `json:"error"`
	RunAfter        string  `json:"run_after"`
	LockedAt        string  `json:"locked_at,omitempty"`
	CancelRequested bool    `json:"cancel_requested"`
	CreatedAt       string  `json:"created_at"`
	UpdatedAt       string  `json:"updated_at"`
	CompletedAt     string  `json:"completed_at,omitempty"`
}

type EnqueueParams struct {
	ID          string
	Type        string
	PayloadJSON string
	Priority    int
	MaxAttempts int
	RunAfter    time.Time
}

type ActiveJobFinder func(context.Context, *sql.Tx) (Job, bool, error)

type ListOptions struct {
	Statuses []Status
	Page     int
	PageSize int
}

type ListResult struct {
	Jobs     []ListItem
	Total    int
	Page     int
	PageSize int
}

type ListItem struct {
	ID              string  `json:"id"`
	Type            string  `json:"type"`
	Status          Status  `json:"status"`
	Priority        int     `json:"priority"`
	Attempts        int     `json:"attempts"`
	MaxAttempts     int     `json:"max_attempts"`
	Progress        float64 `json:"progress"`
	Error           string  `json:"error"`
	RunAfter        string  `json:"run_after"`
	LockedAt        string  `json:"locked_at,omitempty"`
	CancelRequested bool    `json:"cancel_requested"`
	CreatedAt       string  `json:"created_at"`
	UpdatedAt       string  `json:"updated_at"`
	CompletedAt     string  `json:"completed_at,omitempty"`
	ResultSummary   string  `json:"result_summary,omitempty"`
}

type DiagnosticError struct {
	ID        string `json:"id"`
	Type      string `json:"type"`
	Status    Status `json:"status"`
	Error     string `json:"error"`
	UpdatedAt string `json:"updated_at"`
}

type Store struct {
	db *sql.DB
}

type jobExecutor interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func NewStore(db *sql.DB) *Store {
	return &Store{db: db}
}

func (s *Store) Enqueue(ctx context.Context, params EnqueueParams) (Job, error) {
	return enqueue(ctx, s.db, params)
}

func (s *Store) EnqueueTx(ctx context.Context, tx *sql.Tx, params EnqueueParams) (Job, error) {
	return enqueue(ctx, tx, params)
}

func (s *Store) FindOrEnqueue(ctx context.Context, params EnqueueParams, findActive ActiveJobFinder) (Job, bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Job{}, false, err
	}
	defer tx.Rollback()

	job, created, err := s.FindOrEnqueueTx(ctx, tx, params, findActive)
	if err != nil {
		return Job{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return Job{}, false, err
	}

	return job, created, nil
}

func (s *Store) FindOrEnqueueTx(ctx context.Context, tx *sql.Tx, params EnqueueParams, findActive ActiveJobFinder) (Job, bool, error) {
	if findActive != nil {
		job, ok, err := findActive(ctx, tx)
		if err != nil || ok {
			return job, false, err
		}
	}

	job, err := s.EnqueueTx(ctx, tx, params)
	if err != nil {
		return Job{}, false, err
	}

	return job, true, nil
}

func enqueue(ctx context.Context, exec jobExecutor, params EnqueueParams) (Job, error) {
	now := time.Now().UTC()
	id := params.ID
	if id == "" {
		id = uuid.NewString()
	}
	payload := params.PayloadJSON
	if payload == "" {
		payload = "{}"
	}
	maxAttempts := params.MaxAttempts
	if maxAttempts <= 0 {
		maxAttempts = 3
	}
	runAfter := params.RunAfter
	if runAfter.IsZero() {
		runAfter = now
	}

	_, err := exec.ExecContext(ctx, `
INSERT INTO jobs (
  id, type, payload_json, status, priority, max_attempts, run_after, created_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		id,
		params.Type,
		payload,
		StatusQueued,
		params.Priority,
		maxAttempts,
		timeText(runAfter),
		timeText(now),
		timeText(now),
	)
	if err != nil {
		return Job{}, err
	}

	return getJob(ctx, exec, id)
}

func (s *Store) Get(ctx context.Context, id string) (Job, error) {
	return getJob(ctx, s.db, id)
}

func getJob(ctx context.Context, queryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, id string) (Job, error) {
	job, err := scanJob(queryer.QueryRowContext(ctx, selectJobSQL()+" WHERE id = ?", id))
	if errors.Is(err, sql.ErrNoRows) {
		return Job{}, ErrNotFound
	}

	return job, err
}

func (s *Store) ActiveByPayload(ctx context.Context, jobType string, payloadJSON string) (Job, bool, error) {
	return activeByPayload(ctx, s.db, jobType, payloadJSON, true)
}

func (s *Store) ActiveByPayloadTx(ctx context.Context, tx *sql.Tx, jobType string, payloadJSON string) (Job, bool, error) {
	return activeByPayload(ctx, tx, jobType, payloadJSON, true)
}

func (s *Store) ActiveByPayloadWithoutCancelRequestedTx(ctx context.Context, tx *sql.Tx, jobType string, payloadJSON string) (Job, bool, error) {
	return activeByPayload(ctx, tx, jobType, payloadJSON, false)
}

func activeByPayload(ctx context.Context, queryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, jobType string, payloadJSON string, includeCancelRequested bool) (Job, bool, error) {
	where := " WHERE type = ? AND payload_json = ? AND status IN (?, ?)"
	if !includeCancelRequested {
		where += " AND cancel_requested = 0"
	}
	job, err := scanJob(queryer.QueryRowContext(ctx, selectJobSQL()+where+`
ORDER BY created_at ASC, id ASC
LIMIT 1`, jobType, payloadJSON, StatusQueued, StatusRunning))
	if errors.Is(err, sql.ErrNoRows) {
		return Job{}, false, nil
	}
	if err != nil {
		return Job{}, false, err
	}

	return job, true, nil
}

func (s *Store) ActiveByType(ctx context.Context, jobType string, limit int) ([]Job, error) {
	return activeByType(ctx, s.db, jobType, limit, true)
}

func (s *Store) ActiveByTypeTx(ctx context.Context, tx *sql.Tx, jobType string, limit int) ([]Job, error) {
	return activeByType(ctx, tx, jobType, limit, true)
}

func (s *Store) ActiveByTypeWithoutCancelRequestedTx(ctx context.Context, tx *sql.Tx, jobType string, limit int) ([]Job, error) {
	return activeByType(ctx, tx, jobType, limit, false)
}

func activeByType(ctx context.Context, queryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}, jobType string, limit int, includeCancelRequested bool) ([]Job, error) {
	if limit <= 0 || limit > MaxActiveLookupLimit {
		limit = MaxActiveLookupLimit
	}
	where := " WHERE type = ? AND status IN (?, ?)"
	if !includeCancelRequested {
		where += " AND cancel_requested = 0"
	}
	rows, err := queryer.QueryContext(ctx, selectJobSQL()+where+`
ORDER BY created_at ASC, id ASC
LIMIT ?`, jobType, StatusQueued, StatusRunning, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var jobs []Job
	for rows.Next() {
		job, err := scanJob(rows)
		if err != nil {
			return nil, err
		}
		jobs = append(jobs, job)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	return jobs, nil
}

func (s *Store) List(ctx context.Context, options ListOptions) (ListResult, error) {
	page := options.Page
	if page < 1 {
		page = 1
	}
	pageSize := options.PageSize
	if pageSize <= 0 {
		pageSize = DefaultListPageSize
	}
	if pageSize > MaxListPageSize {
		pageSize = MaxListPageSize
	}
	where, args := listWhere(options.Statuses)

	var total int
	if err := s.db.QueryRowContext(ctx, "SELECT count(*) FROM jobs"+where, args...).Scan(&total); err != nil {
		return ListResult{}, err
	}
	queryArgs := append([]any{maxResultSummaryLength + 1}, args...)
	queryArgs = append(queryArgs, pageSize, (page-1)*pageSize)
	rows, err := s.db.QueryContext(ctx, selectJobListSQL()+where+" ORDER BY "+jobListOrderSQL()+" LIMIT ? OFFSET ?", queryArgs...)
	if err != nil {
		return ListResult{}, err
	}
	defer rows.Close()

	items := []ListItem{}
	for rows.Next() {
		item, err := scanListItem(rows)
		if err != nil {
			return ListResult{}, err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return ListResult{}, err
	}

	return ListResult{Jobs: items, Total: total, Page: page, PageSize: pageSize}, nil
}

func (s *Store) ListFailedDiagnostics(ctx context.Context, limit int) ([]DiagnosticError, int, error) {
	if limit <= 0 {
		limit = DefaultDiagnosticLimit
	}
	if limit > MaxDiagnosticLimit {
		limit = MaxDiagnosticLimit
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT id, type, status, substr(error, 1, ?), updated_at
FROM jobs
WHERE status = ? AND error != ''
ORDER BY `+jobListOrderSQL()+`
LIMIT ?`, MaxDiagnosticErrorLength+1, StatusFailed, limit)
	if err != nil {
		return nil, limit, err
	}
	defer rows.Close()

	items := []DiagnosticError{}
	for rows.Next() {
		var item DiagnosticError
		if err := rows.Scan(&item.ID, &item.Type, &item.Status, &item.Error, &item.UpdatedAt); err != nil {
			return nil, limit, err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, limit, err
	}

	return items, limit, nil
}

func (s *Store) Claim(ctx context.Context, now time.Time, staleAfter time.Duration) (Job, bool, error) {
	if staleAfter <= 0 {
		staleAfter = 15 * time.Minute
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Job{}, false, err
	}
	defer tx.Rollback()

	staleBefore := now.UTC().Add(-staleAfter)
	nowText := timeText(now)
	staleBeforeText := timeText(staleBefore)
	// A live runner may explicitly complete a late-cancelled job after recording
	// a result. Stale recovery has no such signal, so cancellation wins and any
	// recorded result is preserved as partial diagnostic state.
	if _, err := tx.ExecContext(ctx, `
UPDATE jobs
SET status = ?, progress = 1, error = '', locked_at = NULL, cancel_requested = 0, completed_at = ?, updated_at = ?
WHERE status = ? AND cancel_requested = 0 AND locked_at IS NOT NULL AND locked_at COLLATE RFC3339_NANO <= ? AND result_committed = 1`, StatusSucceeded, nowText, nowText, StatusRunning, staleBeforeText); err != nil {
		return Job{}, false, err
	}
	if _, err := tx.ExecContext(ctx, `
UPDATE jobs
SET status = ?, result_committed = 0, locked_at = NULL, completed_at = ?, updated_at = ?
WHERE status = ? AND cancel_requested = 1 AND locked_at IS NOT NULL AND locked_at COLLATE RFC3339_NANO <= ?`, StatusCancelled, nowText, nowText, StatusRunning, staleBeforeText); err != nil {
		return Job{}, false, err
	}
	if _, err := tx.ExecContext(ctx, `
UPDATE jobs
SET status = ?, error = ?, locked_at = NULL, completed_at = ?, updated_at = ?
WHERE status = ? AND cancel_requested = 0 AND locked_at IS NOT NULL AND locked_at COLLATE RFC3339_NANO <= ? AND result_committed = 0 AND attempts >= max_attempts`, StatusFailed, staleMaxAttemptsError, nowText, nowText, StatusRunning, staleBeforeText); err != nil {
		return Job{}, false, err
	}

	var id string
	err = tx.QueryRowContext(ctx, `
SELECT id
FROM jobs
WHERE (status = ? AND cancel_requested = 0 AND run_after COLLATE RFC3339_NANO <= ?)
   OR (status = ? AND cancel_requested = 0 AND locked_at IS NOT NULL AND locked_at COLLATE RFC3339_NANO <= ? AND attempts < max_attempts)
ORDER BY priority DESC, run_after COLLATE RFC3339_NANO, created_at COLLATE RFC3339_NANO
LIMIT 1`, StatusQueued, nowText, StatusRunning, staleBeforeText).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return Job{}, false, tx.Commit()
	}
	if err != nil {
		return Job{}, false, err
	}

	result, err := tx.ExecContext(ctx, `
UPDATE jobs
SET status = ?, attempts = attempts + 1, locked_at = ?, cancel_requested = 0, updated_at = ?
WHERE id = ?
  AND ((status = ? AND cancel_requested = 0 AND run_after COLLATE RFC3339_NANO <= ?)
    OR (status = ? AND cancel_requested = 0 AND locked_at IS NOT NULL AND locked_at COLLATE RFC3339_NANO <= ? AND attempts < max_attempts))`, StatusRunning, nowText, nowText, id, StatusQueued, nowText, StatusRunning, staleBeforeText)
	if err != nil {
		return Job{}, false, err
	}
	if changed, err := result.RowsAffected(); err != nil {
		return Job{}, false, err
	} else if changed == 0 {
		return Job{}, false, tx.Commit()
	}

	job, err := scanJob(tx.QueryRowContext(ctx, selectJobSQL()+" WHERE id = ?", id))
	if err != nil {
		return Job{}, false, err
	}

	return job, true, tx.Commit()
}

func (s *Store) Complete(ctx context.Context, id string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	now := timeText(time.Now())
	result, err := tx.ExecContext(ctx, `
UPDATE jobs
SET status = ?,
    progress = 1,
    error = '',
    result_json = CASE WHEN result_committed = 1 THEN result_json ELSE '{}' END,
    locked_at = NULL,
    cancel_requested = 0,
    completed_at = ?,
    updated_at = ?
WHERE id = ? AND status = ? AND (cancel_requested = 0 OR result_committed = 1)`, StatusSucceeded, now, now, id, StatusRunning)
	if err != nil {
		return err
	}
	if changed, err := result.RowsAffected(); err != nil {
		return err
	} else if changed > 0 {
		return tx.Commit()
	}

	result, err = tx.ExecContext(ctx, `
UPDATE jobs
SET status = ?, locked_at = NULL, completed_at = ?, updated_at = ?
WHERE id = ? AND status = ? AND cancel_requested = 1 AND result_committed = 0`, StatusCancelled, now, now, id, StatusRunning)
	if err != nil {
		return err
	}
	if changed, err := result.RowsAffected(); err != nil {
		return err
	} else if changed > 0 {
		return tx.Commit()
	}

	if exists, err := jobExists(ctx, tx, id); err != nil {
		return err
	} else if !exists {
		return ErrNotFound
	}

	return ErrInvalidTransition
}

func (s *Store) CompleteWithResult(ctx context.Context, id string, resultJSON string) error {
	return completeWithResult(ctx, s.db, id, resultJSON)
}

func (s *Store) CompleteWithResultTx(ctx context.Context, tx *sql.Tx, id string, resultJSON string) error {
	if tx == nil {
		return errors.New("job result transaction is required")
	}

	return completeWithResult(ctx, tx, id, resultJSON)
}

// SetResult is kept for existing callers, but final results now complete the
// job in the same store update instead of leaving a running committed-result row.
func (s *Store) SetResult(ctx context.Context, id string, resultJSON string) error {
	return s.CompleteWithResult(ctx, id, resultJSON)
}

func (s *Store) SetResultTx(ctx context.Context, tx *sql.Tx, id string, resultJSON string) error {
	if tx == nil {
		return errors.New("job result transaction is required")
	}

	return s.CompleteWithResultTx(ctx, tx, id, resultJSON)
}

func (s *Store) SetPartialResult(ctx context.Context, id string, resultJSON string) error {
	return setPartialResult(ctx, s.db, id, resultJSON)
}

func (s *Store) SetPartialResultTx(ctx context.Context, tx *sql.Tx, id string, resultJSON string) error {
	if tx == nil {
		return errors.New("job result transaction is required")
	}

	return setPartialResult(ctx, tx, id, resultJSON)
}

func setPartialResult(ctx context.Context, exec jobExecutor, id string, resultJSON string) error {
	if resultJSON == "" {
		resultJSON = "{}"
	}
	now := timeText(time.Now())
	_, err := exec.ExecContext(ctx, `
UPDATE jobs
SET result_json = ?, result_committed = 0, updated_at = ?
WHERE id = ? AND status = ? AND result_committed = 0`, resultJSON, now, id, StatusRunning)

	return err
}

func completeWithResult(ctx context.Context, exec jobExecutor, id string, resultJSON string) error {
	if resultJSON == "" {
		resultJSON = "{}"
	}
	now := timeText(time.Now())
	result, err := exec.ExecContext(ctx, `
UPDATE jobs
SET status = ?,
    progress = 1,
    error = '',
    result_json = ?,
    result_committed = 1,
    locked_at = NULL,
    cancel_requested = 0,
    completed_at = ?,
    updated_at = ?
WHERE id = ? AND status = ?`, StatusSucceeded, resultJSON, now, now, id, StatusRunning)
	if err != nil {
		return err
	}
	if changed, err := result.RowsAffected(); err != nil {
		return err
	} else if changed > 0 {
		return nil
	}

	if exists, err := jobExists(ctx, exec, id); err != nil {
		return err
	} else if !exists {
		return ErrNotFound
	}

	return ErrInvalidTransition
}

// ReportProgress records best-effort UI state. Handlers may report in-flight
// progress in [0, 0.99], while terminal completion methods set progress to 1.
// Download jobs report yt-dlp transfer progress capped below completion,
// TubeArchivist imports report normalized backup processing progress, and
// preview, channel, and retention jobs currently remain at 0 until their final
// result is stored. Runner lease renewal is separate through RenewLease.
func (s *Store) ReportProgress(ctx context.Context, id string, progress float64) error {
	progress = clampInFlightProgress(progress)
	now := timeText(time.Now())
	_, err := s.db.ExecContext(ctx, `
UPDATE jobs
SET progress = CASE WHEN progress > ? THEN progress ELSE ? END,
    updated_at = ?
WHERE id = ? AND status = ?`, progress, progress, now, id, StatusRunning)

	return err
}

// RenewLease refreshes runner-owned liveness without mutating domain progress.
func (s *Store) RenewLease(ctx context.Context, id string) error {
	now := timeText(time.Now())
	_, err := s.db.ExecContext(ctx, `
UPDATE jobs
SET locked_at = ?,
    updated_at = ?
WHERE id = ? AND status = ?`, now, now, id, StatusRunning)

	return err
}

// Heartbeat keeps the legacy combined lease/progress update for direct callers.
func (s *Store) Heartbeat(ctx context.Context, id string, progress float64) error {
	progress = clampInFlightProgress(progress)
	now := timeText(time.Now())
	_, err := s.db.ExecContext(ctx, `
UPDATE jobs
SET locked_at = ?,
    progress = CASE WHEN progress > ? THEN progress ELSE ? END,
    updated_at = ?
WHERE id = ? AND status = ?`, now, progress, progress, now, id, StatusRunning)

	return err
}

func clampInFlightProgress(progress float64) float64 {
	if progress < 0 {
		return 0
	}
	if progress > 0.99 {
		return 0.99
	}

	return progress
}

func (s *Store) Fail(ctx context.Context, id string, cause error, nextRun time.Time) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var status Status
	var attempts int
	var maxAttempts int
	var resultCommitted int
	var cancelRequested int
	if err := tx.QueryRowContext(ctx, "SELECT status, attempts, max_attempts, result_committed, cancel_requested FROM jobs WHERE id = ?", id).Scan(&status, &attempts, &maxAttempts, &resultCommitted, &cancelRequested); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		return err
	}
	if status != StatusRunning {
		return ErrInvalidTransition
	}

	now := timeText(time.Now())
	message := ""
	if cause != nil {
		message = cause.Error()
	}

	if cancelRequested == 1 && resultCommitted == 0 {
		_, err = tx.ExecContext(ctx, `
UPDATE jobs
SET status = ?, locked_at = NULL, completed_at = ?, updated_at = ?
WHERE id = ? AND status = ?`, StatusCancelled, now, now, id, StatusRunning)
	} else if attempts < maxAttempts && resultCommitted == 0 {
		if nextRun.IsZero() {
			nextRun = time.Now()
		}
		result, updateErr := tx.ExecContext(ctx, `
UPDATE jobs
SET status = ?, error = ?, result_json = '{}', result_committed = 0, run_after = ?, locked_at = NULL, cancel_requested = 0, updated_at = ?
WHERE id = ? AND status = ? AND cancel_requested = 0`, StatusQueued, message, timeText(nextRun), now, id, StatusRunning)
		if updateErr != nil {
			return updateErr
		}
		changed, updateErr := result.RowsAffected()
		if updateErr != nil {
			return updateErr
		}
		if changed == 0 {
			_, err = tx.ExecContext(ctx, `
UPDATE jobs
SET status = ?, locked_at = NULL, completed_at = ?, updated_at = ?
WHERE id = ? AND status = ? AND cancel_requested = 1 AND result_committed = 0`, StatusCancelled, now, now, id, StatusRunning)
		}
	} else {
		condition := "WHERE id = ? AND status = ?"
		args := []any{StatusFailed, message, now, now, id, StatusRunning}
		if resultCommitted == 0 {
			condition += " AND cancel_requested = 0"
		}
		result, updateErr := tx.ExecContext(ctx, `
UPDATE jobs
SET status = ?, error = ?, locked_at = NULL, completed_at = ?, updated_at = ?
`+condition, args...)
		if updateErr != nil {
			return updateErr
		}
		changed, updateErr := result.RowsAffected()
		if updateErr != nil {
			return updateErr
		}
		if changed == 0 && resultCommitted == 0 {
			_, err = tx.ExecContext(ctx, `
UPDATE jobs
SET status = ?, locked_at = NULL, completed_at = ?, updated_at = ?
WHERE id = ? AND status = ? AND cancel_requested = 1 AND result_committed = 0`, StatusCancelled, now, now, id, StatusRunning)
		}
	}
	if err != nil {
		return err
	}

	return tx.Commit()
}

func (s *Store) Cancel(ctx context.Context, id string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	now := timeText(time.Now())
	result, err := tx.ExecContext(ctx, `
UPDATE jobs
SET status = ?, cancel_requested = 1, completed_at = ?, updated_at = ?
WHERE id = ? AND status = ? AND result_committed = 0`, StatusCancelled, now, now, id, StatusQueued)
	if err != nil {
		return err
	}
	if changed, err := result.RowsAffected(); err != nil {
		return err
	} else if changed > 0 {
		return tx.Commit()
	}

	result, err = tx.ExecContext(ctx, `
UPDATE jobs
SET cancel_requested = 1, updated_at = ?
WHERE id = ? AND status = ? AND result_committed = 0`, now, id, StatusRunning)
	if err != nil {
		return err
	}
	if changed, err := result.RowsAffected(); err != nil {
		return err
	} else if changed > 0 {
		return tx.Commit()
	}

	if exists, err := jobExists(ctx, tx, id); err != nil {
		return err
	} else if !exists {
		return ErrNotFound
	}

	return ErrInvalidTransition
}

func (s *Store) Retry(ctx context.Context, id string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	now := timeText(time.Now())
	result, err := tx.ExecContext(ctx, `
UPDATE jobs
SET status = ?,
    progress = 0,
    result_json = '{}',
    result_committed = 0,
    run_after = ?,
    locked_at = NULL,
    cancel_requested = 0,
    completed_at = NULL,
    max_attempts = CASE WHEN max_attempts <= attempts THEN attempts + 1 ELSE max_attempts END,
    updated_at = ?
WHERE id = ? AND status = ? AND result_committed = 0`, StatusQueued, now, now, id, StatusFailed)
	if err != nil {
		return err
	}
	if changed, err := result.RowsAffected(); err != nil {
		return err
	} else if changed > 0 {
		return tx.Commit()
	}

	var status Status
	var resultCommitted int
	if err := tx.QueryRowContext(ctx, "SELECT status, result_committed FROM jobs WHERE id = ?", id).Scan(&status, &resultCommitted); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		return err
	}
	if status != StatusFailed {
		return ErrInvalidTransition
	}
	if resultCommitted == 1 {
		return ErrUnsafeRetry
	}

	return ErrInvalidTransition
}

func (s *Store) MarkCancelled(ctx context.Context, id string) error {
	now := timeText(time.Now())
	result, err := s.db.ExecContext(ctx, `
UPDATE jobs
SET status = ?, cancel_requested = 1, locked_at = NULL, completed_at = ?, updated_at = ?
WHERE id = ? AND status = ? AND result_committed = 0`, StatusCancelled, now, now, id, StatusRunning)
	if err != nil {
		return err
	}
	if changed, err := result.RowsAffected(); err != nil {
		return err
	} else if changed > 0 {
		return nil
	}

	var status Status
	if err := s.db.QueryRowContext(ctx, "SELECT status FROM jobs WHERE id = ?", id).Scan(&status); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		return err
	}
	if status == StatusCancelled {
		return nil
	}

	return ErrInvalidTransition
}

func selectJobSQL() string {
	return `
SELECT
  id,
  type,
  payload_json,
  result_json,
  result_committed,
  status,
  priority,
  attempts,
  max_attempts,
  progress,
  error,
  run_after,
  locked_at,
  cancel_requested,
  created_at,
  updated_at,
  completed_at
FROM jobs`
}

func jobListOrderSQL() string {
	return timestampOrderSQL("updated_at") + " DESC, " + timestampOrderSQL("created_at") + " DESC, id DESC"
}

func timestampOrderSQL(column string) string {
	return "CASE " +
		"WHEN instr(" + column + ", 'Z') > instr(" + column + ", '.') AND instr(" + column + ", '.') > 0 THEN " +
		"substr(" + column + ", 1, instr(" + column + ", '.')) || " +
		"substr(substr(" + column + ", instr(" + column + ", '.') + 1, instr(" + column + ", 'Z') - instr(" + column + ", '.') - 1) || '000000000', 1, 9) || " +
		"substr(" + column + ", instr(" + column + ", 'Z')) " +
		"WHEN instr(" + column + ", 'Z') > 0 THEN " +
		"substr(" + column + ", 1, instr(" + column + ", 'Z') - 1) || '.000000000' || substr(" + column + ", instr(" + column + ", 'Z')) " +
		"ELSE " + column + " END"
}

func selectJobListSQL() string {
	return `
SELECT
  id,
  type,
  status,
  priority,
  attempts,
  max_attempts,
  progress,
  error,
  run_after,
  locked_at,
  cancel_requested,
  created_at,
  updated_at,
  completed_at,
  substr(result_json, 1, ?)
FROM jobs`
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanJob(row rowScanner) (Job, error) {
	var job Job
	var lockedAt sql.NullString
	var completedAt sql.NullString
	var resultCommitted int
	var cancelRequested int
	if err := row.Scan(
		&job.ID,
		&job.Type,
		&job.PayloadJSON,
		&job.ResultJSON,
		&resultCommitted,
		&job.Status,
		&job.Priority,
		&job.Attempts,
		&job.MaxAttempts,
		&job.Progress,
		&job.Error,
		&job.RunAfter,
		&lockedAt,
		&cancelRequested,
		&job.CreatedAt,
		&job.UpdatedAt,
		&completedAt,
	); err != nil {
		return Job{}, err
	}

	job.LockedAt = lockedAt.String
	job.CancelRequested = cancelRequested == 1
	job.ResultCommitted = resultCommitted == 1
	job.CompletedAt = completedAt.String
	job.ResultSummary = summarizeResult(job.ResultJSON)

	return job, nil
}

func scanListItem(row rowScanner) (ListItem, error) {
	var item ListItem
	var lockedAt sql.NullString
	var completedAt sql.NullString
	var cancelRequested int
	var resultJSON string
	if err := row.Scan(
		&item.ID,
		&item.Type,
		&item.Status,
		&item.Priority,
		&item.Attempts,
		&item.MaxAttempts,
		&item.Progress,
		&item.Error,
		&item.RunAfter,
		&lockedAt,
		&cancelRequested,
		&item.CreatedAt,
		&item.UpdatedAt,
		&completedAt,
		&resultJSON,
	); err != nil {
		return ListItem{}, err
	}

	item.LockedAt = lockedAt.String
	item.CancelRequested = cancelRequested == 1
	item.CompletedAt = completedAt.String
	item.ResultSummary = summarizeResult(resultJSON)

	return item, nil
}

func jobExists(ctx context.Context, queryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, id string) (bool, error) {
	var exists bool
	if err := queryer.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM jobs WHERE id = ?)", id).Scan(&exists); err != nil {
		return false, err
	}

	return exists, nil
}

func listWhere(statuses []Status) (string, []any) {
	if len(statuses) == 0 {
		return "", nil
	}
	placeholders := make([]string, 0, len(statuses))
	args := make([]any, 0, len(statuses))
	for _, status := range statuses {
		if status == "" {
			continue
		}
		placeholders = append(placeholders, "?")
		args = append(args, status)
	}
	if len(args) == 0 {
		return "", nil
	}

	return " WHERE status IN (" + strings.Join(placeholders, ", ") + ")", args
}

func summarizeResult(raw string) string {
	result := strings.TrimSpace(raw)
	if emptyJobResult(result) {
		return ""
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, []byte(result)); err == nil {
		result = compact.String()
	}
	if len(result) > maxResultSummaryLength {
		return strings.TrimSpace(result[:maxResultSummaryLength]) + " ... [truncated]"
	}

	return result
}

func emptyJobResult(raw string) bool {
	result := strings.TrimSpace(raw)
	return result == "" || result == "{}"
}

func timeText(t time.Time) string {
	return t.UTC().Format(time.RFC3339Nano)
}
