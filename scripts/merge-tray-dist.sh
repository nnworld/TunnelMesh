#!/usr/bin/env bash
# Fold the macOS tray artifacts into a cross-platform release directory.
#
# scripts/build-release.sh cross-compiles three binaries with CGO_ENABLED=0 and can never
# produce the tray, which links Cocoa and WebKit through cgo. The release workflow builds
# the tray on a macOS runner with scripts/package-macos-tray.sh and hands the disk images
# over. This script is that hand-off: it verifies what arrived, copies the .dmg files next
# to the platform archives and extends the single SHA256SUMS, so one `sha256sum -c` covers
# every published asset and one `gh release create` publishes all of them.
#
# It lives in its own file rather than as a block inside build-release.sh because it has to
# be testable on its own (scripts/merge_tray_dist_test.go runs it against fixtures). The
# failure it prevents is a silent one: a release that ships without its macOS client, or
# with a checksum file that no longer verifies, is not noticed until an operator reports a
# missing download.
#
# usage: merge-tray-dist.sh [--check] <tray-dist-dir> [<release-dir>]
set -euo pipefail
export LC_ALL=C

usage() {
  cat <<USAGE
usage: [--check] <tray-dist-dir> [<release-dir>] $0

Verifies the output of scripts/package-macos-tray.sh, then copies its .dmg files into
<release-dir> and appends their checksums to <release-dir>/SHA256SUMS. The tray's own
manifest.json is published beside the cross-platform one as manifest-tray.json.

  --check        validate <tray-dist-dir> and stop, without copying anything

<release-dir> defaults to the current directory.
USAGE
}

if [[ "${1:-}" == "-h" || "${1:-}" == "--help" ]]; then
  usage
  exit 0
fi

check_only=0
if [[ "${1:-}" == "--check" ]]; then
  check_only=1
  shift
fi
if [[ $# -lt 1 || $# -gt 2 ]]; then
  usage >&2
  exit 2
fi

SOURCE_DIR="$1"
TARGET_DIR="${2:-.}"

# sha256sum is not on macOS; shasum always is. Both scripts in this directory use the same
# fallback so the checksums they write are interchangeable.
if command -v sha256sum >/dev/null 2>&1; then
  checksum=(sha256sum)
elif command -v shasum >/dev/null 2>&1; then
  checksum=(shasum -a 256)
else
  echo "sha256sum or shasum is required" >&2
  exit 1
fi

fail() {
  echo "$1" >&2
  exit 1
}

[[ -d "$SOURCE_DIR" ]] || fail "tray dist directory does not exist: $SOURCE_DIR"
# Both files are written unconditionally by the packaging script, so either one missing
# means this is not its output directory - most likely a wrong artifact path in the
# workflow, which would otherwise produce a release with no macOS client at all.
[[ -f "$SOURCE_DIR/SHA256SUMS" ]] || fail "$SOURCE_DIR has no SHA256SUMS; is it the output of scripts/package-macos-tray.sh?"
[[ -f "$SOURCE_DIR/manifest.json" ]] || fail "$SOURCE_DIR has no manifest.json; is it the output of scripts/package-macos-tray.sh?"

images=("$SOURCE_DIR"/*.dmg)
# An unmatched glob stays literal rather than expanding to nothing, which is exactly what
# makes this the right test on bash 3.2 as well as bash 5.
[[ -e "${images[0]}" ]] || fail "$SOURCE_DIR contains no .dmg; the tray job did not package anything"

# Verify before copying. The disk images travel through an artifact upload and download,
# and a name is not evidence of content: this is the only place a truncated or rewritten
# image can still be caught.
echo "verifying $SOURCE_DIR/SHA256SUMS"
if ! (cd "$SOURCE_DIR" && "${checksum[@]}" -c SHA256SUMS); then
  fail "tray disk images do not match $SOURCE_DIR/SHA256SUMS"
fi

if [[ "$check_only" == "1" ]]; then
  echo "tray dist directory is complete: ${#images[@]} disk image(s)"
  exit 0
fi

mkdir -p "$TARGET_DIR"
# Append, never truncate: the platform archives are already listed here.
touch "$TARGET_DIR/SHA256SUMS"

for image in "${images[@]}"; do
  name="$(basename "$image")"
  if [[ -e "$TARGET_DIR/$name" ]]; then
    fail "$TARGET_DIR/$name already exists; refusing to publish two builds under one asset name"
  fi
  cp "$image" "$TARGET_DIR/$name"
  printf '%s  %s\n' "$("${checksum[@]}" "$TARGET_DIR/$name" | awk '{print $1}')" "$name" >> "$TARGET_DIR/SHA256SUMS"
done

# manifest.json is the documented cross-platform contract the admin Downloads page points
# at, so the tray's manifest is published under its own name instead of being merged in.
cp "$SOURCE_DIR/manifest.json" "$TARGET_DIR/manifest-tray.json"

echo "merged ${#images[@]} tray disk image(s) from $SOURCE_DIR into $TARGET_DIR"
