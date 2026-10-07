#!/usr/bin/env bash
# Regenerate the tray's brand icons: the macOS .icns plus menu-bar glyph, and the Windows
# .ico pair that Explorer, the taskbar and the notification area show.
#
# The artwork is drawn by scripts/trayicon from a handful of numbers rather than edited in a
# design tool, because the repository has no binary source for it and no way to review one.
# Re-running this after a brand change rewrites every committed artifact deterministically, and
# the bundle tests fail if a committed icon drifts from the geometry: an icon that cannot be
# regenerated is an icon that will be replaced by a screenshot someone liked.
#
# One run, one geometry, five outputs. The menu-bar glyph, the .icns tile and the Windows
# .ico files are the same mark at different framings, which is what makes "the tray icon does
# not match Finder" structurally impossible rather than a coincidence.
#
#   scripts/generate-tray-icon.sh [output.icns]        # default: deploy/macos/TunnelMeshClient.icns
#   PREVIEW=dir scripts/generate-tray-icon.sh          # also keep the 1024px master and the
#                                                      # .iconset that iconutil consumed
#   WINDOWS_ONLY=1 scripts/generate-tray-icon.sh        # skip the macOS half; for a Linux job
#                                                      # that only has to reproduce the .ico pair
set -euo pipefail
export LC_ALL=C

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
OUTPUT="${1:-$ROOT_DIR/deploy/macos/TunnelMeshClient.icns}"
PREVIEW="${PREVIEW:-}"
WINDOWS_ONLY="${WINDOWS_ONLY:-}"

# The glyph lives beside the .icns so one argument moves the whole brand together.
MENUBAR_DIR="$(dirname "$OUTPUT")"

# The Windows icons live beside winres.json, which names them as siblings; go-winres resolves
# a path relative to its own JSON file, and one that stays inside deploy/windows does not
# depend on which interpretation a given version uses. The notification-area icon is then
# duplicated into the package that embeds it, because go:embed cannot reach outside its own
# directory and the tray draws its taskbar tile from those bytes rather than from a file
# loose next to the executable.
ICO_DIR="$ROOT_DIR/deploy/windows"
EMBED_DIR="$ROOT_DIR/internal/tray/native/icons"

work="$(mktemp -d)"
trap 'rm -r "$work"' EXIT
iconset="$work/TunnelMeshClient.iconset"
menubar="$work/menubar"

copy_embed() {
  mkdir -p "$EMBED_DIR"
  cp "$ICO_DIR/TunnelMeshTray.ico" "$EMBED_DIR/TunnelMeshTray.ico"
}

report_icons() {
  for ico in "$ICO_DIR"/*.ico; do
    echo "wrote $ico ($(wc -c <"$ico" | tr -d ' ') bytes)"
  done
}

generate() {
  local -a args=(-out "$iconset" -ico "$ICO_DIR")
  if [[ -z "$WINDOWS_ONLY" ]]; then
    args+=(-menubar "$menubar" -preview "$work/master.png")
  else
    args+=(-out "$work/iconset")
  fi
  (cd "$ROOT_DIR" && go run ./scripts/trayicon "${args[@]}")
}

if [[ -n "$WINDOWS_ONLY" ]]; then
  generate
  copy_embed
  report_icons
  exit 0
fi

# iconutil is the only tool that writes a legal .icns: it is what stamps the per-size
# representation names (icp4, ic10, ...) that Finder and the Dock select between.
if ! command -v iconutil >/dev/null 2>&1; then
  echo "iconutil is missing; install the Xcode command line tools (xcode-select --install), or run with WINDOWS_ONLY=1 to regenerate just the .ico pair" >&2
  exit 1
fi

generate
iconutil -c icns "$iconset" -o "$OUTPUT"

# iconutil consumed the .iconset; the glyph files are ordinary PNGs and are copied out.
mkdir -p "$MENUBAR_DIR"
cp "$menubar"/TunnelMeshMenuBar*.png "$MENUBAR_DIR/"
copy_embed

if [[ -n "$PREVIEW" ]]; then
  mkdir -p "$PREVIEW"
  cp "$work/master.png" "$PREVIEW/TunnelMeshClient-1024.png"
  cp -R "$iconset" "$PREVIEW/"
  cp "$menubar"/TunnelMeshMenuBar*.png "$PREVIEW/"
  cp "$ICO_DIR"/*.ico "$PREVIEW/"
  echo "preview kept in $PREVIEW"
fi

echo "wrote $OUTPUT ($(wc -c <"$OUTPUT" | tr -d ' ') bytes)"
for glyph in "$MENUBAR_DIR"/TunnelMeshMenuBar*.png; do
  echo "wrote $glyph ($(wc -c <"$glyph" | tr -d ' ') bytes)"
done
report_icons
