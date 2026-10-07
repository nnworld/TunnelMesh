package scripts

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// mergeScript is the hand-off between the macOS tray job and the cross-platform release
// directory. build-release.sh cross-compiles three binaries with CGO_ENABLED=0 and can
// never produce the tray, which links Cocoa and WebKit through cgo; the release workflow
// packages it on a macOS runner and this helper folds the disk images into the same
// release directory and the same SHA256SUMS.
const mergeScript = "merge-tray-dist.sh"

// releaseScriptPath is read by the tests that pin how build-release.sh uses the helper.
const releaseScriptPath = "build-release.sh"

// checksumCommand mirrors the sha256sum/shasum fallback both packaging scripts use, so the
// tests verify the merged result with a tool the release actually ships with.
func checksumCommand(t *testing.T) []string {
	t.Helper()
	if _, err := exec.LookPath("sha256sum"); err == nil {
		return []string{"sha256sum"}
	}
	if _, err := exec.LookPath("shasum"); err == nil {
		return []string{"shasum", "-a", "256"}
	}
	t.Skip("neither sha256sum nor shasum is available")
	return nil
}

// runMergeScript executes the helper the way build-release.sh does: absolute paths, no
// inherited environment, combined output so a failure message can be asserted on.
func runMergeScript(t *testing.T, args ...string) (string, error) {
	t.Helper()
	cmd := exec.Command("bash", append([]string{mergeScript}, args...)...)
	var combined strings.Builder
	cmd.Stdout = &combined
	cmd.Stderr = &combined
	err := cmd.Run()
	return combined.String(), err
}

// sha256Of computes a digest in Go so fixtures do not depend on which checksum tool the
// host happens to have.
func sha256Of(t *testing.T, data []byte) string {
	t.Helper()
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// writeTrayFixture builds a directory shaped like the output of
// scripts/package-macos-tray.sh: one disk image per architecture, their SHA256SUMS and a
// manifest.json. Only SHA256SUMS and manifest.json are optional per caller, so the
// incomplete-hand-off cases can omit exactly one thing.
func writeTrayFixture(t *testing.T, dir string, images []string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	var lines []string
	for _, name := range images {
		// The name is also the content, so a merge that swapped two images cannot still
		// verify against the published checksums.
		body := []byte("tunnelmesh tray disk image " + name + "\n")
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, body, 0o644); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
		lines = append(lines, sha256Of(t, body)+"  "+name)
	}
	if len(lines) > 0 {
		writeOrFail(t, filepath.Join(dir, "SHA256SUMS"), strings.Join(lines, "\n")+"\n")
	}
	writeOrFail(t, filepath.Join(dir, "manifest.json"), `{"version":"v0.0.0-test"}`+"\n")
}

func writeOrFail(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// writeReleaseFixture builds a release directory that already holds one platform archive,
// which is the state build-release.sh is in when it calls the helper.
func writeReleaseFixture(t *testing.T, dir string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	archive := "tunnelmesh-v0.0.0-test-linux-amd64.tar.gz"
	body := []byte("tunnelmesh linux amd64 archive\n")
	writeOrFail(t, filepath.Join(dir, archive), string(body))
	writeOrFail(t, filepath.Join(dir, "SHA256SUMS"), sha256Of(t, body)+"  "+archive+"\n")
	return archive
}

// verifyChecksums runs the same check an operator runs on a downloaded release.
func verifyChecksums(t *testing.T, dir string) string {
	t.Helper()
	checksum := checksumCommand(t)
	cmd := exec.Command(checksum[0], append(checksum[1:], "-c", "SHA256SUMS")...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%s -c SHA256SUMS in %s: %v\n%s", strings.Join(checksum, " "), dir, err, out)
	}
	return string(out)
}

func TestMergeTrayDistFoldsDiskImagesIntoTheReleaseDir(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "tray-dist")
	release := filepath.Join(root, "dist")
	images := []string{
		"tunnelmesh-client-tray-v0.0.0-test-darwin-arm64.dmg",
		"tunnelmesh-client-tray-v0.0.0-test-darwin-amd64.dmg",
	}
	writeTrayFixture(t, source, images)
	archive := writeReleaseFixture(t, release)

	out, err := runMergeScript(t, source, release)
	if err != nil {
		t.Fatalf("%s %s %s: %v\n%s", mergeScript, source, release, err, out)
	}

	for _, name := range images {
		merged, err := os.ReadFile(filepath.Join(release, name))
		if err != nil {
			t.Fatalf("read merged %s: %v", name, err)
		}
		original, err := os.ReadFile(filepath.Join(source, name))
		if err != nil {
			t.Fatalf("read source %s: %v", name, err)
		}
		if string(merged) != string(original) {
			t.Errorf("%s was copied with different contents", name)
		}
	}

	// One SHA256SUMS has to cover every published asset, or an operator verifying the
	// release silently skips the tray.
	lines := strings.Split(strings.TrimSpace(readOrFailPath(t, filepath.Join(release, "SHA256SUMS"))), "\n")
	if len(lines) != len(images)+1 {
		t.Errorf("merged SHA256SUMS has %d entries, want %d:\n%s", len(lines), len(images)+1, strings.Join(lines, "\n"))
	}
	for _, line := range lines {
		if !strings.Contains(line, archive) && !strings.HasSuffix(line, images[0]) && !strings.HasSuffix(line, images[1]) {
			t.Errorf("unexpected SHA256SUMS entry %q", line)
		}
	}
	verifyChecksums(t, release)

	// The tray manifest is published beside the cross-platform one, never over it:
	// manifest.json is the documented contract the admin Downloads page points at.
	trayManifest := readOrFailPath(t, filepath.Join(release, "manifest-tray.json"))
	if !strings.Contains(trayManifest, "v0.0.0-test") {
		t.Errorf("manifest-tray.json was not copied from the tray output: %s", trayManifest)
	}
	if _, err := os.Stat(filepath.Join(release, "manifest.json")); !os.IsNotExist(err) {
		t.Errorf("the merge must not create or overwrite manifest.json, got err=%v", err)
	}
}

// readOrFailPath loads a repository or fixture artifact.
func readOrFailPath(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}

// TestMergeTrayDistRefusesAnIncompleteHandOff covers every way the macOS job can hand over
// something that is not a finished tray build. Each one has to fail loudly: the alternative
// is a published release without its macOS client and no signal anywhere.
func TestMergeTrayDistRefusesAnIncompleteHandOff(t *testing.T) {
	cases := []struct {
		name  string
		build func(t *testing.T, dir string)
	}{
		{
			name: "missing directory",
			build: func(t *testing.T, dir string) {
				// deliberately left absent
			},
		},
		{
			name: "no disk image",
			build: func(t *testing.T, dir string) {
				writeTrayFixture(t, dir, nil)
			},
		},
		{
			name: "no checksums",
			build: func(t *testing.T, dir string) {
				writeTrayFixture(t, dir, []string{"tunnelmesh-client-tray-v0.0.0-test-darwin-arm64.dmg"})
				if err := os.Remove(filepath.Join(dir, "SHA256SUMS")); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "no manifest",
			build: func(t *testing.T, dir string) {
				writeTrayFixture(t, dir, []string{"tunnelmesh-client-tray-v0.0.0-test-darwin-arm64.dmg"})
				if err := os.Remove(filepath.Join(dir, "manifest.json")); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "corrupted image",
			build: func(t *testing.T, dir string) {
				name := "tunnelmesh-client-tray-v0.0.0-test-darwin-arm64.dmg"
				writeTrayFixture(t, dir, []string{name})
				// An artifact truncated or rewritten in transit still has the right name;
				// only the source checksums can catch it.
				writeOrFail(t, filepath.Join(dir, name), "truncated\n")
			},
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			root := t.TempDir()
			source := filepath.Join(root, "tray-dist")
			release := filepath.Join(root, "dist")
			testCase.build(t, source)
			archive := writeReleaseFixture(t, release)
			before := readOrFailPath(t, filepath.Join(release, "SHA256SUMS"))

			out, err := runMergeScript(t, source, release)
			if err == nil {
				t.Fatalf("%s accepted an incomplete hand-off (%s)\n%s", mergeScript, testCase.name, out)
			}
			if strings.TrimSpace(out) == "" {
				t.Errorf("%s failed without saying why", mergeScript)
			}
			if after := readOrFailPath(t, filepath.Join(release, "SHA256SUMS")); after != before {
				t.Errorf("a rejected merge modified SHA256SUMS:\nbefore: %s\nafter: %s", before, after)
			}
			entries, err := os.ReadDir(release)
			if err != nil {
				t.Fatal(err)
			}
			var names []string
			for _, entry := range entries {
				names = append(names, entry.Name())
			}
			if len(names) != 2 || names[0] != "SHA256SUMS" || names[1] != archive {
				t.Errorf("a rejected merge left %v in the release directory, want only [SHA256SUMS %s]", names, archive)
			}
		})
	}
}

// TestMergeTrayDistRefusesToOverwriteAnExistingAsset guards the collision case: two builds
// claiming the same asset name must not silently decide which one an operator downloads.
func TestMergeTrayDistRefusesToOverwriteAnExistingAsset(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "tray-dist")
	release := filepath.Join(root, "dist")
	image := "tunnelmesh-client-tray-v0.0.0-test-darwin-arm64.dmg"
	writeTrayFixture(t, source, []string{image})
	writeReleaseFixture(t, release)
	writeOrFail(t, filepath.Join(release, image), "a different build\n")

	out, err := runMergeScript(t, source, release)
	if err == nil {
		t.Fatalf("%s overwrote an existing asset\n%s", mergeScript, out)
	}
	if got := readOrFailPath(t, filepath.Join(release, image)); got != "a different build\n" {
		t.Errorf("the existing asset was modified: %q", got)
	}
	if !strings.Contains(out, image) {
		t.Errorf("the failure must name the conflicting asset, got: %s", out)
	}
}

// TestMergeTrayDistCheckModeValidatesWithoutWriting is what build-release.sh runs before it
// spends ten minutes cross-compiling eighteen binaries: a missing hand-off has to fail in
// seconds, not after the whole matrix is built.
func TestMergeTrayDistCheckModeValidatesWithoutWriting(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "tray-dist")
	writeTrayFixture(t, source, []string{"tunnelmesh-client-tray-v0.0.0-test-darwin-arm64.dmg"})

	if out, err := runMergeScript(t, "--check", source); err != nil {
		t.Fatalf("%s --check: %v\n%s", mergeScript, err, out)
	}
	entries, err := os.ReadDir(source)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 3 {
		t.Errorf("--check modified its source directory: %d entries", len(entries))
	}

	if _, err := runMergeScript(t, "--check", filepath.Join(root, "absent")); err == nil {
		t.Errorf("%s --check accepted a directory that does not exist", mergeScript)
	}
}

func TestMergeTrayDistRejectsBadArguments(t *testing.T) {
	if out, err := runMergeScript(t); err == nil {
		t.Errorf("%s without arguments must fail, got: %s", mergeScript, out)
	}
	if out, err := runMergeScript(t, "a", "b", "c"); err == nil {
		t.Errorf("%s with three arguments must fail, got: %s", mergeScript, out)
	}
	if out, err := runMergeScript(t, "--help"); err != nil {
		t.Errorf("%s --help: %v\n%s", mergeScript, err, out)
	}
}

// TestBuildReleaseScriptHandsTheTrayOff pins how the cross-platform release consumes the
// macOS artifacts. It never compiles the tray (deploy/macos/tray_bundle_test.go guards that
// from the bundle side); it validates the hand-off early, merges it late, and keeps one
// checksum file for everything it publishes.
func TestBuildReleaseScriptHandsTheTrayOff(t *testing.T) {
	script := readOrFailPath(t, releaseScriptPath)

	for _, want := range []string{
		"TRAY_DIST_DIR",
		mergeScript,
		"--check",
	} {
		if !strings.Contains(script, want) {
			t.Errorf("%s must reference %q", releaseScriptPath, want)
		}
	}
	// The helper is called twice on purpose: once to validate before anything is built, and
	// once to merge afterwards. A single call site means one of the two guarantees is gone.
	if got := strings.Count(script, mergeScript); got != 2 {
		t.Errorf("%s calls %s %d times, want 2 (validate early, merge after the matrix)", releaseScriptPath, mergeScript, got)
	}

	checkAt := strings.Index(script, mergeScript+"\" --check")
	loopAt := strings.Index(script, "for target in \"${targets[@]}\"")
	mergeAt := strings.LastIndex(script, mergeScript)
	if checkAt < 0 || loopAt < 0 || mergeAt < 0 {
		t.Fatalf("%s lost the tray hand-off: check=%d loop=%d merge=%d", releaseScriptPath, checkAt, loopAt, mergeAt)
	}
	if checkAt > loopAt {
		t.Errorf("%s validates TRAY_DIST_DIR after the build loop; a missing hand-off must fail before eighteen cross-compilations", releaseScriptPath)
	}
	if mergeAt < loopAt {
		t.Errorf("%s merges the tray before building the matrix", releaseScriptPath)
	}
}

// TestMergeTrayDistFoldsEveryArtifactShape covers the Windows hand-off: two green archives
// plus one installer, and a manifest published under its own name. The helper may not assume
// ".dmg", because a release that quietly drops the setup.exe and keeps only the zips is the
// same silent failure as a release with no macOS client at all.
func TestMergeTrayDistFoldsEveryArtifactShape(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "windows-tray-dist")
	release := filepath.Join(root, "dist")
	artifacts := []string{
		"TunnelMeshClient-v0.0.0-test-windows-amd64.zip",
		"TunnelMeshClient-v0.0.0-test-windows-arm64.zip",
		"TunnelMeshClient-v0.0.0-test-windows-amd64-setup.exe",
	}
	writeTrayFixture(t, source, artifacts)
	archive := writeReleaseFixture(t, release)

	out, err := runMergeScript(t, "--manifest-name=manifest-windows-tray.json", source, release)
	if err != nil {
		t.Fatalf("%s: %v\n%s", mergeScript, err, out)
	}
	for _, name := range artifacts {
		body, err := os.ReadFile(filepath.Join(release, name))
		if err != nil {
			t.Fatalf("read merged %s: %v", name, err)
		}
		original, err := os.ReadFile(filepath.Join(source, name))
		if err != nil {
			t.Fatalf("read source %s: %v", name, err)
		}
		if string(body) != string(original) {
			t.Errorf("%s was copied with different contents", name)
		}
	}
	lines := strings.Split(strings.TrimSpace(readOrFailPath(t, filepath.Join(release, "SHA256SUMS"))), "\n")
	if len(lines) != len(artifacts)+1 {
		t.Errorf("merged SHA256SUMS has %d entries, want %d:\n%s", len(lines), len(artifacts)+1, strings.Join(lines, "\n"))
	}
	verifyChecksums(t, release)

	named := readOrFailPath(t, filepath.Join(release, "manifest-windows-tray.json"))
	if !strings.Contains(named, "v0.0.0-test") {
		t.Errorf("manifest-windows-tray.json was not copied from the tray output: %s", named)
	}
	// Two platforms, two manifests: the macOS hand-off must stay reachable under its own
	// name, and neither may be mistaken for the cross-platform manifest.json.
	if _, err := os.Stat(filepath.Join(release, "manifest-tray.json")); !os.IsNotExist(err) {
		t.Errorf("--manifest-name must not also write manifest-tray.json")
	}
	if _, err := os.Stat(filepath.Join(release, "manifest.json")); !os.IsNotExist(err) {
		t.Errorf("the merge must not create or overwrite manifest.json, got err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(release, archive)); err != nil {
		t.Errorf("the existing platform archive disappeared: %v", err)
	}
}

// TestMergeTrayDistIgnoresBookkeepingAndStaging proves the merge copies published files only:
// a packaging script's staging directory has to stay out of the release, and its own
// SHA256SUMS must not be flattened next to the release-wide one.
func TestMergeTrayDistIgnoresBookkeepingAndStaging(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "tray-dist")
	release := filepath.Join(root, "dist")
	artifact := "TunnelMeshClient-v0.0.0-test-windows-amd64.zip"
	writeTrayFixture(t, source, []string{artifact})
	if err := os.MkdirAll(filepath.Join(source, ".build"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeOrFail(t, filepath.Join(source, ".build", "leftover"), "staged bytes\n")
	writeOrFail(t, filepath.Join(source, "build.log"), "noise\n")
	writeReleaseFixture(t, release)

	if out, err := runMergeScript(t, source, release); err != nil {
		t.Fatalf("%s: %v\n%s", mergeScript, err, out)
	}
	for _, unwanted := range []string{"build.log", "SHA256SUMS.orig", ".build"} {
		if _, err := os.Stat(filepath.Join(release, unwanted)); err == nil {
			t.Errorf("%s copied %s into the release directory", mergeScript, unwanted)
		}
	}
	entries, err := os.ReadDir(release)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 4 {
		var names []string
		for _, entry := range entries {
			names = append(names, entry.Name())
		}
		t.Errorf("release directory holds %v, want the platform archive, SHA256SUMS, the artifact and manifest-tray.json", names)
	}
}
