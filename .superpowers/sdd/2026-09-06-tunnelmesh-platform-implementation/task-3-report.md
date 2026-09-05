# Task 3 Report: Shared DDL and Storage Repositories

## Status

Implemented and locally verified in the task worktree. The storage package now exposes database/sql-backed repositories for users, API tokens, agents, policies, tunnels, nodes, leases, audit logs, and idempotency records.

## Delivered

- Added the single portable `migrations/ddl.sql` with `IF NOT EXISTS`, string identifiers, TEXT JSON payloads, INTEGER booleans, and all core tables from the design (`schema_meta`, `users`, `api_tokens`, `agents`, `agent_policies`, `tunnel_groups`, `tunnels`, `server_nodes`, `agent_runtime_leases`, `audit_logs`, and `idempotency_keys`).
- Added `migrations/embed.go` so storage executes the same DDL through `go:embed` without duplicating the schema.
- Added `storage.Open`, `OpenConfig`, `OpenSQLite`, and `OpenMySQL` with context-aware pinging, SQLite single-connection handling for in-memory databases, automatic schema initialization, schema version checks, and an explicit auto-init-disabled failure path.
- Added repository interfaces and constructors for all eight requested repository families, CRUD operations, token revocation, lease acquire/renew/release with epoch fencing, audit persistence, idempotency records, and cursor-based pagination.
- Added SQLite contract coverage and an environment-gated MySQL contract test using `TUNNELMESH_TEST_MYSQL_DSN`.

## Verification

- `go test ./internal/storage -v -count=1` — pass; MySQL integration test skipped because `TUNNELMESH_TEST_MYSQL_DSN` is unset.
- `go test -race ./internal/storage -count=1` — pass.
- `go test ./... -count=1` — pass.
- `go vet ./...` — pass.
- `git diff --check` — pass.

## Concerns / follow-ups

- A live MySQL integration run was not possible in this environment because no `TUNNELMESH_TEST_MYSQL_DSN` was provided. The DDL avoids SQLite-only syntax and uses VARCHAR for indexed string identifiers so it can be exercised in CI against MySQL.
- Repository APIs intentionally keep clear-text secrets out of storage models; callers provide password/token hashes.
