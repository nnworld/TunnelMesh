# TunnelMesh Platform Implementation Plan

> For agentic workers: REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox syntax for tracking.

**Goal:** Build a Go TunnelMesh platform with Agent WebSocket tunnels, User Client forwarding, HTTP wildcard hosting, TCP-over-WebSocket SSH bridging, local SQLite, MySQL/etcd cluster mode, Vue administration, Docker delivery, and TDD coverage.

**Architecture:** Separate Server, Agent Client, and tunnelmesh-client binaries share protocol and configuration interfaces. MySQL/SQLite store management data; MySQL leases or etcd track runtime ownership; mTLS gRPC/HTTP2 relays inter-node streams. Public Server exposure is limited to 80/443.

**Tech Stack:** Go; Cobra + Viper; gorilla/websocket; gRPC; database/sql; Vue 3 + TypeScript + Vite + Pinia + Vue Router + Element Plus; Prometheus; Docker Compose.

**Spec:** docs/superpowers/specs/2026-09-06-tunnelmesh-platform-design.md

## Global Constraints

- Use TDD: write a failing test, run it, implement the smallest change, then run focused and package tests.
- Keep Handler -> Service -> DAO boundaries and interface dependencies.
- Maintain exactly one migrations/ddl.sql compatible with SQLite and MySQL.
- Configuration precedence is command-line > environment > config file > defaults; no file starts local SQLite mode.
- Cluster storage is MySQL; registry defaults to database leases and is switchable to etcd.
- Public Server listeners are 80/443; managed public protocols are HTTP, HTTPS, WebSocket, and TCP-over-WebSocket; public UDP is unsupported.
- Dynamic host format is agent-id-ip-text-port with IPv4 dots replaced by hyphens; Agent IDs cannot contain hyphens.
- Never log secrets, passwords, tokens, or complete hardware fingerprints.
- All external connections require timeouts, bounded retries with jitter, backpressure, and explicit limits.
- Dockerfile, local/cluster Compose, health checks, and operator documentation are required.
- Do not commit, push, merge, or create tags automatically.

### Task 1: Bootstrap repository, rules, and build skeleton

**Files:** AGENTS.md, CLAUDE.md, README.md, go.mod, Makefile, .gitignore, cmd/tunnelmesh-server/main.go, cmd/tunnelmesh-agent/main.go, cmd/tunnelmesh-client/main.go, internal/build/build_test.go

**Interfaces:** Produces three buildable commands and project rules consumed by all later tasks.

- [ ] Step 1: Write a failing build test that expects the three binary names from build metadata.
- [ ] Step 2: Run go test ./internal/build -v and verify failure because packages and metadata do not exist.
- [ ] Step 3: Add go.mod, command entrypoints, Makefile targets test/race/lint/build/web-build/docker-build, full AGENTS.md rules, and a CLAUDE.md that only points to AGENTS.md.
- [ ] Step 4: Run go test ./..., go vet ./..., and go build ./cmd/...; verify all commands compile.
- [ ] Step 5: Inspect git diff for secrets and leave changes uncommitted.

### Task 2: Configuration and CLI framework

**Files:** internal/config/config.go, internal/config/config_test.go, internal/cli/root.go, internal/cli/root_test.go, cmd/*/main.go

**Interfaces:** Produces config.Load(ctx, ConfigOptions) (Config, error), config.Validate(Config) error, and Cobra roots.

- [ ] Step 1: Test defaults: no file/environment yields local mode, SQLite, auto_init true, registry database, and TCP Bridge enabled.
- [ ] Step 2: Test file, environment, and CLI precedence; assert CLI > environment > file > defaults.
- [ ] Step 3: Test invalid cluster config without MySQL DSN/TLS/node identity and invalid registry values.
- [ ] Step 4: Run focused tests and verify failure.
- [ ] Step 5: Implement typed config structs and explicit validation with Viper.
- [ ] Step 6: Implement server run/check-config/init-db/print-config; agent run/register/check-config/id; client login/agent/tunnel/forward/publish/proxy/stop/status shells.
- [ ] Step 7: Run go test ./internal/config ./internal/cli -v and go test ./...; verify pass.

### Task 3: Shared DDL and storage repositories

**Files:** migrations/ddl.sql, internal/storage/db.go, internal/storage/models.go, internal/storage/repository.go, internal/storage/sqlite.go, internal/storage/mysql.go, internal/storage/storage_contract_test.go, internal/storage/sqlite_test.go, internal/storage/mysql_test.go

**Interfaces:** Produces UserRepository, TokenRepository, AgentRepository, PolicyRepository, TunnelRepository, NodeRepository, LeaseRepository, and AuditRepository.

- [ ] Step 1: Write repository contract tests for users, agents, policies, tunnels, nodes, leases, audits, cursor pagination, and idempotency keys.
- [ ] Step 2: Run SQLite contract tests and verify failure.
- [ ] Step 3: Add the single SQLite/MySQL-compatible ddl.sql with string IDs, TEXT JSON, INTEGER booleans, IF NOT EXISTS, and required tables.
- [ ] Step 4: Implement database opening, embedded DDL execution, schema_meta versioning, and database/sql repositories.
- [ ] Step 5: Run SQLite contract tests and verify pass.
- [ ] Step 6: Add MySQL integration tests guarded by TUNNELMESH_TEST_MYSQL_DSN and run the same contract in CI.
- [ ] Step 7: Run go test ./internal/storage -v and go test -race ./internal/storage.

### Task 4: Authentication and administrator bootstrap recovery

**Files:** internal/auth/service.go, internal/auth/password.go, internal/auth/bootstrap.go, internal/auth/auth_test.go, internal/auth/bootstrap_test.go, internal/cli/root.go

**Interfaces:** Produces AuthService.Login, ValidateToken, CreateUser, BootstrapService.EnsureAdmin, and RegenerateCredentials.

- [ ] Step 1: Test first-start generation creates exactly one admin, stores only a password hash, returns printable credentials once, and is concurrent-safe.
- [ ] Step 2: Test regeneration requires confirm, obtains a recovery lock, revokes old sessions, writes an audit record, and invalidates old credentials.
- [ ] Step 3: Run focused tests and verify failure.
- [ ] Step 4: Implement Argon2id password hashing, opaque API-token hashing, role checks, bootstrap transaction, recovery lock, and console-safe rendering.
- [ ] Step 5: Add tunnelmesh-server admin regenerate-credentials --config PATH --confirm; never expose anonymous HTTP recovery.
- [ ] Step 6: Run go test ./internal/auth -v and go test -race ./internal/auth.

### Task 5: Versioned WebSocket protocol and stream state machines

**Files:** internal/protocol/frame.go, internal/protocol/codec.go, internal/protocol/stream.go, internal/protocol/udp.go, internal/protocol/protocol_test.go, internal/protocol/fuzz_test.go

**Interfaces:** Produces Frame, Encoder, Decoder, StreamState, and UDPAssociation APIs for server, agent, client, and relay.

- [ ] Step 1: Test valid/invalid frame encoding, maximum payload, unknown version/type, truncated input, stream transitions, half-close, reset, UDP boundaries, and window updates.
- [ ] Step 2: Run go test ./internal/protocol -v and verify failure.
- [ ] Step 3: Implement binary headers, bounded decoder, and stream state validation.
- [ ] Step 4: Add decoder and state-machine fuzz targets that reject malformed input without panic.
- [ ] Step 5: Run focused, race, and fuzz tests.

### Task 6: Registry implementations and cluster ownership

**Files:** internal/registry/registry.go, internal/registry/database.go, internal/registry/etcd.go, internal/registry/registry_contract_test.go, internal/registry/database_test.go, internal/registry/etcd_test.go

**Interfaces:** Produces NodeRegistry with Register, KeepAlive, ResolveAgent, Watch, Revoke; Lease and NodeOwner include epoch/fencing metadata.

- [ ] Step 1: Write contract tests for acquisition, renewal, expiration, takeover, fencing rejection, watch events, and revoke.
- [ ] Step 2: Run contract tests and verify failure.
- [ ] Step 3: Implement MySQL lease transactions with conditional updates and epoch increments using agent_runtime_leases and server_nodes.
- [ ] Step 4: Implement etcd Lease, CAS, Watch, and keys /tunnelmesh/nodes/<node-id> and /tunnelmesh/agents/<agent-id>.
- [ ] Step 5: Add integration tests guarded by TUNNELMESH_TEST_MYSQL_DSN and TUNNELMESH_TEST_ETCD_ENDPOINTS.
- [ ] Step 6: Run focused, race, and integration tests; verify config defaults to database and switches to etcd.

### Task 7: Agent, Server, Client sessions and inter-node relay

**Files:** internal/server/session_manager.go, internal/server/ws_agent.go, internal/server/ws_client.go, internal/server/session_test.go, internal/agent/session.go, internal/agent/dialer.go, internal/client/session.go, internal/relay/service.go, internal/relay/transport.go, internal/relay/relay_test.go

**Interfaces:** Produces AgentSessionManager, ClientSessionManager, RelayService.OpenStream, and transport adapters over WebSocket and gRPC.

- [ ] Step 1: Test authenticated Agent registration, capability negotiation, heartbeat, GOAWAY, User stream open, and local routing.
- [ ] Step 2: Test cross-node relay, epoch validation, backpressure, and node disconnect.
- [ ] Step 3: Run focused tests and verify failure.
- [ ] Step 4: Implement WS handshake/auth, bounded writers, stream dispatch, reconnect backoff with jitter, and gRPC mTLS relay.
- [ ] Step 5: Implement Agent TCP, UDP, and HTTP target dialers with timeouts and policy hooks.
- [ ] Step 6: Run package and race tests for server, agent, client, and relay.

### Task 8: Routing, managed HTTP, dynamic wildcard, and TCP Bridge

**Files:** internal/routing/parser.go, internal/routing/matcher.go, internal/routing/policy.go, internal/routing/routing_test.go, internal/server/http_proxy.go, internal/server/tcp_bridge.go, internal/server/http_proxy_test.go, internal/server/tcp_bridge_test.go

**Interfaces:** Produces ParseDynamicHost, RouteResolver.ResolveHTTP, and TCPBridgeHandler for /ws/tcp.

- [ ] Step 1: Test exact/path, exact-domain, explicit wildcard, dynamic wildcard, and not-found precedence; parse agent-id-ip-text-port IPv4 hosts.
- [ ] Step 2: Test CIDR/port allowlists, dangerous-address rejection, malformed hosts, hyphenated IDs, and DNS label limits.
- [ ] Step 3: Test HTTP reverse proxy and WebSocket Upgrade over Agent logical streams.
- [ ] Step 4: Test TCP Bridge with echo server, fragmented binary WS messages, half-close, refusal, timeout, and 64 KiB limits.
- [ ] Step 5: Run focused tests and verify failure.
- [ ] Step 6: Implement Host/path matching, TLS termination integration, ReverseProxy transport, and one-WS-to-one-TCP Bridge.
- [ ] Step 7: Run package and race tests; verify SSH-compatible raw byte integrity.

### Task 9: tunnelmesh-client local forward and stdio proxy

**Files:** internal/client/listeners.go, internal/client/forward.go, internal/client/udp_assoc.go, internal/client/stdio_proxy.go, internal/client/forward_test.go, cmd/tunnelmesh-client/main.go

**Interfaces:** Produces forward tcp|udp|http, publish http, proxy tcp, tunnel stop/status, and config-file tunnel loading.

- [ ] Step 1: Test local TCP listener lifecycle, UDP source-to-association mapping and idle expiry, HTTP local proxy, stdio byte copying, and reconnect.
- [ ] Step 2: Run focused tests and verify failure.
- [ ] Step 3: Implement local listeners, stream/UDP association requests, bounded copy loops, signal handling, and graceful shutdown.
- [ ] Step 4: Implement proxy tcp for SSH ProxyCommand and document websocat compatibility.
- [ ] Step 5: Run go test ./internal/client -v and -race.

### Task 10: Server API, Web admin, and managed routes

**Files:** internal/server/api.go, internal/server/api_test.go, internal/server/middleware.go, web/package.json, web/vite.config.ts, web/src/main.ts, web/src/router.ts, web/src/stores/auth.ts, web/src/views/Login.vue, web/src/views/Dashboard.vue, web/src/views/Agents.vue, web/src/views/Routes.vue, web/src/views/Tunnels.vue, web/src/views/AuditLogs.vue, web/src/api/client.ts, web/src/tests/routes.spec.ts, docs/api/openapi.yaml

**Interfaces:** Produces /api/v1 resources, unified {code,msg,data} responses, cursor pagination, Idempotency-Key handling, RBAC, and embedded web assets.

- [ ] Step 1: Test login, token validation, admin/user authorization, Agent/policy/route/tunnel CRUD, conflict validation, pagination, and idempotency.
- [ ] Step 2: Run Go API tests and frontend tests and verify failure.
- [ ] Step 3: Implement Handler -> Service -> Repository flow and OpenAPI definitions.
- [ ] Step 4: Build Vue pages with Element Plus for Agent policies, explicit subdomain routes, dynamic wildcard routes, and audit views.
- [ ] Step 5: Embed the production web build with SPA history fallback and isolated WS paths.
- [ ] Step 6: Run API tests, frontend tests, npm run build, and browser smoke tests.

### Task 11: Observability, health, Dockerfile, Compose, and operations docs

**Files:** internal/observability/logging.go, internal/observability/metrics.go, internal/server/health.go, Dockerfile, .dockerignore, docker-compose.local.yml, docker-compose.cluster.yml, deploy/docker/README.md, deploy/systemd/tunnelmesh-server.service, deploy/systemd/tunnelmesh-agent.service, docs/architecture/overview.md, docs/architecture/cluster.md, docs/protocol/websocket.md, docs/deployment/local.md, docs/deployment/cluster-mysql.md, docs/deployment/cluster-etcd.md, docs/user-guide/client.md, docs/user-guide/managed-http-route.md, docs/user-guide/tcp-over-websocket-ssh.md, docs/operations/configuration.md, docs/operations/troubleshooting.md

**Interfaces:** Produces structured logging, Prometheus metrics, health handlers, Docker images, Compose environments, and operator documentation consumed by release and E2E tasks.

- [ ] Step 1: Test readiness transitions for database, schema, registry lease, and certificate expiry; test structured log fields and metric registration.
- [ ] Step 2: Run focused tests and verify failure.
- [ ] Step 3: Implement structured logging, Prometheus metrics, /health/live, /health/ready, /metrics, and graceful GOAWAY shutdown.
- [ ] Step 4: Write a multi-stage Dockerfile with web-build, go-build, and non-root runtime stages; support ARG APP=server|agent|client, amd64/arm64, CA certificates, and healthcheck.
- [ ] Step 5: Write local Compose with Server + SQLite + Agent + Client and cluster Compose with multiple Server + MySQL + optional etcd; keep secrets external.
- [ ] Step 6: Document 80/443 routing, wildcard DNS/TLS, SSH ProxyCommand with websocat, admin recovery, lease modes, backup/restore, and troubleshooting.
- [ ] Step 7: Run Go tests, frontend build, and all three Docker builds; verify non-root startup and health checks.

### Task 12: End-to-end validation and CI quality gate

**Files:** internal/e2e/local_test.go, internal/e2e/cluster_test.go, internal/e2e/ssh_bridge_test.go, .github/workflows/ci.yml, README.md

**Interfaces:** Produces repeatable local/cluster acceptance suites and CI gates.

- [ ] Step 1: Test no-config local startup, SQLite auto-init, generated admin credentials, Agent registration, and Client TCP/UDP/HTTP forwarding.
- [ ] Step 2: Test MySQL with two Servers, Agent on Server-A, request on Server-B, database lease routing, and relay.
- [ ] Step 3: Test etcd switch, ownership watch/fencing, and rejection of new cross-node streams when etcd is unavailable.
- [ ] Step 4: Test websocat-compatible binary WS and SSH-like handshake bytes through TCP Bridge.
- [ ] Step 5: Run E2E tests with disposable dependencies and verify failure before final wiring.
- [ ] Step 6: Implement CI format, lint, unit, race, vet, integration, fuzz smoke, frontend build, and three Docker builds; publish no artifacts automatically.
- [ ] Step 7: Run the full checklist, inspect git diff for secrets, and report exact verification evidence without committing.

## Execution Notes

Implement tasks in order because each task produces interfaces consumed by later tasks. Keep every task independently testable. If a requirement expands beyond the spec, update the design and plan before coding. Use verification-before-completion before claiming success.
