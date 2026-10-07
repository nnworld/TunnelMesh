package windows

import (
	"encoding/binary"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tunnelmesh/tunnelmesh/internal/tray"
)

const (
	appIcon       = "TunnelMeshClient.ico"
	trayIcon      = "TunnelMeshTray.ico"
	winresJSON    = "winres.json"
	installerFile = "installer.nsi"
	// embeddedTrayIcon is the notification-area icon as the shell reads it. go:embed cannot
	// reach outside its own directory, so the generator copies it into the native package;
	// two copies of one artwork must not be allowed to drift.
	embeddedTrayIcon = "../../internal/tray/native/icons/" + trayIcon
	packageScript    = "../../scripts/package-windows-tray.sh"
	releaseScript    = "../../scripts/build-release.sh"
	panelRouteSource = "../../web-tray/src/panelRoute.ts"

	appName    = "TunnelMesh Client"
	exeName    = "TunnelMeshClient.exe"
	appID      = "com.tunnelmesh.client-tray"
	runValue   = "TunnelMeshClient"
	websiteURL = "https://github.com/nnworld/TunnelMesh"
)

// iconSizes lists the squares each committed .ico must carry. They are the sizes Windows
// actually asks for: the notification area, the taskbar, Alt-Tab, Explorer's icon views and
// the properties page. An .ico missing a rung renders as a blurred upscale rather than
// failing, which is exactly the kind of defect no runtime check would notice.
var iconSizes = map[string][]int{
	appIcon:  {16, 24, 32, 48, 64, 128, 256},
	trayIcon: {16, 24, 32},
}

// readOrFail loads a repository artifact.
func readOrFail(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}

// readIconDir parses an .ico directory. It is small and exact: every field a Windows shell
// uses to pick a frame, and nothing it does not.
func readIconDir(t *testing.T, path string) []icoEntry {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if len(data) < 6 {
		t.Fatalf("%s is too short to hold an icon directory", path)
	}
	if got := binary.LittleEndian.Uint16(data[0:2]); got != 0 {
		t.Errorf("%s: reserved field is %d, want 0 (this is a .ico, not a .cur or .ani)", path, got)
	}
	if got := binary.LittleEndian.Uint16(data[2:4]); got != 1 {
		t.Errorf("%s: resource type is %d, want 1 (icon)", path, got)
	}
	count := int(binary.LittleEndian.Uint16(data[4:6]))
	if len(data) < 6+count*16 {
		t.Fatalf("%s declares %d entries but is only %d bytes", path, count, len(data))
	}
	entries := make([]icoEntry, 0, count)
	for i := 0; i < count; i++ {
		record := data[6+i*16 : 6+(i+1)*16]
		entry := icoEntry{
			// A zero byte in these fields means 256, the convention every tool repeats.
			width:     normalizeDimension(record[0]),
			height:    normalizeDimension(record[1]),
			bitCount:  binary.LittleEndian.Uint16(record[6:8]),
			size:      binary.LittleEndian.Uint32(record[8:12]),
			offset:    binary.LittleEndian.Uint32(record[12:16]),
			imageType: imageType(record[0], record[1], record[8:12], data),
		}
		if int(entry.offset)+int(entry.size) > len(data) {
			t.Errorf("%s: entry %d (%dx%d) points at bytes %d-%d of a %d byte file",
				path, i, entry.width, entry.height, entry.offset, entry.offset+entry.size, len(data))
		}
		entries = append(entries, entry)
	}
	return entries
}

type icoEntry struct {
	width, height int
	bitCount      uint16
	size          uint32
	offset        uint32
	imageType     string
}

func normalizeDimension(b byte) int {
	if b == 0 {
		return 256
	}
	return int(b)
}

// imageType distinguishes the two payload formats an .ico may carry, because a PNG entry is
// only legal from Vista on and the notification area of an older shell shows nothing at all.
func imageType(width, height byte, header []byte, data []byte) string {
	offset := binary.LittleEndian.Uint32(header[6:10])
	if int(offset)+8 <= len(data) && string(data[offset:offset+8]) == "\x89PNG\r\n\x1a\n" {
		return "png"
	}
	_ = width
	_ = height
	return "bmp"
}

func TestIconFilesAreLegalAndCarryEverySize(t *testing.T) {
	for name, sizes := range iconSizes {
		entries := readIconDir(t, name)
		if len(entries) != len(sizes) {
			t.Errorf("%s has %d entries, want %d", name, len(entries), len(sizes))
		}
		found := map[int]bool{}
		for _, entry := range entries {
			found[entry.width] = true
			if entry.width != entry.height {
				t.Errorf("%s: entry %dx%d is not square", name, entry.width, entry.height)
			}
			if entry.imageType != "bmp" {
				t.Errorf("%s: the %d px entry is %s; every frame must be a BMP/DIB so the "+
					"notification area can render it without a Vista-only PNG decode", name, entry.width, entry.imageType)
			}
			// 32 bit is the only depth that carries the alpha channel the rounded brand
			// tile needs; a 24 bit frame would be composited onto black.
			if entry.bitCount != 32 {
				t.Errorf("%s: the %d px entry is %d bpp, want 32 for alpha", name, entry.width, entry.bitCount)
			}
		}
		for _, size := range sizes {
			if !found[size] {
				t.Errorf("%s has no %d px entry", name, size)
			}
		}
	}
}

// TestEmbeddedTrayIconMatchesTheGeneratedFile keeps the two copies of the notification-area
// icon honest: the shell loads the embedded bytes, the packaging pipeline ships the file
// beside winres.json, and only scripts/generate-tray-icon.sh is allowed to write either.
func TestEmbeddedTrayIconMatchesTheGeneratedFile(t *testing.T) {
	generated, err := os.ReadFile(trayIcon)
	if err != nil {
		t.Fatalf("read %s: %v", trayIcon, err)
	}
	embedded, err := os.ReadFile(embeddedTrayIcon)
	if err != nil {
		t.Fatalf("read %s: %v", embeddedTrayIcon, err)
	}
	if string(generated) != string(embedded) {
		t.Errorf("%s differs from %s; run scripts/generate-tray-icon.sh", embeddedTrayIcon, trayIcon)
	}
}

// winresDocument is the shape go-winres reads: type -> name -> language id -> payload. The
// language level is what a hand written file gets wrong (a "macOS style" nesting produces
// "invalid language identifier" at package time, hours after the change looked finished).
type winresDocument map[string]map[string]map[string]json.RawMessage

func TestWinresDocumentDeclaresIconManifestAndVersion(t *testing.T) {
	var doc winresDocument
	if err := json.Unmarshal([]byte(readOrFail(t, winresJSON)), &doc); err != nil {
		t.Fatalf("parse %s: %v", winresJSON, err)
	}

	// RT_GROUP_ICON, never RT_ICON: go-winres refuses the latter outright, and the group is
	// what lets Windows pick the best frame per DPI instead of the first one.
	group, ok := doc["RT_GROUP_ICON"]["APP"]["0000"]
	if !ok {
		t.Fatalf("%s must declare RT_GROUP_ICON/APP/0000 as the application icon", winresJSON)
	}
	var iconRef string
	if err := json.Unmarshal(group, &iconRef); err != nil {
		t.Fatalf("the application icon must be a sibling file name: %v", err)
	}
	if iconRef != appIcon {
		t.Errorf("RT_GROUP_ICON/APP/0000 = %q, want %q", iconRef, appIcon)
	}
	// go-winres resolves a relative path against the directory of its own JSON file, so a
	// sibling is the only spelling that does not depend on which working directory the
	// packaging script happens to use.
	if strings.ContainsRune(iconRef, os.PathSeparator) || strings.HasPrefix(iconRef, "..") {
		t.Errorf("%s must name a sibling file, got %q", winresJSON, iconRef)
	}
	if _, err := os.Stat(iconRef); err != nil {
		t.Errorf("the referenced icon is not readable next to %s: %v", winresJSON, err)
	}

	if _, ok := doc["RT_MANIFEST"]["#1"]["0409"]; !ok {
		t.Fatalf("%s must declare RT_MANIFEST/#1/0409", winresJSON)
	}
	if _, ok := doc["RT_VERSION"]["#1"]["0000"]; !ok {
		t.Fatalf("%s must declare RT_VERSION/#1/0000", winresJSON)
	}

	// The manifest is the only place that can make a DPI-aware process before its first
	// window exists; calling SetProcessDpiAwarenessContext from Go is a fallback, not a
	// substitute, and "as invoker" is what keeps a per-user install from prompting.
	manifest := readOrFail(t, winresJSON)
	for _, want := range []string{
		`"dpi-awareness": "per monitor v2"`,
		`"execution-level": "as invoker"`,
		`"minimum-os": "win10"`,
		`"use-common-controls-v6": true`,
		`"OriginalFilename": "` + exeName + `"`,
		`"ProductName": "` + appName + `"`,
	} {
		if !strings.Contains(manifest, want) {
			t.Errorf("%s must contain %s", winresJSON, want)
		}
	}
}

// TestInstallerStaysPerUser pins the two properties that make the installer safe to hand to
// a user without administrator rights, plus the state it must clean up afterwards.
func TestInstallerStaysPerUser(t *testing.T) {
	script := readOrFail(t, installerFile)

	if !strings.Contains(script, "RequestExecutionLevel user") {
		t.Errorf("%s must request user execution level; the tray lives in HKCU and $LOCALAPPDATA "+
			"and must never prompt for elevation", installerFile)
	}
	if !strings.Contains(script, `InstallDir "$LOCALAPPDATA\Programs\`) {
		t.Errorf("%s must install under $LOCALAPPDATA so a per-user install needs no administrator", installerFile)
	}
	if strings.Contains(script, "RequestExecutionLevel admin") || strings.Contains(script, `"$PROGRAMFILES`) {
		t.Errorf("%s must not fall back to a machine-wide install", installerFile)
	}
	// The start-up entry is the one side effect that outlives the files, so the uninstaller
	// has to remove it explicitly; leaving it points Add/Remove Programs at a deleted path.
	if !strings.Contains(script, `DeleteRegValue HKCU "${RUN_KEY}" "${RUN_VALUE}"`) {
		t.Errorf("%s must delete the HKCU Run value on uninstall", installerFile)
	}
	if !strings.Contains(script, `!define RUN_VALUE "`+runValue+`"`) {
		t.Errorf("%s must use the same Run value name the tray writes (%s)", installerFile, runValue)
	}
	if !strings.Contains(script, `WriteUninstaller "$INSTDIR\Uninstall.exe"`) {
		t.Errorf("%s must write an uninstaller", installerFile)
	}
	for _, want := range []string{
		`!define APP_NAME "` + appName + `"`,
		`!define EXE_NAME "` + exeName + `"`,
		`!define APP_ID "` + appID + `"`,
		websiteURL,
		"!include \"MUI2.nsh\"",
	} {
		if !strings.Contains(script, want) {
			t.Errorf("%s must contain %s", installerFile, want)
		}
	}
}

// TestPackageScriptBuildsEveryArtifactItPromises is the contract the release workflow relies
// on. The script is the only thing that can be checked from a non-Windows host, so the
// assertions name the flags that decide whether the produced binary is a GUI program with the
// brand resources in it, rather than a console executable with an icon nobody asked for.
func TestPackageScriptBuildsEveryArtifactItPromises(t *testing.T) {
	script := readOrFail(t, packageScript)

	for _, want := range []string{
		// The tray is gated behind a build tag on every platform; a script that forgot it
		// would package a binary that exits with "built without the tray shell".
		"-tags tray",
		"GOOS=windows",
		// Cross-compiled from Linux and macOS, so cgo has to be off by construction.
		"CGO_ENABLED=0",
		// Without windowsgui the tray opens a console window when started from Explorer or a
		// login item, which is the single most visible way to get a tray app wrong.
		"-H=windowsgui",
		"-trimpath",
		// internal/build carries the version the About tab shows.
		"internal/build.Version",
		"internal/build.Commit",
		"internal/build.BuildTime",
		"go-winres make",
		winresJSON,
		"makensis",
		installerFile,
		"-DVERSION=",
		"-DARCH=",
		"-DBUILD_DIR=",
		"-DICON_FILE=",
		"TunnelMeshClient-${VERSION}-windows-${arch}.zip",
		"TunnelMeshClient-${VERSION}-windows-${INSTALLER_ARCH}-setup.exe",
		"SHA256SUMS",
		"manifest.json",
		"installerLayout",
	} {
		if !strings.Contains(script, want) {
			t.Errorf("%s must contain %q", packageScript, want)
		}
	}

	// The .syso go-winres writes is a build input, not a source file: it is regenerated per
	// release and must never be left behind for the next `go build` to pick up silently.
	if !strings.Contains(script, ".syso") || !strings.Contains(script, "trap ") {
		t.Errorf("%s must clean up the generated .syso files on exit", packageScript)
	}
	// A stale front end would be packaged into a release that shows the previous UI, and the
	// failure only appears on the operator's machine.
	if !strings.Contains(script, "npm run build") {
		t.Errorf("%s must tell the operator how to refresh the embedded bundle", packageScript)
	}
	for _, tool := range []string{"go", "zip", "makensis"} {
		if !strings.Contains(script, tool) {
			t.Errorf("%s must require %s", packageScript, tool)
		}
	}
	// The green archive has to carry the licence the repository states, or a download page
	// ships a binary with no licence next to it.
	for _, name := range []string{"LICENSE", "NOTICE", "README"} {
		if !strings.Contains(script, name) {
			t.Errorf("%s must put %s in the archive", packageScript, name)
		}
	}

	info, err := os.Stat(packageScript)
	if err != nil {
		t.Fatalf("stat %s: %v", packageScript, err)
	}
	if info.Mode().Perm()&0o100 == 0 {
		t.Errorf("%s must be executable", packageScript)
	}
}

// TestPackageScriptRefusesAnUnfinishedBuild covers the two silent failures: an installer that
// makensis could not write, and an archive built from a bundle that is not the one the front
// end just compiled.
func TestPackageScriptRefusesAnUnfinishedBuild(t *testing.T) {
	script := readOrFail(t, packageScript)

	for _, want := range []string{
		`WEB_DIST_DIR="$ROOT_DIR/internal/tray/webdist/dist"`,
		"$WEB_DIST_DIR/index.html",
		"stale",
		"exit 1",
	} {
		if !strings.Contains(script, want) {
			t.Errorf("%s must contain %q", packageScript, want)
		}
	}
	// makensis exits 0 even when nothing useful came out, so the produced file is the check.
	if !strings.Contains(script, "setup.exe") || !strings.Contains(script, "-s \"$setup\"") {
		t.Errorf("%s must verify the installer file exists after makensis runs", packageScript)
	}
}

// TestPanelRouteMatchesTheShell repeats the macOS guard for the Windows shell: both load
// tray.PanelURL(), and the fragment has to be the one the router recognises.
func TestPanelRouteMatchesTheShell(t *testing.T) {
	source := readOrFail(t, panelRouteSource)
	if !strings.Contains(source, `PANEL_ROUTE = '`+tray.PanelRoute+`'`) {
		t.Errorf("%s must export the same fragment tray.PanelRoute (%q) builds", panelRouteSource, tray.PanelRoute)
	}
}

// TestNoCompiledWindowsResourcesAreCommitted fails the moment a generated .syso is staged.
func TestNoCompiledWindowsResourcesAreCommitted(t *testing.T) {
	var found []string
	err := filepath.WalkDir("../../cmd", func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() && strings.HasSuffix(path, ".syso") {
			found = append(found, path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk cmd: %v", err)
	}
	if len(found) > 0 {
		t.Errorf("generated Windows resources must not be in the tree: %s", strings.Join(found, ", "))
	}
}

func walkSyso(found *[]string, path string, info os.FileInfo, err error) error {
	if err != nil {
		return err
	}
	if !info.IsDir() && strings.HasSuffix(path, ".syso") {
		*found = append(*found, path)
	}
	return nil
}

// TestReleaseScriptNeverCompilesTheTray keeps the split honest: build-release.sh merges the
// Windows artifacts like the macOS ones, and never links the tray itself.
func TestReleaseScriptNeverCompilesTheTray(t *testing.T) {
	script := readOrFail(t, releaseScript)
	if strings.Contains(script, "-tags tray") {
		t.Errorf("%s must not compile the tray: it belongs to the platform-specific packaging "+
			"scripts, and a cross-platform matrix that links WebKit or WebView2 fails on every "+
			"host but one", releaseScript)
	}
	if !strings.Contains(script, "WINDOWS_TRAY_DIST_DIR") {
		t.Errorf("%s must accept the Windows tray hand-off through WINDOWS_TRAY_DIST_DIR", releaseScript)
	}
}
