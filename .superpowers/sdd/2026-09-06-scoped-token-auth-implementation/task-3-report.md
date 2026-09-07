# Task 3 Report: CredentialService lifecycle and scope authorization

## Status

Implemented Task 3 without commit, push, merge, or sub-agent delegation.

## Implementation

- Added `CredentialService` lifecycle APIs for create, typed validation, rotation, revocation, and per-stream authorization.
- Added `TokenScope`, `TokenIdentity`, `CreateTokenInput`, `CreatedToken`, and `StreamAuthorizationRequest` domain types.
- Generated bearer secrets from exactly 32 cryptographically random bytes with raw URL-safe base64 encoding.
- Persisted only SHA-256 hashes, a display prefix derived from the hash, normalized scope JSON, bindings, and timestamps. The raw secret is returned only by create/rotate.
- Kept `AuthService.ValidateToken` unchanged in behavior and restricted to the existing `api_tokens` repository.
- Validated token type and binding rules, expiration, revocation, owner status, Agent enabled/ownership state, and Server-node existence/expiry. Missing resources and repository errors fail closed during authentication.
- Re-loaded the authoritative token record and scope on every `AuthorizeStream` call, then intersected it with current owner/Agent state and the complete cursor-paginated Agent Policy set. Repository errors fail closed.
- Added a single bounded last-used worker (queue capacity 128) with non-blocking enqueue, cancellation, close/wait lifecycle, and a two-second write timeout. Saturated updates are intentionally dropped because `last_used_at` is best-effort.
- Normalized supported protocols (`tcp`, `udp`, `http`, `ws`; `websocket` aliases to `ws`), CIDRs, ports, and Agent IDs before persistence.

## TDD evidence

### RED

Command:

```text
go test ./internal/auth -run 'Credential|Scope' -count=1
```

Key output before production implementation:

```text
internal/auth/credential_service_test.go:25:8: undefined: CreateTokenInput
internal/auth/credential_service_test.go:296:70: undefined: CredentialService
FAIL github.com/tunnelmesh/tunnelmesh/internal/auth [build failed]
```

The failure was expected because the CredentialService API and domain types did not exist.

### GREEN

```text
go test ./internal/auth -run 'Credential|Scope' -count=1
ok github.com/tunnelmesh/tunnelmesh/internal/auth 0.700s

go test -race ./internal/auth
ok github.com/tunnelmesh/tunnelmesh/internal/auth 7.491s
```

## Full verification

All commands exited 0:

```text
go test ./... -count=1
go test -race ./...
go vet ./...
git diff --check
```

The full test run passed every package, including `internal/auth`, `internal/server`, `internal/storage`, and `internal/e2e`.

## Modified files

- `internal/auth/credential_service.go` (new)
- `internal/auth/credential_service_test.go` (new)
- `internal/auth/service.go`
- `internal/auth/password.go`
- `.superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-3-report.md` (this report)

## Self-review

- Authentication never converts repository failures into a principal; authorization never converts them into permission.
- Authorization ignores the caller's mutable scope copy and uses the freshly loaded persisted scope.
- Client Token scope can only narrow access; an Agent Policy match is always additionally required.
- Policy pagination is exhausted before denial, and malformed pagination fails closed.
- No raw secret is written to repository fields; the display prefix is derived from the hash rather than raw secret bytes.
- The last-used path starts one worker per service, not one goroutine per authentication, and closes without closing a channel that concurrent validators may still select on.
- Existing unrelated worktree changes were preserved.

## Risks / follow-up

- `last_used_at` is intentionally best-effort: queued updates may be dropped under sustained saturation or service shutdown.
- `ServerNode` has no explicit enabled flag; availability is therefore checked using existence and a non-expired `ExpiresAt` when present.
- Agent Policy evaluation follows the current model: protocol, target host/port, and optional CIDR/port allowlists must all match one policy record.

## Commits

None (not authorized).

---

## Fix round 1/5: strict Policy records and bounded scope parsing

### Review findings addressed

1. Agent Policy records no longer treat empty `AgentID`, empty `TargetHost`, or zero `TargetPort` as wildcards. A usable record must have an Agent ID equal to the request, a non-empty target host of at most 255 bytes equal to the request, a target port in `1..65535` equal to the request, and a valid matching protocol. All loaded policy pages are scanned so a later malformed record cannot be hidden by an earlier match.
2. Token scope now enforces: serialized JSON at most 16 KiB, at most 128 Agent IDs, each Agent ID at most 255 bytes, at most 4 protocols, at most 128 CIDRs, and at most 1024 ports. Creation returns `ErrInvalidTokenScope`; authentication and stream authorization reject malformed/oversized persisted scope.
3. Authorization compiles the persisted scope once per call into Agent/protocol/port sets and parsed CIDR networks. `scopeAllows` reuses the compiled networks and performs no CIDR parsing.
4. Agent Policy CIDR and port lists are limited to 16 KiB and the corresponding 128/1024 entry bounds. Port ranges are represented as intervals, so `1-65535` requires one interval rather than allocating 65,535 integers.

### RED evidence

Focused command before implementation:

```text
go test ./internal/auth -run 'Credential(PolicyMalformed|ScopeCreation|PersistedScope|PolicyListParsing)' -count=1
```

Key expected failures:

```text
empty_agent: AuthorizeStream() error = <nil>, want ErrForbidden
empty_target_host: AuthorizeStream() error = <nil>, want ErrForbidden
zero_target_port: AuthorizeStream() error = <nil>, want ErrForbidden
too_many_agent_ids: Create() error = <nil>, want ErrInvalidTokenScope
serialized_scope_too_large: ValidateAs() error = <nil>, want ErrUnauthenticated
too_many_policy_CIDRs: AuthorizeStream() error = <nil>, want ErrForbidden
FAIL github.com/tunnelmesh/tunnelmesh/internal/auth
```

The complete-policy-scan follow-up test also failed before its fix:

```text
go test ./internal/auth -run 'CredentialPolicyMalformedBindingsFailClosed/malformed_record_after' -count=1
AuthorizeStream() error = <nil>, want ErrForbidden when any loaded policy is malformed
FAIL github.com/tunnelmesh/tunnelmesh/internal/auth
```

### GREEN and verification evidence

Final commands and key output:

```text
go test ./internal/auth -run 'Credential|Scope' -count=1
ok github.com/tunnelmesh/tunnelmesh/internal/auth 0.810s

go test -race ./internal/auth
ok github.com/tunnelmesh/tunnelmesh/internal/auth 7.498s

go test ./internal/auth -count=1
ok github.com/tunnelmesh/tunnelmesh/internal/auth 0.763s

go vet ./internal/auth
git diff --check
# both exited 0 with no output
```

The first race run exposed unsynchronized direct map access in two tests while the last-used worker updated the fake repository. The test fake now provides a lock-protected mutation helper; the fresh race run above is clean. Production concurrency behavior was unchanged.

### Files changed in this round

- `internal/auth/credential_service.go`
- `internal/auth/credential_service_test.go`
- `.superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-3-report.md`

### Commits

None (not authorized).
