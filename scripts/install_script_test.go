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

// TestBuildReleaseShipsOneClickInstallers 守护发布归档布局：一键安装脚本与 user 模式
// 单元模板必须进归档（安装时模板取自已校验的归档，才能保证与二进制同版本），
// 而 *_test.go 与 testdata/ 是仓库自检与 fixture，运维侧的归档里没有 go.mod，不能发布。
func TestBuildReleaseShipsOneClickInstallers(t *testing.T) {
	data, err := os.ReadFile("build-release.sh")
	if err != nil {
		t.Fatalf("read build-release.sh: %v", err)
	}
	script := string(data)
	for _, want := range []string{
		"deploy/install",      // oneclick/ 随 deploy/install 一起进归档
		"deploy/systemd-user", // user 模式单元模板必须进 Linux 归档
		`-name '*_test.go'`,   // 既有：测试文件不发布
		"-name testdata",      // 新增：测试 fixture 不发布
	} {
		if !strings.Contains(script, want) {
			t.Errorf("build-release.sh is missing %q", want)
		}
	}
}
