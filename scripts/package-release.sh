#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
DIST_DIR="${ROOT_DIR}/dist"

mkdir -p "${DIST_DIR}"

platforms=(
  "linux amd64"
  "linux arm64"
  "darwin amd64"
  "darwin arm64"
)

for platform in "${platforms[@]}"; do
  read -r goos goarch <<<"${platform}"
  out="${DIST_DIR}/replicon-${goos}-${goarch}"
  echo "building ${out}"
  GOOS="${goos}" GOARCH="${goarch}" CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o "${out}" "${ROOT_DIR}"
done
