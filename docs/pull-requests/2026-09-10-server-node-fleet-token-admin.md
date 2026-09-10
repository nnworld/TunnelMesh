# PR：Server 节点共享令牌与后台管理

## 用户可见变化

- `server_node` service token 支持 fleet 语义：`scope.serverNodeIds` 为空时允许所有启用、未逻辑删除的 Server 节点；非空时仅允许列表内节点。
- Server 继续从 `server.relay.node_token` 读取共享令牌；旧的单节点 `nodeId` 绑定不再作为认证依据。
- Server 缺少 `node.id` 时，`run` 自动生成小写 `server-<32位十六进制>` 身份并回写 YAML；已有 ID 或显式 CLI/环境变量覆盖时不会改写。
- Server 启动时自注册到 `server_nodes`，并每 30 秒心跳一次，租约有效期 90 秒。
- 管理后台新增管理员专属 `/servers` 页面，支持状态、负载、详情、启用/禁用、逻辑删除和恢复。
- Token 创建表单中 Client Token 的 Agent ID 改为多选框，支持名称/ID 模糊搜索；留空表示所有授权 Agent，选择多个生成 `scope.agentIds` 显式允许列表。
- Token 创建表单中 Server 节点改为多选；留空表示所有节点。
- Token 修改范围支持更新 Client Token 的 `scope.agentIds` 和 server_node Token 的 `scope.serverNodeIds`；列表绑定列同步展示允许列表，WebSocket 协议勾选值与后端规范值 `ws` 保持一致。
- `server.relay.endpoint` 留空时，Server 启动阶段根据 `server.relay.listen` 自动推导本机可路由地址；推导结果只写入进程内配置，不回写 YAML。
- Relay 支持两种模式：
  - **mTLS（生产推荐）**：`ca/cert/key/server_name` 全部配置，每个 Server 使用独立证书，并校验证书 SAN 和节点 epoch。
  - **明文（仅受控内网）**：`ca/cert/key/server_name` 全部省略，仍校验 server-node token、token scope、节点启用/未逻辑删除状态和 epoch。
- `ca/cert/key` 部分填写时启动失败，避免证书配置不完整导致意外降级。

## 数据库迁移

- Schema 版本从 7 升级到 8。
- `server_nodes` 新增：
  - `name VARBINARY(255) NOT NULL DEFAULT ''`，存量回填为节点 ID；
  - `enabled INTEGER/BIGINT NOT NULL DEFAULT 1`；
  - `deleted_at TEXT/VARCHAR(32)`，空值表示未删除。
- 新增 MySQL 与 SQLite 的 `v0007_to_v0008` 增量脚本，并同步更新全量 DDL。
- MySQL 脚本兼容 5.6，不新增超长索引，不改变既有索引长度。
- 预计锁表时间取决于 `server_nodes` 行数；该表为 Server 清单，通常远小于连接租约表。

## 升级步骤

1. 备份 MySQL/SQLite 数据库并验证可恢复。
2. 确认 `schema_meta.version=7`，且增量链完整。
3. 滚动停止或升级 Server；`storage.auto_init=true` 时自动执行 v7→v8。
4. 验证 `schema_meta.version=8`、`server_nodes` 新列和存量 `name` 回填。
5. 验证 `/health/ready`、管理 API、`/servers` 页面和 Agent 连接。
6. 为缺失 `node.id` 的节点执行 `init-node-id` 或启动一次服务；若使用 relay mTLS，按最终 SAN 签发证书。

## 回滚步骤

1. 停止新版 Server 写入。
2. 回退应用二进制到支持 Schema v7 的版本。
3. 保留 v8 新增列，不执行反向 DDL；旧版本会忽略这些列。
4. 若必须精确恢复 Schema，使用升级前备份恢复数据库。
5. 不要手工删除新列或修改 `schema_meta.version`。
6. Relay 行为回滚不依赖数据库：需要固定广播地址时显式填写 `server.relay.endpoint`；需要回到 mTLS 时完整填写 `ca/cert/key/server_name`。

## 运维初始化顺序

1. 安装不包含 `node.id` 的 `server.yaml`。
2. 执行 `tunnelmesh-server --config /etc/tunnelmesh/server.yaml init-node-id`，或启动 systemd 单元。
3. 读取回写后的 `node.id`。
4. 按该 ID 精确匹配证书 SAN，签发该节点独立 relay mTLS 证书；受控内网也可按证书文档选择明文模式。
5. 创建 fleet 或显式允许列表 token，并配置到每个节点的 `server.relay.node_token`。
6. 启动或重启 Server，确认 `/servers` 页面显示 online。

systemd 单元以 root `ExecStartPre` 执行 `init-node-id`，随后以 `tunnelmesh` 用户执行 `check-config` 和 `run`；`/etc/tunnelmesh` 可保持只读，`/var/lib/tunnelmesh` 保持可写。

### Relay 模式与 endpoint 推导

受控内网明文配置：

```yaml
server:
  relay:
    enabled: true
    listen: 0.0.0.0:9443
    # 可省略；启动时自动使用本机可用 IP 和 9443。
    endpoint: ""
    node_token: ""
```

生产推荐 mTLS 配置：

```yaml
server:
  relay:
    enabled: true
    listen: 0.0.0.0:9443
    endpoint: server-1.internal.example.com:9443
    ca: /etc/tunnelmesh/certs/relay-ca.pem
    cert: /etc/tunnelmesh/certs/server-1-relay.pem
    key: /etc/tunnelmesh/certs/server-1-relay-key.pem
    server_name: relay.internal.example.com
    node_token: ""
```

endpoint 推导规则：

1. `listen` 是具体 IP 时直接复用该 IP 和端口；
2. `listen` 是 `0.0.0.0`、`[::]` 或空 host 时，选择本机可用地址，跳过 down、loopback、link-local、broadcast、multicast 和 unspecified 地址；
3. 优先 IPv4，其次全局 IPv6，多个候选按字符串排序取第一个；
4. 找不到可用地址时启动失败，提示显式配置 `endpoint`。

安全边界：明文模式不提供传输加密、防窃听、防中间人篡改或证书级节点身份证明，只适合网络 ACL、主机安全和审计边界均已受控的环境。证书生成、校验和轮换步骤见 [Relay mTLS 证书生成与配置](../operations/relay-mtls.md)。

## 验证

以下命令均在 `/opt/app/workspace/TunnelMesh` 执行：

- `go test ./... -count=1`：通过。
- `go test -race ./...`：通过，其中 `internal/server` 用时约 194 秒。
- `go vet ./...`：通过。
- `git diff --check`：通过。
- `cd web && npm test -- --run`：通过，11 个测试文件、59 个用例。
- `cd web && npm run build`：通过，生产构建完成且无 chunk 大小告警。
- `./scripts/verify-web-embed.sh`：通过，`web/dist` 与 `internal/server/web_dist` 完全一致。
- `systemd-analyze verify deploy/systemd/tunnelmesh-server.service`：未执行。当前 macOS 环境没有 `systemd-analyze`，也未发现 Docker/Podman/nerdctl/VM 可用于 Linux 容器校验；已人工检查 unit 的 section、key/value、`ExecStartPre` 顺序、`+` root 前缀、非特权 `User/Group` 和 `ReadWritePaths`，但该检查不能替代真实 systemd 静态校验。需在 Linux 环境补跑。
- `CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags='-s -w' -o /tmp/tunnelmesh-server ./cmd/tunnelmesh-server`：通过。
- 明文 relay 配置 `check-config`：通过，输出 `configuration valid`。

验证过程中发现并修复以下问题：

- `tokenStatus` 仍按旧 `NodeID` 判断 `server_node` token 状态，导致 fleet token 被误判为 `unavailable`；已改为按 `scope.serverNodeIds` 判断，空列表表示可用，显式列表逐个校验启用、未删除且未过期。
- `TestHTTPProxyForwardConnectTunnelsTCP` 在 200 响应和后续隧道数据同包到达时丢弃 `bufio.Reader` 缓冲导致偶发挂起；已改为响应和隧道数据共用同一个 `bufio.Reader`，并以 20 次重复聚焦验证确认稳定。
- 对照 relay endpoint 计划时发现 broadcast 地址过滤和 wildcard listen 测试覆盖不足；已补充失败测试，并统一用 `net.IP.IsGlobalUnicast()` 过滤不可用地址。
- 证书模式判断已提取为 `relayTLSMode`，集中维护“全空明文、全填 mTLS、部分填写报错”的策略。
- Token scope 更新的第一版实现将资源校验放在 SQLite 写事务中，却通过独立连接读取 Agent/Server 节点，导致写锁等待死锁；已改为使用 `ServiceTokenAuthorizedMutationTransaction` 的事务绑定仓库，在授权读取和写入的同一事务内完成校验。
- 前端 WebSocket 复选框最初提交 `websocket`，而后端规范值并回显为 `ws`，导致重新打开表单时无法选中；已统一前端提交值和回显值为 `ws`。

## 计划复核

实现与计划一致：Schema v7→v8、fleet/explicit token scope、relay 节点校验、`node.id` 自动生成与回写、systemd 初始化顺序、Server 自注册与心跳、管理员专属 Server 节点 API、`/servers` 页面、Token 多选、relay endpoint 自动推导、明文/mTLS 双模式和文档均已覆盖。未发现功能范围偏差；唯一未完成项是本机无法执行真实 `systemd-analyze verify`，需在 Linux 环境补跑。
