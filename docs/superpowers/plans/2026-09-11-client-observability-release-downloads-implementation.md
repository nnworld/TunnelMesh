# Client Observability and Release Downloads Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build admin-visible Client WebSocket and metadata observability plus reproducible GitHub Release downloads for six supported platforms.

**Architecture:** The client reports a stable instance identity and bounded metadata over a negotiated WebSocket subprotocol. Server nodes persist client instances and physical-connection leases in Schema v11, aggregate them through the existing database and relay boundary, and expose owner-scoped management APIs. Release tooling injects build metadata, builds embedded web assets and six platform archives, and publishes immutable GitHub Release assets with checksums and a manifest.

**Tech Stack:** Go 1.23, Cobra, SQLite, MySQL 5.6-compatible SQL, Vue 3, TypeScript, Pinia, Vue Router, Element Plus, Vite, GitHub Actions.

**Spec:** `docs/superpowers/specs/2026-09-11-client-observability-release-design.md`

## Global Constraints

- Work directly in `/opt/app/workspace/TunnelMesh`; do not create or use another worktree.
- Client metadata payload is at most 32 KiB and each field is at most 4 KiB.
- Metadata names match `^[A-Za-z0-9_.-]+$` and reject names containing `password`, `token`, `secret`, `private_key`, or `dsn`.
- Metadata never participates in authorization.
- Token plaintext, token hash, password, remote-validation URL, private key, certificate content, DSN, and full Authorization header must never be stored, logged, or rendered.
- Schema changes must update `migrations/ddl.sql`, add `migrations/incremental/v0010_to_v0011/mysql.sql` and `sqlite.sql`, update `migrations/embed.go`, raise `SchemaVersion` to 11, and update migration tests.
- SQL must remain compatible with MySQL 5.6 and SQLite: no JSON column type, CTE, or functional index.
- Build all binaries with `CGO_ENABLED=0`.
- Release tags use full SemVer `vMAJOR.MINOR.PATCH`; do not create mutable major-version tags.
- Owner filtering happens in repository queries, before pagination.
- Connection close uses `connection_epoch` fencing.
- Before claiming completion, run `go test ./... -count=1`, `go test -race ./...`, `go vet ./...`, `git diff --check`, `cd web && npm test -- --run`, `cd web && npm run build`, and `./scripts/verify-web-embed.sh`.
- Do not commit, push, merge, or create a remote PR without explicit user authorization.

---

### Task 1: Build Metadata Contract

**Files:**

- Modify: `internal/build/build.go`
- Modify: `internal/build/build_test.go`
- Modify: `internal/cli/root.go`
- Modify: `internal/cli/root_test.go`

**Interfaces:**

- Produces: `build.Version string`, `build.Commit string`, `build.BuildTime string`, `build.Info struct`, `build.Current() Info`, and `build.String() string`.
- Produces: all three Cobra roots use `build.String()` for `--version`.

- [ ] **Step 1: Write failing build metadata tests**

Append to `internal/build/build_test.go`:

```go
func TestCurrentReturnsLinkerMetadata(t *testing.T) {
    originalVersion, originalCommit, originalBuildTime := build.Version, build.Commit, build.BuildTime
    t.Cleanup(func() {
        build.Version, build.Commit, build.BuildTime = originalVersion, originalCommit, originalBuildTime
    })
    build.Version = "v1.2.3"
    build.Commit = "0123456789abcdef"
    build.BuildTime = "2026-09-11T10:00:00Z"

    want := build.Info{Version: "v1.2.3", Commit: "0123456789abcdef", BuildTime: "2026-09-11T10:00:00Z"}
    if got := build.Current(); got != want {
        t.Fatalf("Current() = %#v, want %#v", got, want)
    }
}

func TestStringIsStableAndSingleLine(t *testing.T) {
    originalVersion, originalCommit, originalBuildTime := build.Version, build.Commit, build.BuildTime
    t.Cleanup(func() {
        build.Version, build.Commit, build.BuildTime = originalVersion, originalCommit, originalBuildTime
    })
    build.Version = "v1.2.3"
    build.Commit = "0123456789abcdef"
    build.BuildTime = "2026-09-11T10:00:00Z"

    if got, want := build.String(), "v1.2.3 commit=0123456789abcdef built=2026-09-11T10:00:00Z"; got != want {
        t.Fatalf("String() = %q, want %q", got, want)
    }
}
```

- [ ] **Step 2: Run the focused build test and record RED**

Run:

```bash
go test ./internal/build -run 'TestCurrentReturnsLinkerMetadata|TestStringIsStableAndSingleLine' -count=1
```

Expected: compilation failure with `undefined: build.Version`, `undefined: build.Info`, or equivalent.

- [ ] **Step 3: Implement the metadata contract**

In `internal/build/build.go`, retain `BinaryNames()` and add:

```go
var (
    Version   = "dev"
    Commit    = "unknown"
    BuildTime = "unknown"
)

type Info struct {
    Version   string
    Commit    string
    BuildTime string
}

func Current() Info {
    return Info{Version: Version, Commit: Commit, BuildTime: BuildTime}
}

func String() string {
    info := Current()
    return info.Version + " commit=" + info.Commit + " built=" + info.BuildTime
}
```

- [ ] **Step 4: Write failing CLI version tests**

Append to `internal/cli/root_test.go`:

```go
func TestRootsExposeBuildVersion(t *testing.T) {
    roots := map[string]func() *cobra.Command{
        "tunnelmesh-server": cli.NewServerRoot,
        "tunnelmesh-agent":  cli.NewAgentRoot,
        "tunnelmesh-client": cli.NewClientRoot,
    }
    for name, factory := range roots {
        t.Run(name, func(t *testing.T) {
            root := factory()
            var output bytes.Buffer
            root.SetOut(&output)
            root.SetErr(&output)
            root.SetArgs([]string{"--version"})
            if err := root.ExecuteContext(context.Background()); err != nil {
                t.Fatal(err)
            }
            for _, expected := range []string{name, "commit=", "built="} {
                if !strings.Contains(output.String(), expected) {
                    t.Fatalf("--version output %q does not contain %q", output.String(), expected)
                }
            }
        })
    }
}
```

Add the required `bytes`, `context`, `strings`, `testing`, `github.com/spf13/cobra`, and `github.com/tunnelmesh/tunnelmesh/internal/cli` imports to the test package.

- [ ] **Step 5: Run the CLI test and record RED**

Run:

```bash
go test ./internal/cli -run TestRootsExposeBuildVersion -count=1
```

Expected: FAIL because roots do not print build metadata.

- [ ] **Step 6: Attach version output to every root**

In `newRoot`, import `internal/build`, set:

```go
Version: build.String(),
```

and immediately after command construction:

```go
root.SetVersionTemplate("{{.Name}} {{.Version}}\n")
```

- [ ] **Step 7: Run focused tests**

Run:

```bash
go test ./internal/build ./internal/cli -run 'TestCurrentReturnsLinkerMetadata|TestStringIsStableAndSingleLine|TestRootsExposeBuildVersion' -count=1
```

Expected: PASS.

---

### Task 2: Client Instance Identity

**Files:**

- Modify: `internal/config/config.go`
- Modify: `internal/config/config_test.go`
- Create: `internal/client/identity.go`
- Create: `internal/client/identity_test.go`

**Interfaces:**

- Produces: `config.ClientConfig.InstanceID string`.
- Produces: `client.EnsureClientInstanceID(path string) (string, error)`.
- Produces: `client.DefaultClientInstanceIDPath() string`.

- [ ] **Step 1: Write failing identity tests**

Create `internal/client/identity_test.go`:

```go
package client_test

import (
    "context"
    "path/filepath"
    "strings"
    "testing"

    "github.com/tunnelmesh/tunnelmesh/internal/client"
    "github.com/tunnelmesh/tunnelmesh/internal/config"
)

func TestEnsureClientInstanceIDIsStable(t *testing.T) {
    path := filepath.Join(t.TempDir(), "client-instance-id")
    first, err := client.EnsureClientInstanceID(path)
    if err != nil {
        t.Fatal(err)
    }
    second, err := client.EnsureClientInstanceID(path)
    if err != nil {
        t.Fatal(err)
    }
    if first != second {
        t.Fatalf("instance identity changed: %q -> %q", first, second)
    }
    if !strings.HasPrefix(first, "client-") || strings.ToLower(first) != first {
        t.Fatalf("instance identity must be lowercase and client-prefixed: %q", first)
    }
}

func TestClientConfigAcceptsExplicitInstanceID(t *testing.T) {
    path := filepath.Join(t.TempDir(), "client.yaml")
    contents := "mode: local\nclient:\n  server_url: wss://server.example/ws/client\n  instance_id: client-explicit-001\n"
    if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
        t.Fatal(err)
    }
    cfg, err := config.Load(context.Background(), config.ConfigOptions{ConfigFile: path})
    if err != nil {
        t.Fatal(err)
    }
    if cfg.Client.InstanceID != "client-explicit-001" {
        t.Fatalf("explicit instance ID = %q", cfg.Client.InstanceID)
    }
}
```

Add the missing `os` import to the test.

- [ ] **Step 2: Run identity tests and record RED**

Run:

```bash
go test ./internal/client -run 'TestEnsureClientInstanceIDIsStable|TestClientConfigAcceptsExplicitInstanceID' -count=1
```

Expected: compilation failure for `client.EnsureClientInstanceID` and `cfg.Client.InstanceID`.

- [ ] **Step 3: Implement the identity contract**

Add `InstanceID` to `ClientConfig`:

```go
InstanceID string `mapstructure:"instance_id" json:"instance_id" yaml:"instance_id"`
```

Create `internal/client/identity.go` with:

```go
package client

import (
    "crypto/rand"
    "encoding/hex"
    "errors"
    "fmt"
    "os"
    "path/filepath"
    "strings"
)

func DefaultClientInstanceIDPath() string {
    if runtime.GOOS == "windows" {
        return filepath.Join(os.Getenv("ProgramData"), "TunnelMesh", "client-instance-id")
    }
    return "/var/lib/tunnelmesh-client/client-instance-id"
}

func EnsureClientInstanceID(path string) (string, error) {
    path = strings.TrimSpace(path)
    if path == "" {
        return "", errors.New("client instance identity path is required")
    }
    if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
        return "", err
    }
    if data, err := os.ReadFile(path); err == nil {
        if id := strings.TrimSpace(string(data)); id != "" {
            return id, nil
        }
    } else if !errors.Is(err, os.ErrNotExist) {
        return "", err
    }
    var entropy [16]byte
    if _, err := rand.Read(entropy[:]); err != nil {
        return "", fmt.Errorf("generate client instance identity: %w", err)
    }
    id := "client-" + hex.EncodeToString(entropy[:])
    file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
    if err != nil {
        return "", err
    }
    if _, err := file.WriteString(id + "\n"); err != nil {
        _ = file.Close()
        return "", err
    }
    return id, file.Close()
}
```

Add the `runtime` import and bind `client.instance_id` in `bindEnvironment`.

- [ ] **Step 4: Run identity tests**

Run:

```bash
go test ./internal/client ./internal/config -run 'TestEnsureClientInstanceIDIsStable|TestClientConfigAcceptsExplicitInstanceID' -count=1
```

Expected: PASS.

---

### Task 3: Client Metadata Protocol

**Files:**

- Modify: `internal/protocol/frame.go`
- Modify: `internal/protocol/subprotocol.go`
- Modify: `internal/protocol/protocol_test.go`
- Modify: `internal/protocol/subprotocol_test.go`

**Interfaces:**

- Produces: `SubprotocolClientMetadata`, updated `ClientSubprotocols()`, and updated `SelectSubprotocol`.
- Produces: `FrameClientHello`, `FrameClientMetadataUpdate`, `FrameClientMetadataAck`.
- Produces: `ClientMetadataPayload`, `ClientMetadataItem`, `ClientListener`, `ClientMetadataError`, `ClientMetadataAckPayload`.
- Produces: `EncodeClientMetadataPayload`, `DecodeClientMetadataPayload`, `EncodeClientMetadataAckPayload`, and `DecodeClientMetadataAckPayload`.

- [ ] **Step 1: Write failing protocol tests**

Append to `internal/protocol/protocol_test.go`:

```go
func TestClientMetadataPayloadRoundTrip(t *testing.T) {
    payload := ClientMetadataPayload{
        InstanceID: "client-0123456789abcdef0123456789abcdef",
        ConnectionSlot: 2,
        AgentIDs: []string{"agent-0123456789abcdef"},
        Version: "v1.2.3",
        Commit: "0123456789abcdef",
        Platform: "darwin/arm64",
        Hostname: "workstation",
        ReportedAt: time.Date(2026, 9, 11, 10, 0, 0, 0, time.UTC),
        Revision: 1,
        Listeners: []ClientListener{{
            Protocol: "socks5", ListenAddress: "127.0.0.1:10866", AgentID: "agent-0123456789abcdef", Enabled: true,
        }},
        Items: []ClientMetadataItem{{Name: "region", Source: "env", Value: "cn-north"}},
        Capabilities: []string{"client_metadata.v1"},
    }
    encoded, err := EncodeClientMetadataPayload(payload)
    if err != nil {
        t.Fatal(err)
    }
    decoded, err := DecodeClientMetadataPayload(encoded)
    if err != nil {
        t.Fatal(err)
    }
    if decoded.InstanceID != payload.InstanceID || decoded.Listeners[0].ListenAddress != payload.Listeners[0].ListenAddress {
        t.Fatalf("decoded payload mismatch: %#v", decoded)
    }
}
```

Append to `internal/protocol/subprotocol_test.go`:

```go
func TestClientSubprotocolsPreferMetadata(t *testing.T) {
    selected, ok := SelectSubprotocol(ClientSubprotocols())
    if !ok || selected != SubprotocolClientMetadata {
        t.Fatalf("selected = %q, ok=%v", selected, ok)
    }
}
```

- [ ] **Step 2: Run protocol tests and record RED**

Run:

```bash
go test ./internal/protocol -run 'TestClientMetadataPayloadRoundTrip|TestClientSubprotocolsPreferMetadata' -count=1
```

Expected: compilation failure for the new subprotocol, frames, and payload types.

- [ ] **Step 3: Implement protocol frames**

In `frame.go`, append the new frame constants after `FrameOpenResult`:

```go
FrameClientHello
FrameClientMetadataUpdate
FrameClientMetadataAck
```

Update `knownFrameType`, `isMetadataFrame`, and payload validation so:

- known frame types include the new constants.
- metadata payload size is capped by `MaxMetadataPayload`.
- `CLIENT_HELLO`, `CLIENT_METADATA_UPDATE`, and `CLIENT_METADATA_ACK` may use `StreamID=0`.

Define:

```go
type ClientMetadataItem struct {
    Name     string `json:"name"`
    Source   string `json:"source"`
    Value    string `json:"value,omitempty"`
    Redacted bool   `json:"redacted,omitempty"`
}

type ClientMetadataError struct {
    Name    string `json:"name"`
    Code    string `json:"code"`
    Message string `json:"message"`
}

type ClientListener struct {
    Protocol      string `json:"protocol"`
    ListenAddress string `json:"listenAddress"`
    AgentID       string `json:"agentId"`
    Enabled       bool   `json:"enabled"`
}

type ClientMetadataPayload struct {
    InstanceID     string                `json:"instance_id"`
    ConnectionSlot int                   `json:"connection_slot,omitempty"`
    AgentIDs       []string              `json:"agent_ids,omitempty"`
    Version        string                `json:"version,omitempty"`
    Commit         string                `json:"commit,omitempty"`
    Platform       string                `json:"platform,omitempty"`
    Hostname       string                `json:"hostname,omitempty"`
    ProcessStartAt time.Time             `json:"process_start_at,omitempty"`
    ReportedAt     time.Time             `json:"reported_at"`
    Revision       uint64                `json:"revision"`
    Listeners      []ClientListener      `json:"listeners,omitempty"`
    Items          []ClientMetadataItem  `json:"items,omitempty"`
    Capabilities   []string              `json:"capabilities,omitempty"`
    Errors         []ClientMetadataError `json:"errors,omitempty"`
}

type ClientMetadataAckPayload struct {
    ClientInstanceID string                `json:"client_instance_id"`
    InstanceID       string                `json:"instance_id"`
    ConnectionID     string                `json:"connection_id"`
    Revision         uint64                `json:"revision"`
    Accepted         bool                  `json:"accepted"`
    Idempotent       bool                  `json:"idempotent,omitempty"`
    Errors           []ClientMetadataError `json:"errors,omitempty"`
}
```

Implement the four encode/decode functions with JSON marshal and unmarshal, and return `ErrPayloadTooLarge` when encoded bytes exceed `MaxMetadataPayload`.

In `subprotocol.go`, add:

```go
SubprotocolClientMetadata = "tunnelmesh.v1.open-result.flow-control.metadata"
```

Put it first in `ClientSubprotocols()` and first in the `SelectSubprotocol` preference list.

- [ ] **Step 4: Run protocol tests**

Run:

```bash
go test ./internal/protocol -count=1
```

Expected: PASS.

---

### Task 4: Client Metadata Collector and Reporting

**Files:**

- Create: `internal/client/metadata.go`
- Create: `internal/client/metadata_test.go`
- Modify: `internal/client/session_pool.go`
- Modify: `internal/client/session_pool_test.go`
- Modify: `internal/client/session.go`
- Modify: `internal/client/websocket.go`

**Interfaces:**

- Consumes: `protocol.ClientMetadataPayload`, `protocol.FrameClientHello`, and `protocol.FrameClientMetadataUpdate`.
- Produces: `client.SessionOpenMetadata` and `sessionOpenModeForTransport` support for `protocol.SubprotocolClientMetadata`.
- Produces: `ClientMetadataOptions`, `NewClientMetadataCollector`, and `(*ClientMetadataCollector).Snapshot(context.Context)`.
- Produces: `WebSocketRunOptions.Metadata *ClientMetadataOptions` and `WebSocketRunOptions.ConnectionSlot int`.
- Produces: session sends metadata only when selected subprotocol is `SubprotocolClientMetadata`.

- [ ] **Step 1: Write failing metadata collector tests**

Create `internal/client/metadata_test.go`:

```go
package client_test

import (
    "context"
    "testing"

    "github.com/tunnelmesh/tunnelmesh/internal/client"
    "github.com/tunnelmesh/tunnelmesh/internal/protocol"
)

func TestClientMetadataCollectorBuildsSafeSnapshot(t *testing.T) {
    collector, err := client.NewClientMetadataCollector(client.ClientMetadataOptions{
        InstanceID: "client-0123456789abcdef0123456789abcdef",
        AgentIDs: []string{"agent-0123456789abcdef"},
        Version:   "v1.2.3",
        Commit:    "0123456789abcdef",
        Listeners: []protocol.ClientListener{{
            Protocol: "socks5", ListenAddress: "127.0.0.1:10866", AgentID: "agent-0123456789abcdef", Enabled: true,
        }},
        Metadata: []client.MetadataField{{Name: "region", Source: "static", Value: "cn-north"}},
    })
    if err != nil {
        t.Fatal(err)
    }
    snapshot := collector.Snapshot(context.Background())
    if snapshot.InstanceID != "client-0123456789abcdef0123456789abcdef" || snapshot.Version != "v1.2.3" {
        t.Fatalf("snapshot = %#v", snapshot)
    }
    if len(snapshot.Listeners) != 1 || snapshot.Listeners[0].Protocol != "socks5" {
        t.Fatalf("listeners = %#v", snapshot.Listeners)
    }
}

func TestClientMetadataCollectorRejectsSensitiveName(t *testing.T) {
    _, err := client.NewClientMetadataCollector(client.ClientMetadataOptions{
        InstanceID: "client-0123456789abcdef0123456789abcdef",
        Metadata:   []client.MetadataField{{Name: "api_token", Source: "static", Value: "must-not-be-sent"}},
    })
    if err == nil {
        t.Fatal("expected sensitive metadata name to be rejected")
    }
}
```

- [ ] **Step 2: Run collector tests and record RED**

Run:

```bash
go test ./internal/client -run 'TestClientMetadataCollector' -count=1
```

Expected: compilation failure for `client.NewClientMetadataCollector` and `client.ClientMetadataOptions`.

- [ ] **Step 3: Implement the collector**

Define:

```go
type MetadataField struct {
    Name   string
    Source string
    Value  string
}

type ClientMetadataOptions struct {
    InstanceID string
    AgentIDs  []string
    ConnectionSlot int
    Version   string
    Commit    string
    Listeners []protocol.ClientListener
    Metadata  []MetadataField
}
```

`SessionPoolManagerOptions` gains a shared `Metadata ClientMetadataOptions` field. When the default runner starts slot `N`, it deep-copies the metadata options, sets `ConnectionSlot` to `N+1`, and passes them through `WebSocketRunOptions`. This keeps one stable client instance identity across every Agent pool while making each physical WS observable by its slot.

Extend `SessionOpenMode` and mode predicates:

```go
const (
    SessionOpenLegacy SessionOpenMode = iota
    SessionOpenStrict
    SessionOpenFlowControl
    SessionOpenMetadata
)

func (mode SessionOpenMode) supportsOpenResult() bool {
    return mode == SessionOpenStrict || mode == SessionOpenMetadata
}

func (mode SessionOpenMode) supportsFlowControl() bool {
    return mode == SessionOpenFlowControl || mode == SessionOpenMetadata
}

func (mode SessionOpenMode) supportsClientMetadata() bool {
    return mode == SessionOpenMetadata
}
```

Update `sessionOpenModeForTransport` so `protocol.SubprotocolClientMetadata` maps to `SessionOpenMetadata`, and replace direct equality checks against `SessionOpenFlowControl` in `Session` with `supportsFlowControl()` so metadata mode retains flow control.

Implement:

- fixed platform from `runtime.GOOS` and `runtime.GOARCH`
- hostname from `os.Hostname`, with errors converted to an item error
- process start time captured once
- revision incremented only when a snapshot changes
- listener and metadata slices copied defensively
- validation of field count, names, sensitive names, UTF-8, and length

- [ ] **Step 4: Attach reporting to the client session**

In `Session`, add a metadata collector field and an internal send method. Add `NewSessionWithOpenModeAndMetadata(tr FrameTransport, mode SessionOpenMode, collector *ClientMetadataCollector) *Session`. When the mode supports metadata, synchronously send `FrameClientHello` before `Start()` returns, then run a bounded reporter goroutine that sends `FrameClientMetadataUpdate` only when `Snapshot` changes; use a 30-second default interval and stop it with the session. Never send these frames under legacy, open-result, or flow-control-only subprotocols.

Add a session unit test that asserts:

```go
legacy := &receiveTransport{sent: make(chan protocol.Frame, 1), recv: make(chan protocol.Frame, 1), done: make(chan struct{})}
if got := sessionOpenModeForTransport(legacy); got != SessionOpenLegacy {
    t.Fatalf("legacy OpenMode()=%v, want legacy", got)
}
```

Add a metadata transport type that embeds `*receiveTransport` and implements:

```go
func (t *metadataReceiveTransport) Subprotocol() string {
    return protocol.SubprotocolClientMetadata
}
```

Then assert `sessionOpenModeForTransport` returns `SessionOpenMetadata`, and add a session test that receives `FrameClientHello` before any `FrameOpenStream`.

- [ ] **Step 5: Run client tests**

Run:

```bash
go test ./internal/client -count=1
```

Expected: PASS.

---

### Task 5: Schema v11 and Client Repositories

**Files:**

- Modify: `migrations/ddl.sql`
- Create: `migrations/incremental/v0010_to_v0011/mysql.sql`
- Create: `migrations/incremental/v0010_to_v0011/sqlite.sql`
- Modify: `migrations/embed.go`
- Modify: `internal/storage/db.go`
- Modify: `internal/storage/models.go`
- Modify: `internal/storage/repository.go`
- Create: `internal/storage/client_repository.go`
- Create: `internal/storage/client_repository_test.go`
- Modify: `internal/storage/storage_contract_test.go`

**Interfaces:**

- Produces: `storage.ClientInstance`, `storage.ClientConnectionLease`, `storage.ClientInstanceFilter`, and `storage.ClientConnectionFilter`.
- Produces: repository list results using the existing generic `storage.Page[T]` contract.
- Produces: `storage.ClientInstanceRepository` and `storage.ClientConnectionRepository`.
- Produces: `SchemaVersion = 11`.

- [ ] **Step 1: Write failing repository tests**

Create `internal/storage/client_repository_test.go` with contract tests that use both SQLite and MySQL-compatible SQL repositories:

```go
package storage

import (
    "context"
    "testing"
    "time"
)

func TestClientInstanceRepositoryUpsertIsIdempotent(t *testing.T) {
    db := newTestDB(t)
    ctx := context.Background()
    now := time.Date(2026, 9, 11, 10, 0, 0, 0, time.UTC)
    expires := now.Add(time.Minute)
    instance := ClientInstance{
        ID: "client-instance-1", OwnerUserID: "owner-1", InstanceID: "client-0123456789abcdef",
        Metadata: `{"version":"v1.2.3"}`, Capabilities: `["client_metadata.v1"]`,
        ReportedAt: now, LastSeenAt: now, ExpiresAt: &expires, UpdatedAt: now,
    }
    persisted, err := db.ClientInstances().Upsert(ctx, instance)
    if err != nil {
        t.Fatal(err)
    }
    if persisted.ID != instance.ID {
        t.Fatalf("persisted ID = %q, want %q", persisted.ID, instance.ID)
    }
    instance.Metadata = `{"version":"v1.2.4"}`
    if _, err := db.ClientInstances().Upsert(ctx, instance); err != nil {
        t.Fatal(err)
    }
    got, err := db.ClientInstances().GetByOwnerAndInstance(ctx, "owner-1", "client-0123456789abcdef")
    if err != nil {
        t.Fatal(err)
    }
    if got.ID != "client-instance-1" || got.Metadata != `{"version":"v1.2.4"}` {
        t.Fatalf("upsert result = %#v", got)
    }
}

func TestClientConnectionRepositoryFiltersOwnerBeforePagination(t *testing.T) {
    db := newTestDB(t)
    ctx := context.Background()
    createClientConnection(t, db, "connection-1", "owner-1", "client-instance-1")
    createClientConnection(t, db, "connection-2", "owner-2", "client-instance-2")
    page, err := db.ClientConnections().List(ctx, ClientConnectionFilter{OwnerUserID: "owner-1"}, "", 10)
    if err != nil {
        t.Fatal(err)
    }
    if len(page.Items) != 1 || page.Items[0].OwnerUserID != "owner-1" {
        t.Fatalf("owner page = %#v", page.Items)
    }
}

func TestClientInstanceRepositoryMarksExpiredMetadata(t *testing.T) {
    db := newTestDB(t)
    now := time.Date(2026, 9, 11, 10, 0, 0, 0, time.UTC)
    createClientInstance(t, db, "client-instance-1", "owner-1", "client-local-1", now.Add(-time.Minute))
    changed, err := db.ClientInstances().MarkExpired(context.Background(), now)
    if err != nil {
        t.Fatal(err)
    }
    if changed != 1 {
        t.Fatalf("changed = %d, want 1", changed)
    }
    got, err := db.ClientInstances().Get(context.Background(), "client-instance-1")
    if err != nil {
        t.Fatal(err)
    }
    if !got.Stale {
        t.Fatal("expired metadata was not marked stale")
    }
}

func createClientConnection(t *testing.T, db *DB, connectionID, ownerUserID, clientInstanceID string) {
    t.Helper()
    now := time.Date(2026, 9, 11, 10, 0, 0, 0, time.UTC)
    createClientInstance(t, db, clientInstanceID, ownerUserID, "instance-"+clientInstanceID, now)
    lease := ClientConnectionLease{
        ConnectionID: connectionID, ClientInstanceID: clientInstanceID, TokenID: "token-"+connectionID,
        OwnerUserID: ownerUserID, ServerNodeID: "server-1", ConnectionEpoch: 1,
        AcquiredAt: now, ExpiresAt: now.Add(time.Minute), UpdatedAt: now,
    }
    if _, err := db.ClientConnections().Register(context.Background(), lease); err != nil {
        t.Fatal(err)
    }
}

func createClientInstance(t *testing.T, db *DB, id, ownerUserID, instanceID string, now time.Time) {
    t.Helper()
    expires := now.Add(time.Minute)
    instance := ClientInstance{
        ID: id, OwnerUserID: ownerUserID, InstanceID: instanceID, Metadata: `{}`,
        Capabilities: `["client_metadata.v1"]`, ReportedAt: now, LastSeenAt: now,
        ExpiresAt: &expires, UpdatedAt: now,
    }
    if err := db.ClientInstances().Upsert(context.Background(), instance); err != nil {
        t.Fatal(err)
    }
}
```

Add a schema test:

```go
func TestSchemaVersionIs11(t *testing.T) {
    if SchemaVersion != 11 {
        t.Fatalf("SchemaVersion = %d, want 11", SchemaVersion)
    }
}
```

- [ ] **Step 2: Run storage tests and record RED**

Run:

```bash
go test ./internal/storage -run 'TestClientInstanceRepository|TestClientConnectionRepository|TestSchemaVersionIs11' -count=1
```

Expected: compilation failure for the client repository fields and methods; after those compile, `SchemaVersion` remains 10 and the schema test fails.

- [ ] **Step 3: Add Schema v11**

Add both tables from the design to `migrations/ddl.sql`. Create identical logical DDL in the MySQL and SQLite incremental scripts. MySQL uses:

```sql
CREATE TABLE client_instance_metadata (
    id VARBINARY(255) PRIMARY KEY,
    owner_user_id VARBINARY(255) NOT NULL,
    instance_id VARBINARY(128) NOT NULL,
    metadata TEXT NOT NULL,
    capabilities TEXT NOT NULL,
    reported_at TEXT NOT NULL,
    last_seen_at TEXT NOT NULL,
    expires_at TEXT,
    stale INTEGER NOT NULL DEFAULT 0,
    updated_at VARCHAR(32) NOT NULL,
    UNIQUE(owner_user_id, instance_id)
);
```

SQLite uses the same column types that the existing repository abstraction uses. Register `v0010_to_v0011/mysql.sql` and `sqlite.sql` in `migrations/embed.go`, set `SchemaVersion = 11`, and add the adjacent migration branch in `internal/storage/db.go`.

- [ ] **Step 4: Implement models and repositories**

Define storage models:

```go
type ClientInstanceFilter struct {
    OwnerUserID   string
    TokenID       string
    ServerNodeID  string
    Status        string
    AgentID       string
    Keyword       string
}

type ClientConnectionFilter struct {
    ClientInstanceID string
    OwnerUserID       string
    TokenID          string
    ServerNodeID      string
    IncludeExpired    bool
}

type ClientInstance struct {
    ID           string
    OwnerUserID  string
    InstanceID   string
    Metadata     string
    Capabilities string
    ReportedAt   time.Time
    LastSeenAt   time.Time
    ExpiresAt    *time.Time
    Stale        bool
    UpdatedAt    time.Time
}

type ClientConnectionLease struct {
    ConnectionID     string
    ClientInstanceID string
    TokenID          string
    OwnerUserID      string
    ServerNodeID     string
    ConnectionEpoch  int64
    ActiveStreams    int64
    HealthScore      int64
    AcquiredAt       time.Time
    ExpiresAt        time.Time
    UpdatedAt        time.Time
}
```

Implement repository methods:

```go
Upsert(context.Context, ClientInstance) (ClientInstance, error)
GetByOwnerAndInstance(context.Context, ownerUserID, instanceID string) (ClientInstance, error)
Get(context.Context, id string) (ClientInstance, error)
List(context.Context, ClientInstanceFilter, cursor string, limit int) (Page[ClientInstance], error)
TouchInstance(context.Context, ownerUserID, instanceID string, lastSeenAt, expiresAt time.Time) error
MarkStale(context.Context, id string, at time.Time) error
MarkExpired(context.Context, at time.Time) (int64, error)

Register(context.Context, ClientConnectionLease) (ClientConnectionLease, error)
Get(context.Context, connectionID string) (ClientConnectionLease, error)
Renew(context.Context, connectionID string, epoch int64, ttl time.Duration) error
Release(context.Context, connectionID string, epoch int64) error
List(context.Context, ClientConnectionFilter, cursor string, limit int) (Page[ClientConnectionLease], error)
ListByInstance(context.Context, clientInstanceID string) ([]ClientConnectionLease, error)
ListByInstances(context.Context, clientInstanceIDs []string) ([]ClientConnectionLease, error)
UpdateStats(context.Context, ClientConnectionLease) error
```

Owner, token, server, status, and keyword filters must be part of the SQL `WHERE` clause. The Agent filter uses `INSTR(metadata, ?) > 0` with a quoted, bounded Agent ID instead of `LIKE`, so `%` and `_` are never interpreted as wildcards. Status uses the same expiry predicates used by the view: `online` requires an unexpired lease, `offline` has no unexpired lease, `stale` requires `stale=1`, and `metadata_unavailable` means empty capabilities. Cursor pagination uses the composite cursor `(updated_at, id)` for instances and `(updated_at, connection_id)` for connections, with the existing base64 helper. `ListByInstances` returns an empty page before building SQL when the ID slice is empty, limits the `IN` list to one page of instance IDs, and uses one query so page rendering does not become N+1.

`Upsert` opens a transaction, locks `(owner_user_id, instance_id)` on MySQL with `SELECT ... FOR UPDATE`, inserts a new row when absent, updates an existing row otherwise, and returns the persisted row. This gives every Server the same stable `id` even when two connections for one client instance race. `TouchInstance` updates `last_seen_at`, `expires_at`, `stale=0`, and `updated_at` only by the `(owner_user_id, instance_id)` key.

- [ ] **Step 5: Run storage tests**

Run:

```bash
go test ./internal/storage -count=1
```

Expected: PASS.

---

### Task 6: Server Session Registry and Connection Leases

**Files:**

- Modify: `internal/server/session_manager.go`
- Modify: `internal/server/ws_client.go`
- Modify: `internal/server/stream_authorizer.go`
- Modify: `internal/server/middleware.go`
- Modify: `internal/server/runtime.go`
- Create: `internal/server/client_observability.go`
- Create: `internal/server/client_observability_test.go`
- Create: `internal/server/client_connection_lease.go`
- Create: `internal/server/client_connection_lease_test.go`
- Create: `internal/server/client_metadata_sweeper.go`
- Create: `internal/server/client_metadata_sweeper_test.go`
- Modify: `internal/server/session_test.go`
- Modify: `internal/server/ws_client_test.go`

**Interfaces:**

- Consumes: `storage.ClientInstanceRepository` and `storage.ClientConnectionRepository`.
- Produces: `server.ClientSessionRecord`, `server.ClientSessionManager.Register(record, transport)`, `server.ClientSessionManager.CloseConnection(connectionID, epoch)`, and `server.ClientSessionManager.ActiveStreams(connectionID)`.
- Produces: `server.ClientConnectionLeaseController` with `Register`, `Heartbeat`, and `Release`.
- Produces: `server.ClientMetadataSweeper` that periodically calls `storage.ClientInstanceRepository.MarkExpired`.
- Produces: `ClientSessionPrincipal.MetadataEnabled bool`, populated from the selected WebSocket subprotocol.
- Produces: `server.ClientObservabilityService` that owns metadata upserts, lease registration, heartbeat, stream counters, and release.

- [ ] **Step 1: Write failing session registry tests**

Append to `internal/server/session_test.go`:

```go
func TestClientSessionManagerTracksHeartbeatAndStreams(t *testing.T) {
    manager := NewClientSessionManager()
    transport := newFakeTransport()
    manager.Register(ClientSessionRecord{
        ConnectionID: "client_connection_1", TokenID: "token-1", OwnerUserID: "owner-1",
        ServerNodeID: "server-1", ConnectionEpoch: 1, StartedAt: time.Now().UTC(),
    }, transport)
    manager.ObserveHeartbeat("client_connection_1")
    manager.ObserveStreamOpened("client_connection_1")
    manager.ObserveStreamClosed("client_connection_1")
    record, ok := manager.Get("client_connection_1")
    if !ok {
        t.Fatal("connection not found")
    }
    if record.LastHeartbeatAt.IsZero() || record.ActiveStreams != 0 {
        t.Fatalf("record = %#v", record)
    }
    if err := manager.CloseConnection("client_connection_1", 2); !errors.Is(err, ErrEpoch) {
        t.Fatalf("stale close error=%v, want ErrEpoch", err)
    }
    if err := manager.CloseConnection("client_connection_1", 1); err != nil {
        t.Fatal(err)
    }
    select {
    case <-transport.closed:
    case <-time.After(time.Second):
        t.Fatal("current connection was not closed")
    }
}
```

- [ ] **Step 2: Run session tests and record RED**

Run:

```bash
go test ./internal/server -run TestClientSessionManagerTracksHeartbeatAndStreams -count=1
```

Expected: compilation failure for `ClientSessionRecord` and the new manager methods.

- [ ] **Step 3: Extend the session manager**

Change the internal map to:

```go
type ClientSessionManager struct {
    mu       sync.RWMutex
    sessions map[string]clientSessionEntry
}

type clientSessionEntry struct {
    record    ClientSessionRecord
    transport FrameTransport
}

type ClientSessionRecord struct {
    ConnectionID     string
    ClientInstanceID string
    TokenID          string
    OwnerUserID      string
    ServerNodeID     string
    ConnectionEpoch  int64
    StartedAt        time.Time
    LastHeartbeatAt  time.Time
    ActiveStreams    int64
    MetadataEnabled  bool
}
```

Add methods:

- `Register(record ClientSessionRecord, tr FrameTransport)`
- `Remove(id string)`
- `Get(id string) (ClientSessionRecord, bool)`
- `OpenStream(ctx, id, req)`
- `ObserveHeartbeat(id)`
- `ObserveStreamOpened(id)`
- `ObserveStreamClosed(id)`
- `ActiveStreams(id) int`
- `CloseConnection(id, epoch)`

`CloseConnection` must compare the stored `ConnectionEpoch` before closing the transport.

- [ ] **Step 4: Implement lease controller**

Create `internal/server/client_connection_lease.go` modeled on `AgentConnectionLeaseController`:

```go
type ClientConnectionLeaseController struct {
    connections storage.ClientConnectionRepository
    manager     *ClientSessionManager
    serverNodeID string
    ttl         time.Duration
}

func NewClientConnectionLeaseController(connections storage.ClientConnectionRepository, manager *ClientSessionManager, serverNodeID string, ttl time.Duration) *ClientConnectionLeaseController
```

Implement:

- `Register(ctx, record)` inserts or atomically acquires a lease.
- `Heartbeat(ctx, record)` renews the lease and writes active stream count and health score.
- `Release(ctx, connectionID, epoch)` releases the lease with epoch fencing.
- `CloseConnection(connectionID, epoch)` closes only the local current connection.

Default heartbeat interval is 30 seconds and TTL is 90 seconds.

Extend `ClientSessionPrincipal`:

```go
type ClientSessionPrincipal struct {
    ConnectionID    string
    Identity        auth.TokenIdentity
    StrictOpen      bool
    MetadataEnabled bool
}
```

In `clientWebSocketHandshake`, set:

```go
principal.StrictOpen = selected == protocol.SubprotocolOpenResult ||
    selected == protocol.SubprotocolFlowControl ||
    selected == protocol.SubprotocolClientMetadata
principal.MetadataEnabled = selected == protocol.SubprotocolClientMetadata
```

This preserves strict-open and flow-control behavior when the metadata subprotocol is selected.

Create `ClientMetadataSweeper` with:

```go
type ClientMetadataSweeper struct {
    instances storage.ClientInstanceRepository
    interval  time.Duration
}

func NewClientMetadataSweeper(instances storage.ClientInstanceRepository, interval time.Duration) *ClientMetadataSweeper
func (s *ClientMetadataSweeper) Run(ctx context.Context) error
func (s *ClientMetadataSweeper) Sweep(ctx context.Context, now time.Time) (int64, error)
```

`Sweep` calls `MarkExpired(ctx, now)` and is safe to retry. `Run` starts a ticker with context cancellation; runtime starts it alongside the connection lease heartbeat and stops it during shutdown.

Create `ClientObservabilityService` with:

```go
type ClientObservabilityService struct {
    instances   storage.ClientInstanceRepository
    leases      *ClientConnectionLeaseController
    manager     *ClientSessionManager
    serverNodeID string
    metadataTTL time.Duration
}

func NewClientObservabilityService(instances storage.ClientInstanceRepository, leases *ClientConnectionLeaseController, manager *ClientSessionManager, serverNodeID string, metadataTTL time.Duration) *ClientObservabilityService

func (s *ClientObservabilityService) Hello(ctx context.Context, principal ClientSessionPrincipal, payload protocol.ClientMetadataPayload) (protocol.ClientMetadataAckPayload, error)
func (s *ClientObservabilityService) Update(ctx context.Context, principal ClientSessionPrincipal, payload protocol.ClientMetadataPayload) (protocol.ClientMetadataAckPayload, error)
func (s *ClientObservabilityService) RegisterLegacy(ctx context.Context, principal ClientSessionPrincipal) (string, error)
func (s *ClientObservabilityService) Heartbeat(ctx context.Context, principal ClientSessionPrincipal) error
func (s *ClientObservabilityService) StreamOpened(principal ClientSessionPrincipal) error
func (s *ClientObservabilityService) StreamClosed(principal ClientSessionPrincipal) error
func (s *ClientObservabilityService) Release(ctx context.Context, principal ClientSessionPrincipal) error
```

`Hello` and `Update` validate the payload against the token owner and token type, call the transactional repository `Upsert`, store the returned stable `ClientInstanceID` in the session manager, register or renew the lease, and return an ACK. `RegisterLegacy` creates the synthetic `metadata_unavailable` row for an old client and registers its basic lease. `Heartbeat` renews the lease, persists the manager's stream counters, and calls `TouchInstance` so PING-only clients do not become stale. `StreamOpened` and `StreamClosed` update only the in-memory manager; durable stats follow on the next heartbeat. `Release` releases the exact lease with epoch fencing and leaves metadata to the sweeper.

Add `ClientStreamServiceConfig.Observability *ClientObservabilityService`. `NewClientStreamService` keeps its current signature and stores the optional dependency. `serveClientSessionWithService` uses it only when non-nil; the exported `ServeClientSession` path and existing unit tests therefore continue to work without observability.

- [ ] **Step 5: Wire metadata and leases into ServeClientSession**

In `ServeClientSession`:

- At session start, build `ClientSessionRecord` from the principal and local node ID, generate a positive random `ConnectionEpoch`, register it in `ClientSessionManager`, and start a 30-second heartbeat loop. The loop calls `Observability.Heartbeat`; heartbeat failures are logged but do not close the data-plane session.
- When metadata is enabled, require `CLIENT_HELLO` as the first frame. Decode and validate it, call `Observability.Hello`, and send `CLIENT_METADATA_ACK`. A validation failure returns an ACK with `Accepted=false` and field errors, then falls back to `RegisterLegacy` so the basic connection remains observable; it does not close the client session.
- Handle later `CLIENT_METADATA_UPDATE` frames with `Observability.Update`, send an ACK, and ignore duplicate or stale revisions idempotently.
- When metadata is not enabled, call `RegisterLegacy` before entering the normal receive loop, using the synthetic instance key `legacy-` + `connection_id` and empty capabilities/metadata. This lets old clients appear in the basic list without inventing a client identity.
- Update the in-memory heartbeat on `FramePing`, and update stream counters on successful OPEN and terminal CLOSE/RESET paths.
- On every return path, stop the heartbeat loop, call `Observability.Release`, and remove the session from the manager.
- Runtime starts `ClientMetadataSweeper` in `NewServerRuntime` with `context.Background()` and a 30-second interval, and stops it in `ServerRuntime.Close`.

Runtime wiring is explicit:

```go
clientSessions := NewClientSessionManager()
clientLeases := NewClientConnectionLeaseController(db.ClientConnections(), clientSessions, serverNodeID, defaultClientConnectionLeaseTTL)
clientObservability := NewClientObservabilityService(db.ClientInstances(), clientLeases, clientSessions, serverNodeID, defaultClientMetadataTTL)
runtime.ClientSessions = clientSessions
runtime.ClientConnectionLeases = clientLeases
runtime.ClientObservability = clientObservability
runtime.ClientStreamService = NewClientStreamService(runtime.ClientAuthorizer, runtime.ClientTransport, ClientStreamServiceConfig{
    MaxConcurrentOpens: runtimeConfig.Stream.MaxConcurrentOpens,
    MaxPendingOpens:    runtimeConfig.Stream.MaxPendingOpens,
    OpenTimeout:        clientOpenTimeoutFromConfig(runtimeConfig.Stream),
    Observability:       clientObservability,
}, runtime.metrics)
runtime.clientMetadataSweeper = NewClientMetadataSweeper(db.ClientInstances(), defaultClientMetadataSweepInterval)
```

`serveClientWS` no longer calls `ClientSessions.Register` or `Remove` directly; `serveClientSessionWithService` owns that lifecycle. `ServerRuntime.Close` stops the sweeper before closing the client stream service and joins its error into the existing returned error.

- [ ] **Step 6: Run server tests**

Run:

```bash
go test ./internal/server -count=1
```

Expected: PASS.

---

### Task 7: Client Management API

**Files:**

- Create: `internal/server/client_api.go`
- Create: `internal/server/client_api_test.go`
- Create: `internal/server/client_connection_close.go`
- Create: `internal/server/client_connection_close_test.go`
- Modify: `internal/server/api.go`
- Modify: `internal/server/runtime.go`
- Modify: `internal/relay/service.go`
- Modify: `internal/relay/transport.go`
- Modify: `internal/relay/server_node_auth_test.go`
- Modify: `docs/api/openapi.yaml`
- Modify: `docs/README.md`
- Modify: `docs/user-guide/server-admin.md`

**Interfaces:**

- Produces: `GET /api/v1/clients`.
- Produces: `GET /api/v1/clients/{clientInstanceId}`.
- Produces: `GET /api/v1/clients/{clientInstanceId}/connections`.
- Produces: `DELETE /api/v1/clients/{clientInstanceId}/connections/{connectionId}?connectionEpoch=...`.
- Produces: `ClientConnectionCloseService`, `ClusterClientConnectionCloseService`, and authenticated relay method `CloseClientConnection`.

- [ ] **Step 1: Write failing API tests**

Create `internal/server/client_api_test.go`:

```go
package server

import (
    "context"
    "net/http"
    "net/http/httptest"
    "strings"
    "testing"
    "time"

    "github.com/tunnelmesh/tunnelmesh/internal/auth"
    "github.com/tunnelmesh/tunnelmesh/internal/storage"
)

func TestClientAPIListsOnlyOwnerScopedClients(t *testing.T) {
    test := newClientAPITest(t)
    test.createClient("client-instance-1", "owner-1", "client-local-1")
    test.createClient("client-instance-2", "owner-2", "client-local-2")
    response := test.requestAsUser(http.MethodGet, "/api/v1/clients", "owner-1")
    if response.Code != http.StatusOK {
        t.Fatalf("status = %d", response.Code)
    }
    if strings.Count(response.Body.String(), "client-instance-") != 1 {
        t.Fatalf("owner filter leaked data: %s", response.Body.String())
    }
}

func TestClientAPICloseRequiresEpoch(t *testing.T) {
    test := newClientAPITest(t)
    test.createClient("client-instance-1", "owner-1", "client-local-1")
    response := test.requestAsUser(http.MethodDelete, "/api/v1/clients/client-instance-1/connections/client_connection_1", "owner-1")
    if response.Code != http.StatusBadRequest {
        t.Fatalf("status = %d, want 400", response.Code)
    }
}

func TestClientAPICloseDelegatesEpochToClusterService(t *testing.T) {
    test := newClientAPITest(t)
    test.createClient("client-instance-1", "owner-1", "client-local-1")
    response := test.requestAsUser(http.MethodDelete, "/api/v1/clients/client-instance-1/connections/client_connection_1?connectionEpoch=7", "owner-1")
    if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"closed":true`) {
        t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
    }
    request := test.closeService.requests[0]
    if request.ClientInstanceID != "client-instance-1" || request.ConnectionID != "client_connection_1" || request.ConnectionEpoch != 7 {
        t.Fatalf("close request = %#v", request)
    }
}

type clientAPITest struct {
    db           *storage.DB
    handler      http.Handler
    users        map[string]storage.User
    tokens       map[string]string
    closeService *recordingClientConnectionCloseService
}

func newClientAPITest(t *testing.T) *clientAPITest {
    t.Helper()
    ctx := context.Background()
    db, err := storage.OpenSQLite(ctx, "file:client-api-"+t.Name()+"?mode=memory&cache=shared")
    if err != nil {
        t.Fatal(err)
    }
    t.Cleanup(func() { _ = db.Close() })
    authn := auth.NewAuthService(db)
    test := &clientAPITest{db: db, users: map[string]storage.User{}, tokens: map[string]string{}}
    for _, key := range []string{"owner-1", "owner-2"} {
        user, err := authn.CreateUser(ctx, key, key+"-pass", "user")
        if err != nil {
            t.Fatal(err)
        }
        login, err := authn.Login(ctx, key, key+"-pass")
        if err != nil {
            t.Fatal(err)
        }
        test.users[key] = user
        test.tokens[key] = login.Token
    }
    api := NewAPI(db, authn)
    test.closeService = &recordingClientConnectionCloseService{}
    api.SetClusterClientConnections(test.closeService)
    test.handler = api.Handler()
    return test
}

func (t *clientAPITest) requestAsUser(method, path, userKey string) *httptest.ResponseRecorder {
    request := httptest.NewRequest(method, path, nil)
    request.Header.Set("Authorization", "Bearer "+t.tokens[userKey])
    response := httptest.NewRecorder()
    t.handler.ServeHTTP(response, request)
    return response
}

func (t *clientAPITest) createClient(id, ownerKey, instanceID string) {
    now := time.Date(2026, 9, 11, 10, 0, 0, 0, time.UTC)
    expires := now.Add(time.Minute)
    instance := storage.ClientInstance{
        ID: id, OwnerUserID: t.users[ownerKey].ID, InstanceID: instanceID,
        Metadata: `{"version":"v1.2.3"}`, Capabilities: `["client_metadata.v1"]`,
        ReportedAt: now, LastSeenAt: now, ExpiresAt: &expires, UpdatedAt: now,
    }
    if err := t.db.ClientInstances().Upsert(context.Background(), instance); err != nil {
        t.Fatal(err)
    }
}

type recordingClientConnectionCloseService struct {
    requests []ClientConnectionCloseRequest
}

func (s *recordingClientConnectionCloseService) Close(_ context.Context, request ClientConnectionCloseRequest) error {
    s.requests = append(s.requests, request)
    return nil
}
```

- [ ] **Step 2: Run API tests and record RED**

Run:

```bash
go test ./internal/server -run 'TestClientAPI' -count=1
```

Expected: FAIL because `/api/v1/clients` returns 404.

- [ ] **Step 3: Implement API routes and handlers**

In `api.go`, route:

```go
case "clients":
    a.handleClients(w, r, principal, parts[1:])
```

Implement response models:

```go
type ClientView struct {
    ID               string
    InstanceID       string
    OwnerUserID      string
    Version          string
    Platform         string
    Hostname         string
    Status           string
    ActiveConnections int
    ActiveStreams     int
    ServerNodeIDs     []string
    LastSeenAt        string
    Metadata          map[string]string
}

type ClientConnectionView struct {
    ConnectionID     string
    ClientInstanceID string
    TokenID          string
    OwnerUserID      string
    ServerNodeID     string
    ConnectionEpoch  int64
    ActiveStreams    int
    HealthScore      int64
    AcquiredAt       string
    LastHeartbeatAt  string
    ExpiresAt        string
    Local            bool
}
```

For normal users, force `OwnerUserID` to the authenticated user; for admins, allow the filter. Build page views by fetching the instance page first, then one batched connection query through `ListByInstances`, and map lease `UpdatedAt` to `LastHeartbeatAt`. Close first fetches the instance and verifies Owner or admin, validates that the connection belongs to that instance, then delegates to `ClientConnectionCloseService`. Successful close returns the project's existing JSON shape with `closed=true`; stale epoch returns 409, node unavailable returns 503, and missing connection returns 200 with `closed=true` to match the Agent close API's idempotent behavior. Write an audit log for successful and failed close with action `client.connection.close`, containing only IDs and epoch.

Implement cross-node close:

```go
// internal/server/client_connection_close.go
type ClientConnectionCloseRequest struct {
    ClientInstanceID string
    ConnectionID     string
    ConnectionEpoch  int64
}

type ClientConnectionCloseService interface {
    Close(context.Context, ClientConnectionCloseRequest) error
}

type ClientConnectionRelayClient interface {
    CloseClientConnection(context.Context, relay.CloseClientConnectionRequest) error
    Close() error
}

type ClusterClientConnectionCloseService struct {
    connections    storage.ClientConnectionRepository
    nodes          storage.NodeRepository
    local          *ClientConnectionLeaseController
    localNodeID    string
    dialRelayNode  func(context.Context, string, int64) (ClientConnectionRelayClient, error)
}
```

In `internal/relay/service.go`, define:

```go
type CloseClientConnectionRequest struct {
    ConnectionID      string
    ConnectionEpoch   int64
    RequestedByNodeID string
}

type CloseClientConnectionFunc func(context.Context, CloseClientConnectionRequest) error
```

`ClusterClientConnectionCloseService.Close` reads the durable lease by connection ID, rejects an epoch mismatch with `ErrEpoch`, verifies the lease's `ClientInstanceID`, and then branches by `ServerNodeID`. A local lease is closed through `ClientConnectionLeaseController`; a remote lease first calls `storage.NodeRepository.Get(serverNodeID)`, requires the node to be enabled and non-deleted with a positive epoch, then dials that exact node address and epoch. Node lookup or dial failure returns `ErrClientConnectionNodeUnavailable` and leaves the durable lease untouched. Map relay `FailedPrecondition` to `ErrEpoch`, `NotFound` to `ErrSessionClosed`, and all dial/transport failures to `ErrClientConnectionNodeUnavailable`.

Add the runtime control handler:

```go
func (r *ServerRuntime) closeClientConnection(ctx context.Context, req relay.CloseClientConnectionRequest) error {
    if r == nil || r.ClientConnectionLeases == nil {
        return status.Error(codes.Unimplemented, "client connection close control is not configured")
    }
    principal, ok := relay.ServerNodePrincipalFromContext(ctx)
    if !ok || principal.NodeID != req.RequestedByNodeID {
        return status.Error(codes.PermissionDenied, "relay close caller identity is denied")
    }
    if err := r.ClientConnectionLeases.CloseConnection(ctx, req.ConnectionID, req.ConnectionEpoch); err != nil {
        switch {
        case errors.Is(err, ErrEpoch):
            return status.Error(codes.FailedPrecondition, "client connection epoch is stale")
        case errors.Is(err, ErrSessionClosed):
            return status.Error(codes.NotFound, "client connection is not found")
        default:
            return status.Error(codes.Internal, "client connection close failed")
        }
    }
    return nil
}
```

Extend `internal/relay/transport.go`:

```go
type RelayServer interface {
    OpenStream(RelayOpenStreamServer) error
    CloseAgentConnection(context.Context, CloseAgentConnectionRequest) error
    CloseClientConnection(context.Context, CloseClientConnectionRequest) error
}

type RelayControlHandler struct {
    CloseAgentConnection CloseAgentConnectionFunc
    CloseClientConnection CloseClientConnectionFunc
}
```

Add a backward-compatible `NewRelayServerWithControls` constructor:

```go
func NewRelayServerWithControls(
    openResultHandler RelayOpenResultHandler,
    legacyHandler func(context.Context, StreamRequest) (io.ReadWriteCloser, error),
    closeAgentHandler CloseAgentConnectionFunc,
    closeClientHandler CloseClientConnectionFunc,
) RelayServer
```

Keep the four existing constructors unchanged; they set `closeClientHandler=nil`. Register the new unary method as `/tunnelmesh.relay.v1.Relay/CloseClientConnection`, and add the equivalent method to `GRPCNodeTransport`. Existing constructors continue to work and return `Unimplemented` when the client close handler is nil. Encode and decode the new request with `structpb.Struct` fields `connection_id`, `connection_epoch`, and `requested_by_node_id`, following the existing Agent close metadata helpers. `ServerRuntime` switches to the new constructor only in the relay-enabled branch and passes `runtime.closeClientConnection`.

Update `internal/relay/server_node_auth_test.go` with four cases: unauthenticated close, valid authenticated close, stale epoch, and a nil handler returning `Unimplemented`. Do not change the existing Agent close tests or constructor behavior.

- [ ] **Step 4: Update OpenAPI and docs**

Add all four endpoints, response schemas, error codes, cursor parameters, and the admin-only nature of cross-owner filters. Update the documentation index and server admin guide with permissions, status meanings, and examples.

- [ ] **Step 5: Run API tests**

Run:

```bash
go test ./internal/server -run 'TestClientAPI' -count=1
go test ./internal/server -count=1
```

Expected: PASS.

---

### Task 8: Web API Types, Routes, and Navigation

**Files:**

- Modify: `web/src/api/client.ts`
- Modify: `web/src/router.ts`
- Modify: `web/src/layouts/AppShell.vue`
- Modify: `web/src/layouts/breadcrumbs.ts`
- Modify: `web/src/i18n/messages/zh-CN.ts`
- Modify: `web/src/i18n/messages/en-US.ts`
- Modify: `web/src/tests/shell.spec.ts`

**Interfaces:**

- Produces: `ClientInstance`, `ClientConnection`, `ClientPage`, `listClients`, `getClientDetail`, `listClientConnections`, and `closeClientConnection`.
- Produces: `/clients` route and navigation entry.

- [ ] **Step 1: Write failing frontend source tests**

Append to `web/src/tests/shell.spec.ts`:

```ts
it('contains client and download navigation entries', () => {
  const shell = readFileSync('src/layouts/AppShell.vue', 'utf8')
  const router = readFileSync('src/router.ts', 'utf8')
  expect(shell).toContain("'/clients'")
  expect(shell).toContain("'/downloads'")
  expect(router).toContain("path:'/clients'")
  expect(router).toContain("path:'/downloads'")
})
```

Create or extend an API source test that asserts `client.ts` contains:

```ts
export function listClients(params: ClientListParams = {})
export function closeClientConnection(clientInstanceId: string, connectionId: string, connectionEpoch: number)
```

- [ ] **Step 2: Run frontend tests and record RED**

Run:

```bash
cd web && npm test -- --run
```

Expected: FAIL for missing navigation and API functions.

- [ ] **Step 3: Implement types and API functions**

Add:

```ts
export type ClientStatus = 'online' | 'offline' | 'stale' | 'metadata_unavailable'

export type ClientConnection = {
  connectionId: string
  serverNodeId: string
  connectionEpoch: number
  activeStreams: number
  healthScore: number
  acquiredAt: string
  expiresAt: string
}

export type ClientInstance = {
  id: string
  instanceId: string
  ownerUserId: string
  version: string
  platform: string
  hostname: string
  status: ClientStatus
  activeConnections: number
  activeStreams: number
  serverNodeIds: string[]
  lastSeenAt: string
  metadata?: Record<string, string>
  connections?: ClientConnection[]
}
```

Implement the API functions using the existing `api<T>()` helper and URL encoding.

- [ ] **Step 4: Add routes and i18n**

Add `/clients` and `/downloads` routes. Add navigation entries and Chinese/English labels for:

- Clients
- Client details
- Connections
- Downloads
- Refresh
- Reset
- Query
- Close connection

- [ ] **Step 5: Run frontend tests**

Run:

```bash
cd web && npm test -- --run
```

Expected: PASS.

---

### Task 9: Clients Admin Page

**Files:**

- Create: `web/src/views/Clients.vue`
- Modify: `web/src/tests/shell.spec.ts`

**Interfaces:**

- Consumes: `listClients`, `getClientDetail`, `listClientConnections`, and `closeClientConnection`.
- Produces: `/clients` page with filters, summary cards, table, and detail drawer.

- [ ] **Step 1: Write failing page source tests**

Append to `web/src/tests/shell.spec.ts`:

```ts
it('clients page contains filter, summary, table, and detail surface', () => {
  const source = readFileSync('src/views/Clients.vue', 'utf8')
  expect(source).toContain('listClients')
  expect(source).toContain('el-table')
  expect(source).toContain('clients.reset')
  expect(source).toContain('clients.query')
  expect(source).toContain('clientDetailTitle')
  expect(source).toContain('closeConnection')
})
```

- [ ] **Step 2: Run frontend tests and record RED**

Run:

```bash
cd web && npm test -- --run
```

Expected: FAIL because `src/views/Clients.vue` does not exist.

- [ ] **Step 3: Implement the page**

Build `Clients.vue` with:

- `PageHeader`
- `DataState`
- admin-only Owner filter
- Token, server, status, agent, and keyword filters
- Reset and Query buttons in the lower-right filter area
- four summary cards
- client instance table
- detail drawer
- metadata descriptions
- local listener table
- connection table
- per-connection close action with confirm dialog and `connectionEpoch`

Use Element Plus components and existing light SaaS styles. Do not render secrets. Show `—` for unavailable metadata and use `metadata_unavailable` as a distinct status.

- [ ] **Step 4: Run frontend tests and build**

Run:

```bash
cd web && npm test -- --run
cd web && npm run build
```

Expected: PASS with no frontend build errors.

---

### Task 10: Downloads API and Page

**Files:**

- Modify: `internal/config/config.go`
- Modify: `internal/config/config_test.go`
- Modify: `internal/server/api.go`
- Modify: `internal/server/runtime.go`
- Modify: `internal/cli/root.go`
- Create: `internal/server/download_api.go`
- Create: `internal/server/download_api_test.go`
- Create: `web/src/views/Downloads.vue`
- Modify: `web/src/api/client.ts`
- Modify: `web/src/tests/views.spec.ts`
- Modify: `docs/api/openapi.yaml`

**Interfaces:**

- Produces: top-level `config.Config.Downloads DownloadsConfig` with `downloads.github_repository`.
- Produces: `RuntimeConfig.Downloads` and `API.SetDownloads(config.DownloadsConfig)`.
- Produces: `config.DownloadsConfig` with `github_repository` default `nnworld/TunnelMesh`.
- Produces: `GET /api/v1/downloads`.
- Produces: `DownloadAsset`, `DownloadInfo`, and `getDownloads` in the frontend API client.

- [ ] **Step 1: Write failing API tests**

Create `internal/server/download_api_test.go`:

```go
package server

import (
    "context"
    "net/http"
    "net/http/httptest"
    "strings"
    "testing"

    "github.com/tunnelmesh/tunnelmesh/internal/auth"
    "github.com/tunnelmesh/tunnelmesh/internal/build"
    "github.com/tunnelmesh/tunnelmesh/internal/config"
)

func TestDownloadsAPIReturnsCurrentReleaseAssets(t *testing.T) {
    test := newDownloadAPITest(t, "nnworld/TunnelMesh", "v1.2.3")
    response := test.requestAsAdmin(http.MethodGet, "/api/v1/downloads")
    if response.Code != http.StatusOK {
        t.Fatalf("status = %d", response.Code)
    }
    body := response.Body.String()
    for _, expected := range []string{"v1.2.3", "linux-amd64", "SHA256SUMS", "manifest.json"} {
        if !strings.Contains(body, expected) {
            t.Fatalf("downloads response missing %q: %s", expected, body)
        }
    }
}

type downloadAPITest struct {
    handler http.Handler
    token   string
}

func newDownloadAPITest(t *testing.T, repository, version string) *downloadAPITest {
    t.Helper()
    api, admin, _ := apiTestServer(t)
    authn := auth.NewAuthService(api.DB)
    login, err := authn.Login(context.Background(), admin.Username, "admin-pass")
    if err != nil {
        t.Fatal(err)
    }
    originalVersion := build.Version
    build.Version = version
    t.Cleanup(func() { build.Version = originalVersion })
    api.SetDownloads(config.DownloadsConfig{GitHubRepository: repository})
    return &downloadAPITest{handler: api.Handler(), token: login.Token}
}

func (t *downloadAPITest) requestAsAdmin(method, path string) *httptest.ResponseRecorder {
    request := httptest.NewRequest(method, path, nil)
    request.Header.Set("Authorization", "Bearer "+t.token)
    response := httptest.NewRecorder()
    t.handler.ServeHTTP(response, request)
    return response
}
```

- [ ] **Step 2: Run API test and record RED**

Run:

```bash
go test ./internal/server -run TestDownloadsAPIReturnsCurrentReleaseAssets -count=1
```

Expected: FAIL because the endpoint returns 404.

- [ ] **Step 3: Implement downloads API**

Add config:

```go
type Config struct {
    // Existing fields remain unchanged.
    Downloads DownloadsConfig `mapstructure:"downloads" json:"downloads" yaml:"downloads"`
}

type DownloadsConfig struct {
    GitHubRepository string `mapstructure:"github_repository" json:"github_repository" yaml:"github_repository"`
}
```

Set the default to `nnworld/TunnelMesh`, add `Downloads config.DownloadsConfig` to `RuntimeConfig`, pass `cfg.Downloads` into `NewServerRuntime`, and call `runtime.API.SetDownloads(runtimeConfig.Downloads)` during construction.

Return:

```go
type DownloadAsset struct {
    Platform string
    Archive  string
    URL      string
}

type DownloadInfo struct {
    Version       string
    Commit        string
    BuildTime     string
    Repository    string
    ReleaseURL    string
    ChecksumURL   string
    ManifestURL   string
    SchemaVersion int
    Assets        []DownloadAsset
}
```

Generate URLs with:

```text
https://github.com/{repository}/releases/download/{version}/{archive}
```

Restrict the endpoint to administrators.

- [ ] **Step 4: Implement downloads page**

Create `Downloads.vue` with current version, commit, build time, schema version, platform cards, archive links, checksum command, and GitHub Release link. Use the existing light SaaS style and i18n. Extend the views test so it asserts the version, checksum, and platform surface without asserting layout snapshots.

- [ ] **Step 5: Run tests**

Run:

```bash
go test ./internal/server -run TestDownloadsAPI -count=1
cd web && npm test -- --run
```

Expected: PASS.

---

### Task 11: Release Build and GitHub Workflow

**Files:**

- Modify: `scripts/build-release.sh`
- Modify: `docs/deployment/binary-release.md`
- Modify: `README.md`
- Create: `.github/workflows/release.yml`
- Create: `.github/workflows/release-test.md`

**Interfaces:**

- Produces: version-injected binaries.
- Produces: six platform archives, `SHA256SUMS`, and `manifest.json`.
- Produces: GitHub Release workflow for `v*.*.*` tags and manual dispatch.

- [ ] **Step 1: Write failing script checks**

Create `.github/workflows/release-test.md` as a review checklist with:

```markdown
# Release Workflow Manual Checks

1. `bash -n scripts/build-release.sh` exits 0.
2. `VERSION=v0.0.0-test ./scripts/build-release.sh` creates six archives.
3. `dist/v0.0.0-test/manifest.json` contains schemaVersion 11.
4. `sha256sum -c dist/v0.0.0-test/SHA256SUMS` passes.
5. Every archive contains all three binaries and platform service templates.
```

Run:

```bash
VERSION=v0.0.0-test ./scripts/build-release.sh
test -f dist/v0.0.0-test/manifest.json
```

Expected: FAIL because `manifest.json` is not generated.

- [ ] **Step 2: Upgrade the release script**

In `scripts/build-release.sh`:

- validate `VERSION` as `vMAJOR.MINOR.PATCH` for non-dev releases.
- capture `COMMIT` from `git rev-parse HEAD`.
- capture UTC `BUILD_TIME` once.
- inject the three `-X` linker variables for every binary.
- copy `deploy/macos` and `deploy/windows` into archives.
- write `manifest.json` with version, major, commit, build time, schema version, binaries, and platforms.
- append archive checksums to `SHA256SUMS`.

Use this linker flag template:

```bash
LDFLAGS="-s -w -X github.com/tunnelmesh/tunnelmesh/internal/build.Version=${VERSION} -X github.com/tunnelmesh/tunnelmesh/internal/build.Commit=${COMMIT} -X github.com/tunnelmesh/tunnelmesh/internal/build.BuildTime=${BUILD_TIME}"
```

- [ ] **Step 3: Create the GitHub workflow**

Create `.github/workflows/release.yml`:

```yaml
name: release

on:
  push:
    tags:
      - "v*.*.*"
  workflow_dispatch:
    inputs:
      version:
        description: "Full release version, for example v1.2.3"
        required: true

permissions:
  contents: write

jobs:
  release:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with:
          go-version-file: go.mod
          cache: true
      - uses: actions/setup-node@v4
        with:
          node-version: 20
          cache: npm
          cache-dependency-path: web/package-lock.json
      - run: cd web && npm ci
      - run: cd web && npm test -- --run
      - run: cd web && npm run build
      - run: rsync -a --delete web/dist/ internal/server/web_dist/
      - run: ./scripts/verify-web-embed.sh
      - run: go test ./... -count=1
      - run: go test -race ./...
      - run: go vet ./...
      - name: Resolve and validate release version
        id: version
        run: |
          if [[ "${GITHUB_EVENT_NAME}" == "workflow_dispatch" ]]; then
            version="${{ github.event.inputs.version }}"
          else
            version="${GITHUB_REF_NAME}"
          fi
          if [[ ! "$version" =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
            echo "release version must match vMAJOR.MINOR.PATCH" >&2
            exit 1
          fi
          echo "version=$version" >> "$GITHUB_OUTPUT"
      - run: VERSION="${{ steps.version.outputs.version }}" ./scripts/build-release.sh
      - run: gh release create "${{ steps.version.outputs.version }}" dist/"${{ steps.version.outputs.version }}"/* --target "${GITHUB_SHA}" --title "${{ steps.version.outputs.version }}" --generate-notes
        env:
          GH_TOKEN: ${{ github.token }}
```

- [ ] **Step 4: Update release documentation**

Update `docs/deployment/binary-release.md` and `README.md` with:

- GitHub Release URL
- asset naming
- checksum verification
- manifest schema version
- release trigger
- rollback to previous release
- private-repository permission note

- [ ] **Step 5: Run release verification**

Run:

```bash
bash -n scripts/build-release.sh
VERSION=v0.0.0-test ./scripts/build-release.sh
sha256sum -c dist/v0.0.0-test/SHA256SUMS
test "$(jq -r .schemaVersion dist/v0.0.0-test/manifest.json)" = "11"
```

Expected: all commands exit 0.

---

### Task 12: Full Verification and PR Documentation

**Files:**

- Create: `docs/pull-requests/2026-09-11-client-observability.md`
- Create: `docs/pull-requests/2026-09-11-platform-release-downloads.md`
- Modify: generated web embed assets under `internal/server/web_dist/`

**Interfaces:**

- Consumes: all previous tasks.
- Produces: complete verification evidence and PR descriptions.

- [ ] **Step 1: Regenerate embedded frontend**

Run:

```bash
cd web
npm test -- --run
npm run build
cd ..
rsync -a --delete web/dist/ internal/server/web_dist/
./scripts/verify-web-embed.sh
```

Expected: frontend tests and build pass; embed directories match byte-for-byte.

- [ ] **Step 2: Run full backend verification**

Run:

```bash
go test ./... -count=1
go test -race ./...
go vet ./...
git diff --check
```

Expected: all commands exit 0.

- [ ] **Step 3: Write PR documents**

Create two PR documents using the required sections:

1. `docs/pull-requests/2026-09-11-client-observability.md`
2. `docs/pull-requests/2026-09-11-platform-release-downloads.md`

Each must contain:

- title
- target branch `main`
- summary
- user impact
- API / Schema / configuration impact
- security and authorization impact
- exact test evidence
- release steps
- rollback steps
- reviewer focus
- integration status

- [ ] **Step 4: Review diff and wait for authorization**

Run:

```bash
git status --short
git diff --check
```

Expected: only intended source, tests, docs, workflow, and regenerated embed assets are changed. Do not commit or push until the user explicitly authorizes it.
