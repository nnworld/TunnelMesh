package oneclick

import (
	"bytes"
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

// shellEntries 是三个角色的入口脚本；commonLib 是它们唯一的共享实现来源。
var shellEntries = []string{"install-server.sh", "install-agent.sh", "install-client.sh"}

const commonLib = "tunnelmesh-install-common.sh"

func bashPath(t *testing.T) string {
	t.Helper()
	p, err := exec.LookPath("bash")
	if err != nil {
		t.Skipf("bash not available: %v", err)
	}
	return p
}

func readFile(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile(name)
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return string(data)
}

func TestShellFilesExistAndAreExecutable(t *testing.T) {
	for _, name := range append(append([]string{}, shellEntries...), commonLib) {
		info, err := os.Stat(name)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if info.Mode().Perm()&0o111 == 0 {
			t.Errorf("%s is not executable (mode %v)", name, info.Mode())
		}
	}
}

func TestShellSyntax(t *testing.T) {
	bash := bashPath(t)
	names := append(append([]string{}, shellEntries...), commonLib, filepath.Join("testdata", "run_tests.sh"))
	for _, name := range names {
		cmd := exec.Command(bash, "-n", name)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Errorf("bash -n %s: %v\n%s", name, err, out)
		}
	}
}

// bashTimeout 是任何测试驱动 bash 脚本的硬上限。共享库的 tm_ask* 只要探测到输入通道就会
// 真的去读，而测试里没有人在另一头回答；没有这个上限，一次意外的交互提示会把整个 go test
// 挂到包级超时（默认 10m）才失败，panic 堆栈还指不出是哪个脚本卡住了。
const bashTimeout = 3 * time.Minute

// hermeticSuiteTimeout 是函数级套件的更紧上限：它不联网、不 go build、不注册服务，
// 实测 1-4s 跑完，60s 已经足够宽裕，同时能把回归的代价从 10m 压到 1m。
const hermeticSuiteTimeout = 60 * time.Second

// bashWaitDelay 是杀掉脚本之后等待其子进程释放输出管道的宽限期。stdout/stderr 是
// bytes.Buffer 而不是 *os.File，os/exec 会自建管道并起 goroutine 转发，Wait 必须等到
// 管道写端全部关闭才返回；脚本里 $(...) 命令替换留下的子 shell 会继承写端，只杀直接子
// 进程会让 Wait 永久阻塞——那正好是本helper 想要避免的挂死。WaitDelay 到点后 os/exec
// 主动关闭管道并返回，被孤立的子 shell 再写就收到 SIGPIPE 自行退出。
const bashWaitDelay = 5 * time.Second

// runBash 在硬上限内运行一个测试驱动的 bash 脚本，分别返回 stdout 与 stderr。
// stdin 为 nil 时 os/exec 会把它接到 /dev/null，脚本里的 read 立刻拿到 EOF。
// 超时会杀掉脚本并以可定位的信息失败，而不是让 go test 自己超时后只留下一份 goroutine dump。
func runBash(t *testing.T, bash string, env []string, stdin io.Reader, timeout time.Duration, script string, args ...string) (string, string, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, bash, append([]string{script}, args...)...)
	cmd.Env = env
	cmd.Stdin = stdin
	cmd.WaitDelay = bashWaitDelay
	var out, errBuf bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errBuf
	err := cmd.Run()
	if ctx.Err() == context.DeadlineExceeded {
		t.Fatalf("%s timed out after %s: 脚本极可能停在 tm_ask*/tm_read_line 的交互提示上。"+
			"测试驱动的脚本必须是非交互的——切断 stdin、置空 TM_TTY、预置答案或传 --yes。\n"+
			"stdout:\n%s\nstderr:\n%s", script, timeout, out.String(), errBuf.String())
	}
	return out.String(), errBuf.String(), err
}

func TestShellFunctionSuite(t *testing.T) {
	bash := bashPath(t)
	out, errBuf, err := runBash(t, bash,
		append(os.Environ(), "TM_ONECLICK_LIB="+commonLib),
		nil, hermeticSuiteTimeout, filepath.Join("testdata", "run_tests.sh"))
	t.Logf("run_tests.sh output:\n%s%s", out, errBuf)
	if err != nil {
		t.Fatalf("function suite failed: %v", err)
	}
}

// TestShellFunctionSuiteNeverReadsTheCallersStdin 锁死一个真实的挂死缺陷。
//
// run_tests.sh 里 tm_validate_listen 会走到 tm_ask_choice，而共享库的 tm_read_line 在
// TM_TTY 为空时回落到 stdin。脚本自己从不切断 stdin，于是只要调用方给的 stdin 是「打开着
// 但永远没有数据」的通道——交互终端、带 PTY 的 go test、CI 里挂着的管道——read 就永久
// 阻塞，套件要等到包级 10m 超时才失败。
//
// 这里用一条永不写入也永不关闭的 os.Pipe 精确复现该环境，因此无论本机有没有控制终端都能
// 稳定判定：修复前挂在 60s 上限，修复后脚本开头的 exec </dev/null 让它拿不到这条管道。
func TestShellFunctionSuiteNeverReadsTheCallersStdin(t *testing.T) {
	bash := bashPath(t)
	silentRead, silentWrite, err := os.Pipe()
	if err != nil {
		t.Fatalf("create a silent pipe: %v", err)
	}
	// 写端必须在整个运行期间保持打开：一旦关闭，读端立刻 EOF，测试就会假绿。
	t.Cleanup(func() { silentWrite.Close(); silentRead.Close() })

	out, errBuf, err := runBash(t, bash,
		append(os.Environ(), "TM_ONECLICK_LIB="+commonLib),
		silentRead, hermeticSuiteTimeout, filepath.Join("testdata", "run_tests.sh"))
	if err != nil {
		t.Fatalf("function suite must not touch the caller's stdin: %v\nstdout:\n%s\nstderr:\n%s",
			err, out, errBuf)
	}
}

func TestShellScriptsUseStrictMode(t *testing.T) {
	for _, name := range append(append([]string{}, shellEntries...), commonLib) {
		body := readFile(t, name)
		for _, want := range []string{"set -euo pipefail", "#!/usr/bin/env bash"} {
			if !strings.Contains(body, want) {
				t.Errorf("%s is missing %q", name, want)
			}
		}
	}
}

// TestNoHardcodedSecrets 扫描脚本里的字面量秘密。键名允许出现，
// 但不允许出现「键 = 非空字面量」形式的赋值。
func TestNoHardcodedSecrets(t *testing.T) {
	pattern := regexp.MustCompile(`(?i)(token|secret|password|passwd|dsn|private_key)[\"']?\s*[:=]\s*[\"']?[A-Za-z0-9+/_\-]{8,}`)
	allow := regexp.MustCompile(`(?i)(token|secret|password|dsn)_?(file|env|key_id|s)\b`)
	files := append(append(append([]string{}, shellEntries...), commonLib), psEntries...)
	files = append(files, psCommon)
	for _, name := range files {
		for i, line := range strings.Split(readFile(t, name), "\n") {
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "#") {
				continue
			}
			if pattern.MatchString(line) && !allow.MatchString(line) {
				t.Errorf("%s:%d looks like a hardcoded secret: %s", name, i+1, trimmed)
			}
		}
	}
}

// TestReleaseContractMatchesLegacyInstaller 保证一键安装库与 scripts/install.sh
// 共享同一份 Release 契约，避免归档命名或 SHA256SUMS 格式悄悄分叉。
func TestReleaseContractMatchesLegacyInstaller(t *testing.T) {
	legacy := readFile(t, filepath.Join("..", "..", "..", "scripts", "install.sh"))
	lib := readFile(t, commonLib)
	for _, want := range []string{"nnworld/TunnelMesh", "/releases", "/download/", "SHA256SUMS", "releases/latest"} {
		if !strings.Contains(legacy, want) {
			t.Errorf("scripts/install.sh lost contract fragment %q", want)
		}
		if !strings.Contains(lib, want) {
			t.Errorf("%s lost contract fragment %q", commonLib, want)
		}
	}
	for _, want := range []string{`grep "  ${name}\$"`, "sha256sum", "shasum -a 256", "tunnelmesh-%s-%s-%s.%s"} {
		if !strings.Contains(lib, want) {
			t.Errorf("%s is missing contract requirement %q", commonLib, want)
		}
	}
}

// TestCommonLibIsBash32Compatible 守护 macOS /bin/bash 3.2 兼容性：
// 用户会照抄 Homebrew 的 `/bin/bash -c "$(curl ...)"`，而 macOS 自带 bash 是 3.2.57。
func TestCommonLibIsBash32Compatible(t *testing.T) {
	lib := readFile(t, commonLib)
	forbidden := []string{"declare -A", "mapfile", "readarray", "|&", "&>>", "declare -n"}
	for _, bad := range forbidden {
		if strings.Contains(lib, bad) {
			t.Errorf("%s uses bash 4+ feature %q; macOS /bin/bash is 3.2", commonLib, bad)
		}
	}
}

// stripBootstrap 去掉三个入口里必须重复的引导片段：定位共享库这件事本身
// 无法放进共享库（鸡生蛋），因此允许重复，但要求逐字节一致（见下条测试）。
func stripBootstrap(body string) string {
	const begin = "# --- bootstrap-begin ---"
	const end = "# --- bootstrap-end ---"
	i := strings.Index(body, begin)
	j := strings.Index(body, end)
	if i < 0 || j < 0 || j < i {
		return body
	}
	return body[:i] + body[j+len(end):]
}

// TestEntryBootstrapIsIdentical 保证三份入口的引导片段不漂移。
func TestEntryBootstrapIsIdentical(t *testing.T) {
	const begin = "# --- bootstrap-begin ---"
	const end = "# --- bootstrap-end ---"
	var want string
	for _, name := range shellEntries {
		body := readFile(t, name)
		i, j := strings.Index(body, begin), strings.Index(body, end)
		if i < 0 || j < 0 {
			t.Fatalf("%s is missing the bootstrap markers", name)
		}
		block := body[i : j+len(end)]
		if want == "" {
			want = block
			continue
		}
		if block != want {
			t.Errorf("%s bootstrap block differs from %s; keep all three byte-identical", name, shellEntries[0])
		}
	}
	for _, frag := range []string{"tm_load_lib", "TM_ONECLICK_REF", "tunnelmesh-install-common.sh"} {
		if !strings.Contains(want, frag) {
			t.Errorf("bootstrap block lost %q", frag)
		}
	}
}

// TestEntryScriptsStayThin 强制 DRY：入口脚本不得自带下载、校验或渲染逻辑，
// 只能通过共享库完成，否则三份实现会随时间漂移。
func TestEntryScriptsStayThin(t *testing.T) {
	forbidden := []string{"SHA256SUMS", "releases/download", "api.github.com", "shasum", "sha256sum"}
	for _, name := range shellEntries {
		body := stripBootstrap(readFile(t, name))
		for _, bad := range forbidden {
			if strings.Contains(body, bad) {
				t.Errorf("%s must not implement %q inline; put it in %s", name, bad, commonLib)
			}
		}
		// tm_load_lib 与 set -euo pipefail 属于引导片段本身（定位共享库这件事无法放进共享库），
		// 因此对完整正文断言；角色钩子必须在引导片段之外，才对 stripBootstrap 后的正文断言。
		for _, want := range []string{"tm_load_lib", "set -euo pipefail"} {
			if !strings.Contains(readFile(t, name), want) {
				t.Errorf("%s is missing required fragment %q", name, want)
			}
		}
		for _, want := range []string{"tm_main \"$@\"", "tm_role_prompts", "tm_role_render_config", "tm_role_post_install"} {
			if !strings.Contains(body, want) {
				t.Errorf("%s is missing required hook %q", name, want)
			}
		}
	}
}

func TestUsageDocumentsFlagsAndInvocationForms(t *testing.T) {
	lib := readFile(t, commonLib)
	flags := []string{"--version", "--yes", "--uninstall", "--mode", "--user", "--run-user", "--bin-dir",
		"--config-dir", "--state-dir", "--archive", "--base-url", "--raw-base-url", "--no-service",
		"--no-start", "--no-enable", "--no-linger", "--no-cache", "--keep-config", "--reconfigure",
		"--token-file", "--secret-env-file", "--server-url", "--agent-id", "--tunnel"}
	for _, f := range flags {
		if !strings.Contains(lib, f) {
			t.Errorf("%s does not document or parse %s", commonLib, f)
		}
	}
	for _, form := range []string{`bash -c "$(curl`, `| bash -s --`, `TM_ONECLICK_YES=1`, `TM_ONECLICK_REF`, `/dev/tty`} {
		if !strings.Contains(lib, form) {
			t.Errorf("%s lost invocation-form support for %q", commonLib, form)
		}
	}
}

func TestUninstallKeepsConfiguration(t *testing.T) {
	lib := readFile(t, commonLib)
	if !strings.Contains(lib, "tm_uninstall") {
		t.Fatal("missing tm_uninstall")
	}
	// 卸载必须保留配置与数据：不允许出现删除配置目录的实现。
	for _, bad := range []string{"rm -rf /etc/tunnelmesh", "rm -rf \"$HOME/.config/tunnelmesh\"", "rm -rf \"$(tm_default_config_dir)\""} {
		if strings.Contains(lib, bad) {
			t.Errorf("%s must not delete configuration on uninstall (%q)", commonLib, bad)
		}
	}
}

var psEntries = []string{"install-server.ps1", "install-agent.ps1", "install-client.ps1"}

const psCommon = "tunnelmesh-install-common.ps1"

func TestPowerShellFilesExist(t *testing.T) {
	for _, name := range append(append([]string{}, psEntries...), psCommon, "winsw-checksums.txt") {
		if _, err := os.Stat(name); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
	}
}

func TestPowerShellContract(t *testing.T) {
	common := readFile(t, psCommon)
	for _, want := range []string{
		"$ErrorActionPreference = 'Stop'",
		"Get-FileHash", "SHA256", "SHA256SUMS",
		"Resolve-TmVersion", "Test-TmChecksum", "Get-TmWinSW", "Invoke-TmMain",
		"Read-TmSecret", "-AsSecureString",
		"releases/latest", "tag_name",
		"__ENV_BLOCK__", `..\windows\tunnelmesh-service.xml`,
	} {
		if !strings.Contains(common, want) {
			t.Errorf("%s is missing %q", psCommon, want)
		}
	}
	for _, name := range psEntries {
		body := readFile(t, name)
		for _, want := range []string{
			". $PSScriptRoot\\tunnelmesh-install-common.ps1",
			"Invoke-TmMain", "[switch]$Yes", "[switch]$Uninstall", "[string]$Version",
			"[string]$TokenFile", "[string]$SecretEnvFile", "[string[]]$Tunnel",
		} {
			if !strings.Contains(body, want) {
				t.Errorf("%s is missing %q", name, want)
			}
		}
		// 不得提供明文 token 参数：命令行参数会进 PowerShell 历史与进程列表。
		// 用 \b 而不是子串匹配，否则合法的 [string]$TokenFile 会被误判成明文 -Token。
		if regexp.MustCompile(`\[string\]\$Token\b`).MatchString(body) {
			t.Errorf("%s must not accept a plaintext -Token parameter", name)
		}
	}
}

// TestPowerShellEntryBootstrapIsIdentical 保证三份 .ps1 入口的引导片段不漂移，
// 与 .sh 侧的 TestEntryBootstrapIsIdentical 同构：定位共享模块这件事本身无法放进
// 共享模块（鸡生蛋），因此允许重复，但必须逐字节一致。
func TestPowerShellEntryBootstrapIsIdentical(t *testing.T) {
	const begin = "# --- bootstrap-begin ---"
	const end = "# --- bootstrap-end ---"
	var want string
	for _, name := range psEntries {
		body := readFile(t, name)
		i, j := strings.Index(body, begin), strings.Index(body, end)
		if i < 0 || j < 0 {
			t.Fatalf("%s is missing the bootstrap markers", name)
		}
		block := body[i : j+len(end)]
		if want == "" {
			want = block
			continue
		}
		if block != want {
			t.Errorf("%s bootstrap block differs from %s; keep all three byte-identical", name, psEntries[0])
		}
	}
	// 引导片段必须同时取共享模块与 WinSW 校验和表：irm 单独下载入口时，
	// 共享模块被放到临时目录，Get-TmWinSW 只能在同目录找到 winsw-checksums.txt。
	for _, frag := range []string{psCommon, "winsw-checksums.txt", "TM_ONECLICK_REF", "$PSScriptRoot"} {
		if !strings.Contains(want, frag) {
			t.Errorf("PowerShell bootstrap block lost %q", frag)
		}
	}
}

// TestPowerShellEntriesStayThin 强制 DRY：入口不得自带下载、校验或渲染逻辑，
// 只能通过共享模块完成，否则三份实现会随时间漂移。
func TestPowerShellEntriesStayThin(t *testing.T) {
	forbidden := []string{"SHA256SUMS", "releases/download", "api.github.com", "Get-FileHash", "Expand-Archive", "Write-TmYaml"}
	roleOf := regexp.MustCompile(`Invoke-TmMain -Role ([a-z]+) -Bound \$PSBoundParameters`)
	for _, name := range psEntries {
		body := stripBootstrap(readFile(t, name))
		for _, bad := range forbidden {
			if strings.Contains(body, bad) {
				t.Errorf("%s must not implement %q inline; put it in %s", name, bad, psCommon)
			}
		}
		m := roleOf.FindStringSubmatch(body)
		if m == nil {
			t.Fatalf("%s must delegate to Invoke-TmMain -Role <role> -Bound $PSBoundParameters", name)
		}
		if want := "install-" + m[1] + ".ps1"; name != want {
			t.Errorf("%s installs role %q; expected the entry to be named %s", name, m[1], want)
		}
	}
}

// TestPowerShellEntryHelp 要求每个入口自带 comment-based help：Windows 用户靠
// Get-Help / -? 查参数，等价于 .sh 侧的 --help，且必须指向同一份用户文档。
func TestPowerShellEntryHelp(t *testing.T) {
	for _, name := range psEntries {
		body := readFile(t, name)
		for _, want := range []string{".SYNOPSIS", ".DESCRIPTION", ".EXAMPLE", "docs/deployment/oneclick-install.md", "docs/deployment/windows-service.md"} {
			if !strings.Contains(body, want) {
				t.Errorf("%s is missing help section %q", name, want)
			}
		}
		// 帮助示例里也不得出现明文 token 传参，否则用户会照抄。
		if regexp.MustCompile(`-Token\s+\S`).MatchString(body) {
			t.Errorf("%s help/params must not show a plaintext -Token value", name)
		}
	}
}

func TestWinSWChecksumPolicy(t *testing.T) {
	body := readFile(t, "winsw-checksums.txt")
	for _, want := range []string{"# 格式：", "shasum -a 256", "WinSW-x64.exe"} {
		if !strings.Contains(body, want) {
			t.Errorf("winsw-checksums.txt is missing %q", want)
		}
	}
	// 数据行必须是 4 列且 sha256 为 64 位十六进制，杜绝占位符被当成校验和。
	for i, line := range strings.Split(body, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		fields := strings.Fields(trimmed)
		if len(fields) != 4 {
			t.Fatalf("winsw-checksums.txt:%d must have 4 fields, got %d", i+1, len(fields))
		}
		if !regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(fields[2]) {
			t.Errorf("winsw-checksums.txt:%d has an invalid sha256 %q", i+1, fields[2])
		}
	}
	common := readFile(t, psCommon)
	if !strings.Contains(common, "winsw-checksums.txt") {
		t.Errorf("%s must consult winsw-checksums.txt before downloading WinSW", psCommon)
	}
}
