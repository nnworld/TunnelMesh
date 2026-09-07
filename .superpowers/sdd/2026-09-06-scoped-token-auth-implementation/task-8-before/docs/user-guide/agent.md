# tunnelmesh-agent 使用帮助

`tunnelmesh-agent` 部署在被访问的主机或内网中。它主动通过 TLS WebSocket 连接 Server，Server 和 `tunnelmesh-client` 不需要直接访问 Agent 所在网络。

## 1. 最小配置

```yaml
mode: local
agent:
  server_url: wss://tunnel.example.com/ws/agent/v1
  id: agent-devbox
# token is injected through the environment/Secret manager, not committed here
```

也可以使用环境变量：

```bash
export TUNNELMESH_MODE=local
export TUNNELMESH_AGENT_SERVER_URL=wss://tunnel.example.com/ws/agent/v1
export TUNNELMESH_AGENT_ID=agent-devbox
export TUNNELMESH_AGENT_TOKEN='api-token-from-secret-manager'
```

配置优先级为：命令行参数 > 环境变量 > 配置文件 > 默认值。

## 2. 检查身份和配置

打印 Agent ID：

```bash
tunnelmesh-agent --config agent.yaml id
```

验证配置：

```bash
tunnelmesh-agent --config agent.yaml check-config
```

Agent ID 是路由和客户端命令引用 Agent 的稳定标识。生产环境不要在多台主机上复用同一个 ID；若 ID 由设备信息生成，应在设备生命周期内保持不变。

## 3. 注册和运行

```bash
tunnelmesh-agent --config agent.yaml register
tunnelmesh-agent --config agent.yaml run
```

`register` 用于显式检查/登记 Agent；长期运行使用 `run`。Agent 启动后会主动连接 Server，并通过心跳维护在线状态。网络短暂中断时，客户端应重连并重新建立会话，不应依赖固定的 Server 入站连接。

## 4. Agent 提供的能力

Agent 负责从所在网络连接目标服务：

- TCP：连接内网 TCP 服务并转发有序字节流。
- UDP：连接内网 UDP 服务并保持 datagram 边界和源地址 association。
- HTTP：连接内网 HTTP 服务，支持请求转发和 WebSocket Upgrade。
- TCP-over-WebSocket：为 SSH 等原始 TCP 协议提供 binary WebSocket 字节桥。

Agent 不负责公开监听公网端口；公网入口由 Server 的 80/443 处理。目标主机、目标端口、CIDR 和端口白名单由 Server policy 约束。

## 5. 受控 metadata 上报

Agent 只读取配置中明确列出的文件字段或环境变量，不会扫描整个文件系统，也不会上传全部环境变量。示例：

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

`file` 必须使用绝对路径，`env` 必须指定单个变量名；字段名只能包含字母、数字、`.`、`_` 和 `-`。单个 Agent 最多上报 32 个字段，单字段最多 4 KiB，总 payload 最多 32 KiB。读取失败只标记该字段，不会中断 TCP、UDP 或 HTTP 转发。

名称包含 `password`、`token`、`secret`、`private_key` 或 `dsn` 的字段会被遮罩；管理 API 只返回 `redacted: true`，不会返回原值。Server 保存最新快照和 epoch/revision；会话过期后快照标记为 stale，管理 API 默认隐藏 stale 数据，使用 `includeStale=true` 才读取最后安全快照。

metadata 不支持命令执行、shell 插值或任意路径读取。需要执行命令时应使用宿主机已有的 SSH 认证和审计边界，参见 [SSH over WebSocket](tcp-over-websocket-ssh.md)。

## 6. 网络和 TLS 要求

Agent 主机必须满足：

1. 能解析并访问 Server 的 DNS 名称。
2. 能访问 Server 的 HTTPS/WSS 端口，通常是 443。
3. 系统时间准确，能够校验 Server 证书。
4. 能从 Agent 网络访问被代理的目标地址和端口。
5. 防火墙允许 Agent 到 Server 的出站连接以及到目标服务的内网连接。

生产环境使用 `wss://`，不要关闭证书校验或把 Server 证书私钥放在 Agent 主机上。

Agent WebSocket 握手还必须携带 `Authorization: Bearer <token>`。Server 使用同一套 API token 校验该凭据；无效或缺失 token 的连接会在 metadata hello 前被拒绝。请通过 Secret 管理系统注入 token，不要写入仓库或命令行历史。

Token 还必须属于该 Agent 的 owner；管理员 token 可用于运维接管。被禁用或不存在的 Agent ID 会被拒绝。Agent 断线后会以带抖动的指数退避自动重连，并在每次连接发送新的完整 metadata hello。
在线连接默认每 1 分钟发送一次协议级 `PING`；Server 返回 `PONG` 并续期 metadata 租约（默认 5 分钟）。因此 metadata 内容不变时也不会被误标为 stale；只有连接断开、心跳停止或 epoch 被新连接替换后才会进入 stale 状态。

## 7. Docker 运行

```bash
docker build --build-arg APP=agent -t tunnelmesh:agent .
docker run --rm \
  -e TUNNELMESH_MODE=local \
  -e TUNNELMESH_AGENT_SERVER_URL=wss://tunnel.example.com/ws/agent/v1 \
  -e TUNNELMESH_AGENT_ID=agent-devbox \
  tunnelmesh:agent run
```

Agent 容器通常不需要暴露端口。它只需要出站访问 Server 和目标内网服务；在 Kubernetes 或 Compose 中使用 Secret 注入证书、Token 等敏感配置。

## 8. 故障排查

### `check-config` 失败

检查 `mode`、`agent.server_url`、`agent.id` 和 `agent.token`，并确认配置文件格式正确。使用 `--config` 指向实际挂载路径；token 只通过环境变量或 Secret manager 注入。

### Agent 一直离线

- 用 `curl` 或 TLS 工具确认 Server 的 443 可达。
- 检查 WSS 路径是否为 `/ws/agent/v1`，以及反向代理是否透传 Upgrade。
- 检查证书的域名、系统时间和 CA 信任链。
- 检查 Agent ID 是否与另一台在线主机重复。
- 查看 Server 的 Agent lease、heartbeat 和认证日志。

### 目标服务不可达

在 Agent 主机上直接测试目标连接，例如：

```bash
nc -vz db.internal 5432
nc -vzu 10.0.0.53 53
```

如果本机可达但 TunnelMesh 仍被拒绝，检查 Server policy 的 target host、target port、CIDR 和协议配置。

## 9. 安全建议

- Agent 配置文件权限设置为 `0600`，服务使用独立系统用户运行。
- 不要把 Token、私钥、MySQL DSN 或完整配置提交到 Git。
- 只授予必要的目标网段和端口 policy。
- 发生 Agent 凭据泄露时立即在管理后台撤销并重新注册。
