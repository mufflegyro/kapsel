package updater

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Update status values recorded in the app_updates table.
const (
	OfferStatusPending   = "pending"
	OfferStatusApproved  = "approved"
	OfferStatusApplied   = "applied"
	OfferStatusDismissed = "dismissed"
	OfferStatusFailed    = "failed"
)

// ErrOfferNotFound reports that no app_updates row matches the request.
var ErrOfferNotFound = errors.New("update offer not found")

// ErrOfferNotPending reports an approval/dismissal attempt on an offer that
// is no longer awaiting a decision.
var ErrOfferNotPending = errors.New("update offer is not pending")

// ErrOfferNotApproved reports a self-update attempt without a matching
// admin-approved offer row.
var ErrOfferNotApproved = errors.New("update is not approved by the archive admin")

// Offer is one GitHub release discovered as an update candidate and its
// admin decision state.
type Offer struct {
	ID           int64  `json:"id"`
	Version      string `json:"version"`
	ReleaseURL   string `json:"release_url,omitempty"`
	ReleaseNotes string `json:"release_notes,omitempty"`
	PublishedAt  string `json:"published_at,omitempty"`
	DiscoveredAt string `json:"discovered_at,omitempty"`
	UpdatedAt    string `json:"updated_at,omitempty"`
	Status       string `json:"status"`
	ApprovedBy   string `json:"approved_by,omitempty"`
	ApprovedAt   string `json:"approved_at,omitempty"`
	AppliedAt    string `json:"applied_at,omitempty"`
	Error        string `json:"error,omitempty"`

	// appliedBackupPath and appliedPreviousVersion are attached by the
	// apply flow after the swap so the job result can report them.
	appliedBackupPath      string
	appliedPreviousVersion string
}

func (o Offer) result() selfUpdateResult {
	// appliedPreviousVersion is only set when this job actually swapped the
	// binary. The post-crash reconciliation path marks an already-running
	// target applied without a swap, so no restart is needed there.
	return selfUpdateResult{
		Applied:         o.Status == OfferStatusApplied,
		Version:         o.Version,
		PreviousVersion: o.appliedPreviousVersion,
		BackupPath:      o.appliedBackupPath,
		Restarting:      o.appliedPreviousVersion != "",
	}
}

// StatusSummary is the /api/updates payload: configuration, the current
// offer awaiting a decision, and recent offer history.
type StatusSummary struct {
	CurrentVersion     string  `json:"current_version"`
	Repo               string  `json:"repo"`
	CheckInterval      int64   `json:"check_interval_seconds"`
	CheckIntervalLabel string  `json:"check_interval_label"`
	UpdateEnabled      bool    `json:"update_checks_enabled"`
	Pending            *Offer  `json:"pending,omitempty"`
	Recent             []Offer `json:"recent,omitempty"`
	LastCheck          *Check  `json:"last_check,omitempty"`
}

// formatCheckInterval renders the scheduler cadence for the settings UI:
// "disabled" at zero, otherwise hours and minutes ("24h", "1h30m", "15m").
func formatCheckInterval(seconds int64) string {
	if seconds <= 0 {
		return "disabled"
	}
	duration := time.Duration(seconds) * time.Second
	hours := int64(duration.Hours())
	minutes := int64(duration.Minutes()) % 60
	switch {
	case hours >= 1 && minutes == 0:
		return fmt.Sprintf("%dh", hours)
	case hours >= 1:
		return fmt.Sprintf("%dh%dm", hours, minutes)
	case minutes >= 1:
		return fmt.Sprintf("%dm", minutes)
	default:
		return fmt.Sprintf("%ds", seconds)
	}
}

// Check summarizes the most recent release_check job outcome.
type Check struct {
	JobID        string `json:"job_id"`
	Status       string `json:"status"`
	UpdatedAt    string `json:"updated_at"`
	Detail       string `json:"detail,omitempty"`
	UpdateFound  bool   `json:"update_found"`
	FoundVersion string `json:"found_version,omitempty"`
}

func nullableText(value sql.NullString) string {
	return value.String
}

func nullableTimeText(value sql.NullString) string {
	if !value.Valid {
		return ""
	}

	return value.String
}

func scanOffer(row interface{ Scan(dest ...any) error }) (Offer, error) {
	var offer Offer
	var approvedAt sql.NullString
	var appliedAt sql.NullString
	if err := row.Scan(
		&offer.ID,
		&offer.Version,
		&offer.ReleaseURL,
		&offer.ReleaseNotes,
		&offer.PublishedAt,
		&offer.DiscoveredAt,
		&offer.UpdatedAt,
		&offer.Status,
		&offer.ApprovedBy,
		&approvedAt,
		&appliedAt,
		&offer.Error,
	); err != nil {
		return Offer{}, err
	}
	offer.ApprovedAt = nullableTimeText(approvedAt)
	offer.AppliedAt = nullableTimeText(appliedAt)

	return offer, nil
}

const selectOfferSQL = `
SELECT id, version, release_url, release_notes, published_at, discovered_at, updated_at,
       status, approved_by, approved_at, applied_at, error
FROM app_updates`

func findOfferByVersion(ctx context.Context, db *sql.DB, version string) (Offer, error) {
	offer, err := scanOffer(db.QueryRowContext(ctx, selectOfferSQL+" WHERE version = ?", strings.TrimSpace(version)))
	if errors.Is(err, sql.ErrNoRows) {
		return Offer{}, ErrOfferNotFound
	}

	return offer, err
}

func findOfferByID(ctx context.Context, db *sql.DB, id int64) (Offer, error) {
	offer, err := scanOffer(db.QueryRowContext(ctx, selectOfferSQL+" WHERE id = ?", id))
	if errors.Is(err, sql.ErrNoRows) {
		return Offer{}, ErrOfferNotFound
	}

	return offer, err
}

func listOffersByStatus(ctx context.Context, db *sql.DB, status string, limit int) ([]Offer, error) {
	if limit <= 0 {
		limit = 10
	}
	rows, err := db.QueryContext(ctx, selectOfferSQL+" WHERE status = ? ORDER BY discovered_at DESC, id DESC LIMIT ?", status, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	offers := []Offer{}
	for rows.Next() {
		offer, err := scanOffer(rows)
		if err != nil {
			return nil, err
		}
		offers = append(offers, offer)
	}

	return offers, rows.Err()
}

func listRecentOffers(ctx context.Context, db *sql.DB, limit int) ([]Offer, error) {
	if limit <= 0 {
		limit = 10
	}
	rows, err := db.QueryContext(ctx, selectOfferSQL+`
WHERE status IN (?, ?, ?)
ORDER BY discovered_at DESC, id DESC
LIMIT ?`, OfferStatusApproved, OfferStatusApplied, OfferStatusFailed, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	offers := []Offer{}
	for rows.Next() {
		offer, err := scanOffer(rows)
		if err != nil {
			return nil, err
		}
		offers = append(offers, offer)
	}

	return offers, rows.Err()
}

// recordDiscoveredUpdate inserts a pending offer for version. An existing row
// for the same version is left untouched, so dismissed or applied versions
// are never re-offered. The returned bool reports that a new pending row was
// created.
func recordDiscoveredUpdate(ctx context.Context, db *sql.DB, version string, releaseURL string, notes string, publishedAt string) (Offer, bool, error) {
	version = strings.TrimSpace(version)
	if version == "" {
		return Offer{}, false, errors.New("release version is required")
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	result, err := db.ExecContext(ctx, `
INSERT INTO app_updates (version, release_url, release_notes, published_at, discovered_at, updated_at, status)
VALUES (?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(version) DO NOTHING`,
		version, strings.TrimSpace(releaseURL), strings.TrimSpace(notes), strings.TrimSpace(publishedAt), now, now, OfferStatusPending)
	if err != nil {
		return Offer{}, false, err
	}
	created, err := result.RowsAffected()
	if err != nil {
		return Offer{}, false, err
	}
	offer, err := findOfferByVersion(ctx, db, version)
	if err != nil {
		return Offer{}, false, err
	}

	return offer, created > 0, nil
}

func setOfferStatus(ctx context.Context, db *sql.DB, id int64, status string, approvedBy string) (Offer, error) {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	var result sql.Result
	var err error
	switch status {
	case OfferStatusApproved:
		// Approval is granted from a fresh discovery or re-granted after a
		// failed apply, so a transient failure never locks the admin out of
		// retrying. A re-approval also clears the stale failure message.
		result, err = db.ExecContext(ctx, `
UPDATE app_updates
SET status = ?, approved_by = ?, approved_at = ?, updated_at = ?, error = ''
WHERE id = ? AND status IN (?, ?)`, status, strings.TrimSpace(approvedBy), now, now, id, OfferStatusPending, OfferStatusFailed)
	case OfferStatusApplied:
		result, err = db.ExecContext(ctx, `
UPDATE app_updates
SET status = ?, applied_at = ?, updated_at = ?, error = ''
WHERE id = ? AND status = ?`, status, now, now, id, OfferStatusApproved)
	case OfferStatusDismissed:
		result, err = db.ExecContext(ctx, `
UPDATE app_updates
SET status = ?, updated_at = ?
WHERE id = ? AND status = ?`, status, now, id, OfferStatusPending)
	default:
		return Offer{}, errors.New("unsupported update offer status transition")
	}
	if err != nil {
		return Offer{}, err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return Offer{}, err
	}
	if changed == 0 {
		return Offer{}, ErrOfferNotPending
	}

	return findOfferByID(ctx, db, id)
}

func setOfferFailed(ctx context.Context, db *sql.DB, id int64, cause string) (Offer, error) {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	result, err := db.ExecContext(ctx, `
UPDATE app_updates
SET status = ?, error = ?, updated_at = ?
WHERE id = ? AND status IN (?, ?)`, OfferStatusFailed, strings.TrimSpace(cause), now, id, OfferStatusApproved, OfferStatusPending)
	if err != nil {
		return Offer{}, err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return Offer{}, err
	}
	if changed == 0 {
		if _, findErr := findOfferByID(ctx, db, id); findErr != nil {
			return Offer{}, findErr
		}

		return Offer{}, ErrOfferNotPending
	}

	return findOfferByID(ctx, db, id)
}

func lastReleaseCheck(ctx context.Context, db *sql.DB) (*Check, error) {
	var jobID string
	var status string
	var updatedAt string
	var resultJSON string
	err := db.QueryRowContext(ctx, `
SELECT id, status, updated_at, COALESCE(result_json, '')
FROM jobs
WHERE type = ?
ORDER BY updated_at DESC, created_at DESC
LIMIT 1`, ReleaseCheckJobType).Scan(&jobID, &status, &updatedAt, &resultJSON)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	detail, updateFound, foundVersion := releaseCheckResultDetails(resultJSON)

	return &Check{JobID: jobID, Status: status, UpdatedAt: updatedAt, Detail: detail, UpdateFound: updateFound, FoundVersion: foundVersion}, nil
}
