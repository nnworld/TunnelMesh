# 三端一键安装脚本设计

## 1. 背景与目标

当前安装路径分散在三处，且都需要用户先读懂文档再手工拼装：

- `scripts/install.sh`：下载 Release、校验 `SHA256SUMS`、安装三个二进制。不注册服务、不生成配置，且只支持 Linux/macOS。
- `deploy/install/linux-install.sh`、`macos-install.sh`、`windows-install.ps1`：注册服务，但要求调用方**事先准备好配置文件**，全部参数靠位置参数或 flag 传入。
- `docs/deployment/*.md`：手工步骤。

目标是提供**每个角色一条命令**的一键安装入口，交互式完成「选版本 → 下载校验 → 选安装模式与用户 → 生成配置 → 注册服务 → 启动 → 验证 → 输出后续步骤」，同时保留完全非交互的调用方式供 CI 与批量部署使用。

成功标准：一个从未读过本仓库文档的用户，在 Linux/macOS/Windows 上任一平台，用一条命令完成 agent 或 client 的安装并能连上 Server；server 安装完成后能拿到首个管理员凭据并打开后台。

## 2. 用户已确认的决策

| 编号 | 决策 | 选择 |
| --- | --- | --- |
| 1 | Windows 覆盖方式 | A：3 个 `.sh`（Linux+macOS）+ 3 个配套 `.ps1`（Windows，含 WinSW 获取与校验） |
| 2 | Linux 服务托管模式 | A（由 C 修订）：**默认 user 模式**——装到当前用户的 systemd user unit；`--mode system` 切系统模式（root + 专用运行账户 + `/etc/tunnelmesh`） |
| 3 | 「选择其它用户」语义 | C：两者都支持——user 模式可指定另一个普通用户；系统模式可指定服务运行账户 |
| 4 | 附加能力 | 全选：敏感值写独立 env 文件、`--yes` 非交互、`--base-url` 镜像源、重复运行=升级且提供 `--uninstall` |

默认安装到当前用户，与最初的需求描述一致。系统模式完整保留，通过 `--mode system` 选择；两种模式的路径差异见第 6 节，模式判定规则见第 10 节。

## 3. 范围

**做**：

- 三角色 × 三平台的一键安装、升级、卸载。
- 交互式配置生成（YAML）与敏感值注入（平台原生环境机制）。
- 服务注册：Linux systemd（user + system 两种）、macOS launchd LaunchAgent、Windows WinSW Service。
- 非交互模式、镜像源、离线归档安装、SHA256 校验、升级回滚。
- 配套文档与契约测试。

**不做**（YAGNI）：

- 不做 Docker/Compose 一键安装（已有 `docs/deployment/docker.md` 与两份 compose 文件）。
- 不做 macOS LaunchDaemon（root 级）模板；macOS 只有用户级 LaunchAgent，与现有 `deploy/macos/tunnelmesh.plist` 及文档一致。
- 不做 Nginx/OpenResty/MySQL/etcd 的安装或初始化；server 集群模式只生成配置并校验连通性，依赖由运维准备。
- 不做 `--purge`（删除配置与数据）；卸载一律保留配置和数据并打印路径，与现有脚本约定一致。
- 不做图形界面、不做多语言交互文案（中文交互 + 英文 `--help`，与仓库现状一致）。
- 不把一键脚本作为 Release 资产单独上传；脚本通过 `raw.githubusercontent.com` 获取，且已随归档内的 `deploy/install/` 一起分发。

## 4. 交付物与目录布局

```text
deploy/install/oneclick/
├── tunnelmesh-install-common.sh     # Linux/macOS 共享库：平台探测、下载校验、交互、渲染、服务调度
├── install-server.sh                # 入口（薄）：定义角色差异 + 调用 tm_main
├── install-agent.sh
├── install-client.sh
├── tunnelmesh-install-common.ps1    # Windows 共享模块（dot-source）
├── install-server.ps1
├── install-agent.ps1
├── install-client.ps1
├── winsw-checksums.txt              # WinSW 版本/架构 → SHA256 pin 与更新流程
├── oneclick_scripts_test.go         # 契约测试（Go），不随归档发布
└── testdata/                        # 测试 fixture 与 bash 函数级测试套件，不随归档发布
    └── run_tests.sh                 # 由 oneclick_scripts_test.go 调用

deploy/systemd-user/
├── tunnelmesh-server.service        # 新增：user 模式模板，占位符渲染
├── tunnelmesh-agent.service
└── tunnelmesh-client.service
```

设计约束：

- **入口脚本薄、共享库厚**。下载、校验、平台探测、交互、渲染、服务注册全部在共享库；三个入口只声明角色差异（提示项、渲染函数、安装后验证）。避免三份 300 行脚本各自漂移（AGENTS.md DRY）。
- **模板方法模式**：`tm_main` 固定 8 个阶段，角色通过 `tm_role_*` 钩子填充可变步骤。
- `testdata/` 与 `*_test.go` 不进发布归档：`scripts/build-release.sh` 已删除 `*_test.go`，需追加删除 `testdata/` 目录。`winsw-checksums.txt` 与两份共享库**必须**进归档，因为归档内的一键脚本要能离线自洽运行。
- **与 `scripts/install.sh` 的关系**：后者保留为「只装二进制、不注册服务」的轻量入口（README Quick start 现有引用，且被 `scripts/install_script_test.go` 守护），一键脚本不 source 它——它不在发布归档内，也没有交互与服务注册能力。两者共享同一份 Release 契约（归档命名 `tunnelmesh-<ver>-<goos>-<goarch>.<ext>` 与 `SHA256SUMS` 的两空格格式），该契约由 `oneclick_scripts_test.go` 与 `install_script_test.go` 双向断言，避免两处下载器悄悄分叉。这属于两处实现、契约测试兜底，未达 DRY 的三处抽象阈值。
- **PowerShell 参数命名**：`.ps1` 使用 PowerShell 惯例的 `-Version`、`-Yes`、`-Uninstall`、`-Mode`、`-User`、`-RunUser`、`-NoService`、`-NoStart`、`-NoEnable`、`-BaseUrl`、`-RawBaseUrl`、`-Archive`、`-TokenFile`、`-SecretEnvFile`、`-WinSW`、`-InstallDir`、`-ConfigDir`、`-StateDir`、`-ServerUrl`、`-AgentId`、`-Tunnel`，语义与 `.sh` 的同名长参数一一对应。

### 4.1 对既有产物的修改

| 文件 | 修改 | 原因 |
| --- | --- | --- |
| `deploy/systemd/tunnelmesh-agent.service` | 增加 `EnvironmentFile=-/etc/tunnelmesh/agent.env` | agent 单元当前没有 env 注入点，而 `agent.token` 的 YAML 键是 `yaml:"-"`，只能走环境变量；server 已有同款可选 env 文件 |
| `deploy/macos/tunnelmesh.plist` | 增加 `__ENVIRONMENT__` 占位符（渲染为 `EnvironmentVariables` dict，无敏感值时渲染为空串） | launchd 没有 `EnvironmentFile`，模板本身仍不含任何秘密 |
| `deploy/windows/tunnelmesh-service.xml` | 增加 `__ENV_BLOCK__` 占位符（渲染为若干 `<env name= value=>`，无敏感值时为空） | WinSW 只能通过 XML 注入环境变量 |
| `deploy/install/macos-install.sh`、`windows-install.ps1` | 渲染新占位符为空 | 保持既有行为逐字节不变 |
| `deploy/install/install_templates_test.go` | 覆盖新占位符与 `deploy/systemd-user/` 三份模板 | 守护「每角色只有一份模板来源」 |
| `scripts/build-release.sh` | 追加删除 `stage/deploy/**/testdata` | 测试 fixture 不发布 |

系统模式下若用户选择了非默认运行账户或非默认路径，一键脚本**不修改** `deploy/systemd/*.service`，而是安装原单元 + 生成 drop-in：`/etc/systemd/system/tunnelmesh-<role>.service.d/oneclick.conf`，用 `ExecStart=`/`ExecStartPre=` 清空后重写的标准做法覆盖 `User=`、`Group=`、路径与 `EnvironmentFile`。默认值安装时不生成 drop-in，保证与今天的行为完全一致。

## 5. 获取与分发

```bash
# Linux / macOS（推荐先下载再审阅，再执行）
curl -fsSL https://raw.githubusercontent.com/nnworld/TunnelMesh/main/deploy/install/oneclick/install-agent.sh -o install-agent.sh
bash install-agent.sh

# 一行式
curl -fsSL https://raw.githubusercontent.com/nnworld/TunnelMesh/main/deploy/install/oneclick/install-agent.sh | bash -s -- --version v1.1.1
```

Windows：

```powershell
irm https://raw.githubusercontent.com/nnworld/TunnelMesh/main/deploy/install/oneclick/install-agent.ps1 -OutFile install-agent.ps1
.\install-agent.ps1
```

### 5.1 支持的调用形态

一键脚本必须在下列五种调用方式下都能工作，差异集中在「`$0`/`BASH_SOURCE` 是否指向真实文件」与「stdin 是否为终端」两点：

| 形态 | 命令 | `$0` | stdin | 说明 |
| --- | --- | --- | --- | --- |
| 下载后执行（推荐） | `curl -fsSL <url> -o install-agent.sh && bash install-agent.sh` | 真实路径 | 终端 | 可先审阅；共享库从同目录加载 |
| 管道执行 | `curl -fsSL <url> \| bash` | `bash` | **管道** | 交互提示必须改读 `/dev/tty` |
| 管道 + 传参 | `curl -fsSL <url> \| bash -s -- --version v1.1.1` | 同上 | 管道 | 参数写在 `--` 之后 |
| 命令替换执行（Homebrew 风格） | `/bin/bash -c "$(curl -fsSL <url>)"` | `/bin/bash` | 终端 | 交互式安装的首选一行命令；`/bin/bash` 在 macOS 上是 3.2.57，见下方兼容性要求 |
| 命令替换 + 传参 | `/bin/bash -c "$(curl -fsSL <url>)" install-agent --yes --server-url wss://...` | `install-agent` | 终端 | **第一个参数被 bash 当作 `$0` 吃掉**，必须写一个占位名；也可省略参数改用 `TM_ONECLICK_YES=1` 等环境变量 |

由此固定三条实现要求：

1. **不得依赖 `$0` 或 `BASH_SOURCE` 定位共享库**。解析顺序见下，三级回退全部失败则退出码 4。
2. **不得用 `$0` 做自我重新执行**（跨用户安装、sudo 降权场景需要 re-exec）。入口脚本启动时把自身真实路径写进 `TM_ONECLICK_ENTRY`；`bash -c`/管道下取不到真实路径时，先把入口副本写入临时目录再以该副本 re-exec。
3. **交互提示统一从 `/dev/tty` 读取**（见第 8 节），使 `curl \| bash` 与 `bash -c` 都能正常问答。
4. **必须兼容 bash 3.2**（macOS `/bin/bash` 至今是 3.2.57，而用户会照抄 Homebrew 那行 `/bin/bash -c`）。禁用：关联数组 `declare -A`、`${var,,}`/`${var^^}`、`mapfile`/`readarray`、`&>>`、`|&`、nameref `declare -n`。答案存储用 `tm_ans_set`/`tm_ans_get`（`printf -v` + 变量名映射），集合用普通索引数组。
5. **`--yes` 也可由环境变量触发**：`TM_ONECLICK_YES=1`。命令替换形态下传参要先写一个占位 `$0`，环境变量更省事，CI 里也更稳。

`bash -c "$(curl ...)"` 有一个必须写进文档的失效模式：curl 失败时命令替换结果是空串，bash 什么都不执行却返回 0，用户会以为装好了。因此文档把 `curl -fsSL <url> -o /tmp/tm-install.sh && /bin/bash /tmp/tm-install.sh` 作为**推荐**写法（失败可见、可先审阅），一行式写法紧随其后并标注这个风险；`curl \| bash` 同样需要先落盘或显式检查 curl 退出码。

PowerShell 对应形态：`.\install-agent.ps1`（推荐，先 `irm -OutFile` 落盘再审阅）、`& ([scriptblock]::Create((irm <url>))) -Yes ...`（等价于 `bash -c`，参数直接跟在后面，不需要占位名）；不推荐 `iex (irm <url>)`——无法传参，且会把脚本展开到当前作用域。

共享库解析顺序（`bash -c` 与管道执行时 `$0` 不是真实路径，必须有回退）：

1. `TM_ONECLICK_LIB` 环境变量指定的路径；
2. 入口脚本同目录下的 `tunnelmesh-install-common.sh`（本地 clone 或解压归档后）；
3. 从 `raw` 基址下载：`<raw-base>/<ref>/deploy/install/oneclick/tunnelmesh-install-common.sh`，`ref` 取 `TM_ONECLICK_REF`（未设置则 `main`）。

`ref` **刻意不跟随 `--version`**：服务模板（`deploy/systemd/`、`deploy/systemd-user/`、`deploy/macos/`、`deploy/windows/`）一律从**已校验的发布归档**里取，天然与所装版本一致；只有共享库走 raw。若让共享库也跟 tag，反而会出现「`main` 的入口脚本 + `v1.1.1` 的库」这种错配。需要整体锁版本时用 `TM_ONECLICK_REF=v1.1.1`，并把入口 URL 里的 `main` 换成同一 tag。

下载得到的共享库必须满足：HTTP 成功、非空、首行是 `#!/usr/bin/env bash`，否则中止（退出码 4）。

版本解析顺序：

1. `--version vX.Y.Z` 显式指定（格式校验 `^v[0-9]+\.[0-9]+\.[0-9]+$`）；
2. GitHub API `releases/latest` 的 `tag_name`，可选 `--github-token` / `TUNNELMESH_GITHUB_TOKEN` 提高配额；
3. API 失败时回退到 `GET <base>/latest` 的 `Location` 重定向头解析 tag（不受 API 配额限制）；
4. 全部失败则报错并提示显式传 `--version`。

镜像与离线：

- `--base-url` / `TUNNELMESH_RELEASE_BASE_URL`：替换 `https://github.com/nnworld/TunnelMesh/releases`，用于内网镜像；
- `--raw-base-url` / `TUNNELMESH_RAW_BASE_URL`：替换 `https://raw.githubusercontent.com/nnworld/TunnelMesh`；
- `--archive <path>`：跳过下载，直接用本地归档安装（气隙环境，也是测试的主要入口）。

归档与校验：沿用 `scripts/install.sh` 已验证的逻辑——同时下载归档与 `SHA256SUMS`，按 `grep "  <archive>$"` 取期望值，`sha256sum`（缺失时 `shasum -a 256`）比对，不匹配立即中止（退出码 5）并打印期望/实际值。Windows 用 `Get-FileHash -Algorithm SHA256` 比对同一份 `SHA256SUMS`。

## 6. 平台矩阵与托管方式

| 平台 | 归档 | 默认托管 | 可选托管 | 提权要求 |
| --- | --- | --- | --- | --- |
| Linux amd64/arm64 | `.tar.gz` | systemd **user** 单元（当前用户）：二进制 `~/.local/bin/`，配置 `~/.config/tunnelmesh/`，状态 `~/.local/share/tunnelmesh/`，自动 `loginctl enable-linger` | `--mode system`：systemd **system** 单元，运行账户 `tunnelmesh`（缺失则创建系统用户），配置 `/etc/tunnelmesh/`，状态 `/var/lib/tunnelmesh*` | user 不需 root；system 需 root |
| macOS amd64/arm64 | `.tar.gz` | launchd **LaunchAgent**（当前用户），配置 `~/.config/tunnelmesh/`，状态 `~/.local/share/tunnelmesh/`，日志 `~/Library/Logs/` | `--no-service`：只装二进制与配置 | 不需要 root；以 root 调用且存在 `SUDO_USER` 时自动降权到该用户 |
| Windows amd64/arm64 | `.zip` | WinSW 包装的 Windows 服务，安装目录 `C:\Program Files\TunnelMesh`，配置 `C:\ProgramData\TunnelMesh\` | `-NoService`：只装二进制与配置 | 必须管理员 PowerShell |

无 systemd 的环境（容器、WSL1、Alpine openrc）：检测失败时**不中止**，退化为「安装二进制 + 生成配置」，打印等价的前台启动命令并以退出码 0 结束，同时在摘要里标注「未注册服务」。

## 7. 安装流程（8 个阶段）

`tm_main` 固定顺序，`--yes` 只是把每个 `tm_ask*` 换成默认值/传入值：

1. **preflight**：解析参数 → 探测 OS/arch/归档后缀 → 检查必需命令（`curl tar awk sed grep install mktemp`，systemd 侧另需 `systemctl`）→ 判定 EUID 与 `SUDO_USER` → 检测既有安装（二进制 `--version` 输出、服务是否存在）→ 决定是「全新安装」「升级」还是「卸载」。
2. **resolve & fetch**：解析版本 → 命中本地缓存或下载归档与 `SHA256SUMS` → 校验 → 解压到 `mktemp -d`，`trap ... EXIT INT TERM` 清理。缓存目录 Linux/macOS 为 `${XDG_CACHE_HOME:-$HOME/.cache}/tunnelmesh/releases/`，Windows 为 `$env:LOCALAPPDATA\TunnelMesh\releases\`，按归档名存放；缓存命中后仍必须重新校验 SHA256，校验不通过就删除该缓存条目并重新下载。`--no-cache` 绕过缓存，`--archive` 绕过下载与缓存。缓存的意义是同一台机器先后跑 `install-agent.sh` 与 `install-client.sh` 时只下载一次。
3. **placement**：解析安装模式（规则见第 10 节）、目标用户/运行账户、二进制目录、配置目录、状态目录；system 模式下按需创建系统用户与目录并设置属主。
4. **install binary**：只安装**本角色**的二进制（`install-agent.sh` 只装 `tunnelmesh-agent`，`install-client.sh` 只装 `tunnelmesh-client`）；同机需要两个角色就跑两个脚本，共享缓存不会重复下载。`install -m 0755` 写到同目录临时名再 `mv` 原子替换；已存在的旧二进制备份为 `<bin>.bak-<UTC时间戳>`，只保留最近一份（写入前删除更早的备份）。
5. **configure**：角色钩子 `tm_role_prompts` 收集答案 → `tm_role_render_config` 渲染 YAML 与 env 文件。目标文件已存在时**直接覆盖**，覆盖前自动备份为 `<path>.bak-<UTC时间戳>`，最多保留最近 3 份；`--keep-config` 跳过整个 configure 阶段沿用现有文件（第 6 阶段仍会校验它）。
6. **validate**：`<binary> --config <yaml> check-config`；server 追加 `init-node-id`（在写单元前执行，保证 YAML 里 `node.id` 已回写）。失败即中止（退出码 6），不注册服务。
7. **register & start**：渲染并安装 unit/plist/WinSW XML → `daemon-reload` / `bootstrap` / `install` → 按选择 `enable` 与 `start`。
8. **verify & report**：读取服务状态与最近日志 → server 跑 `doctor`、可选 `admin bootstrap`；agent 提示 `tunnelmesh-agent id` 与后台绑定；client 跑 `status` → 打印结构化摘要（版本、路径、服务名、掩码后的敏感项、下一步命令、卸载命令）。

升级路径只跑 1、2、4、6、7、8：**不重新生成配置**，沿用磁盘上现有的 YAML 与 env 文件——这是「重复运行=升级、保留配置」的既定语义，避免升级冲掉手工调优过的参数；确实要重新走问答并覆盖时显式加 `--reconfigure`，覆盖同样先备份。第 6 或第 7 步失败时自动回滚——恢复 `.bak-<ts>` 二进制、重启服务、以退出码 7 报告失败原因。

## 8. 交互项与默认值

所有交互项都有确定默认值，`--yes` 下全部取默认或取显式传入值；缺必填项（如 agent token）时直接失败并指出缺失的 flag/env，不静默降级。

**提示一律从 `/dev/tty` 读取**：`tm_ask*` 内部统一 `read -r ... <"$TM_TTY"`，`TM_TTY` 在启动时解析为 `/dev/tty`（可打开时），否则为空串并回退 stdin。原因是 `curl | bash` 与 `bash install.sh < file` 下 stdin 不是终端，直接 `read` 会立刻 EOF 并把全部答案取成默认值——那会让管道调用静默装出错误配置。没有可用 tty 且未指定 `--yes` 时以退出码 3 失败，并提示改用 `--yes` + 显式参数，绝不静默走默认值。隐藏输入（token、DSN、密码）用 `read -rs` 且同样走 `TM_TTY`，输入两次校验一致；PowerShell 侧用 `Read-Host -AsSecureString`（`-Yes` 下不读）。

### 8.1 通用

| 提示 | 默认 | flag / env |
| --- | --- | --- |
| 使用最新版本 vX.Y.Z？ | 是 | `--version` |
| 安装模式（仅 Linux） | `user`（判定规则见第 10 节） | `--mode user\|system` |
| 目标用户（user 模式） | 当前用户；root 调用且存在 `SUDO_USER` 时取该用户 | `--user <name>` |
| 运行账户（system 模式） | `tunnelmesh`，不存在时询问是否创建 | `--run-user <name>`、`--create-run-user` |
| 二进制目录 | user `~/.local/bin`；system `/usr/local/bin`；Windows `C:\Program Files\TunnelMesh` | `--bin-dir`、`-InstallDir` |
| 配置目录 / 状态目录 | 见第 6 节 | `--config-dir`、`--state-dir` |
| 已有配置文件 | 覆盖（先备份，保留最近 3 份） | `--keep-config`、`--reconfigure` |
| 注册服务并立即启动 | 是 | `--no-service`、`--no-start` |
| 开机自启 | 是（user 模式含 linger） | `--no-enable`、`--no-linger` |
| 下载缓存 | 启用 | `--no-cache` |
| 镜像源 | GitHub | `--base-url`、`--raw-base-url` |

### 8.2 server

| 提示 | 默认 | 落地位置 |
| --- | --- | --- |
| 运行模式 | `local` | `mode` |
| HTTP 监听 | `127.0.0.1:8080` | `server.http_addr` |
| 动态域名后缀 | `apps.example.com` | `server.dynamic_suffix` |
| 存储驱动 | local→`sqlite`，cluster→`mysql` | `storage.driver` |
| SQLite 路径 | `<state>/tunnelmesh.db` | `storage.sqlite.path` |
| MySQL DSN | 无（隐藏输入，二次确认） | `TUNNELMESH_STORAGE_MYSQL_DSN` |
| MySQL TLS | `false` | `storage.mysql.tls` |
| 自动建表/迁移 | `true` | `storage.auto_init` |
| 注册发现 | `database`，cluster 可选 `etcd`（追问 endpoints） | `registry.type`、`registry.endpoints` |
| 启用 relay | `false`；启用后追问 listen `0.0.0.0:9443`、endpoint、mTLS 四件套路径 | `server.relay.*`，`node_token` → `TUNNELMESH_SERVER_RELAY_NODE_TOKEN` |
| 启用 WebSSH | `true` | `server.webssh.enabled` |
| 启用 tp-* 代理入口 | `false`；启用后追问 listen 与 domain_suffix | `server.proxy_entry.*` |
| 原生 TLS | `false`（提示由 Nginx 终止） | `tls.*` |
| Host / Origin 白名单 | 空（逗号分隔输入） | `security.allowed_hosts`、`security.allowed_origins` |
| 自动生成身份主密钥 | 是 | `TUNNELMESH_TOKEN_ENCRYPTION_KEY`（+ 集群时 `TUNNELMESH_TOKEN_ENCRYPTION_KEY_ID`、`TUNNELMESH_TRACE_SIGNING_KEY`） |
| 立即创建首个管理员 | 是 | 安装后执行 `admin bootstrap`，凭据只打印到终端 |

密钥自动生成优先用 `openssl rand -base64 32`，无 openssl 时读 `/dev/urandom` 并 base64；打印时只显示掩码。集群模式下额外提示：所有 Server 节点必须使用完全一致的主密钥与 trace 签名密钥，否则跨节点 SSO/traceroute 会间歇失败。

### 8.3 agent

| 提示 | 默认 | 落地位置 |
| --- | --- | --- |
| Server URL | 无，必填，校验 `ws://`/`wss://` 且以 `/ws/agent` 结尾 | `agent.server_url` |
| Agent ID | `agent-<hostname 规范化>` | `agent.id` |
| Agent token | 无，必填，隐藏输入 + 二次确认 | `TUNNELMESH_AGENT_TOKEN` |
| 连接池 min/max | `1`/`1` | `agent.connections.*` |
| instance_id | 留空自动生成并持久化 | `agent.instance_id`（留空则不写该键） |
| 上报 metadata | 否；选是则逐项添加（name + source `file\|env` + path/key，最多 32 项） | `agent.metadata[]` |

metadata 名称在交互期即校验字符集（字母数字与 `.` `_` `-`）与敏感词（password/token/secret/private key/api key/credential/authorization/cookie/DSN），命中则拒绝该项并说明原因，避免装完才在启动日志里发现被 redact。

### 8.4 client

| 提示 | 默认 | 落地位置 |
| --- | --- | --- |
| Server URL | 无，必填，校验以 `/ws/client` 结尾 | `client.server_url` |
| Client token | 无，必填，隐藏输入 + 二次确认 | `TUNNELMESH_CLIENT_TOKEN` |
| instance_id / 路径 | 留空自动生成；路径按平台默认 | `client.instance_id_path` |
| 添加本地转发 | 否；选是则循环添加 0..n 条 | `client.tunnels[]` |
| └ 协议 | `tcp`，可选 `udp`/`http`/`socks5` | `protocol` |
| └ 本地监听 | `127.0.0.1:<下一个可用端口，从 15432 起递增>` | `listen` |
| └ Agent ID | 上一条的值 | `agent_id` |
| └ 目标 host/port | 无（`socks5` 跳过） | `target_host`、`target_port` |
| └ 允许非 loopback | 否；选是则强制要求 `auth_mode` 非 `none` | `allow_remote`、`auth_mode`、`auth_url` |

监听地址填非 loopback 时立即警告：必须同时配置认证，否则拒绝写入（与 `docs/operations/client-configuration-examples.md` 的安全约定一致）。

## 9. 配置渲染与敏感值注入

YAML 只包含非敏感键；`agent.token` 与 `client.token` 在配置模型里是 `yaml:"-"`，**根本无法**写进 YAML，这从代码层面强制了「秘密走环境变量」的约定。

| 平台 | 非敏感配置 | 敏感值载体 | 权限 |
| --- | --- | --- | --- |
| Linux user | `~/.config/tunnelmesh/<role>.yaml` | `~/.config/tunnelmesh/<role>.env`（user 单元 `EnvironmentFile=`） | 均 `0600` |
| Linux system | `/etc/tunnelmesh/<role>.yaml` | `/etc/tunnelmesh/<role>.env`（systemd `EnvironmentFile`） | YAML `0640 root:<group>`；env `0640 root:<group>`（沿用 client.env 现状） |
| macOS | `~/.config/tunnelmesh/<role>.yaml` | 渲染进 LaunchAgent plist 的 `EnvironmentVariables`（0600，用户目录内） | plist `0600` |
| Windows | `C:\ProgramData\TunnelMesh\<role>.yaml` | 渲染进 WinSW XML 的 `<env>` 元素 | ACL 仅 Administrators + 服务账户，继承 `ProgramData` 保护 |

渲染规则：

- YAML 用行数组累加后一次写盘，标量统一单引号并对内嵌单引号做 `'` → `''` 转义；空字符串键**不输出**（例如未启用 relay 时不写 `relay:` 段），避免与配置模型的默认值打架。
- env 文件为 `KEY='value'` 形式，值内单引号按 POSIX 规则闭合转义；首行写「本文件由一键安装脚本生成，包含敏感值，请勿提交版本库」。
- macOS/Windows 的环境注入块由共享渲染函数产出，模板本身只有占位符，不含任何秘密（延续 `deploy/README.md` 的模板约定）。
- 三处渲染函数的输出都要能被 `config.Load` 成功解析（见第 15 节测试），杜绝键名漂移。

## 10. 服务注册细节

**模式判定规则（Linux）**：`--mode` 显式指定时以其为准；未指定时按下列顺序判定，`--yes` 下同样适用且不提问：

1. EUID ≠ 0 → `user`；
2. EUID = 0 且 `SUDO_USER` 存在且不是 `root` → `user`，目标用户取 `SUDO_USER`，并打印一行说明「检测到 sudo，将以 <user> 身份做用户级安装；需要系统级请加 --mode system」；
3. EUID = 0 且没有 `SUDO_USER`（真实 root 登录、CI）→ `system`；
4. `--mode system` 但 EUID ≠ 0 → 退出码 3，提示改用 `sudo`。

**Linux user（默认）**：渲染 `deploy/systemd-user/tunnelmesh-<role>.service`（占位符 `__BINARY__`、`__CONFIG__`、`__ENV_FILE__`、`__STATE_DIR__`）到 `~/.config/systemd/user/` → `systemctl --user daemon-reload` → `enable --now` → `loginctl enable-linger <user>`（否则注销后服务被杀，交互中说明并提供 `--no-linger`）。硬化项取用户单元可用子集：`NoNewPrivileges`、`PrivateTmp`、`ProtectSystem=full`、`ReadWritePaths=__STATE_DIR__`；不使用 `ProtectHome=true`（会隐藏自身配置目录）。

**Linux system**：安装 `deploy/systemd/tunnelmesh-<role>.service` 原文件 → 非默认项写 drop-in `oneclick.conf` → `systemctl daemon-reload` → `enable`/`restart`。server 的 `init-node-id` 由单元内 `ExecStartPre=+` 以 root 执行，一键脚本在注册前也会先跑一次，确保 relay 证书 SAN 能与最终 `node.id` 对齐。

**跨用户安装（决策 3）**：user 模式下 `--user <name>` 且当前是 root 时，脚本用 `runuser -u <name> --`（无 `runuser` 则 `sudo -u <name> -H`）**重新执行自身**并带上 `TM_ONECLICK_REEXEC=1` 防环；非 root 且目标用户不是当前用户时直接报错，提示改用 `sudo`。这样 `$HOME`、`systemctl --user` 的总线地址、文件属主全部自然正确，不需要逐条命令拼 `XDG_RUNTIME_DIR`。system 模式下的「其它用户」指服务运行账户，由 `--run-user` 指定并通过 drop-in 覆盖 `User=`/`Group=`。

**macOS**：渲染共享 plist 模板 → `plutil -lint` → `launchctl bootout`（忽略失败）→ `bootstrap gui/$(id -u)` → `kickstart -k`。

**Windows**：`-WinSW <path>` 指定本地包装器；未指定时按 `winsw-checksums.txt` 中 pin 的 SHA256 从官方 `winsw/winsw` Release 下载（amd64/arm64 统一使用 `WinSW-x64.exe`，arm64 靠 x64 仿真运行，文档注明），校验失败或无 pin 记录时中止并给出手动下载步骤。随后复用 `deploy/windows/tunnelmesh-service.xml` 渲染（新增 `__ENV_BLOCK__`）→ `stop`/`uninstall`（忽略失败）→ `install` → `start`。

`winsw-checksums.txt` 格式为 `# 注释` + `<version> <arch> <sha256> <asset-name>`，文件头写明「只能在能访问 github.com Release 的机器上，下载后本地计算并追加」的更新流程。当前实现环境（macOS 沙箱）可达 `api.github.com` 但**不可达** `github.com` Release 下载，因此首批 pin 值需在具备网络的机器上补录；补录前 Windows 一键安装会走「未 pin → 中止 + 手动步骤」这条已定义好的分支，不会出现静默信任。

## 11. 非交互模式与凭据传入

`--yes`（PowerShell `-Yes`）关闭全部交互。**刻意不提供 `--token <value>`**：命令行参数会进入 `ps` 输出与 shell 历史。敏感值只接受三种入口：

1. 交互式隐藏输入（`read -rs` + 二次确认；PowerShell `Read-Host -AsSecureString`）；
2. 环境变量：`TUNNELMESH_AGENT_TOKEN`、`TUNNELMESH_CLIENT_TOKEN`、`TUNNELMESH_STORAGE_MYSQL_DSN`、`TUNNELMESH_SERVER_RELAY_NODE_TOKEN`、`TUNNELMESH_TOKEN_ENCRYPTION_KEY`、`TUNNELMESH_TRACE_SIGNING_KEY`；
3. 文件：`--token-file <path>`、`--secret-env-file <path>`（`KEY=VALUE` 行格式，读取后立即从内存丢弃，文件权限校验为 0600/0400，否则拒绝）。

其它非敏感参数都有对应 flag（`--server-url`、`--agent-id`、`--mode`、`--http-addr`、`--dynamic-suffix`、`--storage-driver`、`--tunnel` 可重复传入 `name:protocol:listen:agent:host:port` 等），因此完整无人值守安装可以只靠 flag + env 完成。摘要与日志中的敏感值一律 `tm_mask`（前 4 位 + `****`）。

## 12. 升级、回滚、卸载

- **升级**：重复执行同一脚本即升级。检测到已安装版本（`<binary> --version`，格式 `<version> commit=<sha> built=<time>`）后打印「当前 vX → 目标 vY」并要求确认；`--yes` 下直接执行。停服务 → 原子替换二进制 → `check-config` → 起服务；任一步失败自动恢复 `.bak-<ts>` 并重启，退出码 7。
- **降级**：允许（`--version` 指定旧版），但 schema 已升级时 server 会自行拒绝启动；脚本在第 6 阶段把 `check-config` 的原始错误完整回显，并提示参阅 `docs/operations/schema-upgrades.md`。不做自动降级数据库。
- **卸载**：`--uninstall` 停服务 → `disable` → 删除 unit/plist/drop-in/WinSW 服务 → 删除本脚本安装的二进制及其 `.bak-*` 备份；**保留**配置、env、数据目录与下载缓存，并逐条打印路径。Windows 复用 `windows-uninstall.ps1` 的语义。

## 13. 退出码与输出

| 码 | 含义 |
| --- | --- |
| 0 | 成功（含「无服务管理器，只装二进制」的退化成功） |
| 2 | 参数错误 / 版本格式非法 / 未知协议 |
| 3 | preflight 失败（缺命令、权限不足、目标用户不可用） |
| 4 | 下载失败（含共享库下载失败） |
| 5 | 校验失败（`SHA256SUMS` 缺条目或不匹配、WinSW pin 不匹配） |
| 6 | `check-config` / `init-node-id` / `plutil -lint` 失败 |
| 7 | 服务注册或启动失败（升级场景已完成回滚） |
| 8 | 卸载失败 |

所有输出走 stderr 的只有错误与警告；正常进度、提示与摘要走 stdout，便于 `| tee` 与 CI 日志采集。交互提示统一前缀 `==>`，警告 `WARNING:`，错误 `ERROR:`。脚本不写自身日志文件，服务日志位置在摘要中给出（`journalctl -u ...` / `journalctl --user -u ...` / `~/Library/Logs/tunnelmesh-<role>.log` / `C:\Program Files\TunnelMesh\logs`）。

## 14. 安全边界

- 脚本内**不得**出现任何真实 token、密码、DSN、私钥；契约测试用正则扫描 `token|secret|password|dsn` 附近的字面量赋值，命中即失败。
- 归档必须通过 `SHA256SUMS` 校验后才解压；`SHA256SUMS` 与归档来自同一 Release 目录，走 HTTPS。文档明确写出 `curl | bash` 的信任模型与「先下载再审阅」的推荐做法。
- 生成的配置文件权限按第 9 节收紧；system 模式下 env 文件属组为运行账户所属组，user 模式下 0600。
- 不关闭任何既有安全校验：不生成 `allow_remote: true` 且 `auth_mode: none` 的隧道；不建议用户把 Server 直接暴露公网（默认 `127.0.0.1` 监听 + 提示配 Nginx）。
- 不新增能力边界之外的东西：一键脚本不调用管理 API、不创建 service token（token 由用户从后台复制），不执行任何远程命令。

## 15. 测试策略（TDD）

先写失败测试，再写实现。四层：

1. **契约测试（Go，始终运行）** — `deploy/install/oneclick/oneclick_scripts_test.go`：
   - 6 个入口 + 2 个共享库 + `winsw-checksums.txt` 存在且 `.sh` 可执行；
   - 每个 `.sh` 含 `set -euo pipefail`、`trap`、`mktemp -d`；每个入口引用共享库而不是自带下载逻辑（断言入口内不出现 `SHA256SUMS`、`releases/download` 等字样，强制 DRY）；
   - 三角色 flag 面齐备（`--version`、`--yes`、`--uninstall`、`--base-url`、`--archive`、`--mode`、`--user`、`--run-user`、`--no-service`、`--token-file`）；
   - 敏感值处理断言：存在 `read -rs`、`tm_mask`，不存在 `--token ` 形式的明文参数；
   - 密钥扫描：脚本内无硬编码秘密；
   - 模板一致性：`deploy/systemd-user/` 三份模板含 `--config`、`run` 与四个占位符；`macos/tunnelmesh.plist` 含 `__ENVIRONMENT__`；`windows/tunnelmesh-service.xml` 含 `__ENV_BLOCK__`；
   - `bash -n` 语法检查所有 `.sh`（bash 缺失时 skip 并打印原因）。
2. **函数级测试（bash 套件，由 Go 测试调用，始终运行）** — `testdata/run_tests.sh`：对纯函数做红/绿验证，包含 `tm_verify_checksum`（fixture 造匹配与不匹配两种）、`tm_yaml_quote`、`tm_render_env_file`、`tm_mask`、`tm_parse_tunnel_spec`、`tm_detect_platform` 的 arch 映射、`tm_render_systemd_user_unit`、`tm_render_plist_environment`、`tm_render_winsw_env_block`、`tm_answer_default`（`--yes` 语义）、退出码映射、`tm_resolve_install_mode`（EUID/`SUDO_USER` 四种组合与 `--mode` 显式值）、`tm_cache_lookup`（命中、校验失败回退重下、`--no-cache`）、`tm_backup_and_overwrite`（覆盖前备份、只保留最近 3 份、`--keep-config` 跳过）。渲染类函数全部接受显式参数，不依赖 `uname`，因此在 macOS 上也能覆盖 Linux 渲染路径。
3. **配置漂移测试（Go，始终运行）**：用 `--yes` + 环境变量驱动共享库渲染出四份 YAML（server local/sqlite、server cluster/mysql、agent、client 含 4 种协议隧道），写入 `t.TempDir()`，再用 `config.Load` 解析并断言关键字段值。这是防止渲染键名与 `internal/config` 漂移的权威手段。
4. **E2E 冒烟（env-gated）**：`TM_ONECLICK_E2E=1` 时，`go build` 出真实二进制 → 打成带 `SHA256SUMS` 的归档 fixture → 用 `--archive` 走完整流程 → 服务管理命令（`systemctl`/`launchctl`/`loginctl`）由 `testdata/bin/` 下的 stub 顶替并记录调用序列 → 断言二进制落位、YAML/env 内容与权限、单元/plist 渲染结果、调用序列、摘要内容；再跑一次同命令验证「升级」路径，最后 `--uninstall` 验证清理与「保留配置」。沿用仓库既有 env-gate 约定（对比 `TM_PROXY_E2E_NGINX=1`）。

`.ps1` 无法在 macOS/Linux 执行，只做静态契约断言，并在 PR 描述里记录一次真实 Windows 手工验证结果（与现有 `windows-install.ps1` 的处理方式一致）。

**实现环境的网络限制**：本机可达 `api.github.com`，不可达 `github.com` Release 下载。因此真实下载路径（含 WinSW pin 值补录）需在具备外网的机器上手工验证一次并记入 PR；自动化测试全部走 `--archive` 与 PATH 中的 `curl` stub。

## 16. 文档与索引变更

- 新增 `docs/deployment/oneclick-install.md`：一条命令安装、三平台 × 三角色矩阵、交互项清单、非交互示例（含 CI）、升级/回滚/卸载、镜像源与离线安装、`curl | bash` 信任模型、按退出码的排障表。
- 更新 `docs/README.md`（部署分类新增入口，避免孤儿文档）、`deploy/README.md`（目录表、模板约定新增占位符与 drop-in 规则、归档内容变化、校验命令）。
- 更新 `docs/deployment/binary-release.md`（归档内新增 `deploy/install/oneclick/` 与 `deploy/systemd-user/`，`testdata` 不发布）、`linux-systemd.md`（agent.env、user 模式、drop-in、linger）、`macos-launchd.md`（`EnvironmentVariables` 渲染与 0600）、`windows-service.md`（WinSW 自动获取与校验、`<env>` 块、一键脚本用法）。
- 更新根 `README.md` 的 Quick start → Install：把一键安装作为**首选**路径（三角色三条命令 + Windows PowerShell 三条命令），`scripts/install.sh` 降级为「只要二进制」的补充说明，并保留「先下载再审阅」的措辞。
- 新增 spec/plan/PR 记录后执行 `python3 scripts/gen_doc_index.py` 重建四份索引。
- 全量验证：`go test ./... -count=1`、`go test -race ./...`、`go vet ./...`、`git diff --check`；本次不涉及前端，无需 `npm` 验证。

## 17. 已知限制

- Windows arm64 使用 x64 版 WinSW（上游未发布 arm64 资产），依赖 x64 仿真。
- macOS 不提供 root 级 LaunchDaemon 托管；需要时按 `docs/deployment/macos-launchd.md` 手工处理。
- Windows 没有「当前用户」托管形态：Windows 服务本质是机器级，一键脚本在 Windows 上始终以管理员注册服务（或 `-NoService` 只装二进制与配置），不提供按用户的计划任务托管。
- 一键脚本不初始化 MySQL/etcd/Nginx，也不签发 relay mTLS 证书（继续用 `scripts/gen-relay-certs.sh`）；server 集群模式只在 `check-config` 阶段暴露依赖问题。
- `winsw-checksums.txt` 的首批 pin 值依赖外网机器补录，补录前 Windows 走「中止 + 手动步骤」分支。
