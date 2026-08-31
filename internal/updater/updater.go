// Package updater keeps a kapsel archive current with GitHub releases. The
// scheduler creates release-check jobs on a cadence; a discovered newer
// release is recorded as a pending offer that only the archive admin can
// approve from the web UI. An approved offer is applied by a self-update job
// that downloads the platform binary, verifies its SHA-256 checksum against
// the release checksum sidecar, takes a pre-update database backup, swaps the
// binary in place, and restarts the process.
package updater

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"kapsel/internal/config"
	"kapsel/internal/jobs"
	"kapsel/internal/version"
)

// BackupMetadata describes the pre-update backup the apply flow records in
// the job result. The backup capability is injected so this package does not
// depend on the backup package (whose database dependency would create test
// import cycles through the server).
type BackupMetadata struct {
	SchemaVersion int
}

// CreateBackupFunc snapshots the metadata database into a zip at outputPath.
type CreateBackupFunc func(ctx context.Context, outputPath string) (BackupMetadata, error)

const (
	// ReleaseCheckJobType checks GitHub for a newer release and records a
	// pending offer for the archive admin.
	ReleaseCheckJobType = "release_check"
	// SelfUpdateJobType applies an admin-approved release offer.
	SelfUpdateJobType = "self_update"

	// DefaultCheckInterval paces background GitHub release checks; the
	// canonical default lives in the config package.
	DefaultCheckInterval = config.DefaultUpdateCheckInterval
	// DefaultRepo is the release repository checked for updates.
	DefaultRepo = config.DefaultUpdateRepo

	// updatesDirName stages downloaded release binaries under the data dir.
	updatesDirName = "updates"
	// backupDirName stores pre-update metadata backups under the data dir.
	backupDirName = "backups"
)

// Config carries everything the updater needs from the application.
type Config struct {
	// Repo is the owner/name GitHub repository to check for releases.
	Repo string
	// CurrentVersion is the running binary version (version.Version).
	CurrentVersion string
	// DataDir is the writable data directory for staging and backups.
	DataDir string
	// DBPath is the SQLite metadata database path.
	DBPath string
	// CheckInterval paces background release checks; zero disables checks.
	CheckInterval time.Duration
	// BackupConfig is the application config handed to backup.Create.
	BackupConfig config.Config
	// CreateBackup snapshots the metadata database into the given zip path.
	// The apply flow refuses to swap the binary when it is not set.
	CreateBackup CreateBackupFunc
	// BinaryPath pins the binary to replace. Empty resolves
	// os.Executable() at apply time. Tests set this explicitly.
	BinaryPath string
	// Restart is invoked after an applied update commits its result.
	Restart func()

	// Test hooks (unexported; production wiring uses New's defaults).
	apiBaseURL          string
	allowAnyDownloadURL bool
	fetchRelease        func(ctx context.Context, repo string, tag string) (*githubRelease, error)
	now                 func() time.Time
}

type selfUpdatePayload struct {
	Version string `json:"version"`
	OfferID int64  `json:"offer_id"`
}

type releaseCheckResult struct {
	CurrentVersion  string `json:"current_version"`
	LatestVersion   string `json:"latest_version,omitempty"`
	UpdateAvailable bool   `json:"update_available"`
	OfferCreated    bool   `json:"offer_created,omitempty"`
	Skipped         bool   `json:"skipped,omitempty"`
	SkipReason      string `json:"skip_reason,omitempty"`
	Error           string `json:"error,omitempty"`
}

type selfUpdateResult struct {
	Applied         bool   `json:"applied"`
	Version         string `json:"version"`
	PreviousVersion string `json:"previous_version"`
	BackupPath      string `json:"backup_path,omitempty"`
	Restarting      bool   `json:"restarting"`
	Skipped         bool   `json:"skipped,omitempty"`
	SkipReason      string `json:"skip_reason,omitempty"`
}

// Updater serves the archive admin's update workflow and runs the release
// check and self-update jobs.
type Updater struct {
	db     *sql.DB
	store  *jobs.Store
	github *githubClient
	config Config
}

func New(db *sql.DB, store *jobs.Store, cfg Config) *Updater {
	if cfg.Repo == "" {
		cfg.Repo = DefaultRepo
	}
	if cfg.CurrentVersion == "" {
		cfg.CurrentVersion = version.Version
	}
	if cfg.now == nil {
		cfg.now = func() time.Time { return time.Now().UTC() }
	}
	client := newGitHubClient()
	if cfg.apiBaseURL != "" {
		client.baseURL = cfg.apiBaseURL
	}
	if cfg.fetchRelease == nil {
		cfg.fetchRelease = client.release
	}

	return &Updater{db: db, store: store, github: client, config: cfg}
}

// Enabled reports whether background release checks are configured.
func (u *Updater) Enabled() bool {
	return u != nil && u.config.CheckInterval > 0
}

// CheckIntervalSeconds reports the scheduler cadence in whole seconds.
func (u *Updater) CheckIntervalSeconds() int64 {
	if u == nil || u.config.CheckInterval <= 0 {
		return 0
	}

	return int64(u.config.CheckInterval.Seconds())
}

// Repo reports the configured release repository.
func (u *Updater) Repo() string {
	if u == nil {
		return ""
	}

	return u.config.Repo
}

// CurrentVersion reports the running binary version.
func (u *Updater) CurrentVersion() string {
	if u == nil {
		return version.Version
	}

	return u.config.CurrentVersion
}

// SetRestartFunc installs the process restart hook used after an applied
// update. Safe to call at startup before jobs can run.
func (u *Updater) SetRestartFunc(restart func()) {
	u.config.Restart = restart
}

// Status summarizes update state for the settings surface.
func (u *Updater) Status(ctx context.Context) (StatusSummary, error) {
	summary := StatusSummary{
		CurrentVersion:     u.CurrentVersion(),
		Repo:               u.Repo(),
		CheckInterval:      u.CheckIntervalSeconds(),
		CheckIntervalLabel: formatCheckInterval(u.CheckIntervalSeconds()),
		UpdateEnabled:      u.Enabled(),
		Recent:             []Offer{},
	}
	pending, err := listOffersByStatus(ctx, u.db, OfferStatusPending, 1)
	if err != nil {
		return summary, err
	}
	if len(pending) > 0 {
		summary.Pending = &pending[0]
	}
	recent, err := listRecentOffers(ctx, u.db, 10)
	if err != nil {
		return summary, err
	}
	summary.Recent = recent
	lastCheck, err := lastReleaseCheck(ctx, u.db)
	if err != nil {
		return summary, err
	}
	summary.LastCheck = lastCheck

	return summary, nil
}

// CheckNow enqueues an immediate release check. It dedupes against a job
// that is already queued or running, but ignores the scheduler interval.
func (u *Updater) CheckNow(ctx context.Context) (jobs.Job, bool, error) {
	return u.enqueueReleaseCheck(ctx)
}

// Approve records the archive admin's approval for a pending (or previously
// failed) offer and enqueues the self-update job that applies it.
func (u *Updater) Approve(ctx context.Context, id int64, approvedBy string) (Offer, jobs.Job, bool, error) {
	offer, err := findOfferByID(ctx, u.db, id)
	if err != nil {
		return Offer{}, jobs.Job{}, false, err
	}
	if offer.Status != OfferStatusPending && offer.Status != OfferStatusFailed {
		return Offer{}, jobs.Job{}, false, fmt.Errorf("%w (status %s)", ErrOfferNotPending, offer.Status)
	}
	if version.Dev() {
		return Offer{}, jobs.Job{}, false, errors.New("this kapsel build has no release version; updates require a stamped release binary")
	}
	offer, err = setOfferStatus(ctx, u.db, id, OfferStatusApproved, approvedBy)
	if err != nil {
		return Offer{}, jobs.Job{}, false, err
	}
	job, created, err := u.enqueueSelfUpdate(ctx, offer.Version, offer.ID)
	if err != nil {
		return offer, jobs.Job{}, false, err
	}

	return offer, job, created, nil
}

// Dismiss records the archive admin declining a pending offer. The version is
// never re-offered.
func (u *Updater) Dismiss(ctx context.Context, id int64) (Offer, error) {
	return setOfferStatus(ctx, u.db, id, OfferStatusDismissed, "")
}

func (u *Updater) enqueueReleaseCheck(ctx context.Context) (jobs.Job, bool, error) {
	if u.store == nil {
		return jobs.Job{}, false, errors.New("update scheduler missing job store")
	}

	return u.store.FindOrEnqueue(ctx, jobs.EnqueueParams{Type: ReleaseCheckJobType, PayloadJSON: `{}`, MaxAttempts: 1}, func(ctx context.Context, tx *sql.Tx) (jobs.Job, bool, error) {
		return u.store.ActiveByPayloadWithoutCancelRequestedTx(ctx, tx, ReleaseCheckJobType, `{}`)
	})
}

func selfUpdatePayloadJSON(jobVersion string, offerID int64) string {
	// Deterministic JSON keeps payload-based job dedupe stable. The offer ID
	// is embedded so the apply job re-verifies the exact offer it was armed
	// for; offers are unique per version, so dedupe semantics are unchanged.
	return fmt.Sprintf(`{"offer_id":%d,"version":%s}`, offerID, strconv.Quote(strings.TrimSpace(jobVersion)))
}

func (u *Updater) enqueueSelfUpdate(ctx context.Context, jobVersion string, offerID int64) (jobs.Job, bool, error) {
	if u.store == nil {
		return jobs.Job{}, false, errors.New("update scheduler missing job store")
	}
	payload := selfUpdatePayloadJSON(jobVersion, offerID)

	return u.store.FindOrEnqueue(ctx, jobs.EnqueueParams{Type: SelfUpdateJobType, PayloadJSON: payload, MaxAttempts: 3}, func(ctx context.Context, tx *sql.Tx) (jobs.Job, bool, error) {
		return u.store.ActiveByPayloadWithoutCancelRequestedTx(ctx, tx, SelfUpdateJobType, payload)
	})
}

// EnsureSelfUpdateJob enqueues the apply job for an approved offer. The bool
// reports whether a new job was created (false when one is already active).
func (u *Updater) EnsureSelfUpdateJob(ctx context.Context, jobVersion string, offerID int64) (jobs.Job, bool, error) {
	return u.enqueueSelfUpdate(ctx, jobVersion, offerID)
}

// EnsureReleaseCheckJobs enqueues a release check when the configured
// interval has elapsed since the last attempt. A zero interval disables
// scheduled checks entirely. Failed checks back off exponentially (15m,
// 30m, 60m, … capped at the interval) so a persistent failure — a GitHub
// outage or an exhausted rate limit shared by several instances behind one
// NAT — is not re-queried on every scheduler tick.
func (u *Updater) EnsureReleaseCheckJobs(ctx context.Context) (int, error) {
	if u == nil || u.db == nil || u.store == nil {
		return 0, errors.New("update scheduler missing database or job store")
	}
	if u.config.CheckInterval <= 0 {
		return 0, nil
	}
	now := u.config.now().UTC()

	// Scheduler job-table checks route through jobs.Store helpers — the
	// updater owns policy (intervals, backoff) but not the job table.
	active, err := u.store.HasActiveJobByType(ctx, ReleaseCheckJobType)
	if err != nil {
		return 0, err
	}
	if active {
		return 0, nil
	}

	latest, found, err := u.store.LatestJobOfType(ctx, ReleaseCheckJobType)
	if err != nil {
		return 0, err
	}
	if found {
		latestCreated, parseErr := time.Parse(time.RFC3339Nano, strings.TrimSpace(latest.CreatedAt))
		if parseErr == nil {
			elapsed := now.Sub(latestCreated.UTC())
			if latest.Status == jobs.StatusFailed {
				streak, streakErr := u.consecutiveFailedChecks(ctx)
				if streakErr != nil {
					return 0, streakErr
				}
				if elapsed < failedCheckBackoff(u.config.CheckInterval, streak) {
					return 0, nil
				}
			} else if elapsed < u.config.CheckInterval {
				return 0, nil
			}
		}
	}

	_, created, err := u.enqueueReleaseCheck(ctx)
	if err != nil {
		return 0, err
	}
	if !created {
		return 0, nil
	}

	return 1, nil
}

// failureBackoffBase is the retry delay after the first exhausted check
// attempt; each further consecutive failure doubles it.
const failureBackoffBase = 15 * time.Minute

// failedCheckBackoff returns how long to wait after `streak` consecutive
// exhausted check attempts: base doubling per failure, capped at the
// configured interval so backoff never exceeds normal pacing.
func failedCheckBackoff(interval time.Duration, streak int) time.Duration {
	backoff := failureBackoffBase
	for range streak - 1 {
		if backoff >= interval {
			break
		}
		backoff *= 2
	}
	if backoff > interval {
		backoff = interval
	}

	return backoff
}

// consecutiveFailedChecks counts how many of the most recent release-check
// jobs ended failed. The job runner already retries each enqueued check
// MaxAttempts times; this streak only measures scheduler-level attempts.
func (u *Updater) consecutiveFailedChecks(ctx context.Context) (int, error) {
	rows, err := u.db.QueryContext(ctx, `SELECT status FROM jobs WHERE type = ? ORDER BY created_at DESC LIMIT 32`, ReleaseCheckJobType)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	streak := 0
	for rows.Next() {
		var status jobs.Status
		if err := rows.Scan(&status); err != nil {
			return 0, err
		}
		if status != jobs.StatusFailed {
			break
		}
		streak++
	}

	return streak, rows.Err()
}

// HandleReleaseCheck implements the release_check job. It records a pending
// offer when the latest GitHub release is newer than the running version.
func (u *Updater) HandleReleaseCheck(ctx context.Context, job jobs.Job) error {
	result := releaseCheckResult{CurrentVersion: u.CurrentVersion()}
	if version.Dev() {
		result.Skipped = true
		result.SkipReason = "development build has no release version; build with a stamped version to enable update checks"

		return u.commitCheckResult(ctx, job.ID, result)
	}
	if u.config.Repo == "" || strings.Count(u.config.Repo, "/") != 1 {
		result.Skipped = true
		result.SkipReason = "KAPSEL_UPDATE_REPO is not configured as owner/name"

		return u.commitCheckResult(ctx, job.ID, result)
	}

	release, err := u.config.fetchRelease(ctx, u.config.Repo, "")
	if err != nil {
		result.Error = err.Error()
		_ = u.setPartialCheckResult(ctx, job.ID, result)
		if errors.Is(err, ErrNoReleases) {
			result.Skipped = true
			result.SkipReason = ErrNoReleases.Error()

			return u.commitCheckResult(ctx, job.ID, result)
		}

		return err
	}
	result.LatestVersion = release.TagName
	result.UpdateAvailable = version.Newer(u.CurrentVersion(), release.TagName)
	if !result.UpdateAvailable {
		return u.commitCheckResult(ctx, job.ID, result)
	}

	offer, created, err := recordDiscoveredUpdate(ctx, u.db, release.TagName, release.HTMLURL, release.Body, release.PublishedAt)
	if err != nil {
		result.Error = err.Error()
		_ = u.setPartialCheckResult(ctx, job.ID, result)

		return err
	}
	result.OfferCreated = created
	_ = offer

	return u.commitCheckResult(ctx, job.ID, result)
}

func (u *Updater) commitCheckResult(ctx context.Context, jobID string, result releaseCheckResult) error {
	if u.store == nil {
		return nil
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		return err
	}

	return u.store.CompleteWithResult(ctx, jobID, string(encoded))
}

func (u *Updater) setPartialCheckResult(ctx context.Context, jobID string, result releaseCheckResult) error {
	if u.store == nil {
		return nil
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		return err
	}

	return u.store.SetPartialResult(ctx, jobID, string(encoded))
}

func releaseCheckResultDetails(resultJSON string) (detail string, updateFound bool, foundVersion string) {
	trimmed := strings.TrimSpace(resultJSON)
	if trimmed == "" || trimmed == "{}" {
		return "", false, ""
	}
	var result releaseCheckResult
	if err := json.Unmarshal([]byte(trimmed), &result); err != nil {
		return "", false, ""
	}
	detail = result.SkipReason
	if result.Error != "" {
		if detail != "" {
			detail = detail + "; " + result.Error
		} else {
			detail = result.Error
		}
	}
	if detail == "" && result.LatestVersion != "" {
		if result.UpdateAvailable {
			detail = "update available: " + result.LatestVersion
		} else {
			detail = "up to date at " + result.CurrentVersion
		}
	}

	return detail, result.UpdateAvailable, result.LatestVersion
}

// HandleSelfUpdate implements the self_update job for an admin-approved
// offer: download, verify, back up, swap, and restart.
func (u *Updater) HandleSelfUpdate(ctx context.Context, job jobs.Job) error {
	var payload selfUpdatePayload
	if err := json.Unmarshal([]byte(job.PayloadJSON), &payload); err != nil {
		return fmt.Errorf("invalid self-update payload: %w", err)
	}
	offer, err := u.applyApprovedUpdate(ctx, payload.OfferID, payload.Version)
	if err != nil {
		if offer.ID != 0 {
			if _, statusErr := setOfferFailed(ctx, u.db, offer.ID, err.Error()); statusErr != nil {
				slog.Warn("could not record failed update offer", "offer_id", offer.ID, "error", statusErr)
			}
		}
		_ = u.setPartialSelfUpdateResult(ctx, job.ID, selfUpdateResult{Applied: false, Version: payload.Version, Restarting: false})
		return err
	}

	// The result is committed before the restart request so the job shows
	// applied even if the process exits mid-shutdown.
	if err := u.commitSelfUpdateResult(ctx, job.ID, offer.result()); err != nil {
		return err
	}
	result := offer.result()
	if !result.Restarting {
		slog.Info("self-update reconciled; the running binary already is the target and no restart is needed", "version", result.Version)

		return nil
	}
	if u.config.Restart != nil {
		slog.Info("self-update applied; requesting kapsel restart", "from", offer.appliedPreviousVersion, "to", payload.Version)
		u.config.Restart()
	}

	return nil
}

func (u *Updater) setPartialSelfUpdateResult(ctx context.Context, jobID string, result selfUpdateResult) error {
	if u.store == nil {
		return nil
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		return err
	}

	return u.store.SetPartialResult(ctx, jobID, string(encoded))
}

func (u *Updater) commitSelfUpdateResult(ctx context.Context, jobID string, result selfUpdateResult) error {
	if u.store == nil {
		return nil
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		return err
	}

	return u.store.CompleteWithResult(ctx, jobID, string(encoded))
}

// applyApprovedUpdate runs the guarded update pipeline and returns the offer
// marked applied.
//
// The pipeline is ordered so every failure before the swap leaves the
// archive untouched: approval is re-verified, the release is re-fetched and
// verified tag-for-tag, the asset is checksum-verified in a staging dir, the
// pre-update backup must exist and be non-empty, and only then is the
// binary replaced through an atomic rename that never leaves the path
// missing.
func (u *Updater) applyApprovedUpdate(ctx context.Context, offerID int64, payloadVersion string) (Offer, error) {
	empty := Offer{}
	if u.db == nil {
		return empty, errors.New("self-update missing database")
	}
	if version.Dev() {
		return empty, errors.New("development build has no release version; refusing to self-update")
	}

	offer, err := findOfferByID(ctx, u.db, offerID)
	if err != nil {
		return empty, fmt.Errorf("update offer %d is missing: %w", offerID, err)
	}
	if offer.Version != payloadVersion {
		return offer, fmt.Errorf("update offer %d records version %q but the job targets %q", offerID, offer.Version, payloadVersion)
	}

	// A failed attempt keeps the original approval: promote it back to
	// approved so the runner's retry (or a manual re-approval) can proceed
	// instead of every later attempt refusing with "not approved".
	if offer.Status == OfferStatusFailed {
		if strings.TrimSpace(offer.ApprovedBy) == "" {
			return offer, fmt.Errorf("%w (offer %d is failed and was never approved)", ErrOfferNotApproved, offerID)
		}
		offer, err = setOfferStatus(ctx, u.db, offer.ID, OfferStatusApproved, offer.ApprovedBy)
		if err != nil {
			return offer, fmt.Errorf("could not re-arm offer %d after a failed attempt: %w", offer.ID, err)
		}
	}

	// Reconciliation: the running binary already reports the target version,
	// so a previous attempt completed the swap but died before committing the
	// offer/job. Record applied without downloading, backing up, or swapping
	// again — a second swap would overwrite the .previous rollback copy with
	// the already-new binary. This also closes stale offers when the archive
	// was manually updated to (or past) the offered release.
	if version.Compare(u.CurrentVersion(), offer.Version) >= 0 {
		offer, err = setOfferStatus(ctx, u.db, offer.ID, OfferStatusApplied, "")
		if err != nil {
			return offer, fmt.Errorf("binary already up to date but recording the applied offer failed: %w", err)
		}
		slog.Info("self-update reconciled without a swap; running binary already reports the target", "target", offer.Version)

		return offer, nil
	}
	if offer.Status != OfferStatusApproved {
		return offer, fmt.Errorf("%w (offer %d status %q)", ErrOfferNotApproved, offerID, offer.Status)
	}

	// Re-fetch the release so assets reflect any corrected publish.
	release, err := u.config.fetchRelease(ctx, u.config.Repo, offer.Version)
	if err != nil {
		return offer, fmt.Errorf("could not fetch release %s: %w", offer.Version, err)
	}
	// Never swap content that does not belong to the offered tag: a bad
	// mirror could answer the by-tag request with a different release.
	if release.TagName != offer.Version {
		return offer, fmt.Errorf("release for tag %s reports %q; refusing a mismatched asset", offer.Version, release.TagName)
	}

	binaryPath, err := u.resolveBinaryPath()
	if err != nil {
		return offer, err
	}

	staged, cleanup, err := u.downloadVerifiedAsset(ctx, release, binaryPath)
	if err != nil {
		return offer, err
	}
	defer cleanup()

	backupPath, err := u.createPreUpdateBackup(ctx, offer.Version)
	if err != nil {
		return offer, fmt.Errorf("pre-update database backup failed: %w", err)
	}

	previousVersion := u.CurrentVersion()
	if err := swapBinary(binaryPath, staged); err != nil {
		return offer, err
	}

	offer, err = setOfferStatus(ctx, u.db, offer.ID, OfferStatusApplied, "")
	if err != nil {
		return offer, fmt.Errorf("binary replaced but recording the applied offer failed: %w", err)
	}
	offer.appliedBackupPath = backupPath
	offer.appliedPreviousVersion = previousVersion

	return offer, nil
}

func (u *Updater) resolveBinaryPath() (string, error) {
	if u.config.BinaryPath != "" {
		return u.config.BinaryPath, nil
	}
	executable, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("could not locate the running kapsel binary: %w", err)
	}
	resolved, err := filepath.EvalSymlinks(executable)
	if err != nil {
		return executable, nil
	}

	return resolved, nil
}

// downloadVerifiedAsset downloads the release asset for this platform into
// the data dir, verifies its SHA-256 against the release checksum sidecar,
// and returns the staged file path plus a cleanup func.
func (u *Updater) downloadVerifiedAsset(ctx context.Context, release *githubRelease, binaryPath string) (string, func(), error) {
	goos, goarch := platform()
	asset, err := release.selectAsset(goos, goarch)
	if err != nil {
		return "", nil, err
	}
	checksumAsset, err := release.selectChecksumAsset(asset.Name)
	if err != nil {
		return "", nil, err
	}
	if err := u.downloadURLAllowedFor(asset.BrowserDownloadURL); err != nil {
		return "", nil, err
	}
	if err := u.downloadURLAllowedFor(checksumAsset.BrowserDownloadURL); err != nil {
		return "", nil, err
	}
	checksumBody, err := u.downloadBytes(ctx, checksumAsset.BrowserDownloadURL, maxChecksumAssetBytes)
	if err != nil {
		return "", nil, fmt.Errorf("could not download %s: %w", checksumAsset.Name, err)
	}
	expected, err := expectedChecksum(string(checksumBody), asset.Name, checksumAsset.Name)
	if err != nil {
		return "", nil, err
	}

	stagingDir, err := u.ensureUpdatesDir()
	if err != nil {
		return "", nil, err
	}
	staged, err := u.downloadFile(ctx, asset.BrowserDownloadURL, stagingDir, maxUpdateAssetBytes, asset.Size)
	if err != nil {
		return "", nil, fmt.Errorf("could not download %s: %w", asset.Name, err)
	}
	digest, err := fileSHA256(staged)
	if err != nil {
		_ = os.Remove(staged)
		return "", nil, err
	}
	if !strings.EqualFold(digest, expected) {
		_ = os.Remove(staged)
		return "", nil, fmt.Errorf("checksum mismatch for %s: expected %s, got %s", asset.Name, expected, digest)
	}
	cleanup := func() { _ = os.Remove(staged) }

	return staged, cleanup, nil
}

// ensureUpdatesDir creates the staging directory under the data dir.
func (u *Updater) ensureUpdatesDir() (string, error) {
	if strings.TrimSpace(u.config.DataDir) == "" {
		return "", errors.New("data directory is required to stage update downloads")
	}
	dir := filepath.Join(u.config.DataDir, updatesDirName)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}

	return dir, nil
}

// downloadBytes fetches a small release file into memory with a size cap.
func (u *Updater) downloadBytes(ctx context.Context, downloadURL string, maxBytes int64) ([]byte, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, downloadURL, nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("User-Agent", "kapsel-self-update")
	response, err := u.github.client.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("download failed with status %d", response.StatusCode)
	}

	// Read one byte past the cap so over-cap responses are detected rather
	// than silently truncated, matching the JSON client's behaviour.
	body, err := io.ReadAll(io.LimitReader(response.Body, maxBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > maxBytes {
		return nil, fmt.Errorf("response from %s exceeds %d bytes", downloadURL, maxBytes)
	}

	return body, nil
}

// downloadFile streams a release asset to a temp file under dir with a hard
// size cap. The response size is checked before returning the path. When the
// release metadata declares an asset size, a short or oversized transfer is
// rejected: a connection cut mid-download must never stage a partial binary.
func (u *Updater) downloadFile(ctx context.Context, downloadURL string, dir string, maxBytes int64, expectedSize int64) (string, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, downloadURL, nil)
	if err != nil {
		return "", err
	}
	request.Header.Set("User-Agent", "kapsel-self-update")
	response, err := u.github.client.Do(request)
	if err != nil {
		return "", err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return "", fmt.Errorf("download failed with status %d", response.StatusCode)
	}

	file, err := os.CreateTemp(dir, "kapsel-update-*.bin")
	if err != nil {
		return "", err
	}
	path := file.Name()
	hasher := sha256.New()
	written, copyErr := io.Copy(io.MultiWriter(file, hasher), io.LimitReader(response.Body, maxBytes+1))
	syncErr := file.Sync()
	closeErr := file.Close()
	if copyErr != nil {
		_ = os.Remove(path)
		return "", copyErr
	}
	if syncErr != nil {
		_ = os.Remove(path)
		return "", syncErr
	}
	if closeErr != nil {
		_ = os.Remove(path)
		return "", closeErr
	}
	if written > maxBytes {
		_ = os.Remove(path)
		return "", fmt.Errorf("release asset exceeds %d bytes", maxBytes)
	}
	if expectedSize > 0 && written != expectedSize {
		_ = os.Remove(path)
		return "", fmt.Errorf("release asset transfer was incomplete: received %d of %d bytes", written, expectedSize)
	}

	return path, nil
}

func fileSHA256(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hasher := sha256.New()
	if _, err := io.Copy(hasher, file); err != nil {
		return "", err
	}

	return hex.EncodeToString(hasher.Sum(nil)), nil
}

// expectedChecksum extracts the recorded SHA-256 for assetName from a
// checksums sidecar. Shared sidecars use sha256sum format lines
// (<hex>[ *]<name>); per-asset sidecars may hold a bare digest.
func expectedChecksum(content string, assetName string, checksumFileName string) (string, error) {
	assetBase := strings.ToLower(filepath.Base(assetName))
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.SplitN(line, " ", 2)
		digest := strings.ToLower(strings.TrimSpace(fields[0]))
		if len(digest) != sha256.Size*2 || !isHex(digest) {
			continue
		}
		if len(fields) == 1 {
			// Per-asset sidecar with only a digest.
			if strings.Contains(strings.ToLower(checksumFileName), assetBase) {
				return digest, nil
			}
			continue
		}
		name := strings.TrimSpace(fields[1])
		name = strings.TrimPrefix(name, "*")
		name = strings.ReplaceAll(name, "\\", "/")
		nameBase := strings.ToLower(filepath.Base(name))
		if nameBase == assetBase || strings.ToLower(name) == assetBase {
			return digest, nil
		}
	}

	return "", fmt.Errorf("%s does not record a SHA-256 for %s", checksumFileName, assetName)
}

func isHex(value string) bool {
	for _, r := range value {
		if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F')) {
			return false
		}
	}

	return true
}

// createPreUpdateBackup snapshots the SQLite metadata so the admin can
// restore a pre-update database if a migration misbehaves. The capability is
// injected as Config.CreateBackup.
// preUpdateBackupKeep bounds how many pre-update snapshots pile up in the
// backups dir over the archive's lifetime; older ones are pruned after each
// successful backup.
const preUpdateBackupKeep = 5

// preUpdateBackupPrefix matches only the snapshots the update flow creates,
// so manually placed backups in the same directory are never touched.
const preUpdateBackupPrefix = "pre-update-"

func (u *Updater) createPreUpdateBackup(ctx context.Context, jobVersion string) (string, error) {
	if u.config.CreateBackup == nil {
		return "", errors.New("pre-update backup is not configured; refusing to self-update without one")
	}
	if strings.TrimSpace(u.config.DataDir) == "" {
		return "", errors.New("data directory is required for the pre-update backup")
	}
	backupDir := filepath.Join(u.config.DataDir, backupDirName)
	if err := os.MkdirAll(backupDir, 0o755); err != nil {
		return "", err
	}
	stamp := u.config.now().UTC().Format("20060102-150405")
	output := filepath.Join(backupDir, fmt.Sprintf("%s%s-%s.zip", preUpdateBackupPrefix, strings.TrimPrefix(strings.TrimSpace(jobVersion), "v"), stamp))
	metadata, err := u.config.CreateBackup(ctx, output)
	if err != nil {
		return "", err
	}
	// A snapshot only counts as usable when it is present, non-empty,
	// claims a real schema version, and actually contains the database
	// entry: a metadata-only or truncated zip must not be trusted as the
	// only rollback path before the binary is replaced.
	info, statErr := os.Stat(output)
	if statErr != nil {
		return "", fmt.Errorf("pre-update backup %s is missing after creation: %w", output, statErr)
	}
	if info.Size() == 0 {
		return "", fmt.Errorf("pre-update backup %s is empty; refusing to update without a usable snapshot", output)
	}
	if metadata.SchemaVersion <= 0 {
		return "", fmt.Errorf("pre-update backup %s reports no schema version; refusing to update without a usable snapshot", output)
	}
	if err := verifyBackupContainsDatabase(output); err != nil {
		return "", fmt.Errorf("pre-update backup %s is unusable: %w", output, err)
	}
	u.prunePreUpdateBackups(backupDir)
	slog.Info("pre-update backup created", "path", output, "schema_version", metadata.SchemaVersion)

	return output, nil
}

// verifyBackupContainsDatabase opens the backup zip and confirms the
// database snapshot entry the restore path reads is present.
func verifyBackupContainsDatabase(path string) error {
	parser, err := zip.OpenReader(path)
	if err != nil {
		return fmt.Errorf("not a readable zip: %w", err)
	}
	defer parser.Close()
	for _, entry := range parser.File {
		if strings.EqualFold(filepath.ToSlash(entry.Name), "kapsel.db") {
			return nil
		}
	}

	return errors.New("the kapsel.db database entry is missing")
}

// prunePreUpdateBackups deletes the oldest pre-update snapshots beyond
// preUpdateBackupKeep. Best-effort: a pruning failure is logged but must not
// abort an otherwise ready update.
func (u *Updater) prunePreUpdateBackups(backupDir string) {
	entries, err := os.ReadDir(backupDir)
	if err != nil {
		slog.Warn("could not list backups for pruning", "dir", backupDir, "error", err)

		return
	}
	type snapshot struct {
		name    string
		modTime time.Time
	}
	var snapshots []snapshot
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasPrefix(entry.Name(), preUpdateBackupPrefix) || !strings.HasSuffix(entry.Name(), ".zip") {
			continue
		}
		info, infoErr := entry.Info()
		if infoErr != nil {
			slog.Warn("could not stat backup for pruning", "path", filepath.Join(backupDir, entry.Name()), "error", infoErr)

			continue
		}
		snapshots = append(snapshots, snapshot{name: entry.Name(), modTime: info.ModTime()})
	}
	if len(snapshots) <= preUpdateBackupKeep {
		return
	}
	sort.Slice(snapshots, func(left, right int) bool { return snapshots[left].modTime.After(snapshots[right].modTime) })
	for _, stale := range snapshots[preUpdateBackupKeep:] {
		path := filepath.Join(backupDir, stale.name)
		if err := os.Remove(path); err != nil {
			slog.Warn("could not prune old pre-update backup", "path", path, "error", err)

			continue
		}
		slog.Info("pruned old pre-update backup", "path", path)
	}
}

// swapBinary replaces the running binary with the staged download. The
// previous binary is kept alongside as <binary>.previous for manual rollback.
//
// Crash safety: the current binary is copied (never renamed) to .previous
// first, so binaryPath stays intact; the replacement is then a single
// os.Rename over binaryPath, which atomically replaces the destination on
// every supported platform. There is no window — even across a power cut —
// where binaryPath is missing and the archive cannot restart.
func swapBinary(binaryPath string, stagedPath string) error {
	binaryDir := filepath.Dir(binaryPath)
	binaryName := filepath.Base(binaryPath)
	staged := filepath.Join(binaryDir, binaryName+".update")

	// Re-running against identical content is a no-op: a retried apply after
	// a crash between the swap and the offer commit must not overwrite the
	// .previous rollback copy with the already-new binary.
	same, err := sameFileContent(binaryPath, stagedPath)
	if err != nil {
		return err
	}
	if same {
		_ = os.Remove(stagedPath)
		return nil
	}

	// Copy (not rename) the staged file so the staging volume may differ
	// from the binary volume. The final rename stays within binaryDir.
	if err := copyExecutable(staged, stagedPath); err != nil {
		return err
	}
	if err := syncDir(binaryDir); err != nil {
		_ = os.Remove(staged)
		return err
	}

	previous := binaryPath + ".previous"
	// Copy the current binary aside BEFORE replacing it. Renaming the running
	// binary away first (the old approach) would leave binaryPath missing
	// between the two renames; a crash there makes the archive unstartable.
	if err := copyExecutable(previous, binaryPath); err != nil {
		_ = os.Remove(staged)
		return fmt.Errorf("could not preserve the previous binary (is there space for a second copy?): %w", err)
	}
	if err := syncDir(binaryDir); err != nil {
		return err
	}
	if err := os.Rename(staged, binaryPath); err != nil {
		// binaryPath still holds the original content: the rename either
		// happened or not, never half-way.
		_ = os.Remove(staged)
		return fmt.Errorf("could not replace the current binary: %w", err)
	}
	if err := syncDir(binaryDir); err != nil {
		return err
	}

	return nil
}

// sameFileContent reports whether two files exist and carry identical bytes.
// A missing first file (first apply on a fresh checkout) is not an error.
func sameFileContent(left string, right string) (bool, error) {
	leftInfo, err := os.Stat(left)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, err
	}
	rightInfo, err := os.Stat(right)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, err
	}
	if leftInfo.Size() != rightInfo.Size() {
		return false, nil
	}
	leftDigest, err := fileSHA256(left)
	if err != nil {
		return false, err
	}
	rightDigest, err := fileSHA256(right)
	if err != nil {
		return false, err
	}

	return strings.EqualFold(leftDigest, rightDigest), nil
}

func copyExecutable(destination string, source string) error {
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer in.Close()
	info, err := in.Stat()
	if err != nil {
		return err
	}
	out, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, info.Mode()&0o777|0o755)
	if err != nil {
		return fmt.Errorf("could not stage the new binary next to the current one (is the binary directory writable?): %w", err)
	}
	_, copyErr := io.Copy(out, in)
	syncErr := out.Sync()
	closeErr := out.Close()
	if copyErr != nil {
		_ = os.Remove(destination)
		return copyErr
	}
	if syncErr != nil {
		_ = os.Remove(destination)
		return syncErr
	}

	return closeErr
}

func syncDir(path string) error {
	dir, err := os.Open(path)
	if err != nil {
		return err
	}
	defer dir.Close()
	// Directory fsync is unsupported on some platforms; the renames above
	// already order the swap on filesystems that support it.
	_ = dir.Sync()

	return nil
}
