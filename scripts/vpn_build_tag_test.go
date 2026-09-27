package scripts

import (
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// heavyVPNModules are the two dependencies ADR 0002 confines to the "vpn" build
// tag. The rule is structural rather than stylistic: a Server binary built
// without the tag must not link a userspace TCP/IP stack or a WireGuard
// implementation, because every deployment that never enables the gateway would
// otherwise pay for them in binary size and in CVE surface.
var heavyVPNModules = []string{
	"gvisor.dev/gvisor",
	"golang.zx2c4.com/wireguard",
}

// buildConstraintOf returns the //go:build line that appears before the package
// clause, or "" when the file has none. Only the leading constraint matters: a
// tag mentioned in a comment further down grants nothing.
func buildConstraintOf(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	for _, line := range strings.Split(string(data), "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "package ") {
			return ""
		}
		if strings.HasPrefix(trimmed, "//go:build ") {
			return trimmed
		}
	}
	return ""
}

// TestHeavyVPNDependenciesAreBuildTagIsolated walks every Go file in the module
// and fails when one imports a heavy VPN dependency without carrying the tag.
//
// Scanning sources rather than running "go list -deps" keeps the guard fast
// enough to live in the default test run, and it is sufficient: the linker only
// reaches a package through an import edge, so an untagged file that does not
// import them cannot pull them into the default binary.
func TestHeavyVPNDependenciesAreBuildTagIsolated(t *testing.T) {
	fset := token.NewFileSet()
	checked := 0
	err := filepath.WalkDir("..", func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			name := entry.Name()
			if name == ".git" || name == "node_modules" || name == "web_dist" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		// The Task 0 spike is a separate module that pins these dependencies on
		// purpose and is never linked into a release binary.
		if strings.Contains(filepath.ToSlash(path), "test/spike/") {
			return nil
		}
		checked++
		file, parseErr := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
		if parseErr != nil {
			t.Errorf("%s does not parse: %v", path, parseErr)
			return nil
		}
		for _, importSpec := range file.Imports {
			importPath := strings.Trim(importSpec.Path.Value, `"`)
			for _, module := range heavyVPNModules {
				if importPath != module && !strings.HasPrefix(importPath, module+"/") {
					continue
				}
				constraint := buildConstraintOf(t, path)
				if constraint != "//go:build vpn" {
					t.Errorf("%s imports %s but its build constraint is %q, want %q",
						path, importPath, constraint, "//go:build vpn")
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk the module: %v", err)
	}
	if checked == 0 {
		t.Fatal("no Go files were checked, so the guard proves nothing")
	}
}
