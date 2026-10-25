# Client/Agent 自动升级、失败回滚与进程守护实施计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** `tunnelmesh-client run` 与 `tunnelmesh-agent run` 默认以内置父进程（supervisor）运行：崩溃自动重启，周期性检查新版本并自动升级，升级后若新版本拒绝现有配置、起不来或一直不健康则自动回滚到旧版本。全部行为可通过 `client.update.*` / `agent.update.*` 关闭。

**Architecture:** 已安装的二进制充当 supervisor，真正的服务进程（worker）由 supervisor 以 `TUNNELMESH_WORKER=1` 启动。升级后的二进制只写入状态目录 `<state>/updates/versions/<ver>/`，不修改安装目录，因此只读安装目录（systemd `ProtectSystem=strict`）和 Windows 运行中 exe 都不是问题；回滚即把 `current` 指针切回旧版本。worker 收到 Server 握手 ack 后原子写 `ready.json`，其中带有 Server 版本，supervisor 据此把目标版本限制在 `min(最新 Release, Server 版本)`。升级源支持 GitHub Releases（可配镜像）和 Server 分发两种，完整性只校验 `SHA256SUMS`。

**Tech Stack:** Go（`os/exec`、`internal/reconnect` 退避、`transport.FromConfig` 代理拨号）、Viper/mapstructure、现有 WebSocket protocol 与 `FrameClientMetadataAck` / `FrameAgentMetadataAck`、`archive/tar` + `archive/zip`、`net/http`。不引入新的第三方依赖。

**Spec:** 用户确认的决策：升级源两者都支持（默认 GitHub）；内置父进程守护；仅 SHA256SUMS 校验；目标版本不超过 Server 版本；回滚只处理升级引起的失败；新版本放状态目录分层；范围仅 `client run` 与 `agent run`，不含托盘和 Server。架构决策见 [ADR 0003](../../architecture/adr/0003-constrained-self-update-and-supervision.md)。

## Global Constraints

- 默认开启（`update.enabled=true`、`update.supervise=true`），可关闭。`supervise=false` 时 `run` 的行为必须与改动前完全一致。
- `enabled=true` 且 `supervise=false` 是非法组合，配置校验必须报错，不能静默纠正。
- 升级只能拉取本角色、本平台的发布包；来源固定为配置的 GitHub 或 Server，URL 只由配置和版本推导，任何对端消息都不能提供命令、URL 或参数。
- 只解出名为 `tunnelmesh-<role>[.exe]` 的单个文件；拒绝 `..`、绝对路径、符号链接、硬链接；压缩包上限 256 MiB，二进制上限 200 MiB。
- 绝不修改安装目录和用户配置文件；GitHub token 只走环境变量 `TUNNELMESH_GITHUB_TOKEN`，日志不得输出 token。
- 状态目录不可写时启动不能失败：降级为只重启、不升级，并记录告警。
- `server_version` 是 ack 的新增可选字段；缺失视为版本未知，不升级。老版本忽略未知字段（帧载荷解码不是严格模式）。
- 稳定版不升级到 `-pre` 版本；`dev` 构建不参与升级；永不降级。
- 退出码约定：78 配置错误、75 锁被占用、77 认证致命、70 supervisor 放弃；其余非 0 视为崩溃。
- 必须先写失败测试并观察红灯，再写最小实现；不提交 Token、构建缓存或 `node_modules`。
- 不执行 commit、push、merge 或创建远端 MR，除非用户另行明确授权。

---

### Task 1: `internal/semver`

**Files:**
- Create: `internal/semver/semver.go`
- Test: `internal/semver/semver_test.go`

**Interfaces:**
- `type Version struct{ Major, Minor, Patch int; Pre string }`
- `func Parse(s string) (Version, error)`：接受 `v1.2.3`、`1.2.3`、`v1.2.3-pre`；对 `dev`、空串、非法串返回 `ErrNotRelease`。
- `func (v Version) Compare(o Version) int`：同三元组下 `-pre` 小于正式版。
- `func (v Version) String() string`：总是带 `v` 前缀。
- `func (v Version) IsPre() bool`、`func Min(a, b Version) Version`。

- [ ] 写失败测试：表驱动覆盖 `dev`、空串、`v1.2.3-pre < v1.2.3`、`v1.10.0 > v1.9.0`、非法输入。
- [ ] 运行 `go test ./internal/semver -count=1`，确认红灯。
- [ ] 实现最小解析与比较，确认绿灯。

### Task 2: `internal/workerproto`

**Files:**
- Create: `internal/workerproto/workerproto.go`
- Test: `internal/workerproto/workerproto_test.go`

**Interfaces:**
- 常量：`EnvWorker = "TUNNELMESH_WORKER"`、`EnvReadyDir = "TUNNELMESH_WORKER_READY_DIR"`；`ExitOK=0`、`ExitGeneric=1`、`ExitSupervisorGaveUp=70`、`ExitLockHeld=75`、`ExitAuthFatal=77`、`ExitConfig=78`。
- `type ReadyInfo struct{ PID int; Version, ServerVersion string; ReadyAt time.Time }`
- `func WriteReady(dir string, r ReadyInfo) error`：临时文件 + fsync + rename，权限 0600。
- `func ReadReady(dir string) (ReadyInfo, bool, error)`：文件不存在返回 `false, nil`。
- `type ExitError struct{ Code int; Err error }` 与 `func ExitCodeOf(err error) int`。

- [ ] 写失败测试：写后读一致、并发写不产生半截文件、缺失文件返回 `false`、`ExitCodeOf` 取错误链中第一个 `ExitError` 的退出码，未知错误取 1。
- [ ] 红灯、最小实现、绿灯。

### Task 3: 信号处理与退出码

**Files:**
- Modify: `cmd/tunnelmesh-agent/main.go`、`cmd/tunnelmesh-client/main.go`
- Modify: `internal/cli/root.go`、`internal/cli/client_run.go`
- Test: `internal/cli/root_internal_test.go`、`internal/cli/client_runtime_test.go`

**Interfaces:**
- `cli.Execute` 返回的错误经 `workerproto.ExitCodeOf` 映射为进程退出码，`main` 用 `os.Exit(code)` 替代 `log.Fatal`（仍先把错误打印到 stderr）。
- `configCommand` 在 `config.Load` 失败时返回 `ExitError{Code: ExitConfig}`；`ErrRuntimeAlreadyRunning` 映射为 `ExitLockHeld`。
- `agent run` 与 `client run` 使用 `signal.NotifyContext(ctx, SIGINT, SIGTERM)`，收到信号后 ctx 取消，`runtime.Stop()` 等 defer 能执行。

- [ ] 写失败测试：配置非法时 `ExitCodeOf` 为 78；锁冲突为 75；取消 ctx 后 `client run` 返回且不报错。
- [ ] 红灯、最小实现、绿灯；确认现有 cli 测试不变。

### Task 3b: `client.lock` 在只读配置目录下回退

**说明：** 已有问题。系统 unit 下配置目录 `/etc/tunnelmesh` 在 `ProtectSystem=strict` 下只读，`acquireRuntimeLock` 的 `O_CREATE` 会失败。实施前先在真机或容器内确认；确认存在再做。

**Files:**
- Modify: `internal/client/runtime_lock.go`、`runtime_lock_unix.go`、`runtime_lock_windows.go`
- Test: `internal/client/runtime_lock_test.go`

- [ ] 写失败测试：配置目录不可写时，锁回退到状态目录并仍能互斥。
- [ ] 红灯、最小实现、绿灯。

### Task 4: 配置项 `client.update.*` / `agent.update.*`

**Files:**
- Modify: `internal/config/config.go`
- Test: `internal/config/config_test.go`

**Interfaces:**
- `type UpdateConfig struct{ Enabled, Supervise bool; Source string; CheckInterval, HealthTimeout, StableWindow, BadVersionTTL time.Duration; KeepVersions int; StateDir string; GitHub UpdateGitHubConfig }`，`UpdateGitHubConfig{ Repository, MirrorBaseURL string }`。
- `ClientConfig.Update`、`AgentConfig.Update`，标签 `mapstructure:"update" json:"update" yaml:"update"`。
- 默认值：`enabled=true`、`supervise=true`、`source=github`、`check_interval=6h`、`health_timeout=120s`、`stable_window=120s`、`bad_version_ttl=24h`、`keep_versions=2`。
- `validateUpdate(prefix string, cfg UpdateConfig, hasServerURL bool) []string` 由 `Validate` 调用。
- `bindEnvironment` 追加全部键，环境变量形如 `TUNNELMESH_CLIENT_UPDATE_ENABLED`。

- [ ] 写失败测试：默认值；每个键的环境变量覆盖；`enabled=true,supervise=false` 被拒绝；`source=server` 缺 `server_url` 被拒绝；`check_interval<5m`、`health_timeout` 越界被拒绝；`mirror_base_url` 含凭据被拒绝；agent 与 client 对称。
- [ ] 红灯、最小实现、绿灯。

### Task 5: `internal/supervisor` 核心

**Files:**
- Create: `internal/supervisor/supervisor.go`、`launcher.go`、`stop_unix.go`、`stop_windows.go`
- Test: `internal/supervisor/supervisor_test.go`、`launcher_test.go`

**Interfaces:**
- `type Launcher interface{ Start(ctx context.Context, spec Spec) (Child, error) }`，`Spec{ Bin string; Args, Env []string; ReadyDir string }`。
- `type Child interface{ PID() int; Wait() <-chan ExitResult; Stop(grace time.Duration) error }`。
- `type Clock interface{ Now() time.Time; After(time.Duration) <-chan time.Time }`。
- `type Config struct{ Role string; Args []string; Backoff reconnect Policy; HealthTimeout, StableWindow, StopGrace time.Duration; Clock Clock; Launcher Launcher }`。
- `func (s *Supervisor) Run(ctx context.Context) int`，返回值即进程退出码。
- 退出码处理：0 且已被要求停止则返回 0；78 在无试运行时返回 78；75 重试至多 10 次；77 返回 77；其他按退避重启，存活满 `StableWindow` 后重置；连续 5 次崩溃且 10 分钟内未就绪返回 70。
- 真实 `Launcher`：stdin 为生命线管道；Unix 先 SIGTERM 再 Kill；Windows 用 `CREATE_NEW_PROCESS_GROUP` + `CTRL_BREAK_EVENT`。

- [ ] 写失败测试：`FakeLauncher` + 假时钟覆盖崩溃退避、各退出码、放弃策略、停止信号转发；TestMain 重入的真实子进程测试覆盖 stdin EOF 退出与 SIGTERM。
- [ ] 红灯、最小实现、绿灯。

### Task 6: worker 端就绪信号

**Files:**
- Modify: `internal/cli/root.go`、`internal/cli/client_run.go`
- Modify: `internal/client/runtime.go`
- Modify: `internal/agent/connection_pool.go`
- Test: `internal/cli/client_runtime_test.go`、`internal/cli/agent_runtime_test.go`、`internal/agent/connection_pool_test.go`

**Interfaces:**
- `TUNNELMESH_WORKER=1` 时 `run` 走 worker 路径：收到 Server ack 后调用 `workerproto.WriteReady`；stdin 读到 EOF 即取消 ctx。
- client：`Runtime.Stats().Connected` 为真且收到 `ClientMetadataAck` 后写入，`ServerVersion` 取 ack 字段；重连后 `server_version` 变化则重写。
- agent：连接池新增 `Connected() bool` 与回调，`connectionPoolHandler.Handle` 收到被接受的 `FrameAgentMetadataAck` 时触发。

- [ ] 写失败测试：worker 在 ack 后写出 `ready.json`；stdin EOF 触发关停；缺 `server_version` 时文件里为空串。
- [ ] 红灯、最小实现、绿灯。依赖 Task 13。

### Task 7: 状态目录只读降级

**Files:**
- Modify: `internal/supervisor/supervisor.go`
- Test: `internal/supervisor/supervisor_test.go`

- [ ] 写失败测试：`updates/` 不可写时 supervisor 仍启动子进程、记录告警、ready 目录回退到临时目录；临时目录也不可写时直接运行子进程。
- [ ] 红灯、最小实现、绿灯。

### Task 8: `internal/selfupdate` Store

**Files:**
- Create: `internal/selfupdate/store.go`
- Test: `internal/selfupdate/store_test.go`

**Interfaces:**
- `type State struct{ Schema int; Current, Previous string; Pending *Pending; Bad map[string]BadMark; LastCheck time.Time }`，`Pending{ Target, From string; StartedAt time.Time }`，`BadMark{ Reason string; Until time.Time }`。
- `func (s *Store) Load() (State, error)`：文件损坏按空状态处理并记录日志。
- `func (s *Store) Save(State) error`：临时文件 + fsync + rename + fsync 目录。
- `func (s *Store) BinaryPath(version, role string) string`：Windows 追加 `.exe`。
- `func (s *Store) Prune(keep int, inUse ...string) error`。

- [ ] 写失败测试：原子保存、损坏恢复、坏版本 TTL 过期、`Prune` 不删 current/previous。
- [ ] 红灯、最小实现、绿灯。

### Task 9: 下载、校验与解压

**Files:**
- Create: `internal/selfupdate/source.go`、`source_github.go`、`fetch.go`、`extract.go`
- Test: `internal/selfupdate/fetch_test.go`、`source_github_test.go`、`extract_test.go`

**Interfaces:**
- `type Source interface{ Name() string; Latest(ctx) (Release, error); Fetch(ctx, r Release, asset string, maxBytes int64) (io.ReadCloser, error) }`。
- `NewGitHubSource(repo, mirrorBase string, hc *http.Client, token string) Source`：无镜像时用 API `releases/latest`，失败回退 `/releases/latest` 的 302；有镜像只用重定向。下载地址 `{base}/{repo}/releases/download/{tag}/{asset}`。
- `ParseSHA256SUMS(io.Reader) (map[string]string, error)`。
- `FetchBinary(ctx, src, release, role, goos, goarch, stagingDir string, lim Limits) (string, error)`。
- `ExtractOne(archive io.Reader, kind ArchiveKind, wantBase, dst string, maxBytes int64) error`。
- 重试 3 次带退避；HTTP 客户端的 `DialContext` 来自 `transport.FromConfig`。

- [ ] 写失败测试：校验和不匹配被拒；`../x`、绝对路径、符号链接、硬链接条目被拒；超出大小限制被拒；`./tunnelmesh-client` 带前缀条目可匹配；tar.gz 与 zip 都可解；镜像与重定向解析；重试；代理路径。
- [ ] 红灯、最小实现、绿灯。

### Task 10: `Planner`

**Files:**
- Create: `internal/selfupdate/planner.go`
- Test: `internal/selfupdate/planner_test.go`

**Interfaces:**
- `func (p Planner) Decide(latest, server semver.Version, haveServer bool) (target semver.Version, ok bool, reason string)`。

- [ ] 写失败测试（决策表）：`min(latest, server)`；`dev` 当前版本；目标不大于当前（不降级）；在坏版本表内；Server 版本未知；稳定版遇到 `-pre`；预发布当前版本可升预发布。
- [ ] 红灯、最小实现、绿灯。

### Task 11: `Upgrader` 与回滚

**Files:**
- Create: `internal/selfupdate/upgrader.go`
- Modify: `internal/supervisor/supervisor.go`
- Test: `internal/selfupdate/upgrader_test.go`

**Interfaces:**
- `type Upgrader struct{ Store *Store; Source Source; Clock Clock; ...; faultHook func(point string) }`。
- 状态：检查 → 下载校验 → 预检（`new --version` 必须等于目标；`new --config X check-config`，环境变量同子进程）→ 激活（先写 `pending`，再切 `current`）→ 试运行（`health_timeout` 内 READY，再存活 `stable_window`）→ 提交，或回滚（`current` 切回 `previous`，目标写入坏版本表）。
- 启动时发现 `pending` 非空，视为上次试运行失败，直接回滚。

- [ ] 为每个场景各写一个失败测试（假源、假启动器、假时钟）：正常升级；校验和错误；新版本 `check-config` 失败；新版本启动即崩溃；始终不就绪；就绪后在稳定窗口内崩溃；各 `faultHook` 注入点杀 supervisor 后恢复；源不可达；Server 版本低于当前；`dev` 构建。
- [ ] 红灯、最小实现、绿灯。

### Task 12: 端到端

**Files:**
- Create: `internal/e2e/selfupdate_test.go`

- [ ] 用不同 `-ldflags -X ...build.Version` 构建两个真实二进制，打成 release 形状的 tar.gz，用 `httptest` 模拟 GitHub（`/releases/latest` 重定向 + 资产 + `SHA256SUMS`），运行真实 supervisor 完成升级；另测回滚、`client.lock` 交接、中途杀 supervisor。
- [ ] 用 `-short` 或构建标签保护，避免拖慢常规测试。

### Task 13: ack 增加 `server_version`

**Files:**
- Modify: `internal/protocol/frame.go`
- Modify: `internal/server/client_observability.go`、`internal/server/ws_agent.go`、`internal/server/session_manager.go`
- Modify: `docs/api/openapi.yaml`、`docs/protocol/`
- Test: `internal/protocol/frame_test.go`、`internal/server/*_test.go`

**Interfaces:**
- `ClientMetadataAckPayload.ServerVersion` 与 `AgentMetadataAckPayload.ServerVersion`，标签 `json:"server_version,omitempty"`。
- 抽一个辅助函数统一填充，覆盖全部 ack 构造点（`client_observability.go` 4 处，`ws_agent.go`、`session_manager.go` 各 1 处）。

- [ ] 写失败测试：往返；缺字段；老结构体能解码带新字段的载荷。
- [ ] 红灯、最小实现、绿灯。

### Task 14: Server 分发接口与 `NewServerSource`

**Files:**
- Create: `internal/server/updates_api.go`、`internal/server/update_service.go`
- Create: `internal/selfupdate/source_server.go`
- Modify: `internal/config/config.go`（`server.updates.dir`）、`internal/server/api.go`、`docs/api/openapi.yaml`
- Test: `internal/server/updates_api_test.go`、`internal/selfupdate/source_server_test.go`

**Interfaces:**
- `GET /api/v1/{agent|client}/updates/latest`、`GET /api/v1/{agent|client}/updates/files/{name}`；沿用 `handleAgentScoped` / `handleClientScoped` 的服务令牌鉴权。
- `{name}` 只允许 `SHA256SUMS`、`manifest.json` 或 `^tunnelmesh-v\d+\.\d+\.\d+(-pre)?-(linux|darwin|windows)-(amd64|arm64)\.(tar\.gz|zip)$`；`EvalSymlinks` 拒绝逃出目录的链接；`http.ServeContent` 支持 Range；并发上限 8，超出 503。
- `server.updates.dir` 为空时接口返回 404。

- [ ] 写失败测试：令牌类型不符、路径穿越、目录缺失、文件缺失、并发上限、Range。
- [ ] 红灯、最小实现、绿灯。

### Task 15: 部署、文档与 ADR

**Files:**
- Modify: `deploy/systemd/tunnelmesh-{agent,client}.service`、`Dockerfile`、`deploy/README.md`
- Modify: `docs/operations/configuration.md`、`docs/operations/client-configuration-examples.md`、`docs/user-guide/client.md`、`docs/user-guide/agent.md`、`docs/deployment/*`、`docs/en/*`、`docs/README.md`
- Modify: `AGENTS.md`（边界措辞）
- Create: `docs/pull-requests/2026-10-25-client-agent-auto-update.md`

- [ ] systemd 增加 `RestartPreventExitStatus=78 77`；Dockerfile 设置容器默认关闭（`TUNNELMESH_{CLIENT,AGENT}_UPDATE_ENABLED=false` 与 `..._SUPERVISE=false`）。
- [ ] 文档说明：需先升级 Server，client/agent 才会跟随；SHA256SUMS 的信任边界；如何关闭；排障。
- [ ] 运行 `python3 scripts/gen_doc_index.py`，检查 `git diff` 中索引已更新。
- [ ] 全量验证：`go test ./... -count=1`、`go test -race ./...`（`internal/server` 给足超时）、`go vet ./...`、`git diff --check`。

## 回滚注意事项

- 代码回滚：功能默认开启，若发现问题，可先用 `update.enabled=false` 与 `update.supervise=false` 在配置里关闭，`run` 即恢复为改动前的行为，无需回退二进制。
- 已升级的节点：`<state>/updates/state.json` 的 `current` 指向旧版本，或删除 `<state>/updates/` 目录，下次启动就回到已安装的基线版本。
- Server 端：`server_version` 是可选字段，老 client/agent 忽略；`server.updates.dir` 留空即关闭分发接口。
- 本计划不涉及数据库 schema 变更。
