package scripts

import (
	"os"
	"strings"
	"testing"
)

func readInstallScript(t *testing.T) string {
	t.Helper()

	data, err := os.ReadFile("install.sh")
	if err != nil {
		t.Fatalf("read install.sh: %v", err)
	}
	return string(data)
}

func TestInstallScriptContract(t *testing.T) {
	script := readInstallScript(t)
	required := []string{
		"set -euo pipefail",
		"--version",
		"--install-dir",
		`"${HOME}/.local/bin"`,
		"releases",
		"/download/",
		"SHA256SUMS",
		"mktemp -d",
		"trap",
		"tunnelmesh-server",
		"tunnelmesh-agent",
		"tunnelmesh-client",
	}

	for _, want := range required {
		if !strings.Contains(script, want) {
			t.Errorf("install.sh is missing required contract %q", want)
		}
	}
}

func TestInstallScriptVerifiesReleaseChecksum(t *testing.T) {
	script := readInstallScript(t)
	required := []string{
		"curl --fail --silent --show-error --location",
		"sha256sum",
		"shasum -a 256",
		"expected_checksum",
		"checksum mismatch",
	}

	for _, want := range required {
		if !strings.Contains(script, want) {
			t.Errorf("install.sh is missing checksum requirement %q", want)
		}
	}
}

func TestInstallScriptSupportsOnlyReleasePlatforms(t *testing.T) {
	script := readInstallScript(t)
	required := []string{
		`GOOS="linux"`,
		`GOOS="darwin"`,
		`GOARCH="amd64"`,
		`GOARCH="arm64"`,
		"unsupported operating system",
		"unsupported architecture",
	}

	for _, want := range required {
		if !strings.Contains(script, want) {
			t.Errorf("install.sh is missing platform requirement %q", want)
		}
	}
}

func TestInstallScriptSupportsSafetyModes(t *testing.T) {
	script := readInstallScript(t)
	required := []string{
		"--dry-run",
		"--print-checksum",
		"dry-run: no network request or file write will be performed",
		"verified checksum:",
	}

	for _, want := range required {
		if !strings.Contains(script, want) {
			t.Errorf("install.sh is missing safety-mode requirement %q", want)
		}
	}
}
