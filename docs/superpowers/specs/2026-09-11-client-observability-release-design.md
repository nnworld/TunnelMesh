# 客户端运行观测与跨平台发布设计

**日期：** 2026-09-11
**状态：** 用户已确认方案 A；实现已完成并通过本地验证，待发布
**范围：** Client 到 Server 的 WebSocket 连接观测、客户端 metadata、管理后台 `/clients` 页面、跨平台二进制发布与下载入口。

## 1. 背景

当前 Server 已经能看到 Agent 的实例 metadata 和物理连接，但 Client 侧只有转发通道：

- `ClientSessionManager` 仅保存 `map[string]FrameTransport`，没有客户端身份、Owner、Token、Server 节点、心跳和活跃流。
- `ServeClientSession` 能完成鉴权和流转发，但没有客户端 metadata 协议。
- 管理后台没有 `/clients` 页面。
- `scripts/build-release.sh` 已能交叉编译六个平台，但仓库没有 GitHub Actions 发布流水线、版本注入、release manifest 和后台下载入口。

本设计将需求拆为两个相互独立、但共享构建版本信息的子项目：

1. 客户端运行观测：新增客户端实例身份、metadata 协议、连接租约、管理 API 和后台页面。
2. 跨平台发布下载：新增 GitHub Release 流水线、版本元数据、release manifest 和后台下载页面。

## 2. 目标与非目标

### 2.1 目标

1. 管理后台能按 Owner 权限查看客户端实例、metadata、WS 物理连接、Server 节点、活跃流和状态。
2. 支持跨多个 Server 节点汇总，并能按 `connection_epoch` 安全关闭一条物理连接。
3. 旧客户端仍可连接，新客户端连接旧 Server 时自动降级，不发送未知帧。
4. 为 Linux、macOS、Windows 的 amd64/arm64 生成包含三个二进制的发行包。
5. 通过 GitHub Releases 维护发行物，并提供 SHA256 校验和 manifest。
6. 管理后台提供当前版本的各平台下载链接和校验说明。

### 2.2 非目标

- 不将客户端 metadata 用于授权，不扩大 Token scope。
- 不实现客户端二进制自升级或自动下载安装。
- 不在 Server 内代理 GitHub 二进制下载流量。
- 不创建可变的 `v1`、`v2` tag；大版本通过 Release 列表和 manifest 过滤。
- 不在 metadata 或发行包中包含密码、Token、私钥、DSN 或生产凭据。

## 3. 方案选择

| 方案 | 结果 | 结论 |
| --- | --- | --- |
| A. 数据库租约 + 客户端实例 metadata | 支持跨 Server、离线判定、分页查询和连接关闭 | 采用 |
| B. 仅进程内存 | 改动小，但无法跨 Server 汇总，重启后不可见 | 不采用 |
| C. 仅 Prometheus | 适合告警和曲线，不适合管理列表和操作 | 只作为补充 |

方案 A 与现有 Agent 连接租约架构一致，能复用 `server_nodes`、relay 和 epoch fencing 思路，避免为观测数据建立第二套跨节点通信协议。

## 4. 客户端运行观测设计

### 4.1 身份模型

客户端区分三层身份：

| 身份 | 生成方 | 生命周期 | 用途 |
| --- | --- | --- | --- |
| `instance_id` | 客户端本机 | 安装实例稳定不变 | 聚合同一客户端进程的多个连接 |
| `client_instance_id` | Server | 首次接受 metadata 时生成 | 管理 API 的资源 ID |
| `connection_id` | Server | 每条物理 WS 唯一 | 定位具体连接 |

规则：

- `client.instance_id` 可以显式配置。
- 未配置时自动生成一次，格式为小写 `client-<32hex>`，并持久化到本机状态文件。
- `(owner_user_id, instance_id)` 是存储层唯一键。
- `client_instance_id` 是 opaque ID，不由客户端控制。
- 同一客户端使用不同 Owner 的 Token 时，会形成不同管理资源。

### 4.2 协议设计

新增 WebSocket 子协议：

```text
tunnelmesh.v1.open-result.flow-control.metadata
```

子协议选择顺序：

```text
metadata > flow-control > open-result > legacy
```

兼容性：

- 新客户端连接旧 Server：旧 Server 选择 flow-control 或旧子协议，客户端不发送 metadata 帧。
- 旧客户端连接新 Server：旧客户端不协商 metadata 子协议，Server 只显示基础连接信息。
- 双方都支持 metadata：客户端在鉴权后发送 `CLIENT_HELLO`，之后可发送 `CLIENT_METADATA_UPDATE`。

新增帧：

| 帧 | 方向 | 作用 |
| --- | --- | --- |
| `CLIENT_HELLO` | Client → Server | 上报初始 metadata |
| `CLIENT_METADATA_UPDATE` | Client → Server | 上报变更后的 metadata |
| `CLIENT_METADATA_ACK` | Server → Client | 确认接受、幂等或字段级错误 |

`ClientMetadataPayload` 固定字段：

```go
type ClientMetadataPayload struct {
    InstanceID     string
    ConnectionSlot int
    AgentIDs       []string
    Version        string
    Commit         string
    Platform       string
    Hostname       string
    ProcessStartAt time.Time
    ReportedAt     time.Time
    Revision       uint64
    Listeners      []ClientListener
    Items          []ClientMetadataItem
    Capabilities   []string
    Errors         []ClientMetadataError
}
```

`ClientListener` 只包含：

```go
type ClientListener struct {
    Protocol      string
    ListenAddress string
    AgentID       string
    Enabled       bool
}
```

禁止上报：

- Token 明文或 Token hash
- 用户名、密码
- 远程验证 URL
- 私钥、证书内容
- DSN
- 任意文件内容，除非通过显式 allowlist 配置

限制：

- metadata payload 最大 32 KiB。
- 单个字段最大 4 KiB。
- metadata 字段名沿用 `^[A-Za-z0-9_.-]+$`。
- 名称包含 `password`、`token`、`secret`、`private_key`、`dsn` 时拒绝。

### 4.3 数据模型

当前 `SchemaVersion=10`，本设计新增 Schema 版本 11。

#### `client_instance_metadata`

```sql
CREATE TABLE client_instance_metadata (
    id VARBINARY(255) PRIMARY KEY,
    owner_user_id VARBINARY(255) NOT NULL,
    instance_id VARBINARY(128) NOT NULL,
    metadata TEXT NOT NULL,
    capabilities TEXT NOT NULL,
    reported_at TEXT NOT NULL,
    last_seen_at TEXT NOT NULL,
    expires_at TEXT,
    stale INTEGER NOT NULL DEFAULT 0,
    updated_at VARCHAR(32) NOT NULL,
    UNIQUE(owner_user_id, instance_id)
);
```

索引：

```sql
CREATE INDEX idx_client_instance_metadata_owner ON client_instance_metadata(owner_user_id, stale, last_seen_at);
CREATE INDEX idx_client_instance_metadata_stale ON client_instance_metadata(stale, updated_at);
```

#### `client_connection_leases`

```sql
CREATE TABLE client_connection_leases (
    connection_id VARBINARY(128) PRIMARY KEY,
    client_instance_id VARBINARY(255) NOT NULL,
    token_id VARBINARY(255) NOT NULL,
    owner_user_id VARBINARY(255) NOT NULL,
    server_node_id VARBINARY(255) NOT NULL,
    connection_epoch INTEGER NOT NULL,
    active_streams INTEGER NOT NULL DEFAULT 0,
    health_score INTEGER NOT NULL DEFAULT 100,
    acquired_at TEXT NOT NULL,
    expires_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);
```

索引：

```sql
CREATE INDEX idx_client_connection_leases_instance ON client_connection_leases(client_instance_id);
CREATE INDEX idx_client_connection_leases_node ON client_connection_leases(server_node_id);
CREATE INDEX idx_client_connection_leases_owner ON client_connection_leases(owner_user_id);
CREATE INDEX idx_client_connection_leases_expires ON client_connection_leases(expires_at);
```

兼容性约束：

- MySQL 5.6 和 SQLite 都不使用 JSON 类型、CTE 或函数索引。
- metadata 与 capabilities 均为 TEXT，由 Go 解析。
- 新增表为纯增量，不影响旧表和旧查询。
- 当前应用使用精确 Schema 版本校验，本次发布建议停机或蓝绿升级；如必须滚动升级，需先发布允许兼容版本范围的版本。

### 4.4 Server 运行时

扩展 `ClientSessionManager`：

- 记录 `connection_id`、Token ID、Owner、Server Node、开始时间、最近心跳、活跃流。
- PING 更新最近心跳。
- OPEN / CLOSE 更新活跃流计数。
- 活跃流计数先保存在内存，并随租约心跳批量写库。

新增 `ClientConnectionLeaseController`：

1. 建连成功后注册 `client_connection_leases`。
2. 每 30 秒心跳续租，TTL 90 秒。
3. 正常退出时释放租约。
4. Server 崩溃后由 TTL 自动过期。
5. 元数据超过 TTL 后标记 `stale`。

关闭连接：

- 只关闭当前 WS 连接，不撤销 Token。
- 本节点连接直接关闭。
- 远端节点连接通过现有 relay/server-node 通道转发。
- 请求必须携带 `connection_epoch`，防止旧请求关闭新连接。
- 远端节点不可用时返回明确错误；租约到期后自动显示离线。

### 4.5 管理 API

新增：

```http
GET /api/v1/clients
GET /api/v1/clients/{clientInstanceId}
GET /api/v1/clients/{clientInstanceId}/connections
DELETE /api/v1/clients/{clientInstanceId}/connections/{connectionId}?connectionEpoch=...
```

权限：

- 管理员查看全部客户端。
- 普通用户只查看自己 Owner 的客户端。
- 权限过滤必须在 Repository 查询中完成，不能分页后再过滤。

列表筛选：

- cursor
- limit
- Owner
- Token
- Server 节点
- 状态
- Agent
- 关键词

状态规则：

| 状态 | 条件 |
| --- | --- |
| `online` | 任一连接租约未过期 |
| `offline` | 全部租约过期 |
| `stale` | metadata 超过 TTL |
| `metadata_unavailable` | 旧客户端未协商 metadata |

### 4.6 管理后台

新增 `/clients` 菜单和页面。

页面结构：

1. 筛选区
   - Owner，仅管理员可见
   - Token
   - Server 节点
   - 状态
   - Agent
   - 关键词
   - 重置、查询
2. 汇总卡片
   - 在线客户端
   - 活跃 WS 连接
   - 活跃流
   - metadata 未上报数量
3. 客户端实例列表
   - 客户端实例 ID
   - Owner
   - Token
   - 客户端版本
   - 平台
   - 主机名
   - Server 节点
   - 连接数
   - 活跃流
   - 状态
   - 最近心跳
   - 操作：详情
4. 详情抽屉
   - 基础信息
   - metadata 表格
   - 本地监听入口
   - WS 连接列表
   - 每条连接的关闭按钮

## 5. 跨平台发布与下载设计

### 5.1 版本元数据

扩展 `internal/build`：

```go
var (
    Version   = "dev"
    Commit    = "unknown"
    BuildTime = "unknown"
)
```

三个二进制均支持：

```bash
tunnelmesh-server --version
tunnelmesh-agent --version
tunnelmesh-client --version
```

构建时注入：

```text
-X github.com/tunnelmesh/tunnelmesh/internal/build.Version=$VERSION
-X github.com/tunnelmesh/tunnelmesh/internal/build.Commit=$COMMIT
-X github.com/tunnelmesh/tunnelmesh/internal/build.BuildTime=$BUILD_TIME
```

### 5.2 构建矩阵

| 平台 | 包格式 |
| --- | --- |
| Linux amd64 | `.tar.gz` |
| Linux arm64 | `.tar.gz` |
| macOS amd64 | `.tar.gz` |
| macOS arm64 | `.tar.gz` |
| Windows amd64 | `.zip` |
| Windows arm64 | `.zip` |

每个包包含：

- `tunnelmesh-server`
- `tunnelmesh-agent`
- `tunnelmesh-client`
- README
- docs
- Linux systemd 模板
- macOS launchd 模板
- Windows service 模板
- 安装脚本

### 5.3 GitHub Release

新增 `.github/workflows/release.yml`。

触发：

```yaml
on:
  push:
    tags:
      - "v*.*.*"
  workflow_dispatch:
```

流水线：

1. Checkout
2. Setup Go / Node
3. `cd web && npm ci`
4. `npm test -- --run`
5. `npm run build`
6. 同步 `web/dist` 到 `internal/server/web_dist`
7. `./scripts/verify-web-embed.sh`
8. `go test ./... -count=1`
9. `go test -race ./...`
10. `go vet ./...`
11. `VERSION=$TAG ./scripts/build-release.sh`
12. 生成 `SHA256SUMS`
13. 生成 `manifest.json`
14. `gh release create` 并上传资产

资产命名：

```text
tunnelmesh-v1.2.3-linux-amd64.tar.gz
tunnelmesh-v1.2.3-linux-arm64.tar.gz
tunnelmesh-v1.2.3-darwin-amd64.tar.gz
tunnelmesh-v1.2.3-darwin-arm64.tar.gz
tunnelmesh-v1.2.3-windows-amd64.zip
tunnelmesh-v1.2.3-windows-arm64.zip
SHA256SUMS
manifest.json
```

`manifest.json`：

```json
{
  "version": "v1.2.3",
  "major": 1,
  "commit": "0123456789abcdef",
  "buildTime": "2026-09-11T10:00:00Z",
  "schemaVersion": 11,
  "binaries": ["tunnelmesh-server", "tunnelmesh-agent", "tunnelmesh-client"],
  "platforms": ["linux/amd64", "linux/arm64", "darwin/amd64", "darwin/arm64", "windows/amd64", "windows/arm64"]
}
```

版本策略：

- 使用完整 SemVer tag：`vMAJOR.MINOR.PATCH`。
- GitHub `latest` 指向最新稳定 Release。
- 如需固定大版本，通过 Release 列表或 manifest 的 `major` 过滤。

### 5.4 后台下载页面

新增管理员页面 `/downloads` 和 API：

```http
GET /api/v1/downloads
```

返回：

- 当前 Server 运行版本
- commit
- build time
- GitHub 仓库
- 当前版本各平台资产链接
- `SHA256SUMS` 链接
- GitHub Release 页面链接

页面展示：

- 当前版本
- 平台卡片
- 下载链接
- 校验命令
- 升级注意事项
- Schema 版本

Server 不代理二进制内容，浏览器直接访问 GitHub。若仓库私有，下载者需要拥有 GitHub 权限；内网镜像可以作为独立扩展实现。

## 6. 安全设计

- metadata 不参与授权，Token scope 和 Agent Policy 仍是唯一授权边界。
- 不存储或展示 Token 明文、hash、密码、远程验证 URL 或完整 Authorization header。
- 普通 traceroute、审计日志和错误日志不得包含 metadata 敏感值。
- 客户端上报 metadata 的字段必须显式 allowlist。
- 关闭连接必须校验 Owner 或管理员权限，并使用 `connection_epoch` fencing。
- release 包中不包含配置、数据库、Token、私钥和日志。
- GitHub Release 资产必须提供 SHA256 校验。

## 7. 测试与验收

后端：

```bash
go test ./... -count=1
go test -race ./...
go vet ./...
git diff --check
```

前端：

```bash
cd web
npm test -- --run
npm run build
```

嵌入资源：

```bash
./scripts/verify-web-embed.sh
```

发布：

```bash
VERSION=v0.0.0-test ./scripts/build-release.sh
```

关键场景：

- 新客户端连接新 Server，能看到 metadata 和连接。
- 旧客户端连接新 Server，能看到基础连接，metadata 显示不可用。
- 新客户端连接旧 Server，不发送未知帧。
- 多 Server 节点汇总。
- 连接心跳和 TTL 过期。
- Owner 越权访问被拒绝。
- 管理员可以关闭指定 epoch 的连接。
- MySQL 5.6 与 SQLite 迁移一致。
- 六平台包生成、checksum 和 manifest 正确。

## 8. 发布与回滚

### 8.1 发布顺序

1. 备份数据库。
2. 发布包含 Schema v11 的新 Server。
3. 验证 `/clients` 和 `/downloads`。
4. 发布新版 Agent / Client。
5. 发布 GitHub Release。

### 8.2 回滚

- 应用回滚优先回退二进制。
- 新表为纯增量，旧应用理论上可忽略；但当前精确 Schema 校验会拒绝更高版本，因此需要按停机或蓝绿方案回退。
- 若数据库已迁移且旧版本拒绝启动，使用升级前备份恢复，或等待兼容版本。
- GitHub Release 资产不可变，回滚时下载上一个版本归档并校验 SHA256。

## 9. PR 拆分

建议按三个 PR 交付：

1. `feat(client): report runtime metadata`
2. `feat(server): expose client observability`
3. `ci(release): publish platform packages`

每个 PR 都必须包含：

- 实施计划引用
- PR 描述文档
- OpenAPI 变更
- 测试证据
- 发布和回滚说明

未经用户明确授权，不执行 commit、push、merge 或创建远端 PR。
