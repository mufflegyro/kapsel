# syntax=docker/dockerfile:1
# check=skip=SecretsUsedInArgOrEnv

# Multi-stage build for the Kapsel (Yummle) container image.
#
#   Stage 1: node   -> build the frontend into internal/web/static (embedded)
#   Stage 2: golang -> compile the release binary with the embedded UI
#   Stage 3: debian -> runtime: kapsel + yt-dlp (nightly) + ffmpeg + deno
#
# The runtime image binds KAPSEL_ADDR (default :8080 => 0.0.0.0:8080) so the
# container can be published on a home network or sit behind a reverse proxy.
# See docs/docker.md for HTTPS termination and the auth requirements that come
# with binding outside loopback.

# ---------------------------------------------------------------------------
# Stage 1: frontend
# ---------------------------------------------------------------------------
FROM node:25.9.0-bookworm-slim AS frontend

# pnpm is pinned in frontend/package.json ("packageManager"); corepack is no
# longer bundled with Node 25, so install pnpm explicitly.
RUN npm install --global pnpm@10.33.2

WORKDIR /src/frontend
COPY frontend/package.json frontend/pnpm-lock.yaml ./
RUN pnpm install --frozen-lockfile
COPY frontend/index.html frontend/vite.config.js ./
COPY frontend/src ./src
# outDir is ../internal/web/static relative to the frontend root => /src/internal/web/static
RUN pnpm build

# ---------------------------------------------------------------------------
# Stage 2: Go build
# ---------------------------------------------------------------------------
FROM golang:1.26-bookworm AS build

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download

COPY cmd/ ./cmd/
COPY internal/ ./internal/
# Overwrite internal/web/static with the frontend build from stage 1.
COPY --from=frontend /src/internal/web/static ./internal/web/static

# modernc.org/sqlite is pure Go, so a static binary needs no cgo.
RUN CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o /out/kapsel ./cmd/kapsel

# ---------------------------------------------------------------------------
# Stage 3: runtime
# ---------------------------------------------------------------------------
FROM debian:bookworm-slim AS runtime

ARG TARGETARCH

# Runtime tools: ffmpeg for timeline previews/merging, curl for the
# healthcheck, unzip to unpack deno, ca-certificates for HTTPS fetches.
RUN apt-get update \
    && apt-get install -y --no-install-recommends \
        ca-certificates \
        curl \
        ffmpeg \
        unzip \
    && rm -rf /var/lib/apt/lists/*

# yt-dlp nightly standalone binary. The yt-dlp_linux* assets are
# self-contained (PyInstaller-bundled) and support `--update-to nightly`, so
# Kapsel's KAPSEL_YTDLP_UPDATE_INTERVAL job can keep the binary current in
# place inside the container.
RUN case "${TARGETARCH}" in \
      amd64) asset=yt-dlp_linux ;; \
      arm64) asset=yt-dlp_linux_aarch64 ;; \
      *) echo "unsupported architecture: ${TARGETARCH}" >&2; exit 1 ;; \
    esac \
    && curl -fsSLo /usr/local/bin/yt-dlp \
        "https://github.com/yt-dlp/yt-dlp-nightly-builds/releases/latest/download/${asset}" \
    && chmod 0755 /usr/local/bin/yt-dlp \
    && /usr/local/bin/yt-dlp --version

# Deno: JS runtime for current YouTube client challenges. yt-dlp selects it
# via --js-runtimes deno:/usr/local/bin/deno (see /usr/local/bin/kapsel-ytdlp).
RUN case "${TARGETARCH}" in \
      amd64) asset=deno-x86_64-unknown-linux-gnu.zip ;; \
      arm64) asset=deno-aarch64-unknown-linux-gnu.zip ;; \
    esac \
    && curl -fsSLo /tmp/deno.zip \
        "https://github.com/denoland/deno/releases/latest/download/${asset}" \
    && unzip -d /usr/local/bin /tmp/deno.zip deno \
    && rm -f /tmp/deno.zip \
    && chmod 0755 /usr/local/bin/deno \
    && /usr/local/bin/deno --version

# Non-root service user. The entrypoint chowns the mounted volumes and drops
# privileges before exec'ing kapsel.
RUN groupadd --system kapsel \
    && useradd --system --gid kapsel --home-dir /var/lib/kapsel --shell /usr/sbin/nologin kapsel \
    && install -d -o kapsel -g kapsel /var/lib/kapsel

COPY --from=build /out/kapsel /opt/kapsel/kapsel
COPY deploy/docker/kapsel-ytdlp /usr/local/bin/kapsel-ytdlp
COPY deploy/docker/docker-entrypoint.sh /usr/local/bin/docker-entrypoint.sh
RUN chmod 0755 /usr/local/bin/kapsel-ytdlp /usr/local/bin/docker-entrypoint.sh

# Container defaults. Override via environment (docker-compose env_file /
# environment). KAPSEL_ADDR=:8080 binds 0.0.0.0:8080 inside the container;
# publish with ports: in docker-compose.yml.
ENV KAPSEL_ADDR=:8080 \
    KAPSEL_DATA_DIR=/data \
    KAPSEL_DB_PATH=/data/kapsel.db \
    KAPSEL_MEDIA_ROOT=/media \
    KAPSEL_IMPORT_ROOT=/imports \
    KAPSEL_YTDLP_PATH=/usr/local/bin/kapsel-ytdlp \
    KAPSEL_YTDLP_UPDATE_INTERVAL=24h \
    KAPSEL_FFMPEG_PATH=/usr/bin/ffmpeg \
    KAPSEL_AUTH_MODE=required

EXPOSE 8080

HEALTHCHECK --interval=30s --timeout=5s --start-period=20s --retries=5 \
    CMD curl -fsS http://127.0.0.1:8080/api/health || exit 1

ENTRYPOINT ["/usr/local/bin/docker-entrypoint.sh"]
