#!/usr/bin/env bash
set -euo pipefail

# Deploy minimal source to a remote host and rebuild the Docker image.
#
# Usage:
#   ./scripts/deploy.sh [--dry-run] <host> [push-only|build-only]
#
#   host          SSH hostname or user@host (DEPLOY_HOST env override)
#   --dry-run     show what would be pushed without transferring
#   push-only     rsync only, no remote build
#   build-only    remote build only, no rsync
#
# Config env vars:
#   DEPLOY_HOST       - SSH alias (default: first arg)
#   DEPLOY_PATH       - remote directory (default: /opt/kapsel)
#   DEPLOY_RSYNC_OPTS - extra rsync flags (default: -z)

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

HOST="${DEPLOY_HOST:-${1:-}}"
REMOTE_PATH="${DEPLOY_PATH:-/opt/kapsel}"
RSYNC_OPTS="${DEPLOY_RSYNC_OPTS:--z}"
MODE="${2:-all}"

fail() { echo "deploy: $*" >&2; exit 1; }
info() { echo "deploy: $*"; }

if [[ -z "$HOST" ]]; then
  echo "usage: $0 [--dry-run] <host> [push-only|build-only]"
  exit 2
fi

if [[ "$1" == "--dry-run" ]]; then
  DRY_RUN=1
  HOST="${DEPLOY_HOST:-${2:-}}"
  MODE="${3:-all}"
  [[ -z "$HOST" ]] && fail "specify a host after --dry-run"
fi

# Minimal build context (verified: same image digest as full repo)
BUILD_PATHS=(
  .dockerignore
  deploy/docker
  go.mod
  go.sum
  cmd
  internal
  frontend/package.json
  frontend/pnpm-lock.yaml
  frontend/index.html
  frontend/vite.config.js
  frontend/src
)

# ---- 1. build file list and rsync to host --------------------------------
if [[ "$MODE" != "build-only" ]]; then
  FILES_LIST="$(mktemp /tmp/kapsel-deploy.XXXXXX)"
  trap 'rm -f "$FILES_LIST"' EXIT

  for path in "${BUILD_PATHS[@]}"; do
    if [[ -d "$path" ]]; then
      find "$path" -type f >> "$FILES_LIST"
    elif [[ -f "$path" ]]; then
      echo "$path" >> "$FILES_LIST"
    fi
  done
  sort -u -o "$FILES_LIST" "$FILES_LIST"

  FILE_COUNT="$(wc -l < "$FILES_LIST" | tr -d ' ')"
  info "pushing ${FILE_COUNT} files to ${HOST}:${REMOTE_PATH}"

  ssh "$HOST" -- "mkdir -p '${REMOTE_PATH}'" || fail "cannot create remote dir"

  if [[ -n "${DRY_RUN:-}" ]]; then
    rsync -n --itemize-changes -a --delete --delete-excluded "${RSYNC_OPTS}" \
      --files-from="$FILES_LIST" . "${HOST}:${REMOTE_PATH}/"
    info "dry-run: ${FILE_COUNT} files would be transferred"
  else
    rsync --itemize-changes -a --delete --delete-excluded "${RSYNC_OPTS}" \
      --files-from="$FILES_LIST" . "${HOST}:${REMOTE_PATH}/"
    info "push complete"
  fi
fi

# ---- 2. rebuild + restart on remote host ---------------------------------
if [[ "$MODE" != "push-only" ]]; then
  if [[ -n "${DRY_RUN:-}" ]]; then
    info "dry-run: would run on ${HOST}: cd ${REMOTE_PATH} && docker compose up -d --build"
  else
    info "rebuilding and restarting on ${HOST}..."
    ssh "$HOST" -- "cd '${REMOTE_PATH}' && docker compose up -d --build" || fail "remote build failed"
    info "rebuild and restart complete"
  fi
fi

info "done"
