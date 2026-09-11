# PR: Reduce SOCKS5 web-page latency

## Target Branch

`main`

## Summary

This change implements the P0/P1/P2 SOCKS5 web-page latency work:

- Adds strict `OPEN_RESULT` negotiation and stable failure codes across Client, Server, Agent, and cross-server relay paths.
- Adds bounded asynchronous Agent dialing, per-stream error isolation, flow control, and fair scheduling so one slow or failed target cannot block other page resources.
- Adds database-backed authorization revision invalidation, a server-side authorization cache, and a client-side remote-validation cache.
- Extends logical Agent connection-pool signals with pending dials, open/TTFB/writer-queue latency, and local-agent affinity.
- Extends Schema v8 to v9 with the expand-only `authorization_revision` table.
- Updates the single Grafana dashboard and user/operations documentation.

## User Impact

- SOCKS5 CONNECT now reports success only after the complete negotiated path confirms that the Agent established the target connection.
- One failed or blocked target affects only that stream; concurrent requests and the Agent WebSocket session remain usable.
- Page loading benefits from bounded queues, flow control, control-frame priority, fair DATA scheduling, and local Agent preference.
- MySQL clusters can use a longer positive authorization cache TTL while a shared revision keeps revocation propagation bounded by the poll interval.

## API / Schema / Configuration Impact

- No new public management API is introduced by this change.
- WebSocket subprotocols are negotiated in this order: `tunnelmesh.v1.open-result.flow-control`, `tunnelmesh.v1.open-result`, and legacy `tunnelmesh.v1`.
- Schema version advances from 8 to 9. `migrations/ddl.sql` and the immutable MySQL/SQLite v8-to-v9 incremental scripts both create `authorization_revision`.
- New configurable bounds cover server stream opens/windows, Agent dial concurrency/queues/timeouts, Client stream buffers/open timeout, authorization cache TTLs, and remote-validation cache TTLs.
- Defaults preserve the existing legacy-compatible behavior and Agent connection-pool `min=1,max=1`.

## Security and Authorization Impact

- Strict open never fabricates success when any negotiated path segment lacks the required capability.
- Authorization changes share a transaction with revision advancement; remote nodes invalidate cache entries through the shared revision.
- Cache and remote-validation failures fail closed where required and never cache credentials or response bodies.
- Prometheus labels remain bounded and do not expose target host/IP/port, token, username, password, stream ID, or connection ID.
- Existing SSRF, policy, CIDR, port, ownership, and token-scope checks remain in force.

## Test Evidence

Fresh verification on the final working tree:

```text
go test ./internal/e2e -run 'TestSOCKS5WebPageLatency' -count=1
ok github.com/tunnelmesh/tunnelmesh/internal/e2e

go test ./... -count=1
ok all packages with tests; cmd/* and migrations report no test files

go test ./... -race
ok all packages with tests; internal/server completed in 164.918s

go vet ./...
exit 0

go build ./cmd/...
exit 0

git diff --check
exit 0
```

Additional focused regressions after the initial full race run found and fixed two issues:

```text
go test -race ./internal/cli -run 'TestAgentRunCommandForwardsTCPUDPAndHTTPThroughRealDispatcher' -count=10
ok github.com/tunnelmesh/tunnelmesh/internal/cli

go test -race ./internal/server -run 'TestAgentSessionGoAwayAndBackpressure' -count=100
ok github.com/tunnelmesh/tunnelmesh/internal/server
```

`TUNNELMESH_TEST_MYSQL_DSN` was not set, so MySQL contract tests were skipped by design. SQLite contract and migration tests ran as part of the full suite. MySQL execution remains pending until a test DSN is provided.

The E2E test was also checked against a clean temporary baseline copy with only the new test file added. It failed during setup because the new stream, cache, capability, and dispatcher types did not exist; the same test passes on the final implementation tree.

No files under `web/` or `internal/server/web_dist/` changed, so frontend build/test verification is not applicable.

## Release Steps

1. Back up SQLite/MySQL and verify that the backup can be restored.
2. Roll all Server nodes to this version with `storage.auto_init=true`, advancing Schema v8 to v9.
3. Verify every Server reports `/health/ready`, `schema_meta.version=9`, and a healthy authorization revision source.
4. Roll Agents and Clients to this version while keeping Agent `connections.max=1`.
5. Confirm strict-open capability negotiation, stream latency metrics, authorization revision polling, and legacy compatibility.
6. Only after every Server supports the new connection-pool metadata, gradually increase Agent `connections.max` if needed.

## Rollback Steps

1. Stop new traffic and roll applications back to the previous v8-compatible binaries; retain the v9 `authorization_revision` table.
2. Drain existing strict connections and let Clients reconnect using the legacy subprotocol.
3. If authorization behavior must be isolated, disable `server.authorization_cache.enabled`; authentication and authorization checks remain enforced.
4. Reset Agent connection pool settings to `min=1,max=1`.
5. If exact schema restoration is mandatory, restore the validated pre-upgrade database backup instead of issuing guessed reverse DDL.

## Reviewer Focus

- Verify strict capability negotiation covers the complete Client → Server → Agent/remote-Server path and returns `unsupported_capability` instead of synthetic success.
- Check per-stream cleanup for duplicate IDs, early DATA, late control frames, half-close, timeout, reset, and stale generations.
- Confirm all authorization mutations advance the shared revision in the same transaction.
- Confirm queue/window defaults cannot create unbounded memory growth.
- Confirm migration scripts are idempotent and preserve `schema_meta` ordering.
- Confirm dashboard PromQL uses only bounded labels and remains one dashboard.

## Integration Status

- Backend implementation and documentation are complete and verified in this working tree.
- MySQL contract verification is pending because no test DSN is configured.
- Production probe execution remains intentionally bounded: the HTTP API is routed, but no default Agent TCP/HTTP/UDP executor is injected, so probes return `unsupported` rather than fabricating success.
- The work is uncommitted; no commit, push, merge, or remote PR operation was performed.
