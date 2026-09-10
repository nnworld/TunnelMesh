# Logical Agent Connection Pool Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Allow one logical Agent ID to own multiple authenticated Agent instances and dynamically sized WebSocket connections, with Server-side stream scheduling and cluster relay.

**Architecture:** Separate logical Agent identity from Agent instance and WebSocket connection identity. Store connection-level leases, key streams by connection, select the healthiest local or remote connection for each stream, and let an Agent controller scale its single-URL connection pool using load and network signals.

**Tech Stack:** Go, WebSocket frame protocol, MySQL/SQLite migrations, etcd registry, gRPC relay, Vue 3, TypeScript, Element Plus.

**Spec:** `docs/superpowers/specs/2026-09-09-logical-agent-connection-pool-design.md`

## Global Constraints

- Do not implement Agent-side multiple `server_urls`; one Agent process uses one configured Server URL.
- Do not commit, push, merge, or create a remote PR without explicit user authorization.
- Preserve existing single-connection Agents; default connection pool size is `min=1`, `max=1`.
- A stream is pinned to the connection that opens it and is never migrated.
- Stream ownership includes `agent_id`, `connection_id`, connection epoch, and `stream_id`.
- All Schema changes update full DDL, the next incremental migration, `SchemaVersion`, migration tests, and operations documentation.
- MySQL 5.6 and SQLite must both remain supported.
- TLS, SSRF, CIDR, port, protocol, token type, owner, and enabled-state checks remain mandatory.
- TDD is mandatory: write each failing test first, verify red, implement minimally, verify green.
- Final verification must include `go test ./... -count=1`, `go test -race ./...`, `go vet ./...`, `git diff --check`, frontend tests/build, and embedded web verification.

---

### Task 1: Protocol Identity and Capability Negotiation

**Files:**
- Modify: `internal/protocol/frame.go`
- Test: `internal/protocol/protocol_test.go`

**Interfaces:**
- Produces `AgentMetadataPayload.InstanceID string` with JSON key `instance_id,omitempty`.
- Produces `AgentMetadataPayload.ConnectionID string` with JSON key `connection_id,omitempty`.
- Produces `AgentMetadataAckPayload.ConnectionPoolSupported bool` with JSON key `connection_pool_supported,omitempty`.
- Produces `AgentMetadataAckPayload.MaxConnectionsPerAgent int` with JSON key `max_connections_per_agent,omitempty`.

- [ ] **Step 1: Write failing protocol tests**

Add tests for:

```go
func TestAgentMetadataPayloadCarriesConnectionIdentity(t *testing.T) {
    payload := protocol.AgentMetadataPayload{
        AgentID: "agent-a", InstanceID: "agent-node-a",
        ConnectionID: "conn-1", NodeID: "legacy-node", Epoch: 3,
    }
    encoded, err := protocol.EncodeAgentMetadataPayload(payload)
    // Assert encoding succeeds and decoding preserves all fields.
}

func TestAgentMetadataAckAdvertisesConnectionPool(t *testing.T) {
    payload := protocol.AgentMetadataAckPayload{
        AgentID: "agent-a", Accepted: true,
        ConnectionPoolSupported: true, MaxConnectionsPerAgent: 8,
    }
    encoded, err := protocol.EncodeAgentMetadataAckPayload(payload)
    // Assert round-trip fields.
}
```

Also assert legacy JSON without the new fields decodes with empty values.

- [ ] **Step 2: Verify RED**

Run:

```bash
go test ./internal/protocol -run 'AgentMetadata.*Connection' -count=1
```

Expected: compile failure because the fields do not exist.

- [ ] **Step 3: Implement protocol fields**

Add the fields and JSON tags specified in the interface block. Do not change existing frame type numbers.

- [ ] **Step 4: Verify GREEN**

Run the same focused command.

### Task 2: Agent Instance Identity and Connection Configuration

**Files:**
- Modify: `internal/config/config.go`
- Modify: `internal/config/config_test.go`
- Modify: `internal/cli/root.go`
- Test: `internal/cli/agent_runtime_test.go`

**Interfaces:**
- Produces `config.AgentConnectionConfig`:

```go
type AgentConnectionConfig struct {
    Min int `mapstructure:"min" json:"min" yaml:"min"`
    Max int `mapstructure:"max" json:"max" yaml:"max"`
    HighWatermark int `mapstructure:"high_watermark" json:"high_watermark" yaml:"high_watermark"`
    LowWatermark int `mapstructure:"low_watermark" json:"low_watermark" yaml:"low_watermark"`
    EvaluationInterval time.Duration `mapstructure:"evaluation_interval" json:"evaluation_interval" yaml:"evaluation_interval"`
    Cooldown time.Duration `mapstructure:"cooldown" json:"cooldown" yaml:"cooldown"`
}
```

- Produces `AgentConfig.Connections AgentConnectionConfig`.
- Produces stable Agent instance ID loading/persistence helpers in the agent package.

- [ ] **Step 1: Write failing config and CLI tests**

Test cases:

- Defaults are `Min=1`, `Max=1`, `HighWatermark=16`, `LowWatermark=2`, `EvaluationInterval=10s`, `Cooldown=30s`.
- Reject `Min < 1`.
- Reject `Max < Min`.
- Reject `Max > 64`.
- Reject non-positive intervals.
- Preserve one `agent.server_url`.
- Agent run uses a generated or configured stable instance ID.

- [ ] **Step 2: Verify RED**

```bash
go test ./internal/config ./internal/cli -run 'AgentConnection|AgentRun' -count=1
```

- [ ] **Step 3: Implement configuration and identity**

Add the config structure, defaults, validation, and stable instance ID persistence. Do not add a `server_urls` list.

- [ ] **Step 4: Verify GREEN**

Run the focused command.

### Task 3: Multi-Connection Agent Session Manager

**Files:**
- Modify: `internal/server/session_manager.go`
- Test: `internal/server/session_test.go`
- Test: `internal/server/metadata_protocol_test.go`

**Interfaces:**
- `AgentRegistration` gains:

```go
InstanceID string
ConnectionID string
ConnectionEpoch int64
```

- `AgentSessionManager` stores sessions by logical Agent and connection ID.
- Produces:

```go
func (m *AgentSessionManager) List(agentID string) []*AgentSession
func (m *AgentSessionManager) GetConnection(agentID, connectionID string) (*AgentSession, bool)
```

- Legacy registration without `ConnectionID` receives a deterministic legacy ID.

- [ ] **Step 1: Write failing session tests**

Cover:

- Two connection IDs for one Agent ID coexist.
- Reconnect with the same connection ID and higher epoch replaces only that connection.
- Stale or equal epoch is rejected.
- Removing one connection leaves the other available.
- Dynamic case-insensitive lookup returns a pool rather than one hardcoded session.
- Old API behavior remains valid for a single connection.

- [ ] **Step 2: Verify RED**

```bash
go test ./internal/server -run 'AgentSession.*Connection|DynamicAgent' -count=1
```

- [ ] **Step 3: Implement pooled session storage**

Change the internal map to `map[string]map[string]*AgentSession`. Keep metadata fencing scoped to instance/connection where applicable.

- [ ] **Step 4: Verify GREEN**

Run the focused command, then:

```bash
go test ./internal/server -count=1
```

### Task 4: Connection-Aware Agent Relay Streams

**Files:**
- Modify: `internal/server/agent_relay_transport.go`
- Test: `internal/server/agent_relay_transport_test.go`

**Interfaces:**
- Relay stream keys include Agent ID, connection ID, and connection generation.
- `OpenStream` selects a pooled connection.
- Inbound Agent frames are dispatched using the exact connection session.

- [ ] **Step 1: Write failing relay tests**

Cover:

- Two concurrent connections can both use Stream ID 1 without collision.
- Frames from connection A cannot affect connection B.
- Closing connection A fails only its streams.
- Replacing connection A does not invalidate connection B.
- Remote or explicit connection selection opens the requested connection.

- [ ] **Step 2: Verify RED**

```bash
go test ./internal/server -run 'AgentRelay.*Connection' -count=1
```

- [ ] **Step 3: Implement connection-aware keys**

Replace Agent-generation-only keys with connection identity and keep exact fencing on reconnect.

- [ ] **Step 4: Verify GREEN**

Run the focused command and then the full server package.

### Task 5: Connection-Level Lease Schema and Repository

**Files:**
- Modify: `migrations/ddl.sql`
- Create: `migrations/incremental/v0006_to_v0007/mysql.sql`
- Create: `migrations/incremental/v0006_to_v0007/sqlite.sql`
- Modify: `internal/storage/models.go`
- Modify: `internal/storage/repository.go`
- Modify: `internal/storage/db.go`
- Test: `internal/storage/storage_contract_test.go`
- Test: `internal/storage/mysql_test.go`
- Test: `internal/storage/sqlite_test.go`

**Interfaces:**
- `AgentLease` gains `InstanceID`, `ConnectionID`, `ServerNodeID`, `ConnectionEpoch`, `ActiveStreams`, and `HealthScore`.
- Produces repository methods:

```go
RegisterConnection(context.Context, AgentLease) (AgentLease, error)
RenewConnection(context.Context, string, string, int64, time.Duration) error
ReleaseConnection(context.Context, string, string, int64) error
ListActiveByAgent(context.Context, string) ([]AgentLease, error)
UpdateConnectionStats(context.Context, AgentLease) error
```

- [ ] **Step 1: Write failing storage contract tests**

Cover:

- Two connections for one Agent coexist.
- Same connection ID with higher epoch replaces that row.
- Stale epoch cannot renew, release, or update stats.
- Expired rows are excluded.
- Listing returns connections ordered deterministically.
- Migration preserves legacy rows with generated connection IDs.

- [ ] **Step 2: Verify RED**

```bash
go test ./internal/storage -run 'Agent.*ConnectionLease' -count=1
```

- [ ] **Step 3: Implement migration and repository**

Use the next schema version, update full DDL and both incremental scripts, and preserve MySQL 5.6-compatible syntax.

- [ ] **Step 4: Verify GREEN**

Run focused storage tests, then all storage tests.

### Task 6: Database and Etcd Multi-Connection Registry

**Files:**
- Modify: `internal/registry/registry.go`
- Modify: `internal/registry/database.go`
- Modify: `internal/registry/etcd.go`
- Test: `internal/registry/registry_contract_test.go`
- Test: `internal/registry/etcd_test.go`

**Interfaces:**
- Produces:

```go
ListAgentConnections(context.Context, string) ([]NodeOwner, error)
```

- `NodeOwner` gains Agent instance, connection ID, and Server node fields.
- `ResolveAgent` remains as a compatibility wrapper returning the best active connection.

- [ ] **Step 1: Write failing registry tests**

Cover:

- Registering multiple connections for one Agent succeeds.
- Listing returns all non-expired connections.
- Revoking one connection leaves others.
- Keep-alive is connection-scoped.
- Remote candidates include Server node identity.
- Legacy single-owner behavior still resolves.

- [ ] **Step 2: Verify RED**

```bash
go test ./internal/registry -run 'Agent.*Connections' -count=1
```

- [ ] **Step 3: Implement both registry adapters**

Use database connection leases and etcd per-connection keys.

- [ ] **Step 4: Verify GREEN**

Run all registry tests.

### Task 7: Local and Remote Connection Selection

**Files:**
- Modify: `internal/relay/service.go`
- Modify: `internal/relay/transport.go`
- Modify: `internal/server/agent_relay_transport.go`
- Modify: `internal/server/runtime.go`
- Test: `internal/relay/transport_test.go`
- Test: `internal/server/runtime_test.go`

**Interfaces:**
- `relay.StreamRequest` gains:

```go
TargetConnectionID string
TargetConnectionEpoch int64
```

- Produces a connection selector with:

```go
type ConnectionSelector interface {
    Select(ctx context.Context, agentID, protocol string) (AgentConnectionTarget, error)
}
```

- `AgentConnectionTarget` contains local session or remote `server_node_id`, connection ID, and epoch.

- [ ] **Step 1: Write failing selection and relay tests**

Cover:

- Local healthy connection is preferred.
- Local unhealthy connection is skipped.
- Remote connection is selected when no local candidate exists.
- Explicit remote connection ID is honored.
- Stale remote epoch fails closed.
- Selection uses active streams, queue pressure, RTT, and errors.
- Dynamic routes select from the logical Agent pool.

- [ ] **Step 2: Verify RED**

```bash
go test ./internal/relay ./internal/server -run 'ConnectionSelection|RemoteConnection' -count=1
```

- [ ] **Step 3: Implement selector and relay fields**

Propagate connection identity through local and gRPC relay paths and reject stale targets.

- [ ] **Step 4: Verify GREEN**

Run focused tests, then full relay and server packages.

### Task 8: Adaptive Agent Connection Controller

**Files:**
- Create: `internal/agent/connection_controller.go`
- Create: `internal/agent/connection_controller_test.go`
- Modify: `internal/agent/websocket.go`
- Modify: `internal/cli/root.go`
- Test: `internal/cli/agent_runtime_test.go`

**Interfaces:**
- Produces:

```go
type ConnectionController struct {
    options ConnectionControllerOptions
    mu sync.Mutex
    connections map[string]*managedConnection
    lastScaleAt time.Time
    closed bool
}

func NewConnectionController(options ConnectionControllerOptions) *ConnectionController
func (c *ConnectionController) Run(ctx context.Context) error
func (c *ConnectionController) Snapshot() ConnectionPoolSnapshot
```

- Each connection worker owns one Session and one StreamDispatcher.
- The controller dials the single configured `agent.server_url`.

- [ ] **Step 1: Write failing controller tests**

Cover:

- Starts `min` connections.
- Never exceeds `max`.
- Scales up at high watermark.
- Scales up on persistent queue pressure.
- Scales up on persistent high RTT.
- Does not scale without Server connection-pool acknowledgment.
- Scales down only above `min`, below low watermark, and after cooldown.
- Gracefully drains a connection before close.
- A failed connection reconnects without deleting unrelated connections.
- Context cancellation closes all connections.

- [ ] **Step 2: Verify RED**

```bash
go test ./internal/agent -run 'ConnectionController' -count=1
```

- [ ] **Step 3: Implement controller**

Refactor the existing single-connection runner into a reusable per-connection runner. Add evaluation, hysteresis, scaling, and graceful drain.

- [ ] **Step 4: Verify GREEN**

Run focused Agent tests, then all Agent tests.

### Task 9: Instance-Scoped Metadata and Public API

**Files:**
- Modify: `internal/storage/models.go`
- Modify: `internal/storage/repository.go`
- Modify: `internal/server/metadata_service.go`
- Modify: `internal/server/metadata_api.go`
- Modify: `internal/server/api.go`
- Modify: `docs/api/openapi.yaml`
- Test: `internal/server/metadata_service_test.go`
- Test: `internal/server/metadata_api_test.go`

**Interfaces:**
- Metadata rows are keyed by `(agent_id, instance_id)`.
- Public response adds an `instances` array.
- Top-level legacy fields use the freshest healthy instance.

- [ ] **Step 1: Write failing metadata/API tests**

Cover:

- Two instances report distinct metadata.
- Updating one instance does not overwrite another.
- Stale instance data is marked stale independently.
- API returns `instances` and connection counts.
- Legacy clients can still read top-level fields.
- Sensitive metadata remains redacted.

- [ ] **Step 2: Verify RED**

```bash
go test ./internal/server -run 'Metadata.*Instance|AgentMetadataAPI' -count=1
```

- [ ] **Step 3: Implement instance-scoped metadata**

Use the same schema migration window as connection leases where appropriate and update OpenAPI.

- [ ] **Step 4: Verify GREEN**

Run focused metadata/API tests.

### Task 10: Metrics, Audit, and Management UI

**Files:**
- Modify: `internal/observability/metrics.go`
- Modify: `internal/server/session_manager.go`
- Modify: `internal/server/agent_relay_transport.go`
- Modify: `web/src/api/client.ts`
- Modify: `web/src/views/Agents.vue`
- Modify: `web/src/views/AgentDetail.vue`
- Modify: `web/src/i18n/messages/zh-CN.ts`
- Modify: `web/src/i18n/messages/en-US.ts`
- Test: `internal/server/session_test.go`
- Test: `web/src/tests/agent-token-workflows.spec.ts`

**Interfaces:**
- Produces connection pool metrics listed in the design.
- Audit events include `agent.connection.registered`, `agent.connection.replaced`, `agent.connection.rejected`, and `agent.connection.closed`.
- Agent detail API exposes instances and connections.

- [ ] **Step 1: Write failing observability and UI tests**

Cover:

- Connection gauge increments and decrements.
- Scale decisions are counted.
- Local versus remote selection is counted.
- Agent detail renders instance and connection tables.
- Sensitive values are not rendered.
- Chinese and English labels exist.

- [ ] **Step 2: Verify RED**

```bash
go test ./internal/server -run 'Connection.*Metrics|Connection.*Audit' -count=1
npm --prefix web test -- --run src/tests/agent-token-workflows.spec.ts
```

- [ ] **Step 3: Implement metrics, audit, and UI**

Follow the existing Element Plus light SaaS style and i18n patterns.

- [ ] **Step 4: Verify GREEN**

Run focused backend and frontend tests.

### Task 11: Documentation and Operations Guide

**Files:**
- Modify: `docs/user-guide/agent.md`
- Modify: `docs/operations/config-examples.md`
- Modify: `docs/operations/configuration.md`
- Modify: `docs/operations/troubleshooting.md`
- Modify: `docs/README.md`
- Create: `docs/operations/connection-pool.md`

**Interfaces:**
- Produces operator guidance for enabling, sizing, rolling out, monitoring, and rolling back the connection pool.

- [ ] **Step 1: Write documentation updates**

Include:

- Configuration example.
- Security implications of sharing one token and Agent ID.
- Required unique Agent instance identity.
- Default disabled behavior (`max=1`).
- Cluster rollout order.
- Monitoring and troubleshooting.
- Rollback steps.

- [ ] **Step 2: Verify docs**

Run:

```bash
git diff --check -- docs
```

### Task 12: Full Verification and Embedded Assets

**Files:**
- Regenerate: `internal/server/web_dist/`

- [ ] Run all required verification:

```bash
go test ./... -count=1
go test -race ./...
go vet ./...
git diff --check
npm --prefix web test -- --run
npm --prefix web run build
rsync -a --delete web/dist/ internal/server/web_dist/
./scripts/verify-web-embed.sh
```

- [ ] Re-read the design and plan, then confirm every goal and non-goal is satisfied or explicitly deferred.
- [ ] Record final verification evidence in the corresponding PR description before requesting review.

## Execution Pause

This plan is ready for user review. No implementation task may start until the user explicitly confirms this plan.
