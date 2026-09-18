# Enterprise capability roadmap design

Status: proposed, awaiting user confirmation before any implementation.

## 1. Background

Users asked TunnelMesh to close the gap with enterprise tunnel/zero-trust products
(`frp`, `ngrok`, `cloudflared`, Teleport, Cloudflare Access) on nine capabilities:

1. SSO / OIDC
2. MFA / device trust
3. Policy templates
4. Audit export
5. Access diagnostics enhancement
6. Rate limiting and concurrency policy
7. Session observability
8. Configuration as code
9. Webhook / API automation

All nine are accepted. This document is the design record that fixes the phasing,
the shared architectural decisions, and the boundaries. Each phase gets its own
detailed design spec and its own implementation plan, and each plan must be
confirmed by the user before implementation starts. Phase A is fully specified in
`docs/superpowers/plans/2026-09-18-sso-mfa-device-trust-implementation.md`.

## 2. Phasing

Phases are ordered by dependency, not by size. Later phases consume contracts
introduced by earlier ones.

| Phase | Capabilities | Schema | Detailed design |
| --- | --- | --- | --- |
| A | SSO / OIDC, MFA / device trust | v13 → v14 | [design](2026-09-18-sso-mfa-device-trust-design.md) · [plan](../plans/2026-09-18-sso-mfa-device-trust-implementation.md) |
| B | Policy templates, rate limiting and concurrency policy, session observability, audit export | v14 → v15 | authored and confirmed after Phase A merges |
| C | Configuration as code, webhook / API automation, access diagnostics enhancement | v15 → v16 | authored and confirmed after Phase B merges |

Rationale:

- Phase A is the identity foundation. Rate limiting (B) keys on the principal and
  token identity that A hardens; webhooks (C) must authenticate with the same
  secret-storage pattern A reuses; configuration as code (C) must be able to
  express SSO providers and MFA policy created in A.
- Policy templates (B) depend on the existing `agent_policies` contract only, but
  are grouped with rate limiting because both are evaluated inside the same
  authorization decision path (`internal/routing` + `internal/server/stream_authorizer.go`),
  and shipping them together avoids two incompatible decision-record formats.
- Access diagnostics (C) is last because its output must explain decisions made by
  the Phase B policy engine and must be publishable through Phase C webhooks.

Each phase is independently releasable as a MINOR version. No phase contains a
destructive schema change, so rolling upgrade stays safe.

## 3. Shared architectural decisions

### 3.1 Layering

Every capability keeps `Handler → Service → Repository`:

- Handlers live in `internal/server/*_api.go`, decode transport, enforce
  authorization, render the `{ code, msg, data }` envelope, and never touch SQL.
- Services live in `internal/auth/`, `internal/server/`, or a new
  `internal/governance/` package. They own validation, orchestration, and audit
  emission.
- Repositories live in `internal/storage/` behind interfaces so SQLite, MySQL, and
  in-memory fakes are interchangeable in tests.

New capability domains get their own package instead of growing `internal/server`:

- `internal/auth/oidc/` — OIDC relying party (Phase A).
- `internal/governance/` — policy templates, rate limiting, session observability
  (Phase B).
- `internal/automation/` — declarative bundles, webhooks (Phase C).

### 3.2 Authoritative data source

Administrative configuration that operators change at runtime is stored in the
database and is authoritative there. Process configuration (`internal/config`)
only seeds defaults on an empty database and always wins for values that cannot
change without a restart (listen addresses, TLS material, key injection).

Concretely: MFA policy, device-trust policy, SSO providers, policy templates,
rate-limit rules, webhook endpoints, and desired-state bundle revisions are all
database rows. This avoids the dual-source drift that a config-file-only design
would create in cluster mode.

### 3.3 Secrets

All recoverable secrets reuse the existing `internal/auth/secret_store.go`
AES-256-GCM store keyed by `TUNNELMESH_TOKEN_ENCRYPTION_KEY` /
`TUNNELMESH_TOKEN_ENCRYPTION_KEY_ID`. Nothing new is invented:

- Phase A: OIDC `client_secret`, TOTP shared secret, PKCE `code_verifier` held in
  a short-lived challenge row.
- Phase B: nothing recoverable; rate-limit counters and templates are not secrets.
- Phase C: webhook HMAC signing secret, bundle-declared token secrets.

Rule carried by all phases: when the encryption key is absent, features that need
a recoverable secret fail closed with HTTP 503 and a stable error code. They never
silently degrade to plaintext storage. Values that are never read back
(passwords, API tokens, recovery codes, device tokens, webhook delivery bodies)
stay one-way hashed.

### 3.4 Cluster correctness

TunnelMesh Server runs multi-node. Any per-request state that must survive a
redirect or a retry is externalized:

- Challenge, ticket, and OIDC state rows live in the database (Phase A).
- Rate-limit counters use database-backed atomic upserts for durable rules and a
  documented per-node token bucket for soft limits (Phase B).
- Webhook delivery state, retries, and the dead-letter queue live in the database
  and are claimed with a lease-style `UPDATE ... WHERE state='pending'` so two
  nodes never double-deliver (Phase C).

In-process caches stay advisory only and are invalidated through the existing
`authorization_revision` mechanism.

### 3.5 API conventions

All new endpoints follow the established contract:

- Prefix `/api/v1`, envelope `{ code, msg, data }`, failures carry a stable
  machine-readable string in `data.error`.
- Lists use cursor pagination (`cursor`, `limit`, response `nextCursor`,
  `hasMore`).
- Mutations that can be retried by a browser or an automation client require
  `Idempotency-Key` and reuse `internal/storage` idempotency records.
- Authorization is always evaluated server side from the bearer principal. Client
  supplied `userId`, `agentId`, or `role` values are never trusted.
- Every behaviour change is mirrored in `docs/api/openapi.yaml` in the same PR.

### 3.6 Observability

Each phase adds Prometheus metrics in `internal/observability/metrics.go`,
structured events in `internal/observability/events.go`, and audit rows through
the existing `AuditRepository`. Audit `details` never contain secrets, tokens,
passwords, private keys, full `Authorization` headers, TOTP codes, recovery
codes, or session payload bytes. Trace context propagates with the existing W3C
`traceparent` helper.

### 3.7 Web console

The Vue 3 + Element Plus console gains one navigation entry per capability
domain, always behind the existing `meta.auth` / `meta.admin` route guards:

- Phase A: SSO providers (admin), MFA and trusted devices in `AccountSecurity.vue`,
  an MFA step in `Login.vue`.
- Phase B: policy templates, rate-limit rules, live sessions, audit export.
- Phase C: desired-state bundles, webhook endpoints, diagnostics workbench.

`web/src/i18n/messages/zh-CN.ts` is the schema source of truth
(`MessageSchema = typeof zhCN`), so keys are added there first and `en-US.ts` must
match exactly or `npm test -- --run` fails.

## 4. Capability summaries

### 4.1 SSO / OIDC (Phase A)

OIDC relying party with Authorization Code + PKCE, discovery, JWKS caching,
strict `id_token` validation, group-to-role mapping, and just-in-time
provisioning. Providers are database-managed through an admin API. Detailed
design: `docs/superpowers/specs/2026-09-18-sso-mfa-device-trust-design.md`.

### 4.2 MFA / device trust (Phase A)

TOTP (RFC 6238) with encrypted shared secret, one-time recovery codes, per-user
and global enforcement policy, trusted devices with revocable tokens, and a
two-step login that keeps the existing single-step response shape when MFA is not
triggered. Same detailed design document as 4.1.

### 4.3 Policy templates (Phase B)

Named, versioned, reusable templates for agent policies and proxy-entry policies.
Built-in system templates ship read-only; operator templates are CRUD-managed.
Applying a template materializes concrete `agent_policies` rows and records the
template id plus revision on each row so drift can be detected and re-applied.
Evaluation semantics do not change: templates are an authoring convenience, the
enforced artifact remains the existing policy row validated on the Agent side.

### 4.4 Rate limiting and concurrency policy (Phase B)

A single decision layer that composes:

- Management API limits per user, per token, and per source IP.
- Proxy-entry limits per route, per agent, and per owner.
- Concurrency caps for active streams and active sessions per route/agent/user.

Durable rules are database rows; counters are database-backed for cross-node
accuracy on hard caps and per-node token buckets for soft limits. Rejections use
HTTP 429 with `Retry-After` for API traffic and the existing stable protocol
error codes for stream opens. Login/MFA brute-force throttling introduced in
Phase A becomes the first consumer of this layer.

### 4.5 Session observability (Phase B)

Live inventory of Agent connections, Client connections, WebSSH sessions, and
proxy streams with start time, bytes, duration, owning principal, route, and
close reason. Adds a session event timeline persisted with bounded retention, a
console live view, and admin close operations that already exist for connections.
No payload bytes are stored.

### 4.6 Audit export (Phase B)

Admin-only streaming export of `audit_logs` as JSONL or CSV with the existing
filter fields, a hard row cap per job, `Cache-Control: no-store`,
`Content-Disposition` with a deterministic filename, and an audit row describing
the export itself (filters, row count, format). Large ranges are streamed with
cursor pagination so memory stays flat; no response body is buffered.

### 4.7 Access diagnostics enhancement (Phase C)

Extends the existing logical traceroute and probe surface with a decision explain:
for one `(principal, route, target)` tuple the server returns each evaluated
policy, the matching rule, the deny reason, the resolved Agent connection, and the
probe result, with the same redaction rules as today. `includeSensitive=true`
remains admin-only; internal addresses stay redacted for normal users.

### 4.8 Configuration as code (Phase C)

A declarative YAML bundle (`apiVersion`, `kind: TunnelMeshBundle`, agents, routes,
policies, templates, SSO providers, webhook endpoints) with three verbs:

- `tunnelmesh-server bundle export` — dump current desired state.
- `tunnelmesh-server bundle diff -f bundle.yaml` — server-side plan, no writes.
- `tunnelmesh-server bundle apply -f bundle.yaml` — idempotent converge with
  `Idempotency-Key` and a stored revision row.

Secrets are referenced by name and resolved from the secret store or environment,
never embedded in the bundle. Apply is expand-only by default; deletion requires
`--prune` and an explicit confirmation flag.

### 4.9 Webhook / API automation (Phase C)

Event subscriptions with topic filters, HMAC-SHA256 signed deliveries, exponential
backoff with jitter, a dead-letter queue with alerting metrics, per-endpoint
delivery logs with redacted bodies, and an admin CRUD API. Initial topics:
`agent.online`, `agent.offline`, `route.changed`, `token.expiring`,
`session.closed`, `audit.*`, `bundle.applied`, `probe.failed`. Automation is
completed by bulk-read endpoints and full OpenAPI coverage so external systems can
drive TunnelMesh without scraping the console.

## 5. Global constraints for all phases

- Not implemented, by design: ICMP, TUN/L2 VPN, P2P NAT traversal, arbitrary
  remote command execution. SSH stays limited to the existing stdio/WebSocket
  proxy path.
- Public ingress remains HTTP/HTTPS/WebSocket only. No new public UDP listener.
- No plaintext secret is ever persisted, logged, exported, or returned by an API.
- Every schema change updates `migrations/ddl.sql`, the adjacent
  `migrations/incremental/vNNNN_to_vNNNN/{mysql,sqlite}.sql` pair,
  `storage.SchemaVersion`, migration tests, and `docs/operations/schema-upgrades.md`.
- Released incremental scripts are immutable; fixes go into the next version.
- Every phase updates `docs/api/openapi.yaml`, `docs/README.md`, the user guide,
  the operations guide, and the English mirrors under `docs/en/`.
- Validation per phase: `go test ./... -count=1`, `go test -race ./...`,
  `go vet ./...`, `git diff --check`, and for console changes
  `cd web && npm test -- --run && npm run build` plus `./scripts/verify-web-embed.sh`.
- `go test`, `go test -race`, and `go vet` stay out of CI; the user explicitly
  rejected adding them to the workflow.
- No commit, push, merge, or remote PR without explicit user authorization.

## 6. Risks and mitigations

| Risk | Mitigation |
| --- | --- |
| Login response shape change breaks existing clients | MFA is `disabled` by default; the `{token,user}` shape is unchanged unless a challenge is actually required |
| Users lock themselves out with MFA | Recovery codes printed once at enrollment, admin `mfa/reset` endpoint, existing admin bootstrap recovery flow stays intact |
| OIDC misconfiguration locks out the last admin | Local password login always remains available; admins created by bootstrap keep `auth_source=local` |
| Cluster nodes disagree on policy | All runtime policy is database-authoritative; in-process caches are invalidated through `authorization_revision` |
| Export or webhook leaks secrets | Export covers `audit_logs` only, which already forbids secrets; webhook bodies are redacted and signatures use a stored secret that is never returned |
| Scope creep inside one PR | One phase per PR set, each with its own confirmed plan |

## 7. Acceptance criteria for the roadmap

The roadmap is complete when all nine capabilities are merged with:

- documented API contracts in `docs/api/openapi.yaml`,
- console coverage with Chinese and English parity,
- migration coverage for both SQLite and MySQL,
- audit rows for every administrative mutation,
- Prometheus metrics for every new decision point,
- and a user guide section per capability.
