# Task 8 Fix 2 Review — scoped-token documentation

## Result: FAIL

The main stale wording was corrected, but one remaining sentence still tells operators that an administrator token can be used for Agent WebSocket takeover.

## Finding

### [P1] `agent.md` still permits an administrator token in the normal Agent flow

`docs/user-guide/agent.md:102` says: “管理员 token 可用于运维接管” (an administrator token can be used for operational takeover). This remains an instruction to use a management/admin token for Agent WebSocket access. The runtime's normal path validates `TokenTypeAgent`; management-token fallback is only available when `security.allow_legacy_connection_tokens` is explicitly enabled. The documented default is disabled in `internal/config/config.go:321` and the CLI default is false in `internal/cli/root.go:92`.

Required fix: remove the administrator-token sentence from the normal Agent setup section, or explicitly qualify it as a temporary migration-only path that requires `security.allow_legacy_connection_tokens: true`, emits a deprecation audit, and is scheduled for removal in v0.3.0. The surrounding `docs/user-guide/agent.md:100` and `:150` wording, plus `docs/deployment/docker.md:58`, now correctly require a bound `agent` service token.

## Checks performed

- `docs/user-guide/agent.md:100` now requires a backend-created, Agent-bound `agent` service token.
- `docs/deployment/docker.md:58` now requires a bound `agent` service token instead of an API bearer token.
- `docs/user-guide/agent.md:150` documents the legacy flag as explicit-only and temporary.
- `internal/config/config.go:321` and `internal/cli/root.go:92` confirm the legacy flag defaults to `false`.
- No code was modified; only this review report was added.
