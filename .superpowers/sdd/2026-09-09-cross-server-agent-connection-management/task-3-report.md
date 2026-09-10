# Task 3 Report: Authenticated Relay Control RPC

## Implementation

- Added `relay.CloseAgentConnectionRequest`, `relay.CloseAgentConnectionFunc`, and the unary RPC `/tunnelmesh.relay.v1.Relay/CloseAgentConnection`.
- Added `GRPCNodeTransport.CloseAgentConnection` with `structpb.Struct` wire encoding.
- Added unary server and client authentication interceptors using the same server-node mTLS SAN, token, and node epoch checks as stream relay.
- Added `AgentConnectionLeaseController.CloseConnection`, which maps the registry lease epoch to the current local Agent session epoch before closing.
- Added `ServerRuntime.closeAgentConnection`, requiring an authenticated server-node principal whose node ID matches `RequestedByNodeID`.
- Wired relay-enabled runtimes with the unary auth interceptor and `NewRelayServerWithClose`.
- Mapped stale lease epochs to `FailedPrecondition` and missing sessions to `NotFound`.

## TDD Evidence

RED:

```bash
go test ./internal/server -run 'TestAgentConnectionLeaseControllerCloseConnection' -count=1
go test ./internal/server -run 'TestRuntimeCloseAgentConnectionRequiresAuthenticatedServerNode' -count=1
```

Initial results:

```text
controller.CloseConnection undefined
runtime.closeAgentConnection undefined
FAIL github.com/tunnelmesh/tunnelmesh/internal/server [build failed]
```

GREEN:

```bash
go test ./internal/relay -count=1
go test ./internal/server -run 'AgentRelay|CloseConnection' -count=1
go test ./internal/server -count=1
```

Results:

```text
ok github.com/tunnelmesh/tunnelmesh/internal/relay
ok github.com/tunnelmesh/tunnelmesh/internal/server
```

## Files Changed

- `internal/relay/service.go`
- `internal/relay/transport.go`
- `internal/relay/server_node_auth.go`
- `internal/relay/server_node_auth_test.go`
- `internal/relay/transport_test.go`
- `internal/server/agent_connection_registry.go`
- `internal/server/agent_connection_registry_test.go`
- `internal/server/agent_connection_control_test.go`
- `internal/server/runtime.go`

## Self-Review

- The cross-node request carries the durable registry lease epoch; the owning Server resolves it to the local protocol connection epoch.
- The relay transport applies a five-second handler timeout.
- Unary control requires the same authenticated server-node identity as stream relay.
- Runtime additionally rejects a mismatch between the authenticated principal and `RequestedByNodeID`.
- Stale or unauthorized requests do not close the live transport.
- Unexpected internal errors are deliberately returned as `Internal` without leaking controller details.
- No commit was created.
