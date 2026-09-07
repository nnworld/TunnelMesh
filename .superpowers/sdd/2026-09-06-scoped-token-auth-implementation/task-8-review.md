# Task 8 Review — Token management UI and operator docs

## Result: FAIL

The UI build and current frontend tests pass, but the review found one security-documentation defect and one user-facing error-handling defect that should be fixed before accepting Task 8.

## Findings

### [P1] Agent documentation still instructs operators to use management API tokens

`docs/user-guide/agent.md:100-102` says the Agent WebSocket uses “the same API token” and that an administrator token can be used for operational takeover. This contradicts the scoped-token architecture and the newer guidance at `docs/user-guide/agent.md:150`, which requires a bound `agent` service token; the default runtime rejects legacy management tokens unless `security.allow_legacy_connection_tokens` is explicitly enabled. An operator following the earlier section can deploy an Agent with a management login token, fail to connect by default, or enable the compatibility flag and unintentionally broaden a credential's privileges. `docs/deployment/docker.md:58` repeats the same ambiguity by asking operators to inject an “API bearer token” for Agent WebSocket access.

Required fix: replace both instructions with explicit `agent` service-token wording, and mention the legacy flag only as a temporary migration exception.

### [P2] Rotate/revoke request failures are not surfaced by the UI

`web/src/views/Tokens.vue:74-75` awaits `rotateToken`/`revokeToken` without a `try/catch` (unlike `create` at line 73). A 401/403/409/network failure therefore rejects the click handler without an Element Plus error message, leaves the table stale, and can produce an unhandled promise rejection. This is especially relevant for rotation conflicts and expired/revoked credentials, which the API documents as expected 409/404 outcomes.

Required fix: wrap confirmation plus mutation in `try/catch/finally`, display `ElMessage.error(...)`, and refresh only after a successful mutation.

## Checks performed

- `cd web && npm test -- --run`: PASS — 1 test file, 6 tests.
- `cd web && npm run build`: PASS — Vite build completed; emitted only a chunk-size warning for the ~1.07 MB JS bundle.
- Secret lifecycle inspection: raw secret is held in the `secret` ref only, shown by `TokenSecretDialog`, and cleared on dialog close and component unmount; no secret-like fixture value was found in generated assets.
- Route/API inspection: `/tokens` is authenticated; server-node controls are admin-gated in the UI and server authorization remains authoritative; create/rotate use `Idempotency-Key`; list responses render prefix/metadata only.
- Generated assets: `web/dist/index.html` and `internal/server/web_dist/index.html` reference the current build hashes. `internal/server/web_dist/assets` also contains older unreferenced bundles; this is a cleanup/size concern, not a secret leak.

## Scope and non-findings

- No raw token hash or secret is rendered in the token table, API client list path, docs, or generated assets inspected.
- Management login persistence in `localStorage` is existing session behavior, not the one-time service-token secret path.
- No code or documentation was modified during this review except this report.
