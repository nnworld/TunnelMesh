# 三端一键安装脚本 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 为 server / agent / client 三个角色各提供一条命令的交互式一键安装脚本（Linux+macOS 用 `.sh`，Windows 用 `.ps1`），覆盖下载校验、安装、配置生成、服务注册、升级与卸载。

**Architecture:** 入口脚本薄、共享库厚。`tm_main` 用模板方法固定 8 个阶段（preflight → resolve&fetch → placement → install binary → configure → validate → register&start → verify&report），三个 `.sh` 入口只声明角色钩子（`tm_role_prompts`、`tm_role_render_config`、`tm_role_post_install`）。下载/校验/交互/渲染/服务注册全部在 `tunnelmesh-install-common.sh`；Windows 侧用 `tunnelmesh-install-common.ps1` 镜像同一套阶段与参数语义。服务托管复用仓库既有的单一模板来源：`deploy/systemd/`（system）、新增 `deploy/systemd-user/`（user）、`deploy/macos/tunnelmesh.plist`、`deploy/windows/tunnelmesh-service.xml`。

**Tech Stack:** Bash 3.2 兼容语法（macOS 自带 bash 3.2，禁止 `declare -A` 之外的 4.x 特性；实测 macOS bash 3.2 **不支持**关联数组，因此答案存储用 `tm_ans_get/set` 包装的普通变量命名空间，见 Task 3）、curl、tar、sha256sum/shasum、systemd（system + user）、launchd、PowerShell 5.1+、WinSW v2.12.0、Go 1.23+（契约测试与配置漂移测试）。

**Spec:** [docs/superpowers/specs/2026-09-23-one-click-install-scripts-design.md](../specs/2026-09-23-one-click-install-scripts-design.md)

## Global Constraints

- 仓库：`nnworld/TunnelMesh`，分支 `codex/one-click-install`，基线 `main`。
- Release 契约：归档名 `tunnelmesh-<version>-<goos>-<goarch>.tar.gz`（Windows 为 `.zip`），下载地址 `<base>/download/<version>/<archive>`，校验文件名固定 `SHA256SUMS`，条目格式为「sha256 + **两个空格** + 文件名」。
- 默认 base：`https://github.com/nnworld/TunnelMesh/releases`；默认 raw base：`https://raw.githubusercontent.com/nnworld/TunnelMesh`；latest API：`https://api.github.com/repos/nnworld/TunnelMesh/releases/latest`。
- 版本格式：`^v[0-9]+\.[0-9]+\.[0-9]+$`，不合法退出码 2。
- Linux 默认安装模式 **user**；`--mode system` 需 root。macOS 只有用户级 LaunchAgent。Windows 只有机器级服务。
- 敏感值（token、DSN、私钥、主密钥）**绝不**写入 YAML、模板、脚本或命令行参数；只走交互隐藏输入、`TUNNELMESH_*` 环境变量或 `--token-file`/`--secret-env-file`。日志与摘要一律 `tm_mask`。
- 退出码固定：0 成功、2 参数、3 preflight、4 下载、5 校验、6 配置校验、7 服务、8 卸载。
- 已存在的配置文件默认覆盖，覆盖前备份 `<path>.bak-<UTC时间戳>`，最多保留最近 3 份；`--keep-config` 跳过生成；升级（重复运行）默认不重新生成配置，`--reconfigure` 才重新生成。
- 每个 `.sh` 必须 `set -euo pipefail`，临时目录用 `mktemp -d` 且 `trap ... EXIT INT TERM` 清理。
- 单一模板来源：脚本不得内联生成第二份 unit/plist/XML 模板，只做占位符替换（`deploy/install/install_templates_test.go` 守护）。
- 文档与索引：新增记录后执行 `python3 scripts/gen_doc_index.py`；`docs/README.md` 不得出现无入口的孤儿文档。
- 验证命令（每个 Task 结束前按需执行，最终必须全绿）：`go test ./deploy/... -count=1`、`go test ./scripts/... -count=1`、`go test ./... -count=1`、`go test -race ./...`、`go vet ./...`、`git diff --check`、`bash -n deploy/install/oneclick/*.sh`。
- 本环境网络限制：`api.github.com` 可达，`github.com` Release 下载**不可达**。所有自动化测试走 `--archive` fixture 或 PATH 中的 `curl` stub；真实下载与 WinSW 校验和补录需在具备外网的机器上手工完成，结果记入 PR 描述。
- 提交需用户授权：执行本计划时在第一次 commit 前询问一次；获得授权后按各 Task 给出的提交信息提交，未授权则跳过全部 commit 步骤并在收尾报告中说明。

## File Structure

**新增**

| 路径 | 职责 |
| --- | --- |
| `deploy/install/oneclick/tunnelmesh-install-common.sh` | Linux/macOS 共享库：常量、日志、平台探测、版本解析、下载校验与缓存、交互原语、渲染原语、服务生命周期、`tm_main` 编排 |
| `deploy/install/oneclick/install-server.sh` | server 入口：flag 透传 + 角色钩子（server 问答、YAML/env 渲染、`init-node-id`/`doctor`/`admin bootstrap`） |
| `deploy/install/oneclick/install-agent.sh` | agent 入口：角色钩子（server_url / agent id / token / metadata） |
| `deploy/install/oneclick/install-client.sh` | client 入口：角色钩子（server_url / token / tunnels 循环添加） |
| `deploy/install/oneclick/tunnelmesh-install-common.ps1` | Windows 共享模块：与 `.sh` 同阶段、同参数语义 |
| `deploy/install/oneclick/install-server.ps1`、`install-agent.ps1`、`install-client.ps1` | Windows 三角色入口 |
| `deploy/install/oneclick/winsw-checksums.txt` | WinSW `<version> <arch> <sha256> <asset>` pin 列表 + 更新流程说明 |
| `deploy/install/oneclick/oneclick_scripts_test.go` | Go 契约测试：文件存在性、`bash -n`、调用 bash 函数套件、入口薄壳约束、密钥硬编码扫描、模板占位符、Release 契约与 `scripts/install.sh` 双向一致 |
| `deploy/install/oneclick/oneclick_config_test.go` | 配置漂移测试：渲染出的 YAML 必须能被 `config.Load` 解析且字段值正确 |
| `deploy/install/oneclick/oneclick_e2e_test.go` | `TM_ONECLICK_E2E=1` 门控的端到端冒烟：安装 → 升级 → 卸载 |
| `deploy/install/oneclick/testdata/run_tests.sh` | bash 函数级测试套件（由 Go 测试调用），含 `assert_eq`/`assert_contains`/`assert_file_mode` |
| `deploy/install/oneclick/testdata/bin/{curl,systemctl,launchctl,loginctl,plutil,runuser}_stub` | 记录调用序列的 stub，通过 PATH 前置注入 |
| `deploy/install/oneclick/testdata/fixtures/SHA256SUMS.{ok,bad}` | 校验和 fixture |
| `deploy/systemd-user/tunnelmesh-server.service`、`tunnelmesh-agent.service`、`tunnelmesh-client.service` | user 模式单元模板，占位符 `__BINARY__`、`__CONFIG__`、`__ENV_FILE__`、`__STATE_DIR__` |
| `docs/deployment/oneclick-install.md` | 一键安装用户文档（三平台 × 三角色矩阵、交互项、非交互示例、升级/回滚/卸载、镜像与离线、`curl \| bash` 信任模型、退出码排障表） |
| `docs/pull-requests/2026-09-23-one-click-install-scripts.md` | PR 记录 |

**修改**

| 路径 | 修改点 |
| --- | --- |
| `deploy/systemd/tunnelmesh-agent.service` | 增加 `EnvironmentFile=-/etc/tunnelmesh/agent.env`（`agent.token` 的 YAML 键是 `yaml:"-"`，只能走环境变量） |
| `deploy/macos/tunnelmesh.plist` | 增加 `__ENVIRONMENT__` 占位符（渲染为 `EnvironmentVariables` dict，空时渲染为空串） |
| `deploy/windows/tunnelmesh-service.xml` | 增加 `__ENV_BLOCK__` 占位符（渲染为 `<env>` 元素，空时渲染为空串） |
| `deploy/install/macos-install.sh` | 渲染 `__ENVIRONMENT__` 为空，行为不变 |
| `deploy/install/windows-install.ps1` | 渲染 `__ENV_BLOCK__` 为空，行为不变 |
| `deploy/install/install_templates_test.go` | 覆盖 `deploy/systemd-user/` 三份模板与两个新占位符 |
| `scripts/build-release.sh` | 归档阶段追加删除 `testdata/`；Linux 归档追加 `deploy/systemd-user` |
| `README.md` | Quick start → Install 改为一键安装优先 |
| `docs/README.md`、`deploy/README.md`、`docs/deployment/{binary-release,linux-systemd,macos-launchd,windows-service}.md` | 同步文档 |
| `docs/superpowers/specs/README.md`、`docs/superpowers/plans/README.md`、`docs/pull-requests/README.md` | 由 `gen_doc_index.py` 重建 |

---

### Task 11: Windows PowerShell 一键安装

**Files:**
- Create: `deploy/install/oneclick/tunnelmesh-install-common.ps1`
- Create: `deploy/install/oneclick/install-server.ps1`、`install-agent.ps1`、`install-client.ps1`
- Create: `deploy/install/oneclick/winsw-checksums.txt`
- Modify: `deploy/install/oneclick/oneclick_scripts_test.go`（静态契约断言）
- Modify: `docs/deployment/windows-service.md`

**Interfaces:**
- Consumes: 已在 Task 4 加好的 `deploy/windows/tunnelmesh-service.xml` 的 `__ENV_BLOCK__` 占位符；`deploy/install/windows-install.ps1` 与 `windows-uninstall.ps1` 的服务注册/卸载语义。
- Produces（`.ps1` 函数名即契约，静态测试按名断言）：
  - `Resolve-TmVersion [-Version <string>]` → `vX.Y.Z`（GitHub API `tag_name` → `/releases/latest` 的 `Location` 回退）
  - `Get-TmPlatform` → `@{ GoOs; GoArch; ArchiveExt = 'zip' }`（`$env:PROCESSOR_ARCHITECTURE` 映射 `AMD64→amd64`、`ARM64→arm64`）
  - `Invoke-TmDownload -Url -OutFile`（`Invoke-WebRequest`，失败抛异常 → 退出码 4）
  - `Test-TmChecksum -File -SumsFile -ArchiveName`（`Get-FileHash -Algorithm SHA256`，不匹配 → 退出码 5）
  - `Get-TmCacheDir`（`$env:LOCALAPPDATA\TunnelMesh\releases`）、`Get-TmCachePath`
  - `Read-TmAnswer -Key -Prompt -Default`、`Read-TmBool`、`Read-TmChoice`、`Read-TmSecret`（`Read-Host -AsSecureString`，`-Yes` 下不读）
  - `Get-TmSecretValue -Key -Prompt -EnvName`（`-SecretEnvFile` > `-TokenFile` > 环境变量 > 交互；`-Yes` 且全空 → 退出码 3）
  - `Write-TmYaml -Role`、`Write-TmEnvBlock`（渲染 `<env name= value= />`，XML 转义与 `.sh` 侧一致）
  - `Get-TmWinSW -WinSWPath`（本地路径优先；否则查 `winsw-checksums.txt`，无登记条目则中止并打印手动步骤）
  - `Install-TmService -Role -Binary -Config -EnvBlock`、`Uninstall-TmRole -Role`
  - `Invoke-TmMain -Role -Args`（8 阶段编排，与 `tm_main` 一一对应）
  - 每个入口的 `param()` 块：`-Version`、`-Yes`、`-Uninstall`、`-NoService`、`-NoStart`、`-NoEnable`、`-NoCache`、`-KeepConfig`、`-Reconfigure`、`-BaseUrl`、`-RawBaseUrl`、`-Archive`、`-TokenFile`、`-SecretEnvFile`、`-InstallDir`、`-ConfigDir`、`-StateDir`、`-ServerUrl`、`-AgentId`、`-Tunnel`（可多次）、`-WinSW`

- [x] **Step 1: 写失败测试 —— 静态契约断言**

`.ps1` 无法在 macOS/Linux 执行，与既有 `windows-install.ps1` 同样只做静态断言。追加到 `oneclick_scripts_test.go`：

```go
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
		if strings.Contains(body, "[string]$Token") {
			t.Errorf("%s must not accept a plaintext -Token parameter", name)
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
```

Run: `go test ./deploy/install/oneclick -run 'TestPowerShell|TestWinSW' -count=1`
Expected: FAIL —— `stat install-server.ps1: no such file or directory`。

- [x] **Step 2: 写最小实现 —— `winsw-checksums.txt`**

```text
# WinSW 校验和 pin 列表。
# 格式（4 列，空格分隔）：<version> <arch> <sha256> <asset-name>
#
# 补录流程（必须在可访问 github.com Release 的机器上执行，然后把结果提交到本文件）：
#   curl -fsSL -o /tmp/WinSW-x64.exe \
#     https://github.com/winsw/winsw/releases/download/v2.12.0/WinSW-x64.exe
#   shasum -a 256 /tmp/WinSW-x64.exe
#   # 追加一行： v2.12.0 amd64 <上一步输出的 64 位十六进制> WinSW-x64.exe
#   并在 docs/deployment/windows-service.md 记录版本、日期与补录人。
#
# 安全策略：install-*.ps1 只自动下载本文件中已登记的版本；查不到条目时**中止**，
# 打印手动下载步骤，并提示用 -WinSW <本地路径> 指定。绝不静默信任未 pin 的二进制。
# 上游未发布 arm64 资产，Windows on ARM 统一使用 WinSW-x64.exe（x64 仿真）。
#
# 当前无已登记条目：本仓库的实现环境无法访问 github.com Release 下载，
# 补录步骤见 Task 12 Step 3 与 docs/deployment/windows-service.md。
```

- [x] **Step 3: 写最小实现 —— 共享模块与入口**

`tunnelmesh-install-common.ps1` 按 Task 11 Interfaces 列出的函数实现，关键约束：

```powershell
$ErrorActionPreference = 'Stop'
Set-StrictMode -Version 2.0

# 退出码与 .sh 侧完全一致，便于统一排障文档。
$script:TmExit = @{ Ok = 0; Usage = 2; Preflight = 3; Download = 4; Checksum = 5; Config = 6; Service = 7; Uninstall = 8 }

function Write-TmInfo { param([string]$Message) Write-Host "==> $Message" }
function Write-TmWarn { param([string]$Message) Write-Warning $Message }
function Stop-Tm { param([int]$Code, [string]$Message) Write-Error "ERROR: $Message"; exit $Code }

# 敏感值只经环境变量注入服务进程，绝不写进 YAML；渲染进 WinSW XML 的 <env> 块，
# 因为 WinSW 没有 EnvironmentFile 机制。渲染后对 XML 收紧 ACL。
function Write-TmEnvBlock {
  param([hashtable]$Secrets)
  $sb = New-Object System.Text.StringBuilder
  foreach ($key in ($Secrets.Keys | Sort-Object)) {
    [void]$sb.AppendLine(('  <env name="{0}" value="{1}" />' -f (Convert-TmXml $key), (Convert-TmXml $Secrets[$key])))
  }
  $sb.ToString()
}
```

`Get-TmWinSW` 的核心判定（无登记条目即中止）：

```powershell
function Get-TmWinSW {
  param([string]$WinSWPath, [string]$Version = 'v2.12.0', [string]$Arch = 'amd64')
  if ($WinSWPath) {
    if (-not (Test-Path -LiteralPath $WinSWPath)) { Stop-Tm $script:TmExit.Usage "WinSW not found: $WinSWPath" }
    return (Resolve-Path -LiteralPath $WinSWPath).Path
  }
  $table = Join-Path $PSScriptRoot 'winsw-checksums.txt'
  $row = Get-Content -LiteralPath $table |
    Where-Object { $_ -notmatch '^\s*(#|$)' } |
    ForEach-Object { $_ -split '\s+' } |
    Where-Object { $_[0] -eq $Version -and $_[1] -eq $Arch } |
    Select-Object -First 1
  if (-not $row) {
    Stop-Tm $script:TmExit.Download @"
WinSW $Version ($Arch) 未登记在 winsw-checksums.txt 中，拒绝自动下载。
请手工下载并用 -WinSW 指定路径：
  https://github.com/winsw/winsw/releases/download/$Version/WinSW-x64.exe
或按 winsw-checksums.txt 头部的补录流程登记校验和后重试。
"@
  }
  $dest = Join-Path (Get-TmCacheDir) "WinSW-$Version-$Arch.exe"
  Invoke-TmDownload -Url "https://github.com/winsw/winsw/releases/download/$Version/$($row[3])" -OutFile $dest
  $actual = (Get-FileHash -LiteralPath $dest -Algorithm SHA256).Hash.ToLowerInvariant()
  if ($actual -ne $row[2].ToLowerInvariant()) {
    Remove-Item -Force $dest
    Stop-Tm $script:TmExit.Checksum "WinSW checksum mismatch (expected $($row[2]), actual $actual)"
  }
  return $dest
}
```

三个入口的结构（以 agent 为例）：

```powershell
[CmdletBinding()]
param(
  [string]$Version = 'latest',
  [switch]$Yes, [switch]$Uninstall, [switch]$NoService, [switch]$NoStart, [switch]$NoEnable,
  [switch]$NoCache, [switch]$KeepConfig, [switch]$Reconfigure,
  [string]$BaseUrl, [string]$RawBaseUrl, [string]$Archive,
  [string]$TokenFile, [string]$SecretEnvFile,
  [string]$InstallDir = "$env:ProgramFiles\TunnelMesh",
  [string]$ConfigDir = "$env:ProgramData\TunnelMesh",
  [string]$StateDir = "$env:ProgramData\TunnelMesh",
  [string]$ServerUrl, [string]$AgentId, [string[]]$Tunnel, [string]$WinSW
)
. $PSScriptRoot\tunnelmesh-install-common.ps1
Invoke-TmMain -Role agent -Bound $PSBoundParameters
```

`Invoke-TmMain` 内固定顺序：管理员校验（非管理员 → 退出码 3）→ 版本解析 → 下载/校验/解压（`Expand-Archive`）→ 安装二进制与配置目录 → 渲染 YAML 与 `<env>` 块 → `tunnelmesh-<role>.exe --config <path> check-config` → `Get-TmWinSW` + 渲染 XML + `install`/`start` → 状态与摘要。升级与卸载语义与 `.sh` 侧一致（`.bak-<时间戳>` 备份、保留配置）。

- [x] **Step 4: 运行测试确认通过**

Run: `go test ./deploy/install/oneclick -count=1`
Expected: PASS（含 `TestPowerShellFilesExist`、`TestPowerShellContract`、`TestWinSWChecksumPolicy`）。

Run（有 `pwsh` 时）：`for f in deploy/install/oneclick/*.ps1; do pwsh -NoProfile -Command "\$null = [System.Management.Automation.Language.Parser]::ParseFile('$PWD/$f', [ref]\$null, [ref]\$err); if (\$err) { exit 1 }"; done`
Expected: 全部退出码 0；无 `pwsh` 时跳过并在 PR 描述里记录「PowerShell 语法未在本地校验」。

- [x] **Step 5: 更新 `docs/deployment/windows-service.md`**

新增小节：

```markdown
## 一键安装

以管理员身份打开 PowerShell：

```powershell
irm https://raw.githubusercontent.com/nnworld/TunnelMesh/main/deploy/install/oneclick/install-agent.ps1 -OutFile install-agent.ps1
.\install-agent.ps1
```

等价的一行式（参数直接跟在后面，不需要占位名）：

```powershell
& ([scriptblock]::Create((irm https://raw.githubusercontent.com/nnworld/TunnelMesh/main/deploy/install/oneclick/install-agent.ps1))) -Yes -ServerUrl wss://tunnel.example.com/ws/client
```

脚本会：校验并下载 Release 归档 → 安装 `tunnelmesh-<role>.exe` 到 `C:\Program Files\TunnelMesh`
→ 渲染 `C:\ProgramData\TunnelMesh\<role>.yaml` → 把 token 等敏感值渲染进 WinSW 服务 XML 的
`<env>` 块并收紧 ACL → `check-config` → 注册并启动服务。

WinSW 由脚本自动获取，但**只下载 `winsw-checksums.txt` 中已登记校验和的版本**；没有登记条目时脚本
中止并提示用 `-WinSW <本地路径>`。补录条目（需在可访问 github.com Release 的机器上执行）：

```powershell
Invoke-WebRequest -OutFile $env:TEMP\WinSW-x64.exe `
  https://github.com/winsw/winsw/releases/download/v2.12.0/WinSW-x64.exe
(Get-FileHash $env:TEMP\WinSW-x64.exe -Algorithm SHA256).Hash.ToLowerInvariant()
# 把结果按「<version> <arch> <sha256> <asset-name>」追加到
# deploy/install/oneclick/winsw-checksums.txt 并提交
```

卸载：`.\install-agent.ps1 -Uninstall -Yes`（保留 `C:\ProgramData\TunnelMesh` 下的配置与日志）。
```

- [x] **Step 6: Commit（需授权）** —— 已按用户授权合并为收尾单次提交，提交信息见 PR 记录

```bash
git add deploy/install/oneclick docs/deployment/windows-service.md
git commit -m "feat(install): add Windows one-click installers with pinned WinSW"
```

---

### Task 12: PR 记录、全量验证与收尾

**Files:**
- Create: `docs/pull-requests/2026-09-23-one-click-install-scripts.md`
- Regenerate: `docs/pull-requests/README.md`（`gen_doc_index.py`）

- [x] **Step 1: 全量验证**

```bash
go test ./... -count=1
go test -race ./...
go vet ./...
go test ./deploy/... -count=1
TM_ONECLICK_E2E=1 go test ./deploy/install/oneclick -count=1
bash -n deploy/install/oneclick/*.sh deploy/install/*.sh scripts/*.sh
# 模板含 __ENVIRONMENT__ 占位符，未渲染时不是合法 XML；lint 的是「渲染为空」的结果。
sed 's/^__ENVIRONMENT__$//' deploy/macos/tunnelmesh.plist | plutil -lint -
git diff --check
```

Expected: 全部退出码 0。`go test ./...` 需要 Task 0 已生成 `internal/server/web_dist`，否则 `internal/server` 会以 `pattern all:web_dist: no matching files found` 编译失败——那是环境问题，不是本次改动引入的失败，但必须在本任务解决后才能宣布全绿。

本次不涉及前端源码，`npm test`/`npm run build` 只在 Task 0 用于生成嵌入产物；若 `web/dist` 与 `internal/server/web_dist` 不一致，`scripts/verify-web-embed.sh` 会失败，需重跑 `cd web && npm run build`。

- [x] **Step 2: 文档与实现一致性核对**

逐条核对 `docs/deployment/oneclick-install.md` 与实现：

```bash
# flag 面：文档里出现的每个 --flag 都必须被 tm_parse_common_args 或角色钩子解析
grep -o -- '--[a-z-]\+' docs/deployment/oneclick-install.md | sort -u > /tmp/doc-flags.txt
grep -o -- '--[a-z-]\+' deploy/install/oneclick/tunnelmesh-install-common.sh | sort -u > /tmp/impl-flags.txt
comm -23 /tmp/doc-flags.txt /tmp/impl-flags.txt
```

Expected: 无输出（文档里不存在脚本不认识的 flag）。反向再跑一次 `comm -13`，输出里的每一项都必须是**刻意不文档化**的内部 flag，否则补文档。

退出码表逐条与 `tm_die "$TM_EXIT_*"` 的实际使用位置对照；路径表与 `tm_default_*` 的返回值对照。

- [x] **Step 3: WinSW 校验和补录（条件执行）**

若当前机器可访问 `github.com` Release 下载：

```bash
curl -fsSL -o /tmp/WinSW-x64.exe https://github.com/winsw/winsw/releases/download/v2.12.0/WinSW-x64.exe
shasum -a 256 /tmp/WinSW-x64.exe
```

把结果按 `v2.12.0 amd64 <sha256> WinSW-x64.exe` 追加到 `deploy/install/oneclick/winsw-checksums.txt`（删掉「当前无已登记条目」那三行注释），并执行 `go test ./deploy/install/oneclick -run TestWinSWChecksumPolicy -count=1` 确认 4 列格式与 64 位十六进制校验通过。

若不可访问（本实现环境即如此：`api.github.com` 可达、`github.com` Release 超时）：保持文件无数据行，并在 PR 记录的「测试证据」里写明「WinSW 自动下载路径未验证，Windows 安装需 `-WinSW` 手动指定；补录步骤已写入 `winsw-checksums.txt` 头部与 `docs/deployment/windows-service.md`」。

- [ ] **Step 4: 真实下载路径手工验证（条件执行）** —— 未执行：实现环境可访问 api.github.com 但无法访问 github.com Release 下载；已在 PR 记录「未验证项」写明补做命令

在可访问 github.com 的机器上执行一次，并把输出摘要记入 PR：

```bash
bash deploy/install/oneclick/install-agent.sh --version v1.1.1 --no-service --yes \
  --server-url wss://tunnel.example.com/ws/agent --agent-id agent-smoke
```

Expected: 解析版本 → 下载 → `SHA256SUMS` 校验通过 → 二进制落到 `~/.local/bin` → `check-config` 因 token 占位失败并给出明确原因（退出码 3 或 6）。这一步验证的是**下载与校验链路**，不是配置正确性。

- [x] **Step 5: 写 PR 记录**

创建 `docs/pull-requests/2026-09-23-one-click-install-scripts.md`，按 AGENTS.md 要求包含全部小节：标题、目标分支（`main`）、摘要、用户影响、API/Schema/配置影响、安全与授权影响、测试证据、发布步骤、回滚步骤、Reviewer 关注点、集成状态。其中必须写明：

- **API/Schema 影响：无**。不改任何 HTTP API、不改数据库 Schema、不改协议 frame，因此不涉及 `migrations/`、`SchemaVersion` 与 OpenAPI。
- **配置影响**：新增 `deploy/systemd-user/` 三份模板与两个占位符（`__ENVIRONMENT__`、`__ENV_BLOCK__`）；`deploy/systemd/tunnelmesh-agent.service` 新增可选 `EnvironmentFile=-/etc/tunnelmesh/agent.env`（缺文件不阻塞启动，向后兼容）。
- **安全影响**：新增的敏感值载体是 `~/.config/tunnelmesh/<role>.env`（0600）、渲染后的 LaunchAgent plist（0600）、WinSW XML 的 `<env>` 块（ACL 收紧）；不提供明文 `--token`；`curl | bash` 信任模型与缓解措施写入文档。
- **测试证据**：贴上 Task 12 Step 1 的命令与结论，以及 Step 3/4 的执行情况（含「未验证」项与原因）。
- **回滚步骤**：见下。

Run: `python3 scripts/gen_doc_index.py && git status --short docs`

- [x] **Step 6: 回滚注意事项（写入 PR 记录）**

- 本次改动**不含数据库迁移**，回滚只需 `git revert` 对应提交并重新发布，无需数据处理。
- 模板占位符是**向后兼容**的：旧版 `macos-install.sh`/`windows-install.ps1` 若在新模板上运行，会把 `__ENVIRONMENT__`/`__ENV_BLOCK__` 原样留在渲染结果里——因此回滚代码时必须**同时**回滚 `deploy/macos/tunnelmesh.plist` 与 `deploy/windows/tunnelmesh-service.xml`，两者是一个原子变更。
- `deploy/systemd/tunnelmesh-agent.service` 的 `EnvironmentFile=-` 前缀保证缺文件不阻塞，可独立回滚。
- 已用一键脚本装好的机器不受仓库回滚影响；如需下线，用户执行 `install-<role>.sh --uninstall`（保留配置与数据）。
- 发布归档布局变化（新增 `deploy/install/oneclick/`、`deploy/systemd-user/`）只影响**下一个** tag，已发布的 Release 不可变，无需处理。

- [x] **Step 7: Commit（需授权）** —— 已按用户授权合并为收尾单次提交，提交信息见 PR 记录

```bash
git add docs/pull-requests
git commit -m "docs(install): record one-click installer pull request"
```

---

## 自查清单（执行者收尾前逐条勾选）

- [x] 规格 §5.1 的五种调用形态都能跑通，其中 `bash -c "$(curl ...)"` 与 `curl | bash` 至少各手工验证一次（`--help` 与 `--archive --yes` 两条路径即可）。
- [x] `bash --version` 为 3.2 时脚本可用：`grep -n 'declare -A\|mapfile\|readarray\||&\|&>>' deploy/install/oneclick/*.sh` 无输出。
- [x] `grep -rn 'token\|password\|secret' deploy/install/oneclick/*.sh` 结果里没有真实凭据，只有键名、提示语与掩码逻辑。
- [x] 交互提示在 stdin 是管道时不会静默取默认值（无 tty 且未 `--yes` → 退出码 3）。
- [x] 升级不重新生成配置；`--reconfigure` 会先备份再覆盖；备份最多保留 3 份（配置）与 1 份（二进制）。
- [x] 卸载保留配置、env 与数据目录，并在输出里逐条打印路径。
- [x] `docs/README.md` 与 `deploy/README.md` 都能点到新文档，`python3 scripts/gen_doc_index.py` 无 diff 残留。
- [x] `go test ./... -count=1`、`go test -race ./...`、`go vet ./...`、`git diff --check` 全绿。

### Task 8: 端到端冒烟测试（env 门控）

**Files:**
- Create: `deploy/install/oneclick/oneclick_e2e_test.go`
- Modify: `deploy/install/oneclick/tunnelmesh-install-common.sh`（`tm_detect_service_manager` 增加可注入接缝）
- Modify: `deploy/install/oneclick/testdata/run_tests.sh`

**Interfaces:**
- Consumes: `tm_main` 全流程（Task 7）、`testdata/bin/*` stub（Task 6）、真实 `go build` 产物。
- Produces:`TM_ONECLICK_SERVICE_MANAGER`（覆盖服务管理器探测，供容器/CI 与测试使用）。

- [x] **Step 1: 写失败测试 —— 服务管理器注入接缝**

追加到 `testdata/run_tests.sh`：

```bash
# 服务管理器可注入：容器与 CI 里 /run/systemd/system 常常不存在，
# 端到端测试需要显式指定管理器，否则只能覆盖 none 分支。
assert_eq "svc/inject-user" "systemd-user" "$(TM_ONECLICK_SERVICE_MANAGER=systemd-user tm_detect_service_manager_stdout)"
assert_eq "svc/inject-none" "none" "$(TM_ONECLICK_SERVICE_MANAGER=none tm_detect_service_manager_stdout)"
```

Run: `go test ./deploy/install/oneclick -run TestShellFunctionSuite -count=1`
Expected: FAIL —— `tm_detect_service_manager_stdout: command not found`。

- [x] **Step 2: 写最小实现**

```bash
# tm_detect_service_manager_stdout：纯解析，便于测试；tm_detect_service_manager 在它之上打印警告。
tm_detect_service_manager_stdout() {
  if [[ -n "${TM_ONECLICK_SERVICE_MANAGER:-}" ]]; then printf '%s\n' "$TM_ONECLICK_SERVICE_MANAGER"; return 0; fi
  if [[ "$TM_NO_SERVICE" == "1" ]]; then printf 'none\n'; return 0; fi
  case "$TM_OS_FAMILY" in
    darwin) tm_have launchctl && printf 'launchd\n' || printf 'none\n' ;;
    linux)
      if [[ "$TM_MODE" == "system" ]] && tm_has_systemd; then printf 'systemd-system\n'
      elif tm_has_systemd_user; then printf 'systemd-user\n'
      else printf 'none\n'; fi ;;
    *) printf 'none\n' ;;
  esac
}
```

并把 Task 6 里的 `tm_detect_service_manager` 改成调用它（删除重复的 case 分支，只保留「none 时打印警告 + 赋值 `TM_SERVICE_MANAGER`」）：

```bash
tm_detect_service_manager() {
  TM_SERVICE_MANAGER="$(tm_detect_service_manager_stdout)"
  if [[ "$TM_SERVICE_MANAGER" == "none" && "$TM_NO_SERVICE" != "1" ]]; then
    tm_warn "未检测到可用的服务管理器，将只安装二进制与配置；前台启动命令见安装结束后的摘要"
  fi
}
```

Run: `go test ./deploy/install/oneclick -count=1`
Expected: PASS。

- [x] **Step 3: 写失败测试 —— E2E**

创建 `deploy/install/oneclick/oneclick_e2e_test.go`：

```go
package oneclick

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
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

	root := t.TempDir()   // 假的 $HOME
	work := t.TempDir()   // 构建与归档
	stubs := filepath.Join("testdata", "bin")
	absStubs, err := filepath.Abs(stubs)
	if err != nil {
		t.Fatalf("abs stubs: %v", err)
	}

	// 1) 真实二进制
	binary := filepath.Join(work, "tunnelmesh-agent")
	build := exec.Command("go", "build", "-o", binary, "../../cmd/tunnelmesh-agent")
	build.Env = append(os.Environ(), "CGO_ENABLED=0")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build agent: %v\n%s", err, out)
	}

	// 2) 打包成与发布归档同构的 tar.gz（含服务模板）
	archive := filepath.Join(work, "tunnelmesh-v9.9.9-e2e.tar.gz")
	packArchive(t, archive, map[string]string{
		"tunnelmesh-agent":                           binary,
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
	if !strings.Contains(envBody, "TUNNELMESH_AGENT_TOKEN='e2e-secret-token-value'") {
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
	cmd := exec.Command(bash, append([]string{filepath.Join("install-agent.sh")}, args...)...)
	cmd.Env = env
	var out, errBuf bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errBuf
	if err := cmd.Run(); err != nil {
		t.Fatalf("installer failed: %v\nstdout:\n%s\nstderr:\n%s", err, out.String(), errBuf.String())
	}
	return runResult{stdout: out.String(), stderr: errBuf.String()}
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
		if strings.HasSuffix(name, "tunnelmesh-agent") && !strings.Contains(name, "/") {
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

var _ = sha256.Sum256
var _ = hex.EncodeToString
```

> 末尾两行 `var _ = ...` 只为占位，实现时删除并同步去掉 `crypto/sha256`、`encoding/hex` 两个 import——E2E 走 `--archive`，不做校验和断言（校验路径由 Task 2 的函数级测试覆盖）。

- [x] **Step 4: 运行 E2E 确认通过**

Run: `TM_ONECLICK_E2E=1 go test ./deploy/install/oneclick -run TestOneClickInstallUpgradeUninstall -count=1 -v`
Expected: PASS。首次运行会 `go build` agent 二进制（约 10–30s，有构建缓存）。
Run: `go test ./deploy/install/oneclick -count=1`
Expected: PASS 且 E2E 被 skip（`TM_ONECLICK_E2E` 未设置）。

- [x] **Step 5: Commit（需授权）**

```bash
git add deploy/install/oneclick
git commit -m "test(install): cover one-click install, upgrade and uninstall end to end"
```

---

### Task 9: 发布归档与产物一致性

**Files:**
- Modify: `scripts/build-release.sh`
- Modify: `scripts/install_script_test.go`（新增归档布局断言）

**Interfaces:**
- Produces: 归档内包含 `deploy/install/oneclick/`（不含 `testdata/`、不含 `*_test.go`），Linux 归档额外包含 `deploy/systemd-user/`。

- [x] **Step 1: 写失败测试**

在 `scripts/install_script_test.go` 追加：

```go
func TestBuildReleaseShipsOneClickInstallers(t *testing.T) {
	data, err := os.ReadFile("build-release.sh")
	if err != nil {
		t.Fatalf("read build-release.sh: %v", err)
	}
	script := string(data)
	for _, want := range []string{
		"deploy/install",            // oneclick/ 随 deploy/install 一起进归档
		"deploy/systemd-user",       // user 模式单元模板必须进 Linux 归档
		`-name '*_test.go'`,         // 既有：测试文件不发布
		"-name testdata",            // 新增：测试 fixture 不发布
	} {
		if !strings.Contains(script, want) {
			t.Errorf("build-release.sh is missing %q", want)
		}
	}
}
```

Run: `go test ./scripts -run TestBuildReleaseShipsOneClickInstallers -count=1`
Expected: FAIL —— `build-release.sh is missing "deploy/systemd-user"` 与 `"-name testdata"`。

- [x] **Step 2: 写最小实现**

`scripts/build-release.sh` 中 Linux 分支的 `cp -R "$ROOT_DIR/deploy/systemd" "$stage/deploy/systemd"` 之后追加：

```bash
      cp -R "$ROOT_DIR/deploy/systemd-user" "$stage/deploy/systemd-user"
```

在既有 `find "$stage/deploy" -name '*_test.go' -type f -delete` 之后追加：

```bash
  # testdata/ 是测试 fixture（含 curl/systemctl stub 与现场生成的归档），不是部署产物。
  find "$stage/deploy" -type d -name testdata -prune -exec rm -rf {} +
```

- [x] **Step 3: 验证**

Run: `go test ./scripts -count=1 && bash -n scripts/build-release.sh`
Expected: PASS，语法检查退出码 0。

Run（仅当 `internal/server/web_dist` 已由 Task 0 生成时可行）：`VERSION=v0.0.0-e2e ./scripts/build-release.sh && tar -tzf dist/v0.0.0-e2e/tunnelmesh-v0.0.0-e2e-darwin-arm64.tar.gz | grep -E 'oneclick|systemd-user|testdata'`
Expected: 输出包含 `deploy/install/oneclick/install-agent.sh`、`deploy/install/oneclick/tunnelmesh-install-common.sh`、`deploy/systemd-user/tunnelmesh-agent.service`，且**不含** `testdata`。

- [x] **Step 4: Commit（需授权）**

```bash
git add scripts/build-release.sh scripts/install_script_test.go
git commit -m "build(release): ship one-click installers and user systemd templates"
```

---

### Task 10: 文档与索引

**Files:**
- Create: `docs/deployment/oneclick-install.md`
- Modify: `README.md`、`docs/README.md`、`deploy/README.md`、`docs/deployment/binary-release.md`、`docs/deployment/linux-systemd.md`、`docs/deployment/macos-launchd.md`
- Regenerate: `docs/superpowers/specs/README.md`、`docs/superpowers/plans/README.md`

**Interfaces:** 无代码接口；文档必须与脚本实际 flag、退出码、路径完全一致（Task 12 的一致性检查会逐条核对）。

- [x] **Step 1: 写 `docs/deployment/oneclick-install.md`**

必须包含以下小节，且「安装命令」小节要把**两种一行式调用形态都写出来**：

```markdown
# 一键安装脚本

三个角色各一条命令，交互式完成下载、校验、配置生成、服务注册与启动。

## 安装命令

推荐先下载再审阅（失败可见，且能先看脚本内容）：

```sh
curl -fsSL https://raw.githubusercontent.com/nnworld/TunnelMesh/main/deploy/install/oneclick/install-agent.sh \
  -o /tmp/tunnelmesh-install-agent.sh
less /tmp/tunnelmesh-install-agent.sh
/bin/bash /tmp/tunnelmesh-install-agent.sh
```

一行式（Homebrew 风格，命令替换）：

```sh
/bin/bash -c "$(curl -fsSL https://raw.githubusercontent.com/nnworld/TunnelMesh/main/deploy/install/oneclick/install-agent.sh)"
```

一行式（管道）：

```sh
curl -fsSL https://raw.githubusercontent.com/nnworld/TunnelMesh/main/deploy/install/oneclick/install-agent.sh | bash -s -- --version v1.1.1
```

> `bash -c "$(curl ...)"` 有一个失效模式：curl 失败时命令替换为空串，bash 什么都不执行却返回 0。
> 在脚本化场景请用 `curl ... -o file && /bin/bash file`，或先 `set -o pipefail`。
> 传参时注意 `bash -c '<script>' <name> [args...]` 的第一个参数会被当作 `$0` 吃掉，
> 因此要么写一个占位名，要么改用环境变量：`TM_ONECLICK_YES=1 /bin/bash -c "$(curl -fsSL <url>)"`。
```

其余小节（标题固定，便于交叉引用）：

1. `## 平台与托管矩阵`：复制规格 §6 的表格，补上「Windows 见 install-<role>.ps1」。
2. `## 交互项`：三角色各一张表，列「提示 / 默认值 / 对应 flag」，与 `tm_role_prompts` 逐项对齐。
3. `## 非交互安装（CI）`：给三条完整命令——`--yes` + `--server-url` + `TUNNELMESH_AGENT_TOKEN` 环境变量；并解释**为什么没有 `--token`**（进程列表与 shell 历史泄露），给出 `--token-file`、`--secret-env-file` 两种替代与其权限要求（0600/0400/0640）。
4. `## 安装到其它用户`：user 模式 `--user <name>`（需 root，脚本自动 re-exec）、system 模式 `--run-user <name>`（drop-in 覆盖 `User=`/`Group=`），各给一条完整命令。
5. `## 升级、回滚与卸载`：重复运行即升级、保留配置、`--reconfigure` 重新生成、`.bak-<ts>` 自动回滚、`--uninstall` 保留配置与数据。
6. `## 镜像源与离线安装`：`--base-url`、`--raw-base-url`、`TM_ONECLICK_REF`、`--archive`、下载缓存位置与 `--no-cache`。
7. `## 退出码与排障`：0/2/3/4/5/6/7/8 逐条含义 + 每条的最可能原因与处置。
8. `## 信任模型`：`curl | bash` 的风险、SHA256SUMS 校验、`raw` 与 Release 同源、如何离线审阅。

- [x] **Step 2: 更新 `README.md` 的 Quick start → Install**

把现有 `scripts/install.sh` 代码块替换为（保留「先下载再审阅」措辞，并列出两种一行式）：

```markdown
### Install

One command per role. Download the installer, review it, then run it:

```sh
curl --fail --silent --show-error --location \
  https://raw.githubusercontent.com/nnworld/TunnelMesh/main/deploy/install/oneclick/install-agent.sh \
  --output /tmp/tunnelmesh-install-agent.sh
less /tmp/tunnelmesh-install-agent.sh
/bin/bash /tmp/tunnelmesh-install-agent.sh
```

Prefer a one-liner? Both forms work:

```sh
/bin/bash -c "$(curl -fsSL https://raw.githubusercontent.com/nnworld/TunnelMesh/main/deploy/install/oneclick/install-agent.sh)"
curl -fsSL https://raw.githubusercontent.com/nnworld/TunnelMesh/main/deploy/install/oneclick/install-agent.sh | bash -s -- --version v1.1.1
```

The installer asks for the server URL and token, writes the configuration, registers a
systemd user unit (Linux), a LaunchAgent (macOS), or a WinSW service (Windows, use
`install-agent.ps1`), then starts and verifies it. Add `--yes` for unattended installs.
To install only the three binaries without service registration, use
`scripts/install.sh` (default `~/.local/bin`). Full guide:
[one-click install](docs/deployment/oneclick-install.md).
```

- [x] **Step 3: 更新 `docs/README.md` 与 `deploy/README.md`**

`docs/README.md` 部署清单里，在 `[Linux systemd 安装](deployment/linux-systemd.md)` 那一行**之前**插入：

```markdown
- [一键安装脚本](deployment/oneclick-install.md)：三角色一条命令，交互/非交互、升级与卸载、镜像源与离线安装
```

`deploy/README.md`：目录表新增 `install/oneclick/`、`systemd-user/` 两行；「模板约定」补 `__ENVIRONMENT__`、`__ENV_BLOCK__`、user 单元四占位符与 drop-in 规则；「发布包内容」表把 Linux 一列改成 `deploy/install`、`deploy/systemd`、`deploy/systemd-user`；「校验」代码块追加：

```bash
go test ./deploy/install/oneclick -count=1                 # 一键安装脚本契约与函数级测试
TM_ONECLICK_E2E=1 go test ./deploy/install/oneclick -count=1  # 端到端：安装 → 升级 → 卸载
bash -n deploy/install/oneclick/*.sh
```

- [x] **Step 4: 更新三篇部署文档**

- `docs/deployment/binary-release.md`：归档内容表补 `deploy/install/oneclick/`（三角色入口 + 共享库 + `winsw-checksums.txt`）与 `deploy/systemd-user/`；说明 `testdata/`、`*_test.go` 不进归档。
- `docs/deployment/linux-systemd.md`：开头加「推荐先用[一键安装脚本](oneclick-install.md)」；补 `agent.env`（`EnvironmentFile=-/etc/tunnelmesh/agent.env`）；新增「用户级安装」小节（`~/.config/systemd/user/`、`loginctl enable-linger`、日志用 `journalctl --user`）；新增「drop-in 覆盖」小节（`oneclick.conf` 何时生成、如何手工改）。
- `docs/deployment/macos-launchd.md`：补 `EnvironmentVariables` 渲染说明、plist 权限 0600、token 不写进模板仓库副本、卸载命令。

- [x] **Step 5: 重建索引并验证**

Run: `python3 scripts/gen_doc_index.py && git status --short docs`
Expected: 只有四份 `README.md` 索引与新增文档发生变化，无其它 diff。

Run: `grep -rn "oneclick-install.md" README.md docs/README.md deploy/README.md`
Expected: 三处都有入口链接（避免孤儿文档）。

- [x] **Step 6: Commit（需授权）** —— 已按用户授权合并为收尾单次提交，提交信息见 PR 记录

```bash
git add README.md docs deploy/README.md
git commit -m "docs(install): document one-click installers and invocation forms"
```

---

### Task 6: 安装模式判定与服务生命周期

**Files:**
- Modify: `deploy/install/oneclick/tunnelmesh-install-common.sh`、`testdata/run_tests.sh`
- Create: `deploy/install/oneclick/testdata/bin/systemctl`、`launchctl`、`loginctl`、`plutil`（记录调用序列的 stub）

**Interfaces:**
- Consumes: `tm_ans_get/set`、`tm_die`、`tm_info`、`tm_warn`、`tm_have`、`TM_DEFAULT_INSTALL_MODE`、渲染函数（Task 4）、`TM_MODE`/`TM_TARGET_USER`/`TM_RUN_USER`（Task 3）。
- Produces:
  - `tm_euid`（`id -u`，测试可重定义该函数注入身份）、`tm_is_root`
  - `tm_resolve_install_mode`：按规格 §10 的四条规则设置 `TM_MODE`、`TM_TARGET_USER`、`TM_RUN_USER`；规则 2 命中时打印一行说明；`--mode system` 且非 root → `tm_die 3`
  - `tm_reexec_as_user <user> <args...>`：`runuser -u <user> --` 优先，回退 `sudo -u <user> -H`；用 `TM_ONECLICK_REEXEC=1` 防环，已置位时直接 `tm_die 3`
  - `tm_has_systemd`、`tm_has_systemd_user`（后者检查 `systemctl --user` 是否可用，容器/WSL 里常缺失）
  - `tm_default_bin_dir`、`tm_default_config_dir`、`tm_default_state_dir`、`tm_binary_path <role>`、`tm_config_path <role>`、`tm_env_path <role>`、`tm_unit_path <role>`（按 `TM_MODE` 与 `TM_OS_FAMILY` 返回路径）
  - `tm_service_install <role>`、`tm_service_enable <role>`、`tm_service_start <role>`、`tm_service_stop <role>`、`tm_service_remove <role>`、`tm_service_status <role>`、`tm_service_logs_hint <role>`
  - 全局 `TM_SERVICE_REGISTERED`（0/1）、`TM_SERVICE_MANAGER`（systemd-system|systemd-user|launchd|winsw|none）

- [x] **Step 1: 写失败测试 —— 服务命令 stub**

创建 `deploy/install/oneclick/testdata/bin/systemctl`（可执行；`launchctl`、`loginctl`、`plutil` 同构，只改程序名）：

```bash
#!/usr/bin/env bash
# 服务管理 stub：把「程序名 + 全部参数」追加写入 $SVC_STUB_LOG，永远成功。
# plutil 需要 -lint 时同样只记录不校验（渲染结果由 run_tests.sh 单独断言）。
set -uo pipefail
printf '%s %s\n' "$(basename "$0")" "$*" >>"${SVC_STUB_LOG:-/dev/null}"
exit 0
```

- [x] **Step 2: 写失败测试 —— 追加到 `testdata/run_tests.sh`**

```bash
# --- Task 6: 模式判定与服务生命周期 ---
SVCLOG="$TM_TMP6/svc.log"; TM_TMP6="$(mktemp -d)"; mkdir -p "$TM_TMP6"
export SVC_STUB_LOG="$SVCLOG"
export PATH="$HERE/bin:$PATH"

# 规则 1：非 root → user
tm_euid() { printf '1000\n'; }
TM_MODE="" TM_TARGET_USER="$(id -un)" SUDO_USER=""
out="$(tm_resolve_install_mode_stdout)"
assert_eq "mode/non-root" "user" "$out"

# 规则 2：root + SUDO_USER → user，目标用户取 SUDO_USER
tm_euid() { printf '0\n'; }
out="$(SUDO_USER=alice tm_resolve_install_mode_stdout)"
assert_eq "mode/sudo-user" "user" "$out"
assert_eq "mode/sudo-target" "alice" "$(SUDO_USER=alice tm_resolve_target_user)"

# 规则 3：root 无 SUDO_USER → system
out="$(SUDO_USER= tm_resolve_install_mode_stdout)"
assert_eq "mode/real-root" "system" "$out"

# 规则 4：显式 --mode system 但非 root → 退出码 3
tm_euid() { printf '1000\n'; }
assert_exit_sh 3 "mode/system-needs-root" "tm_euid() { printf '1000\n'; }; TM_MODE=system tm_resolve_install_mode"

# 显式 --mode user 时即使 root 也保持 user
tm_euid() { printf '0\n'; }
assert_eq "mode/explicit-user" "user" "$(SUDO_USER= TM_MODE=user tm_resolve_install_mode_stdout)"

# 路径推导
TM_MODE="user" TM_OS_FAMILY="linux" TM_TARGET_USER="$(id -un)" TM_BIN_DIR="" TM_CONFIG_DIR="" TM_STATE_DIR=""
assert_eq "path/user-binary" "$HOME/.local/bin/tunnelmesh-agent" "$(tm_binary_path agent)"
assert_eq "path/user-config" "$HOME/.config/tunnelmesh/agent.yaml" "$(tm_config_path agent)"
assert_eq "path/user-env" "$HOME/.config/tunnelmesh/agent.env" "$(tm_env_path agent)"
assert_eq "path/user-unit" "$HOME/.config/systemd/user/tunnelmesh-agent.service" "$(tm_unit_path agent)"
assert_eq "path/user-state" "$HOME/.local/share/tunnelmesh" "$(tm_default_state_dir)"
TM_MODE="system"
assert_eq "path/system-binary" "/usr/local/bin/tunnelmesh-agent" "$(TM_BIN_DIR= tm_binary_path agent)"
assert_eq "path/system-config" "/etc/tunnelmesh/agent.yaml" "$(TM_CONFIG_DIR= tm_config_path agent)"
assert_eq "path/system-state" "/var/lib/tunnelmesh-agent" "$(tm_default_state_dir)"
TM_MODE="user" TM_OS_FAMILY="darwin"
assert_eq "path/macos-unit" "$HOME/Library/LaunchAgents/com.tunnelmesh.agent.plist" "$(tm_unit_path agent)"
TM_MODE="user" TM_OS_FAMILY="linux"

# 服务生命周期：systemd user 调用序列
: >"$SVCLOG"
TM_SERVICE_MANAGER="systemd-user" tm_service_install agent
TM_SERVICE_MANAGER="systemd-user" tm_service_enable agent
TM_SERVICE_MANAGER="systemd-user" tm_service_start agent
assert_contains "svc/user-daemon-reload" "$(cat "$SVCLOG")" "systemctl --user daemon-reload"
assert_contains "svc/user-enable" "$(cat "$SVCLOG")" "systemctl --user enable tunnelmesh-agent.service"
assert_contains "svc/user-start" "$(cat "$SVCLOG")" "systemctl --user restart tunnelmesh-agent.service"

# 服务生命周期：systemd system 调用序列
: >"$SVCLOG"
TM_SERVICE_MANAGER="systemd-system" tm_service_install server
TM_SERVICE_MANAGER="systemd-system" tm_service_enable server
assert_contains "svc/system-daemon-reload" "$(cat "$SVCLOG")" "systemctl daemon-reload"
assert_contains "svc/system-enable" "$(cat "$SVCLOG")" "systemctl enable tunnelmesh-server.service"

# 服务生命周期：launchd 调用序列
: >"$SVCLOG"
TM_SERVICE_MANAGER="launchd" tm_service_install agent
TM_SERVICE_MANAGER="launchd" tm_service_start agent
assert_contains "svc/launchd-bootout" "$(cat "$SVCLOG")" "launchctl bootout gui/$(id -u)"
assert_contains "svc/launchd-bootstrap" "$(cat "$SVCLOG")" "launchctl bootstrap gui/$(id -u)"
assert_contains "svc/launchd-kickstart" "$(cat "$SVCLOG")" "launchctl kickstart -k gui/$(id -u)/com.tunnelmesh.agent"

# 无服务管理器：不失败，只标记未注册
: >"$SVCLOG"
TM_SERVICE_MANAGER="none" tm_service_install agent
assert_eq "svc/none-noop" "0" "$TM_SERVICE_REGISTERED"
assert_eq "svc/none-no-calls" "0" "$(wc -l <"$SVCLOG" | tr -d ' ')"
rm -rf "$TM_TMP6"
```

> `tm_resolve_install_mode_stdout` 与 `tm_resolve_target_user` 是为测试拆出的纯函数：前者解析后只打印 `TM_MODE`，后者只打印 `TM_TARGET_USER`。`tm_resolve_install_mode` = 两者 + 说明性输出。测试用「重定义 `tm_euid`」注入身份，这是刻意的可测性接缝，生产代码里 `tm_euid` 只有一行 `id -u`。

- [x] **Step 3: 运行测试确认失败**

Run: `go test ./deploy/install/oneclick -run TestShellFunctionSuite -count=1`
Expected: FAIL —— `tm_resolve_install_mode_stdout: command not found`。

- [x] **Step 4: 写最小实现**

```bash
# --- 身份与模式 ---
tm_euid() { id -u; }
tm_is_root() { [[ "$(tm_euid)" == "0" ]]; }

tm_resolve_target_user() {
  if [[ -n "$TM_TARGET_USER" ]]; then printf '%s\n' "$TM_TARGET_USER"; return 0; fi
  if tm_is_root && [[ -n "${SUDO_USER:-}" && "${SUDO_USER}" != "root" ]]; then
    printf '%s\n' "$SUDO_USER"; return 0
  fi
  id -un
}

tm_resolve_install_mode_stdout() {
  local mode="$TM_MODE"
  if [[ -z "$mode" ]]; then
    if [[ "$TM_OS_FAMILY" != "linux" ]]; then mode="user"
    elif ! tm_is_root; then mode="$TM_DEFAULT_INSTALL_MODE"
    elif [[ -n "${SUDO_USER:-}" && "${SUDO_USER}" != "root" ]]; then mode="user"
    else mode="system"; fi
  fi
  printf '%s\n' "$mode"
}

tm_resolve_install_mode() {
  TM_MODE="$(tm_resolve_install_mode_stdout)"
  if [[ "$TM_OS_FAMILY" != "linux" && "$TM_MODE" == "system" ]]; then
    tm_warn "system 模式只在 Linux 生效；$(uname -s) 上使用用户级服务托管"
    TM_MODE="user"
  fi
  if [[ "$TM_MODE" == "system" ]] && ! tm_is_root; then
    tm_die "$TM_EXIT_PREFLIGHT" "--mode system requires root; re-run with sudo, or use the default user mode"
  fi
  TM_TARGET_USER="$(tm_resolve_target_user)"
  if [[ -z "$TM_RUN_USER" ]]; then TM_RUN_USER="tunnelmesh"; fi
  if tm_is_root && [[ -n "${SUDO_USER:-}" && "${SUDO_USER}" != "root" && "$TM_MODE" == "user" ]]; then
    tm_info "检测到 sudo：将以 ${TM_TARGET_USER} 身份做用户级安装；需要系统级请加 --mode system"
  fi
}

tm_reexec_as_user() { # <user> <args...>
  local user="$1"; shift
  [[ "${TM_ONECLICK_REEXEC:-0}" == "1" ]] && tm_die "$TM_EXIT_PREFLIGHT" "refusing to re-exec twice (TM_ONECLICK_REEXEC already set)"
  [[ -n "$TM_ONECLICK_ENTRY" ]] || tm_die "$TM_EXIT_PREFLIGHT" "cannot re-exec: entry script path unknown (bash -c/管道调用请改用目标用户直接执行)"
  if tm_have runuser; then
    TM_ONECLICK_REEXEC=1 runuser -u "$user" -- "$TM_ONECLICK_ENTRY" "$@"
  elif tm_have sudo; then
    TM_ONECLICK_REEXEC=1 sudo -u "$user" -H "$TM_ONECLICK_ENTRY" "$@"
  else
    tm_die "$TM_EXIT_PREFLIGHT" "need runuser or sudo to install for another user"
  fi
}

# --- 路径推导 ---
tm_default_bin_dir() {
  if [[ -n "$TM_BIN_DIR" ]]; then printf '%s\n' "$TM_BIN_DIR"
  elif [[ "$TM_OS_FAMILY" == "linux" && "$TM_MODE" == "system" ]]; then printf '/usr/local/bin\n'
  else printf '%s/.local/bin\n' "$HOME"; fi
}
tm_default_config_dir() {
  if [[ -n "$TM_CONFIG_DIR" ]]; then printf '%s\n' "$TM_CONFIG_DIR"
  elif [[ "$TM_OS_FAMILY" == "linux" && "$TM_MODE" == "system" ]]; then printf '/etc/tunnelmesh\n'
  else printf '%s/.config/tunnelmesh\n' "$HOME"; fi
}
tm_default_state_dir() {
  if [[ -n "$TM_STATE_DIR" ]]; then printf '%s\n' "$TM_STATE_DIR"
  elif [[ "$TM_OS_FAMILY" == "linux" && "$TM_MODE" == "system" ]]; then
    case "$TM_ROLE" in server) printf '/var/lib/tunnelmesh\n' ;; *) printf '/var/lib/tunnelmesh-%s\n' "$TM_ROLE" ;; esac
  else printf '%s/.local/share/tunnelmesh\n' "$HOME"; fi
}
tm_binary_path() { printf '%s/tunnelmesh-%s\n' "$(tm_default_bin_dir)" "$1"; }
tm_config_path() { printf '%s/%s.yaml\n' "$(tm_default_config_dir)" "$1"; }
tm_env_path() { printf '%s/%s.env\n' "$(tm_default_config_dir)" "$1"; }
tm_unit_path() {
  case "$TM_SERVICE_MANAGER" in
    systemd-user) printf '%s/.config/systemd/user/tunnelmesh-%s.service\n' "$HOME" "$1" ;;
    systemd-system) printf '/etc/systemd/system/tunnelmesh-%s.service\n' "$1" ;;
    launchd) printf '%s/Library/LaunchAgents/com.tunnelmesh.%s.plist\n' "$HOME" "$1" ;;
    *) printf '%s\n' "" ;;
  esac
}

# --- 服务管理器探测 ---
tm_has_systemd() { tm_have systemctl && [[ -d /run/systemd/system ]]; }
tm_has_systemd_user() { tm_has_systemd && systemctl --user show-environment >/dev/null 2>&1; }
tm_detect_service_manager() {
  if [[ "$TM_NO_SERVICE" == "1" ]]; then TM_SERVICE_MANAGER="none"; return 0; fi
  case "$TM_OS_FAMILY" in
    darwin) tm_have launchctl && TM_SERVICE_MANAGER="launchd" || TM_SERVICE_MANAGER="none" ;;
    linux)
      if [[ "$TM_MODE" == "system" ]] && tm_has_systemd; then TM_SERVICE_MANAGER="systemd-system"
      elif tm_has_systemd_user; then TM_SERVICE_MANAGER="systemd-user"
      else TM_SERVICE_MANAGER="none"; fi ;;
    *) TM_SERVICE_MANAGER="none" ;;
  esac
  if [[ "$TM_SERVICE_MANAGER" == "none" && "$TM_NO_SERVICE" != "1" ]]; then
    tm_warn "未检测到可用的服务管理器，将只安装二进制与配置；前台启动命令见安装结束后的摘要"
  fi
}

# --- 服务生命周期：所有调用都经过 tm_svc，测试用 PATH 前置的 stub 覆盖 ---
tm_svc() { "$@"; }
tm_service_install() { # <role>
  local role="$1" unit
  TM_SERVICE_REGISTERED=1
  case "$TM_SERVICE_MANAGER" in
    systemd-user) tm_svc systemctl --user daemon-reload ;;
    systemd-system) tm_svc systemctl daemon-reload ;;
    launchd)
      unit="$(tm_unit_path "$role")"
      tm_svc plutil -lint "$unit" || tm_die "$TM_EXIT_CONFIG" "plutil -lint failed for ${unit}"
      tm_svc launchctl bootout "gui/$(id -u)" "$unit" >/dev/null 2>&1 || true
      tm_svc launchctl bootstrap "gui/$(id -u)" "$(dirname "$unit")" \
        || tm_die "$TM_EXIT_SERVICE" "launchctl bootstrap failed for ${unit}" ;;
    none) TM_SERVICE_REGISTERED=0 ;;
    *) tm_die "$TM_EXIT_SERVICE" "unsupported service manager: $TM_SERVICE_MANAGER" ;;
  esac
}
tm_service_enable() { # <role>
  [[ "$TM_SERVICE_REGISTERED" == "1" ]] || return 0
  case "$TM_SERVICE_MANAGER" in
    systemd-user)
      tm_svc systemctl --user enable "tunnelmesh-$1.service" || tm_die "$TM_EXIT_SERVICE" "systemctl --user enable failed"
      if [[ "$TM_NO_LINGER" != "1" ]] && tm_have loginctl; then
        tm_svc loginctl enable-linger "$TM_TARGET_USER" \
          || tm_warn "loginctl enable-linger 失败：注销后服务会被停止，可手工执行 loginctl enable-linger ${TM_TARGET_USER}"
      fi ;;
    systemd-system) tm_svc systemctl enable "tunnelmesh-$1.service" || tm_die "$TM_EXIT_SERVICE" "systemctl enable failed" ;;
    launchd) : ;;  # RunAtLoad + KeepAlive 已在 plist 内
  esac
}
tm_service_start() { # <role>
  [[ "$TM_SERVICE_REGISTERED" == "1" ]] || return 0
  case "$TM_SERVICE_MANAGER" in
    systemd-user) tm_svc systemctl --user restart "tunnelmesh-$1.service" || tm_die "$TM_EXIT_SERVICE" "start failed" ;;
    systemd-system) tm_svc systemctl restart "tunnelmesh-$1.service" || tm_die "$TM_EXIT_SERVICE" "start failed" ;;
    launchd) tm_svc launchctl kickstart -k "gui/$(id -u)/com.tunnelmesh.$1" || tm_die "$TM_EXIT_SERVICE" "kickstart failed" ;;
  esac
}
tm_service_stop() { # <role>
  case "$TM_SERVICE_MANAGER" in
    systemd-user) tm_svc systemctl --user stop "tunnelmesh-$1.service" >/dev/null 2>&1 || true ;;
    systemd-system) tm_svc systemctl stop "tunnelmesh-$1.service" >/dev/null 2>&1 || true ;;
    launchd) tm_svc launchctl bootout "gui/$(id -u)/com.tunnelmesh.$1" >/dev/null 2>&1 || true ;;
  esac
}
tm_service_remove() { # <role>
  local unit; unit="$(tm_unit_path "$1")"
  tm_service_stop "$1"
  case "$TM_SERVICE_MANAGER" in
    systemd-user) tm_svc systemctl --user disable "tunnelmesh-$1.service" >/dev/null 2>&1 || true; tm_svc systemctl --user daemon-reload || true ;;
    systemd-system)
      tm_svc systemctl disable "tunnelmesh-$1.service" >/dev/null 2>&1 || true
      rm -f "/etc/systemd/system/tunnelmesh-$1.service.d/oneclick.conf"
      rmdir "/etc/systemd/system/tunnelmesh-$1.service.d" 2>/dev/null || true
      rm -f "/etc/systemd/system/tunnelmesh-$1.service"
      tm_svc systemctl daemon-reload || true ;;
    launchd) rm -f "$unit" ;;
  esac
}
tm_service_status() { # <role>
  case "$TM_SERVICE_MANAGER" in
    systemd-user) tm_svc systemctl --user --no-pager --lines=10 status "tunnelmesh-$1.service" || true ;;
    systemd-system) tm_svc systemctl --no-pager --lines=10 status "tunnelmesh-$1.service" || true ;;
    launchd) tm_svc launchctl print "gui/$(id -u)/com.tunnelmesh.$1" || true ;;
    none) tm_warn "未注册服务" ;;
  esac
}
tm_service_logs_hint() {
  case "$TM_SERVICE_MANAGER" in
    systemd-user) printf 'journalctl --user -u tunnelmesh-%s.service -f\n' "$TM_ROLE" ;;
    systemd-system) printf 'journalctl -u tunnelmesh-%s.service -f\n' "$TM_ROLE" ;;
    launchd) printf 'tail -f %s/Library/Logs/tunnelmesh-%s.log\n' "$HOME" "$TM_ROLE" ;;
    none) printf '%s --config %s run\n' "$(tm_binary_path "$TM_ROLE")" "$(tm_config_path "$TM_ROLE")" ;;
  esac
}
```

- [x] **Step 5: 运行测试确认通过**

Run: `go test ./deploy/install/oneclick -count=1`
Expected: PASS，新增 `ok   mode/*`、`ok   path/*`、`ok   svc/*` 全绿。

- [x] **Step 6: Commit（需授权）** —— 已按用户授权合并为收尾单次提交，提交信息见 PR 记录

```bash
git add deploy/install/oneclick
git commit -m "feat(install): resolve install mode and drive service lifecycle"
```

---

### Task 7: `tm_main` 编排与三个角色入口

**Files:**
- Modify: `deploy/install/oneclick/tunnelmesh-install-common.sh`（`tm_preflight`、`tm_install_binary`、`tm_run_validate`、`tm_rollback`、`tm_uninstall`、`tm_summary`、`tm_cleanup`、`tm_main`、`tm_usage`）
- Create: `deploy/install/oneclick/install-server.sh`、`install-agent.sh`、`install-client.sh`
- Modify: `deploy/install/oneclick/oneclick_scripts_test.go`（入口薄壳与 flag 面断言）

**Interfaces:**
- Consumes: 前面全部 Task 的函数。
- Produces:
  - `tm_cleanup`（`trap` 目标，删 `TM_WORKDIR` 与渲染临时文件）
  - `tm_preflight`（`tm_detect_platform` → `tm_tty_init` → `tm_entry_init` → `tm_resolve_install_mode` → `tm_detect_service_manager` → `tm_require_cmds`）
  - `tm_install_binary <role>`（原子替换 + `.bak-<ts>` 备份，只留最近一份）
  - `tm_rollback_binary <role>`
  - `tm_run_validate <role>`（server 先 `init-node-id` 再 `check-config`；其余只 `check-config`；失败 `tm_die 6` 并回显原始错误）
  - `tm_uninstall <role>`
  - `tm_summary`
  - `tm_main "$@"`：`tm_parse_common_args` → `tm_role_parse_args` → 卸载分支 / 安装分支（8 阶段）
  - `tm_usage`：包含全部通用 flag 与五种调用形态示例（`bash -c "$(curl ...)"` 与 `curl ... | bash -s --` 都必须在内）
  - 角色钩子（由各入口定义）：`tm_role_prompts`、`tm_role_render_config`、`tm_role_post_install`、`tm_role_parse_args`（可选，默认空实现）

- [x] **Step 1: 写失败测试 —— 入口薄壳与 flag 面**

在 `oneclick_scripts_test.go` 追加：

```go
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
		for _, want := range []string{"tm_load_lib", "tm_main \"$@\"", "tm_role_prompts", "tm_role_render_config", "tm_role_post_install", "set -euo pipefail"} {
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
```

- [x] **Step 2: 运行测试确认失败**

Run: `go test ./deploy/install/oneclick -run 'TestEntryScriptsStayThin' -count=1`
Expected: FAIL —— `open install-server.sh: no such file or directory`。

- [x] **Step 3: 写最小实现 —— 共享库编排部分**

```bash
tm_cleanup() {
  [[ -n "${TM_WORKDIR:-}" && -d "$TM_WORKDIR" ]] && rm -rf -- "$TM_WORKDIR"
  [[ -n "${TM_RENDER_TMP:-}" && -d "$TM_RENDER_TMP" ]] && rm -rf -- "$TM_RENDER_TMP"
  return 0
}

tm_preflight() {
  tm_detect_platform
  tm_tty_init
  tm_entry_init "${0:-}" "${BASH_SOURCE[0]:-}"
  tm_resolve_install_mode
  tm_detect_service_manager
  tm_require_cmds curl tar awk sed grep head install mktemp date
  TM_RENDER_TMP="$(mktemp -d)"
}

# tm_install_binary：install 到同目录临时名再 mv，避免替换期间进程读到半个文件。
tm_install_binary() { # <role>
  local role="$1" src dest tmp ts
  src="${TM_EXTRACT_DIR}/tunnelmesh-${role}"
  [[ -x "$src" ]] || tm_die "$TM_EXIT_DOWNLOAD" "release archive is missing tunnelmesh-${role}"
  dest="$(tm_binary_path "$role")"
  mkdir -p "$(dirname "$dest")"
  if [[ -e "$dest" ]]; then
    ts="$(date -u +%Y%m%dT%H%M%SZ)"
    cp -p -- "$dest" "${dest}.bak-${ts}"
    ls -1t "${dest}".bak-* 2>/dev/null | tail -n +2 | while IFS= read -r old; do rm -f -- "$old"; done
    TM_BINARY_BACKUP="${dest}.bak-${ts}"
  else
    TM_BINARY_BACKUP=""
  fi
  tmp="${dest}.tmp.$$"
  install -m 0755 "$src" "$tmp"
  mv -f -- "$tmp" "$dest"
  tm_info "installed ${dest}"
}

tm_rollback_binary() { # <role>
  local role="$1" dest; dest="$(tm_binary_path "$role")"
  [[ -n "${TM_BINARY_BACKUP:-}" && -f "$TM_BINARY_BACKUP" ]] || return 0
  tm_warn "回滚到 ${TM_BINARY_BACKUP}"
  tm_service_stop "$role"
  install -m 0755 "$TM_BINARY_BACKUP" "$dest"
  tm_service_start "$role" || true
}

tm_run_validate() { # <role>
  local role="$1" binary config
  binary="$(tm_binary_path "$role")"; config="$(tm_config_path "$role")"
  if [[ "$role" == "server" ]]; then
    "$binary" --config "$config" init-node-id || tm_die "$TM_EXIT_CONFIG" "init-node-id failed"
  fi
  if ! "$binary" --config "$config" check-config; then
    tm_die "$TM_EXIT_CONFIG" "check-config failed；请检查 ${config}（升级后 schema 不匹配见 docs/operations/schema-upgrades.md）"
  fi
}

tm_write_role_config() { # <role>
  local role="$1" config envfile yaml_tmp env_tmp mode_yaml mode_env
  config="$(tm_config_path "$role")"; envfile="$(tm_env_path "$role")"
  yaml_tmp="${TM_RENDER_TMP}/${role}.yaml"; env_tmp="${TM_RENDER_TMP}/${role}.env"
  tm_env_clear
  tm_role_render_config >"$yaml_tmp"
  if [[ "$TM_KEEP_CONFIG" == "1" && -f "$config" ]]; then
    tm_info "--keep-config：保留现有 ${config}"
  else
    if [[ "$TM_MODE" == "system" && "$TM_OS_FAMILY" == "linux" ]]; then mode_yaml=0640; else mode_yaml=0600; fi
    tm_backup_and_overwrite "$config" "$mode_yaml" "$yaml_tmp"
    tm_file_owner_set "$config" "$TM_CONFIG_OWNER" "$TM_CONFIG_GROUP"
  fi
  if [[ ${#TM_ENV_KEYS[@]} -gt 0 ]]; then
    tm_render_env_file >"$env_tmp"
    if [[ "$TM_MODE" == "system" && "$TM_OS_FAMILY" == "linux" ]]; then mode_env=0640; else mode_env=0600; fi
    tm_backup_and_overwrite "$envfile" "$mode_env" "$env_tmp"
    tm_file_owner_set "$envfile" "$TM_CONFIG_OWNER" "$TM_CONFIG_GROUP"
    tm_info "wrote secrets to ${envfile} (mode 0${mode_env})"
  fi
}

tm_register_service() { # <role>
  local role="$1" unit template
  unit="$(tm_unit_path "$role")"
  case "$TM_SERVICE_MANAGER" in
    systemd-user)
      template="${TM_EXTRACT_DIR}/deploy/systemd-user/tunnelmesh-${role}.service"
      [[ -f "$template" ]] || tm_die "$TM_EXIT_SERVICE" "release archive is missing ${template}"
      mkdir -p "$(dirname "$unit")"
      tm_render_systemd_user_unit "$role" "$template" "$(tm_binary_path "$role")" \
        "$(tm_config_path "$role")" "$(tm_env_path "$role")" "$(tm_default_state_dir)" >"${TM_RENDER_TMP}/unit"
      tm_write_file "$unit" 0644 "${TM_RENDER_TMP}/unit" ;;
    systemd-system)
      template="${TM_EXTRACT_DIR}/deploy/systemd/tunnelmesh-${role}.service"
      [[ -f "$template" ]] || tm_die "$TM_EXIT_SERVICE" "release archive is missing ${template}"
      tm_write_file "$unit" 0644 "$template"
      if [[ "$TM_RUN_USER" != "tunnelmesh" || "$TM_BIN_DIR" != "" || "$TM_CONFIG_DIR" != "" || "$TM_STATE_DIR" != "" ]]; then
        mkdir -p "/etc/systemd/system/tunnelmesh-${role}.service.d"
        tm_render_systemd_dropin "$role" "$(tm_binary_path "$role")" "$(tm_config_path "$role")" \
          "$(tm_env_path "$role")" "$(tm_default_state_dir)" "$TM_RUN_USER" "$TM_RUN_GROUP" \
          >"${TM_RENDER_TMP}/dropin"
        tm_write_file "/etc/systemd/system/tunnelmesh-${role}.service.d/oneclick.conf" 0644 "${TM_RENDER_TMP}/dropin"
      fi ;;
    launchd)
      template="${TM_EXTRACT_DIR}/deploy/macos/tunnelmesh.plist"
      [[ -f "$template" ]] || tm_die "$TM_EXIT_SERVICE" "release archive is missing ${template}"
      mkdir -p "$(dirname "$unit")" "$HOME/Library/Logs"
      tm_render_template "$template" \
        "__ROLE__" "$role" "__HOME__" "$HOME" \
        "__BINARY__" "$(tm_binary_path "$role")" "__CONFIG__" "$(tm_config_path "$role")" \
        >"${TM_RENDER_TMP}/plist"
      # __ENVIRONMENT__ 单独替换：它本身是多行 XML，不能走 sed。
      TM_ENV_BLOCK="$(tm_render_plist_environment)"
      tm_render_template "${TM_RENDER_TMP}/plist" "__ENVIRONMENT__" "$TM_ENV_BLOCK" >"${TM_RENDER_TMP}/plist.final"
      tm_write_file "$unit" 0600 "${TM_RENDER_TMP}/plist.final" ;;
    none) return 0 ;;
  esac
}

tm_uninstall() { # <role>
  local role="$1"
  tm_service_remove "$role" || tm_die "$TM_EXIT_UNINSTALL" "failed to remove service for ${role}"
  rm -f -- "$(tm_binary_path "$role")" "$(tm_binary_path "$role")".bak-*
  tm_info "已卸载 tunnelmesh-${role} 的二进制与服务定义；配置与数据保留："
  printf '  %s\n  %s\n  %s\n' "$(tm_config_path "$role")" "$(tm_env_path "$role")" "$(tm_default_state_dir)"
}

tm_summary() { # <role> <version>
  local role="$1" version="$2"
  printf '\n==> TunnelMesh %s %s 安装完成\n' "$role" "$version"
  printf '  二进制:   %s\n' "$(tm_binary_path "$role")"
  printf '  配置:     %s\n' "$(tm_config_path "$role")"
  [[ ${#TM_ENV_KEYS[@]} -gt 0 ]] && printf '  敏感值:   %s（%s）\n' "$(tm_env_path "$role")" "$(tm_env_masked_summary)"
  printf '  服务:     %s\n' "${TM_SERVICE_MANAGER}"
  printf '  查看日志: %s\n' "$(tm_service_logs_hint)"
  printf '  卸载:     重新运行本脚本并加 --uninstall\n'
  tm_role_post_install
}

tm_env_masked_summary() {
  local i out=""
  for i in "${!TM_ENV_KEYS[@]}"; do
    out="${out}${TM_ENV_KEYS[$i]}=$(tm_mask "${TM_ENV_VALUES[$i]}") "
  done
  printf '%s' "${out% }"
}

tm_main() {
  TM_REMAINING_ARGS=(); TM_TUNNEL_SPECS=()
  tm_parse_common_args "$@"
  tm_role_parse_args "${TM_REMAINING_ARGS[@]}"
  trap tm_cleanup EXIT INT TERM
  tm_preflight
  tm_role_prompts
  if [[ "$TM_UNINSTALL" == "1" ]]; then
    tm_uninstall "$TM_ROLE"; exit "$TM_EXIT_OK"
  fi
  if [[ -n "$TM_ARCHIVE" ]]; then
    tm_validate_version "$TM_VERSION" 2>/dev/null || TM_VERSION="$(tm_version_from_archive "$TM_ARCHIVE")"
    TM_WORKDIR="$(mktemp -d)"; TM_EXTRACT_DIR="${TM_WORKDIR}/extract"
    tm_verify_checksum "$TM_ARCHIVE" "${TM_WORKDIR}/SHA256SUMS" "$(basename "$TM_ARCHIVE")" 2>/dev/null || \
      tm_warn "本地归档未提供 SHA256SUMS，跳过校验（--archive 模式）"
    tm_extract_archive "$TM_ARCHIVE" "$TM_EXTRACT_DIR"
  else
    tm_resolve_version
    tm_download_release "$TM_VERSION"
    TM_EXTRACT_DIR="${TM_WORKDIR}/extract"
    tm_extract_archive "$TM_ARCHIVE_PATH" "$TM_EXTRACT_DIR"
  fi
  local installed_version=""
  if [[ -x "$(tm_binary_path "$TM_ROLE")" ]]; then
    installed_version="$("$(tm_binary_path "$TM_ROLE")" --version 2>/dev/null | awk '{print $2}')"
    tm_info "已安装 ${installed_version}，目标 ${TM_VERSION}：按升级流程处理（保留配置）"
  fi
  mkdir -p "$(tm_default_state_dir)" "$(tm_default_config_dir)"
  tm_install_binary "$TM_ROLE"
  if [[ "$installed_version" == "" || "$TM_RECONFIGURE" == "1" ]]; then
    tm_write_role_config "$TM_ROLE"
  else
    tm_info "升级：保留现有配置（需要重新生成请加 --reconfigure）"
  fi
  tm_run_validate "$TM_ROLE" || { tm_rollback_binary "$TM_ROLE"; exit "$TM_EXIT_CONFIG"; }
  if [[ "$TM_NO_SERVICE" != "1" ]]; then
    tm_register_service "$TM_ROLE"
    tm_service_install "$TM_ROLE"
    [[ "$TM_NO_ENABLE" == "1" ]] || tm_service_enable "$TM_ROLE"
    if [[ "$TM_NO_START" == "1" ]]; then tm_info "--no-start：已注册但未启动"
    else tm_service_start "$TM_ROLE" || { tm_rollback_binary "$TM_ROLE"; exit "$TM_EXIT_SERVICE"; }; fi
  fi
  tm_service_status "$TM_ROLE"
  tm_summary "$TM_ROLE" "$TM_VERSION"
}
```

> `tm_version_from_archive <path>`：从归档文件名 `tunnelmesh-<version>-<goos>-<goarch>.<ext>` 中切出 `<version>`（`basename` → 去掉前缀与后两段），用于 `--archive` 且未传 `--version` 的场景。实现约 5 行，必须有对应的 `run_tests.sh` 断言：
>
> ```bash
> assert_eq "archive-version" "v9.9.9" "$(tm_version_from_archive /tmp/tunnelmesh-v9.9.9-linux-amd64.tar.gz)"
> ```

- [x] **Step 4: 写最小实现 —— 三个入口脚本**

`deploy/install/oneclick/install-agent.sh`（可执行）：

```bash
#!/usr/bin/env bash
# TunnelMesh Agent 一键安装。设计与参数见 docs/deployment/oneclick-install.md。
# 本文件刻意保持薄：下载、校验、交互、渲染、服务注册全部在共享库里。
set -euo pipefail

TM_ROLE="agent"

# --- bootstrap-begin ---
# 定位共享库：TM_ONECLICK_LIB → 同目录 → raw 下载。
# `bash -c "$(curl ...)"` 与 `curl | bash` 下 $0/BASH_SOURCE 都不是真实路径，必须回退到下载。
# 三个入口的这段引导必须逐字节一致（TestEntryBootstrapIsIdentical 守护）。
_tm_self="${BASH_SOURCE[0]:-$0}"
_tm_dir=""
[[ -f "$_tm_self" ]] && _tm_dir="$(cd "$(dirname "$_tm_self")" && pwd)"
_tm_raw="${TM_RAW_BASE_URL:-https://raw.githubusercontent.com/nnworld/TunnelMesh}"
_tm_ref="${TM_ONECLICK_REF:-main}"
if [[ -n "${TM_ONECLICK_LIB:-}" && -f "${TM_ONECLICK_LIB}" ]]; then
  _tm_lib="$TM_ONECLICK_LIB"
elif [[ -n "$_tm_dir" && -f "${_tm_dir}/tunnelmesh-install-common.sh" ]]; then
  _tm_lib="${_tm_dir}/tunnelmesh-install-common.sh"
else
  _tm_lib="$(mktemp -d)/tunnelmesh-install-common.sh"
  curl --fail --silent --show-error --location --max-time 60 \
    "${_tm_raw}/${_tm_ref}/deploy/install/oneclick/tunnelmesh-install-common.sh" --output "$_tm_lib" || {
    echo "ERROR: cannot download tunnelmesh-install-common.sh from ${_tm_raw}/${_tm_ref}" >&2; exit 4; }
  [[ -s "$_tm_lib" ]] || { echo "ERROR: downloaded installer library is empty" >&2; exit 4; }
  [[ "$(head -n 1 "$_tm_lib")" == '#!/usr/bin/env bash' ]] || {
    echo "ERROR: downloaded installer library is not a bash script" >&2; exit 4; }
fi
# shellcheck source=/dev/null
source "$_tm_lib"
tm_load_lib() { printf '%s\n' "$_tm_lib"; }   # 供共享库内部与测试查询实际加载路径
tm_entry_init "$_tm_self" "$_tm_self"
# --- bootstrap-end ---

tm_role_parse_args() { :; }

tm_role_prompts() {
  tm_ask server_url "Server WebSocket URL" ""
  [[ -n "$(tm_ans_get server_url)" ]] || tm_die "$TM_EXIT_PREFLIGHT" "缺少 Server URL：交互输入，或 --server-url / TM_ONECLICK_YES=1 + --server-url"
  tm_validate_ws_url "$(tm_ans_get server_url)" "/ws/agent"
  tm_ask agent_id "Agent ID" "agent-$(hostname | tr 'A-Z' 'a-z' | tr -cs 'a-z0-9-' '-' | cut -c1-40)"
  tm_secret_value agent_token "Agent service token" TUNNELMESH_AGENT_TOKEN
  tm_ask conn_min "连接池最小连接数" "1"
  tm_ask conn_max "连接池最大连接数" "1"
  tm_ask instance_id "instance_id（留空自动生成并持久化）" ""
  tm_ask_bool report_metadata "上报宿主机 metadata（来自 allowlist 的文件/环境变量）" "no"
  if [[ "$(tm_ans_get report_metadata)" == "yes" ]]; then
    tm_ask metadata_name "metadata 名称" "device_id"
    tm_ask_choice metadata_source "metadata 来源" "file" file env
    tm_ask metadata_path "文件路径（source=file 时）" "/etc/machine-id"
    tm_ask metadata_key "环境变量名（source=env 时）" ""
    tm_validate_metadata_name "$(tm_ans_get metadata_name)"
  fi
}

tm_role_render_config() { tm_render_agent_yaml; }

tm_role_post_install() {
  printf '  Agent ID:  %s\n' "$(tm_ans_get agent_id)"
  printf '  下一步:    在管理后台为该 Agent 绑定 service token，然后执行 %s id\n' "$(tm_binary_path agent)"
}

tm_main "$@"
```

> `tm_validate_metadata_name <name>`：字符集 `[A-Za-z0-9._-]`，且不得命中敏感词 `password|passphrase|token|secret|private.?key|api.?key|credential|authorization|cookie|dsn`（与 `internal/metadata` 的 allowlist 规则一致），否则 `tm_die 2`。实现放在共享库并配 `run_tests.sh` 断言：
>
> ```bash
> assert_exit_sh 0 "metadata/name-ok" "tm_validate_metadata_name device_id"
> assert_exit_sh 2 "metadata/name-sensitive" "tm_validate_metadata_name db_password"
> assert_exit_sh 2 "metadata/name-charset" "tm_validate_metadata_name 'bad name'"
> ```

`deploy/install/oneclick/install-client.sh`：文件头、`TM_ROLE="client"`、bootstrap 片段（与 agent **逐字节一致**）、`tm_main "$@"` 都相同，只有角色钩子换成：

```bash
tm_role_prompts() {
  tm_ask server_url "Server WebSocket URL" ""
  [[ -n "$(tm_ans_get server_url)" ]] || tm_die "$TM_EXIT_PREFLIGHT" "缺少 Server URL：交互输入，或 --server-url"
  tm_validate_ws_url "$(tm_ans_get server_url)" "/ws/client"
  tm_secret_value client_token "Client service token" TUNNELMESH_CLIENT_TOKEN
  tm_ask instance_id_path "instance_id 持久化路径" "$(tm_default_instance_id_path)"
  if [[ ${#TM_TUNNEL_SPECS[@]} -eq 0 ]]; then
    tm_ask_bool add_tunnel "现在添加本地转发（也可安装后用 --tunnel 或编辑配置）" "no"
    while [[ "$(tm_ans_get add_tunnel)" == "yes" ]]; do
      tm_ask t_name "转发名称" "tunnel-$(( ${#TM_TUNNEL_SPECS[@]} + 1 ))"
      tm_ask_choice t_protocol "协议" "tcp" tcp udp http socks5
      tm_ask t_listen "本地监听地址" "127.0.0.1:$((15432 + ${#TM_TUNNEL_SPECS[@]}))"
      tm_ask t_agent "目标 Agent ID" ""
      if [[ "$(tm_ans_get t_protocol)" != "socks5" ]]; then
        tm_ask t_host "目标 host" ""
        tm_ask t_port "目标 port" ""
      else
        tm_ans_set t_host ""; tm_ans_set t_port ""
      fi
      tm_validate_listen "$(tm_ans_get t_listen)"
      TM_TUNNEL_SPECS+=("$(tm_ans_get t_name):$(tm_ans_get t_protocol):$(tm_ans_get t_listen):$(tm_ans_get t_agent):$(tm_ans_get t_host):$(tm_ans_get t_port)")
      tm_ask_bool add_tunnel "继续添加下一条转发" "no"
    done
  fi
}

tm_role_render_config() { tm_render_client_yaml; }

tm_role_post_install() {
  printf '  本地转发: %s 条\n' "${#TM_TUNNEL_SPECS[@]}"
  printf '  查看状态: %s status\n' "$(tm_binary_path client)"
}
```

`deploy/install/oneclick/install-server.sh` 的角色钩子：

```bash
tm_role_prompts() {
  tm_ask_choice mode "运行模式" "local" local cluster
  tm_ask http_addr "HTTP 监听地址" "127.0.0.1:8080"
  tm_ask dynamic_suffix "动态托管域名后缀" "apps.example.com"
  if [[ "$(tm_ans_get mode)" == "cluster" ]]; then
    tm_ans_set storage_driver "mysql"
    tm_secret_value mysql_dsn "MySQL DSN（含账号密码）" TUNNELMESH_STORAGE_MYSQL_DSN
    tm_ask_bool mysql_tls "MySQL 启用 TLS" "no"
    tm_ask_choice registry_type "注册发现" "database" database etcd
    [[ "$(tm_ans_get registry_type)" == "etcd" ]] && tm_ask registry_endpoints "etcd endpoints（逗号分隔）" "127.0.0.1:2379"
    tm_ask_bool relay_enabled "启用 Server 节点间 relay" "no"
    if [[ "$(tm_ans_get relay_enabled)" == "yes" ]]; then
      tm_ask relay_listen "relay 监听地址" "0.0.0.0:9443"
      tm_ask relay_endpoint "relay 对外 endpoint（留空自动推导）" ""
      tm_ask relay_ca "relay CA 证书路径（留空为明文 relay）" ""
      tm_ask relay_cert "relay 证书路径" ""
      tm_ask relay_key "relay 私钥路径" ""
      tm_ask relay_server_name "relay TLS ServerName" ""
      tm_secret_value relay_node_token "server-node relay token" TUNNELMESH_SERVER_RELAY_NODE_TOKEN
    fi
  else
    tm_ans_set storage_driver "sqlite"
    tm_ask sqlite_path "SQLite 数据库路径" "$(tm_default_state_dir)/tunnelmesh.db"
    tm_ans_set registry_type "database"
    tm_ans_set relay_enabled "no"
  fi
  tm_ask_bool auto_init "自动建表/执行增量迁移" "yes"
  tm_ask_bool webssh_enabled "启用浏览器 WebSSH/SFTP" "yes"
  tm_ask_bool proxy_entry_enabled "启用 tp-* HTTP 代理入口" "no"
  if [[ "$(tm_ans_get proxy_entry_enabled)" == "yes" ]]; then
    tm_ask proxy_entry_listen "代理入口内部监听地址" "127.0.0.1:8089"
    tm_ask proxy_entry_domain_suffix "代理入口域名后缀" ""
  fi
  tm_ask_bool tls_enabled "由本进程终止 TLS（否则交给 Nginx）" "no"
  tm_ask allowed_hosts "Host 白名单（逗号分隔，留空不限制）" ""
  tm_ask allowed_origins "Origin 白名单（逗号分隔）" ""
  tm_ask_bool gen_identity_key "自动生成身份主密钥（SSO/MFA 与 token reveal 必需）" "yes"
  if [[ "$(tm_ans_get gen_identity_key)" == "yes" ]]; then
    tm_ans_set identity_key "$(tm_generate_secret_key)"
    [[ "$(tm_ans_get mode)" == "cluster" ]] && tm_ans_set trace_signing_key "$(tm_generate_secret_key)"
  fi
  tm_ask_bool bootstrap_admin "安装后立即创建首个管理员" "yes"
}

tm_role_render_config() {
  tm_render_server_yaml
  # 敏感值只进 env 载体，绝不进 YAML。
  [[ -n "$(tm_ans_get mysql_dsn)" ]] && tm_env_set TUNNELMESH_STORAGE_MYSQL_DSN "$(tm_ans_get mysql_dsn)"
  [[ -n "$(tm_ans_get relay_node_token)" ]] && tm_env_set TUNNELMESH_SERVER_RELAY_NODE_TOKEN "$(tm_ans_get relay_node_token)"
  [[ -n "$(tm_ans_get identity_key)" ]] && tm_env_set TUNNELMESH_TOKEN_ENCRYPTION_KEY "$(tm_ans_get identity_key)"
  [[ -n "$(tm_ans_get trace_signing_key)" ]] && {
    tm_env_set TUNNELMESH_TRACE_SIGNING_KEY "$(tm_ans_get trace_signing_key)"
    tm_env_set TUNNELMESH_TOKEN_ENCRYPTION_KEY_ID "default"
  }
}

tm_role_post_install() {
  printf '  管理后台:  http://%s/\n' "$(tm_ans_get http_addr)"
  if [[ "$(tm_ans_get bootstrap_admin)" == "yes" ]]; then
    tm_info "创建首个管理员（凭据只输出到本终端，请立即修改）"
    "$(tm_binary_path server)" --config "$(tm_config_path server)" admin bootstrap || \
      tm_warn "admin bootstrap 失败，可稍后手工执行：tunnelmesh-server --config <path> admin regenerate-credentials --confirm"
  fi
  printf '  健康检查:  %s doctor\n' "$(tm_binary_path server)"
}
```

配套实现（共享库）：

```bash
# tm_generate_secret_key：openssl 优先，回退 /dev/urandom；输出 base64 的 32 字节。
tm_generate_secret_key() {
  if tm_have openssl; then openssl rand -base64 32
  else head -c 32 /dev/urandom | base64 | tr -d '\n'; fi
}
# tm_default_instance_id_path：与配置模型的三平台默认值一致。
tm_default_instance_id_path() {
  case "$TM_OS_FAMILY" in
    darwin) printf '%s/Library/Application Support/TunnelMesh/client-instance-id\n' "$HOME" ;;
    linux)
      if [[ "$TM_MODE" == "system" ]]; then printf '/var/lib/tunnelmesh-client/client-instance-id\n'
      else printf '%s/.local/share/tunnelmesh/client-instance-id\n' "$HOME"; fi ;;
  esac
}
# tm_validate_listen <host:port>：非 loopback 监听必须显式确认并配认证。
tm_validate_listen() {
  local listen="$1" host="${1%:*}"
  [[ "$listen" =~ ^[^:]+:[0-9]+$ ]] || tm_die "$TM_EXIT_USAGE" "listen 必须是 host:port，收到 '$listen'"
  tm_validate_port "${listen##*:}" "listen port"
  case "$host" in
    127.* | ::1 | localhost) return 0 ;;
  esac
  tm_warn "监听地址 ${listen} 不是 loopback：必须同时配置认证（auth_mode != none）"
  tm_ask_bool allow_remote "允许非 loopback 监听" "no"
  [[ "$(tm_ans_get allow_remote)" == "yes" ]] || tm_die "$TM_EXIT_USAGE" "已取消：非 loopback 监听需要显式同意"
  tm_ask_choice auth_mode "认证方式" "password" password none
  [[ "$(tm_ans_get auth_mode)" != "none" ]] || tm_die "$TM_EXIT_USAGE" "非 loopback 监听不允许 auth_mode=none"
}
```

- [x] **Step 5: 运行测试确认通过**

Run: `go test ./deploy/install/oneclick -count=1` 与 `bash -n deploy/install/oneclick/*.sh`
Expected: PASS，`TestEntryScriptsStayThin`、`TestUsageDocumentsFlagsAndInvocationForms`、`TestUninstallKeepsConfiguration` 全绿。

- [x] **Step 6: 手工冒烟（本机 macOS，不联网）**

```bash
bash deploy/install/oneclick/install-agent.sh --help
TM_ONECLICK_ALLOW_STDIN=1 printf 'wss://tunnel.example.com/ws/agent\nagent-test\n\n1\n1\n\nno\n' | \
  bash deploy/install/oneclick/install-agent.sh --archive /tmp/tunnelmesh-v1.1.1-darwin-arm64.tar.gz --no-service --no-cache
```

Expected: `--help` 打印五种调用形态与全部 flag；第二条命令在 `check-config` 处因 token 缺失或网络不可达失败并给出**明确原因**（退出码 3 或 6），不静默成功。

- [x] **Step 7: Commit（需授权）** —— 已按用户授权合并为收尾单次提交，提交信息见 PR 记录

```bash
git add deploy/install/oneclick
git commit -m "feat(install): orchestrate one-click install, upgrade and uninstall"
```

---

### Task 4: 渲染原语与服务模板

**Files:**
- Create: `deploy/systemd-user/tunnelmesh-server.service`、`tunnelmesh-agent.service`、`tunnelmesh-client.service`
- Modify: `deploy/macos/tunnelmesh.plist`（新增 `__ENVIRONMENT__`）、`deploy/windows/tunnelmesh-service.xml`（新增 `__ENV_BLOCK__`）、`deploy/systemd/tunnelmesh-agent.service`（新增 `EnvironmentFile=-/etc/tunnelmesh/agent.env`）
- Modify: `deploy/install/macos-install.sh`、`deploy/install/windows-install.ps1`（把新占位符渲染为空，行为不变）
- Modify: `deploy/install/install_templates_test.go`（覆盖新模板与新占位符）
- Modify: `deploy/install/oneclick/tunnelmesh-install-common.sh`、`testdata/run_tests.sh`

**Interfaces:**
- Consumes: `tm_die`、退出码、`tm_ans_get/set`（Task 3）、`tm_file_perms`（Task 3）。
- Produces:
  - `tm_yaml_quote <value>` → 单引号标量，内部 `'` 翻倍
  - `tm_env_clear`、`tm_env_set <KEY> <value>`、`tm_env_get <KEY>`：用索引数组 `TM_ENV_KEYS`/`TM_ENV_VALUES` 累积（bash 3.2 无关联数组）
  - `tm_render_env_file` → stdout，首行固定注释 + `KEY='value'` 行
  - `tm_xml_escape <value>`（`&`→`&amp;`、`<`→`&lt;`、`>`→`&gt;`、`"`→`&quot;`）
  - `tm_render_plist_environment` → stdout，无 env 时输出空串
  - `tm_render_winsw_env_block` → stdout，无 env 时输出空串
  - `tm_render_template <template-file> <placeholder> <value> [<placeholder> <value>...]` → stdout，字面替换（不用正则，避免路径里的 `\` 与 `&` 被吃掉）
  - `tm_render_systemd_user_unit <role> <template-file> <binary> <config> <env-file> <state-dir>` → stdout
  - `tm_render_systemd_dropin <role> <binary> <config> <env-file> <state-dir> <run-user> <run-group>` → stdout
  - `tm_write_file <dest> <mode> <content-file>`、`tm_backup_and_overwrite <dest> <mode> <content-file>`（备份 `<dest>.bak-<UTC时间戳>`，只保留最近 3 份）、`tm_file_owner_set <path> <user> <group>`

- [x] **Step 1: 写失败测试 —— 三份 user 单元模板**

创建 `deploy/systemd-user/tunnelmesh-agent.service`：

```ini
[Unit]
Description=TunnelMesh Agent (user)
Wants=network-online.target
After=network-online.target

[Service]
Type=simple
EnvironmentFile=-__ENV_FILE__
WorkingDirectory=__STATE_DIR__
ExecStartPre=__BINARY__ --config __CONFIG__ check-config
ExecStart=__BINARY__ --config __CONFIG__ run
Restart=on-failure
RestartSec=5s
NoNewPrivileges=true
PrivateTmp=true
ProtectSystem=full
ReadWritePaths=__STATE_DIR__
LimitNOFILE=65536
StandardOutput=journal
StandardError=journal
SyslogIdentifier=tunnelmesh-agent

[Install]
WantedBy=default.target
```

`deploy/systemd-user/tunnelmesh-client.service` 与上面逐行相同，只把两处 `tunnelmesh-agent`/`Agent` 换成 `tunnelmesh-client`/`Client`。

`deploy/systemd-user/tunnelmesh-server.service` 在 `ExecStartPre=... check-config` **之前**多一行 `init-node-id`（user 模式下 YAML 属于当前用户，可原地回写，不需要系统单元里的 `+` 提权前缀）：

```ini
ExecStartPre=__BINARY__ --config __CONFIG__ init-node-id
ExecStartPre=__BINARY__ --config __CONFIG__ check-config
```

- [x] **Step 2: 写失败测试 —— 既有模板加占位符**

`deploy/macos/tunnelmesh.plist`：在 `<key>ProgramArguments</key>` 之前插入一行占位符（渲染为空时 plist 结构不变）：

```xml
  <key>Label</key>
  <string>com.tunnelmesh.__ROLE__</string>
__ENVIRONMENT__
  <key>ProgramArguments</key>
```

`deploy/windows/tunnelmesh-service.xml`：在 `<executable>` 之前插入占位符：

```xml
  <description>TunnelMesh __ROLE__ service</description>
__ENV_BLOCK__
  <executable>__BINARY__</executable>
```

`deploy/systemd/tunnelmesh-agent.service`：在 `WorkingDirectory=/var/lib/tunnelmesh-agent` 之后插入（前缀 `-` 表示文件缺失不阻塞启动，与 server 单元一致）：

```ini
EnvironmentFile=-/etc/tunnelmesh/agent.env
```

`deploy/install/macos-install.sh`：在现有 `sed` 链里追加一行，把占位符渲染为空：

```bash
  -e "s|__ENVIRONMENT__||g" \
```

`deploy/install/windows-install.ps1`：在 `$rendered = $template.Replace('__INSTALL_DIR__', $InstallDir)` 之后追加：

```powershell
$rendered = $rendered.Replace('__ENV_BLOCK__', '')
```

- [x] **Step 3: 写失败测试 —— Go 模板契约**

在 `deploy/install/install_templates_test.go` 的 `genericTemplates` 里补占位符，并新增 user 单元断言：

```go
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
```

Run: `go test ./deploy/install -count=1`
Expected: FAIL —— `../systemd-user/tunnelmesh-server.service: no such file or directory`，以及 plist/XML/agent 单元的占位符断言失败。

- [x] **Step 4: 写失败测试 —— 渲染函数断言**

追加到 `testdata/run_tests.sh`：

```bash
# --- Task 4: 渲染原语 ---
assert_eq "yaml/plain" "'abc'" "$(tm_yaml_quote abc)"
assert_eq "yaml/inner-quote" "'a''b'" "$(tm_yaml_quote "a'b")"
assert_eq "yaml/empty" "''" "$(tm_yaml_quote '')"
assert_eq "yaml/dsn" "'user:p@ss w/db?parseTime=true'" "$(tm_yaml_quote 'user:p@ss w/db?parseTime=true')"

tm_env_clear
tm_env_set TUNNELMESH_AGENT_TOKEN "tok'en"
tm_env_set TUNNELMESH_REGION "shanghai"
assert_eq "env/get" "shanghai" "$(tm_env_get TUNNELMESH_REGION)"
rendered_env="$(tm_render_env_file)"
assert_contains "env/header" "$rendered_env" "包含敏感值"
assert_contains "env/quoted-value" "$rendered_env" "TUNNELMESH_AGENT_TOKEN='tok'\"'\"'en'"
assert_contains "env/plain-value" "$rendered_env" "TUNNELMESH_REGION='shanghai'"

assert_eq "xml/escape" "a&amp;b&lt;c&gt;d&quot;e" "$(tm_xml_escape 'a&b<c>d"e')"
assert_eq "plist/empty-when-no-env" "" "$(tm_env_clear; tm_render_plist_environment)"
tm_env_set TUNNELMESH_AGENT_TOKEN "t0k"
plist_env="$(tm_render_plist_environment)"
assert_contains "plist/dict-open" "$plist_env" "<key>EnvironmentVariables</key>"
assert_contains "plist/key" "$plist_env" "<key>TUNNELMESH_AGENT_TOKEN</key>"
assert_contains "plist/value" "$plist_env" "<string>t0k</string>"
assert_contains "winsw/env" "$(tm_render_winsw_env_block)" '<env name="TUNNELMESH_AGENT_TOKEN" value="t0k" />'
tm_env_clear

unit="$(tm_render_systemd_user_unit agent "$ROOT/deploy/systemd-user/tunnelmesh-agent.service" \
  "$HOME/.local/bin/tunnelmesh-agent" "$HOME/.config/tunnelmesh/agent.yaml" \
  "$HOME/.config/tunnelmesh/agent.env" "$HOME/.local/share/tunnelmesh")"
assert_contains "unit/exec" "$unit" "ExecStart=$HOME/.local/bin/tunnelmesh-agent --config $HOME/.config/tunnelmesh/agent.yaml run"
assert_contains "unit/envfile" "$unit" "EnvironmentFile=-$HOME/.config/tunnelmesh/agent.env"
assert_contains "unit/state" "$unit" "ReadWritePaths=$HOME/.local/share/tunnelmesh"
assert_eq "unit/no-placeholder-left" "" "$(printf '%s' "$unit" | grep -o '__[A-Z_]*__' || true)"

dropin="$(tm_render_systemd_dropin server /opt/tm/tunnelmesh-server /etc/tunnelmesh/server.yaml /etc/tunnelmesh/server.env /var/lib/tunnelmesh tmuser tmgroup)"
assert_contains "dropin/user" "$dropin" "User=tmuser"
assert_contains "dropin/group" "$dropin" "Group=tmgroup"
assert_contains "dropin/clear-execstart" "$dropin" "ExecStart="
assert_contains "dropin/clear-execstartpre" "$dropin" "ExecStartPre="
assert_contains "dropin/init-node-id-root" "$dropin" "ExecStartPre=+/opt/tm/tunnelmesh-server --config /etc/tunnelmesh/server.yaml init-node-id"
assert_contains "dropin/exec" "$dropin" "ExecStart=/opt/tm/tunnelmesh-server --config /etc/tunnelmesh/server.yaml run"

# 覆盖前备份，只保留最近 3 份
bk="$TM_TMP4"; TM_TMP4="$(mktemp -d)"; mkdir -p "$TM_TMP4"
target="$TM_TMP4/agent.yaml"
for i in 1 2 3 4 5; do printf 'v%s\n' "$i" >"$TM_TMP4/content"; TM_BACKUP_TIMESTAMP="2026010${i}T000000Z" tm_backup_and_overwrite "$target" 0600 "$TM_TMP4/content"; done
assert_eq "backup/content" "v5" "$(cat "$target")"
assert_eq "backup/kept-3" "3" "$(ls "$target".bak-* | wc -l | tr -d ' ')"
assert_eq "backup/newest" "v4" "$(cat "$target.bak-20260105T000000Z" 2>/dev/null || cat "$target".bak-2026010*T000000Z | tail -1)"
assert_eq "backup/mode" "600" "$(tm_file_perms "$target")"
rm -rf "$TM_TMP4"
```

> `ROOT` 在套件开头定义为 `ROOT="$(cd "$HERE/../../.." && pwd)"`（仓库根），用于定位 `deploy/systemd-user/`。实现 Step 1 时把这一行加到 `run_tests.sh` 顶部。
> `TM_BACKUP_TIMESTAMP` 是备份文件名的可注入点：默认取 `date -u +%Y%m%dT%H%M%SZ`，测试里显式赋值以便断言轮转顺序。

- [x] **Step 5: 运行测试确认失败**

Run: `go test ./deploy/install/oneclick -run TestShellFunctionSuite -count=1` 与 `go test ./deploy/install -count=1`
Expected: 两者均 FAIL —— 前者 `tm_yaml_quote: command not found`，后者 user 单元文件不存在。

- [x] **Step 6: 写最小实现**

在 `tunnelmesh-install-common.sh` 追加：

```bash
# --- YAML / env / XML 渲染 ---
tm_yaml_quote() {
  local v="$1"
  printf "'%s'\n" "${v//\'/\'\'}"
}

TM_ENV_KEYS=() TM_ENV_VALUES=()
tm_env_clear() { TM_ENV_KEYS=(); TM_ENV_VALUES=(); }
tm_env_set() {
  local i
  for i in "${!TM_ENV_KEYS[@]}"; do
    if [[ "${TM_ENV_KEYS[$i]}" == "$1" ]]; then TM_ENV_VALUES[$i]="$2"; return 0; fi
  done
  TM_ENV_KEYS+=("$1"); TM_ENV_VALUES+=("$2")
}
tm_env_get() {
  local i
  for i in "${!TM_ENV_KEYS[@]}"; do
    [[ "${TM_ENV_KEYS[$i]}" == "$1" ]] && { printf '%s' "${TM_ENV_VALUES[$i]}"; return 0; }
  done
  return 0
}
tm_render_env_file() {
  local i
  printf '# 本文件由 TunnelMesh 一键安装脚本生成，包含敏感值，请勿提交版本库。\n'
  printf '# 权限应保持在 0600（user 模式）或 0640 root:<运行组>（system 模式）。\n'
  for i in "${!TM_ENV_KEYS[@]}"; do
    printf '%s=%s\n' "${TM_ENV_KEYS[$i]}" "$(tm_yaml_quote "${TM_ENV_VALUES[$i]}" | tr -d '\n')"
  done
}

tm_xml_escape() {
  local v="$1"
  v="${v//&/&amp;}"; v="${v//</&lt;}"; v="${v//>/&gt;}"; v="${v//\"/&quot;}"
  printf '%s' "$v"
}
tm_render_plist_environment() {
  [[ ${#TM_ENV_KEYS[@]} -eq 0 ]] && return 0
  local i
  printf '  <key>EnvironmentVariables</key>\n  <dict>\n'
  for i in "${!TM_ENV_KEYS[@]}"; do
    printf '    <key>%s</key>\n    <string>%s</string>\n' \
      "$(tm_xml_escape "${TM_ENV_KEYS[$i]}")" "$(tm_xml_escape "${TM_ENV_VALUES[$i]}")"
  done
  printf '  </dict>\n'
}
tm_render_winsw_env_block() {
  [[ ${#TM_ENV_KEYS[@]} -eq 0 ]] && return 0
  local i
  for i in "${!TM_ENV_KEYS[@]}"; do
    printf '  <env name="%s" value="%s" />\n' \
      "$(tm_xml_escape "${TM_ENV_KEYS[$i]}")" "$(tm_xml_escape "${TM_ENV_VALUES[$i]}")"
  done
}

# tm_render_template 用逐字符字面替换而不是 sed：Windows 路径里的反斜杠、
# XML 里的 & 都会被 sed 当转义/反向引用吃掉（windows-install.ps1 里踩过同一个坑）。
tm_render_template() {
  local template="$1"; shift
  local content; content="$(cat "$template")"
  while [[ $# -gt 0 ]]; do
    local ph="$1" val="$2"; shift 2
    content="${content//$ph/$val}"
  done
  printf '%s\n' "$content"
}

tm_render_systemd_user_unit() { # <role> <template> <binary> <config> <env-file> <state-dir>
  local role="$1" template="$2"
  tm_render_template "$template" \
    "__BINARY__" "$3" "__CONFIG__" "$4" "__ENV_FILE__" "$5" "__STATE_DIR__" "$6"
}

# tm_render_systemd_dropin：系统模式下只在用户选择了非默认值时生成，
# 用 ExecStart=/ExecStartPre= 清空再重写的标准做法覆盖路径与账户，
# 不修改 deploy/systemd/ 里的原始单元（单一模板来源约定）。
tm_render_systemd_dropin() { # <role> <binary> <config> <env-file> <state-dir> <run-user> <run-group>
  local role="$1" binary="$2" config="$3" envfile="$4" state="$5" user="$6" group="$7"
  printf '# 由一键安装脚本生成：覆盖 %s 单元中与默认值不同的部分。\n' "tunnelmesh-${role}.service"
  printf '# 手工修改请编辑本文件，不要改 /etc/systemd/system/tunnelmesh-%s.service。\n' "$role"
  printf '[Service]\n'
  printf 'User=%s\nGroup=%s\n' "$user" "$group"
  printf 'WorkingDirectory=%s\nReadWritePaths=%s\n' "$state" "$state"
  printf 'EnvironmentFile=-%s\n' "$envfile"
  printf 'ExecStartPre=\nExecStart=\n'
  if [[ "$role" == "server" ]]; then
    printf 'ExecStartPre=+%s --config %s init-node-id\n' "$binary" "$config"
  fi
  printf 'ExecStartPre=%s --config %s check-config\n' "$binary" "$config"
  printf 'ExecStart=%s --config %s run\n' "$binary" "$config"
}

# --- 落盘：先备份再覆盖，最多保留最近 3 份 ---
TM_BACKUP_TIMESTAMP=""
tm_backup_and_overwrite() { # <dest> <mode> <content-file>
  local dest="$1" mode="$2" content="$3" ts oldest
  mkdir -p "$(dirname "$dest")"
  if [[ -e "$dest" ]]; then
    ts="${TM_BACKUP_TIMESTAMP:-$(date -u +%Y%m%dT%H%M%SZ)}"
    cp -p -- "$dest" "${dest}.bak-${ts}"
    # shellcheck disable=SC2012  # ls -1t 用于按时间排序，find -printf 在 macOS 不可用
    oldest="$(ls -1t "${dest}".bak-* 2>/dev/null | tail -n +4)"
    if [[ -n "$oldest" ]]; then printf '%s\n' "$oldest" | while IFS= read -r f; do rm -f -- "$f"; done; fi
  fi
  install -m "$mode" "$content" "$dest"
}
tm_write_file() { # <dest> <mode> <content-file>
  mkdir -p "$(dirname "$1")"
  install -m "$2" "$3" "$1"
}
tm_file_owner_set() { # <path> <user> <group>
  if [[ -n "$2" || -n "$3" ]]; then chown "${2:-}:${3:-}" "$1" 2>/dev/null || tm_warn "cannot chown $1 to ${2:-}:${3:-}"; fi
}
```

- [x] **Step 7: 运行测试确认通过**

Run: `go test ./deploy/install -count=1` → PASS；`go test ./deploy/install/oneclick -count=1` → PASS（新增 `ok   yaml/*`、`ok   env/*`、`ok   plist/*`、`ok   winsw/*`、`ok   unit/*`、`ok   dropin/*`、`ok   backup/*`）。
Run: `bash -n deploy/install/macos-install.sh && plutil -lint deploy/macos/tunnelmesh.plist`
Expected: 均退出码 0（占位符渲染为空后 plist 仍合法）。

- [x] **Step 8: Commit（需授权）**

```bash
git add deploy/systemd-user deploy/systemd/tunnelmesh-agent.service deploy/macos/tunnelmesh.plist deploy/windows/tunnelmesh-service.xml deploy/install
git commit -m "feat(deploy): add user-mode systemd templates and env injection placeholders"
```

---

### Task 5: 三角色配置渲染与配置漂移测试

**Files:**
- Modify: `deploy/install/oneclick/tunnelmesh-install-common.sh`（追加三个角色的 YAML 渲染函数）
- Create: `deploy/install/oneclick/testdata/render_yaml.sh`（测试驱动，不进归档）
- Create: `deploy/install/oneclick/oneclick_config_test.go`
- Modify: `deploy/install/oneclick/testdata/run_tests.sh`

**Interfaces:**
- Consumes: `tm_yaml_quote`、`tm_env_set`、`tm_ans_get`、`tm_die`、`TM_TUNNEL_SPECS`。
- Produces:
  - `tm_render_server_yaml` → stdout（读 `tm_ans_get` 的 `mode`、`http_addr`、`dynamic_suffix`、`storage_driver`、`sqlite_path`、`mysql_tls`、`auto_init`、`registry_type`、`registry_endpoints`、`relay_enabled`、`relay_listen`、`relay_endpoint`、`relay_ca`、`relay_cert`、`relay_key`、`relay_server_name`、`webssh_enabled`、`proxy_entry_enabled`、`proxy_entry_listen`、`proxy_entry_domain_suffix`、`tls_enabled`、`allowed_hosts`、`allowed_origins`；同时把 `TUNNELMESH_STORAGE_MYSQL_DSN`、`TUNNELMESH_SERVER_RELAY_NODE_TOKEN`、`TUNNELMESH_TOKEN_ENCRYPTION_KEY`、`TUNNELMESH_TOKEN_ENCRYPTION_KEY_ID`、`TUNNELMESH_TRACE_SIGNING_KEY` 写进 `tm_env_set`）
  - `tm_render_agent_yaml` → stdout（`server_url`、`agent_id`、`instance_id`、`conn_min`、`conn_max`、metadata 三元组；token 写 `TUNNELMESH_AGENT_TOKEN`）
  - `tm_render_client_yaml` → stdout（`server_url`、`instance_id_path`、`TM_TUNNEL_SPECS`；token 写 `TUNNELMESH_CLIENT_TOKEN`）
  - 空值键一律**不输出**，避免覆盖配置模型的默认值
- 依赖的配置键名以 `internal/config/config.go` 的 `yaml` tag 为唯一权威来源：`mode`、`storage.{driver,auto_init,sqlite.path,mysql.tls}`、`registry.{type,endpoints}`、`node.id`、`server.{http_addr,dynamic_suffix,tcp_bridge.enabled,webssh.enabled,proxy_entry.{enabled,listen,domain_suffix},relay.{enabled,listen,endpoint,ca,cert,key,server_name}}`、`security.{allowed_hosts,allowed_origins}`、`tls.enabled`、`agent.{server_url,id,instance_id,connections.{min,max},metadata[].{name,source,path,key}}`、`client.{server_url,instance_id_path,tunnels[].{name,protocol,listen,agent_id,target_host,target_port,auth_mode,allow_remote,auth_url}}`。

- [x] **Step 1: 写失败测试 —— 渲染驱动脚本**

创建 `deploy/install/oneclick/testdata/render_yaml.sh`：

```bash
#!/usr/bin/env bash
# 测试驱动：把 key=value 形式的答案灌进 tm_ans_set，再调用角色渲染函数。
# 只被 oneclick_config_test.go 与 run_tests.sh 使用，不进发布归档。
set -euo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=/dev/null
source "${TM_ONECLICK_LIB:-$HERE/../tunnelmesh-install-common.sh}"
role="$1"; shift
TM_TUNNEL_SPECS=()
for kv in "$@"; do
  case "$kv" in
    tunnel=*) TM_TUNNEL_SPECS+=("${kv#tunnel=}") ;;
    env=*) IFS='=' read -r _ k v <<<"$kv"; tm_env_set "$k" "$v" ;;
    *) tm_ans_set "${kv%%=*}" "${kv#*=}" ;;
  esac
done
case "$role" in
  server) tm_render_server_yaml ;;
  agent) tm_render_agent_yaml ;;
  client) tm_render_client_yaml ;;
  *) echo "unknown role: $role" >&2; exit 2 ;;
esac
```

- [x] **Step 2: 写失败测试 —— Go 配置漂移测试**

创建 `deploy/install/oneclick/oneclick_config_test.go`：

```go
package oneclick

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/tunnelmesh/tunnelmesh/internal/config"
)

// renderYAML 用测试驱动脚本渲染角色配置，返回文件路径。
// 这是防止「脚本渲染的键名与 internal/config 漂移」的权威手段：
// 渲染结果必须能被真实的 config.Load 解析，且字段值与输入一致。
func renderYAML(t *testing.T, role string, args ...string) string {
	t.Helper()
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skipf("bash not available: %v", err)
	}
	cmd := exec.Command(bash, filepath.Join("testdata", "render_yaml.sh"), role)
	cmd.Args = append(cmd.Args, args...)
	cmd.Env = append(os.Environ(),
		"TM_ONECLICK_LIB=tunnelmesh-install-common.sh",
		"TM_ONECLICK_ALLOW_STDIN=1",
		// token 只能来自环境变量；渲染 YAML 时不需要，但 config.Load 的
		// agent/client 校验会用到，因此显式注入测试值。
		"TUNNELMESH_AGENT_TOKEN=test-agent-token",
		"TUNNELMESH_CLIENT_TOKEN=test-client-token",
	)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("render %s yaml: %v", role, err)
	}
	path := filepath.Join(t.TempDir(), role+".yaml")
	if err := os.WriteFile(path, out, 0o600); err != nil {
		t.Fatalf("write yaml: %v", err)
	}
	return path
}

func loadConfig(t *testing.T, path string) config.Config {
	t.Helper()
	cfg, err := config.Load(context.Background(), config.ConfigOptions{ConfigFile: path})
	if err != nil {
		t.Fatalf("config.Load(%s): %v", path, err)
	}
	return cfg
}

func TestServerLocalSQLiteConfigRenders(t *testing.T) {
	path := renderYAML(t, "server",
		"mode=local", "http_addr=127.0.0.1:8080", "dynamic_suffix=apps.example.com",
		"storage_driver=sqlite", "sqlite_path=/var/tmp/tm/tunnelmesh.db", "auto_init=yes",
		"registry_type=database", "webssh_enabled=yes", "proxy_entry_enabled=no", "tls_enabled=no")
	cfg := loadConfig(t, path)
	if cfg.Mode != "local" {
		t.Errorf("mode = %q, want local", cfg.Mode)
	}
	if cfg.Storage.Driver != "sqlite" || !cfg.Storage.AutoInit {
		t.Errorf("storage = %+v, want sqlite + auto_init", cfg.Storage)
	}
	if cfg.Storage.SQLite.Path != "/var/tmp/tm/tunnelmesh.db" {
		t.Errorf("sqlite path = %q", cfg.Storage.SQLite.Path)
	}
	if cfg.Server.HTTPAddr != "127.0.0.1:8080" || cfg.Server.DynamicSuffix != "apps.example.com" {
		t.Errorf("server = %+v", cfg.Server)
	}
	if !cfg.Server.WebSSH.Enabled || cfg.Server.ProxyEntry.Enabled || cfg.TLS.Enabled {
		t.Errorf("feature flags wrong: webssh=%v proxy_entry=%v tls=%v",
			cfg.Server.WebSSH.Enabled, cfg.Server.ProxyEntry.Enabled, cfg.TLS.Enabled)
	}
	if cfg.Registry.Type != "database" {
		t.Errorf("registry = %q", cfg.Registry.Type)
	}
}

func TestServerClusterMySQLRelayConfigRenders(t *testing.T) {
	path := renderYAML(t, "server",
		"mode=cluster", "http_addr=127.0.0.1:8080", "storage_driver=mysql", "auto_init=yes",
		"mysql_tls=no", "registry_type=database", "relay_enabled=yes",
		"relay_listen=0.0.0.0:9443", "relay_endpoint=server-1.internal.example.com:9443",
		"allowed_hosts=tunnel.example.com", "allowed_origins=https://tunnel.example.com",
		"env=TUNNELMESH_STORAGE_MYSQL_DSN=user:pw@tcp(db:3306)/tunnelmesh?parseTime=true",
		"env=TUNNELMESH_SERVER_RELAY_NODE_TOKEN=node-token")
	cfg := loadConfig(t, path)
	if cfg.Mode != "cluster" || cfg.Storage.Driver != "mysql" {
		t.Errorf("mode/storage = %q/%q", cfg.Mode, cfg.Storage.Driver)
	}
	if !cfg.Server.Relay.Enabled || cfg.Server.Relay.Listen != "0.0.0.0:9443" {
		t.Errorf("relay = %+v", cfg.Server.Relay)
	}
	if cfg.Server.Relay.Endpoint != "server-1.internal.example.com:9443" {
		t.Errorf("relay endpoint = %q", cfg.Server.Relay.Endpoint)
	}
	if len(cfg.Security.AllowedHosts) != 1 || cfg.Security.AllowedHosts[0] != "tunnel.example.com" {
		t.Errorf("allowed_hosts = %v", cfg.Security.AllowedHosts)
	}
	if len(cfg.Security.AllowedOrigins) != 1 {
		t.Errorf("allowed_origins = %v", cfg.Security.AllowedOrigins)
	}
}

func TestAgentConfigRenders(t *testing.T) {
	path := renderYAML(t, "agent",
		"server_url=wss://tunnel.example.com/ws/agent", "agent_id=agent-devbox",
		"conn_min=1", "conn_max=2", "metadata_name=device_id", "metadata_source=file", "metadata_path=/etc/machine-id")
	cfg := loadConfig(t, path)
	if cfg.Agent.ServerURL != "wss://tunnel.example.com/ws/agent" || cfg.Agent.ID != "agent-devbox" {
		t.Errorf("agent = %+v", cfg.Agent)
	}
	if cfg.Agent.Connections.Min != 1 || cfg.Agent.Connections.Max != 2 {
		t.Errorf("connections = %+v", cfg.Agent.Connections)
	}
	if len(cfg.Agent.Metadata) != 1 || cfg.Agent.Metadata[0].Name != "device_id" || cfg.Agent.Metadata[0].Source != "file" {
		t.Errorf("metadata = %+v", cfg.Agent.Metadata)
	}
}

func TestClientConfigRendersAllProtocols(t *testing.T) {
	path := renderYAML(t, "client",
		"server_url=wss://tunnel.example.com/ws/client",
		"tunnel=pg:tcp:127.0.0.1:15432:agent-db:db.internal:5432",
		"tunnel=dns:udp:127.0.0.1:15353:agent-net:10.0.0.53:53",
		"tunnel=web:http:127.0.0.1:18080:agent-web:127.0.0.1:8080",
		"tunnel=socks:socks5:127.0.0.1:10866:agent-a::")
	cfg := loadConfig(t, path)
	if cfg.Client.ServerURL != "wss://tunnel.example.com/ws/client" {
		t.Errorf("client server_url = %q", cfg.Client.ServerURL)
	}
	want := []struct{ name, protocol, listen, agent, host string; port int }{
		{"pg", "tcp", "127.0.0.1:15432", "agent-db", "db.internal", 5432},
		{"dns", "udp", "127.0.0.1:15353", "agent-net", "10.0.0.53", 53},
		{"web", "http", "127.0.0.1:18080", "agent-web", "127.0.0.1", 8080},
		{"socks", "socks5", "127.0.0.1:10866", "agent-a", "", 0},
	}
	if len(cfg.Client.Tunnels) != len(want) {
		t.Fatalf("tunnels = %d, want %d (%+v)", len(cfg.Client.Tunnels), len(want), cfg.Client.Tunnels)
	}
	for i, w := range want {
		got := cfg.Client.Tunnels[i]
		if got.Name != w.name || got.Protocol != w.protocol || got.ListenAddr != w.listen ||
			got.AgentID != w.agent || got.TargetHost != w.host || got.TargetPort != w.port {
			t.Errorf("tunnel[%d] = %+v, want %+v", i, got, w)
		}
	}
}

func TestRenderedConfigNeverContainsSecrets(t *testing.T) {
	for _, role := range []string{"server", "agent", "client"} {
		args := []string{"server_url=wss://tunnel.example.com/ws/" + role}
		if role == "server" {
			args = append(args, "mode=local", "storage_driver=sqlite", "sqlite_path=/var/tmp/tm/x.db",
				"env=TUNNELMESH_STORAGE_MYSQL_DSN=user:pw@tcp(db:3306)/tm")
		}
		path := renderYAML(t, role, args...)
		body := readFile(t, path)
		for _, bad := range []string{"pw@tcp", "token:", "password", "TUNNELMESH_AGENT_TOKEN", "TUNNELMESH_CLIENT_TOKEN"} {
			if strings.Contains(body, bad) {
				t.Errorf("%s yaml leaked %q:\n%s", role, bad, body)
			}
		}
	}
}
```

> `oneclick_config_test.go` 的 import 是 `context`、`os`、`os/exec`、`path/filepath`、`strings`、`testing` 与 `github.com/tunnelmesh/tunnelmesh/internal/config`（module 名取自 `go.mod`）；`readFile` 复用同包 `oneclick_scripts_test.go` 里的 helper，不要重复定义。

- [x] **Step 3: 运行测试确认失败**

Run: `go test ./deploy/install/oneclick -run 'Config|Rendered' -count=1`
Expected: FAIL —— `render agent yaml: exit status 127`（`tm_render_agent_yaml: command not found`）。

- [x] **Step 4: 写最小实现**

```bash
# --- 角色 YAML 渲染：只输出用户显式回答过的键，空值一律不写，避免覆盖配置模型默认值 ---
tm_yaml_line() { # <indent> <key> <value>
  [[ -z "$3" ]] && return 0
  printf '%s%s: %s\n' "$1" "$2" "$(tm_yaml_quote "$3")"
}
tm_yaml_bool() { # <indent> <key> <yes|no>
  case "$3" in yes) printf '%s%s: true\n' "$1" "$2" ;; no) printf '%s%s: false\n' "$1" "$2" ;; esac
}
tm_yaml_list() { # <indent> <key> <comma-separated-values>
  local indent="$1" key="$2" csv="$3" item
  [[ -z "$csv" ]] && return 0
  printf '%s%s:\n' "$indent" "$key"
  IFS=',' read -r -a items <<<"$csv"
  for item in "${items[@]}"; do
    item="$(printf '%s' "$item" | sed 's/^[[:space:]]*//; s/[[:space:]]*$//')"
    [[ -n "$item" ]] && printf '%s  - %s\n' "$indent" "$(tm_yaml_quote "$item")"
  done
}

tm_render_server_yaml() {
  local a=tm_ans_get
  printf '# 由 TunnelMesh 一键安装脚本生成。敏感值不在此文件，见同目录 env / plist / 服务 XML。\n'
  printf 'mode: %s\n\n' "$($a mode)"
  printf 'storage:\n'
  tm_yaml_line "  " "driver" "$($a storage_driver)"
  tm_yaml_bool "  " "auto_init" "$($a auto_init)"
  if [[ "$($a storage_driver)" == "sqlite" ]]; then
    printf '  sqlite:\n'; tm_yaml_line "    " "path" "$($a sqlite_path)"
  else
    printf '  mysql:\n'; tm_yaml_bool "    " "tls" "$($a mysql_tls)"
    printf '    # dsn 由 TUNNELMESH_STORAGE_MYSQL_DSN 注入，不要写进本文件。\n'
  fi
  printf '\nregistry:\n'; tm_yaml_line "  " "type" "$($a registry_type)"
  tm_yaml_list "  " "endpoints" "$($a registry_endpoints)"
  printf '\nserver:\n'
  tm_yaml_line "  " "http_addr" "$($a http_addr)"
  tm_yaml_line "  " "dynamic_suffix" "$($a dynamic_suffix)"
  printf '  tcp_bridge:\n    enabled: true\n'
  printf '  webssh:\n'; tm_yaml_bool "    " "enabled" "$($a webssh_enabled)"
  if [[ "$($a proxy_entry_enabled)" == "yes" ]]; then
    printf '  proxy_entry:\n    enabled: true\n'
    tm_yaml_line "    " "listen" "$($a proxy_entry_listen)"
    tm_yaml_line "    " "domain_suffix" "$($a proxy_entry_domain_suffix)"
  fi
  if [[ "$($a relay_enabled)" == "yes" ]]; then
    printf '  relay:\n    enabled: true\n'
    tm_yaml_line "    " "listen" "$($a relay_listen)"
    tm_yaml_line "    " "endpoint" "$($a relay_endpoint)"
    tm_yaml_line "    " "ca" "$($a relay_ca)"
    tm_yaml_line "    " "cert" "$($a relay_cert)"
    tm_yaml_line "    " "key" "$($a relay_key)"
    tm_yaml_line "    " "server_name" "$($a relay_server_name)"
    printf '    # node_token 由 TUNNELMESH_SERVER_RELAY_NODE_TOKEN 注入。\n'
  fi
  printf '\nsecurity:\n'
  tm_yaml_list "  " "allowed_hosts" "$($a allowed_hosts)"
  tm_yaml_list "  " "allowed_origins" "$($a allowed_origins)"
  printf '\ntls:\n'; tm_yaml_bool "  " "enabled" "$($a tls_enabled)"
  printf '\ndownloads:\n'; tm_yaml_line "  " "github_repository" "$TM_REPOSITORY"
}

tm_render_agent_yaml() {
  local a=tm_ans_get
  printf '# 由 TunnelMesh 一键安装脚本生成。agent.token 的 YAML 键被标记为不可序列化，\n'
  printf '# 只能通过 TUNNELMESH_AGENT_TOKEN 注入（见同目录 agent.env / plist / 服务 XML）。\n'
  printf 'mode: local\n\nagent:\n'
  tm_yaml_line "  " "server_url" "$($a server_url)"
  tm_yaml_line "  " "id" "$($a agent_id)"
  tm_yaml_line "  " "instance_id" "$($a instance_id)"
  printf '  connections:\n'
  printf '    min: %s\n    max: %s\n' "$($a conn_min)" "$($a conn_max)"
  if [[ -n "$($a metadata_name)" ]]; then
    printf '  metadata:\n    - name: %s\n      source: %s\n' \
      "$(tm_yaml_quote "$($a metadata_name)")" "$(tm_yaml_quote "$($a metadata_source)")"
    tm_yaml_line "      " "path" "$($a metadata_path)"
    tm_yaml_line "      " "key" "$($a metadata_key)"
  fi
}

tm_render_client_yaml() {
  local a=tm_ans_get spec name protocol listen agent host port
  printf '# 由 TunnelMesh 一键安装脚本生成。client.token 只能通过 TUNNELMESH_CLIENT_TOKEN 注入。\n'
  printf 'mode: local\n\nclient:\n'
  tm_yaml_line "  " "server_url" "$($a server_url)"
  tm_yaml_line "  " "instance_id_path" "$($a instance_id_path)"
  if [[ ${#TM_TUNNEL_SPECS[@]} -gt 0 ]]; then
    printf '  tunnels:\n'
    for spec in "${TM_TUNNEL_SPECS[@]}"; do
      IFS='|' read -r name protocol listen agent host port <<<"$(tm_parse_tunnel_spec "$spec")"
      printf '    - name: %s\n      protocol: %s\n      listen: %s\n      agent_id: %s\n' \
        "$(tm_yaml_quote "$name")" "$(tm_yaml_quote "$protocol")" "$(tm_yaml_quote "$listen")" "$(tm_yaml_quote "$agent")"
      if [[ -n "$host" ]]; then
        printf '      target_host: %s\n      target_port: %s\n' "$(tm_yaml_quote "$host")" "$port"
      fi
    done
  fi
}
```

- [x] **Step 5: 运行测试确认通过**

Run: `go test ./deploy/install/oneclick -count=1`
Expected: PASS —— 4 个 `Test*ConfigRenders*` + `TestRenderedConfigNeverContainsSecrets` 全绿；说明渲染出的键名与 `internal/config` 完全对齐。

- [x] **Step 6: Commit（需授权）** —— 已按用户授权合并为收尾单次提交，提交信息见 PR 记录

```bash
git add deploy/install/oneclick
git commit -m "feat(install): render role configurations guarded by config.Load"
```

---

### Task 3: 参数解析、交互原语与 tty 语义

**Files:**
- Modify: `deploy/install/oneclick/tunnelmesh-install-common.sh`
- Modify: `deploy/install/oneclick/testdata/run_tests.sh`

**Interfaces:**
- Consumes: `tm_die`、`tm_info`、`tm_warn`、退出码常量、`TM_TTY`（Task 1）、`TM_ONECLICK_YES`（Task 1）、`tm_validate_version`（Task 2）、`TM_DEFAULT_INSTALL_MODE`。
- Produces:
  - `tm_ans_key <key>` → `TM_ANS_<KEY>`（`.`/`-` 映射为 `_`，大写；非法字符集直接 `tm_die 2`）
  - `tm_ans_set <key> <value>`、`tm_ans_get <key>`（bash 3.2 下用 `printf -v` + `eval` 实现，不用关联数组）
  - `tm_read_line <prompt> <default>`、`tm_read_secret <prompt>`（均优先 `$TM_TTY`，为空则回退 stdin）
  - `tm_require_input_channel`：`TM_YES=1`、`TM_TTY` 非空、`[[ -t 0 ]]`、`TM_ONECLICK_ALLOW_STDIN=1` 四者全不满足时 `tm_die 3`
  - `tm_ask <key> <prompt> <default>`、`tm_ask_bool <key> <prompt> <yes|no>`、`tm_ask_choice <key> <prompt> <default> <choice...>`、`tm_ask_secret <key> <prompt>`（两次输入必须一致，最多重试 3 次）
  - `tm_secret_value <key> <prompt> <env-var-name>`：优先级 `--secret-env-file` 中的值 > `--token-file`（仅 token 类） > 环境变量 > 交互输入；`--yes` 且四者皆空时 `tm_die 3` 并指出缺失项
  - `tm_load_secret_env_file <path>`：权限必须是 0600/0400/0640，否则 `tm_die 3`；逐行 `KEY=VALUE` 存入 `TM_SECRET_<KEY>`
  - `tm_validate_ws_url <url> <required-suffix>`：必须 `^wss?://` 且以 suffix 结尾，否则 `tm_die 2`
  - `tm_parse_tunnel_spec <name:protocol:listen:agent_id:target_host:target_port>` → 打印 `name|protocol|listen|agent|host|port`；protocol ∈ {tcp,udp,http,socks5}；`socks5` 允许 host/port 为空；端口 1–65535；非法 `tm_die 2`
  - `tm_parse_common_args "$@"`：解析全部通用 flag，未知参数 `tm_die 2`；把 flag 值写进 `tm_ans_set`，使 `tm_ask` 不再重复提问
  - `tm_usage`（通用 help，含五种调用形态示例；角色可用 `tm_role_usage_extra` 追加）
  - 全局：`TM_YES`、`TM_UNINSTALL`、`TM_NO_SERVICE`、`TM_NO_START`、`TM_NO_ENABLE`、`TM_NO_LINGER`、`TM_KEEP_CONFIG`、`TM_RECONFIGURE`、`TM_MODE`、`TM_TARGET_USER`、`TM_RUN_USER`、`TM_CREATE_RUN_USER`、`TM_BIN_DIR`、`TM_CONFIG_DIR`、`TM_STATE_DIR`、`TM_ARCHIVE`、`TM_TOKEN_FILE`、`TM_SECRET_ENV_FILE`、`TM_WITH_ALL_BINARIES`（不存在，见规格 §7 步骤 4）

- [x] **Step 1: 写失败测试**

追加到 `testdata/run_tests.sh`（`printf '\n%s: %d failure(s)\n'` 之前）：

```bash
# --- Task 3: 参数解析与交互原语 ---
export TM_ONECLICK_ALLOW_STDIN=1

tm_ans_set "server.url" "wss://a/ws/agent"
assert_eq "ans/roundtrip-dotted-key" "wss://a/ws/agent" "$(tm_ans_get server.url)"
tm_ans_set "agent-id" "agent-1"
assert_eq "ans/roundtrip-dash-key" "agent-1" "$(tm_ans_get agent-id)"
assert_eq "ans/missing-empty" "" "$(tm_ans_get nope)"
assert_exit_sh 2 "ans/illegal-key" "tm_ans_set 'bad key' v"

# --yes：全部取默认值，不读 stdin
assert_eq "ask/yes-default" "8080" "$(TM_YES=1 tm_ask_http_port)"
# flag/env 已给值时不覆盖
assert_eq "ask/preset-wins" "preset" "$(TM_YES=1 bash -c 'source "$TM_ONECLICK_LIB"; tm_ans_set demo preset; tm_ask demo "prompt" "default"; tm_ans_get demo')"
# 非 --yes：从 stdin 读；空行取默认
assert_eq "ask/stdin-value" "typed" "$(printf 'typed\n' | TM_YES=0 TM_TTY= tm_ask_stdio demo "prompt" "default")"
assert_eq "ask/stdin-empty-uses-default" "default" "$(printf '\n' | TM_YES=0 TM_TTY= tm_ask_stdio demo "prompt" "default")"
assert_eq "ask/bool-normalize-Y" "yes" "$(printf 'Y\n' | TM_YES=0 TM_TTY= tm_ask_bool_stdio demo "prompt" "no")"
assert_eq "ask/bool-normalize-empty" "no" "$(printf '\n' | TM_YES=0 TM_TTY= tm_ask_bool_stdio demo "prompt" "no")"
assert_eq "ask/choice-invalid-then-valid" "udp" "$(printf 'quic\nudp\n' | TM_YES=0 TM_TTY= tm_ask_choice_stdio demo "prompt" "tcp" tcp udp http socks5)"

# 没有输入通道时必须失败，不能静默取默认值
assert_exit_sh 3 "ask/no-channel-dies" "TM_YES=0 TM_TTY= TM_ONECLICK_ALLOW_STDIN= </dev/null tm_require_input_channel"

# URL 与隧道规格校验
assert_exit_sh 0 "url/ok-agent" "tm_validate_ws_url wss://tunnel.example.com/ws/agent /ws/agent"
assert_exit_sh 2 "url/bad-scheme" "tm_validate_ws_url https://tunnel.example.com/ws/agent /ws/agent"
assert_exit_sh 2 "url/bad-suffix" "tm_validate_ws_url wss://tunnel.example.com/ws/client /ws/agent"
assert_eq "tunnel/tcp" "pg|tcp|127.0.0.1:15432|agent-db|db.internal|5432" \
  "$(tm_parse_tunnel_spec 'pg:tcp:127.0.0.1:15432:agent-db:db.internal:5432')"
assert_eq "tunnel/socks5-no-target" "s|socks5|127.0.0.1:10866|agent-a||" \
  "$(tm_parse_tunnel_spec 's:socks5:127.0.0.1:10866:agent-a::')"
assert_exit_sh 2 "tunnel/bad-protocol" "tm_parse_tunnel_spec 'pg:quic:127.0.0.1:1:agent-db:host:5432'"
assert_exit_sh 2 "tunnel/bad-port" "tm_parse_tunnel_spec 'pg:tcp:127.0.0.1:99999:agent-db:host:5432'"
assert_exit_sh 2 "tunnel/missing-fields" "tm_parse_tunnel_spec 'pg:tcp'"

# 参数解析
assert_eq "args/version" "v1.2.3" "$(tm_parse_common_args_stdout --print version --version v1.2.3)"
assert_eq "args/yes-flag" "1" "$(tm_parse_common_args_stdout --print yes --yes)"
assert_eq "args/yes-env" "1" "$(TM_ONECLICK_YES=1 tm_parse_common_args_stdout --print yes)"
assert_eq "args/mode" "system" "$(tm_parse_common_args_stdout --print mode --mode system)"
assert_eq "args/base-url" "https://mirror.example/releases" "$(tm_parse_common_args_stdout --print base-url --base-url https://mirror.example/releases)"
assert_exit_sh 2 "args/unknown" "tm_parse_common_args_stdout --print version --bogus"
assert_exit_sh 2 "args/bad-mode" "tm_parse_common_args_stdout --print mode --mode root"
assert_exit_sh 2 "args/missing-value" "tm_parse_common_args_stdout --print version --version"

# secret env file 权限
secretfile="$TM_TMP3/agent.env"; TM_TMP3="$(mktemp -d)"; mkdir -p "$TM_TMP3"
printf "TUNNELMESH_AGENT_TOKEN='abc'\n" >"$secretfile"; chmod 0644 "$secretfile"
assert_exit_sh 3 "secret-file/insecure-perm" "tm_load_secret_env_file '$secretfile'"
chmod 0600 "$secretfile"
assert_eq "secret-file/value" "abc" "$(tm_load_secret_env_file "$secretfile"; tm_secret_from_loaded TUNNELMESH_AGENT_TOKEN)"
rm -rf "$TM_TMP3"
```

> 测试里出现的 `tm_ask_stdio` / `tm_ask_bool_stdio` / `tm_ask_choice_stdio` / `tm_ask_http_port` / `tm_parse_common_args_stdout` 是**为可测性拆出的纯函数**：`tm_ask` = 「已有答案 → 返回；`--yes` → 默认值；否则 `tm_ask_stdio`」，`tm_parse_common_args` = 解析并写全局，`tm_parse_common_args_stdout` = 解析后打印关键结果供断言。实现时按这个拆分写，测试才能不依赖全局状态。`tm_ask_http_port` 只是 `tm_ask_stdio http_port "HTTP 监听端口" "8080"` 的语义化包装，供 server 角色使用。

- [x] **Step 2: 运行测试确认失败**

Run: `go test ./deploy/install/oneclick -run TestShellFunctionSuite -count=1`
Expected: FAIL —— `tm_ans_set: command not found`，`failure(s)` 计数 > 0。

- [x] **Step 3: 写最小实现**

```bash
# --- 答案存储（bash 3.2 无关联数组，用变量名映射）---
tm_ans_key() {
  case "$1" in
    *[!A-Za-z0-9._-]* | "") tm_die "$TM_EXIT_USAGE" "invalid answer key: '$1'" ;;
  esac
  local mapped
  mapped="$(printf '%s' "$1" | tr 'a-z.-' 'A-Z__')"
  printf 'TM_ANS_%s\n' "$mapped"
}
tm_ans_set() { local k; k="$(tm_ans_key "$1")"; printf -v "$k" '%s' "$2"; }
tm_ans_get() { local k; k="$(tm_ans_key "$1")"; eval "printf '%s' \"\${$k-}\""; }

# --- 输入通道 ---
tm_require_input_channel() {
  [[ "${TM_YES:-0}" == "1" ]] && return 0
  [[ -n "${TM_TTY:-}" ]] && return 0
  [[ -t 0 ]] && return 0
  [[ "${TM_ONECLICK_ALLOW_STDIN:-0}" == "1" ]] && return 0
  tm_die "$TM_EXIT_PREFLIGHT" "no interactive input channel: run with --yes (or TM_ONECLICK_YES=1) plus explicit flags, or execute the script from a terminal"
}

tm_read_line() { # <prompt> <default>
  local prompt="$1" default="$2" reply=""
  tm_require_input_channel
  printf '==> %s [%s]: ' "$prompt" "$default"
  if [[ -n "$TM_TTY" ]]; then IFS= read -r reply <"$TM_TTY" || reply=""
  else IFS= read -r reply || reply=""; fi
  printf '%s' "${reply:-$default}"
}

# tm_read_secret：优先 /dev/tty 并用 read -s 关闭回显；回退 stdin 时无法关回显，
# 因此显式警告一次，避免 token 被 CI 日志录下来。
tm_read_secret() { # <prompt>
  local prompt="$1" reply=""
  tm_require_input_channel
  if [[ -n "$TM_TTY" ]]; then
    printf '==> %s（输入不回显）: ' "$prompt"
    IFS= read -rs reply <"$TM_TTY" || reply=""
    printf '\n'
  else
    tm_warn "no tty available; secret input will be echoed by the piped stdin"
    IFS= read -r reply || reply=""
  fi
  printf '%s' "$reply"
}

# --- 交互原语（纯函数 *_stdio 版本便于测试；对外版本叠加「已有答案/默认值」语义）---
tm_ask_stdio() { local key="$1" prompt="$2" default="$3"; tm_read_line "$prompt" "$default"; }
tm_ask() { # <key> <prompt> <default>
  local current; current="$(tm_ans_get "$1")"
  if [[ -n "$current" ]]; then return 0; fi
  if [[ "${TM_YES:-0}" == "1" ]]; then tm_ans_set "$1" "$3"; return 0; fi
  tm_ans_set "$1" "$(tm_ask_stdio "$1" "$2" "$3")"
}

tm_normalize_bool() {
  case "$(printf '%s' "$1" | tr 'A-Z' 'a-z')" in
    y | yes | true | 1 | on) printf 'yes\n' ;;
    n | no | false | 0 | off | "") printf 'no\n' ;;
    *) return 1 ;;
  esac
}
tm_ask_bool_stdio() { # <key> <prompt> <yes|no>
  local reply attempt
  for attempt in 1 2 3; do
    reply="$(tm_read_line "$2 (y/n)" "$3")"
    if tm_normalize_bool "$reply" >/dev/null; then tm_normalize_bool "$reply"; return 0; fi
    tm_warn "请输入 y 或 n"
  done
  tm_die "$TM_EXIT_USAGE" "invalid yes/no answer after 3 attempts"
}
tm_ask_bool() {
  local current; current="$(tm_ans_get "$1")"
  [[ -n "$current" ]] && return 0
  if [[ "${TM_YES:-0}" == "1" ]]; then tm_ans_set "$1" "$3"; return 0; fi
  tm_ans_set "$1" "$(tm_ask_bool_stdio "$1" "$2" "$3")"
}

tm_ask_choice_stdio() { # <key> <prompt> <default> <choice...>
  local key="$1" prompt="$2" default="$3"; shift 3
  local choices=("$@") reply attempt ok c
  for attempt in 1 2 3; do
    reply="$(tm_read_line "$prompt (${choices[*]})" "$default")"
    ok=0
    for c in "${choices[@]}"; do [[ "$reply" == "$c" ]] && ok=1 && break; done
    if [[ "$ok" == "1" ]]; then printf '%s\n' "$reply"; return 0; fi
    tm_warn "不在可选值内：${choices[*]}"
  done
  tm_die "$TM_EXIT_USAGE" "invalid choice after 3 attempts"
}
tm_ask_choice() {
  local current; current="$(tm_ans_get "$1")"
  [[ -n "$current" ]] && return 0
  if [[ "${TM_YES:-0}" == "1" ]]; then tm_ans_set "$1" "$3"; return 0; fi
  tm_ans_set "$1" "$(tm_ask_choice_stdio "$1" "$2" "$3" "${@:4}")"
}

tm_ask_secret() { # <key> <prompt>
  local first second attempt
  for attempt in 1 2 3; do
    first="$(tm_read_secret "$2")"
    second="$(tm_read_secret "$2（再次输入确认）")"
    if [[ "$first" == "$second" && -n "$first" ]]; then tm_ans_set "$1" "$first"; return 0; fi
    tm_warn "两次输入不一致或为空，请重试"
  done
  tm_die "$TM_EXIT_USAGE" "secret confirmation failed after 3 attempts"
}

# --- 敏感值：文件 > 环境变量 > 交互；--yes 下缺值直接失败 ---
TM_SECRET_ENV_FILE="${TM_SECRET_ENV_FILE:-}"
TM_TOKEN_FILE="${TM_TOKEN_FILE:-}"
tm_secret_loaded_get() { local k; k="$(tm_ans_key "secret.$1")"; eval "printf '%s' \"\${$k-}\""; }
tm_secret_loaded_set() { local k; k="$(tm_ans_key "secret.$1")"; printf -v "$k" '%s' "$2"; }
tm_load_secret_env_file() { # <path>
  local path="$1" perms line key value
  [[ -f "$path" ]] || tm_die "$TM_EXIT_USAGE" "secret env file not found: $path"
  perms="$(tm_file_perms "$path")"
  case "$perms" in
    600 | 400 | 640) ;;
    *) tm_die "$TM_EXIT_PREFLIGHT" "secret env file must be 0600/0400/0640, got 0${perms}: $path" ;;
  esac
  while IFS= read -r line || [[ -n "$line" ]]; do
    case "$line" in '' | \#*) continue ;; esac
    key="${line%%=*}"; value="${line#*=}"
    value="${value%\'}"; value="${value#\'}"
    tm_secret_loaded_set "$key" "$value"
  done <"$path"
}
tm_secret_value() { # <key> <prompt> <env-var-name>
  local key="$1" prompt="$2" envname="$3" value=""
  value="$(tm_secret_loaded_get "$envname")"
  if [[ -z "$value" && -n "$TM_TOKEN_FILE" && "$key" == *token* ]]; then
    [[ -f "$TM_TOKEN_FILE" ]] || tm_die "$TM_EXIT_USAGE" "token file not found: $TM_TOKEN_FILE"
    value="$(head -n 1 "$TM_TOKEN_FILE")"
  fi
  [[ -n "$value" ]] || value="${!envname:-}"
  if [[ -z "$value" ]]; then
    if [[ "${TM_YES:-0}" == "1" ]]; then
      tm_die "$TM_EXIT_PREFLIGHT" "missing required secret for '${key}': set ${envname}, pass --token-file/--secret-env-file, or drop --yes to type it"
    fi
    tm_ask_secret "$key" "$prompt"
    value="$(tm_ans_get "$key")"
  fi
  tm_ans_set "$key" "$value"
}
```

> `tm_file_perms` 在 Task 4 与 `tm_write_file` 一起实现（`stat -f '%Lp'` / `stat -c '%a'` 双分支）。Task 3 的测试会用到它，因此实现顺序上把 `tm_file_perms` 放在 Task 3 里落地，Task 4 复用——**实现时就在本任务加上**：
>
> ```bash
> tm_file_perms() { # <path> → 三位八进制
>   if stat -c '%a' "$1" >/dev/null 2>&1; then stat -c '%a' "$1"; else stat -f '%Lp' "$1"; fi
> }
> ```

参数解析与校验：

```bash
TM_YES="${TM_ONECLICK_YES:-0}"
TM_UNINSTALL=0 TM_NO_SERVICE=0 TM_NO_START=0 TM_NO_ENABLE=0 TM_NO_LINGER=0
TM_KEEP_CONFIG=0 TM_RECONFIGURE=0 TM_CREATE_RUN_USER=0
TM_MODE="" TM_TARGET_USER="" TM_RUN_USER="" TM_BIN_DIR="" TM_CONFIG_DIR="" TM_STATE_DIR=""
TM_VERSION="latest" TM_ARCHIVE=""

tm_validate_ws_url() { # <url> <required-suffix>
  local url="$1" suffix="$2"
  [[ "$url" =~ ^wss?://[^[:space:]]+$ ]] || tm_die "$TM_EXIT_USAGE" "server URL must start with ws:// or wss://: '$url'"
  [[ "$url" == *"$suffix" ]] || tm_die "$TM_EXIT_USAGE" "server URL must end with ${suffix}: '$url'"
}

tm_parse_tunnel_spec() { # <name:protocol:listen:agent_id:target_host:target_port>
  local spec="$1" name protocol listen agent host port
  IFS=':' read -r name protocol listen agent host port <<<"$spec"
  [[ -n "$name" && -n "$protocol" && -n "$listen" && -n "$agent" ]] \
    || tm_die "$TM_EXIT_USAGE" "tunnel spec needs name:protocol:listen:agent_id[:target_host:target_port]: '$spec'"
  case "$protocol" in tcp | udp | http | socks5) ;; *) tm_die "$TM_EXIT_USAGE" "unknown protocol: '$protocol' (tcp|udp|http|socks5)" ;; esac
  [[ "$listen" =~ ^[^:]+:[0-9]+$ ]] || tm_die "$TM_EXIT_USAGE" "listen must be host:port: '$listen'"
  tm_validate_port "${listen##*:}" "listen port"
  if [[ "$protocol" != "socks5" ]]; then
    [[ -n "$host" && -n "$port" ]] || tm_die "$TM_EXIT_USAGE" "protocol ${protocol} requires target_host and target_port"
    tm_validate_port "$port" "target port"
  else
    host=""; port=""
  fi
  printf '%s|%s|%s|%s|%s|%s\n' "$name" "$protocol" "$listen" "$agent" "$host" "$port"
}

tm_validate_port() { # <port> <label>
  [[ "$1" =~ ^[0-9]+$ ]] || tm_die "$TM_EXIT_USAGE" "$2 must be numeric: '$1'"
  if [[ "$1" -lt 1 || "$1" -gt 65535 ]]; then tm_die "$TM_EXIT_USAGE" "$2 out of range 1-65535: '$1'"; fi
}

# tm_parse_common_args：解析通用 flag。角色入口先调用它，剩余的 "$@" 交给 tm_role_parse_args。
tm_parse_common_args() {
  while [[ $# -gt 0 ]]; do
    case "$1" in
      --version) [[ $# -ge 2 ]] || tm_die "$TM_EXIT_USAGE" "missing value for --version"; TM_VERSION="$2"; tm_ans_set version "$2"; shift 2 ;;
      --base-url) [[ $# -ge 2 ]] || tm_die "$TM_EXIT_USAGE" "missing value for --base-url"; TM_BASE_URL="$2"; shift 2 ;;
      --raw-base-url) [[ $# -ge 2 ]] || tm_die "$TM_EXIT_USAGE" "missing value for --raw-base-url"; TM_RAW_BASE_URL="$2"; shift 2 ;;
      --github-token) [[ $# -ge 2 ]] || tm_die "$TM_EXIT_USAGE" "missing value for --github-token"; TM_GITHUB_TOKEN="$2"; shift 2 ;;
      --archive) [[ $# -ge 2 ]] || tm_die "$TM_EXIT_USAGE" "missing value for --archive"; TM_ARCHIVE="$2"; shift 2 ;;
      --mode) [[ $# -ge 2 ]] || tm_die "$TM_EXIT_USAGE" "missing value for --mode"
        case "$2" in user | system) TM_MODE="$2" ;; *) tm_die "$TM_EXIT_USAGE" "invalid --mode: '$2' (user|system)" ;; esac; shift 2 ;;
      --user) [[ $# -ge 2 ]] || tm_die "$TM_EXIT_USAGE" "missing value for --user"; TM_TARGET_USER="$2"; shift 2 ;;
      --run-user) [[ $# -ge 2 ]] || tm_die "$TM_EXIT_USAGE" "missing value for --run-user"; TM_RUN_USER="$2"; shift 2 ;;
      --create-run-user) TM_CREATE_RUN_USER=1; shift ;;
      --bin-dir) [[ $# -ge 2 ]] || tm_die "$TM_EXIT_USAGE" "missing value for --bin-dir"; TM_BIN_DIR="$2"; shift 2 ;;
      --config-dir) [[ $# -ge 2 ]] || tm_die "$TM_EXIT_USAGE" "missing value for --config-dir"; TM_CONFIG_DIR="$2"; shift 2 ;;
      --state-dir) [[ $# -ge 2 ]] || tm_die "$TM_EXIT_USAGE" "missing value for --state-dir"; TM_STATE_DIR="$2"; shift 2 ;;
      --token-file) [[ $# -ge 2 ]] || tm_die "$TM_EXIT_USAGE" "missing value for --token-file"; TM_TOKEN_FILE="$2"; shift 2 ;;
      --secret-env-file) [[ $# -ge 2 ]] || tm_die "$TM_EXIT_USAGE" "missing value for --secret-env-file"; TM_SECRET_ENV_FILE="$2"; shift 2 ;;
      --server-url) [[ $# -ge 2 ]] || tm_die "$TM_EXIT_USAGE" "missing value for --server-url"; tm_ans_set server_url "$2"; shift 2 ;;
      --agent-id) [[ $# -ge 2 ]] || tm_die "$TM_EXIT_USAGE" "missing value for --agent-id"; tm_ans_set agent_id "$2"; shift 2 ;;
      --tunnel) [[ $# -ge 2 ]] || tm_die "$TM_EXIT_USAGE" "missing value for --tunnel"; TM_TUNNEL_SPECS+=("$2"); shift 2 ;;
      --yes) TM_YES=1; shift ;;
      --uninstall) TM_UNINSTALL=1; shift ;;
      --no-service) TM_NO_SERVICE=1; shift ;;
      --no-start) TM_NO_START=1; shift ;;
      --no-enable) TM_NO_ENABLE=1; shift ;;
      --no-linger) TM_NO_LINGER=1; shift ;;
      --no-cache) TM_NO_CACHE=1; shift ;;
      --keep-config) TM_KEEP_CONFIG=1; shift ;;
      --reconfigure) TM_RECONFIGURE=1; shift ;;
      -h | --help) tm_usage; exit "$TM_EXIT_OK" ;;
      --) shift; break ;;
      *) tm_die "$TM_EXIT_USAGE" "unknown argument: $1（--help 查看用法）" ;;
    esac
  done
  TM_REMAINING_ARGS=("$@")
}
# tm_parse_common_args_stdout [--print <field>] <args...>：解析后打印指定字段，供测试断言，
# 避免测试直接依赖全局变量。
tm_parse_common_args_stdout() {
  local field="version"
  if [[ "${1:-}" == "--print" ]]; then field="$2"; shift 2; fi
  tm_parse_common_args "$@"
  case "$field" in
    version) printf '%s\n' "$TM_VERSION" ;;
    yes) printf '%s\n' "$TM_YES" ;;
    mode) printf '%s\n' "$TM_MODE" ;;
    base-url) printf '%s\n' "$TM_BASE_URL" ;;
    *) tm_die "$TM_EXIT_USAGE" "unknown --print field: $field" ;;
  esac
}
```

> `TM_REMAINING_ARGS` 与 `TM_TUNNEL_SPECS` 是普通索引数组，需在 `tm_parse_common_args` 之前用 `TM_REMAINING_ARGS=()`、`TM_TUNNEL_SPECS=()` 初始化（`set -u` 下引用未初始化数组会报错）。

- [x] **Step 4: 运行测试确认通过**

Run: `go test ./deploy/install/oneclick -run TestShellFunctionSuite -count=1`
Expected: PASS，新增 `ok   ans/*`、`ok   ask/*`、`ok   url/*`、`ok   tunnel/*`、`ok   args/*`、`ok   secret-file/*` 全部通过，末行 `0 failure(s)`。

- [x] **Step 5: Commit（需授权）**

```bash
git add deploy/install/oneclick
git commit -m "feat(install): add interactive prompts and argument parsing to one-click installer"
```

---

### Task 2: 版本解析、下载、SHA256 校验与缓存

**Files:**
- Modify: `deploy/install/oneclick/tunnelmesh-install-common.sh`
- Modify: `deploy/install/oneclick/testdata/run_tests.sh`
- Create: `deploy/install/oneclick/testdata/bin/curl`（stub）
- Create: `deploy/install/oneclick/testdata/fixtures/releases__latest`（API JSON）、`testdata/fixtures/noapi/.gitkeep`
- Modify: `deploy/install/oneclick/oneclick_scripts_test.go`

**Interfaces:**
- Consumes: Task 1 的 `tm_die`、`tm_have`、`tm_info`、退出码常量、`tm_detect_arch`、`TM_VERSION_PATTERN`、`TM_DEFAULT_BASE_URL`、`TM_LATEST_API_URL`、全局 `TM_GOOS`/`TM_GOARCH`/`TM_ARCHIVE_EXT`。
- Produces:
  - `tm_fetch <url> <output-path|->`：脚本内唯一的 curl 内容出口，固定 `--fail --silent --show-error --location --max-time ${TM_HTTP_TIMEOUT:-120}`；`TM_GITHUB_TOKEN` 非空且 URL 以 `https://api.github.com/` 开头时追加 `Authorization: Bearer`；curl 非零退出即 `tm_die 4`。
  - `tm_fetch_head <url>`：`curl --silent --show-error --head --max-time 30 <url>`，输出响应头。
  - `tm_checksum_file <path>`：`sha256sum` 优先、回退 `shasum -a 256`，输出 64 位十六进制。
  - `tm_verify_checksum <file> <sums-file> <archive-name>`：期望值取 `grep "  <archive-name>$"`；缺条目或不匹配 `tm_die 5` 并打印 expected/actual。
  - `tm_archive_name <version>` → `tunnelmesh-<version>-<TM_GOOS>-<TM_GOARCH>.<TM_ARCHIVE_EXT>`。
  - `tm_cache_dir` → `${TM_CACHE_DIR:-${XDG_CACHE_HOME:-$HOME/.cache}/tunnelmesh/releases}`。
  - `tm_cache_lookup <archive-name> <sums-file>`：命中且校验通过 → 打印缓存路径并返回 0；命中但损坏 → 删除条目返回 1；未命中返回 1。
  - `tm_validate_version <version>`、`tm_resolve_version_stdout`、`tm_resolve_version`（写回 `TM_VERSION`）。
  - `tm_download_release <version>`：填充 `TM_WORKDIR`、`TM_SUMS_PATH`、`TM_ARCHIVE_PATH`，完成校验并按需写缓存。
  - `tm_extract_archive <archive> <dest-dir>`、`tm_load_lib`（三级回退定位/下载共享库）。
  - 全局：`TM_BASE_URL`、`TM_RAW_BASE_URL`、`TM_NO_CACHE`、`TM_CACHE_DIR`、`TM_GITHUB_TOKEN`、`TM_HTTP_TIMEOUT`。

- [x] **Step 1: 写失败测试 —— curl stub**

创建可执行文件 `deploy/install/oneclick/testdata/bin/curl`：

```bash
#!/usr/bin/env bash
# curl stub：只支持共享库使用的调用形态。
#   curl <flags...> <url> [--output <path>|<path>|-]   取内容
#   curl --head <flags...> <url>                       取响应头
# 环境变量：
#   CURL_STUB_FIXTURES  fixture 目录；按 URL 末两段 <parent>__<name> 查找，回退到 <name>
#   CURL_STUB_LOG       追加记录每个被请求的 URL（断言「缓存命中不再下载」）
#   CURL_STUB_REDIRECT  --head 请求返回的 Location 值（模拟 /releases/latest 302）
set -uo pipefail
fixtures="${CURL_STUB_FIXTURES:?CURL_STUB_FIXTURES must be set}"
log="${CURL_STUB_LOG:-/dev/null}"
url="" out="" head=0
args=("$@") i=0
while [[ $i -lt ${#args[@]} ]]; do
  a="${args[$i]}"
  case "$a" in
    --output|-o) i=$((i + 1)); out="${args[$i]}" ;;
    --head|-I) head=1 ;;
    -*) ;;
    *) url="$a" ;;
  esac
  i=$((i + 1))
done
printf '%s\n' "$url" >>"$log"
if [[ "$head" -eq 1 ]]; then
  if [[ -n "${CURL_STUB_REDIRECT:-}" ]]; then
    printf 'HTTP/1.1 302 Found\r\nLocation: %s\r\n\r\n' "$CURL_STUB_REDIRECT"
    exit 0
  fi
  exit 22
fi
name="${url##*/}"
parent="$(basename "$(dirname "$url")")"
src="${fixtures}/${parent}__${name}"
[[ -f "$src" ]] || src="${fixtures}/${name}"
if [[ ! -f "$src" ]]; then
  printf 'curl: (22) The requested URL returned error: 404\n' >&2
  exit 22
fi
if [[ -n "$out" && "$out" != "-" ]]; then cp "$src" "$out"; else cat "$src"; fi
```

创建 `deploy/install/oneclick/testdata/fixtures/releases__latest`：

```json
{"tag_name":"v9.9.9","name":"TunnelMesh v9.9.9"}
```

创建空目录标记 `deploy/install/oneclick/testdata/fixtures/noapi/.gitkeep`（空文件）。该目录用于「API 不可用 → 回退重定向」的用例：把 `CURL_STUB_FIXTURES` 指向它，API 请求 404，`tm_fetch_head` 才生效。

- [x] **Step 2: 写失败测试 —— 追加到 `testdata/run_tests.sh`**

在 `printf '\n%s: %d failure(s)\n'` 之前插入：

```bash
# --- Task 2: 下载、校验、缓存 ---
# fixture 归档按本机架构现场生成，仓库里不提交二进制。
TM_TMP="$(mktemp -d)"
FIXDIR="$TM_TMP/fixtures"
mkdir -p "$FIXDIR" "$TM_TMP/cache" "$TM_TMP/bin"
cp "$HERE/fixtures/releases__latest" "$FIXDIR/releases__latest"
TM_GOOS="$(uname -s | tr 'A-Z' 'a-z')"
TM_GOARCH="$(tm_detect_arch "$(uname -m)")"
TM_ARCHIVE_EXT="tar.gz"
archive_name="$(tm_archive_name v9.9.9)"
stage="$TM_TMP/stage"; mkdir -p "$stage"
for role in server agent client; do
  printf '#!/bin/sh\necho stub-%s\n' "$role" >"$stage/tunnelmesh-$role"
  chmod +x "$stage/tunnelmesh-$role"
done
tar -czf "$FIXDIR/v9.9.9__${archive_name}" -C "$stage" tunnelmesh-server tunnelmesh-agent tunnelmesh-client
sum="$(tm_checksum_file "$FIXDIR/v9.9.9__${archive_name}")"
printf '%s  %s\n' "$sum" "$archive_name" >"$FIXDIR/v9.9.9__SHA256SUMS"
printf '%s  %s\n' "$(printf '0%.0s' 1 2 3 4 5 6 7 8 9 10 11 12 13 14 15 16 17 18 19 20 21 22 23 24 25 26 27 28 29 30 31 32 33 34 35 36 37 38 39 40 41 42 43 44 45 46 47 48 49 50 51 52 53 54 55 56 57 58 59 60 61 62 63 64)" "$archive_name" >"$TM_TMP/SHA256SUMS.bad"
export PATH="$HERE/bin:$PATH"
export CURL_STUB_FIXTURES="$FIXDIR" TM_CACHE_DIR="$TM_TMP/cache"

assert_eq "archive-name" "tunnelmesh-v9.9.9-${TM_GOOS}-${TM_GOARCH}.tar.gz" "$archive_name"
assert_exit_sh 0 "checksum/ok" "TM_GOOS=$TM_GOOS TM_GOARCH=$TM_GOARCH TM_ARCHIVE_EXT=tar.gz tm_verify_checksum '$FIXDIR/v9.9.9__${archive_name}' '$FIXDIR/v9.9.9__SHA256SUMS' '$archive_name'"
assert_exit_sh 5 "checksum/mismatch" "tm_verify_checksum '$FIXDIR/v9.9.9__${archive_name}' '$TM_TMP/SHA256SUMS.bad' '$archive_name'"
assert_exit_sh 5 "checksum/missing-entry" "tm_verify_checksum '$FIXDIR/v9.9.9__${archive_name}' '$FIXDIR/v9.9.9__SHA256SUMS' 'not-in-sums.tar.gz'"
assert_exit_sh 2 "version/invalid" "tm_validate_version 'v1.2'"
assert_exit_sh 2 "version/no-prefix" "tm_validate_version '1.2.3'"

# latest 解析：API 优先
export CURL_STUB_LOG="$TM_TMP/log-api"
assert_eq "version/api" "v9.9.9" "$(TM_VERSION=latest tm_resolve_version_stdout)"
assert_contains "version/api-url" "$(cat "$CURL_STUB_LOG")" "api.github.com/repos/nnworld/TunnelMesh/releases/latest"

# latest 解析：API 404 时回退到 /releases/latest 的 302 Location
CURL_STUB_FIXTURES="$HERE/fixtures/noapi" CURL_STUB_LOG="$TM_TMP/log-redir" \
  CURL_STUB_REDIRECT="https://github.com/nnworld/TunnelMesh/releases/tag/v9.9.9" \
  resolved="$(TM_VERSION=latest tm_resolve_version_stdout)"
export CURL_STUB_FIXTURES="$FIXDIR"
assert_eq "version/redirect-fallback" "v9.9.9" "$resolved"
assert_contains "version/redirect-url" "$(cat "$TM_TMP/log-redir")" "/releases/latest"

# 缓存：首次下载 → 命中不再下载 → 损坏自动重下
export CURL_STUB_LOG="$TM_TMP/log-dl1"
TM_NO_CACHE=0 tm_download_release v9.9.9
assert_eq "cache/file-created" "yes" "$([[ -s "$TM_TMP/cache/$archive_name" ]] && echo yes)"
CURL_STUB_LOG="$TM_TMP/log-dl2" TM_NO_CACHE=0 TM_WORKDIR="" tm_download_release v9.9.9
assert_eq "cache/hit-no-archive-download" "0" "$(grep -c "download/v9.9.9/${archive_name}\$" "$TM_TMP/log-dl2" || true)"
printf 'corrupted' >"$TM_TMP/cache/$archive_name"
CURL_STUB_LOG="$TM_TMP/log-dl3" TM_NO_CACHE=0 tm_download_release v9.9.9
assert_eq "cache/corrupt-recovered" "yes" "$(tm_verify_checksum "$TM_TMP/cache/$archive_name" "$FIXDIR/v9.9.9__SHA256SUMS" "$archive_name" >/dev/null 2>&1 && echo yes)"
assert_contains "cache/corrupt-redownloaded" "$(cat "$TM_TMP/log-dl3")" "$archive_name"
CURL_STUB_LOG="$TM_TMP/log-dl4" TM_NO_CACHE=1 tm_download_release v9.9.9
assert_contains "cache/bypassed" "$(cat "$TM_TMP/log-dl4")" "download/v9.9.9/${archive_name}"

# 解压
extract="$TM_TMP/extract"
tm_extract_archive "$TM_TMP/cache/$archive_name" "$extract"
assert_eq "extract/agent-binary" "yes" "$([[ -x "$extract/tunnelmesh-agent" ]] && echo yes)"
rm -rf "$TM_TMP"
```

> `tm_download_release` 内部会 `mktemp -d` 覆盖 `TM_WORKDIR`，但缓存命中分支不清理临时目录——由 `tm_main` 的 `trap cleanup EXIT INT TERM` 统一清理。测试里连续调用三次不会互相干扰，因为断言只看 `TM_TMP/cache` 与 `CURL_STUB_LOG`。

- [x] **Step 3: 运行测试确认失败**

Run: `go test ./deploy/install/oneclick -run TestShellFunctionSuite -count=1`
Expected: FAIL —— `run_tests.sh: tm_archive_name: command not found`，末行非 `0 failure(s)`，Go 测试 `Fatalf` 并打印套件输出。

- [x] **Step 4: 写最小实现**

在 `tunnelmesh-install-common.sh` 末尾追加：

```bash
# --- 下载、校验、缓存 ---
TM_HTTP_TIMEOUT="${TM_HTTP_TIMEOUT:-120}"
TM_BASE_URL="${TUNNELMESH_RELEASE_BASE_URL:-$TM_DEFAULT_BASE_URL}"
TM_RAW_BASE_URL="${TUNNELMESH_RAW_BASE_URL:-$TM_DEFAULT_RAW_BASE_URL}"
TM_GITHUB_TOKEN="${TUNNELMESH_GITHUB_TOKEN:-${TM_GITHUB_TOKEN:-}}"
TM_NO_CACHE="${TM_NO_CACHE:-0}"
TM_CACHE_DIR="${TM_CACHE_DIR:-}"
TM_WORKDIR="" TM_ARCHIVE_PATH="" TM_SUMS_PATH="" TM_EXTRACT_DIR=""

# tm_fetch 是唯一的 curl 内容出口，测试用 PATH 前置的 stub 覆盖它。
tm_fetch() {
  local url="$1" out="$2"
  local args=(--fail --silent --show-error --location --max-time "$TM_HTTP_TIMEOUT")
  if [[ -n "$TM_GITHUB_TOKEN" && "$url" == https://api.github.com/* ]]; then
    args+=(--header "Authorization: Bearer ${TM_GITHUB_TOKEN}")
  fi
  if [[ "$out" == "-" ]]; then
    curl "${args[@]}" "$url" || tm_die "$TM_EXIT_DOWNLOAD" "download failed: ${url}"
  else
    curl "${args[@]}" "$url" --output "$out" || tm_die "$TM_EXIT_DOWNLOAD" "download failed: ${url}"
  fi
}

tm_fetch_head() {
  curl --silent --show-error --head --max-time 30 "$1"
}

tm_checksum_file() {
  if tm_have sha256sum; then sha256sum "$1" | awk '{print $1}'; else shasum -a 256 "$1" | awk '{print $1}'; fi
}

tm_verify_checksum() { # <file> <sums-file> <archive-name>
  local file="$1" sums="$2" name="$3" line expected actual
  line="$(grep "  ${name}\$" "$sums" || true)"
  [[ -n "$line" ]] || tm_die "$TM_EXIT_CHECKSUM" "checksum entry not found for ${name}"
  expected="${line%%[[:space:]]*}"
  actual="$(tm_checksum_file "$file")"
  if [[ "$expected" != "$actual" ]]; then
    tm_die "$TM_EXIT_CHECKSUM" "checksum mismatch for ${name} (expected ${expected}, actual ${actual})"
  fi
}

tm_archive_name() { printf 'tunnelmesh-%s-%s-%s.%s\n' "$1" "$TM_GOOS" "$TM_GOARCH" "$TM_ARCHIVE_EXT"; }

tm_cache_dir() {
  if [[ -n "$TM_CACHE_DIR" ]]; then printf '%s\n' "$TM_CACHE_DIR"
  else printf '%s/tunnelmesh/releases\n' "${XDG_CACHE_HOME:-$HOME/.cache}"; fi
}

tm_validate_version() {
  [[ "$1" =~ $TM_VERSION_PATTERN ]] \
    || tm_die "$TM_EXIT_USAGE" "invalid version: expected vMAJOR.MINOR.PATCH, got '$1'"
}

tm_resolve_version_stdout() {
  local tag=""
  tag="$(tm_fetch "$TM_LATEST_API_URL" - 2>/dev/null | sed -n 's/.*"tag_name"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' | head -n 1 || true)"
  if [[ -z "$tag" ]]; then
    # API 配额耗尽或不可达时回退：/releases/latest 的 302 Location 里带 tag，不消耗 API 配额。
    tag="$(tm_fetch_head "${TM_BASE_URL}/latest" 2>/dev/null | sed -n 's#^[Ll]ocation:.*\/tag\/\(v[0-9][^[:space:]]*\).*#\1#p' | head -n 1 || true)"
  fi
  [[ -n "$tag" ]] || tm_die "$TM_EXIT_DOWNLOAD" "cannot resolve latest version; pass --version vX.Y.Z explicitly"
  printf '%s\n' "$tag"
}

tm_resolve_version() {
  if [[ "$TM_VERSION" == "latest" ]]; then
    TM_VERSION="$(tm_resolve_version_stdout)"
    tm_info "resolved latest version: ${TM_VERSION}"
  fi
  tm_validate_version "$TM_VERSION"
}

# tm_cache_lookup 依赖「命令替换子 shell 里 tm_die 的 exit 只结束子 shell」这一隔离，
# 因此调用方必须写成 if cached="$(tm_cache_lookup ...)"; then ...，不要改成直接赋值。
tm_cache_lookup() { # <archive-name> <sums-file>
  local name="$1" sums="$2" path
  path="$(tm_cache_dir)/${name}"
  [[ -f "$path" ]] || return 1
  if tm_verify_checksum "$path" "$sums" "$name" 2>/dev/null; then
    printf '%s\n' "$path"; return 0
  fi
  rm -f -- "$path"
  return 1
}

tm_download_release() { # <version>
  local version="$1" name cached
  name="$(tm_archive_name "$version")"
  TM_WORKDIR="$(mktemp -d)"
  TM_SUMS_PATH="${TM_WORKDIR}/SHA256SUMS"
  tm_fetch "${TM_BASE_URL}/download/${version}/SHA256SUMS" "$TM_SUMS_PATH"
  if [[ "$TM_NO_CACHE" != "1" ]] && cached="$(tm_cache_lookup "$name" "$TM_SUMS_PATH")"; then
    tm_info "using cached archive ${cached}"
    TM_ARCHIVE_PATH="$cached"
    return 0
  fi
  TM_ARCHIVE_PATH="${TM_WORKDIR}/${name}"
  tm_info "downloading ${name}"
  tm_fetch "${TM_BASE_URL}/download/${version}/${name}" "$TM_ARCHIVE_PATH"
  tm_verify_checksum "$TM_ARCHIVE_PATH" "$TM_SUMS_PATH" "$name"
  if [[ "$TM_NO_CACHE" != "1" ]]; then
    mkdir -p "$(tm_cache_dir)"
    cp -f -- "$TM_ARCHIVE_PATH" "$(tm_cache_dir)/${name}"
  fi
}

tm_extract_archive() { # <archive> <dest-dir>
  mkdir -p "$2"
  case "$1" in
    *.tar.gz) tar -xzf "$1" -C "$2" ;;
    *.zip) unzip -q "$1" -d "$2" ;;
    *) tm_die "$TM_EXIT_USAGE" "unsupported archive type: $1" ;;
  esac
}

# tm_load_lib：三级回退定位共享库（见设计规格 §5）。入口脚本在 source 之前调用它，
# 因此这里不能依赖库内函数——只用 POSIX 内建与 curl。
tm_load_lib() { # <entry-dir> <ref>
  local dir="$1" ref="${2:-main}" candidate out
  if [[ -n "${TM_ONECLICK_LIB:-}" && -f "$TM_ONECLICK_LIB" ]]; then
    printf '%s\n' "$TM_ONECLICK_LIB"; return 0
  fi
  candidate="${dir}/tunnelmesh-install-common.sh"
  if [[ -n "$dir" && -f "$candidate" ]]; then printf '%s\n' "$candidate"; return 0; fi
  out="$(mktemp -d)/tunnelmesh-install-common.sh"
  curl --fail --silent --show-error --location --max-time 60 \
    "${TM_RAW_BASE_URL:-https://raw.githubusercontent.com/nnworld/TunnelMesh}/${ref}/deploy/install/oneclick/tunnelmesh-install-common.sh" \
    --output "$out" || return 4
  [[ -s "$out" ]] || return 4
  [[ "$(head -n 1 "$out")" == '#!/usr/bin/env bash' ]] || return 4
  printf '%s\n' "$out"
}
```

- [x] **Step 5: 运行测试确认通过**

Run: `go test ./deploy/install/oneclick -run TestShellFunctionSuite -count=1`
Expected: PASS，输出含 `ok   checksum/ok`、`ok   checksum/mismatch`、`ok   version/api`、`ok   version/redirect-fallback`、`ok   cache/hit-no-archive-download`、`ok   cache/corrupt-recovered`、`ok   cache/bypassed`、`ok   extract/agent-binary`，末行 `0 failure(s)`。

- [x] **Step 6: 追加 Release 契约一致性测试**

在 `oneclick_scripts_test.go` 追加：

```go
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
```

Run: `go test ./deploy/install/oneclick -count=1`
Expected: PASS（Task 1 的 4 个测试 + 本任务 2 个测试）。

- [x] **Step 7: Commit（需授权）** —— 已按用户授权合并为收尾单次提交，提交信息见 PR 记录

```bash
git add deploy/install/oneclick
git commit -m "feat(install): download, verify and cache releases in one-click installer"
```

---

### Task 1: 共享库骨架、日志原语与平台探测

**Files:**
- Create: `deploy/install/oneclick/tunnelmesh-install-common.sh`
- Create: `deploy/install/oneclick/testdata/run_tests.sh`
- Create: `deploy/install/oneclick/oneclick_scripts_test.go`

**Interfaces:**
- Consumes: 无（第一个任务）。
- Produces:
  - 常量 `TM_EXIT_OK=0 TM_EXIT_USAGE=2 TM_EXIT_PREFLIGHT=3 TM_EXIT_DOWNLOAD=4 TM_EXIT_CHECKSUM=5 TM_EXIT_CONFIG=6 TM_EXIT_SERVICE=7 TM_EXIT_UNINSTALL=8`
  - 常量 `TM_REPOSITORY="nnworld/TunnelMesh"`、`TM_DEFAULT_BASE_URL`、`TM_DEFAULT_RAW_BASE_URL`、`TM_LATEST_API_URL`、`TM_DEFAULT_INSTALL_MODE="user"`
  - `tm_die <exit-code> <message...>`（写 stderr，前缀 `ERROR: `，以给定码退出）
  - `tm_warn <message...>`（stderr，前缀 `WARNING: `）、`tm_info <message...>`（stdout，前缀 `==> `）
  - `tm_have <cmd>`（0/1）、`tm_require_cmds <cmd...>`（缺失时 `tm_die 3`）
  - `tm_detect_platform`：读取 `uname -s`/`uname -m`，设置全局 `TM_OS_FAMILY`(linux|darwin)、`TM_GOOS`、`TM_GOARCH`、`TM_ARCHIVE_EXT`(tar.gz)；不支持时 `tm_die 2`
  - `tm_detect_arch <machine>`：纯函数，`x86_64→amd64`、`aarch64|arm64→arm64`，其它输出空并非零退出
  - `tm_mask <secret>`：长度 ≤4 输出 `****`，否则前 4 位 + `****`；空串输出 `(empty)`
  - `tm_tty_init`：设置全局 `TM_TTY`——`/dev/tty` 可打开时为其路径，否则空串（调用方回退 stdin）。所有交互读取都必须走它，因为 `curl | bash` 时 stdin 是管道
  - `tm_entry_init <argv0> <bash-source0>`：设置 `TM_ONECLICK_ENTRY`（入口脚本真实路径）与 `TM_ONECLICK_DIR`（其所在目录）；`bash -c`/管道下两者都取不到真实路径时置空，由调用方走 raw 下载回退
  - 全局 `TM_ONECLICK_YES`（`1` 等价于 `--yes`）、`TM_ONECLICK_REF`（共享库的 git ref，默认 `main`）

- [x] **Step 1: 写失败测试 —— bash 函数套件骨架**

创建 `deploy/install/oneclick/testdata/run_tests.sh`：

```bash
#!/usr/bin/env bash
# 函数级测试套件：由 oneclick_scripts_test.go 调用，也可手工执行。
# 只测纯函数与可注入依赖的函数，不联网、不注册服务。
set -uo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
LIB="${TM_ONECLICK_LIB:-$HERE/../tunnelmesh-install-common.sh}"
# shellcheck source=/dev/null
source "$LIB"

FAILURES=0
assert_eq() { # assert_eq <name> <expected> <actual>
  if [[ "$2" == "$3" ]]; then
    printf 'ok   %s\n' "$1"
  else
    printf 'FAIL %s\n  expected: %s\n  actual:   %s\n' "$1" "$2" "$3"
    FAILURES=$((FAILURES + 1))
  fi
}
assert_contains() { # assert_contains <name> <haystack> <needle>
  case "$2" in
    *"$3"*) printf 'ok   %s\n' "$1" ;;
    *) printf 'FAIL %s\n  missing: %s\n  in:      %s\n' "$1" "$3" "$2"; FAILURES=$((FAILURES + 1)) ;;
  esac
}
assert_exit_sh() { # assert_exit_sh <expected-code> <name> <shell-snippet>
  # tm_die 直接 exit，被测片段必须跑在子 shell 里才能断言退出码而不终止套件。
  local want="$1" name="$2" snippet="$3" got=0
  TM_ONECLICK_LIB="$LIB" bash -c "source \"\$TM_ONECLICK_LIB\"; $snippet" >/dev/null 2>&1 || got=$?
  assert_eq "$name" "$want" "$got"
}

# --- Task 1: 平台探测与掩码 ---
assert_eq "arch/x86_64" "amd64" "$(tm_detect_arch x86_64)"
assert_eq "arch/aarch64" "arm64" "$(tm_detect_arch aarch64)"
assert_eq "arch/arm64" "arm64" "$(tm_detect_arch arm64)"
assert_eq "arch/unknown-empty" "" "$(tm_detect_arch riscv64 || true)"
assert_eq "mask/long" "abcd****" "$(tm_mask abcdefgh)"
assert_eq "mask/short" "****" "$(tm_mask abc)"
assert_eq "mask/empty" "(empty)" "$(tm_mask '')"
assert_eq "exit-codes" "0 2 3 4 5 6 7 8" \
  "$TM_EXIT_OK $TM_EXIT_USAGE $TM_EXIT_PREFLIGHT $TM_EXIT_DOWNLOAD $TM_EXIT_CHECKSUM $TM_EXIT_CONFIG $TM_EXIT_SERVICE $TM_EXIT_UNINSTALL"
assert_eq "default-mode" "user" "$TM_DEFAULT_INSTALL_MODE"
tm_tty_init
assert_contains "tty/value" "/dev/tty " "${TM_TTY} "
tm_entry_init "bash" "main"
assert_eq "entry/bash-c-empty" "" "$TM_ONECLICK_ENTRY"
tm_entry_init "$HERE/fixtures/fake-entry.sh" "$HERE/fixtures/fake-entry.sh"
assert_eq "entry/real-path" "$HERE/fixtures/fake-entry.sh" "$TM_ONECLICK_ENTRY"
tm_detect_platform
assert_contains "platform/family" "linux darwin" "$TM_OS_FAMILY"
assert_eq "platform/ext" "tar.gz" "$TM_ARCHIVE_EXT"

printf '\n%s: %d failure(s)\n' "$0" "$FAILURES"
[[ "$FAILURES" -eq 0 ]]
```

同一 Step 内创建 `tm_entry_init` 断言用的 fixture：

```bash
mkdir -p deploy/install/oneclick/testdata/fixtures
printf '#!/usr/bin/env bash\nexit 0\n' >deploy/install/oneclick/testdata/fixtures/fake-entry.sh
chmod +x deploy/install/oneclick/testdata/fixtures/fake-entry.sh
```

- [x] **Step 2: 写失败测试 —— Go 契约测试骨架**

创建 `deploy/install/oneclick/oneclick_scripts_test.go`：

```go
package oneclick

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
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

func TestShellFunctionSuite(t *testing.T) {
	bash := bashPath(t)
	cmd := exec.Command(bash, filepath.Join("testdata", "run_tests.sh"))
	cmd.Env = append(os.Environ(), "TM_ONECLICK_LIB="+commonLib)
	out, err := cmd.CombinedOutput()
	t.Logf("run_tests.sh output:\n%s", out)
	if err != nil {
		t.Fatalf("function suite failed: %v", err)
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
	files := append(append([]string{}, shellEntries...), commonLib)
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
```

- [x] **Step 3: 运行测试确认失败**

Run: `go test ./deploy/install/oneclick -run 'TestShell' -count=1`
Expected: FAIL —— `TestShellFilesExistAndAreExecutable` 报 `stat tunnelmesh-install-common.sh: no such file or directory`（库文件尚未创建）。

- [x] **Step 4: 写最小实现 —— 共享库骨架**

创建 `deploy/install/oneclick/tunnelmesh-install-common.sh`：

```bash
#!/usr/bin/env bash
# TunnelMesh 一键安装共享库（Linux / macOS）。
# 三个角色入口 install-{server,agent,client}.sh 只做参数透传与角色钩子，
# 下载、校验、交互、渲染、服务注册全部集中在这里，避免三份实现漂移。
# 本文件不得包含任何真实 token、密码、DSN 或私钥。
set -euo pipefail

# --- 退出码契约（docs/deployment/oneclick-install.md 的排障表以此为准）---
TM_EXIT_OK=0
TM_EXIT_USAGE=2
TM_EXIT_PREFLIGHT=3
TM_EXIT_DOWNLOAD=4
TM_EXIT_CHECKSUM=5
TM_EXIT_CONFIG=6
TM_EXIT_SERVICE=7
TM_EXIT_UNINSTALL=8

TM_REPOSITORY="nnworld/TunnelMesh"
TM_DEFAULT_BASE_URL="https://github.com/${TM_REPOSITORY}/releases"
TM_DEFAULT_RAW_BASE_URL="https://raw.githubusercontent.com/${TM_REPOSITORY}"
TM_LATEST_API_URL="https://api.github.com/repos/${TM_REPOSITORY}/releases/latest"
TM_LIB_RELATIVE_PATH="deploy/install/oneclick/tunnelmesh-install-common.sh"
# Linux 默认装到当前用户（systemd user unit）；--mode system 才走 root + /etc/tunnelmesh。
TM_DEFAULT_INSTALL_MODE="user"
TM_VERSION_PATTERN='^v[0-9]+\.[0-9]+\.[0-9]+$'

# --- 日志原语：正常输出走 stdout，错误与警告走 stderr ---
tm_info() { printf '==> %s\n' "$*"; }
tm_warn() { printf 'WARNING: %s\n' "$*" >&2; }
tm_die() {
  local code="$1"; shift
  printf 'ERROR: %s\n' "$*" >&2
  exit "$code"
}

tm_have() { command -v "$1" >/dev/null 2>&1; }

tm_require_cmds() {
  local missing=()
  local cmd
  for cmd in "$@"; do
    tm_have "$cmd" || missing+=("$cmd")
  done
  if [[ ${#missing[@]} -gt 0 ]]; then
    tm_die "$TM_EXIT_PREFLIGHT" "missing required command(s): ${missing[*]}"
  fi
}

# tm_detect_arch <uname -m 输出>：纯函数，便于测试；未知架构输出空并非零退出。
tm_detect_arch() {
  case "$1" in
    x86_64) printf 'amd64\n' ;;
    aarch64 | arm64) printf 'arm64\n' ;;
    *) return 1 ;;
  esac
}

# tm_detect_platform：设置 TM_OS_FAMILY / TM_GOOS / TM_GOARCH / TM_ARCHIVE_EXT。
# 公网入口只有 HTTP/HTTPS/WebSocket，因此 Windows 归档是 zip，其余是 tar.gz。
tm_detect_platform() {
  local kernel machine arch
  kernel="$(uname -s)"
  machine="$(uname -m)"
  case "$kernel" in
    Linux) TM_OS_FAMILY="linux"; TM_GOOS="linux"; TM_ARCHIVE_EXT="tar.gz" ;;
    Darwin) TM_OS_FAMILY="darwin"; TM_GOOS="darwin"; TM_ARCHIVE_EXT="tar.gz" ;;
    *) tm_die "$TM_EXIT_USAGE" "unsupported operating system: ${kernel} (Windows 请使用 install-<role>.ps1)" ;;
  esac
  arch="$(tm_detect_arch "$machine")" || tm_die "$TM_EXIT_USAGE" "unsupported architecture: ${machine}"
  TM_GOARCH="$arch"
}

# tm_tty_init：交互提示统一从 /dev/tty 读，使 `curl | bash`（stdin 是管道）也能问答。
# 无 tty 时 TM_TTY 为空串，调用方回退 stdin；若同时没有 --yes，tm_ask 会以退出码 3 失败，
# 绝不静默地把所有答案取成默认值。
tm_tty_init() {
  if [[ -r /dev/tty && -w /dev/tty ]] && : >/dev/tty 2>/dev/null; then
    TM_TTY="/dev/tty"
  else
    TM_TTY=""
  fi
}

# tm_entry_init <argv0> <BASH_SOURCE[0]>：解析入口脚本真实路径。
# `bash -c "$(curl ...)"` 下 $0 是 bash、BASH_SOURCE 是 main，两者都不是路径，
# 因此这里只接受「存在的普通文件」，否则留空让共享库走 raw 下载回退。
tm_entry_init() {
  local candidate
  TM_ONECLICK_ENTRY="" TM_ONECLICK_DIR=""
  for candidate in "$2" "$1"; do
    if [[ -n "$candidate" && -f "$candidate" ]]; then
      TM_ONECLICK_ENTRY="$(cd "$(dirname "$candidate")" && pwd)/$(basename "$candidate")"
      TM_ONECLICK_DIR="$(dirname "$TM_ONECLICK_ENTRY")"
      return 0
    fi
  done
  return 0
}

TM_ONECLICK_YES="${TM_ONECLICK_YES:-0}"
TM_ONECLICK_REF="${TM_ONECLICK_REF:-main}"

# tm_mask <secret>：摘要与日志里只显示前 4 位。
tm_mask() {
  local value="$1"
  if [[ -z "$value" ]]; then
    printf '(empty)\n'
  elif [[ ${#value} -le 4 ]]; then
    printf '****\n'
  else
    printf '%s****\n' "${value:0:4}"
  fi
}
```

- [x] **Step 5: 运行测试确认通过**

Run: `go test ./deploy/install/oneclick -run 'TestShell|TestNoHardcoded' -count=1`
Expected: PASS（4 个测试全绿；`run_tests.sh` 输出 12 行 `ok`）。

- [x] **Step 6: 既有测试不回归**

Run: `go test ./deploy/... ./scripts/... -count=1`
Expected: PASS（`deploy/install/install_templates_test.go` 与 `scripts/install_script_test.go` 不受影响）。

- [x] **Step 7: Commit（需授权）** —— 已按用户授权合并为收尾单次提交，提交信息见 PR 记录

```bash
git add deploy/install/oneclick/tunnelmesh-install-common.sh deploy/install/oneclick/oneclick_scripts_test.go deploy/install/oneclick/testdata/run_tests.sh
git commit -m "feat(install): scaffold one-click installer library"
```

---
