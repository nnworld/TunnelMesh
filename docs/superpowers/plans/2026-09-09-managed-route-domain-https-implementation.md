# Managed Route Domain and HTTPS Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Explicit managed HTTP/WebSocket routes can reach domain-only and HTTPS-only upstream services by independently controlling the dial address, upstream Host header, upstream scheme, and TLS SNI.

**Architecture:** Store the new options in the existing `tunnels.config` JSON column to avoid a schema migration. Decode that JSON into the route resolver, propagate the options through the relay request and `OPEN_STREAM` payload, and let the Agent wrap the target TCP connection in TLS. The public route protocol remains `http`/`websocket`; `targetScheme` describes only the upstream transport.

**Tech Stack:** Go standard-library `crypto/tls`, existing relay/protocol packages, Vue 3 + TypeScript + Element Plus, Vitest.

**Spec:** Confirmed user requirement: "完整支持。动态泛域名路由可以不支持" for explicit managed routes. Dynamic wildcard routes stay IPv4-only and retain plain HTTP upstream behavior.

## Global Constraints

- Work only in `/opt/app/workspace/TunnelMesh` on the current branch; do not use a worktree.
- Preserve all unrelated uncommitted changes.
- Do not commit, push, merge, or create a remote PR without explicit authorization.
- TDD is mandatory: each behavior must first fail a test, then receive the minimal implementation.
- Explicit route defaults: `targetScheme=http`; upstream Host defaults to `targetHost`; HTTPS SNI defaults to `targetHost`.
- TLS verification cannot be disabled. Use system roots, TLS 1.2+, and no `InsecureSkipVerify`.
- Dynamic routes do not gain domain targets, `hostHeader`, HTTPS upstream, or custom SNI.
- No schema migration; use `tunnels.config`.

---

### Task 1: API Contract and Persistence

**Files:**
- Modify: `internal/server/api.go`
- Test: `internal/server/api_test.go`

**Interfaces:**
- Produces first-class JSON request/response fields: `hostHeader`, `targetScheme`, `tlsServerName`.
- Produces `normalizeTunnelConfig(storage.Tunnel) (storage.Tunnel, string)` where the string is a validation error message; an empty string means valid.

- [x] **Step 1: Write failing API tests**

Add tests covering creation, patch, omission-preserving patch, invalid scheme, and `tlsServerName` with `targetScheme=http`.

```go
func TestAPIRouteUpstreamDomainAndTLSConfig(t *testing.T) {
    // Create with hostHeader=https SNI fields, assert public response and repository config.
    // PATCH only targetScheme=http and assert hostHeader is preserved.
    // PATCH targetScheme=ftp and tlsServerName+http, assert HTTP 400.
}
```

- [x] **Step 2: Verify RED**

Run: `go test ./internal/server -run TestAPIRouteUpstreamDomainAndTLSConfig -count=1`

Expected failure: request fields are ignored, so response and persisted config do not contain them.

- [x] **Step 3: Implement minimal API behavior**

Add the fields to tunnel request structs, normalize/trim them, persist them into `tunnels.config`, and expose them in `publicTunnel`.

- [x] **Step 4: Verify GREEN**

Run: `go test ./internal/server -run TestAPIRouteUpstreamDomainAndTLSConfig -count=1`

### Task 2: Resolver and Route Table

**Files:**
- Modify: `internal/routing/matcher.go`
- Modify: `internal/server/managed_route_handler.go`
- Test: `internal/routing/routing_test.go`
- Test: `internal/server/managed_route_handler_test.go`

**Interfaces:**
- Consumes JSON keys from Task 1.
- Produces `routing.Route.HostHeader`, `routing.Route.TargetScheme`, and `routing.Route.TLSServerName`.

- [x] **Step 1: Write failing tests**

Assert active routes decode all three fields, malformed config fails safely, and dynamic routes leave them empty.

- [x] **Step 2: Verify RED**

Run: `go test ./internal/routing ./internal/server -run 'ManagedRoute|RouteResolver' -count=1`

- [x] **Step 3: Implement route fields and JSON decoding**

Decode `storage.Tunnel.Config` into a small private struct. Invalid JSON is returned as a route-table error, not a panic.

- [x] **Step 4: Verify GREEN**

Run the same focused tests.

### Task 3: HTTP Proxy Host Rewrite and Request Propagation

**Files:**
- Modify: `internal/server/http_proxy.go`
- Test: `internal/server/http_proxy_test.go`

**Interfaces:**
- Consumes Task 2 route fields.
- Produces `relay.StreamRequest{TargetScheme, HostHeader, TLSServerName}`.

- [x] **Step 1: Write failing tests**

Use the existing recording opener to assert both ordinary HTTP and WebSocket upgrade requests send `Host: service.internal.example.com` and propagate scheme/SNI. Also assert empty `hostHeader` defaults to `targetHost`.

- [x] **Step 2: Verify RED**

Run: `go test ./internal/server -run 'HTTPProxy.*Upstream|HTTPProxy.*WebSocket' -count=1`

- [x] **Step 3: Implement minimal propagation**

Use `route.HostHeader`, defaulting to `TargetHost`, in both request writers and include all options in both `OpenStream` calls.

- [x] **Step 4: Verify GREEN**

Run the focused proxy tests.

### Task 4: Relay and Protocol Propagation

**Files:**
- Modify: `internal/relay/service.go`
- Modify: `internal/relay/transport.go`
- Modify: `internal/server/agent_relay_transport.go`
- Test: `internal/protocol/stream_open_test.go`
- Test: `internal/server/agent_relay_transport_test.go`
- Test: `internal/relay/transport_test.go`

**Interfaces:**
- Produces optional payload JSON fields: `target_scheme`, `host_header`, `tls_server_name`.
- Produces equivalent gRPC metadata keys.

- [x] **Step 1: Write failing tests**

Assert exact JSON omits empty fields; local Agent relay and cross-node gRPC preserve all values.

- [x] **Step 2: Verify RED**

Run: `go test ./internal/protocol ./internal/relay ./internal/server -run 'StreamOpen|AgentRelay|GRPC.*Stream' -count=1`

- [x] **Step 3: Implement transport propagation**

Add fields to `StreamRequest`, `StreamOpenPayload`, Agent relay payload construction, and gRPC metadata encode/decode.

- [x] **Step 4: Verify GREEN**

Run the focused protocol and relay tests.

### Task 5: Agent TLS Dialing

**Files:**
- Modify: `internal/agent/dialer.go`
- Modify: `internal/agent/session.go`
- Test: `internal/agent/session_test.go`

**Interfaces:**
- Consumes `protocol.StreamOpenPayload`.
- Produces verified TLS wrapping for `target_scheme=https`.

- [x] **Step 1: Write failing tests**

Use a local TLS listener to assert SNI, certificate verification, TLS 1.2 minimum, default SNI, and failure for an untrusted certificate.

- [x] **Step 2: Verify RED**

Run: `go test ./internal/agent -run 'Stream.*TLS|Dial.*TLS' -count=1`

- [x] **Step 3: Implement minimal Agent dialing**

Change the internal dial callback to receive the stream payload, dial TCP, optionally wrap with `tls.Client`, set `ServerName` to `TLSServerName` or `TargetHost`, and perform a context-aware handshake.

- [x] **Step 4: Verify GREEN**

Run the focused Agent tests.

### Task 6: Web Management UI

**Files:**
- Modify: `web/src/api/client.ts`
- Modify: `web/src/views/Routes.vue`
- Modify: `web/src/i18n/messages/zh-CN.ts`
- Modify: `web/src/i18n/messages/en-US.ts`
- Test: `web/src/tests/routes.spec.ts`

**Interfaces:**
- Consumes Task 1 API fields.
- Produces create/update payloads and edit-form prefills for all new fields.

- [x] **Step 1: Write failing frontend tests**

Assert API payload serialization and that Routes.vue contains scheme select, Host header, TLS server name, and edit prefill.

- [x] **Step 2: Verify RED**

Run: `npm --prefix web test -- --run src/tests/routes.spec.ts`

- [x] **Step 3: Implement UI and types**

Add fields to types and form, show TLS server name for HTTPS, provide Chinese/English labels and help text, and preserve empty values according to PATCH semantics.

- [x] **Step 4: Verify GREEN**

Run the focused frontend tests.

### Task 7: Contract, Documentation, and PR Notes

**Files:**
- Modify: `docs/api/openapi.yaml`
- Modify: `docs/user-guide/managed-http-route.md`
- Create: `docs/pull-requests/2026-09-09-managed-route-domain-https.md`

**Interfaces:**
- Produces the public API contract and user-facing semantics.

- [x] **Step 1: Update OpenAPI and user guide**

Document all new request/response fields, defaults, validation, and the IP-dial/domain-Host/HTTPS-SNI example.

- [x] **Step 2: Add PR description**

Include problem, solution, compatibility, test plan, rollback, and explicitly state that dynamic routes remain unchanged.

### Task 8: Full Verification and Embedded Assets

**Files:**
- Regenerate: `internal/server/web_dist/`

- [x] Run:

```bash
go test ./... -count=1
go test -race ./...
go vet ./...
git diff --check
npm --prefix web test -- --run
npm --prefix web run build
rsync -a --delete web/dist/ internal/server/web_dist/
./scripts/verify-web-embed.sh
```

- [x] Re-read the plan and confirm every requirement has an implementation and verification result.
