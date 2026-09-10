### Task 3: Authenticated Relay Control RPC

**Files:**

- Modify: `internal/relay/transport.go`
- Modify: `internal/relay/transport_test.go`
- Modify: `internal/relay/server_node_auth_test.go`
- Modify: `internal/server/runtime.go`
- Test: `internal/server/agent_relay_transport_test.go`

**Interfaces:**

- Produces in `internal/relay`:

```go
type CloseAgentConnectionRequest struct {
    AgentID          string
    ConnectionID     string
    ConnectionEpoch  int64
    RequestedByNodeID string
}

type CloseAgentConnectionFunc func(context.Context, CloseAgentConnectionRequest) error

func (n *GRPCNodeTransport) CloseAgentConnection(
    ctx context.Context, req CloseAgentConnectionRequest,
) error
```

- Server registration accepts a `CloseAgentConnectionFunc`.
- The unary RPC path is `/tunnelmesh.relay.v1.Relay/CloseAgentConnection`.

- [ ] **Step 1: Write failing relay tests**

Add tests for:

```go
func TestRelayCloseAgentConnectionRequiresServerNodeAuth(t *testing.T)
func TestRelayCloseAgentConnectionUsesExactEpoch(t *testing.T)
func TestGRPCNodeTransportCloseAgentConnectionPropagatesFailure(t *testing.T)
```

Required assertions:

```go
err := client.CloseAgentConnection(ctx, relay.CloseAgentConnectionRequest{
    AgentID: "agent-a", ConnectionID: "conn-a", ConnectionEpoch: 9,
})
status.Code(err) == codes.PermissionDenied

closeCalls[0].ConnectionEpoch == 9
status.Code(staleErr) == codes.FailedPrecondition
```

- [ ] **Step 2: Run failing tests**

Run:

```bash
go test ./internal/relay -run 'TestRelayCloseAgentConnection|TestGRPCNodeTransportCloseAgentConnection' -count=1
```

Expected result: compile failure because the control request and RPC do not exist.

- [ ] **Step 3: Implement the unary RPC**

Implementation requirements:

- Add a unary service descriptor alongside `OpenStream`.
- Add a server-node unary authentication interceptor using the same identity and token validation as the stream interceptor.
- Encode the request as a protobuf `Struct` using snake_case metadata keys.
- Apply a five-second server-side context timeout.
- Runtime installs a handler that calls `AgentSessionManager.CloseConnection`.
- Map `ErrEpoch` to `codes.FailedPrecondition` and missing sessions to `codes.NotFound`.
- Keep `GRPCNodeTransport.Close()` unchanged.

- [ ] **Step 4: Verify task**

Run:

```bash
go test ./internal/relay -count=1
go test ./internal/server -run 'AgentRelay|CloseConnection' -count=1
```

Expected result: all relay and related server tests pass.
