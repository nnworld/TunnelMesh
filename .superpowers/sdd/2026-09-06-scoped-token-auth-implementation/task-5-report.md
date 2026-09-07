# Task 5 Report — Agent Token and WebSocket Boundary

## Status

DONE

Commits: none. No commit, push, merge, or remote mutation was performed.

## Implemented

### 1. Exact WebSocket routing boundary

- `/ws/agent` is the only route dispatched to the Agent WebSocket handler.
- `/ws/client` is reserved for Task 6 and currently returns 404.
- Unknown `/ws/*` paths and `/ws/agent/...` return 404 and cannot fall through to the Agent handler or SPA.
- `/api/*` continues to take precedence over the SPA fallback.

### 2. Scoped Agent Token authentication

- `ServerRuntime` owns an `auth.CredentialService` and validates every Agent connection with `ValidateAs(ctx, raw, storage.TokenTypeAgent)`.
- Agent Hello `AgentID` must exactly match `TokenIdentity.AgentID`.
- Missing credentials, management API Tokens, Client Tokens, wrong Agent bindings, disabled Agents, revoked Agent Tokens, and expired Agent Tokens fail closed.
- Only `Authorization: Bearer ...` is read. Query-string and Cookie-only credentials are rejected.
- `AgentRegistration` now carries the non-secret `TokenID`; raw bearer Tokens are no longer retained in registration/session state.
- Existing `AgentSessionConfig.Authenticate` remains an optional additional session policy after the mandatory scoped-token check.
- `ServerRuntime.Close()` closes the bounded CredentialService last-used worker; the server CLI and runtime tests close it explicitly.

### 3. Deprecated legacy migration switch

- Added `security.allow_legacy_connection_tokens`, default `false`.
- The config field and code path are marked Deprecated with the v0.3.0 removal note.
- When explicitly enabled, a legacy management Token is accepted only after the previous current-user/admin ownership rule and enabled-Agent check.
- Successful legacy use writes both:
  - structured warning `deprecated_connection_token`;
  - audit action `deprecated_connection_token` with user/Agent identity and v0.3.0 removal metadata.
- Warning/audit tests verify the raw management Token is not recorded.

### 4. Host and Origin allowlists

- Added `security.allowed_hosts` and `security.allowed_origins`.
- An empty Host allowlist skips Host membership filtering. An empty Origin allowlist skips Origin membership filtering but still requires exactly one strictly valid absolute `http`/`https` Origin with no userinfo, path, query, or fragment.
- Once configured, Host and Origin matching is exact after safe case normalization.
- Host validation rejects empty values, userinfo delimiters, illegal characters, invalid/non-numeric ports, out-of-range ports, malformed IPv6, and invalid DNS labels.
- Origin validation accepts only one absolute `http`/`https` origin with no userinfo, path, query, or fragment.
- Agent dialer Origin derived from `ws://`/`wss://` Server URL is accepted by the configured Server allowlist in a real Agent-to-Server test.

### 5. Optional native TLS

- Added top-level `tls.enabled`, `tls.cert_file`, `tls.key_file`, and `tls.min_version`.
- Native TLS defaults to disabled; minimum version defaults to `1.2`.
- When enabled, only `1.2` and `1.3` are accepted.
- Certificate/private-key pairs are loaded synchronously before HTTP serving; missing or mismatched files fail fast and close the listener, with no plaintext fallback.
- Enabled mode wraps the existing listener with `tls.NewListener` and a `tls.Config.MinVersion` of TLS 1.2 or TLS 1.3.
- Disabled mode returns the original internal HTTP listener unchanged for Nginx termination.
- Tests cover a real certificate, TLS 1.2 success, TLS 1.1 rejection, TLS 1.3 minimum behavior, plaintext rejection, pair mismatch, and disabled TLS.
- Agent WebSocket dialing still uses normal platform certificate verification; no skip-verify option was added.

### 6. Configuration binding and validation

- File, environment, generic CLI override, and actual Cobra CLI flags are bound with the existing precedence: CLI > env > file > default.
- Environment allowlist values support comma-separated entries through Viper decoding.
- TLS private-key paths are redacted by `RedactedJSON`; Agent Tokens remain excluded by JSON/YAML tags.
- Agent/Client Server URLs, when set, must be absolute `ws://` or `wss://` URLs.
- Fixed an existing Agent dialer defect where an invalid WebSocket URL error was ignored and caused a nil-pointer panic.

## TDD RED Evidence

Each production behavior was driven by a test that failed for the missing behavior before the minimal implementation was added.

### Exact route boundary

Command:

```text
go test ./internal/server -run 'TestAgentWebSocketRouteIsExact' -count=1
```

Expected RED observed:

```text
GET /ws/client status = 204, want 404
GET /ws/agent/other status = 204, want 404
GET /ws/unknown status = 204, want 404
FAIL github.com/tunnelmesh/tunnelmesh/internal/server
```

### Security/TLS configuration model

Command:

```text
go test ./internal/config -run 'Security|NativeTLS' -count=1
```

Expected RED observed:

```text
internal/config/config_test.go:170:9: cfg.Security undefined
internal/config/config_test.go:173:9: cfg.TLS undefined
FAIL github.com/tunnelmesh/tunnelmesh/internal/config [build failed]
```

### Cobra CLI binding

Command:

```text
go test ./internal/cli -run 'SecurityAndNativeTLSFlags' -count=1
```

Expected RED observed:

```text
Execute() error = unknown flag: --security.allowed_hosts
FAIL github.com/tunnelmesh/tunnelmesh/internal/cli
```

### Scoped Agent Token identity and registration TokenID

Command:

```text
go test ./internal/server -run 'TestAgentTokenAuthenticationBoundaries' -count=1
```

Expected RED observed:

```text
registration.TokenID undefined (type AgentRegistration has no field or method TokenID)
FAIL github.com/tunnelmesh/tunnelmesh/internal/server [build failed]
```

The completed test then exercised real SQLite/CredentialService/HTTP/WebSocket behavior for missing, management, Client, wrong binding, disabled, revoked, expired, query, Cookie, and valid Agent Tokens.

### Host/Origin runtime configuration

Command:

```text
go test ./internal/server -run 'TestAgentWebSocketHostAndOriginAllowlist' -count=1
```

Expected RED observed:

```text
undefined: RuntimeConfig
too many arguments in call to NewServerRuntime
FAIL github.com/tunnelmesh/tunnelmesh/internal/server [build failed]
```

### Legacy migration switch

Command:

```text
go test ./internal/server -run 'TestAgentLegacyManagementTokenRequiresMigrationFlagAndAuditsWarning' -count=1
```

Expected RED observed:

```text
legacy management token was rejected while migration flag was enabled
FAIL github.com/tunnelmesh/tunnelmesh/internal/server
```

The owner/enabled checks were separately mutation-checked by temporarily removing them; the test failed with:

```text
legacy management token from another owner was accepted
```

### Native TLS listener

Command:

```text
go test ./internal/server -run 'NativeTLS' -count=1
```

Expected RED observed:

```text
undefined: nativeTLSListener
FAIL github.com/tunnelmesh/tunnelmesh/internal/server [build failed]
```

TLS 1.3 mapping was separately driven RED:

```text
go test ./internal/server -run 'TestNativeTLS13RejectsTLS12' -count=1
native TLS minimum version must be 1.2 or 1.3
FAIL github.com/tunnelmesh/tunnelmesh/internal/server
```

### CLI TLS fail-fast wiring

Command:

```text
go test ./internal/cli -run 'TestServerRunFailsFastWhenNativeTLSCertificateCannotLoad' -count=1
```

Expected RED observed:

```text
Execute() error = <nil>, want native TLS certificate load failure
FAIL github.com/tunnelmesh/tunnelmesh/internal/cli
```

### Invalid Host port

Command:

```text
go test ./internal/config -run 'TestValidateRejectsUnsafeSecurityAndNativeTLSConfiguration/host_with_invalid_port' -count=1
```

Expected RED observed:

```text
Validate() error = <nil>, want "allowed host"
FAIL github.com/tunnelmesh/tunnelmesh/internal/config
```

### Agent URL error handling

Command:

```text
go test ./internal/agent -run 'TestDialWebSocketRejectsNonWebSocketURL' -count=1
```

Expected RED observed:

```text
panic: runtime error: invalid memory address or nil pointer dereference
github.com/tunnelmesh/tunnelmesh/internal/agent.originFor
github.com/tunnelmesh/tunnelmesh/internal/agent.DialWebSocket
FAIL github.com/tunnelmesh/tunnelmesh/internal/agent
```

## GREEN Evidence

Brief focused command:

```text
go test ./internal/server -run 'Agent.*Token|Origin|Host' -count=1
ok github.com/tunnelmesh/tunnelmesh/internal/server 1.245s
```

Brief affected-package command:

```text
go test ./internal/server ./internal/agent -run 'Agent|Token|Origin|Host|NativeTLS' -count=1
ok github.com/tunnelmesh/tunnelmesh/internal/server 6.296s
ok github.com/tunnelmesh/tunnelmesh/internal/agent 0.556s [no tests to run]
```

Affected packages:

```text
go test ./internal/server ./internal/agent ./internal/config ./internal/cli -count=1
ok github.com/tunnelmesh/tunnelmesh/internal/server 7.362s
ok github.com/tunnelmesh/tunnelmesh/internal/agent 0.884s
ok github.com/tunnelmesh/tunnelmesh/internal/config 0.718s
ok github.com/tunnelmesh/tunnelmesh/internal/cli 1.130s
```

Final full repository test, run after the last code change:

```text
go test ./... -count=1
ok github.com/tunnelmesh/tunnelmesh/internal/agent 0.283s
ok github.com/tunnelmesh/tunnelmesh/internal/auth 1.013s
ok github.com/tunnelmesh/tunnelmesh/internal/build 0.466s
ok github.com/tunnelmesh/tunnelmesh/internal/cli 0.612s
ok github.com/tunnelmesh/tunnelmesh/internal/client 0.770s
ok github.com/tunnelmesh/tunnelmesh/internal/config 0.877s
ok github.com/tunnelmesh/tunnelmesh/internal/e2e 0.950s
ok github.com/tunnelmesh/tunnelmesh/internal/protocol 1.061s
ok github.com/tunnelmesh/tunnelmesh/internal/registry 1.329s
ok github.com/tunnelmesh/tunnelmesh/internal/relay 1.338s
ok github.com/tunnelmesh/tunnelmesh/internal/routing 1.157s
ok github.com/tunnelmesh/tunnelmesh/internal/server 8.114s
ok github.com/tunnelmesh/tunnelmesh/internal/storage 1.378s
```

## Race Evidence

Brief affected-package race:

```text
go test -race ./internal/server ./internal/agent
ok github.com/tunnelmesh/tunnelmesh/internal/server 71.835s
ok github.com/tunnelmesh/tunnelmesh/internal/agent 1.661s
```

Fresh final full repository race, with cache disabled:

```text
go test -race ./... -count=1
ok github.com/tunnelmesh/tunnelmesh/internal/agent 1.322s
ok github.com/tunnelmesh/tunnelmesh/internal/auth 7.891s
ok github.com/tunnelmesh/tunnelmesh/internal/build 1.403s
ok github.com/tunnelmesh/tunnelmesh/internal/cli 1.767s
ok github.com/tunnelmesh/tunnelmesh/internal/client 1.957s
ok github.com/tunnelmesh/tunnelmesh/internal/config 2.002s
ok github.com/tunnelmesh/tunnelmesh/internal/e2e 2.218s
ok github.com/tunnelmesh/tunnelmesh/internal/protocol 2.392s
ok github.com/tunnelmesh/tunnelmesh/internal/registry 2.130s
ok github.com/tunnelmesh/tunnelmesh/internal/relay 1.890s
ok github.com/tunnelmesh/tunnelmesh/internal/routing 1.766s
ok github.com/tunnelmesh/tunnelmesh/internal/server 73.408s
ok github.com/tunnelmesh/tunnelmesh/internal/storage 2.497s
```

## Static and Diff Checks

```text
go vet ./...
exit 0

git diff --check
exit 0
```

## Files Changed

Brief-listed files:

- `internal/config/config.go`
- `internal/config/config_test.go`
- `internal/server/tls_listener.go` (new)
- `internal/server/tls_listener_test.go` (new)
- `internal/server/runtime.go`
- `internal/server/runtime_test.go`
- `internal/server/web.go`
- `internal/server/middleware.go`
- `internal/server/session_manager.go`
- `internal/agent/websocket.go`
- `internal/agent/session_test.go`

Minimal files outside the brief list:

- `internal/cli/root.go`
- `internal/cli/root_test.go`

Reason: the brief explicitly requires actual CLI binding, precedence, Runtime wiring, and TLS certificate fail-fast behavior. Editing only `internal/config` would make generic override maps work but would leave the shipped Cobra commands unable to set or apply the new security/TLS options.

Report artifact:

- `.superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-5-report.md`

## Self-review

- Compared all overlapping brief files against `task-5-before`; pre-existing heartbeat and metadata changes remain intact.
- Confirmed no compatibility path for `/ws/agent/v1` was added.
- Confirmed `/ws/client` and unknown `/ws/*` do not enter the Agent handler.
- Confirmed management API authentication was not changed and still uses `api_tokens`.
- Confirmed service Token authentication reads current DB state through `CredentialService.ValidateAs` on every connection.
- Confirmed Agent identity binding is exact and raw Tokens are not copied into `AgentRegistration`, logs, or audits.
- Confirmed legacy mode defaults off and retains owner/admin plus enabled-Agent checks.
- Confirmed configured Host/Origin values are exact allowlists; an empty Origin list still enforces strict Origin syntax while preserving development/Nginx connections with valid generated Origins.
- Confirmed TLS errors close the listener and never start a plaintext fallback.
- Confirmed no `InsecureSkipVerify` or skip-verify flag was introduced.
- Confirmed Runtime-owned CredentialService worker is closed by CLI and tests.
- Confirmed no secrets, `.env`, generated certificates, test databases, or build artifacts were added to the repository.

## Concerns

No blocking Task 5 concerns.

Expected follow-on scope remains unchanged:

- Task 6 will implement `/ws/client`; it is intentionally 404 in this task.
- Task 8 will migrate documentation from `/ws/agent/v1` to `/ws/agent` and document explicit production Host/Origin allowlists and native TLS deployment examples.
- MySQL-specific execution was not required for this boundary task; the repository's full storage test suite passed without requiring an external DSN.

## Commits

none

---

## Fix Round 1 — Pre-upgrade Authentication and First-Hello Bounds

### Status

DONE

Commits: none. No commit, push, merge, or remote mutation was performed.

### Review findings fixed

- Moved credential validation before the HTTP 101 response. The WebSocket handshake now accepts only a current `TokenTypeAgent` credential, or a valid management principal when the explicit deprecated legacy flag is enabled.
- The successful handshake stores only non-secret `TokenIdentity`/`Principal` data in request context and removes the `Authorization` header before the upgraded handler runs. The post-upgrade path never rereads or revalidates a raw credential.
- Kept Hello-dependent authorization after frame parsing: scoped Agent identities must exactly match `hello.AgentID`; legacy principals must still pass enabled-Agent and owner/admin checks before warning/audit emission.
- Added a one-second first-Hello read deadline; it is cleared only after a syntactically and semantically valid Agent Hello.
- Set the x/net WebSocket receive limit to `protocol.MaxPayload + 16`, covering the maximum protocol payload plus its fixed wire header, before reading the first frame.
- Removed the permissive `websocket.Origin` fallback. Even with no configured Origin allowlist, the handshake requires exactly one Origin normalized by the existing strict validator.

### Test files

- `internal/server/runtime_test.go`
  - `TestAgentInvalidCredentialRejectedBeforeUpgrade`
  - `TestAgentWebSocketStrictOriginWithoutAllowlist`
  - `TestAgentWebSocketClosesConnectionWhenHelloTimesOut`
  - `TestAgentWebSocketRejectsOversizedFirstFrameBeforeReadingPayload`
  - Added pre-upgrade invalid/valid credential assertions to the legacy migration test.
  - Updated older tests whose previous contract expected invalid management/Agent credentials to receive HTTP 101 before rejection.

### RED evidence

Focused command after adding the new tests and before production changes:

```text
go test ./internal/server -run 'Agent.*(Token|Hello)|Origin|Host|InvalidCredential|OversizedFirstFrame' -count=1
```

Observed failures:

```text
TestAgentInvalidCredentialRejectedBeforeUpgrade:
  arbitrary token status = 101, want 403
  Client token status = 101, want 403
  revoked Agent token status = 101, want 403
  management token with legacy disabled status = 101, want 403
TestAgentWebSocketStrictOriginWithoutAllowlist:
  ftp/path/query Origins returned 101, want 403
TestAgentWebSocketClosesConnectionWhenHelloTimesOut:
  closed after 3.000421291s, want before 2s
TestAgentWebSocketRejectsOversizedFirstFrameBeforeReadingPayload:
  client read timed out while the server waited for the declared payload
FAIL github.com/tunnelmesh/tunnelmesh/internal/server
```

Legacy pre-upgrade RED was also observed independently:

```text
go test ./internal/server -run 'TestAgentLegacyManagementTokenRequiresMigrationFlagAndAuditsWarning' -count=1
invalid legacy credential handshake status = 101, want 403
FAIL github.com/tunnelmesh/tunnelmesh/internal/server
```

### GREEN evidence

Focused security boundary command:

```text
go test ./internal/server -run 'Agent.*(Token|Hello)|Origin|Host|InvalidCredential|OversizedFirstFrame' -count=1
ok github.com/tunnelmesh/tunnelmesh/internal/server 2.665s
```

Brief affected-package command:

```text
go test ./internal/server ./internal/agent -run 'Agent|Token|Origin|Host|NativeTLS' -count=1
ok github.com/tunnelmesh/tunnelmesh/internal/server 8.118s
ok github.com/tunnelmesh/tunnelmesh/internal/agent 0.266s [no tests to run]
```

### Race and full verification evidence

Affected packages with the race detector:

```text
go test -race ./internal/server ./internal/agent
ok github.com/tunnelmesh/tunnelmesh/internal/server 79.341s
ok github.com/tunnelmesh/tunnelmesh/internal/agent (cached)
```

Fresh full repository tests:

```text
go test ./... -count=1
ok github.com/tunnelmesh/tunnelmesh/internal/agent 0.336s
ok github.com/tunnelmesh/tunnelmesh/internal/auth 1.022s
ok github.com/tunnelmesh/tunnelmesh/internal/build 0.660s
ok github.com/tunnelmesh/tunnelmesh/internal/cli 0.944s
ok github.com/tunnelmesh/tunnelmesh/internal/client 0.644s
ok github.com/tunnelmesh/tunnelmesh/internal/config 1.214s
ok github.com/tunnelmesh/tunnelmesh/internal/e2e 1.785s
ok github.com/tunnelmesh/tunnelmesh/internal/protocol 1.487s
ok github.com/tunnelmesh/tunnelmesh/internal/registry 2.052s
ok github.com/tunnelmesh/tunnelmesh/internal/relay 2.027s
ok github.com/tunnelmesh/tunnelmesh/internal/routing 1.986s
ok github.com/tunnelmesh/tunnelmesh/internal/server 11.097s
ok github.com/tunnelmesh/tunnelmesh/internal/storage 2.240s
```

Fresh full repository race run:

```text
go test -race ./... -count=1
ok github.com/tunnelmesh/tunnelmesh/internal/agent 1.355s
ok github.com/tunnelmesh/tunnelmesh/internal/auth 8.670s
ok github.com/tunnelmesh/tunnelmesh/internal/build 1.682s
ok github.com/tunnelmesh/tunnelmesh/internal/cli 1.666s
ok github.com/tunnelmesh/tunnelmesh/internal/client 1.875s
ok github.com/tunnelmesh/tunnelmesh/internal/config 2.709s
ok github.com/tunnelmesh/tunnelmesh/internal/e2e 2.182s
ok github.com/tunnelmesh/tunnelmesh/internal/protocol 2.430s
ok github.com/tunnelmesh/tunnelmesh/internal/registry 2.351s
ok github.com/tunnelmesh/tunnelmesh/internal/relay 2.029s
ok github.com/tunnelmesh/tunnelmesh/internal/routing 1.898s
ok github.com/tunnelmesh/tunnelmesh/internal/server 119.614s
ok github.com/tunnelmesh/tunnelmesh/internal/storage 2.629s
```

Static and whitespace checks:

```text
go vet ./...
exit 0

git diff --check
exit 0
```

### Self-review

- Verified x/net/websocket passes the same `*http.Request` pointer from its handshake callback into `Conn.Request()`, so replacing the request value with a context-bearing copy reliably transfers the preauthenticated identity.
- Verified the `Authorization` header is cloned and deleted before the upgraded handler receives the request; neither registration, log, audit, nor context contains the raw secret.
- Verified service-token success never enters the legacy branch. Legacy fallback occurs only after Agent-token validation fails and only when `AllowLegacyConnectionTokens` is true.
- Verified wrong Agent binding still fails after Hello, because the Agent ID is not available during the HTTP handshake.
- Verified the Hello deadline is cleared after valid metadata decoding/required-field validation, so established sessions do not inherit the one-second startup deadline.
- Verified the receive limit is installed before the first `Receive`, causing x/net/websocket to reject an oversized declared length without reading or allocating the body.
- Verified the existing heartbeat and metadata session paths were not modified by this fix round.
- The code-review skill normally requests a reviewer subagent, but this task explicitly prohibited subagent creation; this round therefore used local line-by-line diff and dependency-source review.

### Concerns

No blocking Fix Round 1 concerns.

The one-second Hello deadline is intentionally narrow per the review requirement. If production telemetry later shows legitimate high-latency TLS/WebSocket clients exceeding it, expose a bounded configuration value in a separate reviewed change rather than weakening the default silently.

### Commits

none
