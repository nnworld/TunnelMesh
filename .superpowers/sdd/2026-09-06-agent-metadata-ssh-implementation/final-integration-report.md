# Final Integration Report

## Metadata session integration

- `AgentSessionConfig.MetadataService` now persists authenticated Agent metadata frames through `AgentMetadataService.Upsert`.
- Agent and node identity are fenced against the authenticated session; revision overflow and service validation failures return structured ACK errors without closing the data session.
- `ServeAgentSession` now uses the configured service path, so accepted frames are visible through the SQLite repository and `/api/v1/agents/{agentId}/metadata`.
- Session replacement uses identity-aware cleanup: closing an old epoch cannot stale a newer replacement. Removing the current session marks its metadata stale while holding the manager lock, preventing a same-epoch reconnect from racing with stale marking.
- `NewAgentSessionManagerWithMetadata` provides the production wiring point: server startup can pass `storage.DB.Metadata()` (or another repository) once, then reuse the manager for every `ServeAgentSession` call.
- `NewServerRuntime` is the process-level startup factory used by `tunnelmesh-server run`; it wires the management API, embedded Web UI, and authenticated `/ws/agent` endpoint. The CLI now binds the configured HTTP address and shuts down gracefully with command context cancellation.
- Agent WebSocket registration is derived from the authenticated metadata hello (`agent_id`, `node_id`, `epoch`) plus the bearer token. The CLI runtime validates that bearer through `AuthService.ValidateToken`; embedders can inject an equivalent `AgentSessionConfig.Authenticate` policy. Accepted hello frames persist metadata with a five-minute expiry by default.
- The built-in HTTP listener does not terminate TLS; production deployments must use a reverse proxy or load balancer for HTTPS/WSS termination.
- `tunnelmesh-agent run` now uses the configured `agent.server_url`, `agent.id`, `agent.token`, and allowlisted metadata collector to establish the authenticated binary WebSocket session. Missing URL, token, or identity fails fast; no unauthenticated fallback is provided.

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
