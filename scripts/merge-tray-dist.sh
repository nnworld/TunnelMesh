#!/usr/bin/env bash
# Fold tray client artifacts into a cross-platform release directory.
#
# scripts/build-release.sh cross-compiles three binaries with CGO_ENABLED=0 and can never
# produce the tray: the macOS build links Cocoa and WebKit through cgo and needs a Mac, and
# the Windows build needs the NSIS step that belongs to its own packaging script. The release
# workflow runs scripts/package-macos-tray.sh and scripts/package-windows-tray.sh and hands
# their output over. This script is that hand-off: it verifies what arrived, copies every
# published file next to the platform archives and extends the single SHA256SUMS, so one
# `sha256sum -c` covers every asset and one `gh release create` publishes all of them.
#
# It copies whatever the packaging script produced rather than a hard coded extension, because
# the two platforms publish different shapes: disk images on one side, a green .zip per
# architecture plus an NSIS setup.exe on the other. What stays fixed is that both publish
# SHA256SUMS plus manifest.json, which is what makes a partial hand-off detectable.
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
usage: [--check] [--manifest-name=NAME] <tray-dist-dir> [<release-dir>] $0

Verifies the output of a tray packaging script, then copies its artifacts into <release-dir>
and appends their checksums to <release-dir>/SHA256SUMS. The tray's own manifest.json is
published beside the cross-platform one under its own name, so two platforms can hand over
without either one overwriting the other's record.

  --check          validate <tray-dist-dir> and stop, without copying anything
  --manifest-name  name for the copied manifest (default: manifest-tray.json)

<release-dir> defaults to the current directory.
USAGE
}

if [[ "${1:-}" == "-h" || "${1:-}" == "--help" ]]; then
  usage
  exit 0
fi

check_only=0
manifest_name="manifest-tray.json"
while [[ $# -gt 0 ]]; do
  case "$1" in
    --check) check_only=1 ;;
    --manifest-name=*)
      manifest_name="${1#--manifest-name=}"
      if [[ -z "$manifest_name" ]]; then
        echo "--manifest-name needs a file name" >&2
        exit 2
      fi
      ;;
    --*) echo "unknown option: $1" >&2; exit 2 ;;
    *) break ;;
  esac
  shift
done
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
[[ -f "$SOURCE_DIR/SHA256SUMS" ]] || fail "$SOURCE_DIR has no SHA256SUMS; is it the output of a tray packaging script?"
[[ -f "$SOURCE_DIR/manifest.json" ]] || fail "$SOURCE_DIR has no manifest.json; is it the output of a tray packaging script?"

# Everything the packaging script published, and nothing else: SHA256SUMS and manifest.json
# are the bookkeeping files this script re-reads rather than copies, and a build directory
# left behind by a packaging run must never be flattened into the release.
artifacts=()
for path in "$SOURCE_DIR"/*; do
  name="$(basename "$path")"
  # Files only, and only published ones. A loose log would otherwise be attached to the
  # release because it happened to sit in the hand-off directory, and every archive name
  # carries its extension while neither of the two bookkeeping files does.
  [[ -f "$path" ]] || continue
  case "$name" in
    .* | SHA256SUMS | manifest.json | *.log | *.txt | *.json) continue ;;
  esac
  artifacts+=("$(basename "$path")")
done
# A packaging script that produced no artifact still writes both bookkeeping files, so the
# count is the only check that catches "the job ran and shipped nothing".
[[ ${#artifacts[@]} -gt 0 ]] || fail "$SOURCE_DIR contains no artifacts; the tray job did not package anything"

# Verify before copying. The artifacts travel through an upload and a download, and a name is
# not evidence of content: this is the only place a truncated or rewritten image can be caught.
echo "verifying $SOURCE_DIR/SHA256SUMS"
if ! (cd "$SOURCE_DIR" && "${checksum[@]}" -c SHA256SUMS); then
  fail "tray artifacts do not match $SOURCE_DIR/SHA256SUMS"
fi

if [[ "$check_only" == "1" ]]; then
  echo "tray dist directory is complete: ${#artifacts[@]} artifact(s)"
  exit 0
fi

mkdir -p "$TARGET_DIR"
# Append, never truncate: the platform archives are already listed here.
touch "$TARGET_DIR/SHA256SUMS"

for name in "${artifacts[@]}"; do
  if [[ -e "$TARGET_DIR/$name" ]]; then
    fail "$TARGET_DIR/$name already exists; refusing to publish two builds under one asset name"
  fi
  cp "$SOURCE_DIR/$name" "$TARGET_DIR/$name"
  printf '%s  %s\n' "$("${checksum[@]}" "$TARGET_DIR/$name" | awk '{print $1}')" "$name" >> "$TARGET_DIR/SHA256SUMS"
done

# manifest.json is the documented cross-platform contract the admin Downloads page points
# at, so each tray platform's manifest is published under its own name instead of being
# merged in or overwriting the other platform's.
cp "$SOURCE_DIR/manifest.json" "$TARGET_DIR/$manifest_name"

echo "merged ${#artifacts[@]} tray artifact(s) from $SOURCE_DIR into $TARGET_DIR as $manifest_name"
