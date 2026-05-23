package jobs

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"kapsel/internal/redact"
)

type Handler func(context.Context, Job) error

type Runner struct {
	store              *Store
	handlers           map[string]Handler
	StaleAfter         time.Duration
	CancelPollInterval time.Duration
	// ShutdownGracePeriod bounds how long RunOnce waits for a cancelled
	// handler to return. If it expires, the running job is left for stale
	// recovery instead of being finalized with incomplete shutdown state.
	ShutdownGracePeriod time.Duration
	Logger              *slog.Logger
	Now                 func() time.Time
	runOnce             func(context.Context) error
}

const (
	maxLogErrorLength          = 1200
	defaultShutdownGracePeriod = 5 * time.Second
)

func NewRunner(store *Store, handlers map[string]Handler) *Runner {
	return &Runner{
		store:               store,
		handlers:            handlers,
		StaleAfter:          15 * time.Minute,
		CancelPollInterval:  500 * time.Millisecond,
		ShutdownGracePeriod: defaultShutdownGracePeriod,
	}
}

func (r *Runner) RunLoop(ctx context.Context, idleDelay time.Duration) error {
	if idleDelay <= 0 {
		idleDelay = time.Second
	}

	for {
		if err := r.runLoopOnce(ctx); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			r.logRunnerError(ctx, "job runner iteration failed", err)
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(idleDelay):
		}
	}
}

func (r *Runner) runLoopOnce(ctx context.Context) error {
	if r.runOnce != nil {
		return r.runOnce(ctx)
	}

	return r.RunOnce(ctx)
}

func (r *Runner) RunOnce(ctx context.Context) error {
	job, ok, err := r.store.Claim(ctx, r.now(), r.StaleAfter)
	if err != nil || !ok {
		return err
	}
	r.logJob(ctx, slog.LevelInfo, "job started", job, nil)

	handler, ok := r.handlers[job.Type]
	if !ok {
		err := fmt.Errorf("missing handler for job type %q", job.Type)
		if failErr := r.store.Fail(ctx, job.ID, err, r.now()); failErr != nil {
			return failErr
		}
		r.logCurrentJob(ctx, slog.LevelError, "job failed", job.ID, err)
		return nil
	}

	jobCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	done := make(chan error, 1)
	go func() {
		defer func() {
			if recovered := recover(); recovered != nil {
				done <- fmt.Errorf("job handler panicked: %v", recovered)
			}
		}()
		done <- handler(jobCtx, job)
	}()

	poll := r.CancelPollInterval
	if poll <= 0 {
		poll = 500 * time.Millisecond
	}
	ticker := time.NewTicker(poll)
	defer ticker.Stop()

	for {
		select {
		case err := <-done:
			return r.finish(context.WithoutCancel(ctx), job.ID, err, ctx.Err() != nil)
		case <-ticker.C:
			current, err := r.store.Get(ctx, job.ID)
			if err != nil {
				if ctx.Err() != nil {
					cancel()
					return r.finishAfterShutdown(ctx, job, done)
				}
				return err
			}
			if current.CancelRequested {
				cancel()
				continue
			}
			if err := r.store.RenewLease(ctx, job.ID); err != nil {
				if ctx.Err() != nil {
					cancel()
					return r.finishAfterShutdown(ctx, job, done)
				}
				return err
			}
		case <-ctx.Done():
			cancel()
			return r.finishAfterShutdown(ctx, job, done)
		}
	}
}

func (r *Runner) finishAfterShutdown(ctx context.Context, job Job, done <-chan error) error {
	wait := r.ShutdownGracePeriod
	if wait <= 0 {
		wait = defaultShutdownGracePeriod
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()

	select {
	case handlerErr := <-done:
		return r.finish(context.WithoutCancel(ctx), job.ID, handlerErr, true)
	case <-timer.C:
		r.logJob(context.WithoutCancel(ctx), slog.LevelWarn, "job shutdown wait exceeded", job, ctx.Err())
		return ctx.Err()
	}
}

func (r *Runner) finish(ctx context.Context, id string, err error, shutdown bool) error {
	current, getErr := r.store.Get(ctx, id)
	if getErr != nil {
		return getErr
	}
	if current.Status != StatusRunning {
		r.logFinishedJob(ctx, id, current.Status, err)
		return nil
	}
	hasFinalResult := current.ResultCommitted

	if current.CancelRequested && !hasFinalResult {
		if err := r.store.MarkCancelled(ctx, id); err != nil {
			return err
		}
		r.logCurrentJob(ctx, slog.LevelInfo, "job cancelled", id, nil)
		return nil
	}
	if err != nil {
		if isContextCancellation(err, shutdown) && !hasFinalResult {
			if err := r.store.MarkCancelled(ctx, id); err != nil {
				return err
			}
			r.logCurrentJob(ctx, slog.LevelInfo, "job cancelled", id, nil)
			return nil
		}
		if isContextCancellation(err, shutdown) && hasFinalResult {
			if err := r.store.Complete(ctx, id); err != nil {
				return err
			}
			r.logCurrentJob(ctx, slog.LevelInfo, "job completed", id, nil)
			return nil
		}

		if failErr := r.store.Fail(ctx, id, err, retryRunAfter(r.now(), err)); failErr != nil {
			return failErr
		}
		r.logCurrentJob(ctx, slog.LevelError, "job failed", id, err)
		return nil
	}

	if err := r.store.Complete(ctx, id); err != nil {
		return err
	}
	r.logCurrentJob(ctx, slog.LevelInfo, "job completed", id, nil)
	return nil
}

func isContextCancellation(err error, shutdown bool) bool {
	return errors.Is(err, context.Canceled) || (shutdown && errors.Is(err, context.DeadlineExceeded))
}

func (r *Runner) logFinishedJob(ctx context.Context, id string, status Status, cause error) {
	if cause != nil {
		r.logCurrentJob(ctx, slog.LevelError, "job finished after handler error", id, cause)
		return
	}
	switch status {
	case StatusSucceeded:
		r.logCurrentJob(ctx, slog.LevelInfo, "job completed", id, nil)
	case StatusCancelled:
		r.logCurrentJob(ctx, slog.LevelInfo, "job cancelled", id, nil)
	case StatusFailed:
		r.logCurrentJob(ctx, slog.LevelError, "job failed", id, cause)
	default:
		r.logCurrentJob(ctx, slog.LevelWarn, "job finished in unexpected state", id, nil)
	}
}

type retryDelayedError interface {
	RetryDelay() time.Duration
}

func retryRunAfter(now time.Time, err error) time.Time {
	if err == nil {
		return now
	}
	var delayed retryDelayedError
	if errors.As(err, &delayed) {
		if delay := delayed.RetryDelay(); delay > 0 {
			return now.Add(delay).UTC()
		}
	}

	return now
}

func (r *Runner) now() time.Time {
	if r != nil && r.Now != nil {
		return r.Now().UTC()
	}

	return time.Now().UTC()
}

func (r *Runner) logCurrentJob(ctx context.Context, level slog.Level, message string, id string, cause error) {
	job, err := r.store.Get(ctx, id)
	if err != nil {
		return
	}
	r.logJob(ctx, level, message, job, cause)
}

func (r *Runner) logRunnerError(ctx context.Context, message string, cause error) {
	logger := r.Logger
	if logger == nil {
		logger = slog.Default()
	}
	if !logger.Enabled(ctx, slog.LevelError) {
		return
	}

	logger.LogAttrs(ctx, slog.LevelError, message, slog.String("error", sanitizeLogText(cause.Error())))
}

func (r *Runner) logJob(ctx context.Context, level slog.Level, message string, job Job, cause error) {
	logger := r.Logger
	if logger == nil {
		logger = slog.Default()
	}
	if !logger.Enabled(ctx, level) {
		return
	}
	attrs := []slog.Attr{
		slog.String("job_id", job.ID),
		slog.String("job_type", job.Type),
		slog.String("job_status", string(job.Status)),
		slog.Int("attempts", job.Attempts),
	}
	if cause != nil {
		attrs = append(attrs, slog.String("error", sanitizeLogText(cause.Error())))
	}
	logger.LogAttrs(ctx, level, message, attrs...)
}

func sanitizeLogText(text string) string {
	return redact.Text(text, maxLogErrorLength)
}
