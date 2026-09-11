# SOCKS5 网页首屏延迟优化 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Implement P0/P1/P2 latency optimizations for SOCKS5 web-page loading with compatible strict-open, bounded async dialing, authorization caching, per-stream isolation, flow control, fair scheduling, local-agent affinity, and observability.

**Architecture:** Keep the current wire protocol version and gate new semantics behind negotiated capabilities. Move blocking work out of all WebSocket receive loops, isolate errors and queues per stream, and use a shared database authorization revision to make long MySQL cache TTL safe. Keep Handler → Service → Repository boundaries and make the v8→v9 schema change expand-only.

**Tech Stack:** Go standard library, existing `golang.org/x/net/websocket`, existing Prometheus client, SQLite and MySQL drivers, Vue/Element Plus only if observability-facing admin UI changes are required.

**Spec:** `docs/superpowers/specs/2026-09-10-socks5-web-page-latency-design.md`

## Global Constraints

- Do not use a worktree; work directly in `/opt/app/workspace/TunnelMesh`.
- TDD is mandatory: write the failing test, run it and capture the failure, implement minimally, then run the focused and package tests.
- Handler → Service → Repository/Adapter; no direct SQL from handlers and no protocol dependency on services or storage.
- Schema changes must update all of: `migrations/ddl.sql`, `migrations/incremental/v0008_to_v0009/{mysql,sqlite}.sql`, `SchemaVersion`, `migrations/embed.go`, migration tests, and upgrade documentation.
- Public ingress remains HTTP/HTTPS/WebSocket only; no public SOCKS5 listener.
- No SOCKS5 `BIND`, no SOCKS5 `UDP ASSOCIATE`, no HTTP/2 or HTTP/3 proxy semantics.
- No Redis dependency; authorization consistency uses the authoritative SQLite/MySQL revision.
- Prometheus labels must remain bounded and must not contain target host, IP, port, token, username, password, stream ID, or connection ID.
- Do not log or serialize bearer secrets, passwords, private keys, production DSNs, full credentials, or raw `net.OpError` text.
- Defaults preserve legacy behavior and Agent pool `min=1/max=1`.
- Strict mode requires negotiated capabilities on the complete Client → Server → Agent/relay path; otherwise use legacy mode.
- Each task must compile and pass its focused tests independently before the next task starts.
- Do not commit, push, merge, or create a PR without explicit user authorization.

---

### Task 1: Schema v9 and authorization revision repository

**Files:**

- Modify: `migrations/ddl.sql`
- Create: `migrations/incremental/v0008_to_v0009/mysql.sql`
- Create: `migrations/incremental/v0008_to_v0009/sqlite.sql`
- Modify: `migrations/embed.go`
- Modify: `internal/storage/db.go`
- Create: `internal/storage/authorization_revision.go`
- Modify: `internal/storage/repository.go`
- Test: `internal/storage/sqlite_test.go`
- Test: `internal/storage/mysql_test.go`
- Test: `internal/storage/storage_contract_test.go`
- Create: `docs/operations/schema-upgrades.md`

**Interfaces:**

- Produces: `type AuthorizationRevisionRepository interface { Current(context.Context) (uint64, error) }`
- Produces: `func (d *DB) AuthorizationRevisions() AuthorizationRevisionRepository`
- Produces: `func bumpAuthorizationRevision(ctx context.Context, tx *sql.Tx) error`
- Consumes: existing `storage.DB`, `storage.DriverSQLite`, `storage.DriverMySQL`, and `migrations.DDL`

The internal `bumpAuthorizationRevision` helper is unexported in Task 1 so later repository tasks can call it from package `storage`; it is not a public API.

- [x] **Step 1: Write failing migration and repository tests**

Add tests that assert:

```go
func TestSQLiteV8ToV9AuthorizationRevisionMigration(t *testing.T)
func TestMySQLV8ToV9AuthorizationRevisionMigration(t *testing.T)
func TestAuthorizationRevisionCurrentInitializesToOne(t *testing.T)
func TestAuthorizationRevisionMissingRowFails(t *testing.T)
```

The migration test must start from `schema_meta.version=8`, run `Open`, and require version 9 plus the fixed row `(1,1)`. The repository contract test must run against SQLite and, when `TUNNELMESH_TEST_MYSQL_DSN` is set, MySQL.

- [x] **Step 2: Run tests and verify failure**

Run:

```bash
go test ./internal/storage -run 'TestSQLiteV8ToV9AuthorizationRevisionMigration|TestAuthorizationRevision' -count=1
```

Expected failure: missing v8→v9 migration registration, `SchemaVersion` remains 8, and `AuthorizationRevisions` does not exist.

- [x] **Step 3: Implement the minimal schema and repository**

Update:

- `SchemaVersion` from 8 to 9.
- Full DDL with `authorization_revision`.
- Idempotent v8→v9 scripts for both drivers.
- Embedded `V8ToV9MySQL` and `V8ToV9SQLite`.
- Migration switch case `8`.
- Repository implementation with `Current` and the transaction bump helper.

Use one fixed row with `id=1` and `revision=1`. Do not add revision writes to resource repositories in this task.

- [x] **Step 4: Run focused tests**

```bash
go test ./internal/storage -run 'TestSQLiteV8ToV9AuthorizationRevisionMigration|TestMySQLV8ToV9AuthorizationRevisionMigration|TestAuthorizationRevision' -count=1
```

Expected: all tests pass; MySQL tests skip with an explicit message when the DSN environment variable is absent.

- [x] **Step 5: Run storage verification**

```bash
go test ./internal/storage -count=1
go vet ./internal/storage
```

Expected: both commands exit 0.

---

### Task 2: OpenResult protocol and capability negotiation

**Files:**

- Modify: `internal/protocol/frame.go`
- Modify: `internal/protocol/capabilities.go`
- Create: `internal/protocol/open_result.go`
- Create: `internal/protocol/subprotocol.go`
- Test: `internal/protocol/protocol_test.go`
- Test: `internal/protocol/capabilities_test.go`
- Create: `internal/protocol/open_result_test.go`
- Create: `internal/protocol/subprotocol_test.go`

**Interfaces:**

- Produces: `FrameOpenResult protocol.FrameType`
- Produces: `type OpenResultPayload struct { Accepted bool; Stage OpenResultStage; Code OpenResultCode; Retryable bool; RetryAfterMS int }`
- Produces: `const MaxOpenResultPayload = 1 << 10`
- Produces: `func EncodeOpenResultPayload(OpenResultPayload) ([]byte, error)`
- Produces: `func DecodeOpenResultPayload([]byte) (OpenResultPayload, error)`
- Produces: capability constants `CapabilityStreamOpenResult`, `CapabilityStreamFlowControl`, and `CapabilityStreamFairWriter`
- Produces: `func ClientSubprotocols() []string`
- Produces: `func SelectSubprotocol(offered []string) (string, bool)`

The subprotocol list must be exactly:

```text
tunnelmesh.v1.open-result.flow-control
tunnelmesh.v1.open-result
tunnelmesh.v1
```

`SelectSubprotocol` returns the first supported value and reports whether a modern semantic was selected. Absent or unsupported values select legacy `tunnelmesh.v1` behavior.

- [x] **Step 1: Write failing protocol tests**

Add tests for:

- `OPEN_RESULT` is a known frame and permits only a nonzero stream ID.
- Payloads larger than 1 KiB are rejected.
- Every allowed stage and code round-trips through JSON.
- Unknown stage and code values are rejected.
- `accepted=true` requires `code=ok`.
- Duplicate capabilities are deduplicated.
- All three client subprotocols are offered in priority order.
- Supported modern subprotocol, legacy-only, missing, and unsupported cases select correctly.

- [x] **Step 2: Run tests and verify failure**

```bash
go test ./internal/protocol -run 'TestOpenResult|TestClientSubprotocol|TestSelectSubprotocol|TestCapability' -count=1
```

Expected failure: undefined frame, payload, constants, and functions.

- [x] **Step 3: Implement protocol types and negotiation**

Add the frame after the existing core stream frames while preserving all current numeric values. Update `knownFrameType`. Extend `AgentMetadataPayload` and `AgentMetadataAckPayload` with optional `capabilities []string`; old JSON remains valid and unknown fields are ignored.

- [x] **Step 4: Run protocol verification**

```bash
go test ./internal/protocol -count=1
go test ./internal/protocol -race -count=1
go vet ./internal/protocol
```

Expected: all commands exit 0.

---

### Task 3: Configuration and metric contracts

**Files:**

- Modify: `internal/config/config.go`
- Test: `internal/config/config_test.go`
- Modify: `internal/observability/metrics.go`
- Test: `internal/observability/metrics_test.go`
- Modify: `docs/operations/configuration.md`
- Modify: `docs/operations/config-examples.md`

**Interfaces:**

- Produces: `ServerStreamConfig`, `AuthorizationCacheConfig`, `AgentStreamConfig`, `ClientStreamConfig`, and `RemoteValidationConfig`
- Produces metric observers:
  - `ObserveStreamStage(component, stage, result, errorClass string, protocol string, duration time.Duration)`
  - `ObserveStreamOpen(component, result, errorClass, openMode string)`
  - `ObserveStreamQueueWait(component, result string, duration time.Duration)`
  - `ObserveStreamWindowStall(component, result string, duration time.Duration)`
  - `ObserveStreamBackpressure(component, result string)`
  - `ObserveAuthorizationCache(cache, result string)`
  - `SetAuthorizationRevision(revision uint64)`
  - `ObserveAuthorizationRevisionPoll(result, errorClass string)`
  - `ObserveRemoteValidationCache(cache, result string)`

Configuration defaults:

```yaml
server.stream.max_concurrent_opens: 256
server.stream.max_pending_opens: 1024
server.stream.initial_window: 262144
server.stream.window_update_threshold: 131072
server.stream.max_frame_payload: 32768
server.authorization_cache.enabled: true
server.authorization_cache.local_positive_ttl: 5s
server.authorization_cache.cluster_positive_ttl: 5m
server.authorization_cache.negative_ttl: 3s
server.authorization_cache.revision_poll_interval: 2s
server.authorization_cache.max_stale_on_poll_error: 5s
server.authorization_cache.max_entries: 100000
agent.streams.max_concurrent_dials: 32
agent.streams.max_pending_dials: 128
agent.streams.connect_timeout: 5s
agent.streams.open_timeout: 8s
agent.streams.inbound_buffer_bytes: 262144
client.stream.open_timeout: 8s
client.stream.inbound_buffer_bytes: 262144
client.remote_validation.positive_ttl: 15s
client.remote_validation.negative_ttl: 2s
client.remote_validation.timeout: 3s
client.remote_validation.max_entries: 10000
```

- [x] **Step 1: Write failing config and metrics tests**

Cover defaults, CLI override, invalid zero/negative values, oversized frame payload, TTL limits, bounded label normalization, and all new histogram/counter/gauge families.

- [x] **Step 2: Run tests and verify failure**

```bash
go test ./internal/config ./internal/observability -count=1
```

Expected failure: missing configuration types and metric methods.

- [x] **Step 3: Implement configuration and metrics**

Wire defaults, validation, environment binding for non-secret keys, and metric registration. Ensure rendered config does not expose secrets.

- [x] **Step 4: Run focused verification**

```bash
go test ./internal/config ./internal/observability -count=1
go test ./internal/observability -race -count=1
go vet ./internal/config ./internal/observability
```

Expected: all commands exit 0.

---

### Task 4: Agent bounded dial executor and stream error isolation

**Files:**

- Create: `internal/agent/dial_executor.go`
- Create: `internal/agent/dial_executor_test.go`
- Modify: `internal/agent/session.go`
- Modify: `internal/agent/dialer.go`
- Test: `internal/agent/session_test.go`
- Test: `internal/agent/websocket_test.go`
- Modify: `internal/cli/root.go`
- Modify: `internal/cli/agent_runtime_test.go`

**Interfaces:**

- Produces: `type DialExecutor struct`
- Produces: `type DialExecutorConfig struct { MaxConcurrent int; MaxPending int; ConnectTimeout time.Duration; OpenTimeout time.Duration }`
- Produces: `func NewDialExecutor(config DialExecutorConfig) *DialExecutor`
- Produces: `func (e *DialExecutor) Submit(ctx context.Context, request DialRequest) error`
- Produces: `type DialRequest struct { Frame protocol.Frame; Payload protocol.StreamOpenPayload; Result func(DialResult) }`
- Produces: `type DialResult struct { Conn io.ReadWriteCloser; Stage protocol.OpenResultStage; Code protocol.OpenResultCode; Retryable bool; RetryAfter time.Duration; Err error }`
- Produces: `func (e *DialExecutor) Close() error`
- Consumes: Task 2 `OpenResultPayload`, capabilities, and Task 3 `AgentStreamConfig`

Stream-scoped dial, policy, target write, queue, duplicate ID, timeout, and reset failures must not return a session-fatal error from `StreamDispatcher.Handle`. Only transport, authentication, fencing, invalid connection identity, and unrecoverable global frame errors may close the session.

- [x] **Step 1: Write failing Agent tests**

Cover:

- Two blocked dials do not prevent a third ready dial.
- `MaxConcurrent` and `MaxPending` are enforced with immediate `queue_full`.
- Stream reset cancels an in-progress dial.
- Session close cancels all dials and releases goroutines.
- DNS/refused/timeout failures return one `OPEN_RESULT` failure for the current stream.
- Success installs the stream before `OPEN_RESULT(ok)` is sent.
- A target write failure resets only that stream.
- Legacy peers receive only `RESET` for stream-scoped failures.
- One failed web-page subresource does not terminate the session.

- [x] **Step 2: Run tests and verify failure**

```bash
go test ./internal/agent -run 'TestDialExecutor|TestStreamDispatcher' -count=1
```

Expected failure: current dispatcher dials synchronously and `DialExecutor` is undefined.

- [x] **Step 3: Implement the executor and dispatcher changes**

Keep the WebSocket receive loop nonblocking. Use a semaphore and bounded queue, one cancelable context per dial, and a result callback. Extend Agent hello capabilities when configured.

- [x] **Step 4: Run Agent verification**

```bash
go test ./internal/agent -count=1
go test ./internal/agent -race -count=1
go vet ./internal/agent
```

Expected: all commands exit 0.

---

### Task 5: Server strict-open service and WebSocket subprotocol

**Files:**

- Create: `internal/server/client_stream_service.go`
- Create: `internal/server/client_stream_service_test.go`
- Modify: `internal/server/ws_client.go`
- Modify: `internal/server/runtime.go`
- Modify: `internal/server/session_manager.go`
- Test: `internal/server/ws_client_test.go`
- Test: `internal/server/runtime_test.go`

**Interfaces:**

- Produces: `type ClientStreamService struct`
- Produces: `type ClientStreamServiceConfig struct { MaxConcurrentOpens int; MaxPendingOpens int; OpenTimeout time.Duration }`
- Produces: `func NewClientStreamService(authorizer StreamAuthorizer, transport relay.NodeTransport, config ClientStreamServiceConfig, metrics *observability.Metrics) *ClientStreamService`
- Produces: `func (s *ClientStreamService) Open(ctx context.Context, principal ClientSessionPrincipal, request protocol.StreamOpenPayload) OpenFuture`
- Produces: `type OpenFuture interface { Wait(ctx context.Context) (protocol.OpenResultPayload, error); Cancel() }`
- Consumes: Tasks 2–4 protocol and Agent capabilities

Server WebSocket handlers must select one supported subprotocol and persist the selected semantic on the session. Strict `OPEN_STREAM` processing is asynchronous; legacy processing remains synchronous.

- [x] **Step 1: Write failing Server tests**

Cover:

- Modern subprotocol selection and absent-subprotocol legacy fallback.
- Strict open does not block PING or another stream while authorization/relay is blocked.
- Concurrent open limits and pending limits return `queue_full`.
- Agent offline, authorization rejected, relay failure, and internal failure map to stable codes.
- Strict client DATA before `OPEN_RESULT` resets only that stream.
- Legacy client keeps existing behavior.
- Single stream failure leaves Client and Agent sessions online.
- Strict mode is not enabled unless Agent capability is present.

- [x] **Step 2: Run tests and verify failure**

```bash
go test ./internal/server -run 'TestClientStreamService|TestClientWebSocket|TestServeClientSession' -count=1
```

Expected failure: no subprotocol negotiation, async service, or strict state machine.

- [x] **Step 3: Implement service and handler integration**

Add opening/open/reset state, bounded executor, stable error mapping, and `OPEN_RESULT` forwarding. Keep all authorization calls in the service layer.

- [x] **Step 4: Run Server verification**

```bash
go test ./internal/server -count=1
go test ./internal/server -race -count=1
go vet ./internal/server
```

Expected: all commands exit 0.

---

### Task 6: Relay OpenResult propagation

**Files:**

- Modify: `internal/relay/service.go`
- Modify: `internal/relay/transport.go`
- Test: `internal/relay/relay_test.go`
- Test: `internal/relay/transport_test.go`
- Test: `internal/relay/server_node_auth_test.go`
- Modify: `internal/server/agent_relay_transport.go`
- Test: `internal/server/agent_relay_transport_test.go`
- Modify: `docs/protocol/proxy-modules.md`

**Interfaces:**

- Produces: `type RelayOpenMetadata struct { Request relay.StreamRequest; StrictOpen bool; OpenTimeout time.Duration }`
- Produces: `type RelayOpenResult struct { Payload protocol.OpenResultPayload }`
- Consumes: Task 2 `OpenResultPayload` and Task 5 `OpenFuture`

The relay protocol must not assume a remote node supports strict open. If either side is legacy, it must return an explicit `unsupported_capability` result rather than fabricating success.

- [x] **Step 1: Write failing relay tests**

Cover local-to-remote strict open success, all stable failure codes, legacy remote fallback, timeout, cancellation, stream reset, and no secret leakage in relay errors.

- [x] **Step 2: Run tests and verify failure**

```bash
go test ./internal/relay ./internal/server -run 'TestRelayOpen|TestAgentRelay.*Open|TestGRPCNodeTransport' -count=1
```

Expected failure: relay metadata and result types do not exist.

- [x] **Step 3: Implement relay metadata and result propagation**

Preserve mTLS requirements and existing stream byte semantics. Keep result payload bounded at 1 KiB.

- [x] **Step 4: Run relay verification**

```bash
go test ./internal/relay ./internal/server -count=1
go test ./internal/relay ./internal/server -race -count=1
go vet ./internal/relay ./internal/server
```

Expected: all commands exit 0.

---

### Task 7: Client strict open and SOCKS5 result mapping

**Files:**

- Modify: `internal/client/session.go`
- Test: `internal/client/session_test.go`
- Modify: `internal/client/websocket.go`
- Test: `internal/client/websocket_test.go`
- Modify: `internal/client/socks5_forward.go`
- Test: `internal/client/socks5_forward_test.go`
- Modify: `internal/proxy/handshake.go`
- Test: `internal/proxy/handshake_test.go`
- Modify: `docs/user-guide/client.md`

**Interfaces:**

- Produces: `type SessionOpenMode uint8` with `SessionOpenLegacy` and `SessionOpenStrict`
- Produces: `func NewSessionWithOpenMode(tr FrameTransport, mode SessionOpenMode) *Session`
- Produces: `func (s *Session) OpenMode() SessionOpenMode`
- Produces: `func (s *Session) OpenStreamResult(ctx context.Context, req StreamRequest) (io.ReadWriteCloser, protocol.OpenResultPayload, error)`
- Produces: `func SOCKS5ReplyForResult(result protocol.OpenResultPayload) byte`
- Consumes: Task 2 subprotocol and OpenResult, Task 5 Server strict mode, Task 6 relay propagation

Strict mode must not send SOCKS5 success before `accepted=true`. It must not accept DATA for a stream before that result. Open timeout resets only the stream.

- [x] **Step 1: Write failing Client tests**

Cover subprotocol negotiation, strict success, every OpenResult-to-SOCKS5 mapping, open timeout, failure cleanup, duplicate result, early DATA rejection, legacy fallback, and no credential logging.

- [x] **Step 2: Run tests and verify failure**

```bash
go test ./internal/client ./internal/proxy -run 'TestSessionOpen|TestSOCKS5Forward|TestSOCKS5Reply' -count=1
```

Expected failure: no strict open mode, result method, or mapped reply helper.

- [x] **Step 3: Implement client state and SOCKS5 mapping**

Preserve `OpenStreamConn` for legacy callers. Keep `SOCKS5Forward` fail-closed for remote validation and map only stable protocol codes.

- [x] **Step 4: Run Client verification**

```bash
go test ./internal/client ./internal/proxy -count=1
go test ./internal/client ./internal/proxy -race -count=1
go vet ./internal/client ./internal/proxy
```

Expected: all commands exit 0.

---

### Task 8: Authorization revision integration and caching stream authorizer

**Files:**

- Create: `internal/server/authorization_cache.go`
- Create: `internal/server/authorization_cache_test.go`
- Modify: `internal/auth/credential_service.go`
- Test: `internal/auth/credential_service_test.go`
- Modify: `internal/storage/repository.go`
- Modify: `internal/storage/authorization_revision.go`
- Test: `internal/storage/storage_contract_test.go`
- Modify: `internal/server/runtime.go`
- Test: `internal/server/runtime_test.go`
- Modify: `docs/operations/configuration.md`
- Modify: `docs/operations/troubleshooting.md`

**Interfaces:**

- Produces: `type AuthorizationCacheConfig struct { Enabled bool; PositiveTTL time.Duration; NegativeTTL time.Duration; RevisionPollInterval time.Duration; MaxStaleOnPollError time.Duration; MaxEntries int }`
- Produces: `type AuthorizationRevisionSource interface { Current(context.Context) (uint64, error) }`
- Produces: `func NewCachingStreamAuthorizer(inner StreamAuthorizer, revisions AuthorizationRevisionSource, config AuthorizationCacheConfig, metrics *observability.Metrics) (StreamAuthorizer, func() error, error)`
- Consumes: Task 1 repository and Task 3 config/metrics

The returned cleanup function stops the poller. The cache is a Service decorator and never exposes repository types to handlers.

- [x] **Step 1: Write failing cache and transaction tests**

Cover:

- SQLite positive TTL 5 seconds and MySQL positive TTL 5 minutes defaults.
- Negative TTL and network-error behavior.
- Concurrent identical requests call the inner authorizer once.
- Revision increase invalidates the old generation.
- Token create/rotate/update/revoke bumps revision in the same transaction.
- User disable/enable/delete, Agent create/update/delete, and Policy create/update/delete bump revision.
- Poll failure stops serving allow entries after `MaxStaleOnPollError`.
- Revision missing, rollback, or overflow fails closed and reports unhealthy readiness.
- Local writes notify the cache immediately.
- Cache disabled bypasses all cache logic.

- [x] **Step 2: Run tests and verify failure**

```bash
go test ./internal/auth ./internal/storage ./internal/server -run 'TestAuthorization|TestCachingStreamAuthorizer|TestCredentialService' -count=1
```

Expected failure: repository writes do not bump revision and cache decorator does not exist.

- [x] **Step 3: Implement transactions, poller, and cache decorator**

Use generation-based invalidation, approximate LRU, singleflight, bounded entries, and fail-closed stale handling. Add readiness integration so an unhealthy revision source disables the cache and reports storage readiness failure.

- [x] **Step 4: Run authorization verification**

```bash
go test ./internal/auth ./internal/storage ./internal/server -count=1
go test ./internal/auth ./internal/storage ./internal/server -race -count=1
go vet ./internal/auth ./internal/storage ./internal/server
```

Expected: all commands exit 0.

---

### Task 9: Remote validation cache and singleflight

**Files:**

- Create: `internal/client/remote_validation_cache.go`
- Create: `internal/client/remote_validation_cache_test.go`
- Modify: `internal/client/remote_validation.go`
- Test: `internal/client/remote_validation_test.go`
- Modify: `internal/client/http_proxy_forward.go`
- Test: `internal/client/http_proxy_forward_test.go`
- Modify: `internal/client/socks5_forward.go`
- Test: `internal/client/socks5_forward_test.go`
- Modify: `docs/user-guide/client.md`

**Interfaces:**

- Produces: `type RemoteValidationCacheConfig struct { Endpoint string; PositiveTTL time.Duration; NegativeTTL time.Duration; Timeout time.Duration; MaxEntries int }`
- Produces: `func NewRemoteValidatorWithCache(config RemoteValidationCacheConfig) *RemoteValidator`
- Produces: `func (v *RemoteValidator) Invalidate()`
- Consumes: Task 3 configuration and metrics

Cache keys include protocol, Agent ID, normalized target, and an HMAC digest of local credentials. The random HMAC key is generated per process and never persisted or logged.

- [x] **Step 1: Write failing cache tests**

Cover positive/negative/network-error TTLs, concurrent singleflight, different credentials do not share entries, endpoint change invalidates, `Close` drains transport and cache, bounded entry eviction, and no sensitive value appears in errors, logs, or metric labels.

- [x] **Step 2: Run tests and verify failure**

```bash
go test ./internal/client -run 'TestRemoteValidation' -count=1
```

Expected failure: current validator always performs one HTTP request per call.

- [x] **Step 3: Implement cache and singleflight**

Use the existing pooled `http.Transport`, bounded timeout, random process HMAC key, and explicit cache result classification.

- [x] **Step 4: Run client verification**

```bash
go test ./internal/client -count=1
go test ./internal/client -race -count=1
go vet ./internal/client
```

Expected: all commands exit 0.

---

### Task 10: Per-stream queues, flow control, and fair writer

**Files:**

- Create: `internal/session/frame_demux.go`
- Create: `internal/session/bounded_queue.go`
- Create: `internal/session/fair_writer.go`
- Create tests in the same package.
- Modify: `internal/client/session.go`
- Modify: `internal/server/ws_client.go`
- Modify: `internal/server/session_manager.go`
- Modify: `internal/agent/session.go`
- Modify: `internal/protocol/stream.go`
- Test: `internal/client/session_test.go`
- Test: `internal/server/ws_client_test.go`
- Test: `internal/server/session_test.go`
- Test: `internal/agent/session_test.go`
- Modify: `docs/protocol/proxy-modules.md`

**Interfaces:**

- Produces: `type BoundedFrameQueue struct`
- Produces: `func NewBoundedFrameQueue(maxBytes int) *BoundedFrameQueue`
- Produces: `func (q *BoundedFrameQueue) TryPush(protocol.Frame) bool`
- Produces: `func (q *BoundedFrameQueue) Pop() (protocol.Frame, bool)`
- Produces: `type FairFrameWriter struct`
- Produces: `func NewFairFrameWriter(sender func(protocol.Frame) error, config FairWriterConfig) *FairFrameWriter`
- Produces: `type FairWriterConfig struct { ControlQueueSize int; StreamQueueBytes int; QuantumBytes int }`
- Produces: `func (w *FairFrameWriter) EnqueueControl(protocol.Frame) error`
- Produces: `func (w *FairFrameWriter) EnqueueData(streamID uint32, frame protocol.Frame) error`
- Produces: `func (w *FairFrameWriter) Run(ctx context.Context) error`
- Produces: `func (w *FairFrameWriter) Close() error`
- Consumes: Task 2 capabilities and Task 3 windows/metrics

The shared components live in `internal/session` and are adapted by Client, Server, and Agent without duplicating three implementations.

- [x] **Step 1: Write failing data-plane tests**

Cover:

- One slow stream does not block PING/PONG or another stream.
- Byte-bounded queues reject promptly.
- Window consumption, threshold update, overflow, and over-send behavior.
- Control frames are served before data.
- Deficit round-robin prevents one bulk stream from starving small streams.
- Control queue exhaustion closes the session; data queue exhaustion resets only one stream.
- Half-close, EOF, duplicate ID, reset, and close races.
- Payload ownership is copied at most once per receive path.
- Goroutines and queues return to baseline after close.

- [x] **Step 2: Run tests and verify failure**

```bash
go test ./internal/session ./internal/client ./internal/server ./internal/agent -run 'TestBoundedFrameQueue|TestFairFrameWriter|TestFlowControl|Test.*StreamIsolation' -count=1
```

Expected failure: shared queue/writer types do not exist and receive loops still block on per-stream channels.

- [x] **Step 3: Implement shared queue, demux, and writer**

Integrate per-component. In legacy mode use bounded queues and `RESET`; in strict mode use negotiated windows and `WINDOW_UPDATE`. Never switch semantics mid-stream.

- [x] **Step 4: Run data-plane verification**

```bash
go test ./internal/session ./internal/client ./internal/server ./internal/agent -count=1
go test ./internal/session ./internal/client ./internal/server ./internal/agent -race -count=1
go vet ./internal/session ./internal/client ./internal/server ./internal/agent
```

Expected: all commands exit 0.

---

### Task 11: Local-agent affinity and pool latency signals

**Files:**

- Modify: `internal/server/connection_selector.go`
- Test: `internal/server/connection_selector_test.go`
- Modify: `internal/agent/connection_controller.go`
- Test: `internal/agent/connection_controller_test.go`
- Modify: `internal/agent/connection_pool.go`
- Test: `internal/cli/agent_runtime_test.go`
- Modify: `docs/operations/connection-pool.md`
- Modify: `docs/user-guide/agent.md`

**Interfaces:**

- Produces: `type ConnectionSelectionPolicy uint8` with `SelectionLeastStreams` and `SelectionLocalPreferred`
- Produces: `func SelectAgentConnection(policy ConnectionSelectionPolicy, currentServerNodeID string, candidates []AgentConnectionCandidate) (AgentConnectionCandidate, error)`
- Produces: `type AgentConnectionCandidate struct { AgentID string; InstanceID string; ConnectionID string; ServerNodeID string; ActiveStreams int; HealthScore int; RTT time.Duration; Local bool }`
- Extends `ConnectionStats` with `PendingDials`, `OpenP95`, `TTFBP95`, and `WriterQueueWait`

Selection remains authorization-first. Local preference is only a performance ordering and must not bypass policy or fail when only remote candidates exist.

- [x] **Step 1: Write failing selection tests**

Cover local healthy preference, remote fallback, least-stream tie-break, unhealthy candidate filtering, equal-locality ordering, and no candidate stable error.

- [x] **Step 2: Run tests and verify failure**

```bash
go test ./internal/server ./internal/agent -run 'Test.*ConnectionSelection|TestConnectionController' -count=1
```

Expected failure: policy type and latency signals do not exist.

- [x] **Step 3: Implement policy and pool inputs**

Preserve defaults `min=1/max=1`. Scale-up decisions may use pending dials, open P95, TTFB P95, writer wait, active streams, RTT, and error rate.

- [x] **Step 4: Run selection verification**

```bash
go test ./internal/server ./internal/agent ./internal/cli -count=1
go test ./internal/server ./internal/agent -race -count=1
go vet ./internal/server ./internal/agent ./internal/cli
```

Expected: all commands exit 0.

---

### Task 12: E2E, dashboards, documentation, and PR package

**Files:**

- Create: `internal/e2e/socks5_web_page_latency_test.go`
- Modify: `deploy/grafana/dashboards/tunnelmesh.json`
- Modify: `docs/README.md`
- Modify: `docs/user-guide/client.md`
- Modify: `docs/user-guide/agent.md`
- Modify: `docs/operations/configuration.md`
- Modify: `docs/operations/connection-pool.md`
- Modify: `docs/operations/troubleshooting.md`
- Modify: `docs/operations/network-probes.md`
- Modify: `docs/operations/schema-upgrades.md`
- Modify: `docs/protocol/proxy-modules.md`
- Create: `docs/pull-requests/2026-09-11-socks5-web-page-latency.md`

**Interfaces:**

- Consumes all Tasks 1–11.
- Produces the final E2E and documentation package; no new public API.

- [x] **Step 1: Write failing E2E tests**

Cover:

- 32 concurrent SOCKS5 CONNECTs where one target dial blocks; other opens complete.
- One refused target does not reconnect the Agent session.
- Strict Client/Server/Agent path returns SOCKS5 success only after `OPEN_RESULT(ok)`.
- Legacy Client and legacy Agent combinations remain functional.
- Local and cross-node relay paths both propagate strict results.
- Multi-Server authorization revocation blocks new streams within 3 seconds under a healthy revision poller.
- One bulk stream does not unboundedly delay small-stream TTFB.
- Session close releases all dial, queue, writer, and timer goroutines.

- [x] **Step 2: Run E2E and verify failure before integration fixes**

```bash
go test ./internal/e2e -run 'TestSOCKS5WebPageLatency' -count=1
```

Expected failure before Task 12 integration: at least strict-open, concurrent isolation, or revocation assertions fail. If Tasks 1–11 already made a case pass, keep the test as regression coverage and do not fake a failure.

- [x] **Step 3: Integrate final wiring and update docs**

Update the single Grafana dashboard, all listed user and operations documents, schema upgrade guide, and PR description. Ensure `docs/README.md` links every changed document.

- [x] **Step 4: Run full backend verification**

```bash
go test ./... -count=1
go test ./... -race
go vet ./...
go build ./cmd/...
git diff --check
```

Expected: all commands exit 0. Record actual output in the PR description.

- [x] **Step 5: Run frontend verification if web files changed**

If and only if this implementation changes `web/` or embedded assets:

```bash
cd web
npm test -- --run
npm run build
```

Then verify the generated `web/dist` is synchronized with `internal/server/web_dist`.

- [x] **Step 6: Prepare handoff without unauthorized git operations**

Review `git diff` for secrets, credentials, `.env`, `node_modules`, or generated caches. Do not commit, push, merge, or create a PR unless the user explicitly authorizes it.

---

## Rollback Notes

- Roll back application binaries first; retain the v9 `authorization_revision` table because it is forward-compatible and ignored by v8 code.
- Disable `server.authorization_cache.enabled` to bypass the new cache without schema rollback.
- Disable strict-open and flow-control capabilities to force new connections back to legacy mode; existing strict connections should drain and reconnect.
- Reset Agent pool settings to `min=1/max=1`.
- If exact schema restoration is required, restore the pre-upgrade database backup rather than issuing guessed reverse DDL.
- Keep a validated v8 database backup before applying v9 in production.

## Verification Summary

Final implementation must run:

```bash
go test ./... -count=1
go test ./... -race
go vet ./...
go build ./cmd/...
git diff --check
```

If frontend or embedded assets change, also run:

```bash
cd web
npm test -- --run
npm run build
```

MySQL contract tests must run when `TUNNELMESH_TEST_MYSQL_DSN` is available; otherwise record the explicit skip result. Docker verification is not required unless deployment files are changed.
