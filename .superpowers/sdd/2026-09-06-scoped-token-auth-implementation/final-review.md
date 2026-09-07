# Scoped Token Auth Whole-Branch Final Review

## Result: FAIL

The reviewed branch has the expected credential boundaries and the documented legacy-token exception is now correctly gated. However, one high-confidence specification regression remains in the connection liveness behavior.

## Findings

### [P2] Agent heartbeat default is 60s while the approved security specification requires 30s

- `docs/superpowers/specs/2026-09-06-security-observability-release-design.md:71` requires Agent/Client protocol `PING` every 30 seconds.
- `internal/agent/websocket.go:80-82` defaults `HeartbeatInterval` to `time.Minute` when the option is unset.
- `internal/server/runtime_test.go` and `docs/user-guide/agent.md:103` describe the resulting one-minute heartbeat/lease behavior.

This is not a credential bypass, but it violates the approved liveness contract and delays detection/recovery of a dead Agent by up to an additional heartbeat period. Align the default to 30 seconds (or amend the approved specification and associated lease/timeout documentation together).

## Security and architecture checks

- Agent, Client, and Server-node credentials are type-separated; management API authentication remains on `api_tokens`.
- Agent and Client WebSockets require bearer Authorization, reject query/cookie token transport, and apply type/resource checks before session handling.
- Client OPEN requests are re-authorized through `StreamAuthorizer` before relay open; target identity is not taken from untrusted server-node payload fields.
- Server-node relay requires mTLS, exact SAN matching, node-bound service token, and current epoch.
- Service-token API views/audits/idempotency replay metadata omit raw secrets and stored hashes; one-time secrets remain component-memory-only in the UI.
- Handler → Service → Repository separation is maintained for token lifecycle paths; transaction-bound authorization reads and mutation repositories are used.
- Agent documentation and Docker guidance now require bound `agent` service tokens. Legacy management-token use is explicitly limited to `security.allow_legacy_connection_tokens: true`, emits deprecation audit/log records, and is scheduled for v0.3.0 removal.

## Verification evidence

- Existing ledger records full Go/race/vet/frontend gates and focused security/E2E reruns as passing.
- This review rechecked `progress.md`, the security specification, implementation plan, current working-tree diff, token/WS/relay/auth code, docs, and generated web assets.
- `git diff --check`: PASS.
- MySQL live verification remains environment-dependent because `TUNNELMESH_TEST_MYSQL_DSN` is unavailable; the ledger records this deferred risk.

## Scope

No production code or documentation was modified during this review; only this report was added.
