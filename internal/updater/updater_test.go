package updater

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"kapsel/internal/database"
	"kapsel/internal/jobs"
	"kapsel/internal/version"
)

// stampVersion sets the release version for the duration of the test. These
// tests mutate a package global, so they must not run in parallel.
func stampVersion(t *testing.T, value string) {
	t.Helper()
	original := version.Version
	version.Version = value
	t.Cleanup(func() { version.Version = original })
}

func newTestUpdater(t *testing.T, mutate func(*Config)) (*Updater, *jobs.Store, *sql.DB) {
	t.Helper()

	dbPath := filepath.Join(t.TempDir(), "kapsel.db")
	db, err := database.Open(context.Background(), dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := database.Migrate(context.Background(), db); err != nil {
		t.Fatal(err)
	}

	cfg := Config{
		Repo:           "mufflegyro/yummle",
		CurrentVersion: "v1.0.0",
		DataDir:        t.TempDir(),
		DBPath:         dbPath,
		CheckInterval:  24 * time.Hour,
		BinaryPath:     filepath.Join(t.TempDir(), "kapsel"),
	}
	if mutate != nil {
		mutate(&cfg)
	}

	updater := New(db, jobs.NewStore(db), cfg)
	return updater, updater.store, db
}

func mustRecordOffer(t *testing.T, db *sql.DB, offerVersion string) Offer {
	t.Helper()
	offer, _, err := recordDiscoveredUpdate(context.Background(), db, offerVersion, "https://github.com/mufflegyro/yummle/releases/tag/"+offerVersion, "notes for "+offerVersion, "2026-08-01T12:00:00Z")
	if err != nil {
		t.Fatal(err)
	}

	return offer
}

func writeTestBinary(t *testing.T, path string, content []byte) {
	t.Helper()
	if err := os.WriteFile(path, content, 0o755); err != nil {
		t.Fatal(err)
	}
}

// writeTestBackupZip writes the minimal zip shape the pre-update guard
// accepts: a kapsel.db database entry at the archive root.
func writeTestBackupZip(path string) error {
	file, err := os.Create(path)
	if err != nil {
		return err
	}
	defer file.Close()
	writer := zip.NewWriter(file)
	dbEntry, err := writer.Create("kapsel.db")
	if err != nil {
		_ = writer.Close()

		return err
	}
	if _, err := dbEntry.Write([]byte("sqlite database snapshot")); err != nil {
		_ = writer.Close()

		return err
	}

	return writer.Close()
}

// fakeReleaseServer serves one platform binary asset and its shared checksum
// sidecar. checksumOverride replaces the sidecar body (nil serves the true
// digest), which lets tests simulate corrupted mirrors.
func fakeReleaseServer(t *testing.T, binary []byte, checksumOverride []byte) *githubRelease {
	t.Helper()
	assetName := fmt.Sprintf("kapsel_%s_%s", runtime.GOOS, runtime.GOARCH)
	mux := http.NewServeMux()
	mux.HandleFunc("/kapsel.bin", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(binary)
	})
	mux.HandleFunc("/checksums.txt", func(w http.ResponseWriter, _ *http.Request) {
		if checksumOverride != nil {
			_, _ = w.Write(checksumOverride)

			return
		}
		sum := sha256.Sum256(binary)
		fmt.Fprintf(w, "%s  %s\n", hex.EncodeToString(sum[:]), assetName)
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	base := server.URL

	return &githubRelease{
		TagName:     "v1.1.0",
		HTMLURL:     "https://github.com/mufflegyro/yummle/releases/tag/v1.1.0",
		PublishedAt: "2026-08-01T12:00:00Z",
		Body:        "1.1.0",
		Assets: []githubAssetList{
			{Name: assetName, Size: int64(len(binary)), BrowserDownloadURL: base + "/kapsel.bin"},
			{Name: "checksums.txt", Size: 100, BrowserDownloadURL: base + "/checksums.txt"},
		},
	}
}

// newRunningCheckJob enqueues a real release_check row and claims it, mirroring
// production where handlers only ever see claimed (running) jobs.
func newRunningCheckJob(t *testing.T, store *jobs.Store) jobs.Job {
	t.Helper()
	ctx := context.Background()
	job, err := store.Enqueue(ctx, jobs.EnqueueParams{Type: ReleaseCheckJobType, PayloadJSON: "{}"})
	if err != nil {
		t.Fatal(err)
	}
	claimed, ok, err := store.Claim(ctx, time.Now(), time.Minute)
	if err != nil || !ok || claimed.ID != job.ID {
		t.Fatalf("claim check job: ok=%v err=%v", ok, err)
	}

	return claimed
}

// newRunningSelfUpdateJob enqueues a real self_update row and claims it
// `attempts` times so the job reports the attempt number a retried run would.
func newRunningSelfUpdateJob(t *testing.T, store *jobs.Store, offer Offer, attempts int) jobs.Job {
	t.Helper()
	ctx := context.Background()
	payload, err := json.Marshal(selfUpdatePayload{Version: offer.Version, OfferID: offer.ID})
	if err != nil {
		t.Fatal(err)
	}
	job, err := store.Enqueue(ctx, jobs.EnqueueParams{Type: SelfUpdateJobType, PayloadJSON: string(payload), MaxAttempts: 3})
	if err != nil {
		t.Fatal(err)
	}
	claimed := job
	for range attempts {
		claimed, ok, err := store.Claim(ctx, time.Now(), time.Minute)
		if err != nil || !ok || claimed.ID != job.ID {
			t.Fatalf("claim apply job: ok=%v err=%v", ok, err)
		}
	}

	return claimed
}

func claimQueuedJob(t *testing.T, store *jobs.Store, id string) jobs.Job {
	t.Helper()
	claimed, ok, err := store.Claim(context.Background(), time.Now(), time.Minute)
	if err != nil || !ok || claimed.ID != id {
		t.Fatalf("claim job %s: ok=%v err=%v", id, ok, err)
	}

	return claimed
}

func TestRecordDiscoveredUpdateIsIdempotent(t *testing.T) {
	t.Parallel()

	_, _, db := newTestUpdater(t, nil)
	first, created, err := recordDiscoveredUpdate(context.Background(), db, "v1.1.0", "url", "notes", "2026-08-01T12:00:00Z")
	if err != nil {
		t.Fatal(err)
	}
	if !created || first.Status != OfferStatusPending {
		t.Fatalf("first record: created=%v status=%q", created, first.Status)
	}

	second, created, err := recordDiscoveredUpdate(context.Background(), db, "v1.1.0", "url-updated", "notes updated", "2026-08-02T12:00:00Z")
	if err != nil {
		t.Fatal(err)
	}
	if created {
		t.Error("duplicate version must not create a second offer")
	}
	if second.ID != first.ID || second.ReleaseURL != "url" {
		t.Error("duplicate discovery must leave the original offer untouched")
	}

	if _, _, err := recordDiscoveredUpdate(context.Background(), db, "   ", "", "", ""); err == nil {
		t.Fatal("expected error for blank version")
	}
}

func TestSetOfferStatusTransitions(t *testing.T) {
	t.Parallel()

	_, _, db := newTestUpdater(t, nil)
	ctx := context.Background()

	offer := mustRecordOffer(t, db, "v1.1.0")

	approved, err := setOfferStatus(ctx, db, offer.ID, OfferStatusApproved, "admin")
	if err != nil {
		t.Fatal(err)
	}
	if approved.Status != OfferStatusApproved || approved.ApprovedBy != "admin" || approved.ApprovedAt == "" {
		t.Errorf("approved offer = %+v", approved)
	}

	// Applied only transitions from approved.
	if _, err := setOfferStatus(ctx, db, offer.ID, OfferStatusApplied, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := setOfferStatus(ctx, db, offer.ID, OfferStatusDismissed, ""); !errors.Is(err, ErrOfferNotPending) {
		t.Errorf("dismiss after apply = %v, want ErrOfferNotPending", err)
	}

	if _, err := setOfferStatus(ctx, db, 9999, OfferStatusApproved, "admin"); err == nil {
		t.Fatal("expected ErrOfferNotFound for a missing offer")
	}
}

func TestFailedOfferCanBeReApprovedAndReApplied(t *testing.T) {
	t.Parallel()

	_, _, db := newTestUpdater(t, nil)
	ctx := context.Background()

	offer := mustRecordOffer(t, db, "v1.1.0")
	if _, err := setOfferStatus(ctx, db, offer.ID, OfferStatusApproved, "admin"); err != nil {
		t.Fatal(err)
	}
	if _, err := setOfferFailed(ctx, db, offer.ID, "transient network error"); err != nil {
		t.Fatal(err)
	}

	// Re-approval after a failure re-authorizes the offer and clears the
	// stale error text; apply then transitions from approved again.
	reApproved, err := setOfferStatus(ctx, db, offer.ID, OfferStatusApproved, "admin")
	if err != nil {
		t.Fatalf("failed offers must be re-approvable: %v", err)
	}
	if reApproved.Status != OfferStatusApproved || reApproved.Error != "" {
		t.Errorf("re-approved offer = %+v", reApproved)
	}
	if _, err := setOfferStatus(ctx, db, offer.ID, OfferStatusApplied, ""); err != nil {
		t.Fatal(err)
	}
}

func TestSetOfferFailedFromApprovedRecordsCause(t *testing.T) {
	t.Parallel()

	_, _, db := newTestUpdater(t, nil)
	ctx := context.Background()

	offer := mustRecordOffer(t, db, "v1.1.0")
	if _, err := setOfferStatus(ctx, db, offer.ID, OfferStatusApproved, "admin"); err != nil {
		t.Fatal(err)
	}
	failed, err := setOfferFailed(ctx, db, offer.ID, "checksum mismatch")
	if err != nil {
		t.Fatal(err)
	}
	if failed.Status != OfferStatusFailed || failed.Error != "checksum mismatch" {
		t.Errorf("failed offer = %+v", failed)
	}

	// A failed offer without an approval history stays unauthorized.
	unapproved := mustRecordOffer(t, db, "v1.2.0")
	if _, err := setOfferFailed(ctx, db, unapproved.ID, "boom"); err != nil {
		t.Fatal(err)
	}
	if _, err := setOfferStatus(ctx, db, unapproved.ID, OfferStatusApplied, ""); !errors.Is(err, ErrOfferNotPending) {
		t.Errorf("apply from unapproved failure = %v, want ErrOfferNotPending", err)
	}
}

func TestLastReleaseCheckReadsJobs(t *testing.T) {
	t.Parallel()

	updater, _, db := newTestUpdater(t, nil)
	ctx := context.Background()

	check, err := lastReleaseCheck(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	if check != nil {
		t.Fatalf("expected nil check before any job ran, got %+v", check)
	}

	claimed := newRunningCheckJob(t, updater.store)
	if err := updater.commitCheckResult(ctx, claimed.ID, releaseCheckResult{
		CurrentVersion:  "v1.0.0",
		LatestVersion:   "v1.1.0",
		UpdateAvailable: true,
		OfferCreated:    true,
	}); err != nil {
		t.Fatal(err)
	}

	check, err = lastReleaseCheck(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	if check == nil {
		t.Fatal("expected a check after committing a result")
	}
	if check.JobID != claimed.ID || check.UpdateFound != true || check.FoundVersion != "v1.1.0" {
		t.Errorf("check = %+v", check)
	}
}

func TestHandleReleaseCheckCreatesOfferForNewerRelease(t *testing.T) {
	stampVersion(t, "v1.0.0")

	var fetchCalls int
	updater, _, db := newTestUpdater(t, func(cfg *Config) {
		cfg.fetchRelease = func(ctx context.Context, repo, tag string) (*githubRelease, error) {
			fetchCalls++
			if repo != "mufflegyro/yummle" || tag != "" {
				t.Errorf("fetchRelease(repo=%q, tag=%q)", repo, tag)
			}

			return &githubRelease{
				TagName:     "v1.1.0",
				HTMLURL:     "https://github.com/mufflegyro/yummle/releases/tag/v1.1.0",
				PublishedAt: "2026-08-01T12:00:00Z",
				Body:        "What's new in 1.1.0",
			}, nil
		}
	})

	if err := updater.HandleReleaseCheck(context.Background(), newRunningCheckJob(t, updater.store)); err != nil {
		t.Fatal(err)
	}
	if fetchCalls != 1 {
		t.Fatalf("fetchRelease called %d times", fetchCalls)
	}

	offer, err := findOfferByVersion(context.Background(), db, "v1.1.0")
	if err != nil {
		t.Fatal(err)
	}
	if offer.Status != OfferStatusPending || offer.ReleaseNotes != "What's new in 1.1.0" {
		t.Errorf("offer = %+v", offer)
	}

	check, err := lastReleaseCheck(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	if check == nil || check.UpdateFound != true || check.FoundVersion != "v1.1.0" {
		t.Fatalf("check = %+v", check)
	}
}

func TestHandleReleaseCheckIgnoresOlderRelease(t *testing.T) {
	stampVersion(t, "v2.0.0")

	updater, _, db := newTestUpdater(t, func(cfg *Config) {
		cfg.CurrentVersion = "v2.0.0"
		cfg.fetchRelease = func(ctx context.Context, repo, tag string) (*githubRelease, error) {
			return &githubRelease{TagName: "v1.1.0"}, nil
		}
	})

	if err := updater.HandleReleaseCheck(context.Background(), newRunningCheckJob(t, updater.store)); err != nil {
		t.Fatal(err)
	}
	if _, err := findOfferByVersion(context.Background(), db, "v1.1.0"); !errors.Is(err, ErrOfferNotFound) {
		t.Fatalf("err = %v, want ErrOfferNotFound", err)
	}

	check, err := lastReleaseCheck(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	if check == nil || check.UpdateFound != false {
		t.Fatalf("check = %+v", check)
	}
}

func TestHandleReleaseCheckSkipsDevBuilds(t *testing.T) {
	stampVersion(t, "dev")

	called := false
	updater, _, _ := newTestUpdater(t, func(cfg *Config) {
		cfg.fetchRelease = func(ctx context.Context, repo, tag string) (*githubRelease, error) {
			called = true

			return &githubRelease{TagName: "v9.9.9"}, nil
		}
	})

	if err := updater.HandleReleaseCheck(context.Background(), newRunningCheckJob(t, updater.store)); err != nil {
		t.Fatal(err)
	}
	if called {
		t.Error("dev builds must not call GitHub")
	}

	check, err := lastReleaseCheck(context.Background(), updater.db)
	if err != nil {
		t.Fatal(err)
	}
	if check == nil || check.Status != string(jobs.StatusSucceeded) || check.Detail == "" {
		t.Fatalf("check = %+v, want succeeded skip reason", check)
	}
}

func TestHandleReleaseCheckRecordsFetchFailure(t *testing.T) {
	stampVersion(t, "v1.0.0")

	updater, _, db := newTestUpdater(t, func(cfg *Config) {
		cfg.fetchRelease = func(ctx context.Context, repo, tag string) (*githubRelease, error) {
			return nil, errors.New("network down")
		}
	})

	if err := updater.HandleReleaseCheck(context.Background(), newRunningCheckJob(t, updater.store)); err == nil {
		t.Fatal("expected the fetch error to propagate")
	}

	check, err := lastReleaseCheck(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	if check == nil || !strings.Contains(check.Detail, "network down") {
		t.Fatalf("check = %+v, want failure detail", check)
	}
}

func TestApproveAndSelfUpdateEndToEnd(t *testing.T) {
	stampVersion(t, "v1.0.0")

	newBinary := []byte("#!/bin/sh\necho kapsel v1.1.0\n")
	release := fakeReleaseServer(t, newBinary, nil)

	restartRequested := false
	backupPath := ""
	updater, store, db := newTestUpdater(t, func(cfg *Config) {
		cfg.allowAnyDownloadURL = true
		cfg.Restart = func() { restartRequested = true }
		cfg.CreateBackup = func(ctx context.Context, output string) (BackupMetadata, error) {
			backupPath = output
			if err := writeTestBackupZip(output); err != nil {
				return BackupMetadata{}, err
			}

			return BackupMetadata{SchemaVersion: 16}, nil
		}
		cfg.fetchRelease = func(ctx context.Context, repo, tag string) (*githubRelease, error) {
			return release, nil
		}
	})

	// A release check discovers v1.1.0 and records a pending offer.
	if err := updater.HandleReleaseCheck(context.Background(), newRunningCheckJob(t, updater.store)); err != nil {
		t.Fatal(err)
	}
	offer, err := findOfferByVersion(context.Background(), db, "v1.1.0")
	if err != nil {
		t.Fatal(err)
	}
	if offer.Status != OfferStatusPending {
		t.Fatalf("offer status = %q, want pending", offer.Status)
	}

	// The admin approves; a self-update job is enqueued.
	approved, applyJob, created, err := updater.Approve(context.Background(), offer.ID, "admin")
	if err != nil {
		t.Fatal(err)
	}
	if approved.Status != OfferStatusApproved || !created {
		t.Fatalf("approved=%+v created=%v", approved, created)
	}

	// Write the current binary so the swap has something to replace.
	writeTestBinary(t, updater.config.BinaryPath, []byte("#!/bin/sh\necho kapsel v1.0.0\n"))

	// Mirror the runner: claim the queued apply job before the handler runs.
	applyJob = claimQueuedJob(t, updater.store, applyJob.ID)

	if err := updater.HandleSelfUpdate(context.Background(), applyJob); err != nil {
		t.Fatal(err)
	}
	if !restartRequested {
		t.Error("restart hook was not invoked after applying")
	}
	if backupPath == "" {
		t.Error("pre-update backup was not created")
	} else if info, statErr := os.Stat(backupPath); statErr != nil || info.Size() == 0 {
		t.Errorf("backup file missing or empty: %v", statErr)
	}

	applied, err := findOfferByID(context.Background(), db, offer.ID)
	if err != nil {
		t.Fatal(err)
	}
	if applied.Status != OfferStatusApplied || applied.AppliedAt == "" {
		t.Errorf("applied offer = %+v", applied)
	}

	replaced, err := os.ReadFile(updater.config.BinaryPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(replaced) != string(newBinary) {
		t.Error("binary was not replaced with the downloaded release")
	}
	previous, err := os.ReadFile(updater.config.BinaryPath + ".previous")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(previous), "v1.0.0") {
		t.Errorf("previous binary = %q, want the old content", previous)
	}

	stored, err := store.Get(context.Background(), applyJob.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != jobs.StatusSucceeded || stored.ResultJSON == "" {
		t.Errorf("stored job = %+v", stored)
	}
}

func TestBackupFailureAbortsUpdate(t *testing.T) {
	stampVersion(t, "v1.0.0")

	release := fakeReleaseServer(t, []byte("new binary"), nil)
	backupCalls := 0
	updater, _, db := newTestUpdater(t, func(cfg *Config) {
		cfg.allowAnyDownloadURL = true
		cfg.Restart = func() { t.Error("restart must not run when the backup fails") }
		cfg.CreateBackup = func(ctx context.Context, output string) (BackupMetadata, error) {
			backupCalls++

			return BackupMetadata{}, errors.New("disk full while writing backup")
		}
		cfg.fetchRelease = func(ctx context.Context, repo, tag string) (*githubRelease, error) {
			return release, nil
		}
	})

	offer := mustRecordOffer(t, db, "v1.1.0")
	if _, err := setOfferStatus(context.Background(), db, offer.ID, OfferStatusApproved, "admin"); err != nil {
		t.Fatal(err)
	}
	writeTestBinary(t, updater.config.BinaryPath, []byte("old binary"))

	err := updater.HandleSelfUpdate(context.Background(), newRunningSelfUpdateJob(t, updater.store, offer, 1))
	if err == nil || !strings.Contains(err.Error(), "disk full") {
		t.Fatalf("err = %v, want the backup failure", err)
	}
	if backupCalls != 1 {
		t.Errorf("backup attempted %d times", backupCalls)
	}

	// The archive must be untouched: same binary, no swap artifacts.
	current, readErr := os.ReadFile(updater.config.BinaryPath)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(current) != "old binary" {
		t.Error("binary must stay untouched when the backup fails")
	}
	if _, statErr := os.Stat(updater.config.BinaryPath + ".previous"); !errors.Is(statErr, os.ErrNotExist) {
		t.Errorf(".previous must not exist after an aborted update: %v", statErr)
	}
}

func TestBackupMustNotBeEmpty(t *testing.T) {
	stampVersion(t, "v1.0.0")

	release := fakeReleaseServer(t, []byte("new binary"), nil)
	updater, _, db := newTestUpdater(t, func(cfg *Config) {
		cfg.allowAnyDownloadURL = true
		cfg.CreateBackup = func(ctx context.Context, output string) (BackupMetadata, error) {
			return BackupMetadata{}, os.WriteFile(output, nil, 0o600)
		}
		cfg.fetchRelease = func(ctx context.Context, repo, tag string) (*githubRelease, error) {
			return release, nil
		}
	})

	offer := mustRecordOffer(t, db, "v1.1.0")
	if _, err := setOfferStatus(context.Background(), db, offer.ID, OfferStatusApproved, "admin"); err != nil {
		t.Fatal(err)
	}
	writeTestBinary(t, updater.config.BinaryPath, []byte("old binary"))

	if err := updater.HandleSelfUpdate(context.Background(), newRunningSelfUpdateJob(t, updater.store, offer, 1)); err == nil {
		t.Fatal("an empty backup must abort the update")
	}
	assertBinaryUntouched(t, updater.config.BinaryPath, "old binary")
}

func TestBackupWithoutSchemaVersionIsRejected(t *testing.T) {
	stampVersion(t, "v1.0.0")

	release := fakeReleaseServer(t, []byte("new binary"), nil)
	updater, _, db := newTestUpdater(t, func(cfg *Config) {
		cfg.allowAnyDownloadURL = true
		// A real zip whose metadata claims no schema version cannot be
		// trusted as a restore point.
		cfg.CreateBackup = func(ctx context.Context, output string) (BackupMetadata, error) {
			return BackupMetadata{}, writeTestBackupZip(output)
		}
		cfg.fetchRelease = func(ctx context.Context, repo, tag string) (*githubRelease, error) {
			return release, nil
		}
	})

	offer := mustRecordOffer(t, db, "v1.1.0")
	if _, err := setOfferStatus(context.Background(), db, offer.ID, OfferStatusApproved, "admin"); err != nil {
		t.Fatal(err)
	}
	writeTestBinary(t, updater.config.BinaryPath, []byte("old binary"))

	if err := updater.HandleSelfUpdate(context.Background(), newRunningSelfUpdateJob(t, updater.store, offer, 1)); err == nil {
		t.Fatal("a backup without a schema version must abort the update")
	}
	assertBinaryUntouched(t, updater.config.BinaryPath, "old binary")
}

func TestBackupMissingDatabaseEntryIsRejected(t *testing.T) {
	stampVersion(t, "v1.0.0")

	release := fakeReleaseServer(t, []byte("new binary"), nil)
	updater, _, db := newTestUpdater(t, func(cfg *Config) {
		cfg.allowAnyDownloadURL = true
		// A metadata-only zip is larger than zero bytes but holds no
		// database snapshot, so it is not a usable rollback path.
		cfg.CreateBackup = func(ctx context.Context, output string) (BackupMetadata, error) {
			file, createErr := os.Create(output)
			if createErr != nil {
				return BackupMetadata{}, createErr
			}
			writer := zip.NewWriter(file)
			entry, entryErr := writer.Create("metadata.json")
			if entryErr != nil {
				_ = writer.Close()
				_ = file.Close()

				return BackupMetadata{}, entryErr
			}
			_, _ = entry.Write([]byte(`{"schema_version":16}`))
			if closeErr := writer.Close(); closeErr != nil {
				_ = file.Close()

				return BackupMetadata{}, closeErr
			}

			return BackupMetadata{SchemaVersion: 16}, file.Close()
		}
		cfg.fetchRelease = func(ctx context.Context, repo, tag string) (*githubRelease, error) {
			return release, nil
		}
	})

	offer := mustRecordOffer(t, db, "v1.1.0")
	if _, err := setOfferStatus(context.Background(), db, offer.ID, OfferStatusApproved, "admin"); err != nil {
		t.Fatal(err)
	}
	writeTestBinary(t, updater.config.BinaryPath, []byte("old binary"))

	if err := updater.HandleSelfUpdate(context.Background(), newRunningSelfUpdateJob(t, updater.store, offer, 1)); err == nil {
		t.Fatal("a backup without the kapsel.db entry must abort the update")
	}
	assertBinaryUntouched(t, updater.config.BinaryPath, "old binary")
}

func TestPreUpdateBackupsArePruned(t *testing.T) {
	stampVersion(t, "v1.0.0")

	release := fakeReleaseServer(t, []byte("new binary"), nil)
	updater, _, db := newTestUpdater(t, func(cfg *Config) {
		cfg.allowAnyDownloadURL = true
		cfg.CreateBackup = func(ctx context.Context, output string) (BackupMetadata, error) {
			if err := writeTestBackupZip(output); err != nil {
				return BackupMetadata{}, err
			}

			return BackupMetadata{SchemaVersion: 16}, nil
		}
		cfg.fetchRelease = func(ctx context.Context, repo, tag string) (*githubRelease, error) {
			return release, nil
		}
	})

	offer := mustRecordOffer(t, db, "v1.1.0")
	if _, err := setOfferStatus(context.Background(), db, offer.ID, OfferStatusApproved, "admin"); err != nil {
		t.Fatal(err)
	}
	writeTestBinary(t, updater.config.BinaryPath, []byte("old binary"))

	// Seed retention overflow: seven older snapshots plus an unrelated file
	// that pruning must never touch. Stagger mod times so ordering is
	// deterministic even within one second.
	backupDir := filepath.Join(updater.config.DataDir, backupDirName)
	if err := os.MkdirAll(backupDir, 0o755); err != nil {
		t.Fatal(err)
	}
	base := time.Now().Add(-time.Hour)
	for index := range 7 {
		path := filepath.Join(backupDir, fmt.Sprintf("pre-update-1.0.0-%02d.zip", index))
		if err := os.WriteFile(path, []byte("stale"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(path, base.Add(time.Duration(index)*time.Minute), base.Add(time.Duration(index)*time.Minute)); err != nil {
			t.Fatal(err)
		}
	}
	unrelated := filepath.Join(backupDir, "manual-backup.zip")
	if err := os.WriteFile(unrelated, []byte("keep me"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := updater.HandleSelfUpdate(context.Background(), newRunningSelfUpdateJob(t, updater.store, offer, 1)); err != nil {
		t.Fatal(err)
	}

	entries, readErr := os.ReadDir(backupDir)
	if readErr != nil {
		t.Fatal(readErr)
	}
	var remainingPre []string
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), "pre-update-") {
			remainingPre = append(remainingPre, entry.Name())
		}
	}
	if len(remainingPre) != 5 {
		t.Fatalf("expected 5 pre-update backups after pruning, got %d: %v", len(remainingPre), remainingPre)
	}
	// The just-created snapshot must be among the survivors.
	survivedNewest := false
	for _, name := range remainingPre {
		if strings.Contains(name, "1.1.0") {
			survivedNewest = true
		}
	}
	if !survivedNewest {
		t.Errorf("the new backup was pruned: %v", remainingPre)
	}
	if _, statErr := os.Stat(unrelated); statErr != nil {
		t.Errorf("unrelated backup was removed: %v", statErr)
	}
}

func assertBinaryUntouched(t *testing.T, path string, want string) {
	t.Helper()
	current, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(current) != want {
		t.Errorf("binary = %q, want the untouched %q", current, want)
	}
}

func TestGitHubUnreachableKeepsBinaryAndAllowsRetry(t *testing.T) {
	stampVersion(t, "v1.0.0")

	fetchCalls := 0
	updater, _, db := newTestUpdater(t, func(cfg *Config) {
		cfg.allowAnyDownloadURL = true
		cfg.CreateBackup = func(ctx context.Context, output string) (BackupMetadata, error) {
			// Only the attempt whose fetch failed must never reach backup;
			// the recovered attempt takes a real pre-update backup.
			if fetchCalls <= 1 {
				t.Error("backup must not run when the release cannot be fetched")

				return BackupMetadata{}, errors.New("unreachable")
			}
			if err := writeTestBackupZip(output); err != nil {
				return BackupMetadata{}, err
			}

			return BackupMetadata{SchemaVersion: 16}, nil
		}
		cfg.fetchRelease = func(ctx context.Context, repo, tag string) (*githubRelease, error) {
			fetchCalls++
			if fetchCalls == 1 {
				return nil, errors.New("dial tcp: connection refused")
			}

			return fakeReleaseServer(t, []byte("recovered binary"), nil), nil
		}
	})

	offer := mustRecordOffer(t, db, "v1.1.0")
	if _, err := setOfferStatus(context.Background(), db, offer.ID, OfferStatusApproved, "admin"); err != nil {
		t.Fatal(err)
	}
	writeTestBinary(t, updater.config.BinaryPath, []byte("old binary"))

	// Attempt 1: GitHub unreachable. The failure is recorded but the
	// approval must survive so the runner's retry can proceed.
	job := newRunningSelfUpdateJob(t, updater.store, offer, 1)
	err1 := updater.HandleSelfUpdate(context.Background(), job)
	if err1 == nil {
		t.Fatal("attempt 1 should fail while GitHub is unreachable")
	}
	afterFailure, err := findOfferByID(context.Background(), db, offer.ID)
	if err != nil {
		t.Fatal(err)
	}
	if afterFailure.Status != OfferStatusFailed || afterFailure.ApprovedBy != "admin" {
		t.Fatalf("offer after failure = %+v, want failed with the approval kept", afterFailure)
	}
	current, readErr := os.ReadFile(updater.config.BinaryPath)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(current) != "old binary" {
		t.Error("binary must stay untouched when GitHub is unreachable")
	}

	// Attempt 2: connectivity recovered; the runner re-queues the failed
	// job and the offer is re-armed automatically.
	if failErr := updater.store.Fail(context.Background(), job.ID, err1, time.Now()); failErr != nil {
		t.Fatalf("runner requeue: %v", failErr)
	}
	reclaimed := claimQueuedJob(t, updater.store, job.ID)
	if err := updater.HandleSelfUpdate(context.Background(), reclaimed); err != nil {
		t.Fatal(err)
	}
	applied, err := findOfferByID(context.Background(), db, offer.ID)
	if err != nil {
		t.Fatal(err)
	}
	if applied.Status != OfferStatusApplied {
		t.Errorf("offer after retry = %+v, want applied", applied)
	}
}

func TestDownloadCutMidStreamAbortsCleanly(t *testing.T) {
	stampVersion(t, "v1.0.0")

	fullBinary := []byte(strings.Repeat("kapsel", 4096)) // 24 KiB, half-sent below
	release := fakeReleaseServer(t, fullBinary, nil)
	// Replace the asset URL with a server that cuts the connection halfway.
	cut := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hijacker, ok := w.(http.Hijacker)
		if !ok {
			t.Fatal("cannot hijack")
		}
		conn, _, err := hijacker.Hijack()
		if err != nil {
			t.Fatal(err)
		}
		_, _ = conn.Write([]byte("HTTP/1.1 200 OK\r\nContent-Length: 999999\r\n\r\n"))
		_, _ = conn.Write(fullBinary[:len(fullBinary)/2])
		_ = conn.Close() // cut mid-body
	}))
	t.Cleanup(cut.Close)
	for index := range release.Assets {
		if strings.HasPrefix(release.Assets[index].Name, "kapsel_") {
			release.Assets[index].BrowserDownloadURL = cut.URL + "/kapsel.bin"
		}
	}

	updater, _, db := newTestUpdater(t, func(cfg *Config) {
		cfg.allowAnyDownloadURL = true
		cfg.CreateBackup = func(ctx context.Context, output string) (BackupMetadata, error) {
			t.Error("backup must not run when the download fails")
			return BackupMetadata{}, errors.New("unreachable")
		}
		cfg.fetchRelease = func(ctx context.Context, repo, tag string) (*githubRelease, error) {
			return release, nil
		}
	})

	offer := mustRecordOffer(t, db, "v1.1.0")
	if _, err := setOfferStatus(context.Background(), db, offer.ID, OfferStatusApproved, "admin"); err != nil {
		t.Fatal(err)
	}
	writeTestBinary(t, updater.config.BinaryPath, []byte("old binary"))

	if err := updater.HandleSelfUpdate(context.Background(), newRunningSelfUpdateJob(t, updater.store, offer, 1)); err == nil {
		t.Fatal("a mid-download connection cut must fail the update")
	}

	current, readErr := os.ReadFile(updater.config.BinaryPath)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(current) != "old binary" {
		t.Error("binary must stay untouched when the download is cut")
	}

	// The staging dir must not accumulate partial downloads.
	entries, dirErr := os.ReadDir(filepath.Join(updater.config.DataDir, updatesDirName))
	if dirErr == nil {
		for _, entry := range entries {
			if strings.HasSuffix(entry.Name(), ".bin") {
				t.Errorf("partial download %s was left behind", entry.Name())
			}
		}
	}
}

func TestSelfUpdateReconcilesAfterCrashBetweenSwapAndCommit(t *testing.T) {
	// Simulate: the swap completed, the process died, and the restarted
	// process (now the new binary) re-runs the recovered job.
	stampVersion(t, "v1.1.0")

	updater, _, db := newTestUpdater(t, func(cfg *Config) {
		cfg.CurrentVersion = "v1.1.0"
		cfg.CreateBackup = func(ctx context.Context, output string) (BackupMetadata, error) {
			t.Error("reconciliation must not create a second backup")
			return BackupMetadata{}, errors.New("unreachable")
		}
		cfg.fetchRelease = func(ctx context.Context, repo, tag string) (*githubRelease, error) {
			t.Error("reconciliation must not download anything")
			return nil, errors.New("unreachable")
		}
	})

	offer := mustRecordOffer(t, db, "v1.1.0")
	if _, err := setOfferStatus(context.Background(), db, offer.ID, OfferStatusApproved, "admin"); err != nil {
		t.Fatal(err)
	}
	writeTestBinary(t, updater.config.BinaryPath, []byte("already new"))
	writeTestBinary(t, updater.config.BinaryPath+".previous", []byte("true rollback copy"))

	if err := updater.HandleSelfUpdate(context.Background(), newRunningSelfUpdateJob(t, updater.store, offer, 1)); err != nil {
		t.Fatal(err)
	}

	applied, err := findOfferByID(context.Background(), db, offer.ID)
	if err != nil {
		t.Fatal(err)
	}
	if applied.Status != OfferStatusApplied || applied.AppliedAt == "" {
		t.Errorf("reconciled offer = %+v", applied)
	}
	current, readErr := os.ReadFile(updater.config.BinaryPath)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(current) != "already new" {
		t.Error("reconciliation must not touch the binary")
	}
	previous, readErr := os.ReadFile(updater.config.BinaryPath + ".previous")
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(previous) != "true rollback copy" {
		t.Errorf(".previous was overwritten: %q", previous)
	}
}

func TestSelfUpdateRejectsDowngradeAndClosesStaleOffer(t *testing.T) {
	stampVersion(t, "v2.0.0")

	updater, _, db := newTestUpdater(t, func(cfg *Config) {
		cfg.CurrentVersion = "v2.0.0"
		cfg.CreateBackup = func(ctx context.Context, output string) (BackupMetadata, error) {
			t.Error("no backup is needed when the offer is already superseded")
			return BackupMetadata{}, errors.New("unreachable")
		}
		cfg.fetchRelease = func(ctx context.Context, repo, tag string) (*githubRelease, error) {
			t.Error("no download is needed when the offer is already superseded")
			return nil, errors.New("unreachable")
		}
	})

	offer := mustRecordOffer(t, db, "v1.1.0")
	if _, err := setOfferStatus(context.Background(), db, offer.ID, OfferStatusApproved, "admin"); err != nil {
		t.Fatal(err)
	}
	writeTestBinary(t, updater.config.BinaryPath, []byte("v2 binary"))

	// The offer is older than the running release: applying it would be a
	// downgrade. The pipeline must close it without swapping anything.
	if err := updater.HandleSelfUpdate(context.Background(), newRunningSelfUpdateJob(t, updater.store, offer, 1)); err != nil {
		t.Fatal(err)
	}
	closed, err := findOfferByID(context.Background(), db, offer.ID)
	if err != nil {
		t.Fatal(err)
	}
	if closed.Status != OfferStatusApplied {
		t.Errorf("stale offer status = %q, want applied", closed.Status)
	}
	current, readErr := os.ReadFile(updater.config.BinaryPath)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(current) != "v2 binary" {
		t.Error("the running release must never be downgraded")
	}
}

func TestSelfUpdateRejectsTagMismatch(t *testing.T) {
	stampVersion(t, "v1.0.0")

	release := fakeReleaseServer(t, []byte("wrong release content"), nil)
	release.TagName = "v9.9.9" // mirror answers with a different release
	updater, _, db := newTestUpdater(t, func(cfg *Config) {
		cfg.allowAnyDownloadURL = true
		cfg.CreateBackup = func(ctx context.Context, output string) (BackupMetadata, error) {
			t.Error("backup must not run for a mismatched release")
			return BackupMetadata{}, errors.New("unreachable")
		}
		cfg.fetchRelease = func(ctx context.Context, repo, tag string) (*githubRelease, error) {
			return release, nil
		}
	})

	offer := mustRecordOffer(t, db, "v1.1.0")
	if _, err := setOfferStatus(context.Background(), db, offer.ID, OfferStatusApproved, "admin"); err != nil {
		t.Fatal(err)
	}
	writeTestBinary(t, updater.config.BinaryPath, []byte("old binary"))

	err := updater.HandleSelfUpdate(context.Background(), newRunningSelfUpdateJob(t, updater.store, offer, 1))
	if err == nil || !strings.Contains(err.Error(), "mismatch") {
		t.Fatalf("err = %v, want a tag mismatch failure", err)
	}
	current, readErr := os.ReadFile(updater.config.BinaryPath)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(current) != "old binary" {
		t.Error("a mismatched release must never be swapped in")
	}
}

func TestSelfUpdateRefusesUnapprovedOffer(t *testing.T) {
	stampVersion(t, "v1.0.0")

	updater, _, db := newTestUpdater(t, func(cfg *Config) {
		cfg.allowAnyDownloadURL = true
		cfg.CreateBackup = func(ctx context.Context, output string) (BackupMetadata, error) {
			return BackupMetadata{}, errors.New("backup must not run")
		}
		cfg.fetchRelease = func(ctx context.Context, repo, tag string) (*githubRelease, error) {
			return &githubRelease{TagName: "v1.1.0"}, nil
		}
	})

	offer := mustRecordOffer(t, db, "v1.1.0")

	err := updater.HandleSelfUpdate(context.Background(), newRunningSelfUpdateJob(t, updater.store, offer, 1))
	if err == nil || !strings.Contains(err.Error(), ErrOfferNotApproved.Error()) {
		t.Fatalf("err = %v, want %v", err, ErrOfferNotApproved)
	}

	failed, findErr := findOfferByID(context.Background(), db, offer.ID)
	if findErr != nil {
		t.Fatal(findErr)
	}
	if failed.Status != OfferStatusFailed {
		t.Errorf("offer status = %q, want failed", failed.Status)
	}
}

func TestSelfUpdateDetectsChecksumMismatch(t *testing.T) {
	stampVersion(t, "v1.0.0")

	release := fakeReleaseServer(t, []byte("new binary bytes"), []byte(strings.Repeat("a", 64)+"  kapsel.bin\n"))
	updater, _, db := newTestUpdater(t, func(cfg *Config) {
		cfg.allowAnyDownloadURL = true
		cfg.CreateBackup = func(ctx context.Context, output string) (BackupMetadata, error) {
			return BackupMetadata{}, errors.New("backup must not run before verification")
		}
		cfg.fetchRelease = func(ctx context.Context, repo, tag string) (*githubRelease, error) {
			return release, nil
		}
	})

	offer := mustRecordOffer(t, db, "v1.1.0")
	if _, err := setOfferStatus(context.Background(), db, offer.ID, OfferStatusApproved, "admin"); err != nil {
		t.Fatal(err)
	}

	writeTestBinary(t, updater.config.BinaryPath, []byte("old binary"))

	if err := updater.HandleSelfUpdate(context.Background(), newRunningSelfUpdateJob(t, updater.store, offer, 1)); err == nil {
		t.Fatal("expected checksum mismatch failure")
	}

	failed, findErr := findOfferByID(context.Background(), db, offer.ID)
	if findErr != nil {
		t.Fatal(findErr)
	}
	if failed.Status != OfferStatusFailed || !strings.Contains(failed.Error, "checksum") {
		t.Errorf("failed offer = %+v", failed)
	}
	current, readErr := os.ReadFile(updater.config.BinaryPath)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(current) != "old binary" {
		t.Error("binary must stay untouched when verification fails")
	}
}

func TestSwapBinaryPreservesPreviousAndIsIdempotent(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	binaryPath := filepath.Join(dir, "kapsel")
	writeTestBinary(t, binaryPath, []byte("version 1"))

	staged := filepath.Join(dir, "staged")
	writeTestBinary(t, staged, []byte("version 2"))

	if err := swapBinary(binaryPath, staged); err != nil {
		t.Fatal(err)
	}
	replaced, err := os.ReadFile(binaryPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(replaced) != "version 2" {
		t.Errorf("binary = %q, want the staged content", replaced)
	}
	previous, err := os.ReadFile(binaryPath + ".previous")
	if err != nil {
		t.Fatal(err)
	}
	if string(previous) != "version 1" {
		t.Errorf(".previous = %q, want the original content", previous)
	}
	if _, err := os.Stat(staged); err != nil {
		t.Errorf("staged source must survive swapBinary (caller cleans up): %v", err)
	}

	// A retried swap against identical content is a no-op and must not
	// overwrite the .previous rollback copy. The no-op path removes the
	// staged copy, so recreate it first.
	writeTestBinary(t, staged, []byte("version 2"))
	if err := swapBinary(binaryPath, staged); err != nil {
		t.Fatal(err)
	}
	previous, err = os.ReadFile(binaryPath + ".previous")
	if err != nil {
		t.Fatal(err)
	}
	if string(previous) != "version 1" {
		t.Errorf("idempotent re-swap clobbered .previous: %q", previous)
	}
}

func TestEnsureReleaseCheckJobsRespectsInterval(t *testing.T) {
	updater, _, _ := newTestUpdater(t, func(cfg *Config) {
		cfg.CheckInterval = 0
	})
	count, err := updater.EnsureReleaseCheckJobs(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("disabled scheduler enqueued %d jobs", count)
	}

	updater, _, _ = newTestUpdater(t, func(cfg *Config) {
		cfg.CheckInterval = 24 * time.Hour
	})
	count, err = updater.EnsureReleaseCheckJobs(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("first run enqueued %d jobs, want 1", count)
	}
	count, err = updater.EnsureReleaseCheckJobs(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("second run inside the interval enqueued %d jobs", count)
	}
}

func TestCheckNowDedupesActiveJobs(t *testing.T) {
	updater, _, _ := newTestUpdater(t, nil)
	ctx := context.Background()

	if _, created, err := updater.CheckNow(ctx); err != nil || !created {
		t.Fatalf("first CheckNow: created=%v err=%v", created, err)
	}
	if _, created, err := updater.CheckNow(ctx); err != nil || created {
		t.Fatalf("second CheckNow: created=%v err=%v", created, err)
	}
}

func TestEnsureReleaseCheckJobsThrottlesSuccessfulChecks(t *testing.T) {
	updater, _, _ := newTestUpdater(t, func(cfg *Config) {
		cfg.CheckInterval = 24 * time.Hour
	})
	ctx := context.Background()

	if count, err := updater.EnsureReleaseCheckJobs(ctx); err != nil || count != 1 {
		t.Fatalf("first run: count=%d err=%v", count, err)
	}
	// Claim with the live clock, sampled after the enqueue: the job's
	// run_after is the scheduler's now() with a nanosecond fraction, and
	// the RFC3339_NANO collation orders timestamps numerically, so the
	// claim time must be at or after it. (A truncated-second claim time
	// used to work only because BINARY order made ".fractionZ" < "Z".)
	start := time.Now().UTC()
	claimed, ok, err := updater.store.Claim(ctx, start, time.Minute)
	if err != nil || !ok {
		t.Fatalf("claim: ok=%v err=%v", ok, err)
	}
	if err := updater.store.Complete(ctx, claimed.ID); err != nil {
		t.Fatal(err)
	}

	// A completed check inside the interval paces the next one regardless
	// of the outcome.
	updater.config.now = func() time.Time { return start.Add(time.Hour) }
	if count, err := updater.EnsureReleaseCheckJobs(ctx); err != nil || count != 0 {
		t.Fatalf("inside interval: count=%d err=%v", count, err)
	}
	updater.config.now = func() time.Time { return start.Add(25 * time.Hour) }
	if count, err := updater.EnsureReleaseCheckJobs(ctx); err != nil || count != 1 {
		t.Fatalf("past interval: count=%d err=%v", count, err)
	}
}

func TestEnsureReleaseCheckJobsBacksOffFailures(t *testing.T) {
	updater, _, _ := newTestUpdater(t, func(cfg *Config) {
		cfg.CheckInterval = 24 * time.Hour
	})
	ctx := context.Background()

	// One exhausted check attempt: 15m backoff before the scheduler tries
	// again instead of re-enqueuing on every hourly tick.
	if _, err := updater.store.Enqueue(ctx, jobs.EnqueueParams{Type: ReleaseCheckJobType, PayloadJSON: "{}", MaxAttempts: 1}); err != nil {
		t.Fatal(err)
	}
	// Claim after the enqueue with the live clock — the job's run_after
	// carries a nanosecond fraction and the RFC3339_NANO collation orders
	// timestamps numerically (see the throttling test).
	start := time.Now().UTC()
	claimed, ok, err := updater.store.Claim(ctx, start, time.Minute)
	if err != nil || !ok {
		t.Fatalf("claim: ok=%v err=%v", ok, err)
	}
	if err := updater.store.Fail(ctx, claimed.ID, errors.New("403 rate limited"), start.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}

	updater.config.now = func() time.Time { return start.Add(time.Minute) }
	if count, err := updater.EnsureReleaseCheckJobs(ctx); err != nil || count != 0 {
		t.Fatalf("inside first backoff: count=%d err=%v", count, err)
	}
	updater.config.now = func() time.Time { return start.Add(16 * time.Minute) }
	if count, err := updater.EnsureReleaseCheckJobs(ctx); err != nil || count != 1 {
		t.Fatalf("past first backoff: count=%d err=%v", count, err)
	}

	// Two consecutive exhausted attempts double the wait to 30m.
	claimed, ok, err = updater.store.Claim(ctx, start.Add(16*time.Minute), time.Minute)
	if err != nil || !ok {
		t.Fatalf("claim 2: ok=%v err=%v", ok, err)
	}
	if err := updater.store.Fail(ctx, claimed.ID, errors.New("403 rate limited"), start.Add(16*time.Minute)); err != nil {
		t.Fatal(err)
	}
	updater.config.now = func() time.Time { return start.Add(20 * time.Minute) }
	if count, err := updater.EnsureReleaseCheckJobs(ctx); err != nil || count != 0 {
		t.Fatalf("inside doubled backoff: count=%d err=%v", count, err)
	}
	updater.config.now = func() time.Time { return start.Add(31 * time.Minute) }
	if count, err := updater.EnsureReleaseCheckJobs(ctx); err != nil || count != 1 {
		t.Fatalf("past doubled backoff: count=%d err=%v", count, err)
	}
}

func TestFailedCheckBackoffCapsAtInterval(t *testing.T) {
	t.Parallel()

	interval := 24 * time.Hour
	if got := failedCheckBackoff(interval, 1); got != 15*time.Minute {
		t.Errorf("streak 1 = %v, want 15m", got)
	}
	if got := failedCheckBackoff(interval, 2); got != 30*time.Minute {
		t.Errorf("streak 2 = %v, want 30m", got)
	}
	if got := failedCheckBackoff(interval, 4); got != 2*time.Hour {
		t.Errorf("streak 4 = %v, want 2h", got)
	}
	if got := failedCheckBackoff(interval, 100); got != interval {
		t.Errorf("streak 100 = %v, want the interval cap", got)
	}
	// A short interval always wins: backoff never exceeds normal pacing.
	if got := failedCheckBackoff(10*time.Minute, 3); got != 10*time.Minute {
		t.Errorf("short interval = %v, want the interval cap", got)
	}
}

func TestFormatCheckInterval(t *testing.T) {
	t.Parallel()

	cases := []struct {
		seconds  int64
		expected string
	}{
		{0, "disabled"},
		{-5, "disabled"},
		{86400, "24h"},
		{5400, "1h30m"},
		{900, "15m"},
		{45, "45s"},
	}
	for _, test := range cases {
		if got := formatCheckInterval(test.seconds); got != test.expected {
			t.Errorf("formatCheckInterval(%d) = %q, want %q", test.seconds, got, test.expected)
		}
	}
}

func TestDownloadBytesRejectsOverCapResponses(t *testing.T) {
	updater, _, _ := newTestUpdater(t, nil)

	large := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(bytes.Repeat([]byte("x"), 64))
	}))
	t.Cleanup(large.Close)
	if _, err := updater.downloadBytes(context.Background(), large.URL, 10); err == nil {
		t.Fatal("a body larger than maxBytes must be rejected, not truncated")
	}

	exact := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(bytes.Repeat([]byte("x"), 10))
	}))
	t.Cleanup(exact.Close)
	body, err := updater.downloadBytes(context.Background(), exact.URL, 10)
	if err != nil {
		t.Fatalf("a body exactly maxBytes long must be accepted: %v", err)
	}
	if len(body) != 10 {
		t.Errorf("body length = %d, want 10", len(body))
	}
}

func TestApproveFailedOfferReAuthorizesAndEnqueues(t *testing.T) {
	stampVersion(t, "v1.0.0")

	updater, _, db := newTestUpdater(t, nil)

	offer := mustRecordOffer(t, db, "v1.1.0")
	if _, err := setOfferStatus(context.Background(), db, offer.ID, OfferStatusApproved, "admin"); err != nil {
		t.Fatal(err)
	}
	if _, err := setOfferFailed(context.Background(), db, offer.ID, "network unreachable"); err != nil {
		t.Fatal(err)
	}

	reApproved, _, _, err := updater.Approve(context.Background(), offer.ID, "admin")
	if err != nil {
		t.Fatalf("a failed offer must be re-approvable: %v", err)
	}
	if reApproved.Status != OfferStatusApproved || reApproved.Error != "" {
		t.Errorf("re-approved offer = %+v", reApproved)
	}
}
