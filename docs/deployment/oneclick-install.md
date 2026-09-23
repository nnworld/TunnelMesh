# 一键安装脚本

三个角色各一条命令，交互式完成「下载 → 校验 → 安装 → 生成配置 → 注册服务 → 启动 → 自检」。
脚本在 `deploy/install/oneclick/`：Linux/macOS 用 `install-<role>.sh`，Windows 用 `install-<role>.ps1`，
`<role>` 取 `server`、`agent`、`client`。下载、校验、交互、渲染、服务注册全部集中在
`tunnelmesh-install-common.sh` / `tunnelmesh-install-common.ps1`，入口脚本只声明角色与问答项。

重复运行同一命令即升级：保留现有配置，旧二进制备份为 `<binary>.bak-<UTC时间戳>`。

## 安装命令

### Linux / macOS

推荐先落盘、审阅，再执行（curl 失败立刻可见，也能先看清脚本要做什么）：

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

一行式（管道），参数写在 `-s --` 之后：

```sh
curl -fsSL https://raw.githubusercontent.com/nnworld/TunnelMesh/main/deploy/install/oneclick/install-agent.sh \
  | bash -s -- --version v1.1.1
```

`server` 与 `client` 只需把 URL 里的 `install-agent.sh` 换成 `install-server.sh` / `install-client.sh`。

两种一行式的已知坑：

- `bash -c "$(curl ...)"`：curl 失败时命令替换得到**空串**，bash 什么都不执行却返回 0，看起来像装好了。
  脚本化场景请用「先落盘再执行」，或显式检查 curl 退出码。
- `bash -c '<script>' <name> [args...]`：第一个参数会被 bash 当作 `$0` 吃掉。传参时要么补一个占位名，
  要么改用环境变量：

  ```sh
  # 占位名写法
  /bin/bash -c "$(curl -fsSL <url>)" install-agent --yes --server-url wss://tunnel.example.com/ws/agent
  # 环境变量写法（CI 里更稳）
  TM_ONECLICK_YES=1 /bin/bash -c "$(curl -fsSL <url>)"
  ```

- `curl | bash`：stdin 不是终端。脚本的交互提示统一从 `/dev/tty` 读取，因此问答照常工作；
  只有 `/dev/tty` 也打不开时（部分容器、CI）才需要 `--yes`。

### Windows

以**管理员身份**打开 PowerShell：

```powershell
Set-ExecutionPolicy -Scope Process Bypass
irm https://raw.githubusercontent.com/nnworld/TunnelMesh/main/deploy/install/oneclick/install-agent.ps1 -OutFile install-agent.ps1
notepad .\install-agent.ps1
.\install-agent.ps1
```

等价的一行式（参数直接跟在 scriptblock 后面，不需要占位名）：

```powershell
& ([scriptblock]::Create((irm https://raw.githubusercontent.com/nnworld/TunnelMesh/main/deploy/install/oneclick/install-agent.ps1))) -Yes
```

不推荐 `iex (irm <url>)`：无法传参，且会把脚本展开到当前作用域。
参数说明用 `Get-Help .\install-agent.ps1 -Detailed` 或 `.\install-agent.ps1 -?` 查看，
服务托管细节见 [Windows Service 安装](windows-service.md)。

## 平台与托管矩阵

| 平台 | 归档 | 默认托管 | 可选托管 | 提权要求 |
| --- | --- | --- | --- | --- |
| Linux amd64/arm64 | `.tar.gz` | systemd **user** 单元（当前用户） | `--mode system`：systemd **system** 单元，运行账户默认 `tunnelmesh` | user 不需 root；system 需 root |
| macOS amd64/arm64 | `.tar.gz` | launchd **LaunchAgent**（当前用户） | `--no-service`：只装二进制与配置 | 不需要 root；以 root 调用且存在 `SUDO_USER` 时自动降权到该用户 |
| Windows amd64/arm64 | `.zip` | WinSW 包装的 Windows 服务（机器级） | `-NoService`：只装二进制与配置 | 必须管理员 PowerShell |

安装模式判定规则（Linux）：显式 `--mode` 优先；否则非 root → `user`，root 且存在 `SUDO_USER` → `user`
（目标用户取 `SUDO_USER`），root 且无 `SUDO_USER` → `system`。macOS 只有用户级 LaunchAgent，
`--mode system` 会告警并降级为 `user`。

无 systemd 的环境（容器、WSL1、Alpine openrc）不会中止：脚本退化为「安装二进制 + 生成配置」，
打印等价的前台启动命令，以退出码 0 结束，并在摘要里标注「未注册服务」。

### 默认路径

| 用途 | Linux user | Linux system | macOS | Windows |
| --- | --- | --- | --- | --- |
| 二进制 | `~/.local/bin` | `/usr/local/bin` | `~/.local/bin` | `C:\Program Files\TunnelMesh` |
| 配置 YAML | `~/.config/tunnelmesh/<role>.yaml` | `/etc/tunnelmesh/<role>.yaml` | `~/.config/tunnelmesh/<role>.yaml` | `C:\ProgramData\TunnelMesh\<role>.yaml` |
| 敏感值 env | `~/.config/tunnelmesh/<role>.env`（0600） | `/etc/tunnelmesh/<role>.env`（0640 `root:<运行组>`） | 渲染进 LaunchAgent plist（0600） | 渲染进 WinSW 服务 XML 的 `<env>`（ACL 收紧） |
| 状态 | `~/.local/share/tunnelmesh` | `/var/lib/tunnelmesh`（server）/ `/var/lib/tunnelmesh-<role>` | `~/.local/share/tunnelmesh` | `C:\ProgramData\TunnelMesh` |
| 服务定义 | `~/.config/systemd/user/tunnelmesh-<role>.service` | `/etc/systemd/system/tunnelmesh-<role>.service` | `~/Library/LaunchAgents/com.tunnelmesh.<role>.plist` | WinSW 服务 `tunnelmesh-<role>` |
| 日志 | `journalctl --user -u tunnelmesh-<role>` | `journalctl -u tunnelmesh-<role>` | `~/Library/Logs/tunnelmesh-<role>.log` | `C:\Program Files\TunnelMesh\logs` |
| 下载缓存 | `${XDG_CACHE_HOME:-~/.cache}/tunnelmesh/releases/` | 同左 | 同左 | `%LOCALAPPDATA%\TunnelMesh\releases\` |

所有路径都能用 `--bin-dir`、`--config-dir`、`--state-dir`（PowerShell `-InstallDir`、`-ConfigDir`、`-StateDir`）覆盖。

## 交互项

所有提示都有确定默认值，直接回车即取默认。`--yes` 下全部取默认值或显式传入值；
缺必填项（如 agent token）时**直接失败并指出缺失的 flag/env**，不静默降级。
提示一律从 `/dev/tty` 读取，因此 `curl | bash` 下也能问答。

通用项（三个角色都会问，能用 flag 预先给定的就不问）：

| 提示 | 默认 | 对应 flag |
| --- | --- | --- |
| 安装模式（仅 Linux） | 见上面的判定规则 | `--mode user\|system` |
| 目标用户（user 模式） | 当前用户；root + `SUDO_USER` 时取该用户 | `--user <name>` |
| 运行账户 / 运行组（system 模式） | `tunnelmesh` / 同名 | `--run-user`、`--run-group`、`--create-run-user` |
| 注册服务并立即启动 | 是 | `--no-service`、`--no-start` |
| 开机自启 | 是（user 模式含 `loginctl enable-linger`） | `--no-enable`、`--no-linger` |
| 已有配置文件 | 覆盖（先备份，保留最近 3 份） | `--keep-config`、`--reconfigure` |
| 下载缓存 | 启用 | `--no-cache` |

### server

| 提示 | 默认 | 落地位置 |
| --- | --- | --- |
| 运行模式 | `local` | `mode` |
| HTTP 监听地址 | `127.0.0.1:8080` | `server.http_addr` |
| 动态托管域名后缀 | `apps.example.com` | `server.dynamic_suffix` |
| MySQL DSN（仅 cluster，隐藏输入 + 二次确认） | 无，必填 | 环境变量 `TUNNELMESH_STORAGE_MYSQL_DSN` |
| MySQL 启用 TLS（仅 cluster） | `no` | `storage.mysql.tls` |
| 注册发现（仅 cluster） | `database`，可选 `etcd`（追问 endpoints） | `registry.type`、`registry.endpoints` |
| 启用 relay（仅 cluster） | `no`；选是追问 listen/endpoint/mTLS 四件套与 node token | `server.relay.*`，token → `TUNNELMESH_SERVER_RELAY_NODE_TOKEN` |
| SQLite 路径（仅 local） | `<state>/tunnelmesh.db` | `storage.sqlite.path` |
| 自动建表/执行增量迁移 | `yes` | `storage.auto_init` |
| 启用浏览器 WebSSH/SFTP | `yes` | `server.webssh.enabled` |
| 启用 tp-* HTTP 代理入口 | `no`；选是追问 listen 与 domain_suffix | `server.proxy_entry.*` |
| 由本进程终止 TLS | `no`（提示交给 Nginx） | `tls.enabled` |
| Host / Origin 白名单 | 空（逗号分隔） | `security.allowed_hosts`、`security.allowed_origins` |
| 自动生成身份主密钥 | `yes` | `TUNNELMESH_TOKEN_ENCRYPTION_KEY`（cluster 追加 `..._KEY_ID` 与 `TUNNELMESH_TRACE_SIGNING_KEY`） |
| 安装后立即创建首个管理员 | `yes` | 安装后执行 `admin bootstrap`，凭据只打印到终端 |

身份主密钥用 `openssl rand -base64 32` 生成（无 openssl 时读 `/dev/urandom`），输出只显示掩码。
集群模式下所有 Server 节点必须使用**完全一致**的主密钥与 trace 签名密钥，否则跨节点 SSO/traceroute 会间歇失败。

### agent

| 提示 | 默认 | 落地位置 |
| --- | --- | --- |
| Server WebSocket URL | 无，必填；校验 `ws://`/`wss://` 且以 `/ws/agent` 结尾 | `agent.server_url`（flag `--server-url`） |
| Agent ID | `agent-<hostname 规范化>` | `agent.id`（flag `--agent-id`） |
| Agent service token | 无，必填；隐藏输入 + 二次确认 | 环境变量 `TUNNELMESH_AGENT_TOKEN` |
| 连接池 min / max | `1` / `1` | `agent.connections.*` |
| instance_id | 留空自动生成并持久化 | `agent.instance_id`（留空则不写该键） |
| 上报宿主机 metadata | `no`；选是追问 name + 来源（`file`/`env`）+ 路径或变量名 | `agent.metadata[]` |

metadata 名称在交互期就校验字符集（字母数字与 `.` `_` `-`）与敏感词
（password/token/secret/private key/api key/credential/authorization/cookie/DSN），命中即拒绝该项并说明原因，
避免装完才在启动日志里发现被 redact。

### client

| 提示 | 默认 | 落地位置 |
| --- | --- | --- |
| Server WebSocket URL | 无，必填；校验以 `/ws/client` 结尾 | `client.server_url`（flag `--server-url`） |
| Client service token | 无，必填；隐藏输入 + 二次确认 | 环境变量 `TUNNELMESH_CLIENT_TOKEN` |
| instance_id 持久化路径 | 平台默认（见「默认路径」的状态目录） | `client.instance_id_path` |
| 现在添加本地转发 | `no`；选是则循环添加 0..n 条 | `client.tunnels[]`（flag `--tunnel`，可重复） |
| └ 转发名称 | `tunnel-<序号>` | `name` |
| └ 协议 | `tcp`，可选 `udp`/`http`/`socks5` | `protocol` |
| └ 本地监听 | `127.0.0.1:15432` 起递增 | `listen` |
| └ 目标 Agent ID | 无，必填 | `agent_id` |
| └ 目标 host / port | 无（`socks5` 跳过） | `target_host`、`target_port` |
| └ 允许非 loopback 监听 | `no`；选是则强制要求 `auth_mode` 非 `none` | `allow_remote`、`auth_mode` |

已经用 `--tunnel` 传过转发就不再问答，避免覆盖命令行意图。监听地址填非 loopback 时立即告警并要求
显式同意 + 配置认证，否则拒绝写入（与 [客户端配置示例](../operations/client-configuration-examples.md) 的安全约定一致）。

## 非交互安装（CI）

**刻意不提供 `--token <value>`**：命令行参数会进入 `ps` 输出、shell 历史与 CI 日志。
敏感值只接受三种入口，优先级从高到低：

1. `--secret-env-file <path>`：`KEY=VALUE` 行格式；文件权限必须是 `0600`/`0400`/`0640`，否则拒绝读取；
2. `--token-file <path>`：只用于 token，取文件首行；
3. 环境变量：`TUNNELMESH_AGENT_TOKEN`、`TUNNELMESH_CLIENT_TOKEN`、`TUNNELMESH_STORAGE_MYSQL_DSN`、
   `TUNNELMESH_SERVER_RELAY_NODE_TOKEN`、`TUNNELMESH_TOKEN_ENCRYPTION_KEY`、
   `TUNNELMESH_TOKEN_ENCRYPTION_KEY_ID`、`TUNNELMESH_TRACE_SIGNING_KEY`；
4. 交互式隐藏输入（`--yes` 下不可用）。

摘要与日志中的敏感值一律掩码（前 4 位 + `****`）。

agent（全 flag + env，无需任何问答）：

```sh
TM_ONECLICK_YES=1 TUNNELMESH_AGENT_TOKEN="$(cat /run/secrets/agent.token)" \
  /bin/bash /tmp/tunnelmesh-install-agent.sh \
  --version v1.1.1 \
  --server-url wss://tunnel.example.com/ws/agent \
  --agent-id agent-ci-01
```

client（`--tunnel` 可重复；`protocol` 取 `tcp`、`udp`、`http`、`socks5`）：

```sh
TM_ONECLICK_YES=1 TUNNELMESH_CLIENT_TOKEN="$(cat /run/secrets/client.token)" \
  /bin/bash /tmp/tunnelmesh-install-client.sh \
  --server-url wss://tunnel.example.com/ws/client \
  --tunnel pg:tcp:127.0.0.1:15432:agent-ci-01:10.0.0.5:5432 \
  --tunnel dns:udp:127.0.0.1:15353:agent-ci-01:10.0.0.53:53 \
  --tunnel web:http:127.0.0.1:18080:agent-ci-01:10.0.0.7:8080 \
  --tunnel proxy:socks5:127.0.0.1:11080:agent-ci-01
```

server 的 `--yes` 默认产出「local + SQLite + `127.0.0.1:8080`」的单机部署：

```sh
TM_ONECLICK_YES=1 /bin/bash /tmp/tunnelmesh-install-server.sh --mode system --create-run-user
```

server 的监听地址、域名后缀、运行模式等目前**没有对应 flag**，只能问答。集群部署有两种无人值守写法：

```sh
# 写法一：把答案按提示顺序喂给 stdin（不要加 --yes，否则一律取默认值）
TM_ONECLICK_ALLOW_STDIN=1 \
TUNNELMESH_STORAGE_MYSQL_DSN='tm:secret@tcp(127.0.0.1:3306)/tunnelmesh' \
  /bin/bash /tmp/tunnelmesh-install-server.sh --mode system --create-run-user <<'EOF'
cluster
0.0.0.0:8080
apps.example.com
no
database
no
yes
yes
no
no


yes
no
EOF
```

```sh
# 写法二：先用 --yes 装出默认配置，再改 YAML 并重启（升级默认不覆盖配置）
TM_ONECLICK_YES=1 /bin/bash /tmp/tunnelmesh-install-server.sh --mode system --create-run-user
sudo editor /etc/tunnelmesh/server.yaml
sudo systemctl restart tunnelmesh-server
```

写法一的空行表示「取该提示的默认值」，顺序必须与上面的 server 交互项表一致；
`TUNNELMESH_STORAGE_MYSQL_DSN` 已给值时不会再问 DSN，因此它不占一行。

Windows 侧参数名一一对应（`-Yes`、`-ServerUrl`、`-AgentId`、`-TokenFile`、`-SecretEnvFile`、`-Tunnel`）：

```powershell
$env:TUNNELMESH_AGENT_TOKEN = Get-Content C:\secure\agent.token -Raw
.\install-agent.ps1 -Yes -Version v1.1.1 -ServerUrl wss://tunnel.example.com/ws/agent -AgentId agent-ci-01
```

## 安装到其它用户

user 模式装到别的用户（需 root，脚本会自动 re-exec 成目标用户）：

```sh
sudo /bin/bash /tmp/tunnelmesh-install-agent.sh --user alice \
  --server-url wss://tunnel.example.com/ws/agent --agent-id agent-alice
```

`bash -c "$(curl ...)"` 与 `curl | bash` 形态下拿不到入口脚本的真实路径，无法 re-exec；
这两种形态请改用目标用户直接执行，或先落盘再 `sudo bash <file> --user alice`。

system 模式下换服务运行账户（生成 drop-in `/etc/systemd/system/tunnelmesh-<role>.service.d/oneclick.conf`
覆盖 `User=`/`Group=` 与路径，不修改仓库模板）：

```sh
sudo /bin/bash /tmp/tunnelmesh-install-server.sh --mode system \
  --run-user tmsvc --run-group tmsvc --create-run-user
```

`--create-run-user` 会在账户不存在时用 `useradd`/`adduser` 创建系统用户。

## 升级、回滚与卸载

- **升级**：重复执行同一脚本即升级。检测到已安装版本后打印「当前 vX → 目标 vY」，
  停服务 → 原子替换二进制 → `check-config` → 起服务；任一步失败自动恢复 `.bak-<ts>` 并重启，退出码 7。
  **升级不重新生成配置**，沿用磁盘上现有的 YAML 与 env 文件，避免冲掉手工调优过的参数。
- **重新生成配置**：加 `--reconfigure`；覆盖前自动备份为 `<path>.bak-<UTC时间戳>`，最多保留最近 3 份。
- **降级**：允许（`--version` 指定旧版）。schema 已升级时 server 会自行拒绝启动，脚本会完整回显
  `check-config` 的原始错误；数据库不会自动降级，处理方式见 [Schema 升级与回滚](../operations/schema-upgrades.md)。
- **卸载**：`--uninstall` 停服务 → `disable` → 删除 unit/plist/drop-in/WinSW 服务定义 →
  删除本脚本安装的二进制及其 `.bak-*` 备份；**保留**配置、env、数据目录与下载缓存，并逐条打印保留的路径。

```sh
/bin/bash /tmp/tunnelmesh-install-agent.sh --uninstall          # Linux / macOS
.\install-agent.ps1 -Uninstall -Yes                              # Windows
```

## 镜像源与离线安装

| 场景 | 做法 |
| --- | --- |
| 内网 Release 镜像 | `--base-url https://mirror.example.com/tunnelmesh/releases` 或环境变量 `TUNNELMESH_RELEASE_BASE_URL`；镜像需保持 `<base>/download/<version>/<archive>` 与 `SHA256SUMS` 布局 |
| 内网 raw 镜像（共享库下载） | `--raw-base-url <url>` 或环境变量 `TUNNELMESH_RAW_BASE_URL` / `TM_RAW_BASE_URL`。引导阶段发生在参数解析之前，因此一行式安装只能用**环境变量**指定 |
| 锁定共享库版本 | `TM_ONECLICK_REF=v1.1.1`；默认 `main`。服务模板一律取自**已校验的发布归档**，天然与所装版本一致 |
| 气隙安装 | `--archive /path/tunnelmesh-v1.1.1-linux-amd64.tar.gz`：跳过下载与缓存；同目录存在 `SHA256SUMS` 时仍会校验，缺失则告警跳过 |
| 本地已有共享库 | `TM_ONECLICK_LIB=/path/tunnelmesh-install-common.sh` |
| 同机装多个角色 | 直接跑多个入口脚本：下载缓存按归档名共享，只下载一次 |
| 不用缓存 | `--no-cache`（不读也不写 `${XDG_CACHE_HOME:-~/.cache}/tunnelmesh/releases/`） |
| GitHub API 限流 | `TUNNELMESH_GITHUB_TOKEN`（或 `--github-token`，建议用环境变量以免进历史） |

缓存命中后仍会重新校验 SHA256；校验不通过就删除该缓存条目并重新下载。

版本解析顺序：`--version vX.Y.Z`（格式必须匹配 `^v[0-9]+\.[0-9]+\.[0-9]+$`）→ GitHub API
`releases/latest` 的 `tag_name` → `GET <base>/latest` 的重定向头 → 全部失败则报错并提示显式传 `--version`。

## 退出码与排障

| 码 | 含义 | 最可能原因与处置 |
| --- | --- | --- |
| 0 | 成功（含「无服务管理器，只装二进制」的退化成功） | 摘要里会标注「未注册服务」，用打印出来的前台命令启动 |
| 2 | 参数错误 / 版本格式非法 / 未知协议 / 未知 flag | 按报错里的 flag 名改；`--help` 看完整参数面 |
| 3 | preflight 失败：缺命令、权限不足、目标用户不可用、没有输入通道、缺必填敏感值 | 报错会点名缺失项。`--mode system` 需 root；无 tty 且未 `--yes` 时按提示补 flag/env |
| 4 | 下载失败（含共享库下载失败、WinSW 未登记校验和） | 检查网络与 `--base-url`；镜像需保持目录布局。WinSW 用 `-WinSW <本地路径>` 绕过 |
| 5 | 校验失败：`SHA256SUMS` 缺条目或不匹配、WinSW pin 不匹配 | 不要重试绕过。确认镜像内容与官方 Release 一致；缓存损坏时加 `--no-cache` 重新下载 |
| 6 | `check-config` / `init-node-id` / `plutil -lint` 失败 | 原始错误会完整回显。常见于 DSN 不可达、端口被占、schema 版本不匹配 |
| 7 | 服务注册或启动失败（升级场景已完成自动回滚） | 看 `journalctl --user -u tunnelmesh-<role>` / `journalctl -u ...` / `logs\*.err.log` |
| 8 | 卸载失败 | 服务管理器不可用或权限不足；按报错手工执行 `systemctl --user stop` + `disable` / `launchctl bootout` / WinSW `uninstall` |

其它排障入口：

- `--help` 打印完整参数面、五种调用形态与安全约定。
- 脚本自身不写日志文件：正常进度、提示与摘要走 stdout，错误与告警走 stderr，便于 `| tee` 与 CI 采集。
  提示前缀 `==>`，告警 `WARNING:`，错误 `ERROR:`。
- 服务日志位置在安装摘要里给出。
- 脚本行为相关环境变量：`TM_ONECLICK_YES=1`（等价 `--yes`）、`TM_ONECLICK_REF`、`TM_ONECLICK_LIB`、
  `TM_ONECLICK_ALLOW_REMOTE=1`（非交互场景下同意非 loopback 监听）、
  `TM_ONECLICK_ALLOW_STDIN=1`（允许从管道 stdin 读答案）、`TM_HTTP_TIMEOUT=<sec>`（默认 120）。

## 信任模型

`curl | bash` 类安装把「下载什么就执行什么」的信任交给了 raw 域名与 TLS。本项目的缓解措施：

1. **先下载再审阅**是文档里的推荐写法；脚本内容就是 `install-<role>.sh` + `tunnelmesh-install-common.sh`
   两个文件，可以完整读完再执行。
2. 入口脚本与共享库同源（同一个 raw 基址、同一个 ref），共享库下载后会校验
   「HTTP 成功、非空、首行是 `#!/usr/bin/env bash`」，否则以退出码 4 中止。
3. Release 归档必须通过同目录 `SHA256SUMS` 校验后才解压；不匹配立即中止（退出码 5）并打印期望/实际值。
   校验文件名与格式（`sha256` + 两个空格 + 归档名）与 `scripts/install.sh` 共用同一份契约，由测试守护。
4. Windows 侧的 WinSW 只下载 `winsw-checksums.txt` 中**已登记校验和**的版本；没有登记条目时中止并给出手工步骤。
5. 敏感值不进命令行、不进 YAML、不进日志；只经环境变量、`--token-file`、`--secret-env-file`
   或隐藏输入注入，落盘文件权限按平台收紧。
6. 需要完全离线审阅时：从 Release 页下载 `SHA256SUMS` 与归档，校验后用
   `--archive <path>` 安装，全程不访问网络（共享库用 `TM_ONECLICK_LIB` 指向本地副本）。

## 相关文档

- [Linux systemd 安装](linux-systemd.md)：单元文件、drop-in、linger 与日志
- [macOS launchd 安装](macos-launchd.md)：LaunchAgent、`EnvironmentVariables` 渲染与卸载
- [Windows Service 安装](windows-service.md)：WinSW、`<env>` 块与校验和补录
- [二进制发布与归档](binary-release.md)：归档命名、内容与校验
- [部署目录说明](../../deploy/README.md)：模板单一来源与占位符约定
