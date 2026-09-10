# SDD ledger — plan: docs/superpowers/plans/2026-09-09-cross-server-agent-connection-management.md

## Preflight scan

| Tasks / item | Shared file or interface | Finding | Ruling |
|---|---|---|---|
| Task 1 → Task 2 | `internal/server/session_manager.go` | Task 1 adds heartbeat callback plumbing; Task 2 adds exact-fenced close. They are compatible but must run sequentially to avoid edit conflicts. | Execute sequentially; Task 2 consumes the manager only after Task 1 stabilizes it. |
| Task 1 → Task 3 | `internal/server/runtime.go` | Task 1 wires lease lifecycle; Task 3 wires the relay close handler. Both touch runtime construction but responsibilities do not overlap. | Execute sequentially; Task 3 must not bypass the lease controller. |
| Task 3 → Task 4 | `relay.CloseAgentConnection` | Task 4 depends on the unary RPC and exact request type produced by Task 3. | Execute sequentially and verify the interface before Task 4. |
| Task 4 → Task 5 | Agent connection API | Task 5 consumes response fields and close semantics produced by Task 4. | Execute sequentially; frontend must not invent fields. |
| Task 4 → Task 6 | `docs/api/openapi.yaml` | Task 4 updates OpenAPI while Task 6 performs final documentation verification. | Task 4 owns API contract; Task 6 only verifies and complements operations docs. |
| Task 1 | Registry failure semantics | The design says registration failure rejects a WebSocket. This is stricter than current behavior but required for cluster manageability. | Follow the spec; add a test for rejection. |
| Task 3 | Relay control security | A new unary RPC expands the server-node control surface. | Require the same authenticated server-node identity and mTLS as existing relay streams. |
| Task 6 | Generated asset replacement | The plan includes deleting `internal/server/web_dist` before copying build output. | Treat it as a scoped generated-asset refresh only after confirming the directory contains generated assets; no broad deletion. |
| All tasks | Git operations | Project rules and the user's instruction forbid automatic commit/push/merge. | Subagents must not commit; reviews use working-tree snapshot diffs rather than commit ranges. |

Ruling: Execute directly in `/opt/app/workspace/TunnelMesh` rather than creating a worktree — the user explicitly requested no worktree; cost if wrong is potential concurrent edits, mitigated by sequential task dispatch.

Ruling: Subagent execution was unavailable because dispatched agents could not receive task context and explicit model overrides were rejected by the platform — proceed with local implementation while preserving the plan's TDD, review, and verification gates; cost if wrong is less independent review, mitigated by explicit self-review and full test runs.

Ruling: `NodeRegistry` generates its own lease connection epoch, which is intentionally distinct from the Agent protocol connection epoch — later close operations must fence the lease with the registry epoch and the local WebSocket with the session epoch; cost if wrong is closing the wrong incarnation.

Ruling: An unset runtime node ID defaults to `local` only for existing local-mode and embedder behavior; relay-enabled cluster runtimes already fail when node identity is missing — cost if wrong is a lease collision in an invalid configuration, prevented by the existing relay prerequisite check.

## Task status

- [x] Task 1: Agent connection lease lifecycle
- [x] Task 2: Exact-fenced local connection close
- [x] Task 3: Authenticated relay control RPC
- [x] Task 4: Cluster-wide query and close API
- [x] Task 5: Admin UI
- [x] Task 6: Documentation, embed sync, and full verification

## Final review checklist

- [x] Registry registration, renewal, statistics, and release are wired into the real Agent WebSocket lifecycle.
- [x] Query merges local and remote state without leaking another owner's Agent.
- [x] Close is exact-fenced by connection ID and connection epoch.
- [x] Remote close uses authenticated mTLS and does not expose a public unauthenticated control endpoint.
- [x] Unreachable owner returns `503` and preserves the lease.
- [x] UI supports query, refresh, and close in Chinese and English.
- [x] OpenAPI, user guide, operations guide, troubleshooting guide, and embedded assets are updated.
- [x] PR description is present under `docs/pull-requests/` and contains the required integration and rollback information.
- [x] All required Go and frontend validation commands pass.
