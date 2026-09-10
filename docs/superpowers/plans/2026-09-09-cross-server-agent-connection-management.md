# Cross-Server Agent Connection Management Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Provide cluster-wide Agent connection query and exact-fenced close from the admin UI.

**Architecture:** Register every physical Agent WebSocket as a cluster lease, keep the process-local session manager as the only execution authority, and add an authenticated mTLS relay control RPC so one Server node can ask another to close its local connection. The management API merges lease and local state and enforces owner/admin RBAC.

**Tech Stack:** Go, MySQL/SQLite storage repositories, etcd-compatible `NodeRegistry`, gRPC/mTLS relay, Vue 3, TypeScript, Element Plus.

**Spec:** `docs/superpowers/specs/2026-09-09-cross-server-agent-connection-management-design.md`

## Global Constraints

- Work directly in `/opt/app/workspace/TunnelMesh`; do not use a worktree.
- Preserve all existing uncommitted changes; do not reset, revert, commit, push, or merge.
- Use TDD: write each failing test before its implementation.
- No database schema change is allowed in this feature.
- Management response envelope remains `{ code, msg, data }`.
- All authorization is server-side and owner-checked.
- Logs and audits must not contain tokens, raw credentials, target addresses, or stream payloads.

---

### Task 1: Agent Connection Lease Lifecycle ✅

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

### Task 2: Exact-Fenced Local Connection Close ✅

**Files:**

- Modify: `internal/server/session_manager.go`
- Test: `internal/server/session_test.go`

**Interfaces:**

- Produces:

```go
func (m *AgentSessionManager) CloseConnection(
    agentID, connectionID string, connectionEpoch int64,
) error
```

- [ ] **Step 1: Write failing tests**

Add:

```go
func TestAgentSessionManagerCloseConnectionSendsGoAway(t *testing.T)
func TestAgentSessionManagerCloseConnectionRejectsStaleEpoch(t *testing.T)
func TestAgentSessionManagerCloseConnectionDoesNotAffectSiblingConnection(t *testing.T)
```

Required assertions:

```go
err := manager.CloseConnection("agent-a", "conn-a", 7)
errors.Is(err, ErrEpoch) == false
transport.closed == true
lastFrame.Type == protocol.FrameGoAway

err = manager.CloseConnection("agent-a", "conn-a", 6)
errors.Is(err, ErrEpoch) == true
replacementTransport.closed == false
```

- [ ] **Step 2: Run failing tests**

Run:

```bash
go test ./internal/server -run 'TestAgentSessionManagerCloseConnection' -count=1
```

Expected result: compile failure because `CloseConnection` does not exist.

- [ ] **Step 3: Implement exact close**

Implementation requirements:

- Resolve the session by Agent ID and connection ID.
- Reject a mismatched `connectionEpoch` with `ErrEpoch`.
- Mark the session closing, drain queued frames, send `GOAWAY`, close the transport, and return the transport error.
- Do not remove the map entry directly; let the existing read-loop cleanup call `RemoveSession` so replacement fencing remains intact.

- [ ] **Step 4: Verify task**

Run:

```bash
go test ./internal/server -run 'TestAgentSessionManagerCloseConnection' -count=1
go test ./internal/server -count=1
```

Expected result: all tests pass.

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

### Task 4: Cluster-Wide Query and Close API

**Files:**

- Create: `internal/server/agent_connection_api.go`
- Create: `internal/server/agent_connection_api_test.go`
- Modify: `internal/server/api.go`
- Modify: `docs/api/openapi.yaml`

**Interfaces:**

- Produces:

```go
type AgentConnectionClusterView struct {
    AgentID           string    `json:"agentId"`
    InstanceID        string    `json:"instanceId"`
    ConnectionID      string    `json:"connectionId"`
    ConnectionEpoch   int64     `json:"connectionEpoch"`
    ServerNodeID      string    `json:"serverNodeId"`
    ServerNodeEpoch   int64     `json:"serverNodeEpoch"`
    ServerNodeAddress string    `json:"serverNodeAddress"`
    Healthy           bool      `json:"healthy"`
    ActiveStreams     int       `json:"activeStreams"`
    HealthScore       int64     `json:"healthScore"`
    LastHeartbeatAt   time.Time `json:"lastHeartbeatAt"`
    LeaseExpiresAt    time.Time `json:"leaseExpiresAt"`
    Local             bool      `json:"local"`
}

type AgentConnectionCloseService interface {
    Close(ctx context.Context, agentID, connectionID string, epoch int64) error
}
```

- Routes:
  - `GET /api/v1/agents/{agentId}/connections`
  - `DELETE /api/v1/agents/{agentId}/connections/{connectionId}?connectionEpoch={epoch}`

- [ ] **Step 1: Write failing API tests**

Add:

```go
func TestAgentConnectionsListMergesLocalAndRemoteLeases(t *testing.T)
func TestAgentConnectionsListEnforcesOwnership(t *testing.T)
func TestAgentConnectionsCloseLocalExactConnection(t *testing.T)
func TestAgentConnectionsCloseRemoteThroughControlService(t *testing.T)
func TestAgentConnectionsCloseRejectsStaleEpoch(t *testing.T)
func TestAgentConnectionsCloseIsIdempotent(t *testing.T)
func TestAgentConnectionsCloseWritesAudit(t *testing.T)
```

Required assertions:

```go
response.Code == http.StatusOK
len(data.Connections) == 2
data.Connections[0].Local == true
data.Connections[1].ServerNodeID == "server-b"

remoteService.closeRequests[0].ConnectionID == "conn-remote"
remoteService.closeRequests[0].ConnectionEpoch == 11

audit.Action == "agent.connection.closed_by_admin"
```

- [ ] **Step 2: Run failing tests**

Run:

```bash
go test ./internal/server -run 'TestAgentConnections' -count=1
```

Expected result: compile or 404 failure because the routes do not exist.

- [ ] **Step 3: Implement the API**

Implementation requirements:

- Resolve and authorize the Agent before reading leases.
- Query `registry.ListAgentConnections`.
- Overlay local manager state when `ServerNodeID` equals the local node ID.
- For remote views, use lease `ActiveStreams`, `HealthScore`, and expiry; mark `Healthy` as true only when the lease is active.
- Require a positive `connectionEpoch`.
- Close locally when the owner is this node.
- For remote owners, dial the lease address using the existing relay client configuration and invoke `CloseAgentConnection`.
- Return `503` when the remote node is unreachable; do not delete the lease.
- Treat a lease that disappeared before close as successful idempotency.
- Write an audit record for both successful and failed close attempts.
- Update OpenAPI schemas, parameters, responses, and authorization descriptions.

- [ ] **Step 4: Verify task**

Run:

```bash
go test ./internal/server -run 'TestAgentConnections' -count=1
go test ./internal/server -count=1
```

Expected result: all API and server tests pass.

### Task 5: Admin UI

**Files:**

- Modify: `web/src/api/client.ts`
- Modify: `web/src/views/AgentDetail.vue`
- Modify: `web/src/i18n/messages/zh-CN.ts`
- Modify: `web/src/i18n/messages/en-US.ts`
- Test: `web/src/tests/agent-detail.spec.ts`

**Interfaces:**

- Produces:

```ts
export type ClusterAgentConnection = {
  agentId: string
  instanceId: string
  connectionId: string
  connectionEpoch: number
  serverNodeId: string
  serverNodeEpoch: number
  serverNodeAddress?: string
  healthy: boolean
  activeStreams: number
  healthScore: number
  lastHeartbeatAt?: string
  leaseExpiresAt: string
  local: boolean
}

export function listAgentConnections(agentId: string): Promise<ClusterAgentConnection[]>
export function closeAgentConnection(
  agentId: string,
  connectionId: string,
  connectionEpoch: number,
): Promise<{ closed: boolean }>
```

- [ ] **Step 1: Write failing frontend tests**

Add tests that verify:

```ts
await wrapper.find('[data-test="refresh-connections"]').trigger('click')
expect(listAgentConnections).toHaveBeenCalledWith('agent-1')

await wrapper.find('[data-test="close-connection-conn-1"]').trigger('click')
await wrapper.find('[data-test="confirm-close-connection"]').trigger('click')
expect(closeAgentConnection).toHaveBeenCalledWith('agent-1', 'conn-1', 7)
```

Also test the remote-node column and the error message shown when close returns `503`.

- [ ] **Step 2: Run failing tests**

Run:

```bash
cd web
npm test -- --run src/tests/agent-detail.spec.ts
```

Expected result: failure because the UI actions and API helpers do not exist.

- [ ] **Step 3: Implement the UI**

Implementation requirements:

- Add a refresh button above the connection table.
- Add columns for Server node and lease expiry.
- Add a danger “Close” action for each connection.
- Confirm dialog shows Agent ID, connection ID, epoch, server node, and active stream count.
- Disable the confirm button while closing.
- Reload connections after success.
- Show a localized warning for remote-node unavailability.

- [ ] **Step 4: Verify task**

Run:

```bash
cd web
npm test -- --run
npm run build
```

Expected result: frontend tests and production build pass.

### Task 6: Documentation, Embed Sync, and Full Verification

**Files:**

- Modify: `docs/user-guide/server-admin.md`
- Modify: `docs/operations/connection-pool.md`
- Modify: `docs/operations/troubleshooting.md`
- Modify: `docs/api/openapi.yaml`
- Regenerate: `internal/server/web_dist`

- [ ] **Step 1: Update documentation**

Document:

- Cross-node prerequisites: unique `node.id`, enabled relay mTLS, valid server-node token, and reachable relay address.
- Query and close API examples.
- The fact that closing one physical connection does not disable the Agent or prevent reconnect.
- Why an unreachable owner returns `503` and the lease is retained.
- Rollout note: all participating Server nodes must run the new version for complete cluster visibility.

- [ ] **Step 2: Sync embedded frontend**

Run:

```bash
rm -rf internal/server/web_dist
cp -R web/dist internal/server/web_dist
```

Before running deletion, confirm that `web/dist` exists and `internal/server/web_dist` contains only generated assets.

- [ ] **Step 3: Run full verification**

Run:

```bash
go test ./... -count=1
go test -race ./...
go vet ./...
git diff --check
cd web
npm test -- --run
npm run build
```

Expected result: every command exits successfully.

### Final Review Checklist

- [ ] Registry registration, renewal, statistics, and release are wired into the real Agent WebSocket lifecycle.
- [ ] Query merges local and remote state without leaking another owner's Agent.
- [ ] Close is exact-fenced by connection ID and connection epoch.
- [ ] Remote close uses authenticated mTLS and does not expose a public unauthenticated control endpoint.
- [ ] Unreachable owner returns `503` and preserves the lease.
- [ ] UI supports query, refresh, and close in Chinese and English.
- [ ] OpenAPI, user guide, operations guide, troubleshooting guide, and embedded assets are updated.
- [ ] All required Go and frontend validation commands pass.
