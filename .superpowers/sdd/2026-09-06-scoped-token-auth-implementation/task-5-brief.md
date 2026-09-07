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

