# PR: Expose client observability

## Target Branch

`main`

## Summary

This change adds durable Client runtime observability:

- Stable Client instance identity persisted on the Client host.
- Versioned Client metadata protocol over the negotiated WebSocket subprotocol.
- Client metadata collection, bounded payloads, allowlist handling, and reporting.
- Schema v11 tables for client instance metadata and physical connection leases.
- MySQL and SQLite incremental v10-to-v11 migrations plus full-schema DDL.
- Runtime connection manager, lease controller, metadata sweeper, and observability service.
- Cluster-safe Client connection close with epoch fencing.
- Management APIs and OpenAPI definitions.
- `/clients` management page with owner-scoped filters, summaries, details, metadata, listeners, connections, and close actions.
- Compatibility downgrade when a caller negotiates metadata but opens the first frame as a legacy/flow-control session.
- Shared Agent/Client metadata source policy with bounded field and payload limits.
- Runtime Client metadata wiring for `client.instance_id`, `client.instance_id_path`, and the explicit `client.metadata` allowlist.

## User Impact

Administrators can see every Client instance and physical WebSocket connection across Server nodes, including owner, Token, version, platform, hostname, agents, listeners, active streams, Server node, health, and last heartbeat. They can safely close one exact connection. Normal users see only their own Clients. Old Clients remain usable and are represented as `metadata_unavailable`.

## API / Schema / Configuration Impact

- Adds `GET /api/v1/clients`.
- Adds `GET /api/v1/clients/{clientInstanceId}`.
- Adds `GET /api/v1/clients/{clientInstanceId}/connections`.
- Adds `DELETE /api/v1/clients/{clientInstanceId}/connections/{connectionId}?connectionEpoch=...`.
- Adds WebSocket subprotocol `tunnelmesh.v1.open-result.flow-control.metadata` and `CLIENT_HELLO`, `CLIENT_METADATA_UPDATE`, and `CLIENT_METADATA_ACK` frames.
- Advances Schema from 10 to 11.
- Adds `client.instance_id` and `client.instance_id_path`; an omitted ID is generated and persisted by long-running Client startup.
- Adds `client.metadata` for explicitly allowlisted file and environment fields. Sources are collected once at startup, limited to 32 fields, 4 KiB per field, and 32 KiB aggregate payload.
- Metadata is stored as bounded TEXT and uses no JSON columns, CTEs, or functional indexes for MySQL 5.6/SQLite compatibility.

## Security and Authorization Impact

- Client metadata never participates in authorization; Token scope and Agent Policy remain authoritative.
- List and detail authorization filtering is performed in SQL, not after pagination.
- Administrators see all Clients; ordinary users see only their owned resources.
- Connection close validates owner or admin, connection ownership, and `connection_epoch`.
- Metadata collection is bounded, duplicate names are rejected, and sensitive names are rejected before collection or persistence.
- A valid `CLIENT_HELLO` must precede `CLIENT_METADATA_UPDATE`; out-of-order updates cannot modify instance metadata or connection leases.
- No Token plaintext/hash, password, private key, DSN, full Authorization header, or remote-validation URL is persisted or rendered by this feature.

## Test Evidence

Fresh verification on the final working tree:

```text
cd web && npm test -- --run
13 files / 85 tests passed

cd web && npm run build
PASS

./scripts/verify-web-embed.sh
PASS

go test ./... -count=1
all packages with tests passed

go test -race ./...
all packages with tests passed; internal/server completed in 237.523s

go vet ./...
PASS

git diff --check
PASS
```

Focused verification also covered:

```text
go test ./internal/client -count=1
go test ./internal/storage -run 'MySQL|Migration' -v -count=1
go test ./internal/server -count=1
go test ./internal/relay -count=1
go test ./internal/cli -run TestAgentRunCommandForwardsTCPUDPAndHTTPThroughRealDispatcher -count=1
go test ./internal/e2e -run TestSOCKS5WebPageLatency -count=1
go test -race ./internal/server -run TestClientMetadataSweeperMarksExpiredAndStops -count=10
go test ./internal/metadata ./internal/config ./internal/client ./internal/agent ./internal/cli -count=1
```

SQLite migration and repository tests passed. MySQL repository-contract tests that require a live database were skipped because `TUNNELMESH_TEST_MYSQL_DSN` is not configured; the v10-to-v11 DDL avoids MySQL 5.6-incompatible JSON, CTE, and functional-index constructs.

## Release Steps

1. Back up SQLite/MySQL and verify restorability.
2. Stop or blue-green drain old Server nodes.
3. Deploy Schema v11-capable Server binaries with `storage.auto_init=true`.
4. Verify `/health/ready`, `schema_meta.version=11`, `/api/v1/clients`, and `/clients`.
5. Deploy new Agents and Clients.
6. Verify metadata reporting, multi-Server connection lists, heartbeat updates, and epoch-fenced close.

## Rollback Steps

1. Stop new traffic and roll applications back to the previous v10-compatible binaries.
2. The v11 tables are expand-only and can be retained if rollback binaries accept the higher schema; if exact-version checks reject it, restore the validated pre-upgrade backup.
3. Do not issue guessed reverse DDL.
4. Re-enable the previous Client configuration; old Clients can continue using legacy or flow-control subprotocols.

## Reviewer Focus

- Repository owner and status filters remain inside SQL and preserve cursor pagination.
- Lease registration, heartbeat, release, expiry, and sweeper cleanup are idempotent.
- `connection_epoch` is checked before local or remote close.
- Remote close uses authenticated Server-node relay and preserves leases on remote-node failure.
- Metadata decoding is bounded and cannot bypass Token/Agent Policy authorization.
- MySQL 5.6 and SQLite DDL remain syntax-compatible and expand-only.
- Old Client compatibility paths are covered by the CLI, E2E, and missing-hello regression tests.
- Client list Agent filtering uses exact JSON string matching so `agent-1` cannot match `agent-12`.
- Server observability handles a missing observability service without panicking.
- The real `client run` path forwards instance, listener, build, Agent, and allowlisted metadata fields to every session-pool connection.
- Client-side metadata construction also rejects duplicate names and aggregate payloads above 32 KiB before any frame is encoded.
- Default instance identity paths remain writable for packaged Linux, macOS, and Windows services.

## Integration Status

- Backend, schema, OpenAPI, frontend, and documentation are implemented and verified in this working tree.
- SQLite migration and repository integration pass.
- Live MySQL contract execution remains pending because no test DSN is configured.
