# Admin WebSSH/SFTP Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add admin-managed remote servers, SSH public-key credentials, and browser-local SSH/SFTP access through the existing Agent relay.

**Architecture:** The browser owns SSH/SFTP protocol handling and authentication material; the Server creates one-time WebSocket sessions and brokers binary bytes to an existing Agent TCP relay stream. Management data follows Handler → Service → Repository, with an expand-only schema migration from v11 to v12.

**Tech Stack:** Go `database/sql`, `golang.org/x/crypto/ssh`, existing `relay.NodeTransport`, existing `WSUpgrader`/`WSConn` interfaces, Vue 3, TypeScript, Vite, Pinia, Vue Router, Element Plus, `@xterm/xterm`, and a browser-compatible SSH/SFTP package.

**Spec:** `docs/superpowers/specs/2026-09-12-admin-webssh-sftp-design.md`

## Global Constraints

- Preserve Handler → Service → Repository layering; handlers never execute SQL.
- Preserve API prefix `/api/v1` and response `{ code, msg, data }`.
- Use cursor pagination, never offset pagination.
- Create and replace requests use `Idempotency-Key`; PATCH, DELETE, restore, and close keep the existing management API convention and accept the header only for replay-safe token operations.
- `SchemaVersion` changes from 11 to 12.
- Maintain `migrations/ddl.sql`, adjacent MySQL and SQLite migrations, and schema tests together.
- Never store SSH passwords, private keys, terminal output, or SFTP file contents. The private-key extraction API may receive a private key once in memory, but must never persist, log, audit, or relay it.
- The Server is only a byte-stream broker; it does not terminate SSH.
- The Agent performs its existing target-address and port policy checks.
- Every task follows red-green TDD and runs its listed verification before completion.
- Do not commit unless the user explicitly authorizes it in the current task.

---

### Task 1: Schema and Storage Models

**Files:**

- Modify: `internal/storage/models.go`
- Modify: `internal/storage/repository.go`
- Create: `internal/storage/credential_repository.go`
- Create: `internal/storage/credential_repository_test.go`
- Create: `internal/storage/remote_server_repository.go`
- Create: `internal/storage/remote_server_repository_test.go`
- Create: `internal/storage/webssh_session_repository.go`
- Create: `internal/storage/webssh_session_repository_test.go`
- Modify: `internal/storage/db.go`
- Modify: `migrations/ddl.sql`
- Create: `migrations/incremental/v0011_to_v0012/mysql.sql`
- Create: `migrations/incremental/v0011_to_v0012/sqlite.sql`
- Modify: `migrations/embed.go`
- Modify: `internal/storage/sqlite_test.go`
- Modify: `internal/storage/mysql_test.go`

**Interfaces:**

- Produces:

```go
type CredentialStatus string

const (
    CredentialStatusActive  CredentialStatus = "active"
    CredentialStatusDeleted CredentialStatus = "deleted"
    CredentialStatusAll     CredentialStatus = "all"
)

type CredentialType string

const CredentialTypeSSHPublicKey CredentialType = "ssh_public_key"

type Credential struct {
    ID          string
    OwnerUserID string
    Name        string
    Type        CredentialType
    PublicKey   string
    Fingerprint string
    Enabled     bool
    DeletedAt   *time.Time
    CreatedAt   time.Time
    UpdatedAt   time.Time
}

type CredentialFilter struct {
    OwnerUserID string
    Type        CredentialType
    Status      CredentialStatus
    Keyword     string
}

type CredentialRepository interface {
    Create(context.Context, Credential) error
    Get(context.Context, string) (Credential, error)
    Update(context.Context, Credential) error
    Delete(context.Context, string, time.Time) error
    Restore(context.Context, string) error
    List(context.Context, CredentialFilter, string, int) (Page[Credential], error)
}

type RemoteServerStatus string

const (
    RemoteServerStatusEnabled  RemoteServerStatus = "enabled"
    RemoteServerStatusDisabled RemoteServerStatus = "disabled"
    RemoteServerStatusDeleted  RemoteServerStatus = "deleted"
)

type RemoteServer struct {
    ID               string
    OwnerUserID      string
    Name             string
    Host             string
    Port             int
    DefaultUsername  string
    CredentialID     string
    AgentID          string
    Enabled          bool
    DeletedAt        *time.Time
    LastConnectedAt  *time.Time
    LastResult       string
    LastErrorClass   string
    CreatedAt        time.Time
    UpdatedAt        time.Time
}

type RemoteServerFilter struct {
    OwnerUserID string
    AgentID     string
    Status      RemoteServerStatus
    Keyword     string
}

type RemoteServerRepository interface {
    Create(context.Context, RemoteServer) error
    Get(context.Context, string) (RemoteServer, error)
    Update(context.Context, RemoteServer) error
    Delete(context.Context, string, time.Time) error
    Restore(context.Context, string) error
    List(context.Context, RemoteServerFilter, string, int) (Page[RemoteServer], error)
    UpdateConnectionResult(context.Context, string, string, string, time.Time) error
}

type WebSSHSessionStatus string

const (
    WebSSHSessionPending WebSSHSessionStatus = "pending"
    WebSSHSessionActive  WebSSHSessionStatus = "active"
    WebSSHSessionClosed  WebSSHSessionStatus = "closed"
    WebSSHSessionExpired WebSSHSessionStatus = "expired"
)

type WebSSHSession struct {
    ID               string
    OwnerUserID      string
    RemoteServerID   string
    AgentID          string
    OwnerNodeID      string
    TicketHash       string
    TicketExpiresAt  time.Time
    Status           WebSSHSessionStatus
    CreatedAt        time.Time
    ExpiresAt        time.Time
    ConnectedAt      *time.Time
    ClosedAt         *time.Time
    CloseReason      string
}

type WebSSHSessionRepository interface {
    Create(context.Context, WebSSHSession) error
    Get(context.Context, string) (WebSSHSession, error)
    ConsumeTicket(context.Context, string, string, time.Time) (WebSSHSession, error)
    Close(context.Context, string, string, time.Time) error
    CountActiveByOwner(context.Context, string, time.Time) (int, error)
    ListActiveByOwner(context.Context, string, time.Time) ([]WebSSHSession, error)
    ExpirePending(context.Context, time.Time) (int64, error)
    CloseExpiredActive(context.Context, time.Time) (int64, error)
}

func (d *DB) Credentials() CredentialRepository
func (d *DB) RemoteServers() RemoteServerRepository
func (d *DB) WebSSHSessions() WebSSHSessionRepository
```

- [x] **Step 1: Write failing storage contract tests**

Create `internal/storage/credential_repository_test.go`:

```go
package storage

import (
    "context"
    "testing"
    "time"
)

func TestCredentialRepositoryLifecycle(t *testing.T) {
    db := newTestDB(t)
    ctx := context.Background()
    now := time.Now().UTC()
    cred := Credential{
        OwnerUserID: "user-a", Name: "team-key", Type: CredentialTypeSSHPublicKey,
        PublicKey: "ssh-ed25519 AAAATEST", Fingerprint: "SHA256:test", Enabled: true,
    }
    if err := db.Credentials().Create(ctx, cred); err != nil { t.Fatal(err) }
    page, err := db.Credentials().List(ctx, CredentialFilter{OwnerUserID: "user-a"}, "", 10)
    if err != nil { t.Fatal(err) }
    if len(page.Items) != 1 || page.Items[0].Name != "team-key" { t.Fatalf("page = %+v", page.Items) }
    if err := db.Credentials().Delete(ctx, page.Items[0].ID, now); err != nil { t.Fatal(err) }
    active, err := db.Credentials().List(ctx, CredentialFilter{OwnerUserID: "user-a"}, "", 10)
    if err != nil { t.Fatal(err) }
    if len(active.Items) != 0 { t.Fatalf("active items = %+v", active.Items) }
    deleted, err := db.Credentials().List(ctx, CredentialFilter{OwnerUserID: "user-a", Status: CredentialStatusDeleted}, "", 10)
    if err != nil { t.Fatal(err) }
    if len(deleted.Items) != 1 { t.Fatalf("deleted items = %+v", deleted.Items) }
}
```

Create `internal/storage/remote_server_repository_test.go`:

```go
func TestRemoteServerRepositoryOwnerFilterAndCursor(t *testing.T) {
    db := newTestDB(t)
    ctx := context.Background()
    for _, name := range []string{"a", "b", "c"} {
        server := RemoteServer{OwnerUserID: "user-a", Name: name, Host: "10.0.0.8", Port: 22, DefaultUsername: "deploy", AgentID: "agent-a", Enabled: true}
        if err := db.RemoteServers().Create(ctx, server); err != nil { t.Fatal(err) }
    }
    page, err := db.RemoteServers().List(ctx, RemoteServerFilter{OwnerUserID: "user-a"}, "", 2)
    if err != nil { t.Fatal(err) }
    if len(page.Items) != 2 || !page.HasMore || page.NextCursor == "" { t.Fatalf("page = %+v", page) }
    next, err := db.RemoteServers().List(ctx, RemoteServerFilter{OwnerUserID: "user-a"}, page.NextCursor, 2)
    if err != nil { t.Fatal(err) }
    if len(next.Items) != 1 || next.HasMore { t.Fatalf("next = %+v", next) }
    other, err := db.RemoteServers().List(ctx, RemoteServerFilter{OwnerUserID: "user-b"}, "", 10)
    if err != nil { t.Fatal(err) }
    if len(other.Items) != 0 { t.Fatalf("other owner leaked data: %+v", other.Items) }
}
```

Create `internal/storage/webssh_session_repository_test.go`:

```go
func TestWebSSHSessionTicketConsumedOnce(t *testing.T) {
    db := newTestDB(t)
    ctx := context.Background()
    now := time.Now().UTC()
    session := WebSSHSession{ID: "webssh-1", OwnerUserID: "user-a", RemoteServerID: "server-1", AgentID: "agent-a", OwnerNodeID: "server-node-a", TicketHash: "hash", TicketExpiresAt: now.Add(30*time.Second), Status: WebSSHSessionPending, CreatedAt: now, ExpiresAt: now.Add(8*time.Hour)}
    if err := db.WebSSHSessions().Create(ctx, session); err != nil { t.Fatal(err) }
    if _, err := db.WebSSHSessions().ConsumeTicket(ctx, "webssh-1", "hash", now); err != nil { t.Fatal(err) }
    if _, err := db.WebSSHSessions().ConsumeTicket(ctx, "webssh-1", "hash", now); err == nil {
        t.Fatal("expected second ticket use to fail")
    }
}
```

Add migration assertions to SQLite and MySQL tests:

```go
func TestV11ToV12WebSSHMigrationsAreAdjacent(t *testing.T) {
    if SchemaVersion != 12 { t.Fatalf("SchemaVersion = %d, want 12", SchemaVersion) }
    if !strings.Contains(migrations.V11ToV12SQLite, "CREATE TABLE IF NOT EXISTS credentials") { t.Fatal("SQLite migration lacks credentials") }
    if !strings.Contains(migrations.V11ToV12MySQL, "CREATE TABLE IF NOT EXISTS remote_servers") { t.Fatal("MySQL migration lacks remote_servers") }
    if !strings.Contains(migrations.V11ToV12MySQL, "CREATE TABLE IF NOT EXISTS webssh_sessions") { t.Fatal("MySQL migration lacks webssh_sessions") }
}
```

- [x] **Step 2: Run tests and verify red**

Run:

```bash
go test ./internal/storage -run 'Test(Credential|RemoteServer|WebSSH|V11ToV12)' -count=1
```

Expected result: compilation fails because `CredentialRepository`, `RemoteServerRepository`, `WebSSHSessionRepository`, DB accessors, and migration constants do not exist.

- [x] **Step 3: Implement schema and repositories**

1. Add the three tables to `migrations/ddl.sql`.
2. Create both `v0011_to_v0012` scripts with only `CREATE TABLE`/`CREATE INDEX` statements.
3. Bump `SchemaVersion` to 12 and embed the new scripts.
4. Add models, interfaces, SQL implementations, and DB fields/accessors.
5. Keep timestamps as RFC3339 strings in storage, converting at repository boundaries.
6. Implement `ConsumeTicket` as one condition update:

```sql
UPDATE webssh_sessions
SET status='active', connected_at=?
WHERE id=? AND ticket_hash=? AND status='pending' AND ticket_expires_at>?
```

If `RowsAffected() == 0`, return a stable `ErrWebSSHTicketInvalid`.

- [x] **Step 4: Run tests and verify green**

Run:

```bash
go test ./internal/storage -count=1
go test ./internal/storage -run 'TestV11ToV12' -count=1
go vet ./internal/storage
```

Expected result: all tests pass and `go vet` reports no issues.

---

### Task 2: Credential and Remote Server Services

**Files:**

- Create: `internal/server/credential_service.go`
- Create: `internal/server/credential_service_test.go`
- Create: `internal/server/remote_server_service.go`
- Create: `internal/server/remote_server_service_test.go`
- Modify: `internal/server/api.go`

**Interfaces:**

- Consumes repositories from Task 1.
- Produces:

```go
type CredentialService struct { /* repositories and policy dependencies */ }

func NewCredentialService(credentials storage.CredentialRepository, agents storage.AgentRepository, audits storage.AuditRepository) *CredentialService
func (s *CredentialService) Create(ctx context.Context, actor auth.Principal, input CredentialInput) (storage.Credential, error)
func (s *CredentialService) Update(ctx context.Context, actor auth.Principal, id string, input CredentialPatch) (storage.Credential, error)
func (s *CredentialService) Delete(ctx context.Context, actor auth.Principal, id string) error
func (s *CredentialService) Restore(ctx context.Context, actor auth.Principal, id string) (storage.Credential, error)
func (s *CredentialService) List(ctx context.Context, actor auth.Principal, filter CredentialListFilter, cursor string, limit int) (storage.Page[storage.Credential], error)
type CredentialListFilter = storage.CredentialFilter

// CredentialPatch contains pointer fields so PUT can distinguish an omitted
// field from a zero value. CredentialInput is the full-create/replace model.
type CredentialInput struct {
    Name      string
    Type      storage.CredentialType
    PublicKey string
    Enabled   bool
}
type CredentialPatch struct {
    Name      *string
    Type      *storage.CredentialType
    PublicKey *string
    Enabled   *bool
}

type RemoteServerService struct { /* repositories and policy dependencies */ }

func NewRemoteServerService(servers storage.RemoteServerRepository, credentials storage.CredentialRepository, agents storage.AgentRepository, policies storage.PolicyRepository, audits storage.AuditRepository) *RemoteServerService
func (s *RemoteServerService) Create(ctx context.Context, actor auth.Principal, input RemoteServerInput) (storage.RemoteServer, error)
func (s *RemoteServerService) Update(ctx context.Context, actor auth.Principal, id string, input RemoteServerPatch) (storage.RemoteServer, error)
func (s *RemoteServerService) Delete(ctx context.Context, actor auth.Principal, id string) error
func (s *RemoteServerService) Restore(ctx context.Context, actor auth.Principal, id string) (storage.RemoteServer, error)
func (s *RemoteServerService) List(ctx context.Context, actor auth.Principal, filter RemoteServerListFilter, cursor string, limit int) (storage.Page[storage.RemoteServer], error)
type RemoteServerListFilter = storage.RemoteServerFilter

// RemoteServerInput is the full-create/replace model; pointer fields in
// RemoteServerPatch distinguish omitted values from zero values.
type RemoteServerInput struct {
    Name            string
    Host            string
    Port            int
    DefaultUsername string
    CredentialID    string
    AgentID         string
    Enabled         bool
}
type RemoteServerPatch struct {
    Name            *string
    Host            *string
    Port            *int
    DefaultUsername *string
    CredentialID    *string
    AgentID         *string
    Enabled         *bool
}
```

Public key parsing must live in a helper so the same parser protects all create/update paths:

```go
func ParseOpenSSHPublicKey(raw string) (normalized string, fingerprint string, err error)
```

Supported algorithms in this iteration are `ssh-ed25519`, `ssh-rsa`, `ecdsa-sha2-nistp256`, `ecdsa-sha2-nistp384`, and `ecdsa-sha2-nistp521`.

- [x] **Step 1: Write failing service tests**

Cover these cases in Go table tests:

```go
cases := []struct{
    name string
    key string
    valid bool
}{
    {name: "ed25519", key: "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAI... valid-base64", valid: true},
    {name: "rsa", key: "ssh-rsa AAAAB3NzaC1yc2EAAAADAQABAAABgQ... valid-base64", valid: true},
    {name: "wrong-algorithm", key: "ssh-dss AAAAB3NzaC1kc3MAAACBA...", valid: false},
    {name: "invalid-base64", key: "ssh-ed25519 !!!!", valid: false},
    {name: "comment-only", key: "hello", valid: false},
}
```

Also test:

- Normal users can only read/write their own credentials and remote servers.
- Administrators can access all resources.
- A remote server rejects a deleted, disabled, or other owner's credential.
- A remote server rejects a deleted, disabled, or other owner's Agent.
- Logical deletion prevents new session creation but preserves the record.
- Restore checks the active-name uniqueness before restoring.
- Audit callbacks never receive public-key private material because the input model contains only public keys.

- [x] **Step 2: Run tests and verify red**

```bash
go test ./internal/server -run 'Test(CredentialService|RemoteServerService|ParseOpenSSHPublicKey)' -count=1
```

Expected result: new service constructors and types are undefined.

- [x] **Step 3: Implement services**

Implement input validation, owner filtering, authorization, logical delete/restore, audit emission, and stable errors. Keep the exact error strings in `internal/server/errors.go` or equivalent so tests can use `errors.Is`.

- [x] **Step 4: Run tests and verify green**

```bash
go test ./internal/server -run 'Test(CredentialService|RemoteServerService|ParseOpenSSHPublicKey)' -count=1
go test ./internal/server -count=1
go vet ./internal/server
```

---

### Task 3: Management REST API

**Files:**

- Create: `internal/server/credential_api.go`
- Create: `internal/server/credential_api_test.go`
- Create: `internal/server/remote_server_api.go`
- Create: `internal/server/remote_server_api_test.go`
- Modify: `internal/server/api.go`
- Modify: `docs/api/openapi.yaml`

**Interfaces:**

- Consumes services from Task 2.
- Produces routes:

```text
GET/POST        /api/v1/credentials
GET/PUT/PATCH   /api/v1/credentials/{id}
DELETE          /api/v1/credentials/{id}
POST            /api/v1/credentials/{id}/restore
GET/POST        /api/v1/remote-servers
GET/PUT/PATCH   /api/v1/remote-servers/{id}
DELETE          /api/v1/remote-servers/{id}
POST            /api/v1/remote-servers/{id}/restore
```

- Produces JSON view functions:

```go
func publicCredential(v storage.Credential) credentialResponse
func publicRemoteServer(v storage.RemoteServer, credentialName, agentName string, agentOnline bool) remoteServerResponse
```

- [x] **Step 1: Write failing API tests**

Use the existing in-memory SQLite API test setup. Cover:

- Unauthenticated request returns 401.
- Regular user receives an empty list rather than another owner's rows.
- Regular user receives 403 for another owner's ID.
- Create, update, patch, delete, restore, detail, and list all use the envelope.
- Cursor returns only the next page.
- Create and PUT honor `Idempotency-Key`; PATCH, DELETE, and restore do not require it.
- Invalid public key returns 400 without writing a row.
- Invalid port and username return 400.
- OpenAPI contains every operation and response schema.

- [x] **Step 2: Run tests and verify red**

```bash
go test ./internal/server -run 'Test(CredentialAPI|RemoteServerAPI)' -count=1
```

Expected result: route dispatch returns 404 and view functions are undefined.

- [x] **Step 3: Implement handlers and OpenAPI**

Handlers decode requests, call services, and render views only. Query parsing and cursor limits follow the existing API patterns. `PUT` requires every editable field; `PATCH` updates only provided fields using pointer input fields.

- [x] **Step 4: Run tests and verify green**

```bash
go test ./internal/server -run 'Test(CredentialAPI|RemoteServerAPI)' -count=1
go test ./internal/server -count=1
go vet ./internal/server
```

---

### Task 4: WebSSH Session API and Origin/Ticket Handshake

**Files:**

- Create: `internal/server/webssh_session_service.go`
- Create: `internal/server/webssh_session_service_test.go`
- Create: `internal/server/webssh_api.go`
- Create: `internal/server/webssh_api_test.go`
- Create: `internal/server/webssh_middleware.go`
- Create: `internal/server/webssh_middleware_test.go`
- Modify: `internal/server/api.go`

**Interfaces:**

- Consumes repositories and remote-server service.
- Produces:

```go
type WebSSHSessionService struct { /* repositories, services, limits */ }

func NewWebSSHSessionService(
    sessions storage.WebSSHSessionRepository,
    servers storage.RemoteServerRepository,
    credentials storage.CredentialRepository,
    agents storage.AgentRepository,
    leases storage.LeaseRepository,
    audits storage.AuditRepository,
    localNodeID string,
) *WebSSHSessionService
```

`leases.ListActiveByAgent(ctx, agentID)` is the online check. A non-empty
result with a non-expired lease makes the Agent eligible for session creation.
`localNodeID` is written to `webssh_sessions.owner_node_id` when the ticket is
created; an empty value is rejected so cluster routing can never be ambiguous.

```go
type WebSSHSessionLimits struct {
    TicketTTL        time.Duration // default 30s
    SessionTTL       time.Duration // default 8h
    MaxActivePerUser int           // default 5
    OpenTimeout      time.Duration // default 10s
    IdleTimeout      time.Duration // default 5m
}

type CreateWebSSHSessionInput struct {
    Username    string
    CredentialID string
}

type WebSSHSessionTicket struct {
    SessionID     string
    Ticket        string
    WebSocketPath string
}

func (s *WebSSHSessionService) Create(ctx context.Context, actor auth.Principal, serverID string, input CreateWebSSHSessionInput) (WebSSHSessionTicket, error)
func (s *WebSSHSessionService) Get(ctx context.Context, actor auth.Principal, sessionID string) (storage.WebSSHSession, error)
func (s *WebSSHSessionService) Close(ctx context.Context, actor auth.Principal, sessionID, reason string) error
func (s *WebSSHSessionService) AuthenticateTicket(ctx context.Context, sessionID, ticket string, now time.Time) (storage.WebSSHSession, storage.RemoteServer, error)

func validateWebSSHOrigin(security config.SecurityConfig, r *http.Request) error
```

- [x] **Step 1: Write failing tests**

Cover:

- Creating a session requires an enabled, non-deleted server, owner-matching Agent and credential, and a valid username.
- Pending ticket expiration rejects authentication.
- Wrong ticket hash rejects authentication.
- Second use of a valid ticket rejects authentication.
- User concurrency limit rejects creation.
- Empty local node identity rejects creation.
- Owner can close their session; another user gets 403; admin can close any session.
- Repeated close is idempotent.
- Origin allowlist rejects missing, malformed, and non-listed origins.
- API response includes only `sessionId`, `ticket`, and `websocketPath`.
- Audit contains IDs and result class only.

- [x] **Step 2: Run tests and verify red**

```bash
go test ./internal/server -run 'Test(WebSSHSession|WebSSHAPI|WebSSHOrigin)' -count=1
```

Expected result: service, middleware, and API types are undefined.

- [x] **Step 3: Implement service and API**

Generate a 256-bit random ticket with `crypto/rand`, return only its base64url representation, and persist only `sha256(ticket)`. Add routes for:

```text
POST   /api/v1/remote-servers/{id}/ssh-sessions
GET    /api/v1/ssh-sessions/{id}
DELETE /api/v1/ssh-sessions/{id}
```

- [x] **Step 4: Run tests and verify green**

```bash
go test ./internal/server -run 'Test(WebSSHSession|WebSSHAPI|WebSSHOrigin)' -count=1
go test ./internal/server -count=1
go vet ./internal/server
```

---

### Task 5: WebSSH Byte-Stream Broker

**Files:**

- Create: `internal/server/webssh_broker.go`
- Create: `internal/server/webssh_broker_test.go`
- Create: `internal/server/webssh_runtime.go`
- Create: `internal/server/webssh_connection_close.go`
- Create: `internal/server/webssh_connection_close_test.go`
- Modify: `internal/relay/service.go`
- Modify: `internal/relay/transport.go`
- Modify: `internal/relay/server_node_auth_test.go`
- Modify: `internal/server/web.go`
- Modify: `internal/server/runtime.go`
- Modify: `internal/server/runtime_test.go`
- Modify: `internal/cli/root.go`
- Modify: `internal/config/config.go`
- Modify: `internal/config/config_test.go`
- Modify: `docs/operations/config-examples.md`
- Modify: `docs/operations/configuration.md`

**Interfaces:**

- Consumes:

```go
// xNetWebSSHUpgrader adapts the existing golang.org/x/net/websocket server
// to the small WSUpgrader contract already used by TCPBridgeHandler.
type xNetWebSSHUpgrader struct{}

func (xNetWebSSHUpgrader) Upgrade(w http.ResponseWriter, r *http.Request) (WSConn, error)

type WebSSHBrokerDeps struct {
    Sessions  *WebSSHSessionService
    Opener    relay.NodeTransport
    Upgrade   WSUpgrader
    Security  config.SecurityConfig
    Limits    WebSSHSessionLimits
    Metrics   *observability.Metrics
    Audit     func(context.Context, WebSSHAuditEvent)
}

func NewWebSSHBroker(deps WebSSHBrokerDeps) *WebSSHBroker
```

Add this optional capability beside the existing `WSConn` interface:

```go
type deadlineWSConn interface {
    WSConn
    SetReadDeadline(time.Time) error
}
```

If the connection does not implement it, use the session TTL and SSH keepalive
instead of forcing a transport-specific idle timeout. The x/net adapter must
implement it.

For admin-initiated close, the broker also owns a process-local registry:

```go
type WebSSHActiveSession struct {
    Cancel context.CancelFunc
    WS     WSConn
}

func (h *WebSSHBroker) Register(sessionID string, session WebSSHActiveSession) error
func (h *WebSSHBroker) Unregister(sessionID string)
func (h *WebSSHBroker) CloseLocal(sessionID string) error
```

`DELETE /api/v1/ssh-sessions/{id}` marks the durable row closed and calls
`CloseLocal` when the local Server owns the session. Cluster mode routes the
request through a new, narrow authenticated Server-node control method:

```go
type WebSSHConnectionCloseService interface {
    Close(ctx context.Context, sessionID string) error
}

type WebSSHConnectionRelayClient interface {
    CloseWebSSHConnection(context.Context, relay.CloseWebSSHConnectionRequest) error
    Close() error
}

type ClusterWebSSHConnectionCloseService struct { /* dependencies */ }

func NewClusterWebSSHConnectionCloseService(
    sessions storage.WebSSHSessionRepository,
    nodes storage.NodeRepository,
    local *WebSSHBroker,
    localNodeID string,
    dialRelayNode func(context.Context, string, int64) (WebSSHConnectionRelayClient, error),
) *ClusterWebSSHConnectionCloseService
```

The relay request carries `session_id` and `requested_by_node_id`, and the
receiving runtime verifies the authenticated node identity before calling
`CloseLocal`. If the owner node is unavailable, the service returns a stable
503 mapping and leaves the durable row's final state to the sweeper; it does
not decrement `schema_meta` or mutate another node's process registry.

- Produces:

```go
func (h *WebSSHBroker) ServeHTTP(w http.ResponseWriter, r *http.Request)
func (h *WebSSHBroker) Handle(ctx context.Context, ws WSConn, sessionID, ticket string) error
```

Runtime exposes the broker under `/ws/webssh/`. `NewWebHandlerWithManagedRoutes` must route this prefix before SPA fallback.

Because `TCPBridgeHandler` currently requires a route resolver, this task
must add a reusable `websocket.Server` adapter rather than reusing the TCP
bridge's route-specific implementation. The adapter owns Origin validation in
the x/net `Handshake` callback, then delegates the established `*websocket.Conn`
to `WebSSHBroker.Handle`.

- [x] **Step 1: Write failing broker tests**

Use an in-memory WebSocket implementation and a fake relay opener. Cover:

- Binary WebSocket bytes reach the relay stream unchanged.
- Relay bytes reach WebSocket as binary messages.
- Non-binary WebSocket message closes the stream.
- Relay EOF closes the WebSocket.
- WebSocket close closes the relay stream.
- Context cancel closes both sides.
- Invalid/expired/reused ticket returns handshake error and never opens a relay.
- Agent offline returns 502 and marks the session closed.
- Active session close API terminates the local WebSocket broker.
- Cluster close routes a local session to `CloseLocal` directly.
- Cluster close routes a remote session through `CloseWebSSHConnection`.
- Cluster close maps an unavailable owner node to 503 without closing the row.
- Relay control authentication rejects a caller-node mismatch.
- Metrics count bytes, sessions, errors, and duration.
- Panics in copy goroutines cannot terminate the Server process.

- [x] **Step 2: Run tests and verify red**

```bash
go test ./internal/server -run 'TestWebSSHBroker' -count=1
```

Expected result: broker type is undefined.

- [x] **Step 3: Implement broker**

Reuse the proven bidirectional copy and close-propagation approach from `TCPBridgeHandler`, but replace route resolution with authenticated session resolution. The relay request must be:

```go
relay.StreamRequest{
    AgentID: session.AgentID,
    Protocol: "tcp",
    TargetHost: server.Host,
    TargetPort: server.Port,
    StrictOpen: false,
}
```

Add default configuration:

```yaml
server:
  webssh:
    enabled: true
    ticket_ttl: 30s
    session_ttl: 8h
    idle_timeout: 5m
    open_timeout: 10s
    max_active_sessions_per_user: 5
    max_message_bytes: 65536
```

- [x] **Step 4: Run tests and verify green**

```bash
go test ./internal/server -run 'TestWebSSHBroker' -count=1
go test ./internal/config ./internal/server -count=1
go vet ./internal/server ./internal/config
```

---

### Task 6: Session Sweeper and Observability

**Files:**

- Create: `internal/server/webssh_sweeper.go`
- Create: `internal/server/webssh_sweeper_test.go`
- Modify: `internal/server/webssh_runtime.go`
- Modify: `internal/server/runtime.go`
- Modify: `internal/observability/metrics.go`
- Modify: `internal/observability/metrics_test.go`
- Modify: `docs/operations/observability.md`

**Interfaces:**

- Produces:

```go
const DefaultWebSSHSweepInterval = time.Minute

type WebSSHSweeper struct { /* repository and interval */ }
func NewWebSSHSweeper(sessions storage.WebSSHSessionRepository, interval time.Duration) *WebSSHSweeper
func (s *WebSSHSweeper) Run(ctx context.Context) error
```

- Produces Prometheus collectors:

```go
tunnelmesh_webssh_sessions_active
tunnelmesh_webssh_tickets_created_total
tunnelmesh_webssh_ticket_reuse_total
tunnelmesh_webssh_streams_errors_total
tunnelmesh_webssh_stream_duration_seconds
tunnelmesh_webssh_bytes_total
```

- [x] **Step 1: Write failing tests**

Cover:

- Sweeper expires pending tickets after TTL.
- Sweeper closes expired active sessions.
- Sweeper runs repeatedly until context cancel.
- Cancel returns `context.Canceled`.
- All six metric names are registered once and expose expected labels.
- Runtime Close stops the sweeper.

- [x] **Step 2: Run tests and verify red**

```bash
go test ./internal/server ./internal/observability -run 'TestWebSSH(Sweeper|Metrics)' -count=1
```

- [x] **Step 3: Implement sweeper and metrics**

Register metrics in the existing metrics registry and attach counters to the broker. Keep audit and logs metadata-only.

- [x] **Step 4: Run tests and verify green**

```bash
go test ./internal/server ./internal/observability -run 'TestWebSSH(Sweeper|Metrics)' -count=1
go test ./internal/server ./internal/observability -count=1
go vet ./internal/server ./internal/observability
```

---

### Task 7: Server-Side Public-Key Extraction and Credential Management

**Files:**

- Modify: `web/package.json`
- Modify: `web/package-lock.json`
- Modify: `web/src/api/client.ts`
- Create: `web/src/api/credentials.ts`
- Create: `web/src/api/remote-servers.ts`
- Create: `web/src/api/webssh.ts`
- Create: `web/src/tests/credentials-api.spec.ts`
- Create: `web/src/tests/remote-servers-api.spec.ts`
- Create: `web/src/tests/webssh-api.spec.ts`
- Modify: `web/src/router.ts`
- Modify: `web/src/layouts/AppShell.vue`
- Modify: `web/src/i18n/messages/zh-CN.ts`
- Modify: `web/src/i18n/messages/en-US.ts`
- Modify: `web/src/tests/routes.spec.ts`
- Modify: `web/src/tests/shell.spec.ts`
- Modify: `web/src/tests/i18n.spec.ts`
- Create: `web/src/views/Credentials.vue`
- Create: `web/src/tests/credentials-view.spec.ts`
- Create: `web/src/api/ssh-public-key.ts`
- Create: `web/src/tests/ssh-public-key-api.spec.ts`
- Modify: `internal/server/credential_service.go`
- Modify: `internal/server/credential_service_test.go`
- Modify: `internal/server/credential_api.go`
- Modify: `internal/server/credential_api_test.go`
- Modify: `docs/api/openapi.yaml`

**Interfaces:**

- Produces API types:

```ts
export type CredentialType = 'ssh_public_key'
export type ResourceStatus = 'active' | 'deleted'
export type Credential = {
  id: string
  name: string
  type: CredentialType
  publicKey: string
  fingerprint: string
  enabled: boolean
  status: ResourceStatus
  deletedAt?: string | null
  createdAt: string
  updatedAt: string
}
```

- Produces extraction API client:

```ts
export async function extractSSHPublicKey(privateKey: string, passphrase?: string): Promise<{
  publicKey: string
  fingerprint: string
}>
```

The helper calls the one-time Server extraction API, then the caller immediately clears its local private-key and passphrase refs. The Server parses the private key in memory with Go `golang.org/x/crypto/ssh`, returns the normalized public key and fingerprint, and never persists or logs the private key.

Dependency selection rule:

- Add `@xterm/xterm` and `@xterm/addon-fit`.
- Add the selected browser-compatible SSH/SFTP package only after its Vite build and browser smoke test pass; record the exact version in `web/package-lock.json`.
- If the SSH/SFTP package is not browser-compatible under Vite without unsafe Node polyfills, stop before Task 9 and report the blocker.
- Do not add `sshpk`, `ssh2`, or Node polyfills for public-key extraction; extraction is now server-side.

- [x] **Step 1: Write failing frontend tests**

Cover:

- Credential API methods use correct paths, methods, bodies, and `Idempotency-Key`.
- The extraction API sends `privateKey` and optional `passphrase` to `/api/v1/credentials/extract-ssh-public-key` and returns a no-store response.
- Remote server API methods use correct paths and payloads.
- WebSSH create/get/close methods use correct paths.
- Routes expose `/remote-servers` and `/credentials`.
- Admin shell shows both menus.
- Empty, loading, error, active, and deleted states render.
- Paste-public-key form accepts a valid key.
- Private-key extraction fills the public-key field and clears the private-key textarea.
- Extraction errors are shown while private-key and passphrase fields remain cleared.
- Both locales contain all new keys.

- [x] **Step 2: Run tests and verify red**

```bash
cd web
npm test -- --run
```

Expected result: new modules and view imports fail.

- [x] **Step 3: Implement frontend foundation**

Install the browser-compatible dependencies and configure Vite only if the chosen packages require it. Keep private-key text in component-local refs and clear it immediately after successful or failed extraction. The credentials page uses Element Plus dialogs, table pagination, and status filters.

- [x] **Step 4: Run tests and verify green**

```bash
cd web
npm test -- --run
npm run build
```

---

### Task 8: Remote Server Management UI

**Files:**

- Create: `web/src/views/RemoteServers.vue`
- Create: `web/src/tests/remote-servers-view.spec.ts`
- Modify: `web/src/i18n/messages/zh-CN.ts`
- Modify: `web/src/i18n/messages/en-US.ts`
- Modify: `web/src/tests/i18n.spec.ts`

**Interfaces:**

- Consumes APIs from Task 7.
- Produces form and view model:

```ts
export type RemoteServer = {
  id: string
  name: string
  host: string
  port: number
  defaultUsername: string
  credentialId?: string | null
  credentialName?: string
  agentId: string
  agentName?: string
  enabled: boolean
  status: 'enabled' | 'disabled' | 'deleted'
  agentOnline: boolean
  lastConnectedAt?: string | null
  lastResult?: string | null
  lastErrorClass?: string | null
  deletedAt?: string | null
  createdAt: string
  updatedAt: string
}
```

- [x] **Step 1: Write failing tests**

Cover:

- Filter by keyword, Agent, and status.
- Create/edit form validation for name, host, port, username, Agent, and enabled state.
- Credential select shows only active keys owned by the user.
- Agent select shows enabled Agents.
- Actions include edit, details, SSH, delete, and restore for deleted rows.
- SSH button opens an auth dialog and does not persist password.
- Server-side errors are shown in the table or dialog.

- [x] **Step 2: Run tests and verify red**

```bash
cd web
npm test -- --run web/src/tests/remote-servers-view.spec.ts
```

- [x] **Step 3: Implement view**

Use the existing admin view conventions. Password input uses `type="password"` and is never copied into Pinia or localStorage. On SSH submit, navigate to the WebSSH terminal route with session data held in a memory-only Pinia store.

- [x] **Step 4: Run tests and verify green**

```bash
cd web
npm test -- --run web/src/tests/remote-servers-view.spec.ts
npm test -- --run
npm run build
```

---

### Task 9: Browser Byte Stream and SSH Terminal

**Files:**

- Create: `web/src/webssh/byte-stream.ts`
- Create: `web/src/webssh/byte-stream.spec.ts`
- Create: `web/src/webssh/ssh-client.ts`
- Create: `web/src/webssh/ssh-client.spec.ts`
- Create: `web/src/stores/webssh.ts`
- Create: `web/src/tests/webssh-store.spec.ts`
- Create: `web/src/views/WebSSHTerminal.vue`
- Create: `web/src/tests/webssh-terminal.spec.ts`
- Modify: `web/src/router.ts`
- Modify: `web/src/i18n/messages/zh-CN.ts`
- Modify: `web/src/i18n/messages/en-US.ts`

**Interfaces:**

- Produces:

```ts
export interface DuplexByteStream {
  read(onChunk: (chunk: Uint8Array) => void): () => void
  write(chunk: Uint8Array): Promise<void>
  close(): void
  onClose(handler: () => void): () => void
}

export function createWebSocketByteStream(url: string): DuplexByteStream

export interface WebSSHClientOptions {
  stream: DuplexByteStream
  username: string
  password?: string
  privateKey?: string
  hostKeyVerifier: (fingerprint: string) => boolean | Promise<boolean>
}

export async function connectSSH(options: WebSSHClientOptions): Promise<SSHConnection>
export interface SSHConnection {
  openShell(): Promise<TerminalChannel>
  openSFTP(): Promise<SFTPClient>
  close(): Promise<void>
}
```

The internal `ssh-client.ts` adapter must pass a Node-compatible duplex socket to the browser SSH library. It must translate `data`, `end`, `error`, and `close` events to `DuplexByteStream` events without buffering unbounded data.

- [x] **Step 1: Write failing tests**

Cover:

- WebSocket binary `ArrayBuffer` becomes `Uint8Array`.
- `write` sends binary data and rejects before WebSocket opens.
- `close` is idempotent and emits close once.
- Backpressure rejects or waits when `bufferedAmount` exceeds 4 MiB.
- SSH client rejects missing username and absent authentication material.
- SSH client closes WebSocket on SSH error.
- Terminal route leaves and window unload close the store session.
- Resize sends `cols` and `rows`.
- Password and private key are cleared on close.

- [x] **Step 2: Run tests and verify red**

```bash
cd web
npm test -- --run web/src/webssh/byte-stream.spec.ts web/src/webssh/ssh-client.spec.ts web/src/tests/webssh-terminal.spec.ts
```

- [x] **Step 3: Implement terminal**

Use xterm with fit addon. Keep session credentials only in the memory-only Pinia store; never write them to `localStorage`. The route should show connection, connected, error, and closed states and expose a close button.

- [x] **Step 4: Run tests and verify green**

```bash
cd web
npm test -- --run web/src/webssh/byte-stream.spec.ts web/src/webssh/ssh-client.spec.ts web/src/tests/webssh-terminal.spec.ts
npm test -- --run
npm run build
```

---

### Task 10: SFTP UI

**Files:**

- Create: `web/src/webssh/sftp.ts`
- Create: `web/src/webssh/sftp.spec.ts`
- Create: `web/src/views/WebSFTP.vue`
- Create: `web/src/tests/web-sftp-view.spec.ts`
- Modify: `web/src/router.ts`
- Modify: `web/src/i18n/messages/zh-CN.ts`
- Modify: `web/src/i18n/messages/en-US.ts`

**Interfaces:**

- Produces:

```ts
export interface SFTPEntry {
  name: string
  path: string
  type: 'file' | 'directory' | 'symlink'
  size: number
  modifiedAt?: string
  permissions?: string
}

export interface SFTPProgress {
  path: string
  transferred: number
  total: number
}

export async function listDirectory(client: SFTPClient, path: string): Promise<SFTPEntry[]>
export async function uploadFile(client: SFTPClient, file: File, remotePath: string, onProgress?: (event: SFTPProgress) => void): Promise<void>
export async function downloadFile(client: SFTPClient, remotePath: string, size: number, onProgress?: (event: SFTPProgress) => void): Promise<Blob>
export async function deleteEntry(client: SFTPClient, entry: SFTPEntry): Promise<void>
export async function renameEntry(client: SFTPClient, from: SFTPEntry, toPath: string): Promise<void>
```

- [x] **Step 1: Write failing tests**

Cover:

- Directory listing maps names, types, sizes, and timestamps.
- Upload reads in chunks and reports progress.
- Download reads in chunks and reports progress.
- Delete/rename require confirmation.
- Permission and not-found errors show stable UI messages.
- One-gigabyte default limit rejects larger files before transfer.
- Closing the SSH connection stops transfers and resets progress.

- [x] **Step 2: Run tests and verify red**

```bash
cd web
npm test -- --run web/src/webssh/sftp.spec.ts web/src/tests/web-sftp-view.spec.ts
```

- [x] **Step 3: Implement SFTP**

Reuse the same authenticated SSH connection from the terminal store. If the browser SSH library does not support multiple channels on one socket, open a second one-time WebSSH session and document that behavior in the UI. All operations go through SFTP; no Server-side file API is introduced.

- [x] **Step 4: Run tests and verify green**

```bash
cd web
npm test -- --run web/src/webssh/sftp.spec.ts web/src/tests/web-sftp-view.spec.ts
npm test -- --run
npm run build
```

---

### Task 11: Runtime Wiring, Embedded Assets, and E2E Smoke

**Files:**

- Modify: `internal/server/runtime.go`
- Modify: `internal/server/runtime_test.go`
- Modify: `internal/cli/root.go`
- Modify: `Dockerfile`
- Create: `internal/server/webssh_e2e_test.go`
- Build and synchronize: `web/dist/*`
- Build and synchronize: `internal/server/web_dist/*`

**Interfaces:**

- Produces runtime configuration wiring and a repeatable E2E smoke test:

```go
func TestWebSSHBrowserBrokerE2E(t *testing.T)
```

The test uses a local TCP SSH banner server plus an in-memory Agent relay. It verifies:

1. Session API returns a one-time ticket.
2. WebSocket handshake consumes it.
3. Browser-side bytes reach the TCP server.
4. TCP bytes return through WebSocket.
5. Close terminates both streams.

- [x] **Step 1: Write failing E2E test**

Start a local TCP listener that expects `SSH-2.0-test` bytes and returns `SSH-2.0-test\n`. Use a fake relay opener first to prove byte flow, then add an integration test with the real Agent relay if the existing test harness supports it.

- [x] **Step 2: Run and verify red**

The full byte-flow E2E passed immediately because earlier tasks had already
wired the production handler and broker. The runtime-shutdown requirement was
still untested; `TestServerRuntimeCloseTerminatesWebSSHBridges` failed first
with "runtime close did not terminate the Agent stream", then passed after
`WebSSHBroker.CloseAll` was wired into `ServerRuntime.Close`.

```bash
go test ./internal/server -run TestWebSSHBrowserBrokerE2E -count=1
```

- [x] **Step 3: Implement wiring**

Wire broker, sweeper, metrics, API, WebSocket route, runtime close, and embedded production assets. Keep `/api/`, `/health/`, and all `/ws/` routes before SPA fallback.

- [x] **Step 4: Run E2E and builds**

```bash
go test ./internal/server -run 'TestWebSSH' -count=1
go test ./... -count=1
cd web
npm test -- --run
npm run build
cd ..
rm -rf internal/server/web_dist
mkdir -p internal/server/web_dist
cp -a web/dist/. internal/server/web_dist/
./scripts/verify-web-embed.sh
```

`web/dist/` and `internal/server/web_dist/` are generated and ignored by Git;
do not attempt to stage them manually. The verification script proves the
Server binary will embed exactly the bundle produced by this change.

---

### Task 12: Documentation and PR Package

**Files:**

- Modify: `docs/README.md`
- Modify: `docs/api/openapi.yaml`
- Modify: `docs/user-guide/server-admin.md`
- Modify: `docs/operations/schema-upgrades.md`
- Modify: `docs/operations/configuration.md`
- Modify: `docs/deployment/nginx.md`
- Create: `docs/pull-requests/2026-09-12-admin-webssh-sftp.md`

**Interfaces:**

- Consumes final behavior from Tasks 1–11.
- Produces:
  - Admin guide for remote servers, keys, SSH, and SFTP.
  - Security warning that private keys and passwords never leave the browser.
  - Nginx `/ws/webssh/` proxy configuration.
  - v11 → v12 schema guide.
  - Complete OpenAPI models and operations.
  - PR description with test evidence.

- [x] **Step 1: Write documentation**

Document:

- Menu structure.
- Server and key fields.
- Password-per-session behavior.
- Browser-only private-key extraction.
- Host-key confirmation.
- SFTP permissions and transfer limits.
- Ticket expiry and session limits.
- Nginx timeout and buffering settings.
- Schema upgrade, rollback, and verification SQL.

- [x] **Step 2: Validate docs**

```bash
rg -n "TB[D]|TO[D]O|后续补[充]" docs/superpowers/specs/2026-09-12-admin-webssh-sftp-design.md docs/superpowers/plans/2026-09-12-admin-webssh-sftp-implementation.md docs/pull-requests/2026-09-12-admin-webssh-sftp.md
```

Expected result: no matches.

- [x] **Step 3: Full verification**

Run all mandatory checks:

```bash
go test ./... -count=1
go test -race ./...
go vet ./...
go build ./cmd/...
git diff --check
cd web
npm test -- --run
npm run build
cd ..
rm -rf internal/server/web_dist
mkdir -p internal/server/web_dist
cp -a web/dist/. internal/server/web_dist/
./scripts/verify-web-embed.sh
```

---

## Rollback

- The schema change is expand-only. Roll back the application while retaining the v12 tables.
- Do not manually decrement `schema_meta.version`.
- If exact schema restoration is mandatory, restore the pre-upgrade database backup.
- Disable `server.webssh.enabled` to turn off the WebSocket broker without removing management tables.
- Browser authentication material is never persisted, so no secret cleanup is required after rollback.

## Plan Completion Check

Before claiming completion, confirm every task checkbox is checked, full verification has passed, OpenAPI and docs are synchronized, embedded assets are current, and the user has explicitly authorized any commit operation.
