# Client lease epoch width and observability sync

## Title

`fix(server): keep client lease epochs and observability in sync`

## Target branch

`main`

## Summary

On MySQL the client observability page reported a busy, connected client as
disconnected with expired metadata. One narrow column caused all of it.

`internal/server/ws_client.go:553` derives `connection_epoch` from eight random
bytes, so the Client fencing token is a full `int64`. `migrations/ddl.sql` and
`migrations/incremental/v0010_to_v0011/mysql.sql` declared
`client_connection_leases.connection_epoch` as `INTEGER`, which MySQL stores as a
signed 32-bit value and **clamps** to `2147483647` on insert. Every later write
filters on `WHERE connection_id=? AND connection_epoch=?` using the true
in-memory token, so `Renew`, `UpdateStats`, and `Release` all matched zero rows
and returned `sql.ErrNoRows`.

That single failure cascaded:

1. `Renew` never extended `expires_at`, so the lease lapsed after
   `DefaultClientConnectionLeaseTTL` (90s) while the WebSocket stayed open.
   `newClientView` (`internal/server/client_api.go:260`) counts a connection only
   while `ExpiresAt.After(now)`, so `activeConnections`, `activeStreams`, and
   `serverNodeIds` all came back empty.
2. `ClientObservabilityService.Heartbeat` returned early on the lease error, so
   `TouchInstance` never ran. `client_instance_metadata.expires_at` lapsed after
   `DefaultClientMetadataTTL` (5m) and `ClientMetadataSweeper` set `stale=1`,
   which `newClientView` renders as `status=stale` — "metadata 已过期" in the
   console.
3. The summary cards derive entirely from the list rows
   (`web/src/views/Clients.vue:196`), so they showed zeros too.
4. The detail drawer lists every stored lease row without a liveness marker, so
   it showed four connections next to a list row claiming zero — the visible
   contradiction that made this reportable.

SQLite was never affected because its `INTEGER` is already 64-bit, which is why
the unit tests missed it; the MySQL contract tests are gated behind
`TUNNELMESH_TEST_MYSQL_DSN` and do not run in CI.

## User impact

- Client list rows show the real active connection count, active stream count,
  and serving Server node again.
- A connected client no longer drifts into "metadata expired" merely because a
  lease write failed.
- The detail drawer labels each lease 活跃/已过期 (Live/Expired) and only offers
  "Close connection" on a live lease. Closing an expired one could only ever
  return `stale_epoch` or `remote_node_unavailable`.
- The summary gains a fifth card, "metadata 已过期" / "Metadata expired", so the
  card set now covers every status the table can display instead of silently
  omitting the one users were actually seeing.
- Already-connected clients recover on their next heartbeat (default 30s)
  **without reconnecting**, thanks to the self-heal described below.

## API, schema, and configuration impact

- **Schema v14 → v15.** New `migrations/incremental/v0014_to_v0015/` widens
  `connection_epoch` to `BIGINT NOT NULL` on `client_connection_leases` **and**
  `agent_connection_leases`. `migrations/ddl.sql` is updated for both, and
  `SchemaVersion` becomes `15`.
  - The agent table is included because `v0006_to_v0007/mysql.sql` already
    declared `BIGINT` while `ddl.sql` declared `INTEGER`: a fresh MySQL install
    and an upgraded one disagreed about the authoritative schema. Agent epochs
    are small counters today, so this removes latent drift rather than a live
    failure.
  - The SQLite step is structurally inert (SQLite cannot alter a column type and
    does not need to) and runs one idempotent no-op statement, because
    `applySchemaStatements` splits on the statement separator and would otherwise
    hand a comment-only fragment to the driver.
- No API contract change. `ClientConnectionView` and `ClientView` keep their
  fields, so `docs/api/openapi.yaml` needs no edit.
- No configuration change; heartbeat interval and both TTLs are untouched.
- Widening is backward compatible for readers, so this is a MINOR-level schema
  change under the project's versioning rules.

## Security impact

No change to authentication or authorization.

The fencing guarantee is preserved, not relaxed. `Renew` still requires an exact
epoch match; the new fallback triggers only when that match finds **no row**, and
it re-registers through `clientConnectionRepo.Register`, which keeps its
`connection_id` ownership check and its `connection_epoch >= existingEpoch`
monotonicity guard. A stale generation therefore still cannot overwrite a newer
one, and a connection ID owned by a different client instance is still rejected.

`Release` deliberately keeps its strict epoch predicate and gains no fallback:
letting a stale generation delete a newer lease is exactly what fencing exists to
prevent. If a connection closes before its first post-upgrade heartbeat, its
orphan row is reclaimed by the 90s TTL rather than by an unfenced delete.

The self-heal is also scoped so it cannot mask real failures — only
`sql.ErrNoRows` triggers a re-register; any other storage error propagates
unchanged. No new data is exposed, and no secret, token, or DSN is logged.

## Changes

- `migrations/incremental/v0014_to_v0015/mysql.sql`, `sqlite.sql`: the new
  adjacent migration, with the rationale and retry semantics in comments.
- `migrations/embed.go`: `V14ToV15MySQL` / `V14ToV15SQLite` embeds.
- `migrations/ddl.sql`: both `connection_epoch` columns to `BIGINT`.
- `internal/storage/db.go`: `SchemaVersion = 15` and the `case 14` branch.
- `internal/server/client_connection_lease.go`: `Heartbeat` re-registers with the
  authoritative in-memory epoch when `Renew` matches no row, and returns early on
  that path because `Register` already wrote the fresh expiry and counters.
- `internal/server/client_observability.go`: `Heartbeat` performs the lease write
  and the metadata touch independently and joins both errors, so a lease failure
  can no longer suppress the metadata refresh.
- `web/src/views/Clients.vue`: lease-state column, close-action guard, a fixed
  per-load liveness baseline (`connectionsNow`) so a row cannot flip mid-render,
  the fifth summary card, and the grid width.
- `web/src/i18n/messages/zh-CN.ts`, `en-US.ts`: `clients.metadataStale`,
  `clients.leaseState`, `clients.leaseStateLabel.{live,expired}`.
- `internal/storage/mysql_test.go`: `TestMySQLV14ToV15WidensConnectionEpoch`,
  a textual drift guard that runs without a MySQL DSN.
- `internal/storage/client_repository_test.go`: `TestSchemaVersionIs15` and
  `TestClientConnectionLeaseStoresFullInt64Epoch`, which round-trips an epoch
  above `MaxInt32` through Register → Renew → UpdateStats → Release.
- `internal/server/client_connection_lease_test.go`: self-heal on an epoch
  mismatch, a case proving non-epoch storage errors still propagate, and a case
  proving no orphan lease is registered before the instance ID is known.
- `internal/server/client_observability_test.go`: metadata is still touched while
  the lease write fails, and the lease error stays observable.
- `web/src/tests/clients-view.spec.ts`: new spec covering card values and labels,
  the all-zero case, the lease-state rendering in the drawer, the close-action
  guard, and locale key parity.
- `docs/operations/schema-upgrades.md`: the v14 → v15 section, lock impact,
  verification SQL, self-heal window, and rollback rules.
- `docs/operations/troubleshooting.md`: a diagnostic entry keyed on the
  "every connection shows epoch 2147483647" signature, plus a Schema v15 section.
- `docs/superpowers/plans/2026-09-23-client-lease-epoch-observability-sync.md`:
  the implementation plan.

## Test evidence

```
go build ./...                     ok
go test ./... -count=1             23 packages ok, 0 failures
go test -race ./...                see below
go vet ./...                       clean
git diff --check                   clean
cd web && npm test -- --run        35 files, 296 tests passed
cd web && npm run build            built, dist mirrored to internal/server/web_dist
./scripts/verify-web-embed.sh      web/dist and internal/server/web_dist match
python3 scripts/gen_doc_index.py   regenerated
```

Red-light confirmation before implementation:

- `internal/storage`: `undefined: migrations.V14ToV15MySQL` /
  `SchemaVersion = 14, want 15`.
- `internal/server`: `heartbeat should self-heal an epoch mismatch, got sql: no
  rows in result set` and `metadata must still be refreshed while the lease write
  fails; touched = [][2]time.Time(nil)`.
- `web`: all 6 new cases failed, including `expected [ …(57) ] to include
  'metadataStale'`.

The MySQL-backed contract test remains gated: run
`TUNNELMESH_TEST_MYSQL_DSN=<dsn> go test ./internal/storage/ -run MySQL -count=1`
against a real instance to exercise the `ALTER` itself. The new textual guard
runs unconditionally, which is what keeps this class of drift visible in CI.

## Release steps

1. Back up the database and verify the backup restores.
2. Confirm `schema_meta.version=14`.
3. Deploy the new **Server** only, with `storage.auto_init: true`. The migration
   runs at startup and advances the version to `15`.
4. Verify `SHOW COLUMNS FROM client_connection_leases LIKE 'connection_epoch'`
   reports `bigint`.
5. Wait one heartbeat interval (default 30s) and confirm the client list shows
   non-zero active connections and the stale status clears.

**Agent and Client binaries do not need to be upgraded or restarted.** No
protocol frame, capability negotiation, or API contract changed.

Lock impact: both lease tables hold one row per live physical WebSocket, so they
are small. MySQL 8.0 can widen `INT → BIGINT` with `ALGORITHM=INPLACE`; 5.6/5.7
rebuild the table while allowing concurrent DML. Expect seconds.

## Rollback steps

Revert the Server binary and **keep the v15 schema**. A v14 binary reads the
`BIGINT` column as an `int64` and works normally.

Do not narrow the column back to `INTEGER`: that reintroduces the clamping and
would truncate any token already stored above the 32-bit range. If the exact v14
structure is required, restore the pre-upgrade backup rather than writing reverse
DDL, and never decrement `schema_meta.version` by hand.

Five-minute containment: if the new Server fails to start, redeploy the previous
binary. Service is restored without touching the database.

## Reviewer focus

1. Does `MODIFY connection_epoch BIGINT NOT NULL` behave idempotently on
   MySQL 5.6/5.7/8.0 when a partially applied migration is retried?
2. Can the self-heal race a concurrent `Release` and re-register a closing
   connection? The heartbeat goroutine is stopped and joined before `Release`
   runs in `ws_client.go`, and `connection_id` is unique per physical connection
   on one node, so the window appears closed — please confirm.
3. `ws_client.go:173` still discards the heartbeat error (`_ = ...`). With
   `errors.Join` the lease failure is now observable in the returned value but
   still not logged or metered. Should a follow-up add a counter?
4. Is one fixed `connectionsNow` per drawer load the right liveness baseline, or
   should the drawer re-evaluate on a timer for long-lived sessions?
5. Pre-existing and out of scope here: mounting `Clients.vue` logs
   `[intlify] Not found 'clients.statusLabel.undefined'`, reproduced on the
   unmodified file. Worth a separate look.

## Integration status

Built on `main` at `9ea5597`. No dependency on open branches; touches the client
observability path only, which PR #21 (agent connectivity) does not overlap.
