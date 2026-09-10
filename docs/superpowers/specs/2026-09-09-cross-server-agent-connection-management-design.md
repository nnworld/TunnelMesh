# Cross-Server Agent Connection Management Design

## Goal

Administrators and Agent owners can query every live Agent WebSocket connection across all TunnelMesh Server nodes and close one exact connection without affecting other connections in the same logical Agent pool.

## Current Gap

`AgentSessionManager` knows only process-local connections. `agent_connection_leases` and `registry.NodeRegistry` can describe cluster-wide connections, but the server runtime does not currently register, renew, or release those leases when an Agent WebSocket is accepted. There is also no authenticated server-node control RPC to ask another Server node to close its local WebSocket.

## Architecture

- The registry remains the cluster-wide source of truth for connection ownership. Each Server node registers one lease per physical Agent WebSocket, renews it on heartbeat, updates active-stream statistics, and releases it on disconnect.
- The process-local `AgentSessionManager` remains the execution authority. Only the Server node that owns a WebSocket may close it.
- The existing authenticated mTLS relay adds a unary `CloseAgentConnection` control RPC. The management API calls the owner node through this RPC; it never tries to manipulate a remote process-local session directly.
- The management API merges active registry leases with local session state. Local state supplies freshness, health, heartbeat, and exact active-stream counts; lease state supplies cross-node ownership and reachability.

## Query API

`GET /api/v1/agents/{agentId}/connections` returns a cursor-free bounded list for one Agent. Each item contains:

- `agentId`, `instanceId`, `connectionId`, `connectionEpoch`
- `serverNodeId`, `serverNodeEpoch`, `serverNodeAddress`
- `healthy`, `activeStreams`, `healthScore`
- `lastHeartbeatAt`, `leaseExpiresAt`
- `local` indicating whether the responding Server node owns the connection

Only the Agent owner and administrators can query the list.

## Close API

`DELETE /api/v1/agents/{agentId}/connections/{connectionId}?connectionEpoch={epoch}`:

1. Authorizes the caller against the Agent owner.
2. Resolves the exact lease using Agent ID, connection ID, and connection epoch.
3. If the connection is local, closes it through the exact-fenced local session manager.
4. If the connection is remote, dials the owner Server node with the existing relay mTLS and server-node token, then invokes `CloseAgentConnection`.
5. The owner node sends `GOAWAY`, closes the WebSocket, fails its local streams, and releases the lease.
6. The management API writes an audit log and returns the resulting connection state.

Closing is idempotent when the exact connection has already disappeared. A stale epoch never closes a replacement connection that reused the same connection ID.

## Failure Semantics

- Registry registration failure rejects the Agent WebSocket so a cluster node never creates an unmanageable connection.
- Transient heartbeat renewal failure is logged and observed but does not tear down a healthy connection; the lease expires if renewal never resumes.
- If the owner node is unreachable, close returns `503` and leaves the lease intact. The API does not silently delete a lease because the remote WebSocket could re-register after deletion.
- Closing one connection does not prevent the Agent controller from reconnecting. To stop an Agent permanently, use the existing Agent disable or token revocation flows.

## Security

- The relay control RPC is available only to authenticated server-node identities over mTLS.
- Owner and administrator checks happen in the management API before any remote control request.
- Audit details contain non-secret connection identity and state only: Agent ID, instance ID, connection ID, epochs, server node IDs, active streams, and result. Tokens, target addresses, and stream payloads are excluded.

## Compatibility

- No database schema change is required; the existing `agent_connection_leases` table is used.
- Database and etcd registry implementations use the same `registry.NodeRegistry` interface.
- Old Server nodes continue serving local traffic during rollout, but cross-node query and close require all participating nodes to run the new lease lifecycle and relay control RPC.

## Verification

- Unit and repository tests cover registration, renewal, statistics, release, replacement fencing, and expiry.
- Relay tests cover unary mTLS authentication, exact-epoch close, stale-epoch rejection, and transport failure.
- API tests cover owner/admin authorization, unrelated-user denial, local and remote close, idempotency, stale epoch, and audit content.
- Frontend tests cover rendering, refresh, confirmation, and API error handling.
- Full validation runs Go tests, race tests, vet, frontend tests, frontend build, and embed synchronization.
