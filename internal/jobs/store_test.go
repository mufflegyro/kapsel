package jobs

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"kapsel/internal/database"
)

func TestEnqueuePersistsAcrossReopen(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "kapsel.db")
	db := openJobsDB(t, path)
	store := NewStore(db)

	job, err := store.Enqueue(context.Background(), EnqueueParams{Type: "download", PayloadJSON: `{"id":"abc"}`})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	reopened := openJobsDB(t, path)
	stored, err := NewStore(reopened).Get(context.Background(), job.ID)
	if err != nil {
		t.Fatal(err)
	}

	if stored.Type != "download" || stored.PayloadJSON != `{"id":"abc"}` || stored.Status != StatusQueued {
		t.Fatalf("unexpected stored job: %#v", stored)
	}
}

func TestEnqueueTxRollsBackWithTransaction(t *testing.T) {
	t.Parallel()

	db := openJobsDB(t, filepath.Join(t.TempDir(), "kapsel.db"))
	store := NewStore(db)
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	job, err := store.EnqueueTx(context.Background(), tx, EnqueueParams{Type: "download", PayloadJSON: `{"id":"rollback"}`})
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}

	if _, err := store.Get(context.Background(), job.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected rolled back job to be absent, got %v", err)
	}
}

func TestActiveByPayloadFindsQueuedAndRunningJobs(t *testing.T) {
	t.Parallel()

	store := NewStore(openJobsDB(t, filepath.Join(t.TempDir(), "kapsel.db")))
	queued, err := store.Enqueue(context.Background(), EnqueueParams{Type: "download", PayloadJSON: `{"url":"https://example.test/one"}`})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Enqueue(context.Background(), EnqueueParams{Type: "download", PayloadJSON: `{"url":"https://example.test/two"}`}); err != nil {
		t.Fatal(err)
	}

	found, ok, err := store.ActiveByPayload(context.Background(), "download", `{"url":"https://example.test/one"}`)
	if err != nil {
		t.Fatal(err)
	}
	if !ok || found.ID != queued.ID {
		t.Fatalf("expected queued payload job, ok=%v job=%#v", ok, found)
	}
	active, err := store.ActiveByType(context.Background(), "download", 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(active) != 1 || active[0].ID != queued.ID {
		t.Fatalf("expected bounded active jobs by type, got %#v", active)
	}
	claimed, ok, err := store.Claim(context.Background(), futureClaimTime(), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if !ok || claimed.ID != queued.ID {
		t.Fatalf("expected first job claim, ok=%v job=%#v", ok, claimed)
	}
	found, ok, err = store.ActiveByPayload(context.Background(), "download", `{"url":"https://example.test/one"}`)
	if err != nil {
		t.Fatal(err)
	}
	if !ok || found.ID != queued.ID || found.Status != StatusRunning {
		t.Fatalf("expected running payload job, ok=%v job=%#v", ok, found)
	}
	if err := store.Complete(context.Background(), queued.ID); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := store.ActiveByPayload(context.Background(), "download", `{"url":"https://example.test/one"}`); err != nil || ok {
		t.Fatalf("expected completed job to be ignored, ok=%v err=%v", ok, err)
	}
}

func TestClaimCompleteAndRetry(t *testing.T) {
	t.Parallel()

	store := NewStore(openJobsDB(t, filepath.Join(t.TempDir(), "kapsel.db")))
	job, err := store.Enqueue(context.Background(), EnqueueParams{Type: "scan", MaxAttempts: 2})
	if err != nil {
		t.Fatal(err)
	}

	claimed, ok, err := store.Claim(context.Background(), futureClaimTime(), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if !ok || claimed.ID != job.ID || claimed.Attempts != 1 || claimed.Status != StatusRunning {
		t.Fatalf("unexpected claimed job: ok=%v job=%#v", ok, claimed)
	}

	if err := store.Fail(context.Background(), claimed.ID, errors.New("temporary"), time.Now()); err != nil {
		t.Fatal(err)
	}
	retry, err := store.Get(context.Background(), claimed.ID)
	if err != nil {
		t.Fatal(err)
	}
	if retry.Status != StatusQueued || retry.Error != "temporary" {
		t.Fatalf("expected queued retry, got %#v", retry)
	}

	claimed, ok, err = store.Claim(context.Background(), futureClaimTime(), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if !ok || claimed.Attempts != 2 {
		t.Fatalf("expected second claim, ok=%v job=%#v", ok, claimed)
	}

	if err := store.Complete(context.Background(), claimed.ID); err != nil {
		t.Fatal(err)
	}
	done, err := store.Get(context.Background(), claimed.ID)
	if err != nil {
		t.Fatal(err)
	}
	if done.Status != StatusSucceeded || done.Progress != 1 {
		t.Fatalf("expected completed job, got %#v", done)
	}
}

func TestClaimOrdersSameSecondFractionalRunAfter(t *testing.T) {
	t.Parallel()

	store := NewStore(openJobsDB(t, filepath.Join(t.TempDir(), "kapsel.db")))
	// Two queued jobs inside the same second whose RFC3339Nano texts
	// misorder under BINARY comparison (...00.1Z sorts above
	// ...00.100000001Z). Claim must pick the numerically earlier one.
	base := time.Now().UTC().Add(time.Minute).Truncate(time.Second)
	earlyRunAfter := base.Add(100 * time.Millisecond)
	lateRunAfter := earlyRunAfter.Add(100 * time.Nanosecond)
	// Enqueue the later job first so insertion order cannot mask the sort.
	if _, err := store.Enqueue(context.Background(), EnqueueParams{ID: "fraction-late", Type: "download", RunAfter: lateRunAfter}); err != nil {
		t.Fatal(err)
	}
	early, err := store.Enqueue(context.Background(), EnqueueParams{ID: "fraction-early", Type: "download", RunAfter: earlyRunAfter})
	if err != nil {
		t.Fatal(err)
	}

	claimed, ok, err := store.Claim(context.Background(), base.Add(time.Hour), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if !ok || claimed.ID != early.ID {
		t.Fatalf("expected claim to pick the numerically earlier fraction (%s), got ok=%v id=%s", early.ID, ok, claimed.ID)
	}
}

func TestSchedulerIntrospectionHelpers(t *testing.T) {
	t.Parallel()

	store := NewStore(openJobsDB(t, filepath.Join(t.TempDir(), "kapsel.db")))
	ctx := context.Background()

	// Empty table: nothing active, nothing latest.
	if active, err := store.HasActiveJobByType(ctx, "download"); err != nil || active {
		t.Fatalf("expected no active job on empty table, got active=%v err=%v", active, err)
	}
	if _, found, err := store.LatestJobOfType(ctx, "download"); err != nil || found {
		t.Fatalf("expected no latest job on empty table, got found=%v err=%v", found, err)
	}

	active, err := store.Enqueue(context.Background(), EnqueueParams{Type: "download", MaxAttempts: 1})
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := store.HasActiveJobByType(ctx, "download"); err != nil || !ok {
		t.Fatalf("expected queued job to count as active, got active=%v err=%v", ok, err)
	}
	// A cancel-requested active job must not count as active for scheduling.
	claimed, ok, err := store.Claim(context.Background(), futureClaimTime(), time.Hour)
	if err != nil || !ok {
		t.Fatalf("expected claim, ok=%v err=%v", ok, err)
	}
	if err := store.Cancel(ctx, active.ID); err != nil {
		t.Fatal(err)
	}
	if ok, err := store.HasActiveJobByType(ctx, "download"); err != nil || ok {
		t.Fatalf("expected cancel-requested running job not to count as active, got active=%v err=%v", ok, err)
	}
	latest, found, err := store.LatestJobOfType(ctx, "download")
	if err != nil || !found || latest.ID != claimed.ID || latest.Status != StatusRunning {
		t.Fatalf("expected latest job to be the claimed one, got found=%v err=%v job=%#v", found, err, latest)
	}

	// A second job created later wins the latest-by-created_at ranking even
	// when its RFC3339Nano text has a fraction and the first does not.
	second, err := store.Enqueue(context.Background(), EnqueueParams{Type: "download"})
	if err != nil {
		t.Fatal(err)
	}
	latest, found, err = store.LatestJobOfType(ctx, "download")
	if err != nil || !found || latest.ID != second.ID {
		t.Fatalf("expected latest job to be the newer one, got found=%v err=%v id=%s", found, err, latest.ID)
	}
	if latest.Status != StatusQueued || latest.CreatedAt == "" {
		t.Fatalf("unexpected latest job: %#v", latest)
	}
}

func TestFailStopsAfterMaxAttempts(t *testing.T) {
	t.Parallel()

	store := NewStore(openJobsDB(t, filepath.Join(t.TempDir(), "kapsel.db")))
	job, err := store.Enqueue(context.Background(), EnqueueParams{Type: "scan", MaxAttempts: 1})
	if err != nil {
		t.Fatal(err)
	}
	claimed, ok, err := store.Claim(context.Background(), futureClaimTime(), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("expected claim")
	}

	if err := store.Fail(context.Background(), claimed.ID, errors.New("permanent"), time.Now()); err != nil {
		t.Fatal(err)
	}
	failed, err := store.Get(context.Background(), job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if failed.Status != StatusFailed || failed.Error != "permanent" {
		t.Fatalf("expected failed job, got %#v", failed)
	}
}

func TestFailDoesNotRetryJobWithRecordedResult(t *testing.T) {
	t.Parallel()

	store := NewStore(openJobsDB(t, filepath.Join(t.TempDir(), "kapsel.db")))
	job, err := store.Enqueue(context.Background(), EnqueueParams{Type: "scan", MaxAttempts: 3})
	if err != nil {
		t.Fatal(err)
	}
	claimed, ok, err := store.Claim(context.Background(), futureClaimTime(), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if !ok || claimed.ID != job.ID {
		t.Fatalf("expected claim, ok=%v job=%#v", ok, claimed)
	}
	markRunningJobWithCommittedResult(t, store.db, job.ID, `{"video_id":"committed"}`)
	if err := store.Fail(context.Background(), job.ID, errors.New("post-commit error"), time.Now()); err != nil {
		t.Fatal(err)
	}
	failed, err := store.Get(context.Background(), job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if failed.Status != StatusFailed || failed.Error != "post-commit error" || failed.CompletedAt == "" {
		t.Fatalf("expected terminal failure after recorded result, got %#v", failed)
	}
	if claimed, ok, err := store.Claim(context.Background(), futureClaimTime(), time.Hour); err != nil || ok {
		t.Fatalf("expected recorded-result failure not to be reclaimed, ok=%v job=%#v err=%v", ok, claimed, err)
	}
}

func TestFailMarksCancellationRequestedJobCancelled(t *testing.T) {
	t.Parallel()

	store := NewStore(openJobsDB(t, filepath.Join(t.TempDir(), "kapsel.db")))
	job, err := store.Enqueue(context.Background(), EnqueueParams{Type: "scan", MaxAttempts: 3})
	if err != nil {
		t.Fatal(err)
	}
	claimed, ok, err := store.Claim(context.Background(), futureClaimTime(), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if !ok || claimed.ID != job.ID {
		t.Fatalf("expected claim, ok=%v job=%#v", ok, claimed)
	}
	if err := store.Cancel(context.Background(), job.ID); err != nil {
		t.Fatal(err)
	}
	if err := store.Fail(context.Background(), job.ID, errors.New("cancel race"), time.Now()); err != nil {
		t.Fatal(err)
	}
	cancelled, err := store.Get(context.Background(), job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if cancelled.Status != StatusCancelled || !cancelled.CancelRequested || cancelled.CompletedAt == "" {
		t.Fatalf("expected cancelled job after fail/cancel race, got %#v", cancelled)
	}
}

func TestCancelQueuedAndRunningJobs(t *testing.T) {
	t.Parallel()

	store := NewStore(openJobsDB(t, filepath.Join(t.TempDir(), "kapsel.db")))
	queued, err := store.Enqueue(context.Background(), EnqueueParams{Type: "scan"})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Cancel(context.Background(), queued.ID); err != nil {
		t.Fatal(err)
	}
	cancelled, err := store.Get(context.Background(), queued.ID)
	if err != nil {
		t.Fatal(err)
	}
	if cancelled.Status != StatusCancelled || !cancelled.CancelRequested {
		t.Fatalf("expected cancelled queued job, got %#v", cancelled)
	}

	running, err := store.Enqueue(context.Background(), EnqueueParams{Type: "scan"})
	if err != nil {
		t.Fatal(err)
	}
	claimed, ok, err := store.Claim(context.Background(), futureClaimTime(), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if !ok || claimed.ID != running.ID {
		t.Fatalf("expected running claim, ok=%v job=%#v", ok, claimed)
	}
	if err := store.Cancel(context.Background(), running.ID); err != nil {
		t.Fatal(err)
	}
	marked, err := store.Get(context.Background(), running.ID)
	if err != nil {
		t.Fatal(err)
	}
	if marked.Status != StatusRunning || !marked.CancelRequested {
		t.Fatalf("expected running job marked for cancellation, got %#v", marked)
	}
}

func TestCancelRejectsFinishedJobs(t *testing.T) {
	t.Parallel()

	store := NewStore(openJobsDB(t, filepath.Join(t.TempDir(), "kapsel.db")))
	succeeded, err := store.Enqueue(context.Background(), EnqueueParams{Type: "scan"})
	if err != nil {
		t.Fatal(err)
	}
	claimed, ok, err := store.Claim(context.Background(), futureClaimTime(), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if !ok || claimed.ID != succeeded.ID {
		t.Fatalf("expected succeeded job claim, ok=%v job=%#v", ok, claimed)
	}
	if err := store.Complete(context.Background(), succeeded.ID); err != nil {
		t.Fatal(err)
	}
	if err := store.Cancel(context.Background(), succeeded.ID); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("expected invalid transition cancelling succeeded job, got %v", err)
	}

	failed, err := store.Enqueue(context.Background(), EnqueueParams{Type: "scan", MaxAttempts: 1})
	if err != nil {
		t.Fatal(err)
	}
	claimed, ok, err = store.Claim(context.Background(), futureClaimTime(), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if !ok || claimed.ID != failed.ID {
		t.Fatalf("expected failed job claim, ok=%v job=%#v", ok, claimed)
	}
	if err := store.Fail(context.Background(), claimed.ID, errors.New("permanent"), time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := store.Cancel(context.Background(), failed.ID); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("expected invalid transition cancelling failed job, got %v", err)
	}
}

func TestCompleteHonorsAcceptedCancellation(t *testing.T) {
	t.Parallel()

	store := NewStore(openJobsDB(t, filepath.Join(t.TempDir(), "kapsel.db")))
	job, err := store.Enqueue(context.Background(), EnqueueParams{Type: "scan"})
	if err != nil {
		t.Fatal(err)
	}
	claimed, ok, err := store.Claim(context.Background(), futureClaimTime(), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if !ok || claimed.ID != job.ID {
		t.Fatalf("expected claim, ok=%v job=%#v", ok, claimed)
	}
	if err := store.Cancel(context.Background(), job.ID); err != nil {
		t.Fatal(err)
	}
	if err := store.Complete(context.Background(), job.ID); err != nil {
		t.Fatal(err)
	}
	stored, err := store.Get(context.Background(), job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != StatusCancelled || !stored.CancelRequested || stored.CompletedAt == "" {
		t.Fatalf("expected accepted cancellation to win over empty-result completion, got %#v", stored)
	}
}

func TestMarkCancelledIsIdempotent(t *testing.T) {
	t.Parallel()

	store := NewStore(openJobsDB(t, filepath.Join(t.TempDir(), "kapsel.db")))
	job, err := store.Enqueue(context.Background(), EnqueueParams{Type: "scan"})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Cancel(context.Background(), job.ID); err != nil {
		t.Fatal(err)
	}
	if err := store.MarkCancelled(context.Background(), job.ID); err != nil {
		t.Fatalf("expected already-cancelled job to be accepted, got %v", err)
	}
}

func TestCancelRejectsRunningJobWithRecordedResult(t *testing.T) {
	t.Parallel()

	store := NewStore(openJobsDB(t, filepath.Join(t.TempDir(), "kapsel.db")))
	job, err := store.Enqueue(context.Background(), EnqueueParams{Type: "scan"})
	if err != nil {
		t.Fatal(err)
	}
	claimed, ok, err := store.Claim(context.Background(), futureClaimTime(), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if !ok || claimed.ID != job.ID {
		t.Fatalf("expected claim, ok=%v job=%#v", ok, claimed)
	}
	markRunningJobWithCommittedResult(t, store.db, job.ID, `{"video_id":"committed"}`)
	if err := store.Cancel(context.Background(), job.ID); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("expected invalid transition cancelling committed job, got %v", err)
	}
}

func TestClaimMarksStaleCancelledRunningJobCancelled(t *testing.T) {
	t.Parallel()

	store := NewStore(openJobsDB(t, filepath.Join(t.TempDir(), "kapsel.db")))
	job, err := store.Enqueue(context.Background(), EnqueueParams{Type: "scan"})
	if err != nil {
		t.Fatal(err)
	}
	claimedAt := futureClaimTime()
	claimed, ok, err := store.Claim(context.Background(), claimedAt, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if !ok || claimed.ID != job.ID {
		t.Fatalf("expected claim, ok=%v job=%#v", ok, claimed)
	}
	if err := store.Cancel(context.Background(), job.ID); err != nil {
		t.Fatal(err)
	}

	reclaimed, ok, err := store.Claim(context.Background(), claimedAt.Add(2*time.Hour), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatalf("expected cancelled stale job not to be reclaimed, got %#v", reclaimed)
	}
	cancelled, err := store.Get(context.Background(), job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if cancelled.Status != StatusCancelled || !cancelled.CancelRequested || cancelled.CompletedAt == "" {
		t.Fatalf("expected stale cancelled job to become cancelled, got %#v", cancelled)
	}
}

func TestClaimMarksStaleRunningJobWithResultSucceeded(t *testing.T) {
	t.Parallel()

	store := NewStore(openJobsDB(t, filepath.Join(t.TempDir(), "kapsel.db")))
	job, err := store.Enqueue(context.Background(), EnqueueParams{Type: "scan", MaxAttempts: 1})
	if err != nil {
		t.Fatal(err)
	}
	claimedAt := futureClaimTime()
	claimed, ok, err := store.Claim(context.Background(), claimedAt, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if !ok || claimed.ID != job.ID {
		t.Fatalf("expected claim, ok=%v job=%#v", ok, claimed)
	}
	markRunningJobWithCommittedResult(t, store.db, job.ID, `{"video_id":"committed"}`)

	reclaimed, ok, err := store.Claim(context.Background(), claimedAt.Add(2*time.Hour), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatalf("expected stale job with result not to be reclaimed, got %#v", reclaimed)
	}
	completed, err := store.Get(context.Background(), job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if completed.Status != StatusSucceeded || completed.CancelRequested || completed.Progress != 1 || completed.ResultJSON != `{"video_id":"committed"}` {
		t.Fatalf("expected stale job with result to become succeeded, got %#v", completed)
	}
}

func TestClaimFailsExhaustedStaleRunningJob(t *testing.T) {
	t.Parallel()

	store := NewStore(openJobsDB(t, filepath.Join(t.TempDir(), "kapsel.db")))
	job, err := store.Enqueue(context.Background(), EnqueueParams{Type: "scan", MaxAttempts: 1})
	if err != nil {
		t.Fatal(err)
	}
	claimedAt := futureClaimTime()
	claimed, ok, err := store.Claim(context.Background(), claimedAt, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if !ok || claimed.ID != job.ID || claimed.Attempts != 1 {
		t.Fatalf("expected first claim, ok=%v job=%#v", ok, claimed)
	}

	reclaimed, ok, err := store.Claim(context.Background(), claimedAt.Add(2*time.Hour), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatalf("expected exhausted stale job not to be reclaimed, got %#v", reclaimed)
	}
	failed, err := store.Get(context.Background(), job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if failed.Status != StatusFailed || failed.CompletedAt == "" || !strings.Contains(failed.Error, "stale") || failed.Attempts != 1 {
		t.Fatalf("expected exhausted stale job to fail with diagnostic, got %#v", failed)
	}
}

func TestClaimDoesNotCompleteStaleRunningJobWithPartialResult(t *testing.T) {
	t.Parallel()

	store := NewStore(openJobsDB(t, filepath.Join(t.TempDir(), "kapsel.db")))
	job, err := store.Enqueue(context.Background(), EnqueueParams{Type: "scan", MaxAttempts: 2})
	if err != nil {
		t.Fatal(err)
	}
	claimedAt := futureClaimTime()
	claimed, ok, err := store.Claim(context.Background(), claimedAt, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if !ok || claimed.ID != job.ID {
		t.Fatalf("expected claim, ok=%v job=%#v", ok, claimed)
	}
	if err := store.SetPartialResult(context.Background(), job.ID, `{"partial":true}`); err != nil {
		t.Fatal(err)
	}

	reclaimed, ok, err := store.Claim(context.Background(), claimedAt.Add(2*time.Hour), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if !ok || reclaimed.ID != job.ID || reclaimed.Status != StatusRunning || reclaimed.Attempts != 2 {
		t.Fatalf("expected stale partial-result job to be reclaimed, ok=%v job=%#v", ok, reclaimed)
	}
	if reclaimed.ResultJSON != `{"partial":true}` || reclaimed.ResultCommitted {
		t.Fatalf("expected partial result to remain diagnostic only, got %#v", reclaimed)
	}
}

func TestPartialResultDoesNotOverwriteCommittedResult(t *testing.T) {
	t.Parallel()

	store := NewStore(openJobsDB(t, filepath.Join(t.TempDir(), "kapsel.db")))
	job, err := store.Enqueue(context.Background(), EnqueueParams{Type: "scan"})
	if err != nil {
		t.Fatal(err)
	}
	claimed, ok, err := store.Claim(context.Background(), futureClaimTime(), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if !ok || claimed.ID != job.ID {
		t.Fatalf("expected claim, ok=%v job=%#v", ok, claimed)
	}
	if err := store.SetResult(context.Background(), job.ID, `{"video_id":"committed"}`); err != nil {
		t.Fatal(err)
	}
	if err := store.SetPartialResult(context.Background(), job.ID, `{"partial":true}`); err != nil {
		t.Fatal(err)
	}

	stored, err := store.Get(context.Background(), job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != StatusSucceeded || stored.Progress != 1 || stored.ResultJSON != `{"video_id":"committed"}` || !stored.ResultCommitted || stored.CompletedAt == "" {
		t.Fatalf("expected committed result to survive partial write, got %#v", stored)
	}
}

func TestSetResultTxParticipatesInTransaction(t *testing.T) {
	t.Parallel()

	db := openJobsDB(t, filepath.Join(t.TempDir(), "kapsel.db"))
	store := NewStore(db)
	job, err := store.Enqueue(context.Background(), EnqueueParams{Type: "scan"})
	if err != nil {
		t.Fatal(err)
	}
	claimed, ok, err := store.Claim(context.Background(), futureClaimTime(), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if !ok || claimed.ID != job.ID {
		t.Fatalf("expected claim, ok=%v job=%#v", ok, claimed)
	}

	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetResultTx(context.Background(), tx, job.ID, `{"video_id":"rollback"}`); err != nil {
		t.Fatal(err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	stored, err := store.Get(context.Background(), job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != StatusRunning || stored.ResultJSON != "{}" || stored.ResultCommitted || stored.CompletedAt != "" {
		t.Fatalf("expected rolled back result write to leave job unchanged, got %#v", stored)
	}

	tx, err = db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetResultTx(context.Background(), tx, job.ID, `{"video_id":"committed"}`); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	stored, err = store.Get(context.Background(), job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != StatusSucceeded || stored.Progress != 1 || stored.ResultJSON != `{"video_id":"committed"}` || !stored.ResultCommitted || stored.CompletedAt == "" {
		t.Fatalf("expected committed transactional result, got %#v", stored)
	}
}

func TestSetPartialResultTxParticipatesInTransaction(t *testing.T) {
	t.Parallel()

	db := openJobsDB(t, filepath.Join(t.TempDir(), "kapsel.db"))
	store := NewStore(db)
	job, err := store.Enqueue(context.Background(), EnqueueParams{Type: "scan"})
	if err != nil {
		t.Fatal(err)
	}
	claimed, ok, err := store.Claim(context.Background(), futureClaimTime(), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if !ok || claimed.ID != job.ID {
		t.Fatalf("expected claim, ok=%v job=%#v", ok, claimed)
	}

	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetPartialResultTx(context.Background(), tx, job.ID, `{"partial":true}`); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	stored, err := store.Get(context.Background(), job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.ResultJSON != `{"partial":true}` || stored.ResultCommitted {
		t.Fatalf("expected transactional partial result, got %#v", stored)
	}
}

func TestSetResultRejectsTerminalJob(t *testing.T) {
	t.Parallel()

	store := NewStore(openJobsDB(t, filepath.Join(t.TempDir(), "kapsel.db")))
	job, err := store.Enqueue(context.Background(), EnqueueParams{Type: "scan", MaxAttempts: 1})
	if err != nil {
		t.Fatal(err)
	}
	claimed, ok, err := store.Claim(context.Background(), futureClaimTime(), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if !ok || claimed.ID != job.ID {
		t.Fatalf("expected claim, ok=%v job=%#v", ok, claimed)
	}
	if err := store.Fail(context.Background(), job.ID, errors.New("failed"), time.Now()); err != nil {
		t.Fatal(err)
	}

	if err := store.SetResult(context.Background(), job.ID, `{"video_id":"late"}`); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("expected invalid transition writing terminal result, got %v", err)
	}
	stored, err := store.Get(context.Background(), job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.ResultJSON != "{}" || stored.ResultCommitted {
		t.Fatalf("expected terminal job to ignore late result write, got %#v", stored)
	}
}

func TestCompleteClearsUncommittedPartialResult(t *testing.T) {
	t.Parallel()

	store := NewStore(openJobsDB(t, filepath.Join(t.TempDir(), "kapsel.db")))
	job, err := store.Enqueue(context.Background(), EnqueueParams{Type: "scan"})
	if err != nil {
		t.Fatal(err)
	}
	claimed, ok, err := store.Claim(context.Background(), futureClaimTime(), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if !ok || claimed.ID != job.ID {
		t.Fatalf("expected claim, ok=%v job=%#v", ok, claimed)
	}
	if err := store.SetPartialResult(context.Background(), job.ID, `{"partial":true}`); err != nil {
		t.Fatal(err)
	}
	if err := store.Complete(context.Background(), job.ID); err != nil {
		t.Fatal(err)
	}

	stored, err := store.Get(context.Background(), job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != StatusSucceeded || stored.ResultJSON != "{}" || stored.ResultCommitted || stored.ResultSummary != "" {
		t.Fatalf("expected successful job to clear uncommitted partial result, got %#v", stored)
	}
}

func TestCompleteWithResultStoresResultAndSucceededStatusTogether(t *testing.T) {
	t.Parallel()

	store := NewStore(openJobsDB(t, filepath.Join(t.TempDir(), "kapsel.db")))
	job, err := store.Enqueue(context.Background(), EnqueueParams{Type: "scan"})
	if err != nil {
		t.Fatal(err)
	}
	claimed, ok, err := store.Claim(context.Background(), futureClaimTime(), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if !ok || claimed.ID != job.ID {
		t.Fatalf("expected claim, ok=%v job=%#v", ok, claimed)
	}
	if err := store.CompleteWithResult(context.Background(), job.ID, `{"video_id":"committed"}`); err != nil {
		t.Fatal(err)
	}

	stored, err := store.Get(context.Background(), job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != StatusSucceeded || stored.Progress != 1 || stored.ResultJSON != `{"video_id":"committed"}` || !stored.ResultCommitted || stored.CompletedAt == "" || stored.ResultSummary == "" {
		t.Fatalf("expected final result and succeeded status together, got %#v", stored)
	}
}

func TestCompleteWithResultWinsOverRequestedCancellation(t *testing.T) {
	t.Parallel()

	store := NewStore(openJobsDB(t, filepath.Join(t.TempDir(), "kapsel.db")))
	job, err := store.Enqueue(context.Background(), EnqueueParams{Type: "scan"})
	if err != nil {
		t.Fatal(err)
	}
	claimed, ok, err := store.Claim(context.Background(), futureClaimTime(), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if !ok || claimed.ID != job.ID {
		t.Fatalf("expected claim, ok=%v job=%#v", ok, claimed)
	}
	if err := store.Cancel(context.Background(), job.ID); err != nil {
		t.Fatal(err)
	}
	if err := store.CompleteWithResult(context.Background(), job.ID, `{"video_id":"committed"}`); err != nil {
		t.Fatal(err)
	}

	stored, err := store.Get(context.Background(), job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != StatusSucceeded || stored.CancelRequested || stored.ResultJSON != `{"video_id":"committed"}` || !stored.ResultCommitted {
		t.Fatalf("expected final committed result to win over requested cancellation, got %#v", stored)
	}
}

func TestCompleteWithResultTxParticipatesInTransaction(t *testing.T) {
	t.Parallel()

	db := openJobsDB(t, filepath.Join(t.TempDir(), "kapsel.db"))
	store := NewStore(db)
	job, err := store.Enqueue(context.Background(), EnqueueParams{Type: "scan"})
	if err != nil {
		t.Fatal(err)
	}
	claimed, ok, err := store.Claim(context.Background(), futureClaimTime(), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if !ok || claimed.ID != job.ID {
		t.Fatalf("expected claim, ok=%v job=%#v", ok, claimed)
	}

	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CompleteWithResultTx(context.Background(), tx, job.ID, `{"video_id":"rollback"}`); err != nil {
		t.Fatal(err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	stored, err := store.Get(context.Background(), job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != StatusRunning || stored.ResultJSON != "{}" || stored.ResultCommitted || stored.CompletedAt != "" {
		t.Fatalf("expected rolled back result completion to leave job running, got %#v", stored)
	}

	tx, err = db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CompleteWithResultTx(context.Background(), tx, job.ID, `{"video_id":"committed"}`); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	stored, err = store.Get(context.Background(), job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != StatusSucceeded || stored.ResultJSON != `{"video_id":"committed"}` || !stored.ResultCommitted || stored.CompletedAt == "" {
		t.Fatalf("expected transactional result completion, got %#v", stored)
	}
}

func TestClaimMarksStaleCancelledRunningJobWithResultCancelled(t *testing.T) {
	t.Parallel()

	store := NewStore(openJobsDB(t, filepath.Join(t.TempDir(), "kapsel.db")))
	job, err := store.Enqueue(context.Background(), EnqueueParams{Type: "scan"})
	if err != nil {
		t.Fatal(err)
	}
	claimedAt := futureClaimTime()
	claimed, ok, err := store.Claim(context.Background(), claimedAt, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if !ok || claimed.ID != job.ID {
		t.Fatalf("expected claim, ok=%v job=%#v", ok, claimed)
	}
	if err := store.Cancel(context.Background(), job.ID); err != nil {
		t.Fatal(err)
	}
	markRunningJobWithCommittedResult(t, store.db, job.ID, `{"partial":true}`)

	reclaimed, ok, err := store.Claim(context.Background(), claimedAt.Add(2*time.Hour), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatalf("expected cancelled stale job with result not to be reclaimed, got %#v", reclaimed)
	}
	cancelled, err := store.Get(context.Background(), job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if cancelled.Status != StatusCancelled || !cancelled.CancelRequested || cancelled.CompletedAt == "" || cancelled.ResultJSON != `{"partial":true}` || cancelled.ResultCommitted {
		t.Fatalf("expected stale cancelled job with result to become cancelled, got %#v", cancelled)
	}
}

func TestRetryFailedJobQueuesOneAdditionalAttempt(t *testing.T) {
	t.Parallel()

	store := NewStore(openJobsDB(t, filepath.Join(t.TempDir(), "kapsel.db")))
	job, err := store.Enqueue(context.Background(), EnqueueParams{Type: "scan", MaxAttempts: 1})
	if err != nil {
		t.Fatal(err)
	}
	claimed, ok, err := store.Claim(context.Background(), futureClaimTime(), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if !ok || claimed.ID != job.ID {
		t.Fatalf("expected claim, ok=%v job=%#v", ok, claimed)
	}
	if err := store.Heartbeat(context.Background(), job.ID, 0.7); err != nil {
		t.Fatal(err)
	}
	if err := store.Fail(context.Background(), job.ID, errors.New("missing tool"), time.Now()); err != nil {
		t.Fatal(err)
	}

	if err := store.Retry(context.Background(), job.ID); err != nil {
		t.Fatal(err)
	}
	retried, err := store.Get(context.Background(), job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if retried.Status != StatusQueued || retried.Progress != 0 || retried.CompletedAt != "" || retried.CancelRequested {
		t.Fatalf("unexpected retried job state: %#v", retried)
	}
	if retried.Error != "missing tool" || retried.Attempts != 1 || retried.MaxAttempts != 2 {
		t.Fatalf("expected retry to preserve error/history and grant one attempt, got %#v", retried)
	}

	claimed, ok, err = store.Claim(context.Background(), futureClaimTime(), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if !ok || claimed.ID != job.ID || claimed.Attempts != 2 || claimed.MaxAttempts != 2 {
		t.Fatalf("expected retry claim with second attempt, ok=%v job=%#v", ok, claimed)
	}
	if err := store.Fail(context.Background(), job.ID, errors.New("still missing"), time.Now()); err != nil {
		t.Fatal(err)
	}
	failed, err := store.Get(context.Background(), job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if failed.Status != StatusFailed || failed.Error != "still missing" || failed.Attempts != 2 || failed.MaxAttempts != 2 {
		t.Fatalf("expected repeated failure after manual retry, got %#v", failed)
	}
}

func TestRetryRejectsInvalidAndUnsafeJobs(t *testing.T) {
	t.Parallel()

	store := NewStore(openJobsDB(t, filepath.Join(t.TempDir(), "kapsel.db")))
	queued, err := store.Enqueue(context.Background(), EnqueueParams{Type: "scan"})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Retry(context.Background(), queued.ID); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("expected invalid transition retrying queued job, got %v", err)
	}
	if err := store.Cancel(context.Background(), queued.ID); err != nil {
		t.Fatal(err)
	}

	failed, err := store.Enqueue(context.Background(), EnqueueParams{Type: "scan", MaxAttempts: 1})
	if err != nil {
		t.Fatal(err)
	}
	claimed, ok, err := store.Claim(context.Background(), futureClaimTime(), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if !ok || claimed.ID != failed.ID {
		t.Fatalf("expected failed job claim, ok=%v job=%#v", ok, claimed)
	}
	markRunningJobWithCommittedResult(t, store.db, failed.ID, `{"video_id":"committed"}`)
	if err := store.Fail(context.Background(), failed.ID, errors.New("after commit"), time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := store.Retry(context.Background(), failed.ID); !errors.Is(err, ErrUnsafeRetry) {
		t.Fatalf("expected unsafe retry error, got %v", err)
	}
}

func TestHeartbeatDoesNotRegressProgress(t *testing.T) {
	t.Parallel()

	store := NewStore(openJobsDB(t, filepath.Join(t.TempDir(), "kapsel.db")))
	job, err := store.Enqueue(context.Background(), EnqueueParams{Type: "import"})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok, err := store.Claim(context.Background(), futureClaimTime(), time.Hour); err != nil || !ok {
		t.Fatalf("expected running job before heartbeat, ok=%v err=%v", ok, err)
	}
	if err := store.Heartbeat(context.Background(), job.ID, 0.8); err != nil {
		t.Fatal(err)
	}
	if err := store.Heartbeat(context.Background(), job.ID, 0.2); err != nil {
		t.Fatal(err)
	}

	stored, err := store.Get(context.Background(), job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Progress != 0.8 {
		t.Fatalf("expected heartbeat progress not to regress, got %#v", stored)
	}
}

func TestHeartbeatDoesNotMutateTerminalJobs(t *testing.T) {
	t.Parallel()

	db := openJobsDB(t, filepath.Join(t.TempDir(), "kapsel.db"))
	store := NewStore(db)
	terminalIDs := []string{
		completeHeartbeatFixtureJob(t, store),
		failHeartbeatFixtureJob(t, store),
		cancelHeartbeatFixtureJob(t, store),
	}

	for _, id := range terminalIDs {
		id := id
		t.Run(id, func(t *testing.T) {
			const updatedAt = "2026-05-03T00:00:00Z"
			if _, err := db.Exec("UPDATE jobs SET updated_at = ?, locked_at = NULL WHERE id = ?", updatedAt, id); err != nil {
				t.Fatal(err)
			}
			before, err := store.Get(context.Background(), id)
			if err != nil {
				t.Fatal(err)
			}

			if err := store.Heartbeat(context.Background(), id, 0.9); err != nil {
				t.Fatal(err)
			}
			after, err := store.Get(context.Background(), id)
			if err != nil {
				t.Fatal(err)
			}
			if after.LockedAt != "" || after.UpdatedAt != updatedAt || after.Progress != before.Progress {
				t.Fatalf("expected heartbeat not to mutate terminal job, before=%#v after=%#v", before, after)
			}
		})
	}
}

func completeHeartbeatFixtureJob(t *testing.T, store *Store) string {
	t.Helper()
	job, err := store.Enqueue(context.Background(), EnqueueParams{Type: "complete-heartbeat"})
	if err != nil {
		t.Fatal(err)
	}
	if claimed, ok, err := store.Claim(context.Background(), futureClaimTime(), time.Hour); err != nil || !ok || claimed.ID != job.ID {
		t.Fatalf("expected claim before complete, ok=%v job=%#v err=%v", ok, claimed, err)
	}
	if err := store.Complete(context.Background(), job.ID); err != nil {
		t.Fatal(err)
	}

	return job.ID
}

func failHeartbeatFixtureJob(t *testing.T, store *Store) string {
	t.Helper()
	job, err := store.Enqueue(context.Background(), EnqueueParams{Type: "fail-heartbeat", MaxAttempts: 1})
	if err != nil {
		t.Fatal(err)
	}
	if claimed, ok, err := store.Claim(context.Background(), futureClaimTime(), time.Hour); err != nil || !ok || claimed.ID != job.ID {
		t.Fatalf("expected claim before fail, ok=%v job=%#v err=%v", ok, claimed, err)
	}
	if err := store.Heartbeat(context.Background(), job.ID, 0.4); err != nil {
		t.Fatal(err)
	}
	if err := store.Fail(context.Background(), job.ID, errors.New("failed"), time.Now()); err != nil {
		t.Fatal(err)
	}

	return job.ID
}

func cancelHeartbeatFixtureJob(t *testing.T, store *Store) string {
	t.Helper()
	job, err := store.Enqueue(context.Background(), EnqueueParams{Type: "cancel-heartbeat"})
	if err != nil {
		t.Fatal(err)
	}
	if claimed, ok, err := store.Claim(context.Background(), futureClaimTime(), time.Hour); err != nil || !ok || claimed.ID != job.ID {
		t.Fatalf("expected claim before cancel, ok=%v job=%#v err=%v", ok, claimed, err)
	}
	if err := store.Cancel(context.Background(), job.ID); err != nil {
		t.Fatal(err)
	}
	if err := store.MarkCancelled(context.Background(), job.ID); err != nil {
		t.Fatal(err)
	}

	return job.ID
}

func TestListJobsPaginatesAndFiltersByStatus(t *testing.T) {
	t.Parallel()

	db := openJobsDB(t, filepath.Join(t.TempDir(), "kapsel.db"))
	store := NewStore(db)
	seedListJob(t, db, store, "queued-old", StatusQueued, "2026-05-03T10:00:00Z", "{}")
	seedListJob(t, db, store, "failed-old", StatusFailed, "2026-05-03T11:00:00Z", `{"video_id":"old"}`)
	seedListJob(t, db, store, "failed-new", StatusFailed, "2026-05-03T12:00:00Z", `{"video_id":"new"}`)

	first, err := store.List(context.Background(), ListOptions{Statuses: []Status{StatusFailed}, Page: 1, PageSize: 1})
	if err != nil {
		t.Fatal(err)
	}
	if first.Total != 2 || first.Page != 1 || first.PageSize != 1 || len(first.Jobs) != 1 {
		t.Fatalf("unexpected first page: %#v", first)
	}
	if first.Jobs[0].ID != "failed-new" || first.Jobs[0].Status != StatusFailed || first.Jobs[0].ResultSummary == "" {
		t.Fatalf("unexpected first page job: %#v", first.Jobs[0])
	}

	second, err := store.List(context.Background(), ListOptions{Statuses: []Status{StatusFailed}, Page: 2, PageSize: 1})
	if err != nil {
		t.Fatal(err)
	}
	if second.Total != 2 || len(second.Jobs) != 1 || second.Jobs[0].ID != "failed-old" {
		t.Fatalf("unexpected second page: %#v", second)
	}
}

func TestListJobsOrdersVariablePrecisionTimestampsChronologically(t *testing.T) {
	t.Parallel()

	db := openJobsDB(t, filepath.Join(t.TempDir(), "kapsel.db"))
	store := NewStore(db)
	seedListJob(t, db, store, "exact-second", StatusSucceeded, "2026-05-03T12:00:01Z", `{"video_id":"exact"}`)
	seedListJob(t, db, store, "fraction-100ms", StatusSucceeded, "2026-05-03T12:00:01.1Z", `{"video_id":"100ms"}`)
	seedListJob(t, db, store, "fraction-190ms", StatusSucceeded, "2026-05-03T12:00:01.19Z", `{"video_id":"190ms"}`)

	result, err := store.List(context.Background(), ListOptions{Page: 1, PageSize: 3})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Jobs) != 3 {
		t.Fatalf("expected three jobs, got %#v", result)
	}
	ids := []string{result.Jobs[0].ID, result.Jobs[1].ID, result.Jobs[2].ID}
	want := []string{"fraction-190ms", "fraction-100ms", "exact-second"}
	for i := range want {
		if ids[i] != want[i] {
			t.Fatalf("expected chronological timestamp order %v, got %v", want, ids)
		}
	}
}

func TestListJobsBoundsResultSummary(t *testing.T) {
	t.Parallel()

	db := openJobsDB(t, filepath.Join(t.TempDir(), "kapsel.db"))
	store := NewStore(db)
	seedListJob(t, db, store, "large-result", StatusSucceeded, "2026-05-03T12:00:00Z", strings.Repeat("x", maxResultSummaryLength+200))

	result, err := store.List(context.Background(), ListOptions{Page: 1, PageSize: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Jobs) != 1 {
		t.Fatalf("expected one listed job, got %#v", result)
	}
	if len(result.Jobs[0].ResultSummary) > maxResultSummaryLength+len(" ... [truncated]") || !strings.HasSuffix(result.Jobs[0].ResultSummary, "[truncated]") {
		t.Fatalf("expected bounded truncated summary, got %q", result.Jobs[0].ResultSummary)
	}
}

func TestRunnerCancelsContextAwareJob(t *testing.T) {
	t.Parallel()

	store := NewStore(openJobsDB(t, filepath.Join(t.TempDir(), "kapsel.db")))
	job, err := store.Enqueue(context.Background(), EnqueueParams{Type: "wait"})
	if err != nil {
		t.Fatal(err)
	}

	started := make(chan struct{})
	runner := NewRunner(store, map[string]Handler{
		"wait": func(ctx context.Context, _ Job) error {
			close(started)
			<-ctx.Done()
			return ctx.Err()
		},
	})
	// 10ms poll: the renewal writer must not starve store.Cancel's write
	// below (see the pacing note in TestRunnerHeartbeatsRunningJob).
	runner.CancelPollInterval = 10 * time.Millisecond

	done := make(chan error, 1)
	go func() {
		done <- runner.RunOnce(context.Background())
	}()

	<-started
	if err := store.Cancel(context.Background(), job.ID); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}

	stored, err := store.Get(context.Background(), job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != StatusCancelled {
		t.Fatalf("expected cancelled job, got %#v", stored)
	}
}

func TestRunnerCompletesJobWhenResultWasRecordedBeforeCancellationFinished(t *testing.T) {
	t.Parallel()

	store := NewStore(openJobsDB(t, filepath.Join(t.TempDir(), "kapsel.db")))
	job, err := store.Enqueue(context.Background(), EnqueueParams{Type: "commit"})
	if err != nil {
		t.Fatal(err)
	}

	started := make(chan struct{})
	finish := make(chan struct{})
	runner := NewRunner(store, map[string]Handler{
		"commit": func(ctx context.Context, job Job) error {
			close(started)
			<-finish
			_, err := store.db.ExecContext(ctx, "UPDATE jobs SET result_json = ?, result_committed = 1 WHERE id = ? AND status = ?", `{"video_id":"committed"}`, job.ID, StatusRunning)
			return err
		},
	})
	runner.CancelPollInterval = time.Hour

	done := make(chan error, 1)
	go func() {
		done <- runner.RunOnce(context.Background())
	}()
	<-started
	if err := store.Cancel(context.Background(), job.ID); err != nil {
		t.Fatal(err)
	}
	close(finish)
	if err := <-done; err != nil {
		t.Fatal(err)
	}

	stored, err := store.Get(context.Background(), job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != StatusSucceeded || stored.CancelRequested || stored.ResultJSON == "" || stored.ResultJSON == "{}" {
		t.Fatalf("expected recorded-result job to complete despite late cancellation, got %#v", stored)
	}
}

func TestRunnerAcceptsHandlerCompletedResult(t *testing.T) {
	t.Parallel()

	store := NewStore(openJobsDB(t, filepath.Join(t.TempDir(), "kapsel.db")))
	job, err := store.Enqueue(context.Background(), EnqueueParams{Type: "commit"})
	if err != nil {
		t.Fatal(err)
	}
	runner := NewRunner(store, map[string]Handler{
		"commit": func(ctx context.Context, job Job) error {
			return store.CompleteWithResult(ctx, job.ID, `{"video_id":"committed"}`)
		},
	})

	if err := runner.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	stored, err := store.Get(context.Background(), job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != StatusSucceeded || stored.ResultJSON != `{"video_id":"committed"}` || !stored.ResultCommitted || stored.ResultSummary == "" {
		t.Fatalf("expected runner to preserve handler-completed result, got %#v", stored)
	}
}

func TestRunnerLogsHandlerErrorAfterTerminalCompletion(t *testing.T) {
	t.Parallel()

	store := NewStore(openJobsDB(t, filepath.Join(t.TempDir(), "kapsel.db")))
	job, err := store.Enqueue(context.Background(), EnqueueParams{Type: "commit"})
	if err != nil {
		t.Fatal(err)
	}
	var logs bytes.Buffer
	runner := NewRunner(store, map[string]Handler{
		"commit": func(ctx context.Context, job Job) error {
			if err := store.CompleteWithResult(ctx, job.ID, `{"video_id":"committed"}`); err != nil {
				return err
			}
			return errors.New("post-completion failure")
		},
	})
	runner.Logger = slog.New(slog.NewJSONHandler(&logs, nil))

	if err := runner.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	stored, err := store.Get(context.Background(), job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != StatusSucceeded || stored.ResultJSON != `{"video_id":"committed"}` {
		t.Fatalf("expected handler-completed result to remain succeeded, got %#v", stored)
	}
	output := logs.String()
	if !strings.Contains(output, `"msg":"job finished after handler error"`) || !strings.Contains(output, "post-completion failure") {
		t.Fatalf("expected terminal handler error to be logged, got %s", output)
	}
}

func TestRunnerFailsJobWithPartialResultWithoutCommittingResult(t *testing.T) {
	t.Parallel()

	store := NewStore(openJobsDB(t, filepath.Join(t.TempDir(), "kapsel.db")))
	job, err := store.Enqueue(context.Background(), EnqueueParams{Type: "partial", MaxAttempts: 1})
	if err != nil {
		t.Fatal(err)
	}
	runner := NewRunner(store, map[string]Handler{
		"partial": func(ctx context.Context, job Job) error {
			if err := store.SetPartialResult(ctx, job.ID, `{"partial":true}`); err != nil {
				return err
			}
			return errors.New("partial report failed")
		},
	})

	if err := runner.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	stored, err := store.Get(context.Background(), job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != StatusFailed || stored.ResultJSON != `{"partial":true}` || stored.ResultCommitted || stored.ResultSummary == "" {
		t.Fatalf("expected failed diagnostic-only partial result, got %#v", stored)
	}
}

func TestRunnerHeartbeatsRunningJob(t *testing.T) {
	t.Parallel()

	store := NewStore(openJobsDB(t, filepath.Join(t.TempDir(), "kapsel.db")))
	job, err := store.Enqueue(context.Background(), EnqueueParams{Type: "wait"})
	if err != nil {
		t.Fatal(err)
	}

	started := make(chan struct{})
	finish := make(chan struct{})
	runner := NewRunner(store, map[string]Handler{
		"wait": func(ctx context.Context, _ Job) error {
			close(started)
			select {
			case <-finish:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		},
	})
	// paced so the renewal writer does not starve the competing Claim:
	// a 1ms poll with a 5ms stale window can hold the write lock ~100% of
	// the time on slow-fsync systems (CI runners), leaving Claim's
	// BEGIN IMMEDIATE to time out with SQLITE_BUSY after its full
	// busy_timeout instead of observing the (fresh) lease.
	runner.StaleAfter = 100 * time.Millisecond
	runner.CancelPollInterval = 10 * time.Millisecond

	done := make(chan error, 1)
	go func() {
		done <- runner.RunOnce(context.Background())
	}()
	<-started
	time.Sleep(250 * time.Millisecond)
	if claimed, ok, err := store.Claim(context.Background(), time.Now(), runner.StaleAfter); err != nil || ok {
		t.Fatalf("expected heartbeat to prevent stale claim, ok=%v job=%#v err=%v", ok, claimed, err)
	}
	close(finish)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	stored, err := store.Get(context.Background(), job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != StatusSucceeded {
		t.Fatalf("expected succeeded heartbeat job, got %#v", stored)
	}
}

func TestRunnerLeaseRenewalDoesNotDependOnProgressUpdates(t *testing.T) {
	t.Parallel()

	db := openJobsDB(t, filepath.Join(t.TempDir(), "kapsel.db"))
	store := NewStore(db)
	job, err := store.Enqueue(context.Background(), EnqueueParams{Type: "wait"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
CREATE TRIGGER fail_in_flight_progress_update
BEFORE UPDATE OF progress ON jobs
WHEN NEW.status = 'running' AND NEW.progress < 1
BEGIN
  SELECT RAISE(FAIL, 'progress update unavailable');
END`); err != nil {
		t.Fatal(err)
	}

	started := make(chan struct{})
	finish := make(chan struct{})
	finishClosed := false
	closeFinish := func() {
		if !finishClosed {
			close(finish)
			finishClosed = true
		}
	}
	defer closeFinish()
	runner := NewRunner(store, map[string]Handler{
		"wait": func(ctx context.Context, _ Job) error {
			close(started)
			select {
			case <-finish:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		},
	})
	// Same pacing rationale as TestRunnerHeartbeatsRunningJob above: a 1ms
	// renewal loop can starve the competing Claim's BEGIN IMMEDIATE for its
	// whole busy_timeout on slow-fsync systems, surfacing as SQLITE_BUSY
	// instead of the expected not-stale outcome.
	runner.StaleAfter = 100 * time.Millisecond
	runner.CancelPollInterval = 10 * time.Millisecond

	done := make(chan error, 1)
	go func() {
		done <- runner.RunOnce(context.Background())
	}()
	<-started
	time.Sleep(250 * time.Millisecond)
	if claimed, ok, err := store.Claim(context.Background(), time.Now(), runner.StaleAfter); err != nil || ok {
		closeFinish()
		t.Fatalf("expected lease renewal to prevent stale claim despite progress update errors, ok=%v job=%#v err=%v", ok, claimed, err)
	}
	closeFinish()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	stored, err := store.Get(context.Background(), job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != StatusSucceeded {
		t.Fatalf("expected succeeded lease renewal job, got %#v", stored)
	}
}

func TestRunnerReturnsLeaseRenewalErrors(t *testing.T) {
	t.Parallel()

	db := openJobsDB(t, filepath.Join(t.TempDir(), "kapsel.db"))
	store := NewStore(db)
	job, err := store.Enqueue(context.Background(), EnqueueParams{Type: "wait"})
	if err != nil {
		t.Fatal(err)
	}

	started := make(chan struct{})
	runner := NewRunner(store, map[string]Handler{
		"wait": func(ctx context.Context, job Job) error {
			quotedID := strings.ReplaceAll(job.ID, "'", "''")
			if _, err := db.Exec(fmt.Sprintf(`
CREATE TRIGGER fail_lease_renewal
BEFORE UPDATE OF locked_at ON jobs
WHEN NEW.id = '%s' AND OLD.status = 'running' AND NEW.status = 'running'
BEGIN
  SELECT RAISE(FAIL, 'lease renewal unavailable');
END`, quotedID)); err != nil {
				return err
			}
			close(started)
			<-ctx.Done()
			return ctx.Err()
		},
	})
	runner.CancelPollInterval = time.Millisecond

	done := make(chan error, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	go func() {
		done <- runner.RunOnce(ctx)
	}()
	<-started
	err = <-done
	if err == nil || !strings.Contains(err.Error(), "lease renewal unavailable") {
		t.Fatalf("expected lease renewal error, got %v", err)
	}
	stored, err := store.Get(context.Background(), job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != StatusRunning {
		t.Fatalf("expected failed lease renewal to leave running job for stale recovery, got %#v", stored)
	}
}

func TestRunLoopContinuesAfterTransientRunOnceError(t *testing.T) {
	t.Parallel()

	runner := NewRunner(nil, nil)
	var logs bytes.Buffer
	runner.Logger = slog.New(slog.NewJSONHandler(&logs, nil))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var calls int
	runner.runOnce = func(context.Context) error {
		calls++
		if calls == 1 {
			return errors.New("database locked Authorization: Bearer secret")
		}
		cancel()
		return nil
	}

	if err := runner.RunLoop(ctx, time.Millisecond); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context cancellation after retry, got %v", err)
	}
	if calls != 2 {
		t.Fatalf("expected runner to continue after transient error, got %d calls", calls)
	}
	output := logs.String()
	if !strings.Contains(output, `"msg":"job runner iteration failed"`) || !strings.Contains(output, "database locked") || !strings.Contains(output, "Authorization: [redacted]") {
		t.Fatalf("expected transient error to be logged, got %s", output)
	}
	for _, secret := range []string{"Bearer secret"} {
		if strings.Contains(output, secret) {
			t.Fatalf("expected transient error log to redact %q, got %s", secret, output)
		}
	}
}

func TestRunLoopExitsOnContextCancellation(t *testing.T) {
	t.Parallel()

	runner := NewRunner(nil, nil)
	ctx, cancel := context.WithCancel(context.Background())
	runner.runOnce = func(ctx context.Context) error {
		cancel()
		return ctx.Err()
	}

	started := time.Now()
	if err := runner.RunLoop(ctx, time.Hour); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context cancellation, got %v", err)
	}
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("expected cancellation before idle delay, took %s", elapsed)
	}
}

func TestRunnerLogsJobLifecycleWithIDTypeAndRedactedError(t *testing.T) {
	t.Parallel()

	store := NewStore(openJobsDB(t, filepath.Join(t.TempDir(), "kapsel.db")))
	job, err := store.Enqueue(context.Background(), EnqueueParams{Type: "download", MaxAttempts: 1})
	if err != nil {
		t.Fatal(err)
	}
	var logs bytes.Buffer
	runner := NewRunner(store, map[string]Handler{
		"download": func(context.Context, Job) error {
			return errors.New("download failed for https://user:pass@example.com/watch?v=abc&token=secret Authorization: Bearer supersecret")
		},
	})
	runner.Logger = slog.New(slog.NewJSONHandler(&logs, nil))

	if err := runner.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	output := logs.String()
	for _, expected := range []string{`"msg":"job started"`, `"msg":"job failed"`, `"job_id":"` + job.ID + `"`, `"job_type":"download"`} {
		if !strings.Contains(output, expected) {
			t.Fatalf("expected log output to contain %s, got %s", expected, output)
		}
	}
	for _, secret := range []string{"user:pass", "token=secret", "supersecret"} {
		if strings.Contains(output, secret) {
			t.Fatalf("expected log output to redact %q, got %s", secret, output)
		}
	}
}

func openJobsDB(t *testing.T, path string) *sql.DB {
	t.Helper()

	db, err := database.Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = db.Close()
	})

	if err := database.Migrate(context.Background(), db); err != nil {
		t.Fatal(err)
	}

	return db
}

// futureClaimTime returns a claim timestamp one second ahead of the wall
// clock. Beyond reading naturally as "now, but claimable", the margin keeps
// Claim's run_after/locked_at comparisons on opposite sides of a seconds
// digit: timestamps are stored as RFC3339Nano text, and before the
// RFC3339_NANO collation (internal/database) a claim time sampled
// microseconds after Enqueue's run_after could order lexicographically
// backwards (...00.1Z sorts above ...00.100000001Z) and intermittently
// miss the claim. The collation made the margin a plain safety margin
// rather than a correctness requirement; it stays because a one-second
// future claim reads unambiguously claimable.
func futureClaimTime() time.Time {
	return time.Now().Add(time.Second)
}

func seedListJob(t *testing.T, db *sql.DB, store *Store, id string, status Status, updatedAt string, resultJSON string) {
	t.Helper()

	if _, err := store.Enqueue(context.Background(), EnqueueParams{ID: id, Type: "download", PayloadJSON: fmt.Sprintf(`{"secret":"%s"}`, id)}); err != nil {
		t.Fatal(err)
	}
	_, err := db.Exec(`
UPDATE jobs
SET status = ?, progress = ?, error = ?, result_json = ?, updated_at = ?, completed_at = CASE WHEN ? IN ('succeeded', 'failed', 'cancelled') THEN ? ELSE NULL END
WHERE id = ?`, status, 0.5, statusError(status), resultJSON, updatedAt, status, updatedAt, id)
	if err != nil {
		t.Fatal(err)
	}
}

func markRunningJobWithCommittedResult(t *testing.T, db *sql.DB, id string, resultJSON string) {
	t.Helper()

	// This legacy state models stale-recovery and cancel-race windows from older
	// code paths; new final results should use CompleteWithResult instead.
	result, err := db.Exec("UPDATE jobs SET result_json = ?, result_committed = 1 WHERE id = ? AND status = ?", resultJSON, id, StatusRunning)
	if err != nil {
		t.Fatal(err)
	}
	if changed, err := result.RowsAffected(); err != nil {
		t.Fatal(err)
	} else if changed != 1 {
		t.Fatalf("expected one running job to receive committed result, changed %d", changed)
	}
}

func statusError(status Status) string {
	if status == StatusFailed {
		return "download failed: missing ffmpeg"
	}

	return ""
}

func TestRunnerRecoversHandlerPanic(t *testing.T) {
	t.Parallel()

	store := NewStore(openJobsDB(t, filepath.Join(t.TempDir(), "kapsel.db")))
	job, err := store.Enqueue(context.Background(), EnqueueParams{Type: "panic", MaxAttempts: 1})
	if err != nil {
		t.Fatal(err)
	}

	runner := NewRunner(store, map[string]Handler{
		"panic": func(context.Context, Job) error {
			panic("handler exploded")
		},
	})

	if err := runner.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}

	stored, err := store.Get(context.Background(), job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != StatusFailed {
		t.Fatalf("expected panicked job to be failed, got %s", stored.Status)
	}
	if !strings.Contains(stored.Error, "handler panicked") || !strings.Contains(stored.Error, "handler exploded") {
		t.Fatalf("expected panic error details, got %q", stored.Error)
	}
}

func TestRunnerFinalizesJobOnShutdown(t *testing.T) {
	t.Parallel()

	store := NewStore(openJobsDB(t, filepath.Join(t.TempDir(), "kapsel.db")))
	job, err := store.Enqueue(context.Background(), EnqueueParams{Type: "work"})
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	started := make(chan struct{})
	runner := NewRunner(store, map[string]Handler{
		"work": func(jobCtx context.Context, _ Job) error {
			close(started)
			<-jobCtx.Done()
			return nil
		},
	})
	runner.CancelPollInterval = time.Millisecond

	done := make(chan error, 1)
	go func() {
		done <- runner.RunOnce(ctx)
	}()

	<-started
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("expected RunOnce to return without error on shutdown, got %v", err)
	}

	stored, err := store.Get(context.Background(), job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != StatusSucceeded {
		t.Fatalf("expected completed handler to mark job succeeded on shutdown, got %s", stored.Status)
	}
}

func TestRunnerShutdownWaitIsBoundedForSlowHandler(t *testing.T) {
	t.Parallel()

	store := NewStore(openJobsDB(t, filepath.Join(t.TempDir(), "kapsel.db")))
	job, err := store.Enqueue(context.Background(), EnqueueParams{Type: "slow"})
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	started := make(chan struct{})
	release := make(chan struct{})
	defer close(release)
	runner := NewRunner(store, map[string]Handler{
		"slow": func(context.Context, Job) error {
			close(started)
			<-release
			return nil
		},
	})
	runner.CancelPollInterval = time.Millisecond
	runner.ShutdownGracePeriod = 5 * time.Millisecond

	done := make(chan error, 1)
	go func() {
		done <- runner.RunOnce(ctx)
	}()

	<-started
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("expected context cancellation after bounded shutdown wait, got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("expected bounded shutdown wait to return before slow handler finished")
	}

	stored, err := store.Get(context.Background(), job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != StatusRunning || stored.CompletedAt != "" {
		t.Fatalf("expected slow shutdown job to remain running for stale recovery, got %#v", stored)
	}
}

func TestRunnerTreatsDeadlineShutdownAsCancellation(t *testing.T) {
	t.Parallel()

	store := NewStore(openJobsDB(t, filepath.Join(t.TempDir(), "kapsel.db")))
	job, err := store.Enqueue(context.Background(), EnqueueParams{Type: "deadline", MaxAttempts: 1})
	if err != nil {
		t.Fatal(err)
	}

	started := make(chan struct{})
	runner := NewRunner(store, map[string]Handler{
		"deadline": func(ctx context.Context, _ Job) error {
			close(started)
			<-ctx.Done()
			return ctx.Err()
		},
	})
	runner.CancelPollInterval = time.Millisecond
	runner.ShutdownGracePeriod = 50 * time.Millisecond
	ctx := newManualDeadlineContext()

	done := make(chan error, 1)
	go func() {
		done <- runner.RunOnce(ctx)
	}()
	<-started
	ctx.cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	stored, err := store.Get(context.Background(), job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != StatusCancelled || stored.Error != "" {
		t.Fatalf("expected deadline shutdown to cancel job without failure, got %#v", stored)
	}
}

func TestRunnerFailsNonShutdownDeadlineErrors(t *testing.T) {
	t.Parallel()

	store := NewStore(openJobsDB(t, filepath.Join(t.TempDir(), "kapsel.db")))
	job, err := store.Enqueue(context.Background(), EnqueueParams{Type: "deadline", MaxAttempts: 1})
	if err != nil {
		t.Fatal(err)
	}
	runner := NewRunner(store, map[string]Handler{
		"deadline": func(context.Context, Job) error {
			return context.DeadlineExceeded
		},
	})

	if err := runner.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	stored, err := store.Get(context.Background(), job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != StatusFailed || !strings.Contains(stored.Error, context.DeadlineExceeded.Error()) {
		t.Fatalf("expected non-shutdown deadline to fail job, got %#v", stored)
	}
}

type manualDeadlineContext struct {
	context.Context
	done chan struct{}
}

func newManualDeadlineContext() *manualDeadlineContext {
	return &manualDeadlineContext{Context: context.Background(), done: make(chan struct{})}
}

func (c *manualDeadlineContext) Done() <-chan struct{} {
	return c.done
}

func (c *manualDeadlineContext) Err() error {
	select {
	case <-c.done:
		return context.DeadlineExceeded
	default:
		return nil
	}
}

func (c *manualDeadlineContext) cancel() {
	close(c.done)
}
