#!/usr/bin/env bash
# Regenerate the macOS application icon that Finder shows for "TunnelMesh Client.app".
#
# The artwork is drawn by scripts/trayicon from a handful of numbers rather than edited in a
# design tool, because the repository has no binary source for it and no way to review one.
# Re-running this after a brand change rewrites deploy/macos/TunnelMeshClient.icns
# deterministically, and deploy/macos/tray_bundle_test.go fails if the committed icon drifts
# from the geometry: an icon that cannot be regenerated is a icon that will be replaced by a
# screenshot someone liked.
#
#   scripts/generate-tray-icon.sh [output.icns]        # default: deploy/macos/TunnelMeshClient.icns
#   PREVIEW=dir scripts/generate-tray-icon.sh          # also keep the 1024px master and the
#                                                      # .iconset that iconutil consumed
set -euo pipefail
export LC_ALL=C

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
OUTPUT="${1:-$ROOT_DIR/deploy/macos/TunnelMeshClient.icns}"
PREVIEW="${PREVIEW:-}"

# iconutil is the only tool that writes a legal .icns: it is what stamps the per-size
# representation names (icp4, ic10, ...) that Finder and the Dock select between.
if ! command -v iconutil >/dev/null 2>&1; then
  echo "iconutil is missing; install the Xcode command line tools (xcode-select --install)" >&2
  exit 1
fi

work="$(mktemp -d)"
trap 'rm -r "$work"' EXIT
iconset="$work/TunnelMeshClient.iconset"

(cd "$ROOT_DIR" && go run ./scripts/trayicon -out "$iconset" -preview "$work/master.png")
iconutil -c icns "$iconset" -o "$OUTPUT"

if [[ -n "$PREVIEW" ]]; then
  mkdir -p "$PREVIEW"
  cp "$work/master.png" "$PREVIEW/TunnelMeshClient-1024.png"
  cp -R "$iconset" "$PREVIEW/"
  echo "preview kept in $PREVIEW"
fi

echo "wrote $OUTPUT ($(wc -c <"$OUTPUT" | tr -d ' ') bytes)"
