# Scheduler ownership

Kapsel runs recurring work (channel auto-downloads, retention cleanup, yt-dlp
self-updates, release checks) exclusively as **jobs** in the `jobs` table. There
are no cron processes and no inline background work: every recurring
responsibility is a job type with a handler, claimed by the job runner.

Three layers own different concerns. Each layer only talks downward.

## Layer 1 — composition root (`internal/app/app.go`)

Owns **cadence only**. `App.RunJobs` starts one ticker loop per scheduler
family (`runPeriodicScheduler`) and the job runner's poll loop. A loop:

- ticks hourly (cadence, not policy — most ticks legitimately do nothing),
- calls exactly one scheduling-policy function per tick (an `Ensure*` function),
- logs failures with the scheduler's name, and
- stops when its context is cancelled.

A loop never queries the job table, computes intervals, or executes domain
work. If a scheduler's work is worth doing, the policy function turns it into a
job; the runner executes it and the jobs UI shows the history.

## Layer 2 — scheduling policy (`Ensure*` functions)

Owns **whether a job of a given type should exist right now**:

- dedupe against active (queued/running, not cancel-requested) jobs;
- interval throttling after a successful run (`scheduledJobDue` in
  `internal/download/schedule.go`);
- failure handling: retention and yt-dlp updates re-arm at the next tick with
  no exponential backoff, because those jobs are local, idempotent, and
  bounded, and a persistent failure stays visible as a failed job. Release
  checks are the exception: they hit a rate-limited external API, so
  `internal/updater` backs off failures exponentially, capped at the interval;
- `run_after` computation, including per-channel jitter for auto-downloads.

Policy functions read the job table only through `jobs.Store` methods
(`HasActiveJobByType`, `LatestJobOfType`, `FindOrEnqueue`, `ActiveByType*`) —
never with raw SQL. Reads of domain tables (e.g. subscribed channels in
`subscribedChannels`) stay in the domain package and are not job-table checks.

The current policy functions and their jobs:

| Scheduler | Policy function | Default cadence |
|---|---|---|
| channel auto-download | `download.EnsureChannelAutoDownloadJobs` | 24h per channel + jitter |
| retention cleanup | `download.EnsureRetentionJobs` | 24h |
| yt-dlp self-update | `download.EnsureYTDLPUpdateJobs` | 24h |
| release check | `updater.EnsureReleaseCheckJobs` | config `KAPSEL_UPDATE_CHECK_INTERVAL`; failures back off (15m → 2×, capped at interval) |

## Layer 3 — `jobs.Store` (`internal/jobs`)

Owns the job table as a durable queue: enqueue/dedupe
(`FindOrEnqueue`, `ActiveByPayload*`), claim/complete/fail/cancel lifecycle,
scheduling introspection (`HasActiveJobByType`, `LatestJobOfType`), and the
`RFC3339_NANO` collation for correct timestamp ordering. The store holds no
scheduling policy — it cannot know intervals, backoff, or jitter.

## Retention failure behavior

A failed retention cleanup re-arms at the next hourly scheduler tick — the
Ensure policy treats a failed latest job as immediately due, with no
exponential backoff. Rationale: the cleanup pass is local, idempotent, and
bounded (candidate LIMIT + targeted deletes), so a retry costs little; the
hourly tick is the retry cadence; and a persistent failure remains visible as
a failed job in the jobs UI rather than being hidden behind growing backoff.
This is a deliberate contrast with release checks, which do back off because
their failure mode is a rate-limited external API. If retention passes ever
start failing persistently, revisit this decision — do not add backoff
speculatively.

## Failure recap

- Scheduler loop error → logged (`<name> scheduler failed`), retried next tick.
- Job execution failure → normal job retry/attempts semantics in
  `jobs.Store.Fail`; a retention/yt-dlp scheduled job re-arms on the next
  hourly tick, a release check backs off exponentially.
- Loops are context-cancelled on shutdown; no goroutine leaks beyond ctx.
