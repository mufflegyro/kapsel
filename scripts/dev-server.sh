#!/usr/bin/env sh
set -eu

pnpm --dir frontend build
mkdir -p tmp
go build -o tmp/kapsel-dev ./cmd/kapsel
exec ./tmp/kapsel-dev
