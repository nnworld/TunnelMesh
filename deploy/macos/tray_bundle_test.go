package macos

import (
	"image/color"
	"image/png"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/tunnelmesh/tunnelmesh/internal/tray"
)

const (
	iconFile      = "TunnelMeshClient.icns"
	infoTemplate  = "TunnelMeshClient-Info.plist"
	packageScript = "../../scripts/package-macos-tray.sh"
	iconGenerator = "../../scripts/generate-tray-icon.sh"
	// iconGeometry is the drawing itself. The .icns and the menu-bar glyph have to come out
	// of one set of numbers, and this is the file that holds them.
	iconGeometry = "../../scripts/trayicon/main.go"
	// panelRouteSource is the front end's half of the quick-panel address.
	panelRouteSource = "../../web-tray/src/panelRoute.ts"
	releaseScript    = "../../scripts/build-release.sh"

	// layoutScript is the Finder pass that turns the archive into an installer window.
	// It lives beside the plist rather than inside the shell script because it is
	// AppleScript, not bash: as its own file it can be read, diffed and asserted on.
	layoutScript = "tray-dmg-layout.applescript"

	// bundleID follows the com.tunnelmesh.<role> convention the launchd templates use.
	// It is not free to drift: SMAppService keys the login item by bundle identifier, so
	// changing it silently orphans every operator's existing "launch at login" entry.
	bundleID   = "com.tunnelmesh.client-tray"
	executable = "tunnelmesh-client-tray"
	minMacOS   = "13.0"
)

// literalMinimumMacOS matches a second, hand written copy of the deployment floor in the
// packaging script. The plist is the only place that value may be spelled out.
var literalMinimumMacOS = regexp.MustCompile(`MIN_MACOS="[0-9]`)

// readOrFail loads a repository artifact.
func readOrFail(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}

// assertKey checks a plist key is followed by the expected value element. The template is
// hand written and validated with plutil at package time; this only has to catch a key
// that was renamed or dropped, which plutil cannot notice because the result is still a
// well formed plist that behaves differently.
func assertKey(t *testing.T, plist, key, valueElement string) {
	t.Helper()
	want := "<key>" + key + "</key>\n  <string>" + valueElement + "</string>"
	if !strings.Contains(plist, want) {
		t.Errorf("%s must set %s to %q", infoTemplate, key, valueElement)
	}
}

// TestInfoTemplateKeepsTheTrayRequirements covers the three keys whose absence fails
// silently rather than loudly.
func TestInfoTemplateKeepsTheTrayRequirements(t *testing.T) {
	plist := readOrFail(t, infoTemplate)

	// Without LSUIElement the app gets a Dock icon and steals focus from whatever the
	// operator was doing every time the window opens.
	if !strings.Contains(plist, "<key>LSUIElement</key>\n  <true/>") {
		t.Errorf("%s must set LSUIElement: a menu-bar client has no Dock presence", infoTemplate)
	}
	// The settings interface is served over http from 127.0.0.1. App Transport Security
	// blocks that by default and WKWebView renders a blank window with no error, so this
	// is the single easiest way to ship a tray that looks broken.
	if !strings.Contains(plist, "<key>NSAllowsLocalNetworking</key>\n    <true/>") {
		t.Errorf("%s must allow local networking for the loopback settings interface", infoTemplate)
	}
	// NSRequiresAquaSystemAppearance=false is what lets the "follow system" theme actually
	// follow the system instead of being pinned to the light appearance.
	if !strings.Contains(plist, "<key>NSRequiresAquaSystemAppearance</key>\n  <false/>") {
		t.Errorf("%s must not require the Aqua appearance, or the dark theme cannot follow the system", infoTemplate)
	}

	assertKey(t, plist, "CFBundleIdentifier", bundleID)
	assertKey(t, plist, "CFBundleExecutable", executable)
	assertKey(t, plist, "CFBundlePackageType", "APPL")
	assertKey(t, plist, "NSPrincipalClass", "NSApplication")
	// SMAppService mainAppService, which the login item uses, exists from macOS 13.
	assertKey(t, plist, "LSMinimumSystemVersion", minMacOS)

	// Version placeholders are rendered by the packaging script; exactly two are expected
	// (CFBundleShortVersionString and CFBundleVersion), and no other placeholder may leak
	// into a shipped bundle.
	payload := stripComments(plist)
	if got := strings.Count(payload, "__VERSION__"); got != 2 {
		t.Errorf("%s contains %d __VERSION__ placeholders, want 2", infoTemplate, got)
	}
	for _, line := range strings.Split(payload, "\n") {
		trimmed := strings.TrimSpace(line)
		if !strings.Contains(trimmed, "__") {
			continue
		}
		if strings.Contains(trimmed, "__VERSION__") {
			continue
		}
		t.Errorf("%s has an unrendered placeholder outside a comment: %s", infoTemplate, trimmed)
	}

	// The bundle is a distribution surface and a login item; a token in it would outlive
	// the process and be readable by anything that can read /Applications.
	for _, forbidden := range []string{"token", "password", "secret", "private"} {
		if strings.Contains(strings.ToLower(payload), forbidden) {
			t.Errorf("%s must not mention %q outside comments; credentials belong in ~/.config/tunnelmesh/client.yaml", infoTemplate, forbidden)
		}
	}
}

// stripComments removes XML comments so a prohibition can be checked against the payload
// only. The template explains itself in prose, and that prose legitimately names the
// things the values must never contain.
func stripComments(plist string) string {
	var kept []string
	inComment := false
	for _, line := range strings.Split(plist, "\n") {
		if inComment {
			if strings.Contains(line, "-->") {
				inComment = false
			}
			continue
		}
		if strings.Contains(line, "<!--") {
			if !strings.Contains(line, "-->") {
				inComment = true
			}
			continue
		}
		kept = append(kept, line)
	}
	return strings.Join(kept, "\n")
}

// TestPackageScriptMatchesTheTemplate keeps the script and the plist from drifting.
func TestPackageScriptMatchesTheTemplate(t *testing.T) {
	script := readOrFail(t, packageScript)
	for _, want := range []string{
		"-tags tray",
		"CGO_ENABLED=1",
		"deploy/macos/TunnelMeshClient-Info.plist",
		"cmd/tunnelmesh-client-tray",
		"internal/tray/webdist/dist/index.html",
		"codesign --force --sign",
		"codesign --verify",
		bundleID,
		executable,
		"LSMinimumSystemVersion",
		"LICENSE",
		"NOTICE",
	} {
		if !strings.Contains(script, want) {
			t.Errorf("%s must reference %q", packageScript, want)
		}
	}
	// The tray can only be built where the frameworks it links exist.
	if !strings.Contains(script, `uname -s`) || !strings.Contains(script, "Darwin") {
		t.Errorf("%s must refuse to run off macOS", packageScript)
	}
}

// TestPackageScriptPinsTheMachOSDeploymentTarget keeps the shipped binary's minimum macOS
// equal to the version the bundle declares.
//
// A cgo link records the *build host's* OS release as the Mach-O LC_BUILD_VERSION minos,
// and LaunchServices enforces that number rather than the Info.plist. Packaging on a
// macOS 15 runner therefore produced an app that a macOS 14 machine refused to open with
// "requires macOS 15.0 or later", while LSMinimumSystemVersion still said 13.0 and
// manifest.json still reported 13.0. Two halves close this: tell the toolchain the floor,
// then prove the artifact obeys it, because a flag that stops being honoured is exactly as
// invisible as one that was never set.
func TestPackageScriptPinsTheMachOSDeploymentTarget(t *testing.T) {
	script := readOrFail(t, packageScript)
	for _, want := range []string{
		// The plist is the single source of truth for the floor.
		`plutil -extract LSMinimumSystemVersion raw "$INFO_TEMPLATE"`,
		// clang takes the target from the environment for the Go objects and from the
		// flag for the cgo objects; setting only one leaves ld warnings and, on the
		// linker side, a minos that still tracks the host.
		"MACOSX_DEPLOYMENT_TARGET=",
		"-mmacosx-version-min=",
		// The build has to fail rather than ship an app that will not launch.
		"LC_BUILD_VERSION",
		"minos",
	} {
		if !strings.Contains(script, want) {
			t.Errorf("%s must reference %q", packageScript, want)
		}
	}
	if got := literalMinimumMacOS.FindString(script); got != "" {
		t.Errorf("%s must derive the minimum macOS version from %s, not repeat it (%s)",
			packageScript, infoTemplate, got)
	}
}

// TestPackageScriptEmitsOneDiskImagePerArchitecture pins the distribution format.
//
// A .dmg is what a macOS user double-clicks: it shows the app beside an "Applications"
// alias, and unlike a zip it reaches the user with the bundle's signature and quarantine
// handling intact. The name is part of the release contract, because SHA256SUMS and the
// tray manifest both refer to it and the release workflow asserts both filenames exist.
func TestPackageScriptEmitsOneDiskImagePerArchitecture(t *testing.T) {
	script := readOrFail(t, packageScript)
	for _, want := range []string{
		"tunnelmesh-client-tray-${VERSION}-${platform}.dmg",
		"hdiutil create",
		"-format UDZO",
		"hdiutil verify",
		"ln -s /Applications",
		`\"extension\":\"dmg\"`,
		// ditto rather than cp -R: it preserves the extended attributes and resource
		// forks a signed bundle carries, which a plain copy silently drops.
		"ditto",
	} {
		if !strings.Contains(script, want) {
			t.Errorf("%s must reference %q", packageScript, want)
		}
	}
	// The zip path is retired, not kept beside the disk image: two archives per
	// architecture would be two things to sign, checksum and explain.
	for _, retired := range []string{".zip", "--keepParent", "--sequesterRsrc"} {
		if strings.Contains(script, retired) {
			t.Errorf("%s still produces a zip archive (%q); the tray ships as a .dmg", packageScript, retired)
		}
	}
}

// TestReleaseScriptNeverCompilesTheTray pins the split between the two build scripts.
//
// build-release.sh cross-compiles every platform with CGO_ENABLED=0 from a Linux runner.
// The tray links Cocoa and WebKit through cgo, so it can only be produced on a Mac; if it
// ever appeared in the matrix the release job would fail on a host that can never satisfy
// it, and the failure would look like a regression in the other three binaries.
//
// Merging is a different matter and is allowed: the release workflow builds the disk images
// on a macOS runner and build-release.sh folds them into the release directory through
// scripts/merge-tray-dist.sh. What it must never do is *build* the tray.
func TestReleaseScriptNeverCompilesTheTray(t *testing.T) {
	script := readOrFail(t, releaseScript)
	if strings.Contains(script, executable) {
		t.Errorf("%s must not build %s; use scripts/package-macos-tray.sh on a macOS host", releaseScript, executable)
	}
	if strings.Contains(script, "-tags tray") {
		t.Errorf("%s must not enable the tray build tag", releaseScript)
	}
	if strings.Contains(script, "CGO_ENABLED=1") {
		t.Errorf("%s must stay CGO_ENABLED=0 only; cgo cannot be cross-compiled from the Linux release runner", releaseScript)
	}
	if strings.Contains(script, "hdiutil") {
		t.Errorf("%s must not create disk images; hdiutil does not exist on the Linux release runner", releaseScript)
	}
	// The admin bundle check is the precedent the tray script copies, so losing it here
	// would leave the two release paths disagreeing about stale front-end output.
	if !strings.Contains(script, "internal/server/web_dist/index.html") {
		t.Errorf("%s lost its embedded admin bundle check", releaseScript)
	}
}

// TestPackageScriptLaysOutAnInstallerWindow pins the two-stage disk image build.
//
// A folder that happens to hold an "Applications" alias does install, but it does not read
// as an installer: the window size, the icon view and the two icon positions are Finder
// state stored in .DS_Store, and the only supported way to write that state is to script
// Finder over a mounted read-write image. The compressed image the release ships is then
// produced from that image, so create -> attach -> lay out -> detach -> convert is the
// contract. Dropping any step silently ships a plain folder window.
func TestPackageScriptLaysOutAnInstallerWindow(t *testing.T) {
	script := readOrFail(t, packageScript)
	for _, want := range []string{
		"-format UDRW",
		"hdiutil attach",
		"-mountpoint",
		layoutScript,
		"hdiutil detach",
		"hdiutil convert",
		"-imagekey zlib-level=9",
		// A leading dot alone does not hide a folder from Finder; the flag does.
		"chflags hidden",
	} {
		if !strings.Contains(script, want) {
			t.Errorf("%s must reference %q", packageScript, want)
		}
	}

	// The layout pass needs a scriptable Finder, which a headless runner may not offer. It
	// has to degrade loudly instead of failing the release, and the manifest must record
	// what the shipped image really contains, so no release note can claim an installer
	// layout that the build could not write.
	if !strings.Contains(script, `\"installerLayout\"`) {
		t.Errorf("%s must record the layout outcome in the tray manifest", packageScript)
	}
	if !strings.Contains(script, "opens as a plain folder window") {
		t.Errorf("%s must warn when the installer layout could not be applied", packageScript)
	}
}

// TestLayoutScriptDrivesFinderThroughTheInstallerWindow checks the Finder pass by hand.
//
// AppleScript is not compiled by the test suite and a typo in it only surfaces on a macOS
// host, so the lines that carry the behaviour are pinned: without "not arranged" Finder
// re-flows the icons and ignores the positions, and without the close the window state is
// never flushed to .DS_Store.
func TestLayoutScriptDrivesFinderThroughTheInstallerWindow(t *testing.T) {
	body := readOrFail(t, layoutScript)
	for _, want := range []string{
		"on run argv",
		`tell application "Finder"`,
		"set current view of installerWindow to icon view",
		"set arrangement of viewOptions to not arranged",
		"set icon size of viewOptions to iconSize",
		"set bounds of installerWindow to",
		"set position of item appName of installerWindow to appPosition",
		`set position of item "Applications" of installerWindow to applicationsPosition`,
		"set background picture of viewOptions",
		"update volumeRef without registering applications",
		"close installerWindow",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("%s must contain %q", layoutScript, want)
		}
	}
	// The mount point, the app name and the optional background are arguments. Hardcoding
	// one mount point would break as soon as two architectures are packaged in a row.
	for _, forbidden := range []string{"/Volumes/", "TunnelMesh Client"} {
		if strings.Contains(body, forbidden) {
			t.Errorf("%s must not hardcode %q; it is handed the mount point as an argument", layoutScript, forbidden)
		}
	}
}

// TestBundleShipsABrandIcon keeps Finder from showing the generic application tile.
//
// The tray is distributed as a .app, and a bundle whose CFBundleIconFile names a file that
// is not in the archive renders as a blank document in Finder and in the mounted installer.
// The icon is a checked-in asset generated by scripts/generate-tray-icon.sh, so this test is
// the guard that the asset and the key stay together.
func TestBundleShipsABrandIcon(t *testing.T) {
	plist := readOrFail(t, infoTemplate)
	assertKey(t, plist, "CFBundleIconFile", "AppIcon")

	data, err := os.ReadFile(iconFile)
	if err != nil {
		t.Fatalf("%s must exist: %v (regenerate it with scripts/generate-tray-icon.sh)", iconFile, err)
	}
	if len(data) < 8 || string(data[:4]) != "icns" {
		t.Fatalf("%s is not an ICNS container (bad magic %q)", iconFile, data[:min(4, len(data))])
	}

	// An .icns is a header plus length-prefixed entries, each named for the pixel size it
	// carries. iconutil picks the names, and more than one spells the same size (icp4, ic05
	// and icm4 are all 16×16), so the contract is asserted in pixels rather than in tags: a
	// bundle that ships only the large representations looks blurred in a Finder list view,
	// and one that omits the 1024 tile looks pixelated in Cover Flow.
	sizes := map[int]bool{}
	for offset := 8; offset+8 <= len(data); {
		name := string(data[offset : offset+4])
		length := int(data[offset+4])<<24 | int(data[offset+5])<<16 | int(data[offset+6])<<8 | int(data[offset+7])
		if length < 8 || offset+length > len(data) {
			t.Fatalf("%s has a malformed entry %q at offset %d (length %d, file %d)", iconFile, name, offset, length, len(data))
		}
		if edge, ok := icnsEdges[name]; ok {
			sizes[edge] = true
		}
		offset += length
	}
	for _, want := range []int{16, 128, 512, 1024} {
		if !sizes[want] {
			t.Errorf("%s has no %dpx representation; regenerate it with scripts/generate-tray-icon.sh", iconFile, want)
		}
	}

	// Packaging has to copy the asset to the name the plist promises, and the mounted
	// installer volume carries the same tile so the disk image is recognisable too.
	script := readOrFail(t, packageScript)
	for _, want := range []string{"Contents/Resources/AppIcon.icns", ".VolumeIcon.icns"} {
		if !strings.Contains(script, want) {
			t.Errorf("%s must install %s", packageScript, want)
		}
	}
	for _, want := range []string{"scripts/generate-tray-icon.sh", "iconutil -c icns", "deploy/macos/" + "TunnelMeshClient.icns"} {
		if !strings.Contains(readOrFail(t, iconGenerator), want) {
			t.Errorf("the icon generator must reference %q", want)
		}
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// icnsEdges maps an Icon Services representation name to its edge length in pixels. Only the
// PNG kinds matter here; the JPEG and RLE names of the classic era are not produced by
// iconutil on Apple Silicon and are left out rather than guessed at.
var icnsEdges = map[string]int{
	"icp4": 16, "icm4": 16, "ic05": 16,
	"icp5": 32, "ic14": 32, "ic12": 32, "ic06": 32,
	"ic13": 64,
	"icp6": 48, "ic04": 48,
	"ic07": 128,
	"ic11": 256, "ic08": 256,
	"ic09": 512, "ic10": 1024,
}

// menuBarGlyphs are the status-item renderings, at 1x, 2x and 3x of the 18pt bar. They are
// committed artifacts for the same reason the .icns is: the bundle has to be reproducible
// from the repository without a design tool, and a menu bar that shows a different shape
// from Finder is the bug this guards.
var menuBarGlyphs = []struct {
	file string
	edge int
}{
	{"TunnelMeshMenuBar.png", 18},
	{"TunnelMeshMenuBar@2x.png", 36},
	{"TunnelMeshMenuBar@3x.png", 54},
}

// TestMenuBarGlyphIsATemplateOfTheBrandMark checks the committed glyph files.
//
// A template image is tinted by the system, which means only its alpha channel is read.
// Colour in the file would therefore be invisible on the menu bar and visible in a
// preview, so the two halves of the review would disagree. These files are generated, so
// the assertions are about the generator's output contract rather than about bytes.
func TestMenuBarGlyphIsATemplateOfTheBrandMark(t *testing.T) {
	for _, glyph := range menuBarGlyphs {
		path := glyph.file
		file, err := os.Open(path)
		if err != nil {
			t.Fatalf("open %s: %v (regenerate with scripts/generate-tray-icon.sh)", path, err)
		}
		img, err := png.Decode(file)
		file.Close()
		if err != nil {
			t.Fatalf("decode %s: %v", path, err)
		}
		bounds := img.Bounds()
		if bounds.Dx() != glyph.edge || bounds.Dy() != glyph.edge {
			t.Errorf("%s is %dx%d, want %dpx square", path, bounds.Dx(), bounds.Dy(), glyph.edge)
		}
		var covered, coloured int
		for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
			for x := bounds.Min.X; x < bounds.Max.X; x++ {
				pixel := color.GrayModel.Convert(img.At(x, y)).(color.Gray)
				rgba := color.RGBAModel.Convert(img.At(x, y)).(color.RGBA)
				if rgba.A > 200 {
					covered++
					// GrayModel flattens against black, so a coloured pixel reads as a
					// lighter grey than a black one at the same alpha.
					if pixel.Y > 8 {
						coloured++
					}
				}
			}
		}
		total := bounds.Dx() * bounds.Dy()
		// The mark has to be present and has to leave the bar mostly empty: a blank file
		// and a filled square both pass "some pixels are set" but neither is a glyph.
		if covered*100 < total*12 {
			t.Errorf("%s covers %d of %d pixels, want at least 12%%: the mark is missing", path, covered, total)
		}
		if covered*100 > total*75 {
			t.Errorf("%s covers %d of %d pixels, want under 75%%: that is a tile, not a glyph", path, covered, total)
		}
		if coloured != 0 {
			t.Errorf("%s has %d non-black opaque pixels; a template image is tinted by the system "+
				"and colour in the file only misleads whoever previews it", path, coloured)
		}
	}
}

// TestMenuBarGlyphPackagingAndGeneration pins the pipeline: generated beside the .icns,
// copied into the bundle, and drawn from the same geometry as the .icns.
func TestMenuBarGlyphPackagingAndGeneration(t *testing.T) {
	packageScript := readOrFail(t, packageScript)
	for _, want := range []string{"TunnelMeshMenuBar.png", "TunnelMeshMenuBar@2x.png", "Contents/Resources/"} {
		if !strings.Contains(packageScript, want) {
			t.Errorf("%s must copy the menu-bar glyph into the bundle (looking for %q)", packageScript, want)
		}
	}
	generator := readOrFail(t, iconGenerator)
	if !strings.Contains(generator, "-menubar") {
		t.Errorf("%s must regenerate the menu-bar glyph with -menubar, so one command updates both halves of the brand", generator)
	}
	// One geometry, two framings. The glyph calls the same predicate the tile does, which
	// is what makes "the menu bar icon does not match Finder" structurally impossible.
	sources := readOrFail(t, iconGeometry)
	for _, want := range []string{"insideMark(glyphPoint(", "glyphInset"} {
		if !strings.Contains(sources, want) {
			t.Errorf("%s must draw the menu-bar glyph from the tile's own mark (looking for %q)", iconGeometry, want)
		}
	}
	if strings.Contains(sources, "func drawGlyphMark") || strings.Contains(sources, "glyphStrokeScale") {
		t.Errorf("%s must not keep a second copy of the mark geometry; the glyph and the tile drift apart the "+
			"moment either one is redrawn by hand", iconGeometry)
	}
}

// TestPanelRouteMatchesTheBundle pins the two ends of the quick panel's address together.
//
// The shell loads tray.PanelURL() and the bundle picks its root component from the same
// fragment. Either side renaming it alone produces a settings window where a panel should
// be, which no test on one side alone can see.
func TestPanelRouteMatchesTheBundle(t *testing.T) {
	source := readOrFail(t, panelRouteSource)
	if !strings.Contains(source, `PANEL_ROUTE = '`+tray.PanelRoute+`'`) {
		t.Errorf("%s must export the same fragment tray.PanelRoute (%q) builds, or the shell loads the "+
			"full window into the panel", panelRouteSource, tray.PanelRoute)
	}
}
