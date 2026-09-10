# Logical Agent Connection Pool Design

## Status

Approved direction on 2026-09-09. This design implements a logical Agent connection pool without adding Agent-side multiple `server_urls`.

## Problem

The current Server model treats one `agent_id` as one live WebSocket session. `AgentSessionManager.sessions` is keyed only by Agent ID, and a newer connection replaces an older connection. This prevents several useful deployment shapes:

- Multiple physical Agent processes representing the same logical Agent.
- Multiple Agent nodes using the same Agent token and Agent ID.
- One Agent process maintaining multiple WebSocket connections under network pressure.
- Server-side load balancing across those physical connections.

The current `agent_runtime_leases` table is also keyed by `agent_id`, so the registry can represent only one owner for a logical Agent.

## Goals

- Preserve `agent_id` as the logical identity used by routes, tokens, APIs, and the management UI.
- Allow multiple physical Agent instances to authenticate with the same Agent token and Agent ID.
- Allow one Agent process to open multiple WebSocket connections to its configured Server URL.
- Automatically increase and decrease the number of connections based on network and load signals.
- Load balance each new stream across healthy connections.
- Keep a stream pinned to the connection that opened it.
- Support cluster routing when connections for one logical Agent land on different Server nodes.
- Preserve compatibility with existing one-connection Agents.

## Non-Goals

- Agent-side multiple `server_urls` is not implemented.
- Stream migration between live WebSocket connections is not implemented.
- A stream is not split across multiple connections.
- Agent Group or child-Agent resources are not introduced.
- No public UDP listener is added.
- No arbitrary remote command execution is added.

## Identity Model

### Logical Agent

```text
agent_id
```

The logical Agent remains the authorization and routing target. Existing routes and Agent tokens continue to reference this ID.

### Agent Instance

```text
instance_id
```

An Agent instance represents one physical Agent process or deployment node. It is stable across restarts.

- New protocol hello payloads include `instance_id`.
- Legacy hello payloads without `instance_id` use `node_id` as the instance identity.
- If an Agent configuration does not provide an explicit instance identity, the Agent generates and persists a stable instance ID.
- A logical Agent may have multiple active instances.

### Agent Connection

```text
connection_id
```

A connection represents one WebSocket transport.

- New protocol hello payloads include `connection_id`.
- The Agent generates a fresh connection ID for every successful dial.
- A connection ID is unique within a logical Agent.
- Each connection owns an independent Stream ID namespace.
- Each connection has an independent reconnect epoch.

### Server Node

```text
server_node_id
```

The Server node is the cluster process currently serving a WebSocket connection. It is distinct from the Agent instance identity and is required for remote relay.

## Protocol Changes

### Agent Hello

`AgentMetadataPayload` is extended with optional fields:

```json
{
  "agent_id": "agent-devbox",
  "instance_id": "agent-node-a",
  "connection_id": "conn-01h8h2q9v4k7w3r5t",
  "node_id": "legacy-node-id",
  "epoch": 12,
  "revision": 3
}
```

Compatibility rules:

- `agent_id` remains required.
- `instance_id` is optional for legacy Agents. When absent, Server uses `node_id`.
- `connection_id` is optional for legacy Agents. When absent, Server synthesizes a stable legacy connection identity.
- `epoch` becomes the reconnect epoch of that specific connection, not a global logical-Agent epoch.

### Metadata Ack Capability

`AgentMetadataAckPayload` is extended with:

```json
{
  "connection_pool_supported": true,
  "max_connections_per_agent": 8
}
```

An Agent may open its initial connection using the existing protocol. It only creates additional connections after the Server acknowledges connection-pool support.

Rolling upgrades must complete across all Server nodes before operators enable `agent.connections.max > 1`. A single Server acknowledgment is not treated as proof that every node behind a shared URL supports the capability.

## Agent Connection Controller

The Agent keeps one configured Server URL and runs a connection controller.

### Configuration

```yaml
agent:
  id: agent-devbox
  server_url: wss://tunnel.example.com/ws/agent/v1
  connections:
    min: 1
    max: 8
    high_watermark: 16
    low_watermark: 2
    evaluation_interval: 10s
    cooldown: 30s
```

Defaults preserve current behavior:

```yaml
agent:
  connections:
    min: 1
    max: 1
```

### Scaling Signals

The controller evaluates each connection using:

- Active stream count.
- Send queue depth and queue wait.
- Dial success and failure.
- WebSocket close and GOAWAY frequency.
- Heartbeat RTT.
- Recent stream-open failures.
- Server-acknowledged maximum connection count.

### Scale Up

The controller may create a connection when:

- Current connection count is below `max`.
- At least one connection is at or above `high_watermark` active streams, or
- queue pressure persists, or
- heartbeat RTT or send latency remains high for the evaluation window, or
- connection-level errors indicate transport contention.

### Scale Down

The controller may close an idle connection when:

- Connection count is above `min`.
- Candidate connection active streams are at or below `low_watermark`.
- The candidate has been stable for the evaluation window.
- The cooldown period has elapsed since the previous scaling decision.

Scaling down uses graceful drain:

1. Stop selecting the connection for new streams.
2. Send or honor GOAWAY where possible.
3. Wait for active streams to finish or time out.
4. Close the WebSocket.

The controller applies hysteresis and cooldown to prevent oscillation during transient network changes.

## Server Session Model

`AgentSessionManager` changes from:

```go
map[agentID]*AgentSession
```

to:

```go
map[agentID]map[connectionID]*AgentSession
```

Rules:

- Registering a new `connection_id` adds a session to the logical Agent pool.
- A reconnect with the same `connection_id` and a higher epoch replaces only that connection.
- A stale or equal epoch for the same `connection_id` is rejected.
- Closing one connection removes only that session and fails only its streams.
- A logical Agent is online when at least one pooled connection is healthy.
- Case-insensitive dynamic-domain lookup returns the logical Agent pool, not one session.

## Stream Ownership

Stream ownership is extended from:

```text
agent_id + stream_id
```

to:

```text
agent_id + connection_id + connection_epoch + stream_id
```

This prevents two concurrent WebSocket connections from colliding when both use Stream ID 1.

The stream remains pinned to its selected connection for its lifetime. TunnelMesh does not migrate an established stream to another connection.

## Connection Selection

The Server selects a connection for each `OPEN_STREAM`.

### Candidate Filtering

A connection is eligible when:

- Its session is open.
- It supports the requested protocol.
- Its heartbeat is fresh.
- Recent failure rate is below the unhealthy threshold.
- Its active stream count is below the configured maximum.

### Local Priority

The ingress Server first considers connections local to its process. If no local connection is eligible, it resolves remote candidates through the connection registry and forwards through relay.

### Scoring

Recommended first implementation uses weighted least-connections:

```text
score =
    active_streams
  + queue_pressure_penalty
  + heartbeat_rtt_penalty
  + recent_error_penalty
```

The candidate with the lowest score wins. Ties use stable connection ID ordering.

## Registry and Storage

### Database Lease Model

`agent_runtime_leases` becomes a connection-level pool:

```text
agent_id
instance_id
connection_id
server_node_id
connection_epoch
capabilities
active_streams
health_score
expires_at
```

Primary key:

```text
(agent_id, connection_id)
```

Indexes:

```text
(agent_id, expires_at)
(server_node_id, expires_at)
```

Legacy rows are migrated by synthesizing:

```text
instance_id = node_id
connection_id = "legacy-" + epoch
server_node_id = node_id
```

Repository operations:

- `RegisterConnection`
- `RenewConnection`
- `ReleaseConnection`
- `ListActiveByAgent`
- `UpdateConnectionStats`

All operations are fenced by `(agent_id, connection_id, connection_epoch)`.

### Etcd Registry Model

Etcd keys change from a single Agent owner:

```text
/tunnelmesh/agents/{agent_id}
```

to connection keys:

```text
/tunnelmesh/agents/{agent_id}/connections/{connection_id}
```

Each key contains Agent instance, Server node, epoch, capabilities, and expiry. `ResolveAgent` becomes `ListAgentConnections` and returns all non-expired connections.

## Cluster Relay

Remote selection uses the existing relay transport:

1. Ingress Server lists active connections for the logical Agent.
2. It selects a local connection if possible.
3. Otherwise it chooses a remote connection and obtains that connection's `server_node_id`.
4. It sends a relay `OpenStream` request to that Server node.
5. The remote Server opens the stream on the specified `connection_id`.

The relay request gains:

```text
target_connection_id
target_connection_epoch
```

The receiving Server rejects the request if that connection is no longer current. This prevents a stale registry view from opening a stream on a replaced connection.

## Metadata

Agent metadata becomes instance-scoped:

```text
(agent_id, instance_id)
```

The public Agent metadata response keeps a backward-compatible top-level view and adds:

```json
{
  "agentId": "agent-devbox",
  "items": [],
  "instances": [
    {
      "instanceId": "agent-node-a",
      "connectionCount": 3,
      "items": []
    }
  ]
}
```

The top-level view uses the freshest healthy instance for compatibility. Management UI displays all instances and their connections.

## Security

- The Agent token remains bound to the same logical Agent and owner.
- Every WebSocket connection independently authenticates the same token.
- The Server stores token ID, instance ID, connection ID, and Server node for audit.
- Raw bearer tokens are never persisted or logged.
- Connection flooding is bounded by:
  - `max_connections_per_agent`
  - `max_connections_per_token`
  - connection registration rate
  - per-Server-node connection limits
- Rejected duplicate connections, stale epochs, and limit violations emit audit events and metrics.
- Existing SSRF, CIDR, protocol, and port policy checks remain enforced on the selected Agent connection.

## Observability

New metrics:

```text
tunnelmesh_agent_connections
tunnelmesh_agent_connection_capacity
tunnelmesh_agent_active_streams
tunnelmesh_agent_connection_rtt
tunnelmesh_agent_connection_errors
tunnelmesh_agent_connection_scale_decisions
tunnelmesh_agent_selection
```

Labels include:

```text
agent_id
instance_id
connection_id
server_node_id
scope=local|remote
decision=scale_up|scale_down|rejected
```

The Agents management page shows:

- Logical Agent online state.
- Number of active instances.
- Number of active connections.
- Per-instance connection list.
- RTT, active streams, queue pressure, and recent errors.
- Latest scaling decision and reason.

## Rollout

1. Upgrade all Server nodes to the connection-pool protocol.
2. Keep Agent defaults at `min=1`, `max=1`.
3. Deploy new Agents.
4. Verify single-connection behavior.
5. Increase `agent.connections.max` gradually.
6. Observe connection metrics, stream errors, and relay selection.

## Failure Behavior

- A failed WebSocket fails only streams pinned to that connection.
- A new stream can select another healthy connection.
- A disconnected instance does not affect other instances.
- If all connections are unavailable, the logical Agent is offline.
- Registry expiry removes stale remote candidates.
- Stale relay requests fail closed when connection epoch does not match.

## Testing Strategy

- Protocol JSON compatibility tests for old and new hello/ack payloads.
- Session-manager concurrency tests for multiple connections and reconnect fencing.
- Stream ownership tests proving independent Stream ID namespaces.
- Agent controller tests for scale-up, scale-down, cooldown, and failure handling.
- Storage contract tests for MySQL and SQLite lease migration and concurrent registration.
- Registry tests for database and etcd multi-connection listing.
- Relay tests for local and remote connection selection.
- Metadata aggregation tests.
- E2E tests with multiple Agent instances and multiple connections per instance.
- Race tests around registration, removal, scaling, and stream open.
