### Task 1: Agent Connection Lease Lifecycle

**Files:**

- Create: `internal/server/agent_connection_registry.go`
- Create: `internal/server/agent_connection_registry_test.go`
- Modify: `internal/server/runtime.go`
- Modify: `internal/server/session_manager.go`
- Modify: `internal/server/ws_agent.go`
- Test: `internal/server/runtime_test.go`

**Interfaces:**

- Produces:

```go
type AgentConnectionLeaseController struct{}

func NewAgentConnectionLeaseController(
    reg registry.NodeRegistry,
    manager *AgentSessionManager,
    localRelay *AgentRelayTransport,
    serverNodeID string,
    ttl time.Duration,
) *AgentConnectionLeaseController

func (c *AgentConnectionLeaseController) Register(
    ctx context.Context, session *AgentSession,
) (registry.NodeOwner, error)

func (c *AgentConnectionLeaseController) Heartbeat(
    ctx context.Context, owner registry.NodeOwner, session *AgentSession,
) error

func (c *AgentConnectionLeaseController) Release(
    ctx context.Context, owner registry.NodeOwner,
) error
```

- `AgentSessionConfig` gains:

```go
HeartbeatCallback func(context.Context, *AgentSession) error
```

- [ ] **Step 1: Write failing lifecycle tests**

Create tests for:

```go
func TestAgentConnectionLeaseControllerRegistersRenewsAndReleases(t *testing.T)
func TestAgentConnectionLeaseControllerHeartbeatUpdatesActiveStreams(t *testing.T)
func TestAgentConnectionLeaseControllerReleaseIsFenced(t *testing.T)
func TestRuntimeRejectsAgentConnectionWhenLeaseRegistrationFails(t *testing.T)
```

Assertions must verify:

```go
owner := controller.Register(ctx, session)
owner.AgentID == session.AgentID
owner.ConnectionID == session.ConnectionID
owner.ConnectionEpoch == session.ConnectionEpoch
owner.ServerNodeID == serverNodeID

controller.Heartbeat(ctx, owner, session)
activeLeases, _ := db.Leases().ListActiveByAgent(ctx, session.AgentID)
activeLeases[0].ActiveStreams == localRelay.ActiveStreams(session.AgentID, session.ConnectionID)

controller.Release(ctx, owner)
remaining, _ := db.Leases().ListActiveByAgent(ctx, session.AgentID)
len(remaining) == 0
```

- [ ] **Step 2: Run failing tests**

Run:

```bash
go test ./internal/server -run 'TestAgentConnectionLeaseController|TestRuntimeRejectsAgentConnectionWhenLeaseRegistrationFails' -count=1
```

Expected result: compile failure because `AgentConnectionLeaseController` does not exist.

- [ ] **Step 3: Implement the lease controller**

Implementation requirements:

- `Register` calls `registry.Register` with `NodeID` and `ServerNodeID` both set to the local Server node ID.
- TTL defaults to 90 seconds.
- `Heartbeat` uses a one-second bounded context, calls `KeepAlive`, then `UpdateConnectionStats`.
- `Release` calls `Revoke` with the exact owner epoch.
- `AgentSessionManager.Register` does not own registry persistence.
- Runtime registers the lease after local session registration succeeds and rejects the WebSocket if cluster lease registration fails.
- `serveRegisteredAgentSessionWithMetrics` invokes `HeartbeatCallback` for `PING` and `PONG` after refreshing metadata.
- Runtime defer releases the exact owner after the Agent read loop exits.

- [ ] **Step 4: Verify task**

Run:

```bash
go test ./internal/server -run 'TestAgentConnectionLeaseController|TestRuntimeRejectsAgentConnectionWhenLeaseRegistrationFails' -count=1
go test ./internal/server -count=1
```

Expected result: all selected and package tests pass.
