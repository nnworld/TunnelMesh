#!/usr/bin/env bash
set -euo pipefail

readonly REPOSITORY="nnworld/TunnelMesh"
readonly RELEASE_BASE_URL="https://github.com/${REPOSITORY}/releases"
readonly LATEST_API_URL="https://api.github.com/repos/${REPOSITORY}/releases/latest"

VERSION="latest"
INSTALL_DIR="${HOME}/.local/bin"
WORK_DIR=""

usage() {
  cat >&2 <<'EOF'
Usage: install.sh [--version vMAJOR.MINOR.PATCH] [--install-dir DIR]

Downloads a TunnelMesh release archive, verifies its SHA256 checksum, and copies the
three binaries to the requested directory. The default is a user-local install.
EOF
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --version)
      VERSION="${2:?missing version}"
      shift 2
      ;;
    --install-dir)
      INSTALL_DIR="${2:?missing install directory}"
      shift 2
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      echo "unknown argument: $1" >&2
      usage
      exit 2
      ;;
  esac
done

cleanup() {
  if [[ -n "$WORK_DIR" ]]; then
    rm -rf -- "$WORK_DIR"
  fi
}
trap cleanup EXIT INT TERM

require_command() {
  if ! command -v "$1" >/dev/null 2>&1; then
    echo "missing required command: $1" >&2
    exit 1
  fi
}

download() {
  local url="$1"
  local output="$2"

  curl --fail --silent --show-error --location "$url" --output "$output"
}

checksum_file() {
  local path="$1"

  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$path" | awk '{print $1}'
  else
    shasum -a 256 "$path" | awk '{print $1}'
  fi
}

resolve_latest_version() {
  local response

  response="$(download "$LATEST_API_URL" -)"
  printf '%s\n' "$response" | sed -n 's/.*"tag_name"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' | head -n 1
}

for command in curl tar awk sed head install; do
  require_command "$command"
done

if [[ "$VERSION" == "latest" ]]; then
  VERSION="$(resolve_latest_version)"
fi

if [[ ! "$VERSION" =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
  echo "invalid version: expected vMAJOR.MINOR.PATCH" >&2
  exit 2
fi

case "$(uname -s)" in
  Linux) GOOS="linux" ;;
  Darwin) GOOS="darwin" ;;
  *) echo "unsupported operating system: $(uname -s)" >&2; exit 2 ;;
esac

case "$(uname -m)" in
  x86_64) GOARCH="amd64" ;;
  aarch64|arm64) GOARCH="arm64" ;;
  *) echo "unsupported architecture: $(uname -m)" >&2; exit 2 ;;
esac

archive="tunnelmesh-${VERSION}-${GOOS}-${GOARCH}.tar.gz"
archive_url="${RELEASE_BASE_URL}/download/${VERSION}/${archive}"
checksums_url="${RELEASE_BASE_URL}/download/${VERSION}/SHA256SUMS"

WORK_DIR="$(mktemp -d)"
extract_dir="${WORK_DIR}/extract"
mkdir -p "$extract_dir"

echo "Downloading TunnelMesh ${VERSION} for ${GOOS}/${GOARCH}..."
download "$archive_url" "${WORK_DIR}/${archive}"
download "$checksums_url" "${WORK_DIR}/SHA256SUMS"

expected_line="$(grep "  ${archive}$" "${WORK_DIR}/SHA256SUMS" || true)"
if [[ -z "$expected_line" ]]; then
  echo "checksum entry not found for ${archive}" >&2
  exit 1
fi
expected_checksum="${expected_line%%[[:space:]]*}"
actual_checksum="$(checksum_file "${WORK_DIR}/${archive}")"
if [[ "$actual_checksum" != "$expected_checksum" ]]; then
  echo "checksum mismatch for ${archive}" >&2
  echo "expected: ${expected_checksum}" >&2
  echo "actual:   ${actual_checksum}" >&2
  exit 1
fi

tar -xzf "${WORK_DIR}/${archive}" -C "$extract_dir"

mkdir -p "$INSTALL_DIR"
for binary in tunnelmesh-server tunnelmesh-agent tunnelmesh-client; do
  if [[ ! -f "${extract_dir}/${binary}" ]]; then
    echo "release archive is missing ${binary}" >&2
    exit 1
  fi
  install -m 0755 "${extract_dir}/${binary}" "${INSTALL_DIR}/${binary}"
done

echo "Installed TunnelMesh ${VERSION} to ${INSTALL_DIR}"
echo "Run '${INSTALL_DIR}/tunnelmesh-server --help' to get started."
