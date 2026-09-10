# Client SOCKS5 CONNECT Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a loopback-safe local SOCKS5 CONNECT proxy to `tunnelmesh-client` that dynamically opens existing Agent-side TCP streams and optionally requires RFC 1929 username/password authentication.

**Architecture:** Add proper SOCKS5 method/request parsing and response encoding to `internal/proxy`, then implement a `SOCKS5Forward` local listener in `internal/client` that maps each CONNECT to an existing logical TCP stream. Wire the listener to a new `forward socks5` CLI command while leaving authorization and target validation on the Server/Agent side.

**Tech Stack:** Go standard library, existing Client WebSocket session, existing StreamOpener abstraction, Cobra CLI.

**Spec:** `docs/superpowers/specs/2026-09-09-client-socks5-design.md`

## Global Constraints

- Do not commit, push, merge, or create a remote PR without explicit user authorization.
- TDD is mandatory: write the failing test first, verify red, implement minimally, verify green.
- Support SOCKS5 CONNECT only; do not implement BIND or UDP ASSOCIATE.
- Default auth mode accepts SOCKS5 method `0x00` only; password mode accepts method `0x02` only.
- Default listener is loopback; non-loopback requires explicit `--allow-remote` and `--auth password`.
- SOCKS5 credentials are read only from `TUNNELMESH_SOCKS5_USERNAME` and `TUNNELMESH_SOCKS5_PASSWORD`.
- Do not resolve SOCKS5 domain targets locally.
- Internal stream protocol remains `"tcp"` so existing token scope and Agent policy semantics are unchanged.
- Public Server ingress remains HTTP/HTTPS/WebSocket only.
- Do not log target hosts, ports, tokens, or handshake bytes.
- Full verification must include `go test ./... -count=1`, `go test -race ./...`, `go vet ./...`, and `git diff --check`.

---

### Task 1: SOCKS5 Handshake Parser and Replies

**Files:**

- Modify: `internal/proxy/handshake.go`
- Test: `internal/proxy/handshake_test.go`

**Interfaces:**

- Produces:

```go
type SOCKS5Reply byte

const (
    SOCKS5ReplySucceeded          SOCKS5Reply = 0x00
    SOCKS5ReplyGeneralFailure     SOCKS5Reply = 0x01
    SOCKS5ReplyCommandUnsupported SOCKS5Reply = 0x07
)

func ReadSOCKS5Methods(r io.Reader) ([]byte, error)
func SupportsSOCKS5NoAuth(methods []byte) bool
func SupportsSOCKS5UsernamePassword(methods []byte) bool
func ReadSOCKS5UsernamePassword(r io.Reader) (SOCKS5Credentials, error)
func EncodeSOCKS5UsernamePasswordReply(success bool) []byte
func ReadSOCKS5Request(r io.Reader) (SOCKS5Request, error)
func EncodeSOCKS5MethodSelection(method byte) []byte
func EncodeSOCKS5Reply(reply SOCKS5Reply) []byte
```

`ReadSOCKS5Request` must return the parsed command even when it is not CONNECT so the listener can return `0x07`.

- [x] **Step 1: Write failing parser tests**

Add these tests to `internal/proxy/handshake_test.go`:

```go
func TestReadSOCKS5Methods(t *testing.T) {
    methods, err := proxy.ReadSOCKS5Methods(bytes.NewReader([]byte{5, 2, 0, 2}))
    if err != nil || len(methods) != 2 || methods[0] != 0 || methods[1] != 2 {
        t.Fatalf("methods=%v err=%v", methods, err)
    }
    if _, err := proxy.ReadSOCKS5Methods(bytes.NewReader([]byte{5, 0})); err == nil {
        t.Fatal("empty method list was accepted")
    }
}

func TestSOCKS5NoAuthSelection(t *testing.T) {
    if !proxy.SupportsSOCKS5NoAuth([]byte{1, 0, 2}) {
        t.Fatal("no-auth method was not detected")
    }
    if proxy.SupportsSOCKS5NoAuth([]byte{1, 2}) {
        t.Fatal("no-auth was incorrectly detected")
    }
    if got := proxy.EncodeSOCKS5MethodSelection(0xff); !bytes.Equal(got, []byte{5, 0xff}) {
        t.Fatalf("method selection=%x", got)
    }
}

func TestSOCKS5UsernamePasswordNegotiationAndAuth(t *testing.T) {
    if !proxy.SupportsSOCKS5UsernamePassword([]byte{0, 2}) {
        t.Fatal("username/password method was not detected")
    }
    raw := append([]byte{1, 5}, []byte("alice")...)
    raw = append(raw, 6)
    raw = append(raw, []byte("secret")...)
    credentials, err := proxy.ReadSOCKS5UsernamePassword(bytes.NewReader(raw))
    if err != nil || credentials.Username != "alice" || credentials.Password != "secret" {
        t.Fatalf("credentials=%+v err=%v", credentials, err)
    }
    if got := proxy.EncodeSOCKS5UsernamePasswordReply(true); !bytes.Equal(got, []byte{1, 0}) {
        t.Fatalf("success reply=%x", got)
    }
    if got := proxy.EncodeSOCKS5UsernamePasswordReply(false); !bytes.Equal(got, []byte{1, 1}) {
        t.Fatalf("failure reply=%x", got)
    }
}

func TestReadSOCKS5RequestSupportsAddressTypes(t *testing.T) {
    requests := []struct {
        name string
        raw  []byte
        host string
        port int
    }{
        {"ipv4", []byte{5, 1, 0, 1, 127, 0, 0, 1, 0x1f, 0x90}, "127.0.0.1", 8080},
        {"domain", append(append([]byte{5, 1, 0, 3, 11}, []byte("example.com")...), 0, 80), "example.com", 80},
        {"ipv6", []byte{5, 1, 0, 4, 0x20, 0x01, 0x0d, 0xb8, 0, 0, 0, 0, 0, 0, 0, 0, 0x00, 0x53, 0x00, 0x50}, "2001:db8::53", 80},
    }
    for _, test := range requests {
        t.Run(test.name, func(t *testing.T) {
            request, err := proxy.ReadSOCKS5Request(bytes.NewReader(test.raw))
            if err != nil || request.Command != proxy.SOCKS5Connect || request.Host != test.host || request.Port != test.port {
                t.Fatalf("request=%+v err=%v", request, err)
            }
        })
    }
}

func TestReadSOCKS5RequestKeepsUnsupportedCommand(t *testing.T) {
    request, err := proxy.ReadSOCKS5Request(bytes.NewReader([]byte{5, 3, 0, 1, 127, 0, 0, 1, 0, 80}))
    if err != nil || request.Command == proxy.SOCKS5Connect || request.Host != "127.0.0.1" || request.Port != 80 {
        t.Fatalf("request=%+v err=%v", request, err)
    }
}

func TestEncodeSOCKS5Reply(t *testing.T) {
    want := []byte{5, 0, 0, 1, 0, 0, 0, 0, 0, 0}
    if got := proxy.EncodeSOCKS5Reply(proxy.SOCKS5ReplySucceeded); !bytes.Equal(got, want) {
        t.Fatalf("reply=%x want=%x", got, want)
    }
}
```

- [x] **Step 2: Verify RED**

Run:

```bash
go test ./internal/proxy -run 'SOCKS5' -count=1
```

Expected: compile failure because the new parser functions do not exist.

- [x] **Step 3: Implement parser and replies**

In `internal/proxy/handshake.go`:

- Import `io`.
- Add `SOCKS5Reply` constants.
- Read exactly `2 + NMETHODS` bytes for method negotiation.
- Parse RFC 1929 version, bounded username/password lengths, and encode its two-byte result.
- Read the fixed request header, then address bytes by `ATYP`.
- Validate version, reserved byte, address length, hostname length, and port.
- Return unsupported commands as parsed requests rather than errors.
- Encode method selection and the fixed success/failure reply.

Keep the existing `ParseSOCKS5Connect` function for compatibility with its current tests.

- [x] **Step 4: Verify GREEN**

```bash
go test ./internal/proxy -count=1
```

### Task 2: Client SOCKS5 Forward Listener

**Files:**

- Create: `internal/client/socks5_forward.go`
- Test: `internal/client/socks5_forward_test.go`

**Interfaces:**

- Consumes:

```go
type StreamOpener interface {
    OpenStream(context.Context, StreamRequest) (io.ReadWriteCloser, error)
}
```

- Produces:

```go
type SOCKS5ForwardConfig struct {
    ListenAddr       string
    AgentID          string
    ConnectTimeout   time.Duration
    HandshakeTimeout time.Duration
    AllowRemote      bool
    AuthMode         SOCKS5AuthMode
    Username         string
    Password         string
}

type SOCKS5Forward struct{}

func NewSOCKS5Forward(opener StreamOpener, cfg SOCKS5ForwardConfig) (*SOCKS5Forward, error)
func (f *SOCKS5Forward) Start(ctx context.Context) error
func (f *SOCKS5Forward) Addr() net.Addr
func (f *SOCKS5Forward) Close() error
```

- [x] **Step 1: Write failing listener tests**

Create `internal/client/socks5_forward_test.go` with tests for:

```go
func TestSOCKS5ForwardConnectsDynamicTCPTarget(t *testing.T)
func TestSOCKS5ForwardPassesDomainWithoutLocalResolution(t *testing.T)
func TestSOCKS5ForwardRejectsUnsupportedMethodWithoutOpeningStream(t *testing.T)
func TestSOCKS5ForwardRejectsUnsupportedCommandWithoutOpeningStream(t *testing.T)
func TestSOCKS5ForwardRejectsNonLoopbackWithoutExplicitAllowRemote(t *testing.T)
func TestSOCKS5ForwardRequiresPasswordAuthForNonLoopback(t *testing.T)
func TestSOCKS5ForwardUsernamePasswordAuthSucceeds(t *testing.T)
func TestSOCKS5ForwardUsernamePasswordAuthFailsWithoutOpeningStream(t *testing.T)
func TestSOCKS5ForwardClosesActiveConnections(t *testing.T)
```

The main positive test must use a real TCP connection and this handshake:

```go
conn.Write([]byte{5, 1, 0})                  // offer NO AUTH
io.ReadFull(conn, make([]byte, 2))          // expect 05 00
conn.Write([]byte{5, 1, 0, 3, 11})          // CONNECT example.com
conn.Write([]byte("example.com"))
conn.Write([]byte{0, 80})                   // port 80
io.ReadFull(conn, make([]byte, 10))         // expect success reply
conn.Write([]byte("request"))
```

The fake opener must assert:

```go
StreamRequest{
    AgentID:    "agent-a",
    Protocol:   "tcp",
    TargetHost: "example.com",
    TargetPort: 80,
}
```

- [x] **Step 2: Verify RED**

```bash
go test ./internal/client -run 'SOCKS5Forward' -count=1
```

Expected: compile failure because `socks5_forward.go` does not exist.

- [x] **Step 3: Implement the listener**

Implement:

```go
func NewSOCKS5Forward(opener StreamOpener, cfg SOCKS5ForwardConfig) (*SOCKS5Forward, error)
```

Validation:

- `opener` must not be nil.
- `ListenAddr` and `AgentID` must be non-empty.
- If `AllowRemote` is false, listen host must be `localhost`, `127.0.0.1`, `::1`, or another `127.x.x.x` address.
- Empty listen host (`:1080`) is non-loopback and rejected.
- `AuthMode` must be `none` or `password`; the zero value is `none`.
- Password mode requires both a username and password.
- Username and password must each be at most 255 bytes.
- Non-loopback listening requires both `AllowRemote=true` and `AuthMode=password`.

Connection handling:

1. Track accepted connections in a map.
2. Set a handshake deadline; default to 10 seconds.
3. Negotiate only no-auth in `none` mode, or method `0x02` in password mode.
4. In password mode, read RFC 1929 credentials and verify them with a constant-time comparison.
5. Read one SOCKS5 request.
6. Return `0x07` for commands other than CONNECT.
7. Open an existing logical stream with `Protocol: "tcp"`.
8. Return `0x01` if the stream cannot open.
9. Return `0x00` after the stream opens.
10. Clear the handshake deadline and bridge the local connection with the logical stream.
11. Close both connections when either side finishes.

- [x] **Step 4: Verify GREEN**

```bash
go test ./internal/client -count=1
```

### Task 3: CLI `forward socks5`

**Files:**

- Modify: `internal/cli/root.go`
- Test: `internal/cli/client_runtime_test.go`
- Test: `internal/cli/root_test.go`

**Interfaces:**

- Produces command:

```bash
tunnelmesh-client forward socks5 \
  --listen 127.0.0.1:1080 \
    --agent agent-devbox \
    [--auth none|password] \
    [--allow-remote]
```

- [x] **Step 1: Write failing CLI tests**

Add:

```go
func TestClientRootExposesSOCKS5Forward(t *testing.T) {
    client := cli.NewClientRoot()
    forward := findCommand(client, "forward")
    socks5 := findCommand(forward, "socks5")
    if socks5 == nil {
        t.Fatal("client forward command missing socks5")
    }
    for _, flag := range []string{"listen", "agent", "auth", "allow-remote"} {
        if socks5.Flag(flag) == nil {
            t.Fatalf("socks5 command missing --%s", flag)
        }
    }
}
```

Extend the authenticated-session CLI test with a SOCKS5 case that asserts:

- `runClientWebSocket` receives the configured server URL and token.
- The command starts `client.NewSOCKS5Forward`.
- Missing `--agent` fails.
- Non-loopback without `--allow-remote` fails.
- Non-loopback with `--allow-remote` but without password auth fails.
- Non-loopback with `--allow-remote`, `--auth password`, and both environment variables reaches the authenticated session path.
- Password mode with a missing environment variable fails before connecting to the Server.

- [x] **Step 2: Verify RED**

```bash
go test ./internal/cli -run 'SOCKS5' -count=1
```

Expected: failure because the command does not exist.

- [x] **Step 3: Implement the CLI command**

Add `clientSOCKS5ForwardCommand(opts *rootOptions) *cobra.Command`.

Flags:

- `--listen`, default `127.0.0.1:0`
- `--agent`, required at runtime
- `--auth`, default `none`, allowed values `none` and `password`
- `--allow-remote`, default false

Credential inputs:

- Read `TUNNELMESH_SOCKS5_USERNAME` and `TUNNELMESH_SOCKS5_PASSWORD` with `os.Getenv`.
- Do not add username/password CLI flags.
- Do not log either value.

Runtime checks:

- Require `client.server_url` and `client.token`.
- Require `--agent`.
- Construct `client.SOCKS5ForwardConfig`.
- Start the forward inside `runClientWebSocket`.
- Print the actual listener address when `--listen` uses port zero.

Register it with:

```go
forward.AddCommand(
    clientForwardCommand(opts, "tcp"),
    clientForwardCommand(opts, "udp"),
    clientForwardCommand(opts, "http"),
    clientSOCKS5ForwardCommand(opts),
)
```

- [x] **Step 4: Verify GREEN**

```bash
go test ./internal/cli -count=1
```

### Task 4: Documentation and PR Record

**Files:**

- Modify: `docs/user-guide/client.md`
- Modify: `docs/operations/config-examples.md`
- Modify: `docs/protocol/proxy-modules.md`
- Create: `docs/pull-requests/2026-09-09-client-socks5.md`

- [x] **Step 1: Write documentation**

Document:

- Command example.
- Browser and `curl --socks5-hostname` usage.
- CONNECT-only behavior.
- IPv4, IPv6, and domain target support.
- Loopback default and explicit `--allow-remote`.
- `--auth password`, RFC 1929 behavior, environment-variable credential injection, and the requirement that non-loopback listening uses password auth.
- No local DNS resolution; domain is resolved on the Agent side.
- Existing token/policy/CIDR/port authorization.
- Explicitly state that UDP ASSOCIATE, BIND, GSSAPI auth, and public Server SOCKS5 ingress are not supported.

- [x] **Step 2: Create PR record**

Create `docs/pull-requests/2026-09-09-client-socks5.md` with:

- Summary.
- Security model.
- Compatibility and limitations.
- Verification commands and results.
- Rollback: stop the new client command; no schema or protocol migration is required.

### Task 5: Full Verification

- [x] Run:

```bash
go test ./... -count=1
go test -race ./...
go vet ./...
git diff --check
```

- [x] Re-read the design and this plan.
- [x] Confirm every goal and non-goal is satisfied or explicitly deferred.
- [x] Do not commit or push without explicit user authorization.
