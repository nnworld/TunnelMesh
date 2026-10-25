# Client/Agent 自动升级、失败回滚与进程守护

- 标题：为 `client run` 与 `agent run` 增加默认开启的进程守护、自动升级和失败自动回滚
- 目标分支：`main`
- 相关记录：[实施计划](../superpowers/plans/2026-10-25-client-agent-auto-update.md)、[ADR 0003](../architecture/adr/0003-constrained-self-update-and-supervision.md)

## 摘要

`client run` 与 `agent run` 默认变成一个 supervisor：真正的服务进程（worker）作为子进程运行，崩溃后按退避重启。
worker 就绪后 supervisor 按计划检查新版本，下载并校验后在状态目录里切换到新版本；新版本必须在
`health_timeout` 内就绪并稳定运行 `stable_window`，否则自动回滚到旧版本并把该版本记为坏版本一段时间。
安装目录里的二进制从不被修改。

升级目标是“最新 Release”与“Server 版本”中较小的一个，Server 版本通过握手 ack 的新增可选字段 `server_version`
下发。升级源支持 GitHub Releases（可配镜像）和 Server 自己分发两种，完整性只校验同一发布里的 `SHA256SUMS`。

## 用户影响

- 默认开启：升级后的 `run` 进程树变成 supervisor + worker 两个进程；进程退出码有了约定（0/70/75/77/78）。
- 关闭方式：`update.enabled: false` 只关闭升级；再加 `update.supervise: false`，`run` 与此前完全一致。
  `enabled: true` 且 `supervise: false` 被配置校验拒绝。
- 官方 Docker 镜像把两项默认设为 `false`（容器由编排器守护，镜像标签应不可变），可用环境变量重新打开。
- **运维顺序变化**：升级不会超过所连接 Server 的版本，所以要先升级 Server，client/agent 才会跟随。
- 不涉及托盘应用和 Server 自身的升级。

## API、配置与授权

- 新增配置：`client.update.*`、`agent.update.*`（结构相同）、`server.updates.dir`。均有环境变量，默认值写在
  `setDefaults()`，校验见 `validateUpdate`。详见[配置参考](../operations/configuration.md#自动升级与进程守护)。
- WebSocket 握手：`ClientMetadataAckPayload` 与 `AgentMetadataAckPayload` 新增可选字段 `server_version`。
  载荷解码不是严格模式，老版本会忽略它；缺失时视为版本未知，不升级。
- 新增 Server 接口（`server.updates.dir` 为空时均返回 404）：
  `GET /api/v1/{client|agent}/updates/latest` 与 `GET /api/v1/{client|agent}/updates/files/{name}`，使用对应命名空间的服务令牌，
  已写入 `docs/api/openapi.yaml` 并由 `TestClientScopedOpenAPIMatchesTheServedSurface` 守护。
- 授权与安全：文件名只允许 `SHA256SUMS`、`manifest.json` 和固定格式的发行包名；`EvalSymlinks` 拒绝逃出目录的链接；
  下载有并发上限（全局 8，每令牌 2）；令牌只发往配置的 Server，重定向不跟随。
- 压缩包按不可信输入处理：只解出根目录下名为 `tunnelmesh-<role>[.exe]` 的一个普通文件，拒绝 `..`、绝对路径、
  符号链接、硬链接、重复项，并限制大小。
- AGENTS.md 的“不实现任意远程命令执行”边界补充了受限自更新的约束，见 ADR 0003。

## 测试证据

- `go build ./...`、`go vet ./...`：通过；`GOOS=windows|darwin|linux go vet` 针对 `supervisor`、`selfupdate`、`filelock`、`workerproto`、`semver`、`reconnect`、`cmd/tunnelmesh-{agent,client}`：通过。
- `go test ./... -count=1`：全部包通过。
- `go test -race`：`supervisor`、`selfupdate`、`filelock`、`workerproto`、`semver`、`reconnect`、`config`、`protocol`、`agent`、`client` 通过。
- 端到端（`internal/cli`，真实二进制）：
  - 真实 `tunnelmesh-agent` v1.3.0 通过模拟 GitHub 升级到真实 v1.4.0，并确认安装目录里的二进制摘要未变；
  - 升级到“预检通过但一启动就崩溃”的版本，自动回滚到旧版本，坏版本被记录且文件被清理；
  - 从 Server 自己的分发目录（`source: server`）完成升级。
  这三个场景在 `-race` 下连续 12 轮（共 36 次）通过。排查中发现并修复了一个竞争：测试通过改写全局 `build.Version`
  让进程内 Server 宣称版本，而 Server 的协程在并发读取它；现改为通过 `RuntimeConfig.Version` 注入。
- 故障注入：`Upgrader` 在下载后、安装后、激活前、激活后四个点注入崩溃，重启后的 `Resume` 均恢复到一致状态。

## 发布与回滚

- 发布顺序：先发布 Server（它决定升级上限），再发布 client/agent。
- 功能默认开启，发现问题时在配置里设置 `update.enabled: false` 与 `update.supervise: false` 即可恢复旧行为，无需回退二进制。
- 已升级的节点：删除 `<state_dir>/updates/` 或把 `state.json` 的 `current` 清空，下次启动回到已安装版本。
- Server 侧：`server.updates.dir` 留空即关闭分发；`server_version` 是可选字段，老 client/agent 忽略。
- 无数据库 schema 变更。

## Reviewer 关注点

1. `SHA256SUMS` 与压缩包同源，只防损坏不防篡改；`source: server` 会扩大 Server 被攻破时的影响面，因此分发默认关闭。
2. supervisor 逻辑固定在已安装版本，updater 的缺陷靠重装而不是自升级修复。
3. READY 的含义是“已连上且收到 ack”，覆盖不到“某个监听端口绑定失败”这类问题。
4. 已知的既有问题（本次未处理，见实施计划 Task 3b）：系统 unit 下 `client.lock` 位于只读的 `/etc/tunnelmesh/`，
   `acquireRuntimeLock` 的创建可能失败；supervisor 依赖这把锁完成新旧 worker 交接，建议先在真机确认再单独处理。
5. 未在真机验证的假设：Windows 上 `CTRL_BREAK_EVENT` 映射为 `os.Interrupt`；macOS arm64 发布包的 ad-hoc 签名在解出后仍然有效；
   systemd `MemoryDenyWriteExecute` 对子进程的影响。

## 集成状态

- 未提交、未推送，等待明确授权。
