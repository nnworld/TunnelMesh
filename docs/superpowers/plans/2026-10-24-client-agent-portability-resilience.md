# Client/Agent 便携性与容错加固实施计划

> 执行约束：本计划针对 P1-2、P1-3、P2-5、P2-7、P2-9。计划创建后暂停，获得用户明确确认后再修改生产代码；不创建新 worktree，不提交、不推送、不合并。

## 目标

1. 修复 Windows 普通用户托盘创建 client instance ID 时写入 %ProgramData% 失败的问题。
2. 让托盘托管的 Client Runtime 在非主动停止时自动恢复，并避免单条隧道监听异常直接拖垮全部隧道。
3. 为 Agent/Client 到 Server 的 WebSocket 出站链路增加直连、HTTP CONNECT、SOCKS5 和系统/环境代理选择能力。
4. 默认拒绝非本地明文 ws://，保留显式开发/兼容开关，并让 CLI、托盘和安装器给出安全提示。
5. 统一 Windows 服务与托盘的配置路径解析策略，避免静默使用两份 client.yaml。

## 范围与非目标

- 覆盖 internal/client、internal/agent、internal/config、internal/tray、Windows 安装脚本和相关文档/测试。
- 不改变隧道协议、Server API、数据库 Schema、macOS 原生壳层和现有非 Windows 默认路径。
- 不把代理密码写入 client.yaml 或 agent.yaml；不实现远程配置中心、P2P NAT traversal 或通用代理服务。
- 不把 Windows per-user 托盘强行改成需要管理员权限的机器级服务。

## 架构决策

### 1. Windows 身份文件按配置作用域解析

- 保留 client.instance_id_path 显式覆盖。
- 无显式覆盖时，根据配置文件所在目录解析默认身份路径：配置位于 %ProgramData%\TunnelMesh 时使用同目录的 client-instance-id；托盘默认用户目录使用 %LOCALAPPDATA%\TunnelMesh\client-instance-id。
- EnsureClientInstanceID 继续创建父目录、使用 0600 语义并原子创建；权限错误包含实际路径和修复建议。
- macOS、Linux 和已有显式路径行为保持不变。

### 2. Runtime 采用“进程级监督 + 隧道级重建”

- Runtime 保持现有 API 和 advisory lock 语义。
- 监听器异常不再因一个 forwarder 直接取消整个 Client 上下文；每条隧道记录失败状态并由 Runtime 以退避方式重建该条隧道。
- Server 会话池/共享连接全部退出时仍由 Runtime 返回上层，避免隐藏配置错误或锁冲突。
- 托盘新增 supervisor：区分主动 Stop、配置错误/锁冲突和可恢复传输错误；可恢复错误使用带 jitter 的指数退避，重载当前配置后重建 Runtime，并暴露最后错误和重启次数。
- /api/actions/restart 复用同一 supervisor，不产生第二个 Runtime，也不绕过锁。

### 3. 共享 WebSocket 出站代理适配器

- 在 internal/transport 定义 DialOptions/代理模式：direct、http_connect、socks5、system。
- Agent 和 Client 的 WebSocket 拨号统一使用适配器，再分别完成现有协议握手、Origin、Authorization 和子协议协商。
- system 优先使用 HTTP_PROXY/HTTPS_PROXY/NO_PROXY；平台系统代理读取失败时回落环境代理并记录脱敏原因。
- HTTP CONNECT 和 SOCKS5 用户名/密码只允许来自环境变量或独立权限收紧的 secret 文件；日志不输出密码或带用户信息的完整 URL。
- 代理连接、握手和重连沿用现有超时、退避和指标分类。

### 4. 明文 WebSocket 安全默认值

- 在 Agent/Client 传输配置新增 allow_insecure_transport，默认 false。
- wss:// 始终允许；ws:// 仅在回环/本地开发地址，或显式设置 allow_insecure_transport: true 时允许。
- 非本地 ws:// 在 config.Validate 阶段快速失败，错误明确指出切换到 wss:// 或显式开发开关。
- 现有远端 ws:// 测试 fixture 改为显式开启开关；生产文档示例统一使用 wss://。

### 5. Windows 配置路径的可解释解析

- --config、--config-dir 和显式 client.instance_id_path 优先级最高。
- Windows 托盘默认使用 %LOCALAPPDATA%\TunnelMesh，Windows 服务安装器默认继续使用 %ProgramData%\TunnelMesh；托盘不静默切换到服务配置。
- 设置页显示当前路径、服务路径和是否共享配置；需要共用时必须显式指定同一配置文件并校验 ACL。
- 文档说明 per-user 托盘和 machine-level 服务是两个安全边界，避免用户误以为两者自动共享。

## 精确文件清单

### 配置、身份与路径

- 修改 internal/client/identity.go、internal/client/identity_test.go
- 修改 internal/tray/paths.go、internal/tray/paths_test.go
- 修改 internal/tray/app.go、internal/tray/app_test.go
- 修改 internal/config/config.go、internal/config/config_test.go
- 修改 internal/cli/client_run.go 或现有配置校验调用点
- 修改 cmd/tunnelmesh-client-tray/main.go

### Runtime 与托盘监督

- 修改 internal/client/serve_watch.go、internal/client/runtime.go
- 修改或新增 internal/client/serve_watch_test.go、internal/client/runtime_test.go
- 修改 internal/tray/app.go、internal/tray/api.go
- 修改 internal/tray/app_test.go、internal/tray/api_test.go

### WebSocket 代理

- 新增 internal/transport/dialer.go、internal/transport/dialer_test.go
- 新增 internal/transport/proxy_unix.go、internal/transport/proxy_windows.go、internal/transport/proxy_other.go
- 修改 internal/client/websocket.go、internal/client/websocket_test.go
- 修改 internal/agent/websocket.go、internal/agent/websocket_test.go
- 按需要修改 go.mod、go.sum，仅引入必要的 golang.org/x/net/proxy 依赖

### 安全提示、安装与文档

- 修改 deploy/install/oneclick/tunnelmesh-install-common.sh
- 修改 deploy/install/oneclick/tunnelmesh-install-common.ps1
- 修改 deploy/install/oneclick/oneclick_config_test.go
- 修改 deploy/install/oneclick/oneclick_scripts_test.go
- 修改 docs/operations/configuration.md
- 修改 docs/operations/client-configuration-examples.md
- 修改 docs/deployment/oneclick-install.md
- 修改 docs/deployment/windows-client-tray.md
- 修改 docs/user-guide/client-tray.md
- 修改 docs/operations/troubleshooting.md
- 文档索引受影响时执行 python3 scripts/gen_doc_index.py，不手工编辑索引

## 任务接口

1. 配置层提供 AllowInsecureTransport 和代理配置的解析、默认值、校验结果；Client、Agent、托盘、安装器不复制 URL 判断。
2. internal/transport 提供带 context 的 TCP/WebSocket 底层连接能力；上层只传目标地址、代理配置和超时。
3. Runtime 提供单隧道状态、重建次数、最后错误和主动停止标识；托盘 supervisor 不直接操作 forwarder 内部。
4. Windows 路径解析返回配置目录、身份目录和服务目录的结构化结果，UI 和日志只使用脱敏路径摘要。

## TDD 执行步骤

### 阶段 A：先写失败测试

- Windows 身份路径：普通用户环境不默认写 %ProgramData%；显式服务配置仍使用 %ProgramData%；权限和重复创建保持正确。
- 路径解析：托盘默认路径、服务默认路径、显式路径和不自动复制配置。
- Runtime：单隧道失败只标记并重建该隧道；主动 Stop 不重启；配置错误和锁冲突不重试；退避有上限和 jitter；统计暴露最后错误/重启次数。
- 代理：direct、HTTP CONNECT、SOCKS5、NO_PROXY、取消/超时、凭据来源和日志脱敏；Agent/Client 复用同一拨号器。
- 安全：远端 ws:// 默认失败，回环地址允许，显式开关允许，wss:// 正常；CLI/托盘检测显示同一错误。
- 安装器与文档契约：Windows/Unix 默认目录、wss:// 示例、代理 secret 不写 YAML。
- 先运行最小范围测试并确认红灯：

    go test ./internal/client ./internal/agent ./internal/config ./internal/tray ./internal/transport -count=1

预期：新断言因缺少路径、监督、代理和安全策略实现而失败；不得伪造失败结果。

### 阶段 B：最小实现

1. 实现配置字段、默认值和 URL/代理校验。
2. 实现身份路径及 Windows 托盘/服务路径解析。
3. 抽取共享 WebSocket 拨号器，接入 Agent 和 Client，保留现有协议握手行为。
4. 调整 forward watcher 与 Runtime 的单隧道恢复边界，再接入托盘 supervisor。
5. 更新 CLI、托盘设置/检测、安装器和文档。

### 阶段 C：绿灯与重构

- 先通过受影响包测试，再修复竞态、取消、重复启动和权限边界。
- 将重复代理/URL/路径判断收敛到共享包，确认 Handler → Service → Repository 分层不被破坏。
- 对错误文本、指标标签和托盘统计做脱敏检查。

## 验证命令

实现完成后至少执行：

    gofmt -w <本次修改的 Go 文件>
    go test ./internal/client ./internal/agent ./internal/config ./internal/tray ./internal/transport -count=1
    go test ./... -count=1
    go test -race ./internal/client ./internal/agent ./internal/config ./internal/tray ./internal/transport
    go vet ./...
    git diff --check

前端若本次触及托盘或后台资源：

    cd web-tray && npm test -- --run && npm run build
    cd ../web && npm test -- --run && npm run build

Windows 交叉验证：

    CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go test ./internal/client ./internal/agent ./internal/config ./internal/tray
    CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -tags tray ./cmd/tunnelmesh-client-tray
    CGO_ENABLED=0 GOOS=windows GOARCH=arm64 go build -tags tray ./cmd/tunnelmesh-client-tray

如修改安装器，执行对应 bash -n、PowerShell 静态契约测试和已有 NSIS/发行包检查；macOS 上复跑 -tags tray 托盘构建/打包回归。

## 回滚与发布注意事项

- 配置字段采用 additive 方式；旧版本回滚时未知字段安全忽略或保留，不删除用户配置。
- 若 Runtime 监督出现回归，可先关闭托盘自动恢复并回退到进程级停止逻辑；路径和 wss:// 安全修复独立保留。
- allow_insecure_transport 的启用必须在发布说明中明确风险；生产示例不提供远端 ws://。
- Windows 服务与托盘不自动迁移 token；回滚不会覆盖用户目录或服务目录中的 client.yaml。
- 本轮完成并通过验证后，另行按用户指令创建提交、推送和 MR。
