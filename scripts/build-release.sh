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

if [[ "$VERSION" != "dev" && ! "$VERSION" =~ ^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$ ]]; then
  echo "VERSION must be vMAJOR.MINOR.PATCH (an optional pre-release suffix is allowed for test builds): $VERSION" >&2
  exit 1
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

COMMIT="$(git -C "$ROOT_DIR" rev-parse HEAD)"
BUILD_TIME="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
SCHEMA_VERSION="$(sed -n 's/^[[:space:]]*SchemaVersion = \([0-9][0-9]*\)$/\1/p' "$ROOT_DIR/internal/storage/db.go" | head -n 1)"
if [[ ! "$SCHEMA_VERSION" =~ ^[0-9]+$ ]]; then
  echo "unable to read SchemaVersion from internal/storage/db.go" >&2
  exit 1
fi
MAJOR="${VERSION%%.*}"
MAJOR="${MAJOR#v}"
if [[ "$VERSION" == "dev" ]]; then
  MAJOR=0
fi
LDFLAGS="-s -w -X github.com/tunnelmesh/tunnelmesh/internal/build.Version=${VERSION} -X github.com/tunnelmesh/tunnelmesh/internal/build.Commit=${COMMIT} -X github.com/tunnelmesh/tunnelmesh/internal/build.BuildTime=${BUILD_TIME}"

targets=(
  "linux amd64 tar.gz"
  "linux arm64 tar.gz"
  "darwin amd64 tar.gz"
  "darwin arm64 tar.gz"
  "windows amd64 zip"
  "windows arm64 zip"
)
binaries=(tunnelmesh-server tunnelmesh-agent tunnelmesh-client)
platforms=()
archives=()

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
    (cd "$ROOT_DIR" && CGO_ENABLED=0 GOOS="$goos" GOARCH="$goarch" go build -trimpath -ldflags="$LDFLAGS" -o "$output" "./cmd/$binary")
  done
  cp "$ROOT_DIR/README.md" "$stage/README.md"
  cp -R "$ROOT_DIR/docs" "$stage/docs"
  mkdir -p "$stage/deploy"
  cp -R "$ROOT_DIR/deploy/install" "$stage/deploy/install"
  case "$goos" in
    linux)
      cp -R "$ROOT_DIR/deploy/systemd" "$stage/deploy/systemd"
      ;;
    darwin)
      cp -R "$ROOT_DIR/deploy/macos" "$stage/deploy/macos"
      ;;
    windows)
      cp -R "$ROOT_DIR/deploy/windows" "$stage/deploy/windows"
      ;;
  esac
  base="tunnelmesh-${VERSION}-${goos}-${goarch}"
  platform="${goos}-${goarch}"
  platforms+=("\"${goos}/${goarch}\"")
  if [[ "$archive_format" == "zip" ]]; then
    archive="${base}.zip"
    (cd "$stage" && zip -q -r "$OUTPUT_DIR/$archive" .)
  else
    archive="${base}.tar.gz"
    tar -C "$stage" -czf "$OUTPUT_DIR/$archive" .
  fi
  archives+=( "{\"platform\":\"${platform}\",\"archive\":\"${archive}\",\"extension\":\"${archive_format}\"}" )
  archive_hash="$(checksum "$OUTPUT_DIR/$archive" | awk '{print $1}')"
  printf '%s  %s\n' "$archive_hash" "$archive" >> "$OUTPUT_DIR/SHA256SUMS"
  rm -rf "$stage"
done

platform_json="$(IFS=,; printf '%s' "${platforms[*]}")"
archive_json="$(IFS=,; printf '%s' "${archives[*]}")"
binary_json="$(printf '"%s",' "${binaries[@]}")"
binary_json="[${binary_json%,}]"
cat > "$OUTPUT_DIR/manifest.json" <<EOF
{
  "version": "${VERSION}",
  "major": ${MAJOR:-0},
  "commit": "${COMMIT}",
  "buildTime": "${BUILD_TIME}",
  "schemaVersion": ${SCHEMA_VERSION},
  "binaries": ${binary_json},
  "platforms": [${platform_json}],
  "assets": [${archive_json}],
  "checksums": "SHA256SUMS"
}
EOF

echo "release artifacts written to $OUTPUT_DIR"
