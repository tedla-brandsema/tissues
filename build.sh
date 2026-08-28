#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
BUILD_DIR="${ROOT_DIR}/build"
OUTPUT="${BUILD_DIR}/server"

mkdir -p "${BUILD_DIR}"
cd "${ROOT_DIR}"

echo "Building app/gcp/server -> ${OUTPUT}"
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
  go build -trimpath -ldflags="-s -w" -o build/server ./app/gcp/server

echo "Build complete: ${OUTPUT}"
