# Server Node Fleet Token and Administration Design

## Background

TunnelMesh currently binds every `server_node` service token to exactly one
`nodeId`. A deployment with multiple Server nodes therefore needs one token per
node. The Server also generates a missing `node.id` into
`/var/lib/tunnelmesh/node-id` during `run`; only the explicit `init-node-id`
command writes the identity back to YAML. Finally, the admin console has Agent
and connection views but no dedicated Server-node inventory.

## Goals

- Allow one `server_node` token to serve multiple Server nodes.
- Allow a Server to generate a missing `node.id` and write it back to its YAML
  configuration during initialization.
- Keep per-node mTLS verification and epoch fencing.
- Add an admin-only Server-node management page with inventory, status, and
  lifecycle controls.
- Rebuild existing one-node Server-node tokens as fleet or multi-node tokens;
  do not carry forward the old single-node binding behavior.

## Non-goals

- Do not weaken or remove relay mTLS.
- Do not share one mTLS certificate across Server nodes.
- Do not introduce a new relay protocol.
- Do not add dynamic certificate issuance.

## Token semantics

`TokenScope` gains `serverNodeIds`:

```json
{
  "serverNodeIds": ["server-a", "server-b"]
}
```

An empty array means a fleet token: the token may be used by any Server node
that passes all other checks. A non-empty array restricts the token to those
node IDs. The Server process receives the token through the existing
`server.relay.node_token` configuration field.

Relay authentication continues to require all of the following:

1. A valid `server_node` service token.
2. A verified mTLS peer certificate.
3. An exact certificate SAN match for the caller's `nodeId`.
4. A non-deleted, enabled, unexpired `server_nodes` record.
5. A matching node epoch.
6. Caller inclusion in the token allowlist, unless the allowlist is empty.

This makes the shared token a fleet credential while preserving node identity
and transport trust boundaries.

## Node identity and configuration

`run` remains the only normal command that may generate a missing identity.
When cluster mode is active, the YAML file has no `node.id`, and no CLI or
environment override provides one, `run` will:

1. Reuse an existing identity from `/var/lib/tunnelmesh/node-id` when present.
2. Otherwise generate a lowercase `server-<32 hex characters>` identity.
3. Write the value to `node.id` in the configured YAML file.
4. Continue startup using that identity.

`init-node-id` remains available for explicit initialization and uses the same
writer. The YAML writer preserves comments, file mode, and file ownership. It
will not overwrite an explicit identity.

The packaged systemd unit runs `init-node-id` as a root `ExecStartPre` before
the unprivileged `check-config` and `run` commands. This allows `/etc/tunnelmesh`
to remain read-only to the service user while still satisfying automatic YAML
initialization.

Each Server still needs a relay certificate whose SAN exactly matches its final
`node.id`. The documented installation sequence is therefore:

1. Install the configuration without `node.id`.
2. Run `init-node-id` or start the unit once to populate `node.id`.
3. Issue the node's relay certificate with that SAN.
4. Configure the shared Server-node token and relay mTLS files.
5. Start or restart the Server.

## Server-node inventory

`server_nodes` gains logical-management fields:

- `name`: optional display name, defaulting to the node ID.
- `enabled`: defaults to true.
- `deleted_at`: logical deletion timestamp; null means active.

On startup, a Server ensures its own node record exists. It never re-enables or
restores a node that an administrator disabled or deleted. A background
heartbeat updates `last_seen_at` and `expires_at` so nodes without Agent
connections still report status. Agent-connection leases continue to provide
per-connection load and health details.

The admin API exposes:

- `GET /api/v1/server-nodes`
- `GET /api/v1/server-nodes/{id}`
- `PATCH /api/v1/server-nodes/{id}`
- `DELETE /api/v1/server-nodes/{id}`
- `POST /api/v1/server-nodes/{id}/restore`

All endpoints are administrator-only. `DELETE` is logical deletion and sets
`enabled=false`; `restore` clears `deleted_at` and sets `enabled=true`. Status
is derived from lifecycle fields, heartbeat expiry, and active connection
leases. List and detail responses include active connection count, active
stream count, aggregate health, epoch, address, and timestamps.

The admin console adds an admin-only `/servers` page with:

- Inventory table and status tags.
- Name/address/epoch/last-seen/lease-expiry columns.
- Active connections and active streams.
- Enable/disable actions.
- Logical delete and restore actions.
- A detail drawer with all non-secret fields.
- A notice explaining that nodes self-register after identity initialization.

The Token creation dialog replaces the single Server-node ID input with a
multi-select. Leaving it empty creates a fleet token; selecting values creates
an explicit allowlist.

## Data model and migration

Schema version moves from 7 to 8. The migration adds `name`, `enabled`, and
`deleted_at` to `server_nodes`, and backfills `name` to the node ID. Multi-node
bindings are represented by `scope.serverNodeIds`; the legacy token `nodeId`
binding is not used by the new authentication path.

The full DDL and both MySQL and SQLite incremental scripts must be updated
together. MySQL scripts must remain MySQL 5.6-compatible.

## Security considerations

- Fleet tokens are administrator-only.
- mTLS remains the per-node transport identity; a leaked fleet token alone is
  insufficient without a valid node certificate.
- Explicit node allowlists are validated against existing, enabled,
  non-deleted nodes.
- Disabled or deleted nodes fail relay authentication.
- Management responses never expose token secrets or private keys.
- All Server-node lifecycle mutations write structured audit logs.

## Testing

- Config: missing identity writes YAML; explicit and CLI identities are
  preserved; comments, mode, and ownership survive replacement.
- Credential service: empty allowlist, multi-node allowlist, disabled/deleted
  nodes, and scope updates.
- Relay: shared token accepted for multiple mTLS identities, rejected for a
  non-allowlisted node, and rejected on SAN or epoch mismatch.
- Registry: node self-registration, heartbeat, disabled-node preservation, and
  lease aggregation.
- API: RBAC, pagination, validation, logical delete/restore, and audit records.
- Web: Server list/detail/lifecycle flows, token multi-select, i18n, and layout.
- Migration: SQLite and MySQL v7-to-v8 compatibility and full-schema checks.
