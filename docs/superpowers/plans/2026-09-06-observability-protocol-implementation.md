# Observability and Protocol Enhancements Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add production-grade Prometheus/Grafana observability and close the protocol and operational completeness gaps identified in the design.

**Architecture:** Introduce an `internal/observability` package with injected Prometheus registries, typed stage/heartbeat/stream/probe events, and bounded labels. Wire health and metrics handlers at the Server boundary, persist only bounded aggregates, and keep management data authoritative in SQL. Extend the wire protocol with capability-gated frames while preserving existing frame behavior for older peers.

**Tech Stack:** Go 1.23, `prometheus/client_golang`, SQLite/MySQL, etcd, protobuf/gRPC, Vue 3, Grafana dashboard JSON, Prometheus recording/alert rules.

**Spec:** `docs/superpowers/specs/2026-09-06-observability-protocol-design.md`

## Global Constraints

- Prometheus labels must never contain `token_id`, `connection_id`, `stream_id`, target address, raw metadata, response data, or secrets.
- `/health/live` must not depend on the database; `/health/ready` must return 503 on missing required dependencies.
- PING/PONG must be answered without synchronous database I/O.
- New protocol extensions are capability-gated and bounded; unknown non-critical extensions remain safely ignorable.
- All features use TDD: failing test, observed RED, minimal implementation, GREEN, refactor.
- Schema changes only modify `migrations/ddl.sql`; SQLite and MySQL behavior must be considered.
- No commit, push, merge, or destructive cleanup without explicit user authorization.

---

### Task 1: Observability primitives and redacted structured events

**Files:**
- Create: `internal/observability/metrics.go`
- Create: `internal/observability/metrics_test.go`
- Create: `internal/observability/events.go`
- Create: `internal/observability/events_test.go`
- Modify: `go.mod`

**Interfaces:**
- `NewMetrics(reg *prometheus.Registry) *Metrics`
- `Metrics.ObserveConnection(component, mode, result, errorClass string)`
- `Metrics.ObserveStage(component, stage, result, errorClass string, duration time.Duration)`
- `Metrics.ObserveHeartbeat(component, result string, rtt time.Duration)`
- `Metrics.ObserveBytes(component, direction, protocol string, n int64)`
- `Metrics.ObserveStream(protocol, result, errorClass string)`
- `Metrics.ObserveProbe(kind, result, errorClass string, duration time.Duration)`
- `Metrics.SetReady(component string, ready bool)`
- `StageEvent` and `NormalizeErrorClass(error error) string`

- [ ] Write failing tests asserting metric names, bounded labels, injected registry isolation, histogram observations, and redaction of secrets/target payloads.
- [ ] Run `go test ./internal/observability -count=1` and verify RED.
- [ ] Add the Prometheus dependency and implement the minimal collectors with fixed label vectors.
- [ ] Run focused tests, `go vet ./...`, and `git diff --check`.

### Task 2: Health, readiness, and Prometheus HTTP endpoints

**Files:**
- Create: `internal/server/health.go`
- Create: `internal/server/health_test.go`
- Modify: `internal/server/runtime.go`
- Modify: `internal/server/web.go`
- Modify: `internal/server/api.go`

**Interfaces:**
- `HealthChecker` with `Live(context.Context) error` and `Ready(context.Context) []ComponentStatus`.
- `NewHealthHandler(checker HealthChecker, metrics http.Handler) http.Handler`.
- Runtime routes `/health/live`, `/health/ready`, `/metrics` before SPA fallback.

- [ ] Add failing tests for live without DB, ready 503 on DB/registry failure, metrics content type, and route precedence.
- [ ] Run focused RED tests.
- [ ] Implement bounded health responses and an injected registry handler using `promhttp.HandlerFor`.
- [ ] Wire readiness state from runtime startup/close and avoid exposing DSN/cert/token data.
- [ ] Run server tests and race tests.

### Task 3: Runtime instrumentation for connections, heartbeats, streams, relay, and storage

**Files:**
- Modify: `internal/agent/websocket.go`
- Modify: `internal/agent/session.go`
- Modify: `internal/client/websocket.go`
- Modify: `internal/server/runtime.go`
- Modify: `internal/server/ws_agent.go`
- Modify: `internal/server/ws_client.go`
- Modify: `internal/relay/server_node_auth.go`
- Modify: `internal/storage/db.go`
- Create: `internal/observability/integration_test.go`

- [ ] Add failing integration assertions for connection counters, stage duration, heartbeat RTT/miss, bytes, stream result, relay auth failures, and storage errors.
- [ ] Emit immutable events at connection upgrade, token auth, Agent hello, heartbeat, logical OPEN, relay auth, and storage boundaries.
- [ ] Keep all metric labels within the allowlist; use token IDs only in redacted structured logs if needed.
- [ ] Verify existing behavior and run full Go tests/race.

### Task 4: Bounded runtime stats and probe persistence

**Files:**
- Modify: `migrations/ddl.sql`
- Modify: `internal/storage/db.go`
- Modify: `internal/storage/models.go`
- Modify: `internal/storage/repository.go`
- Create: `internal/storage/runtime_stats_test.go`
- Create: `internal/server/probe_service.go`
- Create: `internal/server/probe_service_test.go`

- [ ] Add RED contract tests for minute aggregates, retention, cursor ordering, epoch fencing, probe result persistence, and bounded error classes.
- [ ] Add `agent_runtime_stats` and `agent_probe_results` using portable SQL and explicit indexes.
- [ ] Implement repository and service methods with owner/admin authorization and no response bodies.
- [ ] Run SQLite contracts; record MySQL skip if DSN unavailable.

### Task 5: Capability-gated protocol extensions

**Files:**
- Modify: `internal/protocol/frame.go`
- Modify: `internal/protocol/codec.go`
- Modify: `internal/protocol/stream_open.go`
- Create: `internal/protocol/capabilities.go`
- Create: `internal/protocol/heartbeat.go`
- Create: `internal/protocol/probe.go`
- Create: `internal/protocol/flow_control.go`
- Create: `internal/protocol/protocol_extensions_test.go`
- Create: `docs/architecture/adr/0002-observability-protocol-extensions.md`
- Modify: `docs/protocol/websocket.md`

- [ ] Add RED tests for hello capability negotiation, bounded heartbeat sequence/nonce, stable OPEN result/error codes, unknown extension behavior, flow-control windows, UDP association limits, and probe payload redaction.
- [ ] Implement wire models with payload bounds and compatibility aliases for existing frame names.
- [ ] Ensure unnegotiated critical extensions fail with `unsupported_capability` while non-critical unknown frames do not tear down the connection.
- [ ] Run protocol unit, fuzz, and race tests.

### Task 6: Relay protobuf contract and cluster diagnostics

**Files:**
- Create: `api/relay/v1/relay.proto`
- Create: `api/relay/v1/relay.pb.go`
- Create: `api/relay/v1/relay_grpc.pb.go`
- Modify: `internal/relay/transport.go`
- Modify: `internal/relay/server_node_auth.go`
- Create: `internal/relay/proto_contract_test.go`
- Modify: `docs/architecture/cluster.md`

- [ ] Add RED compatibility tests for typed relay OPEN, caller identity metadata, epoch fencing, and stable status codes.
- [ ] Generate or hand-maintain the checked-in protobuf bindings using the repository's documented toolchain.
- [ ] Replace dynamic struct payloads in production relay paths while retaining a bounded migration adapter for one compatibility window.
- [ ] Run relay focused tests and mTLS integration tests.

### Task 7: Grafana dashboards and Prometheus rules

**Files:**
- Create: `deploy/grafana/dashboards/tunnelmesh.json`
- Create: `deploy/prometheus/recording-rules.yaml`
- Create: `deploy/prometheus/alert-rules.yaml`
- Create: `deploy/prometheus/prometheus.yml.example`
- Create: `deploy/grafana/provisioning/dashboards.yml`
- Create: `deploy/grafana/provisioning/datasources.yml`
- Create: `deploy/grafana/README.md`
- Create: `deploy/grafana/dashboard_schema_test.go`

- [ ] Add failing validation tests for dashboard UID uniqueness, `${DS_PROMETHEUS}` datasource usage, five required Rows, bounded variables, valid panel targets, and no secret/high-cardinality labels.
- [ ] Implement one dashboard containing Overview, Agent, Network, Cluster, and Security Rows.
- [ ] Add recording rules for availability, RTT quantiles, error rate, throughput and probe success; add alerts for offline agents, readiness, relay failures, heartbeat misses, storage failures and scrape absence.
- [ ] Run JSON/YAML validation and dashboard schema tests.

### Task 8: Documentation, deployment, and project completeness audit

**Files:**
- Create: `docs/operations/observability.md`
- Create: `docs/operations/slo.md`
- Create: `docs/operations/capacity.md`
- Modify: `docs/operations/troubleshooting.md`
- Modify: `docs/deployment/docker.md`
- Modify: `docs/architecture/overview.md`
- Modify: `docs/architecture/cluster.md`
- Modify: `docs/protocol/websocket.md`
- Create: `docs/deployment/nginx.md`
- Create: `docs/operations/completeness-checklist.md`

- [ ] Document scrape configuration, dashboard import, alert ownership, label cardinality, retention, SLOs, capacity assumptions, probe safety, and MySQL/etcd test limitations.
- [ ] Add Nginx `/metrics` protection and WSS timeout guidance consistent with 30-second heartbeat.
- [ ] Record remaining deferred items explicitly rather than claiming unsupported integrations.
- [ ] Run documentation link/path checks and `git diff --check`.

### Task 9: Integration, failure injection, and final verification

**Files:**
- Create: `internal/e2e/observability_protocol_test.go`
- Create: `internal/e2e/failure_injection_test.go`
- Modify: `Makefile` or create `scripts/verify-observability.sh`

- [ ] Add E2E coverage for metrics scrape, health transitions, heartbeat miss/recovery, denied stream metrics, probe authorization, relay fencing, and dashboard rule fixtures.
- [ ] Add bounded failure injection for DNS failure, TCP refusal, TLS/WS upgrade failure, token denial, heartbeat blackhole, DB touch failure and backpressure.
- [ ] Run:
  - `go test ./... -count=1`
  - `go test -race ./...`
  - `go vet ./...`
  - `git diff --check`
  - `cd web && npm test -- --run && npm run build`
- [ ] Run staged-content secret scan only after explicit staging authorization; do not commit automatically.
