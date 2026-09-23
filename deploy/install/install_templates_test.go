package install

import (
	"bytes"
	"encoding/xml"
	"os"
	"strings"
	"testing"
)

// roles 是三个安装脚本都必须支持的角色；systemd 采用每角色一个 unit，
// macOS 与 Windows 采用单一模板 + 占位符渲染。
var roles = []string{"server", "agent", "client"}

// systemdUnits 按角色固定命名，安装脚本直接安装，不做内容替换。
func systemdUnit(role string) string {
	return "../systemd/tunnelmesh-" + role + ".service"
}

// genericTemplates 列出跨角色复用的模板及必须存在的占位符。
//
// 为什么必须是单一模板：历史上只有 client 有 checked-in 模板，server/agent 由
// 安装脚本内联生成，两份来源同时存在时 client 分支会原样拷贝模板，从而静默忽略
// 调用方传入的配置路径（macOS 第 3 个参数、Windows 的 -Config），Windows 的
// logpath 也与文档描述的目录不一致。占位符渲染消除了第二份来源。
var genericTemplates = map[string][]string{
	"../macos/tunnelmesh.plist":         {"__ROLE__", "__HOME__", "__BINARY__", "__CONFIG__", "__ENVIRONMENT__"},
	"../windows/tunnelmesh-service.xml": {"__ROLE__", "__BINARY__", "__CONFIG__", "__INSTALL_DIR__", "__ENV_BLOCK__"},
}

// systemdUserUnits 是 user 模式模板：与系统单元不同，路径全部由占位符渲染，
// 因为用户级安装的二进制、配置、状态目录都在 $HOME 下且可被 --bin-dir 等覆盖。
func systemdUserUnit(role string) string { return "../systemd-user/tunnelmesh-" + role + ".service" }

func TestSystemdUserTemplatesCoverEveryRole(t *testing.T) {
	for _, role := range roles {
		data, err := os.ReadFile(systemdUserUnit(role))
		if err != nil {
			t.Fatalf("%s: %v", systemdUserUnit(role), err)
		}
		for _, want := range []string{
			"__BINARY__", "__CONFIG__", "__ENV_FILE__", "__STATE_DIR__",
			"--config", "run", "WantedBy=default.target", "tunnelmesh-" + role,
		} {
			if !bytes.Contains(data, []byte(want)) {
				t.Errorf("%s does not contain %q", systemdUserUnit(role), want)
			}
		}
		if role == "server" && !bytes.Contains(data, []byte("init-node-id")) {
			t.Errorf("%s must run init-node-id before check-config", systemdUserUnit(role))
		}
		// user 单元以非 root 运行且 ReadWritePaths 只放开状态目录，
		// init-node-id 的默认路径 /var/lib/tunnelmesh 不可写，必须显式改指状态目录。
		if role == "server" && !bytes.Contains(data, []byte("--node-id-path __STATE_DIR__/node-id init-node-id")) {
			t.Errorf("%s must point init-node-id at the user-writable state dir", systemdUserUnit(role))
		}
	}
}

// TestAgentSystemUnitCanInjectToken agent.token 的 YAML 键是 `yaml:"-"`，
// 只能通过环境变量注入，因此系统单元必须有可选的 env 文件。
func TestAgentSystemUnitCanInjectToken(t *testing.T) {
	data, err := os.ReadFile(systemdUnit("agent"))
	if err != nil {
		t.Fatalf("%v", err)
	}
	if !bytes.Contains(data, []byte("EnvironmentFile=-/etc/tunnelmesh/agent.env")) {
		t.Errorf("agent system unit lost its optional EnvironmentFile")
	}
}

// TestGenericTemplatesStayWellFormedXML 守护「占位符渲染为空后模板仍是合法 XML」。
// 直接 plutil -lint 原模板做不到这件事：未渲染的 __ENVIRONMENT__ 本身就不是合法 XML，
// 因此这里用 encoding/xml 分别校验「渲染为空」与「渲染出真实 env 块」两种结果。
func TestGenericTemplatesStayWellFormedXML(t *testing.T) {
	cases := []struct {
		name        string
		placeholder string
		sample      string
	}{
		{"../macos/tunnelmesh.plist", "__ENVIRONMENT__", "  <key>EnvironmentVariables</key>\n  <dict>\n    <key>TUNNELMESH_AGENT_TOKEN</key>\n    <string>t0k</string>\n  </dict>"},
		{"../windows/tunnelmesh-service.xml", "__ENV_BLOCK__", `  <env name="TUNNELMESH_AGENT_TOKEN" value="t0k" />`},
	}
	for _, tc := range cases {
		body, err := os.ReadFile(tc.name)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		text := string(body)
		if !strings.Contains(text, tc.placeholder) {
			t.Fatalf("%s lost placeholder %s", tc.name, tc.placeholder)
		}
		for _, rendered := range []string{
			strings.ReplaceAll(text, tc.placeholder, ""),
			strings.ReplaceAll(text, tc.placeholder, tc.sample),
		} {
			// DOCTYPE 不是 encoding/xml 关心的内容，去掉后只校验元素结构。
			rendered = stripDoctype(rendered)
			if err := xml.Unmarshal([]byte(rendered), new(any)); err != nil {
				t.Errorf("%s is not well-formed XML after rendering %s: %v", tc.name, tc.placeholder, err)
			}
		}
	}
}

func stripDoctype(s string) string {
	start := strings.Index(s, "<!DOCTYPE")
	if start < 0 {
		return s
	}
	end := strings.Index(s[start:], ">")
	if end < 0 {
		return s
	}
	return s[:start] + s[start+end+1:]
}

func TestServiceTemplatesCoverEveryRole(t *testing.T) {
	for _, role := range roles {
		data, err := os.ReadFile(systemdUnit(role))
		if err != nil {
			t.Fatalf("%s: %v", systemdUnit(role), err)
		}
		for _, want := range []string{"tunnelmesh-" + role, "--config", "run"} {
			if !bytes.Contains(data, []byte(want)) {
				t.Errorf("%s does not contain %q", systemdUnit(role), want)
			}
		}
	}

	for name, placeholders := range genericTemplates {
		data, err := os.ReadFile(name)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		wants := append([]string{"--config", "run"}, placeholders...)
		for _, want := range wants {
			if !bytes.Contains(data, []byte(want)) {
				t.Errorf("%s does not contain %q", name, want)
			}
		}
	}
}

// TestInstallScriptsRenderSharedTemplates 保证脚本引用共享模板并替换占位符，
// 防止再次退回“脚本内联生成第二份模板”的状态。
func TestInstallScriptsRenderSharedTemplates(t *testing.T) {
	cases := map[string][]string{
		"linux-install.sh":    {"../systemd/tunnelmesh-server.service", "../systemd/tunnelmesh-agent.service", "../systemd/tunnelmesh-client.service"},
		"macos-install.sh":    {"../macos/tunnelmesh.plist", "__ROLE__", "__HOME__", "__BINARY__", "__CONFIG__"},
		"windows-install.ps1": {`..\windows\tunnelmesh-service.xml`, "__ROLE__", "__BINARY__", "__CONFIG__", "__INSTALL_DIR__"},
	}
	for name, wants := range cases {
		data, err := os.ReadFile(name)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		for _, want := range wants {
			if !bytes.Contains(data, []byte(want)) {
				t.Errorf("%s does not reference %q", name, want)
			}
		}
	}
}

func TestInstallScriptsSupportEveryRole(t *testing.T) {
	scripts := []string{"linux-install.sh", "macos-install.sh", "windows-install.ps1", "windows-uninstall.ps1"}
	for _, name := range scripts {
		data, err := os.ReadFile(name)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		for _, role := range roles {
			if !bytes.Contains(data, []byte(role)) {
				t.Errorf("%s does not support the %s role", name, role)
			}
		}
	}
}
