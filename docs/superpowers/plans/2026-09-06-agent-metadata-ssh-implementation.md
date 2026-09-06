# Agent Metadata and SSH Access Implementation Plan

> For agentic workers: REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox syntax.

**Goal:** Add allowlisted Agent host metadata reporting, persisted Server metadata APIs and Web display, plus passwordless SSH access through the existing TCP proxy.

**Architecture:** Agent collects explicit file/environment sources and sends a versioned snapshot over the authenticated Agent WebSocket. Server fences updates by Agent ID, epoch, and revision, stores the current runtime snapshot in a dedicated repository, and exposes owner/admin read APIs and an Agent detail view. SSH remains standard SSH: `tunnelmesh-client proxy tcp` carries bytes to host sshd while public-key or ssh-agent authentication remains on the host.

**Tech Stack:** Go, database/sql, SQLite/MySQL, versioned WebSocket frames, Cobra/Viper, Vue 3, TypeScript, Vite, Pinia, Vue Router, Element Plus, OpenAPI.

**Spec:** `docs/superpowers/specs/2026-09-06-agent-metadata-ssh-design.md`

## Global Constraints

- Use TDD: write a failing test, run it, implement the smallest change, then run focused and package tests.
- Keep Handler → Service → Repository boundaries; handlers must not access SQL or repositories directly.
- Maintain exactly one schema file: `migrations/ddl.sql`, compatible with SQLite and MySQL.
- Metadata sources are limited to explicit `file` and `env` entries; no arbitrary command execution.
- Enforce field, field-count, and total-payload limits before persistence; never log metadata values.
- Fencing uses authenticated Agent ID, epoch, and monotonic revision; duplicate updates are idempotent.
- Stale metadata remains readable as the last safe snapshot but is visibly marked stale.
- SSH authentication stays with host sshd; TunnelMesh never stores SSH private keys.
- Public Server exposure remains limited to 80/443; public UDP is not added.
- Do not commit, merge, or push automatically.

### Task 1: Agent metadata configuration and collector

**Files:** Modify `internal/config/config.go`, `internal/config/config_test.go`, `internal/agent/session.go`; create `internal/agent/metadata.go` and `internal/agent/metadata_test.go`.

**Interfaces:** Produce `config.MetadataSource` and `MetadataCollector.Collect(ctx) (MetadataSnapshot, error)`.

- [ ] Write failing tests for allowlisted file/env reads, missing sources, invalid names, relative paths, sensitive names, UTF-8, field limits, aggregate limits, and deterministic ordering.
- [ ] Run `go test ./internal/agent ./internal/config -run Metadata -count=1` and verify RED.
- [ ] Add `AgentConfig.Metadata`, validate `file`/`env` entries, bound reads, trim one trailing newline, and return per-field errors without closing the session.
- [ ] Run `go test ./internal/agent ./internal/config -count=1` and verify GREEN.

### Task 2: Metadata WebSocket protocol and session reporting

**Files:** Modify `internal/protocol/frame.go`, `internal/protocol/codec.go`, `internal/protocol/protocol_test.go`, `internal/server/session_manager.go`, `internal/server/ws_agent.go`, and `internal/agent/session.go`; create `internal/server/metadata_protocol_test.go`.

**Interfaces:** Produce `FrameAgentHello`, `FrameAgentMetadataUpdate`, `FrameAgentMetadataAck`, bounded payload encoding, and server metadata callbacks.

- [ ] Write failing tests for valid frames, unknown control types, oversized payloads, mismatched Agent ID/epoch, duplicate revisions, lower revisions, and field-level ACK errors.
- [ ] Run `go test ./internal/protocol ./internal/server -run Metadata -count=1` and verify RED.
- [ ] Add control frame types without changing existing numeric values; validate size before JSON decoding.
- [ ] Send a full snapshot after authenticated registration and after reconnect; send updates on revision changes; keep metadata errors separate from data streams.
- [ ] Fence by authenticated Agent ID and epoch, accept equal revision replay idempotently, and return structured ACK errors.
- [ ] Run `go test ./internal/protocol ./internal/server ./internal/agent -count=1` and the matching race tests.

### Task 3: Runtime metadata storage and Service layer

**Files:** Modify `migrations/ddl.sql`, `internal/storage/models.go`, `internal/storage/repository.go`, and `internal/storage/db.go`; create `internal/storage/metadata_test.go`, `internal/server/metadata_service.go`, and `internal/server/metadata_service_test.go`.

**Interfaces:** Produce `AgentRuntimeMetadata`, `AgentMetadataRepository`, and `AgentMetadataService` with `Upsert`, `Get`, `List`, and `MarkStale`.

- [ ] Write failing SQLite contract tests for insert/update, equal-revision replay, stale epoch/revision rejection, stale marking, and cursor pagination.
- [ ] Run `go test ./internal/storage -run RuntimeMetadata -count=1` and verify RED.
- [ ] Add portable `agent_runtime_metadata` DDL with Agent ID primary key, node/epoch/revision, JSON/TEXT metadata, timestamps, expiry, stale flag, and indexes.
- [ ] Implement explicit-column repository queries, epoch/revision fencing, idempotent replay, stale marking, and cursor pagination.
- [ ] Implement Service validation, JSON conversion, stale computation, and sensitive-value redaction.
- [ ] Run SQLite tests and, when `TUNNELMESH_TEST_MYSQL_DSN` exists, the same contract against MySQL.

### Task 4: Metadata API, OpenAPI, and audit behavior

**Files:** Modify `internal/server/api.go`, `internal/server/api_test.go`, `docs/api/openapi.yaml`; create `internal/server/metadata_api_test.go`.

**Interfaces:** Produce `GET /api/v1/agents/{agentId}/metadata` with optional `includeStale=true` and the unified `{code,msg,data}` envelope.

- [ ] Write failing tests for 401, owner/admin 200, unrelated user 403, missing Agent 404, stale response shape, redaction, and the absence of a metadata write endpoint.
- [ ] Run `go test ./internal/server -run MetadataAPI -count=1` and verify RED.
- [ ] Implement Handler → Service → Repository routing, owner/admin authorization, bounded response serialization, and audit read events.
- [ ] Update OpenAPI with metadata schemas, `includeStale`, stale semantics, and 401/403/404 responses.
- [ ] Run `go test ./internal/server ./internal/storage -count=1` and `go test ./... -count=1`.

### Task 5: Web后台 Agent metadata and server function guide

**Files:** Modify `web/src/api/client.ts`, `web/src/views/Agents.vue`, `web/src/router.ts`, `web/src/tests/routes.spec.ts`, `docs/README.md`, and `README.md`; create `web/src/views/AgentDetail.vue` and `docs/user-guide/server-admin.md`.

**Interfaces:** Produce an Agent detail route with metadata card and a complete server后台 function guide.

- [ ] Write failing frontend tests for the detail route, metadata endpoint call, field/source/value rendering, stale badge, redaction, and no edit control.
- [ ] Run `cd web && npm test -- --run src/tests/routes.spec.ts` and verify RED.
- [ ] Add typed API models, `/agents/:id`, loading/error/empty states, metadata table, stale state, timestamps, and masked values using Element Plus.
- [ ] Document login/bootstrap, dashboard, Agent list/detail/metadata, policy, explicit route, wildcard route, tunnel status, audit log, roles, idempotency, and credential recovery in `server-admin.md`.
- [ ] Run `cd web && npm test -- --run && npm run build`.

### Task 6: SSH documentation, integration coverage, and final verification

**Files:** Modify `docs/user-guide/client.md`, `docs/user-guide/agent.md`, `docs/user-guide/tcp-over-websocket-ssh.md`, and `README.md`; create `internal/e2e/agent_metadata_ssh_test.go`.

**Interfaces:** Produce SSH public-key proxy acceptance coverage and complete Agent/Client usage documentation.

- [ ] Write failing integration tests for TCP byte integrity, SSH-like handshake bytes, policy denial, reconnect cleanup, and SSH remote-command exit-code propagation.
- [ ] Run `go test ./internal/e2e -run AgentMetadataSSH -count=1` and verify RED.
- [ ] Ensure the existing TCP proxy carries SSH bytes, supports half-close/disconnect cleanup, and emits Agent/target/user audit context; do not add a second SSH protocol.
- [ ] Document Agent metadata configuration, TCP/UDP simultaneous forwarding, SSH public-key setup, ProxyCommand, remote commands, and the distinction from future command-exec.
- [ ] Run `go test ./... -count=1`, `go test -race ./...`, `go vet ./...`, `git diff --check`, `cd web && npm test -- --run && npm run build`.
- [ ] If Docker is available, run all three Docker builds and validate both Compose files; report unavailable external dependencies explicitly.

## Execution Notes

Implement tasks in order because each task produces interfaces consumed by later tasks. Keep metadata reporting independent from data forwarding so malformed or stale metadata cannot interrupt TCP/UDP/HTTP streams. If arbitrary command execution is requested later, stop and create a separate design.
