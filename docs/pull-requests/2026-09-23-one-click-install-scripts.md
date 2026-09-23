# 三端一键安装脚本（server / agent / client）

## Title

feat(install): 三平台 × 三角色一键安装脚本（.sh / .ps1）与 user 模式 node identity 修复

## Target branch

`main`（实现分支 `codex/one-click-install`）。

## Summary

为 server / agent / client 三个角色各提供一条命令的交互式一键安装，覆盖
「下载 → SHA256 校验 → 安装二进制 → 渲染配置 → 配置校验 → 注册服务 → 启动 → 自检摘要」，
并支持升级、回滚与卸载。

架构是「入口薄、共享库厚」：`tm_main`（PowerShell 侧 `Invoke-TmMain`）用模板方法固定 8 个阶段，
三个入口只声明角色问答、配置渲染与安装后动作。下载、校验、缓存、交互、渲染、服务生命周期
全部集中在 `tunnelmesh-install-common.sh` / `.ps1`，服务模板复用仓库既有的单一来源
（`deploy/systemd/`、新增 `deploy/systemd-user/`、`deploy/macos/`、`deploy/windows/`）。

顺带修复一个被 agent-only 测试掩盖的阻塞缺陷：`init-node-id` 的持久化路径此前硬编码为
`/var/lib/tunnelmesh/node-id`，非 root 不可写，导致 **server 在 Linux 默认 user 模式下必然以
退出码 6 安装失败**。新增 `--node-id-path` 标志后，一键脚本与 systemd user 单元都把它指向
用户可写的状态目录。

## User impact

- 新增面向用户的安装入口：Linux/macOS `install-<role>.sh`，Windows `install-<role>.ps1`。
  根 `README.md` 的 Quick start → Install 改为一键安装优先，`scripts/install.sh`
  降级为「只要二进制、不注册服务」的补充路径（行为未变）。
- 支持五种调用形态：先落盘再执行（推荐）、`bash -c "$(curl ...)"`、`curl | bash`、
  `curl | bash -s -- <args>`、Windows `& ([scriptblock]::Create((irm <url>))) <args>`。
  两种一行式的已知失效模式（curl 失败静默返回 0、`bash -c` 吃掉第一个参数当 `$0`）写进了
  `--help` 与文档。
- Linux 默认**用户级**安装，不需要 root；`--mode system` 才走系统级单元并要求 root。
- 重复运行即升级且**默认保留现有配置**；`--reconfigure` 才重新生成，覆盖前自动备份。
- `tunnelmesh-server` 新增 `--node-id-path` 持久化标志（默认值不变，向后兼容）。

## API, schema, and configuration impact

- **HTTP API：无变更**。不涉及 `docs/api/openapi.yaml`。
- **数据库 Schema：无变更**。不涉及 `migrations/`、`SchemaVersion` 与增量迁移链。
- **协议 frame：无变更**。
- **CLI**：新增 `tunnelmesh-server --node-id-path <file>`（agent/client 也接受该持久化标志但用不到，
  因为它是 root 命令的持久化 flag）。默认值仍是 `config.DefaultNodeIDPath`，既有部署行为不变。
- **部署产物**：
  - 新增 `deploy/systemd-user/tunnelmesh-{server,agent,client}.service`，占位符
    `__BINARY__`、`__CONFIG__`、`__ENV_FILE__`、`__STATE_DIR__`；server 单元的 `init-node-id`
    与 `run` 都带 `--node-id-path __STATE_DIR__/node-id`。
  - `deploy/macos/tunnelmesh.plist` 新增 `__ENVIRONMENT__` 占位符（渲染为 `EnvironmentVariables`
    dict，无敏感值时渲染为空串）。
  - `deploy/windows/tunnelmesh-service.xml` 新增 `__ENV_BLOCK__` 占位符（渲染为 `<env>` 元素，
    无敏感值时为空串）。
  - `deploy/systemd/tunnelmesh-agent.service` 新增可选 `EnvironmentFile=-/etc/tunnelmesh/agent.env`
    （前缀 `-`：缺文件不阻塞启动，向后兼容）。`agent.token` 的 YAML 键是 `yaml:"-"`，只能走环境变量。
  - `deploy/install/macos-install.sh` 与 `windows-install.ps1` 把新占位符渲染为空，行为不变。
- **发布归档**：Linux 归档新增 `deploy/systemd-user/`；所有平台归档新增
  `deploy/install/oneclick/`（入口 + 共享库 + `winsw-checksums.txt`）。`*_test.go` 与 `testdata/`
  由 `scripts/build-release.sh` 在打包前删除，不进归档。
- **配置渲染**：生成的 YAML 只含非敏感键；敏感值落 `~/.config/tunnelmesh/<role>.env`（0600）、
  `/etc/tunnelmesh/<role>.env`（0640 `root:<运行组>`）、渲染后的 LaunchAgent plist（0600）或
  WinSW 服务 XML 的 `<env>` 块（ACL 收紧）。

## Security impact

- **不提供明文 token 参数**：`.sh` 无 `--token`，`.ps1` 无 `-Token`（`oneclick_scripts_test.go`
  用 `\[string\]\$Token\b` 正则守护）。敏感值只走隐藏输入、`TUNNELMESH_*` 环境变量、
  `--token-file`、`--secret-env-file`（后两者校验文件权限为 0600/0400/0640）。
- 摘要与日志中的敏感值一律掩码（前 4 位 + `****`）；`TestNoHardcodedSecrets` 现在同时扫描
  `.sh` 与 `.ps1`，脚本内无任何真实凭据。
- 归档必须通过同 Release 目录的 `SHA256SUMS` 校验后才解压；缓存命中后仍重新校验，
  不匹配即删除缓存条目并重下。校验文件名与「sha256 + 两个空格 + 归档名」格式与
  `scripts/install.sh` 共用同一份契约（`TestReleaseContractMatchesLegacyInstaller` 守护）。
- WinSW **只下载 `winsw-checksums.txt` 中已登记校验和的版本**；无登记条目时中止（退出码 4）
  并给出手工步骤，不做「先下载再信任」。
- 从 raw 下载的共享库必须满足「HTTP 成功、非空、首行是 `#!/usr/bin/env bash`」，否则退出码 4。
- 不放宽任何既有安全策略：非 loopback 监听仍要求显式同意（`TM_ONECLICK_ALLOW_REMOTE=1`）
  且 `auth_mode` 非 `none`；agent metadata 名称在交互期即校验敏感词；server 默认监听
  `127.0.0.1`，TLS 交给 Nginx。
- `--node-id-path` 修复同时收窄了权限面：user 模式不再需要写 `/var/lib/tunnelmesh`，
  systemd user 单元的 `ProtectSystem=full` + `ReadWritePaths=<state>` 得以成立。
- 卸载**保留**配置、env、密钥与数据目录，并逐条打印保留路径（`TestUninstallKeepsConfiguration` 守护）。

## Tests run

全部在本机（macOS，bash 3.2.57）执行，退出码均为 0：

```bash
go test ./... -count=1                       # 全量，无失败
go test -race ./...                          # 全量，无失败
go test -race ./deploy/... ./internal/cli/... -count=1   # 本次改动范围，含新增用例
go vet ./...
git diff --check
bash -n deploy/install/oneclick/*.sh deploy/install/*.sh scripts/*.sh
sed 's/^__ENVIRONMENT__$//' deploy/macos/tunnelmesh.plist | plutil -lint -
TM_ONECLICK_E2E=1 go test ./deploy/install/oneclick -count=1 -run TestOneClick -v
python3 scripts/gen_doc_index.py             # 幂等，无残留 diff
```

E2E 结果：

```
--- PASS: TestOneClickInstallUpgradeUninstall (2.18s)   # agent：安装 → 升级 → 卸载
--- PASS: TestOneClickServerUserModeInstall (2.06s)     # server：非 root user 模式（回归守护）
```

TDD 红/绿证据（`--node-id-path` 修复）：三条新用例先全部 RED，实现后全部 GREEN。

```
root_internal_test.go:102: init-node-id --node-id-path: unknown flag: --node-id-path
install_templates_test.go:55: ../systemd-user/tunnelmesh-server.service must point init-node-id at the user-writable state dir
testdata/run_tests.sh: 1 failure(s)   # validate/node-id-path
```

手工验证（本地 HTTP 镜像模拟 raw 源 + `go build` 出的真实二进制 + `--archive`）：

| 场景 | 命令形态 | 结果 |
| --- | --- | --- |
| 入口单独落盘、共享库走 raw 回退下载 | `bash install-agent.sh --help` | 退出码 0，打印用法 |
| Homebrew 风格命令替换 | `/bin/bash -c "$(curl -fsSL <url>)" install-agent --help` | 退出码 0 |
| Homebrew 风格 + 离线归档 + `--yes` | `/bin/bash -c "$(curl -fsSL <url>)" install-agent --archive <tar.gz> --yes ...` | 退出码 0；`SHA256SUMS` 校验通过；二进制 0755、YAML/env 0600；token 掩码为 `e2e-****`，stdout/stderr 均无明文 |
| 管道 + 传参 | `curl -fsSL <url> \| bash -s -- --help` | 退出码 0 |
| 管道 + 离线归档 + `--yes` | `curl -fsSL <url> \| bash -s -- --archive <tar.gz> --yes ...` | 退出码 0，摘要正常，无 token 泄露 |
| 非法版本格式 | `... --version 1.2.3` | 退出码 2 |
| 篡改 `SHA256SUMS` | `... --archive <tar.gz> --yes` | 退出码 5，打印 expected/actual 两个哈希 |
| 无 tty 且未 `--yes`、缺 Server URL | `TM_ONECLICK_YES=1 ...` | 退出码 3，点名缺失项 |
| `bash -c "$(curl 失败)"` | 404 URL | 退出码 0 且无任何输出 → 文档已写明该失效模式 |
| server cluster + MySQL（stdin 喂答案） | `TM_ONECLICK_ALLOW_STDIN=1 ... <<EOF` | 退出码 0；`mode: cluster`、`driver: 'mysql'`；DSN 只在 `server.env`（0600），不在 YAML；`node-id` 落在状态目录 |
| server local + SQLite（`--yes`） | `TM_ONECLICK_YES=1 ... --no-service` | 退出码 0；`mode: local`、`driver: 'sqlite'`、`http_addr: '127.0.0.1:8080'` |
| 卸载 | `--uninstall` | 退出码 0；二进制删除、配置与 env 保留并逐条打印 |

**未验证项（环境限制，非本次改动引入）**：

1. **PowerShell 语法与真实 Windows 安装未验证**：本机无 `pwsh`/`powershell`，`.ps1` 只有静态契约断言
   （`TestPowerShellContract`、`TestPowerShellEntryBootstrapIsIdentical`、`TestPowerShellEntriesStayThin`、
   `TestPowerShellEntryHelp`）。需在 Windows 上实际执行一次三角色安装与 `-Uninstall`，
   并确认渲染出的 `*-service.xml` 中 `arguments`、`workingdirectory`、`logpath`、`<env>` 与预期一致。
2. **`winsw-checksums.txt` 无已登记条目**：本环境 `api.github.com` 与 `raw.githubusercontent.com`
   可达，但 `github.com` Release 下载超时，无法取得 WinSW 官方资产算校验和。补录前 Windows
   自动获取 WinSW 的分支会主动中止并提示 `-WinSW <本地路径>`；补录步骤写在
   `winsw-checksums.txt` 头部与 `docs/deployment/windows-service.md`。
3. **真实 GitHub Release 下载链路未验证**：同上原因。下载/校验/缓存逻辑由 `testdata/bin/curl`
   桩 + fixture 归档在函数级与 E2E 覆盖，但「真的从 github.com 拉一个 tag」需要在有外网的机器上跑一次
   `bash deploy/install/oneclick/install-agent.sh --version <tag> --no-service --yes --server-url ... --agent-id ...`。

文档一致性核对（Task 12 Step 2）：

```bash
grep -o -- '--[a-z-]\+' docs/deployment/oneclick-install.md | sort -u > /tmp/doc-flags.txt
grep -o -- '--[a-z-]\+' deploy/install/oneclick/tunnelmesh-install-common.sh | sort -u > /tmp/impl-flags.txt
comm -23 /tmp/doc-flags.txt /tmp/impl-flags.txt
```

唯一输出是 `--token`，且它出现在「**刻意不提供** `--token <value>`」这句否定说明里，不是可用参数。
反向 `comm -13` 的输出全部是非安装器 flag：`curl` 的
`--fail/--silent/--show-error/--location/--max-time/--output/--head/--header`、
`launchctl` 的 `--home/--home-dir/--shell`、`systemctl` 的 `--system/--no-pager/--lines`、
二进制的 `--config/--node-id-path`、`admin regenerate-credentials --confirm`，
以及测试专用的 `--print`，均为刻意不在安装文档中罗列。

## Release steps

1. 合并到 `main`。本次不含数据库迁移，无需停机、无需备份窗口。
2. 正常发布流程打 tag：`cd web && npm ci && npm run build` → `scripts/build-release.sh`。
   归档会自动带上 `deploy/install/oneclick/` 与（Linux）`deploy/systemd-user/`。
3. 发布后在**有外网**的机器上补录 WinSW 校验和到
   `deploy/install/oneclick/winsw-checksums.txt`（`v2.12.0 amd64 <sha256> WinSW-x64.exe`），
   跑 `go test ./deploy/install/oneclick -run TestWinSWChecksumPolicy -count=1`，单独提交。
4. 发布后在 Windows 上跑一次 `install-agent.ps1` 与 `-Uninstall`，在 Linux 上跑一次
   真实 `install-server.sh --mode system`，把结果补记到本文件。
5. `raw.githubusercontent.com/.../main/deploy/install/oneclick/*` 在合并后立即可用；
   需要锁版本的用户改用 `TM_ONECLICK_REF=<tag>` 并把 URL 里的 `main` 换成同一 tag。

## Rollback steps

- 本次**不含数据库迁移**，回滚只需 `git revert` 对应提交并重新发布，无需数据处理。
- 模板占位符是向后兼容的，但 `deploy/macos/tunnelmesh.plist` 与
  `deploy/windows/tunnelmesh-service.xml` 必须与各自的渲染脚本**同时**回滚：
  旧版 `macos-install.sh` / `windows-install.ps1` 在新模板上运行会把
  `__ENVIRONMENT__` / `__ENV_BLOCK__` 原样留在渲染结果里。两者是一个原子变更。
- `internal/cli/root.go` 的 `--node-id-path` 与 `deploy/systemd-user/tunnelmesh-server.service`
  也必须同时回滚：user 单元依赖该标志，去掉标志后 user 模式的 server 会退回
  `/var/lib/tunnelmesh` 权限失败。system 单元与既有部署不受影响，可独立保留。
- `deploy/systemd/tunnelmesh-agent.service` 的 `EnvironmentFile=-` 前缀保证缺文件不阻塞，可独立回滚。
- 已用一键脚本装好的机器不受仓库回滚影响；下线执行 `install-<role>.sh --uninstall`
  （保留配置与数据）。
- 发布归档布局变化只影响**下一个** tag；已发布的 Release 不可变，无需处理。
- 5 分钟止损方案：撤回 tag 指向（或把 `latest` 指回上一个 Release），
  并在文档里临时改回 `scripts/install.sh` 的三行安装命令。

## Reviewer focus

1. `tm_run_validate` 与 `deploy/systemd-user/tunnelmesh-server.service` 的 `--node-id-path`
   取值是否都等于 `tm_default_state_dir`/`__STATE_DIR__`；system 单元刻意**不改**，
   确认这个非对称是有意的（system 模式下 `/var/lib/tunnelmesh` 由 root `+` 前缀写入）。
2. 三个 `.sh` 与三个 `.ps1` 入口的 bootstrap 片段是否逐字节一致（两个测试分别守护）；
   `.ps1` 的 raw 回退额外下载 `winsw-checksums.txt`，否则共享库落到临时目录后
   `Get-TmWinSW` 找不到登记表。
3. `Install-TmService` 现在优先用**解压出的归档**里的 `deploy\windows\tunnelmesh-service.xml`，
   只在仓库内直接跑脚本时才回退到共享模块旁的相对路径 —— 确认「模板与二进制同版本」这条成立。
4. bash 3.2 兼容性：`grep -n 'declare -A\|mapfile\|readarray\||&\|&>>\|declare -n'` 无输出；
   空数组展开一律 `${arr[@]+"${arr[@]}"}`。
5. 敏感值路径：`tm_secret_value` 的优先级（secret-env-file > token-file > env > 交互）、
   `--yes` 下缺值即退出码 3、env 文件用双引号转义（systemd 单引号串没有转义机制）。
6. 文档 flag 面与实现是否一致（上面的 `comm` 核对）；`docs/deployment/oneclick-install.md`
   的默认路径表与 `tm_default_*` 的返回值逐条对照。
7. server 角色目前**没有** `--http-addr` / `--storage-driver` 之类的非敏感 flag，
   无人值守集群部署只能靠 stdin 喂答案或装完改 YAML。设计规格 §11 曾假设这些 flag 存在，
   实现按 YAGNI 收窄，文档已如实描述；如果后续要补，应作为独立变更并同步更新规格。

## Integration status

- 分支 `codex/one-click-install`，基线 `main`（`6d3ddec`）。
- 计划：[docs/superpowers/plans/2026-09-23-one-click-install-scripts.md](../superpowers/plans/2026-09-23-one-click-install-scripts.md)
- 规格：[docs/superpowers/specs/2026-09-23-one-click-install-scripts-design.md](../superpowers/specs/2026-09-23-one-click-install-scripts-design.md)
- 用户文档：[docs/deployment/oneclick-install.md](../deployment/oneclick-install.md)
- Task 0–12 全部完成；上述三项「未验证项」因实现环境无 `pwsh`、无法访问 `github.com`
  Release 下载而遗留，需在具备条件的机器上补做并回填本文件。
