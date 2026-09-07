# Scoped Token and Connection Authorization Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Separate management, Agent, Client, and Server-node credentials and enforce Token scope plus Agent Policy on every connection and logical stream.

**Architecture:** Keep `api_tokens` as management-login sessions and introduce one `service_tokens` domain for `agent`, `client`, and `server_node` credentials. HTTP/WebSocket handlers parse transport input, `CredentialService` owns lifecycle and authorization, and repositories own SQL filtering, rotation, and last-used updates. Agent and Client WebSockets use distinct handlers and expected Token types; Server-node relay requires mTLS plus a `server_node` Token.

**Tech Stack:** Go 1.23, SQLite/MySQL, Cobra/Viper, x/net WebSocket, gRPC mTLS, Vue 3, Element Plus.

**Spec:** `docs/superpowers/specs/2026-09-06-security-observability-release-design.md`

## Global Constraints

- Modify schema only in `migrations/ddl.sql`; bump `SchemaVersion` and support upgrades from schema v2.
- Database stores only Token hashes and non-secret metadata; logs, audits, idempotency records, and API lists never contain Token secrets.
- Token status is derived from `revoked_at`, `expires_at`, and owner/resource state; do not add a second authoritative status column.
- `client` Token scope may narrow Agent Policy but never widen it.
- Every logical stream repeats Token scope and Agent Policy authorization.
- API responses use `{code,msg,data}` and cursor pagination; OpenAPI changes ship with the implementation.
- Legacy user Tokens for Agent connections are allowed only behind `security.allow_legacy_connection_tokens=false`, marked Deprecated, and removed no earlier than v0.3.0.
- Token create/rotate side effects are idempotent by `Idempotency-Key`. The first successful operation returns `secret`; a replay returns the same Token ID/metadata with `secret` omitted and `replayed=true`, an intentional secret-bearing endpoint exception documented in the ADR.
- Do not commit, push, merge, or publish unless the user gives explicit authorization after verification.

---

### Task 1: Record the credential-boundary ADR and schema migration

**Files:**
- Create: `docs/architecture/adr/0001-scoped-service-tokens.md`
- Modify: `migrations/ddl.sql`
- Modify: `internal/storage/db.go`
- Modify: `internal/storage/models.go`
- Test: `internal/storage/sqlite_test.go`
- Test: `internal/storage/mysql_test.go`

**Interfaces:**
- Produces: `storage.ServiceToken`, `storage.TokenType`, schema v3, and portable v2→v3 migration behavior.

- [ ] **Step 1: Write failing schema tests**

Add tests that open an existing schema-v2 database, run `Open(..., autoInit=true)`, and assert the following table and indexes exist:

```sql
service_tokens(
  id, token_type, owner_user_id, agent_id, node_id,
  token_prefix, token_hash, scope, expires_at,
  revoked_at, last_used_at, created_at, updated_at
)
```

Also assert `autoInit=false` fails when `service_tokens` is absent.

- [ ] **Step 2: Verify RED**

Run:

```bash
go test ./internal/storage -run 'Schema|ServiceToken' -count=1
```

Expected: failure because schema v3 and `service_tokens` do not exist.

- [ ] **Step 3: Add the portable schema and migration**

Add only the following new table family to `migrations/ddl.sql`:

```sql
CREATE TABLE IF NOT EXISTS service_tokens (
    id VARCHAR(255) PRIMARY KEY,
    token_type VARCHAR(32) NOT NULL,
    owner_user_id VARCHAR(255),
    agent_id VARCHAR(255),
    node_id VARCHAR(255),
    token_prefix VARCHAR(32) NOT NULL,
    token_hash VARCHAR(255) NOT NULL UNIQUE,
    scope TEXT NOT NULL,
    expires_at TEXT,
    revoked_at TEXT,
    last_used_at TEXT,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);
CREATE INDEX idx_service_tokens_owner_type ON service_tokens(owner_user_id, token_type, id);
CREATE INDEX idx_service_tokens_agent ON service_tokens(agent_id, token_type, id);
CREATE INDEX idx_service_tokens_node ON service_tokens(node_id, token_type, id);
```

Set `SchemaVersion = 3`, require the table in `requireSchemaTables`, and make the v2→v3 path execute the same authoritative DDL before updating `schema_meta`.

Define:

```go
type TokenType string
const (
    TokenTypeAgent TokenType = "agent"
    TokenTypeClient TokenType = "client"
    TokenTypeServerNode TokenType = "server_node"
)

type ServiceToken struct {
    ID, OwnerUserID, AgentID, NodeID, Prefix, TokenHash, Scope string
    Type TokenType
    ExpiresAt, RevokedAt, LastUsedAt *time.Time
    CreatedAt, UpdatedAt time.Time
}
```

- [ ] **Step 4: Write the ADR**

Record the decision to keep management sessions in `api_tokens`, store service credentials in `service_tokens`, derive state from timestamps, use mTLS plus Token for Server-node, and omit secrets on idempotency replay. Include alternatives rejected: one shared Token namespace, custom encryption, and recoverable Token ciphertext.

- [ ] **Step 5: Verify GREEN**

Run:

```bash
go test ./internal/storage -run 'Schema|ServiceToken' -count=1
git diff --check
```

### Task 2: Implement the ServiceToken repository contract

**Files:**
- Modify: `internal/storage/repository.go`
- Modify: `internal/storage/db.go`
- Test: `internal/storage/storage_contract_test.go`
- Create: `internal/storage/service_token_test.go`

**Interfaces:**
- Produces:

```go
type ServiceTokenFilter struct { OwnerUserID string; Type TokenType; AgentID string; NodeID string }
type ServiceTokenRepository interface {
    Create(context.Context, ServiceToken) error
    Get(context.Context, string) (ServiceToken, error)
    GetByHash(context.Context, string) (ServiceToken, error)
    List(context.Context, ServiceTokenFilter, string, int) (Page[ServiceToken], error)
    Revoke(context.Context, string, time.Time) error
    TouchLastUsed(context.Context, string, time.Time) error
    Rotate(context.Context, string, ServiceToken, time.Time) error
}
```

- [ ] **Step 1: Write failing repository contract tests**

Cover create/get-by-hash, SQL-level owner/type filtering before cursor pagination, expiry fields, revocation, last-used touch, atomic rotation, duplicate hash, and rotation rollback when replacement insert fails.

- [ ] **Step 2: Verify RED**

```bash
go test ./internal/storage -run ServiceTokenRepository -count=1
```

- [ ] **Step 3: Implement explicit-column SQLite/MySQL SQL**

Use no `SELECT *`. `Rotate` must start one transaction, lock the old row on MySQL with `FOR UPDATE`, reject an already revoked Token, insert the replacement, revoke the old row, and commit. SQLite uses the existing single-writer connection and transaction.

- [ ] **Step 4: Add DB wiring**

Add `serviceTokens ServiceTokenRepository` to `storage.DB`, initialize it in `newDB`, and expose `ServiceTokens()`. Add a transaction runner that supplies `ServiceTokenRepository` and `AuditRepository` for lifecycle operations.

- [ ] **Step 5: Verify GREEN on both contracts**

```bash
go test ./internal/storage -run ServiceTokenRepository -count=1
TUNNELMESH_TEST_MYSQL_DSN="$TUNNELMESH_TEST_MYSQL_DSN" go test ./internal/storage -run MySQL -count=1
```

Skip the MySQL command with an explicit report when the DSN is unavailable.

### Task 3: Add CredentialService lifecycle and scope authorization

**Files:**
- Create: `internal/auth/credential_service.go`
- Create: `internal/auth/credential_service_test.go`
- Modify: `internal/auth/service.go`
- Modify: `internal/auth/password.go`

**Interfaces:**
- Produces:

```go
type TokenScope struct {
    AgentIDs []string `json:"agentIds,omitempty"`
    Protocols []string `json:"protocols,omitempty"`
    TargetCIDRs []string `json:"targetCIDRs,omitempty"`
    TargetPorts []int `json:"targetPorts,omitempty"`
}
type TokenIdentity struct {
    TokenID, OwnerUserID, AgentID, NodeID, Prefix string
    Type storage.TokenType
    Scope TokenScope
}
func (s *CredentialService) Create(ctx context.Context, in CreateTokenInput) (CreatedToken, error)
func (s *CredentialService) ValidateAs(ctx context.Context, raw string, expected storage.TokenType) (TokenIdentity, error)
func (s *CredentialService) AuthorizeStream(ctx context.Context, id TokenIdentity, req StreamAuthorizationRequest) error
func (s *CredentialService) Rotate(ctx context.Context, id string) (CreatedToken, error)
func (s *CredentialService) Revoke(ctx context.Context, id string) error
```

- [ ] **Step 1: Write failing lifecycle tests**

Test valid types, invalid type crossover, owner/resource binding, empty/invalid scopes, expiration, revoke, rotate, disabled owner, disabled Agent, last-used updates, and that repository records never contain the returned raw secret.

- [ ] **Step 2: Write failing scope-intersection tests**

Use a client Token allowing `agent-a`, `tcp`, `10.0.0.0/24`, and port 22. Assert it accepts `10.0.0.8:22`, rejects another Agent/protocol/CIDR/port, and remains rejected when Token scope allows a target that Agent Policy denies.

- [ ] **Step 3: Verify RED**

```bash
go test ./internal/auth -run 'Credential|Scope' -count=1
```

- [ ] **Step 4: Implement lifecycle and authorization**

Generate 32 random bytes, encode URL-safe, hash with the existing token hash function, and expose only a short non-secret prefix. Validate scope at creation, normalize protocol names, parse CIDRs once per authorization request, and update `last_used_at` asynchronously through a bounded worker so authentication does not block on a best-effort timestamp write.

Keep `AuthService.ValidateToken` restricted to `api_tokens`; do not make management API accept a service Token.

- [ ] **Step 5: Verify GREEN and race safety**

```bash
go test ./internal/auth -run 'Credential|Scope' -count=1
go test -race ./internal/auth
```

### Task 4: Expose Token management API and audit events

**Files:**
- Create: `internal/server/token_service.go`
- Create: `internal/server/token_api.go`
- Create: `internal/server/token_api_test.go`
- Modify: `internal/server/api.go`
- Modify: `docs/api/openapi.yaml`

**Interfaces:**
- Produces:

```text
GET  /api/v1/tokens?type=&owner=&cursor=&limit=
POST /api/v1/tokens
GET  /api/v1/tokens/{id}
POST /api/v1/tokens/{id}/rotate
POST /api/v1/tokens/{id}/revoke
```

- [ ] **Step 1: Write failing API tests**

Cover 401, admin create all types, Agent owner create Agent/Client Token only for owned Agents, non-admin Server-node denial, cursor pagination, redacted list/detail, expiry validation, scope validation, revoke/rotate, audit contents, and no hash/secret leakage.

For idempotency assert: first response has `secret`, replay has identical Token ID and metadata, `secret` is absent, `replayed=true`, and `idempotency_keys.response` contains no secret.

- [ ] **Step 2: Verify RED**

```bash
go test ./internal/server -run TokenAPI -count=1
```

- [ ] **Step 3: Implement Handler → Service → Repository flow**

Keep request decoding and owner/admin checks in `token_api.go`; lifecycle orchestration in `token_service.go`; all persistence in repositories. Audit actions are `token.created`, `token.rotated`, `token.revoked`, and `token.authorization_denied`, with details limited to type, prefix, owner/resource IDs, and reason code.

- [ ] **Step 4: Update OpenAPI**

Document request/response schemas, one-time secret behavior, replay behavior, Token types/scopes, pagination, and 400/401/403/404/409 responses.

- [ ] **Step 5: Verify GREEN**

```bash
go test ./internal/server -run TokenAPI -count=1
go test ./internal/server ./internal/auth ./internal/storage -count=1
```

### Task 5: Enforce Agent Token and WebSocket boundary checks

**Files:**
- Modify: `internal/config/config.go`
- Modify: `internal/config/config_test.go`
- Create: `internal/server/tls_listener.go`
- Create: `internal/server/tls_listener_test.go`
- Modify: `internal/server/runtime.go`
- Modify: `internal/server/web.go`
- Modify: `internal/server/middleware.go`
- Modify: `internal/server/session_manager.go`
- Modify: `internal/agent/websocket.go`
- Test: `internal/server/runtime_test.go`
- Test: `internal/agent/session_test.go`

**Interfaces:**
- Produces: distinct `/ws/agent` authentication using `TokenTypeAgent`, Host/Origin allowlists, optional native TLS 1.2+ termination, and a Deprecated legacy migration flag.

- [ ] **Step 1: Write failing boundary tests**

Create Agent Tokens through `CredentialService`, then test missing Token, management Token, Client Token, wrong Agent binding, disabled/revoked/expired Agent Token, query-string Token, cookie Token, invalid Host, invalid Origin, valid CLI Origin, and valid Agent connection. Add native TLS tests for a valid test certificate, plaintext rejection on a TLS listener, minimum TLS 1.2, mismatched cert/key, and disabled TLS preserving the internal HTTP listener used behind Nginx.

- [ ] **Step 2: Verify RED**

```bash
go test ./internal/server -run 'Agent.*Token|Origin|Host' -count=1
```

- [ ] **Step 3: Add configuration**

Add:

```go
type SecurityConfig struct {
    AllowedHosts []string
    AllowedOrigins []string
    AllowLegacyConnectionTokens bool
}
type TLSConfig struct {
    Enabled bool
    CertFile string
    KeyFile string
    MinVersion string
}
```

Bind environment/CLI/file inputs, redact secrets, default the legacy flag and native TLS to false, and validate absolute HTTPS/WSS production URLs without permitting disabled certificate verification. When TLS is enabled, require both certificate and private-key paths and accept only `1.2` or `1.3` as the minimum version.

- [ ] **Step 4: Replace Agent authentication**

Use `CredentialService.ValidateAs(raw, TokenTypeAgent)` and require `identity.AgentID == hello.AgentID`. Copy `TokenID` into `AgentRegistration` for audit/metrics. Implement an exact path router so `/ws/client` and unknown `/ws/*` never reach the Agent handler.

Use `websocket.Server.Handshake` or an HTTP pre-upgrade check to enforce Host/Origin while allowing the Agent-generated Origin matching the Server URL.

When native TLS is enabled, wrap the existing listener with `tls.NewListener` using a `tls.Config` whose `MinVersion` is `tls.VersionTLS12` or `tls.VersionTLS13`. Load the key pair before reporting readiness and fail fast without starting a plaintext fallback listener. When TLS is disabled, keep the loopback/internal HTTP mode required by the Nginx deployment plan.

- [ ] **Step 5: Preserve the migration path**

When the explicit legacy flag is true, accept the old management Token only after the existing owner/admin check and emit a `deprecated_connection_token` warning/audit. Mark this config and code path Deprecated with a v0.3.0 removal note.

- [ ] **Step 6: Verify GREEN**

```bash
go test ./internal/server ./internal/agent -run 'Agent|Token|Origin|Host|NativeTLS' -count=1
go test -race ./internal/server ./internal/agent
```

### Task 6: Implement authenticated Client WebSocket and per-stream authorization

**Files:**
- Create: `internal/client/websocket.go`
- Create: `internal/client/websocket_test.go`
- Rewrite: `internal/server/ws_client.go`
- Create: `internal/server/ws_client_test.go`
- Create: `internal/server/stream_authorizer.go`
- Create: `internal/server/stream_authorizer_test.go`
- Modify: `internal/server/runtime.go`
- Modify: `internal/server/session_manager.go`
- Modify: `internal/client/session.go`
- Modify: `internal/config/config.go`
- Modify: `internal/cli/root.go`
- Test: `internal/cli/root_test.go`

**Interfaces:**
- Produces:

```go
type ClientRegistration struct { Token string }
type ClientSessionPrincipal struct { ConnectionID string; Identity auth.TokenIdentity }
type StreamAuthorizer interface {
    Authorize(context.Context, ClientSessionPrincipal, protocol.StreamOpenPayload) error
}
func RunWebSocket(ctx context.Context, serverURL, token string, onReady func(*client.Session) error) error
```

- [ ] **Step 1: Write failing Client WS tests**

Test exact `/ws/client`, expected Client Token type, revoked/expired Token, reconnect, PING/PONG, and that management/Agent/Server-node Tokens are rejected.

- [ ] **Step 2: Write failing stream authorization tests**

Send two OPEN frames on one authenticated connection: one target allowed by Token scope and Agent Policy and one denied by either layer. Assert only the first reaches `relay.NodeTransport.OpenStream`, denial returns a bounded RESET/error frame, and the connection remains usable.

- [ ] **Step 3: Verify RED**

```bash
go test ./internal/server ./internal/client -run 'ClientWS|StreamAuthorizer' -count=1
```

- [ ] **Step 4: Implement the Server receive loop**

Authenticate the HTTP upgrade with `TokenTypeClient`, derive user/Token identity on the Server, decode OPEN/DATA/HALF_CLOSE/RESET frames, call `StreamAuthorizer` on every OPEN, and route accepted streams through the existing local/cluster relay interface. Never trust owner or role values from frame payloads.

- [ ] **Step 5: Implement the Client dialer and CLI/config wiring**

Add `client.token` to configuration with JSON/YAML redaction, send it only in `Authorization`, and replace Client command stubs with the authenticated `client.Session` transport used by TCP/UDP/HTTP forward and stdio proxy paths.

- [ ] **Step 6: Verify GREEN and E2E behavior**

```bash
go test ./internal/server ./internal/client ./internal/cli -count=1
go test ./internal/e2e -run 'Client|Forward|SSH' -count=1
go test -race ./internal/server ./internal/client
```

### Task 7: Protect Server-node relay with Token plus mTLS

**Files:**
- Modify: `internal/relay/transport.go`
- Modify: `internal/relay/relay_test.go`
- Modify: `internal/server/runtime.go`
- Modify: `internal/config/config.go`
- Modify: `internal/config/config_test.go`

**Interfaces:**
- Produces: gRPC client/server interceptors that validate `TokenTypeServerNode` and bind `node_id` to the mTLS peer identity and Token binding.

- [ ] **Step 1: Write failing relay authentication tests**

Use test certificates and gRPC metadata to cover valid mTLS+Token, missing client certificate, wrong CA, wrong Token type, mismatched node ID, revoked Token, and stale epoch.

- [ ] **Step 2: Verify RED**

```bash
go test ./internal/relay -run 'MTLS|ServerNodeToken' -count=1
```

- [ ] **Step 3: Implement interceptors and configuration**

Require TLS client cert verification and `authorization: Bearer` metadata. Validate the certificate SAN against the registered node ID, then validate the `server_node` Token binding before `OpenStream` reads target fields.

- [ ] **Step 4: Verify GREEN**

```bash
go test ./internal/relay ./internal/server -count=1
go test -race ./internal/relay
```

### Task 8: Add Token management Web UI and migration documentation

**Files:**
- Modify: `web/src/App.vue`
- Modify: `web/src/router.ts`
- Modify: `web/src/api/client.ts`
- Create: `web/src/views/Tokens.vue`
- Create: `web/src/components/TokenSecretDialog.vue`
- Modify: `web/src/tests/routes.spec.ts`
- Modify: `docs/user-guide/server-admin.md`
- Modify: `docs/user-guide/agent.md`
- Modify: `docs/user-guide/client.md`
- Modify: `docs/deployment/docker.md`
- Modify: `docs/operations/configuration.md`
- Modify: `docs/operations/troubleshooting.md`

**Interfaces:**
- Produces: admin/owner Token lifecycle UI with one-time secret display and copy warning.

- [ ] **Step 1: Write failing frontend tests**

Assert `/tokens` route, role-aware type options, scope form, redacted list, one-time secret dialog, revoke confirmation, rotate result, expiry display, and no secret rendering after dialog close or page reload.

- [ ] **Step 2: Verify RED**

```bash
cd web && npm test -- --run src/tests/routes.spec.ts
```

- [ ] **Step 3: Implement Element Plus UI**

Use table filters and cursor loading; show Token prefix/ID/type/owner/binding/scope/last-used/expiry/state. Keep raw secret only in component memory and clear it on close/unmount.

- [ ] **Step 4: Update operator and migration docs**

Document backend creation/downloading workflow, Agent/Client config injection, revoke/rotate, the temporary legacy flag, rollout order, and rollback within five minutes. Replace examples that use login Tokens for Agent WS.

- [ ] **Step 5: Rebuild embedded assets and verify**

```bash
cd web
npm test -- --run
npm run build
cd ..
rm -rf internal/server/web_dist
cp -R web/dist internal/server/web_dist
go test ./internal/server -run Web -count=1
git diff --check
```

### Task 9: Security integration gate

**Files:**
- Create: `internal/e2e/scoped_token_connections_test.go`

**Interfaces:**
- Consumes all prior security tasks.

- [ ] **Step 1: Add E2E scenarios**

Cover admin creates Agent/Client Tokens, Agent connects with its Token, Client opens TCP and UDP streams within scope, cross-type Tokens fail, denied stream leaves WS alive, rotate revokes old connections on next authorization/lease boundary, and audits contain no secrets.

- [ ] **Step 2: Run the complete gate**

```bash
go test ./... -count=1
go test -race ./...
go vet ./...
git diff --check
cd web && npm test -- --run && npm run build
```

- [ ] **Step 3: Review staged content before any authorized commit**

```bash
git diff --cached
git grep -nE '(Bearer [A-Za-z0-9_-]{20,}|BEGIN (RSA|OPENSSH|EC) PRIVATE KEY|password=)' -- ':!web/node_modules'
```

Commit only if the user has explicitly authorized it, using scoped conventional commits such as `feat(auth): add scoped service tokens`.
