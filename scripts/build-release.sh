#!/usr/bin/env bash
set -euo pipefail
export LC_ALL=C

VERSION="${VERSION:-dev}"
ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
DIST_DIR="${DIST_DIR:-dist/${VERSION}}"
if [[ "$DIST_DIR" = /* ]]; then
  OUTPUT_DIR="$DIST_DIR"
else
  OUTPUT_DIR="$ROOT_DIR/$DIST_DIR"
fi

if ! command -v go >/dev/null 2>&1; then
  echo "go is required" >&2
  exit 1
fi
if ! command -v zip >/dev/null 2>&1; then
  echo "zip is required to create Windows archives" >&2
  exit 1
fi
if ! command -v tar >/dev/null 2>&1; then
  echo "tar is required to create Unix archives" >&2
  exit 1
fi

mkdir -p "$OUTPUT_DIR"
: > "$OUTPUT_DIR/SHA256SUMS"

targets=(
  "linux amd64 tar.gz"
  "linux arm64 tar.gz"
  "darwin amd64 tar.gz"
  "darwin arm64 tar.gz"
  "windows amd64 zip"
  "windows arm64 zip"
)
binaries=(tunnelmesh-server tunnelmesh-agent tunnelmesh-client)

checksum() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$1"
  else
    shasum -a 256 "$1"
  fi
}

for target in "${targets[@]}"; do
  read -r goos goarch archive_format <<<"$target"
  stage="$(mktemp -d)"
  for binary in "${binaries[@]}"; do
    output="$stage/$binary"
    [[ "$goos" == "windows" ]] && output+=".exe"
    echo "building $binary for $goos/$goarch"
    (cd "$ROOT_DIR" && CGO_ENABLED=0 GOOS="$goos" GOARCH="$goarch" go build -trimpath -ldflags="-s -w" -o "$output" "./cmd/$binary")
  done
  cp "$ROOT_DIR/README.md" "$stage/README.md"
  cp -R "$ROOT_DIR/docs" "$stage/docs"
  mkdir -p "$stage/deploy"
  cp -R "$ROOT_DIR/deploy/install" "$stage/deploy/install"
  cp -R "$ROOT_DIR/deploy/systemd" "$stage/deploy/systemd"
  base="tunnelmesh-${VERSION}-${goos}-${goarch}"
  if [[ "$archive_format" == "zip" ]]; then
    (cd "$stage" && zip -q -r "$OUTPUT_DIR/$base.zip" .)
    checksum "$OUTPUT_DIR/$base.zip" >> "$OUTPUT_DIR/SHA256SUMS"
  else
    tar -C "$stage" -czf "$OUTPUT_DIR/$base.tar.gz" .
    checksum "$OUTPUT_DIR/$base.tar.gz" >> "$OUTPUT_DIR/SHA256SUMS"
  fi
  rm -rf "$stage"
done

echo "release artifacts written to $OUTPUT_DIR"
