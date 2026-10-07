#!/usr/bin/env bash
# Package the Windows tray client: one green .zip per architecture plus one NSIS .exe installer.
#
# The macOS half of the tray links Cocoa and WebKit through cgo and can only be built on a Mac.
# The Windows half is pure Go over WebView2, so this script cross-compiles it with
# CGO_ENABLED=0 from Linux or macOS - which is what lets the release matrix publish a Windows
# download without a Windows host, and what lets CI compile the installer on ubuntu.
#
# Two artifacts per release, on purpose. The .zip is for an operator who just wants to run the
# tray out of a folder; the setup.exe is for one who wants Start-Menu entries, an
# Add/Remove-Programs record and an uninstaller. Both are per-user and need no administrator.
#
# The executable is not a bare Go binary: go-winres stamps the brand icon, the version info
# Explorer shows in the Properties sheet and the application manifest (DPI awareness, asInvoker)
# into it, because Windows has no equivalent of an Info.plist to carry that information. The
# generated .syso files are build inputs and are removed again, so no release ever ships a
# stale icon that was regenerated on someone's machine weeks earlier.
set -euo pipefail
export LC_ALL=C

APP_NAME="TunnelMesh Client"
EXE_NAME="TunnelMeshClient.exe"
APP_ID="com.tunnelmesh.client-tray"

VERSION="${VERSION:-dev}"
# amd64 covers the fleet; arm64 exists because Windows on ARM is a real developer target and
# cross-compiling it costs nothing here. 386 is deliberately absent: WebView2 ships for it but
# the tunnel runtime does not need a third column in the release table.
ARCHES="${ARCHES:-amd64 arm64}"
# The installer is built for one architecture: it embeds a single executable, and a
# multi-architecture setup.exe would need a Windows host to make.
INSTALLER_ARCH="${INSTALLER_ARCH:-amd64}"
# makensis is the NSIS compiler; the packaged variable lets a host with a differently named
# binary (or a container image) point at it without editing this script.
MAKENSIS="${MAKENSIS:-makensis}"

if [[ -z "${ARCHES//[[:space:]]/}" ]]; then
  echo "ARCHES must name at least one windows architecture (amd64, arm64)" >&2
  exit 1
fi
for arch in $ARCHES; do
  case "$arch" in
    amd64|arm64) ;;
    *) echo "unsupported GOARCH for the windows tray: $arch (want amd64 or arm64)" >&2; exit 1 ;;
  esac
done

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
DIST_DIR="${DIST_DIR:-dist/${VERSION}/windows-tray}"
if [[ "$DIST_DIR" = /* ]]; then
  OUTPUT_DIR="$DIST_DIR"
else
  OUTPUT_DIR="$ROOT_DIR/$DIST_DIR"
fi

CMD_DIR="$ROOT_DIR/cmd/tunnelmesh-client-tray"
WINRES_DIR="$ROOT_DIR/deploy/windows"
WINRES_JSON="$WINRES_DIR/winres.json"
APP_ICON="$WINRES_DIR/TunnelMeshClient.ico"
INSTALLER_SCRIPT="$WINRES_DIR/installer.nsi"
WEB_DIST_DIR="$ROOT_DIR/internal/tray/webdist/dist"

usage() {
  cat <<USAGE
usage: VERSION=vX.Y.Z [ARCHES="amd64 arm64"] [INSTALLER_ARCH=amd64] [DIST_DIR=path] [MAKENSIS=path] $0

Cross-compiles cmd/tunnelmesh-client-tray with -tags tray for each architecture, stamps the
icon / version / manifest resources, and writes per-architecture .zip archives plus one NSIS
setup.exe for INSTALLER_ARCH, next to SHA256SUMS and manifest.json.

  VERSION         release version (default: dev; otherwise vMAJOR.MINOR.PATCH[-suffix])
  ARCHES          space separated windows architectures (default: "amd64 arm64")
  INSTALLER_ARCH  architecture the setup.exe carries (default: amd64)
  DIST_DIR        output directory (default: dist/<VERSION>/windows-tray)
  MAKENSIS        NSIS compiler to use (default: makensis on PATH)

Prerequisites: go, zip, makensis (brew install nsis / apt-get install -y nsis) and a built
front end (cd web-tray && npm run build). Nothing here needs a Windows host.
USAGE
}

if [[ "${1:-}" == "-h" || "${1:-}" == "--help" ]]; then
  usage
  exit 0
fi

if [[ "$VERSION" != "dev" && ! "$VERSION" =~ ^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$ ]]; then
  echo "VERSION must be vMAJOR.MINOR.PATCH (an optional pre-release suffix is allowed for test builds): $VERSION" >&2
  exit 1
fi
case " $ARCHES " in
  *" $INSTALLER_ARCH "*) ;;
  *) echo "INSTALLER_ARCH must be one of ARCHES ($ARCHES), got $INSTALLER_ARCH" >&2; exit 1 ;;
esac

for tool in go zip; do
  if ! command -v "$tool" >/dev/null 2>&1; then
    echo "$tool is required" >&2
    exit 1
  fi
done
# makensis is required, not optional, and the reason is asymmetric with the zips: a release
# without an installer still has a download for every architecture, and nobody notices the
# missing setup.exe until an operator looks for it. Failing loudly here is cheaper.
if ! command -v "$MAKENSIS" >/dev/null 2>&1; then
  echo "makensis is required (brew install nsis, or apt-get install -y nsis, or set MAKENSIS)" >&2
  exit 1
fi
for input in "$WINRES_JSON" "$APP_ICON" "$INSTALLER_SCRIPT" "$CMD_DIR/main.go"; do
  [[ -f "$input" ]] || { echo "missing build input: $input" >&2; exit 1; }
done
# The settings interface is compiled into the binary, so a missing bundle means a tray whose
# window is blank; a stale bundle means a tray showing the previous UI. Both have to fail
# here rather than on the operator's machine.
if [[ ! -f "$WEB_DIST_DIR/index.html" ]]; then
  echo "the embedded settings bundle is missing: run cd web-tray && npm run build" >&2
  exit 1
fi
if [[ -f "$ROOT_DIR/web-tray/dist/index.html" ]] && ! diff -qr "$ROOT_DIR/web-tray/dist" "$WEB_DIST_DIR" >/dev/null 2>&1; then
  echo "internal/tray/webdist/dist is stale relative to web-tray/dist; run: cd web-tray && npm run build" >&2
  exit 1
fi

mkdir -p "$OUTPUT_DIR"
: > "$OUTPUT_DIR/SHA256SUMS"
BUILD_DIR="$OUTPUT_DIR/.build"
mkdir -p "$BUILD_DIR"

COMMIT="$(git -C "$ROOT_DIR" rev-parse HEAD)"
BUILD_TIME="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
# Windows version resources are four dot-separated numbers; the "v" and any pre-release
# suffix belong to the git tag and the binary's own version string, not to the Properties
# sheet. "dev" and friends fall back to 0.0.0.0, which is what an unreleased build is.
RES_VERSION="0.0.0.0"
if [[ "$VERSION" =~ ^v?([0-9]+)\.([0-9]+)\.([0-9]+) ]]; then
  RES_VERSION="${BASH_REMATCH[1]}.${BASH_REMATCH[2]}.${BASH_REMATCH[3]}.0"
fi
BUNDLE_VERSION="${VERSION#v}"
LDFLAGS="-s -w -H=windowsgui -X github.com/tunnelmesh/tunnelmesh/internal/build.Version=${VERSION} -X github.com/tunnelmesh/tunnelmesh/internal/build.Commit=${COMMIT} -X github.com/tunnelmesh/tunnelmesh/internal/build.BuildTime=${BUILD_TIME}"

checksum() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$1"
  else
    shasum -a 256 "$1"
  fi
}

# The .syso files land in the package directory and are picked up by every later `go build`,
# including the ones in `go test ./...` on this machine. Cleaning them up on any exit path is
# the difference between a reproducible build and a binary that quietly carries last week's icon.
clean_syso() {
  rm -f "$CMD_DIR"/rsrc_windows_*.syso
}
trap clean_syso EXIT INT TERM

clean_syso
echo "generating windows resources for: $ARCHES"
# go-winres takes a comma separated list, and only emits the architectures it is asked for:
# generating amd64 and leaving arm64 behind would produce a resource-free arm64 binary.
ARCHES_CSV="$(printf '%s' "$ARCHES" | tr ' ' ',')"
(cd "$CMD_DIR" && go run github.com/tc-hib/go-winres make \
  --in="$WINRES_JSON" \
  --out=rsrc \
  --arch="$ARCHES_CSV" \
  --product-version="$RES_VERSION" \
  --file-version="$RES_VERSION")

# write_readme documents the two things an operator cannot infer from a bare .exe: that the
# file is unsigned, and where the settings live.
write_readme() {
  local dir="$1"
  cat > "$dir/README.txt" <<'README'
TunnelMesh Client for Windows

  Run          Double-click TunnelMeshClient.exe, or start it from a shell. It has no console
               window: look for the TunnelMesh icon in the notification area (system tray).
  Settings     Right-click that icon and choose "Open main window" / "Open settings".
  Config       %USERPROFILE%\.config\tunnelmesh\client.yaml (shared with `tunnelmesh-client run`)
               %USERPROFILE%\.config\tunnelmesh\tray.json   (interface preferences)
               Only one of the tray and `tunnelmesh-client run` may use a given client.yaml at
               a time; the second one to start reports that a TunnelMesh client is already
               running.
  Dependencies Microsoft Edge WebView2 Runtime (preinstalled on Windows 11 and on current
               Windows 10). Without it the tray still runs and opens the settings page in your
               default browser instead of a native window.
  Signature    This build is unsigned. SmartScreen may say "Windows protected your PC"; choose
               "More info" then "Run anyway", or verify the download against SHA256SUMS first.
  Home         https://github.com/nnworld/TunnelMesh

TunnelMesh Windows 托盘客户端。双击 TunnelMeshClient.exe 启动，在通知区域图标上右键打开主界面。
签名缺失时 SmartScreen 可能拦截，选择"更多信息"→"仍要运行"。
README
}

# pe_subsystem reads the PE "Subsystem" field out of the executable. That byte is what decides
# whether Windows starts the process attached to a console, so checking it beats checking the
# command line that was supposed to set it: `go version -m` stops recording -ldflags as soon as
# -X is present, and "a flag that silently stopped applying" is exactly the failure to guard.
# 2 is IMAGE_SUBSYSTEM_WINDOWS_GUI, 3 is the console subsystem.
pe_subsystem() {
  local exe="$1" pe offset
  # e_lfanew is a uint32 at 0x3c; the optional header starts 24 bytes after the "PE"
  # signature, and Subsystem sits 68 bytes into it for PE32+ (amd64 and arm64 always are).
  pe="$(od -An -j60 -N4 -tu4 "$exe" | tr -d ' \n')"
  offset=$((pe + 24 + 68))
  od -An -j"$offset" -N2 -tu2 "$exe" | tr -d ' \n'
}

# verify_binary reads the produced executable back instead of trusting the environment it was
# built in: GOOS/GOARCH say the cross-compile worked, the tag says the tray shell is linked in,
# CGO_ENABLED=0 says no host toolchain leaked in, and the subsystem says no console window.
verify_binary() {
  local exe="$1" arch="$2" settings
  settings="$(go version -m "$exe" 2>/dev/null)"
  for want in "GOOS=windows" "GOARCH=${arch}" "tags=tray" "CGO_ENABLED=0"; do
    if ! printf '%s' "$settings" | grep -qF -- "$want"; then
      echo "the built tray does not report $want; refusing to package it" >&2
      exit 1
    fi
  done
  if [[ "$(pe_subsystem "$exe")" != "2" ]]; then
    echo "the built tray is not a GUI subsystem binary (got $(pe_subsystem "$exe"), want 2);
Explorer would open a console window next to the tray icon" >&2
    exit 1
  fi
}

platforms=()
assets=()
# The installer consumes the same stage directory as the archive, so the setup.exe is built
# against the bytes that were just hashed rather than against a second compilation.
setup_stage=""

for arch in $ARCHES; do
  stage="$BUILD_DIR/windows-$arch"
  rm -rf "$stage"
  mkdir -p "$stage"
  echo "building TunnelMeshClient.exe for windows/$arch"
  CGO_ENABLED=0 GOOS=windows GOARCH="$arch" \
    go build -tags tray -trimpath -ldflags="$LDFLAGS" -o "$stage/$EXE_NAME" ./cmd/tunnelmesh-client-tray
  verify_binary "$stage/$EXE_NAME" "$arch"
  write_readme "$stage"
  cp "$ROOT_DIR/LICENSE" "$ROOT_DIR/NOTICE" "$stage/"

  archive="TunnelMeshClient-${VERSION}-windows-${arch}.zip"
  echo "creating $archive"
  # -X drops the extra platform attributes so the same inputs produce the same archive bytes on
  # a Mac and on Linux; the archive order is fixed by listing the members explicitly.
  (cd "$stage" && rm -f "$OUTPUT_DIR/$archive" && zip -X -q "$OUTPUT_DIR/$archive" \
    "$EXE_NAME" README.txt LICENSE NOTICE)
  archive_hash="$(checksum "$OUTPUT_DIR/$archive" | awk '{print $1}')"
  printf '%s  %s\n' "$archive_hash" "$archive" >> "$OUTPUT_DIR/SHA256SUMS"
  platforms+=("\"windows/${arch}\"")
  assets+=( "{\"platform\":\"windows/${arch}\",\"archive\":\"${archive}\",\"extension\":\"zip\",\"application\":\"${EXE_NAME}\",\"installerLayout\":false}" )
  if [[ "$arch" == "$INSTALLER_ARCH" ]]; then
    setup_stage="$stage"
  fi
done

# The installer is built for one architecture because NSIS embeds one executable. Skipping it
# silently when INSTALLER_ARCH is not in ARCHES is not an option; the check above makes that a
# usage error instead.
if [[ -z "$setup_stage" ]]; then
  echo "INSTALLER_ARCH=$INSTALLER_ARCH was not built (ARCHES=$ARCHES)" >&2
  exit 1
fi
setup="$OUTPUT_DIR/TunnelMeshClient-${VERSION}-windows-${INSTALLER_ARCH}-setup.exe"
echo "creating $(basename "$setup")"
# makensis is a cross compiler: it writes a Windows executable from Linux or macOS, which is
# the whole reason the release matrix can publish a Windows installer without a Windows runner.
"$MAKENSIS" \
  -DVERSION="$BUNDLE_VERSION" \
  -DARCH="$INSTALLER_ARCH" \
  -DBUILD_DIR="$setup_stage" \
  -DICON_FILE="$APP_ICON" \
  -DOUTFILE="$setup" \
  "$INSTALLER_SCRIPT"
# makensis exits 0 on a script that compiled but wrote nothing useful, and a zero byte
# installer on a release page is worse than no installer at all.
if [[ ! -s "$setup" ]]; then
  echo "makensis did not produce $setup" >&2
  exit 1
fi
setup_hash="$(checksum "$setup" | awk '{print $1}')"
printf '%s  %s\n' "$setup_hash" "$(basename "$setup")" >> "$OUTPUT_DIR/SHA256SUMS"
assets+=( "{\"platform\":\"windows/${INSTALLER_ARCH}\",\"archive\":\"$(basename "$setup")\",\"extension\":\"exe\",\"application\":\"${APP_NAME}\",\"installerLayout\":true}" )

# The staging directories hold the same bytes that are already inside the archives; keeping
# them would only invite a later step to package something that was never hashed.
rm -rf "$BUILD_DIR"

platform_json="$(IFS=,; printf '%s' "${platforms[*]}")"
asset_json="$(IFS=,; printf '%s' "${assets[*]}")"
cat > "$OUTPUT_DIR/manifest.json" <<EOF
{
  "version": "${VERSION}",
  "commit": "${COMMIT}",
  "buildTime": "${BUILD_TIME}",
  "bundleIdentifier": "${APP_ID}",
  "executable": "${EXE_NAME}",
  "platform": "windows",
  "renderer": "webview2",
  "signingIdentity": "",
  "notarized": false,
  "platforms": [${platform_json}],
  "assets": [${asset_json}],
  "checksums": "SHA256SUMS"
}
EOF

echo "windows tray artifacts written to $OUTPUT_DIR"
cat >&2 <<'NOTE'
note: the executable and the installer are unsigned. Windows shows a SmartScreen warning for
downloads that have no Authenticode reputation, so a published release either needs a code
signing certificate or has to document the "More info > Run anyway" step.
NOTE
