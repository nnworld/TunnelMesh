# Agent 主机信息上报与 SSH 访问设计

## 目标

在现有 TunnelMesh Agent WebSocket 会话和 TCP relay 能力之上，增加两项能力：

1. `tunnelmesh-agent` 从宿主机受控读取配置文件字段或系统环境变量，并随 WS 会话上报到 Server，Server 持久化当前信息，供管理后台和其他服务组件读取。
2. `tunnelmesh-client` 复用现有 TCP proxy，通过宿主机 SSH 公钥认证实现免输入密码的 SSH 登录和远程命令执行。

本设计不增加任意 shell 执行通道，不绕过宿主机 SSH 认证，不允许 Agent 默认上传全部环境变量。

## 范围

### 本期包含

- Agent metadata source：`file`、`env`。
- WS 认证完成后的 metadata hello、更新和 ACK。
- `agent_runtime_metadata` 持久化表及 Repository/Service。
- Server 管理 API 查询 Agent metadata。
- Web 后台 Agent 详情页展示 metadata、来源类型、更新时间和 stale 状态。
- Agent 所属用户和管理员的读取权限。
- 通过 `tunnelmesh-client proxy tcp` 访问宿主机 SSH 22 端口。
- SSH ProxyCommand 和远程命令帮助文档。
- metadata 字段、payload、过期时间和敏感名校验。

### 明确不包含

- 读取任意命令输出并上报。
- 无 SSH 密钥、无宿主机认证的“裸命令执行”。
- Server 保存 SSH 私钥或代替宿主机 sshd 做密码认证。
- 公网 UDP 监听。
- metadata 历史版本归档；本期只保存最新值和审计更新时间。

## 组件与分层

```text
tunnelmesh-agent
  MetadataCollector -> MetadataPolicy -> WS Agent Transport
                                      -> MetadataReport frame

server WS Handler -> AgentSessionService -> MetadataService -> Repository -> DB
                                      \-> AgentSessionManager

admin API Handler -> AgentMetadataService -> Repository
admin Web          -> /api/v1/agents/{id}/metadata

tunnelmesh-client -> existing TCP proxy -> Agent -> host sshd:22
ssh client         -> authorized_keys / ssh-agent authentication
```

Handler 只做认证、授权、协议解码和响应；metadata 校验、合并、过期判断和持久化由 Service 完成；SQL 只能由 Repository 执行。

## Agent 配置

推荐配置：

```yaml
agent:
  server_url: wss://tunnel.example.com/ws/agent/v1
  id: agent-devbox
  metadata:
    - name: device_id
      source: file
      path: /etc/machine-id
    - name: firmware_version
      source: file
      path: /etc/tunnelmesh/firmware-version
    - name: region
      source: env
      key: TUNNELMESH_REGION
```

内部模型：

```go
type MetadataSource struct {
    Name   string `mapstructure:"name"`
    Source string `mapstructure:"source"` // file | env
    Path   string `mapstructure:"path"`
    Key    string `mapstructure:"key"`
}
```

规则：

- `name` 只允许 `[a-zA-Z0-9_.-]`，长度不超过 64。
- `source=file` 必须指定绝对路径；单字段读取上限默认 4 KiB。
- `source=env` 必须指定环境变量名；不允许通配符读取。
- 单个 Agent 默认最多 32 个字段，总 payload 默认不超过 32 KiB。
- 值统一按 UTF-8 字符串处理；读取失败只记录字段错误，不阻断 WS 会话。
- 名称匹配 `password`、`token`、`secret`、`private_key`、`dsn` 等敏感模式时拒绝启动或拒绝上报。
- Agent 本地配置是第一层 allowlist，Server 可配置第二层可接受字段 allowlist。

## WS 协议

在现有版本化 binary frame 之上增加控制帧类型，不复用 `OPEN_STREAM`：

- `AGENT_HELLO`：Agent 认证完成后发送。
- `AGENT_METADATA_UPDATE`：metadata 内容或来源值变化时发送。
- `AGENT_METADATA_ACK`：Server 返回接受结果和当前 epoch。

示意 payload：

```json
{
  "agent_id": "agent-devbox",
  "epoch": 12,
  "revision": 4,
  "reported_at": "2026-09-06T12:00:00Z",
  "items": [
    {"name": "device_id", "source": "file", "value": "..."},
    {"name": "region", "source": "env", "value": "cn-east-1"}
  ]
}
```

协议约束：

- `agent_id`、epoch 必须匹配已认证会话；旧 epoch 的更新拒绝。
- revision 单调递增；重复 revision 必须幂等。
- Server 必须先做 payload 上限检查，再反序列化和落库。
- metadata 更新失败不应关闭正常 Agent 数据流；ACK 返回字段级错误。
- 重连后 Agent 必须重新上报完整快照，而不是只发送增量。
- session 关闭或租约过期后，Server 将 metadata 标记 stale，不删除最后一次安全值。

## 数据模型

新增唯一 DDL 中的表：

```sql
CREATE TABLE IF NOT EXISTS agent_runtime_metadata (
    agent_id VARCHAR(255) PRIMARY KEY,
    node_id VARCHAR(255) NOT NULL,
    epoch INTEGER NOT NULL,
    revision INTEGER NOT NULL,
    metadata TEXT NOT NULL,
    reported_at TEXT NOT NULL,
    last_seen_at TEXT NOT NULL,
    expires_at TEXT,
    stale INTEGER NOT NULL DEFAULT 0,
    updated_at TEXT NOT NULL
);
```

`metadata` 存储经过校验的 JSON 对象；不保存原始文件路径内容之外的凭据，不保存读取失败的原始错误。Repository 提供：

- `UpsertRuntimeMetadata(ctx, value)`：按 `(agent_id, epoch, revision)` 做 epoch fencing 和 revision 幂等。
- `GetRuntimeMetadata(ctx, agentID)`。
- `ListRuntimeMetadata(ctx, cursor, limit)`。
- `MarkRuntimeMetadataStale(ctx, agentID, epoch)`。

## API 与后台

新增 API：

```text
GET /api/v1/agents/{agentId}/metadata
GET /api/v1/agents/{agentId}/metadata?includeStale=true
```

响应继续使用 `{code,msg,data}`：

```json
{
  "code": 200,
  "msg": "OK",
  "data": {
    "agentId": "agent-devbox",
    "nodeId": "server-1",
    "epoch": 12,
    "revision": 4,
    "stale": false,
    "reportedAt": "2026-09-06T12:00:00Z",
    "updatedAt": "2026-09-06T12:00:00Z",
    "items": [
      {"name": "device_id", "source": "file", "value": "...", "redacted": false},
      {"name": "region", "source": "env", "value": "cn-east-1", "redacted": false}
    ]
  }
}
```

权限：

- 管理员可以读取所有 Agent metadata。
- 普通用户只能读取自己拥有的 Agent。
- 任何角色都不能通过 API 修改 Agent 上报值；修改必须发生在 Agent 配置和下一次上报中。
- OpenAPI 必须同步描述 metadata schema、权限错误、stale 语义和分页行为。

后台 Agent 详情页增加 Metadata 卡片：

- 当前在线/stale 状态。
- 最后上报时间、更新时间、epoch、revision。
- 字段名、来源类型、值；敏感字段默认遮罩。
- 上报字段缺失、读取失败和过期状态提示。
- 不提供直接编辑上报值的入口。

## SSH 访问模型

本期不新增 SSH 专用协议。复用已有 `proxy tcp`：

```bash
ssh \
  -o 'ProxyCommand=tunnelmesh-client proxy tcp --agent agent-devbox --target-host 127.0.0.1 --target-port 22' \
  devuser@host
```

执行远程命令：

```bash
ssh \
  -o 'ProxyCommand=tunnelmesh-client proxy tcp --agent agent-devbox --target-host 127.0.0.1 --target-port 22' \
  devuser@host 'uname -a && systemctl status my-service'
```

安全边界：

- 宿主机 sshd 仍负责用户认证和授权。
- “免密码”必须依赖 `authorized_keys`、ssh-agent 或 SSH certificate。
- Server policy 只允许目标 Agent、目标地址和 `tcp/22`。
- SSH 连接建立、关闭、用户、Agent、目标端口写入审计日志；不记录私钥和会话明文。
- 如果未来增加 command-exec，必须作为独立设计，具备命令白名单、RBAC、审批、PTY、超时、输出上限和审计。

## 可观测性与错误处理

- 指标：metadata 上报成功/拒绝/字段失败、当前 stale Agent 数量、SSH proxy 成功/失败数。
- 日志：包含 trace ID、agent_id、epoch、revision、字段数量和错误码，不记录敏感值。
- metadata 不可用不能阻断 Agent 的 TCP/UDP/HTTP 转发；SSH proxy 失败返回明确的 policy、session 或 target dial 错误。
- 远程命令退出码由 SSH 原样返回，TunnelMesh 不解释命令内容。

## 测试要求

- Metadata source：file/env、缺失字段、超长值、敏感字段、UTF-8 和读取错误。
- 协议：hello/update/ack、重复 revision、旧 epoch、超大 payload、重连全量快照。
- Repository：SQLite/MySQL upsert、fencing、stale、cursor 分页。
- API/RBAC：管理员、owner、无权用户、stale 查询和无法修改上报值。
- Web：Agent 详情 metadata 卡片和敏感值遮罩。
- SSH：TCP byte integrity、ssh-agent 公钥认证、远程命令退出码、policy 拒绝和断线清理。
