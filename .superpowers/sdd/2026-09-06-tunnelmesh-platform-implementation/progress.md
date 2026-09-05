# SDD ledger — plan: docs/superpowers/plans/2026-09-06-tunnelmesh-platform-implementation.md

## Pre-flight scan

### Shared files and interfaces

| Tasks | Shared surface | Finding / ruling |
|---|---|---|
| 1 -> 2 | cmd/*/main.go, CLI roots | Task 1 creates entrypoints; Task 2 adds Cobra/config wiring. Ruling: Task 1 keeps entrypoints minimal so Task 2 owns flags. |
| 2 -> 4 | internal/cli/root.go | Task 2 creates roots; Task 4 adds admin recovery command. Ruling: Task 4 extends the root through a command registration interface. |
| 2 -> 3 | Config storage settings | Task 2 defines typed storage settings; Task 3 consumes them. Ruling: storage accepts already-validated config and owns DSN opening. |
| 3 -> 4 | User/Token/Audit repositories | Task 3 produces repositories; Task 4 consumes them. Ruling: auth never reaches SQL directly. |
| 3 -> 6 | Lease/Node repositories | Task 3 produces persistence interfaces; Task 6 implements registry adapters. Ruling: registry database adapter is the only lease SQL caller. |
| 5 -> 7 | Frame/stream APIs | Task 5 defines protocol primitives; Task 7 consumes them. Ruling: Task 7 does not alter wire layout. |
| 5 -> 8 | TCP/UDP stream semantics | Task 8 uses TCP stream state for Bridge; UDP remains unsupported in public Bridge. Ruling: Bridge is one stream per WS. |
| 5 -> 9 | Client stream APIs | Task 9 consumes OPEN_STREAM and UDP association primitives. Ruling: Client uses protocol package, not server internals. |
| 6 -> 7 | NodeRegistry/NodeOwner | Task 7 resolves Agent ownership through the interface. Ruling: relay rejects stale epoch before opening a stream. |
| 7 -> 8 | Agent session and relay | Task 8 opens target streams through session manager/relay. Ruling: routing depends on an abstract StreamOpener. |
| 7 -> 9 | Client session | Task 9 uses ClientSessionManager for forward operations. Ruling: listeners own local sockets; sessions own WS streams. |
| 7 -> 10 | Server session/API services | Task 10 API creates persisted config only; Task 7 owns live session state. |
| 8 -> 10 | Route model and validation | Task 8 defines resolver semantics; Task 10 calls it for CRUD validation. |
| 8 -> 11 | Public listeners | Task 11 documents and packages the 80/443 endpoints implemented by Task 8. |
| 10 -> 12 | API and web assets | Task 12 smoke tests the API and embedded SPA produced by Task 10. |
| 11 -> 12 | Docker/health/Compose | Task 12 runs the packaged environments; Task 11 owns image and health definitions. |

### Per-task self-consistency

| Task | Finding / ruling |
|---|---|
| 1 | Tests, files, and build skeleton agree; no implementation dependency is missing. |
| 2 | Defaults, precedence, validation, and CLI commands are tested by the files named. |
| 3 | Contract tests cover every repository interface and both database drivers. |
| 4 | Bootstrap and recovery tests match the service methods and CLI command. |
| 5 | Frame, stream, UDP, race, and fuzz tests match the protocol deliverables. |
| 6 | Contract tests cover both database and etcd registry implementations. |
| 7 | Session and relay tests cover local and cross-node behavior specified by the task. |
| 8 | Routing and Bridge tests cover all route precedence and TCP-over-WS edge cases. |
| 9 | Listener and stdio tests cover every client command family named. |
| 10 | API and frontend files cover RBAC, routes, idempotency, and embedded assets. |
| 11 | Health, Docker, Compose, and operations docs are all explicitly tested or verified. |
| 12 | E2E scenarios cover local, MySQL, etcd, SSH Bridge, and CI gates. |

### Rulings

- Ruling: Keep public UDP unsupported even though the shared protocol supports UDP; this follows the approved spec and avoids opening extra Server ports.
- Ruling: Use the current worktree as the implementation workspace; the parent branch contains only the approved design/plan baseline, and no changes will be pushed or merged.
- Ruling: The user selected subagent-driven execution after approving a local baseline; therefore task-local commits inside this isolated worktree are authorized for review, while push/merge remain prohibited. This resolves the plan's generic no-auto-commit wording and Task 1's stale uncommitted step.

## Task status

- Task 1: complete — commit 430cccf; implementation and scoped review passed
- Task 2: complete — commits 52b51d8, 2de0a17; implementation and scoped review passed
- Task 3: complete — commits a95de80, 65ab9ac, 17a327c; implementation and scoped reviews passed
- Task 4: complete — commit e11827e; implementation and scoped review passed (repository-only constructors remain process-scoped; DB-backed path is transaction-locked)
- Task 5: complete — commits 1642e6c, 818f220, 947f45f, 0038dcc; implementation, fix round, and scoped reviews passed
- Task 6: complete — commits fb9d1a2, a5853da, 284bfa6; implementation, fix rounds, and scoped reviews passed (live MySQL/etcd integration remains CI-gated)
- Task 7: complete — commits f9f3170, d16ab04, f72ccae, 2422464, 3935683, 60d4e36; implementation, fix rounds, and scoped reviews passed
- Task 8: complete — commits c4ac736, d49de22, 4f3da65, 44efeff; implementation, fix rounds, and scoped reviews passed (Task7 duplicate-stream test has a known intermittent test-quality flake to harden in CI)
- Task 9: complete — commits d9022d9, 5ee6e41, 5a16ff8; implementation, fix rounds, and scoped review passed (full race has known intermittent Task7 duplicate-stream test flake; client race is stable)
- Task 10: complete — commits ec71938, 303736c, 9dd7e2a, c4a4fbc, d54060a, 0a88b5e, 1a3f599, 6c6e989; implementation and scoped reviews passed, with final service-boundary cleanup and verification
- Task 11: pending
- Task 12: pending
