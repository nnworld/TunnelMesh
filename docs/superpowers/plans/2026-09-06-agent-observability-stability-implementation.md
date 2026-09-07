# Agent Observability and Network Stability Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Provide authoritative Agent ownership, measurable heartbeat/RTT, active network probes, runtime quality history, structured diagnostics, health/metrics endpoints, and graceful recovery under abnormal networks.

**Architecture:** A shared heartbeat monitor measures sequenced PING/PONG and closes unhealthy connections after three misses. Server runtime acquires Agent ownership through the selected database/etcd registry, keeps high-frequency stats in bounded memory, flushes one-minute aggregates to SQL, and sends capability-gated probe requests to Agents. `slog` events and Prometheus metrics observe the same typed events without storing payloads or secrets.

**Tech Stack:** Go 1.23, slog, Prometheus client_golang, SQLite/MySQL, database/etcd registry, Vue 3, Element Plus.

**Spec:** `docs/superpowers/specs/2026-09-06-security-observability-release-design.md`

## Global Constraints

- Complete the scoped Token plan first; probe and runtime APIs consume authenticated Token identity and Agent Policy authorization.
- Schema changes remain in `migrations/ddl.sql`; this plan advances schema v3 to v4.
- Never perform database I/O synchronously before replying to PING/PONG or while holding a session-manager map lock.
- High-frequency counters remain in bounded memory; SQL receives one aggregate per Agent/epoch/minute plus bounded probe results.
- Agent ownership epoch comes from the configured `NodeRegistry`, not from an untrusted Agent frame.
- Unknown extensions are capability-gated. Do not send probe frames to Agents that did not advertise `probe.v1`.
- Metrics labels must not include Token, target host, target IP, route, payload, or unbounded Agent IDs.
- Probe responses contain status, timing, and error class only; no response body or service banner.
- Do not commit, push, merge, or publish without explicit user authorization.

---

### Task 1: Add observability configuration, trace context, and redacted JSON logging

**Files:**
- Create: `internal/observability/context.go`
- Create: `internal/observability/logging.go`
- Create: `internal/observability/logging_test.go`
- Create: `internal/observability/errors.go`
- Create: `internal/observability/stage.go`
- Create: `internal/observability/stage_test.go`
- Modify: `internal/config/config.go`
- Modify: `internal/config/config_test.go`
- Modify: `cmd/tunnelmesh-server/main.go`
- Modify: `cmd/tunnelmesh-agent/main.go`
- Modify: `cmd/tunnelmesh-client/main.go`

**Interfaces:**
- Produces:

```go
type LoggingConfig struct { Level, Format string }
func NewLogger(w io.Writer, cfg LoggingConfig) (*slog.Logger, error)
func WithTrace(ctx context.Context, traceID, connectionID string) context.Context
func TraceAttrs(ctx context.Context) []slog.Attr
func ErrorClass(error) string
type ConnectionStage string
type StageEvent struct {
    TraceID, ConnectionID, AgentID, TokenID string
    Stage ConnectionStage
    StartedAt, EndedAt time.Time
    Attempt int
    ErrorClass string
}
type StageObserver interface { ObserveStage(StageEvent) }
```

- [ ] **Step 1: Write failing logging/config tests**

Assert JSON contains `time`, `level`, `component`, `event`, `trace_id`, and `connection_id`; values under `token`, `authorization`, `password`, `private_key`, `payload`, `ssh`, or sensitive metadata keys must render `[redacted]`. Test production defaults `info/json` and invalid levels/formats. Add table tests for the exact ordered stage names `resolve`, `tcp_connect`, `tls_handshake`, `websocket_upgrade`, `token_auth`, `agent_hello`, `heartbeat`, and `logical_stream`; require non-negative duration, bounded attempt count, and a normalized error class without target or payload fields.

- [ ] **Step 2: Verify RED**

```bash
go test ./internal/observability ./internal/config -run 'Log|Observability' -count=1
```

- [ ] **Step 3: Implement the redacting handler**

Wrap `slog.JSONHandler`, recursively redact matching attributes, and normalize errors into bounded classes such as `dns`, `tcp_connect`, `tls`, `websocket_upgrade`, `authentication`, `authorization`, `policy`, `timeout`, `reset`, `backpressure`, and `target_unavailable`.

Implement the connection-stage enum and a fan-out observer that sends the same immutable `StageEvent` to structured logging and metrics observers. Reject an end time before its start time, cap `Attempt` at the configured retry limit, and never include target address, response body, metadata values, or raw Token material in the event.

- [ ] **Step 4: Add typed configuration**

Add:

```go
type ObservabilityConfig struct {
    LogLevel string
    LogFormat string
    MetricsEnabled bool
    StatsFlushInterval time.Duration
    StatsRetention time.Duration
}
type HeartbeatConfig struct {
    Interval time.Duration
    MissThreshold int
    DrainTimeout time.Duration
    TCPKeepAlive time.Duration
}
type ProbeConfig struct {
    Enabled bool
    Timeout time.Duration
    MaxConcurrent int
    Retention time.Duration
}
```

Defaults: heartbeat 30s, miss threshold 3, drain 5s, TCP keepalive 30s, stats flush 60s, stats retention 168h, probe timeout 5s, max concurrent 16, probe retention 168h.

- [ ] **Step 5: Verify GREEN**

```bash
go test ./internal/observability ./internal/config -count=1
git diff --check
```

### Task 2: Add authoritative Agent registration and registry-owned epoch

**Files:**
- Modify: `internal/protocol/frame.go`
- Create: `internal/protocol/registration.go`
- Modify: `internal/protocol/protocol_test.go`
- Modify: `internal/server/runtime.go`
- Modify: `internal/server/session_manager.go`
- Modify: `internal/server/ws_agent.go`
- Modify: `internal/agent/session.go`
- Modify: `internal/agent/websocket.go`
- Modify: `internal/registry/registry.go`
- Test: `internal/server/runtime_test.go`
- Test: `internal/registry/registry_contract_test.go`

**Interfaces:**
- Produces:

```go
const FrameAgentRegister FrameType = 12
const FrameAgentWelcome FrameType = 13
type AgentRegisterPayload struct {
    AgentID, AgentNodeID, InstanceID string
    Capabilities []string
}
type AgentWelcomePayload struct {
    AgentID, ServerNodeID string
    Epoch int64
    HeartbeatIntervalMillis int64
    MissThreshold int
}
```

- [ ] **Step 1: Write failing protocol and ownership tests**

Test bounded registration payloads, invalid identity, duplicate instance IDs, registry acquire returning the authoritative epoch, reconnect takeover after revoke, stale owner keepalive rejection, and separation of `AgentNodeID` from `ServerNodeID`.

- [ ] **Step 2: Verify RED**

```bash
go test ./internal/protocol ./internal/registry ./internal/server -run 'Register|Welcome|Ownership' -count=1
```

- [ ] **Step 3: Implement the registration handshake**

New Agents first send `AGENT_REGISTER`; Server validates the Agent Token, calls `NodeRegistry.Register` with the local Server node ID, and returns `AGENT_WELCOME` containing the registry epoch. Only then may the Agent send metadata hello/update using that epoch.

Keep the old metadata-first handshake behind an explicit Deprecated compatibility flag; do not send new control frames to an old Agent.

- [ ] **Step 4: Integrate lease keepalive and revoke**

Store `registry.NodeOwner` in `AgentSession`. A bounded worker calls `KeepAlive`; session removal calls registry `Revoke` using compare-and-fence semantics. Never hold `AgentSessionManager.mu` during registry/database/etcd I/O.

- [ ] **Step 5: Verify database and etcd contracts**

```bash
go test ./internal/registry ./internal/server ./internal/agent -count=1
go test -race ./internal/registry ./internal/server ./internal/agent
```

### Task 3: Implement sequenced heartbeats, RTT, missed-heartbeat closure, and cancellable dialing

**Files:**
- Create: `internal/session/heartbeat.go`
- Create: `internal/session/heartbeat_test.go`
- Create: `internal/protocol/heartbeat.go`
- Modify: `internal/protocol/frame.go`
- Modify: `internal/agent/session.go`
- Modify: `internal/agent/websocket.go`
- Modify: `internal/server/ws_agent.go`
- Modify: `internal/server/ws_client.go`
- Modify: `internal/server/middleware.go`
- Modify: `internal/server/stream_authorizer.go`
- Modify: `internal/relay/transport.go`

**Interfaces:**
- Produces:

```go
type HeartbeatPayload struct { Sequence uint64; SentAtUnixNano int64 }
type HeartbeatSample struct { Sequence uint64; RTT time.Duration; SentAt, ReceivedAt time.Time }
type HeartbeatMonitor interface {
    NextPing(time.Time) HeartbeatPayload
    ObservePong(HeartbeatPayload, time.Time) (HeartbeatSample, bool)
    Evaluate(time.Time) (ConnectionStatus, bool)
}
```

- [ ] **Step 1: Write failing fake-clock tests**

Cover RTT calculation, late and duplicate PONG, PING payload maximum, degraded after misses, close after three misses, recovery on a valid PONG, and no goroutine leak after context cancellation.

Using a recording `StageObserver`, cover success and failure at DNS resolution, TCP connect, TLS handshake, WebSocket upgrade, Token authentication, Agent registration, and first heartbeat. Assert stages are emitted in order, retry attempts increment without duplicate success events, and stage logging never contains the target response or Token secret.

- [ ] **Step 2: Add regression tests for current transport defects**

Assert direct `DialWebSocket` with an invalid URL returns an error instead of passing a nil URL to `originFor`; assert dial respects context timeout; assert a blocked Send cannot prevent Close forever.

- [ ] **Step 3: Verify RED**

```bash
go test ./internal/session ./internal/protocol ./internal/agent -run 'Heartbeat|DialWebSocket' -count=1
```

- [ ] **Step 4: Implement the shared monitor and transport deadlines**

Limit PING/PONG payload to 128 bytes. Use `net.Dialer{Timeout, KeepAlive}` and a context-aware WebSocket dial path. Serialize writes through one bounded writer, attach write deadlines where supported, and have Agent/Client/Server close and reconnect after the monitor reaches its threshold.

Instrument Agent/Client dialing with `resolve`, `tcp_connect`, `tls_handshake`, and `websocket_upgrade`; instrument Server middleware and registration with `token_auth` and `agent_hello`; emit `heartbeat` on the first successful sequenced PONG. Task 4 emits `logical_stream` at each authorized OPEN. Failed stages include duration, attempt, and normalized error class, then feed the shared log/metric observer.

- [ ] **Step 5: Make PONG independent of persistence**

Reply to PING immediately, then enqueue metadata/registry Touch into a bounded asynchronous worker with a short timeout. A failed Touch emits a warning/metric but does not delay the control channel.

- [ ] **Step 6: Verify GREEN and race safety**

```bash
go test ./internal/session ./internal/protocol ./internal/agent ./internal/server ./internal/client -count=1
go test -race ./internal/session ./internal/agent ./internal/server ./internal/client
```

### Task 4: Build the in-memory Agent runtime aggregator and stream observer

**Files:**
- Create: `internal/server/runtime_stats.go`
- Create: `internal/server/runtime_stats_test.go`
- Create: `internal/session/observer.go`
- Modify: `internal/server/session_manager.go`
- Modify: `internal/agent/session.go`
- Modify: `internal/client/session.go`
- Modify: `internal/relay/service.go`
- Modify: `internal/server/tcp_bridge.go`
- Modify: `internal/server/http_proxy.go`

**Interfaces:**
- Produces:

```go
type StreamObserver interface {
    Open(agentID string, epoch int64, streamID uint32, protocol string)
    AddBytes(agentID string, epoch int64, streamID uint32, in, out uint64)
    Close(agentID string, epoch int64, streamID uint32, errorClass string)
}
type RuntimeStats interface {
    ObserveConnection(ConnectionEvent)
    ObserveHeartbeat(HeartbeatEvent)
    ObserveStream(StreamEvent)
    ObserveProbe(ProbeEvent)
    Snapshot(agentID string) (AgentRuntimeSnapshot, bool)
    Flush(context.Context, time.Time) error
}
```

- [ ] **Step 1: Write failing aggregation tests**

Use a fake clock to assert online/degraded/offline/stale transitions, active-stream counts, byte direction, current/peak bps, heartbeat availability, RTT P50/P95, reconnect count, stream error rate, and epoch replacement clearing live state without erasing previous aggregate history.

- [ ] **Step 2: Verify RED**

```bash
go test ./internal/server ./internal/session -run 'RuntimeStats|StreamObserver' -count=1
```

- [ ] **Step 3: Implement bounded windows**

Use fixed one-minute buckets and bounded RTT samples or a streaming quantile implementation; do not retain every frame/event. Shard locks by Agent ID or use atomics for counters. Keep target addresses and payloads out of the aggregator.

- [ ] **Step 4: Instrument shared boundaries**

Count streams and bytes at logical stream OPEN/DATA/HALF_CLOSE/RESET boundaries through `StreamObserver`, not separately in every handler. In `RelayService.OpenStream`, copy the transport/epoch under `RLock`, release the lock before network I/O, and rely on transport close/fencing for unregister races.

- [ ] **Step 5: Verify GREEN and benchmark overhead**

```bash
go test ./internal/server ./internal/session ./internal/relay ./internal/client ./internal/agent -count=1
go test -race ./internal/server ./internal/relay ./internal/client ./internal/agent
go test ./internal/server -run '^$' -bench RuntimeStats -benchmem
```

### Task 5: Persist stats/probe history and retention safely

**Files:**
- Modify: `migrations/ddl.sql`
- Modify: `internal/storage/models.go`
- Modify: `internal/storage/repository.go`
- Modify: `internal/storage/db.go`
- Create: `internal/storage/runtime_stats_test.go`
- Modify: `internal/storage/storage_contract_test.go`
- Modify: `internal/server/runtime_stats.go`

**Interfaces:**
- Produces:

```go
type AgentRuntimeStatsRepository interface {
    Append(context.Context, AgentRuntimeStats) error
    ListRange(context.Context, string, time.Time, time.Time, string, int) (Page[AgentRuntimeStats], error)
    DeleteBefore(context.Context, string, time.Time) error
}
type AgentProbeResultRepository interface {
    Create(context.Context, AgentProbeResult) error
    ListByAgent(context.Context, string, string, int) (Page[AgentProbeResult], error)
    DeleteBefore(context.Context, time.Time) error
}
```

- [ ] **Step 1: Write failing SQLite/MySQL contract tests**

Cover append, range/cursor ordering, retention deletion, probe pagination, explicit columns, and rejection when `(agent_id,node_id,epoch)` does not match the current `agent_runtime_leases` row.

- [ ] **Step 2: Verify RED**

```bash
go test ./internal/storage -run 'RuntimeStats|ProbeResult' -count=1
```

- [ ] **Step 3: Add schema v4**

Add `agent_runtime_stats` and `agent_probe_results` to the sole DDL, with indexes on `(agent_id, window_start, id)` and `(agent_id, observed_at, probe_id)`. Persist durations as integer microseconds and times as RFC3339Nano text for current driver portability.

- [ ] **Step 4: Implement flush and retention workers**

Flush closed one-minute buckets every 60 seconds using a bounded queue. On failure retain the bucket for retry with exponential backoff and jitter; do not block forwarding. Run retention cleanup under a distributed lease/lock so cluster nodes do not duplicate work.

- [ ] **Step 5: Verify GREEN**

```bash
go test ./internal/storage ./internal/server -run 'RuntimeStats|ProbeResult|Flush|Retention' -count=1
go test -race ./internal/server ./internal/storage
```

### Task 6: Add capability-gated TCP, HTTP, and UDP echo probes

**Files:**
- Modify: `internal/protocol/frame.go`
- Create: `internal/protocol/probe.go`
- Modify: `internal/protocol/protocol_test.go`
- Create: `internal/agent/probe.go`
- Create: `internal/agent/probe_test.go`
- Modify: `internal/agent/session.go`
- Create: `internal/server/probe_service.go`
- Create: `internal/server/probe_service_test.go`

**Interfaces:**
- Produces `FrameProbeRequest=14`, `FrameProbeResult=15`, capability `probe.v1`, and:

```go
type ProbeExecutor interface {
    Execute(context.Context, protocol.ProbeRequestPayload) protocol.ProbeResultPayload
}
func (s *ProbeService) Diagnose(context.Context, auth.Principal, string, DiagnoseRequest) (ProbeResult, error)
```

- [ ] **Step 1: Write failing bounded protocol tests and fuzz cases**

Validate probe ID, Agent ID, authoritative epoch, kind, host, port, HTTP method allowlist, timeout range, nonce size, payload maximum, and result error classes.

- [ ] **Step 2: Write failing executor tests**

Use local TCP, HTTP, and UDP echo servers. Cover success, DNS/connect/read timeout, Policy denial, wrong UDP nonce, HTTP body discard, response size bounding, and cancellation.

- [ ] **Step 3: Write failing Server correlation tests**

Test owner/admin authorization, Agent Policy, `probe.v1` capability, maximum concurrent probes, probeID correlation, timeout cleanup, late result ignore, epoch takeover, persistence, and no target response body.

- [ ] **Step 4: Verify RED**

```bash
go test ./internal/protocol ./internal/agent ./internal/server -run Probe -count=1
```

- [ ] **Step 5: Implement minimal probe flow**

Agent uses its existing Dialer policy boundary. HTTP permits HEAD and GET but discards a bounded body. UDP sends a random nonce and succeeds only when the same nonce returns. Server stores an in-flight map keyed by probe ID with a semaphore and deadline; it sends frames only to sessions advertising `probe.v1`.

- [ ] **Step 6: Verify GREEN**

```bash
go test ./internal/protocol ./internal/agent ./internal/server ./internal/storage -count=1
go test -race ./internal/agent ./internal/server
```

### Task 7: Expose runtime, metrics history, probes, diagnose, health, and Prometheus endpoints

**Files:**
- Create: `internal/server/runtime_api.go`
- Create: `internal/server/runtime_api_test.go`
- Create: `internal/server/health.go`
- Create: `internal/server/health_test.go`
- Create: `internal/observability/metrics.go`
- Create: `internal/observability/metrics_test.go`
- Modify: `internal/server/runtime.go`
- Modify: `internal/server/api.go`
- Modify: `docs/api/openapi.yaml`
- Modify: `go.mod`
- Modify: `go.sum`

**Interfaces:**
- Produces `/health/live`, `/health/ready`, `/metrics`, and the four Agent runtime APIs from the spec.

- [ ] **Step 1: Write failing API/health tests**

Cover unified envelopes, owner/admin access, unrelated user 403, range allowlist `1m/5m/1h/24h`, cursor pagination, diagnose idempotency, liveness independent of dependencies, readiness failures for DB/schema/registry/certificate, and response timeout.

- [ ] **Step 2: Write failing Prometheus tests**

Use an injected `prometheus.Registry` and assert bounded labels for connections, heartbeats, RTT, bytes, active streams, probes, connection stages, storage flush failures, and readiness.

- [ ] **Step 3: Verify RED**

```bash
go test ./internal/server ./internal/observability -run 'RuntimeAPI|Health|Metrics' -count=1
```

- [ ] **Step 4: Implement endpoints**

Health handlers bypass management authentication but reveal only component names and healthy/unhealthy status. `/metrics` may be bound to a separate management address or protected by an allowlist. Runtime APIs use Agent owner/admin authorization and never expose target response content.

- [ ] **Step 5: Update OpenAPI and verify GREEN**

```bash
go test ./internal/server ./internal/observability -count=1
go test -race ./internal/server ./internal/observability
```

### Task 8: Add Agent quality and network diagnostics to the Web console

**Files:**
- Modify: `web/src/api/client.ts`
- Modify: `web/src/views/AgentDetail.vue`
- Create: `web/src/components/AgentRuntimeCard.vue`
- Create: `web/src/components/AgentQualityChart.vue`
- Create: `web/src/components/AgentProbePanel.vue`
- Modify: `web/src/tests/routes.spec.ts`
- Modify: `docs/user-guide/server-admin.md`

**Interfaces:**
- Produces raw 1m/5m/1h/24h status, RTT, bandwidth, heartbeat, reconnect, stream error, and probe views.

- [ ] **Step 1: Write failing frontend tests**

Assert status tags `online/degraded/offline/stale`, current and peak in/out bps, P50/P95 RTT, heartbeat availability, reconnect/error counts, time-range selector, probe form/result, loading/error/empty states, and absence of payload/Token rendering.

- [ ] **Step 2: Verify RED**

```bash
cd web && npm test -- --run src/tests/routes.spec.ts
```

- [ ] **Step 3: Implement the Element Plus views**

Display raw metrics and concise status explanations. Poll current runtime at a bounded interval only while the tab is visible; cancel stale requests on route change. Historical charts use aggregated snapshots, not per-frame events.

- [ ] **Step 4: Build and refresh embed**

```bash
cd web
npm test -- --run
npm run build
cd ..
rsync -a --delete web/dist/ internal/server/web_dist/
go test ./internal/server -run Web -count=1
```

### Task 9: Implement graceful process lifecycle and abnormal-network E2E tests

**Files:**
- Create: `internal/lifecycle/lifecycle.go`
- Create: `internal/lifecycle/lifecycle_test.go`
- Create: `internal/lifecycle/notifier.go`
- Create: `internal/lifecycle/systemd_linux.go`
- Create: `internal/lifecycle/systemd_other.go`
- Create: `internal/lifecycle/systemd_test.go`
- Modify: `internal/server/runtime.go`
- Modify: `internal/cli/root.go`
- Modify: `cmd/tunnelmesh-server/main.go`
- Modify: `cmd/tunnelmesh-agent/main.go`
- Modify: `cmd/tunnelmesh-client/main.go`
- Create: `internal/e2e/network_stability_test.go`
- Modify: `docs/operations/troubleshooting.md`

**Interfaces:**
- Produces signal-driven stop-accepting → GOAWAY → bounded drain → dependency close lifecycle and:

```go
type ServiceNotifier interface {
	Ready() error
	Watchdog() error
	Stopping() error
}
func NewServiceNotifier() ServiceNotifier
```

- [ ] **Step 1: Write failing lifecycle tests**

Test SIGTERM context cancellation, Server stops new upgrades, all current Agent/Client sessions receive GOAWAY, drain respects timeout, DB/registry close after sessions, and second shutdown is idempotent. With an injected fake notifier, assert `Ready` occurs only after listeners and required dependencies are ready, `Watchdog` stops when the context is cancelled, and `Stopping` occurs before drain begins.

- [ ] **Step 2: Add abnormal-network E2E tests**

Inject DNS failure, TCP refuse, TLS failure, Upgrade failure, Token denial, heartbeat blackhole, half-open connection, delayed PONG, Server restart, database Touch failure, and packet-sized backpressure. Assert error class, bounded reconnect, no goroutine leak, continued healthy streams where degradation is allowed, and recovery.

- [ ] **Step 3: Implement lifecycle orchestration**

Entrypoints use `signal.NotifyContext`. Runtime closes listeners first, broadcasts GOAWAY, waits the configured drain timeout, closes remaining transports, then registry/database. Emit one structured event per lifecycle phase.

On Linux, `systemd_linux.go` sends `READY=1`, `WATCHDOG=1`, and `STOPPING=1` to `NOTIFY_SOCKET` using the systemd notification protocol. Derive the watchdog interval from `WATCHDOG_USEC`, tick at no more than half that duration, and disable notification cleanly when the environment is absent or malformed. `systemd_other.go` is a no-op implementation selected by build tags. Do not add a daemon mode or make systemd a mandatory runtime dependency.

- [ ] **Step 4: Run the complete stability gate**

```bash
go test ./... -count=1
go test -race ./...
go vet ./...
git diff --check
cd web && npm test -- --run && npm run build
```

On a Linux system running systemd, also run:

```bash
go test ./internal/lifecycle -count=1
NOTIFY_SOCKET= WATCHDOG_USEC= go test ./internal/lifecycle -run Systemd -count=1
```

- [ ] **Step 5: Inspect staged content before any authorized commit**

```bash
git diff --cached
git grep -nE '(Authorization: Bearer [^$]|BEGIN (RSA|OPENSSH|EC) PRIVATE KEY|password=)' -- ':!web/node_modules'
```

Commit only after explicit authorization, using scoped commits such as `feat(observability): add agent runtime quality`.
