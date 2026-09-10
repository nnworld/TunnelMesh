# PR: Cross-Server Agent Connection Management

## Title

feat(server): manage agent connections across server nodes

## Target Branch

`main`

## Summary

This change makes each physical Agent WebSocket a durable, cluster-visible connection lease. Server nodes register, renew, update, and release these leases through the existing registry. Administrators and Agent owners can list every live connection across Server nodes and close one exact connection from the management UI or API.

Local closes use the exact-fenced session controller. Remote closes use a new authenticated unary relay control RPC over the existing Server-node mTLS and token identity. The registry remains the source of ownership; the process-local session manager remains the execution authority.

## User Impact

- Agent detail now shows cluster-wide physical connections, including instance, connection, lease epoch, owner Server node, Server address, health, active streams, heartbeat, and lease expiry.
- Users can refresh the cluster connection list.
- Owners and administrators can close one exact physical connection after confirmation.
- Closing one connection does not disable the Agent or prevent reconnect.
- Remote Server unavailability returns `503`, preserves the lease, and shows a localized warning.
- A stale connection epoch returns `409` and never closes a replacement connection.

## API / Schema / Configuration Impact

API additions:

- `GET /api/v1/agents/{agentId}/connections`
- `DELETE /api/v1/agents/{agentId}/connections/{connectionId}?connectionEpoch={epoch}`

`connectionEpoch` in the cluster API is the registry lease epoch. The owning Server resolves it to the local protocol session epoch before closing.

Schema impact:

- No new schema version or DDL is required.
- The feature uses the existing `agent_connection_leases` and `agent_instance_metadata` structures.

Configuration impact:

- Relay-enabled Servers must configure a non-empty, reachable `server.relay.endpoint`.
- `check-config` now rejects relay-enabled configurations without that endpoint.
- Each Server must keep a unique, stable `node.id`.

## Security and Authorization Impact

- The management list and close APIs enforce Agent owner or administrator authorization before reading leases.
- The remote close RPC uses the same mTLS certificate SAN, server-node token, and node epoch validation as relay streams.
- The runtime additionally requires the authenticated caller node ID to match `RequestedByNodeID`.
- Remote close is not exposed as a public unauthenticated endpoint.
- Audit records contain connection identity, epochs, node identity, load, result, and normalized error class only; they do not contain tokens, target addresses, or stream payloads.

## Test Evidence

All commands were run against the final working tree:

```bash
ruby -e 'require "yaml"; YAML.load_file("docs/api/openapi.yaml")'
go test ./... -count=1
go test -race ./...
go vet ./...
git diff --check
cd web
npm test -- --run
npm run build
cd ..
go test ./internal/server -count=1
```

Results:

- OpenAPI YAML parsed successfully.
- All Go package tests passed.
- All Go package race tests passed.
- `go vet` passed.
- Frontend tests passed: 10 files, 50 tests.
- Frontend production build passed.
- Post-embed server tests passed.
- `git diff --check` passed.

## Release Steps

1. Back up the MySQL database and verify the backup is restorable.
2. Confirm `schema_meta.version` is already at the required connection-pool schema version.
3. Build and deploy the new Server binary and embedded frontend to all Server nodes.
4. Verify each node has a unique `node.id`, relay mTLS material, a valid server-node token, and a reachable `server.relay.endpoint`.
5. Run `tunnelmesh-server --config <config> check-config`.
6. Roll nodes one at a time and verify `/health/ready` after each node.
7. Open Agent detail and confirm local and remote connections are visible.
8. Test a non-critical connection close and confirm only the selected connection closes.

## Rollback Steps

1. Stop writes if precise data rollback is required.
2. Roll back the Server binary and embedded frontend to the previous version.
3. Preserve existing registry tables and rows; this feature performs no destructive schema migration.
4. Verify `/health/ready`, Agent WebSocket connectivity, routes, and tunnels.
5. If database state must be exactly restored, use the pre-release backup rather than generating reverse SQL.

## Reviewer Focus

- Lease registration, heartbeat renewal, statistics updates, and release in the real Agent read loop.
- Separation between registry lease epoch and local protocol connection epoch.
- Unary relay authentication and `RequestedByNodeID` correlation.
- Owner/admin authorization before lease reads or remote control.
- `503` handling that preserves the durable lease.
- Idempotency when an exact lease disappears and `409` when a connection ID is replaced.
- Audit detail allowlisting.
- Frontend use of the epoch returned by the cluster API.

## Integration Status

- Implementation and validation are complete in the local working tree.
- No commit, push, merge, or remote PR has been created; those operations require explicit user authorization.
