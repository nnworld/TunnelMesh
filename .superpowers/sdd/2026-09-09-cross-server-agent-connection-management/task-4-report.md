# Task 4 Report: Cluster-Wide Query and Close API

## Implementation

- Added `GET /api/v1/agents/{agentId}/connections`.
- Added `DELETE /api/v1/agents/{agentId}/connections/{connectionId}?connectionEpoch={epoch}`.
- Added `AgentConnectionClusterView` with durable lease epoch, owner Server identity, health, load, heartbeat, and expiry fields.
- Merged registry leases with process-local session state; local views use current relay stream counts and heartbeat freshness.
- Enforced Agent owner/admin authorization before querying leases.
- Added `ClusterAgentConnectionCloseService`:
  - local leases close through `AgentConnectionLeaseController`;
  - remote leases dial the owner relay address and invoke authenticated `CloseAgentConnection`;
  - remote dial/control failure maps to `ErrAgentConnectionNodeUnavailable`;
  - stale and disappeared leases use exact-epoch semantics.
- Wired the runtime API to the registry-backed lister and production close service.
- Added the local relay endpoint to connection lease registration so remote owners are dialable.
- Added success, failure, stale-epoch, and idempotent close audit records with non-secret details only.
- Updated OpenAPI paths, schemas, parameters, authorization, and failure semantics.

## TDD Evidence

RED:

```bash
go test ./internal/server -run 'TestAgentConnections' -count=1
```

Initial result:

```text
undefined: AgentConnectionCloseRequest
api.SetClusterAgentConnections undefined
undefined: AgentConnectionClusterView
undefined: ErrAgentConnectionNodeUnavailable
FAIL github.com/tunnelmesh/tunnelmesh/internal/server [build failed]
```

Additional production-service RED:

```bash
go test ./internal/server -run 'TestClusterAgentConnectionCloseService' -count=1
```

Initial result:

```text
undefined: NewClusterAgentConnectionCloseService
undefined: AgentConnectionRelayClient
FAIL github.com/tunnelmesh/tunnelmesh/internal/server [build failed]
```

GREEN:

```bash
go test ./internal/server -run 'TestAgentConnections' -count=1
go test ./internal/server -count=1
```

Results:

```text
ok github.com/tunnelmesh/tunnelmesh/internal/server
```

## Files Changed

- `internal/server/agent_connection_api.go`
- `internal/server/agent_connection_api_test.go`
- `internal/server/agent_connection_registry.go`
- `internal/server/agent_connection_registry_test.go`
- `internal/server/agent_connection_control_test.go`
- `internal/server/api.go`
- `internal/server/runtime.go`
- `docs/api/openapi.yaml`

## Self-Review

- The list and close handlers resolve and authorize the Agent before reading cluster leases.
- `connectionEpoch` exposed by the cluster API is the registry lease epoch, intentionally distinct from the local protocol epoch.
- Local overlay reports the current local relay load, not the last persisted heartbeat statistic.
- A disappeared exact lease is successful and idempotent; a reused connection ID with a different epoch returns `409`.
- Remote unavailability returns `503` and does not revoke or delete the durable lease.
- The production relay dial path reuses `ServerRuntime.DialRelayNode`, so mTLS and server-node token policy remain centralized.
- Audit details contain connection identity, epochs, node identity, load, result, and normalized error class only.
- No commit was created.
