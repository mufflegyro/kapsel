#!/usr/bin/env bash
set -euo pipefail

# Container entrypoint for Kapsel (Yummle).
#
# 1. Ensure the mounted volumes exist and are owned by the kapsel service
#    user (named volumes start empty and root-owned; bind mounts may carry
#    foreign ownership).
# 2. Warn loudly when auth is disabled while bound to a non-loopback address.
# 3. Drop privileges and exec the kapsel binary so the server and its
#    sandboxed media tools (yt-dlp, ffmpeg) run unprivileged.

data_dir="${KAPSEL_DATA_DIR:-/data}"
media_root="${KAPSEL_MEDIA_ROOT:-/media}"
import_root="${KAPSEL_IMPORT_ROOT:-/imports}"

for dir in "$data_dir" "$media_root" "$import_root"; do
  mkdir -p "$dir"
  chown kapsel:kapsel "$dir"
done

if [[ "${KAPSEL_AUTH_MODE:-required}" != "required" \
      && "${KAPSEL_ADDR:-:8080}" != 127.0.0.1:* \
      && "${KAPSEL_ADDR:-:8080}" != localhost:* ]]; then
  echo "WARNING: KAPSEL_AUTH_MODE=${KAPSEL_AUTH_MODE} with KAPSEL_ADDR=${KAPSEL_ADDR} exposes Kapsel without authentication on a non-loopback interface." >&2
  echo "WARNING: Set KAPSEL_AUTH_MODE=required (with KAPSEL_AUTH_USERNAME / KAPSEL_AUTH_PASSWORD_HASH) before publishing this container beyond localhost." >&2
fi

exec setpriv --reuid=kapsel --regid=kapsel --init-groups /opt/kapsel/kapsel "$@"
