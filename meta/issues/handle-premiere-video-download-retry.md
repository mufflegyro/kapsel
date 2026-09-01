# Handle scheduled premiere videos with smart retry scheduling

## Summary

When yt-dlp encounters a YouTube video that is a scheduled premiere (not yet
published), it exits with a clear message like:

```
ERROR: [youtube] fEDRRQgykd8: Premieres in 26 hours (exit status 1)
```

Currently, the download job system treats this as a generic retryable error and
schedules a retry in 10 minutes (`DefaultYTDLPRetryDelay`). The job will keep
failing every 10 minutes for the entire duration of the premiere window,
wasting compute and filling logs. It only succeeds if the video is still in the
queue when the premiere actually happens.

## Problem

The error is classified via `ytdlpJobError` → `ytdlpRetryError` with a hardcoded
`DefaultYTDLPRetryDelay` of 10 minutes. There is no special handling for the
"Premieres in" pattern, unlike `isMembersOnlyYTDLPFailure` (terminal success)
or `isYTDLPAuthChallenge` (1-hour auth retry delay).

The error flow:

1. `handlePayload` runs yt-dlp → exit status 1, no JSON metadata produced
2. `parseDownloadMetadataOutput` fails (no JSON to parse)
3. Falls through to `return ingestResult{}, ytdlpJobError(command, output, runErr)`
4. `ytdlpRetryDelay` checks for auth challenge → not found → returns 10 min
5. Job is failed with `runAfter = now + 10 min`, repeats until the premiere

## Root cause

`ytdlpRetryDelay` in `internal/download/ytdlp.go` has no awareness of the
"Premieres in" message. The premiere duration is clearly stated in the yt-dlp
output (e.g., "26 hours"), but it is not parsed or used to inform the retry
schedule.

## Proposed fix

Add a `parsePremiereDelay` function that extracts the time-until-premiere from
yt-dlp's error output, and wire it into `ytdlpRetryDelay` so the retry is
scheduled for just after the premiere starts (duration + 30-minute buffer).

### Design

1. **New function** `parsePremiereDelay(text string) (time.Duration, bool)` in
   `internal/download/ytdlp.go`:
   - Case-insensitively searches for `"premieres in"` (or `"premiere in"`) in
     the combined output+error text.
   - Uses a regex to extract duration components after the match:
     `(\d+)\s*(hours?|minutes?|seconds?|days?)`
   - Sums the components into a `time.Duration`, adds a 30-minute buffer
     (`DefaultPremiereBuffer`), and returns it.
   - Returns `(0, false)` if no match is found or the duration is zero.

2. **Modify** `ytdlpRetryDelay` to check `parsePremiereDelay` after the auth
   challenge check and before the default 10-minute return:

   ```go
   func ytdlpRetryDelay(output []byte, err error) time.Duration {
       text := string(output)
       if err != nil {
           text += "\n" + err.Error()
       }
       if isYTDLPAuthChallenge(text) {
           return DefaultYTDLPAuthRetryDelay
       }
       if delay, ok := parsePremiereDelay(text); ok {
           return delay
       }
       return DefaultYTDLPRetryDelay
   }
   ```

3. **New constant** `DefaultPremiereBuffer = 30 * time.Minute` — added time
   beyond the stated premiere duration so the retry lands after the video has
   been published and processed.

### Supported message formats

- `"Premieres in 26 hours"` → 26h + 30m buffer
- `"Premieres in 30 minutes"` → 30m + 30m buffer
- `"Premieres in 1 hour 30 minutes"` → 1h30m + 30m buffer
- `"Premieres in 3 days"` → 72h + 30m buffer
- `"Premiere in 5 seconds"` → 5s + 30m buffer (30m minimum effective delay)

### Edge cases

| Case | Behavior |
|------|----------|
| Video is already live | yt-dlp succeeds normally, no change |
| "Premieres in" not in output | Falls through to default 10-min retry |
| Duration parsing fails | Falls through to default 10-min retry |
| Premiere is very short (< 30 min) | Retry in 30 min (buffer minimum) |
| Video is members-only AND premiere | Members-only check runs first (terminal), no conflict |
| Multiple "Premieres in" matches across output lines | First match after "premieres in" text is used |

### Testing

- Unit test `parsePremiereDelay` with each message format variant.
- Unit test that `ytdlpRetryDelay` returns the parsed delay for premiere
  messages and unchanged defaults for non-premiere messages.
- Integration: submit a download job for a known premiere video, verify the
  job is failed with `run_after` set to approximately now + (premiere duration
  + 30 min).

## Acceptance Criteria

- yt-dlp exit with "Premieres in X hours" does not retry every 10 minutes.
- The job's `run_after` timestamp is set to approximately `now + stated_duration + 30m`.
- Non-premiere failures continue to retry with the existing 10-minute or 1-hour
  delays.
- `isMembersOnlyYTDLPFailure` (runs before the metadata parse) is unaffected.

## Resolution

Implemented in `internal/download/ytdlp.go` exactly as designed, with the classification order now: members-only terminal (checked in `handlePayload`, before retry delay) → auth challenge (1 hour) → premiere delay → default 10 minutes.

- `parsePremiereDelay(text string) (time.Duration, bool)` matches `(?i)premieres?\s+in\s+...` and extracts number/unit components with a second regex, so multi-part durations ("1 hour 30 minutes", "2 days, 4 hours") and singular/plural units all sum correctly. Comma or whitespace separators are accepted. Returns `(0, false)` when there is no premiere message, the matched duration is zero, or the stated duration cannot be parsed — all fall through to the default 10-minute retry.
- `DefaultPremiereBuffer = 30 * time.Minute` is added to the parsed duration, so the effective minimum premiere retry is ~30 minutes and the retry lands after the video has published and processed.
- A consequence worth noting: with `MaxAttempts: 3`, the old 10-minute retry behavior didn't just spam logs — any premiere more than ~20 minutes out exhausted all 3 attempts and the job was permanently failed before the video ever became available. The premiere-aware delay keeps the first retry inside the existing attempt budget.

Tests: `TestParsePremiereDelay` (every stated message format plus negative cases: no premiere message, auth-challenge text, premiere with no parseable duration), `TestYTDLPRetryDelayClassifiesPremieres` (premiere delay returned; auth and default paths unchanged), `TestDownloadHandlerDelaysPremiereRetry` (full job-runner integration: job re-queued with `run_after = now + stated duration + 30m`). Status: **landed 2026-09-01**.