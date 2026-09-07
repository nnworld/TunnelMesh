# Task 7 Report — Server-node relay Token + mTLS fencing

## Status

DONE_WITH_CONCERNS

Commits: none (未执行 commit/push/merge)。

## Design

- 增加 gRPC `StreamServerInterceptor`，在 `relayServer.OpenStream` 读取第一条 target struct 之前完成认证。
- 要求 TLS peer 为已验证 client certificate，TLS 1.2+，证书 DNS/IP/URI SAN 做完整 entry 精确匹配；DNS 比较大小写不敏感，拒绝 wildcard/CN fallback。
- incoming metadata 严格单值读取：`authorization: Bearer <raw>`、`x-tunnelmesh-node-id`、`x-tunnelmesh-node-epoch`。
- 使用 `CredentialService.ValidateAs(raw, TokenTypeServerNode)`，比较 `TokenIdentity.NodeID == caller node ID`；再从 `NodeRepository.Get(callerNodeID)` 检查节点存在、未过期及 epoch 精确匹配。
- 认证 caller identity 通过 `ServerNodePrincipal` 放入 context；首条 `StreamRequest.NodeID` 继续表示目标路由，不覆盖 caller identity。
- 增加 client stream interceptor，每条 stream 注入 caller metadata；增加 authenticated dial constructor，保留旧低层构造器供内部/测试使用。
- 增加 relay 专用 TLS helper、server runtime authenticated gRPC listener、runtime authenticated client dial path，以及独立 `server.relay` 配置区块和 CLI flags。

## RED / GREEN

### RED

先加入真实 loopback TLS/gRPC 测试后运行：

```text
go test ./internal/relay -run 'AuthenticatedGRPCRelay' -count=1
```

失败原因为功能缺失：`NewServerNodeStreamInterceptor` 与 `DialAuthenticatedGRPCNode` 未定义。

### GREEN

实现后以下 focused 测试通过：

```text
go test ./internal/relay -run 'AuthenticatedGRPCRelay|ServerNode' -count=1
go test ./internal/relay ./internal/server ./internal/config ./internal/cli -count=1
go test -race ./internal/relay -count=1
```

覆盖 valid mTLS + server_node Token、stream data、handler invocation、handler 未认证前调用、SAN mismatch、wildcard/CN fallback、wrong Token type、revoked Token、expired node、stale epoch、missing/duplicate metadata、client interceptor metadata 注入、TLS material 缺失校验及配置 precedence/redaction。

## Files

- `internal/relay/server_node_auth.go`：server/client interceptors、principal、metadata parser、SAN matcher、relay TLS policy。
- `internal/relay/server_node_auth_test.go`：真实 TLS/gRPC 认证测试及边界测试。
- `internal/relay/transport.go`：authenticated dial constructors、TLS client verification、保留 `CloseWrite`。
- `internal/config/config.go`：`server.relay` 模型、默认值、env binding、校验、redaction。
- `internal/config/config_test.go`：relay file/env/CLI precedence、redaction、fail-fast validation。
- `internal/cli/root.go`：relay CLI flags 与 server runtime wiring。
- `internal/server/runtime.go`：authenticated relay gRPC server/listener/client wiring、TLS material loading、生命周期关闭。

## Verification

```text
go test ./... -count=1                         PASS
go test -race ./...                           PASS
go vet ./...                                  PASS
git diff --check                              PASS
go test ./internal/relay -run 'AuthenticatedGRPCRelay|ServerNode' -count=1 PASS
go test ./internal/relay ./internal/server ./internal/config ./internal/cli -count=1 PASS
go test -race ./internal/relay -count=1       PASS
```

## Self-review

- Handler 不读取首条 target message 前，interceptor 已完成 TLS、metadata、Token、node、epoch 全部校验。
- Token raw 值只存在于 outgoing/incoming metadata 和认证调用，不进入 request payload、错误文本或日志。
- stale epoch 权威读取 `NodeRepository.Get(callerNodeID)`，不信任目标 `StreamRequest.NodeID` 或 metadata epoch 单独值。
- gRPC server TLS 使用 `RequireAndVerifyClientCert`、显式 ClientCAs、TLS 1.2+；client 要求 certificate、RootCAs、ServerName 且禁止 skip verify。
- runtime relay listener 与 gRPC server 在 `Close` 释放；本地 mode 默认不启用 relay listener。

## Concerns

- 旧低层 `RegisterRelayServer`/`DialGRPCNode` 仍可被直接调用；生产 runtime 已改为 authenticated path，旧构造器仅用于兼容内部/测试，后续可在 API 层进一步收窄可见性。
- runtime 当前按 `server.relay.listen` 建立单 listener；跨节点 endpoint 选择仍依赖上层 registry/路由编排，`DialRelayNode` 提供了无 unauthenticated fallback 的 authenticated client path。
- 配置校验验证 TLS material 路径为绝对路径和非空；证书内容/可读性在 runtime 启动加载时 fail-fast 校验。
