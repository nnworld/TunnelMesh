package install

import (
	"bytes"
	"os"
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
	"../macos/tunnelmesh.plist":         {"__ROLE__", "__HOME__", "__BINARY__", "__CONFIG__"},
	"../windows/tunnelmesh-service.xml": {"__ROLE__", "__BINARY__", "__CONFIG__", "__INSTALL_DIR__"},
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
