package scripts

import (
	"go/ast"
	"go/build/constraint"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// nativePackage is the only place in the module allowed to reach Cocoa and WebKit.
const nativePackage = "github.com/tunnelmesh/tunnelmesh/internal/tray/native"

// nativeSourceSuffixes are the files cgo hands to the platform compiler. Unlike a Go
// import there is no build constraint that can gate them individually: they are compiled
// whenever their package is, so the package's Go files have to carry the tag instead.
var nativeSourceSuffixes = []string{".m", ".mm", ".c", ".cc", ".S"}

// builtWithoutTrayTag reports whether a file would be compiled in a build that does not
// set the "tray" tag.
//
// Every other tag is treated as satisfied on purpose. That is the worst case for this
// guard: a constraint such as "tray || linux" still compiles on a plain Linux checkout,
// and it is exactly the kind of slip that would make the default build need a WebKit
// toolchain. Evaluating the real expression, rather than string matching "//go:build tray",
// also accepts any equivalent spelling a future file might use.
func builtWithoutTrayTag(t *testing.T, path string) bool {
	t.Helper()
	line := buildConstraintOf(t, path)
	if line == "" {
		return true
	}
	expr, err := constraint.Parse(line)
	if err != nil {
		t.Fatalf("%s has an unparsable build constraint %q: %v", path, line, err)
	}
	return expr.Eval(func(tag string) bool { return tag != "tray" })
}

// importsC reports whether the file is a cgo file.
func importsC(t *testing.T, fset *token.FileSet, path string) (bool, []string) {
	t.Helper()
	file, err := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
	if err != nil {
		t.Fatalf("%s does not parse: %v", path, err)
	}
	var imports []string
	cgo := false
	for _, spec := range file.Imports {
		importPath := strings.Trim(spec.Path.Value, `"`)
		if importPath == "C" {
			cgo = true
			continue
		}
		imports = append(imports, importPath)
	}
	return cgo, imports
}

// skipDir names the directories neither walk may descend into.
//
// One list for both walks is the point: they used to disagree about testdata, and a
// fixture `.c` file dropped in there would have made the native-source guard demand a
// build tag from Go files the tool never compiles. testdata is ignored by the go tool,
// node_modules and the dist directories hold no module source, and .worktrees holds
// whole duplicate checkouts that would otherwise be scanned twice.
func skipDir(name string) bool {
	switch name {
	case ".git", ".worktrees", "node_modules", "dist", "web_dist", "testdata":
		return true
	}
	return false
}

// walkModule visits every file in the module, skipping directories that hold no source of
// their own.
func walkModule(t *testing.T, fn func(path string, entry fs.DirEntry)) {
	t.Helper()
	err := filepath.WalkDir("..", func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if skipDir(entry.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		fn(path, entry)
		return nil
	})
	if err != nil {
		t.Fatalf("walk the module: %v", err)
	}
}

// goFilesUnder calls fn for every Go file in the module.
func goFilesUnder(t *testing.T, fn func(path string)) {
	t.Helper()
	seen := 0
	walkModule(t, func(path string, entry fs.DirEntry) {
		if strings.HasSuffix(path, ".go") {
			seen++
			fn(path)
		}
	})
	if seen == 0 {
		t.Fatal("no Go files were checked, so the guard proves nothing")
	}
}

// TestNativeShellIsBuildTagIsolated keeps the default build free of Cocoa and WebKit.
//
// The rule mirrors the one the VPN gateway is held to, and for the same operational
// reason: `go build ./...` and `go test ./...` run with CGO_ENABLED=0 on Linux CI and on
// every developer checkout that has no Xcode toolchain. A cgo file or an importer of the
// native package that lost its tag would turn those into hard failures, and would put a
// WebKit link dependency into binaries that can never open a window.
func TestNativeShellIsBuildTagIsolated(t *testing.T) {
	fset := token.NewFileSet()
	goFilesUnder(t, func(path string) {
		cgo, imports := importsC(t, fset, path)
		reason := ""
		switch {
		case cgo:
			reason = "it uses cgo"
		default:
			for _, importPath := range imports {
				if importPath == nativePackage || strings.HasPrefix(importPath, nativePackage+"/") {
					reason = "it imports " + nativePackage
					break
				}
			}
		}
		if reason == "" {
			return
		}
		if builtWithoutTrayTag(t, path) {
			t.Errorf("%s must carry a build constraint that excludes it without the tray tag: %s, constraint is %q",
				path, reason, buildConstraintOf(t, path))
		}
	})
}

// TestNativeSourceDirectoriesAreFullyGated covers the half of the shim that Go's import
// graph cannot express.
//
// cgo compiles every C-family source in a package when any built Go file in it imports
// "C", and refuses the package outright when none does but the sources are present. So a
// directory holding tray_darwin.m cannot mix tagged and untagged Go files: the untagged
// build would either link Objective-C or fail to compile. Requiring the whole directory
// to be gated is the only stable shape.
func TestNativeSourceDirectoriesAreFullyGated(t *testing.T) {
	dirs := map[string]bool{}
	walkModule(t, func(path string, entry fs.DirEntry) {
		for _, suffix := range nativeSourceSuffixes {
			if strings.HasSuffix(entry.Name(), suffix) {
				dirs[filepath.Dir(path)] = true
			}
		}
	})
	if len(dirs) == 0 {
		t.Fatal("no native source directories were found, so the guard proves nothing")
	}
	for dir := range dirs {
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatalf("read %s: %v", dir, err)
		}
		for _, entry := range entries {
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") {
				continue
			}
			path := filepath.Join(dir, entry.Name())
			if builtWithoutTrayTag(t, path) {
				t.Errorf("%s shares a directory with native cgo sources but builds without the tray tag, constraint is %q",
					path, buildConstraintOf(t, path))
			}
		}
	}
}

// TestTrayLogicPackageStaysCGOFree asserts the split the design depends on:
// internal/tray holds the tray's logic and must be testable everywhere, while
// internal/tray/native holds the platform shim and is only ever compiled on a Mac.
//
// If cgo creeps into the parent package, `go test ./internal/tray/...` stops running on
// Linux CI and the settings, validation and stats code loses its automated coverage -
// which is the coverage that makes the whole feature reviewable.
func TestTrayLogicPackageStaysCGOFree(t *testing.T) {
	fset := token.NewFileSet()
	checked := 0
	entries, err := os.ReadDir("../internal/tray")
	if err != nil {
		t.Fatalf("read internal/tray: %v", err)
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") {
			continue
		}
		path := filepath.Join("../internal/tray", entry.Name())
		checked++
		if cgo, _ := importsC(t, fset, path); cgo {
			t.Errorf("%s uses cgo; only internal/tray/native may, so that the tray logic stays testable with CGO_ENABLED=0", path)
		}
	}
	if checked == 0 {
		t.Fatal("internal/tray holds no Go files, so the guard proves nothing")
	}
}

// TestTrayWebEmbedIsBuildTagIsolated guards the clean-checkout build.
//
// `//go:embed` fails the compile when its pattern matches nothing, and the settings
// bundle is a build output that no checkout has until `cd web-tray && npm run build` has
// run. An untagged embed would therefore make every `go build ./...` depend on a
// front-end toolchain, including a Linux checkout that can never run the tray. The stub
// half must exist for the same reason, or the package is empty without the tag.
func TestTrayWebEmbedIsBuildTagIsolated(t *testing.T) {
	dir := "../internal/tray/webdist"
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read %s: %v", dir, err)
	}
	embedded := 0
	untagged := 0
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		if strings.Contains(string(data), "//go:embed") {
			embedded++
			if builtWithoutTrayTag(t, path) {
				t.Errorf("%s embeds the settings bundle but builds without the tray tag, constraint is %q",
					path, buildConstraintOf(t, path))
			}
		}
		if !builtWithoutTrayTag(t, path) {
			continue
		}
		if strings.Contains(string(data), "import \"C\"") {
			t.Errorf("%s must not use cgo", path)
		}
		untagged++
	}
	if embedded == 0 {
		t.Errorf("%s no longer embeds the bundle; the settings window would have nothing to serve", dir)
	}
	if untagged == 0 {
		t.Errorf("%s has no file that builds without the tray tag, so the default build cannot compile the package", dir)
	}
}

// TestTrayCommandKeepsAnUnsupportedPlatformFallback keeps the entry point buildable
// everywhere.
//
// A wildcard `go build ./...` silently skips a directory whose files are all constrained
// away, so losing the fallback would not break CI - it would break `go build ./cmd/...`
// and every install path that names the tray binary, with a confusing "build constraints
// exclude all Go files" instead of the intended "macOS only" message.
func TestTrayCommandKeepsAnUnsupportedPlatformFallback(t *testing.T) {
	dir := "../cmd/tunnelmesh-client-tray"
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read %s: %v", dir, err)
	}
	fallbacks := 0
	gated := 0
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		if builtWithoutTrayTag(t, path) {
			fallbacks++
		} else {
			gated++
		}
	}
	if gated == 0 {
		t.Errorf("%s has no tray-gated entry point; the macOS shell would never be built", dir)
	}
	if fallbacks == 0 {
		t.Errorf("%s has no file that builds without the tray tag; non-macOS builds would fail instead of reporting that the tray is macOS only", dir)
	}
}

// shellAPISurface is every symbol of the native shell that cmd/tunnelmesh-client-tray
// calls. They are listed rather than discovered because the point of the list is that the
// command stays one file: a function the two shells disagree about forces a second main.go
// behind a build tag, which is how a shared code path silently becomes two.
var shellAPISurface = []string{
	"Run", "Stop", "ShowWindow", "HideWindow", "SetMinimizeToTray", "SetQuickPanel",
	"SetHandlers", "DefaultConfig", "OpenURL", "System", "PreferredLanguage",
	"RendererName", "RendererDetail", "NewAutostart",
}

// shellAPITypes are the types that same command passes across the boundary.
var shellAPITypes = []string{"Config", "Handlers", "SystemInfo", "Autostart"}

// buildConstraintsFor returns the Go files of the native package that a build with the
// "tray" tag on one operating system would compile, together with their top-level
// declarations.
func buildConstraintsFor(t *testing.T, goos string) map[string]bool {
	t.Helper()
	declared := map[string]bool{}
	entries, err := os.ReadDir("../internal/tray/native")
	if err != nil {
		t.Fatalf("read internal/tray/native: %v", err)
	}
	found := 0
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		if !trayBuildIncludes(t, filepath.Join("../internal/tray/native", name), goos) {
			continue
		}
		found++
		for _, symbol := range topLevelSymbols(t, filepath.Join("../internal/tray/native", name)) {
			declared[symbol] = true
		}
	}
	if found == 0 {
		t.Fatalf("no native shell file builds for %s, so the guard proves nothing", goos)
	}
	return declared
}

// trayBuildIncludes reports whether a file compiles when the tray is built for one OS.
//
// "unix" is answered from the two platforms this module supports rather than from a table:
// the darwin files need it, and a future plan9 shell would show up as a missing symbol in
// the surface test instead of quietly disappearing from here.
func trayBuildIncludes(t *testing.T, path, goos string) bool {
	t.Helper()
	line := buildConstraintOf(t, path)
	if line == "" {
		return true
	}
	expr, err := constraint.Parse(line)
	if err != nil {
		t.Fatalf("%s has an unparsable build constraint %q: %v", path, line, err)
	}
	return expr.Eval(func(tag string) bool {
		switch tag {
		case "tray":
			return true
		case "darwin", "windows", "linux":
			return tag == goos
		case "unix":
			return goos == "darwin" || goos == "linux"
		default:
			return false
		}
	})
}

// topLevelSymbols lists the exported functions and types declared by one file.
func topLevelSymbols(t *testing.T, path string) []string {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, parser.DeclarationErrors)
	if err != nil {
		t.Fatalf("%s does not parse: %v", path, err)
	}
	var symbols []string
	for _, declaration := range file.Decls {
		switch typed := declaration.(type) {
		case *ast.FuncDecl:
			if typed.Recv != nil || !typed.Name.IsExported() {
				continue
			}
			symbols = append(symbols, typed.Name.Name)
		case *ast.GenDecl:
			for _, spec := range typed.Specs {
				switch value := spec.(type) {
				case *ast.TypeSpec:
					if value.Name.IsExported() {
						symbols = append(symbols, value.Name.Name)
					}
				}
			}
		}
	}
	return symbols
}

// TestBothShellsExportTheSameSurface keeps the two tray implementations interchangeable.
//
// The macOS shell was written first, and a Windows shell that renamed or dropped one entry
// point would push a build-tag fork into cmd/tunnelmesh-client-tray. That fork is the
// expensive outcome: two entry points drift, and the platform nobody tests locally is the
// one that ships broken.
func TestBothShellsExportTheSameSurface(t *testing.T) {
	darwin, windows := buildConstraintsFor(t, "darwin"), buildConstraintsFor(t, "windows")
	for _, symbol := range append(append([]string{}, shellAPISurface...), shellAPITypes...) {
		for platform, declared := range map[string]map[string]bool{"macOS": darwin, "Windows": windows} {
			if !declared[symbol] {
				t.Errorf("the %s tray shell does not declare %s; cmd/tunnelmesh-client-tray calls it on both platforms", platform, symbol)
			}
		}
	}
}

// TestWindowsShellStaysPureGo protects the cross-compilation the release matrix depends on.
//
// A cgo file in this package would not break a macOS build, so no compile on the machines
// that run the tests would notice. It would break the Windows tray entirely: the packaging
// job builds GOOS=windows with CGO_ENABLED=0 from a Linux runner, and a Windows shell that
// needed a C toolchain could not be produced there at all.
func TestWindowsShellStaysPureGo(t *testing.T) {
	fset := token.NewFileSet()
	checked := 0
	entries, err := os.ReadDir("../internal/tray/native")
	if err != nil {
		t.Fatalf("read internal/tray/native: %v", err)
	}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || !strings.HasSuffix(name, "_windows.go") {
			continue
		}
		path := filepath.Join("../internal/tray/native", name)
		checked++
		if builtWithoutTrayTag(t, path) {
			t.Errorf("%s builds without the tray tag, so a plain ./... would need WebView2; constraint is %q",
				path, buildConstraintOf(t, path))
		}
		if cgo, _ := importsC(t, fset, path); cgo {
			t.Errorf("%s uses cgo; the Windows tray must cross-compile with CGO_ENABLED=0", path)
		}
	}
	if checked == 0 {
		t.Fatal("internal/tray/native has no Windows shell files, so the guard proves nothing")
	}
}

// TestNoCompiledWindowsResourcesAreCommitted keeps generated .syso files out of the module.
//
// go-winres writes the version resource and the exe icon beside the command before the
// Windows build, and a committed artifact would be a binary nobody can re-create: the whole
// point of generating it is that it comes out of deploy/windows/winres.json and the version
// the release job passes in.
func TestNoCompiledWindowsResourcesAreCommitted(t *testing.T) {
	walkModule(t, func(path string, entry fs.DirEntry) {
		if strings.HasSuffix(entry.Name(), ".syso") {
			t.Errorf("%s is a generated Windows resource; build it with scripts/package-windows-tray.sh instead of committing it", path)
		}
	})
}
