# Task 8 Fix 3 Review — scoped-token documentation

## Result: PASS

The documentation now consistently directs normal Agent WebSocket deployments to use a backend-created, Agent-bound `agent` service token. The remaining management-token mention is explicitly constrained to a temporary migration path.

## Verification

- `docs/user-guide/agent.md:100` requires a bound `agent` service token for the normal handshake.
- `docs/user-guide/agent.md:102` states that an old management token is accepted only for temporary migration when `security.allow_legacy_connection_tokens: true`; it also documents the deprecation audit and v0.3.0 removal plan.
- `docs/user-guide/agent.md:150` repeats the same legacy-only qualification.
- `docs/deployment/docker.md:58` requires injecting the bound `agent` service token and no longer says “API bearer token”.
- `internal/config/config.go:321` and `internal/cli/root.go:92` confirm the legacy flag defaults to `false`.
- `internal/server/runtime.go:256-288` confirms the fallback is guarded by the flag and emits `deprecated_connection_token` audit/log records with removal version `v0.3.0`.
- `git diff --check`: PASS.

No code or documentation was modified during this review except this report.
