package oneclick

import (
	"archive/tar"
	"compress/gzip"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// 端到端冒烟：用 go build 出的真实二进制打包成发布归档，再以 --archive 走完整流程。
// 服务管理命令由 testdata/bin 下的 stub 顶替，因此不会真的注册系统服务。
// 与 test/e2e/proxy-entry 一样用环境变量门控：TM_ONECLICK_E2E=1 才执行。
func TestOneClickInstallUpgradeUninstall(t *testing.T) {
	if os.Getenv("TM_ONECLICK_E2E") != "1" {
		t.Skip("set TM_ONECLICK_E2E=1 to run the one-click installer end-to-end smoke test")
	}
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skipf("bash not available: %v", err)
	}

	root := t.TempDir() // 假的 $HOME
	work := t.TempDir() // 构建与归档
	stubs := filepath.Join("testdata", "bin")
	absStubs, err := filepath.Abs(stubs)
	if err != nil {
		t.Fatalf("abs stubs: %v", err)
	}

	// 1) 真实二进制
	binary := filepath.Join(work, "tunnelmesh-agent")
	// 本包在 deploy/install/oneclick 下，仓库根是 ../../..；服务模板在 deploy/ 下，是 ../../。
	build := exec.Command("go", "build", "-o", binary, "../../../cmd/tunnelmesh-agent")
	build.Env = append(os.Environ(), "CGO_ENABLED=0")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build agent: %v\n%s", err, out)
	}

	// 2) 打包成与发布归档同构的 tar.gz（含服务模板）
	archive := filepath.Join(work, "tunnelmesh-v9.9.9-e2e.tar.gz")
	packArchive(t, archive, map[string]string{
		"tunnelmesh-agent": binary,
		"deploy/systemd-user/tunnelmesh-agent.service": filepath.Join("..", "..", "systemd-user", "tunnelmesh-agent.service"),
		"deploy/systemd/tunnelmesh-agent.service":      filepath.Join("..", "..", "systemd", "tunnelmesh-agent.service"),
		"deploy/macos/tunnelmesh.plist":                filepath.Join("..", "..", "macos", "tunnelmesh.plist"),
	})

	// 3) 安装
	svcLog := filepath.Join(work, "svc.log")
	env := []string{
		"HOME=" + root,
		"PATH=" + absStubs + string(os.PathListSeparator) + os.Getenv("PATH"),
		"SVC_STUB_LOG=" + svcLog,
		"TM_ONECLICK_ALLOW_STDIN=1",
		"TM_ONECLICK_SERVICE_MANAGER=" + expectedManager(),
		"TM_CACHE_DIR=" + filepath.Join(work, "cache"),
		"TUNNELMESH_AGENT_TOKEN=e2e-secret-token-value",
		"TM_ONECLICK_LIB=" + absLib(t),
	}
	install := runInstaller(t, bash, env, "--archive", archive, "--version", "v9.9.9", "--yes",
		"--server-url", "wss://tunnel.example.com/ws/agent", "--agent-id", "agent-e2e", "--no-linger")

	// 4) 断言落位与内容
	assertFileMode(t, filepath.Join(root, ".local", "bin", "tunnelmesh-agent"), 0o755)
	yamlBody := assertFileMode(t, filepath.Join(root, ".config", "tunnelmesh", "agent.yaml"), 0o600)
	for _, want := range []string{"wss://tunnel.example.com/ws/agent", "agent-e2e", "server_url", "connections"} {
		if !strings.Contains(yamlBody, want) {
			t.Errorf("agent.yaml missing %q:\n%s", want, yamlBody)
		}
	}
	if strings.Contains(yamlBody, "e2e-secret-token-value") {
		t.Errorf("token leaked into YAML:\n%s", yamlBody)
	}
	envBody := assertFileMode(t, filepath.Join(root, ".config", "tunnelmesh", "agent.env"), 0o600)
	// env 文件用双引号包裹（见 tm_env_quote）：systemd 的单引号串没有转义机制，
	// 值里出现 ' 时单引号方案会得到错误的 token。
	if !strings.Contains(envBody, `TUNNELMESH_AGENT_TOKEN="e2e-secret-token-value"`) {
		t.Errorf("agent.env missing token entry:\n%s", envBody)
	}
	if strings.Contains(install.stdout, "e2e-secret-token-value") {
		t.Errorf("token leaked into installer output:\n%s", install.stdout)
	}
	if !strings.Contains(install.stdout, "e2e-****") {
		t.Errorf("summary did not mask the token:\n%s", install.stdout)
	}
	unitPath := unitPathFor(root)
	assertFileExists(t, unitPath)
	calls := readFile(t, svcLog)
	for _, want := range expectedServiceCalls() {
		if !strings.Contains(calls, want) {
			t.Errorf("service call log missing %q:\n%s", want, calls)
		}
	}

	// 5) 再跑一次 = 升级：保留配置、产生二进制备份
	os.Truncate(svcLog, 0)
	runInstaller(t, bash, env, "--archive", archive, "--version", "v9.9.9", "--yes",
		"--server-url", "wss://tunnel.example.com/ws/agent", "--agent-id", "agent-e2e", "--no-linger")
	backups, _ := filepath.Glob(filepath.Join(root, ".local", "bin", "tunnelmesh-agent.bak-*"))
	if len(backups) != 1 {
		t.Errorf("upgrade backups = %d, want 1: %v", len(backups), backups)
	}
	if body := readFile(t, filepath.Join(root, ".config", "tunnelmesh", "agent.yaml")); !strings.Contains(body, "agent-e2e") {
		t.Errorf("upgrade rewrote configuration:\n%s", body)
	}

	// 6) 卸载：删二进制与服务定义，保留配置与数据
	os.Truncate(svcLog, 0)
	uninstall := runInstaller(t, bash, env, "--uninstall", "--yes")
	if _, err := os.Stat(filepath.Join(root, ".local", "bin", "tunnelmesh-agent")); !os.IsNotExist(err) {
		t.Errorf("binary still present after uninstall: %v", err)
	}
	if _, err := os.Stat(unitPath); !os.IsNotExist(err) {
		t.Errorf("unit still present after uninstall: %v", err)
	}
	assertFileExists(t, filepath.Join(root, ".config", "tunnelmesh", "agent.yaml"))
	assertFileExists(t, filepath.Join(root, ".config", "tunnelmesh", "agent.env"))
	if !strings.Contains(uninstall.stdout, "配置与数据保留") {
		t.Errorf("uninstall summary should state that config is retained:\n%s", uninstall.stdout)
	}
}

func expectedManager() string {
	if runtime.GOOS == "darwin" {
		return "launchd"
	}
	return "systemd-user"
}

// TestOneClickServerUserModeInstall 守护「server 在非 root 的 user 模式下也能装完」。
// init-node-id 的默认持久化路径是 /var/lib/tunnelmesh/node-id，非 root 时不可写；
// 这个组合曾经让整个安装在配置校验阶段以退出码 6 失败，而 agent-only 的 E2E 覆盖不到。
func TestOneClickServerUserModeInstall(t *testing.T) {
	if os.Getenv("TM_ONECLICK_E2E") != "1" {
		t.Skip("set TM_ONECLICK_E2E=1 to run the one-click installer end-to-end smoke test")
	}
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skipf("bash not available: %v", err)
	}
	root := t.TempDir() // 假的 $HOME
	work := t.TempDir()
	absStubs, err := filepath.Abs(filepath.Join("testdata", "bin"))
	if err != nil {
		t.Fatalf("abs stubs: %v", err)
	}

	binary := filepath.Join(work, "tunnelmesh-server")
	build := exec.Command("go", "build", "-o", binary, "../../../cmd/tunnelmesh-server")
	build.Env = append(os.Environ(), "CGO_ENABLED=0")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build server: %v\n%s", err, out)
	}
	archive := filepath.Join(work, "tunnelmesh-v9.9.8-server.tar.gz")
	packArchive(t, archive, map[string]string{
		"tunnelmesh-server":                             binary,
		"deploy/systemd-user/tunnelmesh-server.service": filepath.Join("..", "..", "systemd-user", "tunnelmesh-server.service"),
		"deploy/systemd/tunnelmesh-server.service":      filepath.Join("..", "..", "systemd", "tunnelmesh-server.service"),
		"deploy/macos/tunnelmesh.plist":                 filepath.Join("..", "..", "macos", "tunnelmesh.plist"),
	})

	env := []string{
		"HOME=" + root,
		"PATH=" + absStubs + string(os.PathListSeparator) + os.Getenv("PATH"),
		"SVC_STUB_LOG=" + filepath.Join(work, "svc.log"),
		"TM_ONECLICK_ALLOW_STDIN=1",
		"TM_ONECLICK_SERVICE_MANAGER=" + expectedManager(),
		"TM_CACHE_DIR=" + filepath.Join(work, "cache"),
		"TM_ONECLICK_LIB=" + absLib(t),
	}
	// --yes 下 server 的默认答案是 local + sqlite + 127.0.0.1:8080，无需任何 stdin。
	runRoleInstaller(t, bash, env, "server", "--archive", archive, "--version", "v9.9.8",
		"--yes", "--no-linger")

	yamlBody := assertFileMode(t, filepath.Join(root, ".config", "tunnelmesh", "server.yaml"), 0o600)
	for _, want := range []string{"mode: local", "driver: 'sqlite'", "http_addr: '127.0.0.1:8080'"} {
		if !strings.Contains(yamlBody, want) {
			t.Errorf("server.yaml missing %q:\n%s", want, yamlBody)
		}
	}
	// node identity 必须落在用户可写的状态目录，并回写进 YAML，run 阶段才不会再去碰默认路径。
	nodeIDPath := filepath.Join(root, ".local", "share", "tunnelmesh", "node-id")
	if _, err := os.Stat(nodeIDPath); err != nil {
		t.Errorf("node identity was not written to %s: %v", nodeIDPath, err)
	}
	if !strings.Contains(yamlBody, "node:") {
		t.Errorf("node.id was not persisted into server.yaml:\n%s", yamlBody)
	}
	if _, err := os.Stat("/var/lib/tunnelmesh/node-id"); err == nil {
		t.Error("user-mode install must not write the root-only default node identity path")
	}
	assertFileExists(t, serverUnitPathFor(root))
}

func serverUnitPathFor(home string) string {
	if runtime.GOOS == "darwin" {
		return filepath.Join(home, "Library", "LaunchAgents", "com.tunnelmesh.server.plist")
	}
	return filepath.Join(home, ".config", "systemd", "user", "tunnelmesh-server.service")
}

func unitPathFor(home string) string {
	if runtime.GOOS == "darwin" {
		return filepath.Join(home, "Library", "LaunchAgents", "com.tunnelmesh.agent.plist")
	}
	return filepath.Join(home, ".config", "systemd", "user", "tunnelmesh-agent.service")
}

func expectedServiceCalls() []string {
	if runtime.GOOS == "darwin" {
		return []string{"plutil -lint", "launchctl bootstrap", "launchctl kickstart -k"}
	}
	return []string{"systemctl --user daemon-reload", "systemctl --user enable tunnelmesh-agent.service", "systemctl --user restart tunnelmesh-agent.service"}
}

type runResult struct {
	stdout string
	stderr string
}

func runInstaller(t *testing.T, bash string, env []string, args ...string) runResult {
	t.Helper()
	return runRoleInstaller(t, bash, env, "agent", args...)
}

func runRoleInstaller(t *testing.T, bash string, env []string, role string, args ...string) runResult {
	t.Helper()
	// 安装脚本总是带 --yes 跑，本不该提问；走 runBash 是为了万一有人引入一条交互提示，
	// 失败信息会点名是哪个脚本卡住，而不是让 go test 挂到包级超时。
	out, errBuf, err := runBash(t, bash, env, nil, bashTimeout, "install-"+role+".sh", args...)
	if err != nil {
		t.Fatalf("%s installer failed: %v\nstdout:\n%s\nstderr:\n%s", role, err, out, errBuf)
	}
	return runResult{stdout: out, stderr: errBuf}
}

func absLib(t *testing.T) string {
	t.Helper()
	p, err := filepath.Abs(commonLib)
	if err != nil {
		t.Fatalf("abs lib: %v", err)
	}
	return p
}

func assertFileMode(t *testing.T, path string, want os.FileMode) string {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	if got := info.Mode().Perm(); got != want {
		t.Errorf("%s mode = %o, want %o", path, got, want)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(body)
}

func assertFileExists(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); err != nil {
		t.Errorf("expected file %s: %v", path, err)
	}
}

// packArchive 按发布归档布局打 tar.gz：成员名用正斜杠，二进制置 0755。
func packArchive(t *testing.T, dest string, members map[string]string) {
	t.Helper()
	out, err := os.Create(dest)
	if err != nil {
		t.Fatalf("create archive: %v", err)
	}
	defer out.Close()
	gz := gzip.NewWriter(out)
	defer gz.Close()
	tw := tar.NewWriter(gz)
	defer tw.Close()
	for name, src := range members {
		body, err := os.ReadFile(src)
		if err != nil {
			t.Fatalf("read %s: %v", src, err)
		}
		mode := int64(0o644)
		// 归档根下的可执行文件（tunnelmesh-agent / tunnelmesh-server / ...）必须是 0755，
		// 否则解压后 install -m 0755 之前的「检测已安装版本」步骤会因为不可执行而误判成全新安装。
		if strings.HasPrefix(name, "tunnelmesh-") && !strings.Contains(name, "/") {
			mode = 0o755
		}
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: mode, Size: int64(len(body)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatalf("write header %s: %v", name, err)
		}
		if _, err := tw.Write(body); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
}
