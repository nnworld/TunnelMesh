# Relay Endpoint 自动推导与明文模式实施计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**目标：** `server.relay.enabled: true` 时允许省略 `endpoint` 并自动使用本机可用 IP，同时允许省略 `ca/cert/key` 以运行明文 relay。

**架构：** 配置加载阶段在内存中推导 relay 广播地址，不回写 YAML。relay 证书字段采用“全空为明文、全填为 mTLS、部分填写为配置错误”的规则；明文模式仍强制 server-node token、节点启用状态和 epoch 校验。运行时根据是否存在完整 mTLS 配置选择 gRPC TLS 或 insecure transport。

**技术栈：** Go、gRPC、Viper、SQLite/MySQL、Markdown。

**Spec：** 本计划包含已确认的行为设计；实施时以本文件为唯一规格来源。

## 全局约束

- `server.relay.enabled: false` 时完全不校验 endpoint 和证书字段。
- `server.relay.enabled: true` 且 `endpoint` 为空时，根据 `server.relay.listen` 推导 endpoint。
- 推导结果只写入进程内有效配置，不回写 `/etc/tunnelmesh/server.yaml`。
- `listen` 的端口必须保留；`0.0.0.0`、`[::]` 或空 host 会替换为本机可用 IP。
- `listen` 如果已经是具体 IP，则直接使用该 IP 和端口。
- 选择本机 IP 时跳过 down 接口、loopback、link-local、broadcast 和 multicast 地址；优先 IPv4，其次全局 IPv6；多个候选时按字符串排序取第一个。
- 找不到可用地址时返回明确错误，提示多网卡或特殊网络环境应显式配置 endpoint。
- `ca/cert/key` 三者全空表示明文 relay。
- `ca/cert/key` 三者全填且 `server_name` 非空表示 mTLS relay；路径必须为绝对路径。
- 三者部分填写是配置错误，避免意外降级。
- 明文模式仍必须配置 `server.relay.node_token`。
- 明文模式仍校验 token 类型、token scope、节点启用/未删除状态和 epoch。
- 明文模式不校验证书 SAN；文档必须明确该模式只适合受控内网。
- 不执行 commit/push/merge。

---

### 任务 1：配置推导与校验

**文件：**

- 修改：`internal/config/config.go`
- 测试：`internal/config/config_test.go`

**接口：**

- 新增导出函数：
  ```go
  func InferRelayEndpoint(listen string) (string, error)
  ```
- 新增内部函数：
  ```go
  func selectRelayAdvertiseIP(addrs []net.Addr) (net.IP, error)
  func relayTLSMode(cfg RelayConfig) (useTLS bool, err error)
  ```

- [x] **步骤 1：编写失败测试**

在 `internal/config/config_test.go` 增加以下测试：

```go
func TestInferRelayEndpointUsesListenHostAndPort(t *testing.T) {
	endpoint, err := config.InferRelayEndpoint("10.1.2.3:9443")
	if err != nil {
		t.Fatal(err)
	}
	if endpoint != "10.1.2.3:9443" {
		t.Fatalf("endpoint = %q, want 10.1.2.3:9443", endpoint)
	}
}

func TestSelectRelayAdvertiseIPSkipsUnusableAddresses(t *testing.T) {
	// 构造 net.Interface{Flags: net.FlagUp, Addrs: ...} 不容易直接表达，
	// 测试通过 selectRelayAdvertiseIP 的入参接口结构注入地址列表。
	// 该函数使用同包测试，直接断言候选选择和错误语义。
}
```

同包测试需要覆盖：

- 具体 listen IP 直接复用。
- `0.0.0.0:9443` 推导为 `<本机IPv4>:9443`。
- `:9443` 推导为 `<本机IPv4>:9443`。
- loopback、link-local、down 接口被跳过。
- 无可用地址返回包含 “no usable relay advertise address” 的错误。

再增加配置行为测试：

```go
func TestLoadInfersRelayEndpointAndAcceptsPlaintext(t *testing.T) {
	// 写入临时 YAML：
	// mode: cluster
	// storage.mysql.dsn: mysql://db
	// server.relay.enabled: true
	// server.relay.listen: 127.0.0.1:9443
	// server.relay.node_token: secret
	// endpoint/ca/cert/key/server_name 全部省略。
	//
	// Load 成功后断言 Endpoint == "127.0.0.1:9443"。
}
```

测试还应覆盖：

- mTLS 三字段全填且 `server_name` 非空时校验通过。
- `ca/cert/key` 只填一个时返回配置错误。
- 三字段全填但 `server_name` 为空时返回配置错误。
- 明文模式缺少 `node_token` 仍失败。
- `relay.enabled: false` 时不推导 endpoint、不校验证书。

- [x] **步骤 2：运行聚焦测试确认失败**

```bash
go test ./internal/config -run 'Test(InferRelayEndpoint|SelectRelayAdvertiseIP|LoadInfersRelayEndpoint|ValidateRelay)' -count=1
```

预期：`InferRelayEndpoint` 和新行为不存在，编译失败或测试失败。

- [x] **步骤 3：实现最小配置逻辑**

实现要点：

1. 在 `Load` 中、调用 `Validate` 前处理 relay endpoint：
   ```go
   if cfg.Server.Relay.Enabled && strings.TrimSpace(cfg.Server.Relay.Endpoint) == "" {
       if strings.TrimSpace(cfg.Server.Relay.Listen) != "" {
           endpoint, err := InferRelayEndpoint(cfg.Server.Relay.Listen)
           if err != nil {
               return Config{}, err
           }
           cfg.Server.Relay.Endpoint = endpoint
       }
   }
   ```
2. `InferRelayEndpoint` 使用 `net.SplitHostPort` 提取端口和 host。
3. host 为空、`0.0.0.0` 或 `::` 时调用 `selectRelayAdvertiseIP(net.Interfaces())`。
4. `selectRelayAdvertiseIP` 跳过未 up、loopback、link-local、broadcast、multicast 地址，优先 IPv4。
5. 修改 `validateRelay`：
   - endpoint 仍必填，但 `Load` 已自动填充；
   - 调用 `relayTLSMode` 判断明文或 mTLS；
   - 明文时跳过证书路径和 `server_name` 校验；
   - mTLS 时保留现有绝对路径、`server_name` 和 token 校验。

- [x] **步骤 4：运行聚焦测试**

```bash
go test ./internal/config -count=1
```

预期：全部通过。

### 任务 2：relay 认证支持明文

**文件：**

- 修改：`internal/relay/server_node_auth.go`
- 测试：`internal/relay/server_node_auth_test.go`

**接口：**

- 保留现有构造函数，默认继续要求 mTLS，避免破坏既有调用方。
- 新增显式模式构造函数：
  ```go
  func NewServerNodeStreamInterceptorWithMode(
      credentialsService *auth.CredentialService,
      nodes storage.NodeRepository,
      metrics *observability.Metrics,
      requireMTLS bool,
  ) grpc.StreamServerInterceptor

  func NewServerNodeUnaryInterceptorWithMode(
      credentialsService *auth.CredentialService,
      nodes storage.NodeRepository,
      requireMTLS bool,
  ) grpc.UnaryServerInterceptor
  ```
- 内部认证函数增加 `requireMTLS bool` 参数。

- [x] **步骤 1：编写失败测试**

在 `internal/relay/server_node_auth_test.go` 增加明文集成测试：

```go
func TestServerNodeAuthPlaintextStillValidatesTokenNodeAndEpoch(t *testing.T) {
	// 准备 SQLite、启用 node-a、创建 scope.serverNodeIds 为空的
	// server_node token，并写入 epoch=7。
	// 使用 grpc.NewServer(grpc.ChainStreamInterceptor(
	//     NewServerNodeStreamInterceptorWithMode(credentials, nodes, nil, false),
	// )) 启动明文 gRPC。
	// 使用 DialAuthenticatedGRPCNode(ctx, addr, "node-a", 7, secret, nil)
	// 建立连接并打开流。
	// 断言成功。
}
```

同时覆盖：

- 明文模式下错误 token 被拒绝。
- 明文模式下 scope 不包含 node-a 时被拒绝。
- 明文模式下 node-a 被禁用或逻辑删除时被拒绝。
- 明文模式下 epoch 不匹配时被拒绝。
- mTLS 模式构造函数仍执行证书 SAN 精确匹配。

- [x] **步骤 2：运行聚焦测试确认失败**

```bash
go test ./internal/relay -run 'TestServerNodeAuthPlaintext' -count=1
```

预期：新构造函数不存在，编译失败。

- [x] **步骤 3：实现认证模式**

实现要点：

1. `authenticateServerNode` 在 `requireMTLS=true` 时执行：
   - `verifiedPeerCertificate`
   - `certificateHasExactSAN`
2. `requireMTLS=false` 时跳过证书校验，但继续执行：
   - metadata 中 node ID、epoch、token 解析
   - `CredentialService.ValidateAs(..., TokenTypeServerNode)`
   - token scope 校验
   - 节点存在、启用、未删除校验
   - epoch 校验
3. 现有 `NewServerNodeStreamInterceptor`、`NewServerNodeUnaryInterceptor` 和带 metrics 的包装函数调用新 mode 构造函数并传 `true`。

- [x] **步骤 4：运行聚焦测试**

```bash
go test ./internal/relay -count=1
```

预期：全部通过。

### 任务 3：gRPC 客户端支持明文

**文件：**

- 修改：`internal/relay/transport.go`
- 测试：`internal/relay/server_node_auth_test.go`

**接口：**

修改现有函数行为：

```go
func DialAuthenticatedGRPCNode(
    ctx context.Context,
    endpoint, nodeID string,
    epoch int64,
    rawToken string,
    cfg *tls.Config,
) (*GRPCNodeTransport, error)
```

`cfg == nil` 表示显式明文；非 nil 时保持现有 mTLS 强校验。

- [x] **步骤 1：编写失败测试**

在明文集成测试中使用：

```go
client, err := relay.DialAuthenticatedGRPCNode(ctx, address, "node-a", 7, secret, nil)
```

并增加低层测试：

```go
func TestDialAuthenticatedGRPCNodeRejectsIncompleteTLSConfig(t *testing.T) {
	_, err := relay.DialAuthenticatedGRPCNode(
		context.Background(), "127.0.0.1:1", "node-a", 1, "secret",
		&tls.Config{ServerName: "relay.local"},
	)
	if err == nil || !strings.Contains(err.Error(), "mTLS") {
		t.Fatalf("err = %v, want incomplete mTLS error", err)
	}
}
```

- [x] **步骤 2：运行聚焦测试确认失败**

```bash
go test ./internal/relay -run 'TestDialAuthenticatedGRPCNode' -count=1
```

预期：`cfg == nil` 当前被拒绝。

- [x] **步骤 3：实现明文 dial**

实现要点：

1. `cfg == nil` 时使用：
   ```go
   grpc.WithTransportCredentials(insecure.NewCredentials())
   ```
2. `cfg != nil` 时保留现有证书、RootCA、ServerName、TLS 1.2+ 和非 InsecureSkipVerify 校验。
3. 两种模式都附加现有 node ID、epoch 和 token metadata interceptor。
4. 更新类型注释，明确 `nil` 是显式明文，而不是遗漏 TLS。

- [x] **步骤 4：运行聚焦测试**

```bash
go test ./internal/relay -count=1
```

预期：全部通过。

### 任务 4：Server runtime 接入明文模式

**文件：**

- 修改：`internal/server/runtime.go`
- 测试：`internal/server/runtime_test.go`
- 测试：`internal/server/server_node_lifecycle_test.go`

**接口：**

- 内部函数签名改为：
  ```go
  func loadRelayServerTLS(cfg config.RelayConfig) (*tls.Config, error)
  func loadRelayClientTLS(cfg config.RelayConfig) (*tls.Config, error)
  ```
  三字段全空时返回 `(nil, nil)`。

- [x] **步骤 1：编写失败测试**

增加 runtime 测试：

```go
func TestNewServerRuntimeStartsPlaintextRelayWhenCertificatesOmitted(t *testing.T) {
	// 使用 SQLite 和 NodeID。
	// Relay 配置：
	// Enabled: true
	// Listen: "127.0.0.1:0"
	// Endpoint: "127.0.0.1:9443"
	// NodeToken: "secret"
	// CA/Cert/Key/ServerName 为空。
	// 断言 NewServerRuntime 成功，且 relayListener 非 nil。
}
```

增加 dial 测试：

```go
func TestRuntimeDialRelayNodeUsesPlaintextWhenCertificatesOmitted(t *testing.T) {
	// 启动明文 relay listener 和 runtime。
	// 调用 runtime.DialRelayNode(ctx, listener.Addr().String(), epoch)。
	// 断言连接成功。
}
```

同时覆盖：

- 三字段部分填写时 runtime 创建失败。
- mTLS 完整配置仍加载证书。
- 自动推导后的 endpoint 传给 `ServerNodeLifecycle` 并写入 `server_nodes.address`。

- [x] **步骤 2：运行聚焦测试确认失败**

```bash
go test ./internal/server -run 'Test(NewServerRuntimeStartsPlaintextRelay|RuntimeDialRelayNodeUsesPlaintext)' -count=1
```

预期：当前 `loadRelayServerTLS` 因证书为空失败。

- [x] **步骤 3：实现 runtime 接入**

实现要点：

1. `loadRelayServerTLS` 和 `loadRelayClientTLS`：
   - 证书三字段全空返回 `(nil, nil)`；
   - 全填时加载现有 CA、证书和私钥；
   - 部分填写返回配置错误。
2. `NewServerRuntime` 中：
   ```go
   tlsConfig, err := loadRelayServerTLS(runtimeConfig.Relay)
   if err != nil { ... }

   options := []grpc.ServerOption{
       grpc.ChainStreamInterceptor(
           relay.NewServerNodeStreamInterceptorWithMode(
               credentialService, db.Nodes(), runtime.metrics, tlsConfig != nil,
           ),
       ),
       grpc.ChainUnaryInterceptor(
           relay.NewServerNodeUnaryInterceptorWithMode(
               credentialService, db.Nodes(), tlsConfig != nil,
           ),
       ),
   }
   if tlsConfig != nil {
       options = append(options, grpc.Creds(credentials.NewTLS(tlsConfig)))
   }
   runtime.relayServer = grpc.NewServer(options...)
   ```
3. `DialRelayNode` 中：
   - `loadRelayClientTLS` 返回 nil 时传 nil 给 `DialAuthenticatedGRPCNode`；
   - 非 nil 时保持现有 mTLS 校验。
4. 确认 `ServerNodeLifecycle` 使用 `runtimeConfig.Relay.Endpoint`，自动推导值会注册到数据库。

- [x] **步骤 4：运行聚焦测试**

```bash
go test ./internal/server -run 'Relay|ServerNodeLifecycle' -count=1
```

预期：全部通过。

### 任务 5：文档与 PR 说明

**文件：**

- 修改：`docs/operations/config-examples.md`
- 修改：`docs/operations/configuration.md`
- 修改：`docs/operations/connection-pool.md`
- 修改：`docs/operations/troubleshooting.md`
- 新增：`docs/operations/relay-mtls.md`
- 修改：`docs/pull-requests/2026-09-10-server-node-fleet-token-admin.md`

- [x] **步骤 1：更新配置示例**

新增两种示例：

```yaml
# 受控内网明文 relay
server:
  relay:
    enabled: true
    listen: 0.0.0.0:9443
    # 可省略；启动时自动使用本机可用 IP 和 9443。
    endpoint: ""
    node_token: ""
```

```yaml
# 生产推荐 mTLS relay
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

文档必须写明：

- endpoint 自动推导不回写 YAML。
- 多网卡环境建议显式配置 endpoint。
- 明文模式省略 `ca/cert/key/server_name`。
- 明文模式仍需要 node token。
- 明文模式不提供传输加密和证书级节点身份证明，只适合受控内网。
- 生产环境推荐 mTLS。

新增 `docs/operations/relay-mtls.md`，提供完整证书生成与配置说明：

1. 生成 relay CA：
   - 使用 `openssl genpkey` 生成 CA 私钥。
   - 使用 `openssl req -x509` 生成自签 CA 证书。
   - 设置 CA `basicConstraints=critical,CA:TRUE` 和 `keyUsage=critical,keyCertSign,cRLSign`。
2. 初始化 `node.id` 并读取最终节点 ID。
3. 为每个 Server 节点生成独立私钥和 CSR。
4. 签发节点证书：
   - SAN 必须包含最终 `node.id`。
   - 多节点互通时，同一批节点证书还需要包含共同的 `server_name`，例如 `relay.internal.example.com`。
   - 设置 `extendedKeyUsage=serverAuth,clientAuth`，支持 gRPC 双向认证。
   - 设置合理的 `notBefore`、`notAfter`、序列号和Subject。
5. 安装权限：
   - CA 和证书可读。
   - 私钥仅 `tunnelmesh` 用户或 root 可读。
   - 不提交私钥。
6. 配置 `server.relay.ca/cert/key/server_name`。
7. 校验证书：
   - `openssl verify -CAfile relay-ca.pem server-1-relay.pem`。
   - `openssl x509 -text` 检查 SAN 和 EKU。
8. 轮换和回滚：
   - 新 CA 先分发到所有节点。
   - 新节点证书逐台滚动替换。
   - 保留旧证书直到所有节点完成切换。
   - 出现问题可回退到旧证书文件。

- [x] **步骤 2：更新 PR 说明**

在现有 PR 文档中追加：

- 用户可见变化。
- 明文模式安全边界和适用条件。
- endpoint 推导规则。
- 配置示例。
- 回滚方式：显式填写 endpoint 和完整 mTLS 字段即可回到 mTLS。

- [x] **步骤 3：文档自检**

运行：

```bash
rg -n "endpoint|明文|mTLS" docs/operations/config-examples.md docs/operations/configuration.md docs/operations/connection-pool.md docs/operations/troubleshooting.md
```

确认没有遗留“relay 必须配置 ca/cert/key”的绝对表述。

### 任务 6：完整验证

- [x] **步骤 1：Go 验证**

```bash
go test ./... -count=1
go test -race ./...
go vet ./...
git diff --check
```

- [x] **步骤 2：配置验证**

用文档中的明文示例生成临时 YAML，执行：

```bash
go run ./cmd/tunnelmesh-server --config /tmp/tunnelmesh-plaintext-relay.yaml check-config
```

预期输出：

```text
configuration valid
```

- [x] **步骤 3：对照计划复核**

逐项检查本计划要求，并在 PR 说明中记录实际结果和偏差。

## 自检

- endpoint 推导、明文 relay、token 认证、节点状态、epoch、runtime、文档和测试均已覆盖。
- 明文模式是显式安全取舍，已在计划中写明适用边界。
- 不包含 commit/push 步骤，符合项目 Git 规范。
