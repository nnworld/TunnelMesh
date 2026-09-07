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

