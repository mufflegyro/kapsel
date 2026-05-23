# Restrict direct download URL targets

## Summary

Direct downloads accept arbitrary HTTP(S) URLs and pass them to `yt-dlp`, creating an SSRF-style server-side fetch primitive when an instance is exposed.

## Requirements

- Restrict direct downloads to YouTube URLs by default.
- If arbitrary URLs remain supported, require explicit configuration and block private, link-local, loopback, and redirected private targets.
- Preserve channel URL normalization for channel flows.

## Acceptance Criteria

- Tests prove non-YouTube direct download URLs are rejected by default.
- Tests prove supported YouTube direct video URL variants still normalize correctly.
- `go test ./...` passes.

## Notes

- Review refs: `internal/server/server.go:830-840`, `internal/download/downloader.go:167-189`, `internal/download/downloader.go:376-394`.
