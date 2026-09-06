# Final Integration Report

## Metadata session integration

- `AgentSessionConfig.MetadataService` now persists authenticated Agent metadata frames through `AgentMetadataService.Upsert`.
- Agent and node identity are fenced against the authenticated session; revision overflow and service validation failures return structured ACK errors without closing the data session.
- `ServeAgentSession` now uses the configured service path, so accepted frames are visible through the SQLite repository and `/api/v1/agents/{agentId}/metadata`.
- Session replacement uses identity-aware cleanup: closing an old epoch cannot stale a newer replacement. Removing the current session marks its metadata stale while holding the manager lock, preventing a same-epoch reconnect from racing with stale marking.
- `NewAgentSessionManagerWithMetadata` provides the production wiring point: server startup can pass `storage.DB.Metadata()` (or another repository) once, then reuse the manager for every `ServeAgentSession` call.
- `NewServerRuntime` is the process-level startup factory used by `tunnelmesh-server run`; it opens the configured DB, constructs the metadata-backed Agent session manager, and leaves WebSocket serving to the existing transport integration.

## Schema compatibility

- Schema version is now `2`.
- Auto-init migrates existing version-1 databases after applying the portable DDL.
- `auto-init=false` validates both schema version and required tables, including `agent_runtime_metadata`, and fails fast when the runtime metadata table is missing.

## Deliverables

The final commit includes the requested project instructions, Docker/Compose files, deployment and operations guides, user guides, and metadata/SSH plan/spec artifacts without deleting existing content.

## Verification

- `go test ./... -count=1`
- `go test -race ./...`
- `go vet ./...`
- `git diff --check`
- `cd web && npm test -- --run`
- `cd web && npm run build`

Docker/Compose validation was not run because the environment does not have the `docker` executable.
