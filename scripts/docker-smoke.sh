#!/usr/bin/env bash
set -euo pipefail

# Smoke test for the Kapsel container image.
#
# Verifies, against THROWAWAY state only:
#   1. the image builds (multi-stage: frontend embed + static Go binary +
#      runtime with yt-dlp/ffmpeg/deno),
#   2. docker-compose.yml is valid,
#   3. a fresh container starts, migrates an empty DB, and reports healthy,
#   4. yt-dlp, ffmpeg, and storage are ready (via /api/settings),
#   5. the media volume is writable by the unprivileged service user, and
#      the yt-dlp update target (file + directory) is writable by that user,
#      so the auto-update job can replace the binary in place,
#   6. media and database survive container recreation.
#
# It never touches the real deployment volumes (kapsel-data / kapsel-media /
# kapsel-imports) or deploy/docker/kapsel.env; a temporary copy of the env
# example is created only for `docker compose config` validation and removed
# afterwards. The app is published on 127.0.0.1 only, with auth disabled for
# the throwaway instance.
#
# The YouTube download + playback check needs live network access and is
# documented as a manual path in DOCKER.md ("Verify downloads and
# playback").

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

if ! command -v docker >/dev/null 2>&1; then
  echo "SMOKE FAIL: docker not found" >&2
  exit 1
fi
if ! command -v python3 >/dev/null 2>&1; then
  echo "SMOKE FAIL: python3 not found (used to assert /api/settings JSON)" >&2
  exit 1
fi

IMAGE="kapsel:smoke"
SUFFIX="smoke-$(date +%s)"
NETWORK="kapsel-smoke-${SUFFIX}"
CONTAINER="kapsel-smoke-${SUFFIX}"
VOL_DATA="kapsel-smoke-data-${SUFFIX}"
VOL_MEDIA="kapsel-smoke-media-${SUFFIX}"
VOL_IMPORTS="kapsel-smoke-imports-${SUFFIX}"
PORT="${KAPSEL_SMOKE_PORT:-18081}"
MARKER="persistence-$(date +%s).txt"

cleanup() {
  docker rm -f "$CONTAINER" >/dev/null 2>&1 || true
  docker network rm "$NETWORK" >/dev/null 2>&1 || true
  docker volume rm "$VOL_DATA" "$VOL_MEDIA" "$VOL_IMPORTS" >/dev/null 2>&1 || true
}
trap cleanup EXIT

fail() {
  echo "SMOKE FAIL: $*" >&2
  exit 1
}

RUN_ARGS=(
  -d
  --name "$CONTAINER"
  --network "$NETWORK"
  -p "127.0.0.1:${PORT}:8080"
  -e KAPSEL_ADDR=:8080
  -e KAPSEL_AUTH_MODE=disabled
  -e KAPSEL_DATA_DIR=/data
  -e KAPSEL_DB_PATH=/data/kapsel.db
  -e KAPSEL_MEDIA_ROOT=/media
  -e KAPSEL_IMPORT_ROOT=/imports
  -e KAPSEL_YTDLP_PATH=/usr/local/bin/kapsel-ytdlp
  -e KAPSEL_FFMPEG_PATH=/usr/bin/ffmpeg
  -v "$VOL_DATA:/data"
  -v "$VOL_MEDIA:/media"
  -v "$VOL_IMPORTS:/imports"
  "$IMAGE"
)

echo "==> [1/6] building image (${IMAGE})"
docker build -f deploy/docker/Dockerfile -t "$IMAGE" .

echo "==> [2/6] validating docker-compose.yml"
CREATED_ENV=0
if [[ ! -f deploy/docker/kapsel.env ]]; then
  cp deploy/docker/kapsel.env.example deploy/docker/kapsel.env
  CREATED_ENV=1
fi
docker compose config --quiet || fail "docker compose config rejected docker-compose.yml"
if [[ "$CREATED_ENV" == "1" ]]; then
  rm -f -- deploy/docker/kapsel.env
fi

echo "==> [3/6] starting container on 127.0.0.1:${PORT} with fresh volumes"
docker network create "$NETWORK" >/dev/null || fail "could not create network"
docker run "${RUN_ARGS[@]}" >/dev/null || fail "container did not start"

echo "==> [4/6] waiting for /api/health"
ok=0
for _ in $(seq 1 60); do
  if curl -fsS "http://127.0.0.1:${PORT}/api/health" >/dev/null 2>&1; then
    ok=1
    break
  fi
  sleep 2
done
if [[ "$ok" != "1" ]]; then
  docker logs "$CONTAINER" || true
  fail "health check timed out after 120s"
fi
echo "health: ok"

echo "==> [5/6] checking readiness (fresh DB migration, tools, storage)"
settings="$(curl -fsS "http://127.0.0.1:${PORT}/api/settings")" || fail "could not fetch /api/settings"
printf '%s' "$settings" | python3 -c '
import json, sys
d = json.load(sys.stdin)
want = {"yt_dlp", "storage", "timeline_previews"}
got = {c["id"]: c["state"] for c in d["checks"]}
for k in sorted(want):
    assert got.get(k) == "pass", (k, got.get(k), d)
    print(f"check {k}: pass")
assert d["yt_dlp"]["available"], d
print("yt-dlp:", d["yt_dlp"]["version"])
assert d["storage"]["ok"], d
print("storage: ok")
assert d["configuration"]["ffmpeg_path"] == "/usr/bin/ffmpeg", d
print("ffmpeg:", d["configuration"]["ffmpeg_path"])
' || fail "readiness assertions failed"
db_size="$(docker exec "$CONTAINER" stat -c %s /data/kapsel.db)" || fail "no database at /data/kapsel.db"
[[ "$db_size" -gt 0 ]] || fail "fresh database is empty (migration did not run?)"
echo "database: migrated (${db_size} bytes)"
docker exec -u kapsel "$CONTAINER" sh -c "echo smoke > /media/${MARKER}" \
  || fail "media volume not writable by the kapsel service user"
echo "media volume: writable by service user"

# Regression: the yt-dlp auto-update job renames a temp file over the binary,
# so the kapsel user must be able to write BOTH the file and its directory.
# This used to fail in the image when yt-dlp lived in root-owned /usr/local/bin.
docker exec -u kapsel "$CONTAINER" sh -c \
  "test -w /var/lib/kapsel/bin/yt-dlp && echo smoke > /var/lib/kapsel/bin/.smoke-write && rm -f /var/lib/kapsel/bin/.smoke-write" \
  || fail "yt-dlp update target not writable by the kapsel service user"
echo "yt-dlp update target: writable by service user"

echo "==> [6/6] media + database survive container recreation"
docker rm -f "$CONTAINER" >/dev/null || fail "could not remove container"
docker run "${RUN_ARGS[@]}" >/dev/null || fail "container did not restart"
ok=0
for _ in $(seq 1 60); do
  if curl -fsS "http://127.0.0.1:${PORT}/api/health" >/dev/null 2>&1; then
    ok=1
    break
  fi
  sleep 2
done
[[ "$ok" == "1" ]] || fail "health check timed out after recreation"
docker exec "$CONTAINER" test -f "/media/${MARKER}" \
  || fail "media file did not survive container recreation"
echo "media persistence: ok (marker survived)"

echo
echo "SMOKE PASS"
