# Protocol Modules and Traceroute Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Add encrypted service-token reveal, logical multi-hop traceroute, protocol capability/error foundations, and the remaining approved proxy/control-plane modules without weakening existing security boundaries.

**Architecture:** Keep Handler → Service → Repository layering. Token secrets are encrypted at the service boundary with an injectable key provider. Traceroute is a request/response state machine carried over existing authenticated sessions, with signed hop envelopes and a persistent trace result. Protocol extensions remain capability-gated and are independently testable.

**Tech Stack:** Go standard library, existing Gorilla WebSocket/storage drivers, SQLite/MySQL-compatible DDL, Vue 3/TypeScript/Element Plus, Prometheus.

**Spec:** `docs/superpowers/specs/2026-09-07-protocol-modules-traceroute-design.md`

## Global Constraints

- DDL authority remains `migrations/ddl.sql`.
- API prefix remains `/api/v1` and responses remain `{code,msg,data}`.
- No secret is written to logs, metrics, ordinary token views, or default traceroute.
- Tests must be written and observed failing before production code.
- No ICMP/TUN/L2 VPN, P2P NAT traversal, or arbitrary remote command execution.

### Task 1: Token secret encryption primitives

**Files:** Create `internal/auth/secret_store.go`, `internal/auth/secret_store_test.go`.

- [x] Write tests for AES-GCM round trip, invalid key material, key id mismatch, and tamper rejection; run `go test ./internal/auth -run Secret -count=1` and observe failure.
- [x] Implement `SecretStore` with `Encrypt(secret) (ciphertext, nonce, keyID, version)` and `Decrypt(...)`, accepting base64/hex environment key material and never exposing key bytes.
- [x] Run the focused tests, then `go test ./internal/auth -count=1`.

### Task 2: Service-token schema and repository

**Files:** Modify `migrations/ddl.sql`, `internal/storage/models.go`, `internal/storage/repository.go`; add contract tests.

- [x] Add failing SQLite/MySQL contract tests proving ciphertext fields persist, round-trip, and update `secret_last_read_at`.
- [x] Add nullable encryption columns and repository method `MarkSecretRead`; preserve compatibility with existing hash-only rows.
- [x] Run storage contract tests for both drivers.

### Task 3: Reveal and rotation API

**Files:** Modify `internal/server/token_service.go`, `internal/server/token_api.go`, tests, and `docs/api/openapi.yaml`.

- [x] Add failing tests for admin-only permission, confirmation/idempotency, no-store response, audit, and hash-only token rejection.
- [x] Implement `POST /api/v1/tokens/{id}/reveal`; encrypt secrets on create/rotate and decrypt only inside the authorized service call.
- [x] Run focused server tests and update API documentation.

### Task 4: Traceroute domain and frames

**Files:** Create `internal/protocol/traceroute.go`, `internal/server/traceroute_service.go`, tests.

- [x] Add failing tests for hop ordering, signed-chain tamper detection, timeout, and redaction.
- [x] Implement trace request/result types, `TRACE_START/HOP/END`, HMAC chain, in-memory trace results, and bounded hop count.
- [x] Run protocol/server tests.

### Task 5: Traceroute management endpoints and relay integration

**Files:** Modify `internal/server/api.go`, `internal/server/web.go`, session/relay handlers; add API/E2E tests.

- [x] Add failing endpoint tests for `POST /agents/{id}/trace`, `GET /traces/{id}`, admin sensitive fields, and forbidden secret inclusion.
- [x] Wire management trace frames/domain through the authenticated server path and retain bounded completed results in the service.
- [x] Run API traceroute tests and update OpenAPI/user docs.

### Task 6: Protocol v2 foundations

**Files:** Modify `internal/protocol`, client/agent/server session code; add tests.

- [x] Add failing negotiation/error tests and retain existing flow-control/UDP-association/GOAWAY coverage.
- [x] Implement bounded capability negotiation, stable error codes, and trace frame types without enabling unadvertised features.
- [x] Run all protocol and session tests.

### Task 7: Approved proxy modules and policy

**Files:** Create focused packages under `internal/proxy`; modify routing/config/docs; add unit/integration tests.

- [x] Add failing parser tests for SOCKS5 TCP, HTTP CONNECT, and PROXY v2; document the remaining capability-gated adapters.
- [x] Implement bounded, injection-safe handshake parsers that can be attached to policy-authorized stream adapters.
- [x] Run proxy parser tests and document configuration.

### Task 8: Probe/cluster/observability completion

**Files:** Existing probe, registry, relay, observability, and web admin files plus docs.

- [x] Add failing tests for trace context propagation; retain existing probe, lease, relay, and metric coverage.
- [x] Implement W3C traceparent propagation helpers and the admin token/traceroute views.
- [x] Run full Go/frontend verification and `git diff --check`.
